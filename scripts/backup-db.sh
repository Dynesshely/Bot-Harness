#!/usr/bin/env bash
# backup-db.sh — schedule-safe backup of the bot-harness SQLite volume.
#
# 用途：把命名卷里的 bot.db 抓成一份带 UTC 时间戳、sha256 清单、可轮转的快照，
# 供 restore-db.sh 校验后恢复。默认只读命名卷，不停止、不重启 bot-harness。
#
# 一致性边界（务必先读）
#   1. 若 helper 镜像里有 sqlite3（用 --image 指定，或默认镜像自带），使用 SQLite
#      在线备份 API：sqlite3 DB ".backup ..."，得到事务一致的单文件，机器人运行中
#      也可以安全备份。
#   2. 否则退化为「复制 bot.db + bot.db-wal + bot.db-shm」。WAL 模式下这三个文件
#      合起来才是数据库；在机器人持续写入时逐个复制是**热拷贝**，可能抓到不同
#      瞬间的三个文件，恢复时 SQLite 需要做恢复/回滚。要求强一致的文件级快照时：
#          docker compose stop bot-harness
#          bash scripts/backup-db.sh
#          docker compose up -d
#      或在镜像里提供 sqlite3 后用 --image 走在线备份。
#   3. MANIFEST.txt 会记录 method=sqlite-backup|file-copy 与 hot=true|false，
#      恢复时先看这一行再决定是否需要额外校验。
#
# 快照布局（输出目录默认在仓库外，绝不进 git）：
#   <out>/<volume>-<UTC timestamp>/
#     MANIFEST.txt      volume / 时间 / 方式 / 源文件属主 / 文件清单
#     SHA256SUMS        sha256sum -c 可直接校验
#     OWNER             原始 uid:gid（恢复时还原卷内文件属主）
#     data/bot.db[.db-wal|.db-shm]
#
# 用法：
#   bash scripts/backup-db.sh
#   bash scripts/backup-db.sh --volume bot-harness_botdata --out "$HOME/bot-harness-backups" --keep 7
#   bash scripts/backup-db.sh --image <带 sqlite3 的镜像>   # 走在线备份
#
# 退出码：0 成功；1 运行失败；2 参数/环境错误。
#
set -euo pipefail

VOLUME="${BOT_HARNESS_VOLUME:-bot-harness_botdata}"
OUT="${BOT_HARNESS_BACKUP_DIR:-$HOME/bot-harness-backups}"
KEEP=7
IMAGE="${BOT_HARNESS_BACKUP_IMAGE:-alpine:3.22}"

die() { printf 'backup-db: %s
' "$1" >&2; exit 2; }
fail() { printf 'backup-db: %s
' "$1" >&2; exit 1; }

usage() {
  cat <<'USAGE'
用法: backup-db.sh [选项]

  --volume NAME   要备份的 docker 命名卷（默认 bot-harness_botdata）
  --out DIR       快照输出目录（默认 $HOME/bot-harness-backups，仓库外）
  --keep N        只保留最近 N 份快照（默认 7；0 = 不轮转）
  --image IMAGE   helper 容器镜像（默认 alpine:3.22，需有 sh/cp/chown；
                  若带 sqlite3 则自动改用在线备份）
  -h, --help      显示本帮助

环境变量：BOT_HARNESS_VOLUME / BOT_HARNESS_BACKUP_DIR / BOT_HARNESS_BACKUP_IMAGE
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --volume|-v) [ "$#" -ge 2 ] || die "--volume 需要参数"; VOLUME="$2"; shift 2 ;;
    --out|-o)    [ "$#" -ge 2 ] || die "--out 需要参数"; OUT="$2"; shift 2 ;;
    --keep|-k)   [ "$#" -ge 2 ] || die "--keep 需要参数"; KEEP="$2"; shift 2 ;;
    --image)     [ "$#" -ge 2 ] || die "--image 需要参数"; IMAGE="$2"; shift 2 ;;
    -h|--help)   usage; exit 0 ;;
    *)           die "未知参数：$1（--help 查看用法）" ;;
  esac
done

case "$KEEP" in
  ''|*[!0-9]*) die "--keep 必须是非负整数，实际：$KEEP" ;;
esac
[ -n "$VOLUME" ] || die "--volume 不能为空"
[ -n "$OUT" ] || die "--out 不能为空"

command -v docker >/dev/null 2>&1 || die "找不到 docker"
command -v sha256sum >/dev/null 2>&1 || die "找不到 sha256sum"
docker volume inspect "$VOLUME" >/dev/null 2>&1 || die "命名卷不存在：$VOLUME"

# 源卷当前是否被运行中的容器使用（只用于把一致性风险写进 MANIFEST，不阻止备份）。
volume_users="$(docker ps --filter "volume=$VOLUME" --format '{{.Names}}' 2>/dev/null | tr '
' ' ' || true)"
hot=false
if [ -n "$volume_users" ]; then
  hot=true
fi

mkdir -p "$OUT"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
safe_volume="${VOLUME//[^A-Za-z0-9._-]/_}"
snapshot="$OUT/$safe_volume-$stamp"
if [ -e "$snapshot" ]; then
  fail "快照目录已存在（同一秒内重复执行？）：$snapshot"
