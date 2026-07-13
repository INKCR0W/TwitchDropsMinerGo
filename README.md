# TwitchDropsMinerGo

Twitch Drops 自动挂机工具的 Go 服务端实现，面向无头 24/7 部署场景。以本地常驻进程方式运行，通过 `settings.json` 配置，通过 `state.json` 和日志文件观察状态。

## 环境要求

Go 1.26 或更高版本（以 `go.mod` 的 `go 1.26.0` 为准）。

## 使用方法

```
miner-server --runtime-dir /path/to/runtime
```

首次运行会提示通过 Twitch Device Code 授权。后续运行从持久化 cookie 文件恢复会话。

运行目录下的文件：

| 文件 | 用途 |
|---|---|
| `settings.json` | 配置文件 |
| `state/state.json` | 运行时状态快照 |
| `state/cookies.json` | 持久化认证 cookie |
| `logs/miner-server.log` | 日志输出 |
| `pending_login.txt` | 待登录授权提示，登录成功后自动删除 |

日志文件可能包含短期有效的 Device Code 授权提示和运行错误信息。建议将运行目录放在仅当前用户可读写的位置；服务端默认会限制日志文件权限并按大小轮转。

## Docker 部署

### 方式一：免构建（推荐）

```
mkdir tdm && cd tdm
curl -fLO https://raw.githubusercontent.com/INKCR0W/TwitchDropsMinerGo/main/deploy/compose.yaml
docker compose up -d && docker compose logs -f miner
```

镜像由 CI 构建并推送至 [Docker Hub](https://hub.docker.com/r/inkcrow/twitchdropsminergo)：`latest` 为最新正式版，`edge` 跟随 main，支持 amd64/arm64。更新：`docker compose pull && docker compose up -d`。

### 方式二：从源码构建

```
git clone https://github.com/INKCR0W/TwitchDropsMinerGo.git && cd TwitchDropsMinerGo
docker compose up -d && docker compose logs -f miner
```

每次 `up` 都会基于当前源码带缓存重建镜像，`git pull` 后重跑上面第二条命令即可生效。

两条路线通用：首次运行时日志中会出现登录提示（同时写入 `./runtime/pending_login.txt`），在任意设备的浏览器打开提示中的网址并输入代码即可。`pending_login.txt` 消失即登录成功，Ctrl+C 断开日志查看，容器继续运行。

登录态与日志通过 `./runtime` 挂载持久化在宿主机，重启、重建容器均无需再登录。Linux 上如需宿主机用户直接读取日志文件，先 `mkdir -p runtime` 再启用 compose 文件中注释的 `user:` 配置。

## 配置

`settings.json` 主要字段：

```json
{
  "priority": ["游戏 A", "游戏 B"],
  "exclude": [],
  "priority_mode": "priority_only",
  "smart_priority_safety_minutes": 120,
  "enable_badges_emotes": false,
  "proxy": ""
}
```

`priority_mode` 可选值：`priority_only`、`ending_soonest`、`low_availability_first`、`smart_balance`。

`smart_priority_safety_minutes`（默认 120）仅在 `smart_balance` 模式下生效：只有当 `priority` 列表中每个可挖游戏的富余时间（距结束时间减去还需观看的分钟数，按活动内最紧的 drop 计）都不低于该值时，才允许更紧急的非 priority 游戏插队。这样 priority 游戏可以晚挖，但不会因插队而来不及完成；调小该值会更容易让位给紧急的非 priority 活动。

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
