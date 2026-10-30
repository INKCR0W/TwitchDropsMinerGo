# TwitchDropsMinerGo

Twitch Drops 自动挂机服务，支持活动筛选、自动切台和领取，适合本机或 Docker 常驻运行。

## 快速开始

下载并解压[发布包](https://github.com/INKCR0W/TwitchDropsMinerGo/releases)。授权使用自己平时的浏览器，服务器不需要浏览器。从源码运行需 Go 1.26 或更高版本。

```sh
./miner-server
```

Windows 使用 `miner-server.exe`；从源码运行可替换为 `go run ./cmd/miner-server`。

首次在终端运行时，服务会给出登录链接。在日常浏览器打开链接，完成 Twitch 登录和二步验证后，将地址栏的完整地址粘贴回终端并回车。粘贴内容不会回显，请勿公开完成后的地址。

授权结果由服务校验并保存到 `runtime/state/cookies.json`，之后直接复用。需要重新登录或切换账号时，先停止服务，再执行：

```sh
./miner-server -login
```

可用 `-runtime-dir <目录>` 指定运行目录。旧版 `android-session.json` 仍可用 `-import-session <文件路径>` 导入；默认路径下的旧会话文件仅在 cookie 缺失或失效时自动导入。会话与 cookie 格式不同，不要互相改名，也不要公开。

## Docker 部署

免构建部署：

```sh
mkdir tdm && cd tdm
curl -fLO https://raw.githubusercontent.com/INKCR0W/TwitchDropsMinerGo/main/deploy/compose.yaml
```

或从源码构建：

```sh
git clone https://github.com/INKCR0W/TwitchDropsMinerGo.git
cd TwitchDropsMinerGo
```

在服务器的 compose 目录执行登录，链接可在自己的电脑或手机浏览器中打开：

```sh
docker compose stop miner
docker compose run --rm -it miner -login
docker compose up -d
docker compose logs -f miner
```

`./runtime` 挂载到容器内的 `/data`，登录结果会保存到宿主机，后台启动时直接使用。

镜像：[`inkcrow/twitchdropsminergo`](https://hub.docker.com/r/inkcrow/twitchdropsminergo)。下载的 compose 默认使用正式版 `latest`；需要 main 最新构建时改用 `edge`。更新镜像用 `docker compose pull && docker compose up -d`；源码部署则 `git pull` 后执行 `docker compose up -d`。

两份 compose 默认关闭自动重启，容器退出后不会反复拉起。修复问题后用 `docker compose up -d miner` 启动，宿主机重启后也需手动启动。程序内部的网络重试保留；健康检查显示 `unhealthy` 不会触发容器重启。Ctrl+C 只退出日志查看。

## 配置

配置文件为 `runtime/settings.json`，首次启动自动生成，修改后重启生效。默认 `priority_only`，需要在 `priority` 中填入要挂的游戏，或改用其他模式。

| 字段 | 默认 | 说明 |
|---|---|---|
| `priority` | `[]` | 优先游戏名，有序 |
| `exclude` | `[]` | 排除游戏，优先于其他设置 |
| `priority_mode` | `priority_only` | 选台策略，见下 |
| `smart_priority_safety_minutes` | `120` | 智能排序保护优先游戏的富余分钟数 |
| `enable_badges_emotes` | `false` | 是否参与徽章、表情活动 |
| `proxy` | `""` | 服务端 HTTP 与 PubSub 代理，需含协议和主机 |
| `connection_quality` | `1` | 1–6，网络差时调大以增加超时 |
| `watch_stall_minutes` | `10` | 进度停滞判定分钟数，触发后回避频道 30 分钟 |
| `mainland.enabled` | `false` | 大陆模式，见下 |
| `log.level` | `info` | `debug` / `info` / `warn` / `error` |
| `log.format` | `text` | `text` 或 `json` |
| `log.file_enabled` | `true` | 是否写日志文件 |
| `log.add_source` | `false` | 日志是否附带源码位置 |
| `log.max_size_bytes` | `10485760` | 日志轮转大小 |
| `log.max_backups` | `3` | 轮转保留份数，大小或份数为 0 时不轮转 |

选台策略：

- `priority_only`：只挂优先列表，按列表顺序
- `ending_soonest`：优先即将结束的活动
- `low_availability_first`：优先可用频道较少的活动
- `smart_balance`：优先列表中的每个可挂活动都留有足够富余时间时，才允许更紧急的其他活动插队；保护阈值由 `smart_priority_safety_minutes` 控制

大陆模式可尝试在中国大陆网络下免代理连接 Twitch，不修改 hosts 或安装证书。与 `proxy` 同时设置时以大陆模式为准，效果取决于网络；持续失败时请改用代理。PubSub 不可用时由 GQL 轮询兜底，开播、下播和领奖事件可能延迟。

其他运行文件：`state/state.json` 为状态快照，`logs/miner-server.log` 为日志，均位于运行目录下。

## 致谢与许可证

核心协议和调度逻辑参考 [DevilXD/TwitchDropsMiner](https://github.com/DevilXD/TwitchDropsMiner)。本项目使用 [MIT 许可证](LICENSE)。

## 赞助

TRON（TRX / TRC-20）：

```text
TRyXHMLAmXUK6KQbxhTzr2wFtfc1nkCrow
```
