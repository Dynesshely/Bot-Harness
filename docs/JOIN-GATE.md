# 自主加入（join_gate）

群里的**未 @ 消息**默认只会被记录，不会触发回复。开启 join_gate 后，机器人会先问一次模型：
「这条消息值不值得接，最近的群聊在聊哪几个话题？」——**只有判定值得时才进入回复管线**。
判定与话题切分在**同一次 LLM 调用**里完成，不额外多花一次请求。

- 代码：`internal/core/join_gate.go`（闸门、提示词、解析）、`internal/store/join.go`（两张表）、
  `internal/admin/join_gate.go`（REST）、`web/src/pages/` 下的 `/join` 后台页。
- 开关：`join_gate_enabled`，默认 **false**。关闭时与 0.2.0 行为一致：不评估、不写表、不注入话题背景。
- 只作用于**群聊的未 @ 文本消息**；私聊、@ 消息、命令、空消息、其它机器人的消息都不评估。
- 接口契约：[docs/ADMIN-API.md](ADMIN-API.md) §6.8；取舍记录：D-048。

> 本文描述 0.3.0 的契约。数值与判定结果都是**内部状态**，不写进用户可见消息；
> 唯一主动暴露给管理员的入口是后台 `/join` 页与两个只读端点。

## 1. 目标与非目标

### 目标

- **让「接不接话」成为一次显式判断**：不是「未 @ 就不回」或「未 @ 必回」的二选一，
  而是按消息内容、最近上下文与机器人能否补充来决定。
- **一次调用做两件事**：同一个 JSON 里同时给出 `join` 与 `topics`，避免为「要不要说」
  和「在聊什么」各花一次模型调用。
- **可解释**：每一次评估都落一条 decision（加入 / 跳过、置信度、原因、耗时、模型），
  后台能直接看到「为什么这轮没接」。
- **默认安全、默认省钱**：默认关；开启后有评估冷却、加入间隔与每小时 / 每天硬上限，
  超限时根本不调模型。
- **失败不插话**：解析失败、超时、并发额度拿不到时一律按「不加入」处理（fail-closed）。

### 非目标

- **不做向量聚类 / 语义索引**：话题切分是模型在**有限窗口**里的一次性结构化输出，
  不建 embedding、不建向量库、不做跨群聚类。
- **不做规则版判定**：没有 `join_gate_mode=rules`。当前只有 `off` 与 `llm` 两种模式。
- **不让闸门自己生成回复**：模型只输出 JSON（话题 + 判定），不输出回复正文；
  真正回复仍走原来的 process 管线（人设、工具、润色、限流都不变）。
- **不跨群**：判定与话题都以 `group_openid` 为界，一个群的上下文不会进入另一个群。
- **不是新的 trigger_mode**：@ 语义完全不变；join_gate 只在**显式启用**后，在 trigger gate
  之前对未 @ 群消息做一次内容判断。`at_only` 下被判定加入的未 @ 消息会继续走回复管线
  （这是功能本身），关闭时 `at_only` 行为与 0.2.0 一致。

## 2. 与 trigger_mode、proactive 的区别

三件事经常被混在一起，实际是三条独立的判断：

| 机制 | 回答的问题 | 触发源 | 默认 | 与 join_gate 的关系 |
|---|---|---|---|---|
| **trigger_mode** | 未 @ 消息**要不要直接丢弃** | 入站消息 | `at_only` | join_gate 排在它之前；开启后由 join_gate 先决定，判定加入时绕过 trigger gate |
| **join_gate** | 这条**未 @ 消息值不值得接** | 入站消息 | 关 | 两种 trigger_mode 下都会先评估未 @ 消息；关闭时不运行 |
| **proactive** | **没有新消息时**要不要主动说一句 | 额度 / 定时器 | `off` | 完全独立：join_gate 是「被动消息的接话判断」，不消耗也不增加 proactive 额度 |

更具体地说：

- `trigger_mode=at_only`：关闭 join_gate 时只有 @ 消息会走到回复。开启后，未 @ 消息先过
  join_gate；判定加入的继续回复，其余丢弃。也就是说，join_gate **扩展**了 `at_only` 的
  接话范围，而不是只收窄 `all_messages`。
