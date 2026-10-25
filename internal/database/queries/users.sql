-- 用户聚合查询。
--
-- 约定：
--   1. 所有读取显式过滤 deleted_at IS NULL（sqlc 不提供隐式软删除语义）；
--   2. 删除一律为 UPDATE ... SET deleted_at = now()，用 :execrows 暴露影响行数；
--   3. 局部更新收敛为单条 UpdateUser：设置走 COALESCE，清空走 clear_* 标记，
--      一次往返返回最新整行，禁止逐字段 UPDATE 拼装。

-- name: GetUserByID :one
SELECT *
FROM "user" AS u
WHERE u.id = @id
  AND u.deleted_at IS NULL;

-- name: GetUserByEmail :one
SELECT *
FROM "user" AS u
WHERE lower(u.email) = lower(@email)
  AND u.deleted_at IS NULL;

-- name: GetUserByUsername :one
SELECT *
FROM "user" AS u
WHERE u.username IS NOT NULL
  AND lower(u.username) = lower(@username)
  AND u.deleted_at IS NULL;

-- name: CountActiveUsers :one
SELECT count(*)
FROM "user" AS u
WHERE u.deleted_at IS NULL;

-- name: ListUsers :many
-- 过滤条件统一走 sqlc.narg：传 NULL 表示不过滤，SQL 保持静态可审计。
-- 排序白名单在 Go 侧收敛为单一排序键，这里用 CASE 生成固定排序臂，杜绝字符串拼接。
SELECT sqlc.embed(u), count(*) OVER () AS total_count
FROM "user" AS u
WHERE u.deleted_at IS NULL
  AND (sqlc.narg('status')::text IS NULL OR u.status = sqlc.narg('status')::text)
  AND (sqlc.narg('role')::text IS NULL OR u.role = sqlc.narg('role')::text)
  AND (
    sqlc.narg('search')::text IS NULL
    OR u.name ILIKE sqlc.narg('search')::text
    OR u.email ILIKE sqlc.narg('search')::text
    OR u.username ILIKE sqlc.narg('search')::text
  )
ORDER BY
  CASE WHEN @sort_key::text = 'createdAt' AND NOT @sort_desc::bool THEN u.created_at END ASC NULLS LAST,
  CASE WHEN @sort_key::text = 'createdAt' AND @sort_desc::bool THEN u.created_at END DESC NULLS LAST,
  CASE WHEN @sort_key::text = 'updatedAt' AND NOT @sort_desc::bool THEN u.updated_at END ASC NULLS LAST,
  CASE WHEN @sort_key::text = 'updatedAt' AND @sort_desc::bool THEN u.updated_at END DESC NULLS LAST,
  CASE WHEN @sort_key::text = 'email' AND NOT @sort_desc::bool THEN lower(u.email) END ASC NULLS LAST,
  CASE WHEN @sort_key::text = 'email' AND @sort_desc::bool THEN lower(u.email) END DESC NULLS LAST,
  CASE WHEN @sort_key::text = 'name' AND NOT @sort_desc::bool THEN lower(u.name) END ASC NULLS LAST,
  CASE WHEN @sort_key::text = 'name' AND @sort_desc::bool THEN lower(u.name) END DESC NULLS LAST,
  CASE WHEN @sort_key::text = 'lastLoginAt' AND NOT @sort_desc::bool THEN u.last_login_at END ASC NULLS LAST,
  CASE WHEN @sort_key::text = 'lastLoginAt' AND @sort_desc::bool THEN u.last_login_at END DESC NULLS LAST,
  u.id DESC
LIMIT @page_limit::bigint
OFFSET @page_offset::bigint;

-- name: ListUsersAfter :many
-- 流式导出分块：按主键键集推进，单页成本与已翻页深度无关，避免深 OFFSET 的线性扫描。
-- 导出固定按 id 升序：这是导出的稳定遍历顺序，与列表页的排序参数无关。
SELECT *
FROM "user" AS u
WHERE u.deleted_at IS NULL
  AND (sqlc.narg('status')::text IS NULL OR u.status = sqlc.narg('status')::text)
  AND (sqlc.narg('role')::text IS NULL OR u.role = sqlc.narg('role')::text)
  AND (
    sqlc.narg('search')::text IS NULL
    OR u.name ILIKE sqlc.narg('search')::text
    OR u.email ILIKE sqlc.narg('search')::text
    OR u.username ILIKE sqlc.narg('search')::text
  )
  AND (@after_id::text = '' OR u.id > @after_id::text)
