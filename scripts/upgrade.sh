#!/bin/sh
# ==============================================================================
# Go 依赖检查与升级工具 (upgrade.sh)
# 用法: scripts/upgrade.sh [list|up|patch]
# ==============================================================================
set -eu

export GOFLAGS="${GOFLAGS:-} -mod=mod"

# ── colors (only when stdout is a TTY) ───────────────────────────────────────

if [ -t 1 ]; then
	RESET=$(printf '\033[0m')
	BOLD=$(printf '\033[1m')
	CYAN=$(printf '\033[0;36m')
	GREEN=$(printf '\033[0;32m')
	YELLOW=$(printf '\033[0;33m')
	RED=$(printf '\033[0;31m')
	DIM=$(printf '\033[2m')
else
	RESET='' BOLD='' CYAN='' GREEN='' YELLOW='' RED='' DIM=''
fi

info()  { printf "${CYAN}  info${RESET}  %s\n" "$*"; }
ok()    { printf "${GREEN}    ok${RESET}  %s\n" "$*"; }
warn()  { printf "${YELLOW}  skip${RESET}  %s\n" "$*"; }
bump()  { printf "${GREEN}  bump${RESET}  %s\n" "$*"; }
die()   { printf "${RED} error${RESET}  %s\n" "$*" >&2; exit 1; }

# ── helpers ───────────────────────────────────────────────────────────────────

usage() {
	cat <<EOF
${BOLD}Usage:${RESET}
  scripts/upgrade.sh [list]   Show direct dependencies with newer versions (default)
  scripts/upgrade.sh up       Upgrade direct dependencies to @latest
  scripts/upgrade.sh patch    Upgrade direct dependencies (patch-only)

  -h, --help    Show this help
EOF
}

# Strip "v" prefix and pre-release/build-metadata: v1.2.3+incompatible → 1.2.3
strip_semver() { printf '%s' "$1" | sed -E 's/^v//;s/[-+].*//'; }
semver_field() { printf '%s' "$1" | cut -d. -f"$2"; }

list_direct() {
	go list -m -f '{{if and (not .Main) (not .Indirect)}}{{.Path}}{{end}}' all \
		| awk 'NF' || true
}

list_direct_updates() {
	go list -m -u \
		-f '{{if and (not .Main) (not .Indirect) .Update}}{{.Path}} {{.Version}} {{.Update.Version}}{{end}}' \
		all | awk 'NF' || true
}


# ── list ──────────────────────────────────────────────────────────────────────

# ── helper ────────────────────────────────────────────────────────────────────

# 打印依赖更新表格，清晰直观地向用户展示当前与最新版本对比
print_updates_table() {
	updates="$1"
	printf "\n${BOLD}%-44s  %-22s  %s${RESET}\n" "MODULE" "CURRENT" "LATEST"
	printf "${DIM}%-44s  %-22s  %s${RESET}\n"    "------" "-------" "------"
	printf '%s\n' "$updates" | while read -r mod cur latest; do
		printf "${CYAN}%-44s${RESET}  ${DIM}%-22s${RESET}  ${GREEN}→ %s${RESET}\n" \
			"$mod" "$cur" "$latest"
	done
	printf "\n"
}

# ── list ──────────────────────────────────────────────────────────────────────

cmd_list() {
	info "Checking for updates..."
	updates="$(list_direct_updates)"
	if [ -z "$updates" ]; then
		ok "All direct dependencies are up to date."
		return
	fi
	print_updates_table "$updates"
}

# ── up ────────────────────────────────────────────────────────────────────────

