#!/usr/bin/env bash
# backup-pg.sh — pg_dump -Fc snapshot of the bot-harness PostgreSQL service.
#
# 用途：把 bot-harness-postgres 里的一个数据库抓成带 UTC 时间戳、sha256 清单、
# 可轮转的自定义格式 dump（pg_dump -Fc），供 restore-pg.sh 校验后恢复。
#
# 一致性边界
#   pg_dump 在单个事务里取一致快照，所以**机器人运行中也可以热备**（不像 SQLite
#   文件级拷贝）。备份过程不停止、不重启任何容器，也不锁表（不阻塞机器人写入）。
#   唯一要求：目标容器在跑（healthcheck 不健康也能导，但会打印警告）。
#
# 快照布局（输出目录默认在仓库外，绝不进 git）：
#   <out>/<database>-<UTC timestamp>/
#     <database>.dump   pg_dump -Fc（含 blob）
#     SHA256SUMS        cd 到该目录后 sha256sum -c 可直接校验
#     MANIFEST.txt      format / container / database / user / server_version /
#                       image / created_at_utc / hot / bytes / objects
#
# 用法：
#   bash scripts/backup-pg.sh
#   bash scripts/backup-pg.sh --out "$HOME/bot-harness-backups" --keep 7
#   bash scripts/backup-pg.sh --database bot --container bot-harness-postgres
#
# 环境变量：BOT_HARNESS_PG_CONTAINER / BOT_HARNESS_PG_DATABASE / BOT_HARNESS_PG_USER /
#           BOT_HARNESS_BACKUP_DIR
#
# 退出码：0 成功；1 运行失败；2 参数/环境错误。
set -euo pipefail

CONTAINER="${BOT_HARNESS_PG_CONTAINER:-bot-harness-postgres}"
DATABASE="${BOT_HARNESS_PG_DATABASE:-bot}"
DBUSER="${BOT_HARNESS_PG_USER:-bot}"
OUT="${BOT_HARNESS_BACKUP_DIR:-$HOME/bot-harness-backups}"
KEEP=7

die() { printf "backup-pg: %s\n" "$1" >&2; exit 2; }
fail() { printf "backup-pg: %s\n" "$1" >&2; exit 1; }

usage() {
  cat <<'USAGE'
用法: backup-pg.sh [选项]

  --container NAME   PostgreSQL 容器（默认 bot-harness-postgres）
  --database NAME    要备份的库（默认 bot）
  --user NAME        连接用户（默认 bot）
  --out DIR          快照输出目录（默认 $HOME/bot-harness-backups，仓库外）
  --keep N           只保留最近 N 份快照（默认 7；0 = 不轮转）
  -h, --help         显示本帮助

环境变量：BOT_HARNESS_PG_CONTAINER / BOT_HARNESS_PG_DATABASE /
          BOT_HARNESS_PG_USER / BOT_HARNESS_BACKUP_DIR
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --container|-c) [ "$#" -ge 2 ] || die "--container 需要参数"; CONTAINER="$2"; shift 2 ;;
    --database|-d)  [ "$#" -ge 2 ] || die "--database 需要参数"; DATABASE="$2"; shift 2 ;;
    --user|-u)      [ "$#" -ge 2 ] || die "--user 需要参数"; DBUSER="$2"; shift 2 ;;
    --out|-o)       [ "$#" -ge 2 ] || die "--out 需要参数"; OUT="$2"; shift 2 ;;
    --keep|-k)      [ "$#" -ge 2 ] || die "--keep 需要参数"; KEEP="$2"; shift 2 ;;
    -h|--help)      usage; exit 0 ;;
    *)              die "未知参数：$1（--help 查看用法）" ;;
  esac
done

case "$KEEP" in
  ''|*[!0-9]*) die "--keep 必须是非负整数，实际：$KEEP" ;;
esac
[ -n "$CONTAINER" ] || die "--container 不能为空"
[ -n "$DATABASE" ] || die "--database 不能为空"
[ -n "$OUT" ] || die "--out 不能为空"

command -v docker >/dev/null 2>&1 || die "找不到 docker"
command -v sha256sum >/dev/null 2>&1 || die "找不到 sha256sum"
docker inspect "$CONTAINER" >/dev/null 2>&1 || die "容器不存在：$CONTAINER"
[ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER")" = "true" ] || die "容器没在运行：$CONTAINER"

