-- 认证与用户中心数据库模式定义（对齐 Better-Auth 核心与全量官方扩展生态单数表名规范，基于 PostgreSQL 18 原生内核设计）
-- 包含：
-- 1. 用户核心档案表 ("user"，含 2FA、用户名、手机号、匿名标识、封禁细节扩展字段)
-- 2. 账号凭据与第三方 OAuth 表 ("account")
-- 3. 多租户组织与工作空间表 ("organization")
-- 4. 组织成员关联关系表 ("member")
-- 5. 组织成员邀请表 ("invitation")
-- 6. 持久化会话管理表 ("session"，含激活租户组织与管理员代登审计字段)
-- 7. 验证凭据与一次性口令表 ("verification")
-- 8. TOTP 二步验证配置表 ("two_factor"，含防暴力破解锁定与校验状态)
-- 9. Passkey / WebAuthn 硬件密钥表 ("passkey"，含 AAGUID 厂商型号标识)
-- 10. API Key 开发者密钥管理表 ("apikey"，含多配置组与泛化主体引用)
-- 11. 企业单点登录 SSO / SAML / OIDC 配置表 ("sso_provider"，含全局唯一 provider_id)
-- 12. 非对称密钥对轮换表 ("jwks"，含密钥过期时间与算法参数)
-- 13. 设备码授权流表 ("device_code"，对齐 RFC 8628 Device Authorization Grant)

-- ─────────────────────────────────────────────────────────────────────────────
-- 1. 用户核心档案表 ("user")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "user" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  name text NOT NULL, -- 用户显示名称
  email text NOT NULL, -- 唯一电子邮箱地址
  email_verified boolean NOT NULL DEFAULT false, -- 邮箱验证状态标识
  image text NULL, -- 用户头像图片访问地址
  role text NOT NULL DEFAULT 'user', -- 系统角色标识（如 user, admin, editor 等）
  status text NOT NULL DEFAULT 'active', -- 账号状态标识（active: 正常, inactive: 未激活, banned: 已封禁）
  banned boolean NOT NULL DEFAULT false, -- 账号是否被封禁标识
  two_factor_enabled boolean NOT NULL DEFAULT false, -- 是否启用 TOTP 二步验证开关
  username text NULL, -- 用户唯一短名（纯英文/数字规范）
  display_username text NULL, -- 用户前台展示名（保留原始大小写）
  phone_number text NULL, -- 绑定手机号（国际 E.164 规范）
  phone_number_verified boolean NOT NULL DEFAULT false, -- 手机号验证状态标识
  is_anonymous boolean NOT NULL DEFAULT false, -- 是否为匿名注册用户（Better Auth 匿名插件）
  last_login_method text NULL, -- 最近一次成功登录使用的提供方标识或认证方式
  ban_reason text NULL, -- 账号被封禁的原因说明
  ban_expires timestamptz NULL, -- 账号封禁过期时间戳（为空表示永久封禁或未封禁）
  last_login_at timestamptz NULL, -- 最近一次成功登录时间戳
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  deleted_at timestamptz NULL, -- 软删除时间戳（为空表示正常活跃记录）
  PRIMARY KEY (id),
  CONSTRAINT ck_user_status CHECK (status IN ('active', 'inactive', 'banned'))
);

-- 用户表检索索引与唯一性约束
CREATE UNIQUE INDEX idx_user_email ON "user" (LOWER(email)) WHERE deleted_at IS NULL; -- 活跃用户邮箱大小写不敏感唯一索引
CREATE UNIQUE INDEX idx_user_username ON "user" (LOWER(username)) WHERE deleted_at IS NULL AND username IS NOT NULL; -- 活跃用户短名大小写不敏感唯一索引
CREATE UNIQUE INDEX idx_user_phone_number ON "user" (phone_number) WHERE deleted_at IS NULL AND phone_number IS NOT NULL; -- 活跃用户手机号唯一索引
CREATE INDEX idx_user_role ON "user" (role) WHERE deleted_at IS NULL; -- 角色过滤部分索引
CREATE INDEX idx_user_status ON "user" (status); -- 账号状态过滤索引
CREATE INDEX idx_user_deleted_at ON "user" (deleted_at); -- 软删除检索索引

