# AGENTS.md — Bot Harness

给在本仓库工作的开发者与 AI agent 的指南。它只描述**仓库内**的事实与约定；
第一次参与请先读 [CONTRIBUTING.md](CONTRIBUTING.md)，部署与使用见 [README.md](README.md)，
安全漏洞见 [SECURITY.md](SECURITY.md)。

## 0. 这是什么

QQ 群 / 私聊 AI 机器人：单个 Go 二进制，内嵌 Vue 3 管理后台，容器化部署。
接 QQ 开放平台事件 → 调 OpenAI 兼容模型 → 回群；外加 agent harness
（工具调用、记忆、联网、容器内执行、主动消息、承诺追问、人类状态推测）。

| 位置 | 内容 |
|---|---|
| `cmd/bot-harness/` | 入口、装配；`migrate` / `token` 子命令 |
| `internal/core/` | dispatcher（路由/策略）、turn、reply、agent（工具循环）、**polish**（两阶段生成的 stage 2）、**commands**（聊天命令）、**command_panel**、memory、proactive、tasks、commitments、humanize、human_status |
| `internal/affect/` | 情绪状态框架：tick 演化、事件分类门、注入与消毒、行为层、内置 `daily` 内容包 |
| `internal/qq/` | 平台 API、WS 网关、发送器、`panels`（指令面板封装） |
| `internal/ai/` | OpenAI 协议客户端（含 tools / tool_calls、多模态） |
| `internal/tools/` | 工具注册表、内置工具、SSRF 防线、容器内执行 |
| `internal/store/` | 持久化，PostgreSQL 与 SQLite 双后端：`dialect.go` 只做占位符改写与列自省；`pg.go` 持有 PG schema；SQLite 的版本化迁移在 `runMigrations` |
| `internal/pgmigrate/` | `bot-harness migrate`：SQLite → PostgreSQL 全量搬运 + 零丢失校验（自带 DDL，不依赖 store） |
| `internal/settings/` | 运行期配置（存数据库，支持按群覆盖）与键集合 golden |
| `internal/admin/` | REST + SSE 后台 |
| `internal/httpx/` | 出站 HTTP/TLS（可追加自定义 CA） |
| `internal/render/` | 通用表格 → PNG 渲染器与四款内嵌 OFL 字体 |
| `internal/testpg/` | 测试库守卫（仅测试使用） |
| `web/` | 管理后台；`web/dist` 已提交，非 Docker 构建直接 embed |
| `scripts/` | 提交前 / CI 检查：产物一致性、隐私扫描、配置键 golden、PG 备份恢复 |
| `.github/workflows/` | CI（vet/test/前端/隐私）与 Release（tag → GHCR + GitHub Release） |
| `deploy/` | 可选部署片段；`searxng/` 是默认启用的搜索 sidecar 配置 |

**先读**：`docs/DESIGN.md`（架构）、`docs/DECISIONS.md`（为什么这么做）、
`docs/ADMIN-API.md`（接口契约）、`docs/PG-MIGRATION.md`（切库与校验）。

## 1. 命令

需要 Go **1.27+**；改前端另需 Node **24** + pnpm **11**。

