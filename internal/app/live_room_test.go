package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

func TestLiveRoomCommentsGiftsAndControls(t *testing.T) {
	p := stub("直播", 2)
	s := p.list[0]
	s.v.Live = &dy.Live{WebRID: "123", Viewers: "19"}
	a := withRoots(&app{pages: []*page{p}})
	now := time.Now()
	chat := &liveRoomView{room: s.v.Live, started: now.Add(-2 * time.Second), signedIn: true}
	a.liveChat, a.signedIn = chat, true
	comment := dy.LiveMessage{ID: 10, User: "阿文", Text: "晚上好", Badges: []dy.LiveBadge{{Kind: 1, Label: "荣誉 18"}, {Kind: 7, Label: "粉丝团 5"}}}
	gift := dy.LiveMessage{ID: 11, User: "阿华", Text: "小心心", GiftCount: 3}
	a.addLiveMessages(chat, s, []dy.LiveMessage{comment, gift, comment}, now)
	if len(chat.messages) != 2 || len(s.danmaku) != 1 || s.danmaku[0].Text != comment.Text {
		t.Fatalf("messages %v, danmaku %v", chat.messages, s.danmaku)
	}
	tt := newTester(a)
	if !tt.HasText("荣誉 18 粉丝团 5 阿文：晚上好") || !tt.HasText("阿华 送出 小心心 ×3") {
		t.Fatalf("interaction panel: %v", tt.Texts())
	}
	if err := tt.Click("弹幕"); err != nil || !a.dmOff || len(s.danmaku) != 0 {
		t.Fatalf("disabling danmaku: %v", err)
	}
	a.addLiveMessages(chat, s, []dy.LiveMessage{{ID: 12, Text: "仍然可读"}}, now)
	if err := tt.Click("关闭直播互动"); err != nil || !chat.hidden {
		t.Fatalf("closing panel: %v", err)
	}
	tt.Frame()
	if tt.HasText("阿华 送出") {
		t.Fatal("gift still visible after closing the panel")
	}
	if err := tt.Click("互动"); err != nil || chat.hidden {
		t.Fatalf("reopening panel: %v", err)
	}
	tt.Frame()
	if !tt.HasText("仍然可读") || len(s.danmaku) != 0 {
		t.Fatal("messages should continue when the on-video danmaku is off")
	}
	p.idx = 1
	tt.Frame()
	if a.liveChat != nil || strings.Contains(strings.Join(tt.Texts(), " "), "仍然可读") {
		t.Fatal("previous room's state survived changing videos")
	}
}

func TestLiveCommentSendRetainsDraftAndDeduplicatesEcho(t *testing.T) {
	p := stub("直播", 2)
	s := p.list[0]
	s.v.Live = &dy.Live{WebRID: "123"}
	a := withRoots(&app{pages: []*page{p}, posted: make(chan func(), 1), signedIn: true})
	attempts := make(chan string, 2)
	results := make(chan error, 2)
	chat := &liveRoomView{room: s.v.Live, started: time.Now(), signedIn: true, draft: "晚上好 👋"}
	chat.send = func(text string) (dy.LiveMessage, error) {
		attempts <- text
		return dy.LiveMessage{ID: 42, User: "我", Text: text}, <-results
	}
	a.liveChat = chat
	tt := newTester(a)
	if err := tt.Click("直播评论内容"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyDown)
	if p.idx != 0 {
		t.Fatal("editing the draft changed the video")
	}
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyEnter)
	awaitSend := func() {
		t.Helper()
		select {
		case text := <-attempts:
			if text != "晚上好 👋" {
				t.Fatalf("sent %q", text)
			}
		case <-time.After(time.Second):
			t.Fatal("comment was not submitted")
		}
	}
	finish := func(err error) {
		t.Helper()
		results <- err
		select {
		case f := <-a.posted:
			f()
			tt.Frame()
		case <-time.After(time.Second):
			t.Fatal("send did not finish")
		}
	}
	awaitSend()
	finish(errors.New("本直播间暂时无法发言"))
	if !tt.HasText("本直播间暂时无法发言") || chat.draft != "晚上好 👋" || len(chat.messages) != 0 {
		t.Fatal("the send error should retain the draft and leave the feed alone")
	}
	if err := tt.Click("发送直播评论"); err != nil {
		t.Fatal(err)
	}
	awaitSend()
	finish(nil)
	a.addLiveMessages(chat, s, []dy.LiveMessage{{ID: 42, User: "我", Text: "晚上好 👋", Badges: []dy.LiveBadge{{Kind: 1, Label: "荣誉 8"}}}}, time.Now())
	tt.Frame()
	if chat.draft != "" || len(chat.messages) != 1 || !tt.HasText("荣誉 8 我：晚上好 👋") || len(attempts) != 0 {
		t.Fatalf("draft %q, messages %v, extra sends %d", chat.draft, chat.messages, len(attempts))
	}
}

