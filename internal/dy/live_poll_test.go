package dy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"douyin/internal/sign"
)

type livePollTransport func(*http.Request) (*http.Response, error)

func (f livePollTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestLivePollContinuesAfterEmptyBatch(t *testing.T) {
	gift := pb(nil).str(1, "WebcastGiftMessage").bytes(2, pb(nil).uint(6, 3).bytes(15, pb(nil).str(16, "小心心"))).uint(3, 17)
	var calls int
	var uniqueID string
	c := &Client{signer: sign.New(ua), http: &http.Client{Transport: livePollTransport(func(req *http.Request) (*http.Response, error) {
		q := req.URL.Query()
		calls++
		if req.URL.Host != "live.douyin.com" || q.Get("room_id") != "12" || q.Get("a_bogus") == "" {
			t.Fatal("missing host, room or signature")
		}
		var body pb
		switch calls {
		case 1:
			uniqueID = q.Get("user_unique_id")
			if uniqueID == "" || q.Get("cursor") != "" {
				t.Fatal("invalid initial cursor or viewer ID")
			}
			body = body.str(2, "next").str(5, "context").uint(3, 1000)
		case 2:
			if q.Get("cursor") != "next" || q.Get("internal_ext") != "context" || q.Get("user_unique_id") != uniqueID {
				t.Fatal("poll did not carry forward the response cursor and viewer ID")
			}
			body = body.bytes(1, gift).str(2, "last").uint(3, 1200)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	poll := c.LivePoll(Live{RoomID: "12", WebRID: "34"})
	if m, wait, err := poll(context.Background()); err != nil || len(m) != 0 || wait != time.Second {
		t.Fatalf("first poll: %v, %v, %v", m, wait, err)
	}
	if m, wait, err := poll(context.Background()); err != nil || len(m) != 1 || m[0].Text != "小心心" || m[0].GiftCount != 3 || wait != 1200*time.Millisecond {
		t.Fatalf("second poll: %v, %v, %v", m, wait, err)
	}
}

func TestLiveH264SkipsHEVCAndAudioOnly(t *testing.T) {
	data := `{"data":{
		"hd":{"main":{"hls":"https://example.test/hevc.m3u8","sdk_params":"{\"VCodec\":\"h265\"}"}},
		"ao":{"main":{"hls":"https://example.test/audio.m3u8","sdk_params":"{\"VCodec\":\"h264\"}"}},
		"md":{"main":{"hls":"http://example.test/compatible.m3u8","sdk_params":"{\"VCodec\":\"h264\"}"}}
	}}`
	if got := liveH264(data); got != "https://example.test/compatible.m3u8" {
		t.Fatalf("selected %q", got)
	}
}
