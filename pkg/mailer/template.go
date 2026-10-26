// Package mailer 包含邮件模板渲染与主题注入基础设施。
package mailer

import (
	"bytes"
	"html/template"
	"maps"
	"strings"
	"sync"

	"cgin/pkg/mailer/templates"
)

var (
	tmplMu    sync.RWMutex
	tmplCache = make(map[string]*template.Template)
	themeKeys = [...]string{"ThemePrimary", "ThemeSuccess", "ThemeDanger", "ThemeWarning", "ThemeBg"}
)

// 邮件模板的固定主题色与默认应用名。
const (
	defaultThemePrimary = "#3869D4"
	defaultThemeSuccess = "#22BC66"
	defaultThemeDanger  = "#DC4D2F"
	defaultThemeWarning = "#FF6200"
	defaultThemeBg      = "#F4F4F7"
	defaultAppName      = "cgin"
)

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
	merged := make(map[string]any, len(data)+6)
	merged["ThemePrimary"] = defaultThemePrimary
	merged["ThemeSuccess"] = defaultThemeSuccess
	merged["ThemeDanger"] = defaultThemeDanger
	merged["ThemeWarning"] = defaultThemeWarning
	merged["ThemeBg"] = defaultThemeBg
	merged["AppName"] = defaultAppName

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
