//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/errs"
)

// mailpitBaseURL 是本地 Mailpit 的 HTTP 接口，用于取回真实投递的验证码。
const mailpitBaseURL = "http://127.0.0.1:8025/api/v1"

// TestHealthEndpoint 校验存活探针与根路径端点。
func TestHealthEndpoint(t *testing.T) {
	engine := newEngine(t)

	result := call(t, engine, http.MethodGet, "/livez", "", "")
	assert.Equal(t, http.StatusOK, result.Status)
	assert.Equal(t, "ok", str(result.Body, "status"))

	rootResult := call(t, engine, http.MethodGet, "/", "", "")
	assert.Equal(t, http.StatusOK, rootResult.Status)
	assert.Equal(t, "ok", str(rootResult.Body, "status"))
}

// TestGlobalRoutingAndErrorContract 校验未映射路由与未支持动词返回统一错误 JSON（api.md 5.2）。
func TestGlobalRoutingAndErrorContract(t *testing.T) {
	engine := newEngine(t)

	t.Run("未映射路由返回标准 404 JSON 而非框架纯文本", func(t *testing.T) {
		result := call(t, engine, http.MethodGet, "/v1/definitely-not-found-endpoint", "", "")

		require.Equal(t, http.StatusNotFound, result.Status, result.Raw)
		assert.Equal(t, "NOT_FOUND", str(result.Body, "code"))
		assert.NotEmpty(t, str(result.Body, "message"))
		assert.Contains(t, result.Header.Get("Content-Type"), "application/json")
	})

	t.Run("未支持动词返回标准 405 JSON 并下发 Allow 头", func(t *testing.T) {
		result := call(t, engine, http.MethodDelete, "/v1/auth/sign-in/email", "", "")

		require.Equal(t, http.StatusMethodNotAllowed, result.Status, result.Raw)
		assert.Equal(t, "METHOD_NOT_ALLOWED", str(result.Body, "code"))
		assert.NotEmpty(t, str(result.Body, "message"))
		assert.NotEmpty(t, result.Header.Get("Allow"), "405 响应必须携带 RFC Allow 响应头")
		assert.Contains(t, result.Header.Get("Content-Type"), "application/json")
	})
}

// TestSignUpAndSessionContract 覆盖注册响应体与只读会话端点（Q13）。
func TestSignUpAndSessionContract(t *testing.T) {
	engine := newEngine(t)
	token, refreshToken, userID, email := signUp(t, engine)

	require.NotEmpty(t, userID)
	assert.NotEmpty(t, token)
	assert.NotEmpty(t, refreshToken)
	assert.NotEqual(t, token, refreshToken, "双令牌必须不同")

	t.Run("get-session 返回 user 且不含 session 实体", func(t *testing.T) {
		result := call(t, engine, http.MethodGet, "/v1/auth/get-session", "", token)

		require.Equal(t, http.StatusOK, result.Status, result.Raw)
		user := nested(result.Body, "user")
		require.NotEmpty(t, user)
		assert.Equal(t, email, str(user, "email"))
		assert.NotContains(t, result.Body, "session", "本系统没有会话实体")
		assert.NotContains(t, result.Body, "refreshToken", "get-session 是纯只读，不续签")
	})

	t.Run("时间戳按 UTC 且以 Z 结尾", func(t *testing.T) {
		result := call(t, engine, http.MethodGet, "/v1/auth/get-session", "", token)
		require.Equal(t, http.StatusOK, result.Status)

		user := nested(result.Body, "user")
		createdAt := str(user, "createdAt")
		assert.True(t, strings.HasSuffix(createdAt, "Z"),
			"api.md 2.2 要求时间以 Z 结尾，实际为 %q", createdAt)
	})

	t.Run("邮箱大小写不影响登录", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-in/email", map[string]any{
			"email":    upper(email),
			"password": "secret12345",
		}, "")

		assert.Equal(t, http.StatusOK, result.Status, result.Raw)
	})

	t.Run("重复注册返回 409", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-up/email", map[string]any{
			"email":    email,
			"password": "secret12345",
			"name":     "Duplicate",
		}, "")

		assert.Equal(t, http.StatusConflict, result.Status)
		assert.Equal(t, "AUTH_EMAIL_ALREADY_EXISTS", str(result.Body, "code"))
	})

	t.Run("用户名占用返回 409", func(t *testing.T) {
		first := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-up/email", map[string]any{
			"email": uniqueEmail(), "password": "secret12345", "name": "A", "username": uniqueUsername(),
		}, "").Body
		username := str(nested(first, "user"), "username")

		result := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-up/email", map[string]any{
			"email": uniqueEmail(), "password": "secret12345", "name": "B", "username": username,
		}, "")

		assert.Equal(t, http.StatusConflict, result.Status)
		assert.Equal(t, "AUTH_USERNAME_TAKEN", str(result.Body, "code"))
	})
}

