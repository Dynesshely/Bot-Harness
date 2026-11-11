# Admin API 契约（冻结版 v1.9）

> 本文件是前后端唯一契约。字段只增不改；新增字段前端必须容错。
> 所有路径以 `/api` 为前缀。除标注 **公开** 外，全部需要认证。
>
> 本文件已按实际 handler（`internal/admin/*.go`）逐条核对，修订记录见 §0.1
> （v1.8 → v1.9：自主加入 `join_gate`；新增 12 个配置键、3 个只读端点与 6 个计数器；
> v2 增补 `action` 三分类、话题生命周期（`status` / `resumable` + 年龄兜底）、
> `GET /api/groups/names`、`join_gate_wait_retry_seconds` / `join_gate_topic_dormant_minutes` /
> `join_gate_topic_closed_minutes` 与 `join_gate_waits`，属同一次未发布修订，
> 不再另开 v1.10），
> 更早的修订为 §0.1.1（情绪状态 `affect`）、§0.1.2（承诺可从后台取消）、
> §0.1.3（承诺追问与人类状态）、§0.1.4（QQ 指令面板）、§0.1.5（Bot 命令与 `/help`）、
> §0.1.6（两阶段生成）。
> 三处历史漂移（SSE 事件集合、分页 `page_size` 回显、写接口的三秘密脱敏）已修复，
> 本文件据此定稿；以后改动 handler 响应的提交必须同步本文件（契约测试在 `internal/admin/*_test.go`）。

## 0. 通用约定

- 请求/响应均为 `application/json; charset=utf-8`；请求体上限 1 MiB。
- 时间字段统一为 **RFC3339 带时区** 字符串（如 `2026-07-21T10:00:00+08:00`），或 `null`。
  本项目的 `*_at` 字段一律是字符串，空值用 `""`（不是 `null`）。
- 时长字段统一为**秒**（整数，`*_seconds`），耗时毫秒用 `*_ms`。
- 所有响应都包一层信封：

```json
{ "ok": true, "data": { } }
```

```json
{ "ok": false, "error": { "code": "invalid_request", "message": "human readable" } }
```

- HTTP 状态码同时反映结果：`200` 成功、`400` 参数错、`401` 未认证、`403` 禁止、
  `404` 不存在、`409` 冲突、`429` 限流、`500` 内部错误、`502` 上游（QQ 平台 / 模型网关）失败、
  `503` 依赖未就绪。
- **未知字段一律 `400`**：请求体解码用 `DisallowUnknownFields`。
  `PUT /api/config` 与 `PUT /api/policies` 走 `decodePatch`：空体与空对象也是 `400`
  （`invalid_request`，消息分别为「请求体为空」「没有任何要修改的字段」）；
  其它写接口的空体按各自语义处理（如登录空口令 → `401`，手动发送空内容 → `400 empty_content`）。
- **未知 API 路径返回 `404` JSON**（`error.code = "not_found"`，消息 `unknown API endpoint`），
  不会回落到 SPA 的 `index.html`。
- 认证失败的响应体同样是标准错误信封（`401` + `error.code = "unauthorized"`），不是裸文本。
- 敏感字段永远只返回掩码形式（长度 > 4 时保留前 4 位 + `****`，否则全部 `*`），
  由 `settings.MaskSecret` 实现。`qq_app_secret` / `openai_api_key` / `admin_token`
  三个秘密**只进不出**（见 §3 的三秘密说明）。

### 0.1 修订速览（v1.8 → v1.9）：自主加入（join_gate，含 v2）

新增「未 @ 群消息由模型判断是否加入」及其后台与只读 API。**默认关闭**，关闭时回复管线、
触发模式与现有接口响应**零变化**；没有新增消息状态。

- **12 个配置键**（§3）：`join_gate_enabled`（默认 false）、`join_gate_mode`（默认 `llm`）、
  `join_gate_model`（默认空串 = 主模型）、`join_gate_window`（默认 20，[5,50]）、
  `join_gate_confidence`（默认 0.7，[0,1]）、`join_gate_eval_cooldown_seconds`
  （默认 60，[10,3600]）、`join_gate_min_interval_minutes`（默认 5，[1,240]）、
  `join_gate_max_per_hour`（默认 3，[1,30]）、`join_gate_max_per_day`（默认 12，[1,100]）、
  `join_gate_wait_retry_seconds`（默认 30，[10,300]，v2：上次判定 `wait` 时满这个
  间隔可绕过评估冷却提前重评估）、`join_gate_topic_dormant_minutes`（默认 20，[5,240]）、
  `join_gate_topic_closed_minutes`（默认 180，[30,1440]，必须大于 dormant 阈值）。
  按群覆盖新增 `group_overrides[].join_gate`（`bool | null`）。
- **3 个只读端点**：`GET /api/join/decisions`、`GET /api/join/topics`（§6.8）与
  `GET /api/groups/names`（§5，v2 新增，批量把群 openid 解析成群名，≤100 个）。
  全部 protected；没有写端点，启用与参数仍走 `PUT /api/config`。
- **6 个计数器**（§2）：`join_gate_evals`、`join_gate_joins`、`join_gate_skips`、
  `join_gate_waits`（v2 新增）、`join_gate_errors`、`join_gate_topics`，
  `counters` 由 **24 项变成 30 项**。
- **`action` 三分类**（v2）：`join_decisions` 新增 `action` 列（`join` / `wait` / `skip`），
  `GET /api/join/decisions` 的每条 decision 增加 `action` 字段；`joined` 保留并由
  `action=="join"` 派生，**既有字段含义不变**，老前端可继续只读 `joined`。
- **话题生命周期**（v2）：`join_topics` 的话题 JSON 增加 `status`（`active` / `dormant` /
  `closed`）与 `resumable`；模型给出初值，代码按话题最新消息年龄**单向降级**
  （`join_gate_topic_dormant_minutes` / `_closed_minutes`），`closed` 强制不可接回。
  `GET /api/join/topics` 与 decision 的 `topics` 原样返回这些字段，**没有新表 / 新列**。
- **1 个后台页**：新路由 `/join`（nav「自主加入」），保存走既有 `PUT /api/config`；
  v2 起决策列表显示「插话 / 等一等 / 不插话」，群选择显示群名（`/api/groups/names`），
  最新话题面板显示 `status` / `resumable`（进行中 / 暂歇可接回 / 已结束）。
- **迁移**：SQLite `0014_join_gate`（v2 对已建表的老库补 `ALTER TABLE join_decisions
  ADD COLUMN action`，并补 `idx_messages_quote_index`）；PostgreSQL 在 `pgSchema` 增加
  `join_decisions` / `join_topics` 两表、`action` 列与索引。新列有默认值，旧版本会忽略它。
- **没有新增消息状态**：`records.status` 的枚举不变；判定结果不写 `messages`，
  decision 只存截断后的消息预览，不存消息全文。引用识别只读 `messages.quote_index`。

### 0.1.1 修订速览（v1.7 → v1.8）：情绪状态（affect）

新增「按会话持久化的情绪状态」及其后台、API 与命令。**默认关闭**，关闭时回复管线、
润色、proactive 与现有接口响应**零变化**；没有修改任何既有字段或消息状态。

- **6 个配置键**（§3）：`affect_enabled`（默认 false）、`affect_pack`（默认 `daily`）、
  `affect_inject_mode`（默认 `line`）、`affect_event_classifier`（默认 `rules`）、
  `affect_tick_seconds`（默认 300，[60,1800]）、`affect_inject_max_tokens`
  （默认 120，[40,300]）。按群覆盖新增 `group_overrides[].affect`（`bool | null`）与
  `group_overrides[].affect_inject_mode`（`string | null`）。
- **7 组端点 / 9 个方法**（§6.7）：5 个 GET（packs / conversations / state / history / events）
  + 4 个控制 POST（pause / resume / preset / reset）。全部 protected；
  错误沿用标准信封与 §8 错误码。
- **5 个计数器**（§2）：`affect_ticks`、`affect_events`、`affect_injections`、
  `affect_inject_chars`、`affect_errors`，`counters` 由 **19 项变成 24 项**。
- **1 个管理员命令**：`/mood`（别名 `mood`），用法
  `/mood [status|pause 分钟|resume|preset 名称|reset]`；入站 `command`、出站 `replied`，
  不调模型、不写会话上下文。
- **1 个后台页**：新路由 `/affect`（nav「情绪状态」），保存走既有 `PUT /api/config`。
- **迁移**：SQLite `0013_affect`；PostgreSQL 在 `pgSchema` 增加 `affect_states` /
  `affect_events` / `affect_history` 三表与索引。三表都是新增表，旧版本会忽略它们。
- **没有新增消息状态**：`records.status` 的枚举不变；affect 不写 `messages`，
  事件日志只存事件 id / tone / source / 时间，不存消息原文。

### 0.1.2 修订速览（v1.6 → v1.7）：承诺可从后台取消

只新增**一个写端点**，`Commitment` 的字段与状态枚举**没有任何变化**：

- **新端点** `DELETE /api/commitments/{id}`（§6，protected）→ `{ "canceled": true }`。
  **只有 `waiting` / `leased` 可取消**；`done` / `cancelled` 是终态，返回
  **`409 conflict`**（不覆盖用户收口时写下的 `ack_note` / `ack_at`）；
  `id` 非法 → `400 invalid_request`；不存在 → `404 not_found`。
  取消写入 `status=cancelled`、`ack_note="后台取消"`、`ack_at=now`。
- **语义**：后台取消是运营兜底，不替代聊天里的 `ack_commitment` / `cancel_commitment`。
  取消后该行仍出现在 `GET /api/commitments` 里（它返回完整历史），只是状态变为 `cancelled`。
- **前端已对齐**：`CommitmentItem` 字段改为后端实际字段（`nudge_count` / `next_check_at` 等），
  状态中文标签为「等待追问 / 追问中 / 已完成 / 已取消」；取消按钮不再有 404/405 只读退化。
- **没有新增记录状态、没有新增配置键、没有 schema 改动**：取消只是既有行的一次 UPDATE。

### 0.1.3 上一轮修订速览（v1.5 → v1.6）：承诺追问与人类状态

新增两组后台可观测能力：**承诺（open loop）追问**与**人类状态感知**。

- **新端点** `GET /api/commitments`（§6，protected）：支持 `conversation_id` / `creator` /
  `limit`（默认 50，夹在 1–200），返回 `{items:[Commitment…], count:N}`，字段见 §7。
- **新计数器**（`GET /api/status` 的 `counters` 由 13 项变成 **19** 项）：
  `commitment_nudges`（成功发出的追问数）、`commitment_acked`（被用户以 done 收口的次数）、
  `commitment_postponed`（显式延后次数）、`human_status_runs`（状态推测尝试次数）、
  `human_status_failures`（其中模型/解析失败次数）、`human_status_image_calls`
  （图片分析工具被调用的次数，成功/失败都计）。
- **新配置键**：`commitment_enabled`（默认 true）、`commitment_max_open_per_member`
  （默认 10，[1,50]）、`human_status_enabled`（默认 true）、
  `human_status_trigger_messages`（默认 20，[5,200]）、`human_status_ttl_minutes`
  （默认 120，[10,1440]）、`human_status_timeline_messages`（默认 120，[20,400]）、
  `human_status_vision_enabled`（默认 true）、`human_status_vision_model`
  （默认空串 = 主模型）。
- **没有新增消息状态**：承诺追问是主动消息（写 `messages`，kind=manual），不做润色；
  `/human-status` 与其它命令一样入站 `command`、出站 `replied`、不写上下文，但会
  **同步调用一次模型**做状态推测（45s 超时，计入 `human_status_runs` /
  `human_status_failures`）。命令用法是 `/human-status [人数]`（缺省 10，1–200）：
  名单取自 `store.RecentSpeakers`（该会话 `created_at >= now-24h` 的 `speaker_openid`），
  所以**只分析过去 24 小时发过言的人**；名单为空时回提示、不调用模型，非法参数回用法文本。
- 承诺追问的**频率由模型判断**，代码只做 `>=60s` / `<=180d` 的技术防呆；
  **勿扰只认记忆**（不设代码静默时段），追问**原路返回**（群→群、私聊→私聊）。

### 0.1.4 历史修订速览（v1.4 → v1.5）：QQ 指令面板

把命令注册表投影成平台侧的「指令面板」，让用户点开就能看到命令。契约新增
**一个运维端点 + 一个配置键**；`records.status` 的枚举**没有任何变化**：

- **新端点** `POST /api/actions/sync-commands`（§8）：手动把注册表同步到 `group` / `c2c` 两个
  scope，返回 `{results:[{scope,panel_id,action,version}]}`；失败 `502 upstream_error`
  （`error.message` 是平台原文），调度器未初始化 `400 not_configured`，请求超时 25 秒。
- **新配置键** `command_panel_enabled`（bool，默认 **true**）：只控制**启动时的自动同步**
  （启动后 8s 起、30s/1m/2m/5m/10m 退避共 6 次，失败只记 WARN），手动动作始终可用。
  键在「接入与运维」页（§3 键表已归入该分组）。
- **没有新增消息状态**：面板同步不产生消息、**不写 `records`**；`/help` 与其它命令一样
  入站 `command`、出站 `replied`。§6 的状态表与 v1.4 逐字一致。
- 面板项的来源是注册表：`type=command`、`name` = `usage` 首词**去前导 `/`**（平台会剥掉 `/`：
  写 `/help` 读回 `help`，投影与回读必须同形，否则每次同步都判 `updated`）、`desc` = 注册表描述
  （≤30 rune）、`adminOnly` → `only_admin`；平台限制与排查见 README §5.13.7。
- 自动同步与手动动作走**同一条** `Dispatcher.SyncCommandPanel`（`panelMu` 串行化），
  所以两者不会各自读到旧版本再各写一次。

### 0.1.5 历史修订速览（v1.3 → v1.4）：Bot 命令（`/tokenstats`）与 `/help`

新增了「聊天内命令」这条**不经过模型**的路径。**没有新增端点、没有新增配置键**，
契约只多了一个审计状态值：

- `GET /api/messages` 的 `status` 新增 **`command`**：该入站消息被当作聊天命令处理
  （执行了命令、回了话，但**没有**进入对话管线）。出站行仍是 `replied` / `failed`。
  `status` 的 `received` / `replied` / `early_reply` / `ignored` / `failed` / `manual` 全部语义不变。
- `GET /api/messages?status=command` 可以直接筛出所有命令；`status=ignored` 里
  `error` 含「非管理员」的那些就是「有人试了管理命令但没权限」（静默拒绝，见下）。
