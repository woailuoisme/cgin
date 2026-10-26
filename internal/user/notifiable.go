package user

import (
	"cgin/internal/database/sqlc"
	"cgin/pkg/notification/contract"
)

// Notifiable 把用户行适配为通知接收者，路由 mail 与 sms 渠道。
// 生成结构体不能挂方法，因此由用户内核提供唯一的适配入口。
func Notifiable(u *sqlc.User) contract.Notifiable {
	if u == nil {
		return nil
	}
	recipient := contract.NewRecipient(u.ID, "").WithEmail(u.Email)
	if u.PhoneNumber != nil && *u.PhoneNumber != "" {
		recipient.WithPhone(*u.PhoneNumber)
	}
	return recipient
}