// TestUnauthorizedShapes 覆盖 401 的三种成因。
func TestUnauthorizedShapes(t *testing.T) {
	engine := newEngine(t)

	t.Run("缺少 Authorization 头", func(t *testing.T) {
		result := call(t, engine, http.MethodGet, "/v1/users/me", "", "")

		assert.Equal(t, http.StatusUnauthorized, result.Status)
		assert.Equal(t, "AUTH_TOKEN_INVALID", str(result.Body, "code"))
	})

	t.Run("Authorization 头缺 Bearer 前缀", func(t *testing.T) {
		// 直接写入缺少 Bearer 前缀的原始头，覆盖解析失败分支（call 会自动补前缀，无法表达该场景）。
		result := callRawAuth(t, engine, http.MethodGet, "/v1/users/me", "not-a-jwt")
		assert.Equal(t, http.StatusUnauthorized, result.Status)
		assert.Equal(t, "AUTH_TOKEN_INVALID", str(result.Body, "code"))

		// 前缀正确但令牌本身畸形，同样必须拒绝。
		raw := call(t, engine, http.MethodGet, "/v1/auth/get-session", "", "not-a-jwt")
		assert.Equal(t, http.StatusUnauthorized, raw.Status)
	})

	t.Run("刷新令牌不能访问受保护资源", func(t *testing.T) {
		_, refreshToken, _, _ := signUp(t, engine)

		result := call(t, engine, http.MethodGet, "/v1/users/me", "", refreshToken)
		assert.Equal(t, http.StatusUnauthorized, result.Status, "刷新令牌不得当作访问令牌使用")
	})
}

// TestValidationResponseShape 校验 422 的字段路径与文案（api.md 5.2）。
func TestValidationResponseShape(t *testing.T) {
	engine := newEngine(t)

	result := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-up/email", map[string]any{
		"email": "not-an-email", "password": "short", "name": "",
	}, "")

	require.Equal(t, http.StatusUnprocessableEntity, result.Status)
	assert.Equal(t, "VALIDATION_FAILED", str(result.Body, "code"))
	assert.Equal(t, "Validation failed", str(result.Body, "message"))

	for _, field := range []string{"email", "password", "name"} {
		assert.NotEmpty(t, errorMessages(result.Body, field), "缺少字段 %s 的明细: %s", field, result.Raw)
	}
	assert.Equal(t,
		[]string{"The password must be at least 8 characters."},
		errorMessages(result.Body, "password"),
	)

	t.Run("保留字用户名被拦截", func(t *testing.T) {
		reserved := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-up/email", map[string]any{
			"email": uniqueEmail(), "password": "secret12345", "name": "X", "username": "admin",
		}, "")

		require.Equal(t, http.StatusUnprocessableEntity, reserved.Status)
		assert.Equal(t,
			[]string{"The username is reserved and cannot be used."},
			errorMessages(reserved.Body, "username"),
		)
	})

	t.Run("空请求体是 400 而非 422", func(t *testing.T) {
		empty := call(t, engine, http.MethodPost, "/v1/auth/sign-up/email", "", "")
		assert.Equal(t, http.StatusBadRequest, empty.Status)
		assert.Equal(t, "BAD_REQUEST", str(empty.Body, "code"))
	})
}

