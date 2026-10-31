// Package route_test 验证前端自助档案模块的对外契约（Q27）。
package route_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/dto"
	"cgin/internal/enum"
	"cgin/internal/handler"
	"cgin/internal/service"
	"cgin/internal/user"
	"cgin/pkg/authentication"
	"cgin/pkg/errs"
	"cgin/pkg/validator"
)

// TestMain 安装校验器：本包通过 gin 绑定驱动 JSON。
func TestMain(m *testing.M) {
	v, err := validator.New()
	if err != nil {
		panic(err)
	}
	v.Install()

	os.Exit(m.Run())
}

// TestUpdateProfileRequestShape 校验自助更新只暴露 Q27 允许的字段。
func TestUpdateProfileRequestShape(t *testing.T) {
	var request dto.UpdateProfileBody
	require.NoError(t, json.Unmarshal([]byte(`{
		"name":"Alice",
		"avatar_path":null,
		"username":"alice_01",
		"displayUsername":"Alice",
		"phoneNumber":"+8613800138000",
		"email":"should-be-ignored@cgin.local",
		"role":"admin"
	}`), &request))

	// 邮箱与角色不在自助契约里：JSON 里出现也不会被解析进结构体。
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "email")
	assert.NotContains(t, string(encoded), "role")

	assert.True(t, request.Name.IsValue())
	assert.True(t, request.AvatarPath.IsNull())
	assert.True(t, request.Username.IsValue())
	assert.True(t, request.DisplayUsername.IsValue())
	assert.True(t, request.PhoneNumber.IsValue())
}

// TestUploadAvatarValidation 校验上传头像的权限要求、字段提取与存在性校验。
func TestUploadAvatarValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	storage := &fakeStorage{
		files: map[string]bool{
			"uploads/avatars/valid.png": true,
		},
	}
	users := &fakeUsers{profile: user.Profile{}}
	h := handler.NewProfileHandler(service.MustNewProfileService(service.ProfileDeps{Users: users, Storage: storage}))

	newTestEngine := func(withAuth bool) *gin.Engine {
		engine := gin.New()
		engine.Use(func(c *gin.Context) {
			if withAuth {
				c.Request = c.Request.WithContext(authentication.With(c.Request.Context(), authentication.Identity{
					UserID: "user_test_avatar_1",
					Role:   string(enum.RoleUser),
				}))
			}
			c.Next()
			if len(c.Errors) > 0 {
				errs.Render(c, c.Errors.Last().Err)
			}
		})
		engine.POST("/v1/users/me/avatar", h.UploadAvatar)
		return engine
	}

	t.Run("未登录拒绝 401", func(t *testing.T) {
		engine := newTestEngine(false)
		req := createJSONRequest(http.MethodPost, "/v1/users/me/avatar", `{"avatar_path":"uploads/avatars/valid.png"}`)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("缺失 avatar_path 返回 422", func(t *testing.T) {
		engine := newTestEngine(true)
		req := createJSONRequest(http.MethodPost, "/v1/users/me/avatar", `{}`)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, rec.Body.String(), "avatar_path")
	})

	t.Run("存储中不存在的对象路径返回 400", func(t *testing.T) {
		engine := newTestEngine(true)
		req := createJSONRequest(http.MethodPost, "/v1/users/me/avatar", `{"avatar_path":"uploads/avatars/non_existent.png"}`)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "does not exist in storage")
	})

	t.Run("有效存储路径更新成功 200", func(t *testing.T) {
		engine := newTestEngine(true)
		req := createJSONRequest(http.MethodPost, "/v1/users/me/avatar", `{"avatar_path":"uploads/avatars/valid.png"}`)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.True(t, users.updated)
		assert.Equal(t, "uploads/avatars/valid.png", users.gotFields.AvatarPath.MustGet())
	})
}

// TestUploadAvatarWritesAvatarPath 校验合法头像会把相对 Key 写入 user.avatar_path，且作用域为客户端自助。
func TestUploadAvatarWritesAvatarPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	storage := &fakeStorage{
		files: map[string]bool{
			"uploads/avatars/new.png": true,
		},
	}
	oldPath := "uploads/avatars/old.png"
	users := &fakeUsers{
		profile: user.Profile{AvatarPath: &oldPath},
	}
	engine := newAvatarEngine(t, storage, users)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, createJSONRequest(http.MethodPost, "/v1/users/me/avatar", `{"avatar_path":"uploads/avatars/new.png"}`))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, users.updated)
	assert.Equal(t, user.ScopeClient, users.gotScope)

	avatarPath, ok := users.gotFields.AvatarPath.Get()
	require.True(t, ok)
	assert.Equal(t, "uploads/avatars/new.png", avatarPath)
	assert.Contains(t, storage.deleted, oldPath, "旧头像必须被异步清理")
}

// newAvatarEngine 构造带认证注入与统一错误渲染的头像上传引擎。
func newAvatarEngine(t *testing.T, storage *fakeStorage, users *fakeUsers) *gin.Engine {
	t.Helper()

	h := handler.NewProfileHandler(service.MustNewProfileService(service.ProfileDeps{Users: users, Storage: storage}))
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(authentication.With(c.Request.Context(), authentication.Identity{
			UserID: "user_test_avatar_1",
			Role:   string(enum.RoleUser),
		}))
		c.Next()
		if len(c.Errors) > 0 {
			errs.Render(c, c.Errors.Last().Err)
		}
	})
	engine.POST("/v1/users/me/avatar", h.UploadAvatar)
	return engine
}

// fakeStorage 模拟对象存储。
type fakeStorage struct {
	files   map[string]bool
	deleted []string
}

func (s *fakeStorage) Exists(_ context.Context, path string) (bool, error) {
	if s.files == nil {
		return false, nil
	}
	return s.files[path], nil
}

func (s *fakeStorage) Delete(_ context.Context, path string) error {
	s.deleted = append(s.deleted, path)
	delete(s.files, path)
	return nil
}

// fakeUsers 记录档案写入入参，便于断言作用域与字段。
type fakeUsers struct {
	profile   user.Profile
	err       error
	updated   bool
	gotFields user.FieldSet
	gotScope  user.Scope
}

func (f *fakeUsers) GetProfile(_ context.Context, _ string) (user.Profile, error) {
	return f.profile, f.err
}

func (f *fakeUsers) UpdateProfile(
	_ context.Context,
	_ string,
	fields user.FieldSet,
	scope user.Scope,
) (user.Profile, error) {
	f.updated = true
	f.gotFields = fields
	f.gotScope = scope
	if f.err != nil {
		return user.Profile{}, f.err
	}
	return f.profile, nil
}

// createJSONRequest 构造 JSON 测试请求。
func createJSONRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}
