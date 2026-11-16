# AVPlayerLayer 原生直出试验

这是独立试验入口，不影响 MyDouyin 0.2.1 的默认播放器。目标是在同一个 AVPlayer、同一套 MyGo 界面上比较 RGBA / Bitmap 和 AVPlayerLayer 两条画面路径。

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

## 尚未满足默认接入条件

- `Window.CapturePage()` 读取 MyGo 自己的绘制场景，不包含 AVPlayerLayer。UI 文本、控件及弹窗可以截图，但原生视频区域仍为空。
- 本次 ScreenCaptureKit 自身窗口截图在两种模式都报告 96×102，而 Cocoa 窗口为 1280×820。检查会拒绝这类无效截图；尚未获得可确认原生画面与控件正确合成的窗口截图。
- 早期未固定最小窗口尺寸的原生运行出现过 Metal 渲染编码器等待超过 5 秒；固定主窗口和小窗最小尺寸后的最终对照通过。任意缩放、全屏和完整客户端遮罩仍未验证。
- `Layer.Place` 通过 Cocoa 寻找 MyGo 0.8.1 的 `MyGoSurfaceView`，依赖该内部视图名。默认接入前应明确维护这个边界或增加框架级原生视图接口。

因此保留独立试验入口，暂不改默认播放器、不替换已安装版本。原始对照日志及试验素材在本机 Alma Go 的 `temp/2026-11-16/native-layer/` 和 `temp/2026-11-16/memory-020/`，不进入生产构建。
