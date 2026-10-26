// Package architecture_test 把分层依赖方向固化为可执行契约。
//
// 分层扁平化后，同层多个模块共享一个 Go 包，编译器无法再通过包边界拦住反向依赖，
// 因此把「谁能 import 谁」写成测试：违规会直接让 CI 失败，而不是靠口头约定。
package architecture_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoRoot 是相对测试包目录的仓库根路径。
const repoRoot = "../.."

// layerRule 描述「某个目录下的代码禁止导入哪些前缀」。
type layerRule struct {
	// dir 是相对仓库根的目录。
	dir string
	// forbids 是禁止出现的导入前缀。
	forbids []string
	// reason 是违规时输出的规则说明。
	reason string
}

// TestLayerDependenciesFlowDownward 校验 route → handler → service → dto 的单向依赖，以及 pkg 的纯净性。
func TestLayerDependenciesFlowDownward(t *testing.T) {
	t.Parallel()

	rules := []layerRule{
		{
			dir:     "pkg",
			forbids: []string{"cgin/shared", "cgin/internal/database", "cgin/internal/dto", "cgin/internal/handler", "cgin/internal/service", "cgin/internal/route", "cgin/internal/middleware", "cgin/internal/bootstrap", "cgin/internal/console"},
			reason:  "pkg 是纯基础设施层，严禁反向依赖 internal 业务层或 shared 业务内核（仅 cgin/internal/config 例外）",
		},
		{
			dir:     "internal/dto",
			forbids: []string{"cgin/internal/handler", "cgin/internal/service", "cgin/internal/route", "cgin/internal/middleware"},
			reason:  "dto 是传输契约层，只能被上层引用，严禁反向依赖 handler/service/route/middleware",
		},
		{
			dir:     "internal/service",
			forbids: []string{"cgin/internal/handler", "cgin/internal/route"},
			reason:  "service 是用例层，严禁反向依赖 handler 与 route",
		},
		{
			dir:     "internal/handler",
			forbids: []string{"cgin/internal/route"},
			reason:  "handler 是传输适配层，路由注册必须留在 internal/route",
		},
		{
			dir:     "internal/user",
			forbids: []string{"cgin/internal/dto", "cgin/internal/handler", "cgin/internal/service", "cgin/internal/route", "cgin/internal/middleware", "cgin/internal/bootstrap", "cgin/internal/console"},
			reason:  "user 是用户视图与仓储内核，被 dto/service/export 引用，严禁反向依赖传输层、用例层或装配层",
		},
		{
			dir:     "internal/media",
			forbids: []string{"cgin/internal/dto", "cgin/internal/handler", "cgin/internal/service", "cgin/internal/route", "cgin/internal/middleware", "cgin/internal/bootstrap", "cgin/internal/console"},
			reason:  "media 是媒体处理内核，仅由装配层注入，严禁反向依赖传输层、用例层或装配层",
		},
		{
			dir:     "shared",
			forbids: []string{"cgin/internal"},
			reason:  "shared 已收敛为纯静态资产层（仅 apidocs），严禁依赖 internal 任何包；业务内核一律沉入 internal",
		},
	}

	for _, rule := range rules {
		t.Run(rule.dir, func(t *testing.T) {
			t.Parallel()

			violations := collectViolations(t, rule)

			for _, v := range violations {
				assert.Fail(t, "越界依赖", "%s → %s（%s）", v.file, v.importPath, rule.reason)
			}
		})
	}
}

// violation 记录一条越界导入。
type violation struct {
	file       string
	importPath string
}

// collectViolations 遍历规则目录，返回命中禁用前缀的导入。
func collectViolations(t *testing.T, rule layerRule) []violation {
	t.Helper()

	root := filepath.Join(repoRoot, rule.dir)
	var violations []violation

	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}

		for _, spec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			for _, prefix := range rule.forbids {
				if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
					rel, relErr := filepath.Rel(repoRoot, path)
					require.NoError(t, relErr)
					violations = append(violations, violation{file: rel, importPath: importPath})
				}
			}
		}
		return nil
	})
	require.NoError(t, walkErr)

	return violations
}
