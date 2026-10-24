package realtime

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"bgin/internal/config"
	"bgin/internal/database/models"
	"bgin/internal/platform/errs"
	platformRealtime "bgin/internal/platform/realtime"
)

// Deps 汇总实时通信服务的装配依赖。
type Deps struct {
	// Gateway 实时通信网关（向 Centrifugo 下发指令）。
	Gateway platformRealtime.Gateway
	// Cfg 全局配置，用于判定 realtime 是否启用与读取令牌有效期。
	Cfg *config.Config
	// Secret Centrifugo 反向代理请求的 HMAC 签名密钥。
	Secret string
	// DB 数据库连接，注入后签发的连接凭据以库中最新角色为准；未注入时沿用调用方传入的角色。
	DB bun.IDB
}

// Service 承载实时通信业务规则与鉴权编排。
type Service struct {
	gateway platformRealtime.Gateway
	cfg     *config.Config
	// secret 是 Centrifugo 反向代理请求的 HMAC 签名密钥。
	secret string
	// db 用于签发前核对账号当前角色；未注入时退回调用方传入的角色。
	db bun.IDB
}

// NewService 创建实时通信服务。
func NewService(deps Deps) *Service {
	return &Service{
		gateway: deps.Gateway,
		cfg:     deps.Cfg,
		secret:  deps.Secret,
		db:      deps.DB,
	}
}

func (s *Service) isEnabled() bool {
	return s != nil && s.gateway != nil && s.cfg != nil && s.cfg.Realtime.Enabled
}

// IssueToken 为指定用户签发直连 Centrifugo (WSS/SSE) 所需的 JWT 凭据。
func (s *Service) IssueToken(ctx context.Context, userID, role string) (*TokenResource, error) {
	if !s.isEnabled() {
		return nil, errs.BadRequest("Realtime service is disabled")
	}

	if s.db != nil {
		entity, err := models.FindUserByID(ctx, s.db, userID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errs.NotFound("The requested user does not exist")
			}
			return nil, errs.Internal(err)
		}
		role = string(entity.Role)
	}

	// 有效期来自配置；cmp.Or 让显式零值回退到 2 小时（envDefault 已覆盖常规情况）。
	exp := time.Now().UTC().Add(cmp.Or(s.cfg.Realtime.TokenTTL, 2*time.Hour))
	tok, err := s.gateway.GenerateConnectionToken(userID, exp, map[string]any{
		"role": role,
	}, nil)
	if err != nil {
		return nil, errs.Internal(err)
	}

	return &TokenResource{
		Token:     tok,
		ExpiresAt: exp,
		WSURL:     s.cfg.Realtime.URL,
	}, nil
}

// AuthorizeSubscribe 校验 Centrifugo 反向代理请求签名并裁定频道订阅权限。
func (s *Service) AuthorizeSubscribe(
	ctx context.Context,
	body []byte,
	signature string,
) (*platformRealtime.SubscribeResponse, error) {
	if !platformRealtime.VerifySignature(s.secret, body, signature) {
		return nil, errs.Forbidden("Invalid Centrifugo signature")
	}

	var req platformRealtime.SubscribeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, errs.BadRequest("Invalid JSON payload")
	}

	if !s.isEnabled() {
		return proxyError(platformRealtime.ProxyErrorCodeInternalError, "Realtime service is disabled"), nil
	}

	user := platformRealtime.User{ID: req.User}
	if err := s.gateway.Registry().Authorize(ctx, user, req.Channel); err != nil {
		// 订阅被拒按 Centrifugo 协议是 200 响应内的 error 字段，Go 层不构成错误
		return proxyError(platformRealtime.ProxyErrorCodePermissionDenied, "Permission denied"), nil //nolint:nilerr // 协议要求以 200 + error 字段表达拒绝
	}

	return &platformRealtime.SubscribeResponse{
		Result: &platformRealtime.SubscribeResult{},
	}, nil
}

func proxyError(code uint32, message string) *platformRealtime.SubscribeResponse {
	return &platformRealtime.SubscribeResponse{
		Error: &platformRealtime.ProxyError{
			Code:    code,
			Message: message,
		},
	}
}