```bash
go vet ./... && go test ./... -count=1      # 提交前必须全绿
go test -race -count=1 ./internal/core ./internal/tools ./internal/settings ./internal/store ./internal/admin
cd web && pnpm install --frozen-lockfile && pnpm test
cd web && pnpm build                        # 后台改动必须重新构建（产物在 web/dist）
cd web && pnpm typecheck

bash scripts/privacy-scan.sh                # 隐私扫描（--staged 只扫暂存区新增行）
bash scripts/check-dist.sh                  # 重建 web/dist 并校验与提交产物一致
bash scripts/check-config-keys.sh           # 配置键 golden（新增键后加 --write）

docker compose up -d --build
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/api/health   # 期望 200
docker compose exec searxng wget -qO- http://127.0.0.1:8080/healthz          # 期望 OK

# --- 指令面板 -----------------------------------------------------------------
go test ./internal/core -run 'CommandPanel|SyncCommandPanel' -count=1 -v
go test ./internal/qq -run 'Panel' -count=1 -v
TOKEN=$(docker exec bot-harness /app/bot-harness token)
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:8080/api/actions/sync-commands

# --- PostgreSQL ---------------------------------------------------------------
docker compose up -d postgres && docker compose ps postgres                  # 期望 healthy
docker exec bot-harness /app/bot-harness migrate --from /data/bot.db --dry-run
docker exec bot-harness /app/bot-harness migrate --from /data/bot.db
docker exec bot-harness /app/bot-harness migrate --from /data/bot.db --verify-only
docker exec -it bot-harness-postgres psql -U bot -d bot -c '\dt'            # PG 无宿主端口
bash scripts/backup-pg.sh
bash scripts/restore-pg.sh ~/bot-harness-backups/bot-<UTC 时间戳>            # 先停机器人
```

**注意**：`migrate` / `token` 是当前二进制上的子命令；很旧的镜像可能不认识它们
（会被当成无参数启动）。时间列是原生 `TIMESTAMPTZ`，**不要用旧镜像连新库**；
升级/回滚走 `docs/PG-MIGRATION.md` §12 的停服流程。

## 2. 铁律

1. **配置只存数据库**。程序只读**数据库位置**这一件事：`DATABASE_URL`（PostgreSQL DSN）、
   `DB_PATH`（SQLite 路径）、可选的 `ENV_FILE`。`DATABASE_URL` 是引导值，
   在数据库打开之前就要用到，永远无法入库；规则只有一条 —— 去掉空白后非空就用它，否则用 `DB_PATH`。
   注意顺序：`DATABASE_URL` 里可以写 `${POSTGRES_PASSWORD}`，但程序用 godotenv 逐行解析，
   **被引用的键必须写在前面**（写在后面会展开成空串，静默拿到错口令）。
   `.env` 另有一些**部署键**只给 compose / 构建 / sidecar 用（代理、`ADMIN_BIND`、`VERSION`、
   `SEARXNG_SECRET`、`POSTGRES_PASSWORD` 等），完整清单以 `.env.example` 为准。
   **不要再往 `.env` 加业务开关**：QQ 凭证、传输、后台口令、监听地址、TLS、日志、媒体上限，
   以及模型/回复/能力/主动等全部走 `internal/settings`。新增配置项见 §3。
   旧 `.env` 的值由 `Manager.SeedFromEnv` 一次性搬进数据库，只导入「数据库还没有的键」。
2. **密钥只进不出**：`openai_api_key` 永不回显；`qq_app_secret` 永不回显；
   `admin_token` 只存数据库；日志与接口里的 DSN 一律 `redacted`。
   `.env` 已被 gitignore，**任何提交前都要扫一遍有没有把密钥带进 diff**。
3. **数据库改动走版本化迁移**，不要只改基础 schema（`CREATE TABLE IF NOT EXISTS` 不会给老库加列）。
   PostgreSQL 与 SQLite 两份 schema 必须同步修改，并补迁移测试。
4. **平台限制要当真**：被动回复 5 分钟 + 同 msg 最多 5 条；主动消息有配额；
   撤回仅 2 分钟；群消息不支持流式；单条消息超长要分段。
5. **提交规范**：Conventional Commits、一个提交只做一件事、提交必须签名。
   签名不可用时不要加 `--no-gpg-sign`，先解决本地密钥/agent。
6. **每期功能都要有独立开关 + 可按群覆盖 + 可单独回滚**；先写测试，再改代码。
   两阶段生成（润色）属于回复管线：它必须**既能全局关、也能按群关**
   （`polish_enabled` + `group_overrides[].polish`）。**润色失败绝不能升级成整轮失败**
   （`newPolish` 永远返回可发送文本），改这条路径先读 D-041 与 `internal/core/turn.go` 的注释。
