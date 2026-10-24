// Package mailer 包含邮件模板渲染与主题注入基础设施。
package mailer

import (
	"bytes"
	"cmp"
	"html/template"
	"maps"
	"strings"
	"sync"

	"bgin/internal/platform/mailer/templates"
)

var (
	tmplMu        sync.RWMutex
	tmplCache     = make(map[string]*template.Template)
	globalThemeMu sync.RWMutex
	globalTheme   ThemeConfig
	globalAppName = "bgin"
	themeKeys     = [...]string{"ThemePrimary", "ThemeSuccess", "ThemeDanger", "ThemeWarning", "ThemeBg"}
)

// ThemeConfig 描述 HTML 邮件模板的主题配色选项。
type ThemeConfig struct {
	PrimaryColor string
	SuccessColor string
	DangerColor  string
	WarningColor string
	BgColor      string
}

// SetGlobalTheme 设置邮件模板全局主题配色与应用名称。
func SetGlobalTheme(theme ThemeConfig, appName string) {
	globalThemeMu.Lock()
	defer globalThemeMu.Unlock()
	globalTheme = theme
	if appName != "" {
		globalAppName = appName
	}
}

// RenderTemplate 渲染指定的邮件模板与上下文数据，并自动注入主题样式。
func RenderTemplate(name string, data map[string]any) (string, error) {
	tmplFile := name
	if !strings.HasSuffix(tmplFile, ".tmpl") {
		tmplFile += ".tmpl"
	}

	tmpl, err := getOrParseTemplate(tmplFile)
	if err != nil {
		return "", err
	}

	mergedData := prepareTemplateData(data)

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", mergedData); err != nil {
		buf.Reset()
		if err := tmpl.ExecuteTemplate(&buf, tmplFile, mergedData); err != nil {
			return "", err
		}
	}

	return buf.String(), nil
}

func getOrParseTemplate(tmplFile string) (*template.Template, error) {
	tmplMu.RLock()
	if cached, ok := tmplCache[tmplFile]; ok {
		tmplMu.RUnlock()
		return cached, nil
	}
	tmplMu.RUnlock()

	tmplMu.Lock()
	defer tmplMu.Unlock()

	if cached, ok := tmplCache[tmplFile]; ok {
		return cached, nil
	}

	patterns := []string{
		tmplFile,
		"layouts/*.tmpl",
		"components/*.tmpl",
	}

	parsed, err := template.ParseFS(templates.FS, patterns...)
	if err != nil {
		return nil, err
	}

	tmplCache[tmplFile] = parsed
	return parsed, nil
}

func prepareTemplateData(data map[string]any) map[string]any {
	globalThemeMu.RLock()
	theme := globalTheme
	appName := globalAppName
	globalThemeMu.RUnlock()

	merged := make(map[string]any, len(data)+6)
	merged["ThemePrimary"] = cmp.Or(theme.PrimaryColor, "#3869D4")
	merged["ThemeSuccess"] = cmp.Or(theme.SuccessColor, "#22BC66")
	merged["ThemeDanger"] = cmp.Or(theme.DangerColor, "#DC4D2F")
	merged["ThemeWarning"] = cmp.Or(theme.WarningColor, "#FF6200")
	merged["ThemeBg"] = cmp.Or(theme.BgColor, "#F4F4F7")
	merged["AppName"] = appName

	maps.Copy(merged, data)

	if btn, ok := merged["Button"].(map[string]any); ok {
		if _, exists := btn["Url"]; !exists {
			if u, ok := btn["URL"]; ok {
				btn["Url"] = u
			}
		}
		cascadeTheme(btn, merged)
	}
	if pnl, ok := merged["Panel"].(map[string]any); ok {
		cascadeTheme(pnl, merged)
	}

	return merged
}

func cascadeTheme(target, src map[string]any) {
	for _, key := range themeKeys {
		if _, exists := target[key]; !exists {
			target[key] = src[key]
		}
	}
}
