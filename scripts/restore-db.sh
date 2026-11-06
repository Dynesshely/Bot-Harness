#!/usr/bin/env bash
# restore-db.sh — verify and restore a snapshot produced by backup-db.sh.
#
# 安全性：先校验 SHA256SUMS（以及 SQLite magic），再检查目标卷是否正被运行中的
# 容器使用。目标容器在运行时默认拒绝覆盖（避免把正在写的 SQLite 换掉），
# 加 --force 才继续，并会明确提示先 docker compose stop。恢复会先清空目标卷内容，请确认
# 目标卷正确（脚本会打印源卷/目标卷）。
#
# 用法：
#   bash scripts/restore-db.sh <snapshot-dir>
#   bash scripts/restore-db.sh <snapshot-dir> --volume <target-volume>
#   bash scripts/restore-db.sh <snapshot-dir> --volume <target-volume> --force
#
# 退出码：0 成功；1 运行失败；2 参数/环境错误。
#
set -euo pipefail

SNAPSHOT=""
VOLUME=""
FORCE=0
IMAGE="${BOT_HARNESS_BACKUP_IMAGE:-alpine:3.22}"

die() { printf 'restore-db: %s
' "$1" >&2; exit 2; }
fail() { printf 'restore-db: %s
' "$1" >&2; exit 1; }

usage() {
  cat <<'USAGE'
用法: restore-db.sh <snapshot-dir> [选项]

  --volume NAME   恢复到哪个 docker 命名卷（默认取 MANIFEST 里记录的源卷）
  --force         目标容器正在运行时仍继续（先 docker compose stop bot-harness 更安全）
  --image IMAGE   helper 容器镜像（默认 alpine:3.22，需有 sh/cp/chown/rm）
  -h, --help      显示本帮助
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --volume|-v) [ "$#" -ge 2 ] || die "--volume 需要参数"; VOLUME="$2"; shift 2 ;;
    --force|-f)  FORCE=1; shift ;;
    --image)     [ "$#" -ge 2 ] || die "--image 需要参数"; IMAGE="$2"; shift 2 ;;
    -h|--help)   usage; exit 0 ;;
    -* )         die "未知参数：$1（--help 查看用法）" ;;
    *)
      [ -z "$SNAPSHOT" ] || die "只能指定一个快照目录（多余参数：$1）"
      SNAPSHOT="$1"; shift ;;
  esac
done

[ -n "$SNAPSHOT" ] || die "缺少快照目录（--help 查看用法）"
[ -d "$SNAPSHOT" ] || die "快照目录不存在：$SNAPSHOT"
SNAPSHOT="$(cd "$SNAPSHOT" && pwd)"

command -v docker >/dev/null 2>&1 || die "找不到 docker"
command -v sha256sum >/dev/null 2>&1 || die "找不到 sha256sum"

[ -f "$SNAPSHOT/MANIFEST.txt" ] || die "不是 backup-db.sh 的快照（缺 MANIFEST.txt）：$SNAPSHOT"
[ -f "$SNAPSHOT/SHA256SUMS" ] || die "快照缺 SHA256SUMS：$SNAPSHOT"
[ -f "$SNAPSHOT/data/bot.db" ] || die "快照缺 data/bot.db：$SNAPSHOT"

source_volume="$(sed -n 's/^volume=//p' "$SNAPSHOT/MANIFEST.txt" | head -n 1)"
method="$(sed -n 's/^method=//p' "$SNAPSHOT/MANIFEST.txt" | head -n 1)"
hot="$(sed -n 's/^hot=//p' "$SNAPSHOT/MANIFEST.txt" | head -n 1)"
owner="$(sed -n 's/^owner=//p' "$SNAPSHOT/MANIFEST.txt" | head -n 1)"
if [ -z "$VOLUME" ]; then
  VOLUME="$source_volume"
fi
[ -n "$VOLUME" ] || die "无法确定目标卷：MANIFEST 里没有 volume，且未传 --volume"

printf 'restore-db: 源卷=%s 目标卷=%s 方式=%s hot=%s
' "${source_volume:-?}" "$VOLUME" "${method:-?}" "${hot:-?}"
if [ "$hot" = "true" ]; then
  printf 'restore-db: 注意：这是一份热备（hot=true），恢复后请先做一次健康检查。\n'
fi

# 1) 校验清单。任何改动/截断都在写目标卷之前被发现。
printf 'restore-db: 校验 sha256 清单…
'
if ! ( cd "$SNAPSHOT" && sha256sum -c SHA256SUMS ); then
  fail "快照校验失败：不要用它覆盖任何卷（SHA256SUMS 不匹配）"
fi
head -c 15 "$SNAPSHOT/data/bot.db" | grep -q 'SQLite format 3' || fail "快照里的 bot.db 不是 SQLite 文件"

# 2) 目标卷正被运行中的容器使用时，默认拒绝：覆盖一个还在写的数据库等于制造损坏。
running="$(docker ps --filter "volume=$VOLUME" --format '{{.Names}}' 2>/dev/null || true)"
if [ -n "$running" ]; then
  if [ "$FORCE" -ne 1 ]; then
    fail "目标卷 $VOLUME 正被运行中的容器使用：$(printf '%s' "$running" | tr '
' ' ')。先执行 docker compose stop bot-harness，或确认无人使用后加 --force"
  fi
  printf 'restore-db: --force 已指定，继续覆盖正在被使用的卷 %s（容器：%s）
' "$VOLUME" "$(printf '%s' "$running" | tr '
' ' ')"
fi

# 3) 目标卷不存在则创建（演练/迁移到新卷很方便）。
created=0
if ! docker volume inspect "$VOLUME" >/dev/null 2>&1; then
  docker volume create "$VOLUME" >/dev/null
  created=1
  printf 'restore-db: 已创建目标卷 %s
' "$VOLUME"
fi

# 4) 恢复到目标卷：清空旧内容，逐文件复制（保留快照里的模式/属主），再按 OWNER 还原。
if ! docker run --rm --entrypoint sh   -v "$SNAPSHOT:/backup:ro" -v "$VOLUME:/data"   -e OWNER="$owner"   "$IMAGE" -c '
    set -eu
    rm -rf /data/* /data/.[!.]* 2>/dev/null || true
    cp -a /backup/data/. /data/
    if [ -n "$OWNER" ] && [ "$OWNER" != "0:0" ]; then
      chown -R "$OWNER" /data
    fi
    chmod -R u+rwX /data
  '; then
  fail "写入目标卷失败：$VOLUME（目标卷可能已被部分改写，请重新执行或从备份重建）"
fi

# 5) 回读校验：目标卷里的 bot.db 必须与快照逐字节一致。
want="$(sha256sum "$SNAPSHOT/data/bot.db" | cut -d' ' -f1)"
got="$(docker run --rm --entrypoint sh -v "$VOLUME:/data:ro" "$IMAGE" -c 'sha256sum /data/bot.db' | cut -d' ' -f1)"
[ "$got" = "$want" ] || fail "恢复后校验失败：目标卷 bot.db 的 sha256=$got，快照=$want"
printf 'restore-db: 恢复完成，bot.db sha256=%s
' "$got"

printf '恢复完成：%s -> 卷 %s（%s）
' "$SNAPSHOT" "$VOLUME" "$([ "$created" = 1 ] && echo 新建卷 || echo 覆盖已有卷)"
cat <<NEXT
下一步：
  docker compose up -d
  curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/api/health   # 期望 200
  docker exec bot-harness /app/bot-harness token                              # 读取后台口令
NEXT
