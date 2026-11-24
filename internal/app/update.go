package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// MyDouyin updates itself with MyGo's signed updates: mygo build signs each
// release's archive and manifest with the key whose public half mygo.json
// holds, and they go up with the GitHub release. The app reads the latest
// release's manifest at launch and every few hours, unless the settings
// turn that off, and when asked there; a newer version shows as a compact pill
// in the top-right bar, and clicking it opens the release notes and install
// dialog. A build of go build, which has no key, never checks.

// updateEvery is how often the app checks again while it runs.
const updateEvery = 6 * time.Hour

// release is a newer version, its notes, and how to install it.
type release struct {
	Version string
	Notes   string
	install func(ctx context.Context, progress func(done, total int64)) error
}

// updates is where updating is at, on the main thread like the rest of app.
type updates struct {
	check      func(context.Context) (*release, error) // nil while the build cannot update; a nil release when up to date
	auto       bool                                    // checking by itself (the settings)
	checking   bool
	latest     bool   // the last check found nothing newer
	checkErr   string // why the last check asked for in the settings failed
	found      *release
	dialogOpen bool // the release notes & install dialog is open
	dismissed  bool // user clicked 稍后再说 for the current found version
	busy       bool // downloading and installing found
	progress   float64
	err        string // why installing failed
}

// watchUpdates checks for a newer version now and every updateEvery, when
// the build can update itself.
func (a *app) watchUpdates() {
	if !mygo.Updater.Enabled() {
		return
	}
	a.upd.check = func(ctx context.Context) (*release, error) {
		u, err := mygo.Updater.Check(ctx)
		if err != nil || u == nil {
			return nil, err
		}
		return &release{Version: u.Version, Notes: strings.TrimSpace(u.Notes), install: u.Install}, nil
	}
	go func() {
		for {
			a.post(func() {
				if a.upd.auto {
					a.checkUpdate(false)
				}
			})
			time.Sleep(updateEvery)
		}
	}()
}

// checkUpdate asks whether there is a newer version. manual is the settings
// asking, which are told why it failed; the app's own check is quiet, the
// next one comes in updateEvery.
func (a *app) checkUpdate(manual bool) {
	u := &a.upd
	if u.check == nil || u.busy || u.checking {
		return
	}
	check := u.check
	u.checking, u.checkErr = true, ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r, err := check(ctx)
		a.post(func() {
			u.checking = false
			switch {
			case err != nil:
				if manual {
					u.checkErr = "检查失败：" + err.Error()
				}
			case r == nil:
				u.latest = true
			case !u.busy:
				prev := ""
				if u.found != nil {
					prev = u.found.Version
				}
				u.found, u.err, u.latest = r, "", false
				if manual || r.Version != prev {
					u.dismissed = false
				}
			}
		})
	}()
}

// percent is a share from 0 to 1 as people read it.
func percent(f float64) string { return fmt.Sprintf("%d%%", int(f*100)) }

// installUpdate downloads the version found, checks it and puts it in place,
// then starts it.
func (a *app) installUpdate() {
	u := &a.upd
	r := u.found
	if r == nil || u.busy {
		return
	}
	u.busy, u.progress, u.err, u.dismissed = true, 0, "", false
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		shown := -1
		err := r.install(ctx, func(done, total int64) {
			if total <= 0 {
				return
			}
			if pct := int(done * 100 / total); pct != shown { // a frame a percent, not a chunk
				shown = pct
				a.post(func() { u.progress = float64(pct) / 100 })
			}
		})
		a.post(func() {
			u.busy = false
			if err != nil {
				u.err = "更新没装上：" + err.Error()
				return
			}
			mygo.App.Relaunch()
		})
	}()
}

func (a *app) showUpdatePill() bool {
	u := &a.upd
	return u.found != nil && (!u.dismissed || u.busy || u.err != "")
}

// updatePill sits in the top-right title bar when a newer version is available
// so it never overlaps the video player or its bottom controls.
func (a *app) updatePill(c *ui.Context) {
	if !a.showUpdatePill() {
		return
	}
	u := &a.upd
	w, _ := c.Size()
	compact := w > 0 && w < 1080
	text := "新版本 v" + u.found.Version
	if compact {
		text = "新版本"
	}
	switch {
	case u.busy && u.progress < 1:
		text = "下载 " + percent(u.progress)
	case u.busy:
		text = "安装中…"
	case u.err != "":
		text = "更新失败"
	}

	b := ui.ButtonBase(c.Key("update-pill")).Height(32).Padding(0, 11, 0, 9).Gap(6).Radius(16).
		AlignItems(ui.Center).FocusRing(false).Label("有新版本 v" + u.found.Version)
	bg := onOff(b.Hovered() || u.dialogOpen, pal.pillHoverBg, pal.pillBg)
	bd := pal.accent.Alpha(onOff(b.Hovered() || u.dialogOpen, float32(0.75), float32(0.45)))
	b.Background(bg).Border(1, bd).Children(func() {
		ui.Box(c).Size(7, 7).Radius(4).Background(pal.accent)
		ui.Text(c, text).FontSize(12).FontWeight(600).TextColor(pal.strong).SingleLine()
	})
	if b.Clicked() {
		u.dialogOpen = !u.dialogOpen
	}
}

