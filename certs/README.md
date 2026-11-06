# certs/

把**公开证书**（PEM，不含私钥）放在这里，用于让程序信任自建服务使用的私有 CA。
仓库**不附带任何 CA 证书**；这个目录默认只有本说明，换证书不需要重建镜像。
部署方在本地放入的 `certs/*.pem` / `certs/*.crt` 已被 `.gitignore` 与 `.dockerignore` 排除，
不会进入 Git 仓库或镜像；它们只存在于宿主机，并由 compose 只读挂载到 `/app/certs`。

## 程序如何使用

- 运行期配置项是后台「接入与运维 → 额外 CA」（`tls_extra_ca_files`，可多条）。
  这些 PEM 会被**追加**到系统根证书之上（不是替换），因此访问公网端点（QQ 开放平台等）不受影响。
- 也可以在**首次部署**时用 `TLS_EXTRA_CA_FILE` 环境变量作为引导值；第一次启动会导入数据库，
  之后以数据库为准，再改环境变量不会生效。
- 本地运行：把文件放在 `./certs/`，例如 `./certs/extra-ca.pem`。
- 容器运行：compose 会把宿主机的 `./certs` 只读挂载到 `/app/certs`，
  容器内路径形如 `/app/certs/extra-ca.pem`。

配置了但文件不存在或内容不是合法 PEM 时，程序会在启动阶段直接报错退出，
而不是等到第一次调用模型时才抛出难懂的 TLS 握手错误。

## 更新（CA 轮换或换用别的网关）

```bash
# 1. 取回服务端实际发送的证书链（叶子 + 中间）
openssl s_client -connect <host>:443 -servername <host> -showcerts </dev/null > /tmp/chain.pem

# 2. 从你的 CA 分发渠道取根证书，与中间证书拼接
cat root-ca.crt intermediate-ca.crt > certs/extra-ca.pem

# 3. 校验（应当输出 OK）
openssl verify -CAfile certs/extra-ca.pem leaf.pem
```

## 临时兜底

确实拿不到 CA 时，可以在后台开启 `tls_insecure_verify`（旧环境变量 `TLS_INSECURE_SKIP_VERIFY`）
关闭出站校验。**这会同时取消对 QQ 开放平台的证书校验**，只应作为临时手段，程序启动时会打印显式警告。

> 不要把私钥、真实内网域名或内部 CA 的组织信息提交进仓库；需要举例时用
> `gateway.internal.example` / `<局域网IP>`。
