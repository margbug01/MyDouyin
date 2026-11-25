package dy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"douyin/internal/sign"
)

func TestParseLiveStreamsAndRoom(t *testing.T) {
	raw := []byte(`{
		"id_str": "999",
		"status": 2,
		"title": "测试直播间",
		"owner": {"id_str": "101", "nickname": "主播甲", "sec_uid": "sec-1", "web_rid": "777888"},
		"stats": {"user_count_str": "1.2万"},
		"stream_url": {
			"hls_pull_url": "http://cdn.test/fallback.m3u8",
			"live_core_sdk_data": {
				"pull_data": {
					"options": {
						"default_quality": {"name": "高清", "sdk_key": "sd"},
						"qualities": [
							{"name": "标清", "sdk_key": "ld", "level": 1},
							{"name": "音频", "sdk_key": "ao", "level": 0},
							{"name": "原画", "sdk_key": "origin", "level": 5},
							{"name": "高清", "sdk_key": "sd", "level": 2},
							{"name": "蓝光", "sdk_key": "uhd", "level": 4},
							{"name": "超清", "sdk_key": "hd", "level": 3},
							{"name": "空流", "sdk_key": "empty", "level": 6}
						]
					},
					"stream_data": "{\"data\":{\"origin\":{\"main\":{\"hls\":\"http://cdn.test/origin.m3u8\",\"sdk_params\":\"{\\\"VCodec\\\":\\\"h264\\\"}\"},\"backup\":{\"hls\":\"http://bak.test/origin.m3u8\",\"sdk_params\":\"{\\\"VCodec\\\":\\\"h264\\\"}\"}},\"uhd\":{\"main\":{\"hls\":\"http://cdn.test/uhd.m3u8\",\"sdk_params\":\"{\\\"VCodec\\\":\\\"h265\\\"}\"}},\"hd\":{\"main\":{\"hls\":\"https://cdn.test/hd.m3u8\",\"sdk_params\":\"{\\\"VCodec\\\":\\\"h264\\\"}\"},\"backup\":{\"hls\":\"https://bak.test/hd.m3u8\"}},\"sd\":{\"main\":{\"hls\":\"http://cdn.test/sd.m3u8\",\"sdk_params\":\"{\\\"VCodec\\\":\\\"h264\\\"}\"}},\"ld\":{\"backup\":{\"hls\":\"http://bak.test/ld.m3u8\",\"sdk_params\":\"{\\\"VCodec\\\":\\\"h264\\\"}\"}},\"ao\":{\"main\":{\"hls\":\"http://cdn.test/ao.m3u8\",\"sdk_params\":\"{\\\"VCodec\\\":\\\"h264\\\"}\"}},\"empty\":{\"main\":{\"hls\":\"\"}}}}"
				}
			}
		}
	}`)

	v, ok := room(raw, false)
	if !ok || v.Live == nil {
		t.Fatalf("room() failed: ok=%v live=%v", ok, v.Live)
	}
	wantNames := []string{"原画", "蓝光", "超清", "高清", "标清"}
	if len(v.Live.Streams) != len(wantNames) || len(v.Qualities) != len(wantNames) {
		t.Fatalf("streams=%d qualities=%d, want %d", len(v.Live.Streams), len(v.Qualities), len(wantNames))
	}
	for i, want := range wantNames {
		if v.Live.Streams[i].Name != want || v.Qualities[i].Name != want {
			t.Fatalf("index %d: stream=%q quality=%q, want %q", i, v.Live.Streams[i].Name, v.Qualities[i].Name, want)
		}
	}
	if v.Live.Streams[0].URL != "https://cdn.test/origin.m3u8" || v.Live.Streams[0].BackupURL != "https://bak.test/origin.m3u8" || !v.Live.Streams[0].H264 {
		t.Fatalf("origin stream: %+v", v.Live.Streams[0])
	}
	if v.Live.Streams[1].H264 {
		t.Fatalf("uhd should be non-H264: %+v", v.Live.Streams[1])
	}
	if v.Live.Streams[4].URL != "https://bak.test/ld.m3u8" || v.Live.Streams[4].BackupURL != "" || !v.Live.Streams[4].H264 {
		t.Fatalf("backup-only ld stream: %+v", v.Live.Streams[4])
	}
	if v.Live.DefaultQuality != "高清" {
		t.Fatalf("defaultQuality = %q, want 高清", v.Live.DefaultQuality)
	}
	if v.Live.HLS != "https://cdn.test/sd.m3u8" {
		t.Fatalf("HLS = %q, want default quality URL", v.Live.HLS)
	}
	if v.Live.H264HLS != "https://cdn.test/origin.m3u8" {
		t.Fatalf("H264HLS = %q, want finest H264 origin URL", v.Live.H264HLS)
	}
}

