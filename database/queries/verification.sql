-- 一次性凭据（邮箱验证码 / 密码重置令牌）查询。
-- 现有 schema 的 identifier 非唯一，覆盖写入用「同语句 CTE 删除 + 插入」保证原子。

-- name: UpsertVerification :one
WITH removed AS (
  DELETE FROM "verification" AS v
  WHERE v.identifier = @identifier
)
INSERT INTO "verification" (identifier, value, expires_at)
VALUES (@identifier, @value, @expires_at)
RETURNING *;

-- name: GetVerificationByIdentifier :one
SELECT *
FROM "verification" AS v
WHERE v.identifier = @identifier;

-- name: DeleteVerificationByID :execrows
DELETE FROM "verification" AS v
WHERE v.id = @id;

-- name: ConsumeVerification :execrows
-- 单条 compare-and-delete 保证并发安全：只有真正删掉那一行的调用算核销成功。
DELETE FROM "verification" AS v
WHERE v.identifier = @identifier
  AND v.value = @value;
