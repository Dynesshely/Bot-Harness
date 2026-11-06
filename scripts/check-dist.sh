#!/usr/bin/env bash
#
# check-dist.sh — 「提交的构建产物 == 源码构建出来的产物」自动校验。
#
# 背景：web/dist 是**提交进仓库**的（`go:embed` 直接吃它，非 Docker 构建也靠它）。
# 只要有人改了 web/src 却忘了 `pnpm build`，二进制里嵌的就是旧前端 —— 页面与源码对不上，
# 而且这种漂移在 review 里几乎看不出来。CI 用这个脚本兜住。
#
# 做法（等价于 `pnpm build && git diff --exit-code web/dist`，但对新增/删除文件同样敏感）：
#   1. 记录当前 web/dist 每个文件的 sha256（= 提交进仓库的那份）
#   2. 在 web/ 里跑 `pnpm build`（内含 vue-tsc 类型检查）
#   3. 再记录一次，逐文件比对；有任何差异即失败（退出码 1）
#
# 用法：
#   bash scripts/check-dist.sh
#
# 退出码：0 = 一致；1 = 产物漂移；2 = 环境不满足（缺 node/pnpm/node_modules/dist）
#
# 注意：脚本会**重写 web/dist**。检测到漂移时，工作区里留下的是刚构建出的新产物 ——
# 这正是你要提交的东西（连新增的 hash 文件名一起 git add）。
#
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
WEB="$ROOT/web"
DIST="$WEB/dist"

die() { printf 'check-dist: %s\n' "$1" >&2; exit 2; }

command -v node >/dev/null 2>&1 || die "找不到 node（需要 Node 24+，见 AGENTS.md）"
command -v pnpm >/dev/null 2>&1 || die "找不到 pnpm（需要 pnpm 11+，见 AGENTS.md）"
[ -d "$WEB/node_modules" ] || die "web/node_modules 不存在，先执行：cd web && pnpm install"
[ -f "$DIST/index.html" ] || die "web/dist 不存在或为空（仓库里应保留构建产物，见 D-006）"

if command -v sha256sum >/dev/null 2>&1; then
  HASH_BIN=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
  HASH_BIN=(shasum -a 256)
else
  die "找不到 sha256sum / shasum"
fi

hash_file() {
  "${HASH_BIN[@]}" "$1" | cut -d' ' -f1
}

# snapshot 输出「hash 相对路径」，按路径排序，便于直接 diff。
snapshot() {
  ( cd "$DIST" && find . -type f -print | LC_ALL=C sort | while IFS= read -r f; do
      printf '%s  %s\n' "$(hash_file "$f")" "${f#./}"
    done )
}

TMPDIR_SNAP="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_SNAP"' EXIT
BEFORE="$TMPDIR_SNAP/before.txt"
AFTER="$TMPDIR_SNAP/after.txt"

snapshot > "$BEFORE"
FILE_COUNT="$(wc -l < "$BEFORE" | tr -d ' ')"

printf '==> 重建 web/dist（pnpm build，含 vue-tsc 类型检查）\n'
( cd "$WEB" && pnpm build )

snapshot > "$AFTER"

if cmp -s "$BEFORE" "$AFTER"; then
  printf 'PASS: web/dist 与源码构建结果一致（%s 个文件，sha256 全部相同）\n' "$FILE_COUNT"
  exit 0
fi

printf '\nFAIL: 提交的 web/dist 与源码构建结果不一致：\n'
# 两个 manifest 各两列（hash  path），按路径做集合比对。
awk '
  FNR==NR { old[$2]=$1; next }
  { new[$2]=$1 }
  END {
    for (f in new) if (!(f in old))              print "  + 构建产出但未提交（新增/改名）: web/dist/" f;
    for (f in new) if ((f in old) && old[f] != new[f]) print "  ~ 内容与提交不一致:            web/dist/" f;
    for (f in old) if (!(f in new))              print "  - 提交里残留但已不再生成:      web/dist/" f;
  }' "$BEFORE" "$AFTER" | LC_ALL=C sort

printf '\n修复：cd web && pnpm build，然后 git add web/dist（包含新增与删除的 hash 文件）。\n'
printf '      不要手工编辑 web/dist。\n'
exit 1
