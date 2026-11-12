# 设计说明（DESIGN）

> 需求决策见 [DECISIONS.md](DECISIONS.md)，前后端接口契约见 [ADMIN-API.md](ADMIN-API.md)。

## 1. 目标与约束

做一个能在 QQ 群里对话的机器人：接收 QQ 开放平台的事件 → 调用 OpenAI 兼容模型 → 把回答发回群里；同时提供一个能实时观察与调整它的管理后台；运行期配置存数据库（凭据与启动参数走 `.env`）；用 Docker 部署。

设计必须先满足平台侧的硬约束，这些约束直接决定了架构：

| 约束 | 来源 | 对本设计的影响 |
|---|---|---|
| 群聊默认只有 @机器人 的消息才推事件 | 平台权限模型 | 默认走 E1；E2/C2 需要群主开「接收所有消息」，后台按群选用并提示平台事实 |
| 被动回复窗口 5 分钟，同一条消息最多回 5 次 | 官方文档 | 回复策略只能「一次性整段回」，分段上限锁死 5；超窗降级主动消息 |
| 群消息不支持流式 | 官方文档 | 群里没有打字机效果，只能等生成完再发 |
| 主动消息有频控（未认证 30/qpm、单群 1000 条/天） | 官方文档 | 主动消息只做兜底，主路径永远是被动回复 |
| 单条消息可能重复推送 | 官方文档 | 必须按 `msg_id` 幂等 |
| `access_token` 有效期约 2 小时 | 官方文档 | 需要带缓存与提前刷新（过期前 60 秒） |
| 平台失败时 HTTP 仍返回 200 | 官方文档 | token 接口必须判业务 `code`，不能只看状态码 |

## 2. 技术选型与理由

| 层 | 选择 | 理由 |
|---|---|---|
| 语言 | Go | 单二进制、静态链接、`CGO_ENABLED=0` 可跑最小镜像；goroutine + channel 天然贴合「每会话串行、全局限并发」 |
| QQ 协议 | 自研薄客户端（标准库 `net/http` + `gorilla/websocket`） | SDK 迭代快且群聊/C2C 细节多，协议层只有几百行，自己掌握改动点 |
| AI | 自研 OpenAI 兼容客户端（SSE 流式累积 + 非流式回退） | 不绑定厂商；有的兼容端点会「宣称流式但报错」，回退一次比报错给用户更好 |
| HTTP 路由 | 标准库 `net/http.ServeMux`（Go 1.22+ 方法/通配模式） | 管理 API 只有二十来个端点，引框架不划算 |
| 存储 | **PostgreSQL（`pgx/v5`）为主，SQLite（`modernc.org/sqlite`，纯 Go）为回退** | 由 `DATABASE_URL` 选择：非空走 PG（连接池 8，可多实例/可在线热备），留空走 SQLite（无 cgo → 镜像很小，`MaxOpenConns(1)` 直接消除 `SQLITE_BUSY`）。详见 D-039 |
| 日志 | 标准库 `log/slog` | 结构化 JSON 到 stdout，同一份数据喂给内存环形缓冲供后台查询 |
| 实时推送 | SSE（`text/event-stream`） | 后台只需要单向推送，SSE 比 WebSocket 简单得多，浏览器 `EventSource` 自带重连 |
| 前端 | Vue 3 + Vite + Tailwind v4 | 无组件库、无图表库（SVG 手写），构建产物小且能 `go:embed` 进二进制 |
| 部署 | 多阶段 Dockerfile + compose | 前端在构建阶段产出 `web/dist`，Go 阶段 embed，运行阶段只留二进制 + CA 证书 |

## 3. 总体架构

```
                    ┌───────────────────────── 同一个 Go 进程 ─────────────────────────┐
QQ 平台              │                                                                 │
  │ 事件(WS/Webhook) │  internal/qq/ws.go ──┐                                          │
  └─────────────────►│  internal/webhook/ ──┴─► internal/core/dispatcher.go            │
                     │                              │  幂等 / 白名单 / 限流 / 触发策略    │
                     │                              ▼                                  │
                     │                       每会话串行队列（keyedMutex）                 │
                     │                              │                                  │
                     │                       internal/ai/client.go ──► OpenAI 兼容端点    │
                     │                              │                                  │
                     │                       分段 & 降级（qq/sender.go）                 │
                     │                              │                                  │
                     │                  internal/store（PostgreSQL / SQLite）            │
                     │                              │                                  │
   ◄─────────────────┤  internal/qq/api.go 发送消息  │                                  │
                     │                              ▼                                  │
浏览器 ──► /api/*    │  internal/admin（REST + SSE）◄── internal/bus（进度、消息、日志）  │
   └──► /            │  web/embed.go 托管 SPA                                        │
                     └─────────────────────────────────────────────────────────────────┘
```

