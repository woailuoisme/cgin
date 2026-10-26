package middleware_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/internal/database/sqlc"
	"cgin/internal/middleware"
	"cgin/internal/middleware/global"
	"cgin/pkg/authentication"
	"cgin/pkg/authorization"
	"cgin/pkg/cache"
	"cgin/pkg/errs"
	"cgin/shared/enums"
	"cgin/shared/user"
)

type memAuthBlacklistStore struct {
	mu   sync.RWMutex
	data map[string]string
}

func newMemAuthBlacklistStore() *memAuthBlacklistStore {
	return &memAuthBlacklistStore{data: make(map[string]string)}
}

func (s *memAuthBlacklistStore) Get(_ context.Context, key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.data[key]
	if !ok {
		return "", cache.ErrNotFound
	}
	return val, nil
}

func (s *memAuthBlacklistStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *memAuthBlacklistStore) Exists(_ context.Context, key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[key]
	return ok, nil
}

type mockUserRepo struct {
	users map[string]*sqlc.User
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{users: make(map[string]*sqlc.User)}
}

func (r *mockUserRepo) FindByID(_ context.Context, id string) (*sqlc.User, error) {
	u, ok := r.users[id]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return u, nil
}

func (r *mockUserRepo) FindByCredentials(_ context.Context, creds authentication.Credentials) (*sqlc.User, error) {
	u, ok := r.users[creds.Account]
	if !ok {
		return nil, pgx.ErrNoRows
	}
	return u, nil
}

func (r *mockUserRepo) ValidateCredentials(_ context.Context, _ *sqlc.User, _ authentication.Credentials) (bool, error) {
	return true, nil
}

// Identity 把测试用户投影为令牌身份。
func (r *mockUserRepo) Identity(u *sqlc.User) authentication.Identity {
	return user.Identity(u)
}

// setupAuthEnv 构建测试用中间件与守卫环境。
func setupAuthEnv() (
	*middleware.Middleware,
	*authentication.TokenIssuer,
	*authentication.Guard[*sqlc.User],
	*authentication.Guard[*sqlc.User],
	*mockUserRepo,
) {
	gin.SetMode(gin.TestMode)

	store := newMemAuthBlacklistStore()
	tokens := authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     "0123456789abcdef0123456789abcdef",
		Issuer:     "cgin-test",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	}, store)

	repo := newMockUserRepo()
	clientGuard := authentication.NewGuard(tokens, repo, user.ClientPolicy{}, repo.Identity)
	adminGuard := authentication.NewGuard(tokens, repo, user.AdminPolicy{}, repo.Identity)

	mgr := authentication.NewManager()
	mgr.Register("client", clientGuard)
	mgr.Register("admin", adminGuard)
	mgr.SetDefault("client")

	enforcer, err := authorization.New()
	if err != nil {
		panic(err)
	}

	m := middleware.New(middleware.Deps{
		AuthManager: mgr,
		Authz:       enforcer,
	})

	return m, tokens, clientGuard, adminGuard, repo
}

// TestAuth_MissingOrMalformedHeader 验证缺失或畸形 Authorization 报头时的 401 拦截。
func TestAuth_MissingOrMalformedHeader(t *testing.T) {
	m, _, _, _, _ := setupAuthEnv()

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	router.GET("/protected", m.Auth(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// 1. 缺失报头
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	var errResp errs.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, string(errs.CodeAuthTokenInvalid), errResp.Code)

	// 2. 畸形 scheme
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// 3. 空 Bearer
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer ")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// 4. 仅 Bearer 无后续参数
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// 5. 多段超额参数
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer token1 token2")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestAuth_DefaultGuardSuccess 验证默认守卫 (client) 成功解析令牌并将身份写入上下文，同时支持多空格或制表符容错。
func TestAuth_DefaultGuardSuccess(t *testing.T) {
	m, _, clientGuard, _, repo := setupAuthEnv()

	repo.users["usr_client_1"] = &sqlc.User{
		ID:     "usr_client_1",
		Role:   enums.RoleUser,
		Status: enums.StatusActive,
		Name:   "Client User",
	}

	sess, err := clientGuard.Login(context.Background(), repo.users["usr_client_1"])
	require.NoError(t, err)

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())

	var capturedID authentication.Identity
	router.GET("/client/me", m.Auth(), func(c *gin.Context) {
		id, ok := authentication.FromContext(c.Request.Context())
		if ok {
			capturedID = id
		}
		c.Status(http.StatusOK)
	})

	// 验证标准单空格、连续多空格及制表符均可正确提取
	headers := []string{
		"Bearer " + sess.Token,
		"Bearer   " + sess.Token,
		"Bearer\t" + sess.Token,
	}

	for _, h := range headers {
		capturedID = authentication.Identity{}
		req := httptest.NewRequest(http.MethodGet, "/client/me", nil)
		req.Header.Set("Authorization", h)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "usr_client_1", capturedID.UserID)
		assert.Equal(t, string(enums.RoleUser), capturedID.Role)
	}
}

