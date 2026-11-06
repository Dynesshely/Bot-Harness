# deploy/ — 可选的部署片段（不是必需品）

默认部署**不需要这个目录**：配置全在数据库里，入口就是仓库根目录的
[`docker-compose.yml`](../docker-compose.yml) 与 [`Dockerfile`](../Dockerfile)。
compose 里已经有三个服务（`bot-harness` / `searxng` / `postgres`），全部由根文件定义，
本目录只放**与环境无关、可选启用**的片段。

这里只放**与环境无关、可选启用**的片段，例如：

| 片段 | 用途 |
|---|---|
| 反向代理示例（Caddy / Nginx） | 给后台加 HTTPS 终止；容器仍需只出网即可 |
| systemd unit / timer | 不走 Docker、直接跑二进制时的进程管理；PG 每日快照的定时触发器 |
| 离线镜像导出/导入 | 目标机器不得联网时的搬运方式 |
| 监控探针配置 | 复用内置 `bot-harness healthcheck` 子命令 |

> `searxng/` 是**例外**：它是内置搜索 sidecar 的配置，已由根 compose 直接引用、默认部署即启用，
> 不是可选项（详见下一节）。`postgres` 没有对应目录 —— 它是纯镜像服务，**没有任何配置文件**，
> 所有参数都在根 compose 里（详见「`postgres` 服务」一节）。

## `postgres` — 迁移目标数据库（默认随 compose 启动，无配置文件）

D-039 起，`docker-compose.yml` 直接带一个 `postgres:17-alpine` 服务。它不是可选片段：
机器人的目标是跑在它上面，但**程序是否用它由 `.env` 的 `DATABASE_URL` 决定**（非空走 PG，留空回退 SQLite）。

| 项 | 值 |
|---|---|
| 服务 / 容器 | `postgres` / `bot-harness-postgres` |
| 镜像 | `postgres:17-alpine`（固定大版本，不跟 `latest`） |
| 宿主端口 | **不发布**（`docker port bot-harness-postgres` 为空）；只在 compose 默认网络里以 `postgres:5432` 可达 |
| 数据 | 命名卷 `pgdata` → `/var/lib/postgresql/data`；`PGDATA=/var/lib/postgresql/data/pgdata` 放进子目录，便于将来换镜像/换挂载 |
| 账号 | 用户 `bot` / 库 `bot`（`POSTGRES_USER` / `POSTGRES_DB` 写死在 compose 里，不是密钥） |
| 口令 | 只来自 `.env` 的 `POSTGRES_PASSWORD`；`${POSTGRES_PASSWORD:?}` 缺键直接拒绝解析 |
| 健康检查 | `pg_isready -U bot -d bot`（5s 间隔 / 3s 超时 / 10 次） |
| 共享内存 | `shm_size: 256mb`（默认 64MB 对并行查询/大排序偏小） |
| 停止宽限 | `stop_grace_period: 30s`（给 PG 正常关库的时间） |

- **为什么不发布宿主端口**：5432 暴露到宿主等于把一个弱口令可爆破的数据库放到网卡上。
  排查一律 `docker exec -it bot-harness-postgres psql -U bot -d bot`。
- **没有 `depends_on`**：迁移完成前 PG 起不来不应该拖住机器人；切换时机由运维决定
  （先 `docker compose up -d postgres` 等 healthy，再 `bot-harness migrate`，最后让 `DATABASE_URL` 生效）。
- **不需要写 `deploy/postgres/`**：一个配置文件都不需要。要调 `shared_buffers` 之类，
  用 `command:` 加 `-c` 参数，或挂自己的 `postgresql.conf` —— 但请另开片段文件（`-f` 叠加），
  不要改根 compose。
- **`POSTGRES_PASSWORD` 对已初始化的卷改不动**：口令在 initdb 时写死进 `pgdata`。要换口令得
  `ALTER USER`，或删卷重建（等于丢数据）。
- 备份/恢复用 `scripts/backup-pg.sh` / `restore-pg.sh`（`pg_dump -Fc` + sha256 清单 + 轮转），
  不要 `tar` 卷目录 —— 热备不一致。SQLite 时代的 `backup-db.sh` / `restore-db.sh` 已 deprecated。

