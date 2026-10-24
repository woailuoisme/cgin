#!/bin/sh
# ==============================================================================
# Go Module 自动同步与重命名脚本 (mod-sync.sh)
# ==============================================================================
# 用法:
#   1. 零参数模式（推荐）:
#      修改 go.mod 中的 module <新模块名> 后，直接运行：
#      ./scripts/mod-sync.sh
#
#   2. 带参数模式:
#      ./scripts/mod-sync.sh <新模块名>
# ==============================================================================
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
cd "$repo_root"

export GOFLAGS="${GOFLAGS:-} -mod=mod"

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
	echo "Usage: ./scripts/mod-sync.sh [new-module-name]"
	echo "If no argument is given, it syncs all codebase imports to match the module in go.mod."
	exit 0
fi

if [ ! -f go.mod ]; then
	echo "Error: go.mod not found in $repo_root" >&2
	exit 1
fi

if ! command -v rg >/dev/null 2>&1 || ! command -v sd >/dev/null 2>&1; then
	echo "Error: ripgrep (rg) and sd are required." >&2
	echo "Install with: brew install ripgrep sd  or  mise use -g ripgrep sd" >&2
	exit 1
fi

# 1. 如果传入了新模块名参数，先更新 go.mod
if [ -n "${1:-}" ]; then
	new_module="$1"
	go mod edit -module "$new_module"
fi

# 2. 读取 go.mod 中的目标模块名
target_module=$(awk '$1=="module" {print $2; exit}' go.mod)
if [ -z "$target_module" ]; then
	echo "Error: Failed to parse module name from go.mod" >&2
	exit 1
fi

# 3. 自动嗅探现有 Go 源码中使用的旧模块名
current_import_mod=$(rg -o --no-filename -m 1 '"([^"]+)/internal/[^"]+"' -r '$1' app/ cmd/ internal/ tests/ 2>/dev/null | head -n 1 || true)

if [ -z "$current_import_mod" ]; then
	current_import_mod="$target_module"
fi

# 4. 如果发现代码中的引用与 go.mod 不一致，全量快速替换
if [ "$current_import_mod" != "$target_module" ]; then
	echo "Syncing module imports from '$current_import_mod' to '$target_module'..."
	# 注意: 不能用 files=$(rg -0 ...) 接收 NUL 分隔输出, 命令替换会丢弃 NUL 字节导致路径粘连.
	rg --hidden --glob '!.git' -0 -l -F "$current_import_mod" 2>/dev/null \
		| xargs -0 sd -F "$current_import_mod" "$target_module"
	echo "All import paths updated."
else
	echo "Module is already in sync with go.mod: $target_module"
fi

# 5. 重整依赖
echo "Running go mod tidy..."
go mod tidy
echo "Done!"
