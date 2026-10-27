package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gini18n "github.com/gin-contrib/i18n"
	"github.com/gin-gonic/gin"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	i18ntemplate "github.com/nicksnyder/go-i18n/v2/i18n/template"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/internal/middleware/global"
	"cgin/pkg/i18n"
)

// TestI18nMiddleware_NegotiationAndMessages 验证 gin-contrib/i18n 多语言协商与文案获取。
func TestI18nMiddleware_NegotiationAndMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := global.New(global.Deps{})

	router := gin.New()
	router.Use(m.I18n())

	router.GET("/i18n-test", func(c *gin.Context) {
		currentLang := gini18n.GetCurrentLanguage(c).String()
		requiredMsg := gini18n.MustGetMessage(c, "validation.required")
		c.JSON(http.StatusOK, gin.H{
			"lang":     currentLang,
			"required": requiredMsg,
		})
	})

	// 1. 测试中文协商
	reqZH := httptest.NewRequest(http.MethodGet, "/i18n-test", nil)
	reqZH.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	wZH := httptest.NewRecorder()
	router.ServeHTTP(wZH, reqZH)

	require.Equal(t, http.StatusOK, wZH.Code)
	assert.Contains(t, wZH.Body.String(), "zh")

	// 2. 测试英文默认协商
	reqEN := httptest.NewRequest(http.MethodGet, "/i18n-test", nil)
	reqEN.Header.Set("Accept-Language", "en-US,en;q=0.9")
	wEN := httptest.NewRecorder()
	router.ServeHTTP(wEN, reqEN)

	require.Equal(t, http.StatusOK, wEN.Code)
	assert.Contains(t, wEN.Body.String(), "en")
}

// TestI18nMiddleware_MatchesTranslatorOutput 锁定 HTTP 中间件与 pkg/i18n 的渲染结果一致。
//
// 两条路径共用 pkg/i18n.Loader 作为资源装载器，因此同一 key、同一份变量必须渲染出同一段文案；
// 一旦有人在其中一侧改了分节合并或模板引擎，这里会立刻失败。
func TestI18nMiddleware_MatchesTranslatorOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	translator, err := i18n.New(&config.Config{I18n: config.I18nConfig{DefaultLanguage: "en"}})
	require.NoError(t, err)

	m := global.New(global.Deps{Translator: translator})

	router := gin.New()
	router.Use(m.I18n())
	router.GET("/consistency", func(c *gin.Context) {
		fieldLabel := translator.FieldLabel("en", "name")
		templated, msgErr := gini18n.GetMessage(c, &goi18n.LocalizeConfig{
			MessageID:      "validation.required",
			TemplateData:   map[string]string{"Field": fieldLabel, "Param": ""},
			TemplateParser: &i18ntemplate.TextParser{Option: "missingkey=zero"},
		})
		require.NoError(t, msgErr)

		c.JSON(http.StatusOK, gin.H{
			"plain":     gini18n.MustGetMessage(c, "invoice.title"),
			"templated": templated,
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/consistency", nil)
	req.Header.Set("Accept-Language", "en")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code)

	var body struct {
		Plain     string `json:"plain"`
		Templated string `json:"templated"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))

	assert.Equal(t, translator.Invoice("en", "title", nil), body.Plain)
	assert.Equal(t, translator.ValidationMessage("en", "required", "name", ""), body.Templated)
}