7. **危险能力必须「开关 + 身份」双控**：`run_shell`、`settings_read`、`settings_write`
   除各自开关外还要求调用者在 `admin_openids` 里；工具定义下发层不向非管理员提供这些工具，
   工具内部再校验一次（审计记 `rejected`）。管理员名单为空时等于谁都不能用 ——
   `run_shell` 对空名单返回 `ErrNoAdminsConfigured`（显式错误，不是静默拒绝）。
   `run_shell` 要同时满足：全局 `exec_enabled`（默认关）、该群未被 `.exec` 关闭、
   `tools_enabled` 打开、调用者在名单、名单非空。
8. **`settings_write` 的两个例外不要顺手放开**：`openai_api_key` 与 `admin_openids`
   被硬编码在 `settingsDeniedKeys` 里（一个是密钥、一个是提权入口）。
9. **隐私**：真实主机名、内网 IP / 网段、代理、私有 CA 端点、个人绝对路径、口令与 token
   **绝不出现在仓库文件里**（代码、文档、测试、提交信息都算）。需要举例时用
   `one-api.internal.example`、`10.x.x.x`、`<局域网IP>`、`~`（而不是个人主目录）。
   提交前跑 `bash scripts/privacy-scan.sh`。
10. **改路由名要同步侧边栏**：`web/src/components/AppLayout.vue` 的 `NAV` 按路由 name 跳转，
    删/改一个 name 会让 `router.resolve` 在渲染期抛 `MATCHER_NOT_FOUND`。nav 会跳过不存在的
    name 并告警，但改名仍应同步更新（并检查 `{ name: '...' }` 形式的跳转）。
11. **改了后台接口的返回结构，必须同步前端与契约测试**：`PUT /api/config` 返回
    `{settings, warnings, restart_required}`；改 handler 响应同步 `docs/ADMIN-API.md`。
    `TestConfigUpdateContract` 钉的就是这个契约。
12. **命令 = 管理员身份门 + 只认 @/私聊 + 不进对话管线**（`internal/core/commands.go`）。
    三条都不能单独拆掉：非管理员静默（审计 `ignored`，原因含「非管理员」）；
    未 @ 的 `/xxx` 是普通消息；命中即执行 → 一条被动回复 → `return`，不调模型、不润色、
    不加人味延迟、不发提前回复、**不写会话上下文**。
    解析约束：`/name args`；或整条消息恰为一个已注册命令名（面板点击形态，`args` 为空）。
    无斜杠 + 额外单词不是命令；未注册的 `/xxx` 按普通消息走。
13. **`/help` 是公共命令，且只描述注册表**：`adminOnly=false`。输出只有
    `usage — desc`（管理命令追加「（仅管理员）」），不含配置值、OpenID、管理员名单或运行期状态。
    命令存在性本来就由指令面板公开；真正的门是执行时的 `isAdmin`。
14. **QQ 指令面板是展示层，只更新不删除**（`internal/core/command_panel.go` + `internal/qq/panels.go`）：
    marker 是唯一所有权凭据（`remark == "bot-harness:commands"`）；用户手工面板永不修改、
    **任何面板都不删除**。比较前两边都去掉 `name` 的前导 `/`（平台会剥掉）。
    两个 scope 各一次，一个失败另一个照跑；失败绝不阻塞启动（8s 起退避 6 次，只 WARN）。
    `command_panel_enabled` 只关自动同步，`POST /api/actions/sync-commands` 始终可手动触发。
15. **承诺追问的频率由模型判断，代码只做技术防呆**（`internal/core/commitments.go`）：
    只有用户显式回执才收口；不要加「问 N 次就放弃」或按次数递增的硬编码退避。
    代码只限制两次追问 `>=60s` / `<=180d` 与每人未收口上限。勿扰只认记忆，不设代码静默时段。
    追问原路返回（群→群、私聊→私聊）。模型/JSON 失败 → 模板 + 3h 兜底；发送失败 → 5min 退避。