- **命令不写会话上下文**，所以 `GET /api/conversations/{id}` 的时间线里**看不到**命令与它的回复 ——
  这是设计，不是丢数据。命令的唯一痕迹是审计表（`records`）与进程日志。
- 命令操作的是**既有全局键** `reply_footer_enabled`（`/tokenstats [on|off|status]`），
  与 `PUT /api/config` 写同一个键、走同一条 `settings.Apply` 路径，因此两边看到的 `editable` 值一致。
- 命令自身的口径：不计 `ai_requests`、不做润色、不施加人味延迟、不写 `messages` 表；
  入站 `status=command`、出站 `status=replied`，一对审计行都绑定同一条 `msg_id`。

### 0.1.6 历史修订速览（v1.2 → v1.3）：两阶段生成（润色）

一次回复变成「主模型草稿 → 无上下文润色 → 发送润色稿」，只动了三处，没有新增端点：

- `GET /api/status` 的 `counters` 由 11 项变成 **13** 项：新增 `polish_requests`（发出去的 stage 2 请求）
  与 `polish_fallbacks`（其中失败回退到草稿的次数）。
- `GET /api/config` 的 `editable` 新增 6 个键：`polish_enabled` / `polish_model` / `polish_temperature` /
  `polish_max_tokens` / `polish_timeout_seconds` / `polish_min_chars`（分组见 §3 的键表与语义段）。
- `GET /api/policies` 的 `group_overrides[]` 新增 `polish`（`bool | null`，`null` = 跟随全局）。

**润色失败不影响这一轮**：500 / 超时 / 空结果都回退发送草稿，审计状态仍是 `replied`。

### 0.2 存储双后端（v1.1 → v1.2）

存储从「只有 SQLite」变成「PostgreSQL 为主、SQLite 为回退」，**只动了两个只读块**，其余端点不变：

- `GET /api/status` 的 `store` 新增 `driver` / `target`；`path` 在 PostgreSQL 下**恒为空串**
  （不再是引导配置 `DB_PATH`）；`size_bytes` 在 PostgreSQL 下取自 `pg_database_size`。
- `GET /api/config` 的 `readonly` 由 `{db_path, version}` 改为 `{driver, storage, version}`：
  `storage` 在 PG 下是**遮蔽口令**的 DSN，SQLite 下是文件路径；**`db_path` 已删除**。
- 两个块里的口令一律是 `redacted`，接口不提供任何读回真实的途径。
- 迁移命令（`bot-harness migrate`）不是 HTTP 端点，见 [PG-MIGRATION.md](PG-MIGRATION.md)。

### 0.3 历史修订速览（v1 → v1.1）

- 认证：`POST /api/auth/logout` 是**公开**端点；补 cookie 的 `MaxAge` / `Secure` 语义。
- `GET /api/status`：删 `full_group_mode`；新增 `full_group_detected`、`platform`；
  `ai` 补 `base_url_set` / `api_key_set`；`qq` 补 `bot_id` / `bot_username` / `identity_error` /
  `openid_override_configured`；`counters` 补齐 11 项。
- `GET /api/config`：`readonly` 收窄为 `db_path` + `version`；`source` 增加 `overridden_keys`；
  （`db_path` 在 v1.2 已由 `driver` / `storage` 取代，见 §0.2）
  4 个 `*_set` / `*_source` 挪到顶层；`editable` 是**完整** Settings（三个秘密恒为空串）。
- `PUT /api/config`：响应是 `{settings, warnings, restart_required}`；
  重置语义是回退**可调配置**到内置默认值（不再是 `.env`），**三个凭据保留**。
- `GET/PUT /api/policies`：删 `full_group_mode` 与 `full_group_mode_required`；
  补 `context_scope` / `requested` / `platform` / `warnings`；覆盖项全部 `omitempty`。
- 会话列表 `sort` 收敛为 4 个枚举值（未知值按默认）。
- 会话详情：新增 `platform` 块与 `quote_reply` / `quote_reply_override` / `quote_reply_global`。
- 手动发送：错误表补 `404` / `502` / `503`；`message_row_id` / `record_id` 带 `omitempty`。
- 群状态：响应不再含 `group_openid`；`error` 省略；平台读取失败是 `502`。
- 媒体：ID 非 64 位十六进制 → `400`；成功时返回**裸二进制**，没有 JSON 信封。
- 消息记录：item 补 `conversation_id` / `delivery`；`status` 补 `manual`。
- 定时任务：item 字段表补齐（`creator_openid` / `attempts` / `lease_until` / `sent_at`…），
  `status` 补 `leased`。
- 长期记忆：列表 `{items, count}`、新建 `{id}`、删除 `{deleted: n}`；
  `kind` 固定 `fact`、`source` 固定 `admin`。
- 统计：`range` 只认 `1h | 6h | 24h | 7d`（其余按 24h），`bucket` ∈ `5m | 30m | 1h | 6h`，
  **只返回有数据的点，不补零**；`totals` 复用 `SeriesPoint`（`ts` 为空串）。
- 运维动作：`reconnect` / `test-ai` / `test-send` 的实际响应字段。
- 通用错误码表补 `502`。
- 分页：handler 先夹取再回显**实际生效值**（会话默认 20、消息默认 50，上限 200；`page < 1` 按 1）。
- SSE：契约事件为 `status` / `message` / `log` / `tool` / `ws` / `ping`。
  现在只是删掉了 `internal/admin/sse.go` 里与通用分支等价的 `ws` 特判 —— **`ws` 事件照旧发送**
  （`cmd/bot-harness/main.go` 中 `eventBus.Publish("ws", status)`，payload 为 `qq.WSStatus` 全字段）。
- 写接口脱敏：`PUT /api/config` 与 `reset-config` 的响应同样经 `maskSecrets`，三个秘密恒为空串。
- 主动额度：只统计 `addressed=0` 的旁观人类消息（迁移 `0010` 的 `messages.addressed`），
  `at_only` 且平台只推 @ 事件的群恒 0 额度。

## 1. 认证

会话凭证是 **HttpOnly cookie**，值是 `admin_token` 的 HMAC。因此在后台**改口令会让所有旧会话立刻失效**。
口令为空时**不签发也不接受会话**：`deriveSession()` 返回空串、cookie 分支直接拒绝；
空口令、纯空白口令与空 `Bearer` 一律 `401`（不存在「空等于空」的捷径）。

### `POST /api/auth/login` （公开）

请求：`{ "token": "<admin_token>" }`

响应 `200`：`{ "ok": true, "data": { "authenticated": true } }`，并设置：

```text
Set-Cookie: botharness_admin=<hmac>; Path=/; HttpOnly; SameSite=Lax; MaxAge=604800
```

`Secure` 在请求本身走 HTTPS，或反代声明 `X-Forwarded-Proto: https` 时设置。
**这信任反代头**，所以只在反代（会覆盖该头）之后部署；直连时浏览器只能凭请求本身的 TLS 判断。
`MaxAge` 为 7 天。

失败：

- `401` + `error.code = "invalid_token"`（口令不对；空体等同于空口令，也是 401）
- `400` + `error.code = "invalid_request"`（JSON 不合法或**含未知字段**）
- `429` + `error.code = "too_many_attempts"`（同一来源 IP 在 5 分钟内失败 5 次；
  内存实现，进程重启即清空；带 `Retry-After`。反代之后所有请求共享反代 IP，
  需要更细的限速时应在反代侧另做）

### `POST /api/auth/logout` （公开）

清除 cookie（`MaxAge=-1`），`200` + `{ "authenticated": false }`。无需已登录。

### `GET /api/auth/me` （公开）

前端用它判断是否需要登录。**始终 `200`**：

```json
{ "authenticated": false, "username": "admin", "auth_mode": "none" }
```

已登录时 `authenticated: true`、`auth_mode: "token"`。

> 除 cookie 外，也支持 `Authorization: Bearer <admin_token>`（curl / 脚本用）。

## 2. 运行状态

### `GET /api/status`

```json
{
  "ok": true,
  "data": {
    "version": "0.3.0",
    "started_at": "2026-07-21T10:00:00+08:00",
    "uptime_seconds": 3600,
    "transport": "websocket",
    "trigger_mode": "at_only",
    "context_mode": "per_member",
    "full_group_detected": true,
    "platform": {
      "known_groups": 2,
      "full_access_groups": [
        { "group_openid": "B2C3...", "group_name": "测试群",
          "recv_msg_setting": "all", "allow_proactive_msg": true }
      ],
      "limited_groups": [
        { "group_openid": "D4E5...", "group_name": "另一个群",
          "recv_msg_setting": "only_mention", "allow_proactive_msg": false }
      ]
    },
    "ws": {
      "enabled": true, "connected": true, "state": "ready",
      "session_id": "082ee18c-...", "shard": [0, 1],
      "gateway_url": "wss://api.bot.qq.com/websocket",
      "heartbeat_interval_ms": 45000, "latency_ms": 62,
      "last_event_at": "2026-07-21T10:59:58+08:00",
      "reconnects": 3, "resumes": 2, "last_error": null
    },
    "webhook": {
      "enabled": false, "path": "/qq/webhook",
      "received_total": 0, "verify_failures": 0, "last_event_at": null
    },
    "qq": {
      "app_id_masked": "1020****", "sandbox": false,
      "token_valid": true, "token_expires_in_seconds": 5100, "token_refreshes": 12,
      "last_token_error": null,
      "bot_id": "1234567890", "bot_username": "我的机器人",
      "identity_error": null, "openid_override_configured": false
    },
    "ai": {
      "base_url": "https://one-api.internal.example/v1", "base_url_set": true, "api_key_set": true,
      "model": "deepseek-chat", "in_flight": 1, "max_concurrent": 4,
      "requests_total": 812, "errors_total": 5, "avg_latency_ms": 2310, "last_error": null
    },
    "store": {
      "driver": "postgres", "target": "postgres://bot:redacted@postgres:5432/bot?sslmode=disable",
      "path": "", "size_bytes": 66852531,
      "conversations": 42, "messages": 3301
    },
    "counters": {
      "events_received": 900, "events_deduped": 12, "events_ignored": 40,
      "replies_sent": 801, "replies_failed": 7, "early_replies_sent": 30,
      "messages_observed": 55, "early_replies_recalled": 28,
      "ai_requests": 812, "ai_errors": 5, "tokens_used": 210000,
      "polish_requests": 790, "polish_fallbacks": 12,
      "commitment_nudges": 34, "commitment_acked": 21, "commitment_postponed": 9,
      "human_status_runs": 18, "human_status_failures": 1,
      "human_status_image_calls": 6,
      "affect_ticks": 288, "affect_events": 47, "affect_injections": 132,
      "affect_inject_chars": 9860, "affect_errors": 1,
      "join_gate_evals": 64, "join_gate_joins": 9, "join_gate_skips": 55,
      "join_gate_waits": 21, "join_gate_errors": 2, "join_gate_topics": 60
    }
  }
}
```

说明：

- `transport` / `trigger_mode` / `context_mode` 是**生效值**（全局层；按群覆盖的生效结果见 §4）。
- `full_group_detected`：自启动以来是否至少收到过一次 `GROUP_MESSAGE_CREATE`
  （即平台已为该机器人打开「接收所有消息」）。这是平台事实，不是配置。
- `platform` 只统计**已经读到过平台状态**的群；`recv_msg_setting` ∈
  `all | only_mention | mention_and_context`（其它值原样透传）。
- `ws.shard` 在未连接时是默认值 `[0, 1]`；`ws.state` 是协议状态原样透传。
- `ai.base_url` 是**当前生效**的端点（数据库设置），不是 `.env` 的值：
  `base_url_set` / `api_key_set` 告诉前端「配没配」，空串 = 未配置。
- `webhook.path` 是设置的 `qq_webhook_path`。
- `counters` 就是进程内计数器的全部 **30** 项（没有 `tool_calls` / `proactive_sent` 计数器）。
  其中 `polish_requests` / `polish_fallbacks` 属于两阶段生成：前者是**真的发出去**的 stage 2 请求数
  （润色被跳过——开关关、草稿为空、低于 `polish_min_chars`——不计），后者是其中失败回退到草稿的次数，
  因此恒有 `polish_fallbacks <= polish_requests`。两者都是 0 且回复正常 = 润色没开或被全部跳过。
  按群关掉润色的群不计入分子/分母（它们根本不发 stage 2 请求）。
- 后 5 项属于情绪状态：`affect_ticks` 是 tick 次数；`affect_events` 是识别并记录的事件数；
  `affect_injections` 是**非空**注入次数（档位 `off`、暂停、空状态不计）；
  `affect_inject_chars` 是注入块总字符数（用于估算预算）；`affect_errors` 是读 / 写 / 渲染失败次数。
  不新增按群维度，全部是进程内累计值，重启归零。
- 后 6 项属于自主加入：`join_gate_evals` 是**实际调用模型**的评估次数（闸门超限不计）；
  `join_gate_joins` 是 `action=join` 并进入回复管线的次数；`join_gate_skips` 是没有进入回复管线
  （预检超限、判定 `wait` / `skip`、评估失败）的次数；`join_gate_waits` 是其中模型判定
  为 `wait` 的次数，用来单独观察「等一等」的比例；`join_gate_errors` 是读库 / 模型 / 解析 /
  超时失败次数；`join_gate_topics` 是成功保存话题切分的次数。
  口径上 `join_gate_joins + join_gate_skips` 覆盖所有写下的 decision（`joined` 两个取值）；
  `join_gate_waits` 是 `skips` 的细分，因此 `waits <= skips`。
  同样不新增按群维度，重启归零。
- `store` 是**引擎无关**的一块，六项固定返回、含义随驱动变化：

  | 字段 | PostgreSQL | SQLite |
  |---|---|---|
  | `driver` | `"postgres"` | `"sqlite"` |
  | `target` | DSN，**口令已遮蔽为 `redacted`** | 数据库文件路径 |
  | `path` | **恒为空串** | 数据库文件路径（与 `target` 相同） |
  | `size_bytes` | `pg_database_size(current_database())`（当前库逻辑大小，不含 WAL 与其它库） | 主库文件 `os.Stat` 大小（不含 `-wal` / `-shm`） |
  | `conversations` / `messages` | 行数 | 行数 |

- `store.path` **不再来自引导配置 `DB_PATH`**：它的值来自实际打开的那个 handle，
  在 PostgreSQL 下没有"文件"这个概念，所以固定为 `""`（保留该键只为兼容老前端 ——
  DSN 复制一份到名为 `path` 的字段里会误导所有把它当路径显示的地方）。
  想知道程序到底连了什么，看 `store.driver` + `store.target`。