// TestAuth_NamedAdminGuard 验证指定 admin 守卫时的权限拦截与放行。
func TestAuth_NamedAdminGuard(t *testing.T) {
	m, _, clientGuard, adminGuard, repo := setupAuthEnv()

	repo.users["usr_normal"] = &sqlc.User{
		ID:     "usr_normal",
		Role:   enums.RoleUser,
		Status: enums.StatusActive,
	}
	repo.users["usr_admin"] = &sqlc.User{
		ID:     "usr_admin",
		Role:   enums.RoleAdmin,
		Status: enums.StatusActive,
	}

	clientSess, err := clientGuard.Login(context.Background(), repo.users["usr_normal"])
	require.NoError(t, err)

	adminSess, err := adminGuard.Login(context.Background(), repo.users["usr_admin"])
	require.NoError(t, err)

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	router.GET("/admin/dashboard", m.Auth("admin"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// 1. 普通用户携带有效令牌访问 admin 接口，被 AdminPolicy 拦截返回 403 Forbidden
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+clientSess.Token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	var errResp errs.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, string(errs.CodeForbidden), errResp.Code)

	// 2. 管理员携带有效令牌访问 admin 接口，放行 200 OK
	req = httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+adminSess.Token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestAuth_RevocationEpoch 验证用户级注销纪元触发后，HTTP 网关即时拒绝已注销令牌。
func TestAuth_RevocationEpoch(t *testing.T) {
	m, _, clientGuard, _, repo := setupAuthEnv()

	repo.users["usr_revoked"] = &sqlc.User{
		ID:     "usr_revoked",
		Role:   enums.RoleUser,
		Status: enums.StatusActive,
	}

	sess, err := clientGuard.Login(context.Background(), repo.users["usr_revoked"])
	require.NoError(t, err)

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	router.GET("/client/profile", m.Auth("client"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// 初始状态下有效
	req := httptest.NewRequest(http.MethodGet, "/client/profile", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// 触发用户级全局作废（如密码重置或点击注销所有设备）
	err = clientGuard.InvalidateAll(context.Background(), "usr_revoked")
	require.NoError(t, err)

	// 再次请求返回 401 撤销错误
	req = httptest.NewRequest(http.MethodGet, "/client/profile", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	var errResp errs.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, string(errs.CodeAuthTokenRevoked), errResp.Code)
}

// TestAuth_NilManagerFailsClosed 验证未注入 AuthManager 时 fail-closed 返回 500。
func TestAuth_NilManagerFailsClosed(t *testing.T) {
	m := middleware.New(middleware.Deps{})

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	router.GET("/protected", m.Auth(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestCan_Unauthenticated 验证无认证身份访问受保护资源时返回 401。
func TestCan_Unauthenticated(t *testing.T) {
	m, _, _, _, _ := setupAuthEnv()

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	router.GET("/users", m.Can("users", "read"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	var errResp errs.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, string(errs.CodeAuthTokenInvalid), errResp.Code)
}

// TestCan_EnforcerNotConfigured 验证未配置 Authz Enforcer 时触发 500 内部错误。
func TestCan_EnforcerNotConfigured(t *testing.T) {
	m := middleware.New(middleware.Deps{
		Authz: nil,
	})

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	router.GET("/users", func(c *gin.Context) {
		ctx := authentication.With(c.Request.Context(), authentication.Identity{
			UserID: "usr_admin_1",
			Role:   "admin",
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}, m.Can("users", "read"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestCan_RoleBasedAccess 验证基于 Casbin 角色策略的放行与拒绝。
func TestCan_RoleBasedAccess(t *testing.T) {
	m, _, _, _, _ := setupAuthEnv()

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	router.GET("/users", m.Can("users", "read"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// 1. 普通用户角色无权读取 users 资源 -> 403 Forbidden
	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	ctx := authentication.With(req.Context(), authentication.Identity{
		UserID: "usr_user_1",
		Role:   string(enums.RoleUser),
	})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	var errResp errs.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, string(errs.CodeForbidden), errResp.Code)

	// 2. 管理员角色有权读取 users 资源 -> 200 OK
	req = httptest.NewRequest(http.MethodGet, "/users", nil)
	ctx = authentication.With(req.Context(), authentication.Identity{
		UserID: "usr_admin_1",
		Role:   string(enums.RoleAdmin),
	})
	req = req.WithContext(ctx)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestAuth_MultiGuardFallback 验证 Laravel 风格多守卫回退匹配机制。
func TestAuth_MultiGuardFallback(t *testing.T) {
	m, _, clientGuard, _, repo := setupAuthEnv()

	// 注册普通用户
	repo.users["usr_user_1"] = &sqlc.User{
		ID:     "usr_user_1",
		Status: enums.StatusActive,
		Role:   enums.RoleUser,
	}

	clientSess, err := clientGuard.Login(context.Background(), repo.users["usr_user_1"])
	require.NoError(t, err)

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	// 声明优先匹配 admin 守卫，回退至 client 守卫
	router.GET("/hybrid", m.Auth("admin", "client"), func(c *gin.Context) {
		id, ok := authentication.FromContext(c.Request.Context())
		if !ok {
			c.Status(http.StatusUnauthorized)
			return
		}
		c.JSON(http.StatusOK, gin.H{"userId": id.UserID})
	})

	// 携带 client 令牌请求，admin 守卫校验失败后自动回退至 client 守卫成功认证
	req := httptest.NewRequest(http.MethodGet, "/hybrid", nil)
	req.Header.Set("Authorization", "Bearer "+clientSess.Token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "usr_user_1", resp["userId"])
}

// TestCan_Permission 验证 Laravel 风格 m.Can 简写细粒度权限校验。
func TestCan_Permission(t *testing.T) {
	m, _, _, _, _ := setupAuthEnv()

	router := gin.New()
	router.Use(global.New(global.Deps{}).ErrorHandler())
	router.GET("/users", m.Can("users", "read"), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	ctx := authentication.With(req.Context(), authentication.Identity{
		UserID: "usr_admin_1",
		Role:   string(enums.RoleAdmin),
	})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}