func TestLiveCandidatesOrderAndDedup(t *testing.T) {
	v := dy.Video{
		Live: &dy.Live{
			HLS:            "https://cdn.test/fallback.m3u8",
			H264HLS:        "https://cdn.test/origin.m3u8",
			DefaultQuality: "高清",
			Streams: []dy.LiveStream{
				{Name: "原画", SDKKey: "origin", URL: "https://cdn.test/origin.m3u8", BackupURL: "https://bak.test/origin.m3u8", H264: true, Level: 5},
				{Name: "蓝光", SDKKey: "uhd", URL: "https://cdn.test/uhd.m3u8", H264: false, Level: 4},
				{Name: "超清", SDKKey: "hd", URL: "https://cdn.test/hd.m3u8", BackupURL: "https://bak.test/hd.m3u8", H264: true, Level: 3},
				{Name: "高清", SDKKey: "sd", URL: "https://cdn.test/sd.m3u8", BackupURL: "https://bak.test/sd.m3u8", H264: true, Level: 2},
			},
		},
	}

	got := liveCandidates(v, "超清", true)
	want := []string{
		"https://cdn.test/hd.m3u8",
		"https://bak.test/hd.m3u8",
		"https://cdn.test/sd.m3u8",
		"https://bak.test/sd.m3u8",
		"https://cdn.test/origin.m3u8",
		"https://bak.test/origin.m3u8",
		"https://cdn.test/fallback.m3u8",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("preferH264 selected 超清:\ngot  %v\nwant %v", got, want)
	}

	gotNonNative := liveCandidates(v, "智能", false)
	wantNonNative := []string{
		"https://cdn.test/sd.m3u8",
		"https://bak.test/sd.m3u8",
		"https://cdn.test/origin.m3u8",
		"https://bak.test/origin.m3u8",
		"https://cdn.test/uhd.m3u8",
		"https://cdn.test/hd.m3u8",
		"https://bak.test/hd.m3u8",
		"https://cdn.test/fallback.m3u8",
	}
	if !slices.Equal(gotNonNative, wantNonNative) {
		t.Fatalf("non-native 智能:\ngot  %v\nwant %v", gotNonNative, wantNonNative)
	}

	// When no H.264 stream is advertised, fall back to HLS and non-H.264 streams as last resort.
	hevcOnly := dy.Video{
		Live: &dy.Live{
			HLS: "https://cdn.test/hevc-default.m3u8",
			Streams: []dy.LiveStream{
				{Name: "原画", URL: "https://cdn.test/hevc-origin.m3u8", BackupURL: "https://bak.test/hevc-origin.m3u8", H264: false},
			},
		},
	}
	gotHEVC := liveCandidates(hevcOnly, "智能", true)
	wantHEVC := []string{"https://cdn.test/hevc-default.m3u8", "https://cdn.test/hevc-origin.m3u8", "https://bak.test/hevc-origin.m3u8"}
	if !slices.Equal(gotHEVC, wantHEVC) {
		t.Fatalf("hevc-only fallback:\ngot  %v\nwant %v", gotHEVC, wantHEVC)
	}
}

