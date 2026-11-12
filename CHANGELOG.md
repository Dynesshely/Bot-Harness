# Changelog

本项目的所有重要变更都记录在这里。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### Added

- 根 `LICENSE`（Apache-2.0）、`NOTICE`、`THIRD_PARTY_NOTICES.md`。
- 社区与仓库元数据：`CONTRIBUTING.md`、`CODE_OF_CONDUCT.md`、`SECURITY.md`、
  issue / PR 模板、`CODEOWNERS`、Dependabot 配置。
- 容器发布工作流 `.github/workflows/release.yml`（推送 `ghcr.io/dynesshely/bot-harness`）。
- 文档资产目录 `docs/assets/`（架构图、界面截图）。
- 口吻轮换：`reply_style_variants`（最多 8 条、每条 ≤200 字的一句话风格）+
  `reply_style_variant_probability`（0–1，默认 0.25）。每轮按概率抽一条追加到 stage 1 的
  系统提示词；空数组 = 不启用，润色阶段的硬约束不变。
- 引用回复改为**频率分档** `quote_reply_mode`（`never` / `occasionally`=30% / `often`=70%，
  默认 `occasionally`）。旧布尔 `quote_reply` 继续兼容并在校验时双向同步
  （`true`→`occasionally`、`false`→`never`）；按群覆盖仍是布尔，
  `true`→`occasionally`、`false`→`never`、`null`→跟随全局，全局 `never` 时群覆盖打不开。

### Changed

- README 面向开源用户重写：移除内部环境与运维记录，补充徽章、架构、截图、贡献/安全/许可章节。
- `web/package.json`：公开包（`private: false`）、Apache-2.0、版本 `0.1.0`、仓库地址、
  锁定 `pnpm@11.25.0`。

## [0.4.1] - 2026-11-12

### Added

- systemPrompt 注入当前会话身份：群聊带 `group_openid`，私聊带 `user_openid`。管理员在聊天里
  说「把本群设置改成…」时，模型能直接把正确 id 交给 `settings_read` / `settings_write`，
  不再反问或猜错群。id 缺失时整块省略，群聊不注入成员私聊 OpenID。
- README 重写为项目概览：同步 0.4.x 能力，压缩手册式细节，统一「是什么 / 为什么 / 怎么用 / 怎么管 / 怎么开发」的阅读顺序。

### Security

- Go 工具链升到 1.27.4，修复 `GO-2026-6859`（`net/http` MaxBytesReader header race）：
  CI、Dockerfile 与 `go.mod` 同步升级；`govulncheck ./...` 通过。

## [0.4.0] - 2026-11-12

### Added

- **表达方式库（expression）**：完全对齐 MaiBot 的 expression learner / selector：
  - 异步从真实群聊学习「什么情境 -> 什么说法」，默认进待审，后台审核后才参与召回；
  - `fast` 档按 use_count 加权随机；`precise` 档用 OpenAI 兼容 `/embeddings` 做余弦粗排，
    再由主模型按当前语境选 0–`expression_inject_max` 条；embedding/选择失败自动退回 fast；
  - 只召回本群 + 全局，默认不跨群；注入位置在人设/口吻之后、记忆之前；
  - 新增 `expressions` 表（SQLite + PostgreSQL，迁移 0015）、后台「表达方式」页与 9 个配置键；
  - 召回+选择总预算 2 秒，学习异步，任何失败都不影响回复。
- **会话身份注入**：每轮 systemPrompt 带上「当前场景 + 群 OpenID / 用户 OpenID」，
  管理员说「本群」时模型能直接把正确的 id 交给 `settings_read` / `settings_write`，
  不用反问或猜。群聊注入 `group_openid`，私聊注入 `user_openid`；缺失时不输出空键。

## [0.3.2] - 2026-11-11

### Added

- **人情味 2.0**，吸收 MaiBot 的可用做法并接进现有管线：
  - 群消息合并（`reply_debounce_mode`）：未 @ 的连续消息先攒 1–7 秒（quiet / active 两档，
    本群最近 60 秒人类消息 ≥3 条算活跃），窗口被新消息重置但整批硬上限 20 秒；只让第一条
    （leader）跑模型，其余消息仍写时间线并各留一条 `debounced into <leader_msg_id>` 审计；
    @ / 引用机器人 / 命令零延迟，`off` 与秒数 0 回到逐条处理。
  - 分段气泡（`humanize_segment_split`）：长回复按标点概率拆成 1–3 条；代码块、链接、
    引用块不拆；引用只挂第一条。
  - 逐段打字（`humanize_typing_seconds_per_char` / `humanize_typing_max_seconds`）：
    每条气泡前按字数等待，整轮有上限，已耗时扣除。
  - 口吻轮换（`reply_style_variants` / `reply_style_variant_probability`）：按概率把一条
    备用说话方式注入本轮提示词，草稿与润色稿共享同一份。
  - 概率引用（`quote_reply_mode`）：never / occasionally（约 30%，默认）/ often（约 70%），
    旧 `quote_reply` 布尔双向兼容；按群覆盖仍是布尔粒度，全局 never 是天花板。
  - 新增 `reply_debounced_batches` / `reply_debounced_messages` 计数器（共 32 项）。

