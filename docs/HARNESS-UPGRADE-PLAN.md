# Harness 能力升级（v2 · 已批复）

> 状态：**已批复，按 P0→P5 实施中**。决策记录见 [DECISIONS.md](DECISIONS.md) 的 D-034。
> 目标：把「单轮问答机」升级成「有工具、能执行、有记忆、有人味、会主动说话」的 agent harness。

## 0. 隔离模型（用户批复 Q1：整个 harness 跑在容器里，不另建沙箱）

边界就是容器：非 root（uid 10001）、零 capability、seccomp/apparmor、无 docker socket，
只挂 `/data` 卷与只读的 `/app/certs`、`/app/prompts`。工具能力天然局限在容器内。

三处必须补的硬化（否则容器内部是敞开的）：

| 硬化 | 做法 | 理由 |
|---|---|---|
| `/app` 只读 | 镜像里改成 `root:root 0555` | 否则 `run_shell` 能改掉 `bot-harness` 二进制 |
| 子进程清洗 env | 工具进程只拿 `PATH/HOME/LANG/TZ` | 否则一句 `env` 就把 AppSecret 发进群 |
| 资源限制 | 超时 SIGKILL、`RLIMIT_CPU/AS/NPROC`、输出 8KB 截断、`nice` | 一条命令不能拖死容器 |

**残余风险（接受并记录）**：工具进程与 bot 同 uid，理论上可读 `/data/bot.db`（含 AI Key）。
彻底解决需要严格 rootless dockerd（宿主改造）或第二层沙箱（用户已否决）。
因此 `run_*` 类工具**默认关闭、按群 opt-in**，这个开关就是风险闸门。

## 1. 能力与分期

| 期 | 能力 | 核心内容 | 验收 |
|---|---|---|---|
| **P0** ✅ | 工具调用 | `ai` 协议（tools/tool_calls/流式拼接）、工具注册表、agent loop（6 步 / 100s / 单轮 4 个 / 结果 4k）、`now`·`calc`、`tool_calls` 审计 | 问「现在几点」走工具；关 `tools_enabled` 退回纯聊天 |
| **P0b** ✅ | **Markdown 输出** | `msg_type=2` + `markdown.content`（`content` 必须为空）、权限缺失自动降级、提示词同步放开 | 群里渲染加粗/列表；无权限时自动退回纯文本且只试一次 |
| **P1** ✅ | 记忆 | `memories` 表；作用域跟随上下文（群聊→群、单聊→该用户；`global` 仅管理员 @ 明确要求时写）；显式/抽取/手工三种写入；LIKE+trigram 混合检索；巩固与后台编辑 | 今天说「我养猫」明天还记得；「忘掉」立刻生效 |
| **P2** ✅ | 执行与联网 | `run_shell`（默认关）、`web_fetch`、`web_search`（SearxNG/可插拔）；SSRF 六条防线；结果标记不可信 | 能算/能抓页面；抓 `127.0.0.1`/内网被拒 |
| **P3** ✅ | 人味 | 延迟模型、分段节奏、persona/mood、沉默概率、风格守卫、记忆回调；`humanize_level=off/soft/medium/strong` | 主观体感改善，且可一键关 |
| **P4** ✅ | 委托式主动 | `scheduled_tasks` + 1s ticker + 租约 + grace 补发；`schedule/list/cancel_task`；后台任务页 | 「30 秒后提醒我」准点到达；重启不丢 |
| **P5** ✅ | 自发主动（默认关） | 比例预算（每 N 条人类消息换 1 条，默认 10，可调）+ 保险丝 | 静默时段绝不发；无回应自动降级 |

### P0b Markdown 的特殊约束（实测自官方文档）

- `msg_type=2` 时 **`content` 必须为空**，正文放 `markdown.content`；
- 需要**单独申请 Markdown 模板权限**：`304036` / `40034127` 就是没权限；
- 群消息不支持流式；`40054010` 表示不允许发 URL；
- 因此实现必须带**自动降级**：命中权限/参数错误时该条改发纯文本，并对 markdown 设置冷却，
  避免每条消息都先失败一次；
- 提示词要同步调整：现在明确禁止 markdown 排版，开启后应改为「允许轻量 markdown，
  仍禁表格/HTML/编造的链接」。

### P5 预算模型（用户批复 Q5：不设每日上限）

```
每收到 N 条「人类消息」累积 1 点额度（N 默认 10，可按群调）
每发 1 条自发消息消耗 1 点；额度上限 3 点（防止长期沉默后爆发）
```

只在能看到人类消息的模式下有效（`all_messages` / C2 `include_unaddressed`）；
`at_only` 群永远 0 额度 → 自发主动自然关闭，这是正确行为。

保险丝：静默时段 23:00–08:00、最小间隔 10 分钟、连续 2 次无人回应自动退回「只做 P4」、
每条主动消息写审计（含模型给的一句「为什么现在说」）、全局 kill switch。

## 2. 安全与预算（横切）

| 主题 | 措施 |
|---|---|
| 注入 | 工具结果包「不可信数据」；副作用工具与外部内容同轮隔离；`exec` 默认关 |
| 网络 | 容器可出网（要连云平台），因此 `web_fetch` 必须自带走 SSRF 校验；`exec` 不做网络隔离（用户已确认以容器为界） |
| 预算 | 每轮 6 步 / 100s / 单轮 4 个工具 / 结果 4k tokens；工具轮次单独计 token |
| 可观测 | `tool_calls` 审计 + SSE `tool` 事件 + 日志 `component=tools`；**工具进度不进群聊**（Q8） |
| 撤回 | 统一开关：`tools_enabled`、`markdown_enabled`、`memory_enabled`、`humanize_level`、`proactive_level`、`exec_enabled`、`web_enabled` |

## 3. 设置项（都进现有 DB 设置面板，支持按群覆盖）

```
tools_enabled / tools_max_steps / tools_max_parallel / tools_total_budget_seconds
markdown_enabled
memory_enabled / memory_extract_enabled / memory_max_per_scope
humanize_level / humanize_delay_scale / humanize_silence_probability
proactive_level (off|delegated|auto) / proactive_ratio / proactive_quiet_hours
exec_enabled / exec_timeout_seconds / exec_max_output_bytes
web_enabled / web_search_provider / web_search_base_url
```

## 4. 明确不做

不在容器内加第二层沙箱；不挂 docker socket；不做 MCP；不做全量成员名录；
不给从未交互过的群发消息；不做管理动作（踢人/禁言）；工具进度不进群聊。

## 5. 批复记录

| # | 问题 | 结论 |
|---|---|---|
| Q1 | 沙箱 | 不要沙箱；整个 harness 在容器内，以容器为隔离边界 |
| Q2 | 记忆作用域 | 跟随上下文（群聊/单聊）；`global` 仅管理员 @ 并明确要求记住 |
| Q3 | 自发主动 | 默认关，按群 opt-in |
| Q4 | 人味档位 | 中等 |
| Q5 | 主动消息上限 | 不设每日上限，改按人类消息比例（每 10 条换 1 条，可调） |
| Q6 | 联网工具 | 一期一起做 |
| Q7 | 工具开关默认值 | 总开关开；`run_*`/`schedule_*` 默认关 |
| Q8 | 工具进度可见性 | 只进日志，不发送 |
| — | Markdown | 增加发送侧 markdown 支持（含自动降级） |
