# hebe游戏存档同步

Wails 前端 + Go 后端的游戏存档管理器。Syncthing 负责同步 `data/`，本程序负责维护游戏配置、对比本地/云端存档差异，并在确认后执行双向覆盖。

## 目录约定

把 Windows 版本放到同一个应用目录中运行：

```text
HebeGameSaveSync/
  hebe-game-save-sync.exe
  syncthing.exe
  data/
    bg3/
      <游戏原始存档结构>
  config/
    games.json
  backups/
```

也支持把 Syncthing 放在 `syncthing/syncthing.exe`。如果存在以下任意配置目录，程序启动 Syncthing 时会自动传入 `-home`：

```text
syncthing-home/config.xml
syncthing/config/config.xml
config/syncthing/config.xml
```

## 配置文件

程序会自动创建 `config/games.json`：

```json
{
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

云端路径会映射为：

```text
data/<folderName>
```

例如上面的配置对应 `data/bg3`，程序会把该游戏的所有存档文件和子目录原样放在这个文件夹里。

## 同步策略

- 递归扫描本地存档目录和云端目录下的所有文件，不按扩展名过滤，按相对路径 + SHA-256 判断是否一致。
- 变化判断来自本地/云端目录的实时重扫，不依赖 Syncthing 的文件变化事件。
- 新旧判断结合新增文件、缺失文件、同名文件内容变化、文件修改时间；双方都有变化时标记为冲突，不自动猜测。
- 文件访问时间不作为覆盖依据，因为扫描和杀毒软件都可能更新访问时间，容易制造误判。
- 云端覆盖本地、本地覆盖云端都必须在界面中二次确认，并显示推断的新旧方、判断依据、来源目录、目标目录。
- 覆盖前会把目标目录完整复制到 `backups/<gameId>/<time>_<direction>`，并校验备份与原目录一致。
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
- 手动备份会备份当前本地游戏存档目录。
- 还原备份只还原到本地游戏存档目录，不会直接写云端；需要同步到云端时，再点击“上传本地”。
- 通过本程序启动游戏后会追踪游戏进程。游戏关闭时如果检测到本地存档较新，可配置为询问上传或自动上传。
- 每个游戏可配置上传方式：关闭自动上传完全手动、关闭后询问上传、关闭后自动上传、运行中定时上传。
- 自动上传只在本地明确较新时执行；每次游戏会话第一次自动上传会备份云端，后续自动上传不重复备份同一会话。

## Windows 兼容

- Windows 路径会原样保存到 `config/games.json`，例如 `C:\Users\you\AppData\Local\...`。
- Windows 下直接启动配置的游戏程序并传入启动参数，后台命令不会弹出额外 cmd 窗口。
- 启动程序时会检查 `127.0.0.1:8384`，默认 Syncthing 端口已可连接就不会重复启动 Syncthing。
- 默认寻找 `hebe-game-save-sync.exe` 同目录下的 `syncthing.exe`，也支持 `syncthing/syncthing.exe`；开发运行时也会兼容当前工作目录。
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

本项目已在 macOS 上成功构建 Windows 目标，输出为：

```text
build/bin/hebe-game-save-sync.exe
```

## 验证

```bash
go test ./...
pnpm --dir frontend run build
```
