package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// settings are what the app keeps between launches: the choices of the
// settings dialog, and of the bar under the video that are the same ones
// (连播, the bullet comments, the definition, the volume). They live beside
// the login's cookies, and are saved as soon as one changes.
type settings struct {
	AutoNext       bool    `json:"autoNext"`
	Danmaku        bool    `json:"danmaku"`
	DanmakuSpeed   float64 `json:"danmakuSpeed,omitempty"`
	DanmakuSize    float32 `json:"danmakuSize,omitempty"`
	DanmakuStroke  string  `json:"danmakuStroke,omitempty"`
	DanmakuOpacity float32 `json:"danmakuOpacity,omitempty"`
	Quality        string  `json:"quality"`               // "" or 智能 for Douyin's pick
	LiveQuality    string  `json:"liveQuality,omitempty"` // "" or 智能 for live default
	Volume         float64 `json:"volume"`
	PlaybackSpeed  float64 `json:"playbackSpeed"`
	Muted          bool    `json:"muted"`
	AutoUpdate     bool    `json:"autoUpdate"`
	Theme          string  `json:"theme,omitempty"` // "", "light", or "dark"
	Mode           string  `json:"mode,omitempty"`
	HGDirect       *bool   `json:"hgDirect,omitempty"`
	HGSignerURL    string  `json:"hgSignerURL,omitempty"`
	HGSignerToken  string  `json:"hgSignerToken,omitempty"`
}

func (s settings) equal(other settings) bool {
	d1, d2 := true, true
	if s.HGDirect != nil {
		d1 = *s.HGDirect
	}
	if other.HGDirect != nil {
		d2 = *other.HGDirect
	}
	return s.AutoNext == other.AutoNext &&
		s.Danmaku == other.Danmaku &&
		s.DanmakuSpeed == other.DanmakuSpeed &&
		s.DanmakuSize == other.DanmakuSize &&
		s.DanmakuStroke == other.DanmakuStroke &&
		s.DanmakuOpacity == other.DanmakuOpacity &&
		s.Quality == other.Quality &&
		s.LiveQuality == other.LiveQuality &&
		s.Volume == other.Volume &&
		s.PlaybackSpeed == other.PlaybackSpeed &&
		s.Muted == other.Muted &&
		s.AutoUpdate == other.AutoUpdate &&
		s.Theme == other.Theme &&
		s.Mode == other.Mode &&
		d1 == d2 &&
		s.HGSignerURL == other.HGSignerURL &&
		s.HGSignerToken == other.HGSignerToken
}

// defaultSettings are a first launch's. HGDirect stays nil so unmarshaling
// cannot mutate a shared pointer and absent values default to true.
var defaultSettings = settings{Danmaku: true, Volume: 1, PlaybackSpeed: 1, AutoUpdate: true}

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
	hgDirect := true
	if s.HGDirect != nil {
		hgDirect = *s.HGDirect
	}
	a.autoNext, a.dmOff, a.quality, a.liveQuality, a.upd.auto = s.AutoNext, !s.Danmaku, s.Quality, s.LiveQuality, s.AutoUpdate
	a.setDanmakuStyle(danmakuStyle{
		Speed:   s.DanmakuSpeed,
		Size:    s.DanmakuSize,
		Stroke:  s.DanmakuStroke,
		Opacity: s.DanmakuOpacity,
	})
	a.orbit.theme = s.Theme
	a.mode = parseAppMode(s.Mode)
	a.hgDirect = hgDirect
	a.hgSignerURL = strings.TrimSpace(s.HGSignerURL)
	a.hgSignerToken = strings.TrimSpace(s.HGSignerToken)
	a.applyThemeSource()
	a.setVolume(s.Volume, s.Muted)
	a.setSpeed(s.PlaybackSpeed)
	a.settingsFile, a.saved = file, a.current()
}