关键点：**两个事件通道在 `dispatcher` 处汇合**，所以无论走 WebSocket 还是 Webhook，去重、限流、上下文、回复路径完全一致；同时开启时，同一事件的第二次到达会被 `msg_id` 幂等丢弃。

## 4. 关键设计

### 4.1 事件接入

- **WebSocket**：`GET /gateway/bot` 拿地址 → 连接 → 等 OpCode 10 Hello 取心跳间隔 → 发 OpCode 2 Identify（`Authorization` 语义为 `token: "QQBot <access_token>"`）→ 收 READY 记录 `session_id` → 周期发 OpCode 1 心跳（携带最新 `s`）。
  掉线后按情况处理：`4009`（连接过期）走 OpCode 6 Resume 补发；其他错误清掉 session 重新 Identify；连续 2 次心跳无 ACK 主动断开重连。退避 1s → 60s 指数增长并加抖动，`READY` 成功后立即复位（避免每次重连都等 60 秒）。
- **Webhook**：`X-Signature-Ed25519` / `X-Signature-Timestamp` + 原始 body 做 Ed25519 验签。密钥对由 `QQ_APP_SECRET` 按平台算法派生（重复填充到 32 字节作为 seed），因此**不需要额外配置公私钥**；URL 校验（op 13）用派生私钥对 `event_ts + plain_token` 签名后回包。验签失败计入 `verify_failures` 并返回 401。

### 4.2 事件处理管线

```
事件 → 解码 → 忽略机器人自己/空内容 → 屏蔽词 → 群/成员白名单
     → 触发策略（E1/E2）→ msg_id 幂等 → 落审计记录(status=received) → 入队
入队后（有界并发 + 每会话串行）：
     用户/群限流 → 会话是否暂停 → 取历史并裁剪
     → 【stage 1】调用主模型（含工具循环 / 多模态 / 提前回复）
     → 【stage 2】无上下文润色：只给「系统提示词 + 草稿」，按人设重写；任何失败回退草稿
     → 存 user / assistant 两条消息（assistant = 润色稿）→ 分段发送
     → 记忆抽取（reply = 润色稿）→ 更新审计记录(status=replied/failed)

命令分支（在 route 里、触发门之后、人味之前短路；详见 4.2.2）：
     触发门通过（「@机器人」或私聊）？
     ├─ 否 → 继续走普通管线（群里未 @ 的 /xxx 与 bare 命令名都走这条）
     └─ 是 → 形态匹配：① 首字符是 ASCII '/'，或 ② 整条消息恰为一个已注册命令名
             （② = 指令面板点击：平台剥掉 item.name 的前导 '/'，客户端原样填入）
             ├─ 都不是 → 继续走普通管线（全角 ／、多词消息都在这里）
             └─ 是 → lookupCommand 命中（命令已注册）？
                     ├─ 否（未注册）→ 继续走普通管线（不回「未知命令」）
                     └─ 是 → adminOnly 且调用者不在 admin_openids？
                             ├─ 是 → 静默（审计 ignored + reason「非管理员」），结束
                             └─ 否 → 执行 → 一条被动回复 → return
                                      入站审计=command / 出站=replied；
                                      不调模型、不润色、不写上下文、不计 ai_requests、不加人味延迟
```

- **每会话串行**：`keyedMutex` 按会话 ID 加锁，保证同一会话多轮不串序；等待超过 45 秒视为「还在忙」，E1 下回一句提示，E2 下静默丢弃（避免刷屏）。锁释放后条目会被清理，不会无限增长。
- **全局并发上限**：`dynamicSem` 是可改容量的信号量，后台调 `max_concurrent_llm` 立即生效——等待者通过「关闭旧 channel、换新 channel」被唤醒，同时始终尊重 context 取消。
- **有界队列**：入站 goroutine 由容量 128 的信号量限定，满了直接判 `ignored`，防止洪峰打爆内存。
- **命令为什么在这个位置**：它必须是**纯输入 → 纯输出**的短路，下游任何环节（人味、上下文、
  两阶段生成、记忆抽取）都不该作用在它身上。放在触发门之后是为了复用「@/私聊」这条既有语义；
  放在人味之前是因为「这次选择不插话」不能把一条管理员明确下达的指令随机丢掉。

#### 4.2.1 两阶段生成（stage 1 / stage 2）

一次回复由**两次**模型调用组成（`internal/core/turn.go` + `polish.go`，决策见 D-041）：

| | stage 1 | stage 2（润色） |
|---|---|---|
| 代码位置 | `converse` → `callModel`（`turn.go` / `agent.go`） | `newPolish`（`polish.go`） |
| 模型 | `model` | `polish_model`，空串 = 复用 `model` |
| 输入 | 系统提示词 + 历史 + 用户原文 + 工具结果 + 本轮图片 | **只有两条消息**：系统提示词（人设/格式/当轮记忆块）与草稿正文 |
| 工具 | 按 `tools_enabled` 声明 | **`Tools: nil`**（润色器不许查证） |
| 并发许可 | `processMessage` 在整轮开始时 `aiSem.Acquire` | **复用同一个许可，不再 Acquire** |
| 产出 | 草稿 `result.Text` | 润色稿；失败时就是草稿本身 |
| 下游 | — | 落库的 assistant 行、`extractMemories` 的 reply、实际发送文本**三者一致** |

