// Package database 提供基于 PostgreSQL 关系型数据库持久化的通知渠道实现。
package database

import (
	"cmp"
	"context"
	"encoding/json"
	"reflect"

	"github.com/samber/lo"
	"github.com/samber/oops"

	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/internal/platform/errs"
	"cgin/internal/platform/notification/contract"
)

// Channel 数据库通知渠道实现。
type Channel struct {
	db *database.DB
}

var (
	_ contract.Channel      = (*Channel)(nil)
	_ contract.Formatter    = (*Channel)(nil)
	_ contract.BatchChannel = (*Channel)(nil)
)

// NewChannel 创建数据库通知渠道实例。
func NewChannel(db *database.DB) *Channel {
	return &Channel{db: db}
}

// Name 返回渠道标识名。
func (c *Channel) Name() string {
	return "database"
}

// DB 返回底层数据访问入口。
func (c *Channel) DB() *database.DB {
	return c.db
}

// Format 将领域通知对象转换为数据库持久化载荷 *Message。
func (c *Channel) Format(n contract.Notification, notifiable any) (any, error) {
	if n == nil {
		return nil, oops.Code(errs.CodeNotificationUnsupportedDB).
			Errorf("notification cannot be nil")
	}
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

// ensureDB 报告数据访问入口是否已装配。
func (c *Channel) ensureDB() error {
	if c.db == nil {
		return oops.Code(errs.CodeDatabaseUnavailable).Errorf("database: connection is not available")
	}
	return nil
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

	notifiableType, notifiableID, err := resolveRecipient(target)
	if err != nil {
		return err
	}

	dataBytes, err := json.Marshal(msg.Data)
	if err != nil {
		return oops.Code(errs.CodeNotificationEncodeError).In("DatabaseChannel.Send").Wrap(err)
	}

	if err := c.ensureDB(); err != nil {
		return err
	}

	if _, err := c.db.Queries(ctx).CreateNotification(ctx, sqlc.CreateNotificationParams{
		NotifiableType: notifiableType,
		NotifiableID:   notifiableID,
		Type:           msg.Type,
		Data:           string(dataBytes),
	}); err != nil {
		return oops.Code(errs.CodeInternal).In("DatabaseChannel.Send").Wrap(err)
	}

	return nil
}

// SendBatch 使用 pgx 批量管道一次性将通知持久化至多个接收者，消除多接收人场景下的 N+1 往返。
func (c *Channel) SendBatch(ctx context.Context, notifiables []any, messages []any) error {
	if len(notifiables) == 0 {
		return nil
	}
	if err := c.ensureDB(); err != nil {
		return err
	}

	batchParams := make([]sqlc.CreateNotificationsBatchParams, 0, len(notifiables))
	for i, n := range notifiables {
		if n == nil {
			continue
		}
		var rawMsg any
		if i < len(messages) {
			rawMsg = messages[i]
		}
		msg, err := c.extractMessage(n, rawMsg)
		if err != nil {
			return err
		}
		notifiableType, notifiableID, err := resolveRecipient(n)
		if err != nil {
			return err
		}
		dataBytes, err := json.Marshal(msg.Data)
		if err != nil {
			return oops.Code(errs.CodeNotificationEncodeError).In("DatabaseChannel.SendBatch").Wrap(err)
		}
		batchParams = append(batchParams, sqlc.CreateNotificationsBatchParams{
			NotifiableType: notifiableType,
			NotifiableID:   notifiableID,
			Type:           msg.Type,
			Data:           string(dataBytes),
		})
	}
	if len(batchParams) == 0 {
		return nil
	}

	br := c.db.Queries(ctx).CreateNotificationsBatch(ctx, batchParams)
	defer func() { _ = br.Close() }()

	var batchErr error
	br.Exec(func(_ int, err error) {
		if err != nil && batchErr == nil {
			batchErr = oops.Code(errs.CodeInternal).In("DatabaseChannel.SendBatch").Wrap(err)
		}
	})
	return batchErr
}

// MarkAsRead 将指定通知记录标记为已读。
func (c *Channel) MarkAsRead(ctx context.Context, id string) error {
	if err := c.ensureDB(); err != nil {
		return err
	}
	if _, err := c.db.Queries(ctx).MarkNotificationRead(ctx, sqlc.MarkNotificationReadParams{ID: id}); err != nil {
		return oops.Code(errs.CodeInternal).In("DatabaseChannel.MarkAsRead").Wrap(err)
	}
	return nil
}

// MarkAllAsRead 将指定接收者的所有未读通知标记为已读。
func (c *Channel) MarkAllAsRead(ctx context.Context, notifiable any) error {
	if err := c.ensureDB(); err != nil {
		return err
	}
	notifiableType, notifiableID, err := resolveRecipient(notifiable)
	if err != nil {
		return err
	}

	if _, err := c.db.Queries(ctx).MarkAllNotificationsRead(ctx, sqlc.MarkAllNotificationsReadParams{
		NotifiableType: notifiableType,
		NotifiableID:   notifiableID,
	}); err != nil {
		return oops.Code(errs.CodeInternal).In("DatabaseChannel.MarkAllAsRead").Wrap(err)
	}
	return nil
}

// UnreadCount 查询指定接收者的未读通知数量。
func (c *Channel) UnreadCount(ctx context.Context, notifiable any) (int, error) {
	if err := c.ensureDB(); err != nil {
		return 0, err
	}
	notifiableType, notifiableID, err := resolveRecipient(notifiable)
	if err != nil {
		return 0, err
	}

	count, err := c.db.Queries(ctx).CountUnreadNotifications(ctx, sqlc.CountUnreadNotificationsParams{
		NotifiableType: notifiableType,
		NotifiableID:   notifiableID,
	})
	if err != nil {
		return 0, oops.Code(errs.CodeInternal).In("DatabaseChannel.UnreadCount").Wrap(err)
	}
	return int(count), nil
}

// Paginate 分页查询指定接收者的通知列表（单查询同时返回总数）。
func (c *Channel) Paginate(ctx context.Context, notifiable any, page, perPage int, unreadOnly bool) ([]sqlc.Notification, int, error) {
	if err := c.ensureDB(); err != nil {
		return nil, 0, err
	}
	notifiableType, notifiableID, err := resolveRecipient(notifiable)
	if err != nil {
		return nil, 0, err
	}

	page = max(page, 1)
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	rows, err := c.db.Queries(ctx).ListNotifications(ctx, sqlc.ListNotificationsParams{
		NotifiableType: notifiableType,
		NotifiableID:   notifiableID,
		UnreadOnly:     unreadOnly,
		PageOffset:     int64((page - 1) * perPage),
		PageLimit:      int64(perPage),
	})
	if err != nil {
		return nil, 0, oops.Code(errs.CodeInternal).In("DatabaseChannel.Paginate.Scan").Wrap(err)
	}

	total := 0
	if len(rows) > 0 {
		total = int(rows[0].TotalCount)
	}
	items := lo.Map(rows, func(row sqlc.ListNotificationsRow, _ int) sqlc.Notification {
		return row.Notification
	})

	return items, total, nil
}

// PaginateUnread 仅分页查询指定接收者的未读通知列表。
func (c *Channel) PaginateUnread(ctx context.Context, notifiable any, page, perPage int) ([]sqlc.Notification, int, error) {
	return c.Paginate(ctx, notifiable, page, perPage, true)
}

// Delete 软删除指定的通知记录。
func (c *Channel) Delete(ctx context.Context, id string) error {
	if err := c.ensureDB(); err != nil {
		return err
	}
	if _, err := c.db.Queries(ctx).SoftDeleteNotification(ctx, sqlc.SoftDeleteNotificationParams{ID: id}); err != nil {
		return oops.Code(errs.CodeInternal).In("DatabaseChannel.Delete").Wrap(err)
	}
	return nil
}

// ClearAll 软删除指定接收者的所有通知记录。
func (c *Channel) ClearAll(ctx context.Context, notifiable any) error {
	if err := c.ensureDB(); err != nil {
		return err
	}
	notifiableType, notifiableID, err := resolveRecipient(notifiable)
	if err != nil {
		return err
	}

	if _, err := c.db.Queries(ctx).SoftDeleteNotificationsByRecipient(ctx, sqlc.SoftDeleteNotificationsByRecipientParams{
		NotifiableType: notifiableType,
		NotifiableID:   notifiableID,
	}); err != nil {
		return oops.Code(errs.CodeInternal).In("DatabaseChannel.ClearAll").Wrap(err)
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
