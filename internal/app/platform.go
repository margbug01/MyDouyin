package app

import (
	"errors"
	"strings"
	"time"

	"douyin/internal/dy"
	"douyin/internal/media"
	"douyin/internal/platform"
	"douyin/internal/tiktok"
)

type platformSnapshot struct {
	pages    []*page
	stacks   map[*page][]*page
	me       dy.User
	signedIn bool
	mine     map[string]*page
	mineTab  string
}

func (a *app) activePlatform() platform.ID {
	if a.platform == "" {
		return platform.Douyin
	}
	return a.platform
}

func (a *app) isTikTok() bool {
	return a.activePlatform() == platform.TikTok
}

func (a *app) caps() platform.Capabilities {
	return a.activePlatform().Capabilities()
}

func (a *app) ensureTikTokClient() *tiktok.Client {
	if a.tt != nil {
		return a.tt
	}
	c, err := tiktok.New()
	if err != nil {
		a.toast = "TikTok 初始化失败：" + err.Error()
		return nil
	}
	a.ttWeb.a = a
	c.SetWeb(&a.ttWeb)
	if a.proxyURL != "" {
		_ = c.SetProxy(a.proxyURL)
	}
	if a.ttRegion != "" {
		c.SetRegion(a.ttRegion, regionDefaultLang(a.ttRegion))
	}
	a.tt = c
	a.ttFeed = &page{title: "For You", next: c.Feed}
	a.ttFollow = &page{title: "关注", next: c.FollowFeed(), grid: true, restart: c.FollowFeed}
	a.ttLive = &page{title: "直播", next: c.LiveRooms(), grid: true, restart: c.LiveRooms, covers: liveCovers}
	return c
}

func regionDefaultLang(region string) string {
	switch strings.ToUpper(strings.TrimSpace(region)) {
	case "JP":
		return "ja-JP"
	case "KR":
		return "ko-KR"
	case "TW", "HK":
		return "zh-Hant"
	case "SG":
		return "zh-Hans"
	default:
		return "en"
	}
}

func (a *app) applyProxySetting(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if err := media.SetProxy(trimmed); err != nil {
		return err
	}
	if a.tt != nil {
		if err := a.tt.SetProxy(trimmed); err != nil {
			return err
		}
	}
	a.proxyURL = trimmed
	a.proxyDraft = trimmed
	return nil
}

func (a *app) applyTikTokRegion(region string) {
	region = strings.ToUpper(strings.TrimSpace(region))
	if region == "" {
		region = "US"
	}
	changed := a.ttRegion != "" && a.ttRegion != region
	a.ttRegion = region
	if a.tt != nil {
		a.tt.SetRegion(region, regionDefaultLang(region))
	}
	if changed && a.ttWeb.v != nil && !a.ttWeb.layout {
		a.ttWeb.destroy()
	}
}

// activeVideoHeaders returns the CDN request headers for the current platform.
func (a *app) activeVideoHeaders(v dy.Video) map[string]string {
	if a.isTikTok() && a.tt != nil {
		return a.tt.MediaHeaders(v)
	}
	return videoHeaders
}

// fetchImage routes cover/avatar downloads to the matching platform client so
// TikTok CDN images use the configured proxy, Referer, and cookies.
func (a *app) fetchImage(u string) ([]byte, error) {
	if strings.Contains(u, "tiktok") || (a.isTikTok() && a.tt != nil) {
		if c := a.tt; c != nil {
			return c.Image(u)
		}
	}
	if a.c != nil {
		return a.c.Image(u)
	}
	return nil, errors.New("image client unavailable")
}

// openActiveLogin launches the login flow for the currently active platform.
func (a *app) openActiveLogin() {
	if a.isTikTok() {
		a.openTikTokLogin()
		return
	}
	a.openLogin()
}

// buildTikTokMine constructs the "我的" tabs supported on TikTok (作品 / 喜欢 / 收藏).
func (a *app) buildTikTokMine(me dy.User) map[string]*page {
	if a.tt == nil || me.ID == "" {
		return nil
	}
	list := func(title string, from func() dy.Pager[dy.Video]) *page {
		return &page{title: title, next: from(), grid: true, restart: from, covers: portraitCovers}
	}
	posts := list("作品", func() dy.Pager[dy.Video] { return a.tt.Posts(me.ID) })
	posts.authorID = me.ID
	return map[string]*page{
		"作品": posts,
		"喜欢": list("喜欢", func() dy.Pager[dy.Video] { return a.tt.Liked(me.ID) }),
		"收藏": list("收藏", a.tt.Collected),
	}
}

// startTikTokLogin automatically restores a saved TikTok session from either
// TikTok/cookies.json or the shared WebView cookie store, then loads the account.
func (a *app) startTikTokLogin() {
	tt := a.tt
	if tt == nil {
		return
	}
	if tt.HasSession() {
		if a.isTikTok() {
			a.signedIn = true
		} else if a.ttSnap != nil {
			a.ttSnap.signedIn = true
		}
		a.loadTikTokAccount()
		return
	}
	// Check whether WebView2/WebKit already has a persisted TikTok session cookie.
	go func() {
		cs, err := a.ttWeb.Cookies()
		if err != nil || !tiktok.LoggedIn(cs) {
			return
		}
		_ = tt.SaveCookies(cs)
		a.post(func() {
			if a.isTikTok() {
				a.signedIn = true
			} else if a.ttSnap != nil {
				a.ttSnap.signedIn = true
			}
			a.loadTikTokAccount()
		})
	}()
}