## [0.3.1] - 2026-11-11

### Fixed

- 设置页：修复点击「大屏编辑 / 手动输入 / 模型参数 / 测试 AI / 重置配置覆盖」时
  抛出 `Cannot create property 'value' on boolean 'false'` 的问题。根因是设置布局把
  ref 装进 `reactive` 再 provide，子页解构后拿到的是普通 boolean；模板里的直接赋值被
  编译成 `.value =`。现在布局直接 provide ref，并由布局提供打开/关闭/切换方法，
  子页模板不再给 context 字段赋值。
- 模型调用失败时，聊天里会带上脱敏后的错误摘要（HTTP 状态 / 上游 message / 超时/鉴权/
  限流分类），不再只有笼统的「稍后再试」；5xx 只透状态码，URL、密钥、DSN 一律不出现。
- PostgreSQL 下后台「合并同群会话」报 `column "n" does not exist`：`MergeableGroups` 的
  `HAVING n > 1` / `ORDER BY n DESC` 引用了 SELECT 别名，SQLite 接受、PostgreSQL 不接受；
  改用 `COUNT(*)` 并补真实 PG 测试。

## [0.3.0] - 2026-11-10

> 版本号 **0.3.0 待 Lead 确认**；确认后再统一更新 `internal/config.Version`、Dockerfile 与
> `web/package.json`。本节按 0.3.0 记录，功能开关默认关闭。

自主加入（join_gate）：未 @ 的群消息由模型判断要不要接话，并在同一次调用里完成
**三分类**（join / wait / skip）、`addressed_to` 与话题切分；引用机器人视同被 @；
话题带生命周期（`active` / `dormant` / `closed` + `resumable`），代码按消息年龄单向降级。
图片能力新增**引用 / 合并图片入库**、模型可见的 `[图片 #id]` 标注与按需 `view_image` 工具。
join_gate **默认关闭**；图片能力沿用 `vision_enabled`（默认开）。关闭 join_gate 时回复、
触发模式、proactive 与 0.2.0 完全一致。

### Added

- **自主加入（join_gate）**：群聊未 @ 文本消息先过闸门，再由一次 LLM 调用产出
  **`action` = `join` / `wait` / `skip`**、`addressed_to`、`confidence` / `reason` 与
  1–8 个话题（`title` / `summary` / `message_ids`，可选 `type` / `status` / `resumable`）；
  只有 `join` 进入原回复管线，`wait` / `skip` 只写 decision、不回复；
  解析失败 / 超时 / 非法 `action` / 非法 `status` 一律 fail-closed。
- **引用机器人识别**：入站的 `ref_msg_idx` 与本地 `messages.quote_index` 比对（只认本群、
  assistant 消息），命中即 `AtBot` / `ReplyToBot`，与 @ 一样**跳过 join gate**；
  查询 1s 超时，失败只记 DEBUG、按未命中处理（fail-open，不影响回复）。
- **`wait` 重试**：上次模型判定为 `wait` 且距那次评估满 `join_gate_wait_retry_seconds`（默认 30s）
  时，该群**下一条新消息**可绕过评估冷却提前重评估；不排定时器、不凭空产生模型调用，
  仍受每小时 / 每天加入上限与最小加入间隔约束。
- **硬闸门**：消息长度 ≥2 rune、评估冷却（默认 60s）、每小时 / 每天加入上限（3 / 12）、
  最小加入间隔（5 分钟）、置信度下限（0.7）；超限不调模型，但仍写 decision。
- **话题背景**：最近一次话题缓存（每群一行）在 15 分钟内、以 ≤400 字作为回复背景注入，
  放在 affect 块之后；不要求模型强行转回话题。
