package app

import (
	"log"
	"os"
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
	case a.web.warmIdle():
		a.sendWatchedNow()
	case len(a.watched) == 1:
		a.sendWatchedLater()
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
// shortcut: those waiting when the app quits are not told (the page goes
// with the window); keep them on disk if that loses too many.
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
			case len(a.watched) >= watchBatch:
				a.sendWatchedNow()
			case len(a.watched) > 0:
				a.sendWatchedLater()
			}
		})
	}()
}
