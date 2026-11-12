# 决策记录（DECISIONS）

本文件是唯一权威的需求决策落盘处。对话里定下的事项一律同步到这里，避免决策只存在于上下文。

> 每条记录的是**当时的决定与实现**；后来被推翻或演进的，会写明「被 D-0xx 取代」。
> 需要「现在到底怎么工作」时，看代码与 [DESIGN.md](DESIGN.md)；需要「为什么这么做」时，看这里。

## D-001 语言与运行时：Go

- **决定**：服务端使用 Go（1.25+），单二进制交付。
- **理由**：部署最轻、并发模型直接对应「每会话串行 + 全局并发上限」、静态链接无运行时依赖，适合容器。
- **影响**：AI 生态弱于 Python，因此不引入重型 SDK：QQ OpenAPI 与 OpenAI 协议均**自研薄客户端**（标准库 `net/http` + `encoding/json`）。
- **时间**：2026-xx（用户确认 A3）

## D-002 事件通道：WebSocket 为主，Webhook 可选（B3）

- **决定**：同时实现两种通道，`QQ_TRANSPORT` 支持 `websocket`（默认）/ `webhook` / `both`。
- **理由**：WebSocket 不需要公网 IP、域名与证书，是默认部署形态；Webhook 适合已有公网 HTTPS 入口的场景。两者共用 `dispatcher` 与 `sender`，协议层差异只在入口。
- **影响**：
  - WebSocket 侧需实现 Identify / Heartbeat / Resume / 重连退避。
  - Webhook 侧需实现 Ed25519 签名校验（`X-Signature-Ed25519` + `X-Signature-Timestamp`）与平台回调校验（op 13）。
- **时间**：2026-xx（用户确认 B3）

## D-003 上下文范围：C1 为默认，具备权限时启用 C2

- **决定**：
  - **C1（默认）**：每个会话按 `group_openid + member_openid` 独立存上下文，群成员之间互不可见。
  - **C2（可选）**：整群共享一份群记忆，仅在平台已授予群全量消息权限时**可用**（权限校验已改为后台提示，见 D-016）。
  - 运行时可经 Admin 面板在 C1/C2 间切换（`context_mode`），并支持按群覆盖。
- **理由**：默认权限模型下机器人只看得到 @自己的消息，做群共享记忆是错的；能力具备时再开启才正确。
- **影响**：存储以 `scene` 区分 `group_member` / `group_shared` / `c2c`；C2 会把所有群成员的发言并入同一上下文。
- **时间**：2026-xx（用户确认 C1 + C2）

## D-004 触发模式：默认 E1，支持 E2

- **决定**：
  - **E1（默认）**：只有 @机器人 的消息才触发回复（`GROUP_AT_MESSAGE_CREATE`）。
  - **E2（可选）**：群内任意消息都触发（依赖群全量模式 `GROUP_MESSAGE_CREATE`，即 C2 的权限前提）。
  - 运行时可切换；E2 下支持「冷却时间 + 每群 QPM」二次限流，避免刷屏。
- **理由**：E1 是平台默认权限下的唯一可行形态；E2 需要权限且必须限流兜底。
- **时间**：2026-xx（用户确认 E1 + E2）

## D-005 项目位置与形态

- **决定**：项目根目录 `bot-harness/`，单仓库、单二进制交付。
- **时间**：2026-xx（用户确认 D1）

## D-006 管理后台：Vue3 + Tailwind，同服务端托管

- **决定**：
  - 前端 Vue 3 + Vite + Tailwind CSS（纯 utility class，不引入组件库），构建产物通过 `go:embed` 打进同一个二进制。
  - 后端在 `/api/*` 暴露管理接口，`/` 提供 SPA 静态资源与 history fallback。
  - 单一容器、单一端口：`docker compose up -d` 即同时得到机器人与后台。
- **理由**：用户要求「服务端和当前 qq bot 是同一个服务端」，同二进制 + 同端口最省运维。
- **影响**：`web/dist` 必须先构建再 `go build`；仓库内保留占位 `web/dist/index.html` 以保证未构建前端时 Go 侧仍可编译。
- **时间**：2026-xx（用户确认）

## D-007 AI 接入：OpenAI 兼容协议，自研薄客户端

- **决定**：`OPENAI_BASE_URL` + `OPENAI_API_KEY` + `OPENAI_MODEL` 驱动，直接调用 `POST {base_url}/chat/completions`，支持 SSE 流式累积（群聊侧仍整段发送）。
- **理由**：任意兼容端点（DeepSeek / 通义 / 自建 vLLM）可换，不绑定厂商 SDK。
- **时间**：2026-xx

## D-008 群聊不支持流式（平台约束，非我方选择）

- **事实**：QQ 群聊消息不支持流式参数，流式仅在单聊可用。
- **因此**：群里表现为「等待后整段回复」，不做打字机效果。
- **时间**：依据官方文档《发送群聊消息》

## D-009 存储：SQLite（纯 Go 驱动）—— **已被 D-039 取代**（时间列类型另见 D-040）

> **状态：仅作历史记录保留。** 2026-11-01 之后存储改为「PostgreSQL 为主、SQLite 为回退」，
> 当前事实见本文档末尾的 **D-039**。下面这段只说明当时的取舍，
> 不要在它上面继续推导新结论（例如"备份用 `backup-db.sh`"已经作废）。

- **决定**：`modernc.org/sqlite`（纯 Go，无 cgo），单文件落盘于挂载卷 `/data/bot.db`。
- **理由**：容器构建可用 `scratch`/`distroless`，无需 cgo 与 gcc；单实例场景 SQLite 足够。
- **影响**：若将来多实例，需要迁移到外部存储；已在 store 接口层预留替换点。
- **时间**：2026-xx

## D-010 私有 CA 信任：追加式 extra CA bundle

- **决定**：新增 `TLS_EXTRA_CA_FILE`（可多个、冒号分隔），把内网 CA 根证书与中间证书
  **追加**到系统根证书池之上，同时作用于 AI 网关、QQ REST 与 QQ WebSocket 三条出站链路；
  文件缺失或非法时**启动即失败**。
- **理由**：自建模型网关常使用内部私有 CA，容器镜像的系统信任库不含它。
  追加而非替换，保证公网端点（QQ 开放平台）不受影响；启动即失败比首次调用时抛出
  `x509: certificate signed by unknown authority` 更容易定位。
- **兜底**：`TLS_INSECURE_SKIP_VERIFY=true` 可关闭校验，程序会打印显式警告——它会同时
  取消对 QQ 平台的校验，因此只作为应急手段。
- **时间**：2026-xx（用户提出「需要添加域名的 CA 和中间证书」）

## D-011 模型目录：/models 代理 + 后台可选模型

- **决定**：
  - 后端新增 `GET /api/models`，代理上游 `{OPENAI_BASE_URL}/models`，服务端缓存 5 分钟，
    `?refresh=1` 强制刷新；
  - 后台「系统设置」的模型字段改为下拉选择（可切换手动输入），保存仍走 `PUT /api/config` 的 `model`；
  - `OPENAI_MODEL` 语义收敛为「默认值」：运行时以数据库覆盖为准，「重置配置覆盖」后回退到它。
- **理由**：网关可用模型会随时增减，硬编码模型名容易踩空；让运维从真实列表里选，降低配错概率。
- **兼容**：上游返回 `{"data":[…]} / {"models":[…]} / {"data":["a","b"]} / 裸数组` 均被接受，
  因为各类 OpenAI 兼容网关的实现并不统一。
- **默认值**：`.env` 中设为 `deepseek/deepseek-v4.1-flash`。
- **时间**：2026-xx（用户要求支持 /models 端点与后台选择模型）

## D-012 提前回复与回复页脚

- **决定**（用户提出）：
  - `early_reply_enabled`：模型超过 `early_reply_delay_seconds`（默认 3s）仍未产出时，
    先用**被动回复**发一句 `early_reply_text` 安抚用户；
  - 该提前回复会占用同一条 `msg_id` 的 5 次被动回复额度之一，因此**正式答案改用主动消息推送**；
  - `reply_footer_enabled`：每条**真实 AI 回复**末尾追加 `—— 用时 3.2s · 约 128 tokens`；
  - 提前回复与错误兜底文案**不追加**页脚（它们不是 AI 回复）。
- **理由**：群聊不支持流式，长回答会让用户干等；先给反馈再补结果体验更好。
- **实现要点**：模型调用在独立 goroutine 中执行，主流程用 `select` 在「结果」与「计时器」之间等待；
  提前回复成功才把后续发送切换为主动消息（否则保持被动回复）。
- **代价**：主动消息受平台频控（未认证 30/qpm、单群 1000 条/天），
  且部分用户在客户端关闭「允许主动发送」时会收不到答案；因此该功能可关闭，并记入审计（`status=early_reply`）。
- **页脚只用于展示**：写入上下文的仍是模型原文，避免把「用时/tokens」喂回模型。
- **时间**：2026-xx

## D-013 群全量模式下的触发判定与「不回复也要有解释」

- **背景**（线上故障）：群管理端开启「接收所有消息」后，平台改为推送 `GROUP_MESSAGE_CREATE`，
  **不再推送 `GROUP_AT_MESSAGE_CREATE`**；而本期实现把全量事件在 `at_only` 下整条丢弃，
  于是「@机器人也不回」，且被丢弃的消息不留任何痕迹，运维无从判断。
- **决定**：
  1. `at_only` 的判定从「事件类型」改为「是否被 @」：`GROUP_AT_MESSAGE_CREATE` 或
     全量事件里 `mentions` 含 `bot: true` 的条目；
  2. **任何被丢弃的入站消息都要落审计记录**，`status=ignored`，`error` 写明原因
     （含事件类型、mentions 数量、是否 @ 到机器人）；
  3. 幂等（`msg_id` 去重）提前到策略判定之前：每条唯一入站消息恰好一条记录；
  4. 首次收到全量事件即置 `full_group_detected`，在 `/api/status` 暴露，
     并在平台未开全量权限时于策略页给出提示（`QQ_FULL_GROUP_MODE` 此后废弃，见 D-016）；
  5. 记录前 3 条全量事件原始样本到日志，便于核对 `mentions` 的真实结构。
- **理由**：丢消息是黑盒，必须可解释；@ 的判定依据平台字段而非猜测。
- **时间**：2026-xx

## D-014 未 @ 的群消息：只进时间线，不进上下文

- **决定**（用户提出）：全量模式下未 @ 机器人的群消息
  1. 写入会话（`messages.kind='observed'`，带 `speaker` 昵称），出现在会话详情时间线里；
  2. **不进入模型上下文**（`RecentMessages` 只取 `kind='dialogue'`）；
  3. **不触发回复**（仍受 `trigger_mode` 控制）。
- **理由**：用户明确要求「不必实现额外开关，直接在会话页面显示这些消息即可」；
  同时把旁观消息排除在上下文之外，避免模型被大量与自己无关的群聊带偏。
- **C1 隔离**：观察消息按其发送者归属到该成员自己的会话，不会串进其他人的视图。
- **昵称**：`messages` 新增 `speaker` 列；存量数据通过版本化迁移回填（C1/C2 从会话元数据、
  共享上下文从「昵称：内容」前缀提取）。
- **附带修复**：全量事件的 `content` 保留 `<@OPENID>` 原始标记（平台只在 AT 事件里剥离），
  现在统一清除，避免原始 openid 进入提示词。
- **迁移机制**：新增 `schema_migrations` 表，迁移按名执行且只执行一次——
  之前「列已存在就跳过」的写法导致回填类修复永远不会补跑。
- **时间**：2026-xx

## D-015 多机器人同群：先确认「自己是谁」

- **背景**（线上故障）：群里有两个机器人时，@ 另一个机器人也会让我们回复；
  并且另一个机器人的发言完全没有进入会话时间线。
- **根因**：原实现把「`mentions` 里出现 `bot: true`」等同于「在 @ 我们」。
  平台只告诉我们被 @ 的是个 bot，没说是不是自己；同时 `author.bot == true`
  被一律丢弃（本意是防自我循环），把别的机器人也一起丢了。
- **平台依据**：`GET /v2/groups/{group_openid}/bot_state` 返回本机器人在该群的
  `member_openid`，正是 `mentions[].member_openid` 的取值空间。实测本应用可正常调用，
  同时还能拿到 `recv_msg_setting`（all / only_mention）与 `allow_proactive_msg`。
  另 `GET /users/@me` 返回自身 `id` / `username`；`GET /v2/groups/{id}/info` 返回群资料。
- **决定**：
  1. 身份解析优先级：`bot_state.member_openid` → `QQ_BOT_OPENID` → `/users/@me` 的 username；
     三者都无法确定时**不把 bot mention 当作 @ 自己**（宁可沉默）；
  2. `author.bot == true` 分两类处理：是自己 → 丢弃（防循环）；是别的机器人 →
     记为 `observed_bot`，镜像进该群的每个会话，不进模型上下文、不触发回复；
  3. 群内状态（接收设置 / 主动消息 / 自身 openid）在会话详情页直接展示平台事实。
- **理由**：多机器人共存是群聊的常态，@ 判定必须精确到「openid 相等」而不是「类型相同」。
- **代价**：每个群首次收到消息时多一次 bot_state 调用（按群缓存 30 分钟，并发去重）。
- **时间**：2026-xx

## D-016 触发/上下文模式改为数据库配置，权限按平台事实提示

- **背景**（用户提出）：`QQ_FULL_GROUP_MODE` 作为硬闸门不方便——群里的权限是逐群设置的，
  用一个全局环境变量既表达不了，改一次还要重启；用户已经开好了权限，后台却仍然拦截。
- **决定**：
  1. 删除 `settings.GateError` 与一切基于 `QQ_FULL_GROUP_MODE` 的强制降级/400 拦截；
     `trigger_mode` 与 `context_mode` 只是普通的运行时配置，直接存库、立即生效；
  2. 权限判断改用平台事实 `GET /v2/groups/{group_openid}/bot_state` 的 `recv_msg_setting`
     （逐群），在后台如实展示并在模式与平台不一致时给出提示，**不硬拦**；
  3. `QQ_FULL_GROUP_MODE` 标注废弃：设置后不改变行为，只在启动日志打印警告。
- **理由**：配置是运维的选择，权限是群主的设置，两者本就不该耦合；
  把权限做成闸门会导致「明明是平台侧还没开，用户却以为是程序不给」。
- **影响**：策略页不再有 disabled 状态与 400 错误码；新增平台权限面板（逐群列出 all / only_mention）。
- **时间**：2026-xx

## D-017 即时回复的撤回

- **决定**（用户提出）：正式答复送达成功后，通过
  `DELETE /v2/groups/{group_openid}/messages/{message_id}` 撤回那条「让我多想会儿」，
  由 `early_reply_recall`（默认开）控制。
- **平台约束**：消息发出超过 **2 分钟**不可撤回（`40064004`）；普通成员只能撤回自己发的消息
  —— 我们用的正是发送接口返回的消息 ID，因此无需管理员权限。
- **边界**：正式答复**发送失败时保留**安抚语（否则用户什么都看不到）；
  超过撤回窗口则保留并记 INFO 日志；撤回失败不影响答复本身的成功状态。
- **时间**：2026-xx

## D-018 是否回复其他机器人（reply_to_bots）

- **背景**（用户提出）：群里另一个机器人 @ 我们时不会触发回复。
  原实现把 `author.bot == true` 一律只记录不回复（本意是防自我循环），把「被别的机器人 @」也一起挡了。
- **决定**：
  1. 新增运行时配置 `reply_to_bots`（存数据库、后台可改、**默认允许**）；
  2. 开启后，**只有明确 @ 到我们**的机器人消息才会走正常回复流程
     （AT 事件，或全量事件里 `mentions` 命中我们自己的 openid）；
  3. 机器人**没有 @ 我们**的发言仍然只记录（即使 `trigger_mode=all_messages`）——
     否则两个机器人会互相刷屏；
  4. 新增**机器人互回链条熔断**：同一群内连续回复机器人达到 `BotChainLimit`（5）次后停止并记 WARN，
     真人一发消息立即清零。
- **理由**：机器人和真人不同，它们会立刻回话，缺少熔断就是一个死循环；
  同时用户确实需要「被别的 bot @ 时正常应答」。
- **熔断粒度**：按**群**而非会话计。C1 下机器人消息落在它自己的会话、真人消息落在真人的会话，
  按会话计数会导致真人永远无法打断链条（实现时踩到，已修正）。
- **时间**：2026-xx

## D-019 同群会话合并

- **背景**（用户提出）：群从 C1 切到 C2 后，会话管理页残留一串分成员的会话，需要合并消除。
- **决定**：新增 `GET /api/groups/mergeable` 与
  `POST /api/groups/{group_openid}/conversations/merge`（支持 `dry_run`），
  在会话管理页以「合并同群会话」对话框呈现。
- **严格同群校验**（用户特别强调）：
  1. 源会话由 `WHERE group_openid = ?` 选出，并**逐行复核** `group_openid` 一致；
  2. 目标会话必须也属于该群，否则报 `group_mismatch` 并拒绝；
  3. 只接受 `group_member` / `group_shared` 场景，C2C 永不参与；
  4. 该群当前生效的上下文模式必须是 `shared`，否则拒绝（合并出来的会话没人用）。
- **数据安全**：`UPDATE messages SET conversation_id = ?`（改归属而非复制），
  自增 id 不变即顺序不变；整体一个事务；重复执行不产生副本。
  另一个机器人的消息此前被镜像进每个会话，合并时按 (内容, 发送者, 时间) 去重。
- **时间**：2026-xx

## D-020 图片：本地缓存 + 多模态输入

- **背景**（用户提出）：上下文消息里图片只显示成「[附件消息]」；希望正常显示，并让模型能看图。
- **实测**：当前端点（`deepseek/deepseek-v4.1-flash`）**支持多模态**——
  传 `image_url` + `data:` URI 能正确识别图片内容（用一张纯色图验证，模型答出「红色」）。
- **决定**：
  1. 新增 `media` 表（内容寻址 sha256）与 `/api/media/{id}`，入站时把图片抓回本地；
     平台 URL 的 rkey 会过期，只有缓存下来历史消息才不会烂掉；
  2. 会话详情内嵌缩略图 + 点击放大，非图片附件列成说明行；
  3. 本轮带图时改用多模态 content parts，图片以 `data:` URI **内联**发送
     （网关在内网，不能假设它能访问公网 CDN）；
  4. **只发本轮**：历史图片不重复发送，避免每轮都为旧图付 token；
  5. 多模态请求失败时自动退回纯文本重试一次；
  6. 附件只允许从 QQ 域名下载、限制体积（默认 4MB）与张数（默认 2），
     失败保留占位而不丢消息；过期且无引用的缓存定时清理。
- **时间**：2026-xx

## D-021 拆分「触发模式」与「上下文范围」

- **背景**（用户提出）：`at_only` 同时决定了「要不要回复」和「模型能看到什么」，
  导致想让机器人「听着但不插话、被 @ 时能接上话」做不到。
- **决定**：新增 `context_scope`（`addressed_only` | `include_unaddressed`，默认前者），
  与 `trigger_mode` 完全解耦：
  - `trigger_mode` 只决定是否回复；
  - `context_scope` 只决定旁观消息（`observed` / `observed_bot`）是否进入模型上下文。
- **实现**：`RecentMessages` 增加 `includeUnaddressed` 参数；
  `BuildChatMessages` 给旁观消息加「昵称：内容」前缀，否则模型看到一串没有归属的 user 消息。
- **与上下文模式的交互**（实测发现）：C1 下旁观消息按发送者分属各人的会话，
  所以每个成员只纳入**自己**的发言；要听懂整群必须用 C2。
  后台在开启该范围时逐群提示，并点名仍处于 C1 或平台侧仍是 `only_mention` 的群。
- **代价**：上下文体积显著增大，受 `context_max_turns` / `context_max_tokens` 约束。
- **时间**：2026-xx

## D-022 按群覆盖补齐上下文范围与机器人共存

- **背景**（用户提出）：策略页的「按群覆盖」只能设触发模式 / 上下文模式 / 群限流，
  新增的 `context_scope` 与 `reply_to_bots` 没有按群维度。
- **决定**：`GroupOverride` 增加 `context_scope` 与 `reply_to_bots`（后者用 `*bool`，
  以便区分「显式禁止」与「跟随全局」）；`GroupConfig` 统一解析，调度器改为读取**按群生效值**：
  - `processMessage` 的上下文范围取 `cfg.ContextScope`；
  - `dispatchBotMessage` 的机器人共存取 `cfg.ReplyToBots`。
- **语义**：留空 / `null` = 跟随全局；覆盖优先于全局。
- **前端**：覆盖表格新增「上下文范围」「机器人共存」两列，均为「跟随全局 / …」三态选择。
- **顺带修掉的坑**：前端用 `null` 表示「跟随全局」，但
  `json.Unmarshal` 会**复用切片底层数组的元素**，而 JSON `null` 对 `string` 这类非指针字段是
  **no-op** —— 结果 `"context_scope": null` 清不掉旧值（`*bool` 的 `reply_to_bots` 反而正常，
  因为 null 对指针是置 nil）。现在解 `group_overrides` 前先置空切片，保证元素从零值开始解码；
  这也顺带避免了直接改写当前生效配置共享的底层数组。已用测试固定。
- **时间**：2026-xx

## D-023 工具调用（Tool Calling）：再次搁置

- **背景**：完整方案见 [TOOL-CALLING-PLAN.md](TOOL-CALLING-PLAN.md)（探针实测、六条路线取舍、
  Timer / Web 抓取 / Web 搜索、SSRF 与提示注入防线、P0–P4 分期、10 个待拍板问题）。
- **决定**（用户明确）：**再次搁置，不实现**。方案保留为讨论稿；本文件不为其背书任何分期。
- **遗留前置条件**：方案里最重的依赖（早回复 + 撤回、非流式重试）已经就位，
  真要捡起来时 P0 的第一件事是让 `ai.Client` 认识 `tools` / `tool_calls`
  （含流式拼接），并修「`content:''` + `tool_calls` 被误判为空答复」的判定。
- **注意**：勿把本文件里没有的条目当作已定方案；`TOOL-CALLING-PLAN.md` 仍是讨论稿。
- **时间**：2026-xx

## D-024 思维链（reasoning）绝不外发