- **话题生命周期**：每个话题新增 `status`（`active` / `dormant` / `closed`）与
  `resumable`。代码按话题最新消息年龄**单向降级**：超过 `join_gate_topic_dormant_minutes`
  （默认 20）`active` → `dormant`，超过 `join_gate_topic_closed_minutes`（默认 180）
  任何非 `closed` → `closed`；时间不会把结束的话题升回来。`closed` 不可接回、
  `dormant` 只在被新消息明显接回时继续；上次话题（≤8 条 / 500 字）回灌给闸门模型，
  回复背景按状态标注，全部 `closed` 时不注入。
- **后台页**：新路由 `/join`（侧栏「自主加入」）—— 启用与 12 个参数、按群名选群、
  最近决策列表（插话 / 等一等 / 不插话）、最新话题面板（含状态 / 可接回）、一键启用 / 停用与成本提示。
- **图片（引用 / 合并 + 按需查看）**：入站时递归展开 `msg_elements`，把引用消息、合并 / 转发
  消息里的图片与当前消息图片一起缓存（sha256 内容寻址，同 URL 去重）；模型输入追加
  `[图片 #id]` / `[引用图片 #id]`，历史消息里的已缓存图片也会标（只进模型输入，不写正文）。
  新增只读工具 `view_image`（`media_id` + 可选 `question`），按需返回 ≤500 字描述；
  只在 `vision_enabled` 且工具开启时下发，复用 `human_status_vision_model`（空 = 主模型）。
  当前图片仍按 `vision_max_images` 内联，引用 / 合并图片不内联、只走工具；
  失败（id 失效、非图片、视觉未启用、未配置模型、调用失败）一律返回「图片不可用：…」文本，
  不阻断回复。
- **全站下拉**：新增 `SelectMenu`（按钮 + popover、键盘 Arrow / Enter / Escape、点击外部关闭、
  aria），替换管理后台所有原生 `<select>`；策略页按群覆盖改为「每群一张卡」的 2–3 列 grid
  （窄屏单列 + 搜索框），不再横向滚动。
- **设置**：`join_gate_enabled`（false）、`join_gate_mode`（`llm`）、`join_gate_model`（空）、
  `join_gate_window`（20）、`join_gate_confidence`（0.7）、`join_gate_eval_cooldown_seconds`（60）、
  `join_gate_wait_retry_seconds`（30）、`join_gate_topic_dormant_minutes`（20）、
  `join_gate_topic_closed_minutes`（180，必须大于 dormant）、`join_gate_min_interval_minutes`（5）、
  `join_gate_max_per_hour`（3）、`join_gate_max_per_day`（12）；
  `group_overrides[]` 新增 `join_gate`。
- **API**：`GET /api/join/decisions`（决策新增 `action` 字段）、`GET /api/join/topics`、
  `GET /api/groups/names?openids=a,b,c`（≤100，未知群返回空串）；
  `GET /api/status` 新增 6 个计数器（`join_gate_evals` / `join_gate_joins` /
  `join_gate_skips` / `join_gate_waits` / `join_gate_errors` / `join_gate_topics`）。
- **存储**：SQLite 迁移 `0014_join_gate`（`join_decisions` 含 `action` 列，老库自动补列；
  补 `idx_messages_quote_index`）；PostgreSQL `pgSchema` 新增 `join_decisions` /
  `join_topics` 两表、`action` 列与索引；会话删除 / 清空级联清理 decisions。
- **文档**：新增 [docs/JOIN-GATE.md](docs/JOIN-GATE.md) 与
  [docs/VISION.md](docs/VISION.md)；README 增加 §5.17 与 §5.9 的视觉工具说明；
  ADMIN-API 升到 v1.9（§6.8 + §5 群名端点 + 生命周期字段）；DECISIONS 增加 D-048
  （含 v2 生命周期取舍）与 D-049（图片工具）；AGENTS 增加第 21 条（引用识别、三分类与生命周期）
  与第 22 条（图片工具）。

### Changed

- `GET /api/status` 的 `counters` 由 **24 项变为 30 项**（新增 join_gate 6 项；既有字段不变）。
- `GET /api/join/decisions` 的每条 decision 新增 `action`（`join` / `wait` / `skip`）；
  `joined` 保留并由 `action=="join"` 派生，历史行 `action=""` 按 `joined` 回退。
  decision / topics 里的话题 JSON 增加 `status` 与 `resumable`；二者都是派生展示字段，
  老前端忽略即可。
- `GET /api/config` 的 `editable`、`PUT /api/config` 与 `/api/policies` 的按群覆盖
  接受 join_gate 键；配置键 golden 需同步（新增 `join_gate_wait_retry_seconds` /
  `join_gate_topic_dormant_minutes` / `join_gate_topic_closed_minutes`）。