- `trigger_mode=all_messages`：未 @ 消息原本会进入回复管线（再受人味随机沉默影响）。
  开启 join_gate 后，这些消息先过一道内容判断；判定不加入就 `observe` 后丢弃，判定加入则
  跳过本轮的随机沉默（`joinedByGate`）。
- **@ 消息永远优先**：@ 机器人、命令、私聊都不经过 join_gate，行为与关闭时完全一致。
- join_gate 是**显式开启的能力**：默认关时 `at_only` / `all_messages` 的行为与 0.2.0 相同。
- proactive 是「机器人自己找话说」，受额度、静默时段与最小间隔约束；join_gate 不读写这些状态，
  也不会把一次「加入」记成主动消息。

## 3. 流程

一条群消息的处理顺序（自外向内）：

```text
幂等 / 自身消息过滤
        │
        ▼
屏蔽词（blocked keyword）──命中──▶ ignored
        │
        ▼
join_gate 触发门：群聊 && 未 @ && join_gate_enabled && mode != off
        │                                   │
        │ 不满足                             │ 满足
        ▼                                   ▼
trigger gate（at_only / all_messages）   闸门预检（长度 / 冷却 / 频率）
        │                                   │
        │                                   ├─ 超限 ──▶ 写 decision(joined=false) → observe → drop
        │                                   │
        │                                   ▼
        │                             取最近窗口消息 + 当前消息
        │                                   │
        │                                   ▼
        │                             一次 LLM 调用（JSON）
        │                                   │
        │                         ┌─────────┴─────────┐
        │                         │                   │
        │                    join=false           join=true 且置信度达标
        │                         │                   │
        │                         ▼                   ▼
        │                  写 decision → observe   写 decision + 保存 topics
        │                  → drop("join_gate")     → processMessage（跳过随机沉默与 trigger gate）
        ▼
processMessage（人设 / 工具 / 润色 / 发送）
```

关键点：

- join_gate 在**屏蔽词之后、trigger gate 之前**。被屏蔽的消息不会浪费一次模型调用。
- 预检超限时**不调模型**，但仍写一条 `joined=false` 的 decision，原因以 `skip:` / `cooldown:` /
  `rate:` 开头。
- 判定加入后，`joinedByGate=true`，后续 trigger gate（`at_only` 丢弃）与人味随机沉默都
  不再生效；但模型的并发上限、QPM、分段、润色等既有约束照旧。
- 关闭 join_gate 时这个分支完全不执行，`at_only` / `all_messages` 与 0.2.0 一致。
- 判定不加入时，消息仍按「未 @ 群消息」落库为 `observed`，只是不进上下文、不触发回复。

## 4. LLM prompt 与 JSON 结构

### 4.1 输入

- **最近窗口**：`join_gate_window` 条群消息（默认 20，5–50），按时间升序拼接；
  每条截断到 **200 rune**，格式为 `[#消息id] 昵称: 内容`。
- **当前消息**：作为窗口里的最后一条参与判断；它没有数据库 id 时以 `#0` 表示。
- **机器人最近的 2 条回复**（同一会话）作为参考，标记为 `[bot]`，让模型知道「刚才是不是已经说过」。
- 只拼群聊文本；不拼系统提示词、工具结果、记忆、affect 状态或私聊内容。

### 4.2 提示词要点

```text
你在判断群聊里的一条消息是否值得机器人主动接话，并顺便把最近的群消息切成话题。

只输出一个 JSON 对象，不要输出解释、Markdown 代码块或回复正文。

判定为 join=true 的情形仅限：
- 有人点名 / 回复机器人，且机器人能补充有用信息；
- 当前话题延续，机器人有新的、具体的内容可加；
- 明确求助，或情绪上需要接住；
- 机器人刚刚参与过该话题，继续说完是自然的。

以下一律 join=false，且默认倾向不插话：
闲聊寒暄、明显在互相私聊、其他机器人之间的对话、争吵、广告 / 引流、
机器人没有增量信息、会打断当前对话、内容不适合参与。

话题要求：
- 1–8 个话题；每个话题给出 id（t1、t2…）、title（≤30 字）、summary（≤80 字）与 message_ids；
- message_ids 只能使用输入里出现过的 id，不能编造；
- current_topic_id 是当前消息所属的话题；不属于任何话题时留空字符串。
- confidence 是 0–1 的小数；reason 是一句中文短句（≤200 字），给管理员看，不要复述消息原文。
```

