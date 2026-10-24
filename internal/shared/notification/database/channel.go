// Package database 提供基于 PostgreSQL 关系型数据库持久化的通知渠道实现。
package database

import (
	"cmp"
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/samber/oops"
	"github.com/uptrace/bun"

	"bgin/internal/database/models"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/notification/contract"
)

// Channel 数据库通知渠道实现。
type Channel struct {
	db *bun.DB
}

// NewChannel 创建数据库通知渠道实例。
func NewChannel(db *bun.DB) *Channel {
	return &Channel{db: db}
}

// Name 返回渠道标识名。
func (c *Channel) Name() string {
	return "database"
}

// DB 返回底层 Bun ORM 句柄。
func (c *Channel) DB() *bun.DB {
	return c.db
}

// Format 将领域通知对象转换为数据库持久化载荷 *Message。
func (c *Channel) Format(n contract.Notification, notifiable any) (any, error) {
	notifType := reflect.TypeOf(n).String()

	if dbNotif, ok := n.(Notification); ok {
		data := dbNotif.ToDatabase(notifiable)
		return NewMessage(notifType, data), nil
	}

	if cf, ok := n.(contract.ChannelFormatter); ok {
		raw := cf.Format(c.Name(), notifiable)
		if dataMap, ok := raw.(map[string]any); ok {
			return NewMessage(notifType, dataMap), nil
		}
	}

	return nil, oops.Code(errs.CodeNotificationUnsupportedDB).
		With("notification", n).
		Errorf("notification does not implement database.Notification or ChannelFormatter")
}

// extractMessage 从任意输入中解析 *Message。
func (c *Channel) extractMessage(notifiable, message any) (*Message, error) {
	switch v := message.(type) {
	case *Message:
		return v, nil
	case Message:
		return &v, nil
	case contract.Notification:
		formatted, err := c.Format(v, notifiable)
		if err != nil {
			return nil, err
		}
		msg, ok := formatted.(*Message)
		if !ok {
			return nil, oops.Code(errs.CodeBadRequest).Errorf("database: formatted message is not *database.Message, got %T", formatted)
		}
		return msg, nil
	default:
		return nil, oops.Code(errs.CodeBadRequest).Errorf("database: expected *database.Message, got %T", message)
	}
}

// Send 执行数据库通知入库持久化。
func (c *Channel) Send(ctx context.Context, notifiable any, message any) error {
	msg, err := c.extractMessage(notifiable, message)
	if err != nil {
		return err
	}

	if notifiable == nil {
		return oops.Code(errs.CodeNotificationMissingRecipient).Errorf("database: notifiable entity is nil")
	}

	target := contract.ToNotifiable(notifiable)
	if target == nil {
		return oops.Code(errs.CodeNotificationMissingRecipient).Errorf("database: cannot resolve notifiable target")
	}

	notifiableID := target.GetNotifiableID()
	if notifiableID == "" {
		return oops.Code(errs.CodeNotificationMissingRecipientID).Errorf("database: recipient ID cannot be empty")
	}
	notifiableType := cmp.Or(target.GetNotifiableType(), contract.DefaultNotifiableType)

	dataBytes, err := json.Marshal(msg.Data)
	if err != nil {
		return oops.Code(errs.CodeNotificationEncodeError).In("DatabaseChannel.Send").Wrap(err)
	}

	if c.db == nil {
		return oops.Code(errs.CodeDatabaseUnavailable).Errorf("database: connection is not available for notification")
	}

	notif := &models.Notification{
		Type:           msg.Type,
		NotifiableType: notifiableType,
		NotifiableID:   notifiableID,
		Data:           string(dataBytes),
	}

	_, err = c.db.NewInsert().Model(notif).Exec(ctx)
	if err != nil {
		return oops.Code(errs.CodeInternal).In("DatabaseChannel.Send").Wrap(err)
	}

	return nil
}

// MarkAsRead 将指定通知记录标记为已读。
func (c *Channel) MarkAsRead(ctx context.Context, id string) error {
	if c.db == nil {
		return oops.Code(errs.CodeDatabaseUnavailable).Errorf("database: connection is not available")
	}
	now := time.Now().UTC()
	if _, err := c.db.NewUpdate().
		Model((*models.Notification)(nil)).
		Set("read_at = ?", now).
		Set("updated_at = ?", now).
		Where("id = ? AND read_at IS NULL", id).
		Exec(ctx); err != nil {
		return oops.Code(errs.CodeInternal).In("DatabaseChannel.MarkAsRead").Wrap(err)
	}
	return nil
}

