#!/usr/bin/env bash
#
# check-config-keys.sh — 后端配置面（Settings 全部键 + GroupOverride 全部字段）一致性校验。
#
# 为什么存在：前端设置页、settings_write、CI 都在照着一份键集合工作。后端若改了某个
# json tag（或新增配置项忘了同步），界面上就会出现「看不到 / 改不了」的字段，而且
# 不会有任何报错。manifest 由 Go 侧从类型推导（internal/settings/keys.go），
# golden 提交在 internal/settings/testdata/config-keys.json。
#
# 用法：
#   bash scripts/check-config-keys.sh          # 校验 golden 是否最新（CI 用；不一致退出 1）
#   bash scripts/check-config-keys.sh --write  # 重新生成 golden（改配置项后运行并提交）
#   bash scripts/check-config-keys.sh --json   # 只把清单打印到 stdout（前端/审计比对用）
#
# 输出（--json）：{"settings":[...],"group_override":[...]}
#
# 依赖：Go 工具链（确保 `go` 在 PATH 上）
# 退出码：0 = 一致 / 已写入 / 已打印；1 = 漂移；2 = 环境不满足（缺 go / 缺 golden）
#
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
GOLDEN="$ROOT/internal/settings/testdata/config-keys.json"
PKG="./internal/settings/configkeys"

die() { printf 'check-config-keys: %s\n' "$1" >&2; exit 2; }

command -v go >/dev/null 2>&1 || die "找不到 go（请先把 Go 工具链加入 PATH）"

cd "$ROOT"

render() { go run "$PKG"; }

case "${1:-}" in
  --write)
    mkdir -p "$(dirname "$GOLDEN")"
    render > "$GOLDEN"
    printf 'check-config-keys: 已写入 %s\n' "${GOLDEN#"$ROOT"/}"
    ;;
  --json)
    render
    ;;
  "")
    [ -f "$GOLDEN" ] || die "golden 不存在：${GOLDEN#"$ROOT"/}（先运行 --write）"
    TMP="$(mktemp)"
    trap 'rm -f "$TMP"' EXIT
    render > "$TMP"
    if diff -u "$GOLDEN" "$TMP" >&2; then
      printf 'check-config-keys: OK（%s）\n' "${GOLDEN#"$ROOT"/}"
    else
      printf '%s\n' 'check-config-keys: golden 与类型不一致。若这是有意的配置面变更，' \
        '运行 bash scripts/check-config-keys.sh --write 并提交 golden，' \
        '同时同步前端设置页与 docs/ADMIN-API.md。' >&2
      exit 1
    fi
    ;;
  *)
    die "未知参数 $1（可用：--write / --json）"
    ;;
esac
