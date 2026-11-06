#!/usr/bin/env bash
# check-pg-backup.sh — 外部监控探针：最近一次 PG 快照还好用吗？
#
# 用途：给 systemd timer / cron 的备份做「结果验收」——备份任务退出码 0 只说明
# 脚本跑完了，不说明昨天的快照今天还能恢复。本脚本只读，不写任何东西：
#
#   1) 找到最新的快照目录（<out>/<database>-<UTC 时间戳>/，按名字排序取最后一个）；
#   2) 新鲜度：MANIFEST.txt 的 created_at_utc（取不到则退化为 dump 的 mtime）
#      距今不超过 --max-age-hours（默认 36h，即「每日一份 + 允许错过一次」）；
#   3) 完整性：在快照目录内 sha256sum -c SHA256SUMS 全部 OK；
#   4) 可解析：pg_restore -l 能读出 dump 的目录（header 没坏、能列出对象）。
#
# 为什么这三项：新鲜度防「定时任务悄悄没跑」，sha256 防「磁盘静默损坏/被截断」，
# pg_restore -l 防「文件在但格式不对」——三者都过了才值得在事故时真去恢复。
#
# 用法：
#   bash scripts/check-pg-backup.sh
#   bash scripts/check-pg-backup.sh --dir /srv/backups --max-age-hours 36
#   bash scripts/check-pg-backup.sh --quiet        # 只在失败时输出（cron / 监控）
#
# 环境变量：BOT_HARNESS_BACKUP_DIR / BOT_HARNESS_PG_DATABASE /
#           BOT_HARNESS_PG_CONTAINER / BOT_HARNESS_BACKUP_MAX_AGE_HOURS
#
# 退出码：0 全部通过；1 检查失败（监控按非 0 报警）；2 参数/环境错误。
#   2 也是非 0：外部监控不该把「探针自己配错了」当成正常。
set -euo pipefail

OUT="${BOT_HARNESS_BACKUP_DIR:-$HOME/bot-harness-backups}"
DATABASE="${BOT_HARNESS_PG_DATABASE:-bot}"
CONTAINER="${BOT_HARNESS_PG_CONTAINER:-bot-harness-postgres}"
MAX_AGE_HOURS="${BOT_HARNESS_BACKUP_MAX_AGE_HOURS:-36}"
QUIET=0

die()  { printf 'check-pg-backup: %s\n' "$1" >&2; exit 2; }
fail() { printf 'check-pg-backup: FAIL: %s\n' "$1" >&2; exit 1; }
say()  { [ "$QUIET" = 1 ] || printf 'check-pg-backup: %s\n' "$1"; }

usage() {
  cat <<'USAGE'
用法: check-pg-backup.sh [选项]

  --dir DIR             快照根目录（默认 $HOME/bot-harness-backups）
  --database NAME       要检查哪个库的快照（默认 bot）
  --container NAME      pg_restore 从哪个容器借（默认 bot-harness-postgres）
  --max-age-hours N     最新快照允许的最大年龄，单位小时（默认 36）
  --quiet               成功时不打印，只在失败时输出（适合 cron / 监控）
  -h, --help            显示本帮助

退出码：0 通过；1 检查失败；2 参数/环境错误。

环境变量：BOT_HARNESS_BACKUP_DIR / BOT_HARNESS_PG_DATABASE /
          BOT_HARNESS_PG_CONTAINER / BOT_HARNESS_BACKUP_MAX_AGE_HOURS
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dir|-d)            [ "$#" -ge 2 ] || die "--dir 需要参数"; OUT="$2"; shift 2 ;;
    --database|-b)       [ "$#" -ge 2 ] || die "--database 需要参数"; DATABASE="$2"; shift 2 ;;
    --container|-c)      [ "$#" -ge 2 ] || die "--container 需要参数"; CONTAINER="$2"; shift 2 ;;
    --max-age-hours|-m)  [ "$#" -ge 2 ] || die "--max-age-hours 需要参数"; MAX_AGE_HOURS="$2"; shift 2 ;;
    --quiet|-q)          QUIET=1; shift ;;
    -h|--help)           usage; exit 0 ;;
    *)                   die "未知参数：$1（--help 查看用法）" ;;
  esac
done

case "$MAX_AGE_HOURS" in
  ''|*[!0-9]*) die "--max-age-hours 必须是非负整数，实际：$MAX_AGE_HOURS" ;;
esac
[ -n "$DATABASE" ] || die "--database 不能为空"
[ -n "$OUT" ] || die "--dir 不能为空"
command -v sha256sum >/dev/null 2>&1 || die "找不到 sha256sum"

