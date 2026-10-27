// Package route_test 验证前端自助档案模块的对外契约（Q27）。
package route_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/dto"
	"cgin/internal/enum"
	"cgin/internal/handler"
	"cgin/internal/media"
	"cgin/internal/service"
	"cgin/internal/user"
	"cgin/pkg/authentication"
	"cgin/pkg/errs"
	"cgin/pkg/validator"
)

// TestMain 安装校验器：本包通过 gin 绑定驱动多部分表单与 JSON。
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
		"image":null,
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
	assert.True(t, request.Image.IsNull())
	assert.True(t, request.Username.IsValue())
	assert.True(t, request.DisplayUsername.IsValue())
	assert.True(t, request.PhoneNumber.IsValue())
}

// TestUploadAvatarValidation 校验上传头像的权限要求、字段提取与魔数格式防御。
func TestUploadAvatarValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	avatars := &fakeAvatars{}
	users := &fakeUsers{profile: user.Profile{}}
	handler := handler.NewProfileHandler(service.MustNewProfileService(service.ProfileDeps{Users: users, Avatars: avatars}))

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
		engine.POST("/v1/users/me/avatar", handler.UploadAvatar)
		return engine
	}

	t.Run("未登录拒绝 401", func(t *testing.T) {
		engine := newTestEngine(false)
		req := createMultipartRequest(t, "avatar", "avatar.png", []byte("dummy"))
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("缺失文件返回 422", func(t *testing.T) {
		engine := newTestEngine(true)
		req := createMultipartRequest(t, "", "", nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, rec.Body.String(), "avatar")
	})

	t.Run("非法扩展名返回 422", func(t *testing.T) {
		engine := newTestEngine(true)
		req := createMultipartRequest(t, "avatar", "script.sh", []byte("#!/bin/sh\necho hack"))
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, rec.Body.String(), "avatar")
	})

	t.Run("超限尺寸 (大于 2MB) 返回 422", func(t *testing.T) {
		engine := newTestEngine(true)
		oversized := make([]byte, 2*1024*1024+16)
		req := createMultipartRequest(t, "avatar", "huge.png", oversized)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, rec.Body.String(), "avatar")
	})

	t.Run("伪装图片 (文本伪装成 png) 魔数检测拦截 422", func(t *testing.T) {
		engine := newTestEngine(true)
		fakePNG := []byte("This is plain text pretending to be a png file")
		req := createMultipartRequest(t, "avatar", "fake.png", fakePNG)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, rec.Body.String(), "avatar")
	})

	t.Run("仅接受 avatar 字段，不匹配 file 字段返回 422", func(t *testing.T) {
		engine := newTestEngine(true)
		req := createMultipartRequest(t, "file", "fake.png", []byte("content"))
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		assert.Contains(t, rec.Body.String(), "avatar")
	})
}

// TestUploadAvatarWritesImageInsideTransaction 校验合法头像会把签名 URL 写入 user.image，且作用域为客户端自助。
func TestUploadAvatarWritesImageInsideTransaction(t *testing.T) {
	gin.SetMode(gin.TestMode)

	avatars := &fakeAvatars{}
	users := &fakeUsers{}
	engine := newAvatarEngine(t, avatars, users)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, createMultipartRequest(t, "avatar", "avatar.png", pngHeader))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 1, avatars.calls)
	assert.True(t, users.updated)
	assert.Equal(t, user.ScopeClient, users.gotScope)

	image, ok := users.gotFields.Image.Get()
	require.True(t, ok)
	assert.Equal(t, fakeAvatarURL, image)
}

// TestUploadAvatarAbortsWhenHostWriteFails 校验宿主回写失败时整笔头像替换失败，不返回成功档案。
func TestUploadAvatarAbortsWhenHostWriteFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	avatars := &fakeAvatars{}
	users := &fakeUsers{err: errs.Internal(errors.New("write user image failed"))}
	engine := newAvatarEngine(t, avatars, users)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, createMultipartRequest(t, "avatar", "avatar.png", pngHeader))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Error(t, avatars.linkErr, "宿主回写失败必须透传给媒体事务以触发回滚")
	assert.NotContains(t, rec.Body.String(), "write user image failed")
}

// newAvatarEngine 构造带认证注入与统一错误渲染的头像上传引擎。
func newAvatarEngine(t *testing.T, avatars *fakeAvatars, users *fakeUsers) *gin.Engine {
	t.Helper()

	handler := handler.NewProfileHandler(service.MustNewProfileService(service.ProfileDeps{Users: users, Avatars: avatars}))
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
	engine.POST("/v1/users/me/avatar", handler.UploadAvatar)
	return engine
}

// fakeAvatarURL 是 fake 媒体存储返回的固定签名地址。
const fakeAvatarURL = "https://cdn.example.com/avatar_256.png"

// pngHeader 是足以被魔数嗅探识别为 PNG 的最小文件头。
var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00")

// fakeAvatars 复刻生产实现的关键语义：先校验文件，再在同一事务内回写宿主。
type fakeAvatars struct {
	err     error
	calls   int
	linkErr error
}

func (f *fakeAvatars) ChangeAvatar(
	ctx context.Context,
	_ string,
	file *multipart.FileHeader,
	link func(ctx context.Context, url string) error,
) (string, error) {
	if err := media.ValidateAvatarFile(file); err != nil {
		return "", err
	}
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if link != nil {
		if err := link(ctx, fakeAvatarURL); err != nil {
			f.linkErr = err
			return "", err
		}
	}
	return fakeAvatarURL, nil
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

// createMultipartRequest 构造带有指定表单字段的 multipart/form-data 测试请求。
func createMultipartRequest(t *testing.T, fieldName, filename string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if fieldName != "" {
		part, err := writer.CreateFormFile(fieldName, filename)
		require.NoError(t, err)
		_, err = part.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/users/me/avatar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}
