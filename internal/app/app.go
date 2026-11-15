// Package app is the window: the recommend feed, one video at a time, scrolled
// with the wheel or arrow keys, and searches opened over it.
package app

import (
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"douyin/internal/dy"
	"douyin/internal/media"
)

// slot is one feed entry and, once downloaded and opened, its players. Players
// are made for the videos around the current one so a swipe starts at once.
type slot struct {
	v dy.Video
	players
	loading bool
	gone    bool // dropped while loading: close what arrives
	err     error
	saved   string // what saving to Downloads came to: "" before the click

	liking, saving bool // a like or a save of it on its way to Douyin

	danmaku   []dy.Danmaku  // the bullet comments loaded, in time order
	dmTo      time.Duration // how far into the video they are loaded
	dmLoading bool
	lastAt    time.Duration // where the view last saw it, which a loop goes back from
}

// players are what plays a video once it is opened: vp shows it, ap plays
// its sound if it has any, dec decodes a file, live a stream.
type players struct {
	file *media.File
	vp   *ui.VideoPlayer
	ap   *audio.Player // nil when the clip has no audio, and for a stream, whose sound live plays
	dec  *paced        // vp's decoder for a file, whose clock stands in for a missing audio's
	live *media.Live   // vp's decoder for a live room
}

// close stops and lets go of whatever of pl there is.
func (pl players) close() {
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
// a clock where the video has no sound to give one.
type paced struct {
	*media.VideoDecoder
	speed *atomic.Uint64 // the float64 bits, read on the player's goroutine
	done  atomic.Int64   // the video's time decoded, at its own speed
}

func (p *paced) Next() (ui.VideoFrame, error) {
	f, err := p.VideoDecoder.Next()
	p.done.Add(int64(f.Duration))
	if s := math.Float64frombits(p.speed.Load()); s > 0 {
		f.Duration = time.Duration(float64(f.Duration) / s)
	}
	return f, err
}

func (p *paced) Rewind() error {
	p.done.Store(0)
	return p.VideoDecoder.Rewind()
}

// at is how far s has played: its sound's position, else what its video
// has decoded, a few frames ahead of the one shown.
func (s *slot) at() time.Duration {
	switch {
	case s.ap != nil:
		return s.ap.Position()
	case s.dec != nil:
		return time.Duration(s.dec.done.Load())
	}
	return 0
}

// speeds are the choices of the speed button, which goes round them.
var speeds = []float64{1, 1.25, 1.5, 2, 0.75}

const (
	ahead  = 2 // videos prepared after the current one
	behind = 1
)

// page is a list of videos: the picks (精选) or the recommend feed at the
// bottom, and search results or a user's videos opened over it. A grid page
// shows covers until one is clicked, then plays as the feed does, until
// Escape brings the covers back.
type page struct {
	title    string
	next     dy.Pager[dy.Video]
	list     []*slot
	grid     bool
	playing  bool // a grid page playing its idx
	scroll   ui.ScrollState
	idx      int
	fetching bool
	done     bool // next returned its last page
	msg      string
}

// app is read and changed on the main thread: by the view, and by goroutines through post.
type app struct {
	win     *mygo.Window
	c       *dy.Client
	dir     string
	pages   []*page // the window shows the last
	query   string  // the search box
	paused  bool
	lastNav time.Time
	sheet   *sheet // the comments open beside the video, or nil

	searchBox ui.Handle
	toast     string    // a message to show once
	me        dy.User   // the account logged in, once known
	follows   []dy.User // the accounts it follows, atop 关注
	followMsg string    // why they did not load

	picks, feed *page   // the roots: 精选's covers, 推荐's videos one by one
	img         *images // covers and avatars

	// The sidebar's other roots, each a grid; nil where there is no client.
	follow, friends, live, drama *page
	mine                         map[string]*page // 我的's tabs, by name, once the account is known
	mineTab                      string

	tab      int           // 精选's tab shown, a dy.Tabs ID
	tabPages map[int]*page // 精选's tabs opened, kept as they were scrolled

	notes  *notes // the notifications open beside the page, or nil
	unread int
	hot    []dy.Hot // the hot search list, under the search box
	dms    *dms     // the direct messages open beside the page, or nil

	speed     float64       // 1 as recorded
	speedBits atomic.Uint64 // speed for the decoders
	autoNext  bool          // 连播: the next video once one ends, rather than again
	dmOff     bool          // bullet comments hidden

	posted chan func() // what post runs, in a test without a window

	// The rail's share panel: open, its search, and how sending a video to an
	// account went, by video and account ID: "sending", "done".
	shareOpen  bool
	shareQuery string
	shareBox   ui.Handle
	shared     map[string]string
	// sendShare sends a video to an account by name; a test puts its own.
	sendShare func(videoID, name string) error

	// setLiked and setSaved send a like or a save to Douyin; a test puts its own.
	setLiked, setSaved func(id string, on bool) error
	listen             bool // 听抖音: the cover in the picture's place, the sound on
	moreOpen           bool // the rail's 更多 menu
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
var mineTabs = []string{"作品", "喜欢", "收藏", "观看历史"}

// isMine tells whether p is one of 我的's tabs.
func (a *app) isMine(p *page) bool {
	for _, q := range a.mine {
		if p == q {
			return true
		}
	}
	return false
}

// showMine shows 我的, at its tab name, as it was left.
func (a *app) showMine(name string) {
	if p := a.mine[name]; p != nil {
		a.mineTab = name
		a.root(p)
	}
}

// showTab shows 精选's tab id, as it was left if it was open before.
func (a *app) showTab(id int) {
	p := a.tabPages[id]
	if p == nil {
		p = &page{title: "精选", next: a.c.Picks(id), grid: true}
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
			}
		})
	}()
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

