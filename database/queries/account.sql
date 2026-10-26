-- 账号凭据与第三方 OAuth 关联查询。

-- name: GetCredentialAccount :one
SELECT *
FROM "account" AS a
WHERE a.user_id = @user_id
  AND a.provider_id = @provider_id
ORDER BY a.created_at ASC
LIMIT 1;

-- name: GetPasswordHash :one
SELECT a.password
FROM "account" AS a
WHERE a.user_id = @user_id
  AND a.provider_id = @provider_id
ORDER BY a.created_at ASC
LIMIT 1;

-- name: CreateAccount :one
INSERT INTO "account" (
  user_id,
  account_id,
  provider_id,
  password,
  access_token,
  refresh_token,
  id_token,
  access_token_expires_at,
  refresh_token_expires_at,
  scope
) VALUES (
  @user_id,
  @account_id,
  @provider_id,
  sqlc.narg('password'),
  sqlc.narg('access_token'),
  sqlc.narg('refresh_token'),
  sqlc.narg('id_token'),
  sqlc.narg('access_token_expires_at'),
  sqlc.narg('refresh_token_expires_at'),
  sqlc.narg('scope')
)
RETURNING *;

-- name: CreateAccountsBatch :exec
-- 批量写入本地密码凭据：account_id 与 user_id 同值，符合单用户单凭据约定。
INSERT INTO "account" (user_id, account_id, provider_id, password)
SELECT b.user_id, b.user_id, @provider_id, @password
FROM unnest(@user_id_list::text[]) AS b(user_id);

-- name: UpdateAccountPassword :execrows
UPDATE "account" AS a
SET password = @password,
    updated_at = now()
WHERE a.user_id = @user_id
  AND a.provider_id = @provider_id;