**为什么 stage 2 没有上下文**：只给它"该怎么说"（系统提示词里的角色与格式）与"说什么"（草稿），
它就**没有任何可以编造新事实的材料**（历史里的细节、用户原文、工具结果都可能被它误当成改写素材）。
代价是它也看不到当轮的图片与工具审计——这是刻意的取舍。

- **系统提示词取最后一步实际用的那份**（`outcome.systemPrompt`）：工具循环会把当轮记忆块追加进去，
  用最初那份会漏掉。
- **超时** = `min(polish_timeout_seconds, 剩余 request_timeout)`；整轮预算已经耗尽时直接跳过。
- **跳过 vs 失败**是两件不同的事：开关关闭、草稿 trim 后为空、草稿 rune 数 < `polish_min_chars` 时
  连请求都不发（不计指标、不算失败）；**发出了但失败**（HTTP 错误 / 超时 / 空结果）才计 `polish_fallbacks`。
- **指标**：`counters.polish_requests` 与 `counters.polish_fallbacks`；每轮一行 INFO
  `two-stage reply`（`draft_chars` / `polish_chars` / `polished` / `fallback` / `polish_latency_ms`），
  回退时另有 WARN `polish fell back to the draft`。
- **不经润色的路径**：后台手动发送、主动消息（`proactive.go` 自己调模型）、错误兜底文案、
  提前回复的「让我想想」文案。
- **配置面**：`polish_enabled`（默认 true，总开关）、`polish_model`（默认空 = 复用 `model`）、
  `polish_temperature`（默认 0.7，`[0,2]`）、`polish_max_tokens`（默认 0 = 自动，显式取 `[1,8192]`）、
  `polish_timeout_seconds`（默认 20，`[5,120]`）、`polish_min_chars`（默认 0，`[0,2000]`）；
  按群覆盖只有开关 `group_overrides[].polish`（`bool | null`，`null` = 跟随全局）。
  默认开关的注意事项：**生产默认是开的**，而 e2e 夹具显式写 `false`（老用例断言"草稿原样送达"）。

#### 4.2.2 Bot 命令（`internal/core/commands.go`，决策见 D-042）

命令是**与对话并列**的一条路径，不是对话的一种。它接在 `dispatcher.route` 的触发门之后：

| 关注点 | 实现 | 为什么 |
|---|---|---|
| 识别 | 先判「`in.AtBot` 或场景为 `c2c`」（触发门），再认两种形态：`parseCommand` 走 **ASCII `/`** 前缀 + `strings.Fields` 切词；`parseBareCommand` 要求**整条消息恰为一个 token**。最后 `lookupCommand` 用 `ToLower` 的名字查注册表（`/xxx` 与 bare 名都查 registry；未注册 → 不是命令） | 只认"对机器人说的话"；全角 `／`、朋友间的 `/xxx` 梗、以及「@机器人 help me with X」都留在普通消息里。bare 形态是**指令面板点击**的产物：平台剥掉 `item.name` 的前导 `/`，客户端原样填入，所以点一下就发出 `tokenstats` |
| 注册 | `map[string]*command`，键是小写名与别名；`registerBuiltinCommands` 一处登记 | 加命令 = 一行登记 + 一个 `run`，不必改管线 |
| 未知 | `lookupCommand` 返回 `ok=false`，消息继续走普通路径 | 不回「未知命令」，否则命令集可被枚举 |
| 权限 | `commandAllowed` → `isAdmin`（`member_openid` / `user_openid` ∈ `admin_openids`） | 与 `run_shell` / `settings_*` 同一口径；空名单 = 谁都不能用 |
| 拒绝 | **静默**：`ignored` + 原因「非管理员」，不发送、不调模型 | 任何回执都等于确认命令存在 |
| 执行 | `handleCommand`：`cmd.run` → `commandOutput{Text, Image, ImageAlt}` → 一条绑定原 `msg_id` 的被动回复 | 同一条消息最多 5 条被动回复，命令只用 1 条；图片与文本共用这一个出口 |
| 审计 | 入站 `command`、出站 `replied`（失败 `failed` + `error`）；出站 `content` 在出图成功时是 `ImageAlt`（如 `[图片] 聊天对象状态推测（8 人）`），回退时是实际发出的文本；**入站先终态并先推 SSE**，出站随后 | "被当作命令处理"是与"被模型回答"/"被丢弃"并列的第三种结局；顺序固定，实时流不会先出现回复 |
| 上下文 | **不写** `messages` | 否则下一轮模型会模仿「管理员：/xxx → 助手：✅ 已…」这种对话 |
| 幂等 | 沿用已有的 `msg_id` `MarkSeen` | 平台重推一次不会执行两次 |

