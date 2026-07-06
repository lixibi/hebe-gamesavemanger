# Hebe Game Save Manager / hebe游戏存档同步

Self-hosted game save manager for Windows players. Run a tiny Go save server on your NAS or VPS, then use the Windows x64 desktop client to compare, back up, upload, download, and launch games with safer save handling.

一个适合 NAS 自部署的游戏存档同步项目：服务端是轻量 Go 文件服务，推荐用 Docker 跑在 NAS 上；客户端是 Windows x64 桌面程序，用来管理本地游戏存档、云端备份、双向覆盖和游戏启动。

> Default server password: `hebesave`. Change it after the first successful connection.
>
> 服务端默认密码：`hebesave`。首次连接成功后建议立刻在客户端里修改。

## Features / 功能

- No Syncthing dependency. The client talks directly to the self-hosted Hebe save server.
- Full-folder save sync: every file, extensionless file, hidden file, nested directory, and empty directory is considered.
- SHA-256 manifest comparison for local/cloud differences.
- Safer overwrite: before replacing a save folder, the destination is backed up first.
- Faster overwrite: unchanged files are skipped; only changed/missing files are copied, and stale target files are removed.
- Local backups and cloud backups are separated. Each game keeps the latest 5 backups.
- Windows game launcher supports `.exe`, launch arguments, and `steam://` URLs.
- Game list can use the configured `.exe` icon on Windows.
- GitHub Actions builds Windows x64 client, Linux x64 server, and a Docker image.

## Architecture / 架构

```text
Windows PC
  hebe-game-save-sync.exe
  config/games.json          # local-only paths and cloud settings
  backups/<game>/            # local backups

NAS / VPS / Home server
  Docker: ghcr.io/lixibi/hebe-save-server:latest
  /data/<gameIdentifier>/    # current cloud save files
  /data/.backups/<game>/     # latest 5 cloud backups
  /data/.hebe-games.json     # password and cloud game list
```

Only game name, game id, and game identifier are stored on the server. Local save paths and game executable paths stay on each Windows client.

云端只保存游戏名、游戏 ID、游戏标识名和存档文件。本机存档路径、游戏 exe 路径、启动参数只保存在当前 Windows 客户端。

## Quick Start / 快速开始

### 1. Deploy the server on NAS with Docker / 在 NAS 上用 Docker 部署服务端

Create a persistent folder first, for example `/volume1/docker/hebesave` on Synology or `/mnt/user/appdata/hebesave` on Unraid.

先准备一个持久化目录，例如群晖 `/volume1/docker/hebesave`，Unraid `/mnt/user/appdata/hebesave`。

```bash
docker run -d \
  --name hebe-save-server \
  --restart unless-stopped \
  -p 27843:27843 \
  -v /volume1/docker/hebesave:/data \
  ghcr.io/lixibi/hebe-save-server:latest
```

If your NAS uses another path, only change the left side of `-v`:

如果你的 NAS 路径不同，只需要改 `-v` 左边：

```bash
-v /your/nas/folder/hebesave:/data
```

Health check:

```bash
curl http://NAS_IP:27843/health
```

Optional environment variables:

```bash
docker run -d \
  --name hebe-save-server \
  --restart unless-stopped \
  -e HEBE_SAVE_ADDR=:27843 \
  -e HEBE_SAVE_ROOT=/data \
  -p 27843:27843 \
  -v /volume1/docker/hebesave:/data \
  ghcr.io/lixibi/hebe-save-server:latest
```

You can also put the server behind a reverse proxy. The Windows client accepts both `http://ip:27843` and a reverse-proxy URL such as `https://save.example.com`.

也可以通过反代访问。客户端支持 `http://ip:27843`，也支持 `https://save.example.com` 这种不带端口的反代地址。

### 2. Download the Windows x64 client / 下载 Windows x64 客户端

Download `hebe-game-save-sync-windows-x64.exe` from GitHub Releases.

从 GitHub Releases 下载 `hebe-game-save-sync-windows-x64.exe`。

Run it from any folder you like. The app will create local config and backup folders next to the executable.

把 exe 放到任意目录运行即可。程序会在 exe 同级目录自动创建配置和本地备份目录。

```text
HebeGameSaveSync/
  hebe-game-save-sync.exe
  config/games.json
  backups/
  cache/
```

