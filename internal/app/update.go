package app

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// MyDouyin updates itself with MyGo's signed updates: mygo build signs each
// release's archive and manifest with the key whose public half mygo.json
// holds, and they go up with the GitHub release. The app reads the latest
// release's manifest at launch and every few hours, unless the settings
// turn that off, and when asked there; a newer version shows at the foot of
// the sidebar, and a click downloads it, checks its signature, puts it in
// place of this copy and starts it. A build of go build, which has no key,
// never checks.

// updateEvery is how often the app checks again while it runs.
const updateEvery = 6 * time.Hour

// release is a newer version, and how to install it.
type release struct {
	Version string
	install func(ctx context.Context, progress func(done, total int64)) error
}

// updates is where updating is at, on the main thread like the rest of app.
type updates struct {
	check    func(context.Context) (*release, error) // nil while the build cannot update; a nil release when up to date
	auto     bool                                    // checking by itself (the settings)
	checking bool
	latest   bool   // the last check found nothing newer
	checkErr string // why the last check asked for in the settings failed
	found    *release
	busy     bool    // downloading and installing found
	progress float64 // of the download, 0 to 1
	err      string  // why installing failed
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
		return &release{Version: u.Version, install: u.Install}, nil
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
				u.found, u.err, u.latest = r, "", false
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
	u.busy, u.progress, u.err = true, 0, ""
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

// updateBanner is at the foot of the sidebar while a newer version is
// there: a click installs it; then how far the download is, or why it
// failed.
func (a *app) updateBanner(c *ui.Context) {
	u := &a.upd
	if u.found == nil {
		return
	}
	b := ui.ButtonBase(c.Key("update")).Column().Gap(3).Padding(10, 12).Margin(0, 2, 6, 2).Radius(10).FocusRing(false).
		Background(ui.RGBA(254, 44, 85, 0.14)).Label("有新版本 " + u.found.Version)
	if b.Hovered() && !u.busy {
		b.Background(ui.RGBA(254, 44, 85, 0.24))
	}
	b.Children(func() {
		title, sub := "有新版本 v"+u.found.Version, "点击更新并重启"
		switch {
		case u.busy && u.progress < 1:
			title, sub = "正在下载更新", percent(u.progress)
		case u.busy:
			title, sub = "正在安装更新", "装好后自动重启"
		case u.err != "":
			sub = u.err + "，点击重试"
		}
		ui.Text(c, title).FontSize(13).FontWeight(700).TextColor(pal.strong)
		ui.Text(c, sub).FontSize(11.5).TextColor(pal.muted).MaxLines(3)
	})
	if b.Clicked() {
		a.installUpdate()
	}
}
