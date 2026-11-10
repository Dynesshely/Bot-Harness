# 自主加入（join_gate）

群里的**未 @ 消息**默认只会被记录，不会触发回复。开启 join_gate 后，机器人会先问一次模型：
「这条消息值不值得接，最近的群聊在聊哪几个话题，是在对谁说？」——模型给出
**三分类**（`join` 接手 / `wait` 等一等 / `skip` 不插话）与 `addressed_to`，
**只有 `action=join` 时才进入回复管线**。判定、话题切分与地址判断在**同一次 LLM 调用**里完成，
不额外多花一次请求。

- 代码：`internal/core/join_gate.go`（闸门、提示词、解析、话题生命周期）、`internal/store/join.go`
  （两张表 + 引用查询）、`internal/admin/join_gate.go`（REST）、`web/src/pages/` 下的
  `/join` 后台页。
- 开关：`join_gate_enabled`，默认 **false**。关闭时与 0.2.0 行为一致：不评估、不写表、不注入话题背景。
- 只作用于**群聊的未 @ 文本消息**；私聊、@ 消息、命令、空消息、其它机器人的消息都不评估。
- **引用机器人也算被寻址**：平台事件里的 `ref_msg_idx` 会换算成本地 assistant 消息的
  `quote_index`；命中即视为 `AtBot`，和 @ 一样跳过 join gate（见 §4.4）。
- **话题有生命周期**：每个话题带 `status`（`active` / `dormant` / `closed`）与 `resumable`；
  除模型判断外，代码还按**话题最新消息的年龄**做兜底降级（见 §5.1）。上一次的话题缓存会作为
  「上次话题」回灌给闸门模型，避免旧事重提。
- 接口契约：[docs/ADMIN-API.md](ADMIN-API.md) §6.8；取舍记录：D-048。

> 本文描述 0.3.0 的契约（v2：引用识别 + 三分类 + 话题生命周期）。数值与判定结果都是**内部状态**，
> 不写进用户可见消息；唯一主动暴露给管理员的入口是后台 `/join` 页与只读端点。

## 1. 目标与非目标

### 目标

- **让「接不接话」成为一次显式判断**：不是「未 @ 就不回」或「未 @ 必回」的二选一，
  而是按消息内容、最近上下文、**在跟谁说**与机器人能否补充来决定。
- **三分类而不是布尔**：模型在 `join` / `wait` / `skip` 里选一个。`join` 才是接手；
  `wait` 表示话题还在发展、现在插话会打断但可以稍后重看；`skip` 表示这轮不该出现。
  布尔只有「现在接」与「永远不接」，无法表达「再等等」。
- **一次调用做三件事**：同一个 JSON 里同时给出 `action` / `addressed_to` / `topics`，
  避免为「要不要说」「在对谁说」「在聊什么」各花一次模型调用。
- **可解释**：每一次评估都落一条 decision（`action`、置信度、原因、耗时、模型），
  后台能直接看到「为什么这轮没接」，并能区分「不想接」与「想等一等」。
- **话题会过期**：话题不是永久标签。模型给出 `active` / `dormant` / `closed` 与
  `resumable`；代码再按消息年龄单向降级，保证一个已经结束的旧话题不会因为模型没看见
  新消息而被无限续命。
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
| **join_gate** | 这条**未 @ 消息值不值得接**（join / wait / skip） | 入站消息 | 关 | 两种 trigger_mode 下都会先评估未 @ 消息；关闭时不运行 |
| **proactive** | **没有新消息时**要不要主动说一句 | 额度 / 定时器 | `off` | 完全独立：join_gate 是「被动消息的接话判断」，不消耗也不增加 proactive 额度 |

更具体地说：

- `trigger_mode=at_only`：关闭 join_gate 时只有 @ 消息会走到回复。开启后，未 @ 消息先过
  join_gate；判定加入的继续回复，其余丢弃。也就是说，join_gate **扩展**了 `at_only` 的
  接话范围，而不是只收窄 `all_messages`。
- `trigger_mode=all_messages`：未 @ 消息原本会进入回复管线（再受人味随机沉默影响）。
  开启 join_gate 后，这些消息先过一道内容判断；判定不加入就 `observe` 后丢弃，判定加入则
  跳过本轮的随机沉默（`joinedByGate`）。
- **@ 消息与「引用机器人」永远优先**：@ 机器人、**引用机器人**、命令、私聊都不经过
  join_gate，行为与关闭时完全一致。引用是平台侧的「回复某条消息」：当被回复的那条在本地
  是 assistant 消息时（`ref_msg_idx` → `messages.quote_index`），等价于被点名。
- join_gate 是**显式开启的能力**：默认关时 `at_only` / `all_messages` 的行为与 0.2.0 相同。
- proactive 是「机器人自己找话说」，受额度、静默时段与最小间隔约束；join_gate 不读写这些状态，
  也不会把一次「加入」记成主动消息。

