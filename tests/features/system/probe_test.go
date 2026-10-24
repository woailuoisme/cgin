// 本文件验证两个探针的分工与边界：存活探针不碰依赖；就绪探针并发探测、关键依赖失败给 503、非关键依赖失败给 200 degraded；
// 探测失败的细节只进日志，绝不进响应体。
package system_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/features/system"
	"bgin/internal/platform/logger"
)

// errInternal 模拟底层驱动的真实报错：它带着目标地址与凭据，这类内容绝不能出现在响应体里。
var errInternal = errors.New(
	`dial tcp 10.0.0.5:5432: connect: connection refused (user=app password=hunter2)`)

// stubPinger 是可控的探测替身。
type stubPinger struct {
	err error
	// seenDeadline / seenBudget 记录调用时 ctx 是否带截止时间与剩余预算，
	// 用于验证"探针必须带超时"这条策略确实落到了探测上。
	seenDeadline *bool
	seenBudget   *time.Duration
}

func (s stubPinger) Ping(ctx context.Context) error {
	if s.seenDeadline != nil {
		deadline, ok := ctx.Deadline()
		*s.seenDeadline = ok
		if ok {
			*s.seenBudget = time.Until(deadline)
		}
	}
	return s.err
}

// newProbe 按给定的探测结果装配探针路由，回传引擎与日志缓冲。
func newProbe(t *testing.T, database, cache system.Pinger) (*gin.Engine, *bytes.Buffer) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	logs := &bytes.Buffer{}
	l := logger.NewWithOptions(
		logger.WithLevel("debug"),
		logger.WithChannel("json"),
		logger.WithWriter(logs),
	)

	engine := gin.New()
	system.RegisterRoutes(engine, system.NewHandler(system.NewService(system.Deps{
		Database: database,
		Cache:    cache,
	}), l))
	return engine, logs
}

// get 驱动一次请求，回传状态码与原始响应体。
func get(t *testing.T, engine *gin.Engine, path string) (int, string) {
	t.Helper()

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder.Code, recorder.Body.String()
}

// TestLivenessIgnoresDependencies 锁定存活探针的边界：依赖全挂也必须返回 200。
// 它一旦去查依赖，"数据库抖动 → 容器被反复重启" 就会真实发生。
func TestLivenessIgnoresDependencies(t *testing.T) {
	engine, logs := newProbe(t, stubPinger{err: errInternal}, stubPinger{err: errInternal})

	status, body := get(t, engine, "/livez")
	assert.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `{"status":"ok"}`, body)
	assert.Empty(t, logs.String(), "存活探针不该产生任何探测日志")

	// 校验已彻底移除的 /healthz 返回 404
	oldStatus, _ := get(t, engine, "/healthz")
	assert.Equal(t, http.StatusNotFound, oldStatus)
}

// TestRootEndpointReturnsOK 验证根路径 GET / 返回 200 与 ok 状态。
func TestRootEndpointReturnsOK(t *testing.T) {
	engine, _ := newProbe(t, stubPinger{}, stubPinger{})

	status, body := get(t, engine, "/")
	assert.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `{"status":"ok"}`, body)
}

// TestReadinessAllHealthy 覆盖全通路径。
func TestReadinessAllHealthy(t *testing.T) {
	engine, _ := newProbe(t, stubPinger{}, stubPinger{})

	status, body := get(t, engine, "/readyz")
	require.Equal(t, http.StatusOK, status)

	var got system.ReadinessResponse
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, system.StatusReady, got.Status)
	assert.Equal(t, system.CheckHealthy, got.Checks[system.ComponentDatabase].Status)
	assert.Equal(t, "postgres", got.Checks[system.ComponentDatabase].Driver)
	assert.True(t, got.Checks[system.ComponentDatabase].Critical)
	assert.Equal(t, system.CheckHealthy, got.Checks[system.ComponentCache].Status)
	assert.Equal(t, "valkey", got.Checks[system.ComponentCache].Driver)
	assert.False(t, got.Checks[system.ComponentCache].Critical)
}

// TestReadinessReportsUnreachableDependency 覆盖关键项失败 503 路径：状态码、状态词与逐组件明细。
func TestReadinessReportsUnreachableDependency(t *testing.T) {
	engine, _ := newProbe(t, stubPinger{err: errInternal}, stubPinger{})

	status, body := get(t, engine, "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, status)

	var got system.ReadinessResponse
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, system.StatusNotReady, got.Status)
	assert.Equal(t, system.CheckUnreachable, got.Checks[system.ComponentDatabase].Status)
	assert.Equal(t, system.CheckHealthy, got.Checks[system.ComponentCache].Status)
}

