# 安全策略

## 支持的版本

Bot Harness 目前处于 `0.x` 阶段。安全修复只针对最新发布版本提供。

| 版本 | 是否支持 |
|---|---|
| `0.1.x`（最新） | ✅ |
| 更早的 `0.x` | ❌ |

升级前请阅读 [CHANGELOG.md](CHANGELOG.md) 与 [docs/PG-MIGRATION.md](docs/PG-MIGRATION.md)
（涉及数据库迁移时）。

## 私密上报漏洞

**请使用 GitHub Security Advisories 私密上报，不要开公开 issue、不要在讨论区贴细节。**

1. 打开仓库的 **Security** 标签页。
2. 选择 **Advisories → Report a vulnerability**。
3. 填写受影响版本、复现步骤、影响与（如有）修复建议。

如果仓库尚未启用 Security Advisories，请通过维护者公开资料中的安全联系方式联系，
并在标题注明 `SECURITY`。

请**不要**在公开 issue / PR / 讨论中披露：

- 可被利用的漏洞细节或 PoC；
- 真实令牌、Cookie、数据库 DSN、代理凭证；
- 真实内网地址、主机名、私有 CA 与部署拓扑。

## 我们如何处理

- 收到报告后尽快确认（通常 3 个工作日内）。
- 评估影响与受影响版本，私下沟通修复计划与时间线。
- 修复发布后，在征得报告者同意的前提下于 Advisory / CHANGELOG 中致谢。

## 范围

以下问题属于安全范围：

- 认证/授权绕过（后台口令、会话 cookie、`admin_openids` 身份门、`run_shell` 双控）。
- 密钥或配置泄漏（`GET /api/config` 回显、日志、错误信息、SSE）。
- SSRF / 出站地址策略绕过（`web_fetch`、`web_search`、图片抓取）。
- SQL 注入、路径穿越、任意文件读写。
- 远程代码执行、容器逃逸、命令注入。
- 依赖中的已知高危漏洞（请附 CVE 与受影响模块）。

## 不在范围

- 需要攻击者已拥有管理员令牌或宿主机 root 的问题。
- 缺少安全响应头、TLS 配置等部署侧加固建议（欢迎提普通 issue）。
- 上游第三方服务（QQ 开放平台、模型网关、SearxNG、PostgreSQL）自身的漏洞，
  请直接报告给对应上游；如果影响本项目的集成方式，可以同时告诉我们。
- 社会工程、物理访问与拒绝服务（除非有明确的低成本远程触发路径）。

## 部署者须知

- 后台口令等同于管理员凭证：首次启动随机生成，通过 `bot-harness token` 读取，或
  在后台「接入与运维」轮换。请通过 HTTPS 反向代理访问后台。
- 默认只绑定 `127.0.0.1`；把 `ADMIN_BIND` 改成 `0.0.0.0` 会向所有网卡暴露后台。
- PostgreSQL 服务不发布宿主端口；不要把 5432 映射出来。
- 建议配置群白名单，并保持 `exec_enabled=false` 与 `admin_openids` 非空（除非确实需要 `run_shell`）。
