package global

import (
	gini18n "github.com/gin-contrib/i18n"
	"github.com/gin-gonic/gin"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/text/language"

	"cgin/message"
	"cgin/pkg/i18n"
)

// I18n 【全局中间件】基于 gin-contrib/i18n 统一提供多语言嗅探、语言协商与上下文注入。
//
// 语言资源的分节合并由 pkg/i18n.Loader 统一提供，HTTP 层与 Translator 不再各写一份。
func (m *Middleware) I18n() gin.HandlerFunc {
	defaultTag := language.English
	if m.translator != nil && (m.translator.Fallback() == "zh-CN" || m.translator.Fallback() == "zh") {
		defaultTag = language.MustParse("zh-CN")
	}

	return gini18n.Localize(
		gini18n.WithBundle(&gini18n.BundleCfg{
			RootPath:         "message",
			FormatBundleFile: "toml",
			AcceptLanguage: []language.Tag{
				language.English,
				language.MustParse("zh-CN"),
				language.Chinese,
			},
			DefaultLanguage: defaultTag,
			UnmarshalFunc:   toml.Unmarshal,
			Loader:          i18n.Loader{FS: message.FS},
		}),
	)
}