// TestReadinessDegradedWhenNonCriticalFails 验证非关键项（如缓存）失败时降级返回 200 与 degraded 状态。
func TestReadinessDegradedWhenNonCriticalFails(t *testing.T) {
	engine, logs := newProbe(t, stubPinger{}, stubPinger{err: errors.New("valkey cluster partition")})

	status, body := get(t, engine, "/readyz")
	require.Equal(t, http.StatusOK, status)

	var got system.ReadinessResponse
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, system.StatusDegraded, got.Status)
	assert.Equal(t, system.CheckHealthy, got.Checks[system.ComponentDatabase].Status)
	assert.Equal(t, system.CheckDegraded, got.Checks[system.ComponentCache].Status)

	assert.Contains(t, logs.String(), "valkey cluster partition")
	assert.Contains(t, logs.String(), system.ComponentCache)
}

// TestReadinessKeepsErrorDetailOutOfResponse 是本组里最要紧的一条：底层错误常含目标地址与
// 凭据，它必须只出现在日志里。gin 官方文档的多依赖示例把 err.Error() 直接写进响应体，
// 这里刻意不照抄——响应体只给固定词汇，排查信息留给日志。
func TestReadinessKeepsErrorDetailOutOfResponse(t *testing.T) {
	engine, logs := newProbe(t, stubPinger{err: errInternal}, stubPinger{})

	_, body := get(t, engine, "/readyz")

	for _, leak := range []string{"10.0.0.5", "hunter2", "connection refused"} {
		assert.NotContains(t, body, leak, "响应体不得带出底层错误细节")
	}

	assert.Contains(t, logs.String(), "10.0.0.5", "但细节必须留在日志里，否则无从排查")
	assert.Contains(t, logs.String(), system.ComponentDatabase, "日志要能指出是哪个组件")
}

// TestReadinessAppliesTimeout 锁定"探针必须带超时"：探测收到的 ctx 必须有截止时间，
// 且预算不超过 handler 声明的上限。挂住的连接不该把探针一起拖死。
func TestReadinessAppliesTimeout(t *testing.T) {
	var (
		hasDeadline bool
		budget      time.Duration
	)
	engine, _ := newProbe(t,
		stubPinger{seenDeadline: &hasDeadline, seenBudget: &budget},
		stubPinger{},
	)

	_, _ = get(t, engine, "/readyz")

	assert.True(t, hasDeadline, "探测必须拿到带截止时间的 ctx")
	assert.Positive(t, budget)
	assert.LessOrEqual(t, budget, 2*time.Second)
}

