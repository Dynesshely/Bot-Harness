#!/usr/bin/env bash
#
# privacy-scan.sh — 提交前 / CI 的隐私扫描。
#
# 为什么存在：本仓库的硬规则是「真实主机名 / 内网 IP / 网段 / 代理 / 个人绝对路径 /
# 密钥一律只留在仓库外，绝不进仓库文件（代码、文档、测试、提交信息都算）」。
# 人容易忘，所以交给脚本在 CI 里盯着。
#
# 规则（命中即失败，退出码 1）：
#   rfc1918       真实私网 IPv4 字面量（10/8、172.16/12、192.168/16）
#   home-path     个人主目录绝对路径 /home/<user>
#   private-key   PEM 私钥头（仓库只允许放公开证书）
#   secret-env    ADMIN_TOKEN= / QQ_APP_SECRET= / OPENAI_API_KEY= 后面跟着真实值
#   token-literal sk-… / ghp_… / xoxb-… / AKIA… 形态的密钥
#   internal-host 真实内网域名（.services.local / *.lan / *.local 的非占位形式）
#   proxy-creds   带用户名口令的代理 URL
#
# 允许规则（写清楚，免得为了过扫描去改掉有价值的测试）：
#   - 占位符一律放行：10.x.x.x / 192.168.x.x / 10.0.0.0/8 / 172.16.0.0/12 /
#     192.168.0.0/16 / <局域网IP> / <ip> / one-api.internal.example / xxx.local /
#     example.local / <user> / ${VAR} 形式的展开
#   - 回环与未指定地址不算泄漏：127.0.0.0/8、0.0.0.0、::1、localhost
#   - 测试代码里的 RFC1918 示例地址放行：路径匹配 *_test.go、*/testdata/*、
#     *.test.ts、*.spec.ts 时，私有 IPv4 视为 SSRF 防线的测试样本
#   - 测试夹具里的假密钥放行：只在测试路径下，且 token 里带 fake/dummy/placeholder/
#     should-never/leftover/example/sample 这类夹具标记（真 key 是随机串，不会长这样）；
#     其余规则（/home/、内网域名、带口令代理）在测试里照样生效
#   - 被 .gitignore 排除的文件不扫（尤其 .env）：真实值本来就该只留在本机/容器里
#
# 用法：
#   scripts/privacy-scan.sh             # 扫 tracked + 未被忽略了未跟踪文件
#   scripts/privacy-scan.sh --verbose   # 额外列出「被允许」的命中，便于核对规则
#   scripts/privacy-scan.sh --staged    # 只扫 git 暂存区里新增的行（提交前自查）
#
set -uo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 2

VERBOSE=0
STAGED=0
for arg in "$@"; do
  case "$arg" in
    -v|--verbose) VERBOSE=1 ;;
    --staged) STAGED=1 ;;
    -h|--help) usage=1 ;;
    *) printf 'privacy-scan: 未知参数 %s（--help 看用法）\n' "$arg" >&2; exit 2 ;;
  esac
done
if [ "${usage:-0}" = 1 ]; then
  sed -n '2,45p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit 0
fi

findings=0
allowed=0
scanned=0

# clip 把命中行截短，避免一条超长压缩产物刷屏。
clip() {
  local t="$1"
  t="${t//$'\t'/ }"
  if [ "${#t}" -gt 180 ]; then printf '%s…' "${t:0:180}"; else printf '%s' "$t"; fi
}

hit() { # rule path line text
  findings=$((findings + 1))
  printf '  x %-13s %s:%s\n      %s\n' "$1" "$2" "$3" "$(clip "$4")"
}

hit_secret() { # rule path line  —— 秘密本身不打印，落进 CI 日志等于二次泄漏
  findings=$((findings + 1))
  printf '  x %-13s %s:%s\n      （值已隐藏：不得写进仓库，改用占位符或仓库外私有记录）\n' "$1" "$2" "$3"
}