// TestProfilePatchThreeState 校验 PATCH 的三态语义真的落到库里（api.md 2.3）。
func TestProfilePatchThreeState(t *testing.T) {
	engine := newEngine(t)
	token, _, _, _ := signUp(t, engine)

	t.Run("缺省字段保持原值", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPatch, "/v1/users/me", map[string]any{"name": "Renamed"}, token)

		require.Equal(t, http.StatusOK, result.Status, result.Raw)
		assert.Equal(t, "Renamed", str(result.Body, "name"))
		assert.Nil(t, result.Body["image"], "未提供的字段应保持原值（nil）")
	})

	t.Run("具体值覆盖", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPatch, "/v1/users/me", map[string]any{
			"image": "https://example.com/a.png",
		}, token)

		require.Equal(t, http.StatusOK, result.Status, result.Raw)
		assert.Equal(t, "https://example.com/a.png", str(result.Body, "image"))
		assert.Equal(t, "Renamed", str(result.Body, "name"), "其他字段不应被清掉")
	})

	t.Run("显式 null 置空", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPatch, "/v1/users/me", map[string]any{"image": nil}, token)

		require.Equal(t, http.StatusOK, result.Status, result.Raw)
		assert.Nil(t, result.Body["image"], "显式 null 应把字段置空")
	})

	t.Run("NOT NULL 字段置空返回 422", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPatch, "/v1/users/me", map[string]any{"name": nil}, token)

		require.Equal(t, http.StatusUnprocessableEntity, result.Status)
		assert.NotEmpty(t, errorMessages(result.Body, "name"))
	})

	t.Run("自助修改不接受邮箱与角色", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPatch, "/v1/users/me", map[string]any{
			"email": "attacker@bgin.local",
			"role":  "admin",
		}, token)

		require.Equal(t, http.StatusOK, result.Status, result.Raw)
		assert.NotEqual(t, "attacker@bgin.local", str(result.Body, "email"))
		assert.NotEqual(t, "admin", str(result.Body, "role"))
	})
}

// TestPasswordChangeAndReset 覆盖改密与两条重置路径。
func TestPasswordChangeAndReset(t *testing.T) {
	engine := newEngine(t)
	token, _, _, email := signUp(t, engine)

	t.Run("当前口令错误返回 422", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/change-password", map[string]any{
			"currentPassword": "wrong-password",
			"newPassword":     "secret54321",
		}, token)

		require.Equal(t, http.StatusUnprocessableEntity, result.Status)
		assert.Equal(t, "AUTH_PASSWORD_MISMATCH", str(result.Body, "code"))
	})

	t.Run("改密成功返回 204 且新口令可用", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/change-password", map[string]any{
			"currentPassword": "secret12345",
			"newPassword":     "secret54321",
		}, token)

		require.Equal(t, http.StatusNoContent, result.Status, result.Raw)
		assert.Empty(t, result.Raw)

		login := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-in/email", map[string]any{
			"email": email, "password": "secret54321",
		}, "")
		assert.Equal(t, http.StatusOK, login.Status)
		token = str(login.Body, "token")
	})

	t.Run("弱口令被拒绝", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/change-password", map[string]any{
			"currentPassword": "secret54321",
			"newPassword":     "onlyletters",
		}, token)

		require.Equal(t, http.StatusUnprocessableEntity, result.Status)
		assert.Equal(t, string(errs.CodeAuthPasswordTooWeak), result.Body["code"])
		assert.NotEmpty(t, errorMessages(result.Body, "newPassword"))
	})

	t.Run("忘记密码不会暴露邮箱是否存在", func(t *testing.T) {
		known := callJSON(t, engine, http.MethodPost, "/v1/auth/forget-password", map[string]any{"email": email}, "")
		unknown := callJSON(t, engine, http.MethodPost, "/v1/auth/forget-password", map[string]any{"email": uniqueEmail()}, "")

		assert.Equal(t, http.StatusOK, known.Status)
		assert.Equal(t, http.StatusOK, unknown.Status)
		assert.Equal(t, known.Raw, unknown.Raw, "两种情况的响应体必须完全一致")
	})

	t.Run("重置验证码错误返回 422", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/reset-password", map[string]any{
			"email": email, "otp": "000000", "newPassword": "secret98765",
		}, "")

		require.Equal(t, http.StatusUnprocessableEntity, result.Status)
		assert.Equal(t, "AUTH_OTP_EXPIRED", str(result.Body, "code"), "无待核销凭据时按已过期处理")
	})

	t.Run("既无令牌也无验证码返回 422", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/reset-password", map[string]any{
			"email": email, "newPassword": "secret98765",
		}, "")

		require.Equal(t, http.StatusUnprocessableEntity, result.Status)
		assert.NotEmpty(t, errorMessages(result.Body, "token"))
	})
}

