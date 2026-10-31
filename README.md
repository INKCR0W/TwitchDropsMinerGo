# TwitchDropsMinerGo

Twitch Drops 自动挂机，支持活动筛选、自动切台和领取。

## 快速开始

下载并解压[发布包](https://github.com/INKCR0W/TwitchDropsMinerGo/releases)，运行：

```sh
./miner-server
```

Windows 运行 `miner-server.exe`。源码运行使用 `go run ./cmd/miner-server`，需要 Go 1.26+。

首次在终端启动，按提示用日常浏览器打开登录链接，再将登录后的完整地址粘贴回终端。输入不会回显，登录成功后自动保存。

换账号先停止服务，再运行 `./miner-server -login`。

## Docker 部署

```sh
mkdir tdm && cd tdm
curl -fLO https://raw.githubusercontent.com/INKCR0W/TwitchDropsMinerGo/main/deploy/compose.yaml
docker compose run --rm -it miner -login
docker compose up -d
docker compose logs -f miner
```

登录链接可在自己的电脑或手机上打开。换账号时先执行 `docker compose stop miner`，再重复登录和启动命令。

镜像默认使用正式版 `latest`，main 最新构建使用 `edge`。更新：`docker compose pull && docker compose up -d`。容器默认不自动重启。

## 配置

配置文件 `runtime/settings.json` 自动生成，修改后重启生效。默认只挂 `priority` 中的游戏，**需要先填入游戏名**，或修改 `priority_mode`。

| 字段 | 说明 |
|---|---|
| `priority` | 优先游戏名，按列表排序 |
| `exclude` | 排除的游戏 |
| `priority_mode` | `priority_only` 只挂优先列表；`ending_soonest` 即将结束优先；`low_availability_first` 可用频道少的优先；`smart_balance` 智能排序 |
| `enable_badges_emotes` | 是否参与徽章、表情活动，默认 `false` |
| `proxy` | 代理地址，如 `http://127.0.0.1:7890` |
| `mainland.enabled` | 大陆直连模式，默认 `false`；开启后优先于代理，效果取决于网络 |

运行数据保存在 `runtime/`，Docker 中挂载到 `/data`。可用 `-runtime-dir` 更换目录。登录回调地址和 `state/cookies.json` 含凭据，请勿公开。

## 致谢与许可证

核心协议和调度逻辑参考 [DevilXD/TwitchDropsMiner](https://github.com/DevilXD/TwitchDropsMiner)。本项目使用 [MIT 许可证](LICENSE)。

## 赞助

TRON（TRX / TRC-20）：

```text
TRyXHMLAmXUK6KQbxhTzr2wFtfc1nkCrow
```
