#!/usr/bin/env bash
# restore-pg.sh — verify and restore a snapshot produced by backup-pg.sh.
#
# 用途：把 backup-pg.sh 产出的 pg_dump -Fc 快照恢复进 bot-harness-postgres 里的
# 某个数据库。流程固定为「先校验、再检查占用、后恢复、最后抽查」：
#
#   1) sha256sum -c SHA256SUMS —— 摘要不符就在碰任何数据库之前退出；
#   2) 机器人容器在跑就**拒绝**（恢复会 drop 表，写到一半被读会得到半截数据），
#      打印容器名与「先 docker compose stop bot-harness」，确认无人使用后加 --force 才继续；
#   3) 目标库不存在则 createdb（演练/迁移到新库很方便）；
#   4) pg_restore --clean --if-exists --no-owner（清掉旧对象再灌，出错的逐条列出）；
#   5) 恢复后 psql 抽查：public schema 表数 + version()；
#   6) 打印下一步（docker compose up -d + /api/health）。
#
# 目标库默认取 MANIFEST 里记录的源库名；演练时用 --database 指到另一个库，
# 就不会碰到源库本身。
#
# 用法：
#   bash scripts/restore-pg.sh <snapshot-dir>
#   bash scripts/restore-pg.sh <snapshot-dir> --database bot_restore
#   bash scripts/restore-pg.sh <snapshot-dir> --database bot --force
#
# 环境变量：BOT_HARNESS_PG_CONTAINER / BOT_HARNESS_PG_USER /
#           BOT_HARNESS_PG_MAINTENANCE_DB / BOT_HARNESS_CONTAINER
#
# 退出码：0 成功；1 运行失败；2 参数/环境错误。
set -euo pipefail

SNAPSHOT=""
CONTAINER="${BOT_HARNESS_PG_CONTAINER:-bot-harness-postgres}"
TARGET=""
DBUSER="${BOT_HARNESS_PG_USER:-bot}"
MAINT_DB="${BOT_HARNESS_PG_MAINTENANCE_DB:-postgres}"
BOT_CONTAINER="${BOT_HARNESS_CONTAINER:-bot-harness}"
FORCE=0

die()  { printf 'restore-pg: %s\n' "$1" >&2; exit 2; }
fail() { printf 'restore-pg: %s\n' "$1" >&2; exit 1; }

usage() {
  cat <<'USAGE'
用法: restore-pg.sh <snapshot-dir> [选项]

  --database NAME       恢复到哪个库（默认 = MANIFEST 里的源库名；不存在会自动创建）
  --container NAME      PostgreSQL 容器（默认 bot-harness-postgres）
  --user NAME           连接用户（默认 bot）
  --maintenance-db NAME 建库/查库用的维护库（默认 postgres）
  --force               机器人容器正在运行时仍继续（强烈建议先 docker compose stop bot-harness）
  -h, --help            显示本帮助

环境变量：BOT_HARNESS_PG_CONTAINER / BOT_HARNESS_PG_USER /
          BOT_HARNESS_PG_MAINTENANCE_DB / BOT_HARNESS_CONTAINER
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --database|-d)        [ "$#" -ge 2 ] || die "--database 需要参数"; TARGET="$2"; shift 2 ;;
    --container|-c)       [ "$#" -ge 2 ] || die "--container 需要参数"; CONTAINER="$2"; shift 2 ;;
    --user|-u)            [ "$#" -ge 2 ] || die "--user 需要参数"; DBUSER="$2"; shift 2 ;;
    --maintenance-db|-m)  [ "$#" -ge 2 ] || die "--maintenance-db 需要参数"; MAINT_DB="$2"; shift 2 ;;
    --force|-f)           FORCE=1; shift ;;
    -h|--help)            usage; exit 0 ;;
    -* )                  die "未知参数：$1（--help 查看用法）" ;;
    *)
      [ -z "$SNAPSHOT" ] || die "只能指定一个快照目录（多余参数：$1）"
      SNAPSHOT="$1"; shift ;;
  esac
done

[ -n "$SNAPSHOT" ] || die "缺少快照目录（--help 查看用法）"
[ -d "$SNAPSHOT" ] || die "快照目录不存在：$SNAPSHOT"
SNAPSHOT="$(cd "$SNAPSHOT" && pwd)"
[ -n "$CONTAINER" ] || die "--container 不能为空"
[ -n "$DBUSER" ] || die "--user 不能为空"
[ -n "$MAINT_DB" ] || die "--maintenance-db 不能为空"