// TestTokenRefreshRotationAndSignOut 覆盖刷新令牌轮换与登出撤销（ADR-0001）。
func TestTokenRefreshRotationAndSignOut(t *testing.T) {
	engine := newEngine(t)
	token, refreshToken, _, _ := signUp(t, engine)

	var rotatedRefresh string

	t.Run("刷新换取新令牌", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/token/refresh", map[string]any{
			"refreshToken": refreshToken,
		}, "")

		require.Equal(t, http.StatusOK, result.Status, result.Raw)
		assert.NotEmpty(t, str(result.Body, "token"))
		token = str(result.Body, "token")
		rotatedRefresh = str(result.Body, "refreshToken")
		assert.NotEqual(t, refreshToken, rotatedRefresh, "刷新令牌必须轮换")
	})

	t.Run("登出返回 204", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-out", map[string]any{
			"refreshToken": rotatedRefresh,
		}, token)

		require.Equal(t, http.StatusNoContent, result.Status, result.Raw)
	})

	t.Run("登出后访问令牌进黑名单", func(t *testing.T) {
		result := call(t, engine, http.MethodGet, "/v1/auth/get-session", "", token)

		require.Equal(t, http.StatusUnauthorized, result.Status)
		assert.Equal(t, "AUTH_TOKEN_REVOKED", str(result.Body, "code"))
	})

	t.Run("登出后刷新令牌也不可用", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/token/refresh", map[string]any{
			"refreshToken": rotatedRefresh,
		}, "")

		assert.Equal(t, http.StatusUnauthorized, result.Status)
	})

	t.Run("旧刷新令牌重用被拒绝", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/token/refresh", map[string]any{
			"refreshToken": refreshToken,
		}, "")

		require.Equal(t, http.StatusUnauthorized, result.Status, "轮换后旧令牌必须被拒")
		assert.Equal(t, "AUTH_TOKEN_REVOKED", str(result.Body, "code"))
	})
}