-- ─────────────────────────────────────────────────────────────────────────────
-- 2. 账号凭据与第三方 OAuth 关联表 ("account")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "account" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  user_id text NOT NULL REFERENCES "user"(id) ON DELETE CASCADE, -- 关联的用户ID，级联删除
  account_id text NOT NULL, -- 第三方服务商账号标识符或本地邮箱
  provider_id text NOT NULL, -- 认证提供方标识（如 credential, github, google）
  password text NULL, -- 本地凭证密码哈希（Argon2id/Bcrypt）
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
CREATE INDEX idx_account_user_id ON "account" (user_id); -- 外键显式 B-Tree 索引，加速级联删除与关联查询，杜绝死锁
CREATE UNIQUE INDEX idx_account_provider_account ON "account" (provider_id, account_id); -- 每个第三方提供方的账号复合唯一索引

-- ─────────────────────────────────────────────────────────────────────────────
-- 3. 多租户组织与工作空间表 ("organization")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "organization" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  name text NOT NULL, -- 组织或团队显示名称
  slug text NOT NULL, -- 组织唯一短链接别名（用于租户路由与标识）
  logo text NULL, -- 组织品牌 Logo 访问地址
  metadata text NULL, -- 组织扩展元数据配置（JSON 格式字符串）
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  PRIMARY KEY (id)
);

-- 组织表唯一性约束索引
CREATE UNIQUE INDEX idx_organization_slug ON "organization" (slug); -- 组织 Slug 唯一检索索引

-- ─────────────────────────────────────────────────────────────────────────────
-- 4. 组织成员关联关系表 ("member")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "member" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  organization_id text NOT NULL REFERENCES "organization"(id) ON DELETE CASCADE, -- 所属组织ID，级联删除
  user_id text NOT NULL REFERENCES "user"(id) ON DELETE CASCADE, -- 关联用户ID，级联删除
  role text NOT NULL DEFAULT 'member', -- 成员在组织内的角色标识（如 owner, admin, member）
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  PRIMARY KEY (id)
);

-- 组织成员检索索引与复合唯一约束
CREATE INDEX idx_member_organization_id ON "member" (organization_id); -- 外键索引，加速组织下成员检索与级联删除
CREATE INDEX idx_member_user_id ON "member" (user_id); -- 外键索引，加速用户所属组织检索与级联删除
CREATE UNIQUE INDEX idx_member_org_user ON "member" (organization_id, user_id); -- 组织与用户成员关系复合唯一索引

-- ─────────────────────────────────────────────────────────────────────────────
-- 5. 组织成员邀请表 ("invitation")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "invitation" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  organization_id text NOT NULL REFERENCES "organization"(id) ON DELETE CASCADE, -- 邀请加入的组织ID，级联删除
  email text NOT NULL, -- 受邀目标电子邮箱地址
  role text NOT NULL DEFAULT 'member', -- 邀请加入后分配的组织角色
  status text NOT NULL DEFAULT 'pending', -- 邀请状态标识（pending: 待处理, accepted: 已接受, rejected: 已拒绝, canceled: 已取消）
  expires_at timestamptz NOT NULL, -- 邀请链接失效过期时间戳
  inviter_id text NOT NULL REFERENCES "user"(id) ON DELETE CASCADE, -- 发起邀请的操作者用户ID，级联删除
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  PRIMARY KEY (id),
  CONSTRAINT ck_invitation_status CHECK (status IN ('pending', 'accepted', 'rejected', 'canceled'))
);

-- 组织邀请检索索引
CREATE INDEX idx_invitation_organization_id ON "invitation" (organization_id); -- 外键索引，加速组织待处理邀请列表检索
CREATE INDEX idx_invitation_email ON "invitation" (LOWER(email)); -- 受邀邮箱索引，加速用户接收邀请核对
CREATE INDEX idx_invitation_inviter_id ON "invitation" (inviter_id); -- 发起人外键索引，加速安全审计与级联删除
CREATE INDEX idx_invitation_expires_at ON "invitation" (expires_at); -- 过期时间索引，便于定时任务归档清理过期邀请