## 3. 流程

一条群消息的处理顺序（自外向内）：

```text
入站解码：提取 ref_msg_idx → in.RefMsgIndex
        │
        ▼
引用识别：群聊 && RefMsgIndex 非空
        │        │
        │        └─ IsBotQuote 命中 ──▶ AtBot=true（与 @ 等价，跳过闸门）
        ▼
幂等 / 自身消息过滤
        │
        ▼
屏蔽词（blocked keyword）──命中──▶ ignored
        │
        ▼
join_gate 触发门：群聊 && 未 @（且未引用机器人）&& join_gate_enabled && mode != off
        │                                   │
        │ 不满足                             │ 满足
        ▼                                   ▼
trigger gate（at_only / all_messages）   闸门预检（长度 / 冷却 / 频率）
        │                                   │
        │                                   ├─ 超限 ──▶ 写 decision(action=skip) → observe → drop
        │                                   │
        │                                   ▼
        │                             取最近窗口消息 + 当前消息
        │                                   │
        │                                   ▼
        │                             一次 LLM 调用（JSON）
        │                                   │
        │             ┌─────────────────────┼─────────────────────┐
        │             │                     │                     │
        │        action=skip           action=wait          action=join 且置信度达标
        │             │                     │                     │
        │             ▼                     ▼                     ▼
        │      解析成功则存 topics    解析成功则存 topics    解析成功则存 topics
        │      写 decision(skip)      写 decision(wait)     写 decision(join)
        │      → observe               → observe             → processMessage（跳过随机
        │      → drop("join_gate")     → drop("join_gate")     沉默与 trigger gate）
        ▼
processMessage（人设 / 工具 / 润色 / 发送）
```

关键点：

- join_gate 在**屏蔽词之后、trigger gate 之前**。被屏蔽的消息不会浪费一次模型调用。
- **引用机器人 = 被寻址**：引用识别在**入站解码后、路由前**完成（早于幂等与屏蔽词），
  命中后 `AtBot=true`，
  与 @ 走同一条快路径（见 §4.4）。识别失败只记 DEBUG，退回原来的未 @ 语义，不影响回复。
- 预检超限时**不调模型**，但仍写一条 `action=skip` 的 decision，原因以 `skip:` / `cooldown:` /
  `rate:` 开头。
- 判定 `join` 后，`joinedByGate=true`，后续 trigger gate（`at_only` 丢弃）与人味随机沉默都
  不再生效；但模型的并发上限、QPM、分段、润色等既有约束照旧。
- **`wait` 与 `skip` 都只写 decision、不回复**，消息按「未 @ 群消息」落库为 `observed`，
  不进上下文、不触发回复。两者的区别只在语义与冷却：`wait` 记录「现在不接、稍后可以再看」，
  并允许在 `join_gate_wait_retry_seconds` 之后**提前重新评估**（见 §6）。
- 关闭 join_gate 时这个分支完全不执行，`at_only` / `all_messages` 与 0.2.0 一致。

## 4. LLM prompt 与 JSON 结构

### 4.1 输入

- **最近窗口**：`join_gate_window` 条群消息（默认 20，5–50），按时间升序拼接；
  每条截断到 **200 rune**，格式为 `[#消息id] 昵称: 内容`。
- **引用标注**：窗口按 `quote_index` 建索引，当前消息的 `in.RefMsgIndex` 在窗口内匹配：
  命中用户消息 → 该行追加 `（当前消息引用了这条）`，当前行追加 `（回复 [#id]）`；
  命中机器人 → 当前行追加 `（回复 你）`；窗口里找不到 → `（引用了窗口外的一条消息）`。
  模型因此知道「这句话是在接谁」，判断 `addressed_to` 时有据可依（不新增数据面）。
- **当前消息**：作为窗口里的最后一条参与判断；它没有数据库 id 时以 `#0` 表示。
- **机器人最近的 2 条回复**（同一会话）作为参考，标记为 `[bot]`，让模型知道「刚才是不是已经说过」。
- **上次话题回灌**：如果该群有话题缓存，会把上一次的话题（最多 **8** 个、整块不超过 **500** rune，
  每个摘要最多 **40** rune）放在窗口之前，标题形如
  `上次话题（分析于 01-02 15:04，仅用于判断是否旧事重提）：`，每条带
  `[进行中]` / `[暂歇，可自然接回]` / `[已结束，不要提]`；暂歇且 `resumable` 的再追加
  「（可接回）」。回灌**没有新鲜度窗口**——越旧的 closed 话题越需要提醒模型不要翻出来。
  读缓存失败只当没有这块参考，不影响判定。
- 只拼群聊文本；不拼系统提示词、工具结果、记忆、affect 状态或私聊内容。

### 4.2 提示词要点

