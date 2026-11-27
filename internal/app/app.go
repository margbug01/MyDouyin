// Package app is the window: the recommend feed, one video at a time, scrolled
// with the wheel or arrow keys, and searches opened over it.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
	"douyin/internal/hg"
	"douyin/internal/media"
)

// slot holds a feed entry, its playback state, and its player while visible.
type slot struct {
	v dy.Video
	players
	loading bool
	cached  bool // seeded from disk fast-start cache: refresh PlayURL before playing
	cancel  context.CancelFunc
	loadGen int
	gone        bool // dropped while loading: close what arrives
	err         error
	liveFreshAt time.Time // when LiveRoom last refreshed this live slot's streams
	liveFrom    int       // the first of liveCandidates to open: those before showed no picture
	saved       string    // what saving to Downloads came to: "" before the click

	liking, saving bool // a like or a save of it on its way to Douyin

	resume     time.Duration // where to go on from once opened again
	seekTarget time.Duration
	seeking    bool
	dmGen      int // counts seeks: bullet comments loaded before one are let go

	danmaku   []dy.Danmaku  // the bullet comments loaded, in time order
	dm        dmField       // where they fly
	dmTo      time.Duration // how far into the video they are loaded
	dmLoading bool
	lastAt    time.Duration // where the view last saw it, which a loop goes back from
	reported  bool          // queued to tell Douyin it was watched (watched.go)
	comments  *sheet        // its comments, fetched ahead once it played a moment
}

// players are what plays a video once it is opened: vp shows it, ap plays
// its sound if it has any, dec decodes a file, live a stream.
type players struct {
	file *media.File
	vp   videoPlayer
	ap   *audio.Player // nil when the clip has no audio, and for a stream, whose sound live plays
	dec  *paced        // vp's decoder for a file, whose clock stands in for a missing audio's
	live *media.Live   // AVPlayer for a streamed video or live room
	path string        // an optional local source, used by save
	// stream is which of liveCandidates a live room plays, more whether
	// others are left after it.
	stream int
	more   bool
}

// close stops and lets go of whatever of pl there is.
func (pl players) close() {
	// Next may be waiting on a paused stream; stop it before VideoPlayer waits for it.
	if pl.live != nil {
		pl.live.Close()
	}
	if pl.vp != nil {
		pl.vp.Close() // closes its decoder, a stream with it
	}
	if pl.ap != nil {
		pl.ap.Close()
	}
	if pl.file != nil {
		pl.file.Close()
	}
}

// paced is a video's decoder whose frames last as long as the speed says:
// their own time divided by it. It counts the video's time it has decoded,
// a clock where the video has no sound to give one. It moves to another
// time as the player plays it again: asked to (seek), it says the video
// ended, which a looping player answers with Rewind, and Rewind goes there.
type paced struct {
	*media.VideoDecoder
	speed *atomic.Uint64 // the float64 bits, read on the player's goroutine
	done  atomic.Int64   // the video's time decoded, at its own speed
	to    atomic.Int64   // where to go, plus one; 0 for nowhere
}

// seek has the player go on from t, a few frames on.
func (p *paced) seek(t time.Duration) { p.to.Store(int64(t) + 1) }

func (p *paced) Next() (ui.VideoFrame, error) {
	if p.to.Load() != 0 {
		return ui.VideoFrame{}, io.EOF
	}
	f, err := p.VideoDecoder.Next()
	p.done.Add(int64(f.Duration))
	if s := math.Float64frombits(p.speed.Load()); s > 0 {
		f.Duration = time.Duration(float64(f.Duration) / s)
	}
	return f, err
}

func (p *paced) Rewind() error {
	if to := p.to.Swap(0); to != 0 {
		p.done.Store(to - 1)
		return p.VideoDecoder.RewindTo(time.Duration(to - 1))
	}
	p.done.Store(0)
	return p.VideoDecoder.Rewind()
}

// at is how far s has played: its sound's position, else what its video
// has decoded, a few frames ahead of the one shown.
func (s *slot) at() time.Duration {
	switch {
	case s.live != nil:
		if p, ok := s.vp.(*streamPlayer); ok {
			p.update()
		}
		return s.live.Position()
	case s.ap != nil:
		return s.ap.Position()
	case s.dec != nil:
		return time.Duration(s.dec.done.Load())
	}
	return s.resume
}

// speeds are the choices of the speed menu, fastest first, as Douyin's.
var speeds = []float64{2, 1.5, 1.25, 1, 0.75}

const ahead = 2 // fetch another page before reaching the end of the list

// page is a list of videos: the picks (精选) or the recommend feed at the
// bottom, and search results or a user's videos opened over it. A grid page
// shows covers until one is clicked, then plays as the feed does, until
// Escape brings the covers back.
type page struct {
	title    string
	next     dy.Pager[dy.Video]
	list     []*slot
	grid      bool
	playing   bool // a grid page playing its idx
	gridList  ui.ListState
	idx       int
	fetching bool
	gen      int  // counts refreshes: a page fetched before one is dropped
	done     bool // next returned its last page
	msg      string
	paused   bool   // playback state while another page is open
	authorID string // an author's works, where is_top is meaningful
	covers   coverShape // the shape of the covers Douyin serves this list, which its cards take

	restart func() dy.Pager[dy.Video] // a root's list from the top, as refresh takes it; nil goes on with next

	// A video played from 引力场 plays over its source's list, which fetches
	// for both (playFromOrbit); fromGen is the source's gen then: refreshed
	// since, its list is another.
	from    *page
	fromGen int

	user  *dy.User  // an author's page: their profile atop the videos, once loaded
	users []dy.User // a search's: the accounts found, atop the videos
}

// stageHUD tracks auto-hide state for the video stage overlays (controls,
// caption, rail) and cursor.
type stageHUD struct {
	slot           *slot
	activeAt       time.Time
	pointerX       float32
	pointerY       float32
	wasOver        bool
	overlayHovered bool
}

// want updates stage pointer activity for s and reports whether the HUD should
// be visible and how long until it should auto-hide.
func (h *stageHUD) want(s *slot, now time.Time, px, py float32, over, pinned bool) (show bool, hideIn time.Duration) {
	if h.slot != s {
		h.slot = s
		h.activeAt = now
	}
	switch {
	case over && (!h.wasOver || px != h.pointerX || py != h.pointerY):
		h.activeAt = now
		h.pointerX, h.pointerY = px, py
	case !over && h.wasOver:
		h.activeAt = time.Time{}
	}
	h.wasOver = over
	if pinned {
		return true, 0
	}
	if h.activeAt.IsZero() {
		return false, 0
	}
	elapsed := now.Sub(h.activeAt)
	if elapsed < controlsHideAfter {
		return true, controlsHideAfter - elapsed
	}
	return false, 0
}