-- ─────────────────────────────────────────────────────────────────────────────
-- 6. 持久化会话管理表 ("session")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "session" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  user_id text NOT NULL REFERENCES "user"(id) ON DELETE CASCADE, -- 关联的用户ID，级联删除
  token text NOT NULL, -- 加密安全的唯一会话凭据字串
  expires_at timestamptz NOT NULL, -- 会话绝对过期时间戳
  ip_address text NULL, -- 会话创建时的客户端 IP 地址
  user_agent text NULL, -- 会话创建时的客户端 User-Agent 字串
  active_organization_id text NULL REFERENCES "organization"(id) ON DELETE SET NULL, -- 当前会话激活的工作空间/组织ID（租户切换）
  impersonated_by text NULL REFERENCES "user"(id) ON DELETE SET NULL, -- 管理员模拟代登时的原管理者用户ID（安全审计）
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  PRIMARY KEY (id)
);

-- 会话表检索索引与唯一性约束
CREATE INDEX idx_session_user_id ON "session" (user_id); -- 外键显式 B-Tree 索引，加速用户会话检索与级联删除
CREATE UNIQUE INDEX idx_session_token ON "session" (token); -- 会话 Token 唯一检索索引
CREATE INDEX idx_session_expires_at ON "session" (expires_at); -- 过期时间索引，用于定时任务清理僵尸会话
CREATE INDEX idx_session_active_organization_id ON "session" (active_organization_id); -- 激活组织外键索引，加速租户会话过滤
CREATE INDEX idx_session_impersonated_by ON "session" (impersonated_by); -- 模拟登录管理者外键索引，加速审计追踪

-- ─────────────────────────────────────────────────────────────────────────────
-- 7. 验证凭据与一次性口令表 ("verification")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "verification" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  identifier text NOT NULL, -- 验证主体标识符（如用户邮箱或手机号）
  value text NOT NULL, -- 验证码明文或加密哈希值
  expires_at timestamptz NOT NULL, -- 验证码过期失效时间戳
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  PRIMARY KEY (id)
);

-- 验证凭据检索索引
CREATE INDEX idx_verification_identifier_value ON "verification" (identifier, value); -- 复合索引，用于快速匹配主体与验证码校验
CREATE INDEX idx_verification_expires_at ON "verification" (expires_at); -- 过期时间索引，用于定时任务批量清理失效验证码

-- ─────────────────────────────────────────────────────────────────────────────
-- 8. TOTP 二步验证配置表 ("two_factor")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "two_factor" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  user_id text NOT NULL REFERENCES "user"(id) ON DELETE CASCADE, -- 关联的用户ID，级联删除
  secret text NOT NULL, -- TOTP 密钥加密文本
  backup_codes text NOT NULL, -- 备用恢复码列表（JSON 序列化存储）
  verified boolean NOT NULL DEFAULT true, -- 2FA 是否已完成首次挑战并确认激活
  failed_verification_count integer NOT NULL DEFAULT 0, -- 连续验证失败累计次数（防暴力破解）
  locked_until timestamptz NULL, -- 验证失败过多锁定的截止时间戳
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  PRIMARY KEY (id)
);

-- 二步验证配置索引
CREATE UNIQUE INDEX idx_two_factor_user_id ON "two_factor" (user_id); -- 用户 2FA 配置唯一索引（一对一关联）
CREATE INDEX idx_two_factor_secret ON "two_factor" (secret); -- 密钥检索索引

