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