command -v docker >/dev/null 2>&1 || die "找不到 docker"
command -v sha256sum >/dev/null 2>&1 || die "找不到 sha256sum"

[ -f "$SNAPSHOT/MANIFEST.txt" ] || die "不是 backup-pg.sh 的快照（缺 MANIFEST.txt）：$SNAPSHOT"
[ -f "$SNAPSHOT/SHA256SUMS" ] || die "快照缺 SHA256SUMS：$SNAPSHOT"

source_db="$(sed -n 's/^database=//p' "$SNAPSHOT/MANIFEST.txt" | head -n 1)"
created_at="$(sed -n 's/^created_at_utc=//p' "$SNAPSHOT/MANIFEST.txt" | head -n 1)"
hot="$(sed -n 's/^hot=//p' "$SNAPSHOT/MANIFEST.txt" | head -n 1)"
server_version="$(sed -n 's/^server_version=//p' "$SNAPSHOT/MANIFEST.txt" | head -n 1)"

# dump 文件按 MANIFEST 里的源库名找；找不到就退回快照里唯一那个 *.dump。
dump=""
if [ -n "$source_db" ] && [ -f "$SNAPSHOT/$source_db.dump" ]; then
  dump="$SNAPSHOT/$source_db.dump"
else
  mapfile -t found < <(find "$SNAPSHOT" -maxdepth 1 -type f -name '*.dump' -print)
  [ "${#found[@]}" -eq 1 ] || die "快照里找不到唯一的 *.dump（找到 ${#found[@]} 个）：$SNAPSHOT"
  dump="${found[0]}"
  [ -n "$source_db" ] || source_db="$(basename "$dump" .dump)"
fi
[ -n "$source_db" ] || die "无法从 MANIFEST 或 dump 文件名确定源库名"
[ -n "$TARGET" ] || TARGET="$source_db"
# 库名会被拼进 SQL（pg_database 查询与 create database），先按标识符规则挡一遍，
# 免得一个含引号的 --database 把语句拆坏、或在维护库里执行预期外的语句。
case "$TARGET" in
  -*|*[!A-Za-z0-9_]*) die "--database 只能包含字母、数字、下划线，且不能以 - 开头：$TARGET" ;;
esac
[ "${#TARGET}" -le 63 ] || die "--database 超过 PostgreSQL 标识符上限 63 字节：$TARGET"

dump_bytes="$(wc -c < "$dump" | tr -d ' ')"
printf 'restore-pg: 快照=%s\n' "$SNAPSHOT"
printf 'restore-pg: 源库=%s 目标库=%s 容器=%s 用户=%s\n' "$source_db" "$TARGET" "$CONTAINER" "$DBUSER"
printf 'restore-pg: dump=%s (%s bytes) created_at_utc=%s server_version=%s hot=%s\n' \
  "$(basename "$dump")" "$dump_bytes" "${created_at:-?}" "${server_version:-?}" "${hot:-?}"
if [ "$hot" = "true" ]; then
  printf 'restore-pg: 注意：这是一份热备（hot=true），恢复后请先做一次健康检查。\n'
fi

# 1) 校验清单。任何改动/截断都在写库之前被发现。
printf 'restore-pg: 校验 sha256 清单…\n'
if ! ( cd "$SNAPSHOT" && sha256sum -c SHA256SUMS ); then
  fail "快照校验失败：不要用它恢复任何库（SHA256SUMS 不匹配或被截断）"
fi

# 2) 目标容器必须在跑（pg_restore / psql 都要 docker exec 进去）。
docker inspect "$CONTAINER" >/dev/null 2>&1 || die "容器不存在：$CONTAINER"
[ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER")" = "true" ] \
  || die "容器没在运行：$CONTAINER（先 docker compose up -d postgres）"

# 3) 机器人容器在跑就默认拒绝：pg_restore --clean 会 drop 表，机器人正读着就会拿到
#    半截数据 / 报错，甚至把空表当真。确认无人使用才加 --force。
bot_running="$(docker ps --filter "name=^${BOT_CONTAINER}$" --format '{{.Names}}' 2>/dev/null || true)"
if [ -n "$bot_running" ]; then
  if [ "$FORCE" -ne 1 ]; then
    fail "机器人容器正在运行：$bot_running —— 恢复会 drop 并重建表，正在跑的机器人会读到半截数据。先执行 docker compose stop bot-harness，确认已停后重跑；确定无人使用则加 --force"
  fi
  printf 'restore-pg: --force 已指定，继续在机器人容器 %s 运行时恢复（目标库 %s）\n' "$bot_running" "$TARGET"