func TestParseLiveStreamsFallbacks(t *testing.T) {
	// Synthesize from hls_pull_url_map with resolution_name when SDK options/data are absent.
	var s rawStreamURL
	s.HLSMap = map[string]string{
		"SD1":      "http://cdn.test/sd1.m3u8",
		"FULL_HD1": "http://cdn.test/fhd.m3u8",
		"HD1":      "http://cdn.test/hd1.m3u8",
	}
	s.ResolutionName = map[string]string{"FULL_HD1": "蓝光", "HD1": "超清"}
	streams, qualities, _, hls, _ := parseLiveStreams(s)
	want := []string{"蓝光", "超清", "标清"}
	if len(streams) != len(want) || len(qualities) != len(want) {
		t.Fatalf("streams=%+v qualities=%+v", streams, qualities)
	}
	for i, w := range want {
		if streams[i].Name != w {
			t.Fatalf("streams[%d]=%q, want %q", i, streams[i].Name, w)
		}
	}
	if hls != "https://cdn.test/hd1.m3u8" {
		t.Fatalf("hls = %q", hls)
	}
}

func TestLiveH264PrefersFinestOverMD(t *testing.T) {
	data := `{"data":{
		"md":{"main":{"hls":"http://cdn.test/md.m3u8","sdk_params":"{\"VCodec\":\"h264\"}"}},
		"uhd":{"main":{"hls":"http://cdn.test/uhd.m3u8","sdk_params":"{\"VCodec\":\"h264\"}"}},
		"origin":{"main":{"hls":"http://cdn.test/origin.m3u8","sdk_params":"{\"VCodec\":\"h264\"}"}}
	}}`
	if got := liveH264(data); got != "https://cdn.test/origin.m3u8" {
		t.Fatalf("liveH264 picked %q, want origin", got)
	}
}

func TestLiveRoomSignedRequestAndEndedDetection(t *testing.T) {
	var body string
	var gotReq *http.Request
	c := &Client{
		signer: sign.New(ua),
		http: &http.Client{Transport: livePollTransport(func(req *http.Request) (*http.Response, error) {
			gotReq = req
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(body))),
				Header:     make(http.Header),
			}, nil
		})},
	}
	c.login.Store(&login{cookie: "ttwid=abc123"})

	body = `{
		"status_code": 0,
		"data": {
			"room_status": 0,
			"data": [{
				"id_str": "555",
				"status": 2,
				"title": "开播中",
				"owner": {"id_str": "88", "nickname": "主播乙", "sec_uid": "sec-2"},
				"stats": {"user_count_str": "320"},
				"stream_url": {
					"live_core_sdk_data": {
						"pull_data": {
							"options": {
								"default_quality": {"name": "超清", "sdk_key": "hd"},
								"qualities": [
									{"name": "原画", "sdk_key": "origin", "level": 5},
									{"name": "超清", "sdk_key": "hd", "level": 3}
								]
							},
							"stream_data": "{\"data\":{\"origin\":{\"main\":{\"hls\":\"http://live.test/origin.m3u8\",\"sdk_params\":\"{\\\"VCodec\\\":\\\"h264\\\"}\"},\"backup\":{\"hls\":\"http://bak.test/origin.m3u8\"}},\"hd\":{\"main\":{\"hls\":\"http://live.test/hd.m3u8\",\"sdk_params\":\"{\\\"VCodec\\\":\\\"h264\\\"}\"}}}}"
						}
					}
				}
			}]
		}
	}`

	v, err := c.LiveRoom(context.Background(), "424242")
	if err != nil {
		t.Fatalf("LiveRoom: %v", err)
	}
	if gotReq == nil || gotReq.URL.Host != "live.douyin.com" || gotReq.URL.Path != "/webcast/room/web/enter/" {
		t.Fatalf("unexpected request URL: %v", gotReq)
	}
	q := gotReq.URL.Query()
	if q.Get("web_rid") != "424242" || q.Get("a_bogus") == "" || gotReq.Header.Get("Referer") != "https://live.douyin.com/424242" || gotReq.Header.Get("Cookie") != "ttwid=abc123" {
		t.Fatalf("request metadata mismatch: query=%v referer=%q cookie=%q", q, gotReq.Header.Get("Referer"), gotReq.Header.Get("Cookie"))
	}
	if v.Live == nil || v.Live.WebRID != "424242" || len(v.Live.Streams) != 2 || len(v.Qualities) != 2 || !v.Live.Streams[0].H264 {
		t.Fatalf("unexpected live video: %+v live=%+v", v, v.Live)
	}

	// Ended via room_status != 0
	body = `{"status_code": 0, "data": {"room_status": 2, "data": []}}`
	v, err = c.LiveRoom(context.Background(), "424242")
	if !errors.Is(err, ErrLiveEnded) || v.Live == nil || !v.Live.Ended {
		t.Fatalf("room_status=2: got v=%+v err=%v", v, err)
	}

	// Ended via room status != 2
	body = `{"status_code": 0, "data": {"room_status": 0, "data": [{"id_str": "555", "status": 4}]}}`
	v, err = c.LiveRoom(context.Background(), "424242")
	if !errors.Is(err, ErrLiveEnded) || v.Live == nil || !v.Live.Ended {
		t.Fatalf("status=4: got v=%+v err=%v", v, err)
	}
}