已内置两个命令：

- `/tokenstats [on|off|status]`（别名 `tokens`，默认 `status`）：写全局 `reply_footer_enabled`
  并走 `settings.Apply`（与 `PUT /api/config` 同一条落库路径）。
  **面板点击**发出的 bare `tokenstats` 命中后 `args` 为空 ⇒ 走默认动作 `status`；
  要开关必须带参数，而带参数必须带 `/`（`tokenstats off` 不是命令）。
- `/help`（`adminOnly=false`，公共）：列出注册表里的每个命令，格式 `usage — desc`，
  管理命令追加「（仅管理员）」；按规范名排序、别名折叠。输出**只来自注册表**，
  不含配置值 / OpenID / 管理员名单。它和所有命令一样短路，审计同样是 `command` / `replied`。

命令的输出是 `commandOutput{Text, Image, ImageAlt}`，不再假定"命令只有一段文字"：

| 字段 | 含义 |
|---|---|
| `Text` | 文本版本；`Image != nil` 时它同时是**回退内容** |
| `Image` | 已渲染好的 PNG 字节（不是 URL、不是表格结构） |
| `ImageAlt` | 出站审计/SSE 里描述这张图的文案，如 `[图片] 聊天对象状态推测（8 人）` |

`handleCommand` 的分支只有两条：`Image == nil` 走原来的 `Sender.Send(Text)`；
`Image != nil` 走 `Sender.SendImage`，成功则审计内容取 `ImageAlt`，失败则 WARN 后回退
`Sender.Send(Text)`（`Text` 为空则发「图片发送失败」）。**图片发送失败绝不等于命令失败** ——
命令已经执行完，丢掉的只能是排版。`/tokenstats` 与 `/help` 仍只返回 `Text`。

用户侧的用法与排查见 README「Bot 命令」；新增配置键时**不需要**动命令层。

#### 4.2.3 QQ 指令面板同步（`internal/core/command_panel.go` + `internal/qq/panels.go`，决策见 D-043）

面板是**平台侧的展示层**，与命令执行完全解耦：用户在 QQ 客户端点开指令面板即看到命令列表，
点一下把 `usage` 首词填进输入框（不发送）。同步把注册表投影成面板项并幂等对账。

| 关注点 | 实现 | 为什么 |
|---|---|---|
| 投影 | `commandPanelItems`：`type=command`、`name`=`usage` 首词**去前导 `/`**（rune ≤ 14）、`desc`=注册表描述（rune ≤ 30）、`adminOnly`→`only_admin`、按规范名排序 | 面板与 `/help` 共用一份描述；长度按字符而不是字节，中文不会切坏 UTF-8 |
| 所有权 | `remark=bot-harness:commands` | 平台面板没有归属字段，`remark` 是唯一能携带身份的地方；用户手工面板永不触碰 |
| 对账 | 无 marker → `POST`（`target_type=all`，`version=0`）；items 逐项相同（含顺序）→ `unchanged`；不同 → `PUT`（不传 `version`）→ `updated` | 语义是「让面板等于注册表」，不是「仅在版本未变时更新」；顺序稳定才会收敛到 `unchanged` |
| 范围 | `group` / `c2c` 各一次，互不阻塞 | 缺私聊权限不该拖累群面板 |
| 并发 | `Dispatcher.panelMu` 串行化自动与手动 | 两条路径并发会读到同一旧版本各写一次，白耗平台配额 |
| 删除 | **永不删除**（`DeletePanel` 不用于同步） | 删除无法证明所有权，平台侧面板对用户不可恢复 |

**位置**：`cmd/bot-harness/main.go` 在 `dispatcher.Start(rootCtx)` 之后启动一个**后台 goroutine**：

```go
dispatcher.Start(rootCtx)
go dispatcher.RunCommandPanelSync(rootCtx)   // 8s 后首跑；失败 30s/1m/2m/5m/10m 退避，共 6 次
go dispatcher.WarmIdentity(rootCtx)
```

- **为什么在后台 goroutine**：同步要打平台网络，且可能被权限/风控拒绝；放在主启动路径上会让
  「面板不可用」升级成「机器人起不来」。它只写日志，不返回错误、不 panic。
- **为什么延迟 8 秒**：token 与网络可能尚未就绪，太早跑只会白白消耗一次重试。
- **开关**：每轮重试前重读 `command_panel_enabled`（默认 true）—— 中途关掉立即停；
  关闭只挡自动同步，`POST /api/actions/sync-commands` 始终可手动触发。
- **可观测**：成功 INFO `command panel synced`（`component=command-panel`，带 scope / panel_id /
  action / version）；失败 WARN `command panel sync failed`；耗尽 WARN `command panel auto sync gave up`。
  平台的 `code`/`message` 原样透出（手动动作是 `502 upstream_error`）。排查见 README「QQ 指令面板」。

