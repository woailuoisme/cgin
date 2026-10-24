-- gin-bun 的迁移真相源。atlas 直接读取本文件生成版本式迁移（见 ADR-0004）。
--
-- 与根目录 auth.sql 的关系：auth.sql 是完整构想 schema（13 张表），本文件只保留
-- 本期真正使用的 3 张表，并补上 auth.sql 里没有的 user.role CHECK 约束。
-- 启用被推迟的表时，把对应 DDL 从 auth.sql 搬到这里再跑 atlas migrate diff。

-- ─────────────────────────────────────────────────────────────────────────────
-- 1. 用户核心档案表 ("user")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "user" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（PostgreSQL 18 原生生成，Go 侧生成优先）
  name text NOT NULL, -- 用户显示名称
  email text NOT NULL, -- 电子邮箱地址，活跃记录内大小写不敏感唯一
  email_verified boolean NOT NULL DEFAULT false, -- 邮箱验证状态标识
  image text NULL, -- 用户头像图片访问地址
  role text NOT NULL DEFAULT 'user', -- 系统角色标识（user, admin）；新增角色需同时迁移与改策略矩阵
  status text NOT NULL DEFAULT 'active', -- 账号状态标识（active: 正常, inactive: 未激活, banned: 已封禁）
  banned boolean NOT NULL DEFAULT false, -- 账号是否被封禁标识
  two_factor_enabled boolean NOT NULL DEFAULT false, -- 是否启用 TOTP 二步验证开关（本期未实现）
  username text NULL, -- 用户唯一短名（纯小写英文/数字/下划线）
  display_username text NULL, -- 用户前台展示名（保留原始大小写）
  phone_number text NULL, -- 绑定手机号（E.164 规范）
  phone_number_verified boolean NOT NULL DEFAULT false, -- 手机号验证状态标识
  is_anonymous boolean NOT NULL DEFAULT false, -- 是否为匿名注册用户（本期未实现）
  last_login_method text NULL, -- 最近一次成功登录使用的认证方式
  ban_reason text NULL, -- 账号被封禁的原因说明
  ban_expires timestamptz NULL, -- 账号封禁过期时间戳（为空表示永久封禁或未封禁）
  last_login_at timestamptz NULL, -- 最近一次成功登录时间戳
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  deleted_at timestamptz NULL, -- 软删除时间戳（为空表示正常活跃记录）
  PRIMARY KEY (id),
  CONSTRAINT ck_user_status CHECK (status IN ('active', 'inactive', 'banned')),
  CONSTRAINT ck_user_role CHECK (role IN ('user', 'admin'))
);

-- 用户表检索索引与唯一性约束
CREATE UNIQUE INDEX idx_user_email ON "user" (LOWER(email)) WHERE deleted_at IS NULL; -- 活跃用户邮箱大小写不敏感唯一索引
CREATE UNIQUE INDEX idx_user_username ON "user" (LOWER(username)) WHERE deleted_at IS NULL AND username IS NOT NULL; -- 活跃用户短名唯一索引
CREATE UNIQUE INDEX idx_user_phone_number ON "user" (phone_number) WHERE deleted_at IS NULL AND phone_number IS NOT NULL; -- 活跃用户手机号唯一索引
CREATE INDEX idx_user_role ON "user" (role) WHERE deleted_at IS NULL; -- 角色过滤部分索引
CREATE INDEX idx_user_status ON "user" (status); -- 账号状态过滤索引
CREATE INDEX idx_user_deleted_at ON "user" (deleted_at); -- 软删除检索索引

-- ─────────────────────────────────────────────────────────────────────────────
-- 2. 账号凭据与第三方 OAuth 关联表 ("account")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "account" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符
  user_id text NOT NULL REFERENCES "user"(id) ON DELETE CASCADE, -- 关联的用户ID，级联删除
  account_id text NOT NULL, -- 第三方服务商账号标识符或本地邮箱
  provider_id text NOT NULL, -- 认证提供方标识（credential, github, google）
  password text NULL, -- 本地凭证密码哈希（argon2id）
  access_token text NULL, -- OAuth 访问令牌
  refresh_token text NULL, -- OAuth 刷新令牌
  id_token text NULL, -- OpenID Connect 身份令牌
  access_token_expires_at timestamptz NULL, -- OAuth 访问令牌过期时间戳
  refresh_token_expires_at timestamptz NULL, -- OAuth 刷新令牌过期时间戳
  scope text NULL, -- OAuth 授权作用域
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  PRIMARY KEY (id)
);

-- 账号表检索索引与复合唯一性约束
CREATE INDEX idx_account_user_id ON "account" (user_id); -- 外键显式索引，加速级联删除与关联查询
CREATE UNIQUE INDEX idx_account_provider_account ON "account" (provider_id, account_id); -- 每个提供方的账号复合唯一索引

-- ─────────────────────────────────────────────────────────────────────────────
-- 3. 验证凭据与一次性口令表 ("verification")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "verification" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符
  identifier text NOT NULL, -- 验证主体标识符（形如 email-verify:user@example.com）
  value text NOT NULL, -- 凭据的 HMAC-SHA256 摘要，绝不落明文
  expires_at timestamptz NOT NULL, -- 过期失效时间戳
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  PRIMARY KEY (id)
);

-- 验证凭据检索索引
CREATE INDEX idx_verification_identifier_value ON "verification" (identifier, value); -- 复合索引，用于快速匹配主体与凭据
CREATE INDEX idx_verification_expires_at ON "verification" (expires_at); -- 过期时间索引，用于批量清理失效凭据