- **背景**（用户提出）：群里出现过机器人把**思维链本身**当成回答发出来。
- **根因**：`ai.Client.Chat` 在「`content` 为空但有 `reasoning_content`」时，
  把推理文本当成最终答案回填（`FromReasoning`），于是草稿纸被直接发送。
- **决定**：思维链只用于诊断，**永不出现在任何面向用户的内容里**（群消息、私聊、
  回复页脚、会话上下文都算）。改为：
  1. 流式拿到空正文时**一律**再补一次非流式请求（以前只在「正文与推理都为空」时才补，
     恰好漏掉了最该补的「只有推理」这种情况）；
  2. 重试后正文仍为空：有推理 → `ai.ErrReasoningOnlyCompletion`，完全无产出 →
     `ai.ErrEmptyCompletion`；两者都不返回可发送的结果；
  3. 调度器识别 `ErrReasoningOnlyCompletion`，回一句「这个问题我一时没理清，换个说法再问一次？」
     而不是把推理发出去；该轮**不写入会话上下文**，避免污染后续对话。
- **删掉的字段**：`ChatResult.FromReasoning`（它的存在本身就是"可以拿推理当答案"的暗示）。
- **时间**：2026-xx

## D-025 会话时间线记录发言人 OpenID

- **背景**（用户提出）：会话详情的上下文消息只显示昵称；
  群里同名的人、以及多个机器人之间无法区分。
- **决定**：`messages` 增加 `speaker_openid`：
  - 人类消息 / 旁观消息写 `member_openid`；
  - 另一个机器人的发言写其 `member_openid`（回退 `user_openid`）；
  - 助手消息写**机器人在该群的 `member_openid`**（取自 identity 缓存，不额外发请求）。
    私聊下机器人没有 per-user openid，此列为空——这是事实，不编造。
- **旧数据回填**（迁移 `0006`）：只能靠 `records` 审计表按「同会话 + 同内容」反查，
  且**仅当所有匹配记录都指向同一个人**时才回填。三人同说「好的」时留空：
  时间线上一个空单元格远好过一个错的 OpenID。
- **顺带修掉的坑**（实现时才发现的真 bug）：入站 / 出站审计记录的 `member_openid`
  一直写的是 `conv.MemberOpenID`，而 **C2 共享会话根本没有 member_openid** ——
  于是共享模式下 810 条审计记录的「是谁发的」全是空的（早回复那条反而写对了，
  用的是 `in.MemberOpenID`，前后不一致正说明原意就是发送者）。现已统一改为 `in.MemberOpenID`。
  这也是为什么**历史共享会话的消息无法回填 openid**：审计表里本来就没存。
- **前端**：会话详情每条消息在昵称后显示截断 openid（悬停看全文）+ 复制按钮。
  升级前写入的历史消息大多显示为空，属预期。
- **时间**：2026-xx

## D-026 会话详情：跟随最新 + 右下角「回到底部」角标

- **背景**（用户提出）：群消息在实时进，时间线却不跟着走，得手动滚到底部。
- **第一版被否掉了**：曾按字面做成「只要有新消息就滚到底部」，但群很活跃，
  用户往上翻历史时会被反复拽走。用户要求改成下面的规则。
- **最终决定**：
  - 「自动滚动」开关，**默认开启**，状态记在 `localStorage`
    （键 `bot-harness.conversation.autoScroll`，沿用 `useLiveRefresh` 的 `'1'`/`'0'` 约定）；
  - **只有本来就在底部时才跟随**：贴底判定 = 距底部 ≤ 80px；请求发出前的贴底状态
    决定这一轮刷新是否跟随（刷新期间用户可能滚走，用请求前的判断更稳）；
  - 首次打开 / 切换会话：直接贴到最新；
  - 不在底部时：**右下角浮出「回到底部」按钮**，点击回到底部；
  - 不在底部期间进来的新消息在按钮上显示**角标条数**（>99 折叠为 `99+`），
    回到最底部自动清零；只统计尾巴上新增的消息，往前面翻出来的更早消息不计入；
  - 「加载更早」是向上补历史，不触发跟随，但会让「回到底部」按钮如预期出现；
  - 开关关闭 = 完全不跟随（即使贴底也不跟随），此时新消息仍然累计角标。
- **实现要点**：`atBottom` / `pendingNew` 两个 ref；`@scroll.passive` 与每轮刷新、
  每轮「加载更早」之后都直接读 DOM 重算贴底状态（内容变长不一定会触发 scroll 事件）；
  `restoreScroll` 只负责恢复 `scrollTop`，不再自己决定是否跟随。
- **时间**：2026-xx

## D-027 会话详情：左侧会话切换栏

- **背景**（用户提出）：看某个会话时想切到另一个，只能退回会话列表页再点进去。
- **决定**：会话详情左侧新增固定宽度的**切换栏**（`ConversationSidebar.vue`，仅此页使用）：
  - 搜索框（群名 / 成员名 / OpenID，防抖 220ms）+ 场景下拉筛选；
  - 分页加载（每页 30，「加载更多」按钮）；
  - 条目显示：标题（群名，单聊取成员名）、场景徽标、成员/「整群共享上下文」、
    消息条数、最近活跃；已暂停的会话额外标「已暂停」；
  - 点击即 `router.push` 到该会话详情，**高亮**当前打开的那条；
  - 顶部有刷新按钮，`onMounted` 拉取一次。
- **刻意不做的**：清空 / 启停 / 合并同群会话等操作仍留在会话列表页 ——
  侧栏只负责「切换」，不做成第二个管理页。
- **响应式**：`lg`（≥1024px）以上显示，窄窗口隐藏，回退到顶部的「返回列表」按钮。
  `<aside>` 用 `lg:flex`（不是 `lg:block`）：`card-fill` 靠 `flex-1` 撑高，
  父级不是 flex 容器的话侧栏高度会随内容长出去。
- **已知边界**：当前会话若不在第一页里就不会有高亮；侧栏数据不参与详情页的实时刷新
  （它只是切换器，不是实时面板）。
- **时间**：2026-xx

## D-028 会话详情：元数据可折叠 + 发言成员侧栏 + 纯 UI 输入栏

- **背景**（用户提出，一次三件）：① 元数据板块占地方，希望能折叠（默认不折叠）；
  ② 消息区右侧想要一份「群里发言过的人」的聚合，像群成员列表（默认折叠）；
  ③ 消息区加一个消息编辑 / 发送控件，**暂时只要界面，不要实际功能**。
- **元数据折叠**：卡片头部加「折叠 / 展开」按钮，折叠时标题旁显示一行摘要
  （群名 · N 消息 / M 轮次）。状态用普通 `ref`，**不写 localStorage** ——
  用户要的是「默认不折叠」，持久化反而会让它莫名其妙一直收着。
- **发言成员侧栏**（`w-56`，`v-if="membersOpen"`，默认 false）：
  - 数据**纯前端聚合自已加载的消息**（首屏 200 条，向上翻历史继续累加），不新增后端接口；
  - 每人一行：首字头像（按昵称散列取稳定配色）、昵称、类型徽标（本机器人 / 其他机器人）、
    发言条数、最近发言时间、OpenID 与复制按钮；
  - 排序可切换「按发言数 / 按最近」，默认按发言数；
  - 机器人自己（assistant 轮次）和其他机器人（`observed_bot`）也各算一个成员，
    否则「发言过的所有人」会缺一块。
- **踩到的坑（实测线上数据后发现）**：升级前写入的消息没有 `speaker_openid`，
  若直接按 `openid || 昵称` 分桶，**同一个人会被拆成两行**
  （实测「Gallium Nitride」52 条 + 42 条、「NAK」55 + 22）。
  修法：先扫一遍建立**昵称 → openid** 映射，把没有 openid 的老消息并回唯一确定的那个人；
  同名对应多个 openid 时记成「有歧义」并放弃合并 —— 与 D-025 的回填口径一致，
  宁可不合并也不张冠李戴。机器人自己用固定键，不因老消息缺 openid 而裂成两行。
  修正后同一份数据从 9 行收敛到 5 行。
- **输入栏**：`textarea` + 发送按钮（`disabled`），底部一行注明「纯界面预览：
  输入不会发往 QQ，也不会写进上下文」并实时显示字数。**刻意不接任何逻辑**，
  按钮禁用而不是静默无效，免得用户以为发了。
- **布局**：消息卡片内部改成 `[消息列(列表+回到底部浮层+输入栏)] [成员侧栏]`；
  「回到底部」浮层移进列表的相对定位容器，`bottom-4` 因此落在输入栏之上。
- **时间**：2026-xx

## D-029 后台手动发送消息

- **背景**（用户提出）：会话详情页的输入栏要真的能发消息。
- **用户拍板**：始终主动消息；进上下文；时间线/审计单独标记；要全局开关；
  限流做成全局设置的默认值；**不弹确认框、也不做额外提醒**；已暂停 / 不在允许名单的会话也能发；
  支持私聊；**超长自动拆成多条**；主动消息计数要做；
  旧的 `/api/actions/test-send` **保持原样**，作为独立的测试备用入口。
- **接口**：`POST /api/conversations/{id}/messages {"content": "..."}`，鉴权与其它后台接口一致。
- **语义**：
  - **永远是主动消息**：没有 `msg_id` 可依附，所以发送前先查一次群状态，主动消息权限没开就直接
    拒绝（`409 no_proactive_permission`），不留到平台报错；
  - 顺序**先平台、后落库**：绝不出现「后台显示发过了、实际没发出去」；反过来（发出去了但写库失败）
    会让日志大声报错，因为消息确实已经到群里了；
  - 成功后写时间线（`kind=manual`、`role=assistant`）与审计（`status=manual`、`delivery=active`），
    并推 SSE `message` 事件 → 打开着的会话详情页自动刷新；
  - 失败写一条 `status=failed` 的审计（**保留平台原始错误**，便于排查），但**不写时间线**
    （群里没看到，时间线就不该有）。
- **超长处理**：按 `reply_max_chars` 先切好段，段数超过 `manual_send_max_segments` 才拒绝；
  拒绝发生在调用平台之前，且**绝不静默截断** —— 人工发的消息被截掉一截比发不出去更危险。
  （顺带修正：`Sender.Send` 原来对所有消息都套「最多 5 段」，但 5 段上限是**被动回复**的规则，
  主动消息没有这个限制，现在只对 `msg_id != ""` 生效。）
- **限流**：手动发送**占用该群的 QPM 额度**（与自动回复共用），另加两级：全局
  `manual_send_per_minute`（默认 20）与单会话 `manual_send_per_conversation_qpm`（默认 6）。
  平台侧的主动消息频控（未认证 30/分钟、单群 1000/天）查询不到，只能把平台的错误翻译成人话。
- **主动消息计数**：`records` 新增 `delivery` 列（`passive` / `active`），迁移按
  「有 msg_id 且不是 early_reply → passive，否则 active」回填历史数据（实测线上 117 条出站：
  68 passive + 30 early_reply/active + 2 failed，与事实吻合）。会话详情接口返回 `active_today`，
  输入栏显示「今日已发 N 条」。**这一列的意义**：机器人的「提前回复 / 被动超时兜底」与手动发送
  吃的是同一份配额，没有这个数字就无法解释「为什么机器人突然不主动回复了」。
- **私聊**：C2C 会话没有 `member_openid`，`user_openid` 才是身份与发送目标。现在入站时把它同时写进
  `Inbound.MemberOpenID` 与会话行；老数据没有时从会话 ID（`c2c:<user_openid>`）解出来兜底。
- **前端**：Enter 发送 / Shift+Enter 换行（**必须放过输入法组字的回车**，否则打拼音会被提前发出去）；
  发送中禁用；成功后清空草稿，失败**保留草稿**并显示错误；输入栏写明目标与「主动消息」属性；
  时间线里 `manual` 行用青色气泡 + 「后台发送」徽标。
- **时间**：2026-xx

## D-030 引用回复（quoting）

- **背景**（用户提出）：bot 的回复能不能以「引用」的形式挂在触发它的那条消息下。
- **平台事实**（官方文档 + 抓到的真实事件）：`POST /v2/groups/{group_openid}/messages` 支持
  `message_reference`（"引用回复。填写后以引用形式展示，关联上下文"）。它收的不是 `msg_id`
  （`ROBOT1.0_…`），而是平台的**引用索引** `REFIDX_…`：
  - 别人发的消息 → 从消息事件 `message_scene.ext[]` 里的 **`msg_idx`** 取；
  - 机器人自己发的消息 → 从发消息响应的 **`ext_info.ref_idx`** 取。
  实测线上事件里 `ext` 形如 `["ref_msg_idx=…","msg_idx=…","auth_token=…"]`，每条消息都带 `msg_idx`。
- **用户拍板**：@ 触发的要有引用；**即时回复（占位语）不要引用**；主动消息也允许引用；
  后台发送时可以**右键一条消息**选择引用；**按群**单独开关，开关放在**会话详情**里，默认启用。
- **决定**：
  - `Inbound.QuoteIndex` 取 `message_scene.ext[msg_idx]`；被动回复时按群设置决定是否带上；
    即时回复、`replyBusy` / `replyFallback` 这类元消息**始终不引用**；
  - 主动消息路径（被动超窗后的兜底、后台手动发送）**都保留引用** —— `deliver` 的兜底沿用同一份
    `SendOptions`，手动发送则接受 `quote_message_id`；
  - `messages.quote_index` 列（迁移 0008）：入站消息存它的 `msg_idx`，出站消息存响应里的 `ref_idx`
    （出站行先于发送落库，所以发送成功后再 `UPDATE` 补上）。有了它，右键引用任意一条历史消息才有依据；
  - 后台发送接口收的是**消息行 id**，服务端解析成索引，并校验该行属于本会话 —— 客户端无法直接塞索引；
  - `Settings.QuoteReply`（全局，默认 true）+ `GroupOverride.QuoteReply`（按群，可空 = 跟随全局）
    + `POST /api/conversations/{id}/quote-reply`（会话详情里的开关）。清掉覆盖时若该覆盖已无其它设置，
    整条删除，免得策略页留一行空记录。
- **顺手修的隐患**：策略页保存 `group_overrides` 时按字段白名单重建对象，**会把它不认识的
  `quote_reply` 抹掉**。已在补丁映射与覆盖表格里补上这一列（跟随全局 / 引用回复 / 不引用）。
- **未做的**：别人引用一条我们看不到内容的消息再 @ 机器人时，模型仍然不知道"这条"指什么
  （payload 只给 `ref_msg_idx`，不给正文）。
- **时间**：2026-xx

## D-031 模型端点（openai_base_url）可在设置面板调整

- **背景**（用户提出）：允许在设置面板里改 BASEURL。
- **问题**：端点原本只在启动时从 `.env` 的 `OPENAI_BASE_URL` 读一次，`ai.Client` 把它**烧进了结构体**，
  换端点必须改 `.env` 重启。而模型名早就是运行时设置了（D-016 之后配置权威在数据库）。
- **决定**：
  - `Settings.OpenAIBaseURL`（`openai_base_url`）：默认取 `.env` 的值，**留空 = 回退到 `.env`**
    （这样把 .env 改回去仍然有效，不会被数据库里的一份旧拷贝永久压住）；
  - `ai.Client` 增加 `SetBaseURLResolver(fn func() string)`，`Chat` / `ListModels` **每次请求**都解析一次，
    空值回退到构造时的地址，末尾斜杠在解析时收敛（否则会拼出 `//chat/completions`）；
  - `main.go` 把解析器接到设置管理器上；换个端点不需要重启，`GET /api/models` 也会跟着走新端点；
  - 校验：非空值必须是 `http`/`https` 且带 host，否则 `PUT /api/config` 直接 `400`。
    不校验的话要到第一次模型调用才炸，那时错误信息离病根很远；
  - 启动日志新增 `ai endpoint resolved`（含 `base_url` 与 `from_env`），排查「到底打到了哪个服务」时有用。
- **前端**：系统设置 → 模型卡片上方新增「模型端点」输入框，占位提示 .env 的当前值，
  说明留空即回退；**保存后自动重新拉一次模型列表**（旧列表是旧端点拉来的）。
- **顺带**：`GET /api/config` 的只读块里那一项改名为 `openai_base_url_env`，
  避免和可编辑的 `openai_base_url` 混淆（前端原本没用它）。
- **时间**：2026-xx

## D-032 模型 API Key 也可在设置面板调整（且只进不出）

- **背景**：D-031 把端点做成运行时设置后，实测立刻踩到坑 —— 端点改到另一个网关，
  而密钥只能在 `.env` 里改，于是上游直接 `401 API Key 无效`，**机器人的所有模型调用全挂**。
  换服务商必然要换 key，两者必须一起可配。
- **决定**：
  - `Settings.OpenAIAPIKey`（`openai_api_key`），`ai.Client` 增加 `SetAPIKeyResolver`，
    每次请求解析；留空 = 回退到 `.env` 的 `OPENAI_API_KEY`；
  - **密钥只进不出**：`GET /api/config` 与 `PUT /api/config` 的响应里 `openai_api_key` 恒为空串，
    另给 `openai_api_key_set`（bool）与 `openai_api_key_source`（`db`/`env`/`none`）。
    只进不出的秘密不会从截图、浏览器缓存或日志里漏出去；
  - 前端用 `type=password` 输入，占位文案说明「留空 = 保持现状」，保存后清空输入框；
  - 改端点或改 key 之后**自动重拉模型列表**（上一次很可能就是 401，旧列表不可信）。
- **教训**：把「端点」单独做成运行时设置是不够的 —— 端点、密钥、模型名是一组必须一起生效的
  连接参数。以后再加同类设置时，按组考虑而不是按单个字段。
- **时间**：2026-xx

## D-033 模型端点与密钥彻底改为数据库配置（.env 不再参与）

- **背景**（用户提出）：去掉「读 .env 配 BaseURL / Key」的逻辑，全部改为读数据库；
  并且界面上凡是硬编码或读 .env 显示的 baseurl 都要修掉，界面元素必须真的从后端拿数据。
- **问题**：D-031/D-032 只是把 `.env` 变成了「默认值 + 兜底」，于是出现了两处会说谎的显示：
  - `GET /api/models` 与 `GET /api/status` 的 `base_url` 返回的是 **`d.Config.OpenAIBaseURL`（.env 值）**，
    而真正被请求的是数据库里那个 —— 后台显示一个和真实调用地址无关的 URL；
  - `GET /api/config` 的只读块还专门列了一行 `openai_base_url_env`，引导人去 .env 找答案。
- **决定**：
  - `config.Config` **删除** `OpenAIBaseURL` / `OpenAIAPIKey` 两个字段（连 `.env` 读取、必填校验、
    `MissingSecrets` 一起删掉）；`settings.Defaults` 不再从 cfg 取值，默认为**空**；
  - `ai.NewClient("", "")`：端点/密钥只在每次请求时从设置解析。空值不再是「回退到 .env」，
    而是**未配置**：`Chat`/`ListModels` 直接返回 `ai.ErrNotConfigured`，消息里写明去哪填；
  - 启动时若端点或密钥为空，打一条 WARN 指路，而不是等第一条消息失败；
  - `GET /api/status` 的 `ai.base_url`、`GET /api/models` 的 `base_url` 改成 **`d.AI.BaseURL()`（生效值）**，
    并补 `base_url_set` / `api_key_set`；只读块删掉 `openai_base_url_env`；
  - 密钥来源只剩 `db` / `none`；
  - 前端：设置页去掉「.env 端点」提示与所有「留空回退 .env」文案，改成「唯一来源 / 留空 = 未配置」；
    清除密钥按钮的语义从「改回 .env」变成「变成未配置（会失败）」；模型名输入框的 `placeholder`
    不再写死某个部署的模型名；仪表盘与顶栏在端点为空的显示「未配置模型端点」。
- **代价**：全新部署**必须**先登录后台填写端点与密钥，否则模型调用全部失败（有明确报错与启动 WARN）。
  这是刻意的 —— 见过太多「改了 .env 却不生效」的排查。
- **时间**：2026-xx

## D-034 Harness 能力升级：工具 / 执行 / 记忆 / 人味 / 主动（已批复）

- **背景**（用户提出）：现有 harness 只会单轮问答 —— 没工具、没执行环境、没长期记忆、
  活人感不足、不会主动说话。要求方案先行，批复后按 P0–P5 全做。
- **方案**：[HARNESS-UPGRADE-PLAN.md](HARNESS-UPGRADE-PLAN.md)。
- **Q1 隔离模型（关键修订）**：**不另建沙箱**。整个 harness 跑在容器里，
  以容器为隔离边界，工具能力天然局限在容器内。实测现状：uid 10001、CapEff=0、
  seccomp/apparmor、无 docker socket。配套三处硬化：`/app` 改 root:root 只读、
  子进程清洗 env（避免一句 `env` 泄漏 AppSecret）、子进程 rlimits + 超时 + 输出截断。
  **残余风险如实记录**：同 uid 理论上可读 `/data/bot.db`（含 AI Key）；
  彻底解决需严格 rootless dockerd（宿主改造，另行安排）或第二层沙箱（已否决）。
  因此 `run_*` 默认关闭，按群 opt-in 作为风险闸门。
- **Q2 记忆作用域**：跟随上下文 —— 群聊写入 `group` 作用域、只在该群注入；
  单聊写入 `member` 作用域、只在该私聊注入；`global` **仅当管理员（`admin_openids`）
  在 @ 或私聊中明确要求「记住」时**才写，且所有上下文可用。
- **Q3/Q4/Q5**：自发主动默认关、按群 opt-in；人味默认「中等」；
  主动消息**不设每日硬上限**，改为比例预算：每 N 条人类消息换 1 条（默认 10，可调，
  上限 3 点防爆发）。`at_only` 群看不到人类消息 → 额度恒为 0 → 自发主动自然关闭。
- **Q6**：联网工具一期就做（`web_fetch` + `web_search`，搜索后端可插拔）。
- **Q7**：工具总开关默认开；`run_*` 与 `schedule_*` 默认关。
- **Q8**：工具调用过程**只写日志**，不在群里提示（避免扰民）。
- **附带需求**：发送到 QQ 的消息支持 **Markdown**（`msg_type=2`）。
  平台约束：传 markdown 时 `content` 必须为空；需单独申请模板权限
  （`304036`/`40034127`）；因此必须带**自动降级**（失败改发纯文本 + 冷却），
  并且提示词要在开启时放开排版限制。
- **交付节奏**：P0 工具地基 → P0b Markdown → P1 记忆 → P2 执行与联网 →
  P3 人味 → P4 委托式主动 → P5 自发主动；每期独立上线、可单独开关回滚。