is_test_path() {
  case "$1" in
    *_test.go|*.test.ts|*.spec.ts|*.test.js|*.spec.js) return 0 ;;
    */testdata/*|*/tests/*|*/test/*) return 0 ;;
  esac
  return 1
}

# 单个 IPv4/CIDR token 是否属于放行集合（占位符与笼统网段）。
ip_token_allowed() {
  case "$1" in
    10.x.x.x|192.168.x.x|"<局域网IP>"|"<ip>"|"<LAN-IP>") return 0 ;;
    10.0.0.0/8|172.16.0.0/12|192.168.0.0/16) return 0 ;;
    127.*|0.0.0.0) return 0 ;;
  esac
  return 1
}

# extract_ipv4 从一行里抽出所有点分 IPv4（带可选 /前缀）。
extract_ipv4() {
  printf '%s\n' "$1" | grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}(/[0-9]{1,2})?' || true
}

# extract_private_ipv4 只保留 RFC1918 的那些。
extract_private_ipv4() {
  local tok
  for tok in $(extract_ipv4 "$1"); do
    case "$tok" in
      10.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*) printf '%s\n' "$tok" ;;
    esac
  done
}

domain_label_is_placeholder() {
  local d="$1" label="${1%%.*}"
  case "$label" in
    xxx|example|your|your-domain|placeholder|one-api|my|some|demo|test) return 0 ;;
  esac
  case "$d" in
    internal.example|*.internal.example) return 0 ;;
    env.local) return 0 ;;
    # 裸后缀本身出现在文档/规则说明里不算泄漏；真实泄漏一定会带子域前缀。
    services.local) return 0 ;;
  esac
  return 1
}

# is_fixture_token 判断一个 token 是否明显是测试夹具（只在测试路径下才用它放行）。
is_fixture_token() {
  printf '%s\n' "$1" | grep -qiE '(fake|dummy|placeholder|redacted|example|sample|should-never|leftover|not-a-real|notreal|xxx|changeme)'
}

check_line() { # path line text
  local path="$1" line="$2" text="$3" tok

  # 1. RFC1918 私网地址
  for tok in $(extract_private_ipv4 "$text"); do
    if ip_token_allowed "$tok"; then
      allowed=$((allowed + 1))
      [ "$VERBOSE" = 1 ] && printf '  · allowed rfc1918      %s:%s  %s\n' "$path" "$line" "$tok"
      continue
    fi
    if is_test_path "$path"; then
      allowed=$((allowed + 1))
      [ "$VERBOSE" = 1 ] && printf '  · allowed rfc1918(test) %s:%s  %s\n' "$path" "$line" "$tok"
      continue
    fi
    hit rfc1918 "$path" "$line" "$text"
    break
  done

  # 2. 个人主目录绝对路径
  for tok in $(printf '%s\n' "$text" | grep -oE '/home/[A-Za-z0-9._-]+' || true); do
    case "$tok" in
      /home/user|/home/username|/home/you|/home/youruser|/home/example|/home/app) 
        allowed=$((allowed + 1))
        [ "$VERBOSE" = 1 ] && printf '  · allowed home-path    %s:%s  %s\n' "$path" "$line" "$tok"
        continue ;;
    esac
    hit home-path "$path" "$line" "$text"
    break
  done

  # 3. PEM 私钥
  if printf '%s\n' "$text" | grep -qE -- '-----BEGIN ([A-Z]+ )?PRIVATE KEY-----'; then
    hit_secret private-key "$path" "$line"
  fi

  # 4. .env 风格的密钥赋值
  if printf '%s\n' "$text" | grep -qE '^[[:space:]]*(ADMIN_TOKEN|QQ_APP_SECRET|OPENAI_API_KEY|OPENAI_BASE_URL|QQ_APP_TOKEN)[[:space:]]*=[[:space:]]*[^[:space:]]+'; then
    # 占位符放行：<...>、${...}、xxx、change-me、your-…
    if printf '%s\n' "$text" | grep -qE '=[[:space:]]*(<[^>]*>|\$\{[^}]*\}|xxx|changeme|change-me|your-.*|placeholder|redacted|dummy|example)$'; then
      allowed=$((allowed + 1))
      [ "$VERBOSE" = 1 ] && printf '  · allowed secret-env   %s:%s\n' "$path" "$line"
    else
      hit_secret secret-env "$path" "$line"
    fi
  fi

  # 5. 常见密钥字面量（测试夹具里的 sk-xxxx 只要带 fake/leftover 等标记就放行）
  for tok in $(printf '%s\n' "$text" | grep -oE '\b(sk-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9]{20,}|gho_[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16})\b' || true); do
    if is_test_path "$path" && is_fixture_token "$tok"; then
      allowed=$((allowed + 1))
      [ "$VERBOSE" = 1 ] && printf '  · allowed token-literal %s:%s  %s\n' "$path" "$line" "$tok"
      continue
    fi
    hit_secret token-literal "$path" "$line"
    break
  done

  # 6. 真实内网域名
  for tok in $(printf '%s\n' "$text" | grep -oiE '[A-Za-z0-9][A-Za-z0-9._-]*\.(services\.local|lan|local)' || true); do
    if domain_label_is_placeholder "$tok"; then
      allowed=$((allowed + 1))
      [ "$VERBOSE" = 1 ] && printf '  · allowed internal-host %s:%s  %s\n' "$path" "$line" "$tok"
      continue
    fi
    hit internal-host "$path" "$line" "$tok"
    break
  done

  # 7. 带用户名口令的代理 URL（user:pass@ 这种示例口令放行）
  for tok in $(printf '%s\n' "$text" | grep -oE 'https?://[^/[:space:]:@]+:[^/[:space:]@]+@' || true); do
    if printf '%s\n' "$tok" | grep -qE '^https?://(user|username|your-user|xxx|\$\{[^}]*\}):(pass|password|secret|token|xxx|change-me|placeholder|<pass>|<password>|\$\{[^}]*\})@$'; then
      allowed=$((allowed + 1))
      [ "$VERBOSE" = 1 ] && printf '  · allowed proxy-creds  %s:%s  %s\n' "$path" "$line" "$tok"
      continue
    fi
    hit_secret proxy-creds "$path" "$line"
    break
  done
}

# 快速预筛：任何一条规则都不可能命中的行直接跳过（正则合并，误报无所谓，后面会复核）。
CANDIDATE_RE='([0-9]{1,3}\.){3}[0-9]{1,3}|/home/[A-Za-z0-9._-]+|-----BEGIN [A-Z ]*PRIVATE KEY-----|^[[:space:]]*(ADMIN_TOKEN|QQ_APP_SECRET|OPENAI_API_KEY|OPENAI_BASE_URL|QQ_APP_TOKEN)[[:space:]]*=|sk-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9]{20,}|xox[baprs]-|AKIA[0-9A-Z]{16}|[A-Za-z0-9][A-Za-z0-9._-]*\.(services\.local|lan|local)|https?://[^/[:space:]:@]+:[^/[:space:]@]+@'

list_files() {
  if command -v git >/dev/null 2>&1 && git rev-parse --git-dir >/dev/null 2>&1; then
    git ls-files -z --cached --others --exclude-standard
  else
    find . \
      \( -name .git -o -name node_modules -o -name data -o -name '*.db' -o -name '*.db-wal' -o -name '*.db-shm' \) -prune -o \
      -type f -print0
  fi
}

scan_staged() {
  if ! command -v git >/dev/null 2>&1 || ! git rev-parse --git-dir >/dev/null 2>&1; then
    printf 'privacy-scan: --staged 需要 git 仓库\n' >&2
    exit 2
  fi
  local path="" line_no=0 raw
  while IFS= read -r raw; do
    case "$raw" in
      "+++ b/"*) path="${raw#+++ b/}" ;;
      "+++"*|"---"*) : ;;
      @@*) continue ;;
      +*)
        line_no=$((line_no + 1))
        local text="${raw#+}"
        [ -n "$path" ] || continue
        printf '%s\n' "$text" | grep -qE -- "$CANDIDATE_RE" || continue
        check_line "$path" "$line_no" "$text"
        ;;
    esac
  done < <(git diff --cached --unified=0 --no-color -- . 2>/dev/null)
}

if [ "$STAGED" = 1 ]; then
  printf '==> privacy-scan（仅暂存区新增行）\n'
  scan_staged
else
  files=()
  while IFS= read -r -d '' f; do
    [ -f "$f" ] && files+=("$f")
  done < <(list_files)
  scanned=${#files[@]}
  printf '==> privacy-scan：%d 个文件\n' "$scanned"
  if [ "$scanned" -gt 0 ]; then
    while IFS= read -r raw; do
      [ -z "$raw" ] && continue
      path="${raw%%:*}"
      rest="${raw#*:}"
      line="${rest%%:*}"
      text="${rest#*:}"
      check_line "$path" "$line" "$text"
    done < <(grep -HnIE -- "$CANDIDATE_RE" "${files[@]}" 2>/dev/null || true)
  fi
fi

printf '\n'
if [ "$findings" -gt 0 ]; then
  printf 'FAIL: 发现 %d 处隐私/密钥问题（允许规则见脚本头部注释）\n' "$findings"
  printf '      真实值请留在仓库外，仓库里改用占位符（10.x.x.x / <局域网IP> / ~ / example.com）。\n'
  exit 1
fi
printf 'PASS: 未发现隐私/密钥泄漏'
if [ "$STAGED" != 1 ]; then printf '（%d 个文件）' "$scanned"; fi
if [ "$allowed" -gt 0 ]; then printf '；放行 %d 处示例/占位值' "$allowed"; fi
printf '。\n'
exit 0
