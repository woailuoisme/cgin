package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/middleware"
	"cgin/pkg/authorization"
)

// TestAuthzMiddleware_RESTfulRules 验证 gin-contrib/authz 基于角色、路径与 HTTP 方法执行 RESTful 访问控制。
func TestAuthzMiddleware_RESTfulRules(t *testing.T) {
	gin.SetMode(gin.TestMode)

	enforcer, err := authorization.New()
	require.NoError(t, err)

	mw := middleware.New(middleware.Deps{
		Authz: enforcer,
	})

	router := gin.New()

	// 认证由测试请求直接携带 BasicAuth（gin-contrib/authz 从中提取 subject）。
	adminGroup := router.Group("/v1/admin", mw.Authz())
	{
		adminGroup.GET("/users", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "admin users list"})
		})
	}

	clientGroup := router.Group("/v1", mw.Authz())
	{
		clientGroup.GET("/users/me", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "client profile"})
		})
	}

	// 1. admin 角色访问 /v1/admin/users：允许
	reqAdmin := httptest.NewRequest(http.MethodGet, "/v1/admin/users", nil)
	wAdmin := httptest.NewRecorder()
	router.ServeHTTP(wAdmin, func() *http.Request {
		r := reqAdmin.Clone(reqAdmin.Context())
		r.SetBasicAuth("admin", "")
		return r
	}())
	assert.Equal(t, http.StatusOK, wAdmin.Code)

	// 2. client 角色访问 /v1/admin/users：禁止（403）
	reqClientForbidden := httptest.NewRequest(http.MethodGet, "/v1/admin/users", nil)
	wClientForbidden := httptest.NewRecorder()
	router.ServeHTTP(wClientForbidden, func() *http.Request {
		r := reqClientForbidden.Clone(reqClientForbidden.Context())
		r.SetBasicAuth("client", "")
		return r
	}())
	assert.Equal(t, http.StatusForbidden, wClientForbidden.Code)

	// 3. client 角色访问 /v1/users/me：允许
	reqClientProfile := httptest.NewRequest(http.MethodGet, "/v1/users/me", nil)
	wClientProfile := httptest.NewRecorder()
	router.ServeHTTP(wClientProfile, func() *http.Request {
		r := reqClientProfile.Clone(reqClientProfile.Context())
		r.SetBasicAuth("client", "")
		return r
	}())
	assert.Equal(t, http.StatusOK, wClientProfile.Code)

	// 4. 未认证用户（匿名）访问 /v1/admin/users：禁止（403）
	reqAnon := httptest.NewRequest(http.MethodGet, "/v1/admin/users", nil)
	wAnon := httptest.NewRecorder()
	router.ServeHTTP(wAnon, reqAnon)
	assert.Equal(t, http.StatusForbidden, wAnon.Code)
}