// app is read and changed on the main thread: by the view, and by goroutines through post.
type app struct {
	win                 *mygo.Window
	mainVideo, pipVideo nativeSurface
	c                   *dy.Client
	dir                 string
	cacheDir            string // persistent cache root (empty in tests unless explicitly configured)
	pages               []*page           // the window shows the last
	stacks              map[*page][]*page // each root's pages, as they were when another root was shown, by root
	query               string            // the search box
	paused              bool
	held                *slot // at most one paused video behind a grid or child page
	preload             nextVideo
	lastNav             time.Time
	slide               *slide    // the step to the video shown, sliding in over the last, or nil
	slides              int       // steps so far, each slide's own
	swipeAt             time.Time // the last trackpad scroll of the swipe on the stage
	sheet               *sheet    // the comments open beside the video, or nil
	group               groupPreview
	picViewer           picViewer
	liveChat            *liveRoomView

	searchBox ui.Handle
	toast     string    // a message to show once
	me        dy.User   // the account logged in, once known
	follows   []dy.User // the accounts it follows, atop 关注
	followMsg string    // why they did not load

	picks, feed *page             // the roots: 精选's covers, 推荐's videos one by one
	home        *page             // 引力场: centered search with orbiting covers
	orbitPicks  *page             // 引力场's own deduplicated, infinitely refreshable 精选 stream
	picksIDs    sync.Map          // video IDs already shown in sidebar 精选 (race-free lookup from orbit pager)
	orbit       orbitState        // 引力场 state (orbit.go)
	winBg       string            // the window's background color, as last set (view)
	img        *images                   // covers and avatars
	emoji        map[string]string        // Douyin's emoji by name, as [微笑], once a text asked for them (emojiSpans)
	emojiList    []dy.Emoji               // Douyin's emojis in official order for the picker
	emojiLoading bool                     // emoji list request in flight
	emojiCache   map[emojiKey]emojiCached // parsed emoji spans, bounded and cleared when emoji loads

	// The dock's other roots, each a grid; nil where there is no client.
	follow, friends, live, drama *page
	mine                         map[string]*page // 我的's tabs, by name, once the account is known
	mineTab                      string

	tab      int           // 精选's tab shown, a dy.Tabs ID
	tabPages map[int]*page // 精选's tabs opened, kept as they were scrolled

	notes  *notes // the notifications open beside the page, or nil
	unread int
	hot    []dy.Hot // the hot search list, under the search box
	sugs   []string // what Douyin suggests for sugFor, under the search box
	sugFor string
	sugSeq int  // the search box's changes, the last of which a suggestion waits for
	dms    *dms // the direct messages open beside the page, or nil

	speed          float64       // 1 as recorded
	speedBits      atomic.Uint64 // speed for the decoders
	autoNext       bool          // 连播: the next video once one ends, rather than again
	dmOff          bool          // bullet comments hidden
	dmStyle        danmakuStyle  // bullet-comment speed, font size, stroke, opacity
	dmSettingsOpen bool          // the bar's danmaku style popover

	quality     string  // the definition chosen, "" or "智能" for Douyin's pick
	liveQuality string  // the live room definition chosen, "" or "智能" for live default
	volume      float64 // 0 to 1
	muted       bool
	clear       bool // 清屏: the video alone, without its text, rail or bullet comments
	theater     bool // 网页全屏: the video fills the window, the dock and bars away
	qualityOpen bool // the bar's menus
	speedOpen   bool
	volumeOpen  bool
	pip         *mygo.Window // 小窗, which shows the current video over other windows
	pipOn       bool
	dmText      string // the bullet comment being written
	dmBox       ui.Handle
	later       map[string]bool // the videos in 稍后再看, by ID, as far as known
	laterBusy   map[string]bool // those with a change on its way
	// sendDanmaku and setLater write to Douyin; a test puts its own.
	sendDanmaku func(id string, at time.Duration, text string) error
	setLater    func(id string, on bool) error
	dragging    bool    // the progress pressed, being dragged
	dragAt      float32 // where along it, 0 to 1

	stageHUD stageHUD // Stage HUD (controls, caption, rail, cursor) auto-hide state (ui.go).

	posted chan func() // what post runs, in a test without a window

	// The rail's share panel: open, its search, and how sending a video to an
	// account went, by video and account ID: "sending", "done".
	shareOpen  bool
	shareQuery string
	shareBox   ui.Handle
	shared     map[string]string
	// sendShare sends a video to an account by name; a test puts its own.
	sendShare func(videoID, name string) error

	// setLiked, setSaved, and setFollowed write to Douyin; a test puts its own.
	setLiked, setSaved func(id string, on bool) error
	setFollowed        func(uid, secUID string, on bool) (int, error)
	followState        map[string]int  // latest follow_status by sec_uid (0, 1, 2)
	followBusy         map[string]bool // follow/unfollow request in flight by sec_uid
	// likeComment likes a comment or a reply of a video, or takes it back; a test puts its own.
	likeComment func(videoID, cid string, reply, on bool) error
	// publishComment posts a comment or reply; a test puts its own.
	publishComment func(req dy.PublishCommentReq) (dy.Comment, error)
	// uploadCommentImage uploads a comment image to ImageX; a test puts its own.
	uploadCommentImage func(data []byte, fileName, userID string, measured dy.CommentImage) (dy.CommentImage, error)
	// replies pages through a comment's replies; a test puts its own.
	replies       func(videoID, commentID string) dy.Pager[dy.Comment]
	openAuthorFn  func(p *page, name, id string) // test override for openAuthor
	fetchLiveRoom func(ctx context.Context, webRID string) (dy.Video, error)
	openLive      func(ctx context.Context, url string, native bool) (players, error)
	// keepAwake keeps the computer, and with display the display, from
	// sleeping until release; a test puts its own.
	keepAwake func(reason string, display bool) (release func())
	// awake releases what keeps the computer awake while a video plays,
	// which also kept the display on if awakeDisplay (syncAwake).
	awake        func()
	awakeDisplay bool

	listen   bool    // 听抖音: the cover in the picture's place, the sound on
	moreOpen bool    // the rail's 更多 menu
	upd      updates // checking for new versions and installing them (update.go)

	// sendWatched tells Douyin a video was watched; a test puts its own (watched.go).
	sendWatched  func(id, authorUID string) error
	watched      []dy.Video // watched, not yet told
	watchSending bool
	watchTimer   *time.Timer
	historyWait  int // 观看历史's gen while it waits for them to be told to load (freshHistory), 0 none

	settingsOpen bool     // the settings dialog (settings.go)
	settingsPage settingsPage // the page of it shown
	settingsFile string   // where the settings are kept, "" not to keep them
	saved        settings // as last written

	mode                 appMode
	brandMenuOpen        bool
	hg                   *hgApp
	hgDirect             bool
	hgSignerURL          string
	hgSignerToken        string
	hgSignerURLBox       ui.Handle
	hgSignerTokenBox     ui.Handle
	hgSignerURLFocused   bool
	hgSignerTokenFocused bool
	settingsWasOpen      bool // as of the last frame, so the dialog knows it opened or closed
	hgResetBusy          bool
	douyinStarted        bool
	newHGService         func(hg.Config) (hg.Service, error) // test hook for lazy Hongguo service creation
	openHGStream         func(ctx context.Context, rawURL string, opts media.StreamOptions, native bool) (players, error)

	web       web  // the app's Douyin page, which logs in and writes (web.go)
	signedIn  bool // WebKit holds a login
	loginOpen bool // Douyin's login shows over the window
	loginGen  int  // counts the logins shown, each waited for once
	login     login
}

// notes is the notifications, loaded a page at a time.
type notes struct {
	items    []dy.Notice
	next     dy.Pager[dy.Notice]
	fetching bool
	done     bool
	msg      string
}

// dms is the inbox, and the chat open in it, -1 for the list.
type dms struct {
	chats   []dy.Chat
	open    int
	loading bool
	msg     string
}

// openDMs shows the direct messages, in the place of the comments and the
// notifications; reading them marks nothing read.
func (a *app) openDMs() {
	a.sheet, a.notes = nil, nil
	d := &dms{open: -1, loading: true}
	a.dms = d
	go func() {
		chats, err := a.c.Chats()
		a.post(func() {
			d.loading, d.chats = false, chats
			if err != nil {
				d.msg = err.Error()
			}
		})
	}()
}

// openNotes shows the notifications, in the place of the comments.
func (a *app) openNotes() {
	a.sheet, a.dms, a.unread = nil, nil, 0
	a.notes = &notes{next: a.c.Notices()}
	a.moreNotes()
}

func (a *app) moreNotes() {
	n := a.notes
	if n == nil || n.fetching || n.done {
		return
	}
	n.fetching = true
	go func() {
		ns, err := n.next()
		a.post(func() {
			n.fetching = false
			if err != nil {
				n.msg = err.Error()
				return
			}
			n.msg, n.done = "", len(ns) == 0
			n.items = append(n.items, ns...)
		})
	}()
}

// mineTabs are 我的's tabs, in order.
var mineTabs = []string{"作品", "喜欢", "收藏", "观看历史", "稍后再看"}

// isMine tells whether p is one of 我的's tabs.
func (a *app) isMine(p *page) bool {
	for _, q := range a.mine {
		if p == q {
			return true
		}
	}
	return false
}

// showMine shows 我的, at its tab name, as it was left; 观看历史 shown from
// elsewhere loads anew.
func (a *app) showMine(name string) {
	if p := a.mine[name]; p != nil {
		a.mineTab = name
		if name == "观看历史" && a.pages[0] != p {
			a.freshHistory()
		}
		a.root(p)
	}
}

// showTab shows 精选's tab id, as it was left if it was open before.
func (a *app) showTab(id int) {
	p := a.tabPages[id]
	if p == nil {
		p = picksPage(a.c, "精选", id)
		a.tabPages[id] = p
	}
	a.tab, a.picks = id, p
	a.root(p)
}

// like likes s's video, or takes the like back: at once on screen, then on
// Douyin, the screen put back if Douyin did not take it.
func (a *app) like(s *slot) {
	a.write(s, &s.liking, &s.v.Liked, &s.v.Likes, a.setLiked, "点赞")
}

// collect saves s's video (收藏), or takes it out, as like does; save is
// keeping its file in Downloads.
func (a *app) collect(s *slot) {
	a.write(s, &s.saving, &s.v.Saved, &s.v.Collects, a.setSaved, "收藏")
}

// followStatus returns the latest known follow status (0, 1, or 2) for secUID.
func (a *app) followStatus(secUID string, fallback int) int {
	if secUID != "" {
		if st, ok := a.followState[secUID]; ok {
			return st
		}
	}
	return fallback
}

// toggleFollow follows or unfollows a user from a video or their profile page,
// syncing the result across all views referencing the same secUID.
func (a *app) toggleFollow(secUID, uid, name string, fallback int) {
	if secUID == "" || (a.me.ID != "" && secUID == a.me.ID) || a.followBusy[secUID] {
		return
	}
	if !a.signedIn {
		a.openLogin()
		return
	}
	send := a.setFollowed
	if send == nil && a.c != nil {
		send = a.c.SetFollowed
	}
	if send == nil {
		return
	}
	prev := a.followStatus(secUID, fallback)
	want := prev == 0
	optimistic := 0
	step := -1
	if want {
		optimistic = 1
		step = 1
	}
	if a.followState == nil {
		a.followState = map[string]int{}
	}
	if a.followBusy == nil {
		a.followBusy = map[string]bool{}
	}
	a.followState[secUID] = optimistic
	a.followBusy[secUID] = true
	a.adjustProfileFollowers(secUID, step)

	go func() {
		targetUID := uid
		if targetUID == "" && a.c != nil {
			if u, err := a.c.Profile(secUID); err == nil {
				targetUID = u.UID
			}
		}
		st, err := send(targetUID, secUID, want)
		a.post(func() {
			delete(a.followBusy, secUID)
			if err != nil {
				a.followState[secUID] = prev
				a.adjustProfileFollowers(secUID, -step)
				a.toast = onOff(want, "关注", "取消关注") + "没成功：" + err.Error()
				if errors.Is(err, dy.ErrNeedLogin) {
					a.recheckLogin(onOff(want, "关注", "取消关注"))
				}
				return
			}
			a.followState[secUID] = st
			if a.me.Following+step >= 0 {
				a.me.Following += step
			}
			if want {
				if name != "" {
					a.toast = "已关注 " + name
				} else {
					a.toast = "已关注"
				}
			} else {
				a.toast = "已取消关注"
			}
		})
	}()
}