验证（不需要启动机器人）：

```bash
docker compose config --quiet                       # 能解析（缺 POSTGRES_PASSWORD 会在这里就失败）
docker compose up -d postgres && docker compose ps postgres          # 期望 healthy
docker exec bot-harness-postgres psql -U bot -d bot -c 'select version()'
docker port bot-harness-postgres                    # 期望空输出
```

## 放文件时的规矩

- **不写真实环境事实**：内网 IP / 主机名 / 代理 / 私有 CA 路径 / 证书私钥一律不进本目录，
  用 `10.x.x.x`、`one-api.internal.example`、`<局域网IP>` 之类的占位符；
  真实值由部署方自行保管（`scripts/privacy-scan.sh` 会拦）。
- **不放配置副本**：不要在这里放 `.env`、token、导出的数据库。
  配置的唯一载体是数据库，见 [DECISIONS.md D-036](../docs/DECISIONS.md)。
- **每个片段要能验证**：在文件头写清前置条件与验证命令（例如
  `docker compose -f docker-compose.yml -f deploy/<片段>.yml config` 能解析），
  否则一年后没人敢用。
- 片段只做增量覆盖，**不复制**根 compose 里的服务定义；根文件是唯一事实源。

## PG 定期备份 — `systemd/` 与 `cron/`

[`scripts/backup-pg.sh`](../scripts/backup-pg.sh) 只负责**跑一次**（`pg_dump -Fc` + `SHA256SUMS`
+ `MANIFEST.txt` + 轮转）。要让它**每天自己跑**，用本目录的两个片段之一；二选一，别都装。

| 文件 | 作用 |
|---|---|
| `systemd/bot-harness-pg-backup.service` | `Type=oneshot` 的一次性执行单元，由 timer 触发 |
| `systemd/bot-harness-pg-backup.timer` | `OnCalendar=*-*-* 04:30:00` + `Persistent=true`（错过会补跑） |
| `cron/bot-harness-pg-backup` | `/etc/cron.d` 片段，systemd 不可用时同点位的备选 |
| [`scripts/check-pg-backup.sh`](../scripts/check-pg-backup.sh) | 只读探针：新鲜度（36h）+ sha256 + `pg_restore -l`，给外部监控用 |

**前置条件**（缺一个就一定失败，先核对再装）：

1. 仓库已 clone 到某个路径，`scripts/backup-pg.sh` 存在；下文用 `<repo>` 指它。
2. 定时任务用的用户 `<user>` **在 docker 组里**：脚本走 `docker exec` 进 postgres 容器，
   不能 sudo（oneshot 单元没有 tty，cron 也没人输密码）。核对：
   `id -nG <user> | tr ' ' '\n' | grep -qx docker && echo OK`。
3. `postgres` 容器在跑（默认 `bot-harness-postgres`）。备份是**热备**：不重启、不停服、
   不锁表，机器人运行中导出的是一致的单事务快照。
4. 目标目录可写：默认 `$HOME/bot-harness-backups`（`HOME` 由单元/cron 片段显式给出，
   不依赖交互登录环境）。**不要**把它放进仓库目录 —— 那是会被 `git add` 扫到的位置。

> `deploy/systemd/*` 与 `deploy/cron/*` 里的 `<repo>` / `<user>` 是**占位符**：
> 仓库不得出现个人绝对路径（[`scripts/privacy-scan.sh`](../scripts/privacy-scan.sh) 会拦），
> 所以真实值在安装时用 `sed` 替换，不进 git。下面所有 `cp` 命令都自带替换。

### 安装（systemd，推荐）

```bash
cd <repo>
user="$(id -un)"; repo="$PWD"

# 1) 渲染占位符到临时文件（仓库里那份保持占位符，不能直接 cp 用）
install -d /tmp/bh-units
for u in service timer; do
  sed -e "s|<repo>|$repo|g" -e "s|<user>|$user|g" \
      "deploy/systemd/bot-harness-pg-backup.$u" > "/tmp/bh-units/bot-harness-pg-backup.$u"
done

# 2) 装到系统目录（这一处需要 root，只写这两个文件）
sudo install -m 0644 -o root -g root /tmp/bh-units/bot-harness-pg-backup.service /etc/systemd/system/
sudo install -m 0644 -o root -g root /tmp/bh-units/bot-harness-pg-backup.timer   /etc/systemd/system/
rm -rf /tmp/bh-units

# 3) 重新加载并只启用 timer（**不要** enable .service：那会开机立刻跑一次且没有时间表）
sudo systemctl daemon-reload
sudo systemctl enable --now bot-harness-pg-backup.timer
```

