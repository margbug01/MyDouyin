<div align="center">

<img src="docs/icon.png" width="104" alt="MyDouyin 应用图标">

# MyDouyin

**把抖音放进轻量原生的桌面软件**

基于 Go 与 [MyGo](https://github.com/egoist/mygo) 构建的原生抖音桌面客户端，支持 **macOS（Apple Silicon）** 与 **Windows 10/11（x64）**。非 Electron 套壳，主打低内存、Mac 播视频不写盘、引力场首页与原生丝滑体验。

**简体中文** · [English](README.en.md)

[![最新版本](https://img.shields.io/github/v/release/margbug01/MyDouyin?style=flat-square&label=%E6%9C%80%E6%96%B0%E7%89%88%E6%9C%AC&color=fe2c55)](https://github.com/margbug01/MyDouyin/releases/latest) ![macOS Apple Silicon](https://img.shields.io/badge/macOS-Apple%20Silicon-111111?style=flat-square&logo=apple&logoColor=white) ![Windows 10/11 x64](https://img.shields.io/badge/Windows-10%20%7C%2011%20x64-0078d4?style=flat-square) ![Go + MyGo](https://img.shields.io/badge/Go-MyGo%20Native-00add8?style=flat-square&logo=go&logoColor=white)

**[下载最新版](https://github.com/margbug01/MyDouyin/releases/latest)** · [核心特性](#为什么选择-mydouyin) · [播放预览](#原生视频播放页) · [安装指南](#安装与使用) · [常见问题](#常见问题)

<br>

<img src="docs/screenshots/hero.webp" width="880" alt="MyDouyin 引力场首页截图，展示极净瓷白浅色与纯黑黑洞深色双主题及原生悬浮导航栏">

</div>

> [!IMPORTANT]
> MyDouyin 是个人开源项目，与抖音（Douyin）及字节跳动没有任何关联。应用仅在本地运行，不收集任何统计或遥测数据；请仅使用个人账号合理浏览，并遵守平台服务条款。

## MyDouyin 是什么

**MyDouyin** 是一款面向 macOS 和 Windows 用户的轻量级原生抖音桌面客户端（Douyin Desktop Client）。它使用 Go 语言与原生界面框架 [MyGo](https://github.com/egoist/mygo) 直接渲染 UI 与视频画面，不依赖 Electron 或常驻浏览器内核：macOS 端直接调用系统 AVFoundation 原生视频层硬件解码，Windows 端调用系统自带的 Media Foundation 硬件解码，画面以 GPU 纹理直接绘制。

打开应用即可免登录浏览公开内容与「引力场」精选，需要点赞、收藏、关注、评论、同步观看历史或直播互动时，在应用内扫码登录即可。

## 为什么选择 MyDouyin

| 维度 | MyDouyin（原生桌面端） | 浏览器网页版 | 传统 Electron 套壳客户端 |
| --- | --- | --- | --- |
| **界面与架构** | Go + MyGo 原生绘制，双主题与悬浮胶囊导航 | 依赖浏览器标签页，易被插件和标签打断 | 捆绑整套 Chromium，启动与切换较重 |
| **打开即用体验** | 默认进入「引力场」搜索与精选卡片，不强制自动连播 | 打开首页直接自动播放推荐流 | 打开即播放，后台常驻多进程 |
| **视频解码与写盘** | macOS 走 AVFoundation 原生层，Windows 走系统 Media Foundation，均**不写磁盘缓存** | 浏览器缓存持续增长，多开标签内存飙升 | 渲染与媒体缓存占用较高 |
| **内存控制** | 1080p 原生解码层约 **65 MB**；写操作签名页约 **80 MB**，闲置 10 秒自动释放 | 长时间刷视频易持续上涨 | 常驻内存普遍较高 |
| **更新与维护** | 内置签名增量更新（Delta Update），顶部胶囊一键重启升级 | 无桌面独立形态 | 每次更新常需下载完整安装包 |

## 原生视频播放页

<div align="center">

<img src="docs/screenshots/player.webp" width="880" alt="MyDouyin 原生视频播放页截图，展示高清原生视频画面、右侧点赞评论收藏互动栏与底部弹幕控制条">

</div>

播放页保留完整的抖音观看与互动体验：上下平滑滑动切换视频、悬停即显的自动隐藏进度条、车道避让不重叠的实时弹幕、右侧一键关注作者与展开评论区，以及清晰度、倍速、连播、清屏、听抖音、稍后再看、小窗播放与全屏模式。

## 核心功能

| 模块 | 能力说明 |
| --- | --- |
| **引力场首页（Orbit）** | 启动即进入居中搜索框与 8 张错落环绕的视频卡片，不再一打开就被推荐流强行抓走注意力。支持一键切换「精选 / 关注 / 稍后 / 历史」、无重复「换一批」、纯静搜索与抖音热搜。 |
| **双主题与悬浮导航** | 内置「纯黑黑洞」与「极净瓷白」两套全页面主题，右上角一键切换并记住偏好。左侧悬浮胶囊导航静息态收起为极简图标，悬停展开完整频道（引力场、精选、推荐、关注、朋友、直播、短剧）。 |
| **原生视频与直播** | macOS 采用系统 AVFoundation 原生视频层，支持预读下一条视频信息缩短起播等待；Windows 使用系统 Media Foundation 播放点播与 HLS 直播，无需附带解码器；直播线路出不了画面时自动换下一条线路。 |
| **评论区与带图互动** | 支持虚拟化滚动评论列表、楼中楼展开与回复、抖音官方表情面板、粘贴截图或拖拽发带图评论、点击评论图片查看大图（缩放/切换/保存），以及点头像、昵称与 `@` 提及直达作者主页。 |
| **完整频道与账号同步** | 覆盖推荐、精选分区、关注、朋友、直播间（含实时弹幕、评论与礼物互动）、短剧、合集、作者主页、我的喜欢/收藏/稍后再看、私信（只读）与通知；观看满 2 秒自动攒批上报观看历史，刷推荐不再反复刷到看过的视频。 |
| **轻量签名与隐私安全** | 仅在点赞、收藏、发评论、发弹幕等写操作时按需拉起约 80 MB 的轻量签名页，闲置 10 秒自动释放；单实例运行防止多开互踢登录；所有凭证与偏好仅保存在本机。 |

## 性能与资源实测

以下数据均来自本仓库实测记录（详见 [PERFORMANCE.md](PERFORMANCE.md) 与 [CHANGELOG.md](CHANGELOG.md)）：

- **原生视频解码更省内存**：macOS 改用 `AVPlayerLayer` 原生视频层后，1080p 播放时系统解码器内存由约 **180 MB** 降至约 **65 MB**。
- **Mac 刷视频不再写盘**：通过 `AVURLAssetUsesNoPersistentCacheKey` 禁用系统持久磁盘缓存，视频仅在内存中边播边预读，消除此前连看 8 条写入约 **300 MB** 临时缓存的问题。
- **写操作后台页低占且即用即放**：轻量签名页内存约 **80 MB**（对比完整网页约 340–520 MB），操作完成并闲置 **10 秒** 后自动销毁，不常驻后台。
- **长列表不越刷越卡**：视频网格与评论列表采用虚拟化按需渲染，封面、头像和评论配图按显示尺寸解码并在上传 GPU 后立即释放 CPU 像素内存。

## 安装与使用

### macOS（Apple Silicon）

1. 前往 [GitHub Releases](https://github.com/margbug01/MyDouyin/releases/latest) 下载最新的 `mydouyin-<版本>-arm64.dmg`。
2. 打开 `.dmg`，将 **MyDouyin.app** 拖入「应用程序（Applications）」文件夹。
3. 首次启动若被 macOS 拦截（未经过 Apple 公证），请前往「系统设置 › 隐私与安全性」底部点击「仍要打开」，或在终端执行：

```sh
xattr -dr com.apple.quarantine /Applications/MyDouyin.app
```

4. **应用内更新**：程序放在「应用程序」目录后，有新版本时右上角会出现「新版本」胶囊，点击即可查看更新说明、增量下载并自动重启升级。

### Windows 10 / 11（x64）

1. 前往 [GitHub Releases](https://github.com/margbug01/MyDouyin/releases/latest) 下载 `mydouyin-setup-<版本>-amd64.exe` 安装程序（或免安装压缩包 `mydouyin-<版本>-windows-amd64.tar.gz`）。
2. 视频由系统自带的 Media Foundation 播放，**无需额外安装解码器**。Windows N / KN 版默认不带媒体功能，需先在「设置 › 应用 › 可选功能」中安装 Media Feature Pack。
3. 首次运行若触发 Windows SmartScreen 提示，点击「更多信息 › 仍要运行」即可，也可使用 Release 中的 `SHA256SUMS` 校验文件完整性。

### 常用快捷键

| 操作 | 快捷键 |
| --- | --- |
| 播放 / 暂停当前视频 | `Space`（空格） |
| 切换上一条 / 下一条视频 | `↑` / `↓` |
| 关闭弹窗 / 退出大图预览或小窗 / 返回上级 | `Esc` |
| 评论原图预览切换上一张 / 下一张 | `←` / `→` |
| 评论原图放大 / 缩小 / 还原比例 | `+` / `-` / `0` |

## 从源码运行与构建

需要 **Go 1.27.3** 或更新版本（通过 `go tool mygo` 调用内置 MyGo 工具链）。

```sh
# 开发运行与测试
go run .
go test ./...
```

### 发布构建（带磁盘上限控制）

- **macOS（支持同时构建 macOS + Windows 发布包）**：

```sh
./scripts/build-release.sh
```

脚本会在系统临时目录导出干净 Git 提交快照并在退出时自动删除，构建完成后 `build.noindex/release-*` 默认只保留最新 **1** 份（可通过 `--keep N` 或 `KEEP_RELEASES=N` 调整，上传后运行 `./scripts/build-release.sh --prune-only --keep 0` 可清空）。

- **Windows（PowerShell 7）**：

```powershell
./scripts/build-windows.ps1
```

默认同样只保留最新 1 份 `build.noindex/release-*`（`-KeepReleases` 可调）。

## 常见问题

<details>
<summary><strong>MyDouyin 支持哪些操作系统？</strong></summary>

MyDouyin 目前稳定支持 **macOS（Apple Silicon / arm64）** 与 **Windows 10 / 11（x64）**。macOS 使用原生 AVFoundation 播放视频，Windows 使用系统自带的 Media Foundation（N / KN 版需安装 Media Feature Pack）。
</details>

<details>
<summary><strong>不登录抖音账号可以直接使用 MyDouyin 吗？</strong></summary>

可以。首次启动 MyDouyin 时点击扫码弹窗中的「以后再说」，即可直接浏览引力场精选、推荐视频、分类精选、搜索、作者主页与直播公开内容。需要点赞、收藏、关注、发评论、发弹幕或同步观看历史时，再点击右上角「登录」扫码即可。
</details>

<details>
<summary><strong>macOS 提示“无法验证开发者”或“已损坏”怎么办？</strong></summary>

MyDouyin 尚未进行 Apple 官方公证，macOS Gatekeeper 会默认拦截首次打开。将应用拖入 `/Applications` 后，在「系统设置 › 隐私与安全性」点击「仍要打开」，或在终端运行 `xattr -dr com.apple.quarantine /Applications/MyDouyin.app` 即可正常使用。
</details>

<details>
<summary><strong>MyDouyin 会把刷过的视频缓存到硬盘导致磁盘爆满吗？</strong></summary>

不会。在 macOS 上，MyDouyin 播放视频时显式启用 `AVURLAssetUsesNoPersistentCacheKey`，只做小量内存预读而不写入 `$TMPDIR/MediaCache`；视频临时目录也会在启动时自动清理。
</details>

<details>
<summary><strong>登录态和个人设置保存在哪里？</strong></summary>

所有登录凭证、观看记录待上报队列与偏好设置均保存在本机系统标准配置目录中（macOS 位于 `~/Library/Application Support/Douyin` 与 `~/Library/Application Support/mydouyin`，Windows 位于 `%APPDATA%` 下），覆盖升级或应用内更新不会丢失登录状态。
</details>
