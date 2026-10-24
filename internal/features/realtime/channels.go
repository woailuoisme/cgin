package realtime

import (
	"context"
	"errors"
	"strings"

	"github.com/uptrace/bun"

	"bgin/internal/database/models"
	platformRealtime "bgin/internal/platform/realtime"
)

// UserChannel 构建受订阅代理保护的个人用户私有频道名称（private:users.userID）。
func UserChannel(userID string) string {
	return "private:users." + userID
}

// PersonalChannel 构建 Centrifugo 原生用户限定频道名称（命名空间 personal#userID）。
// Centrifugo 网关将基于连接 sub 自动鉴权，无需触发订阅代理 Webhook。
func PersonalChannel(userID string) string {
	return "personal#" + userID
}

// AdminNotificationChannel 返回系统管理员通知广播频道名称。
func AdminNotificationChannel() string {
	return "admin:notifications"
}

// RegisterChannels 集中向实时通信注册中心登记所有业务频道的订阅授权策略。
func RegisterChannels(r *platformRealtime.Registry, db bun.IDB) {
	if r == nil {
		return
	}

	r.OnceDo(func() {
		// 1. 个人用户私有通知频道: private:users.{id}
		// 规则：仅允许当前已认证用户订阅自身的数据流；若用户为 admin 亦可查看
		r.Register("private:users.{id}", func(ctx context.Context, u platformRealtime.User, _ string, params map[string]string) error {
			targetID := strings.TrimSpace(params["id"])
			if targetID == "" {
				return errors.New("unauthorized: missing target user id")
			}

			if (u.ID != "" && u.ID == targetID) || isAdmin(ctx, db, u.ID) {
				return nil
			}

			return errors.New("unauthorized: permission denied for user private channel")
		})

		// 2. 系统管理员全局通知频道: admin:notifications
		// 规则：必须具备系统管理员角色 (models.RoleAdmin)，依赖缺失时遵循 Fail-Closed 默认拒绝
		r.Register("admin:notifications", func(ctx context.Context, u platformRealtime.User, _ string, _ map[string]string) error {
			if u.ID == "" {
				return errors.New("unauthorized: authentication required")
			}
			if db == nil {
				return errors.New("unauthorized: authorization service unavailable")
			}
			if !isAdmin(ctx, db, u.ID) {
				return errors.New("unauthorized: admin privileges required")
			}
			return nil
		})

		// 3. 公共事件与系统广播频道: public:*
		// 规则：向全站已登录用户开放
		r.Register("public:*", func(_ context.Context, u platformRealtime.User, _ string, _ map[string]string) error {
			if u.ID == "" {
				return errors.New("unauthorized: login required for public channel")
			}
			return nil
		})
	})
}

// isAdmin 检查指定用户是否具备系统管理员角色。
func isAdmin(ctx context.Context, db bun.IDB, userID string) bool {
	if db == nil || userID == "" {
		return false
	}
	user, err := models.FindUserByID(ctx, db, userID)
	return err == nil && user != nil && user.Role == models.RoleAdmin
}