cmd_up() {
	info "Checking for updates..."
	updates="$(list_direct_updates)"
	if [ -z "$updates" ]; then
		ok "All direct dependencies are up to date."
		return
	fi
	print_updates_table "$updates"

	# 非 TTY 环境下直接报错退出，避免交互式 read 导致脚本阻塞挂起
	if [ ! -t 0 ]; then
		die "Standard input is not a TTY. Cannot ask for confirmation."
	fi

	printf "Do you want to upgrade these dependencies to @latest? [y/N]: "
	read -r resp
	case "$resp" in
		[yY][eE][sS]|[yY])
			;;
		*)
			ok "Upgrade aborted."
			return
			;;
	esac

	# 提取有更新的包并拼接成 space-separated 字符串以进行批量升级，提升执行效率
	upgrade_targets="$(printf '%s\n' "$updates" | awk '{print $1 "@latest"}' | tr '\n' ' ')"

	info "Upgrading dependencies to @latest..."
	# shellcheck disable=SC2086
	go get $upgrade_targets
	go mod tidy
	go mod verify
	ok "All done."
}

# ── patch ─────────────────────────────────────────────────────────────────────

cmd_patch() {
	info "Checking for patch updates..."
	updates="$(list_direct_updates)"
	if [ -z "$updates" ]; then
		ok "No updates available."
		return
	fi

	# 使用临时文件缓存待升级的 patch 依赖，避免 while read 子进程中变量丢失的问题
	tmp_file="$(mktemp)"
	trap 'rm -f "$tmp_file"' EXIT INT TERM

	printf '\n'
	printf '%s\n' "$updates" | while read -r mod cur latest; do
		cur_clean="$(strip_semver "$cur")"
		lat_clean="$(strip_semver "$latest")"

		cur_major="$(semver_field "$cur_clean" 1)"
		cur_minor="$(semver_field "$cur_clean" 2)"
		cur_patch="$(semver_field "$cur_clean" 3)"
		lat_major="$(semver_field "$lat_clean" 1)"
		lat_minor="$(semver_field "$lat_clean" 2)"
		lat_patch="$(semver_field "$lat_clean" 3)"

		# 过滤非标准 SemVer 版本（如 Hash、Prerelease 等），只对纯数字版本执行升级
		has_non_numeric=0
		for v in "$cur_major" "$cur_minor" "$cur_patch" "$lat_major" "$lat_minor" "$lat_patch"; do
			case "$v" in
			'' | *[!0-9]*)
				has_non_numeric=1
				break
				;;
			esac
		done

		if [ "$has_non_numeric" -eq 1 ]; then
			warn "$(printf '%-44s  %s → %s  (non-standard version)' "$mod" "$cur" "$latest")"
			continue
		fi

		if [ "$cur_major" = "$lat_major" ] \
			&& [ "$cur_minor" = "$lat_minor" ] \
			&& [ "$lat_patch" -gt "$cur_patch" ]; then
			bump "$(printf '%-44s  %s → %s' "$mod" "$cur" "$latest")"
			printf '%s@%s\n' "$mod" "$latest" >> "$tmp_file"
		else
			warn "$(printf '%-44s  %s → %s  (major/minor change)' "$mod" "$cur" "$latest")"
		fi
	done

	if [ ! -s "$tmp_file" ]; then
		printf '\n'
		ok "No patch updates to apply."
		return
	fi

	printf '\n'

	# 非 TTY 环境直接退出，防阻塞
	if [ ! -t 0 ]; then
		die "Standard input is not a TTY. Cannot ask for confirmation."
	fi

	printf "Do you want to apply these patch updates? [y/N]: "
	read -r resp
	case "$resp" in
		[yY][eE][sS]|[yY])
			;;
		*)
			ok "Patch update aborted."
			return
			;;
	esac

	# 批量执行升级操作
	patch_targets="$(tr '\n' ' ' < "$tmp_file")"

	info "Applying patch updates..."
	# shellcheck disable=SC2086
	go get $patch_targets
	go mod tidy
	ok "Patch update complete."
}

# ── entry ─────────────────────────────────────────────────────────────────────

mode="${1:-list}"
case "$mode" in
list)  cmd_list  ;;
up)    cmd_up    ;;
patch) cmd_patch ;;
-h | --help | help) usage ;;
*) die "Unknown mode: $mode" ;;
esac