16. **人类状态只是提示词参考，不是用户画像**（`internal/core/human_status.go`）：
    只推断活动状态，不推断敏感属性；结果只用于调整语气与承诺追问时机，不写进用户可见消息。
    阈值触发 + 同会话单飞；推测 goroutine 自己 `aiSem.Acquire`，**不要**在已持有
    `aiSem` 的回合里嵌套获取（`max_concurrent_llm=1` 会死锁）。视觉调用不得再 Acquire。
    `/human-status` 是管理员命令（别名 `humanstatus`），渲染不依赖列对齐；人数缺省 10、
    允许 1–200，且**只分析过去 24 小时在本会话发过言的人**。
17. **测试库隔离**：`PG_TEST_DSN`（store / media / admin / token）与
    `BOT_HARNESS_TEST_PG_DSN`（pgmigrate）只能指向 `bot_test` 或一次性 scratch 库。
    `internal/testpg.Guard` 在**建立连接之前**拒绝生产库名（默认 `bot`，可用
    `BOT_HARNESS_PROD_DB` 覆盖）；新增任何打开 PG 的测试入口都要先调它。
    **绝不**在 source 过 `.env` 的 shell 里直接跑 `go test`（继承的 `DATABASE_URL` 会被读到）。
    发布脚本必须**显式**设置测试 DSN，并清掉/隔离继承的 `DATABASE_URL`。
18. **富媒体：先上传、再发 `msg_type=7`，失败必须回退文本**：上传 body 固定
    `{file_type:1, file_name:"status.png", file_data:<base64>, srv_send_msg:false}`，
    取回 `file_info` 后发 `{msg_type:7, media:{file_info}}`；`srv_send_msg` 必须是 false。
    单张图 **<=5 MB**，本地先检查。命令输出统一走 `commandOutput{Text, Image, ImageAlt}`；
    渲染/上传/发送任一失败 → WARN → 发文本，命令本身仍算成功。
19. **字体是回退链**：`[NotoSansCJKsc, NotoEmoji, NotoSansMath, NotoSansSymbols2]`，
    逐 rune 用 `GlyphIndex != 0` 选 face。测量与绘制必须走同一套 rune-aware 逻辑
    （`fontChain.measure` / `fontChain.draw`）。彩色 emoji 不支持（x/image 只光栅化轮廓），
    ZWJ 序列不做塑形。新增字体 = 同字号、同基线、补 sha256 与覆盖范围。
20. **情绪状态是表达层，不是用户画像**（`internal/affect` + `docs/AFFECT.md`）：
    默认 `affect_enabled=false`；按群覆盖 `group_overrides[].affect` / `affect_inject_mode`。
    **全局 `affect_enabled` 是总开关**：群覆盖只能进一步关闭或改档（`false` 是可靠关闭），
    不能越过全局关。它只影响**语气、长度、分段、人味与主动性**，
    不进入事实、工具、白名单、限流与安全策略；数值不写进提示词，只注入自然语言片段。
    不推断年龄 / 性别 / 健康 / 政治 / 宗教 / 性取向等敏感属性，不写记忆与用户画像；
    **不做性 / 占有 / 成人内容**，不用内疚、恐惧或排他话术操纵用户。
    注入必须消毒（去换行 / 引号 / 括号、限 40 字）并按 `off` / `line` / `full` 控预算；
    读设置、选包、渲染或超时失败一律返回空串，**绝不能让 affect 让一条回复失败**。
    内容包只改数据（基线、事件、文案、行为档），**不改框架 / 表 / API**；5 维是框架契约。
    新增设置键按 §3 全流程（字段 / applyKey / 范围 / 后台 / golden / 文档 / 测试）；
    改行为或注入要补 core 单测、e2e、store 乐观锁与设置 golden。
