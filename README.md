![Bot Harness banner](docs/assets/banner.png)

[![CI](https://github.com/Dynesshely/Bot-Harness/actions/workflows/ci.yml/badge.svg)](https://github.com/Dynesshely/Bot-Harness/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.27%2B-00ADD8.svg)](go.mod)

QQ 群 AI 对话机器人 + 内置 Web 管理后台。**一个 Go 二进制、一个容器**：前端构建产物通过
`go:embed` 打进可执行文件，运行时只需要数据库（PostgreSQL 推荐，SQLite 可回退）。
业务配置全部存在数据库里，在后台改完即时生效。

> 产品名 **Bot Harness**；仓库 / 二进制 / 容器用户名为 `bot-harness` / `botharness`（历史命名，保持兼容）。

![Bot Harness 管理后台概览](docs/assets/dashboard.png)

- **QQ 接入**：QQ 机器人开放平台 API v2（`api.bot.qq.com`），同时支持 **WebSocket 网关**
  （默认，无需公网 IP）与 **Webhook**（需要公网 HTTPS）两条事件通道。
- **AI 接入**：任何 OpenAI 兼容端点（DeepSeek / 通义 / 自建 vLLM / 自建网关），
  端点与 Key 在后台「系统设置」里改，保存即生效、无需重启。
- **两阶段生成**：主模型（含工具循环）先产出**草稿**，再由一次**无上下文**的润色调用只改口吻，
  实际发送润色稿；润色失败/超时/空输出一律**回退草稿**。默认开启，可按群覆盖（见 §5.12）。
- **Bot 命令**：管理员在聊天里用 `@机器人 /命令 参数`（或私聊）开关功能，
  `/help` 对所有人列出可用命令。命令**不进对话管线** —— 不调模型、不润色、
  不写上下文，只写审计（见 §5.13）。
- **QQ 指令面板**：启动后把命令注册表同步成群聊 / 私聊的面板，失败只记 WARN；
  可用 `command_panel_enabled` 关闭自动同步，后台动作仍可手动触发（见 §5.13.7）。
- **管理后台**：Vue 3 + Tailwind CSS，由同一个 Go 进程托管在 `/`，`/api/*` 提供 REST + SSE。
- **设置页**：6 个二级子页面（模型与提示词 / 回复与人味 / 能力 / 主动消息 / 接入与运维 / 系统与诊断），
  页面底部常驻「保存修改」条；只提交被改动的字段。
- **配置**：业务配置全部在数据库里，后台随时可改、即时生效。`.env` 只承载**引导键 + 部署键**
  （数据库位置、代理、端口绑定等），完整清单与默认值以 [`.env.example`](.env.example) 为准。
- **存储**：**PostgreSQL 17 为主**（`pgx/v5` + 容器内 `postgres:17-alpine`），
  **SQLite 为回退**（纯 Go 驱动、无 cgo、单文件）。`DATABASE_URL` 非空走 PG，留空回退 SQLite。
  持久化上下文、审计记录、运行时配置与媒体文件，两种后端共用同一份逻辑 schema。

**导航**：[快速开始](#1-快速开始) · [架构总览](#架构总览) · [管理后台](#5-管理后台) ·
[开发与测试](#8-开发与测试) · [贡献](CONTRIBUTING.md) · [安全](SECURITY.md) ·
[许可证](#14-许可证与第三方组件)

---

## 架构总览

![Bot Harness 架构图](docs/assets/architecture.svg)

```text
QQ 开放平台 ── WS / Webhook ──▶ internal/qq ──▶ internal/core ──▶ internal/ai ──▶ OpenAI 兼容端点
                                                    │
                                                    ├─ internal/store   (PostgreSQL / SQLite)
                                                    ├─ internal/tools   (工具循环、SSRF 防线、run_shell)
                                                    ├─ internal/render  (表格 PNG、内嵌 OFL 字体)
                                                    └─ internal/settings / core/* (记忆、承诺、主动消息、人类状态)

浏览器 ── HTTP + SSE ──▶ internal/admin ──▶ internal/core
                          └─ go:embed web/dist（Vue 3 SPA）
```

- **单进程装配**：`cmd/bot-harness` 负责读引导配置、打开数据库、装配 QQ / AI / 存储 / 后台，
  然后启动 HTTP 服务与事件循环。`migrate` 与 `token` 是同一个二进制上的子命令。
- **配置边界**：只有「数据库在哪」是引导配置；其余配置经 `internal/settings` 落库，
  后台、Bot 命令与 `settings_write` 工具走同一条写入路径。
- **出站安全**：`internal/httpx` 统一出站 TLS（可追加自定义 CA）；`internal/tools` 做 SSRF
  地址策略；`run_shell` 默认关闭，并要求管理员身份。
- **部署形态**：容器以非 root 用户运行，只暴露一个 HTTP 端口；PostgreSQL 与 SearxNG
  在 compose 默认网络内，不发布宿主端口。

## 界面预览

| 登录页 | 系统设置 → 模型与提示词 |
|---|---|
| ![登录页](docs/assets/login.png) | ![系统设置](docs/assets/settings-provider.png) |

> 截图来自一个未配置的全新实例（无真实群数据与凭据）。

---

## 1. 快速开始

### 1.1 Docker（推荐）

```bash
cp .env.example .env      # 先填 POSTGRES_PASSWORD 与 SEARXNG_SECRET（缺任一个 compose 拒绝解析）
                          # DATABASE_URL 非空 = 走 PostgreSQL；留空 = 继续用 SQLite
docker compose up -d --build
docker compose logs -f    # 首次启动会打印随机生成的后台口令
```

打开 <http://127.0.0.1:8080>，用**后台口令**登录：口令存在数据库里，首次启动随机生成
（`generated admin token` 那行日志只会打印一次）。之后随时用子命令取，不要去 `.env` 里 grep：

```bash
docker exec bot-harness /app/bot-harness token
```

> `token` 子命令直接打开数据库读取当前口令，不启动机器人。对**已存在**的库它走正常打开流程
> （可能应用待执行的迁移），但不会删除或改写业务数据；数据库文件不存在时直接失败、
> 不会顺手建一个新库 —— 误配 `DB_PATH` 不会被它「变成一个空部署」。

改口令在后台「接入与运维」，改完旧会话立即失效。
QQ AppID / AppSecret、模型端点与 API Key **都不写在 `.env` 里**：启动后到后台
「接入与运维」「模型与提示词」填写，保存即生效。

- **PostgreSQL 是推荐后端**：compose 里带一个 `postgres:17-alpine` 服务（容器 `bot-harness-postgres`，
  **不发布宿主端口**、只在 compose 默认网络里以 `postgres:5432` 访问，数据落在 `pgdata` 卷）。
  `.env` 里 `DATABASE_URL` 非空（`postgres://…`）时程序走 PG；**留空则回退 SQLite**，与迁移前行为一致。
  切库不是原地改几个字符：先按 [docs/PG-MIGRATION.md](docs/PG-MIGRATION.md) 用 `bot-harness migrate` 搬数据并校验。
- **控制台可核对实际后端**：`GET /api/status` 的 `store.driver` 为 `postgres` 时，`store.path` 为空串。
- **时间列是原生 `TIMESTAMPTZ`**：Go 侧仍以 RFC3339 字符串交互，API 输出不变。
  类型不能原地回退 —— 升级/回滚都要走
  [docs/PG-MIGRATION.md §12](docs/PG-MIGRATION.md) 的停服流程。
- **回退路径仍然在**：`DB_PATH` 指到的 SQLite 文件继续被保留（`botdata` 卷内 `/data/bot.db`），
  它就是切换那一刻的权威副本 —— 清空 `DATABASE_URL` 重启即回到它。想把 SQLite 文件直接放宿主机，
  请按 `docker-compose.yml` 里的注释改用 `./data:/data` 绑定挂载
  （先 `mkdir -p data`，否则 Docker 会以 root 创建该目录，容器内非 root 用户写不进去）。
- 宿主机端口绑定由 `.env` 的 `ADMIN_BIND` 决定（默认只绑 `127.0.0.1`）；需要远程访问请自行加反向代理。
- WebSocket 模式下**不需要**对外暴露端口，容器只需出网。
- 容器以非 root 用户 `botharness`（uid 10001）运行；镜像体积主要来自四款内嵌字体。
- `docker compose up` 会同时起一个**内部 SearxNG sidecar**（`bot-harness-searxng`，不发布宿主端口，
  仅在 compose 默认网络内以 `searxng:8080` 访问），`web_search` 默认走它，详见 §5.10。
  它要求 `.env` 里设置 `SEARXNG_SECRET`（缺了 compose 直接拒绝解析）。

**从 SQLite 切到 PostgreSQL（一次性，含校验）**：

```bash
# 1) 先只起数据库，等它 healthy
docker compose up -d postgres
docker compose ps postgres                    # 期望 healthy

# 2) 计划：打印 DDL、逐表行数、目标现状、序列计划，一个字节都不写
#    （不写 --to 时，migrate 读**容器内**的 DATABASE_URL —— .env 已由 env_file 注入）
docker exec bot-harness /app/bot-harness migrate --from /data/bot.db --dry-run

# 3) 正式搬运：建表 → 逐表一个事务 → setval → 立刻自校验
docker exec bot-harness /app/bot-harness migrate --from /data/bot.db

# 4) 让程序改走 PG（DATABASE_URL 非空即可），重建容器
docker compose up -d --build bot-harness
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/api/health   # 期望 200
```

- `--from` 是**容器内**的 SQLite 路径（`/data/bot.db`），`migrate` 以 `mode=ro` 打开它，物理上改不了生产库。
- **不要写 `--to "$DATABASE_URL"`**：那会被**宿主机** shell 展开（宿主机通常没有这个变量，或更糟——有另一个值）。
  省略 `--to` 时程序读容器自己的环境变量，那才是 `.env` 注入的那一份；要显式指定就直接写完整 DSN。
- `migrate` 与 `token` 都是同一二进制上的子命令；很旧的镜像可能不认识它们。
- 校验不通过就**不要**切：`verification FAILED` 会指出具体表与第一个不同的列。
- 切换最好在**停写窗口**里做（先 `docker compose stop bot-harness` 再搬）：搬运是快照式全量拷贝，
  窗口内到达的新消息不会进新库；窗口长度取决于数据量，尽量在低峰执行。
  `migrate` 建出来的目标时间列是 **`TIMESTAMPTZ`**（与运行时 `pgSchema` 一致），
  只有当前版本的二进制认它 —— **别用旧镜像去连新库**（旧代码对可空时间列仍绑 `''`，写库会直接报错）。
- 只校验不写：`--verify-only`；重跑：`--replace`（先 `TRUNCATE … RESTART IDENTITY`）。
- 回滚 = 把 `.env` 的 `DATABASE_URL` 留空再重启，SQLite 文件原封不动。
- 细节与实测数据见 [docs/PG-MIGRATION.md](docs/PG-MIGRATION.md)。

### 1.2 本地运行

```bash
# 前端（只有改动 web/ 时才需要重新构建）
cd web && pnpm install && pnpm build && cd ..

# 后端
go build -o bot-harness ./cmd/bot-harness
./bot-harness
```

---

## 2. QQ 开放平台配置

1. 在 [QQ 机器人开放平台](https://q.qq.com/qqbot) 创建机器人，拿到 **AppID** 与 **AppSecret**，登录后台「接入与运维」填写（`.env` 不再读取它们）。
2. **开发阶段**建议先在 **沙箱环境** 调试（后台「接入与运维 → 沙箱环境」开关）。沙箱域名以管理端说明为准。
3. 机器人需要**先被拉进群**才能工作；群内默认只有 **@机器人** 的消息会被推送（对应本项目的 E1 模式）。
4. 想让机器人响应群内**所有**消息（E2）或使用**整群共享上下文**（C2），需要群管理员在 QQ 群设置里
   开启「接收所有消息」。触发模式与上下文模式**直接在后台「策略配置」页设置并存入数据库**，
   不再受环境变量限制；平台侧每个群的真实权限会自动读取并在页面上提示。
   - ⚠️ **开启后平台只推送 `GROUP_MESSAGE_CREATE`**（含 @机器人的消息），
     不再推送 `GROUP_AT_MESSAGE_CREATE`。因此 `at_only` 是靠事件里的 `mentions` 是否包含
     **我们自己的 openid** 来判断的，二者可同时生效：既能收到全部消息，又只在被 @ 时回复。
   - 若发现该群 @ 后仍不回复，去「消息记录」看那条消息的 `ignored` 原因——
     里面会写明 `event=GROUP_MESSAGE_CREATE mentions=N bot_mentions=M`。
5. **Webhook 模式**（后台「接入与运维 → 传输方式」选 `webhook` 或 `both`）：
   - 回调地址必须是 **HTTPS**，端口只能是 **80 / 443 / 8080 / 8443**。
   - 密钥由 `QQ_APP_SECRET` 按平台算法派生，启动日志里会打印 **public_key_hex**，把它填到管理端的回调配置里即可。
   - 服务端会校验 `X-Signature-Ed25519` + `X-Signature-Timestamp`，并自动应答 URL 校验（op 13）。

---

## 3. 事件通道：WebSocket 还是 Webhook？

| | WebSocket（默认） | Webhook |
|---|---|---|
| 需要公网 IP / 域名 / 证书 | 不需要 | 需要（HTTPS） |
| 端口要求 | 无（主动外连） | 80 / 443 / 8080 / 8443 |
| 断线补偿 | 支持 Resume，平台补发漏掉的事件 | 平台重试推送 |
| 适用场景 | 绝大多数自建部署 | 已有公网入口的部署 |

后台「接入与运维 → 传输方式」选 `both` 可同时开启，两条通道共用同一套事件处理逻辑。处理逻辑本身做 `msg_id` 幂等，所以两者同时收到同一事件也不会重复回复。

---

## 4. 配置：程序只读数据库位置（`DATABASE_URL` / `DB_PATH` / `ENV_FILE`），其余都在数据库

`.env` 只有**引导键 + 部署键**，不含任何业务配置。完整清单、默认值与必填项以
[`.env.example`](.env.example) 为准（该文件是唯一事实源）：

| 键 | 谁读 | 说明 |
|---|---|---|
| `DATABASE_URL` | 程序 | **引导值**：PostgreSQL DSN（`postgres://` 或 `postgresql://` 前缀）。**非空即优先**，留空回退 SQLite |
| `DB_PATH` | 程序 | SQLite 文件路径（回退路径）；必须先于数据库存在，所以无法入库 |
| `ENV_FILE` | 程序 | 要加载的 dotenv 文件名（可选，默认 `.env`） |
| `POSTGRES_PASSWORD` | compose | PostgreSQL 服务口令；compose 用 `:?` 强校验，缺失直接拒绝解析 |
| `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` | compose / 容器 | 出网代理；`NO_PROXY` 必须含 `searxng` 与内网域名 |
| `ADMIN_BIND` | compose | 宿主机端口绑定（默认 `127.0.0.1`），不是程序配置 |
| `VERSION` | 构建 | 镜像版本号（写入 OCI label 与 `/api/status.version`） |
| `SEARXNG_SECRET` | compose / searxng | SearxNG 的 `secret_key` 来源；缺失时 compose 直接拒绝解析 |
| `TLS_EXTRA_CA_FILE` | 程序（首次启动） | 可选：自定义 CA bundle 的引导值；导入数据库后以后台配置为准 |

**后端的选法只有一条规则**（`internal/config` 的 `DataSource()`）：`DATABASE_URL` 去掉空白后非空 → 用它，
否则用 `DB_PATH`。所以「切回 SQLite」= 把 `DATABASE_URL` 清空再重启，不需要改代码、不需要回退镜像。
`DATABASE_URL` 里**含口令**：只写进 `.env`（已被 `.gitignore` / `.dockerignore` 排除），
不要贴进文档、工单、截图或提交信息；后台 `GET /api/config` 与启动日志里出现的那份已被遮蔽成 `redacted`。
若要手写 `postgres://bot:${POSTGRES_PASSWORD}@…`，**`POSTGRES_PASSWORD` 必须写在 `DATABASE_URL` 之前**：
程序用 `godotenv` 逐行解析，变量只能引用它前面已经出现的键（compose 自己插值时不受此限）。

**其余全部配置都在数据库里**，登录后台 →「设置」→ 6 个二级子页面修改，保存即生效、重启不丢：

| 子页面 | 管什么 |
|---|---|
| 模型与提示词 | 模型端点 / API Key / 模型 / 温度 / 上下文窗口 / 提示词 / 看图 |
| 回复与人味 | 分段与页脚、**两阶段生成（润色）**（`polish_enabled` / `polish_model` / `polish_temperature` / `polish_max_tokens` / `polish_timeout_seconds` / `polish_min_chars`，见 §5.12）、提前回复、人味强度 |
| 能力 | 工具、记忆、联网（SearxNG）、容器内执行、定时任务 |
| 主动消息 | 自发主动、比例预算、静音时段 |
| 接入与运维 | QQ 凭证与传输、后台口令、监听地址、TLS、日志、附件上限、**QQ 指令面板**（`command_panel_enabled` 开关 + 「立即同步」按钮，见 §5.13.7） |
| 系统与诊断 | 并发与限流、存储引擎与位置（只读：`driver` / `storage` / `version`）、诊断动作 |

> 上面 6 个 `polish_*` 键是**回复页**的字段（不是"能力"开关）；它们在**策略配置**页还有一份
> 按群开关 `group_overrides[].polish`（`null` = 跟随全局）。两份都改完才算"某群关掉润色"。

**旧键不再生效。** 历史上写在 `.env` 里的 `QQ_APP_ID` / `ADMIN_TOKEN` / `HTTP_ADDR` / `TLS_*` /
`LOG_*` / `OPENAI_*` 等 40 个键，只会在**升级后第一次启动**被一次性导入数据库
（只导入数据库里还没有的键），此后改 `.env` **完全无效** —— 要改请去后台。
这些旧键已从 `.env.example` 删除；升级前的原值请自行备份，不要提交进仓库。

已经从代码里彻底移除、写了也没有任何效果的键：

- `ALLOW_DEGRADED`（凭证缺失也启动的开关，早已删除）
- `QQ_FULL_GROUP_MODE`（自 D-016 起废弃）
- `QQ_API_BASE`、`OPENAI_BASE_URL` / `OPENAI_API_KEY` / `OPENAI_MODEL`（模型配置只存在数据库里）

> 用 compose 部署时，`ADMIN_BIND` / `VERSION` / `SEARXNG_SECRET` / `POSTGRES_PASSWORD` 只影响
> **compose / 镜像 / sidecar / PG 容器**；程序内的监听地址是数据库里的 `http_addr`
> （默认 `127.0.0.1:8080`，只监听回环；改成 `0.0.0.0:8080` 时启动会打 WARN，改它需要重启进程）。

### 4.1 存储：PostgreSQL 为主，SQLite 为回退

两种后端**共用同一份逻辑 schema**（14 张表），差别只在方言与 DDL 渲染上：

| | PostgreSQL（默认目标） | SQLite（回退） |
|---|---|---|
| 选择方式 | `DATABASE_URL` 非空 | `DATABASE_URL` 为空，用 `DB_PATH` |
| 驱动 / 连接池 | `pgx/v5` stdlib；`SetMaxOpenConns(8)` | `modernc.org/sqlite`（纯 Go）；**`MaxOpenConns(1)`** + WAL / `busy_timeout` / `synchronous(NORMAL)` PRAGMA |
| 建表 | `internal/store/pg.go` 的 `pgSchema`（`CREATE TABLE IF NOT EXISTS`） | `internal/store/store.go` 的 `schema` + 版本化迁移 |
| 迁移版本 | 12 条既有迁移作为**基线**写入 `schema_migrations`，不重放 | 逐条执行 `runMigrations` |
| 自增主键 | `BIGINT GENERATED BY DEFAULT AS IDENTITY`（显式 id 可插，`setval` 续接） | `INTEGER PRIMARY KEY AUTOINCREMENT` |
| 布尔 | 仍是 `INTEGER`（Go 侧按 int 读、与 1 比较） | `INTEGER` |
| 时间 | 原生 **`TIMESTAMPTZ`**（25 列；8 个「未设置」列可空、`NULL` = 未设置） | `TEXT`（RFC3339） |
| 二进制 | `BYTEA`（`media.data`） | `BLOB` |
| 外键 | **一个都没有** | 一个都没有（`records` 有 `conversation_id` 孤儿） |
| 数据库大小 | `pg_database_size(current_database())` | 主库文件 `os.Stat`（不含 WAL） |

- 方言层只有两件事：占位符（`?` → `$n`，重写时跳过字符串字面量与注释）与自省语句
  （SQLite 的 `pragma_table_info(表名)` vs PostgreSQL 的 `information_schema.columns`）。
  `ON CONFLICT DO NOTHING` / `INSERT … RETURNING id` 两引擎通用，没有第三套 SQL。
- **时间仍然只以字符串交互**：时间列在 PG 里是 `TIMESTAMPTZ`、在 SQLite 里是 `TEXT`，但
  **Go 侧读写都是 RFC3339 字符串**（`2026-11-02T02:31:05+08:00`），API 输出与迁移前逐字节一致。
  PG 的读取时区由连接上的 `TimestamptzCodec` 固定为 `+08:00`；可空列的「未设置」在 PG 是 `NULL`、
  在 SQLite 是 `''`，差异收在 `dialect.NullableTimeArg` / `TimeIsUnset` / `EmptyTimeLiteral`
  三个 helper 里，SQL 仍只有一份。决策与代价见 [D-040](docs/DECISIONS.md)。
- **14 张表**：`conversations` / `messages` / `records` / `tool_calls` / `memories` / `proactive_state` /
  `scheduled_tasks` / `commitments` / `human_status` / `dedup` / `settings` / `media` /
  `schema_migrations` / `stats`。
- **切换数据库不是改配置那么简单**：数据要先搬过去并证明没丢。见
  [docs/PG-MIGRATION.md](docs/PG-MIGRATION.md)（`bot-harness migrate`：动态列发现、逐表事务、
  media 分批、`setval` 续接、行数 + 全列规范化哈希校验）。

---

## 5. 管理后台

| 页面 | 能力 |
|---|---|
| 概览 | 连接状态、模型、token 有效期、存储引擎与用量、收发统计图、SSE 实时事件流 |
| 会话管理 | 按会话查看完整消息时间线（含**未 @ 机器人**的群消息）、清空单个/全部上下文、暂停或恢复某个会话；详情页可切换**实时刷新**；工具栏**合并同群会话**（把某群分散的成员会话并入该群的共享会话） |
| 策略配置 | E1/E2 触发模式、C1/C2 上下文模式、上下文范围、机器人共存、群/成员白名单、屏蔽词；**按群覆盖**可对单个群分别设置触发模式 / 上下文模式 / 上下文范围 / 机器人共存 / 群限流（留空即跟随全局） |
| 消息记录 | 收发审计（状态：received / replied / early_reply / command / ignored / failed / manual；重复推送的同一条 `msg_id` 不入库，只计入 counters）。**被忽略的消息也会记录并写明原因**，是排查「机器人为什么不回」的第一现场 —— 包括「非管理员发了管理命令」这种静默拒绝。`command` 是 Bot 命令专用状态（见 §5.13） |
| 日志 | 级别过滤 + 关键词搜索的进程内日志 |
| 系统设置 | 运行时改模型（下拉自 `/api/models`）/温度/上下文窗口/限流/提前回复/页脚；强制重连、刷新 token、重置配置、测试 AI、测试发送 |

界面细节：
- 左侧导航固定；列表/详情类页面（会话详情、会话列表、消息记录、日志）采用**填充式布局**——
  页面本身不滚动，只有列表区域内部滚动，工具条与卡片头保持不动；表格表头吸顶。
- 全局统一深色滚动条；群 OpenID / 成员 OpenID / 消息 ID 等标识旁有一键复制按钮。
- 会话详情页支持**实时刷新**：默认开启，可一键暂停；另有手动刷新按钮与「最后更新」时间，
  自动刷新时会保持你的阅读位置（原本贴在底部则自动跟随最新）。
  实现上订阅 SSE 的 `message` 事件（按 `conversation_id` 过滤）并叠加 5 秒兜底轮询。

> 「测试发送」发的是**主动消息**，受平台频控约束（未认证 Bot 30/qpm，单群每天 1000 条）。

---

## 5.5 回复行为（可在后台调整）

| 行为 | 开关 | 默认 |
|---|---|---|
| 回复末尾追加「—— 用时 3.2s · 约 128 tokens」 | `reply_footer_enabled`（也可在聊天里用 `/tokenstats` 开关，见 §5.13） | 开 |
| 模型超过 N 秒无结果时先安抚、再以主动消息补发答案 | `early_reply_enabled` / `early_reply_delay_seconds` / `early_reply_text` | 开 / 3 秒 |
| 正式答复送达后**撤回**那条安抚语 | `early_reply_recall` | 开 |
| 主模型出草稿后，再用一次无上下文调用按人设重写措辞 | `polish_enabled`（详见 §5.12） | 开 |

**提前回复的工作方式**：群聊不支持流式，长回答会让用户干等。开启后，模型超过 3 秒仍未产出时，
先用**被动回复**发一句「有点复杂，让我多想会儿～稍后直接把答案发给你」；等模型给出结果，
再把正式答案用**主动消息**推送（因为被动回复额度已经被那条安抚语占用了一次）。

- 安抚语**不会**追加用时 / Token 页脚（它不是 AI 回复）。
- `early_reply_recall` 开启时，正式答复发出成功后会立刻撤回那条安抚语，群里不会留下过期的「让我多想会儿」。
  平台限制**消息发出 2 分钟内**才能撤回，超时则保留原消息；若正式答复发送失败，也会保留安抚语，
  以免用户什么都看不到。
- 主动消息受平台频控（未认证 30/qpm、单群 1000 条/天），部分用户可能在客户端关掉了「允许主动发送」；
  不想承担这个风险可以把 `early_reply_enabled` 关掉。
- 提前回复会单独记入消息记录，状态为 `early_reply`。
- 页脚只影响最终呈现：写进上下文的仍是模型原文，不会把用时/tokens 喂回模型。

## 5.6 群消息的落库规则

开启「接收所有消息」后，群里每条消息都会到达。本项目按下面规则处理：

| 消息 | 落库 | 进模型上下文 | 触发回复 |
|---|---|---|---|
| 私聊（C2C） | ✅ dialogue | ✅ | ✅ |
| 群里 @机器人 | ✅ dialogue | ✅ | ✅ |
| 群里未 @（`at_only` 模式） | ✅ **observed** | ❌ | ❌ |
| 群里未 @（`all_messages` 模式） | ✅ dialogue | ✅ | ✅ |

- `observed` 的消息会出现在**会话详情的时间线**里（虚线气泡 + 「未 @ 机器人」标记），并带发送者昵称，
  但不进入模型上下文、不触发回复 —— 相当于「机器人看到了，但没插话」。
- C1（每人独立）下，某成员未 @ 的消息只会记进**他自己**的会话，不会污染别人的会话。
- 会话详情页每条消息都显示发送者昵称（旧的存量数据会在升级时自动回填）。
- 消息内容里的 `<@OPENID>` @标记会被自动清除，避免把原始 openid 喂给模型。

## 5.5.1 触发模式 与 上下文范围 是两件事

「要不要回复」和「模型能看到什么」被拆成两个独立设置：

| 设置 | 取值 | 作用 |
|---|---|---|
| **触发模式** `trigger_mode` | `at_only` / `all_messages` | 决定**要不要回复** |
| **上下文范围** `context_scope` | `addressed_only` / `include_unaddressed` | 决定**模型能看到多少群内容** |

典型组合：**只在被 @ 时回复，但听得懂群里在聊什么** ——
`trigger_mode=at_only` + `context_scope=include_unaddressed`。
机器人平时不插话，一旦被 @ 就能接着刚才的话题说，也能知道是谁在说（旁观消息会带上「昵称：内容」）。

| 触发模式 | 上下文范围 | 效果 |
|---|---|---|
| `at_only` | `addressed_only`（默认） | 只回 @，模型也只看到与它有关的对话（最省 token） |
| `at_only` | `include_unaddressed` | 只回 @，但模型能看到全群聊天与其它机器人的发言 |
| `all_messages` | 任意 | 每条消息都回；因为它参与了，这些轮次本来就在上下文里 |

两处需要注意：

- **上下文范围受上下文模式影响**：C1（每人独立）下，每个成员只会把**自己**未 @ 的发言纳入上下文；
  想让机器人听懂整个群，需要配合 C2（整群共享）。后台会在选择 `include_unaddressed` 时
  逐群提示哪些群还停在 C1、哪些群在平台侧仍是「仅 @机器人」（那样根本收不到未 @ 的消息）。
- **token 消耗会明显上升**：旁观内容也占上下文预算。裁剪仍受「上下文最大轮次」
  与「上下文最大 tokens」约束，必要时调大前者。

## 5.6.1 合并同群会话

群从 C1（每人独立）切到 C2（整群共享）后，历史里会留下一串分成员的会话。会话管理页工具栏的
**「合并同群会话」**可以把它们并进该群的共享会话：

- 只列出**会话数 > 1** 的群，并显示该群当前生效的上下文模式；不是「整群共享」的群不允许合并
  （并出来的会话没人用）。
- 执行前先**试运行**，把"将并入几个会话、搬移多少条消息"显示出来再确认。
- **严格限制在同一群内**：源会话由 `WHERE group_openid = ?` 选出并逐行复核，目标会话也必须属于该群，
  跨群合并一律拒绝（`group_mismatch`）。
- 消息行是**改归属**不是复制：时间顺序（自增 id）保持不变，重复执行不会产生副本，整体在一个事务里完成。
- 另一个机器人的消息此前被镜像进该群每个会话，合并时按 (内容, 发送者, 时间) 自动去重，
  去重条数会在结果里告诉你。

## 5.7 群里有多个机器人时

群里存在第二个机器人时，会出现一个很容易踩的坑：**别人 @ 那个机器人，我们也会收到事件。**
平台在 `mentions` 里给出的只是「被 @ 的是一个 bot」，并没有说「是你」。
只判断 `mentions[].bot == true` 就会变成抢话。

本项目的做法是**先确认自己是谁**：

| 来源 | 用途 |
|---|---|
| `GET /v2/groups/{openid}/bot_state` → `member_openid` | 我们在该群的 openid，与 `mentions[].member_openid` 精确比对（**首选**，按群缓存 30 分钟） |
| 后台设置的 `qq_bot_openid` | 该接口返回 `11253`（需白名单）时的手填兜底 |
| `GET /users/@me` → `username` | 最后兜底：按机器人昵称比对 |

**都无法确定时宁可沉默** —— 抢答别人 @ 的机器人，比漏答一次更容易被用户发现。

同时区分了三类消息：

| 消息 | 落库 | 进模型上下文 | 触发回复 |
|---|---|---|---|
| 我们自己的消息（平台回显） | ❌ | ❌ | ❌（防自我循环） |
| 群里**其他机器人 @ 我们** | ✅ dialogue | ✅ | ✅（受 `reply_to_bots` 控制，默认开） |
| 群里其他机器人的**普通发言** | ✅ `observed_bot` | ❌ | ❌（即使 `all_messages` 也不回） |
| 群成员未 @ 的发言 | ✅ `observed` | ❌ | ❌ |

**为什么对机器人更严格**：如果连「没有 @ 我们」的机器人发言都回复，群里的两个机器人会互相刷屏。
所以只有明确 @ 到我们的机器人消息才会触发回复，且**连续互回 5 次后主动停下**等真人插话
（真人一说话，计数立刻清零）。开关 `reply_to_bots` 在后台「策略配置 → 与其他机器人共存」。

- 别的机器人不是「某个成员的私聊」，它的发言属于群的公共上下文，因此会镜像进该群的**每个会话**
  （C1 下是每个成员各自的会话），你在自己的会话里就能看到，标记为「其他机器人」。
- 会话详情页会直接显示平台侧的群内状态：**接收设置**（接收所有消息 / 仅 @机器人）、
  **是否允许主动消息**、以及我们在这个群里的 openid（带一键复制），
  这样后台看到的是平台事实，而不是我们自己的假设。

## 5.8 触发模式 / 上下文模式：存在数据库，按平台事实提示

`trigger_mode`（E1 仅 @ / E2 群内任意消息）与 `context_mode`（C1 每人独立 / C2 整群共享）
现在**完全由后台「策略配置」页管理并持久化到数据库**，可以随时切换、立即生效，不需要改 `.env`、
不需要重启。

平台侧每个群的权限是**逐群独立**的（可以在管理端或群里单独设置），所以程序会主动读取并在页面上
如实展示，而不是用一个全局开关去猜：

| 平台 `recv_msg_setting` | 含义 | 对配置的影响 |
|---|---|---|
| `all` | 该群会推送全部消息（含未 @ 的） | E2 / C2 在该群完全生效 |
| `only_mention` | 只推送 @机器人的消息 | 选 E2 也收不到未 @ 的消息；C2 的共享上下文只含被 @ 的消息 |
| `mention_and_context` | @机器人及其引用 | 同上 |

- 读取方式：`GET /v2/groups/{group_openid}/bot_state`，按群缓存；**收到该群消息时顺带刷新**，
  会话详情页也有手动刷新按钮。
- 会话详情页有「平台接收设置 / 主动消息 / 机器人群内 OpenID」三行平台事实，旁边有手动刷新按钮。
- 「策略配置」页会列出哪些群已开放全量、哪些仍是「仅 @机器人」，并在你选择 E2/C2 时说明对哪些群不生效。
- **不硬拦**：模式是运维的选择，平台权限是群主的设置，两者不一致时给出清楚提示即可。

> `QQ_FULL_GROUP_MODE` 已从代码里彻底移除（自 D-016 起废弃）：即使写在 `.env` 里也不会有任何行为，
> 连警告都没有 —— 触发模式与上下文模式只看后台配置。

## 5.9 图片：时间线可见 + 交给模型看

QQ 的附件地址带有会过期的 `rkey`，直接用原链接渲染，历史消息里的图过几天就会变成裂图。
所以入站时就把图片**抓回本地**（`media` 表，按 sha256 内容寻址；SQLite 下是 `BLOB`、
PostgreSQL 下是 `BYTEA`），后台用 `GET /api/media/{id}` 提供，`content-addressed` 因此可以永久缓存。

- **会话详情**里图片直接以内嵌缩略图显示，点击放大；非图片附件（语音/视频/文件）列成一行说明。
- **多模态**：本轮消息带图时，图片以 `data:` URI 内联发给模型（不是让模型去拉 URL ——
  网关在内网，未必能访问公网 CDN，而且原链接会过期）。
  ```json
  {"role":"user","content":[
     {"type":"text","text":"这张图里是什么？"},
     {"type":"image_url","image_url":{"url":"data:image/png;base64,…"}}]}
  ```
- **只发本轮**：历史图片不会在后续轮次重复发送，否则每轮都在为旧图付 token。
  历史记录里的附件仍然保留，后台照样能看到。
- **引用 / 合并图片也入库**：平台把引用消息、合并转发里的附件放在 `msg_elements` 而不是顶层
  `attachments`，入站时会递归展开并缓存，否则引用图永远进不了模型视野。
- **图片 id 标注**：模型输入里会追加 `[图片 #id]`（当前消息）或 `[引用图片 #id]`（引用 / 合并），
  历史消息里已缓存的图片也会标；标注只进模型输入，不写进消息正文。
- **按需回看 `view_image`**：模型可对标注里的 `media_id` 调用视觉工具，拿回一段最多 500 字的
  图片描述（可选 `question`，≤200 字）。工具只在 `vision_enabled` 且工具开启时下发；
  复用 `human_status_vision_model`（空 = 主模型），每次调用是一次额外的视觉请求。
- **失败不阻断**：图片不存在 / 已过期、不是图片、视觉未启用、未配置模型或视觉调用失败，
  都只返回「图片不可用：…」文本，回复照常完成；多模态请求失败还会自动退回纯文本重试一次。
- **安全与配额**：只允许从 QQ 的域名抓取（`qq.com.cn` / `qq.com` / `qpic.cn` / `gtimg.cn`，
  且只允许 http/https），单文件默认 4MB 上限，单条消息默认最多 2 张；
  下载失败的图片保留占位记录而不是丢掉消息。过期的、没有任何消息引用的缓存会被定时清理。

配置：`vision_enabled`（默认开）、`vision_max_images`（默认 2）、`media_max_bytes`（默认 4MB），
全部在后台改（前两项在「模型与提示词」，附件上限在「接入与运维」），`.env` 不再生效。
完整来源、工具契约、降级与隐私边界见 [docs/VISION.md](docs/VISION.md)；取舍见 D-049。

## 5.10 联网搜索：内置 SearxNG sidecar

`web_fetch` 之外，机器人还能用 `web_search` 查资料。搜索**不依赖公网 SaaS**：根目录
`docker-compose.yml` 里带一个 SearxNG sidecar，与机器人同容器网络。

- **拓扑**：服务名 `searxng`、容器 `bot-harness-searxng`、镜像 `searxng/searxng:2026.11.1-9ded3bec1`；
  **不发布任何宿主端口**，只在 compose 默认网络里以 `http://searxng:8080` 访问。
- **配置**：片段在 [deploy/searxng/settings.yml](deploy/searxng/settings.yml)（`search.formats` 含 `json`）；
  `secret_key` 由 `.env` 的 `SEARXNG_SECRET` 注入，仓库里不存 secret。
- **机器人侧**：`web_search_provider=searxng`、`web_search_base_url=http://searxng:8080`（后台「能力」页）；
  `web_search` 支持 `limit`（1–10，默认 5），请求 `{base}/search?format=json`。
- **`NO_PROXY` 必须含 `searxng`**：否则 HTTP 客户端会把内网搜索请求送进代理。

排障（对照返回文案）：

| 现象 | 原因 / 处理 |
|---|---|
| `HTTP 403` | `settings.yml` 的 `search.formats` 没启用 `json`（未启用的 format 会直接 403），或来源被 WAF 拦 |
| `HTTP 429` | 本地或上游限流；降低频率，或调整 SearxNG 的 limiter / 引擎配额 |
| 「返回的不是 JSON」 | 后端返回了 HTML（常见于没开 json）；看错误里的 `Content-Type` 与响应开头 |
| 结果尾部「部分搜索引擎无响应」 | SearxNG 的 `unresponsive_engines`；**不阻断**，只是结果可能不完整 |
| 冷启动后第一次搜索 0 条 | 引擎尚未预热，复测即正常（不是配置错误） |

> SearxNG 镜像**没有 HEALTHCHECK**，`docker ps` 不会显示 `healthy`；就绪探针用
> `docker compose exec searxng wget -qO- http://127.0.0.1:8080/healthz`（期望 `OK`）。
> 它也没有宿主端口，**不要**从宿主 curl 它 —— 这是刻意的暴露面收敛。

### 地址策略与残余风险

搜索走独立的「origin 窄口」守卫，而不是把私网整体放开：

- **只放行配置的 origin**：只有 `web_search_base_url` 解析出的**精确 `ip:port`** 才允许拨号
  （scheme + host + port 三者都对上）；同一 IP 的其它端口、其它私网 / 回环地址一律拒绝。
- **元数据地址在建 client 阶段就拒**：后端若解析到 link-local 或云元数据别名
  （`169.254.169.254` / `100.100.100.200` / `fd00:ec2::254`），创建客户端直接失败，不会等到第一次请求。
- **重定向**：上限 3 次；同 origin 放行（拨号守卫仍限制地址），跨 origin 必须过普通「只允许公网」校验。
- **搜索 client 显式不走 `HTTP_PROXY` / `HTTPS_PROXY`**：sidecar 是直连的，把内部搜索塞进公网代理
  既无意义、又会丢掉上面的地址策略。将来若必须经代理访问公网 SearxNG，需要另行设计（当前不支持）。

普通出站（`web_fetch` 等）仍走系统代理，规则是「放行代理端点、不放行目标私网」：

- `HTTP_PROXY` / `HTTPS_PROXY` 解析出的**精确 `ip:port`** 允许拨号（否则代理本身会被当成私网拦掉）；
  目标地址依旧由 pre-flight 解析与**每一跳重定向**校验。
- **残余风险**：代理会自行解析目标 DNS —— DNS rebinding 对 pre-flight 不可见。因此不要把代理指向不可信网络；
  内网端点应放进 `NO_PROXY` 直连。

换后端：把 `web_search_provider` 清空 = 只用 `web_fetch`；或把 `web_search_base_url` 指向另一个
SearxNG 兼容的 JSON 端点（同一套 `?format=json` 契约）。

## 5.11 容器内执行（`run_shell`）：默认关闭 + 身份双控

`run_shell` 让模型在**容器内**跑命令（工作目录每次重建、环境变量只留 `PATH/HOME/LANG/TZ`、
`ulimit` 限制、超时对进程组 SIGKILL、输出截断）。它是最危险的能力，因此要同时满足：

1. 全局 `exec_enabled=true`（**默认 false**，后台「能力」页）；
2. 该群没有被按群覆盖关掉（`group_overrides[].exec` 优先于全局）；
3. `tools_enabled` 打开（否则工具定义根本不下发）；
4. 调用者 openid 在 `admin_openids` 里；
5. `admin_openids` **非空** —— 空名单返回 `ErrNoAdminsConfigured`，等于谁都不能用。

非管理员的请求里连 `run_shell` 的工具定义都看不到；工具内部再校验一次，审计记 `status=rejected`。
建议部署保持 `exec_enabled=false`、`admin_openids` 非空；按群重开用
`PUT /api/config` 的 `{"group_overrides":[{"group_openid":"<群openid>","exec":true}]}`。
同 uid 的残余风险见 [DECISIONS.md](docs/DECISIONS.md) D-034。

## 5.12 两阶段生成（润色）：草稿 → 只改口吻 → 发送

**一句话**：主模型先正常答一遍（草稿），再由**第二个、没有任何上下文的**模型按人设重写措辞，
群里看到的是重写后的版本。**润色是增值步骤，不是必经步骤 —— 它失败就发草稿，这一轮照常算回复成功。**

### 5.12.1 时序

```
事件 → 触发策略 → 取历史并裁剪
     → 【stage 1】主模型（含工具循环；可能有「让我想想」提前回复）
                  ↓ 草稿（result.Text）+ 本步实际使用的系统提示词
     → 【stage 2】润色调用：只给「系统提示词 + 草稿」两条消息
                  ↓ 成功 = 润色稿 / 失败·超时·空 = 回退草稿
     → 写库（assistant 消息 = 润色稿）
     → 人味停顿（按整轮耗时算）→ 分段发送
     → 记忆抽取（reply = 润色稿）
```

- **stage 2 的输入只有两样东西**：润色器指令 + 已解析的系统提示词（人设、Markdown/格式规则、
  当轮记忆块），以及**草稿正文**。它**看不到**历史消息、用户原文、工具结果、图片 ——
  这既是隐私边界，也让润色器**没有材料**编造新事实。
- 系统提示词取**工具循环最后一步**实际用过的那份（记忆块是在工具循环里追加的），
  否则工具轮里刚记下的事实会被漏掉。
- **润色器不声明任何工具**（`Tools: nil`）：它只负责改写，不许再去查证。

### 5.12.2 两个模型

| | stage 1 | stage 2 |
|---|---|---|
| 用哪个模型 | `model` | `polish_model`，**空串 = 复用 `model`** |
| 看到什么 | 系统提示词 + 历史 + 用户原文 + 工具结果 | **系统提示词 + 草稿**（2 条消息） |
| 能不能调工具 | 能（`tools_enabled`） | **不能** |
| 温度 | `temperature` | `polish_temperature`（默认 0.7） |
| 输出上限 | `max_tokens` | `polish_max_tokens`（0 = 自动） |
| 超时 | `request_timeout_seconds`（整轮） | `polish_timeout_seconds`，且 **不超过整轮剩余预算** |

> 两个模型是**同一个端点**上的两个模型名（`polish_model` 只能填 `openai_base_url` 认识的模型）。
> 想省钱可以把它设成比主模型更小/更快的那个。

### 5.12.3 开关与默认

| 键 | 默认 | 范围 / 取值 | 说明 |
|---|---|---|---|
| `polish_enabled` | **true** | 布尔 | 总开关。关掉 = 回到一次生成，不会产生第二次调用 |
| `polish_model` | `""` | 模型名 | 空串 = 复用 `model` |
| `polish_temperature` | `0.7` | `[0, 2]` | 只求措辞，不需要太发散 |
| `polish_max_tokens` | `0` | `0` 或 `[1, 8192]` | **0 = 自动**：草稿估算 token ×1.5，下限 128，上限 `max_tokens` |
| `polish_timeout_seconds` | `20` | `[5, 120]` | 实际生效值 = `min(本值, 剩余 request_timeout)` |
| `polish_min_chars` | `0` | `[0, 2000]` | 草稿 rune 数低于它就**跳过**润色（省一次调用），**不算失败** |

后台位置：「设置 → 回复与人味 → 两阶段生成」。所有键都走 `PUT /api/config`，保存即生效、无需重启。
（e2e 测试的公共夹具把 `polish_enabled` 显式写成 `false`，断言的是「草稿原样送达」那条路径；
默认是开的，两者不要混。）

### 5.12.4 按群覆盖

`group_overrides[].polish` 是 `bool | null`：`true` 强制开、`false` 强制关、`null` / 省略 = 跟随全局。
后台在「策略配置」的按群覆盖表里（润色列）。

- 覆盖的**优先级高于全局**，所以「全局关、某个群单独开」也成立。
- 覆盖只改**开关**，6 个润色参数没有按群覆盖 —— 那些是"用什么模型/多长超时"，按群各配一套没有实际收益。

### 5.12.5 失败回退

| 情况 | 结果 |
|---|---|
| stage 2 报错（HTTP 4xx/5xx、连接失败） | 发**草稿**，审计状态仍是 `replied` |
| stage 2 超时 | 发**草稿**，同上 |
| stage 2 返回空内容 | 发**草稿**，同上 |
| 模型/端点未配置（`d.ai == nil`） | 发**草稿**（stage 1 能跑通说明这条一般不成立） |
| 全局/按群开关关闭 | **不发** stage 2 请求，直接发草稿 |
| 草稿 trim 后为空，或 rune 数 < `polish_min_chars` | 同上，跳过（debug 日志，不算失败） |
| 整轮 `request_timeout` 已经用完 | 跳过 stage 2，发草稿 |

**「回退后仍然 replied」是硬不变量**：`newPolish` 永远返回可发送的文本，调用方没有「润色失败 → 整轮失败」
这条分支。所以 `polish_fallbacks` 上升只代表**这一轮少了那层打磨**，不代表用户没收到回复。

### 5.12.6 不经过润色的内容

这些不是"AI 正文"，润色只会让原文变样，一律原样发送：

- **后台手动发送**（会话详情页以机器人身份发消息）；
- **主动消息**（`proactive.go` 直接调模型拿结果就发，不回灌 stage 2）；
- **错误兜底文案**（「抱歉，我这边刚才出错了…」「排队中」等固定话术）；
- **提前回复的「让我想想」**文案（`early_reply_text`，在 stage 1 期间就发出去了）。

### 5.12.7 日志与指标

每轮一行 INFO（`component=ai`）：

```
two-stage reply  draft_chars=210 polish_chars=224 polish_model=… polished=true fallback= ""
                 polish_latency_ms=640 draft_latency_ms=1830
```

- **回退**时另有一行 WARN：`polish fell back to the draft`，`reason` 是 `error: <上游错误>` /
  `empty result`。
- **跳过**（开关关、草稿为空、短于 `polish_min_chars`、整轮预算耗尽、进程没有模型客户端）只有 DEBUG
  的 `polish skipped`，`reason` 是 `disabled` / `empty draft` / `draft below polish_min_chars` /
  `no time left in the request budget` / `no model client`，**不刷 WARN**
  （跳过的常见原因是"设计如此"，不是异常）。
- `GET /api/status` 的 `counters` 里两个新计数：

  | 计数 | 含义 |
  |---|---|
  | `polish_requests` | **真的发出去**的 stage 2 请求数（跳过的不算） |
  | `polish_fallbacks` | 其中失败回退的次数（`≤ polish_requests`） |

  排查口径：`polish_fallbacks / polish_requests` 的比例就是润色的失败率；
  两个都是 0 且回复正常 → 润色没开（或被 `polish_min_chars` 全跳过了）。
- **页脚的 token 数是两段之和**（stage 1 含工具步 + stage 2）；`ai_requests` 仍只数主路径，
  所以「请求数」与「token 数」不再一一对应。

### 5.12.8 与提前回复、人味的关系

- **与提前回复**：安抚语在 stage 1 期间就发出去了，**不润色**；正式答复照常走 stage 2。
  stage 2 的耗时会算进「整轮耗时」，人味的停顿是按**整轮**算的（目标停顿 − 已经花掉的时间），
  所以**不会**因为多了一次润色而再额外等一遍。
- **与人味**：两者是**正交**的两层。`humanize_*` 管的是**怎么说出去**（停顿、分段节奏、未 @ 时沉默）
  以及一句 `persona` 的风格约束；两阶段管的是**说什么内容**（先把事实答对，再换口吻）。
  常见搭配：`polish_enabled=true` + `humanize_level=medium`。
- **时长提示（页脚）**：追加的时间是**整轮**耗时（含润色），不是只有 stage 1。
- **上下文一致性**：写进 `messages` 的 assistant 行、喂给记忆抽取的 reply、实际发送的文本
  **三者都是润色稿** —— 否则下一轮模型会「记成另一个人说的话」。页脚不在其中（不喂回模型）。

### 5.12.9 关闭与回滚

- 只想省钱 / 觉得润色没用：`PUT /api/config` `{"polish_enabled":false}`（即时生效，不用重启）。
- 只关某几个群：`{"group_overrides":[{"group_openid":"<群openid>","polish":false}]}`。
- 想换回「按群各配一套」之外的默认行为：`{"polish_model":""}` 让 stage 2 回到主模型。
- 代码级回滚：这期没有 schema 改动、没有新表、没有新列 —— 回退二进制即回到一次生成，
  数据库里多出来的 6 个键会被**忽略**（旧代码不认识它们，`SeedFromEnv` 也不会删）。

## 5.13 Bot 命令：在聊天里开关功能

除了在后台改设置，管理员还可以**直接在聊天里发命令**。命令由 `internal/core/commands.go` 实现
（注册表 + 解析 + 权限），走的是与对话完全分开的一条短路。

### 5.13.1 前缀与触发条件

| 规则 | 说明 |
|---|---|
| **两种写法：`/命令 参数`，或「整条消息恰为一个已注册命令名」** | 前者是通用形态；后者是**指令面板点击**产生的形态（平台剥掉了 `name` 的前导 `/`，客户端把名字原样填进输入框，见 §5.13.7）。 |
| **带参数必须带 `/`** | `tokenstats off`（无斜杠 + 额外单词）**不是**命令，仍走普通消息；否则「@机器人 help me with X」会被误判成命令。 |
| **只认 `@机器人` 或私聊** | 群消息必须 @ 了机器人才算候选；c2c 私聊没有 @ 的概念，天然可用。全量群未 @ 的消息一条都不受影响。 |
| **命令名大小写不敏感** | `/tokenstats`、`/TokenStats`、`/TOKENSTATS`、`tokenstats` 等价；参数同样不敏感（`ON` = `on`）。 |
| **空白由程序切分** | 多个空格 / Tab 都能识别；只发一个 `/`（没有命令名）不算命令。 |
| **未注册的名字不是命令** | `/hello`、`foo` 这类没人注册过的写法**按普通消息走**（照常触发 AI 对话），不会回「没有这个命令」。 |
| **全角 `／` 不是命令前缀** | 中文输入法下很容易打出来的 `／tokenstats` 是普通文本（它也不是「已注册命令名」，不会走面板那条路）。 |

**「整条消息恰为一个已注册命令名」的边界**（`parseBareCommand` + `lookupCommand`）：

- 触发条件三件套：**被 @ 或 c2c**、`strings.TrimSpace` 后**只有一个 token**、该 token（小写）**在注册表里**。
- 命中后 `args` 为空，命令按**默认动作**执行 —— 对 `/tokenstats` 就是 `status`。
- 多一个词就不算：`@机器人 help me with X`、`tokenstats off`、`foo bar` 都是普通消息。
- 未注册的单词不算：`@机器人 foo` 照常走 AI。
- 群里**未 @** 时这条规则**完全不生效**：`tokenstats` 和 `/tokenstats` 一样是普通消息。

> **为什么必须 @ 或私聊**：全量群模式（E2）下机器人能看见群里每一条消息。如果未 @ 的 `/xxx`
> 也算命令，群里任何人随口打一句 `/tokenstats off` 就能改掉**全局**配置 —— 那是提权，不是便利。
> 未 @ 的 `/xxx` 因此完全等同普通聊天：照常进上下文、照常被人味与 AI 处理。

### 5.13.2 权限：管理命令只有管理员，`/help` 公开

- **管理类命令**（`adminOnly=true`，如 `/tokenstats`）判定口径与后台其它管理员能力一致：
  发送者的 `member_openid`（群）或 `user_openid`（私聊）在 `admin_openids` 名单里。
  名单为空 = 谁都不能用（不是「谁都能用」）。
- **非管理员得到的是「静默」**：不回复、不调模型、不改任何设置，审计里记一条
  `status=ignored`、原因含「非管理员」。
- 为什么静默而不是「无权限」/「未知命令」：在群里，任何一句回执都等于**确认这次调用真的生效了**，
  等于把"这个命令在这里能用"直接告诉对方。**静默只保证执行被拒时不给回执、不确认本次调用**，
  所以「试探出管理员命令再想办法绕过」这条路拿不到有效信号。但要注意：
  **命令的存在性**自 D-043 起由 `/help` 与指令面板**对全群公开**（面板项带 `only_admin` 标记），
  静默不再等于"命令不可见"，它挡的是执行与确认，不是名字。
  想知道谁试过，查审计（见 §5.13.4）。

### 5.13.3 首个命令：`/tokenstats`

```text
tokenstats              # 指令面板点击形态（不带斜杠），等价于 /tokenstats status
/tokenstats             # 等价于 status
/tokenstats status      # 查看当前状态
/tokenstats on          # 打开「token 用量统计追加」
/tokenstats off         # 关闭
/tokens [on|off|status]  # 别名，效果完全相同
```

- **面板点击那一行**值得单独说：QQ 指令面板的 `item.name` 会被平台剥掉前导 `/`，客户端把名字
  **原样填进输入框**，所以用户从面板点「tokenstats」后发出去的是 `tokenstats`（无斜杠、无参数）。
  它按 §5.13.1 的「整条消息恰为一个已注册命令名」识别，`args` 为空 ⇒ 默认动作 `status`。
  想改开关仍然要带参数，而**带参数必须带 `/`**：`/tokenstats on`、`/tokenstats off`。
  `tokenstats off` 这种无斜杠 + 参数的写法**不是命令**，会照常走 AI。

- 作用对象是全局设置 **`reply_footer_enabled`** —— 就是「回复与人味」里的那一项：
  开启后每条真实 AI 回复末尾追加 `—— 用时 3.2s · 约 128 tokens`。
- **只影响页脚**：回复正文、写进上下文的 assistant 行、模型调用都不变；页脚本身也不会喂回模型。
- 走的是和后台完全相同的写入路径（`settings.Apply`），所以**立即生效、落库持久**，
  与在后台点开关等价；反过来在后台改，命令的 `status` 也能立刻读到新值。
- 参数非法（`/tokenstats maybe`）或给多了（`/tokenstats on off`）：只回一行用法，**不改设置**。
  那行用法原文是 `用法：/tokenstats [on\|off\|status]`（源码里的 `usage` 字符串，`status` 也在其中）；
  不打参数就等同于 `status`。`status` 子命令的回复里同样会附这行用法。
- 别名 `tokens` 是为了少打字；命令名和别名共用一张表，后注册的同名命令会覆盖先前的。
- `/help` 是**公开**命令（`adminOnly=false`），只列出已注册命令的用法与描述，见 §5.13.6。

### 5.13.4 审计与错用行为

命令的执行点刻意**不在对话管线里**：

```text
去重 → 白名单 → 屏蔽词 → 触发门 → 【命令分支】→ 人味 → 上下文 → stage 1 → stage 2 → 发送
                                      │
                                      └─ 命中：执行 → 一条被动回复 → 返回（不往下走）
```

命中命令后**不做**这些事：不调模型（因而不计 `ai_requests`）、不润色、不施加人味延迟、
不发提前回复、不写会话上下文（命令与回复都不会出现在会话时间线里）。
短路点也在 `EnsureConversation` **之前**，所以一条从未聊过的私聊发命令**不会**凭空建出会话记录。
回复是**一条**绑定原 `msg_id` 的被动消息（同一条消息最多 5 条被动回复，命令只用 1 条）。
幂等沿用 `msg_id`：平台重复推送同一条命令，只会执行一次。

| 情况 | 入站审计 | 出站审计 | 群里看到 |
|---|---|---|---|
| 管理员执行成功 | `status=command` | `status=replied`（投递失败则 `failed` + `error`） | 一条确认/状态，内容与审计原文一致 |
| 管理员参数不对 | `status=command` | `status=replied` | 一行用法：`用法：/tokenstats [on\|off\|status]` |
| 命令执行失败（写设置报错） | `status=command` | `status=replied` | 「修改失败：<原因>」 |
| **非管理员** | `status=ignored`，`error` 含「非管理员」 | **没有出站行** | 什么也没有 |
| **面板点击**（无斜杠单命令名，`args` 为空） | `status=command` | `status=replied` | 命令的默认动作，如 tokenstats 的 `status` |
| 无斜杠 + 额外单词（`tokenstats off`、`help me with X`） | 走普通路径（`replied` / `observed` …） | 同普通路径 | 照常对话 |
| 未 @ 的 `/xxx` 或无斜杠命令名（群里） | 走普通路径（`replied` / `observed` …） | 同普通路径 | 照常对话 |

- 入站 `command` 的含义是「这条消息被当作命令处理了」：既不是「被模型回答」，也不是「被丢弃」。
  后台「消息记录」的状态筛选里有这一项（契约见 [docs/ADMIN-API.md](docs/ADMIN-API.md) §6）。
- 日志：成功一行 INFO `command handled`（`component=command`，带 `command` / `member` /
  `group_openid` / `args`）；执行出错一行 WARN `command failed`；非管理员走通用的
  `message ignored` 行（`component=dispatch`、`reason=command: 非管理员`）。
  这条忽略日志按「同一 reason 每 30 秒最多一行」节流：群消息是 INFO、私聊是 DEBUG，
  且被节流时会降到 DEBUG —— **审计表不节流**，要数完整次数请查 `records`。

### 5.13.5 扩展一个新命令

注册表在 `internal/core/commands.go` 的 `registerBuiltinCommands` 里，加一个命令 = 加一条登记 + 一个
`run` 函数：

```go
d.registerCommand(&command{
    name:      "你的命令",        // 不带斜杠；大小写不敏感
    aliases:   []string{"别名"},   // 可空
    adminOnly: true,              // 管理类命令一律 true；查询类可用 false
    usage:     "/你的命令 <参数>",
    desc:      "一句话描述（≤30 字，/help 与指令面板共用）",
    run:       runYourCommand,    // func(ctx, d, in, s, args) (reply string, err error)
})
```

- `run` 返回的字符串就是回给群里的那句话；返回 `error` 会被包成「修改失败：<原因>」，
  不影响其它命令与对话管线。
- 改配置请走 `d.applySettings(ctx, patch)`（带校验、落库、立即生效），不要在命令里直接改内存。
- `adminOnly: false` 是留给「谁都能用的查询类命令」的口子；在此之前**默认都该是 true**。
- 解析层（`parseCommand` / `parseBareCommand` / `lookupCommand`）无需改动：新增命令自动获得
  「只认 @/私聊、`/name args`、大小写不敏感、未注册按普通消息」这一整套规则 ——
  **并且立刻可以「面板点击」**：只要名字（或别名）在注册表里，无斜杠的单命令名也算命中
  （§5.13.1）。

### 5.13.6 `/help`：对所有人列出已注册命令

`/help` 是**公共命令**（`adminOnly=false`）：群里的任何人 @ 机器人发 `/help`、或在私聊里发，
都能拿到一份可用命令清单。它遵守 §5.13.1 的全部规则（@/私聊、两种写法、大小写、未注册走普通消息），
清单**末行**还会说明面板点击得到的是不带斜杠的命令名、带参数要写 `/命令 参数`。

- **输出格式**：每行 `usage — desc`，管理命令在行尾追加「（仅管理员）」；没有 `desc` 的命令只输出
  `usage`。整个清单按**规范命令名**排序，别名只出现一次（别名与命令名指向同一个条目）：

  ```text
  可用命令：
  /help — 查看可用命令
  /tokenstats [on|off|status] — 开关 token 用量统计（仅管理员）
  ```

- **不泄露任何东西**：清单只有命令名、用法与描述，没有配置值、没有 OpenID、没有管理员名单。
  标「（仅管理员）」是**如实告知**（非管理员执行它依旧会被静默拒绝），不是权限提示。
- **和所有命令一样不进对话管线**：不调模型（不计 `ai_requests`）、不润色、不加人味延迟、
  不发提前回复、不写会话上下文；入站审计 `status=command`、出站 `status=replied`，
  回复是一条绑定原 `msg_id` 的被动消息。
- **为什么公共**：命令的存在本来就由「指令面板」公开（§5.13.7 —— 面板对全群可见，
  管理命令只是带 `only_admin` 标记）。在这种前提下再把 `/help` 藏起来，只是让普通群友
  多一条「去后台/看文档才知道」的门槛，并不增加安全性；真正的门始终是执行时的身份校验。
  决策与取舍见 D-043。

### 5.13.7 QQ 指令面板注册（同步）

面板是**平台侧的展示层**：用户在群里点开指令面板就能看到命令列表，点一下由**平台**
把该项的 `name` 填进输入框 —— **点击只填入，不发送**。`name` 的内容由平台管理与回读：
**平台会剥掉前导 `/`**（写 `/help`，读回来是 `help`），所以投影一律发不带斜杠的名字
（见下方「投影规则」），文档里也不写死带斜杠的示例形态。

> **点击 → 发送 → 解析**这条链是本节的重点：用户点「tokenstats」后，客户端填入并发出的是
> **`tokenstats`**（不带斜杠、不带参数）。解析层因此必须有「整条消息恰为一个已注册命令名」
> 这条分支（`parseBareCommand`，见 §5.13.1）—— 只认 ASCII `/` 的话，面板会「看起来能用、
> 点了没反应」。等价关系：**点 `tokenstats` ≡ `/tokenstats status`**；要带参数必须手打
> `/tokenstats on|off`（无斜杠 + 参数按普通消息走）。

实现在两层：`internal/qq/panels.go` 封装平台 `/v2/panels`（列表 / 新建 / 更新 / 删除），
`internal/core/command_panel.go` 负责「注册表 → 面板项」的投影与对账。

**投影规则**（`commandPanelItems`）

- 每个命令一项，`type=command`；`name` = `usage` 的第一个词**去掉前导 `/`**（`/tokenstats` → `tokenstats`），
  没有 `usage` 时退回命令名；`desc` = 注册表里的 `desc`；`adminOnly` → `only_admin`。
  去斜杠不是审美选择：**平台会剥掉 `/`** —— 写 `/help` 读回来是 `help`。投影必须与回读同形，
  否则每次同步都判「不同」，面板永远 `updated`、永不收敛（2026-11-02 首次上线实测到的）。
- 顺序按规范命令名排序，别名折叠（同 `/help`）：**同步结果稳定**，
  不会因为 map 遍历顺序而每次「看起来都变了」。
- 长度按 **rune** 截断（不是字节）：`name` ≤ 14、`desc` ≤ 30。中文描述不会被切坏 UTF-8。

**自动同步（启动后 8 秒，后台 goroutine）**

- `cmd/bot-harness/main.go` 在 `dispatcher.Start` 之后 `go dispatcher.RunCommandPanelSync(rootCtx)`：
  等 **8s**（token 与网络就绪）→ 跑一次 → 失败按 **30s / 1m / 2m / 5m / 10m** 退避，
  连同首轮**最多 6 次**；6 次都失败就 WARN 一句「gave up」然后停手。
- 每次真正开跑前都会重读 `command_panel_enabled`：中途被关掉 → 打一条 INFO
  `command panel auto sync skipped`（`reason=command_panel_enabled=false`）并退出。
- **失败只记 WARN**，绝不阻塞启动、绝不影响命令本身：面板是展示层，命令由消息事件驱动。

**状态机（每个 scope 独立一次）**

| 平台现状 | 动作 | 结果 |
|---|---|---|
| 该 scope 下没有 `remark=bot-harness:commands` 的面板 | `POST /v2/panels`（`target_type=all`） | `action=created`、`version=0`（新建接口只回 `panel_id`） |
| 有，且 items 与投影**逐项相同**（含顺序） | 不发写请求 | `action=unchanged`、`version=<平台版本>` |
| 有，但 items 不一致 | `PUT /v2/panels/{id}`（不传 version） | `action=updated`、`version=<新版本>` |

- **识别标记**：`remark` 固定为 `bot-harness:commands`。同步只认这个标记 ——
  **用户手工创建的面板永远不碰**，也**永不删除**任何面板（`DeletePanel` 只作协议完整性的储备，
  同步路径不调用）。marker 之外的面板哪怕 items 明显更旧，也不归我们管。
- **两个 scope**：`group` 与 `c2c` 各同步一次，**一个失败另一个照跑**；
  列表读全（分页）再比对，避免把第二页上的自己的面板当成「没有」而重复创建。

**手动动作**

- 端点：`POST /api/actions/sync-commands`（管理后台认证，25s 超时），返回
  `{"results":[{"scope":"group","panel_id":"…","action":"created|updated|unchanged","version":0}]}`。
- 失败是 **`502 upstream_error`**（消息保留平台原始文本与平台 `code`；若只有一个 scope 失败，
  成功写入的那半边**已经生效**，但响应里**不返回部分结果** —— 以日志 / 平台为准）。
  `Dispatcher.SyncCommandPanel` 本身是**正向隔离**的：一个 scope 失败不影响另一个，成功的结果会
  与 error 一起返回（回归用例 `TestSyncCommandPanelOneScopeFailuresStillReturnsTheOther`）；
  是 HTTP 处理器刻意只回错误、不回半截结果。
- **开关只管自动同步**：`command_panel_enabled=false` 时启动同步不跑，但这个动作**始终可用**
  （管理员的手动兜底，改完命令后不想重启也能推一次）。
- 前端在「设置 → 接入与运维 → QQ 指令面板」：`command_panel_enabled` 开关 + 「立即同步」按钮，
  结果按 scope 列出 `panel_id` / `action` / `version`；失败显示 `upstream_error` 原文。
- 契约细节见 [docs/ADMIN-API.md](docs/ADMIN-API.md) §8。

**平台限制（硬约束，超了就报错）**

| 限制 | 值 | 对本实现的影响 |
|---|---|---|
| 一个机器人的面板总数 | 20 | 我们只用 2 个（group / c2c），其余留给手工面板 |
| 一个面板的项数 | 20 | 命令超过 20 个时**不会被本地裁剪**：平台会拒绝整次写入（见 D-043「未做」） |
| `item.name` | ≤ 14 字符 | 点击后由平台填入输入框的内容，即 `usage` 首词（去前导 `/`；平台会剥掉，写带斜杠会被读回成不带斜杠，导致对账不收敛） |
| `item.desc` | ≤ 30 字符 | 注册表的 `desc` 必须守住这个长度（`/help` 共用同一字段） |
| 写接口频控 | 10 QPM | 启动只写 0–2 次、重试有退避；手动连点会被平台限流 |
| 读接口频控 | 30 QPM | 每次同步每个 scope 至少 1 次列表读，分页时更多 |

**失败排查**

1. **先看日志**：每次成功一行 INFO `command panel synced`（`component=command-panel`，
   带 `scope` / `panel_id` / `action` / `version`）；失败一行 WARN `command panel sync failed`
   （带 `attempt` / `max_attempts` / `error`）；6 次耗尽再一行 WARN `command panel auto sync gave up`；
   被开关关掉是 INFO `command panel auto sync skipped`。手动动作的日志是
   `component=admin` 的 `command panel synced manually` / `command panel sync failed`。

   ```bash
   docker logs bot-harness --tail=300 | grep -i 'command panel'
   ```

2. **再核事实**：后台「接入与运维」点一次「立即同步」，看返回的 `action` 是 `created` /
   `updated` / `unchanged`。一直是 `unchanged` 说明注册表没变；一直 `updated` 说明每次投影
   都不同（通常是 `desc` 或顺序被动过）。
3. **平台拒绝**：错误里带平台的 `code` 与 `message`（原样透出，接口是 `502 upstream_error`）。
   面板接口的拒绝集中在 `40030011` / `40030013` / `40030015` / `40030016` / `40030018` /
   `40030020` 这一组（无权限 / 参数或内容不合规 / 数量或长度超限 / 频控）。
   **不要凭 code 猜**：以 `message` 为准，对照平台错误码文档定位到具体字段。
4. **一个 scope 成功、一个失败**：常见于私聊（`c2c`）没开权限。group 面板已经建好，
   修好权限后手动再同步一次即可；**不会有半成品面板被删**（本实现不删除）。
5. **命令超过 20 个**：面板最多 20 项，平台会拒绝写入；先减命令或接受「面板只展示前 N 个」
   （当前实现不做裁剪，见 D-043「未做」）。
6. **面板没出现**：先确认 `command_panel_enabled` 是 true、启动日志里有
   `command panel synced`；再确认群里/私聊里平台真的把这个机器人的面板露出了
   （面板权限由平台侧决定，机器人 API 只负责写）。

---

## 5.14 承诺（open loop）追问：提醒不再是一次性闹钟

「提醒我交周报」不是发一条消息就结束。承诺是一条 **open loop**：到点提醒之后，如果用户没有回应，
机器人会按人类生活常识继续追问，直到用户明确说「完成了 / 晚点 / 别问了」才收口。

- **创建**：模型在用户明确要求时调用 `create_commitment`（text + `first_delay_seconds` 或
  `at`，priority 0–3，可选 `deadline_at`）。首次提醒至少 60 秒、最多 180 天。
  每人同时未收口的承诺默认上限 **10**（`commitment_max_open_per_member`，1–50）——
  这是防误操作，不是追问频率限制。
- **追问**：调度器每秒领取到期承诺，调用一次**独立决策模型**，输出本次文案与
  `next_check_seconds`。输入包括承诺文本/优先级/已追问次数/上次追问时间/最近 3 条会话消息/
  该会话的人类状态推测/记忆里与勿扰有关的条目。**频率完全由模型判断**，代码只强制
  `>=60s` / `<=180d`；没有按次数退避、没有自动放弃、没有代码静默时段。
- **勿扰只认记忆**：记忆里明确写了「某时段不要打扰」时，模型会把下一次追问排到之后；
  没写过就没有静默期。追问**原路返回**：群里的承诺回到群（主动消息，一条气泡），
  私聊的承诺回到私聊。
- **群内「点名」是文案，不是平台 @**：群承诺回到群时，模型可以在文案里写「小明，……」；
  发送路径不调用平台的 @mention（`qq.SendOptions` 里没有 mention 字段），所以对方不会收到
  「有人@我」的提醒。想做真正的 @ 需要平台侧 mention 结构，不在当前实现里。
- **后台列表**：`GET /api/commitments` 默认 `limit=50`（夹在 1–200），**没有分页**；
  支持 `conversation_id` / `creator` 过滤，且不过滤状态 —— 它展示完整历史。
  需要看更早的行就显式调大 `limit`，没有 offset / cursor。
- **收口**：用户回执时模型调用 `ack_commitment`：
  `done` / `cancel` 收口；`postpone` 只把 `next_check_at` 推到
  `now + remind_after_seconds`（缺省 **4 小时**）。
- **失败不卡死**：模型/JSON 失败 → 模板文案 + **3 小时**保守间隔，WARN；
  发送失败 → **5 分钟**退避重试，不增加 `nudge_count`。
- **提示注入**：构建回复系统提示时会列出该会话/该用户未收口承诺，并告诉模型
  「用户表达完成/延后/取消才调用 `ack_commitment`；没提就正常聊天，不要每轮都催」。
- **开关**：`commitment_enabled`（默认 true）。观测：`counters.commitment_nudges` /
  `commitment_acked` / `commitment_postponed`，后台 `GET /api/commitments`。

### 5.14.1 用户视角：怎么让机器人「记住并追到底」

| 你想做什么 | 在聊天里怎么说 / 做什么 | 机器人会怎样 |
|---|---|---|
| 让它记着一件事 | 「明天下午三点提醒我交周报」 | 模型建一条承诺，到点发提醒（群承诺回群、私聊承诺回私聊） |
| 当时没回应 | 什么都不用说 | 按生活常识挑时间再问，**不是**固定每 5 分钟；记忆里写了勿扰就避开 |
| 已经做完了 | 「交了」「已经弄好了」 | 模型收口为 `done`，不再追问 |
| 暂时不想做 | 「晚点再说」「明天再提醒我」 | 只把下次追问往后推（默认 **4 小时**），不算完成 |
| 不想再被问了 | 「别问了」「取消吧」 | 模型收口为 `cancelled`，这条永远不再出现 |
| 后台兜底 | 后台「定时任务 → 承诺追问」点垃圾桶 | 只能取消 `waiting` / `leased`；已 `done` / `cancelled` 的行没有按钮（服务端也会拒绝） |

页面上的状态标签：**等待追问** / **追问中**（调度器刚领走）/ **已完成** / **已取消**。
"已追问 N 次"与"下次追问时间"分别来自 `nudge_count` / `next_check_at`。

流程与取舍见 D-044，接口字段见 `docs/ADMIN-API.md` § `/api/commitments`。

## 5.15 人类状态感知：语气与时机的参考

机器人会从最近的聊天内容里推测对方此刻可能在做什么（吃饭/睡觉/工作/打游戏/通勤/上课/在线…），
只推断活动状态，不推断年龄、性别、健康等敏感属性。状态**只用于调整回复语气与承诺追问时机**，
不会写进用户可见消息，除非用户主动发 `/human-status`。

- **触发**：按 conversation 累计新消息，达到 `human_status_trigger_messages`（默认 20，5–200）
  时异步跑一次推测；同一会话单飞去重。回复构建前若缓存已过期且消息够了，也会异步刷新一次，
  **本轮仍用现有缓存**，不阻塞回复。
- **缓存**：新表 `human_status`（scope / conversation_id / member_openid 唯一），
  `human_status_ttl_minutes` 默认 **120**（10–1440），过期行由维护循环清理。
- **推测输入（成员时间线）**：每次取最近 `human_status_timeline_messages`（默认 **120**，20–400）
  条消息，按 `SpeakerOpenID` 分组，渲染成「`## 成员：昵称 (openid)`」+ 按时间顺序的
  `[HH:MM] 内容`。消息带图片时在同一行追加 `[图片 media_id=…]`（每人最多 **4 张**、
  总计 **8 张**，避免提示词膨胀）；没有昵称的成员显示「未知」。末尾另附最多 **20 条**
  按时间排序的「全局最近消息」，帮助理解上下文。系统提示要求结合发言节奏、用词、活跃时段
  与图片内容判断状态。
- **图片分析工具（默认开）**：`human_status_vision_enabled` 打开且时间线里有图片时，
  推测请求带 `analyze_image` 工具。模型**只在图片确实有助于判断时**调用；一次推测最多
  **4 次**工具调用、**2 轮**模型循环。视觉请求使用 `human_status_vision_model`
  （空 = 主模型），temperature 0.2、max_tokens 300，返回的描述以 role=tool 回灌主模型。
  图片加载失败、视觉模型失败或超出次数都只返回「图片不可用：…」文本，**推测继续**，
  不会升级成整轮失败。观测：`counters.human_status_image_calls`。
- **输出与容错**：推测请求的输出预算下限 **2000 tokens**（配置的 `max_tokens` 更大时沿用），
  evidence 要求 ≤40 字。解析**不要求整段是合法 JSON**：去掉 markdown 围栏后从第一个 `[`
  起逐个解码对象，数组前后的解释文字可容忍；被截断时保留已经解出的对象（WARN 标记
  `partial=true`）。一个对象都解析不出（含空数组）时，会追加一条「只输出 JSON 数组」的
  user 消息**重试一次**；两次都失败才回「分析失败」。重试是额外计费调用，token 用量写入
  INFO 日志 `human status JSON retry`，**不新增 counter**（`human_status_runs` 仍按
  「一次推测 = 1」计）。
- **提示词**：系统提示追加「当前聊天对象状态推测（可能不准，仅用于调整语气/时机）」，
  只给昵称 + 状态 + 置信度，并明确要求不要说出来。承诺追问决策读同一份数据。
- **命令**：`/human-status [人数]`（管理员，别名 `humanstatus`）会**立即同步分析一次并返回结果**，
  不再只读缓存。人数缺省 **10**，允许 **1–200**；无论传多少，**只分析过去 24 小时在本会话
  发过言的人**（按最近发言时间倒序，最多 `人数` 个）。24h 内没人发言时直接回
  「过去 24 小时没有可分析的对象。」，不调用模型；非法参数回用法文本。列固定为
  对象 / 状态 / 置信度 / 依据 / 更新，成功时优先发**表格图片**：
  - **图片（默认路径）**：用 `internal/render` 把同一份表格渲染成 PNG（标题
    「聊天对象状态推测（N 人）」），再走富媒体发送（上传 `/files` → `msg_type=7`）。
    对象名不截断，依据放宽到 **80 rune** 交给渲染器按列宽自动换行。出站审计的 `content`
    记为 `[图片] 聊天对象状态推测（N 人）`，不是二进制也不是文本列表。
  - **文本回退（渲染/上传/发送任一失败）**：命令输出里始终带着文本版本，图片失败时原样回退。
    纯文本（`markdown_enabled=false`，默认值）是**一人一行的列表**，字段用全角「｜」
    分隔，不做空格 padding —— QQ 群聊是比例字体，空格对齐的表格在客户端一定错位。
    对象名不截断，依据截到 40 rune，更新时间只显示时分。
  - markdown 开启时，**回退文本**是**代码块包裹的等宽表格**（` ```text `），靠等宽字体对齐。
    QQ 官方 Markdown 文档的「支持格式」里没有表格语法（只有标题/样式/链接/图片/列表/引用/分割线），
    而代码块在桌面 QQNT、iOS 与新版安卓都会渲染。图片仍然优先，除非渲染/上传失败。
  群聊最多显示最近 `人数` 个对象（缺省 10），且只会是**过去 24 小时发过言**的人；
  私聊只显示对方。图片标题与 Audit 的 `[图片] 聊天对象状态推测（N 人）` 里的 N 是
  **实际分析人数**（请求 100、24h 只有 15 人发言时就是 15）。
  成功但没有可推断对象时回「已分析，但暂时没有可推断的对象。」；模型失败且缓存为空时回
  「分析失败，请稍后再试（模型调用失败）。」；有未过期缓存时失败则照常展示缓存。
  手动命令**不受 `human_status_enabled` 限制**，单次分析最多等 45 秒。
- **开关**：`human_status_enabled`（默认 true）。关闭后停止**自动推测**，也不再向回复/承诺
  提示词注入状态；但已有且未过期的缓存不会被删除，管理员手动发 `/human-status` 仍会
  强制执行一次分析。`human_status_vision_enabled`（默认 true）单独控制图片分析工具：
  关掉后请求里不出现 `tools`、不会产生任何视觉调用，纯文字时间线照常工作。
  观测：`counters.human_status_runs` / `human_status_failures` / `human_status_image_calls`。

### 5.15.1 `/human-status` 输出示例

管理员在群里 **@机器人** 发 `/human-status [人数]`（或私聊直接发；别名 `humanstatus`）。
不传参数默认分析 **10** 人，允许 **1–200**；只有**过去 24 小时在本会话发过言**的成员
才会被分析（按最近发言时间倒序），24h 内无人发言时回「过去 24 小时没有可分析的对象。」，
非法参数回「用法：/human-status [人数]」。

**图片（默认输出）** —— 表格渲染成 PNG，客户端显示为一张图片：

```text
聊天对象状态推测（2 人）
对象  状态    置信度  依据        更新
----  ------  ------  ----------  -----
小明  打游戏  82%     刚说在开黑  21:40
小红  工作    65%     在赶周报    18:05
```

图片路径失败时（渲染、上传或发送），同一条命令会**回退**到下面的文本版本，答案不会丢。

**回退纯文本（markdown 关闭）** —— 一人一行，不依赖列宽：

```text
最近聊天对象状态（2 人）：
- 小明｜打游戏｜置信度 82%｜依据 刚说在开黑｜更新 21:40
- 小红｜工作｜置信度 65%｜依据 在赶周报｜更新 18:05
```

**回退文本（markdown 开启）** —— 代码块包裹的等宽表格，列与上面一一对应：

~~~text
最近聊天对象状态（2 人）：

```text
对象  状态    置信度  依据        更新
----  ------  ------  ----------  -----
小明  打游戏  82%     刚说在开黑  21:40
小红  工作    65%     在赶周报    18:05
```
~~~

- 群聊最多显示最近 **人数** 个对象（缺省 10），且只包含过去 24 小时发过言的人；
  私聊只显示对方。每次发命令都会先同步分析一次。模型没有给出可推断对象时回
  「已分析，但暂时没有可推断的对象。」；24h 内无人发言时回
  「过去 24 小时没有可分析的对象。」（不调用模型）。

```text
/human-status 20        # 最多分析 20 人（若 24h 内只有 15 人发言，就只分析 15 人）
/human-status abc       # 用法：/human-status [人数]
```
- 置信度是模型自评，**可能不准**；这个列表只是方便管理员核对，不会自动发到群里，
  也不会写进用户画像或长期记忆（缓存按 `human_status_ttl_minutes` 过期）。

流程与取舍见 D-045。

### 5.15.2 表格转图片（通用渲染组件）

`internal/render` 是一个**与业务无关**的表格渲染器：输入 `Table{Title, Headers []string,
Rows [][]string}`，输出 PNG 字节。它不引用 `core` / `store`，所以任何"把结构化数据发成图片"
的场景都可以直接复用它（目前只有 `/human-status` 一个调用方）。

- **字体已内嵌，且是回退链**：`go:embed` 打进 `[Noto Sans CJK SC, Noto Emoji,
  Noto Sans Math, Noto Sans Symbols2]` 四款 OFL 字体，逐 rune 用 `GlyphIndex(rune) != 0` 判定
  覆盖并按顺序选 face（0=CJK、1=Emoji、2=Math、3=Symbols2）。容器/宿主机不需要装任何字体。
  来源、许可、sha256 与覆盖范围见 `internal/render/fonts/README.md`。代价是二进制与镜像变大
  （CJK 约 +16.4 MB，Emoji +1.98 MB，Math + Symbols2 再 +2.25 MB），这是"图片在任意部署环境
  里都不出豆腐块"的交换。
- **花体昵称可渲染**：`𝐶𝑟𝑎𝑛𝑦𝑜𝑧𝑒𝑛`（U+1D436…）、`𝓒𝓻𝓪𝓷𝔂𝓸𝔃𝓮𝓷`（U+1D4D2…）这类
  Mathematical Alphanumeric Symbols 不在 CJK 字体里，过去每个 rune 一个 `.notdef` 方框；
  现在由 Noto Sans Math 接住，同一字号、同一基线（行高仍取 CJK face 的 metrics）。
- **单色 emoji 可渲染，彩色仍然不支持**：新增的 Noto Emoji 是**轮廓单色**字体
  （`glyf`/`gvar`，无 CBDT/COLR/CPAL/SVG），`😀 🎉 👍 🔥 ❤️` 由 Emoji face 接住，按正文
  颜色画成单色线/实心字形，**不是 QQ 里的彩色表情**。`x/image` 只光栅化轮廓、不渲染彩色
  glyph，换 NotoColorEmoji 反而只会得到豆腐块。VS16/ZWJ/肤色修饰/键帽组合符按零宽跳过，所以
  `❤️` 与 `❤` 像素一致；ZWJ 序列没有 GSUB 塑形，`👨‍👩‍👧` / `🏳️‍🌈` 会按组件并排
  （可读、不崩）。四款都不覆盖的 rune 才落到主字体的 `.notdef`。
- **测量与绘制同源**：列宽、换行、标题截断全部走 `fontChain.measure`（逐 rune 累加所选
  face 的 `GlyphAdvance`，忽略 kerning），绘制则把连续同 face 的 rune 聚成 run 用
  `font.Drawer` 画 —— 两者不会算出不同的宽度。
- **按内容换行**：列宽由内容决定（有上限），超长单元格按字素/空格自动换行；行数超过 40 行
  时只渲染前 40 行并在页脚注明截断。2x 渲染，客户端缩放后仍然清晰。
- **接口**：`render.RenderPNG(t Table) ([]byte, error)`；`RenderImage` 供测试直接检查尺寸。
- **发送侧**：PNG 走 QQ 富媒体 —— `POST /v2/groups/{group_openid}/files`（或
  `/v2/users/{user_openid}/files`）上传，`file_type=1`、`file_name=status.png`、
  `file_data` 为 **base64**、`srv_send_msg=false`，再发 `msg_type=7` 的 `media` 消息。
  单张图 **≤ 5 MB**；被动回复用原 `msg_id` + `msg_seq=1`（一张图就是一条消息）。
- **失败即回退**：渲染失败、上传失败、媒体消息发送失败都只降级为文本，绝不让命令失败。

## 5.16 情绪状态（affect）：表达层的连续心境

机器人可以保存一份**按会话隔离、会随时间演化、只影响表达方式**的情绪状态。它不是情绪识别，
也不做用户画像：不判断用户真实感受、不写长期记忆、不影响事实与安全策略。**默认关闭**，
关闭时回复、润色与主动消息与 0.1.0 完全一致。

- **一键启用**：后台「情绪状态」页（`/affect`）打开开关，内容包选 `daily`（日常心境），
  注入档位默认 `line`。也可以只给某个群关 / 调档（`group_overrides[].affect`）；
  全局总开关关闭时所有群都不生效，群覆盖只能进一步关闭，不能越过总开关开启。
- **5 个维度**：心情 `valence`、精力 `energy`、压力 `stress`、熟悉度 `closeness`、
  耐心 `patience`，范围 0–100。状态按半衰期向基线回落；压力只**单向**压低耐心上限；
  超出上限的部分按指数回落。数值不会进入提示词。
- **事件驱动**：内容包用正则识别「被夸 / 被批评 / 道谢 / 道歉」等对话事件，叠加到维度上。
  分类器统一加否定、假设、疑问与长度门，避免反讽或玩笑被当成真事件；同类事件 1 小时内递减，
  每个事件有 24 小时滚动上限。
- **注入**：`off` / `line`（≤25 tokens，一行）/ `full`（≤`affect_inject_max_tokens`，
  默认 120）。动态文案会消毒、限长，只包在「仅作语气参考」的块里；任何失败都返回空串，
  **不影响本轮回复**。
- **行为层**：状态只做软约束 —— 回复字数系数（下限 40 字）、分段上限、人味停顿系数、
  未 @ 沉默概率增量、主动消息概率系数。不改工具、白名单、限流与安全策略。
- **主动消息联动**：精力 <25、心情 <20 或暂停中时，proactive 跳过本轮且不扣额度；
  其余情况只把概率乘以系数，原有额度、静默时段与最小间隔不变。
- **后台与命令**：`/affect` 页可看 5 维、历史曲线、事件、注入预览，可暂停 / 恢复 /
  应用预设 / 重置；管理员在聊天里用 `/mood [status|pause 分钟|resume|preset 名称|reset]`
  （别名 `mood`）查看与操作。命令不调模型、不写会话上下文。
- **设置键**：`affect_enabled`（false）、`affect_pack`（`daily`）、
  `affect_inject_mode`（`line`）、`affect_event_classifier`（`rules`）、
  `affect_tick_seconds`（300，60–1800）、`affect_inject_max_tokens`（120，40–300）。
  按群覆盖新增 `affect` 与 `affect_inject_mode`。
- **隐私边界**：不推断年龄、性别、健康、政治、宗教、性取向等敏感属性；不做性 / 占有 / 成人内容；
  不用内疚、恐惧或占有话术操纵用户；事件日志保留 7 天、历史点保留 30 天，不跨会话聚合。

完整设计、演化公式、事件表、API、存储表与运维步骤见
[docs/AFFECT.md](docs/AFFECT.md)；取舍见 D-047。

## 5.17 自主加入（join_gate）：未 @ 群消息的接话判断

群里的**未 @ 消息**默认只记录、不回复。开启 join_gate 后，机器人会先让模型判断一次：
「这条消息值不值得接，在对谁说，最近的群聊在聊哪几个话题？」——模型给出
**三分类**（`join` 插话 / `wait` 等一等 / `skip` 不插话）与 `addressed_to`，
只有 `join` 才进入回复管线。**默认关闭**，关闭时与 0.2.0 行为一致。

- **一键启用**：后台「自主加入」页（`/join`）打开开关，模式选 `llm`，按群可选是否开启
  （`group_overrides[].join_gate`）；全局总开关关闭时所有群都不生效，群覆盖只能在该群
  进一步关闭或显式开启，不能越过全局关。
- **三分类而不是开关**：`join` = 现在就该接；`wait` = 话题还在发展、此刻插话会打断，
  过一会儿再看；`skip` = 这轮不该出现。`wait` / `skip` 都只写 decision、不回复。
- **引用机器人也算被点名**：平台事件里的 `ref_msg_idx` 会换算成本地 assistant 消息的
  `quote_index`，命中即视同 `@`，走 @ 快路径、**不经过 join gate**；@ 与引用都不消耗评估。
- **LLM 判断**：每次评估最多一次模型调用，输出一个 JSON（`action` / `addressed_to` /
  `confidence` / `reason` + `topics`）。模型只做判定，不生成回复；判定加入后仍走原有的人设、
  工具、润色与限流管线。
- **话题分析**：同一次调用顺便把最近窗口切成 1–8 个话题（每个可带 `type`：
  help / chat / question / plan / conflict / joke / other），保存为该群的最近缓存；
  后续回复可把它作为 ≤400 字的背景，不要求模型强行转回话题。
- **话题生命周期**：每个话题还带 `status`（`active` / `dormant` / `closed`）与 `resumable`。
  代码按话题最新消息的年龄做**单向降级**：超过 `join_gate_topic_dormant_minutes`（默认 20 分钟）
  变 `dormant`，超过 `join_gate_topic_closed_minutes`（默认 180 分钟）变 `closed`；
  `closed` 不接、不旧事重提，`dormant` 只有被新消息明显接回时才可继续。上一次的话题
  （最多 8 条、≤500 字）会回灌给闸门模型，避免旧事重提；回复背景也会标「进行中 / 暂歇 / 已结束」。
- **只处理群聊**：私聊、@ 消息、引用机器人、命令、空消息、机器人自己的消息都不评估。
  join_gate 排在 trigger gate 之前：开启后 `at_only` 下被判定加入的未 @ 消息也会回复，
  其余仍丢弃；关闭时行为与 0.2.0 一致。
- **闸门与成本**：默认评估冷却 60 秒、两次加入至少间隔 5 分钟、每小时最多 3 次、每天最多 12 次；
  达到上限时**不调模型**。若上次评估是 `wait`，满 `join_gate_wait_retry_seconds`（默认 30 秒）
  且该群又有新消息时，可绕过评估冷却提前再看一眼（`wait` 不是定时器：没有新消息就不会重评估）。
  输入窗口默认 20 条、每条截断 200 字，输出上限 600 tokens。
- **失败不插话**：解析失败、超时、非法 JSON 或拿不到模型并发额度时一律按「不加入」处理
  （fail-closed）；每一次评估都写一条 decision，后台能看「为什么这轮没接」。
- **后台与 API**：`/join` 页可改 12 个设置、按群名选群、看最近决策（插话 / 等一等 / 不插话）
  与最新话题（含 `status` / `resumable`）；只读端点 `GET /api/join/decisions`、`GET /api/join/topics`，
  群名由 `GET /api/groups/names` 批量解析（三者都要求管理员）。后台页的群名来自本地会话记录，
  缺失时回落 openid。
- **隐私边界**：只处理群聊；decision 里的消息预览 ≤80 字；不把系统提示词交给模型、不写用户画像、
  不做广告 / 骚扰；频率是硬闸门。

完整流程、prompt / JSON、闸门、存储表与运维步骤见
[docs/JOIN-GATE.md](docs/JOIN-GATE.md)；取舍见 D-048。

## 6. 平台硬约束（设计依据）

这些都是 QQ 开放平台的规定，不是本项目的选择：

- 群消息必须 **@机器人** 才会推事件（E1 是默认权限下唯一可行的形态）。
- 被动回复窗口 **5 分钟**，同一条消息**最多回 5 次**；超时后只能发主动消息。
- **群聊不支持流式输出**，所以群里表现为「等待后整段回复」。
- 主动消息有频控，且用户可在客户端关闭「允许主动发送」。
- 单条消息长度超限会报 `40054007`，因此长回答会被分段发送。

对应的处理逻辑：超长回答按段落切分并递增 `msg_seq`；窗口过期自动降级为主动消息；同一条 `msg_id` 重复推送会被幂等丢弃。

---

## 6.5 自定义 CA 与模型目录

### 6.5.1 自建网关的证书信任

如果模型端点指向自建网关（例如 `https://one-api.internal.example/v1`，在后台「系统设置」里配置），
它可能由**私有 CA** 签发；容器和 CI 的系统信任库通常没有它，直接调用会报
`x509: certificate signed by unknown authority`。

做法是把 **CA 根证书 / 中间证书 PEM** 放进 `./certs`，通过后台「接入与运维 → 额外 CA」
（`tls_extra_ca_files`）**追加**到系统根证书之上（追加而非替换，访问 QQ 开放平台等公网端点不受影响）：

```ini
/app/certs/extra-ca.pem     # 多个文件在后台里逐条添加（等价于旧的冒号分隔写法）
```

- 仓库**不附带任何 CA 证书**；`certs/` 默认只有说明文件，按需放入你自己的公开证书（**绝不放私钥**）。
- 该路径不存在或不是合法 PEM 时，程序**启动即报错**，不会拖到第一次调用模型才抛握手错误。
- 容器内路径为 `/app/certs/...`，compose 把宿主机 `./certs` 只读挂载过去，换证书不需要重建镜像。
- 也可以用 `TLS_EXTRA_CA_FILE` 环境变量作为**首次部署的引导值**：第一次启动时导入数据库，
  之后以数据库为准 —— 再改环境变量不会生效，请到后台改。
- 替换 / 轮换步骤见 [certs/README.md](certs/README.md)。
- 应急开关：后台的 `tls_insecure_verify`（旧 `.env` 的 `TLS_INSECURE_SKIP_VERIFY`）会关闭全部出站校验
  （**同时影响 QQ 平台连接**），启动日志会打印显式警告。

> 容器里如果被注入了 `HTTP_PROXY`，记得把自建网关放进 `NO_PROXY`，否则请求会绕到代理上去。

### 6.5.2 在后台选择模型

上游 OpenAI 兼容端点的 `GET /models` 会被代理到后台接口 `GET /api/models`（服务端缓存 5 分钟，
`?refresh=1` 强制刷新）。登录后台 →「系统设置」→ 模型一栏即可从真实模型列表中选择，
保存后写入数据库并即时生效；下拉里没有的模型名可切到「手动输入」。

模型名、端点与 API Key **只存在数据库里**，没有 `.env` 兜底（D-033）。
「重置配置覆盖」会把它们退回**内置默认值**：模型 `gpt-4o-mini`、端点与 Key 为空 =「未配置」，
所以点重置之前先记下端点和 Key，重置后立刻回后台重填。

## 7. 项目结构

```
cmd/bot-harness/            进程入口：装配各组件、HTTP 服务、优雅退出
  migrate.go                 migrate 子命令（SQLite → PostgreSQL 全量搬运 + 自校验）
  token.go                   token 子命令（从数据库读后台口令）
internal/config/      引导配置（DATABASE_URL / DB_PATH / ENV_FILE）
internal/httpx/       出站 HTTP/TLS 客户端（可追加私有 CA）
internal/settings/    运行时可变配置（后台可改，落库持久化）
internal/qq/          QQ 开放平台协议层
  auth.go             access_token 获取与续期
  api.go              REST 客户端（发送消息、获取网关）
  ws.go               WebSocket 网关：Identify / 心跳 / Resume / 重连
  sender.go           分段发送、msg_seq、被动窗口降级
  panels.go           指令面板 `/v2/panels` 封装（列表 / 新建 / 更新；同步不删除）
  events.go           事件模型与解析
internal/ai/          OpenAI 兼容客户端（SSE 流式累积 + 非流式回退）
internal/core/        业务核心：分发、去重、限流、上下文、统计
  polish.go           两阶段生成的 stage 2：无上下文润色 + 失败回退（见 §5.12）
  commands.go         聊天命令：注册表 + 解析 + 管理员门 + 与对话解耦的执行（见 §5.13）
  command_panel.go    指令面板同步：注册表 → 面板项投影、marker 对账、启动重试（见 §5.13.7）
internal/store/       持久化：SQLite 与 PostgreSQL 双后端（dialect.go 方言层、pg.go 的 PG schema）
internal/pgmigrate/   SQLite → PostgreSQL 一次性搬运器与零丢失校验器（不依赖 store，自带 DDL）
internal/admin/       管理 API + SSE + SPA 托管
internal/webhook/     Webhook 回调：Ed25519 验签 + URL 校验
internal/bus/         进程内事件广播
internal/logging/     slog + 内存环形缓冲
certs/                自定义 CA 证书放置点（默认只有说明文件；公开证书，无密钥）
web/                  Vue 3 + Tailwind 管理后台（构建产物嵌入二进制）
docs/                 设计文档、决策记录、API 契约；PG-MIGRATION.md 是搬迁与校验手册
.github/workflows/    CI（vet/test/前端/隐私扫描）与 Release（tag 触发的镜像发布）
scripts/              开发与 CI 脚本（check-dist.sh、privacy-scan.sh）
  backup-pg.sh         PostgreSQL 备份（pg_dump -Fc + sha256 清单 + 轮转）
  restore-pg.sh        PostgreSQL 恢复（先校 sha256、拒绝覆盖运行中的机器人、--force）
  backup-db.sh         SQLite 阶段方案（**已 deprecated**，仅在回退到 SQLite 时使用）
  restore-db.sh        SQLite 阶段方案（**已 deprecated**，同上）
deploy/               可选部署片段（默认部署不需要；searxng/ 是内置搜索的 sidecar 配置，由根 compose 挂载）
```

设计说明见 [docs/DESIGN.md](docs/DESIGN.md)，需求决策见 [docs/DECISIONS.md](docs/DECISIONS.md)，前后端接口契约见 [docs/ADMIN-API.md](docs/ADMIN-API.md)。

---

## 8. 开发与测试

需要 **Go 1.27+**、**Node 24**、**pnpm 11**（开发流程与提交规范见 [CONTRIBUTING.md](CONTRIBUTING.md)）。

```bash
go vet ./... && go test ./... -count=1  # 单元测试 + 端到端管线测试（内置假 QQ / 假 OpenAI 服务端）
cd web && pnpm install --frozen-lockfile && pnpm build   # 前端构建，产物 web/dist 会被 go:embed 打进二进制
cd web && pnpm test                     # 前端单测（vitest）
cd web && pnpm typecheck                # vue-tsc 类型检查

bash scripts/privacy-scan.sh  # 隐私扫描（CI 也跑；--staged 只扫暂存区新增行）
bash scripts/check-dist.sh    # 重建 web/dist 并逐文件比对；产物与源码不一致就失败
bash scripts/check-config-keys.sh  # 配置面 golden（Settings 全键 + 按群覆盖字段）；加了配置项要 --write 提交
```

CI 见 [.github/workflows/ci.yml](.github/workflows/ci.yml)，每次 push 与 PR 跑三件事：

| 任务 | 内容 |
|---|---|
| go | `go vet ./...`、`go test ./... -count=1`，外加 core/tools/settings/store 的 `-race` 子集 |
| web | `pnpm install --frozen-lockfile`、`pnpm test`、构建产物与源码一致（做不到就红） |
| hygiene | `scripts/privacy-scan.sh`：内网 IP / 个人绝对路径 / 密钥 / 内网域名；`scripts/check-config-keys.sh`：配置面 golden 漂移 |

它只跑仓库里已有的命令，不依赖真实网络与任何凭证。发布由
[.github/workflows/release.yml](.github/workflows/release.yml) 负责：推送 `v*` tag 时构建并推送
`ghcr.io/dynesshely/bot-harness`（tag + `latest`），同时创建 GitHub Release。

端到端测试覆盖：被动回复、重复推送去重、E1/E2 权限门控、C1/C2 上下文、长回复分段、被动窗口过期降级、用户限流、白名单、屏蔽词、模型失败兜底；
两阶段生成另有 7 个专属用例（`TestPolish*` / `TestNonAIBodiesAreNeverPolished`：润色稿送达与落库、开关关闭、超时/500/空结果回退、
工具轮只送最终草稿、按群覆盖、`polish_min_chars` 跳过、手动发送与兜底文案不润色）。

还有一个**对真实模型**的联调测试（默认跳过，会消耗 token；端点/密钥直接从机器人数据库里读）：

```bash
QQBOT_LIVE_TEST=1 ENV_FILE=../../.env go test ./internal/e2e -run TestLivePipeline -v
```

它会先拉一次 `/models` 确认配置的模型确实存在，再走完整「事件 → 上下文 → 模型 → 分段回复」链路并打印真实回复。

---

## 9. 运维提示

- **出网与代理**：`HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` 只写在 `.env`（唯一事实源），
  构建期做 build-arg、运行期由 compose 注入容器，程序对外请求遵循这些变量（`NO_PROXY` 请包含 `127.0.0.1`）。
  **不要**同时在 `~/.docker/config.json` 配 `proxies.default` —— Docker 会给每个容器再注入一份小写变量，
  两份 `no_proxy` 不一致时内部域名可能被错误代理。`NO_PROXY` 要覆盖 `.internal.example` 与内网 CIDR。
- **健康检查**：内置子命令 `bot-harness healthcheck` 会请求自身 `/api/health`，`200` 退出 0、否则退出 1（已显式绕过 `HTTP_PROXY`，容器里注入了代理也不会误判）。Dockerfile 的 `HEALTHCHECK` 用的就是它，不需要镜像里装 curl/wget。
  `/api/health` 在凭证缺失或尚未拿到 token 时返回 `503 degraded` —— 这是**刻意**的：配置不对就不该显示健康。
- **日志**：JSON 行输出到 stdout，`docker compose logs -f` 可查；后台「日志」页是进程内缓冲，重启即清空。
- **备份 / 恢复（PostgreSQL 主路径）**：用仓库脚本，别手搓 `docker cp` 或 `tar` 卷目录。
  ```bash
  # 备份：pg_dump -Fc，默认输出 ~/bot-harness-backups，--keep 7 轮转；可热备
  bash scripts/backup-pg.sh

  # 恢复：先校 sha256 清单；机器人容器在跑时默认拒绝，确认无人使用才加 --force
  docker compose stop bot-harness               # 恢复会 --clean 覆盖，先停写
  bash scripts/restore-pg.sh ~/bot-harness-backups/bot-<UTC 时间戳>
  bash scripts/restore-pg.sh ~/bot-harness-backups/bot-<UTC 时间戳> --database bot_restore   # 演练到另一个库

  # 恢复后
  docker compose up -d bot-harness
  curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/api/health   # 期望 200
  ```
  - 快照目录含 `bot.dump`（`pg_dump -Fc`，含 BLOB）、`SHA256SUMS`（在该目录内 `sha256sum -c` 可直接校验）、
    `MANIFEST.txt`（format / container / database / db_user / server_version / image / created_at_utc /
    hot / bytes / objects）；默认在仓库外，不进 git。
  - **一致性边界**：`pg_dump` 在单个事务里取一致快照，所以**机器人运行中也能热备**，不锁表、不停容器；
    `hot=true` 只是记一笔"导出时机器人在跑"。备份完脚本会用 `pg_restore -l` 回读一遍，解析不出来的 dump 直接判失败。
  - `restore-pg.sh` 的动作顺序是固定的：先 `sha256sum -c`（不符就在碰库之前退出）→ 检查机器人容器是否在跑
    → 目标库不存在则 `createdb`（演练很方便）→ `pg_restore --clean --if-exists --no-owner`
    → 抽查 `public` 表数与 `version()`。出现任何 `pg_restore: error:` 都算失败，不靠退出码掩盖。
  - **SQLite 脚本已 deprecated**：`scripts/backup-db.sh` / `restore-db.sh` 是 SQLite 阶段的临时方案，
    只在 `DATABASE_URL` 留空、仍然跑 SQLite 时才用；迁到 PG 之后不要再把它当备份手段（见 D-039）。
- **切库与回滚**：`bot-harness migrate --from <sqlite 路径>` 会建表、逐表一个事务、
  修 `setval`，最后立刻自校验（行数 + 全列规范化哈希 + media 聚合 + settings 逐键）。
  目标 DSN 缺省取**容器内**的 `DATABASE_URL`（不要在宿主 shell 里写 `--to "$DATABASE_URL"`，
  那展开的是宿主机的变量，通常是空或另一个值）。
  步骤、`--dry-run` / `--verify-only` / `--replace` 的语义与实测结果见 [docs/PG-MIGRATION.md](docs/PG-MIGRATION.md)。
  回滚只要把 `.env` 的 `DATABASE_URL` 留空并重启 —— SQLite 副本此时仍是切换那一刻的完整数据。
- **时区**：镜像内未安装 tzdata，时区数据库由 Go 二进制自带，`TZ` 环境变量照常生效。
- **重置后台配置**：系统设置页的「重置配置覆盖」会把**可调配置**全部退回**内置默认值** ——
  **不是** `.env` 的值（D-036 之后 `.env` 已不参与运行时配置）。三点必须知道：
  - **三个凭据保留**：`admin_token` / `openai_api_key` / `qq_app_secret` 不是覆盖项，重置不清空，
    旧登录会话继续有效（口令此前从未生成过时才会补一个并提示用 `bot-harness token` 读取）。
  - **模型端点回到未配置**（端点不是凭据），模型名回到 `gpt-4o-mini`：重置后必须立刻回后台填端点，
    否则所有模型调用都会失败（`warnings` 会提示）。
  - API Key 通常被保留，所以「重置后马上要填」的一般只有端点一项。

---

## 10. 安全须知

- `.env` 只承载**引导键 + 部署键**（数据库位置、代理、端口绑定、sidecar secret 等），
  **不含 QQ / 模型 / 后台口令**（业务凭据只在数据库）；它被 `.gitignore` 与 `.dockerignore` 排除，
  请勿用 `git add -f .env` 强行提交。升级前的原值备份由部署方自行保管，不要外传。
- **`DATABASE_URL` 里含 PostgreSQL 口令**（典型形态 `postgres://bot:<口令>@postgres:5432/bot?sslmode=disable`）。
  三条硬规则：① 只写进项目 `.env`，不写进仓库文件、文档、日志、聊天或截图；
  ② 后台与日志里它一律是被遮蔽的形态 —— `GET /api/config` 的 `readonly.storage`、
  `GET /api/status` 的 `store.target`、启动日志的 `db` 字段都把口令换成 `redacted`；
  ③ `POSTGRES_PASSWORD` 用 `openssl rand -hex 16` 之类生成，不要把示例值直接上线。
- **PG 服务不发布宿主端口**：`postgres:17-alpine` 只在 compose 默认网络内以 `postgres:5432` 可达，
  宿主上 `docker port bot-harness-postgres` 是空的。这是刻意的暴露面收敛 —— 需要连库排查时
  用 `docker exec -it bot-harness-postgres psql -U bot -d bot`，不要把 5432 映射出来。
- PostgreSQL 走原生 TCP 协议，**不经 `HTTP(S)_PROXY`**，所以 `NO_PROXY` 不需要（也不该）为它加条目。
- 后台口令（`admin_token`）等同于管理员凭证，泄露后任何人都能改配置、读聊天记录。它存在数据库里、
  **只进不出**，首次启动随机生成并打印一次；轮换在后台「接入与运维」里做，旧会话会立即失效。
  请通过 HTTPS 反代访问后台。
- 端口暴露面由 `.env` 的 `ADMIN_BIND` 决定：compose 默认 `127.0.0.1:8080 → 容器 0.0.0.0:8080`，
  也就是只有宿主机回环可达。改成 `0.0.0.0` 等于向所有网卡暴露后台，只在可信网络 / 反代后面才这么做。
- 建议在后台「策略配置」页配置群白名单，把机器人限制在指定群内。
- 漏洞上报请走私密渠道，见 [SECURITY.md](SECURITY.md)；**不要**在公开 issue 里贴复现细节。

---

## 11. 版本与兼容性

当前版本 **0.3.0**（版本号待 Lead 确认；确认前构建产物可能仍标 0.2.0），变更记录见 [CHANGELOG.md](CHANGELOG.md)。`0.x` 阶段不承诺 API 完全冻结，
但会遵循 SemVer：破坏性变更进 minor，并写清迁移步骤。

| 项 | 要求 / 说明 |
|---|---|
| Go | **1.27+**（`go.mod` 指定 1.27.1） |
| Node / pnpm | 只在构建前端时需要：**Node 24**、**pnpm 11**（仓库锁定 `pnpm@11.25.0`） |
| 浏览器 | 支持 ES2022 与 `EventSource`（SSE）的现代浏览器 |
| 数据库 | **PostgreSQL 17**（推荐）或 **SQLite**（回退，单文件） |
| 容器 | Linux amd64 / arm64；以非 root 用户运行 |
| 升级 | 镜像/二进制启动时自动应用数据库迁移；跨版本升级前先按 [§9](#9-运维提示) 备份 |
| 配置 | 业务配置存数据库；`.env` 只有引导键 + 部署键，以 [`.env.example`](.env.example) 为准 |
| 数据迁移 | SQLite → PostgreSQL 用 `bot-harness migrate`，见 [docs/PG-MIGRATION.md](docs/PG-MIGRATION.md) |

设置的键集合由 `internal/settings/testdata/config-keys.json` 钉住（`scripts/check-config-keys.sh`）；
接口契约见 [docs/ADMIN-API.md](docs/ADMIN-API.md)，架构与决策见
[docs/DESIGN.md](docs/DESIGN.md) / [docs/DECISIONS.md](docs/DECISIONS.md)。

## 12. 贡献

欢迎 issue 与 PR。开发环境、提交前检查、提交规范、e2e 与「如何新增设置键」见
[CONTRIBUTING.md](CONTRIBUTING.md)；参与社区即表示同意 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。

- 提交前至少跑 `go vet`、`go test`、`bash scripts/privacy-scan.sh`；
- 改了 `web/src` 必须重新构建并提交 `web/dist`（`scripts/check-dist.sh` 会校验）；
- 改了设置键要更新配置 golden（`scripts/check-config-keys.sh --write`）；
- 提交需要 GPG 签名。

## 13. 安全

- **支持的版本**：最新 `0.1.x`。
- **私密上报**：使用 GitHub Security Advisories（**Security → Report a vulnerability**）；
  **禁止**用公开 issue 披露漏洞细节、令牌或内网信息。
- 部署加固要点：后台只绑回环 / 放在 HTTPS 反代后、PostgreSQL 不发布宿主端口、
  保持 `run_shell` 默认关闭、收紧 `admin_openids` 与群白名单。

完整策略与范围见 [SECURITY.md](SECURITY.md)。

## 14. 许可证与第三方组件

本项目以 **Apache License 2.0** 发布，全文见 [LICENSE](LICENSE)；版权与归属声明见 [NOTICE](NOTICE)。

- 第三方 Go / npm 组件、四款内嵌 OFL 字体（Noto Sans CJK SC / Noto Emoji /
  Noto Sans Math / Noto Sans Symbols2）、构建期工具与容器基础镜像的许可与版权行，
  统一列在 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
- 字体使用 **SIL Open Font License 1.1**，完整文本在
  [internal/render/fonts/](internal/render/fonts/)（`OFL.txt` 与各 `*-OFL.txt`）。
- SearxNG sidecar 是 **AGPL-3.0** 的独立容器镜像，不属于本仓库代码；
  以 compose 部署时请自行确认其许可义务。
- 本项目不附带任何私有 CA 证书、真实环境配置或凭据。

## 15. Star History

[![Star History Chart](https://api.star-history.com/svg?repos=Dynesshely/Bot-Harness&type=Date&theme=dark)](https://star-history.com/#Dynesshely/Bot-Harness&Date)

> 图表由 [star-history.com](https://star-history.com) 提供，仓库公开后自动生效。
