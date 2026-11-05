package clientauth

import "time"

// UserRegistered 在新用户完成基本注册流程后向全系统广播。
type UserRegistered struct {
	UserID       string    `json:"userId"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	RegisteredAt time.Time `json:"registeredAt"`
}

// EventName 实现 eventbus.Event 契约。
func (UserRegistered) EventName() string {
	return "auth.user_registered"
}

// PasswordChanged 在用户登录口令修改或重置成功后广播，用于撤销旧凭据与安全审计。
type PasswordChanged struct {
	UserID    string    `json:"userId"`
	ChangedAt time.Time `json:"changedAt"`
}

// EventName 实现 eventbus.Event 契约。
func (PasswordChanged) EventName() string {
	return "auth.password_changed"
}