- `size_bytes` 探测失败（超时 3 秒、权限不足）**不会让接口失败**，只返回 `0`，前端渲染为「未知」。

### `GET /api/health` （公开，无信封，供 docker healthcheck）

`200` + `{"status":"ok"}`；未就绪时 `503` + `{"status":"degraded","reason":"missing credentials: …"}`。
未就绪的条件：凭证缺失（`qq_app_id` / `qq_app_secret` 为空）或尚未拿到有效的 access_token。

## 3. 配置

### `GET /api/config`

```json
{
  "ok": true,
  "data": {
    "source": {
      "file": ".env",
      "overrides_from_db": 3,
      "overridden_keys": ["model", "openai_base_url", "qq_app_id"]
    },
    "readonly": {
      "driver": "postgres",
      "storage": "postgres://bot:redacted@postgres:5432/bot?sslmode=disable",
      "version": "0.2.0"
    },
    "qq_app_secret_set": true,
    "admin_token_set": true,
    "openai_api_key_set": true,
    "openai_api_key_source": "db",
    "editable": { "…": "完整 Settings 文档，见下" }
  }
}
```

- `source.file`：本次启动加载的 dotenv 文件名（**当前实现固定返回 ".env"**）。
- `source.overrides_from_db`：数据库里**有记录**的键数量。
- `source.overridden_keys`：这些键的名单，**已按字母排序**。前端用它标注「已覆盖」。
- `readonly` 只剩**启动期决定、运行期改不了**的三项：

  | 字段 | 含义 |
  |---|---|
  | `driver` | 实际打开的存储引擎：`"postgres"` 或 `"sqlite"` |
  | `storage` | 存储位置。PostgreSQL 下是**口令已遮蔽**的 DSN（`postgres://bot:redacted@…`）；SQLite 下是数据库文件路径 |
  | `version` | 构建版本号（`-ldflags` 注入，Docker 下即 `VERSION`） |

  **`db_path` 已从本块删除**：它是 SQLite 的引导值，在 PostgreSQL 下毫无意义；
  原先的语义由 `driver` + `storage` 覆盖。QQ 接入与监听地址都已进数据库，也不再是只读。
- 三个秘密各自的 `*_set`（`qq_app_secret_set` / `admin_token_set` / `openai_api_key_set`）
  告诉前端「配没配」；`openai_api_key_source` 只有 `db` / `none` 两个值
  （密钥只来自数据库，`.env` 已不参与）。
- `editable` 是**完整的 Settings 文档**（80+ 键），仅 `openai_api_key` / `qq_app_secret` /
  `admin_token` 三个秘密恒为空串。它同时包含策略字段（`trigger_mode` 等）——
  前端日常只用 §4 的 `/api/policies`，但 `PUT /api/config` 实际可以改任意已知键。

`editable` 的键（按用途分组，键名即 JSON 字段名）：

| 分组 | 字段 |
|---|---|
| 提示词与模型 | `system_prompt`、`system_prompt_file`、`openai_base_url`、`openai_api_key`、`model`、`temperature`、`max_tokens`、`request_timeout_seconds`、`context_max_turns`、`context_max_tokens`、`history_ttl_hours` |
| 回复与人味 | `reply_max_chars`、`reply_max_segments`、`reply_footer_enabled`、`early_reply_enabled`、`early_reply_delay_seconds`、`early_reply_text`、`early_reply_recall`、`reply_to_bots`、`quote_reply`、`quote_reply_mode`、`reply_style_variants`、`reply_style_variant_probability`、`humanize_level`、`humanize_delay_scale`、`persona`、`markdown_enabled` |
| 看图与附件 | `vision_enabled`、`vision_max_images`、`media_max_bytes` |
| 能力开关 | `tools_enabled`、`tools_max_steps`、`tools_total_budget_seconds`、`tools_result_max_chars`、`memory_enabled`、`memory_inject_max`、`memory_max_per_scope`、`memory_extract_enabled`、`memory_extract_every`、`web_enabled`、`web_search_provider`、`web_search_base_url`、`exec_enabled`、`exec_timeout_seconds`、`exec_max_output_bytes`、`tasks_enabled`、`tasks_max_per_member`、`tasks_max_delay_hours` |
| 承诺追问 | `commitment_enabled`、`commitment_max_open_per_member` |
| 人类状态 | `human_status_enabled`、`human_status_trigger_messages`、`human_status_ttl_minutes`、`human_status_timeline_messages`、`human_status_vision_enabled`、`human_status_vision_model` |
| 情绪状态（affect） | `affect_enabled`、`affect_pack`、`affect_inject_mode`、`affect_event_classifier`、`affect_tick_seconds`、`affect_inject_max_tokens`（按群覆盖用 `group_overrides[].affect` / `affect_inject_mode`） |
| 自主加入（join_gate） | `join_gate_enabled`、`join_gate_mode`、`join_gate_model`、`join_gate_window`、`join_gate_confidence`、`join_gate_eval_cooldown_seconds`、`join_gate_wait_retry_seconds`、`join_gate_topic_dormant_minutes`、`join_gate_topic_closed_minutes`、`join_gate_min_interval_minutes`、`join_gate_max_per_hour`、`join_gate_max_per_day`（按群覆盖用 `group_overrides[].join_gate`） |
| 两阶段生成（润色） | `polish_enabled`、`polish_model`、`polish_temperature`、`polish_max_tokens`、`polish_timeout_seconds`、`polish_min_chars`（按群覆盖用 `group_overrides[].polish`） |
| 主动消息 | `proactive_level`、`proactive_ratio`、`proactive_max_credits`、`proactive_quiet_start`、`proactive_quiet_end`、`proactive_min_interval_minutes` |
| 接入与运维 | `qq_app_id`、`qq_app_secret`、`qq_sandbox`、`qq_transport`、`qq_intents`、`qq_shard_count`、`qq_webhook_path`、`qq_bot_openid`、`command_panel_enabled`、`admin_token`、`http_addr`、`tls_extra_ca_files`、`tls_insecure_verify`、`log_level`、`log_buffer_size` |
| 手动发送 | `manual_send_enabled`、`manual_send_per_minute`、`manual_send_per_conversation_qpm`、`manual_send_max_segments` |
| 策略 | `trigger_mode`、`context_mode`、`context_scope`、`group_allowlist`、`member_allowlist`、`admin_openids`、`group_overrides`、`blocked_keywords` |
| 并发与限流 | `rate_limit_user_qpm`、`rate_limit_group_qpm`、`group_cooldown_seconds`、`max_concurrent_llm` |

#### 配置语义（前端文案与后台行为的事实来源）

- **模型端点与密钥只来自数据库**：`openai_base_url` 是唯一来源，空串 = 未配置
  （模型调用直接返回 `ai: 模型端点未配置…`，不会偷偷用默认地址）。
  保存即生效（每次请求重新解析，末尾斜杠自动收敛），换端点后要重拉 `/api/models`。
  非法地址（非 http/https、缺 host）在 `PUT` 当场 `400`。
- **上下文预算与输出上限**（默认值与校验范围，`PUT /api/config` 越界即 `400`）：

  | 键 | 默认 | 范围 | 语义 |
  |---|---|---|---|
  | `context_max_tokens` | `256000` | `[256, 10000000]` | 喂给模型的历史 token 预算；10M 是预留上限，不是当前模型的真实窗口 |
  | `max_tokens` | `1024` | `[1, 10000000]` | 单次回复输出上限；默认刻意保持 1024，避免每次请求都向上游索要巨量输出而被拒绝 |

  历史裁剪预算 = `context_max_tokens - max_tokens`（从新到旧累加，超预算即停）。
- **两阶段生成（润色）**：`polish_enabled` 默认 **true**；主模型先产出草稿，再由一次
  **无上下文**的调用（只给「已解析的系统提示词 + 草稿」）改写成人设口吻，最后发送润色稿。
  `polish_model` 默认空串 = 复用 `model`；`polish_temperature` 默认 0.7（[0,2]）；
  `polish_max_tokens` 默认 0 = 自动（草稿估算 token ×1.5，下限 128，上限 `max_tokens`），
  显式上限取 **[1, 8192]**（越界或负数 `400`，与后台控件的 `min=0` / `max=8192` 一致）；
  `polish_timeout_seconds` 默认 20（[5,120]，实际还会被剩余请求预算截短）；
  `polish_min_chars` 默认 0（[0,2000]，草稿 rune 数低于它就跳过润色）。
  **失败一律回退草稿**：500 / 超时 / 空结果都不会让这一轮失败，审计状态仍是 `replied`。
  手动发送、主动消息、失败兜底文案与「让我想想」提前回复**不经过**润色。
  按群覆盖用 `group_overrides[].polish`（`bool | null`，`null` = 跟随全局；只覆盖开关，6 个参数是全局的）。
  落库的 assistant 消息、记忆抽取的 reply 与实际发送文本**都是润色稿**；页脚 token 是 stage1（含工具步）+ stage2 之和。
  观测：`counters.polish_requests` / `counters.polish_fallbacks`，以及每轮的 INFO 日志 `two-stage reply`。
- **`openai_api_key` 只进不出**：GET/PUT 的响应里恒为空串，用 `openai_api_key_set` /
  `openai_api_key_source` 判断；空值提交表示「保持现状」，清空需显式调用对应动作。
- **自发主动**：`proactive_level` ∈ `off` / `auto`（默认 `off`），按群覆盖用
  `group_overrides[].proactive`。`proactive_ratio` 默认 10（3–100，每 N 条人类消息换 1 点额度）；
  `proactive_max_credits` 默认 3（1–10）；`proactive_quiet_start` / `_end` 默认 23 / 8（可跨午夜）；
  `proactive_min_interval_minutes` 默认 10（1–240）。另有：最近 30 分钟必须有人说话、
  连续 2 次主动无人回应降级、每条都写审计（含模型给的「为什么现在说」）。
  **额度口径**是「上一次主动发言之后的**旁观人类消息**」：
  `role=user AND addressed=0 AND kind IN ('dialogue','observed')`。
  - 迁移 `0010` 给 `messages` 加了 `addressed` 列：存量行默认 `1`（视为已寻址，升级后不会拿旧的 @
    历史换额度），`kind=observed` 回填 `0`；全量模式下机器人也回过话的未 @ 对话同样是 `addressed=0`。
  - 因此 **`at_only` 且平台只推 @ 事件的群额度恒为 0**，自发主动自然关闭（D-034 的语义由代码保证）。
  - `silent_streak` 只是「主动发言后无人回应」的计数器；真正拦住「对着死群说话」的是**额度门**
    （没有新的人类消息就没有额度），两次连续无人回应只是额外防线。
- **定时提醒**：`tasks_enabled` 默认 true；`tasks_max_per_member` 默认 5（1–50）、
  `tasks_max_delay_hours` 默认 168（1–720）。按群覆盖用 `group_overrides[].tasks`。
  工具：`schedule_task`（text + `delay_seconds` 或 `at`）、`list_tasks`、`cancel_task`（只作用于当前会话/当前人）。
- **承诺追问（open loop）**：`commitment_enabled` 默认 **true**；`commitment_max_open_per_member`
  默认 **10**（1–50，**防误操作的每人未收口上限，不是追问频率限制**）。
  工具：`create_commitment`（text + `first_delay_seconds` 或 `at`，priority 0–3，可选 `deadline_at`）、
  `list_commitments`、`cancel_commitment`、`ack_commitment`（outcome = done / postpone / cancel；
  postpone 的 `remind_after_seconds` 缺省时按 **4 小时**兜底）。
  **频率由模型判断**（代码只强制两次追问相差 ≥60 秒、≤180 天）；
  **勿扰只认记忆**（不设代码静默时段），追问**原路返回**（群→群、私聊→私聊）。
  模型决策失败时用模板文案 + **3 小时**保守间隔继续，不会卡死。
  观测：`counters.commitment_nudges` / `commitment_acked` / `commitment_postponed`。
- **人类状态感知**：`human_status_enabled` 默认 **true**；`human_status_trigger_messages`
  默认 **20**（5–200，每累计 N 条新消息异步推测一次）；`human_status_ttl_minutes`
  默认 **120**（10–1440）；`human_status_timeline_messages` 默认 **120**（20–400，
  每次推测取多少条消息构建成员时间线）。图片工具：`human_status_vision_enabled`
  默认 **true**，视觉模型 `human_status_vision_model` 默认空串（= 主模型）。
  推测结果只作为回复语气与追问时机的参考，不写进用户可见消息；
  `/human-status [人数]`（管理员）会**立即同步分析一次**再展示结果，不受 `human_status_enabled`
  限制；人数缺省 10、允许 1–200，且只分析过去 24 小时在本会话发过言的人（名单为空
  直接回提示、不调用模型）。失败时回退到未过期缓存。推测输出预算下限 2000 tokens，解析容忍 markdown 围栏、
  数组前后解释文字与被截断的数组（保留已解析对象）；完全解析不出对象时**追加一条合约消息
  重试一次**（不新增 counter，重试的 token 用量见日志 `human status JSON retry`）。
  观测：`counters.human_status_runs` / `human_status_failures` /
  `human_status_image_calls`。
- **情绪状态（affect）**：`affect_enabled` 默认 **false**；关闭时回复、润色与主动消息零变化。
  `affect_pack` 默认 `"daily"`（当前唯一内置包；未知 id 运行期回退 `daily`）；
  `affect_inject_mode` ∈ `off` / `line` / `full`（默认 `line`）；
  `affect_event_classifier` ∈ `off` / `rules`（默认 `rules`）；
  `affect_tick_seconds` 默认 300（60–1800）；`affect_inject_max_tokens` 默认 120（40–300），
  只约束 `full` 档（`line` 固定 ≤25 tokens）。按群覆盖用 `group_overrides[].affect`
  （`bool | null`）与 `group_overrides[].affect_inject_mode`（`string | null`）；
  **全局 `affect_enabled` 是总开关**，群覆盖只能进一步关闭或调档，不能越过全局关；
  `false` 是可靠关闭。状态只影响表达，不影响事实、工具与安全策略；
  默认关是刻意的（见 D-047）。完整设计见
  [AFFECT.md](AFFECT.md)。
