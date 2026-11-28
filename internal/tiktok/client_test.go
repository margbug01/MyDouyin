package tiktok

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"douyin/internal/dy"
)

type stubWeb struct {
	cookies []dy.Cookie
	reply   func(js string) ([]byte, error)
}

func (s *stubWeb) Eval(_ context.Context, js string) ([]byte, error) {
	return s.reply(js)
}

func (s *stubWeb) Load(_ context.Context, _ string, _ bool) error { return nil }

func (s *stubWeb) Cookies() ([]dy.Cookie, error) { return s.cookies, nil }

func TestParseItemsSelectsH264AndQualities(t *testing.T) {
	raw := `{
		"statusCode": 0,
		"itemList": [{
			"id": "741234567890",
			"desc": "hello tiktok #fyp",
			"createTime": "1730000000",
			"digged": true,
			"author": {
				"id": "1001",
				"uniqueId": "alice_tt",
				"nickname": "Alice",
				"secUid": "MS4wLjABAAAA_alice",
				"avatarThumb": "https://p16.tiktokcdn.com/avatar.jpg",
				"relation": 1
			},
			"video": {
				"width": 1080,
				"height": 1920,
				"duration": 18,
				"cover": "https://p16.tiktokcdn.com/cover.jpg",
				"originCover": "https://p16.tiktokcdn.com/origin.jpg",
				"playAddr": "https://v16.tiktokcdn.com/fallback.mp4",
				"bitrateInfo": [
					{
						"GearName": "normal_1080_0",
						"Bitrate": 1800000,
						"CodecType": "h265_hvc1",
						"PlayAddr": {
							"DataSize": "1200000",
							"Width": 1080,
							"Height": 1920,
							"UrlList": ["https://v16.tiktokcdn.com/1080_hevc.mp4"]
						}
					},
					{
						"GearName": "normal_1080_0",
						"Bitrate": 2100000,
						"CodecType": "h264",
						"PlayAddr": {
							"DataSize": 1800000,
							"Width": 1080,
							"Height": 1920,
							"UrlList": ["https://v16.tiktokcdn.com/1080_h264.mp4"]
						}
					},
					{
						"GearName": "normal_720_0",
						"Bitrate": 900000,
						"CodecType": "h264",
						"PlayAddr": {
							"DataSize": 950000,
							"Width": 720,
							"Height": 1280,
							"UrlList": ["https://v16.tiktokcdn.com/720_h264.mp4"]
						}
					}
				]
			},
			"statsV2": {
				"diggCount": "12345",
				"commentCount": "67",
				"collectCount": "890",
				"shareCount": "45"
			}
		}]
	}`
	var resp struct {
		ItemList []ttItem `json:"itemList"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	vs := parseItems(resp.ItemList)
	if len(vs) != 1 {
		t.Fatalf("got %d videos, want 1", len(vs))
	}
	v := vs[0]
	if v.ID != "741234567890" || v.Author != "Alice" || v.AuthorID != "MS4wLjABAAAA_alice" {
		t.Fatalf("unexpected video meta: %+v", v)
	}
	// Default smart pick prefers smallest H.264 stream (720P).
	if v.PlayURL != "https://v16.tiktokcdn.com/720_h264.mp4" {
		t.Fatalf("PlayURL = %q, want 720_h264.mp4", v.PlayURL)
	}
	if len(v.Qualities) != 2 || v.Qualities[0].Name != "1080P" {
		t.Fatalf("unexpected qualities: %+v", v.Qualities)
	}
	if v.Likes != 12345 || v.Comments != 67 || v.Collects != 890 || v.Shares != 45 {
		t.Fatalf("unexpected stats: likes=%d comments=%d collects=%d shares=%d", v.Likes, v.Comments, v.Collects, v.Shares)
	}
	if !v.Liked {
		t.Fatalf("expected Liked=true")
	}
}

func TestFeedViaWebSyncsChainTokenAndDetectsVerify(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Logout() })

	payload := `{"statusCode":0,"itemList":[{"id":"1","desc":"clip","author":{"nickname":"Bob","secUid":"MS4w_bob"},"video":{"playAddr":"https://v16.tiktokcdn.com/a.mp4","duration":10}}]}`
	webReply, _ := json.Marshal(map[string]any{"status": 200, "text": payload})
	c.SetWeb(&stubWeb{
		cookies: []dy.Cookie{{Name: "tt_chain_token", Value: "chain123"}},
		reply: func(js string) ([]byte, error) {
			if !strings.Contains(js, "/api/recommend/item_list/") {
				t.Errorf("unexpected fetch js: %s", js)
			}
			return webReply, nil
		},
	})

	vs, err := c.Feed()
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(vs) != 1 || vs[0].PlayURL != "https://v16.tiktokcdn.com/a.mp4" {
		t.Fatalf("unexpected feed videos: %+v", vs)
	}
	hdrs := c.MediaHeaders(vs[0])
	if hdrs["Referer"] != BaseURL+"/" {
		t.Fatalf("Referer = %q", hdrs["Referer"])
	}
	if !strings.Contains(hdrs["Cookie"], "tt_chain_token=chain123") {
		t.Fatalf("expected tt_chain_token in MediaHeaders Cookie, got %q", hdrs["Cookie"])
	}

	// Verify statusCode 10000 triggers ErrVerify.
	verifyReply, _ := json.Marshal(map[string]any{"status": 200, "text": `{"statusCode":10000,"statusMsg":"verify"}`})
	c.SetWeb(&stubWeb{
		reply: func(string) ([]byte, error) { return verifyReply, nil },
	})
	if _, err := c.Feed(); !errors.Is(err, ErrVerify) {
		t.Fatalf("expected ErrVerify, got %v", err)
	}
}

func TestLoggedInAndProxy(t *testing.T) {
	if LoggedIn([]dy.Cookie{{Name: "ttwid", Value: "abc"}}) {
		t.Fatal("anonymous ttwid should not count as LoggedIn")
	}
	if !LoggedIn([]dy.Cookie{{Name: "sessionid_ss", Value: "sess_ok"}}) {
		t.Fatal("sessionid_ss should count as LoggedIn")
	}
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.SetProxy("socks5://127.0.0.1:1080"); err != nil {
		t.Fatalf("SetProxy socks5: %v", err)
	}
	if err := c.SetProxy("bad://host"); err == nil {
		t.Fatal("expected error for invalid proxy scheme")
	}
}

func TestFeedHandlesHTMLAndFallsBackToExplore(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Logout() })

	explorePayload := `{"statusCode":0,"itemList":[{"id":"99","desc":"explore clip","author":{"nickname":"Eve","secUid":"MS4w_eve"},"video":{"playAddr":"https://v16.tiktokcdn.com/e.mp4","duration":12}}]}`
	htmlReply, _ := json.Marshal(map[string]any{"status": 200, "contentType": "text/html", "text": "<!DOCTYPE html><html><head><title>TikTok</title></head><body>blocked</body></html>"})
	okReply, _ := json.Marshal(map[string]any{"status": 200, "contentType": "application/json", "text": explorePayload})

	c.SetWeb(&stubWeb{
		reply: func(js string) ([]byte, error) {
			if strings.Contains(js, "/api/recommend/item_list/") {
				return htmlReply, nil
			}
			if strings.Contains(js, "/api/explore/item_list/") {
				return okReply, nil
			}
			return htmlReply, nil
		},
	})

	vs, err := c.Feed()
	if err != nil {
		t.Fatalf("Feed fallback failed: %v", err)
	}
	if len(vs) != 1 || vs[0].ID != "99" {
		t.Fatalf("unexpected fallback videos: %+v", vs)
	}

	// When both endpoints return HTML, return a readable error rather than JSON syntax error.
	c.SetWeb(&stubWeb{reply: func(string) ([]byte, error) { return htmlReply, nil }})
	_, err = c.Feed()
	if err == nil || strings.Contains(err.Error(), "invalid character '<'") {
		t.Fatalf("expected friendly HTML diagnostic error, got %v", err)
	}
}

func TestRegionParametersPreservedInWebCall(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.SetRegion("TW", "zh-Hant")

	var seenJS string
	okReply, _ := json.Marshal(map[string]any{
		"status": 200,
		"text":   `{"statusCode":0,"itemList":[{"id":"7","desc":"tw","author":{"nickname":"A","secUid":"MS4w_a"},"video":{"playAddr":"https://v16.tiktokcdn.com/tw.mp4","duration":8}}]}`,
	})
	c.SetWeb(&stubWeb{
		reply: func(js string) ([]byte, error) {
			seenJS = js
			return okReply, nil
		},
	})
	if _, err := c.Feed(); err != nil {
		t.Fatalf("Feed: %v", err)
	}
	for _, want := range []string{
		"region=TW",
		"priority_region=TW",
		"app_language=zh-Hant",
		"browser_language=zh-TW",
		"tz_name=Asia%2FTaipei",
	} {
		if !strings.Contains(seenJS, want) {
			t.Fatalf("expected %q in web JS, got: %s", want, seenJS)
		}
	}
	if strings.Contains(seenJS, "encodeURIComponent(pageRegion)") || strings.Contains(seenJS, "encodeURIComponent(pageLang)") {
		t.Fatalf("callViaWeb should not clobber user region/language with pageRegion/pageLang")
	}
}

func TestAccountListsCommentLikeAndNeedLogin(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Logout() })

	var paths []string
	c.SetWeb(&stubWeb{
		reply: func(js string) ([]byte, error) {
			switch {
			case strings.Contains(js, "/passport/web/account/info/"):
				paths = append(paths, "self")
				return json.Marshal(map[string]any{
					"status": 200,
					"text":   `{"message":"success","data":{"user_id_str":"123","sec_user_id":"MS4w_me","username":"me_tt","screen_name":"Me","avatar_url":"https://p16.tiktokcdn.com/me.jpg"}}`,
				})
			case strings.Contains(js, "/api/favorite/item_list/"):
				paths = append(paths, "liked")
				return json.Marshal(map[string]any{
					"status": 200,
					"text":   `{"statusCode":0,"hasMore":false,"cursor":"0","itemList":[{"id":"11","desc":"liked clip","author":{"nickname":"A","secUid":"MS4w_a"},"video":{"playAddr":"https://v16.tiktokcdn.com/11.mp4","duration":5}}]}`,
				})
			case strings.Contains(js, "/api/user/collect/item_list/"):
				paths = append(paths, "collected")
				return json.Marshal(map[string]any{
					"status": 200,
					"text":   `{"statusCode":0,"hasMore":false,"cursor":"0","itemList":[{"id":"22","desc":"saved clip","author":{"nickname":"B","secUid":"MS4w_b"},"video":{"playAddr":"https://v16.tiktokcdn.com/22.mp4","duration":6}}]}`,
				})
			case strings.Contains(js, "/api/comment/digg/"):
				paths = append(paths, "comment_digg")
				return json.Marshal(map[string]any{
					"status": 200,
					"text":   `{"status_code":0}`,
				})
			case strings.Contains(js, "/api/commit/item/digg/"):
				return json.Marshal(map[string]any{
					"status": 200,
					"text":   `{"status_code":8,"status_msg":"user not login"}`,
				})
			default:
				return json.Marshal(map[string]any{"status": 200, "text": `{"statusCode":0}`})
			}
		},
	})

	me, err := c.Self()
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	if me.ID != "MS4w_me" || me.Handle != "me_tt" || me.Name != "Me" {
		t.Fatalf("unexpected Self user: %+v", me)
	}

	liked, err := c.Liked(me.ID)()
	if err != nil || len(liked) != 1 || liked[0].ID != "11" {
		t.Fatalf("Liked: vs=%+v err=%v", liked, err)
	}

	collected, err := c.Collected()()
	if err != nil || len(collected) != 1 || collected[0].ID != "22" {
		t.Fatalf("Collected: vs=%+v err=%v", collected, err)
	}

	if err := c.SetCommentLiked("11", "c99", false, true); err != nil {
		t.Fatalf("SetCommentLiked: %v", err)
	}

	if err := c.SetLiked("11", true); !errors.Is(err, ErrNeedLogin) {
		t.Fatalf("expected ErrNeedLogin on status 8, got %v", err)
	}

	if len(paths) != 4 {
		t.Fatalf("unexpected web call sequence: %v", paths)
	}
}

func TestParseImportedCookiesFormats(t *testing.T) {
	// 1. Cookie-Editor JSON array (ignores non-tiktok domain).
	jsonInput := `[
		{"name":"sessionid","value":"sess_json","domain":".tiktok.com","path":"/","expirationDate":1900000000.5,"httpOnly":true,"secure":true},
		{"name":"tt_chain_token","value":"chain_json","domain":".tiktok.com"},
		{"name":"other","value":"skip","domain":".google.com"}
	]`
	cs, err := ParseImportedCookies(jsonInput)
	if err != nil {
		t.Fatalf("JSON import failed: %v", err)
	}
	if len(cs) != 2 || !LoggedIn(cs) {
		t.Fatalf("unexpected JSON cookies: %+v", cs)
	}

	// 2. Chrome DevTools "Copy as cURL" (multiline bash).
	curlInput := "curl 'https://www.tiktok.com/api/recommend/item_list/' \\\n  -H 'accept: */*' \\\n  -b 'ttwid=tw1; sessionid_ss=sess_curl; msToken=ms1'"
	cs, err = ParseImportedCookies(curlInput)
	if err != nil {
		t.Fatalf("cURL import failed: %v", err)
	}
	if len(cs) != 3 || !LoggedIn(cs) {
		t.Fatalf("unexpected cURL cookies: %+v", cs)
	}

	// 3. Raw Cookie header string.
	cs, err = ParseImportedCookies("Cookie: sid_tt=sid_raw; ttwid=tw_raw")
	if err != nil {
		t.Fatalf("raw header import failed: %v", err)
	}
	if len(cs) != 2 || !LoggedIn(cs) {
		t.Fatalf("unexpected header cookies: %+v", cs)
	}

	// 4. Netscape cookies.txt.
	netscape := "# Netscape HTTP Cookie File\n#HttpOnly_.tiktok.com\tTRUE\t/\tTRUE\t1900000000\tsessionid\tsess_net\n.google.com\tTRUE\t/\tTRUE\t1900000000\tNID\tskip\n"
	cs, err = ParseImportedCookies(netscape)
	if err != nil {
		t.Fatalf("Netscape import failed: %v", err)
	}
	if len(cs) != 1 || !cs[0].HTTPOnly || cs[0].Value != "sess_net" {
		t.Fatalf("unexpected Netscape cookies: %+v", cs)
	}

	// 5. Missing login cookie rejected with clear error.
	if _, err := ParseImportedCookies("ttwid=anon_only; msToken=abc"); err == nil {
		t.Fatal("expected error when no TikTok session cookie is present")
	}
}

func TestWebVTTParseAndTrackSelection(t *testing.T) {
	raw := "\uFEFFWEBVTT\n\nNOTE ignored comment\n\n1\n00:00:01.000 --> 00:00:03.500 align:center\n<c.color>Hello &amp; <b>world</b></c>\n\n00:04.000 --> 00:06.250\nSecond line\nPart two\n"
	cues := ParseWebVTT(raw)
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2: %+v", len(cues), cues)
	}
	if cues[0].Text != "Hello & world" {
		t.Fatalf("cue[0].Text = %q, want %q", cues[0].Text, "Hello & world")
	}
	if got := ActiveSubtitle(cues, 2*time.Second); got != "Hello & world" {
		t.Fatalf("ActiveSubtitle(2s) = %q", got)
	}
	if got := ActiveSubtitle(cues, 3800*time.Millisecond); got != "" {
		t.Fatalf("ActiveSubtitle(3.8s) = %q, want empty", got)
	}
	if got := ActiveSubtitle(cues, 5*time.Second); got != "Second line\nPart two" {
		t.Fatalf("ActiveSubtitle(5s) = %q", got)
	}

	tracks := parseSubtitleTracks([]ttSubtitleInfo{
		{LanguageCodeName: "eng-US", URL: "https://v16.tiktokcdn.com/en.vtt", Format: "webvtt", Source: "ASR"},
		{LanguageCodeName: "zho-Hant-TW", URL: "https://v16.tiktokcdn.com/tw.vtt", Format: "webvtt", Source: "MT"},
		{LanguageCodeName: "jpn-JP", URL: "https://v16.tiktokcdn.com/ja.vtt", Format: "webvtt", Source: "ASR"},
	})
	if len(tracks) != 3 {
		t.Fatalf("got %d tracks, want 3: %+v", len(tracks), tracks)
	}
	if idx := PickSubtitleTrack(tracks, "zh-Hant"); idx != 1 || tracks[idx].Label != "中文 (繁體)" {
		t.Fatalf("PickSubtitleTrack(zh-Hant) = %d (%+v)", idx, tracks)
	}
	if idx := PickSubtitleTrack(tracks, "ja-JP"); idx != 2 || tracks[idx].Lang != "ja" {
		t.Fatalf("PickSubtitleTrack(ja-JP) = %d (%+v)", idx, tracks)
	}
}

func TestParseTikTokLiveRoomsAndMultiQuality(t *testing.T) {
	streamData := `{"data":{"origin":{"main":{"hls":"http://pull.tiktokcdn.com/origin.m3u8","sdk_params":"{\"VCodec\":\"h264\"}"}},"hd":{"main":{"hls":"https://pull.tiktokcdn.com/hd.m3u8","sdk_params":"{\"VCodec\":\"h265\"}"}}}}`
	rm := ttLiveRoom{
		IDStr:     "741999888777",
		Title:     "Late night piano live",
		Status:    2,
		UserCount: 15200,
		Cover:     ttImageURL{URLList: []string{"https://p16.tiktokcdn.com/live_cover.jpg"}},
		Owner: ttLiveOwner{
			IDStr:       "9001",
			SecUID:      "MS4w_host",
			DisplayID:   "piano_host",
			Nickname:    "Piano Host",
			AvatarThumb: ttImageURL{URLList: []string{"https://p16.tiktokcdn.com/host.jpg"}},
		},
		StreamURL: ttLiveStreamURL{
			HLS: "https://pull.tiktokcdn.com/fallback.m3u8",
		},
	}
	rm.StreamURL.SDK.Pull.Data = streamData

	vs := parseTikTokLiveRooms([]ttLiveRoom{rm})
	if len(vs) != 1 || vs[0].Live == nil {
		t.Fatalf("unexpected live rooms: %+v", vs)
	}
	lv := vs[0].Live
	if lv.RoomID != "741999888777" || lv.WebRID != "piano_host" || lv.Viewers != "1.5万" {
		t.Fatalf("unexpected live meta: %+v", lv)
	}
	if lv.HLS != "https://pull.tiktokcdn.com/origin.m3u8" || lv.H264HLS != "https://pull.tiktokcdn.com/origin.m3u8" {
		t.Fatalf("expected HTTPS upgraded origin H264 HLS, got %+v", lv)
	}
	if len(lv.Streams) != 2 || lv.Streams[0].Name != "原画" || lv.Streams[1].Name != "超清" {
		t.Fatalf("unexpected live streams: %+v", lv.Streams)
	}
}

func TestTranslateTextAndCues(t *testing.T) {
	if !IsMostlyChinese("今天天气真好 #vlog") {
		t.Fatal("expected Chinese caption to be detected as mostly Chinese")
	}
	if IsMostlyChinese("Rules absolute, between you and me. #dubbing") {
		t.Fatal("expected English caption not to be detected as Chinese")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" && r.Method == http.MethodPost {
			_ = r.ParseForm()
			q = r.FormValue("q")
		}
		if strings.Contains(q, "\n") {
			_, _ = w.Write([]byte(`[[["第一句\n","First line\n"],["第二句","Second line"]]]`))
			return
		}
		_, _ = w.Write([]byte(`[[["你我之间的绝对规则。","Rules absolute, between you and me."]]]`))
	}))
	defer srv.Close()

	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.translateURL = srv.URL

	got, err := c.TranslateText("Rules absolute, between you and me.", "zh-CN")
	if err != nil || got != "你我之间的绝对规则。" {
		t.Fatalf("TranslateText: got=%q err=%v", got, err)
	}

	cues := []SubtitleCue{
		{Start: 1 * time.Second, End: 2 * time.Second, Text: "First line"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "Second line"},
	}
	translated, err := c.TranslateCues(cues, "zh-CN")
	if err != nil {
		t.Fatalf("TranslateCues: %v", err)
	}
	if len(translated) != 2 || translated[0].Text != "第一句" || translated[1].Text != "第二句" {
		t.Fatalf("unexpected translated cues: %+v", translated)
	}
}

func TestTranslateFallbackOn429AndClients5Batch(t *testing.T) {
	var rateLimitedHits int
	srv429 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rateLimitedHits++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv429.Close()

	srvClients5 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST to clients5, got %s", r.Method)
		}
		_ = r.ParseForm()
		qs := r.Form["q"]
		if len(qs) == 2 {
			_, _ = w.Write([]byte(`[["你好","en"],["世界","en"]]`))
			return
		}
		_, _ = w.Write([]byte(`[["规则是绝对的。","en"]]`))
	}))
	defer srvClients5.Close()

	srvMyMemory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"responseData":{"translatedText":"来自备用翻译"},"quotaFinished":false,"responseStatus":200}`))
	}))
	defer srvMyMemory.Close()

	// 1. Primary returns 429 -> cools down and falls back to clients5 native POST batch.
	c := &Client{
		http: srvClients5.Client(),
		translateChain: []translateProvider{
			{name: "primary-429", url: srv429.URL, kind: providerGoogleSingle},
			{name: "clients5", url: srvClients5.URL, kind: providerClients5},
		},
	}
	cues, err := c.TranslateCues([]SubtitleCue{
		{Start: 0, End: time.Second, Text: "Hello"},
		{Start: time.Second, End: 2 * time.Second, Text: "World"},
	}, "zh-CN")
	if err != nil {
		t.Fatalf("TranslateCues fallback err: %v", err)
	}
	if cues[0].Text != "你好" || cues[1].Text != "世界" {
		t.Fatalf("unexpected clients5 batch result: %+v", cues)
	}
	if rateLimitedHits != 1 {
		t.Fatalf("expected 1 hit on 429 provider, got %d", rateLimitedHits)
	}

	// Second call skips cooled-down 429 provider entirely.
	got, err := c.TranslateText("Rules absolute.", "zh-CN")
	if err != nil || got != "规则是绝对的。" {
		t.Fatalf("TranslateText after cooldown = %q, %v", got, err)
	}
	if rateLimitedHits != 1 {
		t.Fatalf("expected cooled-down 429 provider to be skipped, got %d hits", rateLimitedHits)
	}

	// 2. All Google providers return 429 -> falls back to non-Google MyMemory.
	c2 := &Client{
		http: srvMyMemory.Client(),
		translateChain: []translateProvider{
			{name: "g1", url: srv429.URL, kind: providerClients5},
			{name: "g2", url: srv429.URL, kind: providerGoogleSingle},
			{name: "mymemory", url: srvMyMemory.URL, kind: providerMyMemory},
		},
	}
	got2, err := c2.TranslateText("Fallback test", "zh-CN")
	if err != nil || got2 != "来自备用翻译" {
		t.Fatalf("MyMemory fallback = %q, %v", got2, err)
	}
}