调用参数：`temperature=0.1`、`max_tokens=600`、超时 **10s**；并发信号量最多等 **2s**，
拿不到就不评估（不阻塞回复请求）。模型取 `join_gate_model`，空串 = 主模型。

### 4.3 输出

```json
{
  "topics": [
    { "id": "t1", "title": "周末聚餐", "summary": "几个人在约周六晚饭的时间和地点。", "message_ids": [12, 13, 14] },
    { "id": "t2", "title": "模型选型", "summary": "在比较本地部署与调用云端的成本。", "message_ids": [15, 16] }
  ],
  "current_topic_id": "t1",
  "join": true,
  "confidence": 0.82,
  "reason": "有人在问群里有没有人懂这家店，且刚才 bot 参与过这个话题"
}
```

约束：

| 字段 | 类型 | 约束 |
|---|---|---|
| `topics` | array | 必须 1–8 个；不足 / 超出 → 整次解析失败 |
| `topics[].id` | string | 本次调用内唯一，建议 `t1`…`t8` |
| `topics[].title` | string | 目标 ≤30 字；超长截断 |
| `topics[].summary` | string | 目标 ≤80 字；超长截断 |
| `topics[].message_ids` | int array | 必须是输入里出现过的 id；任一非法 → 整次解析失败 |
| `current_topic_id` | string | 可为空；通常指向 `topics[].id`，实现不做一致性校验 |
| `join` | bool | 缺失 → `false`；类型错误 → 解析失败（fail-closed） |
| `confidence` | number | 0–1；低于 `join_gate_confidence` 时按不加入处理 |
| `reason` | string | ≤200 字；用于 decision 与控制台展示 |

解析失败、超时、非法 JSON、字段类型错误：**fail-closed**，按不加入处理，
decision 记 `joined=false` 且 `reason="gate error: …"`。不重试、不降级成规则判定。

## 5. 话题切分规则

话题不是独立的第二次分析，而是**这一次判定调用的副产物**：

- 模型把窗口里的消息按「在聊什么」聚成 1–8 个话题；同一个话题内的消息应能共用一句 summary。
- `message_ids` 只允许引用**输入里出现过的 id**；出现编造的 id 会让**整次解析失败**
  （fail-closed），不会写进 `join_topics`。
- 一条消息可以出现在多个话题里（例如一句话同时涉及两个话题）；不强制互斥。
- `current_topic_id` 用来标记「当前这条消息属于哪个话题」；为空表示没有合适归属，
  不影响 `join` 的判定。
- 解析成功时保存到 `join_topics`（每个群一行，覆盖上一次），供后续回复作为背景；
  解析失败时**不覆盖**上一次的话题缓存。

话题背景的使用位置：

- 只在该群 `join_gate` 生效（`cfg.JoinGateEnabled`）时注入；`mode=off` 不再产生新缓存，
  但 15 分钟内已有缓存在 `mode=off` 下仍可能被注入。
- 只取**最近 15 分钟**内分析过的缓存，最多 **400 字**；
- 追加在 affect 注入块之后，包装成：
  `群里最近的话题（仅背景，不要强行转回）：…`
- 它只是背景，不是指令；模型不需要在回复里提及话题，也不应为了「用上话题」而硬转。

## 6. 闸门

顺序固定：前 5 道在调模型之前，任何一道不通过都直接结束本次评估（不调模型）；
置信度在模型返回后判断。