- **时间**：2026-xx

## D-035 Harness 升级交付（P0–P5 全部落地）

- **结果**：[HARNESS-UPGRADE-PLAN.md](HARNESS-UPGRADE-PLAN.md) 的七期全部完成并部署验证。
- **能力矩阵（开关 / 默认 / 按群覆盖）**：

| 能力 | 开关 | 默认 | 按群覆盖 | 说明 |
|---|---|---|---|---|
| 工具调用 | `tools_enabled` | **开** | `group_overrides[].tools` | now/calc/remember/recall/web_*/schedule_* |
| Markdown | `markdown_enabled` | **开** | — | 无模板权限时自动降级并停放 24h |
| 记忆 | `memory_enabled` | **开** | `.memory` | 作用域跟随上下文；global 仅管理员 |
| 记忆自动抽取 | `memory_extract_enabled` | **关** | — | 每 N 轮一次便宜调用 |
| 联网 | `web_enabled` | **开** | `.web` | web_fetch 可用；web_search 需配 SearxNG |
| 容器内执行 | `exec_enabled` | **关** | `.exec` | 同 uid 的残余风险已记录，按群 opt-in |
| 人味 | `humanize_level` | **medium** | `.humanize` | off/soft/medium/strong |
| 定时提醒 | `tasks_enabled` | **开** | `.tasks` | 唯一可在无人时发消息的委托能力 |
| 自发主动 | `proactive_level` | **off** | `.proactive` | 比例额度 + 五道保险丝 |

- **回滚**：任一能力关掉即可恢复上一形态；数据库表只增不改，关掉的功能不删数据。
- **实测结论**（真机）：网关支持函数调用（`finish_reason=tool_calls`）；容器内 `ulimit` 四道限制生效；
  SSRF 防线拦住 `127.0.0.1` / 内网 / 元数据地址。
- **已知边界**：`run_shell` 与 bot 同 uid（可读 `/data/bot.db`），彻底隔离需严格 rootless dockerd
  或第二层沙箱（已否决），因此 exec 默认关闭；`web_search` 未配置后端时明确报错。
- **时间**：2026-xx

## D-036 配置全部入库；设置页拆成二级子页面

- **背景**（用户要求）：降低对 `.env`/环境变量的依赖，所有配置都能在运行时于后台设置页修改；
  同时把设置页拆成多级子页面。
- **决定**：
  1. **只有 `DB_PATH` 与 `ENV_FILE` 留在环境变量里**（前者是鸡生蛋，后者是加载哪个 dotenv 文件）。
     其余 80+ 项配置全部进 `internal/settings`（数据库），包括原先只能写 `.env` 的
     QQ 应用凭证与传输、后台口令、监听地址、TLS、日志级别与缓冲、媒体上限、提示词文件路径。
  2. `internal/config` 收缩到 51 行（Version / DBPath / EnvFile）；`Defaults()` 不再依赖 config，
     内置默认值写死在 settings 里。
  3. **升级平滑**：`Manager.SeedFromEnv` 在启动时把旧 `.env` 值一次性导入数据库，
     只导入「数据库还没有记录的键」，已有覆盖永远优先，重复启动是空操作。
  4. **秘密只进不出**：`openai_api_key`、`qq_app_secret`、`admin_token` 永不回显，
     接口只返回 `*_set` 标志；空值表示「保持现状」，清空需显式调用对应动作。
  5. 后台口令改为**持久化**（首次为空时生成并写库），且每次请求从设置读取 —— 改口令立刻踢掉旧会话。
  6. 设置页拆成 6 个二级子页面（嵌套路由）：模型与提示词 / 回复与人味 / 能力 / 主动消息 /
     接入与运维 / 系统与诊断，共用一份 draft 与底部常驻保存栏，仍然只提交被改动的字段。
- **代价 / 已知边界**：
  - `http_addr`、`tls_*`、`log_buffer_size` 改完需要重启进程（保存后页面会提示）；
  - 首次启动会把 `.env` 里的值写进数据库，之后 `.env` 即便保留也不再被读取（除 DB_PATH）；
  - 配置进了 SQLite 文件，备份/迁移数据库即备份了配置（`.env` 不再是配置载体）。
- **时间**：2026-11-01

## D-037 工程化与验证：把「记得要跑」交给机器

- **背景**：这一轮不加功能，由智能体团队并行做**验证与工程化**。项目已有 80+ 配置项、
  39 个后台端点、7 期 harness 能力，但「文档说的事」与「代码做的事」开始分叉：
  README 仍在教写 `ALLOW_DEGRADED` / `QQ_FULL_GROUP_MODE`，`.env` 里 40+ 个旧键早已不生效；
  `web/dist` 靠人记得重新构建才不漂移；后台契约只写在文档里、没有测试钉住；
  「提交前要跑什么」全靠人记。
- **决定**（已全部落地，细节见 ADMIN-API v1.1 与各包测试）：
  1. **能力诚实化**（T8）：配置键集合由代码导出并与后台表单对齐 ——
     `internal/settings/testdata/config-keys.json` 钉住 **81 个设置键 + 15 个按群覆盖字段**
     （golden），`scripts/check-config-keys.sh` 在提交前/CI 校验；补齐按群覆盖的漏字段与枚举校验；
     README / AGENTS / ADMIN-API 删掉不存在的配置教学，明确 `.env` 只剩 `DB_PATH` / `ENV_FILE`。
  2. **启动诊断与口令子命令**（T1）：启动日志把「已死键 / 只导入一次的键 / `.env` 口令与数据库不一致」
     分开说明；新增 `bot-harness token` 从数据库读当前口令 —— 不要再 grep `.env`。
  3. **后台契约测试**（T2/T12）：39 个端点的字段、错误码、鉴权与分页边界由
     `internal/admin/admin_*_test.go` 钉住，包覆盖 **0.0% → 91.1%**（改造前 admin 包没有任何测试）；
     `internal/settings` 覆盖 **68.6%**；e2e 新增主动消息与记忆抽取链路
     （`internal/e2e/proactive_memory_test.go`，16 个测试函数）与工具循环用例；
     `internal/store` 的迁移测试覆盖 **0010（`messages.addressed`）** 的升级路径。
  4. **后台安全与契约修正**（T9/T15）：三秘密在 GET/PUT/reset 一律经 `maskSecrets` 只进不出；
     **重置只回退可调配置、保留三个凭据**，口令此前为空才生成并写 warning，同时关掉了
     「reset 后空口令可认证」的 P0；SSE 的 `ws` 冗余特判删除，但 **`ws` 事件照常发送**；
     `page/page_size` 由 handler 夹取并回显**实际生效值**。
  5. **前端质量门**（T3/T10）：vitest **10 个测试文件 / 139 个用例**，覆盖路由与侧栏一致性、
     config draft 语义、API 信封解析、按群覆盖白名单完整性、页面冒烟；`web/dist` 与源码一致提交。
  6. **CI 与脚本**（T4/T8）：CI 三个 job —— go（`go vet` + `go test ./...` 含 e2e +
     core/tools/settings/store/admin 的 `-race`）、web（`pnpm test` + 构建产物一致性）、
     hygiene（隐私扫描）；`scripts/check-dist.sh` 按文件 sha256 比对产物（对新增/删除敏感）、
     `scripts/privacy-scan.sh` 拦内网 IP / 个人绝对路径 / 常见密钥形态
     （测试里的 RFC1918 与夹具密钥放行）、`scripts/check-config-keys.sh` 校验键集合 golden。
  7. **文档清账**（T6）：ADMIN-API 升到 v1.1（按实际 handler 重写 22 处漂移 + 本轮定稿的
     脱敏/分页/SSE 三项 + 重置与鉴权语义）、README / AGENTS 与本轮实现同步；
     本记录。
  8. **发布流程**：回滚点、健康验证、风险面收敛（端口暴露面、`.env` 残留、代理）见 README 与 `.github/workflows/release.yml`。
- **为什么要有交叉审计（案例）**：本轮对 39 个后台端点做独立审计时发现，
  `POST /api/actions/reset-config` 会把 `admin_token` 一起清空 —— 而 `tokenEqual` 当时用
  `hmac.Equal("", "")` 比较，于是**空口令 / 空 Bearer 能直接通过鉴权**，直到进程重启才恢复。
  这条路径只有「重置 + 构造空口令请求」组合才触发，单看任何一个 handler 都像是对的；
  审计 → 当天立项（T15）→ 修复（重置保留凭据 + 空口令/空白口令/空 Bearer 一律 401 +
  `deriveSession` 在口令为空时返回空串并拒绝 cookie 分支）→ 测试钉住。
  同一轮交叉审计还发现「轮换 `admin_token` 后 login 仍下发启动时的陈旧 session cookie，
  导致改完口令登录循环、要重启才恢复」的 P1，已修复（T18）：登录 cookie 改用实时的
  `deriveSession()` 计算，不再缓存启动时的会话 HMAC。这就是「契约与权限需要
  交叉审计」的实证：能力越密，错误越藏在两个看起来都对的模块交界处。
- **理由**：能力和配置已经多到「靠人记」必然漂移。把每条规则换成一个可执行检查点 ——
  契约用测试钉、产物用脚本比、隐私用扫描拦、文档用代码核对；人只负责判断，不负责记忆。
- **代价**：CI 每次多花数分钟（go 全量含 e2e 约 1 分钟、`-race` 子集约 30 秒、前端约 2 秒）；
  契约测试与 ADMIN-API 必须随接口同步维护，否则 CI 会红 —— 这正是想要的效果；
  提交仍要人执行（GPG 无法非交互签名）。
- **未做项 / 待回填**：
  1. **T14 备份脚本（已完成）**：`scripts/backup-db.sh` / `restore-db.sh` 已交付 ——
     快照带 UTC 时间戳、`MANIFEST.txt`、`SHA256SUMS`、`OWNER`，默认保留 7 轮；
     镜像里有 `sqlite3` 时走在线备份，否则 `bot.db + -wal + -shm` 热拷贝（强一致需先停容器）；
     恢复先校验 sha256 与 SQLite magic，目标卷被运行中容器占用时默认拒绝（`--force` 才继续）。
     演练结论（临时卷）：热拷贝 / 校验 / 拒绝覆盖运行中卷 / 轮转 / 篡改检测全部通过；
     **在线备份分支未实跑**（镜像内无 `sqlite3`）。README §9 已换成实测用法。
  2. **受控上线**：发布、切换容器、健康验证与回滚演练的步骤见 README
     「快速开始」与「运维提示」，不要跳过回滚点。
  3. **T16 额度口径（已完成）**：额度只由「未被 @ 的旁观人类消息」产生
     （`role=user AND addressed=0 AND kind IN ('dialogue','observed')`；迁移 0010 加 `addressed` 列，
     存量默认 `1`、`observed` 回填 `0`）。`at_only` 且平台只推 @ 事件的群额度恒为 0 ——
     D-034 的语义现在由代码保证，e2e 用例钉住：`TestProactiveAtOnlyGroupsEarnNoCreditsFromMentions`、
     `TestProactiveAllMessagesChatterEarnsCredits`、`TestProactiveManualSendsDoNotEarnCredits`、
     `TestProactiveSilentStreakCountsUnansweredMessages`。`silent_streak` 只是计数器，
     真正的熔断是额度门。
  4. **能力默认值的风险面**：`run_shell`（exec）默认关闭但仍与 bot 同 uid，默认值待 ops 收敛
     （由部署方决定）；`web_search` 当时仍未配 SearxNG 后端，只有 `web_fetch` 可用
     （D-038 已接入内置 SearxNG sidecar）。
  5. 严格 rootless 容器隔离不在当前版本范围；`web/dist` 仍选择「提交产物」而不是 CI 构建分发。
- **时间**：2026-11-01

## D-038 内置搜索、环境最小化、品牌统一与 exec 策略固化

- **背景**：此前阶段已把「文档 / 契约 / 产物 / 隐私」收进了自动化，但联网能力仍只有 `web_fetch`、
  `.env` 里还留着大量已失效的旧键、UI 品牌与仓库名不一致、`run_shell` 的「开关 + 身份」双控
  在管理员名单为空时只靠调用方自觉。本阶段把这些收口，并把「数据库换代」排进后续工作。
- **决定**：
  1. **内置搜索（SearxNG sidecar）**：`docker-compose.yml` 新增 `searxng` 服务
     （镜像 `searxng/searxng:2026.11.1-9ded3bec1`、容器 `bot-harness-searxng`、**不发布宿主端口**、
     只走 compose 默认网络并以服务名 `searxng:8080` 访问）；配置片段提交在 `deploy/searxng/`
     （`settings.yml`，`search.formats` 含 `json`；`secret_key` 由 `SEARXNG_SECRET` 环境变量注入，
     不落仓库）。`web_search` 后端设为 `searxng`（`web_search_provider` / `web_search_base_url`），
     每次调用带可选 `limit`（1–10，默认 5）；对 403（未启用 json）、429（限流）、非 JSON 响应
     与 SearxNG 的 `unresponsive_engines` 都有明确诊断文案。
  2. **`.env` 最小化**：只保留**引导键与部署键**（数据库位置、代理、端口绑定、版本号、
     searxng secret 等；完整清单以 `.env.example` 为准）。程序侧仍只读 `DATABASE_URL` /
     `DB_PATH`（与可选的 `ENV_FILE`）；代理、端口绑定、版本号与 searxng secret 分别给 compose / 构建 / sidecar 用，
     **不再承载任何业务配置**。旧值已在首次启动导入数据库；升级前的备份由部署方自行保管，不进仓库。
  3. **品牌统一**：产品名统一 **Bot Harness**（后台标题 / 登录页 / `index.html` / `package.json` 描述），
     二进制与容器用户仍为 `bot-harness` / `botharness`（兼容性与部署脚本不变）。
  4. **exec 策略固化**：`run_shell` 的可用条件写成可测试的多重门 —— 全局 `exec_enabled`（默认关）、
     按群覆盖 `.exec`、调用者在 `admin_openids`、名单非空（新增 `ErrNoAdminsConfigured`）、
     且非管理员连工具定义都不下发。空名单不再只是「拒一次」，而是显式错误。
     建议部署保持 `exec_enabled=false`、`admin_openids` 非空。
- **理由**：搜索是 harness 最常被要求的能力，内置 sidecar 能避免把外部搜索服务暴露到宿主；
  `.env` 只剩「引导 + 运维」键，减少「改了 .env 却不生效」的排查面；
  品牌与双控条件是安全与可维护性问题，越早固化越不容易在后续迭代里被顺手放开。
- **代价**：searxng 是第二个容器（约 369 MiB 镜像、无 HEALTHCHECK、冷启动首查可能 0 结果），
  需要把 `searxng` 加进 `NO_PROXY` 并接受上游引擎抖动；数据库仍是单文件 SQLite。
- **下一轮规划（已确认）**：**迁移 PostgreSQL**。用户决定本轮**先不做** SQLite 备份体系的长期投入
  （`scripts/backup-db.sh` / `restore-db.sh` 只是 SQLite 阶段的临时方案，迁移后重做或废弃）；
  PG 迁移要重做：连接与迁移框架、`store` 的 SQL 方言、统计聚合、备份 / 恢复与运维文档。
- **未做项**：SQLite→PG 迁移本身；searxng 缓存持久化（`/var/cache/searxng` 随容器重建而冷启动）；
  `SEARXNG_SECRET` 目前也经 `env_file` 进入 bot-harness（同信任域，未拆独立 env 文件）。
- **时间**：2026-11-01

## D-039 PostgreSQL 为主、SQLite 为回退（D-009 的落地）

- **背景**：D-009 当时把「单实例、单文件」当作充分条件，并把「将来要多实例 / 要外部存储」记为影响项；
  D-038 又把备份体系的长期投入**明确推迟**到迁库之后（`backup-db.sh` / `restore-db.sh` 只是 SQLite 阶段的临时方案）。
  这一轮把那条路走完：**存储换代，但保留可回退的旧路径**。三条硬要求是——数据零丢失、
  切换和回滚都是一步操作、不因为迁库让机器人停机很久。

- **目标**
  1. 运行时能直接跑在 PostgreSQL 上（不再是"导出 SQL 再手工灌"）；
  2. 线上 SQLite 的全量数据能**一次性搬完并被证明没丢**；
  3. 任何一步出问题都能**一步回退**到 SQLite，且回退时数据与切换那一刻完全一致；
  4. 备份 / 恢复换成 PostgreSQL 的原生工具（`pg_dump` / `pg_restore`），不再依赖 SQLite 文件快照。

- **方案**
  1. **双后端，由引导值选择**：`DATABASE_URL`（`postgres://` / `postgresql://` 前缀）非空走 PostgreSQL，
     留空走 `DB_PATH` 的 SQLite。判定只做前缀检查（`internal/config.DataSource()` 与
     `internal/store.isPostgresDSN` 是同一条契约），所以**切回 SQLite = 清空一个环境变量**。
  2. **方言层只做两件事**（`internal/store/dialect.go`）：占位符 `?` → `$n`（重写时跳过字符串字面量、
     双引号标识符与 `--` 行注释，问号不会误判），以及列自省（SQLite 的 `pragma_table_info(表名)`
     对 PostgreSQL 的 `information_schema.columns`）。其余 SQL 两引擎通用（`ON CONFLICT DO NOTHING`、
     `INSERT … RETURNING id`、UPSERT `DO UPDATE`），没有第三套 SQL、没有 ORM。
  3. **PostgreSQL schema 手工持有**（`internal/store/pg.go` 的 `pgSchema`，12 张表）：
     无外键（SQLite 也没有；`records` 有 `conversation_id` 孤儿必须原样保留）；
     `INTEGER PRIMARY KEY AUTOINCREMENT` → `BIGINT GENERATED BY DEFAULT AS IDENTITY`（显式 id 可插、可 `setval`）；
     布尔仍是 `INTEGER`；~~时间仍是 `TEXT`~~（**已被 D-040 结束**：现在是 `TIMESTAMPTZ`）；
     `BLOB` → `BYTEA`；`REAL` → `DOUBLE PRECISION`。
     **10 条既有迁移作为基线写入 `schema_migrations`、不重放**（它们的 `ALTER TABLE` 靠 PRAGMA 探列，
     在 PG 上跑不通；回填 UPDATE 对空库也没意义）。
  4. **一次性搬运器**（`internal/pgmigrate` + `bot-harness migrate`）：源库 `mode=ro` 只读打开、
     动态列发现（不硬编码列）、逐表一个事务、media 16 行/批（唯一昂贵的表压尾）、
     五个 IDENTITY 表用一条 `setval(pg_get_serial_sequence(...), GREATEST(COALESCE(max(pk),0), 源 sqlite_sequence), true)`
     续接、搬完**立刻自校验**。
     详见 [PG-MIGRATION.md](PG-MIGRATION.md)。
  5. **运维侧**：compose 增加 `postgres:17-alpine`（无宿主端口、`pgdata` 卷、`pg_isready` 健康检查、
     口令来自 `POSTGRES_PASSWORD`）；`scripts/backup-pg.sh` / `restore-pg.sh` 用 `pg_dump -Fc`，
     带 sha256 清单、拒绝在机器人运行时覆盖、`--force` 才继续；`backup-db.sh` / `restore-db.sh`
     标记为 **deprecated**（只在回退到 SQLite 时还有意义）。

- **为什么保留双后端而不是"直接换掉"**
  - **回滚成本**：线上只有一份数据。迁移期最坏的情况是"PG 跑不起来但机器人必须活着"，
    清空 `DATABASE_URL` 重启即可回到 SQLite —— 这个动作不需要镜像、不需要 SQL、不需要停机窗口。
  - **单机开发与测试**：跑单元测试不需要一个 PostgreSQL 实例；`go test ./...` 在纯 SQLite 下依旧全绿。
  - **代价被限制住了**：方言层只有两个函数，schema 有两份（见下面"代价"），这是刻意换来
    回退路径的确定性。若将来确认不再回退，删掉 SQLite 分支比反过来补回退路径便宜得多。

- **零丢失是怎么证明的**（不是"跑通了一次"）
  - 逐表**行数**相等；
  - 逐表**全列规范化有序 sha256**：两侧按主键排序流式扫描，每行按 `len:tag:value` 编码
    （`N`/`I`/`F`/`S`/`X`，长度是字节数，整值浮点降级为 `I`，BLOB 与同内容 TEXT 不碰撞）
    后取 sha256，再把摘要串进聚合 sha256 —— 聚合哈希相等 ⇔ 行集合与顺序完全一致；
  - `media`：每行 `sha256(data)` 必须同时等于 `id` 与 `sha256` 列，另比对 per-row 摘要的聚合值；
  - `settings`：逐键做 **JSON 语义等值**比较（键序、`1` 与 `1.0` 视为相等），并报出只在单侧存在的键；
  - 五个 IDENTITY 表的**下一个 id** 必须等于 `max(pk)+1`（`memories` 为空表时按源计数器得 `2`）。
  - 不一致时打印**首条差异**：定位到第一个摘要不同的行，按主键回查两侧整行，报出第一个不同的列。
  - 演练实测（2026-11-01，快照副本 → 临时库）：**12/12 表全 OK**，media 144 行 `sha256(data)==id` 全通过、
    摘要聚合一致，settings 31 键 JSON 全等，序列 `messages=13344 records=12489 tool_calls=42
    scheduled_tasks=10 memories=2`；独立用 SQL 复核 0 NULL、111 行 `records` 孤儿仍在、外键 0 个。
    12 张表 1.1 s（media 0.82 s）。**上面是演练数据（快照副本 → 临时库）。**

  **正式切换已执行（2026-11-02，停写窗口约 4 分钟）**：12/12 表校验通过、行数 15,901 全部一致、
  序列 `messages=13344 records=12489 tool_calls=42 scheduled_tasks=10 memories=2`；
  切换后 `/api/status` 返回 `driver=postgres`、`path=""`、`size_bytes`≈64 MB，
  `/api/config.readonly` = `{driver, storage, version}` 且口令显示为 `redacted`；
  切换后既有人工后台消息、也有真实群消息落库（`messages` / `records` / `media` / `stats` 全部在 PG 上增长）。
  回滚镜像与 SQLite 卷 `botdata` 均保留，**未触发回滚**。

