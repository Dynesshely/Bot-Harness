# 贡献指南

感谢你愿意参与 Bot Harness。本文说明开发环境、提交前检查、提交规范，以及几个容易踩坑的改动流程。

- 行为准则见 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。
- 安全漏洞请走私密渠道，**不要**开公开 issue，见 [SECURITY.md](SECURITY.md)。
- 使用与部署见 [README.md](README.md)，架构见 [docs/DESIGN.md](docs/DESIGN.md)。

## 1. 开发环境

| 工具 | 版本 | 用途 |
|---|---|---|
| Go | **1.27+**（以 `go.mod` 为准） | 后端、测试、e2e |
| Node.js | **24** | 前端构建与单测 |
| pnpm | **11** | 前端包管理（仓库锁定 `packageManager: pnpm@11.25.0`） |
| Docker + Compose | 任意近期版本（可选） | 容器化自测、PostgreSQL / SearxNG |

不需要全局安装 Go 之外的构建工具；前端依赖由 pnpm 管理。

### 初始化

```bash
git clone https://github.com/Dynesshely/Bot-Harness.git
cd bot-harness

# 后端依赖
go mod download

# 前端依赖
cd web && pnpm install --frozen-lockfile && cd ..
```

### 本地构建与运行

```bash
# 前端（改动 web/ 后必须重新构建；web/dist 是提交进仓库的产物）
cd web && pnpm build && cd ..

# 后端
go build -o bot-harness ./cmd/bot-harness
./bot-harness

# 或整套容器
cp .env.example .env   # 按文件内注释填写必填项
docker compose up -d --build
```

首次启动后，后台口令打印在日志里（`generated admin token`，只打印一次）；也可以从数据库读取：

```bash
docker exec bot-harness /app/bot-harness token
```

## 2. 提交前必须通过的检查

在仓库根目录执行：

```bash
go vet ./...
go test ./... -count=1 -timeout=10m
cd web && pnpm test && cd ..
bash scripts/privacy-scan.sh
bash scripts/check-dist.sh
bash scripts/check-config-keys.sh
```

CI（`.github/workflows/ci.yml`）会跑同一组命令，**不要**依赖 CI 帮你发现本地就能发现的问题。

- `scripts/check-dist.sh` 会重新构建 `web/dist` 并逐文件比对 sha256；改了 `web/src` 就必须把新产物一起提交。
- `scripts/check-config-keys.sh` 校验配置键 golden。新增/删除设置键后，用
  `bash scripts/check-config-keys.sh --write` 更新 `internal/settings/testdata/config-keys.json`，并在 PR 里说明。
- `scripts/privacy-scan.sh` 拦截真实内网 IP、个人绝对路径、密钥等。仓库里需要举例时用
  `one-api.internal.example`、`10.x.x.x`、`<局域网IP>`、`~` 这类占位符；真实值不要提交。

## 3. 测试

### 3.1 单元测试与端到端（默认）

```bash
go test ./... -count=1
```

`internal/e2e` 会启动**内置的假 QQ 平台与假 OpenAI 端点**，不访问外网、不需要凭证。
覆盖被动回复、去重、E1/E2 门控、C1/C2 上下文、长回复分段、被动窗口降级、限流、
两阶段生成（`TestPolish*`）等链路。

前端测试：

```bash
cd web
pnpm test          # vitest
pnpm typecheck     # vue-tsc --noEmit
```

### 3.2 对真实模型的联调（可选，会消耗 token）

端点与 Key 从机器人数据库读取，默认跳过：

```bash
QQBOT_LIVE_TEST=1 ENV_FILE=../../.env go test ./internal/e2e -run TestLivePipeline -v
```

### 3.3 PostgreSQL 测试

PG 测试只允许连 `bot_test` 或一次性 scratch 库。`internal/testpg.Guard` 会在建立连接前
拒绝生产库名（默认 `bot`，可用 `BOT_HARNESS_PROD_DB` 覆盖）：

```bash
PG_TEST_DSN='postgres://bot:<口令>@127.0.0.1:5432/bot_test?sslmode=disable' \
  go test ./internal/store ./internal/media ./internal/admin -count=1
```

**不要**在 source 过 `.env` 的 shell 里直接跑测试：继承的 `DATABASE_URL` 可能指向生产库。
新增任何会打开 PG 的测试入口，都必须在连接前调用 Guard。

## 4. 提交规范

- 使用 [Conventional Commits](https://www.conventionalcommits.org/)：
  `feat(scope): …` / `fix(scope): …` / `docs: …` / `test: …` / `chore: …` / `refactor(scope): …`。
- 一个提交只做一件事；格式化/重命名与行为改动分开。
- 提交信息用**祈使句**，说明「为什么」而不只是「改了什么」。
- 提交必须 **GPG 签名**（仓库启用了 `commit.gpgsign`）。签名不可用时不要加 `--no-gpg-sign`，
  先解决本地密钥/agent 问题。
- 不要在提交里包含 `.env`、数据库文件、日志、截图中的真实凭据或内网地址。
- 改动用户可见行为时同步更新 README / CHANGELOG；改接口时同步 `docs/ADMIN-API.md` 与契约测试。

## 5. 修改设置键（新增一个配置项）

配置只存在于数据库，通过 `internal/settings` 管理。新增一个键的完整顺序是：

1. **字段**：在 `Settings` 结构体加字段与 json tag。
2. **应用**：在 `applyKey` 增加分支（处理类型转换与空值语义）。
3. **校验**：在 `validateRanges` 加范围/枚举校验；非法值必须报错，不能静默截断。
4. **后台表单**：在 `web/src/pages/settings/` 对应子页面加控件，并接入 config draft 的脏字段追踪。
5. **按群覆盖（如适用）**：在 `group_overrides` 白名单与后台表格中同步。
6. **测试**：补 settings 单测、后台契约测试（如涉及 `PUT /api/config`）与前端用例。
7. **golden**：`bash scripts/check-config-keys.sh --write` 更新键集合。
8. **文档**：在 README 对应章节写清默认值、范围与生效方式；需要重启的键要显式标注。

凭据类键（`openai_api_key`、`qq_app_secret`、`admin_token`）**永不回显**；`settings_write`
的拒绝名单不要顺手放开。

## 6. 修改后台接口

- 返回结构变了，必须同步前端与契约测试（`internal/admin/*_test.go`、`web/src/**/__tests__`）。
- 分页参数由 handler 夹取并回显实际生效值。
- 错误一律走统一信封，不要把上游原文里的密钥/DSN 透出。
- 更新 `docs/ADMIN-API.md`。

## 7. 文档与隐私

- 公开仓库里不写真实主机名、内网 IP/网段、代理地址、私有 CA 端点、个人绝对路径、令牌。
- 需要举例时使用保留域名与占位符：`one-api.internal.example`、`10.x.x.x`、`<局域网IP>`、`~`。
- 提交前跑 `bash scripts/privacy-scan.sh`；它同时会在 CI 里跑。

## 8. Pull Request 清单

- [ ] 说明动机与行为变化（不只是文件列表）
- [ ] `go vet` / `go test` 全绿；前端 `pnpm test` 与 `pnpm build` 通过
- [ ] `web/dist` 已重新构建并提交（如改了前端）
- [ ] 配置键 golden 已更新（如改了设置）
- [ ] 文档 / CHANGELOG 已更新
- [ ] 隐私扫描通过，无真实凭据或内网信息
- [ ] 新增/修改的能力有开关、可按群覆盖（如适用）且可回滚
- [ ] 提交已 GPG 签名