- 图片入站缓存从顶层 `attachments` 扩展到 `msg_elements`（引用 / 合并），模型输入增加图片 id
  标注；新增 `view_image` 工具。视觉模型复用 `human_status_vision_model`，未新增配置键。
- `GET /api/policies` 的 `group_overrides[]` 增加 `join_gate`（`bool | null`）；
  全局 `join_gate_enabled` 是总开关，群覆盖只能进一步关闭或显式开启，不能越过全局关。
- 后台导航新增「自主加入」（`/join`）；策略与加入页改用 `SelectMenu` 与群名卡片；
  前端需同步 `web/dist` 产物。

### Security

- join_gate **默认关闭**；全局 `join_gate_enabled` 是总开关，群覆盖不能越过全局关。
- **只处理群聊**：私聊、命令、空消息、机器人自己的消息、其他机器人的普通发言都不评估；
  join_gate 排在 trigger gate 之前：开启后 `at_only` 下被判定加入的未 @ 消息也会回复，
  关闭时行为不变；`@` 消息与**引用机器人**的消息永远跳过 join_gate。
- **引用识别 fail-open、join gate fail-closed**：前者 1s 超时、失败只 DEBUG、按未命中处理，
  绝不拖慢或中断回复；后者解析失败 / 超时 / 非法 JSON / 非法 `action` / 并发额度拿不到时
  一律不加入，失败不能让一条本可回复的消息失败。
- **`wait` 不是定时器**：只为「话题还在发展」提供出口，重评估必须由该群的新消息触发，
  不会在安静的群里凭空调用模型。
- decision 里的消息预览 **≤80 字**，日志不记消息全文；不把系统提示词交给模型，
  模型只输出 JSON，不生成回复。`addressed_to` 只进提示词与 INFO 日志（加入成功时），不落新列。
- 群名端点只读本地 `conversations.group_name`，不调平台、不新增存储；未知群返回空串。
- **图片只在 `vision_enabled` 时进入模型**：当前图片受 `vision_max_images` 限制，引用 / 合并
  图片不内联；`view_image` 只读、只在工具开启时下发，失败只返回文本、不阻断回复。
  图片仍只从 QQ 附件域名抓取、受 `media_max_bytes` 限制；工具审计只记 `media_id` /
  `question`（截断 500 字），**不落图片描述**；不提供跨会话图片列表或搜索。
- 不做用户画像、不写记忆、不跨群聚合；不做骚扰 / 广告 / 引流；
  频率是硬闸门（评估冷却、每小时 / 每天上限、最小间隔、置信度）。
- 不做向量聚类、不建 embedding / 向量库，没有规则版判定。
- **话题生命周期只做展示与提示**：`status` / `resumable` 不参与频率、安全或权限判断；
  `closed` 是提示词 + `resumable=false` 的输入约束，不是代码层硬过滤。

## [0.2.0] - 2026-11-10

情绪状态（affect）框架与内置 `daily` 内容包。**默认关闭**；关闭时回复、润色与主动消息
与 0.1.0 完全一致。

### Added

- **情绪状态框架**：按会话持久化的 5 维状态（心情 / 精力 / 压力 / 熟悉度 / 耐心），
  半衰回归、单向 cap 耦合、越界指数回落、`dt` 封顶；事件分类带否定 / 假设 / 疑问 / 长度门，
  同类事件 1 小时内递减、24 小时滚动上限、单次增量封顶。
- **内置 `daily` 内容包**：基线、半衰期、耦合、事件正则与增量、注入片段 / 自述 / 时段文案、
  行为档与预设；通过 `go:embed` 打进二进制，运行时只读。
- **注入三档**：`off` / `line`（≤25 tokens）/ `full`（≤`affect_inject_max_tokens`，默认 120）；
  动态文案消毒、限长、不注入数值与用户原文；读设置 / 选包 / 渲染 / 超时失败返回空串，
  **不影响回复**。
- **行为层**：回复字数系数（下限 40）、分段上限、人味停顿系数、未 @ 沉默概率增量、
  proactive 概率系数；proactive 在精力 / 心情过低或暂停时跳过本轮且不扣额度。
- **后台页**：`/affect`（侧栏「情绪状态」）—— 启用开关、内容包、注入档位、分类器、tick、
  token 上限、5 维条、历史 SVG 曲线、事件列表、注入预览、暂停 / 恢复 / 预设 / 重置。