- **自主加入（join_gate）**：`join_gate_enabled` 默认 **false**；关闭时未 @ 群消息只记录、
  不评估、不写表。`join_gate_mode` ∈ `off` / `llm`（默认 `llm`；`off` 表示不进入 join gate，未 @ 消息仍走原 trigger gate）；
  `join_gate_model` 默认空串（= 主模型）；`join_gate_window` 默认 20（5–50）；
  `join_gate_confidence` 默认 0.7（0–1）；`join_gate_eval_cooldown_seconds` 默认 60（10–3600）；
  `join_gate_wait_retry_seconds` 默认 30（10–300，上次判定 `wait` 时满这个间隔可绕过评估冷却
  提前重评估；仍需该群有新消息）；`join_gate_min_interval_minutes` 默认 5（1–240）；
  `join_gate_max_per_hour` 默认 3（1–30）；`join_gate_max_per_day` 默认 12（1–100）；
  `join_gate_topic_dormant_minutes` 默认 20（5–240）、`join_gate_topic_closed_minutes`
  默认 180（30–1440，且必须大于 dormant 阈值），两者只做**单向年龄降级**：
  `active` 超时变 `dormant`，任何非 `closed` 话题超时变 `closed`；时间不会把结束的话题升回来。
  按群覆盖用 `group_overrides[].join_gate`（`bool | null`）；**全局 `join_gate_enabled` 是总开关**，
  群覆盖只能进一步关闭，不能越过全局关。判定（`action` = `join` / `wait` / `skip` +
  `addressed_to`）与话题切分（含 `status` / `resumable`）在同一次 LLM 调用里完成；
  解析失败 / 超时 / 非法 JSON / 非法 `action` / 非法 `status` 一律 fail-closed（按 `skip` 记录）。
  引用机器人（`ref_msg_idx` 命中本群 assistant 消息的 `quote_index`）视为 @，不进入闸门。
  完整设计见 [JOIN-GATE.md](JOIN-GATE.md)。
- **人味**：`humanize_level` ∈ `off` / `soft` / `medium` / `strong`（默认 `medium`，空或未知值按 `medium`）。
  `humanize_delay_scale` 默认 1（0.5–3），乘在所有停顿上；`persona` 是一句话人设，附加到系统提示词。
  按群覆盖用 `group_overrides[].humanize`。

  | 档 | 基础停顿 | 每 10 字 | 上限 | 分段间隔 | 未 @ 沉默概率 |
  |---|---|---|---|---|---|
  | off | 0 | 0 | 0 | 0 | 0 |
  | soft | 0.4s | 0.25s | 3s | 0.25s | 0 |
  | medium | 0.8s | 0.6s | 8s | 0.65s | 10% |
  | strong | 1.5s | 0.9s | 15s | 1.1s | 25% |

  停顿按**整轮耗时**计算（目标值 − 模型已花时间）；@机器人 的消息永远回复，
  沉默只作用于群里未被 @ 的普通消息（E2 全量模式下）。
- **口吻轮换（备用表达风格）**：`reply_style_variants` 是一组一句话风格
  （如「用 1–2 个字回复」「带一点翻译腔」），每条 ≤200 字、最多 8 条，提交时去空白去重；
  空数组 = 不启用。`reply_style_variant_probability` ∈ [0,1]（默认 0.25）是**每轮**应用其中
  一条的概率。命中时该条以「本轮说话方式：…」追加到 **stage 1 的系统提示词**（人味之后），
  同一轮的润色阶段复用同一份提示词，所以不会出现草稿一种口吻、润色稿另一种；未命中时
  提示词与关闭该功能逐字节一致。它只影响表达，不改润色阶段的硬约束（不许加事实等）。
- **引用回复的档位**：`quote_reply_mode` ∈ `never`（0%）/ `occasionally`（30%，默认）/
  `often`（70%），每轮按概率决定这一条正式答复是否引用触发消息。旧键 `quote_reply`
  （布尔）继续兼容：写 `true` = `occasionally`、`false` = `never`，两个字段在每次保存后
  同步（`mode != never` ⇔ 布尔为 true）；老库里只有布尔时按布尔映射。按群覆盖仍是布尔
  `group_overrides[].quote_reply`：`false` = 该群 never，`true` = 该群 occasionally
  （粒度不到 often），`null` = 跟随全局档位；**全局 `never` 时群 `true` 也打不开**。
  只有正式答复经过这个判定：提前回复、错误兜底、后台手动发送的显式引用都不抽签。
- **联网**：`web_enabled` 默认 true。`web_search_provider` 取 `"searxng"` 或空（空 = 只有 `web_fetch`）；
  `web_search_base_url` 是 SearxNG 根地址（请求 `{base}/search?format=json`；默认指向内置
  sidecar `http://searxng:8080`）。`web_search` 工具接受 `limit`（1–10，默认 5）；后端 403（未启用
  json）、429（限流）、非 JSON 响应都有明确报错文案，`unresponsive_engines` 作为结果尾部 warning。
  按群覆盖用 `group_overrides[].web`。
  安全语义（搜索后端 origin 窄口）：只放行 `web_search_base_url` 解析出的**精确 `ip:port`**
  （scheme / host / port 全匹配）与公网地址；其它私网 / 回环、同 IP 其它端口一律拒绝；
  跨 origin 重定向必须过「只允许公网」校验（跳公网允许，跳私网才拒）；
  后端解析到 link-local 或云元数据别名（`169.254.169.254` / `100.100.100.200` / `fd00:ec2::254`）
  时在建 client 阶段即失败。搜索 client **不使用 `HTTP(S)_PROXY`**（sidecar 直连）。
  普通出站会放行 `HTTP(S)_PROXY` 解析出的精确 `ip:port`，目标仍由 pre-flight 与每跳重定向校验；
  **残余风险**：代理侧自行解析目标 DNS，DNS rebinding 对 pre-flight 不可见 —— 不要把代理指向不可信网络，
  内网端点放进 `NO_PROXY` 直连。
- **容器内执行**：`exec_enabled` 默认 false；`exec_timeout_seconds` 默认 15（3–60）、
  `exec_max_output_bytes` 默认 8192（512–65536）。按群覆盖用 `group_overrides[].exec`。
- **记忆**：`memory_enabled` 默认 true；`memory_inject_max` 默认 6（1–20）；
  `memory_extract_enabled` 默认 false（每 `memory_extract_every` 轮多一次模型调用，
  `memory_extract_every` 默认 10，1–50）；`memory_max_per_scope` 默认 300（20–5000，达上限淘汰权重最低的一条）。
  按群覆盖用 `group_overrides[].memory`。关闭后既不声明 `remember`/`recall`，也不再把事实注入提示词
  （已有记忆仍保留）。
- **工具**：`tools_enabled` 默认 true；`tools_max_steps` 默认 6（1–12）、
  `tools_total_budget_seconds` 默认 100（10–300）、`tools_result_max_chars` 默认 8000（500–20000）。
  按群覆盖用 `group_overrides[].tools`。关闭时不声明任何工具；
  首次带工具请求失败会自动降级为不带工具重试一次（只打 WARN）。
- **Markdown**：`markdown_enabled` 默认 true，回复以 `msg_type=2` 发送（`content` 留空）。
  平台需要单独申请模板权限（`304036` / `40034127`）；缺权限时自动降级纯文本并停放 24 小时。
- **看图**：`vision_enabled` 默认 true、`vision_max_images` 默认 2；当前消息图片只在**本轮**以
  `data:` URI 发给模型，历史 / 引用 / 合并图片不重发；模型可通过 `view_image`（`media_id`，
  可选 `question`）按需回看，复用 `human_status_vision_model`（空 = 主模型）。
  视觉未启用 / id 失效 / 视觉调用失败都只返回文本、不阻断回复；模型不支持图片时退回纯文本重试一次。
  来源、标注、失败降级与隐私边界见 [VISION.md](VISION.md)。
- **手动发送**：`manual_send_enabled` 默认 true、`manual_send_per_minute` 默认 20、
  `manual_send_per_conversation_qpm` 默认 6、`manual_send_max_segments` 默认 5。
  手动发送占用该群的 QPM 额度，并消耗平台主动消息配额。
- **指令面板**：`command_panel_enabled` 默认 **true**，位于「接入与运维」页（与「立即同步」按钮同一张卡），
  只控制**启动时的自动同步**（群聊 `group` + 私聊 `c2c`，启动后 8s 起、退避重试，失败只记 WARN、
  不影响启动）；关闭后 `POST /api/actions/sync-commands` 仍可手动触发。
  **命令本身与面板无关**：关掉开关照样能在聊天里用命令，面板坏了也不影响命令执行。
  平台限制（面板 ≤ 20 个/机器人、项 ≤ 20/面板、`name` ≤ 14 / `desc` ≤ 30 字符、写 10 QPM / 读 30 QPM）
  见 README §5.13.7；同步只碰 `remark=bot-harness:commands` 的面板，且**永不删除**。
- **管理员身份**：`admin_openids` 建议填 `user_openid`（应用级、跨群稳定）；填 `member_openid`
  只在该群生效。留空 = **没有任何管理员**，`run_shell` / `settings_read` / `settings_write`
  对所有人关闭（工具定义也不下发，工具内部再校验一次，审计记 `rejected`）；
  `run_shell` 对空名单额外返回 `ErrNoAdminsConfigured`（显式错误），且可用性还要同时满足
  全局 `exec_enabled`、该群未被 `.exec` 关闭、`tools_enabled` 打开。
  后两者的两个例外（`openai_api_key`、`admin_openids` 不可经对话修改）也是刻意的：
  密钥不通过聊天传递，提权入口不能由一条被注入的消息打开。
- **`run_shell` 在容器内执行**：工作目录每次重建并清空、环境变量只保留 `PATH/HOME/LANG/TZ`、
  `ulimit -t/-f/-u/-v` 限制、超时对整个进程组 SIGKILL、输出截断。
  `web_fetch` / `web_search` 走 SSRF 防线：只允许 http/https、拒绝用户名密码、
  解析后逐个校验 IP（回环 / RFC1918 / 链路本地 / 云元数据 / CGNAT），并在真正建连的 socket 上再校验一次
  （防 DNS rebinding）；重定向 ≤3 次且逐跳校验。
- **程序只读数据库位置**：`DATABASE_URL`（PostgreSQL DSN，**非空即优先**）、`DB_PATH`（SQLite 回退）、
  可选的 `ENV_FILE`。`.env` 另有运维键（代理、`ADMIN_BIND`、`VERSION`、`SEARXNG_SECRET`、
  `POSTGRES_PASSWORD`）只给 compose / 构建 / sidecar / PG 容器用，不再承载业务配置。升级时
  `SeedFromEnv` 会把旧 `.env` 的值一次性导入数据库（只导入数据库尚无记录的键），之后 `.env` 不再生效。
  注意 `DATABASE_URL` 与 `POSTGRES_PASSWORD` **不**是「配置项」：它们决定的是数据库在哪、用什么口令进去，
  只能来自环境；其中 `DATABASE_URL` 里的口令在 `readonly.storage` 与日志里恒为 `redacted`。

### `PUT /api/config`

请求体是上述 `editable` 的**部分字段**（一个 JSON 对象；省略即不改）。

响应 `200`：

```json
{
  "ok": true,
  "data": {
    "settings": { "…": "更新后的完整 Settings（三个秘密按只进不出的规则处理）" },
    "warnings": [],
    "restart_required": []
  }
}
```

- **`restart_required` 当前恒为 `[]`**：需要重启的字段（`http_addr`、`tls_*`、`log_buffer_size`）
  由前端自己提示，接口不再尝试推断。其余字段保存即生效。
- `warnings` 当前恒为 `[]`（平台权限相关的提示只出现在 `/api/policies`）。
- 校验失败、类型错误、未知键、空体一律 `400` + `error.code = "invalid_request"`：
  - 空体：「请求体为空」
  - `{}`：「没有任何要修改的字段」
  - 未知/只读键：`unknown or read-only field "xxx"`
  - 值非法：`field "xxx": …` 或范围校验消息
- **重置语义**：`POST /api/actions/reset-config` 把**可调配置**全部退回内置默认值
  （`settings.Defaults()`；不是 `.env`，`.env` 已不参与运行时配置），但**保留三个凭据**：
  `admin_token` / `openai_api_key` / `qq_app_secret` 不是覆盖项，重置不清空、旧登录会话仍然有效。
  - 模型端点不是凭据 → 重置后为 `""`（未配置），模型名回到 `gpt-4o-mini`，`warnings` 会提示重填；
    API Key 通常被保留，只有「从未配置过」时才一起出现在提示里。
  - 口令**此前为空**（从未生成过）时，重置会生成一个 `botharness-…` 新口令并持久化，
    `warnings` 提示用 `bot-harness token` 读取（此时旧会话失效）。

> **三个秘密在所有响应里只进不出**：`PUT /api/config` 与 `POST /api/actions/reset-config`
> 返回的 `settings` 都经同一个 `maskSecrets` 处理，`openai_api_key` / `qq_app_secret` /
> `admin_token` 恒为空串；配没配看对应的 `*_set`。

### `GET /api/models`

代理上游 OpenAI 兼容端点的 `GET {base_url}/models`，供后台下拉选择。

```json
{
  "ok": true,
  "data": {
    "base_url": "https://one-api.internal.example/v1",
    "count": 41,
    "models": [{ "id": "MiniMax-M2" }, { "id": "deepseek/deepseek-v4.1-flash" }],
    "ids": ["MiniMax-M2", "deepseek/deepseek-v4.1-flash"],
    "current": "deepseek/deepseek-v4.1-flash",
    "current_in_list": true,
    "cached": false,
    "fetched_at": "2026-07-21T10:00:00+08:00"
  }
}
```

- `models[].id` 按**升序**排列且已去空；`ids` 是同一顺序的纯 id 数组。
- 上游返回 `{"data":[…] }`、`{"models":[…] }`、`{"data":["a","b"]}` 或裸数组都被接受。
- 服务端缓存 5 分钟；`?refresh=1` 强制刷新。
- `base_url` 是**当前实际生效**的端点（数据库设置）。
- 错误：端点/密钥未配置 → `502 upstream_error`（消息里指路去后台填写）；
  上游不可达或结构无法识别 → `502 upstream_error`；内部未装配 AI 客户端 → `400 not_configured`。

## 4. 策略

### `GET /api/policies`