#### 4.2.4 承诺 worker（`internal/core/commitments.go`，决策见 D-044）

承诺是**与对话并列的第二条主动管线**：由时间驱动、由模型决定「这次问什么、下次什么时候问」。

```
每秒 tick（RunCommitmentScheduler）
  → commitment_enabled？否 → 直接返回
  → LeaseDueCommitments(now, 10, 2min)：status ∈ {waiting, leased} 且 next_check_at <= now
      把 status 改成 leased、next_check_at 写成租约到期（2 分钟）
  → 逐条 deliverCommitment：
       决策模型（独立一次调用，输出 message / next_check_seconds / reason）
       → 模型/JSON 失败：模板文案 + 3h 兜底（WARN）
       → 发送（群承诺回群、私聊回私聊；MaxSegments=1，不润色）
       → 发送失败：status 回 waiting、next_check_at = now + 5min（不计 nudge_count）
       → 成功：补一条 assistant / kind=manual 时间线 → status=waiting
               next_check_at = now + clamp(next_check_seconds, 60s, 180d)
               nudge_count+1、last_nudge_at=now、counters.commitment_nudges+1
```

| 关注点 | 实现 | 为什么 |
|---|---|---|
| 租约 | 不新增 `lease_until`：`status=leased` + `next_check_at` 就是租约到期 | 进程崩溃后租约自然过期，下一 tick 自动回收；少一列、少一套状态 |
| 频率 | 模型给 `next_check_seconds`，代码只 clamp 到 `[60s, 180d]` | 「什么时候再问」是生活常识；按次数退避/自动放弃会把它写死成产品规则 |
| 勿扰 | 决策提示要求检索「某时段不要打扰」类记忆 | 用户说过才算；代码不设静默时段 |
| 失败 | 决策失败 → 模板 + 3h；发送失败 → 5min 退避且不计数 | 承诺不能卡死，也不能把「没发出去」算成一次追问 |
| 收口 | 用户回执 → `ack_commitment`（done/cancel/postpone）；后台可 `DELETE /api/commitments/{id}`（仅 waiting/leased，409 保护终态） | 主路径是聊天，后台只是运营兜底 |
| 可观测 | 日志 `commitment nudged` / `commitment decision fell back to the template` / `commitment nudge failed to send`；counters `commitment_nudges` / `commitment_acked` / `commitment_postponed` | 追问是否真的发出、为什么没发，都能从一行日志定位 |
| 配置 | `commitment_enabled`、`commitment_max_open_per_member`；表由版本化迁移 `0011_commitments` 建立 | 关掉后 tick 直接返回，已存在的行保持不动 |

**位置**：`cmd/bot-harness/main.go` 在 `dispatcher.Start(rootCtx)` 之后
`go dispatcher.RunCommitmentScheduler(rootCtx)`。它和对话共用 `aiSem`，
但**不在回合里嵌套获取**：每条承诺是独立的一次模型调用。

#### 4.2.5 人类状态推测触发（`internal/core/human_status.go`，决策见 D-045）

```
入站消息 → noteHumanStatusMessage(conv)
  → human_status_enabled？否 → 返回
  → 该会话 pending++（内存计数，重启归零可接受）
  → pending >= human_status_trigger_messages（默认 20）？
       → startHumanStatusRefresh：同会话 humanRunning 单飞；pending=0
       → goroutine：aiSem.Acquire（自己拿，不在回合里嵌套）
                    → 一次模型调用（最近 40 条消息 + 本轮原文）
                    → 解析 JSON 数组 → UpsertHumanStatus（TTL 120 分钟）
                    → 失败只 WARN + counters.human_status_failures+1
```

| 关注点 | 实现 | 为什么 |
|---|---|---|
| 触发 | 按 conversation 累计新消息，达阈值即**异步**跑一次 | 推测是锦上添花，绝不能拖慢/阻塞回复 |
| 单飞 | 每会话 `humanRunning` 标记；进行中再触发直接返回 | 群消息密集时不会同时打十次模型 |
| 并发 | 推测 goroutine **自己** `aiSem.Acquire` | 在已持有 aiSem 的回合里再获取，`max_concurrent_llm=1` 会死锁 |
| 刷新 | 构建提示词前若缓存全过期且 pending 够，再异步刷一次；**本轮仍用旧缓存** | 回复延迟可预测，状态只是参考 |
| 注入 | 回复系统提示 + 承诺追问决策都读未过期行（昵称/状态/置信度，明确「可能不准」） | 同一份数据两处使用，避免两套口径 |
| 可见输出 | 只有管理员命令 `/human-status`（优先表格 PNG；文本列表 / markdown 代码块表格作为回退）；不写用户画像、不进长期记忆 | 状态是推测，不是事实；用户主动问才展示 |
| 过期 | `human_status_ttl_minutes`（默认 120）；维护循环 `PruneExpiredHumanStatuses` | 过时的「在睡觉」比没有更糟 |
| 输入 | 最近 `human_status_timeline_messages` 条按成员分组的时间线（含时间/图片 media_id） | 发言节奏与活跃时段比混合转录更有信息量 |
| 图片 | `human_status_vision_enabled` 才提供 `analyze_image`，单次 ≤4 次调用 / ≤2 轮；视觉模型 `human_status_vision_model`（空 = 主模型） | 图片只在有帮助时看，失败降级为「图片不可用」 |
| 配置 | `human_status_enabled` / `human_status_trigger_messages` / `human_status_ttl_minutes` / `human_status_timeline_messages` / `human_status_vision_enabled` / `human_status_vision_model`；表由版本化迁移 `0012_human_status` 建立 | 阈值与开关都能在后台调整，不必重启 |

