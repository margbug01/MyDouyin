# MyDouyin

用 MyGo 原生界面浏览抖音，支持 macOS 和 Windows 10/11（x64）。首次使用在应用里扫码登录，也可以先跳过登录浏览公开内容。

## 开发

需要 Go 1.27.3 或更新版本。macOS 使用系统 AVFoundation；Windows 需要 PATH 中有 `ffmpeg.exe` 和 `ffprobe.exe`，正式包会把两者放在 `MyDouyin.exe` 旁边，无须另装。

```sh
go run .
go test ./...
```

## Windows 构建

在 Windows 的 PowerShell 7 中，从干净且已提交的 Git 工作区运行：

```powershell
./scripts/build-windows.ps1
```

脚本从当前提交创建独立源码快照，带上本机 FFmpeg 工具、许可证和来源说明，再用 MyGo 生成安装程序。FFmpeg 的安装目录需保留上游的 `LICENSE` 和 `README.txt`（例如 WinGet 的 Gyan.FFmpeg 包）。网络访问使用本机 `127.0.0.1:10808` 代理。`BUILD-INFO.txt` 记录源提交和两个工具的 SHA256。

输出在 `build.noindex/release-<提交>/windows-amd64/`。加 `-SkipInstaller` 可只生成完整应用目录；整个目录需要一起保留。应用内更新同样需要包含两个 FFmpeg 工具，不能只替换为没有工具的独立 EXE。

Windows 播放走 FFmpeg 解码和 MyGo 绘制，支持声音、暂停、拖动、倍速、循环及 HLS 直播；画面输出最高 60 fps。macOS 继续使用系统原生视频层。
