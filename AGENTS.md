# MyDouyin

## 打包与磁盘占用规则

- 正式/跨平台打包一律走 `./scripts/build-release.sh`（macOS）或 `./scripts/build-windows.ps1`（Windows），不要手工在 `build.noindex/release-*` 里建 `src/` 快照或往 `Alma Go/temp` 拷贝 Windows `ffmpeg.exe` / `ffprobe.exe`。
- `build.noindex/release-*` 默认只保留最新 **1** 份（可通过 `KEEP_RELEASES` / `--keep` / `-KeepReleases` 调整），构建失败自动清理半成品目录。
- 发布上传完成后如需进一步释放空间，运行 `./scripts/build-release.sh --prune-only --keep 0`。
- Windows 打包所需的 `ffmpeg.exe` 与 `ffprobe.exe` 统一单份缓存在 `~/Library/Caches/mydouyin-win-ffmpeg`（macOS 跨平台构建时用 APFS clone 复制到临时源码快照，退出即删除）。
- 测试和一次性验证脚本使用 `t.TempDir()` 或 `$TMPDIR` 并在结束时清理，不要在 `~/Library/Caches` 下以自定义应用名遗留测试缓存目录。
