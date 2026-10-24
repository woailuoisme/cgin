// Package profile_test 验证前端自助档案模块的对外契约（Q27）。
package profile_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/features/client/profile"
	"bgin/internal/platform/authentication"
	"bgin/internal/platform/errs"
	"bgin/internal/shared/enums"
)

// TestUpdateProfileRequestShape 校验自助更新只暴露 Q27 允许的字段。
func TestUpdateProfileRequestShape(t *testing.T) {
	var request profile.UpdateProfileRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"name":"Alice",
		"image":null,
		"username":"alice_01",
		"displayUsername":"Alice",
		"phoneNumber":"+8613800138000",
		"email":"should-be-ignored@bgin.local",
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

	handler := profile.NewHandler(nil, nil)

	newTestEngine := func(withAuth bool) *gin.Engine {
		engine := gin.New()
		engine.Use(func(c *gin.Context) {
			if withAuth {
				c.Request = c.Request.WithContext(authentication.With(c.Request.Context(), authentication.Identity{
					UserID: "user_test_avatar_1",
					Role:   string(enums.RoleUser),
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