- **代价 / 已知边界**（写清楚，免得下次当成 bug）
  1. ~~**时间仍然是 `TEXT`**（RFC3339，25 字符，`+08:00`），不迁 `timestamptz`。好处是搬运零改写、
     Go 侧读写代码一行不用动；代价是 PG 里的时间列**不能直接用 SQL 做时区运算 / 区间索引**，
     排序仍按字符串（对同格式 RFC3339 恰好正确，但换时区就未必）。~~
     **已被 D-040 结束（2026-11-02）**：18 个时间列已改为原生 `TIMESTAMPTZ`，Go 侧仍以
     RFC3339 字符串交互。上面这条代价只在「SQLite→PG 那一次搬运」的语境里还成立（搬运仍然
     零改写，因为源是文本、目标列接受字符串参数）。
  2. **没有外键**。`records.conversation_id` 的 111 行孤儿要保留，加 FK 会直接让搬运失败；
     代价是引用完整性只能靠应用层（与 SQLite 时期完全一致，不是新引入的风险）。
  3. **双方言维护**：改一处 schema 要同时改 `internal/store/store.go` 的 `schema`（+ 一条迁移）
     与 `internal/store/pg.go` 的 `pgSchema`，以及 `internal/pgmigrate/ddl.go` 的类型映射；
     漏改不会报错，只会在另一个后端上表现为"列不存在 / 默认值不对"。这是双后端最真实的成本。
  4. ~~PG schema 里 UNIQUE 约束名与 `pgmigrate` 期待的名字不同~~（**这也是 D-040 时同类问题再次出现的
     原因：两份 schema 要手工对齐，时间列的清单同样如此**） **已修复（2026-11-02）**：
     store 原本写显式名 `uq_memories_scope_group_openid_member_openid_text`，`pgmigrate` 按
     PostgreSQL 命名规则期待 `memories_scope_group_openid_member_openid_text_key`，在 store
     建过的库上重跑迁移器会**额外加一条语义重复的唯一索引**。现改为两边同名：`pgSchema`
     直接写 PostgreSQL 生成的名字，另外每次打开 PG 跑一次幂等的 `normalizeMemoriesUnique`
     （旧名库 `RENAME CONSTRAINT`；两个名字都在且 `conkey` 相同则 `DROP` 冗余项；
     `conkey` 不同则不动）。回滚 = 两个常量对调后执行一次反向改名。详见 PG-MIGRATION §5。
  5. **`pg_dump` 备份没有加密**，快照目录里是明文 dump；只放在仓库外的受控目录，别随手同步到网盘。
  6. **PG 没有宿主端口**，所有排查必须 `docker exec`；好处是暴露面更小，代价是宿主上没有 `psql` 可用。
  7. **`POSTGRES_PASSWORD` 对已初始化的卷改不动**（initdb 写死），换口令要走 `ALTER USER` 或重建卷。

- **未做项**
  - ~~**`timestamptz`**：要把时间列改成真时区类型，需要一次带时区语义的数据迁移 + 全部读写代码复核；
    收益是 SQL 侧能做时间窗口聚合，风险是 `+08:00` 硬编码会暴露出来。~~
    **已完成（2026-11-02）**，见 **D-040**。
  - **增量同步 / 双写**：现在是一次性全量搬运，切换必须在一个停写窗口内完成（本次窗口约 4 分钟，
    这期间的群消息只存在于平台侧）。若将来停机窗口不可接受，需要逻辑复制或双写 + 对账。
  - **备份加密**：`backup-pg.sh` 产出的是明文 `-Fc` dump，没有 `gpg` / `age` 封装。
  - **store 侧 `pgSchema` 与 `pgmigrate` DDL 的单一来源化**（两个 UNIQUE 约束名已于 2026-11-02 统一，见代价 4）。
  - **定时备份任务**：`backup-pg.sh` 已可用，但还没有 cron / systemd timer，也没有轮转策略
    （切换后 `~/bot-harness-backups` 里混放 pg dump 与 sqlite-final / env 备份）。
  - **旧 SQLite 卷的处置**：`bot-harness_botdata` 与 `sqlite-final-*.tgz` 保留作回滚依托，
    观察一段时间后是否归档/删除未定。

- **时间**：2026-11-01

## D-040 时间列改为 timestamptz（D-039 代价 1 的收尾）

- **背景**：D-039 把存储换成 PostgreSQL 时，时间列**原样保留成 `TEXT`**（RFC3339，25 字符，`+08:00`），
  换的是「搬运零改写、Go 侧一行不用动」，代价写得很清楚：PG 里的时间列**不能直接用 SQL 做时区运算
  或区间索引**，排序按字符串（同格式下恰好正确，换时区就未必）。D-039 的「未做项」里也把
  `timestamptz` 记为下一笔债。这条决策就是把那笔债还掉 —— 在同一天（2026-11-02）里做完并上线。

- **动机**
  1. **语义**：`TEXT` 里存的是「碰巧长得像时刻的字符串」，任何 `now() - created_at`、窗口聚合、
     `BETWEEN` 都得先 `::timestamptz` 强转，且**转错了不会报错**（只会算错）；
  2. **索引**：字符串列按字节序排，跨时区/跨格式就失真；换成 `timestamptz` 后排序与索引都按真正的时间线走；
  3. **可验证**：迁移脚本与校验器能证明「时刻一个不差」（`extract(epoch …)` 逐行对照），
     而「字符串没变」这种性质本来就是类型系统该保证的事。

- **方案（三层，互不侵入）**
  1. **Schema**：18 个时间列改成 `TIMESTAMPTZ`。13 个非空列是 `TIMESTAMPTZ NOT NULL`；
     5 个「`''` 表示未设置」的列（`proactive_state.last_sent_at`、`proactive_state.updated_at`、
     `scheduled_tasks.lease_until`、`scheduled_tasks.sent_at`、`memories.last_used_at`）
     改成**可空、无默认值**的 `TIMESTAMPTZ` —— 因为空串不是合法的 `timestamptz` 字面量。
     `internal/store/pg.go` 的 `pgSchema` 与 `internal/pgmigrate/ddl.go` 的
     `timestampColumns` / `nullableTimestampColumns` 是**两份必须一致的列清单**
     （与 D-039 代价 3 是同一笔账：schema 有两份，改一处要改两处）。
  2. **读**：`internal/store/pg.go` 用 `stdlib.OptionAfterConnect(overrideTimestamptzScanLocation)`
     给每条连接注册 `pgtype.TimestamptzCodec{ScanLocation: time.FixedZone("CST", 8*3600)}`。
     pgx 默认把 `timestamptz` 解成 UTC，`database/sql` 再 `RFC3339Nano` 出来就是 `…Z`；
     加上这个 codec 之后 Go 拿到的是 `2026-11-02T02:31:05+08:00`，**API 输出与迁移前逐字节一致**。
     可空列走 `nullableTimeText`（`store.go`）：`NULL` → `""`，原生时间按 `scanLocation` 格式化。
  3. **写 / 比较**：字符串参数可直接绑进 `timestamptz`（PG 按列类型推断并解析 RFC3339）。
     可空列写 `NULLIF(?, '')` + `::timestamptz`（`dialect.NullableTimeArg()`），SQLite 分支仍是 `?`；
     「未设置」的判定是 `TimeIsUnset()`：PG `IS NULL`、SQLite `= ''`；字面量是
     `EmptyTimeLiteral()`：PG `NULL`、SQLite `''`。**SQL 仍然只有一份**，差异全在这三个 helper 里。

- **为什么读侧用 codec 而不是 SQL 投影**
  `to_char(col AT TIME ZONE 'Asia/Shanghai', …) || '+08:00'` 也能把值渲染成 RFC3339，但那样
  **每条读时间的 SQL 都得写方言分支**（SQLite 没有这些函数），等于把双后端的代价从 3 个 helper
  扩散到所有查询。codec 只在连接建立时装一次，SQL 完全不动。代价是：**任何绕过 `openPostgres`
  另开的 pgx 连接（例如 `pgmigrate` 的校验器）都没有这个 codec** —— 所以校验器**必须**自己用
  SQL 投影（`targetTimestampProjection`），它也确实那么做了。

- **停机窗口执行**（`scripts/pg-timestamptz.sql` + `scripts/pg-timestamptz-verify.sql`）
  类型不能原地回退，且**旧二进制 + 新 schema 会写失败**（旧代码按 text 绑定 `timestamptz` 列），
  所以只能：**停服 → 整库备份 → 迁移 + 校验（同一 psql 会话）→ 重建镜像启动**。
  实测 2026-11-02：**停机窗口 98 秒**（容器退出 → health 200；从发 `stop` 算 114 秒），
  其中「迁移 + 校验」同一会话合计 **1.709 s**；99 项校验 0 失败；18/18 列转换；
  27 个索引全部 `indisvalid`；幂等（重跑 18 条跳过 NOTICE、数据与 schema 字节不变）。
  回滚 = 恢复整库备份 + 回退镜像 `bot-harness:pre-timestamptz-20261102`（`802bc0e6`）。
  细节与坑见 [PG-MIGRATION.md §12](PG-MIGRATION.md)。

- **代价 / 已知边界**
  1. **`+08:00` 假设被固化了**（比 D-039 时更硬）：读侧 codec 固定 `Asia/Shanghai`，
     校验器与 `pgmigrate` 的投影也固定 `Asia/Shanghai` + `'+08:00'` 后缀。
     这依赖「上海自 1991 年起无 DST、恒为 +08:00」这一事实（用 `FixedZone` 而非 `LoadLocation`，
     就是为了不依赖 tzdata）。要支持多时区显示，得把 `scanLocation` 变成可配置项。
  2. **可空列语义变了**：旧 DDL 是 `NOT NULL DEFAULT ''`，新的是可空无默认值。漏改的 `''` 写入会
     **立即报错**（`invalid input syntax for type timestamp with time zone`）—— 这是刻意的：
     静默写进一个非法值比报错更糟。**Go 侧的对外表示仍是 `""`**，所以 API 契约不变。
  3. **校验器要归一**：`internal/pgmigrate` 的校验器不能直接对 `timestamptz` 列取列值 ——
     `time.Time` 没有规范编码，且 PG 的 `NULL` 不等于 SQLite 的 `''`。所以
     `targetSelectList` 按**目标列的真实类型**决定是否套
     `to_char(col AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD"T"HH24:MI:SS') || '+08:00'`
     （可空列再 `COALESCE(…, '')`），把两个引擎的值拉回同一根字符串再哈希。
     投影**按列类型条件化**，这样「比 timestamptz 更老的库」仍然按 `TEXT` 校验，而不是报函数不存在。
  4. **双方言 helper 变成 3 个**：`NullableTimeArg` / `TimeIsUnset` / `EmptyTimeLiteral`。
     新写一条涉及可空时间的 SQL 时**必须**用它们，而不是手写 `?` 或 `''`，否则会在 PG 上报类型
     错误、或在 SQLite 上把「未设置」写成 `NULL` 而读不出来。这是 D-039「代价 3：双方言维护」
     的具体化，没有变轻。
  5. **`ALTER TYPE` 是全表重写**：没有 Online 路径，必须进停机窗口；`media` 因为 44 MB TOAST
     一并重写，是单表最慢的（0.73 s）。
  6. **备份/恢复仍是明文 `-Fc` dump**（D-039 代价 5 未变），而回滚完全依赖它 —— 窗口越长，
     「备份之后、停服之前」丢失的行越多。

- **未做**
  - **把时间统一转 UTC**：现在库里存的是**时刻**（`timestamptz` 本身不携带显示时区），但**读取与显示
    固定 `+08:00`**。没有做成「库里 UTC、按用户时区渲染」，因为本项目只有一个部署、一个时区，
    多一层时区配置只增加出错面。要全球化时必须重做 `scanLocation` 与 API 的时间格式。
  - **`timestamptz` 的历史数据审计**：迁移只证明「时刻一个不差」（`instant` 项 99/99），
    没有审计**源数据的时刻本身是否合理**（有没有 1970 年、未来年份、跨夏令时之类的历史行）。
    线上 18 列 100% 是 `+08:00` 的 25 字符文本，所以这次风险实际为 0 —— 但**这是运气，不是保证**。
  - **Scheduled task / 主动消息的时间边界没有重新推导**：行为与迁移前一致（读回来还是同一根字符串），
    而「用 SQL 直接算时间窗口」这条新能力目前**还没有任何调用方** —— 收益尚未兑现。

- **时间**：2026-11-02

## D-041 两阶段生成：主模型出草稿，无上下文润色器改口吻

- **背景**：人设只写在系统提示词里时，模型「知道」自己是某个角色，却经常用通用助手的
  腔调说话 —— 提示词越靠前、上下文越长，风格约束越容易被事实性内容稀释。
  直接加强提示词会连带影响事实准确性（角色扮演过头、编造剧情）。
  D-034/D-035 把人味拆成了措辞与停顿两层（`humanize_*`），但**措辞本身**仍是一次生成，
  没有「先答对、再说得像」的分离。

- **动机**
  1. **分离两个目标**：答对（草稿）与说得像（润色）对模型的要求不同，一次生成只能二选一折中；
  2. **一次润色的上下文越小越安全**：只给「人设/格式规则 + 草稿」，润色器**没有材料**编造新事实；
  3. **绝不能因此丢消息**：润色是**增值步骤**，失败必须回到草稿 —— 这是整条管线最硬的不变量。

- **方案**
  1. **两段式**：`processMessage` 拿到 `converse` 的成功结果后，把 `result.Text` 当**草稿**，
     调 `newPolish(...)` 得到**发送文本**；写库的 assistant 消息、`extractMemories` 的 reply、
     实际发送内容**全部用润色稿**（上下文里必须是用户真正看到的版本）。
  2. **入参只有两样**：草稿 + `d.promptFor(s,cfg)` 解析后的系统提示词（已含人设、人味、记忆块）。
     系统提示词取**工具循环最后一步**实际使用的那份（`outcome.systemPrompt`），
     避免工具步中途追加的记忆块被漏掉。**没有历史消息、没有用户原文、没有工具结果。**
     - **为什么刻意不给上下文**：润色的任务只有「换个说法」，给它历史/用户原文/工具结果
       只会让它把这些内容当成改写素材 —— 于是它开始"补全"细节、把工具输出里的数字写进正文、
       甚至改掉结论。上下文越小，它能编造的空间就越小，**"只改措辞不改事实"才有机会成立**。
       这也是一条隐私边界：润色模型（可能是另一个更便宜的模型/另一个上游）**看不到会话内容**。
     - 代价：它也就看不到当轮图片与工具审计；真要润色引用型内容（引用回复、带链接的回答），
       只能靠草稿本身已经写全。这是明知而选的舍。
  3. **请求**：`ai.ChatRequest{Model: polish_model 或 model, Stream: true, Tools: nil,
     Temperature, MaxTokens}`。`Stream: true` 与主路径一致（有网关只支持流式）；
     `Tools: nil` 是硬约束 —— 润色器不许再调工具去「查证」。
     `MaxTokens` 直接取 `polish_max_tokens`：**0 = 自动**（按草稿估算），
     显式上限的合法区间是 **[1, 8192]**（与后台控件 `min=0` / `max=8192` 对齐，
     界面选得出的值后端必须接受），负数或越界由 `validateRanges` 拒绝并 `400`。
  4. **不再 Acquire aiSem**：`processMessage` 在整个回合里持有它，
     嵌套获取在 `max_concurrent_llm=1` 时必现死锁。润色与主调用共享同一次许可。
     - 为什么是"共享"而不是"释放后重取"：释放会让另一个会话在这两步之间插进来，
       既破坏「一轮对话的两次调用在时间上连续」，也让 `request_timeout` 的剩余预算不再连续可算。
     - 代价：一次回复占到 `max_concurrent_llm` 的一个名额更久（多一次调用的时长）。
       默认 4 个许可时影响很小；调到 1 时，两阶段会让串行队列的吞吐再慢一次调用的时间 ——
       但**不会死锁**，这是这个设计最主要的目的。
  5. **超时**：`min(polish_timeout_seconds, 剩余 request timeout)`。
  6. **回退**：HTTP 错误 / 超时 / 空结果 / 模型未配置 → 发草稿，审计仍记 `replied`。
     跳过条件（全局关 / 按群关 / 草稿 trim 为空 / rune 数 < `polish_min_chars` / 整轮预算已耗尽）
     不算失败。
     - **为什么必须回退而不是报错**：用户要的是那条答案，润色只是"更好听"。
       如果因为第二段失败而让整轮失败，就等于**用一个可选增值步骤否决了一次已经成功的生成** ——
       而 stage 1 的草稿是完整的、可发送的。
     - 代码上的保证不是"我记得处理了错误"，而是 `newPolish` 的**返回类型本身**：
       `Text` 默认就是草稿，任何出错分支只改 `Fallback` 字段、不改 `Text`。
       调用方拿到的**永远**是可直接发送的文本，所以**不存在"回退的错误路径"**可走。
  7. **页脚 token** = stage1（**含工具步**，`outcome.usage` 逐轮累加）+ stage2 的 usage；
     两边都没有 usage 时按 `estimatePromptTokens + EstimateTokens` 估算并相加。
  8. **指标**：`polish_requests`（真的发出去的 stage2 请求）/ `polish_fallbacks`（其中失败回退），
     两个都在 `/api/status` 的 `counters` 里。日志每轮一行
     `draft_chars / polish_chars / polish_model / polish_latency_ms / fallback`。
  9. **不润色的内容**：手动发送、主动消息、失败兜底文案、「让我想想」提前回复 ——
     它们不是 AI 正文，润色只会让操作员的原文变样。主动消息尤其：`proactive.go` 自己
     调模型拿一段文本就直接发，**不经过 `processMessage`**，因此天然没有 stage 2。
 10. **与提前回复/人味的分工**：
     - 提前回复（`early_reply_*`）是**发送时机**的补救，发生在 stage 1 期间，文案固定、不润色；
     - 人味（`humanize_*`）是**怎么发出去**（停顿/分段/沉默）；
     - 两阶段是**说什么内容**（先答对，再说得像）。
     三者正交，可任意组合；人味停顿按整轮耗时扣减，所以多一次润色不会再多等一遍停顿。

- **代价**
  1. **每轮多一次模型调用**：token 与延迟都增加（默认开启，可用 `polish_enabled` 全局关、
     `group_overrides[].polish` 按群关）。`polish_min_chars` 是给短回复的省调用闸门。
  2. **两次调用的用量口径**：`ai_requests` 仍只数主路径，润色走自己的两个计数器，
     所以「请求数」与「token 数」不再一一对应。
  3. **润色稿不是逐字可控**：它仍可能微调 Markdown 结构；`±30%` 长度约束写在提示词里，
     没有在代码里做强校验（强截断会截断 Markdown 代码块）。
  4. **可能只改口吻、也可能把口吻改坏**：润色器拿到的是"结论已知"的草稿，理论上不该改事实，
     但它能改**语气**——把结论说得更强硬/更敷衍、把保留意见删掉、把玩笑改成正经。
     事实没变、观感变了；这类退化没有自动检测，只能靠人看（或靠 `polish_fallbacks` 之外的抽样）。
  5. **多一次延迟**：`polish_latency_ms` 直接加在一轮回复的墙钟时间上（人味的停顿按整轮耗时扣减，
     所以总量上不会叠加两次等待，但**首字时间**确实晚了）。默认 20s 上限是给慢网关上的一道保险。
  6. **多一份配置面**：6 个全局键 + 1 个按群覆盖字段，默认开意味着"不配置也在花这次调用"。
     不想付这笔账的部署必须显式关掉；`polish_min_chars` 只能救短回复。

- **未做**
  - **润色后的事实校验**（例如把数字/链接与草稿逐个比对）：现在是靠提示词约束，
    代码里没有强校验；一旦线上出现编造，应补这一步而不是继续加提示词。
    具体的候选做法：抽出草稿与润色稿里的 URL / 数字 / 专有名词做集合比对，多出来的就回退草稿；
    或对两者算一个小的语义相似度闸门。**都不在这次范围内**，因为先要证明它真的会编造。
  - **`±30%` 长度约束没有代码级兜底**：约束写在润色指令里，润色稿比草稿长很多时照样发送。
    真要做只能是"超长回退草稿"，但那会把"润色器补足了省略号"这类合理变长也一起否掉。
  - **stage2 的流式透传**：现在仍是等完整结果再发送（群消息本来也不支持流式）。
    即使将来支持，也要先解决"润色稿会覆盖草稿"——透传意味着用户可能先看到草稿再看到改写版，
    那是两段不同的文本，比现在的"等一下"更糟。
  - **按群覆盖 6 个润色参数**：只覆盖了开关（`group_overrides[].polish`）。`polish_model` /
    `polish_timeout_seconds` 等是全局的 —— 不同群用不同润色模型的收益不明确，先保持一份配置。
  - **润色 A/B 与效果度量**：没有"开了润色的群 vs 没开的群"的对照指标。现在只有
    `polish_requests` / `polish_fallbacks` 两个过程指标，没有质量指标（用户是否更满意）。
  - **后台操作手册（已完成）**：键可以通过 `PUT /api/config` 与 `settings_write` 改，
    管理后台的控件也已就位 —— `ReplySettings.vue` 的「两阶段生成」区块、
    `PoliciesPage.vue` 的按群 `polish` 列、`configContext.ts` 的默认值与脏字段追踪。
    端到端验收由 e2e 用例与后台冒烟测试覆盖。

## D-042 Bot 命令框架与 `/tokenstats`：管理员在聊天里改配置

> **部分结论已被 D-043 取代**：`/help` 已实现且为公共命令；指令面板同步已加入。
> 下文「`/help` 是负收益」「框架只服务一个命令」「未做：`/help`」三处均以 **D-043** 为准。

- **背景**：后台能改的东西越来越多，但**改的那一刻手上不一定有后台**。运维在群里看到
  「页脚太吵 / 想临时关掉 token 统计」时，唯一路径是：开电脑 → 登录后台 → 找设置页 → 改 → 保存。
  对一个只是想切一个布尔值的动作，这条路径太重，实际结果是**干脆不改**。
  把"给 AI 用的配置写工具"（`settings_write`，D-035）当成解决方案也不合适：它要求管理员
  先说服模型、再让模型发起工具调用，**多一次模型调用、多一个不确定环节**，
  而且改完的措辞是模型生成的、不可预期。

