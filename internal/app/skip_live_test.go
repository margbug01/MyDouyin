package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"douyin/internal/dy"
)

func testLiveVideo(id, author string) dy.Video {
	return dy.Video{
		ID:     id,
		Desc:   "直播间 " + id,
		Author: author,
		Live:   &dy.Live{RoomID: id, WebRID: "web-" + id, Viewers: "128"},
	}
}

func testNormalVideo(id, desc string) dy.Video {
	return dy.Video{
		ID:       id,
		Desc:     desc,
		Author:   "作者",
		Duration: 30 * time.Second,
	}
}

func waitPosted(t *testing.T, a *app, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		select {
		case fn := <-a.posted:
			fn()
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("timed out waiting for posted updates")
}

// Toggling "跳过直播开关" in 设置 -> 播放 persists to settings.json and reloads
// on next launch; a legacy settings.json without skipLive defaults to false.
func TestSkipLiveSettingsPersistence(t *testing.T) {
	dir := t.TempDir()

	legacyFile := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(legacyFile, []byte(`{"autoNext":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := &app{}
	legacy.loadSettings(legacyFile)
	if legacy.skipLive || legacy.current().SkipLive {
		t.Fatalf("legacy settings without skipLive should default to false, got %v", legacy.skipLive)
	}

	file := filepath.Join(dir, "settings.json")
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	a.loadSettings(file)
	if a.skipLive {
		t.Fatal("default skipLive should be false")
	}

	tt := newTester(a)
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("跳过直播") {
		t.Fatalf("play settings missing 跳过直播: %q", tt.Texts())
	}
	if err := tt.Click("跳过直播开关"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.skipLive {
		t.Fatal("clicking 跳过直播开关 did not enable skipLive")
	}

	b, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(b), `"skipLive": true`) {
		t.Fatalf("settings.json missing skipLive=true: %s, err=%v", b, err)
	}

	reloaded := &app{}
	reloaded.loadSettings(file)
	if !reloaded.skipLive || !reloaded.current().SkipLive {
		t.Fatalf("reloaded skipLive = %v, want true", reloaded.skipLive)
	}
}

// With skipLive enabled, mixed feeds filter out live rooms, auto-fetch past
// all-live batches, stop after maxEmptySkipBatches on an infinite all-live
// source, and leave the dedicated 直播 tab completely unfiltered.
func TestSkipLiveFeedFilteringAndLiveTabUnaffected(t *testing.T) {
	batches := [][]dy.Video{
		{testLiveVideo("live-1", "主播1"), testLiveVideo("live-2", "主播2")},
		{testLiveVideo("live-3", "主播3"), testNormalVideo("v-1", "正常视频1"), testNormalVideo("v-2", "正常视频2")},
		{},
	}
	var calls atomic.Int32
	feed := &page{
		title: "推荐",
		next: func() ([]dy.Video, error) {
			i := int(calls.Add(1)) - 1
			if i >= len(batches) {
				return nil, nil
			}
			return batches[i], nil
		},
	}
	liveTab := &page{
		title:    "直播",
		grid:     true,
		liveOnly: true,
		next: func() ([]dy.Video, error) {
			return []dy.Video{testLiveVideo("room-1", "官方主播")}, nil
		},
	}
	a := withRoots(&app{pages: []*page{feed}, live: liveTab, posted: make(chan func(), 32)})
	a.setSkipLive(true)

	a.more(feed)
	waitPosted(t, a, func() bool { return len(feed.list) == 2 })
	if feed.list[0].v.ID != "v-1" || feed.list[1].v.ID != "v-2" {
		t.Fatalf("filtered feed = [%s, %s], want [v-1, v-2]", feed.list[0].v.ID, feed.list[1].v.ID)
	}
	if calls.Load() < 2 {
		t.Fatalf("expected auto-fetch past all-live first batch, got %d calls", calls.Load())
	}

	// Dedicated 直播 tab keeps live rooms even with skipLive enabled.
	a.more(liveTab)
	waitPosted(t, a, func() bool { return len(liveTab.list) == 1 })
	if liveTab.list[0].v.Live == nil || liveTab.list[0].v.ID != "room-1" {
		t.Fatalf("live tab filtered unexpectedly: %+v", liveTab.list[0].v)
	}

	// Infinite all-live source stops after maxEmptySkipBatches without looping forever.
	var infCalls atomic.Int32
	infFeed := &page{
		title: "推荐",
		next: func() ([]dy.Video, error) {
			n := infCalls.Add(1)
			return []dy.Video{testLiveVideo(fmt.Sprintf("inf-%d", n), "主播")}, nil
		},
	}
	b := withRoots(&app{pages: []*page{infFeed}, posted: make(chan func(), 64)})
	b.setSkipLive(true)
	b.more(infFeed)
	waitPosted(t, b, func() bool { return infCalls.Load() >= maxEmptySkipBatches && !infFeed.fetching })
	// Extra frames/fill calls must not restart infinite fetching.
	b.fill()
	time.Sleep(40 * time.Millisecond)
	for len(b.posted) > 0 {
		(<-b.posted)()
	}
	if got := infCalls.Load(); got != maxEmptySkipBatches {
		t.Fatalf("infinite all-live source fetched %d batches, want %d", got, maxEmptySkipBatches)
	}
	if len(infFeed.list) != 0 {
		t.Fatalf("infinite all-live source appended %d items, want 0", len(infFeed.list))
	}
}

// Clicking "跳过直播" on a live room's bottom bar in a mixed feed immediately
// skips to the next non-live video, closes live chat & comment sheet, shows the
// guidance toast, and never renders on a dedicated liveOnly page.
func TestSkipLiveBarChipAndImmediateSkip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	feed := stub("推荐", 2)
	feed.list[0].v = testLiveVideo("live-cur", "主播A")
	feed.list[1].v = testNormalVideo("v-next", "下一条普通视频")
	ready(t, feed.list[0])
	ready(t, feed.list[1])

	liveTab := stub("直播", 1)
	liveTab.grid = true
	liveTab.playing = true
	liveTab.liveOnly = true
	liveTab.list[0].v = testLiveVideo("live-tab-1", "直播页主播")
	ready(t, liveTab.list[0])

	a := withRoots(&app{
		pages:    []*page{feed},
		live:     liveTab,
		sheet:    &sheet{v: feed.list[0].v},
		liveChat: &liveRoomView{room: feed.list[0].v.Live},
	})
	a.loadSettings(file)

	tt := newTester(a)
	tt.Frame()
	if !tt.HasText("跳过直播") {
		t.Fatalf("mixed-feed live room bar missing 跳过直播 chip: %q", tt.Texts())
	}

	if err := tt.Click("跳过直播"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.skipLive {
		t.Fatal("clicking 跳过直播 did not turn skipLive on")
	}
	if len(feed.list) != 1 || feed.idx != 0 || feed.list[0].v.ID != "v-next" {
		t.Fatalf("after skip: len=%d idx=%d cur=%v", len(feed.list), feed.idx, feed.list)
	}
	if a.liveChat != nil {
		t.Fatal("a.liveChat was not closed after skipping live room")
	}
	if a.sheet != nil {
		t.Fatal("a.sheet was not cleared after skipping live room")
	}
	if !tt.HasText("下一条普通视频") {
		t.Fatalf("next non-live video not shown after skip: %q", tt.Texts())
	}
	if !tt.HasText("已开启跳过直播，可在「设置 → 播放」关闭") {
		t.Fatalf("expected skip-live toast, got texts: %q", tt.Texts())
	}
	b, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(b), `"skipLive": true`) {
		t.Fatalf("clicking 跳过直播 did not persist skipLive: %s, err=%v", b, err)
	}

	// Dedicated live tab (liveOnly=true) must NOT show the "跳过直播" chip,
	// even when skipLive is off.
	a.setSkipLive(false)
	a.pages = []*page{liveTab}
	tt.Frame()
	if _, ok := tt.Find("跳过直播"); ok {
		t.Fatalf("liveOnly page must not show 跳过直播 chip: %q", tt.Texts())
	}
}

// Turning skipLive ON while watching a normal video surrounded by live rooms
// keeps the current video playing uninterrupted and steps straight to the next
// non-live video.
func TestSkipLivePreservesCurrentVideoAndStepsOverPruned(t *testing.T) {
	feed := stub("推荐", 5)
	feed.list[0].v = testNormalVideo("v0", "视频0")
	feed.list[1].v = testLiveVideo("live1", "主播1")
	feed.list[2].v = testNormalVideo("v2", "视频2")
	feed.list[3].v = testLiveVideo("live3", "主播3")
	feed.list[4].v = testNormalVideo("v4", "视频4")
	feed.idx = 2
	ready(t, feed.list[2])
	ready(t, feed.list[4])
	playingVP := feed.list[2].vp

	a := withRoots(&app{pages: []*page{feed}})
	a.fill()
	a.setSkipLive(true)

	if len(feed.list) != 3 || feed.idx != 1 || feed.list[feed.idx].v.ID != "v2" {
		t.Fatalf("after pruning: len=%d idx=%d curID=%s", len(feed.list), feed.idx, feed.list[feed.idx].v.ID)
	}
	if feed.list[feed.idx].vp != playingVP || !feed.list[feed.idx].vp.Playing() {
		t.Fatal("current non-live video was interrupted when enabling skipLive")
	}

	a.lastNav = time.Time{}
	a.step(1)
	if feed.idx != 2 || feed.list[feed.idx].v.ID != "v4" {
		t.Fatalf("step(1) landed on idx=%d id=%s, want v4", feed.idx, feed.list[feed.idx].v.ID)
	}
}

// Turning skipLive ON when the current live room is the only/last item in a
// non-done feed triggers fetching the next page and lands on the first non-live
// video that arrives.
func TestSkipLiveLastItemFetchesNextBatch(t *testing.T) {
	nextBatch := make(chan []dy.Video, 1)
	feed := &page{
		title: "推荐",
		list: []*slot{
			{v: testLiveVideo("only-live", "主播"), loading: true},
		},
		next: func() ([]dy.Video, error) {
			return <-nextBatch, nil
		},
	}
	a := withRoots(&app{pages: []*page{feed}, posted: make(chan func(), 16)})

	a.setSkipLive(true)
	if len(feed.list) != 0 || feed.idx != 0 {
		t.Fatalf("pruned last live room: len=%d idx=%d, want 0/0", len(feed.list), feed.idx)
	}
	if !feed.fetching {
		t.Fatal("pruning the only item in a non-done feed did not start fetching next batch")
	}

	nextBatch <- []dy.Video{
		testLiveVideo("next-live", "主播2"),
		testNormalVideo("arrived-v1", "新拉到的视频"),
	}
	close(nextBatch)
	waitPosted(t, a, func() bool { return len(feed.list) == 1 && feed.done && !feed.fetching })
	if feed.idx != 0 || feed.list[0].v.ID != "arrived-v1" {
		t.Fatalf("after next batch arrived: idx=%d id=%s, want 0/arrived-v1", feed.idx, feed.list[0].v.ID)
	}
}

// A feed whose batches are all live rooms stops fetching after
// maxEmptySkipBatches and says so where 载入中… would otherwise stay forever.
func TestSkipLiveAllLiveBatchesStopWithMessage(t *testing.T) {
	var fetches atomic.Int32
	feed := &page{
		title: "推荐",
		next: func() ([]dy.Video, error) {
			n := fetches.Add(1)
			return []dy.Video{testLiveVideo(fmt.Sprintf("live-%d", n), "主播")}, nil
		},
	}
	a := withRoots(&app{pages: []*page{feed}, posted: make(chan func(), 16)})
	a.skipLive = true
	a.more(feed)
	waitPosted(t, a, func() bool { return feed.msg != "" && !feed.fetching })
	if got := fetches.Load(); got != maxEmptySkipBatches {
		t.Fatalf("fetched %d batches, want %d", got, maxEmptySkipBatches)
	}
	if !strings.Contains(feed.msg, "直播") || len(feed.list) != 0 {
		t.Fatalf("msg=%q len=%d, want a 直播 message and no items", feed.msg, len(feed.list))
	}
	a.more(feed)
	if feed.fetching {
		t.Fatal("more fetched again after the cap without a refresh")
	}
}