// setSpeed plays every video at s, and the ones to come.
func (a *app) setSpeed(s float64) {
	a.speed = s
	a.speedBits.Store(math.Float64bits(s))
	for _, p := range a.pages {
		for _, sl := range p.list {
			if sl.ap != nil {
				sl.ap.SetSpeed(s)
			}
		}
	}
}

// loadDanmaku loads s's bullet comments for the next 32 seconds, as Douyin's
// page does, once the video nears where they end.
func (a *app) loadDanmaku(s *slot, at time.Duration) {
	if a.dmOff || a.c == nil || s.dmLoading || s.dmTo >= s.v.Duration || at+5*time.Second < s.dmTo {
		return
	}
	s.dmLoading = true
	from := s.dmTo
	go func() {
		ds, err := a.c.Danmaku(s.v.ID, s.v.Duration, from, from+32*time.Second)
		a.post(func() {
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

// sheet is the comments of the current video, loaded a page at a time.
type sheet struct {
	total    int
	items    []dy.Comment
	next     dy.Pager[dy.Comment]
	fetching bool
	done     bool
	msg      string

	related bool // the 相关推荐 tab shown rather than the comments
	v       dy.Video
	rel     []dy.Video // the videos related to v, loaded once the tab shows
	relNext dy.Pager[dy.Video]
	relBusy bool
	relDone bool
	relMsg  string
}

// comments opens v's comments over it; the video goes on playing.
func (a *app) comments(v dy.Video) {
	a.notes, a.dms = nil, nil
	a.sheet = &sheet{total: v.Comments, next: a.c.Comments(v.ID), v: v}
	a.moreComments()
}

// moreRelated loads the next page of the videos related to the sheet's.
func (a *app) moreRelated() {
	sh := a.sheet
	if sh == nil || sh.relBusy || sh.relDone {
		return
	}
	if sh.relNext == nil {
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

func (a *app) moreComments() {
	sh := a.sheet
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
				return
			}
			sh.msg, sh.done = "", len(cs) == 0
			sh.items = append(sh.items, cs...)
		})
	}()
}

// escape leaves a chat for the inbox, closes what is open beside the page,
// or else the page on top.
func (a *app) escape() {
	if a.dms != nil && a.dms.open >= 0 {
		a.dms.open = -1
		return
	}
	if a.sheet != nil || a.notes != nil || a.dms != nil {
		a.sheet, a.notes, a.dms = nil, nil, nil
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
	a.win.Update(func() { fn(); a.win.Invalidate() })
}

// more fetches the next page of p's list.
func (a *app) more(p *page) {
	if p.fetching || p.done {
		return
	}
	p.fetching = true
	go func() {
		vs, err := p.next()
		a.post(func() {
			p.fetching = false
			if err != nil {
				p.msg = err.Error()
				return
			}
			p.msg, p.done = "", len(vs) == 0
			for _, v := range vs {
				p.list = append(p.list, &slot{v: v})
			}
			if p == a.cur() { // a page opened over p while it loaded prepares nothing of p
				a.fill()
			}
		})
	}()
}

// fill prepares the videos around the current one, frees the ones far from
// it, and asks for more of the list near its end.
// The current video comes first: the ones around it start once it plays (or
// failed), so they do not share its bandwidth.
func (a *app) fill() {
	p := a.cur()
	if !p.plays() { // covers, the grid asking for more as it scrolls; only 精选's first plays
		for i, s := range p.list {
			if i == 0 && p == a.picks {
				a.prepare(s)
			} else {
				a.release(s)
			}
		}
		if len(p.list) == 0 {
			a.more(p)
		}
		a.sync()
		return
	}
	ready := false
	if p.idx < len(p.list) {
		s := p.list[p.idx]
		a.prepare(s)
		ready = s.vp != nil || s.err != nil
	}
	for i, s := range p.list {
		switch {
		case i < p.idx-behind || i > p.idx+ahead:
			a.release(s)
		case ready:
			a.prepare(s)
		}
	}
	if len(p.list)-p.idx <= ahead+2 {
		a.more(p)
	}
	a.sync()
}

// open shows p over the current page, whose players it frees: only one page
// plays, and memory stays that of one page.
func (a *app) open(p *page) {
	for _, s := range a.cur().list {
		a.release(s)
	}
	a.pages = append(a.pages, p)
	a.sheet = nil
	a.paused = false
	a.fill()
}

// root shows r, the picks or the feed, with nothing over it.
func (a *app) root(r *page) {
	for _, p := range a.pages {
		for _, s := range p.list {
			a.release(s)
		}
	}
	a.pages, a.sheet, a.paused = []*page{r}, nil, false
	a.fill()
}

// play plays p's video i, from its covers.
func (a *app) play(p *page, i int) {
	p.idx, p.playing, a.paused, a.sheet = i, true, false, nil
	a.fill()
}

// loadAccount fetches the account logged in and all it follows, for the sidebar.
// shortcut: loads every account followed at once; page it on scroll if lists of thousands matter.
func (a *app) loadAccount() {
	go func() {
		me, err := a.c.Self()
		var us []dy.User
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
			a.me, a.follows = me, us
			if me.ID != "" && a.mine == nil {
				a.mine = map[string]*page{
					"作品":   {title: "作品", next: a.c.MyPosts(me.ID), grid: true},
					"喜欢":   {title: "喜欢", next: a.c.Liked(me.ID), grid: true},
					"收藏":   {title: "收藏", next: a.c.Collected(), grid: true},
					"观看历史": {title: "观看历史", next: a.c.History(), grid: true},
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
	a.pages = a.pages[:len(a.pages)-1]
	a.sheet = nil
	a.paused = false
	a.fill()
}

func (a *app) prepare(s *slot) {
	s.gone = false // back in range while still loading: keep what arrives
	if s.loading || s.vp != nil || s.err != nil {
		return
	}
	s.loading = true
	go func() {
		pl, err := a.build(s.v)
		a.post(func() {
			s.loading = false
			if s.gone {
				pl.close()
				return
			}
			s.players, s.err = pl, err
			a.fill() // plays it if current, and starts the ones around it
		})
	}()
}

// save copies s's video, already downloaded to play, to the Downloads folder.
func (a *app) save(s *slot) {
	home, err := os.UserHomeDir()
	if err != nil {
		s.saved = "下载失败：" + err.Error()
		return
	}
	s.saved = "保存中…"
	src := filepath.Join(a.dir, s.v.ID+".mp4")
	go func() {
		dst, err := copyTo(src, filepath.Join(home, "Downloads"), fileName(s.v))
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

func (a *app) build(v dy.Video) (pl players, err error) {
	if v.Live != nil {
		if pl.live, err = media.OpenLive(v.Live.HLS); err != nil {
			return pl, err
		}
		if pl.vp, err = ui.NewVideoPlayer(pl.live); err != nil {
			pl.live.Close()
			return players{}, err
		}
		pl.vp.Pause()
		return pl, nil
	}
	path := filepath.Join(a.dir, v.ID+".mp4")
	if _, err = os.Stat(path); err != nil {
		if err = a.c.Download(v.PlayURL, path); err != nil {
			return
		}
	}
	if pl.file, err = media.Open(path); err != nil {
		return
	}
	vd, err := pl.file.Video()
	if err == nil {
		pl.dec = &paced{VideoDecoder: vd, speed: &a.speedBits}
		pl.vp, err = ui.NewVideoPlayer(pl.dec)
	}
	if err != nil {
		pl.file.Close()
		return players{}, err
	}
	pl.vp.SetLoop(true).Pause()
	if ad, aerr := pl.file.Audio(); aerr == nil {
		pl.ap = audio.NewPlayer(ad)
		pl.ap.SetLoop(true).SetPreservesPitch(true)
		if s := math.Float64frombits(a.speedBits.Load()); s > 0 {
			pl.ap.SetSpeed(s)
		}
	}
	return
}

// release frees a slot's players and its cached file.
func (a *app) release(s *slot) {
	s.gone = s.loading
	if s.vp == nil {
		return
	}
	s.players.close()
	os.Remove(filepath.Join(a.dir, s.v.ID+".mp4"))
	s.players = players{}
	s.err = nil
}

// sync plays the current video and holds the others; 精选's covers play
// their first video, silent.
func (a *app) sync() {
	p := a.cur()
	for i, s := range p.list {
		switch {
		case s.vp == nil:
		case !p.plays() && i == 0:
			s.play(false)
		case i == p.idx && !a.paused && p.plays():
			s.play(true)
		default:
			s.pause()
		}
	}
}

// play plays pl's picture, and its sound if sound, else none of it.
func (pl players) play(sound bool) {
	pl.vp.Play()
	if pl.live != nil {
		pl.live.SetMuted(!sound)
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
	p.idx, a.paused = i, false
	a.sheet = nil // the comments were the last video's
	a.fill()
}

// watch follows where s plays, at, and reports whether it has looped back
// to its start with 连播 on: the next video is to play instead.
func (a *app) watch(s *slot, at time.Duration) bool {
	looped := at+time.Second < s.lastAt
	s.lastAt = at
	return looped && a.autoNext
}

func (a *app) toggle() {
	a.paused = !a.paused
	a.sync()
}

// search opens the videos found for q.
func (a *app) search(q string) {
	if q = strings.TrimSpace(q); q != "" {
		a.open(&page{title: "搜索：" + q, next: a.c.Search(q), grid: true})
	}
}

// openAuthor opens a user's videos, unless p is that page already.
func (a *app) openAuthor(p *page, name, id string) {
	if title := "@" + name; p.title != title && id != "" {
		a.open(&page{title: title, next: a.c.Posts(id), grid: true})
	}
}

// selfCheck (DY_SHOT=dir) walks through what the last change touched, as
// clicks would, saves a shot of each step and quits: now liking and saving
// the video DY_WRITE names, each checked on Douyin and taken back.
func (a *app) selfCheck(dir string) {
	shot := func(name string) {
		time.Sleep(12 * time.Second)
		b, err := a.win.CapturePage()
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, name), b, 0o644)
		}
		done := make(chan string)
		a.post(func() {
			state := ""
			for _, p := range a.pages {
				state += fmt.Sprintf(" | %q idx=%d n=%d playing=%v scrollY=%.0f", p.title, p.idx, len(p.list), p.plays(), p.scroll.Y)
				if a.sheet != nil && p == a.cur() {
					state += fmt.Sprintf(" comments=%d done=%v msg=%q", len(a.sheet.items), a.sheet.done, a.sheet.msg)
				}
				for i, s := range p.list {
					if s.vp != nil || s.loading || s.err != nil {
						state += fmt.Sprintf(" %d:vp=%v,loading=%v,err=%v", i, s.vp != nil, s.loading, s.err)
					}
				}
			}
			done <- state
		})
		log.Println("shot", name, err, <-done)
	}
	id := os.Getenv("DY_WRITE")
	v, err := a.c.Video(id)
	if err != nil || v.Liked || v.Saved {
		log.Printf("DY_WRITE %q: %v liked %v saved %v", id, err, v.Liked, v.Saved)
		os.Exit(1)
	}
	a.post(func() { a.open(&page{title: "测试", list: []*slot{{v: v}}, done: true}) })
	time.Sleep(3 * time.Second)
	step := func(name string, do func(*slot), busy func(*slot) bool, want func(dy.Video) bool) {
		start := time.Now()
		a.post(func() { do(a.cur().list[0]) })
		for {
			time.Sleep(200 * time.Millisecond)
			ch := make(chan bool)
			a.post(func() { ch <- busy(a.cur().list[0]) })
			if !<-ch {
				break
			}
		}
		took := time.Since(start)
		ok := false
		for range 10 {
			if v, err := a.c.Video(id); err == nil && want(v) {
				ok = true
				break
			}
			time.Sleep(time.Second)
		}
		log.Printf("WRITE %s: took %v, Douyin agrees %v", name, took.Round(100*time.Millisecond), ok)
	}
	liking := func(s *slot) bool { return s.liking }
	saving := func(s *slot) bool { return s.saving }
	step("like", a.like, liking, func(v dy.Video) bool { return v.Liked })
	step("save", a.collect, saving, func(v dy.Video) bool { return v.Saved })
	shot("1.png")
	step("unlike", a.like, liking, func(v dy.Video) bool { return !v.Liked })
	step("unsave", a.collect, saving, func(v dy.Video) bool { return !v.Saved })
	shot("2.png")
	os.Exit(0)
}

// Run opens the window and blocks until it closes.
func Run() error {
	c, err := dy.New()
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	a := &app{c: c, dir: filepath.Join(cache, "Douyin", "videos"),
		feed: &page{title: "推荐", next: c.Feed}, tabPages: map[int]*page{}}
	a.picks = &page{title: "精选", next: c.Picks(0), grid: true}
	a.tabPages[0] = a.picks
	a.follow = &page{title: "关注", next: c.FollowFeed(), grid: true}
	a.friends = &page{title: "朋友", next: c.FriendFeed(), grid: true}
	a.live = &page{title: "直播", next: c.LiveRooms(), grid: true}
	a.drama = &page{title: "短剧", next: c.Picks(300214), grid: true} // 精选's 小剧场: Douyin's own 短剧 page comes rendered, with no list to read
	a.pages = []*page{a.picks}
	a.img = newImages(c.Image, a.post)
	a.setLiked, a.setSaved = c.SetLiked, c.SetSaved
	a.sendShare = func(id, name string) error { return c.ShareTo(id, name, false) }
	a.setSpeed(1)
	os.RemoveAll(a.dir) // leftovers of a crashed run
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "抖音",
			Width:     1280,
			Height:    820,
			MinWidth:  960,
			MinHeight: 620,
			StateKey:  "main",
			// As MySpotify's: no title bar, the window buttons inset within the sidebar.
			TitleBarStyle:        mygo.TitleBarHidden,
			TitleBarHeight:       topBarHeight,
			TrafficLightPosition: &mygo.Point{X: 18, Y: 18},
			BackgroundColor:      bgHex, // dark always, as Douyin's
			Content:              ui.View(a.view),
		})
		a.fill()
		a.loadAccount()
		go func() {
			if n, err := c.Unread(); err == nil {
				a.post(func() { a.unread = n })
			}
			if hot, err := c.HotSearch(); err == nil {
				a.post(func() { a.hot = hot })
			}
		}()
		if dir := os.Getenv("DY_SHOT"); dir != "" {
			go a.selfCheck(dir)
		}
	})
	log.Println("douyin: running")
	return mygo.App.Run()
}
