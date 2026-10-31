-- 默认与公共基础设施服务数据库模式定义
-- 包含通用站内通知系统表 (notifications)

-- ─────────────────────────────────────────────────────────────────────────────
-- 1. 通用站内消息通知表 (notifications，基于 UUIDv7)
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE notifications (
  id uuid NOT NULL DEFAULT uuidv7(), -- UUIDv7 主键标识符（默认自动生成）
  notifiable_type varchar(255) NOT NULL, -- 接收主体类型（如 App\Models\User, user）
  notifiable_id varchar(255) NOT NULL, -- 接收主体 ID
  type varchar(255) NOT NULL, -- 业务通知类别标识（如 App\Notifications\InvoicePaid, invoice.paid）
  data text NOT NULL, -- 结构化通知消息体数据（JSON 文本序列化）
  read_at timestamptz NULL, -- 用户阅读时间戳（为空表示未读）
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  deleted_at timestamptz NULL, -- 软删除时间戳
  PRIMARY KEY (id)
);

-- 消息通知检索索引
CREATE INDEX idx_notifiable ON notifications (notifiable_type, notifiable_id); -- 接收主体复合检索索引
CREATE INDEX idx_notifications_read_at ON notifications (read_at); -- 已读/未读状态过滤索引
CREATE INDEX idx_notifications_deleted_at ON notifications (deleted_at); -- 软删除过滤索引