#### 4.2.6 表格转图片与富媒体发送（`internal/render` + `internal/qq`，决策见 D-045）

`/human-status` 的渲染链路是「结构化数据 → PNG → 平台文件 → 富媒体消息」，任何一步失败都退回
文本，所以它只是**排版增强**，不是新的依赖：

```
rows[]humanStatusRow
  → render.Table{Title:"聊天对象状态推测（N 人）", Headers:[对象 状态 置信度 依据 更新], Rows}
  → render.RenderPNG(table)                    // internal/render，纯函数、无业务依赖
       ├─ 字体链 CJK→Math→Symbols2（逐 rune 选 face，均不覆盖才 .notdef）
       ├─ 失败 → WARN + commandOutput{Text: 文本列表}
       └─ 成功 → commandOutput{Text: 文本列表, Image: png, ImageAlt: "[图片] …（N 人）"}
  → handleCommand
       → Sender.SendImage
            → POST /v2/{groups|users}/{openid}/files
                 {file_type:1, file_name:"status.png", file_data:<base64 png>, srv_send_msg:false}
                 ← {file_info, ttl}
            → POST /v2/{groups|users}/{openid}/messages
                 {msg_type:7, media:{file_info}, msg_id?, msg_seq?=1, message_reference?}
       → 失败 → WARN（记录原因）→ Sender.Send(Text)
```

| 关注点 | 实现 | 为什么 |
|---|---|---|
| 渲染 | `render.RenderPNG(Table)`；字体回退链 `[CJK, Math, Symbols2]` 以 `go:embed` 打进二进制，逐 rune 选 face，2x 渲染，单元格自动换行，>40 行截断 | 不依赖宿主机字体，花体昵称/符号不出豆腐块，换行不靠调用方 padding |
| 上传 | 每张图先 `/files`（`file_type=1`、`file_data` base64、`srv_send_msg=false`），再发消息 | 平台没有"一步发图"接口；`srv_send_msg=true` 会多发一条消息 |
| 体积 | 上传前检查 ≤5 MB（平台限制），超出直接报 `APIError` | 本地失败比平台 400 更好排查，也省一次请求 |
| 被动回复 | `msg_id` 原样带上，`msg_seq=1`；`QuoteMessageID` 走 `message_reference` | 一张图就是一条消息，不存在多段编号 |
| 审计 | 成功记 `ImageAlt`；回退记实际文本；两者都写 `replied` | 后台不该展示二进制，也不该把"图片发送成功"记成文本 |
| 降级 | 渲染 / 上传 / 发送任一失败 → 文本；`Text` 为空 → 「图片发送失败」 | 命令已经执行，不能因为排版失败丢答案 |

### 4.3 上下文（C1 / C2）

| 模式 | 会话键 | 场景 |
|---|---|---|
| C1 `per_member`（默认） | `group:<group_openid>:<member_openid>` | 每个群成员各自一条上下文，互不可见 |
| C2 `shared`（需权限） | `group-shared:<group_openid>` | 整群共享；用户消息会带上「昵称：内容」前缀 |
| 单聊 | `c2c:<user_openid>` | 与用户一对一 |

会话 ID 用 base64url 编码成不透明字符串暴露给前端，避免前端拼装内部键。历史按「最近 N 轮 + token 预算」双重裁剪：token 用「CJK 每字 1 token、ASCII 每 4 字符 1 token」估算，预算 = `context_max_tokens - max_tokens`，从新到旧累加直到超预算。

> C2 的语义依赖群主的「接收所有消息」：默认权限下机器人只看得到 @自己的消息，「群共享记忆」就名不副实。
> 因此后台在选用 E2/C2 时会把每个群的平台实际权限列出来（哪些群还没开），但**不做硬拦截** ——
> 模式是运维的选择，权限是群主的设置，两者不一致时给出清楚提示即可（D-016）。

### 4.4 回复与分段