// resolveRecipient 解析可通知实体的类型标识与主键 ID。
func resolveRecipient(notifiable any) (string, string, error) {
	target := contract.ToNotifiable(notifiable)
	if target == nil || target.GetNotifiableID() == "" {
		return "", "", oops.Code(errs.CodeNotificationMissingRecipientID).Errorf("database: recipient ID cannot be empty")
	}
	return cmp.Or(target.GetNotifiableType(), contract.DefaultNotifiableType), target.GetNotifiableID(), nil
}

// MarkAllAsRead 将指定接收者的所有未读通知标记为已读。
func (c *Channel) MarkAllAsRead(ctx context.Context, notifiable any) error {
	if c.db == nil {
		return oops.Code(errs.CodeDatabaseUnavailable).Errorf("database: connection is not available")
	}
	notifiableType, notifiableID, err := resolveRecipient(notifiable)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	_, err = c.db.NewUpdate().
		Model((*models.Notification)(nil)).
		Set("read_at = ?", now).
		Set("updated_at = ?", now).
		Where("notifiable_type = ? AND notifiable_id = ? AND read_at IS NULL", notifiableType, notifiableID).
		Exec(ctx)
	if err != nil {
		return oops.Code(errs.CodeInternal).In("DatabaseChannel.MarkAllAsRead").Wrap(err)
	}
	return nil
}

// UnreadCount 查询指定接收者的未读通知数量。
func (c *Channel) UnreadCount(ctx context.Context, notifiable any) (int, error) {
	if c.db == nil {
		return 0, oops.Code(errs.CodeDatabaseUnavailable).Errorf("database: connection is not available")
	}
	notifiableType, notifiableID, err := resolveRecipient(notifiable)
	if err != nil {
		return 0, err
	}

	count, err := c.db.NewSelect().
		Model((*models.Notification)(nil)).
		Where("notifiable_type = ? AND notifiable_id = ? AND read_at IS NULL", notifiableType, notifiableID).
		Count(ctx)
	if err != nil {
		return 0, oops.Code(errs.CodeInternal).In("DatabaseChannel.UnreadCount").Wrap(err)
	}
	return int(count), nil
}

// Paginate 分页查询指定接收者的通知列表。
func (c *Channel) Paginate(ctx context.Context, notifiable any, page, perPage int, unreadOnly bool) ([]models.Notification, int, error) {
	if c.db == nil {
		return nil, 0, oops.Code(errs.CodeDatabaseUnavailable).Errorf("database: connection is not available")
	}
	notifiableType, notifiableID, err := resolveRecipient(notifiable)
	if err != nil {
		return nil, 0, err
	}

	page = max(page, 1)
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	offset := (page - 1) * perPage

	q := c.db.NewSelect().
		Model((*models.Notification)(nil)).
		Where("notifiable_type = ? AND notifiable_id = ?", notifiableType, notifiableID)

	if unreadOnly {
		q = q.Where("read_at IS NULL")
	}

	count, err := q.Count(ctx)
	if err != nil {
		return nil, 0, oops.Code(errs.CodeInternal).In("DatabaseChannel.Paginate.Count").Wrap(err)
	}

	var items []models.Notification
	err = q.Order("created_at DESC").Limit(int64(perPage)).Offset(int64(offset)).Scan(ctx, &items)
	if err != nil {
		return nil, 0, oops.Code(errs.CodeInternal).In("DatabaseChannel.Paginate.Scan").Wrap(err)
	}

	return items, int(count), nil
}

// PaginateUnread 仅分页查询指定接收者的未读通知列表。
func (c *Channel) PaginateUnread(ctx context.Context, notifiable any, page, perPage int) ([]models.Notification, int, error) {
	return c.Paginate(ctx, notifiable, page, perPage, true)
}

// Delete 软删除指定的通知记录。
func (c *Channel) Delete(ctx context.Context, id string) error {
	if c.db == nil {
		return oops.Code(errs.CodeDatabaseUnavailable).Errorf("database: connection is not available")
	}
	_, err := c.db.NewDelete().
		Model((*models.Notification)(nil)).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return oops.Code(errs.CodeInternal).In("DatabaseChannel.Delete").Wrap(err)
	}
	return nil
}

// ClearAll 软删除指定接收者的所有通知记录。
func (c *Channel) ClearAll(ctx context.Context, notifiable any) error {
	if c.db == nil {
		return oops.Code(errs.CodeDatabaseUnavailable).Errorf("database: connection is not available")
	}
	notifiableType, notifiableID, err := resolveRecipient(notifiable)
	if err != nil {
		return err
	}

	_, err = c.db.NewDelete().
		Model((*models.Notification)(nil)).
		Where("notifiable_type = ? AND notifiable_id = ?", notifiableType, notifiableID).
		Exec(ctx)
	if err != nil {
		return oops.Code(errs.CodeInternal).In("DatabaseChannel.ClearAll").Wrap(err)
	}
	return nil
}
