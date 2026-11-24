package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// settings are what the app keeps between launches: the choices of the
// settings dialog, and of the bar under the video that are the same ones
// (连播, the bullet comments, the definition, the volume). They live beside
// the login's cookies, and are saved as soon as one changes.
type settings struct {
	AutoNext   bool    `json:"autoNext"`
	Danmaku    bool    `json:"danmaku"`
	Quality    string  `json:"quality"` // "" or 智能 for Douyin's pick
	Volume     float64 `json:"volume"`
	Muted      bool    `json:"muted"`
	AutoUpdate bool    `json:"autoUpdate"`
	Theme      string  `json:"theme,omitempty"` // "", "light", or "dark"
}

// defaultSettings are a first launch's.
var defaultSettings = settings{Danmaku: true, Volume: 1, AutoUpdate: true}

// settingsFile is where they are kept; "" without a configuration
// directory, as in a test.
func settingsFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "Douyin", "settings.json")
}

// loadSettings reads the settings, the defaults for what is not there, and
// puts them in place.
func (a *app) loadSettings(file string) {
	s := defaultSettings
	if b, err := os.ReadFile(file); err == nil {
		json.Unmarshal(b, &s) // a damaged file leaves the defaults
	} else if !errors.Is(err, os.ErrNotExist) {
		a.toast = "设置没读出来：" + err.Error()
	}
	a.settingsFile, a.saved = file, s
	a.autoNext, a.dmOff, a.quality, a.upd.auto = s.AutoNext, !s.Danmaku, s.Quality, s.AutoUpdate
	a.orbit.theme = s.Theme
	a.applyThemeSource()
	a.setVolume(s.Volume, s.Muted)
}

// current is the settings as they are now.
func (a *app) current() settings {
	return settings{AutoNext: a.autoNext, Danmaku: !a.dmOff, Quality: a.quality,
		Volume: a.volume, Muted: a.muted, AutoUpdate: a.upd.auto, Theme: a.orbit.theme}
}

// saveSettings writes the settings once they changed, as each frame ends.
func (a *app) saveSettings() {
	s := a.current()
	if a.settingsFile == "" || s == a.saved {
		return
	}
	a.saved = s
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := a.settingsFile + ".tmp"
	err := os.MkdirAll(filepath.Dir(a.settingsFile), 0o755)
	if err == nil {
		err = os.WriteFile(tmp, b, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, a.settingsFile)
	}
	if err != nil {
		a.toast = "设置没存上：" + err.Error()
	}
}

// qualityChoices are the settings dialog's definitions, Douyin's pick first.
var qualityChoices = []string{"智能", "1080P", "720P", "540P"}

// settingsDialog is the settings, over the window while open: playback,
// and updates.
func (a *app) settingsDialog(c *ui.Context) {
	ui.DialogBase(c.Key("settings"), &a.settingsOpen, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.Width(420).Padding(18, 20, 20, 20).Gap(6).Radius(14).Background(pal.popover).
			Border(1, pal.popoverBd).Shadow(0, 10, 30, -6, pal.cardShadow).Label("设置")
		ui.Row(c).AlignItems(ui.Center).Margin(0, 0, 8, 0).Children(func() {
			ui.Text(c, "设置").FontSize(17).FontWeight(700).TextColor(pal.strong).Grow(1)
			if iconButton(c.Key("close"), iconClose, 16, pal.muted, "关闭设置").Clicked() {
				a.settingsOpen = false
			}
		})
		section := func(title string) {
			ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(pal.faint).Margin(10, 0, 2, 0)
		}
		row := func(label, note string, control func()) {
			ui.Row(c).Gap(12).AlignItems(ui.Center).MinHeight(40).Children(func() {
				ui.Column(c).Gap(2).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					ui.Text(c, label).FontSize(14).TextColor(pal.text)
					if note != "" {
						ui.Text(c, note).FontSize(11.5).TextColor(pal.faint)
					}
				})
				control()
			})
		}
		if a.c != nil {
			section("账号")
			who := "没登录"
			if a.signedIn {
				who = a.me.Name
			}
			row("抖音账号", who, func() {
				if !a.signedIn {
					if ui.Button(c.Key("login"), "扫码登录").Clicked() {
						a.settingsOpen = false
						a.openLogin()
					}
				} else if ui.Button(c.Key("logout"), "退出登录").Clicked() {
					a.settingsOpen = false
					a.logout()
				}
			})
		}
		section("播放")
		row("连播", "一个视频播完接着播下一个", func() { ui.Switch(c.Key("autoNext"), &a.autoNext).Label("连播开关") })
		on := !a.dmOff
		row("显示弹幕", "", func() {
			if ui.Switch(c.Key("danmaku"), &on).Label("显示弹幕开关").Changed() {
				a.dmOff = !on
			}
		})
		q := a.quality
		if q == "" {
			q = "智能"
		}
		row("清晰度", "智能：由抖音按网速挑选", func() {
			if ui.Select(c.Key("quality"), &q, qualityChoices).Width(110).Label("清晰度").Changed() {
				a.setQuality(q)
			}
		})
		section("更新")
		row("当前版本", "", func() {
			v := mygo.App.Version()
			if v == "" {
				v = "开发版"
			}
			ui.Text(c, v).FontSize(13.5).TextColor(pal.muted)
		})
		u := &a.upd
		if u.check == nil {
			row("自动更新", "这个版本不是正式打包的，不能自动更新", func() {})
			return
		}
		row("自动检查更新", "启动时和之后每 6 小时检查一次", func() { ui.Switch(c.Key("autoUpdate"), &u.auto).Label("自动检查更新") })
		status := ""
		switch {
		case u.checking:
			status = "正在检查…"
		case u.busy && u.progress < 1:
			status = "正在下载 " + percent(u.progress)
		case u.busy:
			status = "正在安装，装好后自动重启"
		case u.err != "":
			status = u.err
		case u.found != nil:
			status = "发现新版本 v" + u.found.Version
		case u.checkErr != "":
			status = u.checkErr
		case u.latest:
			status = "已是最新版本"
		}
		row("检查更新", status, func() {
			switch {
			case u.found != nil && !u.busy:
				if ui.Button(c.Key("install"), "查看更新").Clicked() {
					a.settingsOpen = false
					u.dialogOpen = true
				}
			default:
				b := ui.Button(c.Key("check"), "检查更新")
				if u.checking || u.busy {
					b.Disabled(true)
				} else if b.Clicked() {
					a.checkUpdate(true)
				}
			}
		})
	})
}