// TestAdminUserCRUDAndPermissions 覆盖 Casbin 权限矩阵与管理员 CRUD（ADR-0003）。
func TestAdminUserCRUDAndPermissions(t *testing.T) {
	engine := newEngine(t)
	userToken, _, userID, email := signUp(t, engine)

	t.Run("普通用户访问管理端点返回 403", func(t *testing.T) {
		result := call(t, engine, http.MethodGet, "/v1/admin/users", "", userToken)

		require.Equal(t, http.StatusForbidden, result.Status)
		assert.Equal(t, "FORBIDDEN", str(result.Body, "code"))
	})

	adminToken := signInAdmin(t, engine)

	t.Run("管理员列表使用 items/meta 信封", func(t *testing.T) {
		result := call(t, engine, http.MethodGet, "/v1/admin/users?page=1&pageSize=5&sort=-createdAt", "", adminToken)

		require.Equal(t, http.StatusOK, result.Status, result.Raw)
		items, ok := result.Body["items"].([]any)
		require.True(t, ok, "响应体必须带 items 数组: %s", result.Raw)
		assert.LessOrEqual(t, len(items), 5)

		meta := nested(result.Body, "meta")
		assert.Equal(t, 1, num(meta, "page"))
		assert.Equal(t, 5, num(meta, "pageSize"))
		assert.GreaterOrEqual(t, num(meta, "totalPages"), 0)
	})

	t.Run("搜索结果为空时 items 仍是数组", func(t *testing.T) {
		result := call(t, engine, http.MethodGet, "/v1/admin/users?search=definitely-no-such-user-xyz", "", adminToken)

		require.Equal(t, http.StatusOK, result.Status)
		assert.JSONEq(t, `[]`, mustJSON(t, result.Body["items"]))
	})

	t.Run("分页与排序非法值返回 422", func(t *testing.T) {
		result := call(t, engine, http.MethodGet, "/v1/admin/users?pageSize=999&sort=hacked", "", adminToken)

		require.Equal(t, http.StatusUnprocessableEntity, result.Status)
		assert.NotEmpty(t, errorMessages(result.Body, "pageSize"))
		assert.NotEmpty(t, errorMessages(result.Body, "sort"))
	})

	t.Run("搜索命中刚注册的用户", func(t *testing.T) {
		// 邮箱里的 + 属于表单编码的保留字符，必须转义，否则会被解成空格。
		result := call(t, engine, http.MethodGet, "/v1/admin/users?search="+url.QueryEscape(email), "", adminToken)

		require.Equal(t, http.StatusOK, result.Status)
		items, _ := result.Body["items"].([]any)
		require.Len(t, items, 1)
		assert.Equal(t, userID, str(obj(items[0]), "id"))
	})

	t.Run("搜索关键词里的 LIKE 通配符按字面量处理", func(t *testing.T) {
		result := call(t, engine, http.MethodGet, "/v1/admin/users?search="+url.QueryEscape("%"), "", adminToken)

		require.Equal(t, http.StatusOK, result.Status)
		items, _ := result.Body["items"].([]any)
		assert.Empty(t, items, "通配符必须被转义，不能退化成匹配全部")
	})

	t.Run("管理员读取单个用户", func(t *testing.T) {
		result := call(t, engine, http.MethodGet, "/v1/admin/users/"+userID, "", adminToken)

		require.Equal(t, http.StatusOK, result.Status, result.Raw)
		assert.Equal(t, email, str(result.Body, "email"))
		assert.Contains(t, result.Body, "banned", "管理视图必须带封禁字段")
	})

	t.Run("管理员封禁用户会同步 status", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPatch, "/v1/admin/users/"+userID, map[string]any{
			"banned":    true,
			"banReason": "integration test",
		}, adminToken)

		require.Equal(t, http.StatusOK, result.Status, result.Raw)
		assert.Equal(t, true, result.Body["banned"])
		assert.Equal(t, "banned", str(result.Body, "status"), "banned=true 必须同步 status=banned")

		// 封禁后该用户不能再登录。
		login := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-in/email", map[string]any{
			"email": email, "password": "secret12345",
		}, "")
		assert.Equal(t, http.StatusForbidden, login.Status)
		assert.Equal(t, "AUTH_ACCOUNT_BANNED", str(login.Body, "code"))

		// 恢复原状，避免影响后续用例。
		restore := callJSON(t, engine, http.MethodPatch, "/v1/admin/users/"+userID, map[string]any{
			"banned": false, "status": "active",
		}, adminToken)
		require.Equal(t, http.StatusOK, restore.Status, restore.Raw)
	})

	t.Run("管理员改角色需要重新签发令牌才生效", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPatch, "/v1/admin/users/"+userID, map[string]any{
			"role": "admin",
		}, adminToken)
		require.Equal(t, http.StatusOK, result.Status, result.Raw)

		// 旧令牌里的 role 声明仍是 user，因此拿不到管理权限（ADR-0001 / ADR-0003）。
		stillForbidden := call(t, engine, http.MethodGet, "/v1/admin/users", "", userToken)
		assert.Equal(t, http.StatusForbidden, stillForbidden.Status)

		// 重新登录后新令牌才带上 admin。
		relogin := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-in/email", map[string]any{
			"email": email, "password": "secret12345",
		}, "")
		require.Equal(t, http.StatusOK, relogin.Status, relogin.Raw)

		allowed := call(t, engine, http.MethodGet, "/v1/admin/users", "", str(relogin.Body, "token"))
		assert.Equal(t, http.StatusOK, allowed.Status)

		// 复位。
		demote := callJSON(t, engine, http.MethodPatch, "/v1/admin/users/"+userID, map[string]any{
			"role": "user",
		}, adminToken)
		require.Equal(t, http.StatusOK, demote.Status, demote.Raw)
	})

	t.Run("管理员软删用户返回 204 并释放邮箱", func(t *testing.T) {
		_, _, targetID, targetEmail := signUp(t, engine)

		deleted := call(t, engine, http.MethodDelete, "/v1/admin/users/"+targetID, "", adminToken)
		require.Equal(t, http.StatusNoContent, deleted.Status, deleted.Raw)

		// 被删用户不再出现在业务查询里。
		missing := call(t, engine, http.MethodGet, "/v1/admin/users/"+targetID, "", adminToken)
		assert.Equal(t, http.StatusNotFound, missing.Status)

		// 同一邮箱可以重新注册。
		again := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-up/email", map[string]any{
			"email": targetEmail, "password": "secret12345", "name": "Re-registered",
		}, "")
		assert.Equal(t, http.StatusCreated, again.Status, again.Raw)
	})
}

// TestRateLimitHeaders 校验 429 相关响应头（api.md 7）。
func TestRateLimitHeaders(t *testing.T) {
	engine := newEngine(t)

	result := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-in/email", map[string]any{
		"email": "nobody@bgin.local", "password": "whatever1",
	}, "")

	assert.Equal(t, http.StatusUnauthorized, result.Status)
	assert.NotEmpty(t, result.Header.Get("X-RateLimit-Limit"))
	assert.NotEmpty(t, result.Header.Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, result.Header.Get("X-RateLimit-Reset"))
	assert.NotEmpty(t, result.Header.Get("x-request-id"), "每个响应都必须回写追踪标识")
}