func TestLiveQualityMenuAndSwitch(t *testing.T) {
	p := stub("直播", 1)
	s := p.list[0]
	s.liveFreshAt = time.Now()
	s.v.Live = &dy.Live{
		RoomID:         "10",
		WebRID:         "12345",
		Viewers:        "88",
		DefaultQuality: "高清",
		Streams: []dy.LiveStream{
			{Name: "原画", URL: "https://cdn.test/origin.m3u8", H264: true},
			{Name: "蓝光", URL: "https://cdn.test/uhd.m3u8", H264: true},
			{Name: "超清", URL: "https://cdn.test/hd.m3u8", H264: true},
			{Name: "高清", URL: "https://cdn.test/sd.m3u8", H264: true},
			{Name: "标清", URL: "https://cdn.test/ld.m3u8", H264: true},
		},
	}
	s.v.Qualities = []dy.Quality{
		{Name: "原画", URL: "https://cdn.test/origin.m3u8"},
		{Name: "蓝光", URL: "https://cdn.test/uhd.m3u8"},
		{Name: "超清", URL: "https://cdn.test/hd.m3u8"},
		{Name: "高清", URL: "https://cdn.test/sd.m3u8"},
		{Name: "标清", URL: "https://cdn.test/ld.m3u8"},
	}
	ready(t, s)
	openedURLs := make(chan string, 4)
	a := withRoots(&app{
		pages:   []*page{p},
		quality: "1080P",
		posted:  make(chan func(), 8),
		openLive: func(ctx context.Context, url string, native bool) (players, error) {
			openedURLs <- url
			return players{}, errors.New("stop after capture")
		},
	})
	tt := newTester(a)
	if err := tt.Click("清晰度：智能"); err != nil {
		t.Fatalf("live quality chip missing: %v (texts=%v)", err, tt.Texts())
	}
	tt.Frame()
	for _, want := range []string{"原画", "蓝光", "超清", "高清", "标清"} {
		if !tt.HasText(want) {
			t.Fatalf("menu missing %q: %v", want, tt.Texts())
		}
	}
	if err := tt.Click("超清"); err != nil {
		t.Fatal(err)
	}
	if a.liveQuality != "超清" || a.quality != "1080P" {
		t.Fatalf("qualities after live switch: live=%q vod=%q", a.liveQuality, a.quality)
	}
	if s.liveFreshAt.IsZero() {
		t.Fatal("setLiveQuality should retain liveFreshAt")
	}
	select {
	case u := <-openedURLs:
		if u != "https://cdn.test/hd.m3u8" {
			t.Fatalf("reopened with %q, want 超清 URL", u)
		}
	case <-time.After(time.Second):
		t.Fatal("quality switch did not trigger reload")
	}
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(time.Second):
		t.Fatal("reload did not post back")
	}
	// Switching VOD quality must not alter liveQuality or release live slots.
	ready(t, s)
	s.err = nil
	a.setQuality("720P")
	if a.liveQuality != "超清" || s.vp == nil {
		t.Fatalf("VOD setQuality affected live slot: liveQuality=%q vp=%v", a.liveQuality, s.vp)
	}
	// If the current room lacks the chosen liveQuality, the chip shows 智能 while preserving a.liveQuality.
	s.v.Qualities = []dy.Quality{{Name: "高清", URL: "https://cdn.test/sd.m3u8"}}
	tt.Frame()
	if err := tt.Click("清晰度：智能"); err != nil || a.liveQuality != "超清" {
		t.Fatalf("fallback chip: err=%v liveQuality=%q", err, a.liveQuality)
	}
}

