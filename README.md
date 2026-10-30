# TwitchDropsMinerGo

Twitch Drops 自动挂机工具的 Go 服务端实现，面向无头 24/7 部署场景。以本地常驻进程方式运行，通过 `settings.json` 配置，通过 `state.json` 和日志文件观察状态。

## 环境要求

从源码运行或构建需要 Go 1.26 或更高版本（以 `go.mod` 为准），使用发布包或 Docker 镜像不需要安装 Go。

首次授权需要在有图形界面的电脑上安装 Chrome 或 Edge。浏览器只在登录时使用，服务器和 Docker 容器不需要安装浏览器。

## 使用方法

下载适合本机系统的[发布包](https://github.com/INKCR0W/TwitchDropsMinerGo/releases)，解压后包含 `miner-login` 和 `miner-server`。先运行登录工具，在打开的独立浏览器窗口中完成 Twitch 登录和二步验证：

```sh
./miner-login
```

Windows 使用 `.\miner-login.exe`。从源码运行可使用 `go run ./cmd/miner-login`。

授权成功后，会话默认保存到**执行命令时所在目录**下的 `runtime/state/android-session.json`。可用 `-output <路径>` 更改保存位置，用 `-browser <路径>` 指定浏览器；需要代理时使用 `-proxy <代理 URL>`。

登录工具只通过浏览器完成授权并导出会话，不再额外联网校验令牌。浏览器能访问 Twitch，但本机命令行流量没有经过代理或加速器时，也可以正常导出；`-proxy` 用于指定浏览器代理。令牌有效性、账号和 Client-ID 由 `miner-server` 导入时校验，因此运行服务的环境仍需能连接 Twitch。

本机运行服务时，先导入会话，再启动服务：

```sh
./miner-server -runtime-dir ./runtime -import-session ./runtime/state/android-session.json
./miner-server -runtime-dir ./runtime
```

Windows 使用 `.\miner-server.exe`；从源码运行可将 `./miner-server` 替换为 `go run ./cmd/miner-server`。Docker 部署的导入命令见下方。

服务使用 Android 客户端会话，不再通过设备码登录。之前的 TV 会话需要重新授权。导入会话前应停止正在使用同一运行目录的实例；程序会先校验令牌所属客户端和账号，成功后才覆盖原来的认证信息。后续启动从持久化 cookie 恢复会话；会话过期或被撤销后，重新授权并导入即可。

运行目录下的文件：

| 文件 | 用途 |
|---|---|
| `settings.json` | 配置文件 |
| `state/state.json` | 运行时状态快照 |
| `state/cookies.json` | 持久化认证 cookie |
| `state/android-session.json` | 登录工具默认导出的会话，供本机或服务器导入 |
| `logs/miner-server.log` | 日志输出 |

会话文件和 cookie 都包含登录凭据，不要公开或提交到仓库。建议将运行目录放在仅当前用户可读写的位置；服务端默认会限制日志文件权限并按大小轮转。

## Docker 部署

### 方式一：免构建（推荐）

```
mkdir tdm && cd tdm
curl -fLO https://raw.githubusercontent.com/INKCR0W/TwitchDropsMinerGo/main/deploy/compose.yaml
```

镜像由 CI 构建并推送至 [Docker Hub](https://hub.docker.com/r/inkcrow/twitchdropsminergo)：`latest` 为最新正式版，`edge` 跟随 main，支持 amd64/arm64。更新：`docker compose pull && docker compose up -d`。

下载的 `compose.yaml` 默认使用 `latest`。如需测试 main 的最新构建，将 `image` 改为 `inkcrow/twitchdropsminergo:edge`，再执行上面的更新命令。main 的 CI 全部通过并完成镜像发布后，`edge` 才会更新；`latest` 随 `v*` 版本标签发布更新。

### 方式二：从源码构建

```
git clone https://github.com/INKCR0W/TwitchDropsMinerGo.git && cd TwitchDropsMinerGo
```

每次 `up` 都会基于当前源码带缓存重建镜像，`git pull` 后重新执行 `docker compose up -d` 即可更新。

### 登录与会话导入

两条部署路线都使用前面介绍的 `miner-login` 在本机授权。服务器没有桌面也可以使用：在自己的电脑完成登录，把生成的 `android-session.json` 通过 SSH/SCP 等方式上传到服务器项目目录下的 `runtime/state/android-session.json`。

在服务器的 compose 文件所在目录执行：

```sh
mkdir -p runtime/state
# 将本机生成的会话文件上传到 runtime/state/android-session.json 后执行
docker compose stop miner
docker compose run --rm -T --no-deps miner -import-session /data/state/android-session.json
docker compose up -d
docker compose logs -f miner
```

`./runtime` 在容器内挂载为 `/data`，因此导入命令使用 `/data/state/android-session.json`。确认输出“Android 会话已导入”后再启动服务；如果导入失败，先检查文件、网络或重新授权。导入成功后可删除上传的会话文件，后续运行使用 `state/cookies.json`。

镜像只包含服务端，`miner-login` 需要在有浏览器的电脑上运行。Ctrl+C 只会退出日志查看，容器继续运行。

镜像内置健康检查：等待登录授权或调度持续出错时，`docker ps` 会显示 unhealthy（Docker 不会因此自动重启容器，仅作状态可见）。

登录态与日志通过 `./runtime` 挂载持久化在宿主机，重启、重建容器均无需再登录。Linux 上如需宿主机用户直接读取日志文件，先 `mkdir -p runtime` 再启用 compose 文件中注释的 `user:` 配置。

## 配置

`settings.json` 首次运行自动生成，完整字段：

```json
{
  "priority": ["游戏 A", "游戏 B"],
  "exclude": [],
  "priority_mode": "priority_only",
  "smart_priority_safety_minutes": 120,
  "enable_badges_emotes": false,
  "proxy": "",
  "connection_quality": 1,
  "watch_stall_minutes": 10,
  "mainland": {
    "enabled": false
  },
  "log": {
    "level": "info",
    "format": "text",
    "file_enabled": true,
    "add_source": false,
    "max_size_bytes": 10485760,
    "max_backups": 3
  }
}
```

| 字段 | 默认 | 含义 |
|---|---|---|
| `priority` | `[]` | 优先游戏名列表，有序 |
| `exclude` | `[]` | 永不挖的游戏名列表，优先级高于一切 |
| `priority_mode` | `priority_only` | 选台策略，见下 |
| `smart_priority_safety_minutes` | `120` | 仅 `smart_balance` 生效，见下 |
| `enable_badges_emotes` | `false` | 是否挖只送徽章/表情（无实体 Drop 道具）的活动 |
| `proxy` | `""` | 代理 URL（HTTP 与 PubSub WebSocket 均生效），须含协议和主机 |
| `connection_quality` | `1` | 网络质量系数 1–6：连接超时 = 5s × 该值，请求超时 = 10s × 该值，网络差调大 |
| `watch_stall_minutes` | `10` | 观看正常但 drop 进度连续该分钟数无增长时，判定频道卡死，回避 30 分钟并切台 |
| `mainland.enabled` | `false` | 大陆模式：中国大陆网络下免代理连接 Twitch，见下 |
| `log.level` | `info` | `debug` / `info` / `warn` / `error` |
| `log.format` | `text` | `text` 或 `json`，stdout 与文件同格式 |
| `log.file_enabled` | `true` | 除 stdout 外同时写 `logs/miner-server.log` |
| `log.add_source` | `false` | 日志行附带源码位置 |
| `log.max_size_bytes` | `10485760` | 单文件轮转阈值，与 `max_backups` 任一为 0 则不轮转 |
| `log.max_backups` | `3` | 轮转保留的旧日志份数 |

`priority_mode` 可选值：

- `priority_only` —— 只挖 `priority` 列表中的游戏，按列表顺序
- `ending_soonest` —— 全局按活动最早结束优先
- `low_availability_first` —— 可用频道少的活动优先
- `smart_balance` —— 按 `priority` 挖，但允许紧急的非 priority 活动在安全前提下插队

`smart_priority_safety_minutes`（默认 120）仅在 `smart_balance` 模式下生效：只有当 `priority` 列表中每个可挖游戏的富余时间（距结束时间减去还需观看的分钟数，按活动内最紧的 drop 计）都不低于该值时，才允许更紧急的非 priority 游戏插队。这样 priority 游戏可以晚挖，但不会因插队而来不及完成；调小该值会更容易让位给紧急的非 priority 活动。

### 大陆模式

`mainland.enabled` 默认 `false`。在中国大陆网络下，Twitch 的域名解析会被污染，且 TLS 握手中的明文域名会触发连接重置，因此通常需要代理。开启大陆模式后，程序会在自己的进程内换一条连接路径：用 DNS-over-HTTPS 取得真实 IP，握手时改用该域名在 CDN 上对应的基础设施域名，并自行用系统根证书对真实域名校验服务器证书。

它不修改 hosts、不安装任何根证书、不需要管理员权限，也不启动本地代理，关闭设置即完全恢复原状。

几点须知：

- 与 `proxy` 互斥：两者同时配置时以大陆模式为准，`proxy` 被忽略并在启动日志中告警。
- 若该网络下 PubSub（`pubsub-edge.twitch.tv`）无法连接，掉宝进度会使用 GQL 轮询兜底。开播、下播和可领取等事件可能延迟到后续状态同步或活动清单刷新时才被发现，不保证在固定秒数内同步。
- 连接失败时会明确报错并指出是哪个域名、哪一步失败，不会静默回退。若日志显示持续失败，说明该网络环境不适用，请改用 `proxy`。
- 各地区网络环境不同，且随时可能变化，该模式不保证长期可用。
- 修改后重启生效。

配置修改后重启生效（Docker 部署：`docker compose restart`）。

## 致谢

核心协议行为、数据模型和调度逻辑源自 [TwitchDropsMiner](https://github.com/DevilXD/TwitchDropsMiner)（MIT 许可证），由 DevilXD 及贡献者开发。

## 赞助

如果这个项目对你有帮助，欢迎请我喝杯咖啡。

TRON（TRX / TRC-20）地址：

```
TRyXHMLAmXUK6KQbxhTzr2wFtfc1nkCrow
```

## 许可证

本项目基于 MIT 许可证发布，详见 [LICENSE](LICENSE)。
