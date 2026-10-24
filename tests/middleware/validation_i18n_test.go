package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/middleware/global"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/i18n"
	shareduser "bgin/internal/shared/user"
)

// TestValidationMessageTranslation 验证以语言包键表达的字段级校验文案，
// 会在渲染边界按 Accept-Language 翻译，而不是把英文提示硬编码在业务代码里。
func TestValidationMessageTranslation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	translator, err := i18n.New(&config.Config{I18n: config.I18nConfig{DefaultLanguage: "en"}})
	require.NoError(t, err)

	cases := []struct {
		name     string
		language string
		want     string
	}{
		{name: "未指定语言时回退默认语言", want: "The name field cannot be null."},
		{name: "按 Accept-Language 渲染中文", language: "zh-CN", want: "名称不能为 null。"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := global.New(global.Deps{Translator: translator})
			r := gin.New()
			r.Use(m.ErrorHandler())
			r.PATCH("/profile", func(c *gin.Context) {
				_ = c.Error(shareduser.FieldCannotBeNull("name"))
			})

			req := httptest.NewRequest(http.MethodPatch, "/profile", nil)
			if tc.language != "" {
				req.Header.Set("Accept-Language", tc.language)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())

			var body errs.Response
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, string(errs.CodeValidationFailed), body.Code)
			assert.Equal(t, []string{tc.want}, body.Errors["name"])
		})
	}
}
