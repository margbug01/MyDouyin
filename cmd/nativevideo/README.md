# AVPlayerLayer 原生直出试验

此命令保留为独立对照入口，在同一个 AVPlayer、同一套 MyGo 界面上比较 RGBA / Bitmap 和 AVPlayerLayer。MyDouyin 从 0.2.2 起默认使用原生显示；正式接入结果见仓库 `PERFORMANCE.md`，下方表格记录此前的轻量原型数据。

```sh
go run ./cmd/nativevideo -mode layer /absolute/path/to/video.mp4
go run ./cmd/nativevideo -mode bitmap /absolute/path/to/video.mp4
```

界面包含移动文字、半透明提示、暂停/继续、跳到 2 秒、填满/适应、主窗口与小窗转移、覆盖弹窗。两种模式均保留原视频分辨率并静音。

自动检查（约 27 秒；需要窗口持续重绘）：

```sh
go run ./cmd/nativevideo -mode layer -check /absolute/path/to/video.mp4
go run ./cmd/nativevideo -mode bitmap -check /absolute/path/to/video.mp4
```

默认输出到 `~/Library/Application Support/Alma Go/temp/YYYY-MM-DD/native-layer/<mode>/`；可用 `-out` 指定独立目录。输出包括各次 `footprint` 和 MyGo 的 UI 截图。检查会验证视频已就绪、窗口绑定正确、时钟暂停/恢复与跳转，切到小窗再回到主窗口，并关闭原生资源。界面回调超过 5 秒或窗口没有持续绘制会报错，不能将这些失败运行用于性能结论。

## 2026-11-16 本机对照

固定 H.264、1920×1080、30 fps、10 秒本地视频；1280×820 主窗口、640×440 小窗；相同动画和控件；GOGC=25；每 5 秒采样一次，五次采样后才执行截图和一次 GC。两种模式均经历暂停、恢复、跳转、循环、小窗及填充模式切换。

当前环境会把窗口视为遮挡，因此性能对照仅在独立 MyGo 0.8.1 测试副本中将遮挡检查设为可见、用定时器触发刷新，两条路径使用相同测试副本。正式 `go.mod` 和已安装应用没有这项适配。最终累计 view 构建数为 3874 / 4045，计数包含布局 pass，不等于视频呈现帧数。

| 指标 | RGBA / Bitmap | AVPlayerLayer |
| --- | ---: | ---: |
| 五次 physical footprint | 109 / 109 / 117 / 118 / 113 MB | 87 / 87 / 97 / 97 / 89 MB |
| physical footprint 均值 | 113.2 MB | 91.4 MB |
| ps 瞬时 CPU 采样中位数 | 38.7% | 15.7% |
| GC 后 Go 堆 | 11.59 MiB | 1.76 MiB |

这个固定原型场景约省 21.8 MB 物理占用（19.3%）。CPU 采样中位数约低 59.4%，不是长期能耗基准。此原型不含完整客户端的封面、列表、登录网页及真实弹幕请求，不能当作整个客户端的内存上限。

## 原型发现的问题及 0.2.2 的处理

- `Window.CapturePage()` 只读取 MyGo 自己的场景。正式应用新增按需取帧的合成截图，取帧结束即释放临时输出，不让截图缓冲驻留播放路径。此独立命令仍保留原始截图对照。
- ScreenCaptureKit 在本机两种模式都曾报告 96×102，而 Cocoa 窗口为 1280×820。此命令拒绝无效截图；系统级窗口截图仍是当前环境的未决限制，合成截图不冒充系统截图。
- 原型曾把 AVPlayerLayer 直接放进 Metal 所在的视图背板，出现过渲染编码器等待。正式接入改为独立 NSView，位于 MyGo 视图下方、后台 WebKit 上方，并对滚动视口裁剪；主播放、小窗、缩放、全屏、页面切换及暂停截图已在实际应用窗口检查。
- 原生接入仍依赖 MyGo 0.8.1 的 `MyGoSurfaceView` 内部视图名。这是有意保留的兼容边界；找不到宿主时恢复像素输出并回退，升级框架时要重新验证。

原始对照日志与素材在本机 Alma Go 的 `temp/2026-11-16/native-layer/`、`memory-020/`；默认接入的窗口检查、合成截图及对照数据位于 `native-default/`，不进入生产构建。