-- ─────────────────────────────────────────────────────────────────────────────
-- 9. Passkey / WebAuthn 硬件密钥表 ("passkey")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "passkey" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  name text NULL, -- Passkey 硬件密钥友好别名（如 Touch ID, YubiKey）
  public_key text NOT NULL, -- WebAuthn 公钥凭证数据（Base64/PEM 格式）
  user_id text NOT NULL REFERENCES "user"(id) ON DELETE CASCADE, -- 绑定的用户ID，级联删除
  credential_id text NOT NULL, -- WebAuthn 凭据全局唯一标识符
  counter bigint NOT NULL DEFAULT 0, -- 客户端签名计数器（递增校验，防御重放攻击）
  device_type text NOT NULL DEFAULT 'singleDevice', -- 设备支持类型（singleDevice: 单设备绑定, multiDevice: 多端同步）
  backed_up boolean NOT NULL DEFAULT false, -- 凭据是否已在云端 Passkey 同步链中安全备份
  transports text NULL, -- 传输通信协议列表（如 usb, ble, nfc, internal）
  aaguid text NULL, -- WebAuthn 认证器证明 GUID（AAGUID，用于精确识别硬件制造商与型号）
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 凭据注册创建时间戳
  PRIMARY KEY (id)
);

-- Passkey 凭据索引
CREATE INDEX idx_passkey_user_id ON "passkey" (user_id); -- 外键索引，加速用户 Passkey 凭据检索与级联删除
CREATE UNIQUE INDEX idx_passkey_credential_id ON "passkey" (credential_id); -- 凭据全局唯一索引，用于身份握手定位

-- ─────────────────────────────────────────────────────────────────────────────
-- 10. API Key 开发者密钥管理表 ("apikey")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "apikey" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  config_id text NOT NULL DEFAULT 'default', -- API Key 配置规则组标识符
  name text NULL, -- API Key 友好显示名称
  start text NULL, -- 安全脱敏前缀片段（如 ak_live_xxxx，用于界面识别）
  prefix text NULL, -- 自定义前缀标识
  key text NOT NULL, -- API Key 安全哈希摘要值（严禁明文存储）
  user_id text NOT NULL REFERENCES "user"(id) ON DELETE CASCADE, -- 绑定的归属用户ID，级联删除
  reference_id text NULL, -- 关联扩展主体标识符（支持泛化绑定组织/用户）
  refill_interval integer NULL, -- 额度充能刷新周期（单位：秒）
  refill_amount integer NULL, -- 每次刷新补充的额度配额数量
  last_refill_at timestamptz NULL, -- 最近一次额度刷新时间戳
  enabled boolean NOT NULL DEFAULT true, -- 密钥可用状态开关
  rate_limit_enabled boolean NOT NULL DEFAULT true, -- 是否启用内置速率限制保护
  rate_limit_time_window integer NULL DEFAULT 86400000, -- 速率限制滑动窗口时长（单位：毫秒，默认 24小时）
  rate_limit_max integer NULL DEFAULT 10, -- 滑动窗口内最大允许请求次数
  request_count integer NOT NULL DEFAULT 0, -- 当前窗口内已累计请求次数
  remaining integer NULL, -- 剩余可用配额
  last_request_at timestamptz NULL, -- 最近一次请求调用时间戳
  expires_at timestamptz NULL, -- 密钥绝对过期时间戳（为空表示永不过期）
  permissions text NULL, -- 细粒度权限作用域列表（JSON 格式字符串）
  metadata text NULL, -- 扩展自定义元数据（JSON 格式字符串）
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  PRIMARY KEY (id)
);

-- API Key 索引
CREATE INDEX idx_apikey_config_id ON "apikey" (config_id); -- 配置组索引
CREATE INDEX idx_apikey_user_id ON "apikey" (user_id); -- 外键索引，加速用户 API Key 管理列表检索
CREATE INDEX idx_apikey_reference_id ON "apikey" (reference_id); -- 扩展主体检索索引
CREATE UNIQUE INDEX idx_apikey_key ON "apikey" (key); -- 密钥哈希值唯一检索索引，用于鉴权匹配

