package app

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	"douyin/internal/dy"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type picViewer struct {
	open   bool
	pics   []dy.CommentPic
	idx    int
	zoom   float32
	saving bool
}

func (a *app) openPicViewer(pics []dy.CommentPic, idx int) {
	if len(pics) == 0 {
		return
	}
	if idx < 0 || idx >= len(pics) {
		idx = 0
	}
	a.picViewer = picViewer{open: true, pics: pics, idx: idx, zoom: 1}
}

func (v *picViewer) step(delta int) {
	if len(v.pics) <= 1 {
		return
	}
	next := (v.idx + delta) % len(v.pics)
	if next < 0 {
		next += len(v.pics)
	}
	v.idx = next
	v.zoom = 1
}

func (v *picViewer) setZoom(z float32) {
	if z < 0.5 {
		z = 0.5
	}
	if z > 3 {
		z = 3
	}
	v.zoom = z
}

func (a *app) picViewerDialog(c *ui.Context) {
	v := &a.picViewer
	if !v.open || len(v.pics) == 0 {
		v.open = false
		return
	}
	if v.idx < 0 || v.idx >= len(v.pics) {
		v.idx = 0
	}
	if v.zoom <= 0 {
		v.zoom = 1
	}
	pic := v.pics[v.idx]
	fullURL := pic.Full()
	bmp := a.img.getFull(fullURL)
	if bmp == nil && pic.Thumb != "" {
		bmp = a.img.getSized(pic.Thumb, commentPicEdge)
	}

	winW, winH := c.Size()
	if winW <= 0 {
		winW = 960
	}
	if winH <= 0 {
		winH = 680
	}
	stageW := max(float32(320), winW-140)
	stageH := max(float32(260), winH-150)

	ui.DialogBase(c.Key("comment-pic-viewer"), &v.open, func(back, panel ui.Element) {
		if panel.OverlayShortcut(0, ui.KeyLeft) {
			v.step(-1)
		}
		if panel.OverlayShortcut(0, ui.KeyRight) {
			v.step(1)
		}
		if panel.OverlayShortcut(0, ui.KeyEqual) {
			v.setZoom(v.zoom + 0.25)
		}
		if panel.OverlayShortcut(0, ui.KeyMinus) {
			v.setZoom(v.zoom - 0.25)
		}
		if panel.OverlayShortcut(0, ui.Key0) {
			v.zoom = 1
		}
		back.Background(ui.RGBA(0, 0, 0, 0.78))
		panel.Gap(10).Padding(12, 16, 16, 16).Radius(14).Background(ui.RGBA(18, 20, 28, 0.94)).Label("查看评论图片")

		ui.Row(c.Key("bar")).Gap(8).AlignItems(ui.Center).Children(func() {
			title := "图片预览"
			if len(v.pics) > 1 {
				title = fmt.Sprintf("%d / %d", v.idx+1, len(v.pics))
			}
			ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(vid.text).Grow(1)

			toolBtn := func(key string, icon *ui.SVG, label string) bool {
				b := ui.ButtonBase(c.Key(key)).Size(28, 28).Radius(7).Center().FocusRing(false).Label(label)
				if b.Hovered() {
					b.Background(ui.RGBA(255, 255, 255, 0.12))
				}
				b.Children(func() {
					ui.Icon(c, icon).Size(15, 15).TextColor(vid.text)
				})
				return b.Clicked()
			}
			if toolBtn("zoom-out", iconZoomOut, "缩小") {
				v.setZoom(v.zoom - 0.25)
			}
			pct := ui.ButtonBase(c.Key("zoom-reset")).Padding(4, 6).Radius(6).FocusRing(false).Label("重置缩放")
			if pct.Hovered() {
				pct.Background(ui.RGBA(255, 255, 255, 0.12))
			}
			pct.Children(func() {
				ui.Text(c, fmt.Sprintf("%d%%", int(v.zoom*100+0.5))).FontSize(12).TextColor(vid.muted)
			})
			if pct.Clicked() {
				v.zoom = 1
			}
			if toolBtn("zoom-in", iconZoomIn, "放大") {
				v.setZoom(v.zoom + 0.25)
			}
			if toolBtn("copy-pic", iconCopy, "复制图片") {
				a.copyCommentPic(fullURL)
			}
			if toolBtn("save-pic", iconDownload, "保存图片") {
				a.saveCommentPic(fullURL)
			}
			if toolBtn("close-pic", iconClose, "关闭图片预览") {
				v.open = false
			}
		})

		ui.Row(c.Key("stage-row")).Gap(10).AlignItems(ui.Center).Children(func() {
			if len(v.pics) > 1 {
				nav := ui.ButtonBase(c.Key("prev-pic")).Size(34, 34).Radius(17).Center().FocusRing(false).Label("上一张")
				nav.Background(onOff(nav.Hovered(), ui.RGBA(255, 255, 255, 0.22), ui.RGBA(255, 255, 255, 0.1)))
				nav.Children(func() { ui.Icon(c, iconBack).Size(16, 16).TextColor(white) })
				if nav.Clicked() {
					v.step(-1)
				}
			}

			drawW, drawH := float32(320), float32(320)
			if bmp != nil {
				bw, bh := float32(0), float32(0)
				iw, ih := bmp.Size()
				bw, bh = float32(iw), float32(ih)
				if bw > 0 && bh > 0 {
					fit := min(stageW/bw, stageH/bh)
					if fit > 1 && !pic.Sticker {
						fit = min(fit, 2)
					}
					drawW, drawH = bw*fit*v.zoom, bh*fit*v.zoom
				}
			}
			boxW := min(stageW, max(float32(240), drawW))
			boxH := min(stageH, max(float32(200), drawH))

			renderImg := func() {
				if bmp == nil {
					ui.Box(c).Size(boxW, boxH).Center().Children(func() {
						ui.Text(c, "图片载入中…").FontSize(13).TextColor(vid.muted)
					})
					return
				}
				imgBtn := ui.ButtonBase(c.Key("pic-canvas")).Size(drawW, drawH).FocusRing(false).Label("切换图片缩放")
				imgBtn.Children(func() {
					ui.Image(c, bmp).Size(drawW, drawH)
				})
				if imgBtn.Clicked() {
					if v.zoom > 1.05 {
						v.zoom = 1
					} else {
						v.zoom = 1.75
					}
				}
			}

			if drawW > stageW+1 || drawH > stageH+1 {
				ui.ScrollBoth(c.Key("pic-scroll")).Size(stageW, stageH).Radius(8).Children(renderImg)
			} else {
				ui.Box(c.Key("pic-frame")).Size(boxW, boxH).Center().Radius(8).Clip().Children(renderImg)
			}

			if len(v.pics) > 1 {
				nav := ui.ButtonBase(c.Key("next-pic")).Size(34, 34).Radius(17).Center().FocusRing(false).Label("下一张")
				nav.Background(onOff(nav.Hovered(), ui.RGBA(255, 255, 255, 0.22), ui.RGBA(255, 255, 255, 0.1)))
				nav.Children(func() { ui.Icon(c, iconNext).Size(16, 16).TextColor(white) })
				if nav.Clicked() {
					v.step(1)
				}
			}
		})
	})
}