func (a *app) adjustProfileFollowers(secUID string, delta int) {
	for _, p := range a.pages {
		if p.user != nil && p.user.ID == secUID && p.user.Followers+delta >= 0 {
			p.user.Followers += delta
		}
	}
	for _, stack := range a.stacks {
		for _, p := range stack {
			if p.user != nil && p.user.ID == secUID && p.user.Followers+delta >= 0 {
				p.user.Followers += delta
			}
		}
	}
}

func (a *app) write(s *slot, busy, on *bool, n *int, send func(string, bool) error, what string) {
	if *busy || send == nil || s.v.Live != nil {
		return
	}
	want, step := !*on, 1
	if !want {
		step = -1
	}
	*on, *n, *busy = want, *n+step, true
	id := s.v.ID
	go func() {
		err := send(id, want)
		a.post(func() {
			*busy = false
			if err != nil {
				*on, *n = !want, *n-step
				a.toast = onOff(want, "", "取消") + what + "没成功：" + err.Error()
				if errors.Is(err, dy.ErrNeedLogin) {
					a.recheckLogin(onOff(want, "", "取消") + what)
				}
			}
		})
	}()
}

// find is the comment or the reply of sh with the given ID, or nil.
func (sh *sheet) find(id string) *dy.Comment {
	for i := range sh.items {
		if sh.items[i].ID == id {
			return &sh.items[i]
		}
	}
	for _, th := range sh.threads {
		for i := range th.items {
			if th.items[i].ID == id {
				return &th.items[i]
			}
		}
	}
	return nil
}

// toggleCommentLike likes the comment of sh with the given ID, or takes the like
// back, as like does a video's: at once on screen, put back if Douyin did
// not take it.
func (a *app) toggleCommentLike(sh *sheet, id string, reply bool) {
	cm := sh.find(id)
	if cm == nil || sh.liking[id] || a.likeComment == nil {
		return
	}
	want, step := !cm.Liked, 1
	if !want {
		step = -1
	}
	cm.Liked, cm.Likes = want, cm.Likes+step
	if sh.liking == nil {
		sh.liking = map[string]bool{}
	}
	sh.liking[id] = true
	video := sh.v.ID
	go func() {
		err := a.likeComment(video, id, reply, want)
		a.post(func() {
			delete(sh.liking, id)
			if err == nil {
				return
			}
			if cm := sh.find(id); cm != nil { // the list may have grown meanwhile: find it again
				cm.Liked, cm.Likes = !want, cm.Likes-step
			}
			a.toast = onOff(want, "", "取消") + "赞评论没成功：" + err.Error()
			if errors.Is(err, dy.ErrNeedLogin) {
				a.recheckLogin(onOff(want, "", "取消") + "赞评论")
			}
		})
	}()
}

func (a *app) recheckLogin(what string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		st := a.web.checkSignedIn(ctx)
		cancel()
		a.post(func() {
			a.applyLoginRecheck(what, st)
		})
	}()
}

func (a *app) applyLoginRecheck(what string, st loginCheck) {
	if st != loginCheckInvalid {
		a.toast = what + "没成功：抖音没接受，登录还在，请稍后再试"
		return
	}
	a.signedIn = false
	a.openLogin()
}

// shareTo sends v to u in a direct message, the panel's button showing how
// it goes; one that fails can be tried again, and says why.
func (a *app) shareTo(v dy.Video, u dy.User) {
	if a.sendShare == nil {
		return
	}
	if a.shared == nil {
		a.shared = map[string]string{}
	}
	key := v.ID + "|" + u.ID
	a.shared[key] = "sending"
	go func() {
		err := a.sendShare(v.ID, u.Name)
		a.post(func() {
			if err != nil {
				delete(a.shared, key)
				a.toast = "没分享给" + u.Name + "：" + err.Error()
				return
			}
			a.shared[key] = "done"
		})
	}()
}

// seek has s's video go on from t: its picture a few frames on, its sound
// at once, its bullet comments loaded again from there.
func (a *app) seek(s *slot, t time.Duration) {
	if s.dec == nil && (s.live == nil || s.v.Live != nil) {
		return
	}
	t = max(0, min(t, s.v.Duration-time.Second))
	if s.live != nil {
		s.live.Seek(t)
	} else {
		s.dec.seek(t)
	}
	if s.ap != nil {
		s.ap.Seek(t)
	}
	s.lastAt = t // not a loop, which 连播 would take for the end
	s.seekTarget, s.seeking = t, true
	s.clearDanmaku()
	s.dmTo, s.dmLoading = max(0, t-a.dmVideoFlight())/(32*time.Second)*(32*time.Second), false
	s.dmGen++
}

// setQuality reopens in q at the saved position, canceling obsolete loads.
func (a *app) setQuality(q string) {
	if q == a.quality {
		return
	}
	a.quality = q
	if a.held != nil {
		a.release(a.held)
	}
	for _, p := range a.pages {
		for _, s := range p.list {
			if s.v.Live == nil {
				a.release(s)
			}
		}
	}
	a.fill()
}

// setLiveQuality reopens live rooms in q, keeping fresh room streams intact.
func (a *app) setLiveQuality(q string) {
	if q == a.liveQuality {
		return
	}
	a.liveQuality = q
	if a.held != nil && a.held.v.Live != nil {
		a.held.liveFrom = 0 // the chosen stream comes first
		a.release(a.held)
	}
	for _, p := range a.pages {
		for _, s := range p.list {
			if s.v.Live != nil {
				s.liveFrom = 0
				a.release(s)
			}
		}
	}
	a.fill()
}

// volumeNow is the volume streams play at, which the player of a stream
// reads as it plays; the view sets it.
var volumeNow atomicFloat

// atomicFloat is a float64 that goroutines read and write.
type atomicFloat struct{ bits atomic.Uint64 }

func (f *atomicFloat) Load() float64   { return math.Float64frombits(f.bits.Load()) }
func (f *atomicFloat) Store(v float64) { f.bits.Store(math.Float64bits(v)) }

// loudness is the gain the sound plays at: the volume, or none when muted.
func (a *app) loudness() float64 {
	if a.muted {
		return 0
	}
	return a.volume
}

// setVolume sets the sound of every video, muted or at v from 0 to 1.
func (a *app) setVolume(v float64, muted bool) {
	a.volume, a.muted = max(0, min(1, v)), muted
	g := a.loudness()
	volumeNow.Store(g)
	for _, p := range a.pages {
		for _, s := range p.list {
			if s.ap != nil {
				s.ap.SetVolume(g)
			}
			if s.live != nil {
				s.live.SetVolume(g)
			}
		}
	}
	a.hgApplyVolume(g, muted)
}

// toggleLater puts v in 稍后再看, or takes it out: at once on screen, then
// on Douyin, the screen put back if Douyin did not take it.
func (a *app) toggleLater(v dy.Video) {
	if a.setLater == nil || a.laterBusy[v.ID] || v.Live != nil {
		return
	}
	if a.later == nil {
		a.later, a.laterBusy = map[string]bool{}, map[string]bool{}
	}
	on := !a.later[v.ID]
	a.later[v.ID], a.laterBusy[v.ID] = on, true
	go func() {
		err := a.setLater(v.ID, on)
		a.post(func() {
			delete(a.laterBusy, v.ID)
			if err != nil {
				a.later[v.ID] = !on
				a.toast = onOff(on, "加入", "移出") + "稍后再看没成功：" + err.Error()
				return
			}
			a.toast = onOff(on, "已加入稍后再看", "已移出稍后再看")
		})
	}()
}

// sendBullet posts what is written as a bullet comment on s's video, where
// it plays: it flies at once, and goes back off it if Douyin refused it.
func (a *app) sendBullet(s *slot) {
	text := strings.TrimSpace(a.dmText)
	if text == "" || a.sendDanmaku == nil || s.v.Live != nil {
		return
	}
	if n := len([]rune(text)); n > 50 {
		a.toast = fmt.Sprintf("弹幕最多 50 个字，现在 %d 个", n)
		return
	}
	at := s.at()
	d := dy.Danmaku{At: at + 300*time.Millisecond, Text: text}
	i := sort.Search(len(s.danmaku), func(i int) bool { return s.danmaku[i].At > d.At })
	s.danmaku = slices.Insert(s.danmaku, i, d)
	s.dm.own = d
	a.dmText, a.dmOff = "", false
	id := s.v.ID
	go func() {
		err := a.sendDanmaku(id, at, text)
		a.post(func() {
			if err != nil {
				if j := slices.Index(s.danmaku, d); j >= 0 {
					s.danmaku = slices.Delete(s.danmaku, j, j+1)
				}
				a.toast = "弹幕没发出去：" + err.Error()
				return
			}
			a.toast = "弹幕已发送"
		})
	}()
}