- **动机**
  1. **把"改一个开关"从分钟级降到秒级**：在聊天框里打一行就生效，走的是后台同一条落库路径；
  2. **确定性**：命令的输入输出都是固定字符串，不经过模型 —— 没有"模型理解错了我的意思"；
  3. **可审计**：谁在什么时候改了什么，落在审计表里，与普通消息同一套查询；
  4. **先把地基做对**：命令框架（注册表 + 解析 + 权限 + 执行短路）一旦成型，
     以后加命令是一行登记的事；反过来先堆一堆 if-else 再抽框架，权限和边界一定漏。

- **方案**
  1. **注册表**（`internal/core/commands.go`）：`map[string]*command`，键是**小写**的命令名与别名；
     `command` 带 `name` / `aliases` / `adminOnly` / `usage` / `run`。
     后注册的同名命令覆盖先前的，所以将来可以用一个"改过的命令"顶掉内置行为，不必删代码。
  2. **解析**（`parseCommand`）：纯语法，不做权限与注册判断。顺序是固定的 ——
     **先看触发门**（`in.AtBot` 或 `Scene == "c2c"`），再看是不是**ASCII `/`** 开头，
     然后 `strings.Fields` 切词（多个空格 / Tab 都能吃），命令名 `ToLower`。
     只发一个 `/`、或去掉斜杠后没有词，都不算命令。
  3. **未注册即普通消息**：`lookupCommand` 找不到就返回 `ok=false`，消息继续走下面的普通管线
     （人味 → 上下文 → 模型）。**不回「未知命令」** —— 回了就等于告诉所有人"这里有一套命令"，
     而且会让群里随口打的 `/Facepalm` 之类的梗突然得到一句机器人的纠正。
  4. **权限**：`adminOnly` 的命令用 `isAdmin` 判定，口径与 `run_shell` / `settings_*` 完全一致
     （群消息看 `member_openid`、私聊看 `user_openid`，在 `admin_openids` 里）。
     名单为空 = 谁都不能用，与 D-035 的 `ErrNoAdminsConfigured` 同向。
  5. **执行短路**：接在 `dispatcher.route` 的**触发门之后、人味判定之前**。
     命中就 `handleCommand` 然后 `return`，**不进入 `processMessage`**。
  6. **首个命令** `/tokenstats [on|off|status]`（别名 `tokens`，默认 `status`）：
     开关全局 `reply_footer_enabled`（回复末尾的「—— 用时 x.xs · 约 N tokens」）。
     写入走 `settings.Apply`，与 `PUT /api/config` 同一条路径：校验、落库、立即生效。
     参数非法或给多了只回一行用法、**不改设置**。
  7. **审计口径**：入站 `status=command`（"被当作命令处理"，既不是被模型回答、也不是被丢弃），
     出站 `status=replied`（投递失败则 `failed` + `error`）。两条都绑原 `msg_id`。
     幂等沿用 `msg_id`：重复推送只执行一次、只留一对审计行。
     **SSE 顺序固定**：先把入站行写成终态 `command` 并推上实时流，再发送、再写出站行 ——
     第一帧必须是 `in/command`，不能是 `out/replied`（与 ADMIN-API §6 的 SSE 契约一致）。
  8. **扩展方式**：在 `registerBuiltinCommands` 里加一条登记 + 一个
     `run(ctx, d, in, s, args) (reply string, err error)`。解析、权限、审计、去重全部复用，
     新命令不需要碰 `dispatcher.route`。

- **为什么先做管理员命令**（而不是先做 `/help`、`/ping` 这类人人可用的）
  - 命令是**改状态**的入口，第一个必须验证的是"权限门关得住"。先做全员可用的命令，
    等于先把"谁能用"这个问题推到以后，而后来的命令都会照着第一个的样子写 ——
    一旦第一个没有身份门，后面的也很难加上。
  - 管理命令的价值立刻可验证：改完设置，下一条回复就能看到页脚变化，
    不需要额外造一个只读命令来试验框架。
  - **（已由 D-043 取代）** 当时认为 `/help` 是负收益：它必须对所有人生效，
    而当时唯一注册的命令是管理员命令 —— 一份"看起来谁都能用、实际用不了"的帮助文本，
    比没有帮助更糟。D-043 已把 `/help` 实现为公共命令，理由见 D-043「决定 1」。

- **为什么只认 `@机器人` 或私聊**
  - 全量群模式（E2）下机器人**能看见每条消息**。如果未 @ 的 `/xxx` 也算命令，
    群里任何一个人打一句 `/tokenstats off` 就能改掉全局配置 —— 这不是"方便"，是**提权**：
    群友本来连后台都进不去，却获得了和后台等价的写权限。
  - 只认 @ 的语义很干净：**@ 就是"我在对机器人说话"**。这与触发门（E1/E2）也一致 ——
    命令只在"机器人被叫到"的时候才可能存在。
  - 私聊天然没有 @ 的概念，且已经是一对一通道（对方也在 `admin_openids` 里才有效），
    所以 c2c 直接算候选。
  - 代价：管理员在群里忘了 @ 就打命令，命令不生效、会当普通消息聊。这是**正确的失败方向** ——
    宁可让管理员重打一次，也不能让群友误触发。

- **为什么不进对话管线**
  1. **命令不该被模型"接话"**：进了 `processMessage` 就会被上下文、工具循环、润色、人味
     任何一环改写，而命令的输出必须是确定的（"已开启"就是"已开启"）。
  2. **命令不该变成上下文**：命令与回执一旦写进 `messages`，下一轮模型就会看到
     「管理员：/tokenstats on」「助手：✅ 已开启…」，然后**开始模仿**这种对话风格，
     甚至在普通聊天里主动汇报设置状态。
  3. **不该计 `ai_requests`**：命令没有调用模型。混进模型计数会让"请求数 = 花了多少钱"
     这个口径失效。
  4. **不该有人味延迟**：人味是给"像人在聊天"服务的。对着一条运维指令停顿 3 秒再回，
     只会让人以为卡住了。
  5. **不该发提前回复**：提前回复的前提是"模型要很久"，而命令是**同步几毫秒**出结果的。
  6. 实现上就是"命中 → 执行 → 发一条被动回复 → `return`"，比"在管线里加分支"更不容易漏：
     管线里加分支，将来任何新增的步骤（比如新的发送前处理）都会自动作用到命令上。

- **为什么非管理员静默**
  - 在群里，回一句「无权限」或「未知命令」**都是在确认这个命令存在**。一旦确认存在，
    攻击面就从"猜"变成"针对性尝试"（试探参数、找绕过、社工管理员）。
    静默只保证**执行被拒时不给回执、不确认本次调用**；命令的**存在性**自 D-043 起
    由 `/help` 与指令面板对全群公开（面板项带 `only_admin` 标记），所以静默不再意味着
    "命令不可见"（**本句已由 D-043 取代/补充**，见下文 D-043「决定 1」）。
  - 静默的代价是"管理员自己没生效时也不好判断"，这个代价用**审计**补：
    入站 `status=ignored` 且 `error` 含「非管理员」，后台一筛就能看到谁在试。
  - 同理，**未注册的命令也不回「未知命令」** —— 否则"哪些是注册过的"就能被枚举出来。

- **审计口径**（为什么是 `command` 而不是复用 `replied`）
  - 复用 `replied` 会把命令混进"AI 回复"里，让"有多少条消息真的被模型回答了"无法计算；
    复用 `ignored` 又会把"执行了"说成"丢弃了"。
  - `command` 描述的是**处理方式**，与 `replied`（被模型回答）/ `ignored`（被丢弃）
    正好构成三分：一条入站消息要么被模型回答、要么被丢弃、要么被当成命令执行。
    出站侧不需要新值 —— 命令的回复**确实是一条已交付的消息**，就是 `replied`。
  - 已知的小代价：`records.status` 没有 `CHECK` 约束，历史行里可能存在其它值；
    但筛选是精确匹配，新增值不会影响老查询。契约同步在 `docs/ADMIN-API.md` §6（v1.4）。

- **代价**
  1. **多一份"写配置的入口"**：命令能改的东西后台也能改。两条路径写同一个键，
     好处是不会不一致（都走 `settings.Apply`），坏处是**误操作面变大** ——
     管理员在群里手滑打错就会改掉全局配置。缓解：`status` 随时可查、后台设置页显示当前值、审计留痕。
  2. **命令是全局的**：`reply_footer_enabled` 没有按群版本，所以一条命令影响所有群。
     这与该键本身的粒度一致；将来要按群，命令的参数语法就得扩展（见"未做"）。
  3. **群里可见**：管理员在群里 @ 机器人打命令，同群的人**能看到回执**（这本来就是群聊的性质）。
     敏感操作应该在私聊里做，或干脆用后台。
  4. **（已由 D-043 取代）** 当时框架只服务一个命令：注册表、别名、`adminOnly` 的能力
     都还没有被第二个命令验证过；真正的复用收益要等第二、第三个命令落地才能确认。
     D-043 已加上公共 `/help` 作为第二个命令（第一个 `adminOnly=false`），这条假设已被部分证伪。

- **未做**
  - **（已由 D-043 取代）`/help`**：当时判定为负收益，计划等有了第一个非管理员命令再一起做，
    且要求"按身份裁剪"（非管理员看不到管理命令名）。D-043 的做法相反且更简单：
    `/help` 列出全部注册命令、只给管理命令加「（仅管理员）」标记 —— 因为命令名的公开性
    本来就由指令面板承担，真正的门在执行侧的 `isAdmin`。详见 D-043「决定 1」。
  - **按群开关**：`/tokenstats` 只能改全局。要做按群，得先确定语法
    （`/tokenstats on --group <openid>`？还是"在哪个群发就作用于哪个群"？后者更自然，
    但会让同一条命令的语义依赖发送场景），还得解决"群里改了只影响这个群"的可见性问题。
  - **命令的参数校验与补全**：参数错了只回用法文本，没有"你是不是想打 on"的提示。
  - **命令的速率限制**：命令走普通入站路径，受群/成员白名单与去重保护，
    但**没有单独的命令频控**。管理员账号被盗时，命令可以被用来反复开关配置。
    影响有限（只改这一个键），所以没做；命令变多、能改的东西变多时应补上。
  - **审计里的参数没有结构化**：`records.content` 存命令原文（含参数），
    日志 `command handled` 也带 `args`，但没有"命令名 / 参数 / 结果"三列。
    要按命令统计使用频次，目前只能对 `content` 做字符串匹配。
  - **私聊里的命令没有二次确认**：`/tokenstats off` 立即生效。对布尔开关是合理的；
    将来若有破坏性命令（清空上下文、重置配置），必须加二次确认。
  - **命令与 `settings_write` 的关系没有收敛**：两条路都能改配置，一条是确定性的命令、
    一条是模型驱动的工具调用。长期看应该让"常用开关"都有命令入口，
    把 `settings_write` 留给"没有专门命令的冷门键"。

- **时间**：2026-11-02

## D-043 `/help` 与 QQ 指令面板注册：把「有哪些命令」做成可发现、可对账的事实

- **背景**：D-042 落地了命令框架与 `/tokenstats` 之后，暴露出两个缺口：
  1. **不知道有哪些命令**：命令只存在于注册表与 README 里。管理员要查"现在能打什么"得去翻文档；
     普通群友更是完全看不见 —— 而 QQ 客户端本来有一个「指令面板」，是可以直接展示命令的入口。
  2. **平台侧入口靠手工维护必然漂移**：加一个命令就要有人去平台后台改一次面板，
     改了描述、删了命令也不会有人记得同步。**注册表才是事实源**，面板必须从它对账出来。

- **动机**
  1. **可发现性**：让「有哪些命令」在聊天里（`/help`）和客户端里（指令面板）都能看到，
     而不是只有读过 README 的人知道；
  2. **可对账**：面板内容从注册表投影，加/改/删命令后一次同步就收敛，不依赖人的记性；
  3. **展示层与执行层解耦**：面板只是"看得到、点一下填入输入框"，命令能不能执行仍由消息事件
     + `adminOnly` 决定。面板同步失败绝不能反过来影响机器人。

- **决定 1：`/help` 对所有人开放**（推翻 D-042 里"先不做 /help"的判断）
  - D-042 的判断是：当前唯一的命令是管理员命令，一份"谁都能看"的帮助文本会暴露管理命令存在，
    而它又不可执行 —— 属于负收益。
  - **本轮推翻它的理由**：命令的存在性**本来就由指令面板对全群公开**（面板项带 `only_admin` 标记，
    QQ 客户端会展示）。既然"有 /tokenstats 这个命令"已经是一个公开事实，
    再把帮助文本藏起来只是多一道"去后台/看文档"的门槛，并不增加安全性。
  - 真正的门没有移动：非管理员执行管理命令仍然是**静默拒绝**（`ignored` + 原因「非管理员」）。
    `/help` 的输出只有命令名、用法、描述与「（仅管理员）」标记 —— **不含配置值、OpenID、
    管理员名单或任何运行期状态**。
  - 形态与其它命令完全一致：`adminOnly=false`，只认 @/私聊，`/help` 与 bare `help` 都命中，
    命中即短路（不调模型、不润色、不加人味、不写上下文），入站审计 `status=command`、出站 `replied`。
  - 清单**末行**固定一句面板点击说明（不带斜杠的命令名 ≡ 默认动作；带参数要写 `/命令 参数`），
    因为「点面板」是比打 `/help` 更常见的入口。
  - 输出按**规范命令名**排序、别名折叠（别名与命令名指向同一个 `*command`），
    所以输出稳定、可断言，也不会出现同一个命令两行。

- **决定 2：指令面板用「marker + 对账」，而不是平台后台手工配置**
  - **数据来源**：`registeredCommands()` 是唯一事实源；面板项由它投影 ——
    `type=command`、`name` = `usage` 首个词**去前导 `/`**（平台自己也会剥掉：写 `/help`
    读回 `help`；投影必须与回读同形，否则对账永远判「不同」、面板只增版本不收敛 ——
    这是 2026-11-02 首次上线实测到的坑，回归用例
    `TestSyncCommandPanelIsIdempotentAgainstPlatformNormalisation` 钉住了它）、
    `desc` = 注册表 `desc`（按 rune 截到 30，中文不会切坏 UTF-8）、`adminOnly` → `only_admin`。
    顺序按规范名排序，所以"同样的注册表 → 同样的面板"，对账才有意义。
  - **marker `remark=bot-harness:commands` 是唯一的所有权凭据**：平台的面板记录没有"归属"字段，
    `remark` 是唯一能稳定携带我们身份的地方。同步只认这个 marker —— **用户手工创建的面板
    永远不碰**，哪怕它的 items 明显更旧。
  - **状态机**：目标 scope 下没有 marker 面板 → `POST` 新建（`target_type=all`，`version=0`
    因为创建接口只回 `panel_id`）；有且 items 逐项相同（含顺序）→ `unchanged`；
    有但不同 → `PUT`（**不传 `version`**，语义是"让面板等于注册表"，不是"只在版本没变时更新"）→ `updated`。
  - **只更新，不删除**：删除无法证明所有权（marker 可能被手工面板占用、也可能被平台清洗），
    而平台侧面板对用户不可恢复。宁可留一个过期的自有面板，也不误删别人的东西 ——
    `DeletePanel` 只保留作协议完整性，同步路径**不调用**。
  - **两个 scope**：`group` 与 `c2c` 各对账一次，一个失败另一个照跑（缺私聊权限不应该拖累群面板）。

- **决定 3：失败不阻塞启动**
  - 面板是**纯展示层**：平台可能没给权限、命令描述可能触发内容风控、可能被限流 ——
    这些都是"面板不对"，不是"机器人不能用"。
  - 所以同步在 `main` 里以 `go dispatcher.RunCommandPanelSync(rootCtx)` 跑：
    启动后 **8 秒**首跑（token 与网络就绪），失败按 **30s / 1m / 2m / 5m / 10m** 退避，
    连同首轮**最多 6 次**；每次重试前重读 `command_panel_enabled`（中途关掉就停）；
    全部失败只 WARN，`RunCommandPanelSync` 既不返回错误也不 panic。
  - 成本：平台侧的内容/权限问题只会出现在日志里，不会让运维在启动阶段就发现 —— 这是刻意的取舍，
    用"面板缺失"换"机器人一定能起来"。排查入口写在 README §5.13.7 / AGENTS §4。

- **决定 4：`command_panel_enabled` 只关自动同步，不关手动动作**
  - 关掉它通常有两个原因：平台侧权限/风控让人不想在启动时打网络请求，或想完全手动控制面板。
    两种情况下**管理员仍需要一次"立即同步"**（改完命令、修好权限后不想重启）。
  - 因此开关只挡启动 goroutine；`POST /api/actions/sync-commands` **始终可用**。
    它也不是"命令总开关" —— 命令执行与面板无关，关掉开关照样能在聊天里用命令。
  - 自动与手动共用 `Dispatcher.SyncCommandPanel`，由 `panelMu` 串行化：
    否则两条路径可能读到同一旧版本、各写一次（第二次写的是同样的内容，纯浪费配额）。

- **决定 5：面板点击的无斜杠单命令名也算命令**（2026-11-02 上线当天发现并修掉）
  - **问题**：面板写成功后，用户从面板点「tokenstats」，客户端填进输入框的是 **`tokenstats`**
    ——不带斜杠，因为平台在写 `item.name` 时就把前导 `/` 剥掉了（决定 2 已记录这个平台事实）。
    而最初的 `parseCommand` 只认 ASCII `/` 前缀，于是这条消息**没被当成命令**：走 AI、不执行、
    面板"看起来能用、点了没反应"。面板白做。
  - **决定**：新增 `parseBareCommand` —— 当且仅当**触发门通过**（`in.AtBot` 或 `Scene=="c2c"`）、
    `strings.TrimSpace` 后**整条消息只有一个 token**、且该 token（小写）**在注册表里**（`name` 或 `alias`）
    时，视为命令，`args` 为空 ⇒ 走命令的**默认动作**（`/tokenstats` 的默认动作是 `status`）。
  - **为什么必须"整条消息恰好一个 token"**：面板点击只会得到命令名本身；多词的消息是人在说话。
    放开这条会让「@机器人 help me with X」变成一次 `help` 调用 —— 这是把功能误判成命令，
    比"面板点了没反应"更难排查。所以 `tokenstats off`（无斜杠 + 参数）也**不是**命令：
    **带参数必须带 `/`**。
  - **为什么不做前缀匹配**：注册表可能含中文/短名，前缀匹配会把 `help me`、`tools 怎么开`
    之类都吃掉。整串相等是唯一可预测的判据。
  - **未注册的 bare 单词不算**：`@机器人 foo` 照常走 AI。形态判定在 `parseBareCommand`，
    是否命中仍由 `lookupCommand` 查注册表 —— 与 `/xxx` 那条"未注册按普通消息走"完全同构。
  - **触发门一条不松**：这条只影响被 @ 或私聊的消息。全量群里未 @ 的 `tokenstats`
    和未 @ 的 `/tokenstats` 一样是普通消息 —— 否则群里谁打一句 `tokenstats` 就能改全局配置。
  - **`/help` 与文档同步**：`/help` 末行说明「点面板得到不带斜杠的命令名（如 `tokenstats`），
    等价于 `/tokenstats status`；带参数请用 `/命令 参数`」；README §5.13.1 / §5.13.3 / §5.13.7、
    AGENTS 铁律 12、DESIGN 4.2.2 一并改写。
  - **回归用例**：`TestLookupCommandAcceptsPanelBareName`（bare / c2c / 大小写 / 别名 / 多词不触发 /
    未注册不触发 / 未 @ 不触发）、`TestParseBareCommandIsSyntacticOnly`、
    端到端 `TestCommandPanelTapRunsBareCommand`（管理员 @ 发 `tokenstats` → 返回状态、不调模型、
    审计 `command`）与 `TestBareNameWithArgumentsIsNotACommand`。

- **代价**
  1. **平台硬限制直接成为产品上限**：面板 ≤ 20 个/机器人、项 ≤ 20/面板、`name` ≤ 14 字符、
     `desc` ≤ 30 字符、写 10 QPM / 读 30 QPM。命令一旦超过 20 个，**本实现不做本地裁剪**，
     平台会拒绝整次写入 —— 这是"故意不猜"：截断会让用户以为命令丢了。
  2. **两处文案要一起守**：`desc` 同时供 `/help` 与面板，长度与语气得兼顾（≤ 30 rune）。
  3. **每次同步都要列面板**：不持久化 `panel_id`，靠分页读 `/v2/panels` 找 marker；
     面板多时多花读配额（我们上限只有 2 个自有面板）。
  4. **marker 冲突**：如果用户手工建了 `remark=bot-harness:commands` 的面板，会被当成我们的。
     概率极低，且后果只是"那个面板被同步成命令列表"（不会删除）；接受。
  5. **`/help` 让命令名对普通群友可见**：这正是被推翻的 D-042 顾虑。缓解是执行侧的身份门不变，
     且面板本来就会公开这些名字。

- **未做**
  - **`channel` / `dm` scope**：平台支持 `channel`、`dm`，但本机器人部署形态只有群与私聊
    （D-002 / D-004）；等真的接了频道/私信场景再按同一套状态机扩。
  - **按群不同的面板**：`target_type=specific` + `group_openid` 可以做到"每个群一套"，
    但受 20 个面板的上限约束，且需要一套"哪些群要哪套"的配置与管理界面。
  - **平台「菜单」（menu）**：另一套展示入口，与面板不是同一个 API；本轮只做面板。
  - **自定义面板项**：目前 1:1 投影注册表，不能调整顺序、分组、加"常用"项，也不能隐藏某个命令
    （隐藏要靠注册表 `adminOnly` 或干脆不注册）。
  - **超过 20 个命令的裁剪策略**：不裁剪、不分组、不分页；先靠"命令别超过 20 个"的人为约束。
  - **面板内容的预览/审计**：后台只显示同步结果的 `action` 与 `version`，
    不显示"平台现在到底有哪些项"；要核对只能看回归同步的 `unchanged` 或直接查平台。
  - **同步指标**：没有 `counters.panel_syncs` 之类的计数，只能看日志与后台动作的返回。
  - **`panel_id` 持久化**：每次都要列，不能直接按 id 更新；面板多了以后值得做。
  - **删除/回收**：不删面板；如果命令全被移除，面板会被更新成 0 项而不是消失 ——
    平台是否允许 0 项面板需实测，当前未验证。

- **时间**：2026-11-02

## D-044 承诺（open loop）追问：问一次是闹钟，问到底才是承诺

