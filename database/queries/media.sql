-- 多态媒体附件查询（media 为硬删除表，无软删除语义）。

-- name: GetMediaByID :one
SELECT *
FROM media AS m
WHERE m.id = @id;

-- name: GetMediaByUUID :one
SELECT *
FROM media AS m
WHERE m.uuid = @uuid;

-- name: ListMediaByModel :many
-- collection 传 NULL 表示不限定集合。
SELECT *
FROM media AS m
WHERE m.model_type = @model_type
  AND m.model_id = @model_id
  AND (sqlc.narg('collection_name')::text IS NULL OR m.collection_name = sqlc.narg('collection_name')::text)
ORDER BY m.order_column ASC NULLS LAST, m.id ASC;

-- name: ListMediaByModels :many
-- 批量按多个宿主 ID 预加载媒体附件，消除列表页 N+1 查询。
SELECT *
FROM media AS m
WHERE m.model_type = @model_type
  AND m.model_id = ANY(@model_ids::text[])
  AND (sqlc.narg('collection_name')::text IS NULL OR m.collection_name = sqlc.narg('collection_name')::text)
ORDER BY m.order_column ASC NULLS LAST, m.id ASC;

-- name: GetFirstMedia :one
SELECT *
FROM media AS m
WHERE m.model_type = @model_type
  AND m.model_id = @model_id
  AND m.collection_name = @collection_name
ORDER BY m.order_column ASC NULLS LAST, m.id ASC
LIMIT 1;

-- name: CreateMedia :one
INSERT INTO media (
  model_type,
  model_id,
  collection_name,
  disk,
  name,
  file_name,
  mime_type,
  conversions_disk,
  size,
  order_column,
  manipulations,
  custom_properties,
  generated_conversions,
  responsive_images
) VALUES (
  @model_type,
  @model_id,
  @collection_name,
  @disk,
  @name,
  @file_name,
  @mime_type,
  sqlc.narg('conversions_disk'),
  @size,
  sqlc.narg('order_column'),
  sqlc.narg('manipulations'),
  sqlc.narg('custom_properties'),
  COALESCE(@generated_conversions::jsonb, '{}'::jsonb),
  sqlc.narg('responsive_images')
)
RETURNING *;

-- name: DeleteMediaByUUID :one
DELETE FROM media AS m
WHERE m.uuid = @uuid
RETURNING *;

-- name: DeleteMediaByModelAndCollection :many
-- 删除并回传被移除的行，供调用方清理底层物理文件。
DELETE FROM media AS m
WHERE m.model_type = @model_type
  AND m.model_id = @model_id
  AND m.collection_name = @collection_name
RETURNING *;
