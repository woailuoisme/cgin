// Package realtime_test 验证实时通信 feature 切片的 Token 签发与代理鉴权端点契约。
package realtime_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	featureRealtime "bgin/internal/features/realtime"
	"bgin/internal/platform/authentication"
	platformRealtime "bgin/internal/platform/realtime"
	"bgin/internal/shared/enums"
)

func generateTestSig(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// TestServiceIssueToken 验证服务层 Token 签发逻辑。
func TestServiceIssueToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("未启用时返回错误", func(t *testing.T) {
		cfg := &config.Config{
			Realtime: config.CentrifugoConfig{
				Enabled: false,
			},
		}
		gw := platformRealtime.NewManager(&cfg.Realtime)
		svc := featureRealtime.NewService(featureRealtime.Deps{Gateway: gw, Cfg: cfg, Secret: cfg.Realtime.Secret})

		_, err := svc.IssueToken(t.Context(), "u101", "user")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Realtime service is disabled")
	})

	t.Run("正常启用时成功生成 Token", func(t *testing.T) {
		cfg := &config.Config{
			Realtime: config.CentrifugoConfig{
				Enabled: true,
				URL:     "http://127.0.0.1:8000/api",
				Secret:  "test-secret-key-12345",
			},
		}
		gw := platformRealtime.NewManager(&cfg.Realtime)
		svc := featureRealtime.NewService(featureRealtime.Deps{Gateway: gw, Cfg: cfg, Secret: cfg.Realtime.Secret})

		res, err := svc.IssueToken(t.Context(), "u101", "admin")
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.NotEmpty(t, res.Token)
		assert.True(t, res.ExpiresAt.After(time.Now().UTC()))
		assert.Equal(t, "http://127.0.0.1:8000/api", res.WSURL)
	})
}

// TestServiceAuthorizeSubscribe 验证服务层频道订阅鉴权调度。
func TestServiceAuthorizeSubscribe(t *testing.T) {
	cfg := &config.Config{
		Realtime: config.CentrifugoConfig{
			Enabled: true,
			Secret:  "secret-123",
		},
	}
	reg := platformRealtime.NewRegistry()
	reg.Register("private:orders.{id}", func(_ context.Context, u platformRealtime.User, channel string, params map[string]string) error {
		if u.ID != "owner_1" {
			return errors.New("forbidden")
		}
		return nil
	})

	gw := platformRealtime.NewManager(&cfg.Realtime, platformRealtime.WithRegistry(reg))
	svc := featureRealtime.NewService(featureRealtime.Deps{Gateway: gw, Cfg: cfg, Secret: cfg.Realtime.Secret})

	t.Run("无权限时拒绝订阅", func(t *testing.T) {
		req := &platformRealtime.SubscribeRequest{
			User:    "stranger",
			Channel: "private:orders.888",
		}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		resp, err := svc.AuthorizeSubscribe(t.Context(), body, generateTestSig(cfg.Realtime.Secret, body))
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.NotNil(t, resp.Error)
		assert.Equal(t, platformRealtime.ProxyErrorCodePermissionDenied, resp.Error.Code)
	})

	t.Run("有权限时允许订阅", func(t *testing.T) {
		req := &platformRealtime.SubscribeRequest{
			User:    "owner_1",
			Channel: "private:orders.888",
		}
		body, err := json.Marshal(req)
		require.NoError(t, err)

		resp, err := svc.AuthorizeSubscribe(t.Context(), body, generateTestSig(cfg.Realtime.Secret, body))
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Nil(t, resp.Error)
		assert.NotNil(t, resp.Result)
	})
}

// TestHandlerHTTP 针对 Handler 执行全链路 HTTP 测试。
func TestHandlerHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "centrifugo-secret-hmac-test"

	cfg := &config.Config{
		Realtime: config.CentrifugoConfig{
			Enabled: true,
			URL:     "http://127.0.0.1:8000/api",
			Secret:  secret,
		},
	}

	reg := platformRealtime.NewRegistry()
	reg.Register("public:chat", func(context.Context, platformRealtime.User, string, map[string]string) error {
		return nil
	})

	gw := platformRealtime.NewManager(&cfg.Realtime, platformRealtime.WithRegistry(reg))
	svc := featureRealtime.NewService(featureRealtime.Deps{Gateway: gw, Cfg: cfg, Secret: cfg.Realtime.Secret})
	handler := featureRealtime.NewHandler(svc)

	router := gin.New()

	// 模拟已认证中间件
	router.GET("/v1/realtime/token", func(c *gin.Context) {
		ctx := authentication.With(c.Request.Context(), authentication.Identity{
			UserID: "user_777",
			Role:   string(enums.RoleUser),
		})
		c.Request = c.Request.WithContext(ctx)
		handler.GetToken(c)
	})

	router.POST("/v1/realtime/proxy/subscribe", handler.ProxySubscribe)

	t.Run("GET /v1/realtime/token 返回成功凭据", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/v1/realtime/token", nil)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var res featureRealtime.TokenResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
		assert.NotEmpty(t, res.Token)
		assert.Equal(t, cfg.Realtime.URL, res.WSURL)
	})

	t.Run("POST /v1/realtime/proxy/subscribe 签名错误拦截", func(t *testing.T) {
		w := httptest.NewRecorder()
		body := []byte(`{"user":"u1","channel":"public:chat"}`)
		req, _ := http.NewRequest(http.MethodPost, "/v1/realtime/proxy/subscribe", bytes.NewReader(body))
		req.Header.Set("X-Centrifugo-Signature", "wrong-signature")
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("POST /v1/realtime/proxy/subscribe 验签通过并允许订阅", func(t *testing.T) {
		w := httptest.NewRecorder()
		body := []byte(`{"user":"u1","channel":"public:chat"}`)
		sig := generateTestSig(secret, body)

		req, _ := http.NewRequest(http.MethodPost, "/v1/realtime/proxy/subscribe", bytes.NewReader(body))
		req.Header.Set("X-Centrifugo-Signature", sig)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp platformRealtime.SubscribeResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Nil(t, resp.Error)
		assert.NotNil(t, resp.Result)
	})
}