// setPip opens 小窗, which plays the current video over other windows, or
// closes it; the main window shows the video's cover meanwhile.
func (a *app) setPip(on bool) {
	if on == a.pipOn {
		return
	}
	a.pipOn = on
	if !on {
		a.pipVideo.hide()
		if a.pip != nil {
			w := a.pip
			a.pip = nil
			w.Close()
		}
		a.redraw()
		return
	}
	if a.win == nil { // a test's view
		return
	}
	a.pip = mygo.NewWindow(mygo.WindowOptions{
		Title:           "MyDouyin 小窗",
		Width:           480,
		Height:          270,
		MinWidth:        240,
		MinHeight:       135,
		StateKey:        "pip",
		Frameless:       true,
		BackgroundColor: "#000000",
		Content:         ui.View(a.pipView),
	})
	a.pip.SetAlwaysOnTop(true)
	w := a.pip
	w.OnClosed(func() {
		if a.pip == w { // closed from its own window
			a.pipVideo.hide()
			a.pip, a.pipOn = nil, false
			a.redraw()
		}
	})
}

// setSpeed plays every video at s, and the ones to come.
func (a *app) setSpeed(s float64) {
	if !slices.Contains(speeds, s) {
		s = 1
	}
	a.speed = s
	a.speedBits.Store(math.Float64bits(s))
	for _, p := range a.pages {
		for _, sl := range p.list {
			if sl.ap != nil {
				sl.ap.SetSpeed(s)
			}
			if sl.live != nil && sl.v.Live == nil {
				sl.live.SetSpeed(s)
			}
		}
	}
	a.hgApplySpeed(s)
}

func (s *slot) pruneDanmaku(at, flightDur time.Duration) {
	if flightDur <= 0 {
		flightDur = flight
	}
	if i := sort.Search(len(s.danmaku), func(i int) bool { return s.danmaku[i].At >= at-flightDur }); i > 0 {
		for _, d := range s.danmaku[:i] {
			delete(s.dm.lane, d)
		}
		s.danmaku = slices.Delete(s.danmaku, 0, i)
	}
}

// loadDanmaku loads s's bullet comments for the next 32 seconds, as Douyin's
// page does, once the video nears where they end.
func (a *app) loadDanmaku(s *slot, at time.Duration) {
	flightDur := a.dmVideoFlight()
	s.pruneDanmaku(at, flightDur)
	if !s.dmLoading { // Skip windows missed while hidden, keeping comments still in flight.
		s.dmTo = max(s.dmTo, max(0, at-flightDur)/(32*time.Second)*(32*time.Second))
	}
	if a.dmOff || a.c == nil || s.dmLoading || s.dmTo >= s.v.Duration || at+5*time.Second < s.dmTo {
		return
	}
	s.dmLoading = true
	from, gen := s.dmTo, s.dmGen
	go func() {
		ds, err := a.c.Danmaku(s.v.ID, s.v.Duration, from, from+32*time.Second)
		a.post(func() {
			if gen != s.dmGen { // a seek came meanwhile
				return
			}
			s.dmLoading = false
			if err != nil {
				log.Printf("danmaku %s: %v", s.v.ID, err)
				s.dmTo = s.v.Duration // not again for this video
				return
			}
			s.danmaku, s.dmTo = append(s.danmaku, ds...), from+32*time.Second
		})
	}()
}

type commentRowKind uint8

const (
	rowComment commentRowKind = iota
	rowReply
	rowReplyActions
	rowFooter
)

type commentRow struct {
	kind commentRowKind
	ci   int
	ri   int
}

// sheet is the comments of the current video, loaded a page at a time.
type sheet struct {
	total    int
	items    []dy.Comment
	next     dy.Pager[dy.Comment]
	fetching bool
	done     bool
	msg      string
	verified bool // Douyin's verification passed for comments once: not asked again
	list     ui.ListState
	rows     []commentRow

	threads map[string]*thread // the replies of comments, by comment ID, once asked for
	liking  map[string]bool    // the comments, by ID, whose like is on its way
	draft   commentDraft       // the comment being composed at the bottom of the panel

	related bool // the 相关推荐 tab shown rather than the comments
	v       dy.Video
	rel     []dy.Video // the videos related to v, loaded once the tab shows
	relNext dy.Pager[dy.Video]
	relBusy bool
	relDone bool
	relMsg  string
}

// thread is a comment's replies, shown under it while open, loaded a page
// at a time.
type thread struct {
	video, cid string // the comment's
	items      []dy.Comment
	next       dy.Pager[dy.Comment]
	open       bool
	fetching   bool
	done       bool
	msg        string
	verified   bool // Douyin's verification passed for it once: not asked again
}

// comments opens s's comments beside it; the video goes on playing. Their
// first page is there already if s played a moment (prefetchComments).
func (a *app) comments(s *slot) {
	a.notes, a.dms = nil, nil
	if s.comments == nil {
		var next dy.Pager[dy.Comment]
		if a.c != nil {
			next = a.c.Comments(s.v.ID)
		}
		s.comments = &sheet{total: s.v.Comments, next: next, done: next == nil, v: s.v}
	}
	a.sheet = s.comments
	if len(a.sheet.items) == 0 && a.sheet.next != nil { // not fetched ahead, or that failed: again
		a.moreComments(a.sheet)
	}
	if a.signedIn {
		a.web.warm() // for the replies, which Douyin gives only the page, and the likes
	}
}

func (a *app) followSheet(s *slot, related bool) {
	if s == nil || s.v.Live != nil {
		a.sheet = nil
		return
	}
	a.comments(s)
	a.sheet.related = related
	if related && len(a.sheet.rel) == 0 && a.c != nil {
		a.moreRelated()
	}
}

// prefetchComments fetches the first page of s's comments: Douyin takes some
// 0.3–0.45 s to give them (measured 2026-11-19), which the panel waited
// out empty. Not before s has played a moment, so as not to ask for every
// video flicked past.
func (a *app) prefetchComments(s *slot) {
	if s.comments != nil || a.c == nil || s.v.Live != nil {
		return
	}
	s.comments = &sheet{total: s.v.Comments, next: a.c.Comments(s.v.ID), v: s.v}
	a.moreComments(s.comments)
}

// toggleReplies shows the replies to cm under it, or hides them.
func (a *app) toggleReplies(cm dy.Comment) {
	sh := a.sheet
	if sh.threads == nil {
		sh.threads = map[string]*thread{}
	}
	th := sh.threads[cm.ID]
	if th == nil {
		th = &thread{video: sh.v.ID, cid: cm.ID, next: a.replies(sh.v.ID, cm.ID)}
		sh.threads[cm.ID] = th
	}
	th.open = !th.open
	if th.open && len(th.items) == 0 {
		a.moreReplies(th)
	}
}

// moreReplies loads the next page of th.
func (a *app) moreReplies(th *thread) {
	if th.fetching || th.done {
		return
	}
	th.fetching = true
	go func() {
		cs, err := th.next()
		a.post(func() {
			th.fetching = false
			if err != nil {
				th.msg = err.Error()
				switch {
				case !errors.Is(err, dy.ErrVerify):
				case th.verified: // passed, and held back still: asking again would only flash the verification
					th.msg = "验证过了，抖音仍没给回复，稍后点「展开」再试"
					th.verified = false
				default:
					th.msg += "，完成后会自动载入"
					a.verify("/aweme/v1/web/comment/list/reply/?device_platform=webapp&aid=6383&channel=channel_pc_web&item_id="+th.video+
						"&comment_id="+th.cid+"&cut_version=1&cursor=0&count=20&item_type=0", func() { th.verified = true; a.moreReplies(th) }, verifyRead)
				}
				return
			}
			th.msg, th.done, th.verified = "", len(cs) == 0, false
			th.items = append(th.items, cs...)
		})
	}()
}

// moreRelated loads the next page of the videos related to the sheet's.
func (a *app) moreRelated() {
	sh := a.sheet
	if sh == nil || sh.relBusy || sh.relDone {
		return
	}
	if sh.relNext == nil {
		if a.c == nil {
			sh.relDone = true
			return
		}
		sh.relNext = a.c.Related(sh.v.ID)
	}
	sh.relBusy = true
	go func() {
		vs, err := sh.relNext()
		a.post(func() {
			sh.relBusy = false
			if err != nil {
				sh.relMsg = err.Error()
				return
			}
			sh.relMsg, sh.relDone = "", len(vs) == 0
			sh.rel = append(sh.rel, vs...)
		})
	}()
}

// playRelated plays the related video i, in a page of them, which goes on
// with more of them as it nears their end.
func (a *app) playRelated(i int) {
	sh := a.sheet
	p := &page{title: "相关推荐", next: sh.relNext, done: sh.relDone || sh.relNext == nil}
	for _, v := range sh.rel {
		p.list = append(p.list, &slot{v: v})
	}
	p.idx = i
	a.open(p)
}

// openMix opens v's series, unless p is it already.
func (a *app) openMix(p *page, v dy.Video) {
	if title := "合集：" + v.Mix.Name; p.title != title {
		a.open(&page{title: title, next: a.c.MixVideos(v.Mix.ID), grid: true})
	}
}