| 顺序 | 闸门 | 默认 | 范围 | 说明 |
|---|---|---|---|---|
| 1 | 消息长度 | — | ≥2 rune | 空消息、单字、纯表情不评估 |
| 2 | 评估冷却 | 60s | 10–3600 | 同群两次评估的最小间隔；依据该群最近一条 decision 时间 |
| 3 | 每小时加入上限 | 3 | 1–30 | 统计最近 1 小时 `joined=true` 的 decision |
| 4 | 每天加入上限 | 12 | 1–100 | 统计最近 24 小时 `joined=true` 的 decision |
| 5 | 最小加入间隔 | 5 分钟 | 1–240 | 两次真正加入之间的最小间隔 |
| 6 | 置信度 | 0.7 | 0–1 | 模型 `join=true` 且 `confidence ≥` 阈值才加入 |

- **闸门是硬限制**：达到每小时 / 每天上限后，即使模型认为值得接也不会加入；
  后台计数器会体现为 skip。
- 冷却按「评估」而不是「加入」计算：短时间内连续来消息只评估一次。注意跳过的 decision
  也会更新冷却基准，所以持续刷屏的群可能一直停留在冷却里。
- 超限的跳过**不调模型**，但仍写 decision，`reason` 以 `cooldown:` / `rate:` 开头，
  便于在后台区分「模型不想接」与「系统不让接」。
- 评估失败（HTTP / 解析 / 超时）也写 decision（`joined=false`），不会无限重试。

## 7. 设置键

九个键都在「系统设置 → 能力」与后台 `/join` 页可改，走 `PUT /api/config` 同一条路径。

| 键 | 类型 | 默认 | 范围 / 枚举 | 说明 |
|---|---|---|---|---|
| `join_gate_enabled` | bool | **false** | — | 全局总开关；关闭时零变化，群覆盖不能越过它开启 |
| `join_gate_mode` | string | `"llm"` | `off` / `llm` | 当前只实现 LLM 判定；`off` 表示不进入 join gate，未 @ 消息仍走原 trigger gate |
| `join_gate_model` | string | `""` | 非空模型名或空串 | 空串 = 复用主模型 `model` |
| `join_gate_window` | int | **20** | [5,50] | 参与判定与话题切分的最近群消息条数 |
| `join_gate_confidence` | float64 | **0.7** | [0,1] | 加入所需的模型置信度下限 |
| `join_gate_eval_cooldown_seconds` | int | **60** | [10,3600] | 同群两次评估之间的最小间隔 |
| `join_gate_min_interval_minutes` | int | **5** | [1,240] | 两次加入之间的最小间隔 |
| `join_gate_max_per_hour` | int | **3** | [1,30] | 每小时最多加入次数 |
| `join_gate_max_per_day` | int | **12** | [1,100] | 每 24 小时最多加入次数 |

按群覆盖（`group_overrides[]`）新增一个字段：

| 字段 | 类型 | 语义 |
|---|---|---|
| `join_gate` | `bool / null` | `null` = 跟随全局；`false` = 该群强制关闭；`true` = 该群显式开启（仍需全局开启） |

覆盖优先级：

1. 全局 `join_gate_enabled=false` → **所有群都不生效**；群里的 `true` 不能越过总开关。
2. 全局开启后，群 `join_gate=false` → 该群关闭；`true` / `null` → 该群开启。
3. 其余 8 个键只有全局值，没有按群版本。
4. 群覆盖保存时必须**原样透传**：前端不能重建白名单，否则改一行 QPM 会把别的群的
   join_gate 设置抹掉。

## 8. 后台与 API

### 8.1 后台页

新路由 `/join`，侧栏「自主加入」；「系统设置 → 能力」里也有一行入口与说明。页面包含：

- 启用开关、模式、模型、窗口、置信度、评估冷却、最小间隔、每小时 / 每天上限
  （保存走 `PUT /api/config`，只提交脏字段）；
- 群选择（从 `/api/conversations` 去重 `group_openid`，或手工输入）；
- 最近决策列表：加入 / 跳过、置信度、原因、时间、消息预览、所属话题；
- 最新话题面板：标题 / 摘要 / 消息 id；
- 一键启用 / 停用，以及「默认关、每次评估会消耗一次模型调用」的成本提示。

### 8.2 REST