21. **自主加入是群聊表达层**（`internal/core/join_gate.go` + `docs/JOIN-GATE.md`）：
    默认 `join_gate_enabled=false`；**全局开关是总开关**，按群覆盖 `group_overrides[].join_gate`
    只能进一步关闭或显式开启，不能越过全局关。**只处理群聊的未 @ 文本消息**——私聊、命令、
    空消息、机器人自己的消息、其他机器人的普通发言都不评估，不读私聊内容。
    **@ 消息永远跳过 join_gate**；join_gate 排在 trigger gate 之前，开启后 `at_only` 下
    被判定加入的未 @ 消息也会回复（关闭时行为不变）。
    **引用机器人也算被寻址**：入站的 `ref_msg_idx` 用 `store.IsBotQuote` 比对**本群、
    assistant** 消息的 `quote_index`，命中即 `AtBot=true` / `ReplyToBot=true`，与 @ 同样
    跳过 join gate。识别必须 fail-open（1s 超时、失败只 DEBUG、按未命中处理），
    **绝不能因为它拖慢或中断一条回复**；也不得把引用识别扩成跨群或跨会话匹配。
    **输出是三分类 `action` = `join` / `wait` / `skip`**（外加 `addressed_to`），不是布尔：
    `joined` 由 `action=="join"` 派生，`wait` / `skip` 只写 decision、不回复。`wait` 允许
    在 `join_gate_wait_retry_seconds` 后**由该群的下一条新消息**触发提前重评估（绕过评估冷却，
    仍受频率闸门约束）；**不要**给 `wait` 排定时器或后台轮询，那会凭空产生模型调用。
    模型只输出 JSON（判定 + 地址 + 话题，话题可带 `type` / `status` / `resumable`），
    不生成回复；判定加入后仍走原管线。
    **话题生命周期是单向棘轮**：模型给出 `active` / `dormant` / `closed` 与 `resumable`，
    代码按话题最新消息的年龄只降不升——`join_gate_topic_dormant_minutes`（默认 20）把
    `active` 降为 `dormant`，`join_gate_topic_closed_minutes`（默认 180）把任何非 `closed`
    降为 `closed`；时间流逝永远不会把 `closed` 升回来。`closed` 强制
    `resumable=false`（不接、不旧事重提），`active` 强制 `resumable=true`，`dormant` 保留
    模型值。保存前必须跑一次年龄兜底，缓存里只能存**已降级**的状态；读取回灌时以
    `analyzed_at` 作兜底时间重跑。上次话题最多回灌 **8 条 / 500 rune**，只用于判断
    「是否旧事重提」；回复背景只取 15 分钟内、≤400 字，全部 `closed` 时不注入。
    `closed` 是提示词 + `resumable=false` 的输入约束，**不要**把它写成代码层硬过滤
    `action` 的逻辑（模型仍可因明确重新提起而选择 `join`）；话题状态 / 类型只用于展示与
    提示，不参与频率、安全或权限判断。新增生命周期 / 阈值行为要补年龄降级、单向性、
    closed 不可接回、上次话题预算与回复块测试。
    解析失败 / 超时 / 非法 JSON / 非法 `action` / 拿不到并发额度一律 **fail-closed**
    （记 `action=skip`、`joined=false`），失败不插话、不能让一条本可回复的消息失败。
    decision 里的 `preview` **≤80 字**，日志不记消息全文；
    不把系统提示词交给模型，不写用户画像与记忆。**不做骚扰 / 广告 / 引流**，不按个人追问；
    频率是硬闸门（评估冷却、每小时 / 每天加入上限、最小加入间隔、置信度），超限不调模型。
    不做向量聚类、不跨群、没有规则版；改行为要补闸门 / 解析 / 引用识别 / fail-closed /
    触发顺序测试。