func (a *app) copyCommentPic(rawURL string) {
	if a.c == nil || rawURL == "" {
		return
	}
	go func() {
		data, err := a.c.Image(rawURL)
		if err == nil {
			err = mygo.Clipboard.WriteImage(data)
		}
		a.post(func() {
			if err != nil {
				a.toast = "复制图片失败：" + err.Error()
			} else {
				a.toast = "图片已复制"
			}
		})
	}()
}

func (a *app) saveCommentPic(rawURL string) {
	if a.c == nil || rawURL == "" || a.picViewer.saving {
		return
	}
	a.picViewer.saving = true
	win := a.win
	go func() {
		defer a.post(func() { a.picViewer.saving = false })
		data, err := a.c.Image(rawURL)
		if err != nil {
			a.post(func() { a.toast = "下载图片失败：" + err.Error() })
			return
		}
		ext := guessImageExt(rawURL, data)
		dest, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent:      win,
			Title:       "保存评论图片",
			DefaultPath: "douyin-comment" + ext,
		})
		if err != nil || dest == "" {
			return
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			a.post(func() { a.toast = "保存失败：" + err.Error() })
			return
		}
		a.post(func() { a.toast = "已保存图片" })
	}()
}

func guessImageExt(rawURL string, data []byte) string {
	if u, err := url.Parse(rawURL); err == nil {
		if ext := strings.ToLower(path.Ext(u.Path)); ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" || ext == ".gif" {
			return ext
		}
	}
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return ".png"
	}
	if len(data) >= 4 && string(data[:4]) == "GIF8" {
		return ".gif"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return ".webp"
	}
	return ".jpg"
}