```json
{
  "ok": true,
  "data": {
    "trigger_mode": "at_only",
    "context_mode": "per_member",
    "context_scope": "addressed_only",
    "requested": { "trigger_mode": "at_only", "context_mode": "per_member" },
    "platform": {
      "known_groups": 2,
      "full_access_groups": [ { "group_openid": "…", "group_name": "…", "recv_msg_setting": "all", "allow_proactive_msg": true } ],
      "limited_groups": []
    },
    "group_allowlist": [],
    "member_allowlist": [],
    "admin_openids": [],
    "group_overrides": [
      { "group_openid": "B2C3...", "enabled": true,
        "trigger_mode": "all_messages", "context_mode": "shared",
        "context_scope": "include_unaddressed", "reply_to_bots": false,
        "quote_reply": true, "tools": true, "proactive": "auto", "tasks": true,
        "memory": true, "humanize": "medium", "web": true, "exec": false,
        "polish": false, "affect": true, "affect_inject_mode": "line",
        "join_gate": true, "rate_limit_group_qpm": 30 }
    ],
    "blocked_keywords": ["政治", "违法"],
    "reply_to_bots": true,
    "quote_reply": true,
    "quote_reply_mode": "occasionally",
    "warnings": []
  }
}
```

- `trigger_mode` / `context_mode` / `context_scope` 是**生效值**（全局层，不含按群覆盖）；
  `requested` 里是**存库的原值**（当前实现两者相同：不再有任何「权限闸门」改写配置）。
- `platform` 与 `/api/status` 的同名字段一致：平台逐群的真实接收设置。
- `warnings` 是字符串数组：把「模式选择 vs 平台事实」的差异讲清楚，**从不修改你的配置**。
  典型内容：`trigger_mode=all_messages` 但某些群平台侧仍是「仅 @机器人」；
  `context_scope=include_unaddressed` 但某些群生效的上下文模式是 C1；还没读到任何群状态。
- `group_overrides[]` 的字段全部 `omitempty`：`enabled`、`trigger_mode`、`context_mode`、
  `context_scope`、`reply_to_bots`、`quote_reply`、`tools`、`proactive`、`tasks`、`memory`、
  `humanize`、`web`、`exec`、`polish`、`affect`、`affect_inject_mode`、`join_gate`、
  `rate_limit_group_qpm`。
  **缺省 / `null` = 跟随全局**，覆盖优先级高于全局值（**affect / join_gate 例外**：
  全局总开关，见下）。
- `group_overrides[].quote_reply` 是**引用回复**的按群开关（`bool | null`），它只有布尔粒度，
  映射到全局的三档 `quote_reply_mode`：`false` = 本群 `never`、`true` = 本群 `occasionally`、
  `null` = 跟随全局档位。**全局 `never` 是天花板**：此时群里的 `true` 也打不开引用
  （群覆盖只能收窄）。详见上文「引用回复的档位」。
- `group_overrides[].polish` 是**两阶段生成**的按群开关（`bool | null`）：`true` 强制开、`false` 强制关、
  `null` = 跟随全局 `polish_enabled`。它只覆盖**开关**；6 个 `polish_*` 参数是全局的，没有按群版本。
  保存时必须原样透传（前端 `toGroupOverridePayload` 不做白名单重建），否则改一行 QPM 会把别的群的润色设置抹掉。
- `group_overrides[].affect` 是**情绪状态**的按群开关（`bool | null`）：`null` 跟随全局、
  `false` 强制关闭、`true` 显式开启。**全局 `affect_enabled` 是总开关**：全局关时所有群
  都不生效，群里的 `true` 不能越过；全局开启后群 `false` 是可靠的紧急关闭。
  `affect_inject_mode` 覆盖该群档位（`off` / `line` / `full`；`null` = 跟随全局）。
  这两项同样必须原样透传；其余 4 个 `affect_*` 键只有全局值，没有按群版本。
- `group_overrides[].join_gate` 是**自主加入**的按群开关（`bool | null`）：`null` 跟随全局、
  `false` 强制关闭、`true` 显式开启。**全局 `join_gate_enabled` 是总开关**：全局关时所有群
  都不生效，群里的 `true` 不能越过；全局开启后群 `false` 是可靠的紧急关闭。
  它同样必须原样透传；其余 11 个 `join_gate_*` 键只有全局值，没有按群版本。
- 空数组表示**不限制**（白名单 / 屏蔽词）。
- `trigger_mode` ∈ `at_only`（E1）| `all_messages`（E2）；
  `context_mode` ∈ `per_member`（C1）| `shared`（C2）；
  `context_scope` ∈ `addressed_only` | `include_unaddressed`。

### `PUT /api/policies`

接受 `Settings` 的任意已知键子集（策略页只发策略字段），返回**与 GET 相同的完整对象**。
空体、空对象、未知键、非法值 → `400 invalid_request`。
**没有** `full_group_mode_required` 这类闸门：平台权限不足只在 `warnings` 里说明，不改配置。

## 5. 会话与上下文

### `GET /api/conversations`

查询参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `q` | — | 模糊匹配群名 / 成员名 / 群 openid / 成员 openid |
| `scene` | — | `group_member` / `group_shared` / `c2c` |
| `enabled` | — | `true` / `false` |
| `page` | 1 | `< 1` 按 1 |
| `page_size` | 20 | 只能 1–200；越界时按 **20** 取数，响应里回显的是**实际生效值** |
| `sort` | `last_active_desc` | `last_active_desc` / `last_active_asc` / `messages_desc` / `created_desc`，其余值按默认 |

```json
{
  "ok": true,
  "data": {
    "total": 42, "page": 1, "page_size": 20,
    "items": [
      {
        "id": "Z3JvdXA6QjJDMw",
        "scene": "group_member",
        "group_openid": "B2C3D4...", "group_name": "测试群",
        "member_openid": "A1B2C3...", "member_name": "小明",
        "enabled": true, "message_count": 24, "turn_count": 12,
        "token_estimate": 3120,
        "last_active_at": "2026-07-21T10:59:00+08:00",
        "created_at": "2026-07-20T09:00:00+08:00"
      }
    ]
  }
}
```

> `page` / `page_size` 在 handler 里就用同一套边界夹取，再原样回显：
> 面板显示的页大小永远等于真正取数的页大小（不会出现「显示 500 条、实际 200 条」）。

### `GET /api/conversations/{id}?limit=200&before_id=`

在上面字段基础上追加 `platform`、`active_today`、引用回复三项与 `messages`：

```json
{
  "platform": {
    "member_openid": "BOT-IN-GROUP", "recv_msg_setting": "all",
    "allow_proactive_msg": true, "member_role": "member",
    "joined_at": "…", "fetched_at": "…"
  },
  "active_today": 12,
  "quote_reply": true,
  "quote_reply_override": null,
  "quote_reply_global": true,
  "messages": [
    { "id": 3301, "role": "user", "content": "...", "tokens": 12, "kind": "dialogue",
      "speaker": "小明", "speaker_openid": "A1B2...", "quote_index": "REFIDX_…",
      "attachments": [ { "kind": "image", "media_id": "9f2c…", "content_type": "image/png" } ],
      "created_at": "..." },
    { "id": 3302, "role": "assistant", "content": "...", "tokens": 88, "kind": "manual",
      "speaker": "", "created_at": "..." }
  ]
}
```

- `platform`：群会话附带平台事实（可能为 `null` —— 私聊、或还没读到）。字段同 §5 群状态。
- `messages` 默认最近 200 条（`limit` 夹在 1–500），`before_id` 向上翻页；**返回按时间正序**（id 升序）。
- `messages[].kind` ∈ `dialogue` / `observed` / `observed_bot` / `manual`；`manual` 表示后台手动发出。
- `speaker` 是显示昵称（可能与真实身份重名），`speaker_openid` 才是身份（`omitempty`）；历史数据可能没有后者。
- `quote_index` 是该条消息的平台引用索引（`REFIDX_…`），有值才能被引用回复；升级前的老消息为空。
- `attachments` 只在有附件时出现（`omitempty`），字段见 §5.6。
- `active_today` 是该群**今天已发出的主动消息条数**（手动发送 + 机器人的主动兜底共用平台配额；私聊恒为 0）。

### `POST /api/conversations/{id}/messages` — 以机器人身份手动发送

请求：

```json
{ "content": "今晚 8 点开会，记得来", "quote_message_id": 3388 }
```

`quote_message_id` 可选，填**本会话里某条消息的行 id**（`messages[].id`）即以引用回复的形式发出。
服务端把它解析成平台的引用索引，客户端无法直接塞索引；跨会话的行 id 会被拒绝。

响应 `200`（即 `core.ManualSendResult`）：

```json
{
  "message_ids": ["MSG-1"], "message_id": "MSG-1", "segments": 1,
  "latency_ms": 412, "active": true, "quoted": true,
  "message_row_id": 3391, "record_id": 8801, "active_today": 13
}
```

`message_row_id` / `record_id` 带 `omitempty`（写库失败时可能缺省，见下文「先平台、后落库」）。

语义与边界：

- **永远是主动消息**（没有可回复的 `msg_id`）：需要该群已开启主动消息权限，并消耗平台配额
  （未认证 30 条/分钟、单群 1000 条/天）。发送前先查一次群状态，权限不足直接拒绝。
- 内容超过 `manual_send_max_segments` 段时**拒绝**（`400 content_too_long`），绝不静默截断。
- 成功后写入会话时间线（`kind=manual`、`role=assistant`）与审计表（`status=manual`、
  `delivery=active`），并推 SSE `message` 事件，所以打开着的会话详情页会自动刷新。
  因为它是机器人的真实发言，**会进入模型上下文**。
- 失败也会留一条 `status=failed` 的审计记录（保留原文与平台原始错误），但不会写进时间线。

| 状态 | code | 说明 |
|---|---|---|
| 400 | `invalid_request` | JSON 不合法 / 含未知字段 |
| 400 | `empty_content` | 内容为空（空白字符也算空） |
| 400 | `content_too_long` | 超出 `manual_send_max_segments` 分段上限 |
| 403 | `manual_send_disabled` | 设置里关掉了 `manual_send_enabled` |
| 404 | `not_found` | 会话不存在 |
| 409 | `no_proactive_permission` | 该群未开启主动消息权限 |
| 409 | `rejected_by_platform` | 被禁言 / 非群成员 / 内容违规 / 不允许发链接 |
| 409 | `no_target` | 会话缺少群或用户 OpenID，或引用了别的会话的消息 |
| 409 | `quote_disabled` | 该群已关闭引用回复（会话详情里可打开） |
| 409 | `quote_unavailable` | 想引用的那条消息没有平台引用索引 |
| 429 | `rate_limited` | 命中本地限流或平台主动消息频控 |
| 502 | `upstream_error` | 平台调用失败（未归类到上面任何一类的错误） |
| 503 | `unavailable` | 调度器尚未就绪（极早期启动阶段） |

### `GET /api/groups/{group_openid}/state?refresh=1` — 平台侧的群权限事实

```json
{
  "member_openid": "BOT-IN-GROUP",
  "recv_msg_setting": "all",
  "allow_proactive_msg": true,
  "member_role": "member",
  "joined_at": "…",
  "fetched_at": "…"
}
```

- 按群缓存（最长 30 分钟）；`refresh=1` 绕过缓存强制重读（会话详情页的刷新按钮用它）。
- `error` 带 `omitempty`，只在最近一次读取失败时出现（此时其余字段可能是旧缓存）。
- 错误：QQ 客户端未配置 → `400 not_configured`；平台读取失败 / 群不存在 / 未授权 → `502 upstream_error`。

### `GET /api/groups/names?openids=a,b,c` — 批量群名（v2）

策略页与 `/join` 页用群 openid 做键，但人只认群名；本地会话表已经缓存了平台给的群名，
这个端点一次把一批 openid 解析成名字，避免前端逐行发请求。

| 参数 | 必填 | 说明 |
|---|---|---|
| `openids` | 是 | 逗号分隔，去空白、去重；解析后 **1–100 个**，超出 → `400 invalid_request` |

```json
{
  "ok": true,
  "data": {
    "names": { "B2C3...": "测试群", "D4E5...": "" }
  }
}
```

- **未知群也出现在 map 里，值为空串**（不是缺键）：前端统一用「有名字显示名字、否则显示 openid」，
  不必为缺键分支。
- 名字来自本地 `conversations.group_name`（平台在首次读到该群时写入）；**从未见过、
  或平台没给名字**的群就是空串，接口不会为此去调平台。
- 空 `openids`、全是空白、字段缺失 → `400 invalid_request`；存储未接线 → `503 unavailable`。
- 只读、无副作用；名字本身不是敏感信息，但仍要求认证（属于后台页面数据）。

### `POST /api/conversations/{id}/quote-reply` — 按群开关「引用回复」

请求 `{ "enabled": false }`（`enabled: null` 表示清掉覆盖、跟随全局）。

也可以传档位：`{ "mode": "never" }` 等价于 `{"enabled": false}`，
`{"mode": "occasionally"}` / `{"mode": "often"}` 等价于 `{"enabled": true}`（按群覆盖只有布尔粒度，
`often` 不会把该群单独抬到 70%，仍是全局档位）；非法 `mode` → `400 invalid_request`。

响应：

```json
{ "group_openid": "…", "quote_reply": false, "quote_reply_mode": "never",
  "override": false, "global": true, "global_mode": "occasionally" }
```

- 只有群会话可以设置：私聊返回 `409 no_group`（跟随全局默认）。
- 会话不存在 → `404 not_found`。
- 这个开关存在策略文档的 `group_overrides[].quote_reply` 里，所以策略页的覆盖表格同样能看到并修改它；
  `quote_reply_mode` 是**生效值**（跟随全局 + 本群覆盖解析后），不是存储值。

### 其它会话操作

| 方法 / 路径 | 请求 | 响应 |
|---|---|---|
| `DELETE /api/conversations/{id}` | — | `{ "cleared": true }`；清空消息、保留会话元数据；`404 not_found` |
| `POST /api/conversations/{id}/enabled` | `{ "enabled": false }` | `{ "id": "…", "enabled": false }`；缺 `enabled` → `400`；`404 not_found` |
| `DELETE /api/conversations?confirm=CLEAR_ALL` | — | `{ "cleared": true }`；缺 `confirm=CLEAR_ALL` → `400 invalid_request` |

## 5.5 会话合并（同为群内，绝不跨群）

把同一个群里分散的成员会话并入该群的共享会话，用于清理某群切到「整群共享上下文」后残留的分成员会话。

### `GET /api/groups/mergeable`