```text
你在判断群聊里的最后一条消息是否值得机器人主动接话，并顺便把最近的群消息切成话题。

只输出一个 JSON 对象，不要输出解释、Markdown 代码块或回复正文。

先判断这条消息在对谁说（addressed_to）：
- bot：点名机器人、引用机器人、明确在等机器人回应；
- someone_else：在回复某个具体的人、两人在对话；
- group：公开抛给群里的问题或话题；
- unknown：判断不出来。

再在 join / wait / skip 里选一个：
- join：addressed_to=bot（被点名 / 被引用），或 addressed_to=group 的公开求助 /
  明确问群里，且机器人能补充有用信息；
- wait：话题还在发展（有人正在打字、问题还没定论、争论未结束），现在插话会打断，
  但过一会儿可能适合加入；
- skip：不该插话——闲聊寒暄、两人私聊、其他机器人之间的对话、争吵、广告 / 引流、
  机器人没有增量信息、内容不适合参与。

引用 / 点名 / 公开求助是强信号；两人私聊、其他 bot、广告、争吵中不插话；
话题还在发展时选 wait。
不要为了显得活跃而加入；不确定时选 skip。
confidence 是 0–1 的小数；reason 是一句中文短句（≤200 字），给管理员看，不要复述消息原文。

话题要求：
- 1–8 个话题；每个话题给出 id（t1、t2…）、title（≤30 字）、summary（≤80 字）与 message_ids；
- 每个话题可给 type：help / chat / question / plan / conflict / joke / other（缺省按 other）；
- 每个话题给 status：active（还在聊）/ dormant（暂时停了，新消息可以自然接回）/
  closed（已经结束），以及 resumable（这个话题还值不值得被新消息接回）；
- 输入里的「上次话题」是上一次分析的缓存（含它自己的 status）。沿用它的话题 id 与状态：
  closed 的话题不要再接、不要旧事重提；dormant 的话题只有当前消息明显接回它时才可 wait / join；
  active 的话题按 addressed_to 判断；
- message_ids 只能使用输入里出现过的 id，不能编造；
- current_topic_id 是当前消息所属的话题；不属于任何话题时留空字符串。
```

调用参数：`temperature=0.1`、`max_tokens=600`、超时 **10s**；并发信号量最多等 **2s**，
拿不到就不评估（不阻塞回复请求）。模型取 `join_gate_model`，空串 = 主模型。

### 4.3 输出