启用即生效，但**下一次触发是明天的 04:30**。想立刻验证真的能跑，手动触发一次
（这是一次性执行，不是常驻服务，不影响机器人）：

```bash
sudo systemctl start bot-harness-pg-backup.service
```

### 验证

```bash
# 时间表有没有装上、下一次什么时候跑（期望 NEXT 是明天的 04:30 左右）
systemctl list-timers 'bot-harness-pg-backup*' --all

# 单元内容与展开后的路径（确认 <repo>/<user> 真的被替换了）
systemctl cat bot-harness-pg-backup.service bot-harness-pg-backup.timer

# 最近一次执行的输出（stdout/stderr 全进 journal，这是唯一的运行日志）
journalctl -u bot-harness-pg-backup -n 50 --no-pager
journalctl -u bot-harness-pg-backup.timer -n 20 --no-pager

# 单元自身没问题（也可以对装好的文件跑）
systemd-analyze verify /etc/systemd/system/bot-harness-pg-backup.{service,timer}
systemd-analyze calendar '*-*-* 04:30:00'      # 核对表达式与本地时区

# 结果验收：最新快照是否 <36h、sha256 是否 OK、能不能被 pg_restore 解析
bash scripts/check-pg-backup.sh            # 0 = PASS；1 = 失败；2 = 探针自身参数/环境错
ls -1 "$HOME/bot-harness-backups"          # 期望看到 bot-<UTC 时间戳>/ 目录
```

### cron 备选（systemd 不可用时二选一）

`/etc/cron.d` 的硬性要求：文件名**不能带点**、必须 `root:root`、不能给 group/other 写权限，
每行是「分 时 日 月 周 **用户** 命令」六段式。片段里已经带了 `SHELL`/`PATH`/`HOME`
（cron 默认 `HOME` 是 `/root` 或空，不给的话 `--out` 会落到错的地方）。

```bash
cd <repo>
user="$(id -un)"; repo="$PWD"
sed -e "s|<repo>|$repo|g" -e "s|<user>|$user|g" \
    deploy/cron/bot-harness-pg-backup | sudo tee /etc/cron.d/bot-harness-pg-backup >/dev/null
sudo chown root:root /etc/cron.d/bot-harness-pg-backup
sudo chmod 0644 /etc/cron.d/bot-harness-pg-backup

# 验证
sudo systemctl status cron --no-pager | head -3          # 或 cronie，取决于发行版
journalctl -t CRON -n 20 --no-pager                      # 执行记录（Debian/Ubuntu: /var/log/syslog）
tail -n 20 "$HOME/bot-harness-backups/backup.log"        # 片段把输出追加到这里
bash scripts/check-pg-backup.sh
```

> 装 systemd 版就不要装 cron 版：同一个 04:30 跑两次不会坏，但会白占一份空间、让 `--keep 14`
> 提前把有用的一天轮掉。

### 保留策略（`--keep 14`）

- 每天一份、保留 **14 份 ≈ 两周**。轮转按快照目录名里的 UTC 时间戳排序，删最旧的；
  `--keep 0` 表示不轮转（只在手工导出时用）。
- **空间估算**：单份 ≈ 逻辑库大小（`pg_dump -Fc` 默认带压缩）。当前库 64 MB → 单份 dump ≈ 47 MB，
  14 份 ≈ **0.7 GB**；库每翻一倍，占用量同步翻倍。规划经验值：
  `预留 ≈ 2 × 当前库大小 × 14`（压缩率、WAL 波动、临时文件都算进去）。
  核对命令：`du -sh "$HOME/bot-harness-backups"`，单份大小看 `ls -l <snapshot>/<db>.dump`。
- 快照目录默认权限紧（单元里 `UMask=0077`，cron 版继承用户 umask），**不要**把它们放进仓库
  或任何会被同步/提交的目录。