-- ─────────────────────────────────────────────────────────────────────────────
-- 11. 企业单点登录 SSO / SAML / OIDC 配置表 ("sso_provider")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "sso_provider" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  provider_id text NOT NULL, -- SSO 提供商全局唯一标识符（如 okta-enterprise）
  issuer text NOT NULL, -- SSO 身份提供商发行方标识（如 Okta/AzureAD Issuer URL）
  oidc_config text NULL, -- OpenID Connect 协议客户端配置（JSON 格式）
  saml_config text NULL, -- SAML 2.0 协议客户端配置与元数据 XML/JSON
  user_id text NULL REFERENCES "user"(id) ON DELETE CASCADE, -- 关联创建人用户ID（可选），级联删除
  organization_id text NULL REFERENCES "organization"(id) ON DELETE CASCADE, -- 关联企业组织ID（可选），级联删除
  domain text NOT NULL, -- 企业邮箱匹配路由域名（如 company.com）
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  PRIMARY KEY (id)
);

-- SSO 提供商索引
CREATE UNIQUE INDEX idx_sso_provider_provider_id ON "sso_provider" (provider_id); -- 提供商唯一标识索引
CREATE UNIQUE INDEX idx_sso_provider_domain ON "sso_provider" (LOWER(domain)); -- 邮箱域名唯一索引，用于企业登录自动路由
CREATE INDEX idx_sso_provider_organization_id ON "sso_provider" (organization_id); -- 外键索引，加速企业组织 SSO 配置检索
CREATE INDEX idx_sso_provider_user_id ON "sso_provider" (user_id); -- 外键索引，加速配置所有者检索

-- ─────────────────────────────────────────────────────────────────────────────
-- 12. 非对称密钥对轮换表 ("jwks")
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "jwks" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  public_key text NOT NULL, -- RSA/EC 公钥数据（PEM 格式或 JWK JSON 文本）
  private_key text NOT NULL, -- RSA/EC 私钥安全加密存储文本
  alg text NULL, -- 签名算法标识（如 RS256, EdDSA）
  crv text NULL, -- 椭圆曲线加密参数（如 Ed25519, P-256）
  expires_at timestamptz NULL, -- 密钥绝对失效过期时间戳（支持密钥轮换注销）
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 密钥对生成创建时间戳
  PRIMARY KEY (id)
);

-- ─────────────────────────────────────────────────────────────────────────────
-- 13. 设备码授权流表 ("device_code"，对齐 RFC 8628 Device Authorization Grant)
-- ─────────────────────────────────────────────────────────────────────────────
CREATE TABLE "device_code" (
  id text NOT NULL DEFAULT uuidv7()::text, -- UUIDv7 主键标识符（默认基于 PostgreSQL 18 原生内核自动生成）
  device_code text NOT NULL, -- 客户端设备授权码（高熵不透明凭据）
  user_code text NOT NULL, -- 用户在网页端输入确认的简短验证码（如 BCD-FGH）
  user_id text NULL REFERENCES "user"(id) ON DELETE CASCADE, -- 授权绑定的用户ID（用户同意授权后填充）
  expires_at timestamptz NOT NULL, -- 设备码过期失效时间戳
  status text NOT NULL DEFAULT 'pending', -- 授权状态（pending: 待授权, approved: 已授权, denied: 已拒绝）
  last_polled_at timestamptz NULL, -- 设备客户端最近一次轮询拉取时间戳
  polling_interval integer NOT NULL DEFAULT 5, -- 建议设备客户端轮询最小间隔（单位：秒）
  client_id text NULL, -- 请求授权的客户端应用标识
  scope text NULL, -- 请求的权限作用域
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录创建时间戳
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP, -- 记录最后更新时间戳
  PRIMARY KEY (id),
  CONSTRAINT ck_device_code_status CHECK (status IN ('pending', 'approved', 'denied'))
);

-- 设备码授权流索引与唯一约束
CREATE UNIQUE INDEX idx_device_code_device_code ON "device_code" (device_code); -- 设备码全局唯一索引
CREATE UNIQUE INDEX idx_device_code_user_code ON "device_code" (user_code); -- 用户验证码全局唯一索引
CREATE INDEX idx_device_code_user_id ON "device_code" (user_id); -- 用户外键索引
CREATE INDEX idx_device_code_expires_at ON "device_code" (expires_at); -- 过期时间索引，便于定时清理失效授权

