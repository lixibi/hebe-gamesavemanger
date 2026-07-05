# hebe游戏存档同步

Wails 前端 + Go 后端的游戏存档管理器。`dev` 分支开始移除 Syncthing 依赖，改为连接自建的轻量 Go 云存档服务，对比本地/云端存档差异，并在确认后执行双向覆盖。

## 目录约定

把 Windows 版本放到同一个应用目录中运行：

```text
HebeGameSaveSync/
  hebe-game-save-sync.exe
  data/
    bg3/
      <本地缓存，可自动生成>
  config/
    games.json
  backups/
```

云端服务的数据目录按 `云存档根目录/<folderName>` 保存游戏原始存档结构，备份按 `云存档根目录/.backups/<folderName>` 保存。

## 配置文件

程序会自动创建 `config/games.json`：

```json
{
  "cloudServerURL": "http://127.0.0.1:27843",
  "games": [
    {
      "id": "bg3",
      "name": "博德之门3",
      "folderName": "bg3",
      "localSavePath": "C:\\Users\\you\\AppData\\Local\\Larian Studios\\Baldur's Gate 3\\PlayerProfiles\\Public\\Savegames\\Story",
      "gameExePath": "D:\\Games\\Baldurs Gate 3\\bin\\bg3_dx11.exe",
      "gameArgs": "-windowed"
    }
  ]
}
```

`cloudServerURL`、`cloudPassword`、本机存档路径、游戏程序路径和启动参数都只保存在当前客户端。云端只保存游戏名、游戏 ID、云端文件夹名，方便另一台电脑连接云服务后自动下发游戏列表，再分别设置该电脑自己的本地路径。

服务端默认密码是 `hebesave`，明文保存在云端数据目录的 `.hebe-games.json` 里。客户端连接时需要填写云地址和密码；连接成功后，可以在客户端修改服务端密码。

云端路径会映射为服务端：

```text
<cloud-root>/<folderName>
```

例如上面的配置对应服务端数据目录里的 `bg3`，程序会把该游戏的所有存档文件和子目录原样上传到这个文件夹里。

## 云存档服务

默认端口：`27843`。

Docker 运行：

```bash
docker build -f Dockerfile.save-server -t hebe-save-server .
docker run -d --name hebe-save-server \
  -p 27843:27843 \
  -v /path/to/cloud-saves:/data \
  hebe-save-server
```

也可以直接运行：

```bash
go run ./cmd/save-server -addr :27843 -root ./cloud-saves
```

服务端除 `GET /health` 外，API 都需要请求头：

```text
X-Hebe-Password: hebesave
```

服务端提供：

- `GET /health`：健康检查。
- `GET /api/games`：列出云端游戏目录。
- `PUT /api/password`：修改服务端密码，需要先用当前密码认证。
- `PUT /api/games/{game}/config`：保存云端游戏配置，不包含任何客户端本地路径。
- `GET /api/games/{game}/manifest`：列出文件 hash、大小、修改时间。
- `GET /api/games/{game}/archive`：下载云端存档 tar.gz。
- `PUT /api/games/{game}/archive`：上传本地存档 tar.gz，服务端替换前自动备份旧云端。
- `GET /api/games/{game}/backups`：列出云端最近 5 个备份。
- `POST /api/games/{game}/backups`：手动创建云端备份。
- `POST /api/games/{game}/backups/restore/{backup}`：把云端备份还原为当前云端存档。

## 同步策略

- 递归扫描本地存档目录和云端目录下的所有文件，不按扩展名过滤，按相对路径 + SHA-256 判断是否一致。
- 变化判断来自本地目录扫描和云服务 manifest，不依赖 Syncthing 的文件变化事件。
- 新旧判断结合新增文件、缺失文件、同名文件内容变化、文件修改时间；双方都有变化时标记为冲突，不自动猜测。
- 文件访问时间不作为覆盖依据，因为扫描和杀毒软件都可能更新访问时间，容易制造误判。
- 云端覆盖本地、本地覆盖云端都必须在界面中二次确认，并显示推断的新旧方、判断依据、来源目录、目标目录。
- 本地覆盖前会把目标目录完整复制到 `backups/<gameId>/<time>_<direction>`，并校验备份与原目录一致；云端覆盖前由服务端备份到 `.backups/<game>`。
- 备份按游戏分组保存到 `backups/<gameId>/`，每个游戏只保留最新 5 个备份。
- 真正覆盖时会先复制到临时目录并校验，再替换目标目录；失败时会尝试从备份恢复。
- 文件校验覆盖任意扩展名文件、无扩展名文件、隐藏文件、多层子目录文件，并保留空目录。
- 启动游戏前如果本地/云端不一致，界面会提示当前游戏将读取本地存档，并要求选择是否先用云端覆盖本地。
- 删除游戏配置不会删除本地存档，也不会删除 `data/` 下的云端目录。

## 操作入口

- 左侧游戏列表支持右键菜单。
- 右键可编辑配置、导出游戏配置、打开本地存档目录、打开云端文件夹、打开游戏目录、备份当前存档、还原备份、刷新、启动游戏、删除配置。
- 新增游戏窗口支持导入导出的 JSON 配置，方便迁移到另一台电脑后再确认本地路径并保存。
- 配置窗口支持用系统选择器选择本机存档文件夹和游戏程序文件，并可填写启动参数。
- 左侧可设置云服务地址和密码，支持直接填写 `IP:27843`，也支持反代地址不带端口；点击测试连接会验证密码，保存后会重新下发云端游戏列表。
- 新增或保存游戏会把游戏名、ID、云端文件夹名保存到云端；如果已设置本机存档目录，会检查目录中文件数，文件数为 0 会阻止首次上传并提示用户。
- 手动备份会备份当前本地游戏存档目录。
- 还原备份只还原到本地游戏存档目录，不会直接写云端；需要同步到云端时，再点击“上传本地”。
- 通过本程序启动游戏后会追踪游戏进程。游戏关闭时如果检测到本地存档较新，可配置为询问上传或自动上传。
- 每个游戏可配置上传方式：关闭自动上传完全手动、关闭后询问上传、关闭后自动上传、运行中定时上传。
- 自动上传只在本地明确较新时执行；每次游戏会话第一次自动上传会备份云端，后续自动上传不重复备份同一会话。

## Windows 兼容

- Windows 路径会原样保存到 `config/games.json`，例如 `C:\Users\you\AppData\Local\...`。
- Windows 下直接启动配置的游戏程序并传入启动参数，后台命令不会弹出额外 cmd 窗口。
- 客户端默认连接 `http://127.0.0.1:27843`，可在 `config/games.json` 的 `cloudServerURL` 修改为 NAS 或公网服务地址。
- 程序启用单实例锁，重复打开会唤起已有窗口，不再多开。

## 开发

```bash
export NVM_DIR="$HOME/.nvm"
. "$NVM_DIR/nvm.sh"

pnpm --dir frontend install
wails dev
```

## 构建

macOS 当前机器构建：

```bash
wails build -clean
```

Windows amd64 构建：

```bash
wails build -platform windows/amd64 -clean
```

服务端 Linux amd64 构建：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o build/bin/hebe-save-server ./cmd/save-server
```

本项目已在 macOS 上成功构建 Windows 目标，输出为：

```text
build/bin/hebe-game-save-sync.exe
```

## 验证

```bash
go test ./...
pnpm --dir frontend run build
```