```json
{
  "ok": true,
  "data": {
    "global_mode": "shared",
    "total_groups": 2,
    "total_conversations": 15,
    "items": [
      {
        "group_openid": "A2F7A3FD…", "group_name": "群名",
        "shared_conversation_id": "Z3JvdXAtc2hhcmVk…",
        "context_mode": "shared", "mergeable": true,
        "conversations": [
          { "id": "…", "scene": "group_member", "member_name": "小明",
            "message_count": 24, "token_estimate": 900, "last_active_at": "…" }
        ]
      }
    ]
  }
}
```

- 只列出**会话数 > 1** 的群（最多 50 个）。
- `context_mode` 是该群**生效**的上下文模式；`mergeable` 即 `context_mode == "shared"`。

### `POST /api/groups/{group_openid}/conversations/merge`

请求：`{ "target_id": "…（可省略）", "dry_run": true }`。省略 `target_id` 时用该群的共享会话；
没有就新建一个。（`dry_run` 省略 = `false`）

```json
{
  "group_openid": "A2F7A3FD…", "target_id": "Z3JvdXAtc2hhcmVk…",
  "source_ids": ["…"], "moved_messages": 92, "deduped_messages": 4,
  "deleted_conversations": 10, "target_message_count": 211,
  "target_turn_count": 96, "dry_run": false
}
```

| HTTP | code | 说明 |
|---|---|---|
| 400 | `invalid_request` | 缺 `group_openid`；JSON 不合法 / 未知字段 |
| 400 | `context_mode_not_shared` | 该群生效的上下文模式不是「整群共享」 |
| 400 | `group_mismatch` | 目标会话不属于该群（**跨群合并一律拒绝**） |
| 404 | `not_found` | 该群没有可合并的会话 |
| 500 | `store_error` | 数据库错误 |

安全语义：消息行是**改归属**而不是复制，因此 id（即时间顺序）保持不变，重复执行不会产生副本；
全部操作在一个事务里完成。另一个机器人的消息此前被镜像进该群每个会话，合并时按 (内容, 发送者, 时间) 去重，
重复条数在 `deduped_messages` 中返回。

## 5.6 媒体与附件

### `GET /api/media/{id}`

返回缓存下来的附件二进制（目前是图片）。**没有 JSON 信封**，`Content-Type` 是附件的真实类型。

- `id` 是内容 sha256（64 位十六进制）：不是 64 位 → `400 invalid_request`。
- 成功：`200` + 裸字节 + `Cache-Control: private, max-age=31536000, immutable` + `Content-Length`。
- 不存在 / 已被清理 → `404 not_found`；媒体存储未启用 → `404 not_found`；数据库错误 → `500 store_error`。

会话详情里每条消息的 `attachments` 形如：

```json
[
  { "kind": "image", "media_id": "9f2c…", "content_type": "image/png",
    "filename": "a.png", "width": 800, "height": 600, "size": 42113 }
]
```

- `kind` ∈ `image` / `voice` / `video` / `file`；`media_id` / `content_type` / `filename` /
  `width` / `height` / `size` / `asr_text` 全部 `omitempty`。
- 只有 `image` 且带 `media_id` 的会被下载缓存；其余只保留元数据；
  `media_id` 为空表示当时下载失败（消息仍会保留）。

## 6. 消息、日志与审计

### `GET /api/messages`

查询参数：`direction`（`in` / `out`）、`status`、`q`（内容 / 昵称 / 群 openid / msg_id 模糊匹配）、
`page`（默认 1）、`page_size`（默认 50；只能 1–200，越界按 50，回显实际生效值）、`since` / `until`（RFC3339 字符串，含边界）。

```json
{
  "ok": true,
  "data": {
    "total": 900, "page": 1, "page_size": 50,
    "items": [
      {
        "id": 900, "direction": "in", "scene": "group_member",
        "conversation_id": "Z3JvdXA6…",
        "group_openid": "B2C3...", "member_openid": "A1B2...", "member_name": "小明",
        "content": "@bot 你好", "msg_id": "ROBOT1.0_xxx",
        "status": "replied",
        "latency_ms": 2340,
        "created_at": "..."
      }
    ]
  }
}
```

- `status` ∈ `received` / `replied` / `early_reply` / `command` / `ignored` / `failed` / `manual`；
  被忽略的消息**也会记录**，原因写在 `error` 里（`error` 带 `omitempty`）。

  | 值 | 方向 | 含义 |
  |---|---|---|
  | `received` | 入站 | 已落审计、正在处理（终态会被覆盖；SSE 上同一 `id` 可能先 `received` 后终态） |
  | `replied` | 出站 | 出站消息已交付（模型回复、命令回复、提前回复的正式答案都算） |
  | `early_reply` | 出站 | 提前安抚语（`early_reply_enabled`）；`delivery=active` |
  | `command` | **入站** | **被当作聊天命令处理**：执行了命令并回了话，但**没进对话管线**（不调模型、不润色、不写上下文）。出站那一行仍是 `replied` |
  | `ignored` | 入站 | 被丢弃，原因在 `error`。**非管理员发管理命令**也落在这一档（`error` 含「非管理员」），属预期行为，不是故障 |
  | `failed` | 双向 | 入站：处理失败（模型/上游）；出站：投递失败（原因在 `error`） |
  | `manual` | 出站 | 后台会话详情页由管理员手动发出（§5 的 `POST …/messages`） |

  `command` 的细节见 README §5.13 与 `docs/DECISIONS.md` **D-042**：命令只在
  `@机器人` 或私聊时识别，两种形态等价：`/name args`，或**整条消息恰为一个已注册命令名**
  （指令面板点击的形态：平台剥掉 `name` 的前导 `/`，`args` 为空 ⇒ 走命令的默认动作）。
  命令名大小写不敏感；**带参数必须带 `/`**（`tokenstats off` 不是命令）；未注册的 `/xxx`
  与未注册的 bare 单词都按普通消息走。
  `/help` 是其中唯一的公共命令（`adminOnly=false`），审计同样是入站 `command` / 出站 `replied`。
  **指令面板同步（§8 的 `sync-commands`）不产生 `records` 行**，也不新增任何 `status` 值 ——
  它操作的是平台侧面板，与消息审计无关。
- 筛选：`direction` / `status` / `q` / `since` / `until` 都是**可选**且可组合，语义是 **AND**。
  `status` 是**精确匹配**（不是前缀/模糊），未知值不会报错、只是筛出 0 条；
  由于 `records` 没有 `CHECK` 约束，后端也不做枚举校验 —— 值是按上面的表**约定**使用的。
  `q` 只匹配 **`content` / `member_name` / `group_openid` / `msg_id`**，
  **不匹配 `error`** —— 所以「非管理员」这类忽略原因只能先按 `status=ignored` 筛出来，
  再看每一行的 `error`（要按原因搜，就 `docker logs` 里 grep，日志行里有 `reason`）。
  常用组合：`status=command`（谁在用命令）、`status=ignored`（含未 @ 的 at_only 群消息、
  人味沉默、非管理员命令等）、`direction=out&status=failed`（投递失败）。
- 重复推送的同一条 `msg_id` **不会入库**：只在 `counters.events_deduped` 里 +1 并打一条 debug 日志
  （旧文档里的 `deduped` 状态并不存在）。命令同样沿用 `msg_id` 幂等 —— 重复推送只会执行一次、
  只留一对 `in/out` 审计行。
- `delivery`（`omitempty`）：出站消息的投递方式，`passive`（绑定入站 `msg_id` 的被动回复，
  同一条最多 5 次）或 `active`（主动消息，消耗群日配额）；入站行没有这个字段。
- `conversation_id` 是该条消息所属会话。

> 与 §5 会话列表同一套分页边界：越界回退到默认值并回显实际生效值。

### `GET /api/logs?level=&q=&limit=200&before_id=`

```json
{
  "ok": true,
  "data": {
    "items": [
      { "id": 12001, "ts": "...", "level": "info", "component": "ws", "message": "resume ok", "fields": { "seq": 2002 } }
    ]
  }
}
```

- `level` ∈ `debug` / `info` / `warn` / `error`（返回**该级别及以上**；空或未知值按 `info`）。
- `limit` 夹在 1–1000（默认 200）；`before_id` 向更早翻页；`q` 匹配 `message` 或 `component`。
- 只有 `items`，没有 `total`（进程内环形缓冲，`log_buffer_size` 决定容量）。
- `fields` 带 `omitempty`。

### `GET /api/tool-calls?limit=50&before_id=` — 工具调用审计

```json
{
  "ok": true,
  "data": {
    "items": [{
      "id": 12, "conversation_id": "…", "group_openid": "…", "member_openid": "…",
      "tool": "now", "args": "{}", "status": "ok", "error": "",
      "latency_ms": 1, "result_chars": 42, "created_at": "2026…"
    }],
    "next_before_id": 0
  }
}
```

- `status` ∈ `ok` / `error` / `rejected`（`rejected` = 权限/身份校验拒绝，不是工具执行失败）。
- `args` 已截断到 500 字；`error` 无 `omitempty`（成功时是空串）。
- `limit` 默认 50（`<=0` 或 `>500` 时按 50）；`next_before_id` 仅当**返回条数等于 `limit`** 时非 0，
  否则为 `0`（到底了）。
- 后台「工具调用」页用它分页。

### `GET /api/proactive` — 自发主动状态（按群）

```json
{ "ok": true, "data": {
  "items": [{"group_openid":"…","last_sent_at":"…","silent_streak":1,"sends":4,
             "last_reason":"有人卡在链接上","updated_at":"…"}],
  "count": 1, "level": "auto", "ratio": 10, "max_credits": 3,
  "quiet_hours": [23, 8], "min_interval_minutes": 10 } }
```

`level` / `ratio` / `max_credits` / `quiet_hours` / `min_interval_minutes` 是当前生效的全局设置。

### `/api/tasks` — 定时提醒（委托式主动）

- `GET /api/tasks` 支持 `status`（`pending` / `leased` / `sent` / `failed` / `canceled`）、
  `limit`（默认 50，夹在 1–200）、`offset`。响应 `{ "items": [ScheduledTask…], "count": N }`
  （`count` 是本页条数，不是总数）。
- `DELETE /api/tasks/{id}` → `{ "canceled": true }`；`id` 非数字 → `400 invalid_request`；
  不存在 / 已发出 / 已取消 → `404 not_found`。

`ScheduledTask` 字段：

| 字段 | 说明 |
|---|---|
| `id` | 主键（自增，按它倒序返回） |
| `conversation_id` | 归属会话 |
| `group_openid` / `member_openid` | 目标会话标识（私聊只有 `member_openid`） |
| `creator_openid` | 发起人；聊天里的 `list_tasks` / `cancel_task` 按它隔离 |
| `kind` | 目前恒为 `reminder` |
| `text` | 提醒正文 |
| `due_at` | 到期时间（RFC3339） |
| `status` | `pending` / `leased` / `sent` / `failed` / `canceled` |
| `attempts` | 已尝试次数 |
| `lease_until` | 抢执行权的租约到期时间（空 = 未被租） |
| `error` | 失败原因（无 `omitempty`，成功时为空串） |
| `created_at` / `sent_at` | 创建 / 发出时间（`sent_at` 空 = 未发出） |

**归属按人隔离**：聊天里的 `list_tasks` / `cancel_task` 只作用于自己的提醒，每人待发上限
（`tasks_max_per_member`）按人计算；后台 API 是运营视角，能看到全部并取消任意一条。
到点由调度器（1s tick、`lease_until` 抢执行权）用**主动消息**发出，写审计并补进会话时间线。
**停机超过 10 分钟**的到期提醒标记 `failed`（原因写明「错过发送窗口」）而不是补发。

### `/api/commitments` — 承诺（open loop）追问

- `GET /api/commitments` 支持 `conversation_id`、`creator`、`limit`（默认 50，夹在 1–200）。
  响应 `{ "items": [Commitment…], "count": N }`（`count` 是本页条数）。
  它返回**完整历史**（不过滤状态），所以 `done` / `cancelled` 的行也会出现在列表里。
- `DELETE /api/commitments/{id}`（protected，无请求体）→ `{ "canceled": true }`。
  **只有 `waiting` / `leased` 可取消**；`done` / `cancelled` 是终态，返回
  `409 conflict`，**不覆盖**用户收口时写下的 `ack_note` / `ack_at`。
  `id` 非数字或 ≤ 0 → `400 invalid_request`；不存在 → `404 not_found`。
  取消成功时写入 `status=cancelled`、`ack_note="后台取消"`、`ack_at=now`。
- 聊天里的 `ack_commitment` / `cancel_commitment` 仍是主要收口路径；
  后台删除是运营兜底（例如用户说「别问了」但模型没调工具）。

`GET /api/commitments` 示例：

```json
{ "ok": true, "data": {
  "items": [{
    "id": 7,
    "conversation_id": "group:G1:U1",
    "group_openid": "G1",
    "member_openid": "U1",
    "creator_openid": "U1",
    "text": "明天把方案发到群里",
    "status": "waiting",
    "priority": 1,
    "deadline_at": "",
    "next_check_at": "2026-11-04T09:00:00+08:00",
    "last_nudge_at": "2026-11-03T09:00:00+08:00",
    "nudge_count": 1,
    "ack_at": "",
    "ack_note": "",
    "meta": "",
    "created_at": "2026-11-03T08:00:00+08:00"
  }],
  "count": 1
} }
```

`Commitment` 字段：

| 字段 | 说明 |
|---|---|
| `id` | 主键（自增，按它倒序返回） |
| `conversation_id` | 归属会话 |
| `group_openid` / `member_openid` | 投递目标（私聊只有 `member_openid`） |
| `creator_openid` | 建立承诺的人；聊天工具按它隔离 |
| `text` | 承诺内容 |
| `status` | `waiting` / `leased` / `done` / `cancelled` |
| `priority` | 0–3（0 最低） |
| `deadline_at` | 可选截止时间；空串 = 未设置 |
| `next_check_at` | 下次追问时间；`leased` 时它是租约到期时间 |
| `last_nudge_at` | 上次追问时间；空串 = 还没追问过 |
| `nudge_count` | 已追问次数 |
| `ack_at` | 收口时间；空串 = 未收口 |
| `ack_note` | 用户回执原话（done/cancel 时记录） |
| `meta` | 预留扩展（当前为空串） |
| `created_at` | 创建时间 |

**状态机**：`waiting → leased`（调度器抢到执行权）→ 发送成功后再回到 `waiting`
（`next_check_at` = 模型给的间隔，`nudge_count+1`）；发送失败退避 **5 分钟**重试；
`done` / `cancelled` 是终态。

### `/api/memories` — 长期记忆