safe_db="${DATABASE//[^A-Za-z0-9._-]/_}"
[ -d "$OUT" ] || fail "备份目录不存在：$OUT（备份任务从来没成功跑过？）"

# 1) 最新快照：目录名以 UTC 时间戳结尾，字典序 == 时间序。
snap=""
while IFS= read -r d; do
  [ -n "$d" ] && snap="$d"
done < <(find "$OUT" -mindepth 1 -maxdepth 1 -type d -name "$safe_db-*" -print 2>/dev/null | sort)
[ -n "$snap" ] || fail "在 $OUT 里找不到任何 $safe_db-* 快照目录"

dump="$snap/$DATABASE.dump"
[ -f "$dump" ] || fail "最新快照里没有 dump 文件：$dump"
[ -s "$dump" ] || fail "dump 文件是空的：$dump"
[ -f "$snap/SHA256SUMS" ] || fail "快照缺少 SHA256SUMS：$snap"
[ -f "$snap/MANIFEST.txt" ] || fail "快照缺少 MANIFEST.txt：$snap"

# 2) 新鲜度。优先用 MANIFEST 里的 created_at_utc（UTC，ISO8601），
#    读不到/格式怪就退化为 dump 的 mtime —— 宁可多查一次，也不要因为
#    清单字段缺失就放过一个陈年快照。
# 用秒做比较：先按整小时整除会让 36.9h 的快照算成 36h 蒙混过关。
now="$(date -u +%s)"
created="$(sed -n 's/^created_at_utc=//p' "$snap/MANIFEST.txt" | head -n1)"
if [ -n "$created" ] && created_epoch="$(date -u -d "$created" +%s 2>/dev/null)"; then
  :
else
  mtime="$(stat -c %Y "$dump" 2>/dev/null || stat -f %m "$dump" 2>/dev/null || echo '')"
  [ -n "$mtime" ] || fail "读不出快照时间（MANIFEST 无 created_at_utc，且 stat 失败）：$snap"
  created_epoch="$mtime"
  created="(mtime；MANIFEST created_at_utc=$created 不可解析)"
  printf 'check-pg-backup: 警告：MANIFEST 缺少可解析的 created_at_utc，改用 dump 的 mtime\n' >&2
fi
age_sec=$(( now - created_epoch ))
[ "$age_sec" -ge 0 ] || fail "快照时间在未来（时钟漂移？）：created_at_utc=$created"
age_human="$(printf '%dh%02dm' $(( age_sec / 3600 )) $(( age_sec % 3600 / 60 )))"
[ "$age_sec" -le "$(( MAX_AGE_HOURS * 3600 ))" ] \
  || fail "最新快照太旧：$age_human > 允许的 ${MAX_AGE_HOURS}h（$snap）"

# 3) 完整性：在快照目录内校验，SHA256SUMS 里存的是相对文件名。
checksum_out="$(cd "$snap" && sha256sum -c SHA256SUMS 2>&1)" \
  || fail "sha256 校验不通过（$snap）：$checksum_out"
printf '%s\n' "$checksum_out" | grep -q ': OK$' \
  || fail "SHA256SUMS 里没有一条 OK 记录（$snap）"

# 4) 可解析：能用 pg_restore -l 读出目录，说明 -Fc header 完好。
#    宿主有 pg_restore 就用宿主（不依赖容器），否则借容器的（与 backup-pg.sh 一致）。
objects=""
if command -v pg_restore >/dev/null 2>&1; then
  objects="$(pg_restore -l "$dump" 2>/dev/null | grep -cvE '^;|^$' || true)"
else
  command -v docker >/dev/null 2>&1 || fail "既没有宿主 pg_restore，也没有 docker，无法校验 dump 格式"
  docker inspect "$CONTAINER" >/dev/null 2>&1 \
    || fail "容器不存在：$CONTAINER（且宿主没有 pg_restore）"
  objects="$(docker exec -i "$CONTAINER" pg_restore -l < "$dump" 2>/dev/null | grep -cvE '^;|^$' || true)"
fi
[ "${objects:-0}" -gt 0 ] || fail "pg_restore -l 解析不出任何对象（dump 可能损坏）：$dump"

bytes="$(wc -c < "$dump" | tr -d ' ')"
say "PASS: 快照 $snap"
say "  年龄 $age_human（上限 ${MAX_AGE_HOURS}h）  sha256 OK  pg_restore 可解析"
say "  大小 ${bytes} 字节 / ${objects} 个对象  created_at_utc=$created"
exit 0