fi

# 先探测 helper 镜像是否带 sqlite3；有则用在线备份 API，否则复制 db+wal+shm。
method=file-copy
if docker run --rm --entrypoint sh "$IMAGE" -c 'command -v sqlite3 >/dev/null 2>&1'; then
  method=sqlite-backup
fi
if [ "$hot" = "true" ] && [ "$method" = "file-copy" ]; then
  printf 'backup-db: 警告：源卷正被运行中的容器使用，file-copy 模式是热拷贝（恢复时可能需要 SQLite 回滚）。强一致请先 docker compose stop bot-harness，或用带 sqlite3 的 --image。\n' >&2
fi

mkdir -p "$snapshot/data"
cleanup() { rm -rf "$snapshot"; }
trap cleanup EXIT

printf 'backup-db: 卷=%s 方式=%s（hot=%s）
' "$VOLUME" "$method" "$hot"

if [ "$method" = "sqlite-backup" ]; then
  docker run --rm --entrypoint sh     -v "$VOLUME:/data:ro" -v "$snapshot:/backup"     -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)"     "$IMAGE" -c '
      set -eu
      test -f /data/bot.db
      stat -c "%u:%g" /data/bot.db > /backup/OWNER
      sqlite3 /data/bot.db ".backup /backup/data/bot.db"
      chown -R "$HOST_UID:$HOST_GID" /backup
      chmod -R u+rwX,go+rX /backup
    ' || fail "在线备份失败（sqlite3 .backup）"
else
  docker run --rm --entrypoint sh     -v "$VOLUME:/data:ro" -v "$snapshot:/backup"     -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)"     "$IMAGE" -c '
      set -eu
      test -f /data/bot.db
      stat -c "%u:%g" /data/bot.db > /backup/OWNER
      cp -a /data/bot.db /backup/data/
      for f in /data/bot.db-wal /data/bot.db-shm; do
        if [ -e "$f" ]; then cp -a "$f" /backup/data/; fi
      done
      chown -R "$HOST_UID:$HOST_GID" /backup
      chmod -R u+rwX,go+rX /backup
    ' || fail "文件级备份失败（bot.db / -wal / -shm）"
fi

db="$snapshot/data/bot.db"
[ -s "$db" ] || fail "快照里没有有效的 bot.db：$db"
head -c 15 "$db" | grep -q 'SQLite format 3' || fail "bot.db 不是 SQLite 文件（magic 不符）：$db"
[ -s "$snapshot/OWNER" ] || fail "缺少 OWNER（无法在恢复时还原文件属主）"

# 清单：先写入来源与一致性信息，再逐文件 sha256（相对路径，便于 restore 校验）。
{
  printf 'format=bot-harness-db-backup/v1
'
  printf 'volume=%s
' "$VOLUME"
  printf 'created_at_utc=%s
' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'method=%s
' "$method"
  printf 'hot=%s
' "$hot"
  printf 'volume_users=%s
' "$(printf '%s' "$volume_users" | sed 's/[[:space:]]*$//')"
  printf 'helper_image=%s
' "$IMAGE"
  printf 'docker_server=%s
' "$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)"
  printf 'owner=%s
' "$(cat "$snapshot/OWNER")"
  printf 'files:
'
  ( cd "$snapshot" && find data -type f -print | sort | while IFS= read -r f; do
      printf '  %s %s bytes
' "$f" "$(wc -c < "$f" | tr -d ' ')"
    done )
} > "$snapshot/MANIFEST.txt"

( cd "$snapshot" && find data -type f -print | sort | while IFS= read -r f; do
    sha256sum "$f"
  done ) > "$snapshot/SHA256SUMS"
[ -s "$snapshot/SHA256SUMS" ] || fail "SHA256SUMS 为空"

# 触发一次落盘，确保快照目录内容完整可见。
sync || true

trap - EXIT

# 轮转：只删除本脚本生成的（带 MANIFEST.txt 且 volume 匹配）目录。
if [ "$KEEP" -gt 0 ]; then
  mapfile -t snapshots < <(find "$OUT" -mindepth 1 -maxdepth 1 -type d -name "$safe_volume-*" -print | sort)
  total="${#snapshots[@]}"
  if [ "$total" -gt "$KEEP" ]; then
    remove=$((total - KEEP))
    i=0
    while [ "$i" -lt "$remove" ]; do
      old="${snapshots[$i]}"
      i=$((i + 1))
      if [ -f "$old/MANIFEST.txt" ] && grep -qx "volume=$VOLUME" "$old/MANIFEST.txt"; then
        rm -rf -- "$old"
        printf 'backup-db: 轮转删除 %s
' "$old"
      fi
    done
  fi
fi

printf 'backup-db: 完成 %s
' "$snapshot"
printf 'backup-db: sha256 %s
' "$(sha256sum "$snapshot/data/bot.db" | cut -d' ' -f1)"
printf 'backup-db: 恢复示例（先停机器人）：docker compose stop bot-harness && bash scripts/restore-db.sh %s
' "$snapshot"