fi

psql_do() {
  local db="$1"; shift
  docker exec -i "$CONTAINER" psql -U "$DBUSER" -d "$db" "$@"
}

# 4) 目标库不存在则创建；已存在时先报一下将被覆盖的表数，别让人误伤生产库。
printf 'restore-pg: 检查目标库 %s\n' "$TARGET"
exists="$(psql_do "$MAINT_DB" -tAc "select 1 from pg_database where datname = '$TARGET'" 2>/dev/null | tr -d ' \n' || true)"
created=0
if [ "$exists" = "1" ] || [ "$exists" = "t" ]; then
  existing_tables="$(psql_do "$TARGET" -tAc \
    "select count(*) from information_schema.tables where table_schema = 'public'" 2>/dev/null | tr -d ' \n' || true)"
  printf 'restore-pg: 目标库已存在（public 表数=%s），将被 --clean 覆盖\n' "${existing_tables:-?}"
else
  psql_do "$MAINT_DB" -tAc "create database \"$TARGET\"" >/dev/null \
    || fail "创建目标库失败：$TARGET"
  created=1
  printf 'restore-pg: 已创建目标库 %s\n' "$TARGET"
fi

# 5) 恢复。--clean --if-exists 先 drop 已有对象再灌；--no-owner 让库归连接用户所有
#    （快照里的属主与目标环境无关）。
log="$(mktemp -t restore-pg-XXXXXX.log)"
trap 'rm -f "$log"' EXIT
set +e
docker exec -i "$CONTAINER" pg_restore -U "$DBUSER" -d "$TARGET" --clean --if-exists --no-owner \
  < "$dump" > "$log" 2>&1
rc=$?
set -e

# pg_restore 返回码：0 = 干净；1 = 有错误但跑完了；>1 = 致命。
# 出现真正的 "pg_restore: error:" 就当失败，不靠返回码掩盖问题。
# 两道判断都要有：pg_restore 只把「没被 --if-exists 兜住的」错误算进返回码。
errors="$(grep -c 'pg_restore: error:' "$log" 2>/dev/null || true)"
if [ "$rc" -gt 1 ]; then
  printf 'restore-pg: pg_restore 退出码 %s\n' "$rc" >&2
  tail -n 40 "$log" >&2 || true
  fail "恢复失败（退出码 $rc）：$TARGET"
fi
if [ "${errors:-0}" -gt 0 ]; then
  printf 'restore-pg: 恢复过程中有 %s 条错误：\n' "${errors:-0}" >&2
  grep 'pg_restore: error:' "$log" | head -n 20 >&2 || true
  fail "恢复未干净完成：$TARGET（目标库可能已被部分改写，检查上面的错误后重跑或从备份重建）"
fi
if [ "$rc" -ne 0 ]; then
  printf 'restore-pg: pg_restore 退出码 %s，但没有 error 行（warnings 可忽略）\n' "$rc" >&2
  tail -n 10 "$log" >&2 || true
fi

# 6) 恢复后抽查：表数与版本都从目标库里现查，不是抄 MANIFEST。
table_count="$(psql_do "$TARGET" -tAc \
  "select count(*) from information_schema.tables where table_schema = 'public'" | tr -d ' \n')"
pg_version="$(psql_do "$TARGET" -tAc 'select version()' | tr -d '\n')"
[ "${table_count:-0}" -gt 0 ] || fail "恢复后目标库 public schema 里没有任何表：$TARGET"
printf 'restore-pg: 恢复完成 %s -> %s（%s）\n' "$source_db" "$TARGET" \
  "$([ "$created" = 1 ] && echo 新建库 || echo 覆盖已有库)"
printf 'restore-pg: public 表数=%s\n' "$table_count"
printf 'restore-pg: version=%s\n' "$pg_version"

cat <<NEXT

下一步：
  docker compose up -d
  curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/api/health   # 期望 200
  docker exec bot-harness /app/bot-harness token                              # 读取后台口令
NEXT
