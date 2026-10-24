// Package database 提供基于 PostgreSQL 等关系型数据库持久化的通知渠道实现。
package database

// Message 表示标准的数据库通知载荷。
type Message struct {
	Type string
	Data map[string]any
}

// NewMessage 创建数据库通知消息。
func NewMessage(notifType string, data map[string]any) *Message {
	return &Message{
		Type: notifType,
		Data: data,
	}
}

// Notification 声明支持持久化到数据库的通知接口契约。
type Notification interface {
	ToDatabase(notifiable any) map[string]any
}