func (a *app) moreComments(sh *sheet) {
	if sh == nil || sh.fetching || sh.done {
		return
	}
	sh.fetching = true
	go func() {
		cs, err := sh.next()
		a.post(func() {
			sh.fetching = false
			if err != nil {
				sh.msg = err.Error()
				switch {
				case !errors.Is(err, dy.ErrVerify):
				case sh.verified:
					sh.msg = "验证过了，抖音仍没给评论，稍后点「重试」再试"
					sh.verified = false
				default:
					if a.sheet == sh {
						sh.msg += "，完成后会自动载入"
						a.verify("/aweme/v1/web/comment/list/?device_platform=webapp&aid=6383&channel=channel_pc_web&aweme_id="+sh.v.ID+
							"&cursor=0&count=20&item_type=0&insert_ids=&whale_cut_token=&cut_version=1&rcFT=", func() { sh.verified = true; a.moreComments(sh) }, verifyRead)
					}
				}
				return
			}
			sh.msg, sh.done, sh.verified = "", len(cs) == 0, false
			sh.items = append(sh.items, cs...)
		})
	}()
}

// escape leaves a chat for the inbox, closes what is open beside the page,
// or else the page on top.
func (a *app) escape() {
	if a.brandMenuOpen {
		a.brandMenuOpen = false
		return
	}
	if a.picViewer.open {
		a.picViewer.open = false
		return
	}
	if a.upd.dialogOpen {
		a.upd.dialogOpen = false
		return
	}
	if a.settingsOpen {
		a.settingsOpen = false
		return
	}
	if a.mode == modeHongguo {
		if a.theater || a.fullScreen() {
			a.setFullScreen(false)
			return
		}
		if a.hg != nil && a.hg.player != nil {
			hp := a.hg.player
			if hp.qualityOpen || hp.speedOpen || hp.volumeOpen || hp.dmSettingsOpen {
				hp.qualityOpen, hp.speedOpen, hp.volumeOpen, hp.dmSettingsOpen = false, false, false, false
				return
			}
		}
		if a.hg != nil && a.hg.root == hgRootOrbit && len(a.hg.stack) == 0 && a.hg.orbit.hotOpen {
			a.hg.orbit.hotOpen = false
			return
		}
		a.hgPop()
		return
	}
	if a.mode != modeDouyin {
		return
	}
	if a.dmSettingsOpen {
		a.dmSettingsOpen = false
		return
	}
	if chat := a.liveChat; chat != nil && chat.gifts.open {
		chat.gifts.open = false
		return
	}
	if a.group.open {
		a.group.open = false
		return
	}
	if a.sheet != nil && a.sheet.draft.emojiOpen {
		a.sheet.draft.emojiOpen = false
		return
	}
	if a.theater { // out of 网页全屏 or full screen first
		a.setFullScreen(false)
		return
	}
	if a.dms != nil && a.dms.open >= 0 {
		a.dms.open = -1
		return
	}
	if a.sheet != nil || a.notes != nil || a.dms != nil {
		a.sheet, a.notes, a.dms = nil, nil, nil
		return
	}
	if a.isHome(a.cur()) && a.orbit.hotOpen {
		a.orbit.hotOpen = false
		return
	}
	a.back()
}

func (a *app) cur() *page { return a.pages[len(a.pages)-1] }

// plays reports whether p shows a video rather than its covers.
func (p *page) plays() bool { return !p.grid || p.playing }

func (a *app) post(fn func()) {
	if a.win == nil { // a test's view without a window: its own queue, if it keeps one
		if a.posted != nil {
			a.posted <- fn
		}
		return
	}
	a.win.Update(func() {
		fn()
		if a.pip != nil {
			a.pip.Invalidate()
		}
	})
}

// more fetches the next page of p's list.
func (a *app) more(p *page) {
	if p.from != nil {
		p = p.from
	}
	if p.fetching || p.done {
		return
	}
	p.fetching = true
	next, gen := p.next, p.gen
	go func() {
		vs, err := next()
		a.post(func() {
			if gen != p.gen {
				return
			}
			p.fetching = false
			if err != nil {
				p.msg = err.Error()
				return
			}
			p.msg, p.done = "", len(vs) == 0
			existing := make(map[string]bool, len(p.list))
			for _, s := range p.list {
				if s.v.ID != "" {
					existing[s.v.ID] = true
				}
			}
			added := 0
			for _, v := range vs {
				if v.ID != "" && existing[v.ID] {
					continue
				}
				if v.ID != "" {
					existing[v.ID] = true
					if p == a.picks {
						a.picksIDs.Store(v.ID, struct{}{})
					}
				}
				p.list = append(p.list, &slot{v: v})
				added++
			}
			a.onOrbitSourceLoaded(p, added)
			if c := a.cur(); c == p || c.from == p { // a page opened over p while it loaded prepares nothing of p
				a.fill()
			}
		})
	}()
}

// fill only opens the visible video. The next item can preload its track data
// without creating another decoder.
func (a *app) fill() {
	if a.mode != modeDouyin {
		return
	}
	p := a.cur()
	if f := p.from; f != nil {
		if f.gen == p.fromGen {
			p.list = append(p.list, f.list[len(p.list):]...)
		} else {
			p.from, p.done = nil, true
		}
	}
	defer a.preloadNextVideo(p)
	if !p.plays() { // covers, the grid asking for more as it scrolls; only 精选's first plays
		if a.isHome(p) {
			a.ensureOrbitLoaded()
			a.sync()
			return
		}
		for i, s := range p.list {
			if i == 0 && p == a.picks {
				a.prepare(s)
			} else if s != a.held {
				a.release(s)
			}
		}
		if len(p.list) == 0 {
			a.more(p)
		}
		a.sync()
		return
	}
	for i, s := range p.list {
		if i != p.idx && s != a.held {
			a.release(s)
		}
	}
	if p.idx < len(p.list) {
		s := p.list[p.idx]
		if a.held == s {
			a.held = nil
		}
		a.prepare(s)
	}
	if len(p.list)-p.idx <= ahead+2 {
		a.more(p)
	}
	a.sync()
}

// hold keeps one paused on-demand player for an immediate return. A live room
// reopens at the current live edge, so there is no playback position to retain.
func (a *app) hold(s *slot) {
	if s.vp == nil || s.v.Live != nil {
		return
	}
	if a.held != nil && a.held != s {
		a.release(a.held)
	}
	a.held = s
	s.pause()
}

// open preserves the current video while browsing a child page.
func (a *app) open(p *page) {
	old := a.cur()
	old.paused = a.paused
	if old.plays() && old.idx < len(old.list) {
		a.hold(old.list[old.idx])
	}
	for _, s := range old.list {
		if s != a.held {
			a.release(s)
		}
	}
	a.pages = append(a.pages, p)
	a.sheet = nil
	a.paused = false
	a.fill()
}

// picksPage is 精选's tab id, which refresh takes from the top again.
func picksPage(c *dy.Client, title string, id int) *page {
	return &page{title: title, next: c.Picks(id), grid: true, restart: func() dy.Pager[dy.Video] { return c.Picks(id) }}
}

// root shows r with the pages that were over it when another root was
// shown; pressed with pages over it, r alone; r shown already, it is
// refreshed, as Douyin's sidebar does pressed again; the dock does the same.
func (a *app) root(r *page) {
	if len(a.pages) == 1 && a.cur() == r {
		if r.grid && r.playing {
			a.back() // to the covers first
		} else {
			a.refresh(r)
		}
		return
	}
	a.cur().paused = a.paused
	if a.held != nil {
		a.release(a.held)
	}
	for _, p := range a.pages {
		for _, s := range p.list {
			a.release(s)
		}
	}
	pages := []*page{r}
	if a.pages[0] != r {
		if a.stacks == nil {
			a.stacks = map[*page][]*page{}
		}
		a.stacks[a.pages[0]] = a.pages
		if s := a.stacks[r]; s != nil {
			pages = s
		}
	}
	delete(a.stacks, r)
	a.pages, a.sheet, a.paused = pages, nil, pages[len(pages)-1].paused
	a.emojiCache = nil
	a.fill()
}

// refresh empties p and loads it anew, from the top where it has a restart:
// what comes is new, as Douyin's lists are recommendations (2026-11-19,
// 精选 and 关注 shared nothing with the page before).
func (a *app) refresh(p *page) {
	a.empty(p)
	a.sheet, a.paused = nil, false
	a.fill()
}

// empty drops p's list, which loads anew once p shows.
func (a *app) empty(p *page) {
	p.gen++
	p.fetching = false
	for _, s := range p.list {
		a.release(s)
	}
	if p.restart != nil {
		p.next = p.restart()
	}
	p.list, p.idx, p.playing, p.done, p.msg, p.paused = nil, 0, false, false, "", false
	p.gridList = ui.ListState{}
}

// play plays p's video i, from its covers.
func (a *app) play(p *page, i int) {
	p.idx, p.playing, a.paused, a.sheet = i, true, false, nil
	a.fill()
}