```json
{
  "topics": [
    { "id": "t1", "title": "周末聚餐", "summary": "几个人在约周六晚饭的时间和地点。", "message_ids": [12, 13, 14], "type": "plan", "status": "active", "resumable": true },
    { "id": "t2", "title": "模型选型", "summary": "在比较本地部署与调用云端的成本。", "message_ids": [15, 16], "type": "question", "status": "dormant", "resumable": true }
  ],
  "current_topic_id": "t1",
  "addressed_to": "group",
  "action": "join",
  "confidence": 0.82,
  "reason": "在问群里有没有人懂这家店，bot 能补充具体信息"
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
| `topics[].type` | string | 可选；`help` / `chat` / `question` / `plan` / `conflict` / `joke` / `other`；空或未知 → `other` |
| `topics[].status` | string | 可选；`active` / `dormant` / `closed`；空 → `active`；非法值 → 整次解析失败 |
| `topics[].resumable` | bool | 可选；`closed` 强制 `false`，`active` 强制 `true`，`dormant` 保留模型值 |
| `current_topic_id` | string | 可为空；通常指向 `topics[].id`，实现不做一致性校验 |
| `addressed_to` | string | `bot` / `group` / `someone_else` / `unknown`；缺失或未知 → `unknown` |
| `action` | string | **`join` / `wait` / `skip`**；缺失 / 非法 → 解析失败（fail-closed，按 `skip` 记录） |
| `confidence` | number | 0–1；`action=join` 时低于 `join_gate_confidence` 按 `skip` 处理 |
| `reason` | string | ≤200 字；用于 decision 与控制台展示 |

话题的 `status` / `resumable` 在解析后还会经过一次**年龄兜底**（§5.1），
缓存里保存的是**已经降级过**的状态；因此 API 与后台看到的是代码的最终判断，而不是模型的原始值。

`joined` 不再由模型直接给，而是**由 `action=="join"` 派生**：落库时同时写 `action` 与
`joined`，老版本读 `joined` 的查询行为不变，新 UI 直接显示三分类。

`addressed_to` 只参与模型的推理链与加入成功时的 INFO 日志，**不落列、不返回给前台**：
加列要同时改两种方言的 schema、迁移与契约，而它本身不改变判定结果。

解析失败、超时、非法 JSON、字段类型错误、`action` 非法值：**fail-closed**，按不加入处理，
decision 记 `action=skip`、`joined=false` 且 `reason="gate error: …"`。不重试、不降级成规则判定。

### 4.4 引用机器人识别

平台事件里的引用只有 `message_reference` 的 `REFIDX_…` 令牌（`ref_msg_idx`），
没人能直接从它看出「引用的是谁」。所以链路分三步：

1. **入站**：`in.RefMsgIndex = ev.MessageScene.Get("ref_msg_idx")`。
2. **换算**：群聊且非空时，用 `store.IsBotQuote(ctx, groupOpenID, refIdx)` 查本地消息：
   `SELECT 1 FROM messages m JOIN conversations c ON c.id=m.conversation_id`
   `WHERE c.group_openid=? AND m.quote_index=? AND m.role='assistant' LIMIT 1`。
   命中说明被引用的是**本群的机器人发言**。
3. **分流**：命中时 `in.AtBot=true` 且 `in.ReplyToBot=true`，于是这条消息走 @ 快路径，
   不进入 join gate；同时回复侧的引用回复（`quote_reply`）照旧生效。

约束：

- 查询带 **1s 超时**；超时或读库失败只记 DEBUG，**当作未命中**——引用识别失败不能拖慢或
  中断正常回复（fail-open 到原有未 @ 语义，与 join gate 自身的 fail-closed 方向相反）。
- 只认**本群、assistant 消息**：引用别的成员、引用别的群的消息都不算 `AtBot`。
- 只对**人类发送者**生效：`ev.Author.Bot` 的消息在更早的分支进入 bot 对话处理，不会标
  `ReplyToBot`——这与「其它机器人的普通发言不评估」一致。
- 审计上 `ReplyToBot` **不改既有 `records.status`**，只在日志里记录；
  它是「为什么这条未 @ 消息会被回复」的解释，不是一个新状态。
- 识别只解决「谁被引用」，不改变 `addressed_to` 的判断——后者由模型在看到窗口与引用标注后给出。

## 5. 话题切分规则

话题不是独立的第二次分析，而是**这一次判定调用的副产物**：

- 模型把窗口里的消息按「在聊什么」聚成 1–8 个话题；同一个话题内的消息应能共用一句 summary。
- `message_ids` 只允许引用**输入里出现过的 id**；出现编造的 id 会让**整次解析失败**
  （fail-closed），不会写进 `join_topics`。
- 一条消息可以出现在多个话题里（例如一句话同时涉及两个话题）；不强制互斥。
- 每个话题可带 `type`：`help`（求助）、`chat`（闲聊）、`question`（提问）、`plan`（约时间 / 计划）、
  `conflict`（争执）、`joke`（玩梗）、`other`（其它）。空或未知值一律归一为 `other`，
  后端与前端都不因它解析失败——它只是给人看的标签，不参与闸门。
- 每个话题还带 `status` 与 `resumable`：`status` 决定这个话题现在该怎么对待，
  `resumable` 只对 `dormant` 有意义（是否值得被新消息接回）。详见 §5.1。
- `current_topic_id` 用来标记「当前这条消息属于哪个话题」；为空表示没有合适归属，
  不影响 `action` 的判定。
- 解析成功时保存到 `join_topics`（每个群一行，覆盖上一次），供后续回复作为背景；
  解析失败时**不覆盖**上一次的话题缓存。

### 5.1 话题生命周期

话题不是一次性标签：模型每次都会重新切分，而同一个「周末聚餐」可能从进行中聊到结束。
为了避免旧话题被无限续命，每次解析后都跑一遍生命周期归一 + 年龄兜底，**缓存里存的是最终状态**。

| 状态 | 含义 | 对闸门 / 回复的影响 |
|---|---|---|
| `active` | 还在聊 | 按 `addressed_to` 正常判断；回复背景标「进行中」 |
| `dormant` | 暂时停了，但新消息可以自然接回 | 只有当前消息**明显接回**它时才可 `wait` / `join`；`resumable=true` 时回复背景标「暂歇，可自然接回」 |
| `closed` | 已经结束 | **不接、不旧事重提**；`resumable` 恒为 `false`；回复背景标「已结束，不要提」 |

规则：

- **模型建议，代码降级**：模型给出 `status`；代码只做两件事——把非法 / 空值归一
  （空 → `active`，非法 → 解析失败）、按年龄**单向降级**。时间流逝永远不会把
  `closed` 升回 `dormant` / `active`，也不会把 `dormant` 升回 `active`。
- **年龄怎么算**：取该话题 `message_ids` 里在当前窗口中能查到的最新消息时间；都查不到时
  退回**本次分析时间**（模型上一次看房间的时刻）。窗口里没有消息时间、时间解析失败或时钟偏差
  （消息时间在分析时间之后）时，保留模型给的状态，不擅自关闭。
- **两档阈值**：最新消息距今 ≥ `join_gate_topic_dormant_minutes`（默认 **20** 分钟）时，
  `active` 降为 `dormant`；≥ `join_gate_topic_closed_minutes`（默认 **180** 分钟）时，
  任何非 `closed` 话题都降为 `closed`。closed 阈值必须大于 dormant 阈值，
  否则两个档位没有意义（设置校验会拒绝）。
- **什么时候跑**：每次模型解析成功、写缓存之前跑一次（此时 `now` = 分析时间，等价于按窗口消息年龄
  判断）；读取缓存回灌给闸门 / 注入回复背景时，再以 `analyzed_at` 作兜底时间、以当前时间重跑一次，
  所以缓存放久了也会自然变旧。
- **closed 是输入约束，不是硬过滤**：代码强制 `resumable=false`、把「已结束，不要提」同时喂给
  闸门与回复模型，但**不会**在代码层直接把 `action=join` 改写成 `skip`；是否接回仍由模型在
  看到状态后判断。这样保留模型对反讽 / 明确重启旧话题的处理空间（见 §14）。
- **上次话题回灌**：闸门模型的输入会带上缓存里的上一次话题（§4.1），让「closed 不要翻出来」
  成为模型可见的约束；回灌不受 15 分钟新鲜度限制——越旧的 closed 越需要提醒。
- **回复背景**：只取最近 15 分钟内分析过的缓存；每条带状态标签，`type` 非 `other` 时追加类型；
  如果缓存里**所有话题都是 `closed`**，整块不注入（没有值得说的背景）。

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
| 2 | 评估冷却 | 60s | 10–3600 | 同群两次评估的最小间隔；依据该群最近一条**模型评估**的时间 |
| 3 | 每小时加入上限 | 3 | 1–30 | 统计最近 1 小时 `joined=true` 的 decision |
| 4 | 每天加入上限 | 12 | 1–100 | 统计最近 24 小时 `joined=true` 的 decision |
| 5 | 最小加入间隔 | 5 分钟 | 1–240 | 两次真正加入之间的最小间隔 |
| 6 | 置信度 | 0.7 | 0–1 | `action=join` 且 `confidence ≥` 阈值才加入 |

- **闸门是硬限制**：达到每小时 / 每天上限后，即使模型认为值得接也不会加入；
  后台计数器会体现为 skip。
- 冷却按「评估」而不是「加入」计算：短时间内连续来消息只评估一次。注意**模型评估**产生的
  decision（即使结果是 skip / wait / 失败）也会更新冷却基准，所以持续刷屏的群可能在冷却里
  停留较久；只有预检产生的 cheap skip 不推进基准。
- **`wait` 重试**：若该群最近一次**模型评估**的 `action=="wait"`，且距那次评估已满
  `join_gate_wait_retry_seconds`（默认 30，10–300），则这次**绕过评估冷却**提前重评估一次——
  语义是「上次觉得话题还在发展，现在再看一眼」。除这个例外，其余情况仍按
  `join_gate_eval_cooldown_seconds` 冷却。重试仍然受每小时 / 每天加入上限与最小加入间隔约束。
- 冷却只读**真正的模型评估**（`model<>''`）：预检超限写下的 cheap skip（`model=''`）
  不会把下一次真实评估推后，否则繁忙的群会永远停在冷却里。
- 超限的跳过**不调模型**，但仍写 decision，`reason` 以 `cooldown:` / `rate:` 开头，
  便于在后台区分「模型不想接」与「系统不让接」。
- 评估失败（HTTP / 解析 / 超时）也写 decision（`action=skip`、`joined=false`），不会无限重试。

## 7. 设置键

十二个键都在「系统设置 → 能力」与后台 `/join` 页可改，走 `PUT /api/config` 同一条路径。

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
| `join_gate_wait_retry_seconds` | int | **30** | [10,300] | 上次评估是 `wait` 时，满这个秒数后可绕过评估冷却提前重评估 |
| `join_gate_topic_dormant_minutes` | int | **20** | [5,240] | 话题最新消息超过这么久没有更新 → `active` 降为 `dormant` |
| `join_gate_topic_closed_minutes` | int | **180** | [30,1440] | 话题最新消息超过这么久没有更新 → 任何非 `closed` 话题降为 `closed`；必须大于 dormant 阈值 |

按群覆盖（`group_overrides[]`）新增一个字段：

| 字段 | 类型 | 语义 |
|---|---|---|
| `join_gate` | `bool / null` | `null` = 跟随全局；`false` = 该群强制关闭；`true` = 该群显式开启（仍需全局开启） |

> 这十二个键里，`join_gate_wait_retry_seconds`、`join_gate_topic_dormant_minutes` 与
> `join_gate_topic_closed_minutes` 是 v2 追加；前九个与按群开关属于 v1 契约。
> shadow keys golden（`internal/settings/testdata/config-keys.json`）与 ADMIN-API 已同步。

覆盖优先级：

1. 全局 `join_gate_enabled=false` → **所有群都不生效**；群里的 `true` 不能越过总开关。
2. 全局开启后，群 `join_gate=false` → 该群关闭；`true` / `null` → 该群开启。
3. 其余 11 个键只有全局值，没有按群版本。
4. 群覆盖保存时必须**原样透传**：前端不能重建白名单，否则改一行 QPM 会把别的群的
   join_gate 设置抹掉。

## 8. 后台与 API

### 8.1 后台页

新路由 `/join`，侧栏「自主加入」；「系统设置 → 能力」里也有一行入口与说明。页面包含：

- 启用开关、模式、模型、窗口、置信度、评估冷却、**wait 重试**、**话题休眠 / 结束阈值**、
  最小间隔、每小时 / 每天上限（共 12 个键；保存走 `PUT /api/config`，只提交脏字段）；
- 群选择：群名优先、无名字时回落 openid（群名由 `GET /api/groups/names` 批量解析），
  也可手工输入 openid；
- 最近决策列表：**插话 / 等一等 / 不插话**（`action` = join / wait / skip）、
  置信度、原因、时间、消息预览、所属话题及其 `type`；
- 最新话题面板：标题 / 摘要 / 消息 id / 类型 / **状态**（进行中 / 暂歇 / 已结束）；
  `dormant` 且 `resumable=true` 的话题标「可接回」，`closed` 的话题标「已结束，不要提」；
  这些字段来自 `/api/join/topics` 的话题 JSON，展示的是**已经过年龄兜底**的状态；
- 一键启用 / 停用，以及「默认关、每次评估会消耗一次模型调用」的成本提示。

### 8.2 REST

join 页用到三个只读端点（两个 join 端点 + 一个群名批量端点）。全部 protected，
响应沿用 `{ok, data}` 信封；join 端点的字段与错误码见 [ADMIN-API](ADMIN-API.md) §6.8，
群名端点见 §5。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/join/decisions?group_openid=&limit=` | 最近判定记录（新增 `action`）；空 `group_openid` = 全部群 |
| GET | `/api/join/topics?group_openid=` | 该群最近一次话题切分缓存（话题含 `type` / `status` / `resumable`） |
| GET | `/api/groups/names?openids=a,b,c` | 批量把群 openid 解析成群名（≤100 个；见 ADMIN-API §5） |