- `GET /api/memories` 支持 `scope`（`group` / `member` / `global`）、`group_openid`、
  `member_openid`、`limit`（默认 50，夹在 1–200）、`offset`。
  响应 `{ "items": [Memory…], "count": N }`（`count` 是本页条数）。
- `POST /api/memories` 手工写一条：
  请求 `{ "scope": "group", "group_openid": "…", "member_openid": "", "text": "…" }`
  → `{ "id": 12 }`。`scope` 只能是 `global` / `group` / `member`；
  群记忆必须带 `group_openid`、个人记忆必须带 `member_openid`、`text` 不能为空，否则 `400`。
  写入时 `kind` 固定 `fact`、`source` 固定 `admin`。
- `DELETE /api/memories/{id}` → `{ "deleted": 1 }`；`id` 非数字 → `400`；不存在 → `404`。
- `DELETE /api/memories` 带 `group_openid` 查询参数 → `{ "deleted": N }`（忘掉整个群）；
  既没有 `id` 也没有 `group_openid` → `400`。

`Memory` 字段：`id`、`scope`（`group`/`member`/`global`）、`group_openid`、`member_openid`、
`kind`（接口写入恒为 `fact`，模型抽取的记忆也是 `fact`）、`text`、`source`
（接口写入恒为 `admin`；模型自动抽取的是 `extract`）、`salience`、
`hits`、`last_used_at`、`created_at`。

作用域决定可见性：`group` 只在该群、`member` 只在该成员私聊、`global` 处处可见。
同一作用域内重复写入相同文本不会新增行，而是 `salience +0.5`。

## 6.7 情绪状态（affect）

后台 `/affect` 页与 `/mood` 命令使用这组端点。全部 **protected**；除特别说明外，
错误信封与 §8 通用错误码一致。数值状态只影响表达，不进入消息内容；接口不返回用户原文。

**7 组端点、9 个方法**：5 个 GET（packs / conversations / state / history / events）
+ 4 个控制 POST（pause / resume / preset / reset）。没有批量修改状态的接口；
启用与档位仍走 `PUT /api/config`。

### `GET /api/affect/packs`

列出内置内容包（只读，随二进制发布）。

```json
{
  "ok": true,
  "data": {
    "packs": [
      { "id": "daily", "name": "日常心境", "version": "1.0.0",
        "description": "群里日常聊天用的通用心境：随着对话慢慢起伏，也慢慢回落，只影响语气和长短。",
        "dimensions": ["心情", "精力", "压力", "熟悉度", "耐心"],
        "events": 12, "presets": 4 }
    ]
  }
}
```

- 当前只有 `daily`；`dimensions` 是标签数组（不是键），`events` / `presets` 是数量。
- 不需要服务或数据库就能返回；内置包解析失败时数组为空，不返回 5xx。

### `GET /api/affect/conversations?limit=50`

最近有状态的会话，按 `updated_at DESC`。

| 参数 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `limit` | 50 | 1–200 | 返回条数；越界夹取后回显实际生效值 |

```json
{
  "ok": true,
  "data": {
    "conversations": [
      { "id": "c_…", "pack": "daily", "updated_at": "2026-11-10T12:00:00+08:00",
        "dims": { "valence": 62.5, "energy": 48, "stress": 31, "closeness": 57, "patience": 66 } }
    ]
  }
}
```

- `id` 是 `conversation_id`；`dims` 是 5 维当前值，保留 2 位小数。
- 没有状态时返回空数组，不是 404。

### `GET /api/affect/state?conversation=`

| 参数 | 必填 | 说明 |
|---|---|---|
| `conversation` | 是 | 会话 id；缺失或空 → `400 invalid_request` |

```json
{
  "ok": true,
  "data": {
    "state": {
      "conversation_id": "c_…",
      "pack_id": "daily",
      "pack_name": "日常心境",
      "dims": { "valence": 62.5, "energy": 48, "stress": 31, "closeness": 57, "patience": 66 },
      "events": [ { "id": "praise", "at": "2026-11-10T11:58:00+08:00" } ],
      "ticked_at": "2026-11-10T12:00:00+08:00",
      "updated_at": "2026-11-10T12:00:00+08:00",
      "version": 3
    },
    "behavior": {
      "max_chars_factor": 1, "max_segments": 0, "humanize_scale": 1,
      "silence_boost": 0, "initiative_factor": 1,
      "tone": "neutral", "reason": "no band matched"
    },
    "blocks": { "line": "[此刻的状态｜…] 今天比较平静。", "full": "[此刻的状态｜…] …" }
  }
}
```

- `state` 直接复用持久化结构：`pack_id` / `pack_name`、`dims`、最近去重标记 `events`、
  `paused_until` / `ticked_at` / `updated_at`（空值字段省略）、乐观锁 `version`。
- `blocks` 是 **预览**：忽略当前总开关与档位，强制计算 line / full；但**暂停中的会话仍为空串**
  （与真实回复路径一致）。预览为空串表示该档没有可注入内容。
- 会话存在但从未产生状态时，返回**内容包基线**（`version=0`），不是 404；
  只有 `conversation` 不存在才 `404 not_found`。DB 读失败 → `500 internal_error`。

### `GET /api/affect/history?conversation=&hours=24`

| 参数 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `conversation` | 必填 | — | 缺失 → `400 invalid_request` |
| `hours` | 24 | 1–720 | 回看窗口；越界夹取 |
| `limit` | 1000 | 1–1000 | 采样点上限；越界夹取 |

```json
{
  "ok": true,
  "data": {
    "points": [
      { "at": "2026-11-10T11:00:00+08:00",
        "dims": { "valence": 60, "energy": 50, "stress": 30, "closeness": 56, "patience": 65 } }
    ],
    "hours": 24
  }
}
```

- **只返回有数据的点，不补零**；前端按 `at` 连线。`hours` 回显实际生效的窗口。
- 历史保留 30 天；更早的点已被清理，不报错。

### `GET /api/affect/events?conversation=&limit=50`

| 参数 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `conversation` | 必填 | — | 缺失 → `400 invalid_request` |
| `limit` | 50 | 1–200 | 最近事件条数；越界夹取 |

```json
{
  "ok": true,
  "data": {
    "events": [
      { "id": 12, "conversation_id": "c_…", "event_id": "praise",
        "tone": "positive", "source": "classifier",
        "created_at": "2026-11-10T11:58:00+08:00" }
    ]
  }
}
```

- 事件日志不含消息原文、用户昵称或 openid；`source` 记录来源（分类器写入 `classifier`）。
- 没有事件时返回空数组；日志保留 7 天。

### `POST /api/affect/pause`

请求：

```json
{ "conversation": "c_…", "minutes": 60 }
```

- `minutes` 必须是正整数，范围 **1–1440**（24 小时）；缺失 / 非数字 / 越界 → `400 invalid_request`。
- 暂停期间不注入、不施加行为影响、proactive 跳过；到期自动恢复。暂停不删除状态。
- 响应 `200` + `{ "data": { "conversation": "c_…", "minutes": 60, "paused_until": "…" } }`。
- `conversation` 不存在 → `404 not_found`；服务未接线 → `503 unavailable`；
  乐观锁冲突由服务最多尝试 3 次（含首次），仍失败 → `500 internal_error`（不透传 409）。

### `POST /api/affect/resume`

请求 `{ "conversation": "c_…" }`。清空 `paused_until`，幂等：未暂停时也返回 `200`。
响应 `{ "conversation": "c_…", "paused_until": "" }`；错误同 `/pause`。

### `POST /api/affect/preset`

请求 `{ "conversation": "c_…", "preset": "calm" }`。

- `preset` 必须是当前内容包里存在的 id；未知 / 缺失 → `400 invalid_request`。
- 应用预设会把 5 维设为预设值（仍 clamp 到 0–100、保留 2 位小数）；
  不改变内容包、注入档位或暂停状态，也不清事件标记。
- 响应 `{ "conversation": "c_…", "preset": "calm" }`；`conversation` 不存在 → `404 not_found`。

### `POST /api/affect/reset`

请求 `{ "conversation": "c_…" }`。

- 把 5 维重置为内容包基线、清空暂停与最近事件标记；**保留历史点**（供曲线对照）。
  用于把某个会话「哄回正常」，不删除会话本身。
- 响应 `{ "conversation": "c_…", "reset": true }`；`conversation` 不存在 → `404 not_found`。

### affect 错误码

| HTTP | code | 场景 |
|---|---|---|
| 400 | `invalid_request` | 缺 `conversation` / `preset` / `minutes`，类型或范围错误；预设不存在 |
| 401 | `unauthorized` | 未登录 |
| 404 | `not_found` | 会话不存在 |
| 500 | `internal_error` | 数据库读 / 写失败，或乐观锁尝试 3 次（含首次）仍冲突 |
| 503 | `unavailable` | 情绪状态服务 / 存储未接线 |

- 没有 `409`：乐观锁冲突在服务内部重读重试，不暴露给调用方。
- `GET /api/affect/packs` 不依赖服务，其余端点在 `Affect == nil` 时返回 `503`。

### `GET /api/events/stream` — **SSE**（`text/event-stream`）

连接成功后立即推一帧 `status`，之后每 5 秒推一次 `status`；总线事件按名转发；每 15 秒一帧 `ping`。

| event | data |
|---|---|
| `status` | 与 `/api/status` 的 `data` 同构；**连接成功后立即推一帧**，之后每 5 秒一帧 |
| `message` | 与 `/api/messages` 的 item 同构（收发各推一次）。命令也走这里，且**顺序固定**：先推入站那帧 `status=command`，**之后**才推出站那帧 `status=replied`（投递失败则是 `status=failed` + `error`）。入站帧不会晚于出站帧出现 |
| `log` | 与 `/api/logs` 的 item 同构 |
| `tool` | `{ "tool": "now", "status": "ok", "latency_ms": 1, "conversation_id": "…", "chars": 42 }` |
| `ws` | `qq.WSStatus` 全字段（QQ 网关状态，变化时推送）：`{ "enabled": true, "connected": true, "state": "ready", "session_id": "…", "shard": [0, 1], "gateway_url": "wss://…", "heartbeat_interval_ms": 45000, "latency_ms": 62, "last_event_at": "…"（或 null）, "reconnects": 0, "resumes": 0, "last_error": null }` |
| `ping` | `{}`，每 15 秒 |

- 断线由浏览器 `EventSource` 自动重连，服务端不补发历史。
- 未认证时返回标准 `401` JSON 信封（不是流）：`EventSource` 依赖同源 cookie，
  脚本环境请改用 `Authorization: Bearer` + 轮询。

> 上表即**契约事件集合**（共 6 个）：服务端把总线事件名原样转发，新增事件名要先进这张表再被前端使用。

## 6.8 自主加入（join_gate）

后台 `/join` 页使用这组端点。全部 **protected**；除特别说明外，错误信封与 §8
通用错误码一致。判定与话题都是**内部状态**，不写进用户可见消息；接口不返回消息全文，
decision 里只有截断后的预览。没有写端点：启用与参数仍走 `PUT /api/config`。

**2 个只读端点**：`GET /api/join/decisions`、`GET /api/join/topics`（页面另用 §5 的
`GET /api/groups/names` 把 openid 显示成群名）。
`join_gate_enabled` 默认 **false**；关闭时端点仍可读历史记录，但不会产生新的评估。

### `GET /api/join/decisions?group_openid=&limit=`

最近判定记录，按 `created_at DESC`。

| 参数 | 默认 | 范围 | 说明 |
|---|---|---|---|
| `group_openid` | 空 | ≤256 rune | 空串 = 全部群；非空只返回该群；超长 → `400 invalid_request` |
| `limit` | 50 | 1–200 | 返回条数；非数字 / 越界 → `400 invalid_request`（不静默夹取） |

```json
{
  "ok": true,
  "data": {
    "decisions": [
      {
        "id": 128, "group_openid": "B2C3...", "conversation_id": "c_...",
        "message_id": "m_...", "speaker": "小明",
        "preview": "有人懂这家店吗？周六想约个饭。",
        "joined": true, "action": "join", "confidence": 0.82,
        "reason": "公开求助且 bot 参与过话题",
        "topics": "[{\"id\":\"t1\",\"title\":\"周末聚餐\",\"summary\":\"在约周六晚饭。\",\"message_ids\":[12,13],\"type\":\"plan\",\"status\":\"active\",\"resumable\":true}]",
        "model": "deepseek-chat", "latency_ms": 840,
        "created_at": "2026-11-10T12:00:00+08:00"
      }
    ]
  }
}
```

- `action` 是 **v2 新增**，取值 `join` / `wait` / `skip`：`join` = 已接手；`wait` = 模型想
  等话题再发展一会儿；`skip` = 判定不接或闸门 / 失败拦下。历史行可能是空串 `""`
  （v2 之前写入），前端应按 `joined` 回退显示。
- `joined` **保留且含义不变**，由 `action=="join"` 派生：`action` 与 `joined` 永不自相矛盾。
  只读 `joined` 的老调用方无需改动。
- `action=wait` / `skip` 都可能是模型判定，也可能是闸门（`reason` 以 `cooldown:` / `rate:` /
  `skip:` 开头）或失败（`gate error: …`）造成，**具体看 `reason`**。
- `addressed_to`（`bot` / `group` / `someone_else` / `unknown`）**不是本端点返回的字段**：
  模型会输出，但它只进提示词判断与加入成功时的 INFO 日志，`join_decisions` 没有对应列，
  因此 decision 里拿不到。前端按「可选、缺失即不显示」容错；**排查以 `reason` 为准**
  （模型被要求把「在跟谁说」的依据写进 reason），不要把它当稳定契约依赖。
- `preview` 最多 **80 字**；不返回消息全文。
- `topics` 是 **JSON 字符串**（存储层原样返回），内容是话题数组，每个话题可带
  `type`（`help` / `chat` / `question` / `plan` / `conflict` / `joke` / `other`，缺省 `other`）、
  `status`（`active` / `dormant` / `closed`）与 `resumable`；
  前端解析时对「字符串 / 数组 / 空串」都容错，对未知新字段忽略。没有话题时为空串。
  这里的 `status` 是**已按年龄降级**的最终值（§6.8 topics 同源），不是模型原始输出。
- `confidence` 是模型自评的 0–1 小数，不是系统二次计算的结果。
- 没有记录时返回空数组，不是 404。

### `GET /api/join/topics?group_openid=`

该群最近一次话题切分缓存。

| 参数 | 必填 | 说明 |
|---|---|---|
| `group_openid` | 是 | 群 openid；缺失、空或超过 256 rune → `400 invalid_request` |

