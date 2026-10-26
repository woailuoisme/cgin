// Package geoip_test 验证离线 IP 归属地解析与安全降级服务契约。
package geoip_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"cgin/internal/config"
	"cgin/pkg/geoip"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestNoopService(t *testing.T) {
	svc := geoip.NewNoop()
	require.NotNil(t, svc)

	ctx := t.Context()

	// 1. 环回与局域网私有地址识别
	loc, err := svc.Lookup(ctx, "127.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, "Internal IP", loc.Country)
	assert.Equal(t, "Internal IP", loc.City)

	loc, err = svc.Lookup(ctx, "192.168.1.100")
	require.NoError(t, err)
	assert.Equal(t, "Internal IP", loc.Country)

	loc, err = svc.Lookup(ctx, "::1")
	require.NoError(t, err)
	assert.Equal(t, "Internal IP", loc.Country)

	// 2. IPv6 地址识别
	loc, err = svc.Lookup(ctx, "2400:3200::1")
	require.NoError(t, err)
	assert.Equal(t, "IPv6", loc.Country)

	// 3. 公网 IPv4 在无数据库时降级为 Unknown
	loc, err = svc.Lookup(ctx, "8.8.8.8")
	require.NoError(t, err)
	assert.Equal(t, "Unknown", loc.Country)

	// 4. 非法 IP 格式返回错误
	_, err = svc.Lookup(ctx, "not-an-ip")
	require.Error(t, err)

	// 5. 零堆分配快速路径 LookupAddr
	addr := netip.MustParseAddr("10.0.0.1")
	loc, err = svc.LookupAddr(ctx, addr)
	require.NoError(t, err)
	assert.Equal(t, "Internal IP", loc.Country)

	loc, err = svc.LookupAddr(ctx, addr)
	require.NoError(t, err)
	assert.Equal(t, "Internal IP", loc.Country)

	// 6. Summary 与 SummaryAddr
	assert.Equal(t, "Internal IP", svc.Summary(ctx, "127.0.0.1"))
	assert.Equal(t, "Internal IP", svc.SummaryAddr(ctx, addr))
	assert.Equal(t, "Unknown", svc.Summary(ctx, "invalid"))

	// 7. Reload 与 Close / Shutdown 空操作安全调用
	require.NoError(t, svc.Reload())
	if shutdowner, ok := svc.(interface{ Shutdown() error }); ok {
		require.NoError(t, shutdowner.Shutdown())
	}
	svc.Close()
}

func TestLocationFormatAndParseRaw(t *testing.T) {
	raw := "中国|0|上海|上海市|电信"
	loc := geoip.ParseRaw(raw)

	assert.Equal(t, "中国", loc.Country)
	assert.Empty(t, loc.Region)
	assert.Equal(t, "上海", loc.Province)
	assert.Equal(t, "上海市", loc.City)
	assert.Equal(t, "电信", loc.ISP)
	assert.Equal(t, raw, loc.Raw)

	// 默认拼接（空值省略、相邻重复去重）
	assert.Equal(t, "中国·上海·上海市·电信", loc.Format())

	// 全部为空时回退 Unknown
	assert.Equal(t, "Unknown", geoip.ParseRaw("0|0|0|0|0").Format())
}

func TestNewServiceFallback(t *testing.T) {
	// 1. 未开启时返回降级实例
	svc, err := geoip.NewService(config.GeoIPConfig{
		Enabled: false,
	}, nil)
	require.NoError(t, err)
	assert.NotNil(t, svc)

	// 2. 开启但数据库文件不存在时安全降级
	svc, err = geoip.NewService(config.GeoIPConfig{
		Enabled: true,
		DBPath:  "/nonexistent/ip2region.xdb",
	}, nil)
	require.NoError(t, err)
	assert.NotNil(t, svc)
	assert.Equal(t, "Internal IP", svc.Summary(t.Context(), "127.0.0.1"))
}

// mockProvider 用于单元测试模拟自定义物理地理定位提供者。
type mockProvider struct {
	called bool
}

func (m *mockProvider) SearchAddr(_ context.Context, _ netip.Addr) (*geoip.Location, error) {
	m.called = true
	return &geoip.Location{
		Country:  "United States",
		Province: "California",
		City:     "San Francisco",
		ISP:      "Cloudflare",
	}, nil
}

func (m *mockProvider) Close() error {
	return nil
}

func TestWithCustomProviderAndContext(t *testing.T) {
	mock := &mockProvider{}
	svc := geoip.NewWithProvider(mock)
	require.NotNil(t, svc)

	ctx := t.Context()
	loc, err := svc.Lookup(ctx, "1.1.1.1")
	require.NoError(t, err)
	assert.True(t, mock.called)
	assert.Equal(t, "United States", loc.Country)
	assert.Equal(t, "San Francisco", loc.City)
	assert.Equal(t, "United States·California·San Francisco·Cloudflare", svc.Summary(ctx, "1.1.1.1"))

	// 局域网地址不经过底层 Provider
	mock.called = false
	locInternal, err := svc.Lookup(ctx, "192.168.1.1")
	require.NoError(t, err)
	assert.False(t, mock.called)
	assert.Equal(t, "Internal IP", locInternal.Country)

	svc.Close()
}

func BenchmarkNoopService_LookupAddr(b *testing.B) {
	svc := geoip.NewNoop()
	defer svc.Close()

	ctx := context.Background()
	addr := netip.MustParseAddr("127.0.0.1")

	b.ReportAllocs()
	for b.Loop() {
		_, _ = svc.LookupAddr(ctx, addr)
	}
}

func BenchmarkNoopService_Lookup(b *testing.B) {
	svc := geoip.NewNoop()
	defer svc.Close()

	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		_, _ = svc.Lookup(ctx, "127.0.0.1")
	}
}

func BenchmarkNoopService_Summary(b *testing.B) {
	svc := geoip.NewNoop()
	defer svc.Close()

	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		_ = svc.Summary(ctx, "127.0.0.1")
	}
}