- **问题**：定时提醒（D-033 / `scheduled_tasks`）发一次就结束。用户说「提醒我交周报」，
  到点机器人发一句，然后就没有然后了 —— 如果用户当时在开会、在睡觉、在忙，提醒等于没发。
  真正的承诺需要**追问**，而「什么时候再问」是生活常识问题，不是配置项。

- **决定 1：一张 `commitments` 表，状态机 `waiting / leased / done / cancelled`**
  - 字段见 `docs/ADMIN-API.md` § `/api/commitments`。`next_check_at` 是唯一的时间驱动；
    `nudge_count` / `last_nudge_at` 让模型知道「问过几次、上次什么时候问的」。
  - **租约复用 `status=leased` + `next_check_at`**（不新增 `lease_until`）：worker 抢到一条时
    把 `next_check_at` 改成租约到期时间；进程崩溃后租约自然过期，下一次 tick 重新领走。
    这样不需要额外的列，也不会出现「task 租约过期但状态不是 pending」的旧坑。
  - 索引 `(status, next_check_at)` 给调度器，`(creator_openid, status)` 给「每人未收口数」。

- **决定 2：频率由模型判断，代码只做技术防呆**
  - 每次到点调用一次**独立的决策模型**，输入：承诺文本/优先级/已追问次数/上次追问时间/
    最近 3 条会话消息/该会话的人类状态缓存/记忆里与勿扰相关的条目。
    系统提示要求输出 `{"message": "...", "next_check_seconds": N, "reason": "..."}`。
  - 代码只强制 `>= 60s`、`<= 180d`（`commitmentInterval`）。**没有**按次数递增的退避、
    **没有**「问 N 次就放弃」、**没有**代码静默时段 —— 这些都会把「人类常识」写死成产品规则。
  - 模型/JSON 失败 → 模板文案 + **3 小时**保守间隔，并打 WARN；承诺不能卡死。
  - 发送失败 → **5 分钟**退避重试，不增加 `nudge_count`（没发出去不算追问）。

- **决定 3：勿扰只认记忆，原路返回**
  - 决策提示要求检索「某时段不要打扰 / 睡觉 / 上班」类记忆，并把 `next_check_seconds`
    排到那个时段之后。**代码不设静默时段**：没有记忆就没有勿扰，这是「用户说过才算」。
  - 群里的承诺回到群（主动消息，`MaxSegments:1`），私聊的承诺回到私聊；
    不跨场景投递，也不因为「群里没人理」改私聊。
  - **群内点名只是文案**：模型可以在群提醒里写「小明，……」，但发送层不做平台 @mention
    （`qq.SendOptions` 没有 mention 字段），所以对方不会收到平台级「有人@我」提醒。
    真正的 @ 需要平台 mention 结构，属于未做项，不要按「已经 @ 到人」来宣传。

- **决定 4：收口必须显式**
  - `ack_commitment` 的 `outcome=done|cancel` 收口；`postpone` 只改
    `next_check_at = now + remind_after_seconds`，没给就按 **4 小时**兜底并记录。
  - 构建回复系统提示时注入该会话/该用户未收口承诺（id、text、追问次数、上次/创建时间），
    并明确告诉模型：用户表达完成/延后/取消才调用 `ack_commitment`，没提就正常聊天，
    不要每轮都催。
  - 每人未收口上限 `commitment_max_open_per_member` 默认 **10**（1–50）：
    这是防误操作，不是限频 —— 它限制的是「同时挂着多少条」，不是「多久问一次」。
  - `GET /api/commitments` 默认 `limit=50`（夹在 1–200），**没有分页/offset/cursor**；
    只支持 `conversation_id` / `creator` 过滤，且不过滤状态（返回完整历史）。

- **默认值**：`commitment_enabled=true`、`commitment_max_open_per_member=10`、
  `create_commitment` 首次提醒 `>=60s && <=180d`、postpone 兜底 **4h**、
  决策失败兜底 **3h**、发送失败退避 **5min**。

- **代价**
  1. 每条到点的承诺会多一次模型调用（只在真正到点时才花），成本与「问得勤」成正比；
  2. 模型可能给出很长或很短的间隔，代码只能兜底上下界，不能保证「像人」；
  3. 群主动消息有平台配额，追问会与其它主动消息竞争；发不出去就退避，不重试到无限；
  4. 决策依赖记忆检索的质量：用户没把「勿扰」写进记忆，代码不会替他们猜。

- **未做**
  - 没有「承诺模板/分类」（如吃药、交作业各自不同话术），完全交给模型；
  - 没有「追问次数上限/自动放弃」，这是有意为之；
  - 没有按群覆盖开关（全局开关足够，按群覆盖等真的有需求再加）；
  - 没有承诺的「子任务/依赖」关系，一条承诺就是一个独立 open loop。

- **前端契约（2026-11-03 对齐）**
  - `CommitmentItem` 以**后端实际 JSON** 为准（不是按假设命名）：`nudge_count`（不是 ask_count）、
    `next_check_at`（不是 next_ask_at），另含 `priority` / `last_nudge_at` / `ack_at` / `ack_note` /
    `meta` / `creator_openid` 等全部字段。
  - 状态标签固定四档：`waiting` 等待追问、`leased` 追问中、`done` 已完成、`cancelled` 已取消；
    列表页展示完整历史（后端 GET 不过滤状态），不再叫「未收口承诺」。
  - 取消走 `DELETE /api/commitments/{id}`：只有 `waiting` / `leased` 渲染按钮；
    `done` / `cancelled` 服务端返回 **409 conflict**（保护用户收口时写下的 `ack_note` / `ack_at`），
    前端按普通错误 toast。**不要**再加 404/405「只读退化」逻辑——端点已存在。

- **时间**：2026-11-03

## D-045 人类状态感知：只做语气与时机的参考，不做用户画像

- **问题**：同一句「在吗」，对方在上班、在睡觉、在打游戏时，合适的回复语气与时机完全不同；
  承诺追问也需要知道「现在是不是在忙」。但这些信息只在聊天内容里，没有结构化字段。

- **决定 1：从聊天内容推测「活动状态」，不推断敏感属性**
  - 状态枚举是开放的活动描述（吃饭/睡觉/工作/学习/打游戏/通勤/上课/运动/休息/在线/忙碌/离线…），
    系统提示明确禁止推断年龄、性别、健康、政治倾向、宗教、性取向等。
  - 输出 JSON 数组 `[{member_openid, display_name, status, confidence, evidence}]`，
    最多 20 人；失败只 WARN，不影响任何回复。

- **决定 2：阈值触发 + 单飞，不阻塞对话**
  - dispatcher 按 conversation 维护「自上次推测以来新增消息数」（内存计数，重启归零可接受）。
    达到 `human_status_trigger_messages`（默认 20，5–200）时**异步**跑一次。
  - 同会话 `humanRunning` 保证单飞；推测 goroutine **自己** `aiSem.Acquire`，
    不在已持有 aiSem 的回合里嵌套获取（那会在 `max_concurrent_llm=1` 时死锁）。
  - 构建回复提示词前若缓存已过期（`expires_at < now`）且累计消息达到阈值，
    再触发一次异步刷新；**本轮仍用现有缓存**，不阻塞回复。

- **决定 3：状态只进提示词与追问决策，不进用户可见消息**
  - 回复系统提示追加「当前聊天对象状态推测（可能不准，仅用于调整语气/时机）」：
    昵称 + 状态 + 置信度，并明确要求不要直接说出来。
  - 承诺追问决策读取同一份未过期状态。
  - 用户可见的输出只有 `/human-status` 命令；它是管理员命令，列为对象/状态/置信度/依据/更新。
    渲染**不依赖列对齐**：纯文本用列表，markdown 开启用代码块包裹的等宽表格（见下方
    「渲染策略」）。

- **关闭开关的语义**：`human_status_enabled=false` 只停止自动推测与提示词注入；
  已有的未过期缓存不会被删除，管理员手动发 `/human-status` 仍会强制执行一次分析。

- **手动命令强制同步分析（2026-11-04）**
  - `/human-status` 不再只读缓存：命中后先同步执行一次 `RefreshHumanStatus`（45s 超时），
    命令路径**自己**获取 `aiSem`、不嵌套（命令不进入回复管线，不持有信号量）。
  - 成功则立刻渲染新结果；成功但 0 条回「已分析，但暂时没有可推断的对象。」；失败时
    未过期缓存仍照常渲染、只记 WARN，缓存为空才回「分析失败，请稍后再试（简短原因）。」。
    原因经白名单转换，**不把 DSN/密钥/上游原文写进回复**。
  - 手动命令**不受 `human_status_enabled` 限制**；成功/失败分别计入
    `human_status_runs` / `human_status_failures`，与后台触发口径一致且不双计
    （`inferHumanStatus` 仍是 `HumanStatusRuns` 的唯一加计点）。

- **渲染策略：不依赖列对齐（2026-11-04）**
  - 问题：线上 `markdown_enabled=false`，纯文本路径原本输出「空格对齐的表格」。QQ 群聊是
    比例字体，列宽 padding 在客户端必然错位（用户反馈「没有正确渲染 markdown 表格」）；
    而 markdown 开启也不保证渲染表格 —— QQ 官方 Markdown 文档的「支持格式」只有标题、
    文字样式、链接、图片、有序/无序列表、列表嵌套、块引用、分割线和换行，没有表格语法。
  - **纯文本（默认路径）改为列表**：第一行 `最近聊天对象状态（N 人）：`，然后一人一行
    `- 对象｜状态｜置信度 85%｜依据 …｜更新 05:13`。字段用全角「｜」分隔，**不做任何列宽
    padding**；对象名不截断（列表不需要对齐），依据仍截 40 rune，更新时间只到时分。
    单人对象（c2c 只取对方）与群聊最多 10 人的上限不变（**2026-11-04 起**改为
    `/human-status [人数]` 的可选上限，见下方「人数参数 + 24h 发言者硬限制」）。
  - **markdown 开启时输出代码块包裹的等宽表格**（` ```text ` + 表头/虚线/数据行 + ` ``` `）。
    理由：表格语法没有官方支持，而代码块在桌面 QQNT、iOS 与新版安卓会以等宽字体渲染，
    空格对齐只有在这种字体下才成立；列内容与纯文本路径完全一致。若平台拒绝 markdown，
    `qq.Sender` 会把同一段正文以纯文本重发并停放 markdown 24 小时 —— 此时围栏会以字面量
    出现（排版变丑但信息不丢），纯文本路径（markdown 关闭）不受影响。
  - 代价：旧版安卓客户端可能不渲染代码块（会把围栏当普通文本），此时只是排版变丑，
    信息不丢；markdown 关闭的列表路径在所有客户端表现一致，所以默认值仍保持关闭时最稳。
  - 测试钉住两件事：纯文本必须含「- 」与「｜」、不得含 `| ---` 或空格 padding；markdown
    必须含代码块围栏与表头/分隔行（`internal/core/human_status_test.go` +
    `internal/e2e/commitments_status_test.go`）。

- **表格转图片（2026-11-04）**
  - 上线后确认 `markdown_enabled=false` 是线上实际值，列表虽是"不会错位"的下限，但仍不如图；
    于是新增通用渲染组件 `internal/render`（`Table{Title, Headers, Rows}` →
    `RenderPNG`），并把 `/human-status` 的输出升级为 **图片优先、文本回退**。
  - **渲染组件独立于业务**：`internal/render` 不 import `core`/`store`；字体以 `go:embed`
    打进二进制（容器不需要装字体），2x 渲染、按内容换行、>40 行截断。
    代价是二进制/镜像约 **+16.4 MB**（CJK face），这是"任何部署环境都不出豆腐块"的交换。
  - **字体是回退链（2026-11-04 补）**：单字体改为 `[NotoSansCJKsc, NotoEmoji,
    NotoSansMath, NotoSansSymbols2]`，逐 rune 用 `GlyphIndex(rune) != 0` 判定覆盖、
    按顺序选 face（0=CJK、1=Emoji、2=Math、3=Symbols2）。起因是花体昵称（`𝐶𝑟𝑎𝑛𝑦𝑜𝑧𝑒𝑛` / `𝓒𝓻𝓪𝓷𝔂𝔬𝔃𝓮𝓷`，Mathematical Alphanumeric Symbols
    U+1D400–U+1D7FF）CJK face 不覆盖，每个 rune 渲染成 `.notdef` 方框。
    - **测量与绘制同源**：列宽/换行/标题截断全部走 `fontChain.measure`（逐 rune 累加所选
      face 的 `GlyphAdvance`，忽略 kerning）；绘制按选中的 face 把连续 rune 聚成 run，
      用对应 `font.Face` + `font.Drawer` 画。基线/行高沿用 CJK face 的 metrics，
      Emoji/Math/Symbols2 同字号同基线，视觉高度一致。
    - **Noto Emoji（2026-11-04 补）**：NotoEmoji 是**轮廓单色**字体（`glyf`/`gvar`，无
      CBDT/COLR/CPAL/SVG），`opentype.Parse` 可直接解析，所以 😀 🎉 👍 🔥 ❤️ 等单码点
      emoji 由它接住；输出是正文颜色的单色字形，**不是彩色表情**。
      VS15/VS16、ZWJ/ZWNJ、肤色修饰（U+1F3FB–1F3FF）、U+20E3 等按零宽跳过，`❤️` 与
      `❤` 像素一致；没有 GSUB 塑形，ZWJ 序列（👨‍👩‍👧 / 🏳️‍🌈）按组件并排，可读不崩。
      代价再 **+1,982,596 B**。
    - **都不覆盖 = 豆腐块**：回退到主字体的 `.notdef`，与改动前行为一致。
      **彩色 emoji 仍不支持**：`x/image` 不渲染 CBDT/COLR/CPAL/SVG，NotoEmoji 只提供
      轮廓单色字形。
    - 代价：字体再 **+4,231,120 B**（NotoEmoji 1,982,596 B + Math 1,015,396 B +
      Symbols2 1,233,128 B）。来源/许可/sha256/覆盖范围见 `internal/render/fonts/README.md`；
      测试见 `internal/render/fonts_test.go`（`TestEmojiFallback` / `TestEmojiHasGlyph` /
      `TestFontFallbackCoverage` 等，含"移除 Math 字体"的对照渲染）。
  - **发送走平台富媒体**：先 `POST /v2/{groups|users}/{openid}/files` 上传
    （`file_type=1`、`file_name=status.png`、`file_data` base64、`srv_send_msg=false`），
    再发 `{msg_type:7, media:{file_info}}`。被动回复沿用原 `msg_id` + `msg_seq=1`；
    一张图就是一条消息；单张图 ≤5 MB（本地先检查再编码）。
  - **命令输出通用化**：`cmd.run` 返回 `commandOutput{Text, Image, ImageAlt}`。
    `Image == nil` 走原文本路径；有图则 `handleCommand` 调 `Sender.SendImage`，成功时
    出站审计 `content` 记 `ImageAlt`（如 `[图片] 聊天对象状态推测（8 人）`）。
  - **失败只降级排版**：渲染、上传、媒体消息任一失败 → WARN 记录原因 → 回退
    `Send(Text)`；`Text` 也为空时发「图片发送失败」。命令本身仍算成功，审计仍写
    `replied`。图片失败不能升级成命令失败，更不能丢答案。
  - 数据与文本列表同源：对象名不截断，依据在图片里放宽到 80 rune 交给渲染器换行；
    `/human-status` 的缓存/失败语义、45s 超时、管理员门都不变。
  - 测试：`internal/qq` 钉上传体与 `msg_type=7` 线形；`internal/core` 钉图片输出/回退/
    渲染失败；`internal/e2e` 钉 `/human-status` 的上传+图片消息、审计 `[图片] …`、
    无文本消息，以及上传失败时文本列表仍送达。
- **成员时间线 + 自主看图（2026-11-04）**
  - **问题**：旧的推测提示词只给 40 条「谁：内容」的混合转录，丢掉了说话人归属与时间，
    模型看不到「谁在什么时段活跃」；图片消息只以文字存在，模型无从判断画面。
  - **决定 1：输入改成按成员分组的时间线**。取最近 `human_status_timeline_messages`
    （默认 120，20–400）条消息，按 `SpeakerOpenID` 分组，每人一段
    「`## 成员：昵称 (openid)`」+ 按时间排序的 `[HH:MM] 内容`；图片追加
    `[图片 media_id=…]`，每人最多 4 张、总计 8 张（提示词防爆）。没有昵称用「未知」。
    末尾保留最多 20 条全局最近消息，帮助理解跨成员上下文。
  - **决定 2：图片由模型自主决定是否看**。推测请求带 `analyze_image`
    （`{media_id, question?}`）；系统提示明确要求**只在图片确有帮助时调用**，不要逐张调用。
    工具执行时用 `human_status_vision_model`（空 = 主模型）发一次多模态请求
    （`Parts=[TextPart(question), ImagePart(DataURI)]`，temperature 0.2、max_tokens 300），
    结果以 role=tool 回灌。预算：单次推测最多 **4 次调用、2 轮循环**。
  - **决定 3：失败只降级工具结果，不降级推测**。图片未缓存、媒体存储不可用、视觉调用失败、
    超次数都返回「图片不可用：…」文本，模型继续用文字证据输出 JSON；若两轮后仍没有 JSON，
    走原有失败路径（WARN + `human_status_failures`），不影响任何回复。
  - **并发**：推测 goroutine / 命令路径**已经持有** `aiSem`；工具内的视觉调用**不再 Acquire**，
    复用同一许可，避免 `max_concurrent_llm=1` 时自锁。
  - **成本**：每次图片工具调用 = 一次额外视觉请求；`human_status_vision_enabled=false`
    时不提供工具、不产生视觉调用（纯文字时间线照常）。观测新增
    `counters.human_status_image_calls`。
  - **隐私**：图片只在本进程内从媒体缓存读取并发给配置的视觉模型，不新增外部存储；
    时间线仍只用于本次推测，不写回 `messages` / 记忆 / 用户画像。
  - 测试：`internal/core` 钉时间线分组/时限/图片配额、工具循环、视觉失败降级、
    关闭开关不提供工具；`internal/e2e` 钉带图片的 `/human-status` 走完整工具链且仍发图片。

- **JSON 截断容错与重试（2026-11-04）**
  - **问题**：推测请求的 `max_tokens` 固定 800，成员多、evidence 长时输出数组被上游截断，
    整段 `json.Unmarshal` 直接失败（日志 `human status reply was not a JSON array: [{…`）。
    这不是模型不可用，却让 `/human-status` 回「分析失败」。
  - **决定 1：输出预算改为 `max(max_tokens, 2000)`**。用户配置更大时沿用配置值；图片分析
    工具那次仍保持 300（只需要一句描述）。主模型 context 足够，2000 只是下限。
  - **决定 2：增量解析，不再要求整段是合法 JSON**。先去掉 markdown 围栏
    （```json / ```）；从第一个 `[` 开始用 `json.Decoder`：`Token()` 吃 `[`，
    循环 `More()` 逐个 `Decode` 对象；遇到错误立即停止并保留已成功解析的对象。
    模型在数组前后加解释文字因此可以容忍；**至少 1 个对象即视为成功**，并打 WARN
    （`partial=true`、`parsed=N`）。**0 个对象才算解析失败**（纯文本、空数组、首个对象
    就坏掉都算）。空数组也进入重试：输出合约要求「证据不足可少输出」，但一次合法的
    `[]` 不构成一次可用的分析结果。
  - **决定 3：解析失败重试一次**。追加一条 user 消息「上次输出不是合法 JSON 数组或为空。
    只输出 JSON 数组，不要任何解释、不要 markdown；每个 evidence ≤ 40 字。」，`max_tokens`
    同上，且**不再带 tools**（第一次已经有机会看图，第二次只要求 JSON）。重试结果仍解析
    失败才走原失败路径（WARN + `human_status_failures`）。重试是真实计费调用：它的
    prompt/completion/total tokens 写进 INFO 日志 `human status JSON retry`；**不新增
    counter** —— `human_status_runs` 仍是「一次推测 = 1」，`human_status_failures` 仍是
    「一次最终失败 = 1」，避免改变 `/api/status` 与前端已有的计数语义。
  - **提示词**：evidence 收紧到 **≤40 字**（减少截断概率），分析维度、成员时间线、
    `analyze_image` 工具与 4 次/2 轮预算全部不变。
  - 测试：`internal/core` 钉 markdown 围栏、截断 JSON 只保留有效对象且日志 partial、
    纯文本→合法 JSON 的一次重试、两次都失败仍不写状态；`internal/e2e` 钉两次失败后
    `/human-status` 回「分析失败」且只调用模型两次。
  - **回滚**：镜像 tag `bot-harness:pre-status-jsonfix-20261104`（本次发布前打）；
    代码层可单独 revert 本节的改动，提示词与解析器都没有数据库/配置变更。

