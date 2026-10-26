package i18n

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"cgin/message"
)

// sectionSuffix 是语言资源文件的分节后缀。
const sectionSuffix = ".toml"

// Loader 把 message/<语言>/<分节>.toml 合并成单文件分节 TOML，是语言资源的唯一装载器。
//
// 它同时满足 gin-contrib/i18n 的 Loader 契约（LoadMessage(path) ([]byte, error)），
// 因此 HTTP 中间件与本包的 Translator 共用同一份分节拼装逻辑，不再各自实现一遍。
type Loader struct {
	// FS 是语言资源文件系统；为零值时回退到内嵌的 message.FS。
	FS fs.FS
}

// LoadMessage 读取并合并一门语言的全部分节文件。
//
// filePath 形如 message/en.toml 或 message/zh-CN.toml。语言名允许写成 zh 或 zh-Hans，
// 会映射到 zh-CN 目录，以兼容 gin-contrib/i18n 按 language.Tag 拼接路径的行为。
func (l Loader) LoadMessage(filePath string) ([]byte, error) {
	fsys := l.filesystem()

	lang := normalizeLanguage(strings.TrimSuffix(trimRoot(filePath), sectionSuffix))

	entries, err := fs.ReadDir(fsys, lang)
	if err != nil && (lang == "zh" || lang == "zh-Hans") {
		lang = "zh-CN"
		entries, err = fs.ReadDir(fsys, lang)
	}
	if err != nil {
		// 已经是单文件资源（如测试用的自定义 FS），直接透传读取。
		if rfs, ok := fsys.(fs.ReadFileFS); ok {
			return rfs.ReadFile(filePath)
		}
		return nil, fmt.Errorf("i18n: read language directory %s failed: %w", lang, err)
	}

	var buf bytes.Buffer
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sectionSuffix) {
			continue
		}

		raw, readErr := fs.ReadFile(fsys, path.Join(lang, entry.Name()))
		if readErr != nil {
			return nil, fmt.Errorf("i18n: read %s/%s failed: %w", lang, entry.Name(), readErr)
		}

		section := strings.TrimSuffix(entry.Name(), sectionSuffix)
		fmt.Fprintf(&buf, "\n[%s]\n", section)
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// Languages 返回资源中存在语言包的语言目录名，已排序以保证协商结果稳定。
func (l Loader) Languages() ([]string, error) {
	entries, err := fs.ReadDir(l.filesystem(), ".")
	if err != nil {
		return nil, fmt.Errorf("i18n: read language directory failed: %w", err)
	}

	languages := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			languages = append(languages, entry.Name())
		}
	}
	return languages, nil
}

// filesystem 返回实际使用的文件系统，零值回退内嵌资源。
func (l Loader) filesystem() fs.FS {
	if l.FS != nil {
		return l.FS
	}
	return message.FS
}

// trimRoot 去掉资源根前缀，得到 "<语言>.toml" 形式的文件名。
func trimRoot(filePath string) string {
	return strings.TrimPrefix(strings.TrimPrefix(filePath, "message/"), "/")
}

// normalizeLanguage 把路径中的语言片段归一为语言包目录名。
func normalizeLanguage(lang string) string {
	return strings.TrimSpace(lang)
}
