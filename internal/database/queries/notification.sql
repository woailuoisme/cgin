-- 站内通知查询（软删除表，读取一律过滤 deleted_at IS NULL）。

-- name: CreateNotification :one
INSERT INTO notifications (
  notifiable_type,
  notifiable_id,
  type,
  data
) VALUES (
  @notifiable_type,
  @notifiable_id,
  @type,
  @data
)
RETURNING *;

-- name: CreateNotificationsBatch :batchexec
INSERT INTO notifications (
  notifiable_type,
  notifiable_id,
  type,
  data
) VALUES (
  @notifiable_type,
  @notifiable_id,
  @type,
  @data
);

-- name: ListNotifications :many
SELECT sqlc.embed(n), count(*) OVER () AS total_count
FROM notifications AS n
WHERE n.notifiable_type = @notifiable_type
  AND n.notifiable_id = @notifiable_id
  AND n.deleted_at IS NULL
  AND (NOT @unread_only::bool OR n.read_at IS NULL)
ORDER BY n.created_at DESC, n.id DESC
LIMIT @page_limit::bigint
OFFSET @page_offset::bigint;

-- name: CountUnreadNotifications :one
SELECT count(*)
FROM notifications AS n
WHERE n.notifiable_type = @notifiable_type
  AND n.notifiable_id = @notifiable_id
  AND n.read_at IS NULL
  AND n.deleted_at IS NULL;

-- name: MarkNotificationRead :execrows
UPDATE notifications AS n
SET read_at = now(),
    updated_at = now()
WHERE n.id = @id
  AND n.read_at IS NULL
  AND n.deleted_at IS NULL;

-- name: MarkAllNotificationsRead :execrows
UPDATE notifications AS n
SET read_at = now(),
    updated_at = now()
WHERE n.notifiable_type = @notifiable_type
  AND n.notifiable_id = @notifiable_id
  AND n.read_at IS NULL
  AND n.deleted_at IS NULL;

-- name: SoftDeleteNotification :execrows
UPDATE notifications AS n
SET deleted_at = now(),
    updated_at = now()
WHERE n.id = @id
  AND n.deleted_at IS NULL;

-- name: SoftDeleteNotificationsByRecipient :execrows
UPDATE notifications AS n
SET deleted_at = now(),
    updated_at = now()
WHERE n.notifiable_type = @notifiable_type
  AND n.notifiable_id = @notifiable_id
  AND n.deleted_at IS NULL;
