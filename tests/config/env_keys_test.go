// 本文件把"新增配置项时漏改 configKeys 白名单"这件静默失效变成测试失败。
//
// 做法是行为探测：一个键有没有接进配置，只能靠"改这个变量，加载结果会不会变"来观察。
// 对 .env.example 与 .env 里出现的每个键，依次用一组候选值经进程环境（最高优先层）
// 覆盖，若没有任何候选值能改变加载结果、也没能让加载失败，该键就是被静默忽略的。
//
// 之所以不直接遍历 configKeys：仓库约定测试全部是 tests/ 下的外部测试包（ADR-0002），
// 拿不到未导出的白名单，而且断言"表里有这个键"也不如断言"这个键真的生效"。
package config_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
)

// nonConfigEnvVars 记录环境文件里刻意不属于应用配置的变量：它们服务于开发或测试工具
// 而非进程自身，因此不该出现在 configKeys 里。新增这类变量时要补到这里。
var nonConfigEnvVars = map[string]string{
	"ATLAS_DEV_URL":     "atlas migrate diff 的比对库，只被 atlas CLI 读取",
	"TEST_DATABASE_URL": "集成测试专用库，只被 tests/integration 读取",
}

// probeValues 是探测每个键时依次尝试的候选值，覆盖结构体里的五种落点：字符串、整数、
// 布尔、time.Duration、逗号分隔切片。单个值可能被类型拒绝或与默认值相同，但只要有一个
// 被采纳，就说明该键确实接进了配置。
var probeValues = []string{"probe", "1", "true", "1m", "a,b"}

// envNamePattern 匹配环境变量名，用来跳过环境文件里的注释与无关文本。
var envNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// TestEnvFileKeysAreRecognized 校验环境文件里的每个键都真的会被配置采纳。
func TestEnvFileKeysAreRecognized(t *testing.T) {
	names := envFileKeys(t)

	setRequiredEnv(t)
	baseline, err := config.Load()
	require.NoError(t, err, "基准配置必须能加载，否则下面的比较没有意义")

	for _, name := range names {
		if reason, ok := nonConfigEnvVars[name]; ok {
			t.Logf("%s 刻意不接入配置：%s", name, reason)
			continue
		}

		t.Run(name, func(t *testing.T) {
			for _, probe := range probeValues {
				t.Setenv(name, probe)

				cfg, err := config.Load()
				if err != nil {
					// 加载失败也算"生效"，但必须不是键路径写错导致的：configKeys 里
					// 的值指向不存在的配置键时，严格解码会报 invalid keys。
					assert.NotContains(t, err.Error(), "invalid keys",
						"%s 在 configKeys 里的键路径写错了", name)
					return
				}
				if !reflect.DeepEqual(baseline, cfg) {
					return
				}
			}

			t.Errorf("%s 被静默忽略：既不在 configKeys 的白名单里，也不属于 nonConfigEnvVars", name)
		})
	}
}

// envFileKeys 收集 .env.example（必须有，它是入库的键集合基准）与 .env（本地可选）
// 的实际键名，按首次出现的顺序去重。
func envFileKeys(t *testing.T) []string {
	t.Helper()

	seen := make(map[string]bool)
	var names []string
	for _, target := range []struct {
		path     string
		required bool
	}{
		{filepath.Join("..", "..", ".env.example"), true},
		{filepath.Join("..", "..", ".env"), false},
	} {
		keys, err := envFileKeysIn(target.path)
		if errors.Is(err, fs.ErrNotExist) && !target.required {
			continue
		}
		require.NoError(t, err)

		for _, key := range keys {
			if !seen[key] {
				seen[key] = true
				names = append(names, key)
			}
		}
	}
	require.NotEmpty(t, names, "环境文件里没有解析出任何键，解析逻辑可能坏了")
	return names
}

// envFileKeysIn 读出一个环境文件里实际提供的键名，跳过注释行、空行与非法行。
func envFileKeysIn(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var names []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, ok := strings.Cut(line, "=")
		if ok && envNamePattern.MatchString(name) {
			names = append(names, name)
		}
	}
	return names, nil
}