// loadAccount fetches the account logged in and all it follows, for the dock and 关注.
// shortcut: loads every account followed at once; page it on scroll if lists of thousands matter.
func (a *app) loadAccount() {
	go func() {
		me, err := a.c.Self()
		var us []dy.User
		later, _ := a.c.WatchLater()() // the first page, enough to mark most
		unread, _ := a.c.Unread()
		if err == nil {
			next := a.c.Following(me.ID)
			for {
				page, perr := next()
				if perr != nil {
					err = perr
				}
				if perr != nil || len(page) == 0 {
					break
				}
				us = append(us, page...)
			}
		}
		a.post(func() {
			a.me, a.follows, a.unread = me, us, unread
			if me.ID != "" {
				a.resumeWatched(me.ID)
			}
			if a.later == nil {
				a.later, a.laterBusy = map[string]bool{}, map[string]bool{}
			}
			for _, v := range later {
				a.later[v.ID] = true
			}
			if me.ID != "" && a.mine == nil {
				list := func(title string, from func() dy.Pager[dy.Video]) *page {
					return &page{title: title, next: from(), grid: true, restart: from, covers: portraitCovers}
				}
				posts := list("作品", func() dy.Pager[dy.Video] { return a.c.MyPosts(me.ID) })
				posts.authorID = me.ID
				a.mine = map[string]*page{
					"作品":   posts,
					"喜欢":   list("喜欢", func() dy.Pager[dy.Video] { return a.c.Liked(me.ID) }),
					"收藏":   list("收藏", a.c.Collected),
					"观看历史": list("观看历史", a.c.History),
					"稍后再看": list("稍后再看", a.c.WatchLater),
				}
				a.mineTab = "作品"
			}
			if err != nil {
				a.followMsg = "关注列表没载入：" + err.Error()
			}
		})
	}()
}

// back closes the page on top and goes on with the one under it.
// A grid page playing goes back to its covers first.
func (a *app) back() {
	if p := a.cur(); p.grid && p.playing {
		p.paused = a.paused
		if p.idx < len(p.list) {
			a.hold(p.list[p.idx])
		}
		p.playing, a.sheet = false, nil
		a.fill()
		return
	}
	if len(a.pages) == 1 {
		return
	}
	for _, s := range a.cur().list {
		a.release(s)
	}
	a.pages[len(a.pages)-1] = nil
	a.pages = a.pages[:len(a.pages)-1]
	a.sheet = nil
	a.paused = a.cur().paused
	a.fill()
}

func (a *app) prepare(s *slot) {
	s.gone = false // back in range while still loading: keep what arrives
	if s.loading || s.vp != nil || s.err != nil {
		return
	}
	s.loading = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.loadGen++
	gen, v, quality, resume, from, cached := s.loadGen, s.v, a.quality, s.resume, s.liveFrom, s.cached
	var fetchLive func(context.Context, string) (dy.Video, error)
	if v.Live != nil {
		quality = a.liveQuality
		lv := *v.Live
		v.Live = &lv
		if v.Live.WebRID != "" && (s.liveFreshAt.IsZero() || time.Since(s.liveFreshAt) > 2*time.Minute) {
			fetchLive = a.fetchLiveRoom
			if fetchLive == nil && a.c != nil {
				fetchLive = a.c.LiveRoom
			}
		}
	}
	var preload *media.WarmVideo
	if v.Live == nil && a.preload.slot == s && a.preload.url == videoQuality(v, quality).URL {
		preload = a.preload.asset
		a.preload = nextVideo{}
	}
	go func() {
		var fresh *dy.Video
		var freshAt time.Time
		var refreshedVideo *dy.Video
		if cached && v.Live == nil && v.ID != "" && v.PlayURL == "" && a.c != nil {
			if fv, ferr := a.c.Video(v.ID); ferr == nil && fv.PlayURL != "" {
				v = fv
				refreshedVideo = &fv
			}
		}
		if fetchLive != nil {
			rctx, rcancel := context.WithTimeout(ctx, 6*time.Second)
			fv, ferr := fetchLive(rctx, v.Live.WebRID)
			rcancel()
			if errors.Is(ferr, dy.ErrLiveEnded) {
				a.post(func() {
					if gen != s.loadGen || s.gone {
						cancel()
						return
					}
					s.loading, s.cancel = false, nil
					cancel()
					if s.v.Live != nil {
						s.v.Live.Ended = true
					}
					s.err = dy.ErrLiveEnded
					a.fill()
				})
				return
			}
			if ferr == nil && fv.Live != nil {
				fresh = &fv
				freshAt = time.Now()
				lv := *fv.Live
				v.Live = &lv
				v.Qualities = fv.Qualities
			}
		}
		pl, err := a.build(ctx, v, quality, resume, from, preload)
		if err != nil && cached && refreshedVideo == nil && v.Live == nil && v.ID != "" && a.c != nil && ctx.Err() == nil {
			if fv, ferr := a.c.Video(v.ID); ferr == nil && fv.PlayURL != "" {
				v = fv
				refreshedVideo = &fv
				pl, err = a.build(ctx, v, quality, resume, from, nil)
			}
		}
		a.post(func() {
			if gen != s.loadGen || s.gone {
				pl.close()
				cancel()
				return
			}
			if fresh != nil && s.v.Live != nil && fresh.Live != nil {
				// Update Live fields in place so a.liveChat.room == s.v.Live stays intact.
				if fresh.Live.RoomID != "" {
					s.v.Live.RoomID = fresh.Live.RoomID
				}
				if fresh.Live.OwnerID != "" {
					s.v.Live.OwnerID = fresh.Live.OwnerID
				}
				if fresh.Live.Viewers != "" {
					s.v.Live.Viewers = fresh.Live.Viewers
				}
				s.v.Live.HLS = fresh.Live.HLS
				s.v.Live.H264HLS = fresh.Live.H264HLS
				s.v.Live.DefaultQuality = fresh.Live.DefaultQuality
				s.v.Live.Ended = fresh.Live.Ended
				s.v.Live.Streams = fresh.Live.Streams
				s.v.Qualities = fresh.Qualities
				if err == nil {
					s.liveFreshAt = freshAt
				}
			}
			if refreshedVideo != nil {
				s.v = *refreshedVideo
			}
			s.cached = false
			s.loading, s.cancel = false, nil
			cancel()
			s.players, s.err = pl, err
			if err == nil {
				s.lastAt, s.resume = resume, 0
			}
			if s.live != nil && s.v.Live == nil {
				s.live.SetSpeed(a.speed)
			}
			a.fill()
		})
	}()
}

// save downloads the chosen source to Downloads; streaming does not retain a full file.
func (a *app) save(s *slot) {
	home, err := os.UserHomeDir()
	if err != nil {
		s.saved = "下载失败：" + err.Error()
		return
	}
	s.saved = "保存中…"
	src, v, quality := s.path, s.v, a.quality
	go func() {
		var dst string
		var err error
		if src != "" {
			dst, err = copyTo(src, filepath.Join(home, "Downloads"), fileName(v))
		} else {
			dst = filepath.Join(home, "Downloads", fileName(v))
			err = a.c.Download(videoQuality(v, quality).URL, dst)
		}
		a.post(func() {
			if err != nil {
				s.saved = "下载失败：" + err.Error()
			} else {
				s.saved = "已存到 下载/" + filepath.Base(dst)
			}
			a.toast = s.saved
		})
	}()
}

// fileName is a video's name in Downloads: its author and id, without the
// characters a file name cannot have.
func fileName(v dy.Video) string {
	name := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < ' ' {
			return '_'
		}
		return r
	}, v.Author)
	return "抖音-" + name + "-" + v.ID + ".mp4"
}

// copyTo copies src into dir as name, through a .part file so a failed copy
// leaves nothing half-written, and returns the path.
func copyTo(src, dir, name string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	dst := filepath.Join(dir, name)
	out, err := os.Create(dst + ".part")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(dst+".part", dst)
	}
	if err != nil {
		os.Remove(dst + ".part")
		return "", err
	}
	return dst, nil
}

var qualityRank = map[string]int{"4K": 5, "2K": 4, "1080P": 3, "720P": 2, "540P": 1}

// videoQuality is what v plays in for the definition chosen: the one named,
// else the best below it, as 1080P for 4K where the video has none, else
// Douyin's pick (智能), which has no name. The bar shows its name.
func videoQuality(v dy.Video, quality string) dy.Quality {
	pick := dy.Quality{URL: v.PlayURL}
	if quality == "" || quality == "智能" {
		return pick
	}
	for _, q := range v.Qualities {
		if q.Name == quality {
			return q
		}
	}
	if want := qualityRank[quality]; want > 0 {
		var best dy.Quality
		for _, q := range v.Qualities {
			if r := qualityRank[q.Name]; r > 0 && r <= want && r > qualityRank[best.Name] {
				best = q
			}
		}
		if best.URL != "" {
			return best
		}
	}
	return pick
}

func liveCandidates(v dy.Video, quality string, preferH264 bool) []string {
	if v.Live == nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	add := func(u string) bool {
		if u == "" || seen[u] {
			return false
		}
		seen[u] = true
		out = append(out, u)
		return true
	}
	addStream := func(st dy.LiveStream) bool {
		a := add(st.URL)
		b := add(st.BackupURL)
		return a || b
	}
	if quality != "" && quality != "智能" {
		for _, st := range v.Live.Streams {
			if st.Name == quality {
				addStream(st)
				break
			}
		}
	}
	hasH264 := false
	if preferH264 {
		if def := v.Live.DefaultQuality; def != "" {
			for _, st := range v.Live.Streams {
				if st.Name == def && st.H264 {
					if addStream(st) || st.URL != "" || st.BackupURL != "" {
						hasH264 = true
					}
					break
				}
			}
		}
		for _, st := range v.Live.Streams {
			if st.H264 {
				if addStream(st) || st.URL != "" || st.BackupURL != "" {
					hasH264 = true
				}
			}
		}
		if add(v.Live.H264HLS) || v.Live.H264HLS != "" {
			hasH264 = true
		}
	} else {
		if def := v.Live.DefaultQuality; def != "" {
			for _, st := range v.Live.Streams {
				if st.Name == def {
					addStream(st)
					break
				}
			}
		}
		for _, st := range v.Live.Streams {
			addStream(st)
		}
	}
	add(v.Live.HLS)
	if !preferH264 || !hasH264 {
		for _, st := range v.Live.Streams {
			addStream(st)
		}
	}
	return out
}