- **管理员命令**：`/mood`（别名 `mood`），用法
  `/mood [status|pause 分钟|resume|preset 名称|reset]`；不调模型、不写会话上下文。
- **设置**：`affect_enabled`（false）、`affect_pack`（`daily`）、
  `affect_inject_mode`（`line`）、`affect_event_classifier`（`rules`）、
  `affect_tick_seconds`（300，60–1800）、`affect_inject_max_tokens`（120，40–300）；
  `group_overrides[]` 新增 `affect` 与 `affect_inject_mode`。
- **存储**：SQLite 迁移 `0013_affect`、PostgreSQL `pgSchema` 新增 `affect_states` /
  `affect_events` / `affect_history` 三表与索引；状态写入使用乐观锁。
- **API**：7 组 / 9 个 `/api/affect/*` 端点；`GET /api/status` 新增 5 个计数器
  （`affect_ticks` / `affect_events` / `affect_injections` / `affect_inject_chars` /
  `affect_errors`）。
- **文档**：新增 [docs/AFFECT.md](docs/AFFECT.md)；README 增加 §5.16；ADMIN-API 升到 v1.8；
  DECISIONS 增加 D-047；AGENTS 增加第 20 条；`docs/assets/affect-page.svg` / `.png` 后台页示意。

### Changed

- `GET /api/status` 的 `counters` 由 **19 项变为 24 项**（新增 affect 5 项；既有字段不变）。
- `GET /api/config` 的 `editable`、`PUT /api/config` 与 `/api/policies` 的按群覆盖
  接受 affect 键；配置键 golden 同步。
- README、`internal/config.Version`、Dockerfile 与 `web/package.json` 统一为 **0.2.0**。

### Security

- affect **默认关闭**；全局 `affect_enabled` 是总开关，按群可进一步关闭或调档，群里的 `true` 不能越过全局关。
- 不做性 / 占有 / 成人内容；不推断年龄、性别、健康、政治、宗教、性取向等敏感属性；
  不写用户画像与长期记忆；不用内疚、恐惧或排他话术操纵用户。
- 注入文案按不可信输入消毒（去换行 / 引号 / 括号、限 40 字），只作语气参考；
  注入失败绝不影响回复。
- 事件日志保留 7 天、历史点保留 30 天，由维护循环清理；日志不记录消息原文；后台页与命令都要求管理员。

## [0.1.0] - 2026-11-06

首个公开版本。单个 Go 二进制 + 单容器交付的 QQ 群 AI 机器人，内置 Web 管理后台。

### Added

- **QQ 接入**：QQ 机器人开放平台 API v2；WebSocket 网关（默认）与 Webhook 双事件通道；
  多机器人共存时的身份判定（不在被 @ 他人时抢话）。
- **模型接入**：任意 OpenAI 兼容端点；模型列表探测与后台选择；多模态图片输入与失败降级。
- **回复管线**：两阶段生成（草稿 + 无上下文润色，失败回退草稿）、提前回复与撤回、
  token/用时页脚、人味停顿、长回复分段。
- **上下文**：触发模式（仅 @ / 全部消息）与上下文范围（仅对己 / 含旁观）分离；
  C1 每人独立 / C2 整群共享；同群会话合并。
- **Bot 命令**：聊天内命令框架（管理员身份门、只认 @/私聊、不进对话管线）、
  `/tokenstats`、公开的 `/help`、QQ 指令面板同步与幂等对账。
- **能力**：工具调用循环、长期记忆、承诺（open loop）追问、定时任务、
  人类状态推测（含表格 PNG 渲染）、内置 SearxNG 联网搜索、可选容器内 `run_shell`（默认关闭 + 身份双控）。
- **管理后台**：Vue 3 + Tailwind SPA，REST + SSE；概览、会话、消息审计、策略、日志、
  模型与提示词、回复与人味、能力、主动消息、接入与运维、系统与诊断。
- **存储**：PostgreSQL 17 为主（`pgx/v5`，原生 `TIMESTAMPTZ`），SQLite 为回退；
  版本化迁移、SQLite → PostgreSQL 全量搬运与零丢失校验。
- **安全与工程质量**：密钥只进不出、DSN 口令遮蔽（redacted）、SSRF 地址策略、
  CI（vet/test/race/前端/产物一致/隐私扫描）、GPG 签名提交约定。
- **部署**：多阶段 Dockerfile（非 root、无额外 apk 依赖）、docker compose
  （PostgreSQL + SearxNG sidecar）、健康检查子命令、PG 备份/恢复脚本。

### License

- Apache License 2.0。第三方组件与字体许可见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
