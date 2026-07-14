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