// TestReadinessUnconfiguredDependency 覆盖"依赖没接进来"：应当报未就绪，而不是 panic。
func TestReadinessUnconfiguredDependency(t *testing.T) {
	engine, _ := newProbe(t, nil, stubPinger{})

	status, body := get(t, engine, "/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Contains(t, body, system.CheckUnreachable)
}

// stubComponent 模拟实现 Driver 和 Enabled 契约的基础设施组件。
type stubComponent struct {
	driver  string
	enabled bool
	err     error
}

func (s stubComponent) Ping(ctx context.Context) error {
	return s.err
}

func (s stubComponent) Driver() string {
	return s.driver
}

func (s stubComponent) Enabled() bool {
	return s.enabled
}

// TestReadinessWithExtendedComponents 验证 storage(garage), search(meilisearch), realtime(centrifugo), imgproxy 接入。
func TestReadinessWithExtendedComponents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logs := &bytes.Buffer{}
	l := logger.NewWithOptions(
		logger.WithLevel("debug"),
		logger.WithChannel("json"),
		logger.WithWriter(logs),
	)

	engine := gin.New()
	svc := system.NewService(system.Deps{
		Database: stubPinger{},
		Cache:    stubPinger{},
		Storage:  stubComponent{driver: "garage", enabled: true},
		Search:   stubComponent{driver: "meilisearch", enabled: true},
		Realtime: stubComponent{driver: "centrifugo", enabled: false},
		Imgproxy: stubComponent{driver: "imgproxy", enabled: false},
		Mailer:   stubComponent{driver: "mailpit", enabled: true},
		Temporal: stubComponent{driver: "temporal", enabled: false},
	})
	system.RegisterRoutes(engine, system.NewHandler(svc, l))

	status, body := get(t, engine, "/readyz")
	require.Equal(t, http.StatusOK, status)

	var got system.ReadinessResponse
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, system.StatusReady, got.Status)

	// storage (garage)
	storageCheck, ok := got.Checks[system.ComponentStorage]
	require.True(t, ok)
	assert.Equal(t, system.CheckHealthy, storageCheck.Status)
	assert.Equal(t, "garage", storageCheck.Driver)
	assert.False(t, storageCheck.Critical)

	// search (meilisearch)
	searchCheck, ok := got.Checks[system.ComponentSearch]
	require.True(t, ok)
	assert.Equal(t, system.CheckHealthy, searchCheck.Status)
	assert.Equal(t, "meilisearch", searchCheck.Driver)
	assert.False(t, searchCheck.Critical)

	// realtime (centrifugo, disabled)
	realtimeCheck, ok := got.Checks[system.ComponentRealtime]
	require.True(t, ok)
	assert.Equal(t, system.CheckDisabled, realtimeCheck.Status)
	assert.Equal(t, "centrifugo", realtimeCheck.Driver)
	assert.False(t, realtimeCheck.Critical)

	// imgproxy (disabled)
	imgproxyCheck, ok := got.Checks[system.ComponentImgproxy]
	require.True(t, ok)
	assert.Equal(t, system.CheckDisabled, imgproxyCheck.Status)
	assert.Equal(t, "imgproxy", imgproxyCheck.Driver)
	assert.False(t, imgproxyCheck.Critical)

	// mailer (mailpit, enabled)
	mailerCheck, ok := got.Checks[system.ComponentMailer]
	require.True(t, ok)
	assert.Equal(t, system.CheckHealthy, mailerCheck.Status)
	assert.Equal(t, "mailpit", mailerCheck.Driver)
	assert.False(t, mailerCheck.Critical)

	// temporal (disabled)
	temporalCheck, ok := got.Checks[system.ComponentTemporal]
	require.True(t, ok)
	assert.Equal(t, system.CheckDisabled, temporalCheck.Status)
	assert.Equal(t, "temporal", temporalCheck.Driver)
	assert.False(t, temporalCheck.Critical)
}

// TestReadinessDegradedWhenExtendedComponentFails 验证启用的扩展组件故障时优雅降级为 degraded。
func TestReadinessDegradedWhenExtendedComponentFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logs := &bytes.Buffer{}
	l := logger.NewWithOptions(
		logger.WithLevel("debug"),
		logger.WithChannel("json"),
		logger.WithWriter(logs),
	)

	engine := gin.New()
	svc := system.NewService(system.Deps{
		Database: stubPinger{},
		Cache:    stubPinger{},
		Storage:  stubComponent{driver: "garage", enabled: true},
		Imgproxy: stubComponent{driver: "imgproxy", enabled: true, err: errors.New("imgproxy connection refused")},
	})
	system.RegisterRoutes(engine, system.NewHandler(svc, l))

	status, body := get(t, engine, "/readyz")
	require.Equal(t, http.StatusOK, status)

	var got system.ReadinessResponse
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, system.StatusDegraded, got.Status)

	imgCheck := got.Checks[system.ComponentImgproxy]
	assert.Equal(t, system.CheckDegraded, imgCheck.Status)
	assert.Equal(t, "imgproxy", imgCheck.Driver)
	assert.Contains(t, logs.String(), "imgproxy connection refused")
}

// TestReadinessDegradedWhenTemporalFails 验证启用的 Temporal 组件故障时优雅降级为 degraded。
func TestReadinessDegradedWhenTemporalFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logs := &bytes.Buffer{}
	l := logger.NewWithOptions(
		logger.WithLevel("debug"),
		logger.WithChannel("json"),
		logger.WithWriter(logs),
	)

	engine := gin.New()
	svc := system.NewService(system.Deps{
		Database: stubPinger{},
		Cache:    stubPinger{},
		Temporal: stubComponent{driver: "temporal", enabled: true, err: errors.New("temporal connection refused")},
	})
	system.RegisterRoutes(engine, system.NewHandler(svc, l))

	status, body := get(t, engine, "/readyz")
	require.Equal(t, http.StatusOK, status)

	var got system.ReadinessResponse
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, system.StatusDegraded, got.Status)

	temporalCheck := got.Checks[system.ComponentTemporal]
	assert.Equal(t, system.CheckDegraded, temporalCheck.Status)
	assert.Equal(t, "temporal", temporalCheck.Driver)
	assert.Contains(t, logs.String(), "temporal connection refused")
}