- **人数参数 + 24h 发言者硬限制（2026-11-04）**
  - **问题**：`/human-status` 固定只展示缓存里最近 10 人，而这个 10 是**缓存行数**，
    不是「最近说过话的人」：一个昨天发言、两小时前被推测过的人会占着位置，
    真正刚说过话的人却可能不在列表里；管理员也无法要求「再多看几个」。
  - **决定 1：命令接受可选人数**。`humanStatusUsage = "/human-status [人数]"`，
    缺省 **10**（历史行为），允许 **1–200**，其余（非数字、0、201、多个参数）一律回
    用法文本 `用法：/human-status [人数]` 并**不调用模型**。desc 改为
    「分析最近24h发言者状态」（12 rune，仍 ≤30，与指令面板共用）。
  - **决定 2：24h 发言者是硬限制，不是提示**。名单来自新 store 方法
    `RecentSpeakers(ctx, conversationID, now-24h, count)`：
    `SELECT speaker_openid, MAX(created_at), COUNT(*), 最新非空 speaker FROM messages
    WHERE conversation_id=? AND created_at>=? AND speaker_openid<>''
    GROUP BY conversation_id, speaker_openid ORDER BY last_at DESC, speaker_openid
    LIMIT ?`。时间参数是 **RFC3339 字符串**：SQLite 存 TEXT、PostgreSQL 是 timestamptz，
    pgx 以 text 格式发送字符串，PostgreSQL 按列类型把它 cast 成 timestamptz。
    24h 内没人发言 → 回「过去 24 小时没有可分析的对象。」、**不调用模型**；
    请求 100、24h 只有 15 人 → 只分析 15 人（名单按最近发言倒序截断）。
  - **决定 3：名单在四个地方同时生效**，少一个都会漏人：
    ① 提示词（system 追加「只分析这些成员」，user 列出 `- 昵称 (openid)` 并写明
    「不要分析列表外的人」）；② 成员时间线（`buildHumanStatusTimelineWithin` 的
    allow-list，名单外发言只留在最多 20 条的全局最近消息里当语境）；
    ③ 落库（模型多给的名单外对象直接丢弃，不写 `human_status`）；
    ④ 渲染（命令只显示名单内成员，缓存里其它人的状态过滤掉）。
    图片标题与 `ImageAlt` 的 N 取**实际渲染行数**，所以「传 100 / 24h 15 人」显示 15。
  - **决定 4：后台自动触发语义不变**。`RefreshHumanStatus` 原签名保留并委托
    `RefreshHumanStatusFor(..., humanStatusRefreshOptions{})`（`Restrict=false`）：
    整条时间线、最多 20 人、仍然把本轮触发消息补进转录。只有命令路径传
    `Members + Restrict`，也因此不在名单模式下补 `/human-status` 命令文本本身
    （命令不是发言，命令也不写会话上下文）。
  - **输出上限**：名单模式最多 `len(roster)` 项（≤200）；后台路径仍是历史的 20 项。
    上限在**过滤之后**按已落库条数计算，避免模型把名单外对象排在前面挤掉有效对象。
    输出预算仍是 `max(max_tokens, 2000)`：200 人的 JSON 可能被上游截断，走既有
    「截断保留已解析对象」路径，不新增失败模式。
  - **索引**：沿用 `idx_messages_conversation(conversation_id, id)`（先按会话收窄）。
    时间条件在该会话的行上过滤，是管理员手动命令，量级可接受；**本次不新增迁移**
    （新 `(conversation_id, created_at)` 索引要在 SQLite `runMigrations` 和 PG schema
    各加一版，收益不足）。若未来自动路径也要按时间窗查询，再补索引。
  - **测试**：store 钉 SQLite + gated PostgreSQL 的同语义（23h 内算、24h 边界包含、
    25h 前不算、按 openid 去重、最新名字、最近排序、limit）；core 钉参数解析、
    默认 10、非法参数、空名单、只分析最近成员、传 100 只分析 24h 内 15 人、
    落库/渲染过滤、后台旧路径不回归、ImageAlt 人数；e2e 钉 `/human-status 2` 只分析
    2 人、`/human-status abc` 回用法。
  - **回滚**：镜像 tag `bot-harness:pre-status-count-20261104`（本次发布前打）；
    代码层可单独 revert 本节（无数据库/配置键变更，无需迁移回滚）。

- **默认值**：`human_status_enabled=true`、`human_status_trigger_messages=20`、
  `human_status_timeline_messages=120`、`human_status_vision_enabled=true`、
  `human_status_vision_model=""`（= 主模型）、`human_status_ttl_minutes=120`（10–1440）。
  TTL 过期的行由维护循环 `PruneExpiredHumanStatuses` 清掉；列表接口仍可读到过期行，由调用方过滤。

- **代价**
  1. 推测会额外消耗模型调用（每 N 条消息一次），N 太小会变成成本项；
  2. 推测可能错，提示词里必须持续标注「可能不准」；模型仍可能在语气上误判；
  3. 同一会话的群聊在 `per_member` 上下文下只看到该成员的对话，
     状态覆盖范围也限于该会话 —— 这是上下文隔离的必然结果，不做跨会话窥探。

- **未做**
  - 没有按群覆盖 `human_status_*`（全局够用）；
  - 没有后台页面展示状态缓存（只有 `/human-status` 与 API 数据表）；
  - 没有把状态持久化到用户画像/长期记忆，TTL 到期即失效；
  - 没有「状态变化通知/主动关心」——状态只用于调整已有回复与追问时机。

- **前端契约（2026-11-03 对齐）**
  - 人类状态**没有独立管理页面，也没有专用 REST 端点**，所以前端没有对应的 `types.ts` 模型；
    可见入口只有聊天命令 `/human-status`（管理员；优先表格 PNG，失败回退纯文本列表 /
    markdown 代码块表格），
    以及 `GET /api/status` 的 `counters.human_status_runs` / `human_status_failures`。
  - 状态数据只进回复提示词与承诺追问决策，**不写进 `messages`、不进用户画像**；
    前端不要把它当成可编辑的用户资料来展示。

- **时间**：2026-11-03

## D-046 测试库隔离：PG 测试在建立连接前拒绝生产库

- **背景**：一次本地发布验证跑 `go test` 时，`PG_TEST_DSN` 指向了生产 `bot` 库
  （不是 `bot_test` / scratch）。`internal/admin` 的 PostgreSQL 配置契约测试通过真实 store
  打开该库，harness 随即把测试种子写进 `settings`：`qq_app_id=test-app-id`、`model=test-model`、
  `openai_base_url` 指向本地假上游、`openai_api_key` / `admin_token` 也被测试值覆盖。
  生产实例随即出现 token 认证失败、`/api/health` 返回 503。事故时间线与恢复步骤由部署方
  自行记录，不进入仓库。

- **根因**：
  1. 测试 DSN 的库名没有断言 —— 只要 `PG_TEST_DSN` 写错，测试就会照常连接并写库；
  2. 发布验证在可能继承了 `.env` / `DATABASE_URL` 的 shell 里执行，测试库与生产库的来源没有隔离；
  3. admin harness 在 `store.Open()` 之后立刻 `applySettings` 写测试种子，没有“这是不是一次性库”的前置检查；
  4. 文档里的 pgmigrate 示例甚至直接写了 `.../bot`，容易被复制。

- **影响**：被改写的是 `settings` 表（QQ 凭证、模型配置、API key、admin token）；线上机器人
  无法完成平台鉴权与健康检查。业务表（messages / memories 等）没有测试写入的迹象。
  已按升级前的配置备份逐键恢复，恢复后 token 与 health 正常。

- **决定 1：新增 `internal/testpg.Guard(t, dsn)`（仅测试用）**
  - 在**建立连接之前**解析 DSN 并取 database 名；URL（`postgres://` / `postgresql://`）与
    key=value（`dbname=...`，支持单引号）两种形式都支持；库名比较大小写不敏感。
  - 生产库名默认是 `bot`，可用 `BOT_HARNESS_PROD_DB` 覆盖（逗号分隔多个）；非 PostgreSQL DSN
    （SQLite 路径、`mysql://` 等）直接放行。
  - 拒绝时 `t.Fatalf`，提示改用 `bot_test` / scratch；消息只带库名，**不打印 DSN**（避免口令进日志）。
  - 它挡的是“库名写错”，不是数据库权限隔离：别名、端口转发、另一个名字指向生产库仍可能绕过，
    所以它是必要条件，不是充分条件。

- **决定 2：所有会打开 PG 的测试入口都接入 Guard**
  `internal/admin/admin_harness_test.go`（`store.Open` 前）、`internal/store` 的
  `pgScratchDatabase` / smoke / timestamptz 读 DSN 处、`internal/media` 的 `pgMediaStore`、
  `internal/pgmigrate` 的 `testDSN`、`cmd/bot-harness/token_test.go`（它会改写 admin_token）。
  新增入口必须同样在连接前调用；不要以“这个测试只读”为理由跳过。

- **决定 3：流程与文档**
  - `AGENTS.md` 铁律新增第 17 条「测试库隔离」：PG 测试只能连 `bot_test` / 一次性 scratch；
    **绝不**在 source 过 `.env` 的 shell 里直接跑 `go test`（`DATABASE_URL` 会被测试读到）；
    发布脚本必须显式设置 `PG_TEST_DSN=<bot_test 或 scratch>`。
  - `docs/PG-MIGRATION.md` §10 的集成示例从 `.../bot` 改为 `.../bot_test`，并写明 Guard 与
    继承 `.env` 的风险。

- **恢复口径（只记录做法，不记录任何密钥值）**
  从部署方自行保管的配置备份中读取 `QQ_APP_ID` / `QQ_APP_SECRET` / `OPENAI_API_KEY` /
  `OPENAI_BASE_URL` / `OPENAI_MODEL` / `ADMIN_TOKEN`，只回写被覆盖的 settings 键，保留事故之后
  新增的其它配置；取值和 SQL 不进入 shell history、日志或仓库。恢复后验证 token 子命令、
  `/api/health` 与一次平台调用。

- **代价 / 未做**
  - 新增 PG 测试入口需要人工记得调用 Guard；漏调不会自动报错。
  - CI 没有 PostgreSQL，PG 用例仍靠本地 / 发布脚本手动跑，Guard 只在真正执行时生效。
  - 没有自动创建 / 清理 `bot_test`，也没有在 shell 层拦截 `DATABASE_URL` —— 这两件事靠流程约束。

- **时间**：2026-11-03

## D-047 情绪状态：框架 + 内容包，默认关

- **问题**：0.1.0 的回复质量主要靠人设与润色，但角色没有「连续的心境」：同一句回复在连续被夸、
  连续赶工、深夜聊天时语气完全一样。直接把这些写进人设提示词会带来三个问题：状态无法持久化、
  无法解释「为什么这轮更短」、也无法在不改代码的情况下调整内容。需要一个**可开关、可观测、
  可回滚**的表达层。

- **决定 1：只做表达层，不做情绪识别 / 用户画像**
  - 状态按 `conversation_id` 隔离，只由**对话事件**推动；不判断用户真实情绪，不给用户贴标签，
    不写 `memories`、不进用户资料。
  - 数值只用于选择文案与行为系数，**不进入提示词**；注入只写自然语言片段，且明确要求模型
    「不要向用户解释、复述或索要回应」。
  - 事实、工具、白名单、限流、安全策略一律不读 affect；affect 也不能改这些。

- **决定 2：5 维，不做 7 维 / 成人向**
  - 固定 `valence`（心情）、`energy`（精力）、`stress`（压力）、`closeness`（熟悉度）、
    `patience`（耐心）。5 维足以覆盖「语气、长度、主动性」三个表达维度。
  - **不做 7 维**：再加「孤独 / 依恋 / 欲望 / 占有」之类的维度，会把产品推向拟真关系与情感索取；
    这些维度也很难在不越界的前提下定义。维度是框架契约，不是内容包可随意增删的字段。
  - **不做性 / 占有 / 成人内容**：不生成暧昧独占、嫉妒、吃醋、依赖培养、情感勒索或 NSFW 文案；
    `closeness` 只表示对话熟悉度，不表示恋爱好感度。这是产品边界，不是内容包配置。
  - 不推断年龄、性别、健康、残障、政治、宗教、性取向、种族、身份、财务等敏感属性。

- **决定 3：框架 + 内容包；内置 `daily`，默认关**
  - 框架负责演化、事件分类门、注入三档与预算、消毒、行为系数、存储、REST、命令、计数器；
    内容包（JSON，`go:embed`）负责基线、半衰期、耦合、事件正则 / delta、文案、行为档、预设。
  - 换内容包不改回复管线；但**维度与存储 / API 契约固定在框架里**，避免「换包 = 换协议」。
  - `affect_enabled` 默认 **false**：这是新增的表达层，默认开启会改变所有既有部署的回复风格，
    且情绪文案有内容风险。默认关让升级行为可预测，启用是管理员显式决定。
  - 按群覆盖 `affect` / `affect_inject_mode` 用于单独关闭或调档；**全局 `affect_enabled` 是总开关**，
    全局关时所有群都不生效，群里的 `true` 不能越过；`false` 是可靠关闭。

- **决定 4：落库 + 乐观锁，而不是进程内存**
  - 状态跨重启、跨实例必须一致；内存方案重启归零，无法解释历史曲线，也无法在后台看到。
  - 三张表：`affect_states`（每会话一行快照，JSON 存 5 维与最近事件标记）、
    `affect_events`（追加式事件日志，不含消息原文）、`affect_history`（曲线采样）。
  - **乐观锁**：`SaveAffectState` 以 `version` 匹配更新并 `+1`；冲突返回 `ErrAffectConflict`，
    上层重读最多尝试 **3 次（含首次）**，仍冲突只记 DEBUG 跳过。tick 与事件写入可能并发，悲观锁会拖慢回复路径；
    单飞 tick 下冲突很少，乐观锁足够且不会阻塞回复。
  - SQLite 走迁移 `0013_affect`，PostgreSQL 在 `pgSchema` 加三表与索引；时间统一 RFC3339，
    PG 用 `TIMESTAMPTZ`。

- **决定 5：演化公式固定，数值由内容包给**
  - 半衰回归：`v' = b + (v-b) * 2^(-dt/h)`；`dt` 封顶 24 小时，避免停机数天后一步跳变。
  - 单向 cap：`cap = clamp(base + scale*source, floor, 100)`；超过 cap 的部分按
    `exp(-0.4*dt)` 回落。只封顶、不托底，不做反向耦合，避免维度间互相驱动。
  - 事件：单次累计 clamp 到 ±6（`strong` ±20）；同类 1 小时内按 `1/(1+n)` 递减；
    每个事件有 24 小时滚动上限（daily 包为 10–30）。分类器统一加否定 / 假设 / 疑问 / ≤120 rune 门，
    内容包不能关掉这些门。
  - **不写撤销逻辑**：撤回 / 删除消息不回滚已计 delta；需要归零用重置。

- **决定 6：注入三档，失败不影响回复**
  - `off` / `line`（≤25 tokens，一行）/ `full`（≤`affect_inject_max_tokens`，默认 120）。
    注入放在 system prompt 的 human status 块之后，作为独立块。
  - 动态文案去换行 / 引号 / 括号，限 40 字，包在「仅作语气参考」标记里；不注入数值与用户原文。
  - 读设置、选包、渲染、超时任一失败 → 空串 + DEBUG，回复照常；**affect 永远不能
    让一条回复失败**，也不能改变既有的润色回退。

- **决定 7：行为层只做软约束，proactive 只做跳过与系数**
  - 行为系数作用在本地 settings 副本：字数下限 40、分段取 min、人味系数相乘、未 @ 沉默概率相加、
    proactive 概率**只会被下调**（`initiative_factor` ≥1 时忽略）。不改工具、白名单、限流与安全策略。
  - proactive 在群内任一会话 `energy<25` / `valence<20` / 暂停时**跳过本轮且不扣额度**；
    其余情况只按最小 `initiative_factor` 降概率，原有额度、静默时段与最小间隔不变。

- **决定 8：隐私与保留期**
  - 事件日志只存 `event_id` / `tone` / `source` / 时间，不存消息原文、昵称或 openid；
    `affect_events` 保留 **7 天**、`affect_history` 保留 **30 天**，由 `PruneAffect` 清理；
    `affect_states` 每会话保留最新一行，最近事件标记只留 48 小时 / 最多 200 条。
  - 不做操纵：不用内疚、恐惧、紧迫感、过度恭维或「你不理我我就难过」影响用户决策；
    不引导打赏、下单、充值或绕过安全策略。
  - 后台 `/affect` 与 `/mood` 都要求管理员；普通用户看不到数值与事件。

- **代价 / 未做**
  - 多三张表与一个 ticker；事件分类是正则，反讽 / 隐喻仍可能误判，所以门要保守、宁可漏计。
  - 状态可能让回复变得比人设更短或更少主动；管理员需要能一键关、按群关、按会话暂停。
  - `daily` 是唯一内置包；没有后台上传 / 编辑内容包，也没有按群选不同包。
  - 没有把状态写进记忆或用户画像，没有跨会话聚合，没有情绪变化的主动通知。
  - 设计参考了 dsh-longing 的公开思路（MIT），但**未复制代码 / 数据 / 提示词，也未运行它**；
    实现与内容均为本仓库独立编写。

- **时间**：2026-11-10


## D-048 自主加入：LLM 单次判定 + 话题切分，默认关

- **问题**：`trigger_mode` 只有「只回 @」与「未 @ 也回」两档。`all_messages` 下机器人会对
  每条群消息都走回复管线，只能靠人味的随机沉默碰运气；`at_only` 下又完全接不住
  「大家在聊的话题正好与它有关」的机会。需要一层**内容判断**：这条未 @ 消息值不值得接，
  以及群里最近在聊什么。它不能是新的触发模式（不改变 @ 语义），也不能变成用户画像。

- **决定 1：用一次 LLM 判定，不做规则版**
  - 值不值得接高度依赖语义：是求助还是闲扯、是不是在跟别人私聊、机器人刚才是否已经说过、
    接话会不会打断。关键词 / 正则只能覆盖「有人问 X」这类最粗的形态，误判成本高，
    且规则会不断膨胀、难以解释。
  - 模型输出 **JSON**（v1 为 `join` / v2 起为 `action`，外加 `addressed_to` / `confidence` /
    `reason` + `topics`），不输出回复正文；它只做「接不接」的判断，真正回复仍走原管线，
    人设、工具、润色、限流都不变。
  - 代价是每次评估一次模型调用；用默认冷却与频率上限把成本压到可控范围（见决定 3）。
  - **没有实现 `rules` 模式**：`join_gate_mode` 只有 `off` / `llm`。规则版需要另一套
    维护与测试成本，当前证据不足，先不做。

- **决定 2：同一次调用顺便出话题，而不是单独再调一次**
  - 「要不要接」与「在聊什么」需要同一份上下文；分两次调用会重复送窗口、增加延迟与费用，
    还可能给出互相矛盾的结论。
  - 一次调用里同时返回 `topics`（1–8 个，`title` ≤30 字、`summary` ≤80 字，
    `message_ids` 必须是输入里出现过的 id；v2 起每个话题可带 `type`）与 `current_topic_id`；
    解析成功才覆盖 `join_topics`，失败保留上一次缓存。
  - 话题只作为**回复背景**（最近 15 分钟、≤400 字，包装成「仅背景，不要强行转回」），
    不要求模型使用；它不进入用户可见消息，也不是指令。
  - 窗口固定为最近 `join_gate_window` 条（默认 20，5–50）、每条截断 200 rune；
    不做全量历史分析，避免 prompt 无界增长。

- **决定 3：默认关 + 硬闸门**
  - 这是新增的**表达行为**：默认开启会改变所有既有部署「未 @ 时回不回」的预期，
    且每次评估都花钱。`join_gate_enabled` 默认 **false**，关闭时与 0.2.0 逐字节一致。
  - **全局开关是总开关**：`group_overrides[].join_gate` 只能进一步关闭或显式开启，
    不能越过全局关；`false` 是可靠的紧急关闭。
  - 硬闸门依次是：消息长度 ≥2 rune、评估冷却（默认 60s）、每小时加入上限（3）、
    每天加入上限（12）、最小加入间隔（5 分钟）、置信度下限（0.7）。
    超限时**不调模型**，但仍写一条 decision，后台能区分「模型不想接」与「系统不让接」。
  - 这些默认值刻意保守：宁可少接几次，也不要变成刷屏机器人。

- **决定 4：fail-closed，失败不插话**
  - 解析失败、超时、非法 JSON、字段类型错误、`action` 非法值、拿不到模型并发额度，
    一律按**不加入**处理，写一条 `action=skip` / `joined=false` 的 decision
    （`reason="gate error: …"`），不重试、不降级成规则。
  - 理由：错误地插话比漏接一次更伤群体验；而且 join_gate 位于回复管线之前，
    它失败**不能**让一条本可回复的消息失败；关闭时也不改变既有 `at_only` 行为。
  - 反过来，模型返回 `action=join` 但置信度低于阈值，同样按不加入（`skip`）处理——
    置信度是第二道闸；`wait` 不参与置信度判定（它本来就不回复）。

- **决定 5：不做向量聚类 / 话题库**
  - 话题切分是**单次窗口内的结构化输出**，不建 embedding、不建向量索引、不做跨群聚类。
    原因：引入向量库会新增依赖与运维面，且聚类结果对管理员不可解释；
    当前窗口只有 20 条，模型直接切分已经够用，话题也只需要支撑「背景」而不是检索。
  - 不做跨群、不做长期话题档案、不把话题写进 `memories`；`join_topics` 每群只保留
    最近一次，覆盖式更新。
  - 如果未来要跨群主题分析，应作为独立能力重新评估，而不是把 join_gate 扩成主题系统。

- **决定 6：只处理群聊，存储与接口保持最小**
  - 私聊、命令、空消息、机器人自己的消息、其他机器人的普通发言都不评估；
    join_gate 排在 trigger gate 之前，开启后 `at_only` 下被判定加入的未 @ 消息也会回复
    （关闭时行为不变）；`@` 消息永远跳过 join_gate。
  - 两张新表：`join_decisions`（每次评估一行，预览 ≤80 字，不存全文）与
    `join_topics`（每群一行缓存）；SQLite 迁移 `0014_join_gate`，PostgreSQL 同步建表。
  - 只读 API：`GET /api/join/decisions`、`GET /api/join/topics`；启用与参数走既有
    `PUT /api/config`，不新增写端点。计数器 6 项加入 `/api/status`（v2 补 `join_gate_waits`）。
  - decision 是运维审计，不是用户档案：不按成员聚合、不写记忆、不跨群共享。

- **v2 追加（同一个「自主加入」能力的第二轮迭代；未发布前并入本节，不另开 D-0xx）**

- **决定 7：三分类 `join` / `wait` / `skip`，而不是布尔 `join`**
  - 布尔强迫模型在「现在就接」和「永远不接」之间二选一。真实群聊里最常见的是**第三种**：
    话题还在发展（有人在打字、问题还没定论、两个人正在争论），现在插话会打断，
    但过一会儿很可能正好。布尔只能把它记成 `join=false`，于是这条消息永远失去机会，
    运维也无法从 decision 里区分「不该接」与「先等等」。
  - 三分类让模型**只表达判断**，把「什么时候再看」留给代码（`join_gate_wait_retry_seconds`）：
    模型不掌握时钟，也不该被要求在 JSON 里算秒数。
  - `wait` / `skip` 在存储上仍写 `joined=false`，所以**频率闸门、上下文落库、回复管线
    完全不需要分叉**；`joined` 由 `action=="join"` 派生，老查询与老前端零改动。
  - `join_gate_waits` 计数器单独统计 `wait`（同时仍计进 `join_gate_skips`），
    因为「等一等」的比例直接反映阈值是不是调得偏保守。

