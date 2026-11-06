# 不使用 `# syntax=docker/dockerfile:1`：本文件只用普通语法，去掉后 buildkit 用内置
# 前端解析，构建期不必再连 Docker Hub 拉 dockerfile 镜像（内网环境常常拉不到）。

# Base images are pinned to a multi-arch index digest so a rebuild uses the
# exact bytes that were reviewed (the tag is kept for readability). Refresh the
# digest deliberately, together with CI, when upgrading.
#
# --- stage 1: build the admin SPA -------------------------------------------
FROM node:25-alpine@sha256:bdf2cca6fe3dabd014ea60163eca3f0f7015fbd5c7ee1b0e9ccb4ced6eb02ef4 AS web
# Pinned explicitly: corepack's default pnpm resolution is not deterministic
# across node image revisions.
RUN npm install -g pnpm@11.25.0
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml* ./
# --frozen-lockfile: CI (and releases) must fail when the lockfile drifted
# instead of silently resolving a new dependency tree.
RUN pnpm install --frozen-lockfile || (sleep 3 && pnpm install --frozen-lockfile)
COPY web/ ./
RUN pnpm build

# --- stage 2: build the Go binary -------------------------------------------
FROM golang:1.27-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
ARG VERSION=0.1.0
# Module proxy is overridable for mirrors / locked-down networks. The default
# matches the Go toolchain; "direct" keeps a working fallback when
# proxy.golang.org is unreachable.
ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux
COPY go.mod go.sum ./
# 代理偶尔会在 TLS 握手中途断开（EOF），重试一次比整轮构建失败划算。
RUN GOPROXY="${GOPROXY}" go mod download || (sleep 3 && GOPROXY="${GOPROXY}" go mod download)
COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go ./web/
COPY --from=web /src/web/dist ./web/dist
RUN go build -trimpath -tags netgo \
      -ldflags "-s -w -X github.com/dynesshely/bot-harness/internal/config.Version=${VERSION}" \
      -o /out/bot-harness ./cmd/bot-harness

# --- stage 3: minimal runtime ------------------------------------------------
# No apk packages are installed on purpose: the Alpine base already ships
# ca-certificates, the bot embeds the IANA tzdata, exposes its own healthcheck
# subcommand, and Go handles SIGTERM directly. That keeps the runtime image
# buildable even where apk's HTTPS fetches are blocked by a proxy.
FROM alpine:3.21@sha256:87a4361f6c819618a6bafa2ed5228714cb0bb22195d2b49c95eb26799990128a AS runtime
ARG VERSION=0.1.0
LABEL org.opencontainers.image.title="bot-harness" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.description="QQ group AI chat bot with a built-in admin panel" \
      org.opencontainers.image.source="https://github.com/Dynesshely/Bot-Harness" \
      org.opencontainers.image.licenses="Apache-2.0"

RUN adduser -D -u 10001 -h /app botharness \
 && mkdir -p /data /app/prompts /app/certs /licenses/fonts

WORKDIR /app
COPY --from=build /out/bot-harness /app/bot-harness
COPY prompts/ /app/prompts/
# Extra CA bundles for internal endpoints (public certificates only, no keys).
COPY certs/ /app/certs/
# License and attribution stay inside the image as well as in the source tree.
# The OFL texts are also compiled into the binary (internal/render/embed.go);
# copying them here makes the licence files easy to find in the image.
COPY LICENSE NOTICE THIRD_PARTY_NOTICES.md /app/
COPY THIRD_PARTY_NOTICES.md LICENSE /licenses/
COPY internal/render/fonts/OFL.txt internal/render/fonts/NotoEmoji-OFL.txt \
     internal/render/fonts/NotoSansMath-OFL.txt internal/render/fonts/NotoSansSymbols2-OFL.txt \
     /licenses/fonts/
RUN chown -R botharness:botharness /app /data /licenses

USER botharness
ENV HTTP_ADDR=":8080" \
    DB_PATH="/data/bot.db" \
    TZ="Asia/Shanghai"

EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=25s --retries=3 \
  CMD ["/app/bot-harness", "healthcheck"]

ENTRYPOINT ["/app/bot-harness"]
