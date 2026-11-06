## 动机

<!-- 这个 PR 解决什么问题？关联 issue：#123 -->

## 改了什么

<!-- 行为变化，而不是文件列表 -->

## 验证

<!-- 贴出实际跑过的命令与结果 -->
- [ ] `go vet ./...`
- [ ] `go test ./... -count=1 -timeout=10m`
- [ ] `cd web && pnpm test && pnpm build`（如改了前端）
- [ ] `bash scripts/check-dist.sh`（如改了前端）
- [ ] `bash scripts/check-config-keys.sh`（如改了设置键）
- [ ] `bash scripts/privacy-scan.sh`

## 自查清单

- [ ] 文档 / CHANGELOG 已同步（接口变更同步 `docs/ADMIN-API.md`）
- [ ] 新能力有独立开关、可按群覆盖（如适用）且可回滚
- [ ] 凭据/DSN 不会出现在日志、接口响应或错误信息里
- [ ] 没有提交真实内网地址、个人绝对路径、令牌或截图
- [ ] 提交已 GPG 签名

## 截图 / 补充

<!-- 可选；请先脱敏 -->