- 没有写端点：启用与参数仍走 `PUT /api/config`，避免再开一套配置入口。
- 参数非法 → `400`；存储 / 服务未接线 → `503`；未认证 → `401`。

### 8.3 计数器

`GET /api/status` 的 `counters` 新增 6 项（由 24 项变为 **30** 项）：

| 计数器 | 含义 |
|---|---|
| `join_gate_evals` | 实际调用模型的评估次数（预检超限不计） |
| `join_gate_joins` | 判定为加入（`action=join`）并进入回复管线的次数 |
| `join_gate_skips` | 没有进入回复管线的次数：预检超限、判定 `wait` / `skip`、评估失败 |
| `join_gate_waits` | 模型判定 `wait` 的次数（v2 新增） |
| `join_gate_errors` | 读库 / 模型 / 解析 / 超时失败次数 |
| `join_gate_topics` | 成功保存话题切分的次数 |

## 9. 存储

两张新表，SQLite 迁移名 `0014_join_gate`；PostgreSQL 在 `pgSchema` 里同步建表。

| 表 | 主要列 | 说明 |
|---|---|---|
| `join_decisions` | `id` 自增、`group_openid`、`conversation_id`、`message_id`、`speaker`、`preview`、`joined`、`action`、`confidence`、`reason`、`topics` JSON、`model`、`latency_ms`、`created_at` | 每次评估一行；`preview` 只存截断后的消息预览 |
| `join_topics` | `group_openid` PK、`topics` JSON、`analyzed_at` | 每个群最近一次话题切分，覆盖式更新；JSON 里含每个话题的 `type` / `status` / `resumable`（已降级） |