两个只读端点，全部 protected，响应沿用 `{ok, data}` 信封；字段与错误码见
[docs/ADMIN-API.md](ADMIN-API.md) §6.8。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/join/decisions?group_openid=&limit=` | 最近判定记录；空 `group_openid` = 全部群 |
| GET | `/api/join/topics?group_openid=` | 该群最近一次话题切分缓存 |

- 没有写端点：启用与参数仍走 `PUT /api/config`，避免再开一套配置入口。
- 参数非法 → `400`；存储 / 服务未接线 → `503`；未认证 → `401`。

### 8.3 计数器

`GET /api/status` 的 `counters` 新增 5 项（由 24 项变为 **29** 项）：

| 计数器 | 含义 |
|---|---|
| `join_gate_evals` | 实际调用模型的评估次数（预检超限不计） |
| `join_gate_joins` | 判定为加入并进入回复管线的次数 |
| `join_gate_skips` | 预检超限或判定不加入的次数 |
| `join_gate_errors` | 读库 / 模型 / 解析 / 超时失败次数 |
| `join_gate_topics` | 成功保存话题切分的次数 |

## 9. 存储

两张新表，SQLite 迁移名 `0014_join_gate`；PostgreSQL 在 `pgSchema` 里同步建表。

| 表 | 主要列 | 说明 |
|---|---|---|
| `join_decisions` | `id` 自增、`group_openid`、`conversation_id`、`message_id`、`speaker`、`preview`、`joined`、`confidence`、`reason`、`topics` JSON、`model`、`latency_ms`、`created_at` | 每次评估一行；`preview` 只存截断后的消息预览 |
| `join_topics` | `group_openid` PK、`topics` JSON、`analyzed_at` | 每个群最近一次话题切分，覆盖式更新 |

- 索引覆盖 `(group_openid, created_at DESC)` 与 `conversation_id`；后台按群 + 时间倒序查询。
- 时间统一 RFC3339 字符串；PG 列为 `TIMESTAMPTZ`。
- **级联**：删除 / 清空会话时，按 `conversation_id` 清掉该会话的 decisions；
  清空全部会话时清空所有 join 数据。`join_topics` 是群级缓存，按群保留。
- 不新增消息状态：判定结果不写 `messages`，也不改 `records.status` 枚举。

## 10. 成本

- **默认关**：关闭时 0 次调用、0 张表写入、0 字符注入。
- **每次评估最多 1 次 LLM 调用**；一次调用同时产出判定与话题，不额外再调一次。
- 默认评估冷却 **60s**、每小时最多 **3** 次加入、每天最多 **12** 次加入；
  这些是**评估 / 加入**的上限，不是「每收到一条消息就调一次」。
- 输入窗口默认 20 条、每条 ≤200 rune，输出上限 600 tokens；单次评估通常是千级 prompt tokens。
- 话题缓存只保留最近一次，回复时最多注入 400 字；不会把整个窗口反复拼进每轮 prompt。
- 拿不到模型并发额度（2s）时直接跳过本轮，不排队、不阻塞其他回复。

## 11. 隐私与安全边界

- **默认关**：不启用时没有评估、没有 decision、没有话题注入；升级不会改变现有回复。
- **只处理群聊**：私聊（c2c）、命令、空消息、机器人自己的消息、其他机器人的普通发言都不评估。
- **preview 限长**：decision 里的消息预览最多 **80 字**；日志不记录用户原文全文。
- **不泄露系统提示**：模型只看到群消息窗口与机器人最近 2 条回复；提示词本身不进入 decision，
  也不返回给前端。模型只输出 JSON，不生成回复正文。
- **fail-closed**：解析失败、超时、非法 JSON、并发额度拿不到时一律不加入；
  **宁可沉默，不可乱插话**。
- **不操纵、不骚扰**：join_gate 只决定「接不接」，不生成广告、引流、骚扰或针对个人的追问；
  硬频率闸门限制单位时间内的加入次数。
- **不做用户画像**：不按成员聚合兴趣、不写 `memories`、不跨群共享判断；
  decision 是运维审计，不是用户档案。
- **管理员面**：`/join` 与两个端点都要求管理员身份；普通用户看不到判定与话题。

## 12. 排障

| 现象 | 先看 |
|---|---|
| 开关开了但从不加入 | `GET /api/join/decisions` 是否有记录；`group_overrides[].join_gate` 是否把该群关了；`join_gate_mode` 是否为 `off` |
| 完全没有 decision | 群消息是否 @ 了机器人（@ 消息跳过 join gate）；平台该群是否只推「仅 @机器人」；`join_gate_enabled`；`join_gate_mode` 是否为 `off`；是否被屏蔽词先拦掉；消息是否 <2 rune |
| 有 decision 但都是 skip | 看 `reason`：`cooldown` / `rate` 是闸门；`gate error` 是模型失败；否则是模型判定不值得接 |
| 一直说「冷却」 | 看 `join_gate_eval_cooldown_seconds` 与该群最近 decision 时间；确认 skip 记录是否也更新了冷却基准 |
| 加入太频繁 / 太贵 | 调低 `join_gate_max_per_hour` / `_max_per_day`，调大 `join_gate_eval_cooldown_seconds` 与 `_min_interval_minutes`；或直接关闭 |
| 话题不更新 | 只有**解析成功**的评估才覆盖 `join_topics`；模型失败时保留上一次缓存 |
| 回复里没有话题背景 | 缓存是否在 15 分钟内；`join_gate_enabled` 是否开启；注入是否被 400 字预算截掉 |

计数器（`GET /api/status`）：`join_gate_evals`（调模型次数）、`join_gate_joins`（加入次数）、
`join_gate_skips`（跳过次数）、`join_gate_errors`（失败次数）、`join_gate_topics`（话题保存次数）。
日志按 `join_gate` 关键字过滤；错误日志带 `group_openid` 与错误类型，不带消息原文。

## 13. 测试

| 层 | 覆盖 |
|---|---|
| 闸门 | 长度 <2、冷却、每小时 / 每天上限、最小间隔、置信度；超限不调模型但仍写 decision |
| 解析 | 合法 JSON、字段缺失 / 类型错误、topics 超 8 个、非法 message id、confidence 越界、超时 |
| fail-closed | HTTP 失败、非法 JSON、并发额度拿不到 → `joined=false`，回复不受影响 |
| 触发 | join_gate 在 trigger gate 之前：`at_only` 下选中的未 @ 消息也会加入，`all_messages` 下先过 gate；@ 消息跳过；关闭时与 0.2.0 逐字节等价 |
| 话题 | 缓存读写、15 分钟窗口、400 字预算、解析失败不覆盖 |
| 存储 | SQLite `0014_join_gate`、PG schema、按群 / 会话查询、级联清理 |
| 设置 | 9 个键的默认值 / 范围 / 枚举、golden、group override 透传 |
| API | 两个端点契约、`limit` / `group_openid` 校验、401 / 400 / 503、未接线 |
| 后台 | 页面 smoke、路由 / NAV、configContext 键、api mock |

提交前至少跑：

```bash
go vet ./... && go test ./... -count=1
go test -race -count=1 ./internal/core ./internal/store ./internal/settings ./internal/admin
cd web && pnpm test && pnpm typecheck
bash scripts/check-config-keys.sh
```

## 14. 限制

- **不跨群**：判定、话题、缓存、频率闸门都以 `group_openid` 为界；不会把 A 群的判断用到 B 群。
- **不做向量聚类**：没有 embedding、没有向量库、没有离线主题模型；话题是单次窗口内的模型输出。
- **没有规则版**：`join_gate_mode` 只有 `off` / `llm`，不支持纯正则 / 关键词判定。
- **不保证话题稳定**：同一批消息在不同时间调用可能得到不同切分；缓存只保留最近一次。
- **不能突破平台权限**：平台侧仍只推「仅 @机器人」的群，收不到未 @ 消息，join_gate 无从评估。
- **不是新的 trigger_mode**：它排在 trigger gate 之前，开启后 `at_only` 下被判定加入的
  未 @ 消息也会回复；关闭时两种模式都恢复 0.2.0 行为。
- **不是主动消息**：加入后走被动回复管线，受平台 5 分钟窗口与 5 条上限约束；
  join_gate 不提供 proactive 的额度、静默时段或跨轮主动性。
