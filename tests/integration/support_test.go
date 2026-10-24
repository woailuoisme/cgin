//go:build integration

// Package integration_test 驱动真实数据库与真实路由引擎，验证端到端契约。
// 运行方式：TEST_DATABASE_URL=... go test -tags=integration ./tests/integration/...
package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"bgin/internal/bootstrap"
	"bgin/internal/config"
	"bgin/internal/database"
)

func init() { gin.SetMode(gin.TestMode) }

// reusedApp 让同一进程内的多个用例共用一套装配好的依赖，避免反复建连接池。
var reusedApp *bootstrap.Application

// appStorageRoot 是整套集成测试共用的存储根目录，由 TestMain 创建与回收。
//
// reusedApp 是进程级单例，若把它绑到某个用例的 t.TempDir()，该用例一结束目录就被删除，
// 而后面的用例仍会复用它，最终表现为"文件写入成功但读取 404"这类难查的偶发失败。
var appStorageRoot string

// TestMain 准备进程级共享资源并在退出前回收。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bgin-integration-storage-")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to create integration storage root: %v\n", err)
		os.Exit(1)
	}

	appStorageRoot = dir
	code := m.Run()
	_ = os.RemoveAll(dir)

	os.Exit(code)
}

// newEngine 装配真实应用并返回路由引擎。
// TEST_DATABASE_URL 存在时指向测试库，否则沿用 .env 里的库。
func newEngine(t *testing.T) *gin.Engine {
	t.Helper()

	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		t.Setenv("DB_URL", dsn)
	}
	// 集成测试会在短时间内打很多请求，把配额抬高避免测试自身触发 429。
	t.Setenv("RATELIMIT_SENSITIVE", "100000")
	t.Setenv("RATELIMIT_AUTHENTICATED", "100000")
	t.Setenv("RATELIMIT_ANONYMOUS", "100000")
	t.Setenv("APP_ENV", "test")
	// 本地存储驱动默认写到工作目录下的 ./storage/app/uploads；测试改到进程级临时目录，
	// 免得在仓库里留下运行时产物（详见 appStorageRoot 的说明）。
	t.Setenv("STORAGE_LOCAL_ROOT", appStorageRoot)

	if reusedApp == nil {
		application, err := bootstrap.NewApplication(t.Context(), 0)
		require.NoError(t, err, "装配应用失败，请确认 DB_URL / REDIS_URL 可达且已应用迁移")
		reusedApp = application
	}
	return reusedApp.Engine()
}

// openTestDB 直接打开数据库，供需要绕过仓储层的用例使用。
func openTestDB(t *testing.T) *bun.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DB_URL")
	}
	require.NotEmpty(t, dsn, "需要 TEST_DATABASE_URL 或 DB_URL")

	// 池参数沿用应用默认值，只把上限调小：集成测试的并发远低于生产，
	// 没必要占满共享 Postgres 的连接预算。寿命与超时用默认值，避免测试与线上两套参数。
	cfg, err := config.Load()
	require.NoError(t, err, "加载配置失败")

	cfg.Database.URL = dsn
	cfg.Database.MaxOpenConns = 4
	cfg.Database.MaxIdleConns = 4

	db, err := database.Open(t.Context(), cfg.Database, "bgin-tests")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return db
}

// response 是一次 HTTP 调用的结果。
type response struct {
	Status int
	Header http.Header
	Body   map[string]any
	Raw    string
}

// call 用真实引擎发起一次请求。token 为空时不带 Authorization 头。
func call(t *testing.T, engine *gin.Engine, method, path, body, token string) response {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	result := response{
		Status: recorder.Code,
		Header: recorder.Header(),
		Raw:    recorder.Body.String(),
	}
	if recorder.Body.Len() > 0 {
		_ = json.Unmarshal(recorder.Body.Bytes(), &result.Body)
	}
	return result
}

// callRawAuth 用原始 Authorization 头发起请求，用于覆盖"缺少 Bearer 前缀"这类畸形头分支。
// call 会自动补上 Bearer 前缀，无法表达该场景。
func callRawAuth(t *testing.T, engine *gin.Engine, method, path, authorization string) response {
	t.Helper()

	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", authorization)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	result := response{
		Status: recorder.Code,
		Header: recorder.Header(),
		Raw:    recorder.Body.String(),
	}
	if recorder.Body.Len() > 0 {
		_ = json.Unmarshal(recorder.Body.Bytes(), &result.Body)
	}
	return result
}

// callJSON 与 call 相同，但接受任意结构体作为请求体。
func callJSON(t *testing.T, engine *gin.Engine, method, path string, payload any, token string) response {
	t.Helper()

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	return call(t, engine, method, path, bytes.NewBuffer(encoded).String(), token)
}

// str 从响应体里取字符串字段，缺失时返回空串。
func str(body map[string]any, key string) string {
	value, _ := body[key].(string)
	return value
}

// nested 从响应体里取嵌套对象。
func nested(body map[string]any, key string) map[string]any {
	value, _ := body[key].(map[string]any)
	return value
}

// num 从响应体里取数值字段。JSON 数字一律解成 float64，这里统一转成 int 便于断言。
func num(body map[string]any, key string) int {
	value, ok := body[key].(float64)
	if !ok {
		return -1
	}
	return int(value)
}

// obj 从数组元素里取对象，类型不符时返回空 map 而不是 panic。
func obj(value any) map[string]any {
	item, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return item
}

// errorMessages 取出 422 响应里某个字段的错误文案列表。
func errorMessages(body map[string]any, field string) []string {
	raw, ok := body["errors"].(map[string]any)
	if !ok {
		return nil
	}
	items, ok := raw[field].([]any)
	if !ok {
		return nil
	}

	messages := make([]string, 0, len(items))
	for _, item := range items {
		if message, ok := item.(string); ok {
			messages = append(messages, message)
		}
	}
	return messages
}

// uniqueEmail 生成不会与既有数据冲突的邮箱。
func uniqueEmail() string {
	return fmt.Sprintf("it+%d@bgin.local", time.Now().UnixNano())
}

// uniqueUsername 生成符合字符集规范的唯一用户名。
func uniqueUsername() string {
	return fmt.Sprintf("it_%d", time.Now().UnixNano())
}

// signUp 注册一个全新账号并返回令牌与标识。
func signUp(t *testing.T, engine *gin.Engine) (token, refreshToken, userID, email string) {
	t.Helper()

	email = uniqueEmail()
	result := callJSON(t, engine, http.MethodPost, "/v1/auth/sign-up/email", map[string]any{
		"email":    email,
		"password": "secret12345",
		"name":     "Integration Tester",
		"username": uniqueUsername(),
	}, "")
	require.Equal(t, http.StatusCreated, result.Status, "注册失败: %s", result.Raw)

	return str(result.Body, "token"), str(result.Body, "refreshToken"), str(nested(result.Body, "user"), "id"), email
}