- `action` 是 v2 新列（`TEXT NOT NULL DEFAULT ''`）：SQLite 的 `0014_join_gate` 会在老库上
  `ALTER TABLE` 补列，PostgreSQL 的 `pgSchema` 直接建列；`joined` 保留并由 `action=="join"` 派生，
  历史行 `action=''` 在 UI 上按 `joined` 回退显示。
- 索引覆盖 `(group_openid, created_at DESC)` 与 `conversation_id`；后台按群 + 时间倒序查询。
- 时间统一 RFC3339 字符串；PG 列为 `TIMESTAMPTZ`。
- **级联**：删除 / 清空会话时，按 `conversation_id` 清掉该会话的 decisions；
  清空全部会话时清空所有 join 数据。`join_topics` 是群级缓存，按群保留。
- 不新增消息状态：判定结果不写 `messages`，也不改 `records.status` 枚举。
- 引用识别用的是既有 `messages.quote_index` 与索引 `idx_messages_quote_index`（v2 把它补进
  SQLite base schema + 迁移与 PG `pgSchema`），不新增表。

## 10. 成本

- **默认关**：关闭时 0 次调用、0 张表写入、0 字符注入。
- **每次评估最多 1 次 LLM 调用**；一次调用同时产出三分类、地址判断与话题，不额外再调一次。
- 默认评估冷却 **60s**、每小时最多 **3** 次加入、每天最多 **12** 次加入；
  这些是**评估 / 加入**的上限，不是「每收到一条消息就调一次」。