// loadTikTokAccount fetches the signed-in TikTok account profile and populates
// the TikTok "我的" tabs (作品, 喜欢, 收藏).
func (a *app) loadTikTokAccount() {
	tt := a.tt
	if tt == nil {
		return
	}
	go func() {
		me, err := tt.Self()
		if err != nil && !errors.Is(err, tiktok.ErrNeedLogin) {
			// Retry once after a short pause on transient startup errors.
			time.Sleep(1200 * time.Millisecond)
			me, err = tt.Self()
		}
		if err != nil {
			a.post(func() {
				if errors.Is(err, tiktok.ErrNeedLogin) {
					if a.isTikTok() {
						a.signedIn = false
						a.me = dy.User{}
						a.mine = nil
					} else if a.ttSnap != nil {
						a.ttSnap.signedIn = false
						a.ttSnap.me = dy.User{}
						a.ttSnap.mine = nil
					}
				}
			})
			return
		}
		if me.ID != "" {
			if full, perr := tt.Profile(me.ID); perr == nil {
				if full.Name != "" {
					me.Name = full.Name
				}
				if full.Avatar != "" {
					me.Avatar = full.Avatar
				}
				if full.Handle != "" {
					me.Handle = full.Handle
				}
				me.Signature = full.Signature
				me.Following = full.Following
				me.Followers = full.Followers
				me.Liked = full.Liked
			}
		}
		a.post(func() {
			mine := a.buildTikTokMine(me)
			if a.isTikTok() {
				a.signedIn = true
				a.me = me
				if mine != nil {
					a.mine = mine
					if a.mineTab == "" || a.mine[a.mineTab] == nil {
						a.mineTab = "作品"
					}
				}
			} else if a.ttSnap != nil {
				a.ttSnap.signedIn = true
				a.ttSnap.me = me
				if mine != nil {
					a.ttSnap.mine = mine
					a.ttSnap.mineTab = "作品"
				}
			}
		})
	}()
}

// switchPlatform toggles the live application state between Douyin and TikTok,
// isolating page stacks, playback decoders, and account metadata per platform.
func (a *app) switchPlatform(target platform.ID) {
	target = platform.Parse(string(target))
	if target == a.activePlatform() {
		return
	}
	// Release active playback and preload before switching platform state.
	a.sugSeq++
	a.preload.close()
	a.shareOpen, a.shareQuery = false, ""
	if a.held != nil {
		a.release(a.held)
		a.held = nil
	}
	for _, p := range a.pages {
		for _, s := range p.list {
			a.release(s)
		}
	}
	a.sheet, a.notes, a.dms = nil, nil, nil
	a.paused = false
	a.ttWeb.layout = false

	// Save outgoing platform snapshot.
	snap := &platformSnapshot{
		pages:    a.pages,
		stacks:   a.stacks,
		me:       a.me,
		signedIn: a.signedIn,
		mine:     a.mine,
		mineTab:  a.mineTab,
	}
	if a.isTikTok() {
		a.ttSnap = snap
	} else {
		a.dySnap = snap
	}

	a.platform = target
	if target == platform.TikTok {
		tt := a.ensureTikTokClient()
		if tt == nil {
			a.platform = platform.Douyin
			return
		}
		a.setLiked = tt.SetLiked
		a.setSaved = tt.SetSaved
		a.setFollowed = tt.SetFollowed
		a.likeComment = tt.SetCommentLiked
		a.publishComment = tt.PublishComment
		a.uploadCommentImage = nil
		a.replies = tt.Replies
		a.fetchLiveRoom = tt.LiveRoom
		a.sendDanmaku = nil

		if a.ttSnap != nil && len(a.ttSnap.pages) > 0 {
			a.pages = a.ttSnap.pages
			a.stacks = a.ttSnap.stacks
			a.me = a.ttSnap.me
			a.signedIn = a.ttSnap.signedIn
			a.mine = a.ttSnap.mine
			a.mineTab = a.ttSnap.mineTab
		} else {
			a.pages = []*page{a.ttFeed}
			a.stacks = map[*page][]*page{}
			a.me = dy.User{}
			a.signedIn = false
			a.mine = nil
			a.mineTab = ""
		}
		if a.win != nil {
			a.win.SetTitle("MyDouyin · TikTok")
			if !a.signedIn || a.me.ID == "" {
				a.startTikTokLogin()
			}
		}
		a.toast = "已切换到 TikTok"
	} else {
		if a.c != nil {
			a.setLiked = a.c.SetLiked
			a.setSaved = a.c.SetSaved
			a.setFollowed = a.c.SetFollowed
			a.likeComment = a.c.SetCommentLiked
			a.publishComment = a.c.PublishComment
			a.uploadCommentImage = a.c.UploadCommentImage
			a.replies = a.c.Replies
			a.fetchLiveRoom = nil
			a.sendDanmaku = func(id string, at time.Duration, text string) error {
				if a.me.UID == "" {
					return errors.New("账号还没载入")
				}
				return a.c.SendDanmaku(id, a.me.UID, at, text)
			}
		}
		if a.dySnap != nil && len(a.dySnap.pages) > 0 {
			a.pages = a.dySnap.pages
			a.stacks = a.dySnap.stacks
			a.me = a.dySnap.me
			a.signedIn = a.dySnap.signedIn
			a.mine = a.dySnap.mine
			a.mineTab = a.dySnap.mineTab
		} else if a.home != nil {
			a.pages = []*page{a.home}
			a.stacks = map[*page][]*page{}
		}
		if a.win != nil {
			a.win.SetTitle("MyDouyin")
			a.startDouyinServices()
		}
		a.toast = "已切回抖音"
	}
	a.fill()
}