// current is the settings as they are now.
func (a *app) current() settings {
	hgDirect := a.hgDirect
	dm := a.danmakuStyle()
	return settings{
		AutoNext:       a.autoNext,
		Danmaku:        !a.dmOff,
		DanmakuSpeed:   dm.Speed,
		DanmakuSize:    dm.Size,
		DanmakuStroke:  dm.Stroke,
		DanmakuOpacity: dm.Opacity,
		Quality:        a.quality,
		LiveQuality:    a.liveQuality,
		Volume:         a.volume,
		PlaybackSpeed:  a.playSpeed(),
		Muted:          a.muted,
		AutoUpdate:     a.upd.auto,
		Theme:          a.orbit.theme,
		Mode:           a.mode.String(),
		HGDirect:       &hgDirect,
		HGSignerURL:    strings.TrimSpace(a.hgSignerURL),
		HGSignerToken:  strings.TrimSpace(a.hgSignerToken),
	}
}

// saveSettings writes the settings once they changed, as each frame ends.
func (a *app) saveSettings() {
	s := a.current()
	if a.settingsFile == "" || s.equal(a.saved) {
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
var qualityChoices = []string{"智能", "4K", "2K", "1080P", "720P", "540P"}

// settingsPage is a page of the settings, which the dialog's side lists.
type settingsPage int

const (
	settingsPlay settingsPage = iota
	settingsDanmaku
	settingsHongguo
	settingsAccount
	settingsUpdate
)

var settingsPageNames = [...]string{"播放", "弹幕", "红果短剧", "账号", "更新"}

// settingsDialog is the settings, over the window while open: its pages
// listed down the side, the one chosen beside them in cards of rows. It
// opens on the mode's own page.
func (a *app) settingsDialog(c *ui.Context) {
	if a.settingsOpen && !a.settingsWasOpen {
		a.settingsPage = onOff(a.mode == modeHongguo, settingsHongguo, settingsPlay)
	}
	// The signer's fields apply as they lose focus, the page or the dialog
	// closing with them.
	urlFocused, tokFocused := c.Focused(a.hgSignerURLBox), c.Focused(a.hgSignerTokenBox)
	if (a.hgSignerURLFocused && !urlFocused || a.hgSignerTokenFocused && !tokFocused) && a.hg != nil && a.hgConfig() != a.hg.cfg {
		a.reconfigureHG()
	}
	a.hgSignerURLFocused, a.hgSignerTokenFocused = urlFocused, tokFocused
	_, winH := c.Size()
	ui.DialogBase(c.Key("settings"), &a.settingsOpen, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.Width(720).Height(min(520, winH-48)).Radius(14).Clip().Background(pal.popover).
			Border(1, pal.popoverBd).Shadow(0, 10, 30, -6, pal.cardShadow).Label("设置")
		ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c.Key("settings-nav")).Width(172).Padding(22, 12, 22, 12).Gap(2).
				Background(onOff(pal.dark, pal.raised, pal.bg)).Role(ui.RoleTabList).Children(func() {
				ui.Text(c, "设置").FontSize(17).FontWeight(700).TextColor(pal.strong).Padding(0, 10, 14, 10)
				for i, name := range settingsPageNames {
					p := settingsPage(i)
					if p == settingsAccount && a.c == nil {
						continue
					}
					on := p == a.settingsPage
					b := ui.ButtonBase(c.Key("settings-nav-" + name)).Role(ui.RoleTab).AccessSelected(on).Label(name).
						Height(36).PaddingX(12).Radius(8).AlignItems(ui.Center).Justify(ui.Start).FocusRing(false)
					switch {
					case on:
						b.Background(pal.press)
					case b.Hovered() || b.FocusVisible():
						b.Background(pal.hover)
					}
					b.Children(func() {
						ui.Text(c, name).FontSize(14).FontWeight(onOff(on, 600, 400)).TextColor(onOff(on, pal.strong, pal.muted))
					})
					if b.Clicked() {
						a.settingsPage = p
					}
				}
			})
			ui.Box(c).Width(1).Background(pal.hairline)
			ui.Column(c).Grow(1).MinWidth(0).Padding(22, 16, 0, 28).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Padding(0, barGutter, 0, 0).Margin(0, 0, 16, 0).Children(func() {
					ui.Text(c, settingsPageNames[a.settingsPage]).FontSize(15).FontWeight(700).TextColor(pal.strong).Grow(1)
					if iconButton(c.Key("close"), iconClose, 16, pal.muted, "关闭设置").Clicked() {
						a.settingsOpen = false
					}
				})
				ui.Scroll(c.Key(fmt.Sprint("settings-page", a.settingsPage))).Grow(1).Padding(0, barGutter, 24, 0).Children(func() {
					a.settingsBody(c)
				})
			})
		})
	})
	if a.settingsWasOpen && !a.settingsOpen {
		a.hgSignerURLFocused, a.hgSignerTokenFocused = false, false
		if a.hg != nil && a.hgConfig() != a.hg.cfg {
			a.reconfigureHG()
		}
	}
	a.settingsWasOpen = a.settingsOpen
}

