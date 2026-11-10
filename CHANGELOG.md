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

### Changed

- README 面向开源用户重写：移除内部环境与运维记录，补充徽章、架构、截图、贡献/安全/许可章节。
- `web/package.json`：公开包（`private: false`）、Apache-2.0、版本 `0.1.0`、仓库地址、
  锁定 `pnpm@11.25.0`。

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

- `GET /api/status` 的 `counters` 由 **18 项变为 23 项**（新增 affect 5 项；既有字段不变）。
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