### 3. Connect the client / 连接客户端

Open cloud settings in the lower-left area:

在客户端左下角打开云端配置：

- Server URL / 云端地址: `http://NAS_IP:27843`
- Password / 连接密码: `hebesave`
- Click test, then save / 点击测试连接，然后保存
- Change the server password after connecting / 连接成功后修改服务端密码

### 4. Add a game / 添加游戏

In the client:

客户端中填写：

- Game name / 游戏名: `Baldur's Gate 3`
- Game identifier / 游戏标识名: `bg3`
- Local save folder / 本机存档目录: the game's real save directory
- Launch target / 启动目标: game `.exe` or `steam://rungameid/<appid>`
- Auto upload mode / 自动上传方式: manual, ask on exit, upload on exit, or interval upload

When adding a game, the game list is saved to the server, but local paths are kept only on the current PC.

新增游戏时会把游戏列表保存到云端，但本机路径只保存在当前电脑。

## Backup And Sync Rules / 备份与同步规则

- The app recursively scans the full save folder.
- File identity is based on relative path, size, and SHA-256 hash.
- Newer-side hints use added files, removed files, changed content, and modified time.
- Access time is ignored because antivirus, indexing, and scanning can change it.
- Upload local: replaces the cloud save with local files. The server backs up the previous cloud save first.
- Download cloud: replaces the local save with cloud files. The client backs up the previous local save first.
- Differential overwrite skips identical files and only copies changed/missing files.
- Files that exist only on the target side are removed so the target matches the source.
- Each game keeps the latest 5 local backups and latest 5 cloud backups.
- Restore local backup writes only to the local save folder. It does not automatically upload to cloud.
- Restore cloud backup writes only to the cloud save folder. It does not automatically download to local.

## Server API / 服务端 API

All API routes except `GET /health` require:

除了 `GET /health`，其它接口都需要请求头：

```text
X-Hebe-Password: hebesave
```

Main routes:

```text
GET  /health
GET  /api/games
PUT  /api/password
PUT  /api/games/{game}/config
GET  /api/games/{game}/manifest
GET  /api/games/{game}/archive
PUT  /api/games/{game}/archive
GET  /api/games/{game}/backups
POST /api/games/{game}/backups
POST /api/games/{game}/backups/restore/{backup}
```

## Build From Source / 从源码构建

Requirements:

- Go 1.23+
- Node.js 22+
- pnpm
- Wails v2.12+

Install Wails:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0
```

Run in development:

```bash
pnpm --dir frontend install
wails dev
```

Build Windows x64 client:

```bash
wails build -platform windows/amd64 -clean
```

Output:

```text
build/bin/hebe-game-save-sync.exe
```

Build Linux x64 server:

```bash
mkdir -p build/bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o build/bin/hebe-save-server-linux-x64 ./cmd/save-server
```

Build Docker image locally:

```bash
docker build -f Dockerfile.save-server -t hebe-save-server:local .
```

## GitHub Actions / 自动构建

The workflow in `.github/workflows/release.yml` builds:

- Windows x64 client artifact
- Linux x64 server artifact
- Docker image: `ghcr.io/lixibi/hebe-save-server:latest`
- GitHub Release assets when pushing a tag like `v1.0.0`

`.github/workflows/release.yml` 会自动构建：

- Windows x64 客户端
- Linux x64 服务端
- Docker 镜像：`ghcr.io/lixibi/hebe-save-server:latest`
- 推送 `v1.0.0` 这类 tag 时自动创建 GitHub Release 附件

Create a release:

```bash
git tag v1.0.0
git push origin v1.0.0
```

## Notes / 注意

- This tool overwrites real game saves. Always check the source and target before confirming upload or download.
- Keep the NAS data folder backed up if your saves are important.
- Do not expose the server directly to the public internet without HTTPS and a strong password.
- The default password is intentionally simple for first boot only. Change it.

- 这是会真实覆盖游戏存档的工具，上传/下载前请确认来源和目标。
- 重要存档建议 NAS 侧再做一层快照或备份。
- 不建议裸奔暴露到公网；如需公网访问，请使用 HTTPS 反代和强密码。
- 默认密码只是方便首次启动，请尽快修改。