- **`wait` 不会自己定时重试**：只有该群**又来了一条新消息**、且距上次评估已满
  `join_gate_wait_retry_seconds`（默认 30s）时才会提前重评估；没有新消息就没有额外调用。
  因此「一个群一直 wait」的实际开销上限仍是「每 30 秒最多一次」，而不是每 30 秒一次。
- **引用识别只查本地库**（1s 超时、单条 `SELECT 1`），不产生模型调用。
- 输入窗口默认 20 条、每条 ≤200 rune，输出上限 600 tokens；单次评估通常是千级 prompt tokens。
- **上次话题回灌**额外最多 8 条、整块 ≤500 rune（摘要每条 ≤40 rune）；它只在闸门评估时拼入，
  不进入回复 prompt。
- 话题缓存只保留最近一次，回复时最多注入 400 字；不会把整个窗口反复拼进每轮 prompt。
- 拿不到模型并发额度（2s）时直接跳过本轮，不排队、不阻塞其他回复。

## 11. 隐私与安全边界

- **默认关**：不启用时没有评估、没有 decision、没有话题注入；升级不会改变现有回复。
- **只处理群聊**：私聊（c2c）、命令、空消息、机器人自己的消息、其他机器人的普通发言都不评估。
- **引用识别不新增数据面**：只按 `group_openid` + `quote_index` 查本地 `messages`
  （已存字段），不向外请求、不记录被引用内容；失败只记 DEBUG，不含消息原文。
- **preview 限长**：decision 里的消息预览最多 **80 字**；日志不记录用户原文全文。
- **不泄露系统提示**：模型只看到群消息窗口与机器人最近 2 条回复；提示词本身不进入 decision，
  也不返回给前端。模型只输出 JSON，不生成回复正文。
- **fail-closed**：解析失败、超时、非法 JSON、并发额度拿不到时一律不加入；
  **宁可沉默，不可乱插话**。
- **不操纵、不骚扰**：join_gate 只决定「接不接」，不生成广告、引流、骚扰或针对个人的追问；
  硬频率闸门限制单位时间内的加入次数。
- **不做用户画像**：不按成员聚合兴趣、不写 `memories`、不跨群共享判断；
  decision 是运维审计，不是用户档案。
- **生命周期不新增数据面**：`status` / `resumable` 来自模型输出与本地消息时间，
  只存在 `join_topics` / decision 的 JSON 与提示词里，不额外抓取内容、不对外请求。
- **管理员面**：`/join` 页与三个只读端点（join decisions / topics / groups names）
  都要求管理员身份；普通用户看不到判定、话题与群名。

## 12. 排障

| 现象 | 先看 |
|---|---|
| 开关开了但从不加入 | `GET /api/join/decisions` 是否有记录；`group_overrides[].join_gate` 是否把该群关了；`join_gate_mode` 是否为 `off` |
| 完全没有 decision | 群消息是否 @ 了机器人（@ 消息跳过 join gate）；**是不是引用了机器人**（引用命中同样跳过，日志 `reply-to-bot` / DEBUG）；平台该群是否只推「仅 @机器人」；`join_gate_enabled`；`join_gate_mode` 是否为 `off`；是否被屏蔽词先拦掉；消息是否 <2 rune |
| 有 decision 但都是 skip | 看 `action` 与 `reason`：`action=wait` 是模型想等；`cooldown` / `rate` 是闸门；`gate error` 是模型失败；`action=skip` 且无前缀是模型判定不该接 |
| 一直是 wait，从不回来 | 需要**该群再来新消息**才会重评估；距上次评估不满 `join_gate_wait_retry_seconds` 时仍按普通冷却；有人 @ 或引用机器人会直接跳过闸门回复 |
| 引用了机器人却没回 | 被引用的那条是否**本群、assistant** 且落库时带了 `quote_index`；进群 / 重启前写入的旧消息可能没有该字段；识别超时只 DEBUG，等同未命中 |
| 一直说「冷却」 | 看 `join_gate_eval_cooldown_seconds` 与该群最近一次**模型评估**时间（`model<>''`）；cheap skip 不更新冷却基准 |
| 加入太频繁 / 太贵 | 调低 `join_gate_max_per_hour` / `_max_per_day`，调大 `join_gate_eval_cooldown_seconds` 与 `_min_interval_minutes`；或直接关闭 |
| 话题不更新 | 只有**解析成功**的评估才覆盖 `join_topics`；模型失败时保留上一次缓存 |
| 话题刚分析就变 `dormant` / `closed` | 话题最新消息是否已经超过 `join_gate_topic_dormant_minutes` / `_closed_minutes`；`message_ids` 是否都落在当前窗口外（此时会退回分析时间）；检查两个阈值的大小关系 |
| 旧话题又被接回 | `closed` 是提示词 + `resumable=false` 的输入约束，不是代码硬过滤；看 decision 的 `topics` 状态与 `reason`。若模型仍反复翻旧账，可调低 `_closed_minutes` 或收紧话题相关设置 |
| 回复里没有话题背景 | 缓存是否在 15 分钟内；`join_gate_enabled` 是否开启；缓存是否全部为 `closed`；注入是否被 400 字预算截掉 |
| 群名显示成 openid | `GET /api/groups/names?openids=…` 是否返回空串；群名来自会话落库时平台给的名字，从没见过的群没有名字 |