22. **图片工具是视觉能力的按需出口，不是把图片塞满上下文**（`internal/core/vision_parts.go` +
    `internal/tools/vision.go` + `docs/VISION.md`）：
    入站时把**当前消息**、**引用消息**与**合并 / 转发消息**里的图片都缓存下来：当前消息读顶层
    `attachments`，引用 / 合并必须递归展开 `msg_elements`（平台不把它们放进顶层字段），
    否则引用图永远没有 `media_id`。图片按 sha256 内容寻址；非图片附件只记元数据。
    模型可见的 id **只通过标注进入提示词**：当前消息 `[图片 #id]`、引用 / 合并
    `[引用图片 #id]`，历史消息里已缓存的图片也要在模型输入副本里标；标注**不写进落库正文**，
    也不改变消息内容。
    **只在 `vision_enabled` 且工具开启时下发 `view_image`**（全局开关；没有按群覆盖）；
    当前消息图片可按 `vision_max_images` 内联，引用 / 合并图片**不内联**，只走工具。
    关闭视觉时不要内联、不要声明工具；若模型被旧上下文诱导调用，工具内部还要再判一次并返回
    「图片不可用：视觉能力未启用」文本。
    **复用 `human_status_vision_model`**（空 = 主模型），不要新增视觉模型键；
    它与 `human_status_vision_enabled` / `analyze_image` 是两条独立开关，互不复用。
    工具参数只有 `media_id`（必填）与 `question`（可选，≤200 字），结果 ≤500 字；
    失败（id 不存在 / 过期、不是图片、未配置模型、视觉调用失败 / 空结果）**一律返回文本、
    不返回 Go error、不阻断回复**；下载 / 存储失败也只记 WARN，消息照常处理。
    图片只发给配置的视觉端点；不提供跨会话图片列表 / 搜索，工具审计不落返回描述；
    描述可能看错，不能当事实来源；默认提示词不推断年龄 / 性别 / 健康等敏感属性。
    改这条路径要补当前 / 引用 / 合并来源、历史标注、视觉关闭、失败降级与多模态回退测试。

23. **模型错误对用户可见，但只透出脱敏摘要**（`internal/core/ai_error.go`）：
    普通对话的模型失败兜底必须带摘要（HTTP 状态 / 上游 `error.message` / 超时 / 鉴权 /
    限流分类），不能只说「出错了，稍后再试」。摘要统一走 `userFacingAIError`：URL 主机与路径、
    `sk-…`、Bearer、`api_key`/`token`/`password`、`postgres://` DSN 一律替换；5xx 只透状态码；
    截断 220 rune。完整 err 只进日志与 `LastAIError`，同时 `finishRecord` 仍记 `failed`。
    reasoning-only 保持原有人话术（不把思维链倒给用户）；join_gate / human-status 等内部判定的
    失败文案不在透传范围。改这条路径要补 4xx/5xx/超时/鉴权/限流/密钥与 URL 泄漏测试。

## 3. 修改配置项

`internal/settings` 是运行期配置的唯一入口。新增一个键的完整顺序：

1. **字段**：`Settings` 结构体加字段与 json tag。
2. **应用**：`applyKey` 加分支（类型转换、空值语义）。
3. **校验**：`validateRanges` 加范围/枚举校验；非法值必须报错。
4. **后台表单**：`web/src/pages/settings/` 对应子页面加控件，接入 config draft 的脏字段追踪。
5. **按群覆盖（如适用）**：`group_overrides` 白名单与后台表格同步。
6. **测试**：settings 单测、后台契约测试、前端用例。
7. **golden**：`bash scripts/check-config-keys.sh --write` 更新
   `internal/settings/testdata/config-keys.json`。
8. **文档**：README 写默认值、范围与生效方式；需重启的键显式标注。

凭据类键永不回显；`settingsDeniedKeys` 不要顺手放开。

## 4. 测试

- **默认**：`go test ./... -count=1`。`internal/e2e` 会起内置的假 QQ / 假 OpenAI 服务端，
  不依赖外网与真实凭证；覆盖被动回复、去重、E1/E2、C1/C2、分段、窗口降级、限流、润色回退等。