- 被动回复带 `msg_id` + 递增 `msg_seq`（1..N）。平台对 `msg_id+msg_seq` 去重，因此**重复发送会被平台拒绝**（`40054005`）——代码把该错误视为「已送达」而不是失败，天然获得发送幂等。
- 长文本 `SplitText`：优先按空行分段，其次按行，单行超长再按句末标点硬切；跨段时保持 ``` 代码块闭合（本段补 ```、下段补 ```）。
- 被动回复受平台「同一条消息最多 5 次」限制（`msg_seq` 1..5），默认只发 3 段，超出截断并加提示；主动消息没有这个上限，分段数由 `manual_send_max_segments` 决定。
- 被动窗口过期（`304103` / `40034005` / `40034128`）→ 自动改用主动消息兜底，并在日志中记录。

### 4.5 失败与降级

| 情况 | 行为 |
|---|---|
| 模型报错/超时 | 存下用户消息，群里回一句「出错了稍后再试」，审计记 `failed`，`last_error` 暴露到后台 |
| 模型返回空 | 视为失败（`ErrEmptyCompletion`） |
| **润色（stage 2）报错/超时/返回空** | **不降级整轮**：发送 stage 1 的草稿，审计仍记 `replied`，`polish_fallbacks` +1，WARN 一行写明 `reason` |
| 润色被跳过（开关关 / 草稿为空 / 短于 `polish_min_chars` / 整轮预算耗尽） | 发送草稿，两个指标都不动，只打 DEBUG |
| 并发排队过久 | 回「排队中」或静默丢弃 |
| 用户/群限流 | 静默丢弃并记 `ignored` + 原因 |
| 被动窗口过期 | 降级主动消息 |

> 两阶段的关键不变量：**stage 2 永远不返回"不可发送"的结果**（`newPolish` 的 `Text` 在回退时就是草稿），
> 所以调用分支里根本不存在「润色失败 → 这一轮失败」这条路径。

### 4.6 运行时配置

- **启动参数与凭据**（`.env` → `internal/config`）：QQ 凭证、监听地址、DB 路径、TLS、日志等，改完要重启。
- **运行期配置**（`internal/settings`，存数据库）：模型端点/密钥/模型名、采样参数、上下文与回复策略、
  两阶段生成（`polish_*` 六个键 + 按群 `polish` 覆盖）、指令面板自动同步（`command_panel_enabled`）、
  限流、白名单、按群覆盖等。
  用 `atomic.Pointer` 持有整份文档，读路径无锁；改动逐字段写库并立即生效。

模型端点与密钥**只**来自这里（`.env` 不再读取）：留空即「未配置」，模型调用会返回一条指路的错误，而不是悄悄回退到某个默认地址。密钥只进不出，接口永远只回「有没有配」。

`settings.Apply` 做范围与形态校验（温度 0–2、`reply_max_segments` 1–5、端点必须是 http(s) 等）。加载存量覆盖时若某字段非法，**只跳过该字段**而不是让进程起不来。

### 4.7 管理后台

- 认证：`POST /api/auth/login` 用数据库里的 `admin_token` 换一个 HMAC 签名、HttpOnly 的 Cookie（7 天）；也支持 `Authorization: Bearer <token>` 方便脚本调用。签名密钥即 `admin_token`，轮换口令会同时让旧会话失效（D-036 后 `.env` 不再承载口令）。
- 统一信封 `{"ok":true,"data":...}` / `{"ok":false,"error":{code,message}}`，HTTP 状态码同步反映结果。
- SSE 事件：`status`（每 5 秒全量快照）、`message`（收发审计，同一 `id` 可能先 `received` 后终态）、`log`、`ws`、`ping`（15 秒保活）。慢客户端丢事件而不是阻塞生产者。
- 静态资源：`go:embed all:dist`，`assets/` 下带 hash 的文件给一年 immutable 缓存，其余请求回退 `index.html` 以支持 history 路由。

## 5. 数据模型（PostgreSQL / SQLite 同一份逻辑 schema）

| 表 | 用途 | 保留策略 |
|---|---|---|
| `conversations` | 会话元数据（场景、群/成员、计数、最近活跃） | 长期 |
| `messages` | 会话上下文消息 | 按设置项 `history_ttl_hours`（默认 72h）每小时清理 |
| `records` | 收发审计（后台「消息记录」） | 保留最新 50000 条 |
| `dedup` | `msg_id` 幂等键 | 24 小时 |
| `settings` | 运行时配置覆盖 | 长期，可一键重置 |
| `stats` | 分钟级统计桶（重启后图表不空） | 7 天，内存聚合 + 30 秒落盘 |

- 共 **12 张表**（上表 6 张 + `tool_calls` / `memories` / `proactive_state` / `scheduled_tasks` / `media` / `schema_migrations`）。
- **两后端的列类型差异都收在这里**：`media.data` 是 `BLOB`（SQLite）/ `BYTEA`（PostgreSQL），
  `media.size` 两边都是 `BIGINT`，`memories.salience` 两边都是 `REAL` / `DOUBLE PRECISION`。
  布尔字段（`conversations.enabled`、`messages.addressed`）两种后端**都保留 `INTEGER`**。
- **时间列**：PostgreSQL 侧是原生 **`TIMESTAMPTZ`**（18 列：13 个非空、5 个可空无默认值，
  `NULL` 表示「未设置」），SQLite 侧仍是 `TEXT`（RFC3339）。
  **但 Go 侧两边都只看见字符串**：写进去的是 `2026-11-02T02:31:05+08:00` 形式的 RFC3339 参数，
  读回来的也是同一形态（PG 由连接上的 `TimestamptzCodec{ScanLocation: +08:00}` 保证，
  可空列的 `NULL` 变成 `""`）。所以 API 契约与迁移前完全一致，时区语义只存在于数据库里。
  差异被收在 `dialect` 的三个 helper 里（`NullableTimeArg` / `TimeIsUnset` / `EmptyTimeLiteral`），
  SQL 仍然只有一份 —— 为什么这么做、代价与未做项见 **D-040**。
- 没有外键：`records.conversation_id` 允许孤儿行（历史上跨会话/已删会话的审计记录必须留着）。

## 6. 可观测性

- `GET /api/health`：`200 ok` / `503 degraded`（凭证缺失或尚未拿到 token），供容器健康检查。
- `GET /api/status`：连接状态、session_id、心跳延迟、重连/Resume 次数、模型在飞请求数、token 有效期、存储用量、计数器
  （含两阶段的 `polish_requests` / `polish_fallbacks`）。
- 两阶段每轮一行 INFO `two-stage reply`（草稿/润色字数、模型、是否润色成功、两段延迟），
  回退时额外 WARN `polish fell back to the draft` 并带 `reason`；跳过只有 DEBUG `polish skipped`。
- 结构化日志统一带 `component` 字段（`ws` / `sender` / `dispatch` / `ai` / `admin` / `maintenance`），后台按组件与级别过滤。
- 敏感值只以掩码出现（`1020****`）。`QQ_APP_SECRET` 永不回显；模型密钥 `openai_api_key` 只进不出，接口只返回「是否已配置」。

## 7. 测试策略

| 层 | 手段 |
|---|---|
| 单元 | `SplitText`（长度/硬切/代码块闭合）、token 缓存与业务错误、Ed25519 派生与验签（含官方文档样例公钥）、设置校验与规范化、限流器、可调信号量、keyedMutex、上下文裁剪 |
| 组件 | OpenAI 客户端：SSE 累积、非流式回退、provider 错误、超时 |
| 端到端 | 假 QQ 服务端（token + gateway REST）+ 假 OpenAI 服务端，驱动**真实 dispatcher**，断言发送路径、去重、E1/E2 触发、C1/C2、分段、引用回复、手动发送、窗口过期降级、限流、白名单、屏蔽词、失败兜底 |

不依赖真实平台即可回归全部主路径；真实平台连接另由启动冒烟测试覆盖。

## 8. 私有 CA 与模型目录

### 8.1 出站 TLS 信任

三条出站链路（AI 网关 REST、QQ REST、QQ WebSocket）共用同一份 `httpx.TLSOptions`：

```
系统根证书池 ──追加──> TLS_EXTRA_CA_FILE 中的 PEM ──> tls.Config{RootCAs}
                                    │
      AI 网关 http.Client ──────────┤
      QQ REST http.Client ──────────┤
      QQ WS dialer.TLSClientConfig ─┘