// TestEmailVerificationOTPFlow 走一遍真实的邮件验证码链路。
// 依赖 Mailpit 的 HTTP 接口；不可达时跳过而不是伪装通过。
func TestEmailVerificationOTPFlow(t *testing.T) {
	engine := newEngine(t)
	token, _, _, email := signUp(t, engine)

	sent := callJSON(t, engine, http.MethodPost, "/v1/auth/send-verification-otp", map[string]any{"email": email}, "")
	require.Equal(t, http.StatusOK, sent.Status, sent.Raw)

	code := waitForOTP(t, email)
	if code == "" {
		t.Skip("Mailpit 不可达或未收到验证码，跳过")
	}

	t.Run("验证码错误被拒绝", func(t *testing.T) {
		wrong := wrongCode(code)
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/verify-email", map[string]any{
			"email": email, "otp": wrong,
		}, "")

		require.Equal(t, http.StatusUnprocessableEntity, result.Status)
		assert.Equal(t, "AUTH_OTP_INVALID", str(result.Body, "code"))
	})

	t.Run("正确验证码置位 emailVerified", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/verify-email", map[string]any{
			"email": email, "otp": code,
		}, "")
		require.Equal(t, http.StatusOK, result.Status, result.Raw)

		session := call(t, engine, http.MethodGet, "/v1/auth/get-session", "", token)
		require.Equal(t, http.StatusOK, session.Status)
		assert.Equal(t, true, nested(session.Body, "user")["emailVerified"])
	})

	t.Run("同一验证码不能复用", func(t *testing.T) {
		result := callJSON(t, engine, http.MethodPost, "/v1/auth/verify-email", map[string]any{
			"email": email, "otp": code,
		}, "")

		assert.Equal(t, http.StatusUnprocessableEntity, result.Status, "核销后必须失效")
	})
}

// signInAdmin 用 .env 里的种子管理员登录。
func signInAdmin(t *testing.T, engine *gin.Engine) string {
	t.Helper()

	email := envOr("SEED_ADMIN_EMAIL", "admin@bgin.local")
	password := envOr("SEED_ADMIN_PASSWORD", "admin12345")

	result := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-in/email", map[string]any{
		"email": email, "password": password,
	}, "")
	require.Equal(t, http.StatusOK, result.Status, "管理员登录失败，请先在测试库执行 db:seed: %s", result.Raw)

	return str(result.Body, "token")
}

// waitForOTP 轮询 Mailpit，取回发往该邮箱的 6 位验证码。
func waitForOTP(t *testing.T, email string) string {
	t.Helper()

	for range 10 {
		if code := fetchOTP(email); code != "" {
			return code
		}
		time.Sleep(300 * time.Millisecond)
	}
	return ""
}

// fetchOTP 从 Mailpit 的消息列表里找出该邮箱的最新验证码。
func fetchOTP(email string) string {
	client := &http.Client{Timeout: 2 * time.Second}

	listResponse, err := client.Get(mailpitBaseURL + "/messages")
	if err != nil {
		return ""
	}
	defer func() { _ = listResponse.Body.Close() }()

	var list struct {
		Messages []struct {
			ID string `json:"ID"`
			To []struct {
				Address string `json:"Address"`
			} `json:"To"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(listResponse.Body).Decode(&list); err != nil {
		return ""
	}

	pattern := regexp.MustCompile(`\b(\d{6})\b`)
	for _, message := range list.Messages {
		if len(message.To) == 0 || message.To[0].Address != email {
			continue
		}

		detailResponse, err := client.Get(mailpitBaseURL + "/message/" + message.ID)
		if err != nil {
			continue
		}
		var detail struct {
			Text string `json:"Text"`
		}
		decodeErr := json.NewDecoder(detailResponse.Body).Decode(&detail)
		_ = detailResponse.Body.Close()
		if decodeErr != nil {
			continue
		}

		if match := pattern.FindStringSubmatch(detail.Text); match != nil {
			return match[1]
		}
	}
	return ""
}

// wrongCode 构造一个与正确验证码不同的 6 位数字。
func wrongCode(correct string) string {
	if correct == "000000" {
		return "000001"
	}
	return "000000"
}

// upper 把邮箱转成大写，用于验证大小写不敏感。
func upper(value string) string {
	encoded := []byte(value)
	for i := range encoded {
		if encoded[i] >= 'a' && encoded[i] <= 'z' {
			encoded[i] -= 'a' - 'A'
		}
	}
	return string(encoded)
}

// envOr 读取环境变量，缺省时返回兜底值。
func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// mustJSON 把任意值序列化成字符串，供 JSONEq 断言使用。
func mustJSON(t *testing.T, value any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}