- **竞态**：`go test -race` 覆盖 core/tools/settings/store/admin。
- **前端**：`cd web && pnpm test && pnpm typecheck`。
- **真实模型联调**（默认跳过，会消耗 token）：

  ```bash
  QQBOT_LIVE_TEST=1 ENV_FILE=../../.env go test ./internal/e2e -run TestLivePipeline -v
  ```

- **PostgreSQL**：DSN 只能指向 `bot_test` / scratch；测试入口先调 `testpg.Guard`。
  CI 没有 PostgreSQL，PG 用例靠本地或发布流程手动跑。
- **CI** 跑三组：go（vet + 全量 test + race 子集）、web（frozen lockfile、单测、产物一致）、
  hygiene（隐私扫描 + 配置键 golden）。提交前在本地跑同一组命令。

## 5. 排查入口（开发者视角）

- `GET /api/status`：连接、模型、并发、存储驱动；`GET /api/tool-calls`：模型实际调了什么。
- `GET /api/config`：可编辑配置（秘密只回 `*_set` 标志）；`PUT /api/config` 返回
  `{settings, warnings, restart_required}`。
- 日志：JSON 行到 stdout（`docker compose logs -f bot-harness`）；后台「日志」页是进程内环形缓冲。
- **润色**：`counters.polish_requests` / `polish_fallbacks`；每轮 INFO `two-stage reply`，
  回退 WARN `polish fell back to the draft`，跳过 DEBUG `polish skipped`。
- **Bot 命令**：审计里入站 `status=command`、出站 `replied`；非管理员是 `ignored`；
  命令**不会**出现在会话时间线（设计如此）。日志 `command handled` / `command failed`。
- **指令面板**：日志 `command panel synced` / `sync failed` / `auto sync gave up`；
  手动动作是 `POST /api/actions/sync-commands`（失败 `502 upstream_error`，保留平台 code/message）。
- **PostgreSQL**：`GET /api/status` 的 `store.driver` 是事实源；PG 没有宿主端口，
  连接一律 `docker exec -it bot-harness-postgres psql -U bot -d bot`。
  时间显示偏移先查 `TimestamptzCodec`；写库时间类型错误先查 `NullableTimeArg` /
  `TimeIsUnset` / `EmptyTimeLiteral`。

## 6. 代码约定

- **Go**：标准库优先；错误用 `%w` 包装并带上下文；需要取消/超时的路径传 `context`；
  不引全局可变状态；表驱动测试；注释解释「为什么」，不要复述代码。
- **前端**：Vue 3 `<script setup lang="ts">`；路由 **name** 是侧栏契约；颜色用 Tailwind token；
  页面只提交被改动的字段；契约解析集中在 `web/src/api/`；新增页面补路由与冒烟测试。
- **产物**：`web/dist` 是提交进仓库的构建产物，不是缓存；改源码必须重新构建。
- **文档**：行为变化同步 README / CHANGELOG；接口变化同步 `docs/ADMIN-API.md`；
  架构取舍同步 `docs/DECISIONS.md`。
- **依赖**：新增 Go/npm 依赖要评估许可（本项目 Apache-2.0）并更新
  [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)；不要把 MPL/AGPL 代码链接进二进制而不记录。
- **容器**：最终镜像只保留运行所需内容；非 root 用户；不要装不必要的包。

## 7. 隐私与安全

- 不提交 `.env`、数据库文件、日志、备份、截图中的真实凭据或内网信息。
- 例子一律用保留域名与占位符：`one-api.internal.example`、`10.x.x.x`、`192.168.x.x`、
  `<局域网IP>`、`~`。
- 后台口令等同管理员凭证；日志与 API 里必须保持 `redacted`。
- 漏洞请通过 GitHub Security Advisories 私密上报，不要开公开 issue（见 [SECURITY.md](SECURITY.md)）。
- 部署建议：后台只绑回环或放在 HTTPS 反代后、PostgreSQL 不发布宿主端口、
  保持 `run_shell` 默认关闭、收紧 `admin_openids` 与群白名单。