func TestLiveRoomRefreshFailoverEndedAndRetry(t *testing.T) {
	p := stub("直播", 1)
	s := p.list[0]
	s.loading = false
	origLive := &dy.Live{RoomID: "1", WebRID: "9001", Viewers: "10", HLS: "https://stale.test/old.m3u8"}
	s.v.Live = origLive
	chat := &liveRoomView{room: origLive, started: time.Now()}

	var fetchCalls int
	var opened []string
	var fetchResult dy.Video
	var fetchErr error

	a := withRoots(&app{
		pages:    []*page{p},
		liveChat: chat,
		posted:   make(chan func(), 8),
		fetchLiveRoom: func(ctx context.Context, webRID string) (dy.Video, error) {
			fetchCalls++
			return fetchResult, fetchErr
		},
		openLive: func(ctx context.Context, url string, native bool) (players, error) {
			opened = append(opened, url)
			if strings.Contains(url, "primary-bad") {
				return players{}, errors.New("edge stalled")
			}
			vp, err := ui.NewVideoPlayer(still{})
			return players{vp: vp}, err
		},
	})
	drain := func() {
		t.Helper()
		select {
		case fn := <-a.posted:
			fn()
		case <-time.After(time.Second):
			t.Fatal("prepare did not finish")
		}
	}

	// 1. Refresh populates fresh H.264 streams in place (preserving *dy.Live pointer) and fails over to backup URL.
	fetchResult = dy.Video{
		Qualities: []dy.Quality{{Name: "原画", URL: "https://primary-bad.test/origin.m3u8"}},
		Live: &dy.Live{
			RoomID:         "1",
			WebRID:         "9001",
			Viewers:        "25",
			DefaultQuality: "原画",
			HLS:            "https://primary-bad.test/origin.m3u8",
			H264HLS:        "https://primary-bad.test/origin.m3u8",
			Streams: []dy.LiveStream{
				{Name: "原画", SDKKey: "origin", URL: "https://primary-bad.test/origin.m3u8", BackupURL: "https://backup-good.test/origin.m3u8", H264: true, Level: 5},
			},
		},
	}
	a.prepare(s)
	drain()
	if fetchCalls != 1 {
		t.Fatalf("fetchCalls = %d, want 1", fetchCalls)
	}
	if s.v.Live != origLive || a.liveChat.room != s.v.Live {
		t.Fatal("refresh replaced *dy.Live pointer instead of updating in place")
	}
	if s.err != nil || s.vp == nil || s.liveFreshAt.IsZero() || len(s.v.Qualities) != 1 || s.v.Live.Viewers != "25" {
		t.Fatalf("after refresh: err=%v vp=%v fresh=%v qualities=%v viewers=%q", s.err, s.vp, s.liveFreshAt, s.v.Qualities, s.v.Live.Viewers)
	}
	wantOpened := []string{"https://primary-bad.test/origin.m3u8", "https://backup-good.test/origin.m3u8"}
	if !slices.Equal(opened, wantOpened) {
		t.Fatalf("failover attempts = %v, want %v", opened, wantOpened)
	}
	s.vp.Close()
	s.vp = nil

	// 2. ErrLiveEnded fails immediately without opening any stream and shows 主播已下播; retry clears liveFreshAt and refetches.
	s.liveFreshAt = time.Time{}
	opened = nil
	fetchErr = dy.ErrLiveEnded
	a.prepare(s)
	drain()
	if !errors.Is(s.err, dy.ErrLiveEnded) || !s.v.Live.Ended || len(opened) != 0 {
		t.Fatalf("ended room: err=%v ended=%v opened=%v", s.err, s.v.Live.Ended, opened)
	}
	tt := newTester(a)
	if !tt.HasText("主播已下播") {
		t.Fatalf("ended message missing: %v", tt.Texts())
	}
	fetchErr = nil
	s.liveFreshAt = time.Now()
	if err := tt.Click("重新加载"); err != nil {
		t.Fatal(err)
	}
	if !s.liveFreshAt.IsZero() {
		t.Fatal("retry button did not clear liveFreshAt")
	}
	drain()
	if fetchCalls != 3 || s.err != nil || s.vp == nil {
		t.Fatalf("after retry: fetchCalls=%d err=%v vp=%v", fetchCalls, s.err, s.vp)
	}
	s.vp.Close()
}

func TestLiveQualitySettingsPersistence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "settings.json")
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	a.loadSettings(file)
	a.setQuality("720P")
	a.setLiveQuality("蓝光")
	a.saveSettings()

	next := &app{}
	next.loadSettings(file)
	if next.quality != "720P" || next.liveQuality != "蓝光" {
		t.Fatalf("persisted settings: quality=%q liveQuality=%q", next.quality, next.liveQuality)
	}
}