- **决定 8：`wait` 的重试是「下一条消息 + 最小间隔」，不是定时器**
  - 最直接的做法是给 `wait` 排一个延时任务，到点重新评估。否决理由：那会**凭空产生**
    一次模型调用（群里可能早已安静），既费钱又容易对着一个已经结束的话题插话。
  - 现在的语义：只有当**该群又来了新消息**、且距上次模型评估已满
    `join_gate_wait_retry_seconds`（默认 30s，10–300）时，才**绕过评估冷却**提前重评估。
    没有新消息就永远不会因为 `wait` 自己醒来 —— 既保留「回头再看」的机会，
    又不改变「评估只由入站消息驱动」的既有结构。
  - 重试仍然受每小时 / 每天加入上限与最小加入间隔约束：绕过的是冷却，不是频率。
  - 冷却基准只认 `model<>''` 的**真实评估**：预检 / 超限写下的 cheap skip 若也推进冷却，
    繁忙的群会永远停在冷却里（这也是 v2 把 `LastJoinDecisionAt` 换成 `LastJoinDecision` 的原因：
    需要读上一次的 `action`，而不再只是时间）。

- **决定 9：引用机器人识别放在 dispatcher + store，不在模型侧猜**
  - 平台只给 `ref_msg_idx`（`REFIDX_…`），它本身不含「被引用者是谁」。
    要么让模型从窗口里猜（不可靠、且引用目标可能在窗口外），要么把**本地已知的
    assistant `quote_index`** 拿去比对 —— 后者是一次单表 `SELECT 1`，确定、便宜、可测。
  - 命中即 `AtBot=true` + `ReplyToBot=true`，与 @ 走同一条快路径：**引用不进入 join gate**。
    理由：引用机器人是比「公开话题」强得多的寻址信号，让它再过一次「值不值得接」的闸门，
    等于把已经明确的 @ 降级成一次赌博。
  - 识别**fail-open**（1s 超时、失败只 DEBUG、当作未命中），与 join gate 自身的 fail-closed
    方向相反但一致于同一条原则：**任何辅助能力都不能让一条本可回复的消息失败**。
    引用识别失败只是退回 v1 的未 @ 语义，不会丢回复。
  - 不新增表、不新增列：`quote_index` 早已存在于 `messages`（迁移 `0008`），
    这次只把它补进 SQLite base schema 的索引与 PG 的 `pgSchema`（`idx_messages_quote_index`），
    让 `WHERE group_openid=? AND quote_index=?` 不必扫全表。`ReplyToBot` 也不改
    `records.status` 枚举——它只解释「为什么这条未 @ 消息被回复」，不是一个新状态。
  - 顺带在 prompt 的窗口行上标注引用关系（`（回复 [#id]）` / `（回复 你）` /
    `（当前消息引用了这条）`），让模型判断 `addressed_to` 时也能看到「谁在回谁」；
    这不是新数据面，只是把已有的 `quote_index` 渲染出来。

- **决定 10：群名批量端点 + 按群卡片的界面，而不是继续用 openid 表格**
  - 策略页与 `/join` 页的键是 `group_openid`，对人不可读；运维改动时极易改错群。
    群名本来就在 `conversations.group_name` 里（平台首次给到时写入），缺的只是一个批量出口。
  - 选 `GET /api/groups/names?openids=a,b,c`（≤100）而不是 `/api/groups/{id}/name` 逐行：
    一个策略页可能有几十个群，逐行请求会把页面变成一个 N+1；批量接口一次往返、
    且对**未知群返回空串**（不是 404），前端统一「有名字用名字、否则用 openid」。
  - 不为此新增存储：名字是会话的既有字段，端点只做 read-through，不调平台、
    不缓存额外状态；接口不引入新的写路径。
  - 按群覆盖从宽表格改成**每群一张卡**的 2–3 列 grid：原来 20+ 个字段一行，横向滚动
    且看不清边界；卡片把「该群的 enabled / trigger / join_gate / affect …」收在一处，
    窄屏自然单列，再配搜索框过滤群名 / openid。代价是同屏看到的群变少，
    但策略页的任务是「改某一个群」，不是「横向比较所有群」。

- **决定 11：自研 `SelectMenu` 替换原生 `<select>`**
  - 原生 `<select>` 的下拉层由操作系统 / 浏览器绘制，**暗色主题下无法着色**：
    深色页面会弹出一块刺眼的白底列表，且 `option` 的 padding、字体、hover 都不受控。
  - 全站有几十处下拉（策略、加入、情绪、设置各页与分页条），逐个页面打补丁必然走形；
    统一成一个组件（按钮 + popover、`v-model`、`options`、`placeholder`、`disabled`、
    `aria` 角色、Arrow / Enter / Escape、点击外部关闭）才能保证行为一致且可单测。
  - 代价：需要自己实现键盘与可访问性，并替换所有既有 `<select>`；这是**一次性 UI 债**，
    比在每个页面重复处理「暗色下拉」便宜。
  - 可接受的替代方案（引入组件库）被否决：AGENTS §6 明确「纯 utility class，不引组件库」，
    为一个下拉引入整套设计系统不划算。

- **决定 12：话题带 `status` + `resumable`，让「结束」成为可表达的状态**
  - 只切分话题而不标状态，模型每次都会把同一个「周末聚餐」重新识别成进行中；
    旧话题没有出口，回复背景会一直把它当成活话题，闸门也可能在几天后重新接一句。
  - 引入三态：`active` 还在聊、`dormant` 暂时停了但可被新消息自然接回、`closed` 已结束；
    `resumable` 只对 `dormant` 有意义（是否值得接回）。`closed` 在解析时强制
    `resumable=false`，`active` 强制 `true`，`dormant` 保留模型值。
  - 状态放在 `join_topics` 的 JSON 里，**不新增列 / 表 / 迁移**：它天然属于「这一次话题切分」，
    和 `type` 一样是展示与提示字段，不参与频率、安全或权限判断。

- **决定 13：年龄兜底是单向降级，不能靠模型自觉**
  - 模型只看最近一窗消息，**看不见窗口外的旧消息**；如果它把三小时前的话题继续标成
    `active`，没有任何机制会纠正。让代码按「该话题最新消息的时间」兜底，是不依赖模型记忆的
    最后一道闸：超过 `join_gate_topic_dormant_minutes`（默认 20）`active` → `dormant`；
    超过 `join_gate_topic_closed_minutes`（默认 180）任何非 `closed` → `closed`。
  - **只降不升**：时间流逝永远不会把 `closed` 升回 `dormant` / `active`。否则一个已经结束的
    话题会随着时间反复「复活」，状态就失去意义。模型自己给的 `closed` 也不会因为过了很久
    被重新打开。
  - **无时间戳不猜**：`message_ids` 都落在窗口外时退回本次分析时间；时间解析失败或消息时间
    晚于分析时间（时钟偏差）时保留模型判断，避免把活话题误关。阈值必须
    `closed > dormant`，设置校验拒绝反序。
  - **保存前必跑**：写缓存前先降级，缓存里只能存**已降级**状态；回灌给闸门时再以
    `analyzed_at` 作兜底时间、按当前时间重跑一次，所以缓存放久了也会自然变旧。
    回复背景不重跑：它使用保存时的状态，且只取 15 分钟内的缓存。

- **决定 14：上次话题回灌 + closed 不接，但保留「明确重新提起」的余地**
  - 闸门模型看不到上一次的话题状态，容易把 closed 话题当新话题重新切出来。每次评估把缓存里的
    上次话题（最多 **8** 条、整块 ≤**500** rune，摘要每条 ≤40 rune）放在窗口前，让
    「closed 不要旧事重提、dormant 只有明显接回才可 wait/join」成为**模型可见的约束**；
    它只在闸门 prompt 里，不进入回复 prompt。
  - 回灌**不设新鲜度窗口**：越旧的 closed 越需要提醒；回复背景则相反，只取 15 分钟内、
    ≤400 字，并且全部 `closed` 时整块不注入——没有值得说的背景就不要硬提。
  - **closed 不接是输入约束，不是代码硬过滤**：代码强制 `resumable=false` 并把「已结束」
    标签同时喂给闸门与回复模型，但不会在代码层把 `action=join` 改写成 `skip`。
    理由：反讽、明确重启旧话题、或用户直接问「之前那个后来怎样了」都需要模型判断；
    硬过滤会把「旧事重提」和「合理追问」一起误杀。代价是模型仍可能偶尔翻旧账，
    由 prompt 与状态标签约束，必要时用 `_closed_minutes` 收紧。

- **代价 / 未做**
  - 每次评估一次模型调用；默认冷却 60s、每小时 3 次、每天 12 次，仍可能对高活跃群产生
    可观调用量，管理员需要按群观察与收紧。`wait` 重试（默认 30s）在高活跃群里会把这个
    上限抬到「每 30 秒最多一次评估」，需要时把它调大或直接关闭。
  - 话题切分不保证稳定：同一批消息不同时间可能得到不同结果，缓存只保留最近一次。
  - **生命周期是启发式**：`message_ids` 都落在窗口外时退回分析时间，长窗口 / 冷群可能比实际
    更早进入 `dormant` / `closed`；`closed` 不是代码层硬过滤，模型仍可能在被明确重新提起时
    选择 `join`。状态只用于提示与展示，不影响频率 / 安全 / 权限。
  - 上次话题回灌会让闸门 prompt 多出最多 8 条 / 500 rune；它不进入回复 prompt，成本可忽略，
    但模型仍可能在窗口很旧时误判「当前消息是不是在接回」。
  - 没有规则版、没有向量聚类、没有跨群主题分析；也没有「接话后自动结束话题」之类的策略
    （生命周期由年龄与模型判断，不由一次 join 触发）。
  - 模型可能偏保守（漏接）或偏积极（多接）；当前只提供置信度阈值与频率闸门，
    没有在线学习或按群自动调参。三分类给了更细的出口，但**代价是模型可能滥用 `wait`**
    （一直等下去），所以 `join_gate_waits` 与 `action=wait` 的单群占比需要观察。
  - `addressed_to` 目前**不落列**（只用于提示词判断与加入成功时的 INFO 日志）：加列要同时改
    SQLite / PG / 迁移 / 契约，收益只是分类统计；需要时再作为一次独立改动补上。
  - 引用识别只认**已落库**的 assistant `quote_index`：平台撤回、或 `quote_index` 缺失的
    历史消息只能退回未 @ 语义——宁可漏判一次引用，也不误判成被寻址。
  - 群名来自本地缓存，从没见过的群仍显示 openid；界面不会为它去调平台接口。
  - 版本号 `0.3.0` 待 Lead 确认后再统一写进代码与构建产物（v2 不改变版本号，仍未发布）。

- **时间**：2026-11-10

## D-049 图片工具：引用 / 合并图片入库 + 按需 `view_image`

- **问题**：D-020 已经解决「当前消息图片本地缓存 + 多模态输入」，但平台只把**当前消息**的附件
  放在顶层 `attachments`；引用消息、合并 / 转发消息里的图片在 `msg_elements` 里，
  不入库就没有 `media_id`，模型既看不到、也没法回看。另一方面，历史图片如果每轮都重新内联，
  token 会随对话轮数线性增长；把整段引用链的图片全塞进本轮也不现实。需要一条
  「图片可被寻址、但只在需要时查看」的路径。

- **决定 1：入站递归展开 `msg_elements`，引用 / 合并图片与当前图片一起缓存**
  - 平台事件里引用 / 合并的附件不在顶层字段，必须深度优先遍历 `msg_elements`（合并转发还会
    嵌套）。不展开的结果不是「稍后能看到」，而是**永远没有 id**——模型连调用工具的依据都没有。
  - 图片按 sha256 内容寻址，同 URL 去重；非图片附件只记元数据。下载 / 存储失败只记 WARN、
    保留占位，消息照常落库与回复，不把「一张图抓不到」升级成「整条消息失败」。
  - 复用既有的 `media` 表、域名白名单、`media_max_bytes` 与 `vision_max_images`，
    不新增表 / 迁移 / 抓取通道。

- **决定 2：模型只拿 id 标注，不拿图片本身**
  - 当前消息标 `[图片 #id]`，引用 / 合并标 `[引用图片 #id]`；历史消息里已缓存的图片也会在
    **模型输入副本**里补同样的标注。标注不写进落库正文，下一轮可依据 `attachments` 重建。
  - 标注是「可寻址」而不是「已看过」：它让模型知道有这张图、可以用工具查，同时不承诺内容。
  - 这是 D-020「不把图片写进正文」的延续：后台时间线与模型上下文保持解耦。

- **决定 3：当前图片内联，引用 / 合并图片只走工具**
  - 当前消息的图片就是用户此刻在说的东西，直接以 `data:` URI 内联（最多
    `vision_max_images`），省掉一次工具往返；引用 / 合并图片不内联，因为它们可能很多、
    也可能与当前问题无关，全塞进去会让 token 随引用链增长。
  - 历史图片也不重发；模型需要时调用 `view_image`。这样「看什么图」由模型按相关性决定，
    而不是每轮把整个相册重新付一遍钱。
  - 多模态请求失败时退回纯文本重试一次，图片 id 标注仍在；不会因为端点不支持图片而丢回复。

- **决定 4：`view_image` 是一个只读、按需、失败即文本的工具**
  - 参数只有 `media_id`（必填）与 `question`（可选，≤200 字）；返回 ≤500 字描述。
    不提供「列出图片」「跨会话搜索」「生成 / 修改图片」——工具越窄，越不容易被诱导滥用。
  - 只读（`ReadOnly`），不需要管理员身份；与 `run_shell` / 设置写入等危险能力无关。
  - **失败一律返回文本，不返回 Go error**：id 不存在 / 已过期、不是图片、视觉未启用、
    未配置模型、调用失败 / 空结果，都变成「图片不可用：…」交给模型；工具审计仍算成功，
    回复管线继续。图片能力是辅助，**任何图片问题都不能让一条本可回复的消息失败**。
  - 视觉未启用时工具定义本就不下发；工具内部再判一次，防止模型被旧上下文诱导后自行调用。

- **决定 5：复用 `human_status_vision_model`，不新增视觉模型键**
  - 人类状态推测已经有一个视觉模型配置；再开一个 `vision_model` 会让「换视觉模型」变成
    两处配置，且大多数部署会用同一个多模态端点。因此 `view_image` 优先用
    `human_status_vision_model`，为空回退主模型 `model`。
  - 但**开关不复用**：`human_status_vision_enabled` 只控制 `analyze_image`；
    `view_image` 由 `vision_enabled` 控制。两个能力的成本、失败影响与使用场景不同，
    共用一个开关会让管理员无法只关其中一个。
  - `vision_enabled` 是全局开关（没有按群覆盖），与既有的 `vision_max_images` 一致；
    工具下发还要求 `tools_enabled` 与该群 `tools` 打开，避免在禁用工具时留下可达入口。

- **决定 6：隐私边界沿用 D-020，并把「描述」也当作敏感数据**
  - 图片只从 QQ 附件域名抓取，限制单文件大小与每条消息张数；图片字节只发给配置的视觉端点。
  - 工具结果只进入当前模型上下文；**不落库描述**，工具审计只记 `media_id` / `question`
    （截断 500 字）与耗时。不提供跨会话图片列表或按内容搜索，id 是内容 sha256，
    不会从外部被枚举。
  - 默认提示词明确不推断年龄 / 性别 / 健康等敏感属性；但视觉模型仍可能看错或过度推断，
    因此描述只作参考，不能当事实来源写进记忆或用户画像。

- **代价 / 未做**
  - 每调用一次 `view_image` 就是一次额外的视觉请求（最多 500 输出 tokens、45s 超时）；
    系统提示词要求「只在图片确实重要时调用」，但模型仍可能过度调用，成本需要观察。
  - 图片理解只覆盖 `image/*`：没有 OCR 结构化、PDF、视频、音频；语音只记录 ASR 文本元数据。
  - 描述可能不准确、跨轮不一致；没有针对图片描述的缓存（同一张图重复调用会重复付费）。
  - 单条消息的抓取张数取**启动时**的 `vision_max_images`，改小 / 改大后抓取上限要重启才更新；
    本轮内联张数按当前设置即时生效。
  - 没有图片管理界面；清理只按 `history_ttl_hours` 删除过期且无消息引用的缓存。
  - 版本号 `0.3.0` 待 Lead 确认；本节与 D-048 同属未发布迭代。

- **时间**：2026-11-10

## D-050：模型调用失败时向聊天透传脱敏摘要

- **背景**：模型 4xx/5xx、超时、鉴权失败时，聊天里只有「抱歉，我这边刚才出错了，稍后再试试～」。
  用户无法判断是自己问错了、模型限流、还是后台配置坏了；管理员也只能去翻日志。
- **决定**：普通对话的失败兜底改为「抱歉，我这边出错了（<摘要>）。可以稍后再试，或让管理员检查后台模型配置。」
  - 摘要由 `userFacingAIError` 统一生成：4xx 保留状态码与上游 `error.message` 摘要；
    5xx 只透状态码（响应体常是网关 HTML/内部堆栈）；超时、空回复、reasoning-only、鉴权、限流分别有固定文案。
  - **脱敏**：URL 主机/路径、`sk-…`/Bearer、`api_key`/`token`/`password`、`postgres://` DSN 一律替换；
    摘要截断到 220 rune；完整 err 仍只进日志与 `LastAIError`。
  - reasoning-only 保持原有人话术（不把思维链倒给用户），其余路径统一带摘要。
- **不做**：不透传请求体、完整响应、堆栈、内部端点；不改 `join_gate` / human-status 等内部判定的失败文案。
- **代价**：用户会看到「HTTP 400」这类技术词，但比笼统文案可行动；上游 message 可能仍含业务错误码，已截断且不含密钥。
- **时间**：2026-11-11


## D-051：PostgreSQL 不认 HAVING 的 SELECT 别名

- **背景**：合并同群会话在 PG 下报 `SQLSTATE 42703: column "n" does not exist`，SQLite 一直正常。
- **根因**：`HAVING n > 1` / `ORDER BY n DESC` 引用 `COUNT(*) AS n` 的别名。SQLite 允许，
  PostgreSQL 的 HAVING 在 SELECT 别名之前求值；ORDER BY 才允许别名。
- **决定**：SQL 里 HAVING / ORDER BY 一律重复聚合表达式（`COUNT(*)`），不依赖别名；
  并在 `TestPostgresQueries` 里固定覆盖 `MergeableGroups`，避免只靠 SQLite 测试放行。
- **教训**：所有 `HAVING` / 别名排序语句都要在真实 PG 上跑一遍；SQLite 通过不代表 PG 通过。
- **时间**：2026-11-11


## D-052：人情味 2.0 的四条边界

- **背景**：MaiBot 在「像人」上比我们细：群消息防抖、分段气泡、逐段打字、备用口吻、概率引用。
  本轮吸收这些做法，但必须保持我们已有的 join gate / 情绪 / 两阶段润色的语义不变。
- **决定**：
  1. 合并只发生在**未 @ 的群消息**、且已通过策略门 / 命令 / 静默判定之后；@ / 引用机器人 /
     命令零延迟。批次 leader 是第一条消息，窗口内新消息把计时器重置为「从该消息起算」，
     整批硬上限 20s（从 leader 到达起算）；到点后只有 leader 跑模型，其余消息仍写时间线
     并各留一条审计（`debounced into <leader_msg_id>`），不静默丢。批次结构由 mutex 保护，
     可被 baseCtx 取消；关闭时清空 pending 并记录条数。等待取 quiet / active 两档，
     本群最近 60 秒人类消息 >= 3 条即视为活跃。
  2. 分段气泡只在 `humanize_level != off` 时生效；代码围栏、URL、引用块整体不拆；
     硬限制仍受 `reply_max_chars` / `reply_max_segments` 约束。
  3. 打字等待按**每条气泡**计算，整轮有上限并扣除模型/润色已耗时；off 模式零等待。
  4. 口吻轮换每轮只掷一次，草稿与润色稿共享同一份 systemPrompt；不修改润色硬约束。
     概率引用只作用于正式答复，即时回复/错误兜底/手动发送的显式引用不抽签。
- **兼容**：`quote_reply` 布尔保留为 `quote_reply_mode` 的视图（true=occasionally，false=never），
  新旧键同 patch 时新键优先；按群覆盖仍是布尔粒度，全局 never 是天花板。
- **不做**：不引入向量表达库与嵌入模型；不做 LLM 断句（规则拆分更便宜、可预测）。
- **时间**：2026-11-11

## D-053：表达方式库：先规则/向量，不引入第二套记忆

- **背景**：MaiBot 的高人情味有一半来自「表达方式库」：从真实聊天里学情境→说法，
  再按语境选几条注入。我们已有长期记忆（事实）与两阶段润色（这一条的措辞），但缺少
  「跨轮次的说话方式」这一层。
- **决定**：新增独立的 `expressions` 表与表达管线，不与记忆表混用：
  - 学习异步、每 `expression_learn_every` 轮一次，只学 situation/style，不学事实；
    默认进待审（disabled），审核后才参与召回。
  - `fast` 档不调 embedding，按 use_count 加权随机；`precise` 档先 embedding 余弦粗排，
    超过注入上限时再让主模型按语境选 0–N 条；任何 embedding/选择失败都退回 fast 或空。
  - 只召回本群 + 全局，默认不跨群；私聊内容不进群聊。
  - 注入位置固定在「人设/口吻之后、记忆之前」，开关关闭时提示词零变化。
- **不做**：不做自建向量数据库/ANN（候选量小，余弦足够）；不做表达自动改写记忆；
  不把学到的 style 当成事实写进长期记忆。
- **代价**：precise 档每轮最多两次额外模型/embedding 调用（总预算 2s）；表达可能学歪，
  因此默认审核 + 后台可停用/删除。
- **时间**：2026-11-12


## D-054：把当前会话身份注入 systemPrompt

- **背景**：后台按 `group_openid` / 用户 OpenID 定位群与私聊。管理员在聊天里说
  「把本群的触发模式改成仅 @」时，模型原本不知道「本群」是哪个 id，只能反问。
- **决定**：每轮构造 systemPrompt 时注入一块「当前会话」：
  - 群聊：`群 OpenID：<group_openid>`；
  - 私聊：`用户 OpenID：<user_openid>`（缺失时回退 member_openid）；
  - id 为空时整块省略，不输出空键；群聊不注入成员私聊 OpenID。
- **位置**：人设/口吻之后、表达方式/记忆之前；润色阶段复用同一份 systemPrompt。
- **不做**：不把它做成开关（这是模型正确使用管理工具的必要上下文，不是可选风格）。
- **代价**：每轮多几十个 token；相比一次反问/改错群的代价可以忽略。
- **时间**：2026-11-12