```json
{
  "ok": true,
  "data": {
    "topics": {
      "group_openid": "B2C3...",
      "topics": "[{\"id\":\"t1\",\"title\":\"周末聚餐\",\"summary\":\"几个人在约周六晚饭的时间和地点。\",\"message_ids\":[12,13,14],\"type\":\"plan\",\"status\":\"active\",\"resumable\":true}]",
      "analyzed_at": "2026-11-10T12:00:00+08:00"
    }
  }
}
```

- `topics` 是记录 **1–8 个话题**的 JSON 字符串；`message_ids` 只包含输入窗口里出现过的 id；
  每个话题可带 `type`（`help` / `chat` / `question` / `plan` / `conflict` / `joke` / `other`）、
  `status`（`active` / `dormant` / `closed`）与 `resumable`（bool）。
- **状态是降级后的**：保存前按话题最新消息年龄做单向降级（`join_gate_topic_dormant_minutes` /
  `_closed_minutes`），`closed` 的 `resumable` 恒为 `false`，`active` 恒为 `true`；
  时间流逝不会把 `closed` 升回来。端点返回完整缓存，方便后台核对。
- 缓存只在**解析成功**时覆盖；模型失败时保留上一次。
- **没有缓存时 `topics` 为 `null`**（200，不是 404），页面展示空状态。
- 供回复注入的缓存只取最近 **15 分钟**、最多 **400 字**；该端点返回完整缓存，
  方便后台核对，不代表注入会全部使用。

### join_gate 错误码

| HTTP | code | 场景 |
|---|---|---|
| 400 | `invalid_request` | `limit` 非数字 / 越界，`group_openid` 超过 256 rune 或缺失（topics） |
| 400 | `invalid_request` | `/api/groups/names` 的 `openids` 为空、全空白或超过 100 个 |
| 401 | `unauthorized` | 未登录 |
| 500 | `store_error` | 数据库读失败 |
| 503 | `unavailable` | 存储未接线（`deps.Store == nil`） |

- 没有 `404`：没有 decision 返回空数组，没有 topics 返回 `topics: null`，
  群名端点对未知群返回空串而不是 404。
- `action` 是**派生展示字段**：非法值不会被返回，历史空串按 `joined` 回退。

## 6.9 表达方式库（expression）

学习与召回由回复管线在后台驱动；后台接口只做人工维护与审核。

### `GET /api/expressions?group_openid=&enabled=&q=&page=&page_size=`

返回 `{items,total,page,page_size}`。`enabled` 可传 `true` / `false` 过滤待审/停用；
`q` 同时匹配 `situation` 与 `style`；`group_openid` 为空表示全部群。

```json
{
  "items": [{
    "id": 12, "group_openid": "GROUP_OPENID",
    "situation": "有人问吃了吗", "style": "刚吃完，你呢",
    "source": "learn", "use_count": 3, "enabled": true,
    "created_at": "2026-11-11T20:00:00+08:00", "updated_at": "2026-11-11T20:00:00+08:00"
  }],
  "total": 1, "page": 1, "page_size": 20
}
```

### `POST /api/expressions`

```json
{ "group_openid": "GROUP_OPENID", "situation": "有人问吃了吗", "style": "刚吃完，你呢" }
```

`group_openid` 空 = 全局表达。`situation` / `style` 必填且各 ≤200 rune。
同一 `(group_openid,situation,style)` 重复提交不报错，返回已有行并累加 `use_count`。

### `POST /api/expressions/{id}/enabled`

```json
{ "enabled": true }
```

用于审核学习结果（默认 `review_enabled=true` 时新学到的行是 disabled）。

### `DELETE /api/expressions/{id}`

删除一条；不存在返回 `404 not_found`。

### `POST /api/expressions/{id}/reinforce`

人工加权：`use_count + 1`，让这条在召回排序里更靠前。

### 错误码

- `invalid_request`：缺 `situation` / `style`、超长、非法 `enabled`、非法 id。
- `not_found`：启停/删除/强化一个不存在的 id。
- `store_error`：数据库错误；原始错误只进日志，不透出隐私。

---

## 7. 统计

### `GET /api/stats?range=24h`

`range` ∈ `1h` / `6h` / `24h` / `7d`，**其余值按 `24h`**。`bucket` 由 range 推导：

| range | bucket |
|---|---|
| `1h` | `5m` |
| `6h` | `30m` |
| `24h` | `1h` |
| `7d` | `6h` |

```json
{
  "ok": true,
  "data": {
    "range": "24h",
    "bucket": "1h",
    "series": [
      { "ts": "2026-07-21T10:00:00+08:00", "in": 12, "out": 12, "errors": 0,
        "avg_latency_ms": 2100, "tokens": 3400 }
    ],
    "totals": { "ts": "", "in": 812, "out": 801, "errors": 7, "avg_latency_ms": 2310, "tokens": 210000 },
    "top_groups": [
      { "group_openid": "B2C3...", "group_name": "测试群", "messages": 120 }
    ]
  }
}
```

- `series` **只包含有数据的桶，不补零** —— 前端画图要自己按 `bucket` 补齐空档。
- `totals` 复用 `SeriesPoint` 的形状（`ts` 是空串），`avg_latency_ms` 是各点平均值的再平均。
- `top_groups` 是窗口内消息数前 10 的群（固定 10 条）。
- 统计数据来自进程内聚合 + 数据库，分钟桶保留 7 天。

## 8. 运维动作

| 方法 | 路径 | 请求 | 成功响应 |
|---|---|---|---|
| `POST` | `/api/actions/reconnect` | 无（空体也可以） | `{ "requested": true }` |
| `POST` | `/api/actions/reload-token` | 无 | `{ "token_valid": true, "token_expires_in_seconds": 5100, "token_refreshes": 13 }` |
| `POST` | `/api/actions/sync-commands` | 无 | `{ "results": [{ "scope": "group", "panel_id": "…", "action": "created", "version": 0 }, …] }` |
| `POST` | `/api/actions/reset-config` | 无 | `{ "settings": …, "warnings": […] }`（settings 为内置默认值；三个凭据保留且仍脱敏） |
| `POST` | `/api/actions/test-ai` | `{ "prompt": "hi", "tools": false }` | 见下 |
| `POST` | `/api/actions/test-send` | `{ "group_openid": "…", "content": "…" }` | `{ "message_ids": ["…"], "message_id": "…", "segments": 1, "active": true }` |

### `POST /api/actions/reconnect`

仅对 WebSocket 传输有效：未启用 WS → `400 not_configured`（消息里带当前 `qq_transport`）。
成功只是**发出重建请求**，连接状态请继续看 `/api/status` 的 `ws` 或 SSE `ws` 事件。

### `POST /api/actions/reload-token`

立即向上游刷新 access_token（20 秒超时）。QQ 凭证未配置 → `400 not_configured`；
上游失败 → `502 upstream_error`。

### `POST /api/actions/sync-commands`

把命令注册表同步到 QQ 指令面板，`scope=group` 与 `scope=c2c` 各一次，返回每项的
`{scope, panel_id, action, version}`，`action` ∈ `created | updated | unchanged`。
请求体为空（不读任何参数），整个请求 25 秒超时。

```json
{ "results": [
  { "scope": "group", "panel_id": "P-…", "action": "created",  "version": 0 },
  { "scope": "c2c",   "panel_id": "P-…", "action": "unchanged", "version": 3 }
] }
```

- 只更新 `remark == "bot-harness:commands"` 的面板；用户手工创建的面板永不触碰，**也永不删除**。
- 无面板 → 创建（`target_type=all`，响应里 `version=0`，因为创建接口只回 `panel_id`）；
  有面板但 items 不一致 → 更新（`PUT /v2/panels/{id}`，不传 `version`）；一致 → `unchanged`
  （`version` 回显平台当前版本）。
- **自动同步开关** `command_panel_enabled` 关闭不影响这个动作：它是管理员的手动兜底。
  反过来，这个端点不会去改 `command_panel_enabled`，也不会触发启动同步。
- 两个 scope **各自独立尝试**：一个失败时另一个仍会执行；但由于失败即整体报错，
  **成功的那半边不会出现在 `data.results` 里**（契约是"要么 200 + 完整 results，要么 502"）。
  已写入平台的部分**不会回滚**，重新同步即可收敛（对账是幂等的）。
- 错误码：

  | HTTP | code | 触发 |
  |---|---|---|
  | 400 | `not_configured` | `deps.Dispatcher` 为 nil（装配未完成；不是"QQ 未配置"） |
  | 401 | `unauthorized` | 未登录 / 口令错误 |
  | 502 | `upstream_error` | 平台拒绝或网络失败。`error.message` 是**平台原文**（含 `code`）：无权限 / 参数或内容不合规 / 数量或长度超限 / 频控，常见 `40030011` / `40030013` / `40030015` / `40030016` / `40030018` / `40030020` |
- **不写审计**：面板同步不是消息，不产生 `records` 行；`records.status` 枚举与 v1.4 完全一致
  （`received` / `replied` / `early_reply` / `command` / `ignored` / `failed` / `manual`）。
- 日志：成功 INFO `command panel synced manually`（`component=admin`），失败 WARN
  `command panel sync failed`（带 `error`）；启动路径另有 `component=command-panel` 的日志。

### `POST /api/actions/reset-config`

把**可调配置**全部退回内置默认值（不是 `.env`），随后写一条 WARN 日志。语义细节：

1. **三个凭据保留**：`admin_token` / `openai_api_key` / `qq_app_secret` 不是覆盖项，
   重置不清空，旧登录会话继续有效。
2. **模型端点 / 模型名回到默认**：端点为 `""`（未配置）、模型名 `gpt-4o-mini`；
   `warnings` 按实际缺项提示（端点必提示；API Key 只有「从未配置过」才一起提示）。
3. **口令为空时重新生成**（此前从未生成过）：生成 `botharness-…` 并持久化，
   `warnings` 提示「请用 `bot-harness token` 读取」，此时旧会话失效。
4. **响应仍脱敏**：`settings` 经 `maskSecrets`，三个凭据恒为空串；`*_set` 如实反映是否配置。

### `POST /api/actions/test-ai`

请求可带 `prompt`（空则用一句默认问候）与 `tools`（true 时附带一个一次性 `echo_probe` 工具定义，
用来验证端点是否接受工具参数）。最大 512 tokens、流式请求、超时为请求超时设置再加 15 秒。

```json
{
  "text": "你好，我是……", "model": "deepseek-chat", "latency_ms": 900,
  "usage": { "prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3 },
  "tools_advertised": false, "tool_calls": [], "finish_reason": "stop"
}
```

错误：`400 not_configured`（内部未装配 AI 客户端）、`502 upstream_error`（端点未配置 / 上游失败）。
注意它**不写审计、不进会话**，只用于连通性验证。

### `POST /api/actions/test-send`

发送一条**主动消息**（`MaxSegments=1`，受平台频控）。本地另有每分钟 10 次的限制
（`429 rate_limited`）。缺 `group_openid` 或 `content` → `400 invalid_request`；
平台失败 → `502 upstream_error`。
它与手动发送（§5）的区别：不进会话时间线、不写审计、不分段、不占 `manual_send_*` 配额。

### 通用错误码

| HTTP | code | 含义 |
|---|---|---|
| 400 | `invalid_request` | 参数 / JSON / 未知字段 / 校验失败 |
| 400 | `not_configured` | 运行所需组件或凭证未配置 |
| 401 | `unauthorized` / `invalid_token` | 未登录 / 口令错误 |
| 403 | `manual_send_disabled` | 功能被设置关闭 |
| 404 | `not_found` | 资源不存在（顶层消息是 `unknown API endpoint`） |
| 409 | 见 §5 发送错误表 | 业务冲突 |
| 429 | `rate_limited` | 限流 |
| 500 | `internal_error` / `store_error` | 服务端 / 数据库错误 |
| 502 | `upstream_error` | QQ 平台或模型端点失败（新增） |
| 503 | `unavailable` / `degraded` | 依赖未就绪（发送处理器 / 健康检查） |

## 9. 前端交付要求

- 目录：`web/`（源码），构建产物 `web/dist/`（会被 `go:embed` 打进二进制，**必须与源码同步提交**）。
- 技术：Vue 3 + Vite + Vue Router + Tailwind CSS（纯 utility class，不引组件库）。
- 页面：登录、概览（状态卡 + SSE 实时事件流 + 统计）、会话管理（列表 + 详情 + 清空/启停/合并 + 手动发送 + 引用回复）、
  策略配置（触发模式、上下文模式与范围、白名单、按群覆盖）、消息记录、日志、工具调用、长期记忆、定时任务、
  情绪状态（`/affect`：5 维、历史曲线、事件、注入预览、暂停/恢复/预设/重置）、
  自主加入（`/join`：启用与 12 个参数、按群名选群、最近决策（插话 / 等一等 / 不插话）、
  最新话题及状态）、系统设置（6 个二级子页面）。
- **基础组件**：`SelectMenu.vue` 是全站统一的暗色下拉（按钮 + popover，支持 `v-model`、
  `options`、`placeholder`、`disabled`、`aria`、Arrow / Enter / Escape 与点击外部关闭），
  **页面里不要再写原生 `<select>`**；`/policies` 的按群覆盖是「每群一张卡」的 2–3 列 grid
  （窄屏单列、带搜索框），不再用会横向滚动的宽表格。
- 构建：`pnpm install` 后 `pnpm build` 必须成功（含类型检查），产物 `web/dist/index.html` + `web/dist/assets/*`。
- 单测：`pnpm test`（vitest），测试放 `web/src/**/__tests__/`。
- 开发代理：`vite.config.ts` 中 `/api` 代理到 `http://127.0.0.1:8080`。
- 路由使用 history 模式；后端对非 `/api` 路径回退到 `index.html`。
- 未登录时任意页面重定向到登录页；用 `/api/auth/me` 判断。

## 10. 契约维护

- 字段只增不改：新增字段前端必须容错；删除/重命名是破坏性变更，需在 DECISIONS 里记一条。
- 改 handler 响应的提交必须同步本文件；`internal/admin/admin_*_test.go` 里的契约测试是第二道闸。
- 三秘密（`openai_api_key` / `qq_app_secret` / `admin_token`）在任何响应、日志、错误信息里
  都不得出现明文；读当前后台口令用 `bot-harness token` 子命令，不要 grep `.env`。
- `web/dist` 是提交进仓库的构建产物：改前端必须 `pnpm build` 并一起提交（CI 有 check-dist 检查）。