- 保留期要覆盖「发现坏了→决定回滚」的决策时间：两周一般够；把 `--keep` 调大只需改
  `ExecStart`（或 cron 行的命令）后 `systemctl daemon-reload`。

### 恢复演练（建议每季度一次）

演练的意义是证明**备份真的能恢复**，而不是只证明「目录里有文件」。
`check-pg-backup.sh` 覆盖了前一半（新鲜度 + 完整性 + 可解析），完整演练走下面这条路径：

```bash
# 0) 先看快照清单，挑一个（不要动生产库）
ls -1t "$HOME/bot-harness-backups" | head

# 1) 恢复到**另一个库**（演练专用名，不碰 bot）
bash scripts/restore-pg.sh "$HOME/bot-harness-backups/<快照目录名>" --database bot_restore

# 2) 抽查：表数量、行数、关键表（settings 存着口令与全部配置）
docker exec -it bot-harness-postgres psql -U bot -d bot_restore \
  -c '\dt' \
  -c 'select count(*) from messages' \
  -c 'select count(*) from settings'

# 3) 抽查通过后删掉演练库
docker exec -it bot-harness-postgres psql -U bot -d postgres -c 'drop database bot_restore'

# 4) 恢复进生产才需要停机器人（restore-pg.sh 会拒绝在机器人运行时覆盖，除非 --force）
docker compose stop bot-harness
bash scripts/restore-pg.sh "$HOME/bot-harness-backups/<快照目录名>" --database bot --force
docker compose start bot-harness
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/api/health   # 期望 200
```

演练记录（日期 / 快照名 / 恢复耗时 / 抽查结果）由部署方自行保存，不进仓库。

### 外部监控

定时任务「悄悄没跑」是最常见的失效方式 —— 备份脚本自身不会报警。把探针挂到已有的监控上
（Uptime Kuma / cron + 邮件 / `systemd timer` 都行），判据只有退出码：

```bash
bash scripts/check-pg-backup.sh --quiet    # 0 通过；1 失败；2 探针自身错
```

也可以直接读 timer 状态：`systemctl is-failed bot-harness-pg-backup.service`（失败的服务单元会留在
failed 状态，直到下一次成功或被 `systemctl reset-failed` 清掉）。

## `searxng/` — 内置搜索 sidecar（默认启用，不是可选项）

`settings.yml` 是 SearxNG 的配置，挂载到 sidecar 容器的 `/etc/searxng`；服务本身定义在根
`docker-compose.yml`（服务 `searxng`、容器 `bot-harness-searxng`），所以**不需要** `-f` 叠加，
也不需要手工起。

- 镜像固定 `searxng/searxng:2026.11.1-9ded3bec1`；**不发布宿主端口**，只在 compose 默认网络里以
  `http://searxng:8080` 访问；`.env` 的 `NO_PROXY` 必须含 `searxng`，否则搜索请求会被送进代理。
- `search.formats` 必须含 `json`：`web_search` 走 `?format=json`，未启用的 format 会返回 403。
- `secret_key` 不写进本文件：由 `.env` 的 `SEARXNG_SECRET` 注入；缺该键时 compose 直接拒绝解析。
- 镜像**没有 HEALTHCHECK**，`docker ps` 不会显示 `healthy`；就绪探针用
  `docker compose exec searxng wget -qO- http://127.0.0.1:8080/healthz`（期望 `OK`）。
- **冷启动后第一次搜索可能 0 条**（引擎尚未预热），复测即正常；`/var/cache/searxng` 未持久化，
  容器重建后同样要再预热。
- `FORCE_OWNERSHIP=false`：否则镜像入口会把本目录 chown 成容器内 uid，之后人和 CI 都改不动仓库文件。

验证：`docker compose config --quiet` 能解析 + 上面的 `/healthz` 探针。

> 除了 `searxng/`，目录里的片段（`systemd/`、`cron/`）**默认都不启用**：
> 必须手工装到 `/etc/systemd/system` 或 `/etc/cron.d` 才会生效，仓库里放着不产生任何定时任务。
> `postgres` 同理 —— 它不需要任何片段文件，参数全在根 compose 里。
