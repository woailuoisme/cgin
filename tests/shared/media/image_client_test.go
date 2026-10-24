// Package media_test 验证 imgproxy 客户端的对外 URL 与内部探测地址分离。
package media_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/internal/shared/media"
)

// TestImageClientSeparatesPublicAndInternalURL 验证：
// 签名 URL 使用面向浏览器的 BaseURL，健康探测使用容器内部的 InternalURL。
func TestImageClientSeparatesPublicAndInternalURL(t *testing.T) {
	probes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes++
		assert.Equal(t, "/health", r.URL.Path)
		_, _ = w.Write([]byte("imgproxy is running"))
	}))
	t.Cleanup(server.Close)

	client := media.NewImageClient(config.ImgproxyConfig{
		Enabled:     true,
		BaseURL:     "https://img.example.com/",
		InternalURL: server.URL + "/",
		Insecure:    true,
	})

	url, err := client.URL("local://avatars/2026/10/a.png", media.WithResize(64, 64))
	require.NoError(t, err)
	assert.Contains(t, url, "https://img.example.com/insecure/", "签名 URL 必须走对外地址")
	assert.NotContains(t, url, server.URL, "签名 URL 不得暴露内部探测地址")

	require.NoError(t, client.CheckHealth(t.Context()))
	assert.Equal(t, 1, probes, "健康探测应命中内部地址")
}

// TestImageClientFallsBackToBaseURL 验证未配置 InternalURL 时回落 BaseURL。
func TestImageClientFallsBackToBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("imgproxy is running"))
	}))
	t.Cleanup(server.Close)

	client := media.NewImageClient(config.ImgproxyConfig{
		Enabled:  true,
		BaseURL:  server.URL,
		Insecure: true,
	})

	require.NoError(t, client.CheckHealth(t.Context()))
}
