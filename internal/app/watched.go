package app

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"douyin/internal/dy"
)

// Douyin's page tells it of each video as it plays (history/write and
// aweme/stats); the recommend feed leaves out what it knows was watched.
// Those writes go through the app's Douyin page, some 400 MB while open
// (web.go), so they are told in batches, the page open seconds at a time.
const (
	watchedAfter = 2 * time.Second // played this far, a video counts as watched
	watchBatch   = 20              // told once this many are waiting
	watchWait    = 8 * time.Minute // or once the first has waited this long
)

// markWatched queues v to tell Douyin it was watched, while logged in.
func (a *app) markWatched(v dy.Video) {
	if a.sendWatched == nil || !a.signedIn || v.ID == "" {
		return
	}
	a.watched = append(a.watched, v)
	switch {
	case len(a.watched) >= watchBatch:
		a.sendWatchedNow()
	case len(a.watched) == 1:
		a.sendWatchedLater()
	}
}

// freshHistory has 观看历史 load anew once it shows, after what was watched
// here and not yet told is told, so it is there.
func (a *app) freshHistory() {
	h := a.mine["观看历史"]
	a.empty(h)
	if len(a.watched) > 0 || a.watchSending {
		h.fetching, a.historyWait = true, h.gen
		a.sendWatchedNow()
	}
}

// pendingWatched is what waited to be told when the app quit, and the
// account it was watched on.
type pendingWatched struct {
	Account string      `json:"account"`
	Videos  [][2]string `json:"videos"` // ID, the author's numeric ID
}

func (a *app) watchedFile() string {
	return filepath.Join(filepath.Dir(a.settingsFile), "watched.json")
}

// keepWatched writes what waits to be told as the window closes, the page
// that tells it going with the window.
func (a *app) keepWatched() {
	if a.settingsFile == "" || a.me.ID == "" || len(a.watched) == 0 {
		return
	}
	k := pendingWatched{Account: a.me.ID}
	for _, v := range a.watched {
		k.Videos = append(k.Videos, [2]string{v.ID, v.AuthorUID})
	}
	b, _ := json.Marshal(k)
	if err := os.WriteFile(a.watchedFile(), b, 0o600); err != nil {
		log.Printf("douyin: %d videos watched not kept: %v", len(k.Videos), err)
	}
}

// resumeWatched queues what an earlier run kept, the account logged in
// being the one it was watched on; another's is dropped.
func (a *app) resumeWatched(account string) {
	if a.settingsFile == "" {
		return
	}
	b, err := os.ReadFile(a.watchedFile())
	if err != nil {
		return
	}
	os.Remove(a.watchedFile())
	var k pendingWatched
	if json.Unmarshal(b, &k) != nil || k.Account != account {
		return
	}
	for _, v := range k.Videos {
		a.markWatched(dy.Video{ID: v[0], AuthorUID: v[1]})
	}
}

func (a *app) flushWatchedIfWarm() {
	if a.watchSending || len(a.watched) == 0 || !a.signedIn || !a.web.warmIdle() {
		return
	}
	a.sendWatchedNow()
}

// watchedCheck, with DY_WATCHED_CHECK naming a video not among the first
// of 观看历史, tells Douyin it was watched through the page, checks it
// comes into 观看历史, and quits: 0 when it did. It writes to the account,
// so it runs only when asked.
func (a *app) watchedCheck(id string) {
	fail := func(f string, v ...any) { log.Printf("watched check: "+f, v...); os.Exit(1) }
	for in := false; !in; time.Sleep(time.Second) {
		a.call(func() { in = a.signedIn })
	}
	inHistory := func() bool {
		vs, err := a.c.History()()
		if err != nil {
			fail("观看历史: %v", err)
		}
		return slices.ContainsFunc(vs, func(v dy.Video) bool { return v.ID == id })
	}
	v, err := a.c.Video(id)
	if err != nil {
		fail("read: %v", err)
	}
	if v.AuthorUID == "" || inHistory() {
		fail("no author ID (%q), or already in 观看历史: pick another video", v.AuthorUID)
	}
	start := time.Now()
	if err := a.c.Watched(id, v.AuthorUID); err != nil {
		fail("told: %v", err)
	}
	took := time.Since(start)
	for range 15 {
		time.Sleep(time.Second)
		if inHistory() {
			log.Printf("watched check: told in %v, in 观看历史 after %v", took.Round(10*time.Millisecond), time.Since(start).Round(time.Second))
			os.Exit(0)
		}
	}
	fail("told in %v, but not in 观看历史 after 15 s", took.Round(10*time.Millisecond))
}

func (a *app) sendWatchedLater() {
	if a.watchTimer != nil {
		a.watchTimer.Stop()
	}
	a.watchTimer = time.AfterFunc(watchWait, func() { a.post(a.sendWatchedNow) })
}

// sendWatchedNow tells Douyin of the videos waiting, one batch at a time.
func (a *app) sendWatchedNow() {
	if a.watchTimer != nil {
		a.watchTimer.Stop()
		a.watchTimer = nil
	}
	if a.watchSending || len(a.watched) == 0 {
		return
	}
	vs := a.watched
	a.watched, a.watchSending = nil, true
	go func() {
		sent := 0
		var err error
		for _, v := range vs {
			if err = a.sendWatched(v.ID, v.AuthorUID); err != nil {
				break
			}
			sent++
		}
		if err != nil { // dropped, not retried: a broken login would only fail again
			log.Printf("douyin: told %d of %d videos watched: %v", sent, len(vs), err)
		}
		a.post(func() {
			a.watchSending = false
			switch {
			case a.historyWait != 0 && len(a.watched) > 0: // 观看历史 waits for these too
				a.sendWatchedNow()
			case a.historyWait != 0:
				if h := a.mine["观看历史"]; h != nil && h.gen == a.historyWait {
					h.fetching = false
					if c := a.cur(); c == h || a.isHome(c) && a.orbit.source == orbitHistory {
						a.fill()
					}
				}
				a.historyWait = 0
			case len(a.watched) >= watchBatch:
				a.sendWatchedNow()
			case len(a.watched) > 0:
				a.sendWatchedLater()
			}
		})
	}()
}