```

- 追加而非替换：公网端点（`api.bot.qq.com`）依旧由系统根证书校验。
- `TLSOptions.Config()` 在没有任何额外配置时返回 `nil`，即完全沿用 Go 默认行为，
  不给默认路径增加风险面。
- 配置了文件却读不到、或文件里没有可用 PEM，都会在**组件装配阶段**返回错误让进程退出——
  把配置错误暴露在启动时刻，而不是第一次对话时。
- WebSocket 走的是 `gorilla/websocket` 的 `Dialer.TLSClientConfig`，与 REST 共用同一份配置，
  避免「REST 通、长连接握手失败」这类不一致。

### 8.2 模型目录

`GET /api/models` 代理上游 `{当前生效端点}/models`（端点来自数据库设置），服务端按 5 分钟 TTL 缓存（`?refresh=1` 强制刷新），
避免后台每次打开设置页都打一次上游。响应解析刻意做得宽松：

| 上游形态 | 是否接受 |
|---|---|
| `{"object":"list","data":[{"id":...}]}` | ✅ OpenAI 标准 |
| `{"models":[{"id":...}]}` | ✅ 部分网关 |
| `{"data":["model-a","model-b"]}` | ✅ 只给字符串数组 |
| `["model-a","model-b"]` | ✅ 裸数组 |

结果统一按 `id` 升序返回，并附带 `current` / `current_in_list`，让后台能立刻提示
「当前模型不在可用列表里」。模型本身仍由 `PUT /api/config` 的 `model` 字段修改——
`/api/models` 是只读的目录接口，不承担写入职责。
