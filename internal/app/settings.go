package app

import (
	"encoding/json"
	"errors"
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

// settingsDialog is the settings, over the window while open: playback,
// Hongguo configuration, and updates.
func (a *app) settingsDialog(c *ui.Context) {
	ui.DialogBase(c.Key("settings"), &a.settingsOpen, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.Width(440).Padding(18, 20, 20, 20).Gap(6).Radius(14).Background(pal.popover).
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
		ui.Scroll(c.Key("settings-scroll")).MaxHeight(580).Gap(6).Padding(0, barGutter, 0, 0).Children(func() {
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
			dm := a.danmakuStyle()
			dmSpeedLabel := dmLabelFor(dmSpeedChoices, dm.Speed, "1x")
			dmSizeLabel := dmLabelFor(dmSizeChoices, dm.Size, "标准")
			dmStrokeLabel := dmLabelFor(dmStrokeChoices, dm.Stroke, "阴影")
			dmOpacityLabel := dmLabelFor(dmOpacityChoices, dm.Opacity, "90%")
			ui.Column(c.Key("dm-style-settings")).Gap(6).Margin(2, 0, 4, 0).Children(func() {
				dmCell := func(key, title string, value *string, opts []string, onChange func(string)) {
					ui.Row(c.Key(key+"-cell")).Grow(1).Basis(0).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, title).FontSize(12.5).TextColor(pal.muted).Grow(1).SingleLine()
						if ui.Select(c.Key(key), value, opts).Width(96).Label("弹幕" + title).Changed() {
							onChange(*value)
						}
					})
				}
				ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
					dmCell("dmSpeed", "速度", &dmSpeedLabel, dmChoiceLabels(dmSpeedChoices), func(v string) {
						dm.Speed = dmValueFor(dmSpeedChoices, v, defaultDanmakuStyle.Speed)
						a.setDanmakuStyle(dm)
					})
					dmCell("dmSize", "字号", &dmSizeLabel, dmChoiceLabels(dmSizeChoices), func(v string) {
						dm.Size = dmValueFor(dmSizeChoices, v, defaultDanmakuStyle.Size)
						a.setDanmakuStyle(dm)
					})
				})
				ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
					dmCell("dmStroke", "描边", &dmStrokeLabel, dmChoiceLabels(dmStrokeChoices), func(v string) {
						dm.Stroke = dmValueFor(dmStrokeChoices, v, defaultDanmakuStyle.Stroke)
						a.setDanmakuStyle(dm)
					})
					dmCell("dmOpacity", "透明度", &dmOpacityLabel, dmChoiceLabels(dmOpacityChoices), func(v string) {
						dm.Opacity = dmValueFor(dmOpacityChoices, v, defaultDanmakuStyle.Opacity)
						a.setDanmakuStyle(dm)
					})
				})
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
			section("红果短剧 (MyHongguo)")
			row("绕过系统代理直连", "直连红果接口与媒体，不走系统代理", func() {
				if ui.Switch(c.Key("hgDirect"), &a.hgDirect).Label("红果直连开关").Changed() {
					a.reconfigureHG()
				}
			})
			urlFocused := c.Focused(a.hgSignerURLBox)
			if a.hgSignerURLFocused && !urlFocused && a.hg != nil && a.hgConfig() != a.hg.cfg {
				a.reconfigureHG()
			}
			a.hgSignerURLFocused = urlFocused
			row("签名服务地址", "留空用内置签名；填写后内置取址失败自动回退到此服务", func() {
				box := ui.Row(c.Key("hg-signer-url-wrap")).Width(188).Height(32).PaddingX(10).Radius(8).
					AlignItems(ui.Center).Background(pal.searchTint).
					Border(1, onOff(urlFocused, pal.focusBorder, pal.searchBorder))
				box.Children(func() {
					in := ui.TextInputBase(c.Key("hgSignerURL"), &a.hgSignerURL).Bind(&a.hgSignerURLBox).Grow(1).
						Placeholder("https://...").FontSize(12.5).Label("红果签名服务地址")
					if in.Submitted() {
						a.reconfigureHG()
						a.toast = "已应用红果签名配置"
					}
				})
			})
			tokFocused := c.Focused(a.hgSignerTokenBox)
			if a.hgSignerTokenFocused && !tokFocused && a.hg != nil && a.hgConfig() != a.hg.cfg {
				a.reconfigureHG()
			}
			a.hgSignerTokenFocused = tokFocused
			row("签名服务 Token", "远程签名服务的鉴权令牌（可选）", func() {
				box := ui.Row(c.Key("hg-signer-token-wrap")).Width(188).Height(32).PaddingX(10).Radius(8).
					AlignItems(ui.Center).Background(pal.searchTint).
					Border(1, onOff(tokFocused, pal.focusBorder, pal.searchBorder))
				box.Children(func() {
					in := ui.TextInputBase(c.Key("hgSignerToken"), &a.hgSignerToken).Bind(&a.hgSignerTokenBox).Grow(1).
						Placeholder("可选 Token").FontSize(12.5).Label("红果签名服务 Token")
					if in.Submitted() {
						a.reconfigureHG()
						a.toast = "已应用红果签名配置"
					}
				})
			})
			row("应用签名配置", "修改签名地址或 Token 后立即生效", func() {
				if ui.Button(c.Key("hg-apply-config"), "应用配置").Clicked() {
					a.reconfigureHG()
					if a.toast == "" {
						a.toast = "已应用红果配置"
					}
				}
			})
			row("匿名设备", "红果模式免登录运行，异常时可重置匿名设备标识", func() {
				b := ui.Button(c.Key("hg-reset-device"), onOff(a.hgResetBusy, "重置中…", "重置匿名设备"))
				if a.hgResetBusy {
					b.Disabled(true)
				} else if b.Clicked() {
					a.resetHGDevice()
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
			} else {
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
			}
		})
	})
	if a.hgSettingsWasOpen && !a.settingsOpen {
		a.hgSignerURLFocused = false
		a.hgSignerTokenFocused = false
		if a.hg != nil && a.hgConfig() != a.hg.cfg {
			a.reconfigureHG()
		}
	}
	a.hgSettingsWasOpen = a.settingsOpen
}