计数器（`GET /api/status`）：`join_gate_evals`（调模型次数）、`join_gate_joins`（加入次数）、
`join_gate_skips`（未进入回复管线的次数，含 wait 与失败）、`join_gate_waits`（其中等一等的次数）、
`join_gate_errors`（失败次数）、`join_gate_topics`（话题保存次数）。
日志按 `join_gate` 关键字过滤；错误日志带 `group_openid` 与错误类型，不带消息原文。
引用识别失败只记 DEBUG；它永远不会把一条正常回复变成失败。

## 13. 测试

| 层 | 覆盖 |
|---|---|
| 闸门 | 长度 <2、冷却、wait 重试、每小时 / 每天上限、最小间隔、置信度；超限不调模型但仍写 decision |
| 解析 | 合法 JSON、字段缺失 / 类型错误、`action` 非法值、`addressed_to` 缺省、topic `type` 归一、topic `status` 非法值、topics 超 8 个、非法 message id、confidence 越界、超时 |
| fail-closed | HTTP 失败、非法 JSON、并发额度拿不到 → `action=skip` / `joined=false`，回复不受影响 |
| 引用 | `IsBotQuote` 命中 / 非 assistant / 跨群 / 未知 ref / 空 ref；命中后跳过闸门，失败按未命中 |
| 触发 | join_gate 在 trigger gate 之前：`at_only` 下选中的未 @ 消息也会加入，`all_messages` 下先过 gate；@ 与引用消息跳过；关闭时与 0.2.0 逐字节等价 |
| 话题 | 缓存读写、15 分钟窗口、400 字预算、解析失败不覆盖、`type` / `status` / `resumable` 透传 |
| 生命周期 | 空 status → active、非法 status 解析失败、closed 强制不可接回、active 强制可接回、按消息年龄 active→dormant→closed、单向不回升、无时间戳 / 时钟偏差保留模型判断、上次话题最多 8 条 / 500 rune、回复块全 closed 不注入 |
| 存储 | SQLite `0014_join_gate`（含 `action` 补列）、PG schema、`idx_messages_quote_index`、按群 / 会话查询、级联清理 |
| 设置 | 12 个键的默认值 / 范围 / 枚举、golden、group override 透传 |
| API | 两个 join 端点 + `/api/groups/names` 契约、`limit` / `group_openid` / `openids` 校验、401 / 400 / 503、未接线 |
| 后台 | 页面 smoke、路由 / NAV、configContext 键、api mock、`SelectMenu` 键盘与点击外部 |

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
- **生命周期是启发式，不是事实**：状态来自模型建议 + 本地消息年龄；话题的 `message_ids`
  都落在窗口外时会退回分析时间，长窗口 / 冷群可能比实际更早进入 `dormant` / `closed`。
  `closed` 不是代码层的硬性 action 过滤，模型仍可能在被明确重新提起时选择 `join`。
- **`wait` 不是定时器**：它只表达「此刻别插话」，下一次重评估仍要等该群有新消息；
  没有新消息就永远不会因为 wait 自己醒来。
- **引用识别只认本地已落库的 bot 消息**：平台侧撤回、或 `quote_index` 缺失的历史消息
  都会退回「未 @」语义，不会误判为被寻址。
- **不能突破平台权限**：平台侧仍只推「仅 @机器人」的群，收不到未 @ 消息，join_gate 无从评估。
- **不是新的 trigger_mode**：它排在 trigger gate 之前，开启后 `at_only` 下被判定加入的
  未 @ 消息也会回复；关闭时两种模式都恢复 0.2.0 行为。
- **不是主动消息**：加入后走被动回复管线，受平台 5 分钟窗口与 5 条上限约束；
  join_gate 不提供 proactive 的额度、静默时段或跨轮主动性。