func TestTikTokNativeTranslateEndpointsAndCLAInfo(t *testing.T) {
	tracks := parseAllSubtitleTracks(nil, []ttCLACaptionInfo{
		{
			Language:          "eng-US",
			LanguageCode:      "en",
			URLList:           []string{"https://v16.tiktokcdn.com/en.vtt"},
			CaptionFormat:     "webvtt",
			IsOriginalCaption: true,
			ClaSubtitleID:     "741000111222333444",
		},
		{
			Language:      "cmn-Hans-CN",
			LanguageCode:  "zh-Hans",
			URLList:       []string{"https://v16.tiktokcdn.com/zh.vtt"},
			CaptionFormat: "webvtt",
			ClaSubtitleID: "741000111222333999",
		},
	})
	if len(tracks) != 2 || tracks[0].ID != "741000111222333444" || !tracks[0].Original || tracks[1].Lang != "zh-Hans" {
		t.Fatalf("unexpected CLA tracks: %+v", tracks)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/aweme/v1/translation/description/":
			if r.URL.Query().Get("item_id") != "7106594312292453675" || r.URL.Query().Get("target_lang") != "zh-Hans" {
				t.Errorf("unexpected description translate query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"status_code":0,"translated_content":"你找到了多少只青蛙？"}`))
		case "/tiktok/cla/subtitle_translation/get/v1/":
			if r.URL.Query().Get("item_id") != "7106594312292453675" ||
				r.URL.Query().Get("subtitle_id") != "741000111222333444" ||
				r.URL.Query().Get("target_language") != "zh-Hans" {
				t.Errorf("unexpected subtitle translate query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"status_code":0,"webvtt_subtitle":"WEBVTT\n\n00:00:01.000 --> 00:00:03.000\n原生中文字幕\n"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), baseURL: srv.URL}
	c.region.Store("US")
	c.lang.Store("en")

	desc, err := c.TranslateDescription("7106594312292453675", "How many frogs did you find?", "zh-CN")
	if err != nil || desc != "你找到了多少只青蛙？" {
		t.Fatalf("TranslateDescription = %q, %v", desc, err)
	}

	cues, err := c.TranslateSubtitle("7106594312292453675", "741000111222333444", []SubtitleCue{
		{Start: time.Second, End: 3 * time.Second, Text: "Original English"},
	}, "zh-CN")
	if err != nil || len(cues) != 1 || cues[0].Text != "原生中文字幕" {
		t.Fatalf("TranslateSubtitle = %+v, %v", cues, err)
	}
}