func (a *app) openLiveCandidate(ctx context.Context, url string, native bool) (players, error) {
	var pl players
	var err error
	if native {
		pl.live, err = media.OpenNative(ctx, url, liveHeaders, false)
	} else {
		pl.live, err = media.OpenLiveHeaders(ctx, url, liveHeaders)
	}
	if err != nil {
		return players{}, err
	}
	stream := pl.live
	stop := context.AfterFunc(ctx, func() { stream.Close() })
	newPlayer := newStreamPlayer
	if native {
		newPlayer = newNativePlayer
	}
	vp, err := newPlayer(stream, false)
	stop() // a close it did not stop has begun: ctx is done
	if err == nil {
		pl.vp = vp
		err = ctx.Err()
	} else if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		pl.close()
		return players{}, err
	}
	pl.vp.Pause()
	if !native {
		stream.Pause()
	}
	return pl, nil
}

// build opens v; a live room from the stream of its liveCandidates at from.
func (a *app) build(ctx context.Context, v dy.Video, quality string, resume time.Duration, from int, preload *media.WarmVideo) (pl players, err error) {
	if preload != nil {
		defer preload.Close()
	}
	if err = ctx.Err(); err != nil {
		return
	}
	native := a.win != nil && media.NativeAvailable
	if v.Live != nil {
		candidates := liveCandidates(v, quality, media.PreferH264)
		if len(candidates) == 0 {
			return players{}, errors.New("media: empty stream address")
		}
		if len(candidates) > 4 {
			candidates = candidates[:4]
		}
		open := a.openLive
		if open == nil {
			open = a.openLiveCandidate
		}
		lastErr := errors.New("media: no stream of the room showed a picture")
		for i := from; i < len(candidates); i++ {
			if err = ctx.Err(); err != nil {
				return players{}, err
			}
			attemptCtx := ctx
			var attemptCancel context.CancelFunc
			if i < len(candidates)-1 {
				attemptCtx, attemptCancel = context.WithTimeout(ctx, 8*time.Second)
			}
			pl, err = open(attemptCtx, candidates[i], native)
			if attemptCancel != nil {
				attemptCancel()
			}
			if err == nil {
				pl.stream, pl.more = i, i < len(candidates)-1
				return pl, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				return players{}, ctx.Err()
			}
		}
		return players{}, lastErr
	}
	url, headers := videoQuality(v, quality).URL, videoHeaders
	switch {
	case native:
		if preload != nil {
			pl.live, _ = preload.Open()
		}
		if pl.live == nil {
			pl.live, err = media.OpenNative(ctx, url, headers, true)
		}
	default:
		if preload != nil {
			pl.live, _ = preload.OpenContext(ctx, false)
		}
		if pl.live == nil {
			pl.live, err = media.OpenVideo(ctx, url, headers)
		}
	}
	if err != nil {
		return players{}, err
	}
	stream := pl.live
	stop := context.AfterFunc(ctx, func() { stream.Close() })
	if resume > 0 {
		stream.Seek(resume)
	}
	newPlayer := newStreamPlayer
	if native {
		newPlayer = newNativePlayer
	}
	vp, err := newPlayer(stream, true)
	stop() // a close it did not stop has begun: ctx is done
	if err == nil {
		pl.vp = vp
		err = ctx.Err()
	}
	if err != nil {
		pl.close()
		return players{}, err
	}
	pl.vp.Pause()
	stream.Pause()
	return pl, nil
}

// switchStream has a live room that shows no picture play the next of its
// streams, once this frame is built: one whose player failed before its
// first picture, or took over 8 s for it while others are left. It
// reports whether it switches, so the failure does not show meanwhile.
func (a *app) switchStream(s *slot) bool {
	if s.v.Live == nil || s.vp == nil || videoPictureReady(s.vp) ||
		s.vp.Err() == nil && !(s.more && pictureWait(s.vp) > 8*time.Second) {
		return false
	}
	vp := s.vp
	a.post(func() {
		if s.vp == vp {
			s.liveFrom = s.stream + 1
			a.release(s)
			a.prepare(s)
		}
	})
	return true
}

// release remembers progress and cancels work that can no longer be shown.
func (a *app) release(s *slot) {
	if a.held == s {
		a.held = nil
	}
	s.gone = s.loading
	if s.cancel != nil {
		s.cancel()
		s.cancel, s.loading = nil, false
		s.loadGen++
	}
	if s.vp != nil && s.v.Live == nil {
		s.resume = s.at()
		if s.seeking && s.live != nil && s.live.Seeking() {
			s.resume = s.seekTarget
		}
	}
	s.players.close()
	s.players = players{}
	s.err = nil
	s.clearDanmaku()
	if s.comments != nil && a.sheet != s.comments {
		s.comments = nil
	}
	s.dmLoading, s.seeking = false, false
	s.dmTo = max(0, s.resume-a.dmVideoFlight()) / (32 * time.Second) * (32 * time.Second)
	s.dmGen++
}

// sync plays the current video and holds the others; 精选's covers play
// their first video, silent.
func (a *app) sync() {
	if a.mode != modeDouyin {
		return
	}
	p := a.cur()
	for i, s := range p.list {
		switch {
		case s.vp == nil:
		case !p.plays() && i == 0 && p == a.picks:
			s.play(false)
		case i == p.idx && !a.paused && p.plays():
			s.play(true)
		default:
			s.pause()
		}
	}
}

// syncAwake keeps the computer from sleeping while the video shown plays,
// and the display on too unless it plays as sound alone (听抖音), as each
// frame finds it: a pause, an error or leaving the video lets them sleep.
func (a *app) syncAwake() {
	on, display := false, false
	switch {
	case a.mode == modeDouyin:
		if p := a.cur(); p.plays() && p.idx < len(p.list) {
			vp := p.list[p.idx].vp
			on, display = vp != nil && vp.Playing(), !a.listen
		}
	case a.hgPlaying() && a.hg.player != nil:
		vp := a.hg.player.pl.vp
		on, display = vp != nil && vp.Playing(), true
	}
	if on == (a.awake != nil) && (!on || display == a.awakeDisplay) {
		return
	}
	if a.awake != nil {
		a.awake()
		a.awake = nil
	}
	hold := a.keepAwake
	if hold == nil && a.win != nil {
		hold = mygo.Power.KeepAwake
	}
	if on && hold != nil {
		a.awake, a.awakeDisplay = hold("播放视频", display), display
	}
}

// play plays pl's picture, and its sound if sound, else none of it.
func (pl players) play(sound bool) {
	pl.vp.Play()
	if pl.live != nil {
		pl.live.SetMuted(!sound)
		pl.live.SetVolume(volumeNow.Load())
		pl.live.Play()
	}
	if pl.ap != nil {
		if sound {
			pl.ap.Play()
		} else {
			pl.ap.Pause()
		}
	}
}

func (pl players) pause() {
	pl.vp.Pause()
	if pl.live != nil {
		pl.live.Pause()
	}
	if pl.ap != nil {
		pl.ap.Pause()
	}
}

// step moves to the next (+1) or previous (-1) video. A wheel's momentum sends
// events for a second after the finger lifts, so one step per 400 ms.
func (a *app) step(d int) {
	now := time.Now()
	if now.Sub(a.lastNav) < 400*time.Millisecond {
		return
	}
	p := a.cur()
	i := p.idx + d
	if !p.plays() || i < 0 || i >= len(p.list) {
		return
	}
	a.lastNav = now
	keepSheet, relatedTab := a.sheet != nil, a.sheet != nil && a.sheet.related
	if p.idx < len(p.list) { // past the end, 载入中… shows: nothing slides away
		a.slides++
		a.slide = &slide{n: a.slides, from: p.list[p.idx].v.Poster, to: p.list[i], dir: d}
	}
	p.idx, a.paused = i, false
	if keepSheet {
		a.followSheet(p.list[i], relatedTab)
	} else {
		a.sheet = nil
	}
	a.fill()
}

// watch follows where s plays, at, and reports whether it has looped back
// to its start with 连播 on: the next video is to play instead.
func (a *app) watch(s *slot, at time.Duration) bool {
	if s.seeking {
		// AVPlayer seeks asynchronously; the old position is not a loop.
		if s.live != nil {
			if s.live.Seeking() {
				return false
			}
		} else if at < s.seekTarget-500*time.Millisecond || at > s.seekTarget+500*time.Millisecond {
			return false
		}
		s.seeking, s.lastAt = false, at
		return false
	}
	if at >= time.Second {
		a.prefetchComments(s)
	}
	if !s.reported && at >= watchedAfter {
		s.reported = true
		a.markWatched(s.v)
	}
	looped := at+time.Second < s.lastAt
	if looped {
		s.clearDanmaku()
		s.dmTo, s.dmLoading = 0, false
		s.dmGen++
	}
	s.lastAt = at
	return looped && a.autoNext
}

