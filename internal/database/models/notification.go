package models

import (
	"time"

	"github.com/uptrace/bun"

	"bgin/internal/database"
)

// Notification 表示系统通用站内消息通知模型，映射 notifications 表。
type Notification struct {
	bun.BaseModel `bun:"table:notifications,alias:n"`
	database.Base

	NotifiableType string     `bun:"notifiable_type,notnull" json:"notifiableType"`
	NotifiableID   string     `bun:"notifiable_id,notnull" json:"notifiableId"`
	Type           string     `bun:"type,notnull" json:"type"`
	Data           string     `bun:"data,notnull" json:"data"`
	ReadAt         *time.Time `bun:"read_at" json:"readAt,omitempty"`
	DeletedAt      *time.Time `bun:"deleted_at,soft_delete" json:"deletedAt,omitempty"`
}

// IsRead 判断通知是否已被阅读。
func (n *Notification) IsRead() bool {
	return n.ReadAt != nil
}

// MarkAsRead 标记通知为已读状态。
func (n *Notification) MarkAsRead(t time.Time) {
	utc := t.UTC()
	n.ReadAt = &utc
}