ORDER BY u.id ASC
LIMIT @page_limit::bigint;

-- name: ListUsersForSearch :many
-- 搜索索引导入使用 id 键集分页，避免深 OFFSET 的线性扫描。
SELECT *
FROM "user" AS u
WHERE u.deleted_at IS NULL
  AND (@after_id::text = '' OR u.id > @after_id::text)
ORDER BY u.id ASC
LIMIT @page_limit::bigint;

-- name: CreateUser :one
INSERT INTO "user" (
  name,
  email,
  email_verified,
  image,
  role,
  status,
  username,
  display_username,
  phone_number,
  phone_number_verified
)
VALUES (
  @name,
  @email,
  @email_verified,
  sqlc.narg('image'),
  @role::text,
  @status::text,
  sqlc.narg('username'),
  sqlc.narg('display_username'),
  sqlc.narg('phone_number'),
  @phone_number_verified
)
RETURNING *;

-- name: CreateUsersBatch :many
-- 批量播种：单条语句完成整批插入，行集合由 JSONB recordset 解析，避免 N 次往返。
INSERT INTO "user" (
  name,
  email,
  email_verified,
  image,
  role,
  status,
  username,
  display_username,
  phone_number,
  phone_number_verified
)
SELECT
  b.name,
  b.email,
  b.email_verified,
  b.image,
  b.role,
  b.status,
  b.username,
  b.display_username,
  b.phone_number,
  b.phone_number_verified
FROM jsonb_to_recordset((@payload::jsonb) -> 'rows') AS b(
  name text,
  email text,
  email_verified boolean,
  image text,
  role text,
  status text,
  username text,
  display_username text,
  phone_number text,
  phone_number_verified boolean
)
RETURNING *;

-- name: UpdateUser :one
-- 单语句局部更新：COALESCE 表达“设置”，clear_* 表达“显式清空”，一次往返返回最新整行。
UPDATE "user" AS u
SET
  name = COALESCE(sqlc.narg('name'), u.name),
  image = CASE WHEN @clear_image::bool THEN NULL ELSE COALESCE(sqlc.narg('image'), u.image) END,
  username = CASE WHEN @clear_username::bool THEN NULL ELSE COALESCE(sqlc.narg('username'), u.username) END,
  display_username = CASE WHEN @clear_display_name::bool THEN NULL ELSE COALESCE(sqlc.narg('display_username'), u.display_username) END,
  phone_number = CASE WHEN @clear_phone::bool THEN NULL ELSE COALESCE(sqlc.narg('phone_number'), u.phone_number) END,
  email_verified = COALESCE(sqlc.narg('email_verified'), u.email_verified),
  role = COALESCE(sqlc.narg('role')::text, u.role),
  status = COALESCE(sqlc.narg('status')::text, u.status),
  banned = COALESCE(sqlc.narg('banned'), u.banned),
  ban_reason = CASE WHEN @clear_ban_reason::bool THEN NULL ELSE COALESCE(sqlc.narg('ban_reason'), u.ban_reason) END,
  ban_expires = CASE WHEN @clear_ban_expires::bool THEN NULL ELSE COALESCE(sqlc.narg('ban_expires'), u.ban_expires) END,
  updated_at = now()
WHERE u.id = @id
  AND u.deleted_at IS NULL
RETURNING *;

-- name: SetUserEmailVerified :one
UPDATE "user" AS u
SET email_verified = true,
    updated_at = now()
WHERE u.id = @id
  AND u.deleted_at IS NULL
RETURNING *;

-- name: RecordUserLogin :exec
UPDATE "user" AS u
SET last_login_at = now(),
    last_login_method = @method,
    updated_at = now()
WHERE u.id = @id
  AND u.deleted_at IS NULL;

-- name: SoftDeleteUser :execrows
UPDATE "user" AS u
SET deleted_at = now(),
    updated_at = now()
WHERE u.id = @id
  AND u.deleted_at IS NULL;