# 目标库存在性（连不上时 psql 直接失败，比导出空文件好排查）。
docker exec "$CONTAINER" psql -U "$DBUSER" -d "$DATABASE" -tAc "select 1" >/dev/null 2>&1 \
  || fail "连不上 $CONTAINER 的库 $DATABASE（用户 $DBUSER）"

# 机器人是否在跑：只写进 MANIFEST（print 出来提示），不阻止热备。
hot=false
if [ -n "$(docker ps --filter "name=^bot-harness$" --format '{{.Names}}' 2>/dev/null || true)" ]; then
  hot=true
fi

health="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$CONTAINER")"
if [ "$health" = "unhealthy" ] || [ "$health" = "starting" ]; then
  printf "backup-pg: 警告：容器健康状态是 %s，仍继续（pg_dump 只要连得上就安全）。\n" "$health" >&2
fi

mkdir -p "$OUT"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
safe_db="${DATABASE//[^A-Za-z0-9._-]/_}"
snapshot="$OUT/$safe_db-$stamp"
[ ! -e "$snapshot" ] || fail "快照目录已存在（同一秒内重复执行？）：$snapshot"
mkdir -p "$snapshot"
cleanup() { rm -rf "$snapshot"; }
trap cleanup EXIT

dump="$snapshot/$DATABASE.dump"
printf "backup-pg: 容器=%s 库=%s 用户=%s hot=%s\n" "$CONTAINER" "$DATABASE" "$DBUSER" "$hot"

# 1) 导出。stdout 直接落盘：不在容器里留临时文件，也不需要 cp。
docker exec "$CONTAINER" pg_dump -U "$DBUSER" -d "$DATABASE" -Fc > "$dump" \
  || fail "pg_dump 失败（库 $DATABASE）"
[ -s "$dump" ] || fail "pg_dump 输出为空：$dump"

# 2) 立刻回读校验：-Fc 的 header 能被 pg_restore -l 解析才算有效。
objects="$(docker exec -i "$CONTAINER" pg_restore -l < "$dump" 2>/dev/null | grep -cvE '^;|^$' || true)"
[ "${objects:-0}" -gt 0 ] || fail "dump 无法被 pg_restore -l 解析，或里没有任何对象（$dump）"

# 3) 清单 + sha256（相对路径，恢复时可在快照目录内 sha256sum -c）。
image="$(docker inspect -f '{{.Config.Image}}' "$CONTAINER")"
server_version="$(docker exec "$CONTAINER" psql -U "$DBUSER" -d "$DATABASE" -tAc "show server_version" | tr -d ' \n')"
bytes="$(wc -c < "$dump" | tr -d ' ')"
{
  printf 'format=bot-harness-pg-backup/v1\n'
  printf 'container=%s\n' "$CONTAINER"
  printf 'database=%s\n' "$DATABASE"
  printf 'db_user=%s\n' "$DBUSER"
  printf 'server_version=%s\n' "$server_version"
  printf 'image=%s\n' "$image"
  printf 'created_at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'hot=%s\n' "$hot"
  printf 'bytes=%s\n' "$bytes"
  printf 'objects=%s\n' "$objects"
} > "$snapshot/MANIFEST.txt"

( cd "$snapshot" && sha256sum "$DATABASE.dump" ) > "$snapshot/SHA256SUMS"
[ -s "$snapshot/SHA256SUMS" ] || fail "SHA256SUMS 为空"
sync || true
trap - EXIT

if [ "$KEEP" -gt 0 ]; then
  mapfile -t snaps < <(find "$OUT" -mindepth 1 -maxdepth 1 -type d -name "$safe_db-*" -print | sort)
  total="${#snaps[@]}"
  if [ "$total" -gt "$KEEP" ]; then
    remove=$((total - KEEP))
    i=0
    while [ "$i" -lt "$remove" ]; do
      old="${snaps[$i]}"
      i=$((i + 1))
      if [ -f "$old/MANIFEST.txt" ] && grep -qx "database=$DATABASE" "$old/MANIFEST.txt"; then
        rm -rf -- "$old"
        printf "backup-pg: 轮转删除 %s\n" "$old"
      fi
    done
  fi
fi

printf "backup-pg: 完成 %s\n" "$snapshot"
printf "backup-pg: sha256 %s\n" "$(sha256sum "$dump" | cut -d' ' -f1)"
printf "backup-pg: objects=%s bytes=%s server=%s\n" "$objects" "$bytes" "$server_version"
printf "backup-pg: 恢复示例（先停机器人）：bash scripts/restore-pg.sh %s\n" "$snapshot"