func (a *app) toggle() {
	a.paused = !a.paused
	a.sync()
	a.preloadNextVideo(a.cur())
}

// search opens the videos found for q, and the accounts above them.
func (a *app) search(q string) {
	if q = strings.TrimSpace(q); q != "" {
		p := &page{title: "搜索：" + q, next: a.c.Search(q), grid: true}
		a.open(p)
		go func() {
			if us, err := a.c.SearchUsers(q); err == nil {
				a.post(func() { p.users = us })
			}
		}()
	}
}

// suggest asks Douyin what it suggests for the search box's text once the
// typing rests a moment.
func (a *app) suggest() {
	a.sugSeq++
	seq, q := a.sugSeq, strings.TrimSpace(a.query)
	if q == "" || q == a.sugFor {
		return
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		a.post(func() {
			if seq != a.sugSeq {
				return // typed on
			}
			go func() {
				ws, err := a.c.Suggest(q)
				if err == nil {
					a.post(func() { a.sugs, a.sugFor = ws, q })
				}
			}()
		})
	}()
}

// openAuthor opens a user's videos, their profile above, unless p is that
// page already.
func (a *app) openAuthor(p *page, name, id string) {
	if id == "" || p.authorID == id {
		return
	}
	if a.openAuthorFn != nil {
		a.openAuthorFn(p, name, id)
		return
	}
	if a.c != nil {
		q := &page{title: "@" + name, authorID: id, next: a.c.Posts(id), grid: true, covers: portraitCovers}
		a.open(q)
		go func() {
			if u, err := a.c.Profile(id); err == nil {
				a.post(func() { q.user = &u })
			}
		}()
	}
}

// selfCheck (DY_SHOT=dir) walks through what the last change touched, as
// clicks would, saves a shot of each step and quits: now the replies, which
// go through the page kept warm while the comments show. For each video
// DY_SHOT_VIDEOS names, comma separated, it opens the comments, expands the
// replies of the first comment that has some at once and of the second one
// past webIdle, logs how long each took, and saves a shot after each
// (1-1.png, 1-2.png…). A verification Douyin asks for waits for the user.
func (a *app) selfCheck(dir string) {
	time.Sleep(3 * time.Second) // the feed up
	for in, start := false, time.Now(); !in && time.Since(start) < 30*time.Second; time.Sleep(200 * time.Millisecond) {
		a.call(func() { in = a.signedIn }) // the page is warmed for a login only
	}
	for i, id := range strings.Split(os.Getenv("DY_SHOT_VIDEOS"), ",") {
		v, err := a.c.Video(id)
		if err != nil {
			log.Println("video:", err)
			os.Exit(1)
		}
		var sh *sheet
		a.call(func() {
			mygo.App.Focus() // a window under others is not drawn, nor its shot
			s := &slot{v: v}
			a.open(&page{title: "评论", done: true, list: []*slot{s}})
			a.comments(s)
			sh = a.sheet
		})
		var withReplies []dy.Comment
		for start := time.Now(); len(withReplies) < 2 && time.Since(start) < 15*time.Second; time.Sleep(100 * time.Millisecond) {
			a.call(func() {
				withReplies = withReplies[:0]
				for _, cm := range sh.items {
					if cm.Replies > 0 {
						withReplies = append(withReplies, cm)
					}
				}
			})
		}
		for j, wait := range []time.Duration{0, webIdle + 2*time.Second} {
			if j >= len(withReplies) {
				log.Printf("self check: %s: %d comments with replies, wanted %d", id, len(withReplies), j+1)
				os.Exit(1)
			}
			time.Sleep(wait)
			cm := withReplies[j]
			var th *thread
			var n int
			var msg string
			var asked, asking bool
			start := time.Now()
			a.call(func() { a.toggleReplies(cm); th = sh.threads[cm.ID] })
			for time.Since(start) < 2*time.Minute {
				a.call(func() { n, msg, asking = len(th.items), th.msg, a.loginOpen && a.login.verify })
				asked = asked || asking
				if n > 0 || msg != "" && !asking {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			log.Printf("self check: %s replies %d (waited %v): %d in %v, verification asked %v, msg %q",
				id, j+1, wait, n, time.Since(start).Round(10*time.Millisecond), asked, msg)
			b, err := a.capture(a.win)
			if err == nil {
				err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d-%d.png", i+1, j+1)), b, 0o644)
			}
			log.Println("shot", i+1, j+1, err)
		}
	}
	os.Exit(0)
}

func (a *app) startDouyinServices() {
	if a.douyinStarted || a.win == nil || a.c == nil {
		return
	}
	a.douyinStarted = true
	if len(a.pages) > 0 && a.isHome(a.cur()) {
		a.prefetchOrbitCovers()
		a.ensureOrbitLoaded()
	}
	a.startLogin()
	go func() {
		if hot, err := a.c.HotSearch(); err == nil {
			a.post(func() { a.hot = hot })
		}
	}()
}

// Run opens the window and blocks until it closes.
func Run() error {
	// One runs: two would share the login, and Douyin's scripts in each page
	// renew the device's signing keys, which Douyin then holds the other's
	// writes against, answering 用户未登录 and ending the session (2026-11-17,
	// a login lost after test builds ran beside the installed app).
	if !mygo.App.RequestSingleInstanceLock() {
		log.Println("douyin: already running")
		return nil
	}
	mygo.App.OnSecondInstance(func([]string, string) { mygo.App.Focus() })
	c, err := dy.New()
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	a := &app{c: c, dir: filepath.Join(cache, "Douyin", "videos"), cacheDir: filepath.Join(cache, "Douyin"),
		feed: &page{title: "推荐", next: c.Feed}, tabPages: map[int]*page{}}
	a.picks = picksPage(c, "精选", 0)
	a.tabPages[0] = a.picks
	a.home = &page{title: "引力场", grid: true}
	a.orbitPicks = a.orbitPicksPage(c)
	a.orbit = orbitState{source: orbitPicks}
	a.loadOrbitCache()
	a.follow = &page{title: "关注", next: c.FollowFeed(), grid: true, restart: c.FollowFeed}
	a.friends = &page{title: "朋友", next: c.FriendFeed(), grid: true, restart: c.FriendFeed}
	a.live = &page{title: "直播", next: c.LiveRooms(), grid: true, restart: c.LiveRooms, covers: liveCovers}
	a.drama = picksPage(c, "短剧", 300214) // 精选's 小剧场: Douyin's own 短剧 page comes rendered, with no list to read
	a.pages = []*page{a.home}
	a.img = newImages(c.Image, a.post)
	a.img.cacheDir = a.coverCacheDir()
	a.img.releasePixels = true
	a.setLiked, a.setSaved, a.likeComment = c.SetLiked, c.SetSaved, c.SetCommentLiked
	a.setFollowed = c.SetFollowed
	a.publishComment, a.uploadCommentImage = c.PublishComment, c.UploadCommentImage
	a.replies = c.Replies
	a.sendShare = func(id, name string) error { return c.ShareTo(id, name, false) }
	a.setLater = c.SetWatchLater
	a.sendWatched = c.Watched
	a.sendDanmaku = func(id string, at time.Duration, text string) error {
		if a.me.UID == "" {
			return errors.New("账号还没载入")
		}
		return c.SendDanmaku(id, a.me.UID, at, text)
	}
	a.web.a = a
	c.SetWeb(&a.web)
	a.loadSettings(settingsFile())
	os.RemoveAll(a.dir) // leftovers of a crashed run
	if os.Getenv("DY_LIVE_WRITE") != "" {
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyProhibited)
	}
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     a.windowTitle(),
			Hidden:    os.Getenv("DY_LIVE_WRITE") != "",
			Width:     1280,
			Height:    820,
			MinWidth:  960,
			MinHeight: 620,
			StateKey:  "main",
			// As MySpotify's: no title bar, the window buttons inset at the top left.
			TitleBarStyle:        mygo.TitleBarHidden,
			TitleBarHeight:       topBarHeight,
			TrafficLightPosition: &mygo.Point{X: 18, Y: 18},
			BackgroundColor:      onOff(a.orbit.theme == "dark" || a.orbit.theme == "" && mygo.Theme.IsDark(), darkPal.bgHex, lightPal.bgHex),
			Content:              ui.View(a.view),
		})
		a.win.OnClosed(func() {
			a.hgClose()
			a.keepWatched()
			a.preload.close()
			a.closeLiveRoom()
			if a.watchTimer != nil {
				a.watchTimer.Stop()
				a.watchTimer = nil
			}
			a.web.closed = true
			a.web.destroy()
			for _, p := range a.pages {
				for _, s := range p.list {
					a.release(s)
				}
			}
			a.setPip(false)
		})
		if a.mode == modeHongguo {
			a.ensureHG()
			a.hgEnsureActiveLoaded()
		} else {
			a.startDouyinServices()
		}
		a.watchUpdates()
		mygo.Theme.OnUpdated(func() { a.post(func() {}) })
		if dir := os.Getenv("DY_SHOT"); dir != "" {
			go a.selfCheck(dir)
		}
		if id := os.Getenv("DY_LIVE_WRITE"); id != "" {
			go a.liveWriteCheck(id)
		}
		if id := os.Getenv("DY_WATCHED_CHECK"); id != "" {
			go a.watchedCheck(id)
		}
	})
	log.Println("douyin: running")
	return mygo.App.Run()
}