// updateDialog shows the found release's version, release notes, download
// progress, and install / dismiss controls over the window.
func (a *app) updateDialog(c *ui.Context) {
	u := &a.upd
	if !u.dialogOpen || u.found == nil {
		u.dialogOpen = false
		return
	}
	r := u.found
	ui.DialogBase(c.Key("update-dialog"), &u.dialogOpen, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.Width(400).Padding(18, 20, 20, 20).Gap(12).Radius(14).Background(pal.popover).
			Border(1, pal.popoverBd).Shadow(0, 10, 30, -6, pal.cardShadow).Label("发现新版本")

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(9, 9).Radius(5).Background(pal.accent)
			title := "发现新版本 v" + r.Version
			if u.busy && u.progress < 1 {
				title = "正在下载 v" + r.Version
			} else if u.busy {
				title = "正在安装 v" + r.Version
			}
			ui.Text(c, title).FontSize(16).FontWeight(700).TextColor(pal.strong).Grow(1)
			if cur := mygo.App.Version(); cur != "" {
				ui.Text(c, "当前 v"+cur).FontSize(11.5).TextColor(pal.faint)
			}
			if iconButton(c.Key("close-update"), iconClose, 16, pal.muted, "关闭更新窗口").Clicked() {
				u.dialogOpen = false
			}
		})

		lines := parseReleaseNotes(r.Notes)
		if len(lines) > 0 {
			ui.Column(c).Gap(6).Padding(10, 12).Radius(10).Background(pal.hover).Children(func() {
				ui.Text(c, "更新内容").FontSize(12).FontWeight(600).TextColor(pal.muted)
				ui.Scroll(c.Key("update-notes")).MaxHeight(180).Gap(6).Children(func() {
					for i, line := range lines {
						ui.Row(c.Key(i)).Gap(8).AlignItems(ui.Start).Children(func() {
							ui.Box(c).Size(5, 5).Radius(3).Background(pal.accent).Margin(6, 0, 0, 0)
							ui.Text(c, line).FontSize(12.5).TextColor(pal.text).Grow(1).Shrink(1).MinWidth(0)
						})
					}
				})
			})
		} else {
			ui.Text(c, "包含最新功能改进与问题修复，点击下方按钮下载并重启应用。").FontSize(13).TextColor(pal.muted)
		}

		if u.busy {
			ui.Column(c).Gap(6).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					status := "正在下载更新…"
					right := percent(u.progress)
					if u.progress >= 1 {
						status = "正在安装更新，完成后自动重启"
						right = ""
					}
					ui.Text(c, status).FontSize(12).TextColor(pal.muted).Grow(1)
					if right != "" {
						ui.Text(c, right).FontSize(12).FontWeight(600).TextColor(pal.strong)
					}
				})
				const trackW = float32(360)
				fillW := max(float32(0), min(trackW, trackW*float32(u.progress)))
				ui.Box(c).Width(trackW).Height(6).Radius(3).Clip().Background(pal.hover).Children(func() {
					if fillW > 0 {
						ui.Box(c).Width(fillW).Height(6).Radius(3).Background(pal.accent)
					}
				})
			})
		} else if u.err != "" {
			ui.Text(c, u.err).FontSize(12).TextColor(pal.accent).MaxLines(3)
		}

		ui.Row(c).Gap(8).Justify(ui.End).AlignItems(ui.Center).Margin(4, 0, 0, 0).Children(func() {
			if !u.busy {
				later := ui.ButtonBase(c.Key("update-later")).Height(32).PaddingX(14).Radius(8).Center().FocusRing(false).Label("稍后再说")
				later.Background(onOff(later.Hovered(), pal.hover, ui.Color{})).Children(func() {
					ui.Text(c, "稍后再说").FontSize(13).TextColor(pal.muted)
				})
				if later.Clicked() {
					u.dialogOpen = false
					u.dismissed = true
				}
			}
			btnText := "更新并重启"
			switch {
			case u.busy && u.progress < 1:
				btnText = "下载中…"
			case u.busy:
				btnText = "安装中…"
			case u.err != "":
				btnText = "重试更新"
			}
			primary := ui.ButtonBase(c.Key("update-install")).Height(32).PaddingX(16).Radius(8).Center().FocusRing(false).
				Label(btnText).Disabled(u.busy)
			primary.Background(onOff(u.busy, pal.hover, onOff(primary.Hovered(), pal.accent.Alpha(0.9), pal.accent))).Children(func() {
				ui.Text(c, btnText).FontSize(13).FontWeight(600).TextColor(onOff(u.busy, pal.faint, white))
			})
			if primary.Clicked() {
				a.installUpdate()
			}
		})
	})
}

func parseReleaseNotes(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimLeft(s, "-*• ")
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