// settingsBody is the rows of the page chosen, in cards.
func (a *app) settingsBody(c *ui.Context) {
	rows := 0 // in the card being built, each after the first under a line
	group := func(title string, body func()) {
		if title != "" {
			ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(pal.faint).Margin(18, 4, 8, 4)
		}
		ui.Column(c).Radius(12).Background(pal.hover).Children(func() {
			rows = 0
			body()
		})
	}
	row := func(label, note string, control func()) {
		if rows > 0 {
			ui.Box(c).Height(1).Background(pal.hairline)
		}
		rows++
		ui.Row(c).Gap(16).AlignItems(ui.Center).MinHeight(52).Padding(10, 16, 10, 16).Children(func() {
			ui.Column(c).Gap(3).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Text(c, label).FontSize(14).TextColor(pal.text)
				if note != "" {
					ui.Text(c, note).FontSize(12).TextColor(pal.faint)
				}
			})
			if control != nil {
				control()
			}
		})
	}
	field := func(key string, box *ui.Handle, text *string, placeholder, label string) {
		ui.Row(c.Key(key+"-wrap")).Width(230).Height(32).PaddingX(10).Radius(8).AlignItems(ui.Center).
			Background(pal.searchTint).Border(1, onOff(c.Focused(*box), pal.focusBorder, pal.searchBorder)).Children(func() {
			in := ui.TextInputBase(c.Key(key), text).Bind(box).Grow(1).Placeholder(placeholder).FontSize(12.5).Label(label)
			if in.Submitted() {
				a.reconfigureHG()
				a.toast = "已应用红果签名配置"
			}
		})
	}
	switch a.settingsPage {
	case settingsPlay:
		q := a.quality
		if q == "" {
			q = "智能"
		}
		group("", func() {
			row("连播", "播完自动下一个", func() { ui.Switch(c.Key("autoNext"), &a.autoNext).Label("连播开关") })
			row("全局倍速", "切换及重启后保留，直播除外", func() {
				labels := make([]string, len(speeds))
				for i, speed := range speeds {
					labels[i] = fmt.Sprintf("%gx", speed)
				}
				label := fmt.Sprintf("%gx", a.playSpeed())
				if ui.Select(c.Key("playbackSpeed"), &label, labels).Width(120).Label("全局倍速选择").Changed() {
					for i, choice := range labels {
						if label == choice {
							a.setSpeed(speeds[i])
							break
						}
					}
				}
			})
			row("清晰度", "智能：按网速自动选择", func() {
				if ui.Select(c.Key("quality"), &q, qualityChoices).Width(120).Label("清晰度").Changed() {
					a.setQuality(q)
				}
			})
		})
	case settingsDanmaku:
		on := !a.dmOff
		group("", func() {
			row("显示弹幕", "", func() {
				if ui.Switch(c.Key("danmaku"), &on).Label("显示弹幕开关").Changed() {
					a.dmOff = !on
				}
			})
		})
		dm := a.danmakuStyle()
		styleRow := func(key, title, label string, labels []string, set func(v string)) {
			row(title, "", func() {
				if ui.Select(c.Key(key), &label, labels).Width(120).Label("弹幕" + title).Changed() {
					set(label)
					a.setDanmakuStyle(dm)
				}
			})
		}
		group("样式", func() {
			styleRow("dmSpeed", "速度", dmLabelFor(dmSpeedChoices, dm.Speed, "1x"), dmChoiceLabels(dmSpeedChoices), func(v string) {
				dm.Speed = dmValueFor(dmSpeedChoices, v, defaultDanmakuStyle.Speed)
			})
			styleRow("dmSize", "字号", dmLabelFor(dmSizeChoices, dm.Size, "标准"), dmChoiceLabels(dmSizeChoices), func(v string) {
				dm.Size = dmValueFor(dmSizeChoices, v, defaultDanmakuStyle.Size)
			})
			styleRow("dmStroke", "描边", dmLabelFor(dmStrokeChoices, dm.Stroke, "阴影"), dmChoiceLabels(dmStrokeChoices), func(v string) {
				dm.Stroke = dmValueFor(dmStrokeChoices, v, defaultDanmakuStyle.Stroke)
			})
			styleRow("dmOpacity", "透明度", dmLabelFor(dmOpacityChoices, dm.Opacity, "90%"), dmChoiceLabels(dmOpacityChoices), func(v string) {
				dm.Opacity = dmValueFor(dmOpacityChoices, v, defaultDanmakuStyle.Opacity)
			})
		})
	case settingsHongguo:
		group("", func() {
			row("绕过系统代理", "红果接口和视频直连", func() {
				if ui.Switch(c.Key("hgDirect"), &a.hgDirect).Label("红果直连开关").Changed() {
					a.reconfigureHG()
				}
			})
			row("匿名设备", "出问题时可以重置", func() {
				b := ui.Button(c.Key("hg-reset-device"), onOff(a.hgResetBusy, "重置中…", "重置"))
				if a.hgResetBusy {
					b.Disabled(true)
				} else if b.Clicked() {
					a.resetHGDevice()
				}
			})
		})
		group("签名服务", func() {
			row("地址", "留空使用内置签名", func() {
				field("hgSignerURL", &a.hgSignerURLBox, &a.hgSignerURL, "https://...", "红果签名服务地址")
			})
			row("Token", "可选", func() {
				field("hgSignerToken", &a.hgSignerTokenBox, &a.hgSignerToken, "Token", "红果签名服务 Token")
			})
		})
	case settingsAccount:
		who := "没登录"
		if a.signedIn {
			who = a.me.Name
		}
		group("", func() {
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
		})
	case settingsUpdate:
		version := "开发版"
		if v := mygo.App.Version(); v != "" {
			version = "版本 " + v
		}
		u := &a.upd
		if u.check == nil {
			group("", func() { row(version, "这个版本不是正式打包的，不能自动更新", nil) })
			return
		}
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
		group("", func() {
			row(version, status, func() {
				if u.found != nil && !u.busy {
					if ui.Button(c.Key("install"), "查看更新").Clicked() {
						a.settingsOpen = false
						u.dialogOpen = true
					}
					return
				}
				b := ui.Button(c.Key("check"), "检查更新")
				if u.checking || u.busy {
					b.Disabled(true)
				} else if b.Clicked() {
					a.checkUpdate(true)
				}
			})
			row("自动检查更新", "启动时及每 6 小时", func() { ui.Switch(c.Key("autoUpdate"), &u.auto).Label("自动检查更新") })
		})
	}
}
