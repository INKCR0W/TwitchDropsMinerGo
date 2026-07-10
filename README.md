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

日志文件可能包含短期有效的 Device Code 授权提示和运行错误信息。建议将运行目录放在仅当前用户可读写的位置；服务端默认会限制日志文件权限并按大小轮转。

## 配置

`settings.json` 主要字段：

```json
{
  "priority": ["游戏 A", "游戏 B"],
  "exclude": [],
  "priority_mode": "priority_only",
  "smart_priority_safety_minutes": 120,
  "enable_badges_emotes": false,
  "available_drops_check": false,
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
