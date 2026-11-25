package media

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamProxyHeadersRangeAndCancel(t *testing.T) {
	payload := strings.Repeat("0123456789abcdef", 64)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://www.douyin.com/" {
			http.Error(w, "missing referer", http.StatusForbidden)
			return
		}
		hits.Add(1)
		http.ServeContent(w, r, "clip.mp4", time.Now(), strings.NewReader(payload))
	}))
	defer srv.Close()

	if _, _, err := localProxy.register(context.Background(), srv.URL+"/clip.mp4", map[string]string{"Referer": "bad\r\nX: 1"}, true); err == nil {
		t.Fatal("accepted CRLF header injection")
	}

	pu, release, err := localProxy.register(context.Background(), srv.URL+"/clip.mp4", map[string]string{"Referer": "https://www.douyin.com/"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(pu, "/stream.mp4") {
		release()
		t.Fatalf("expected .mp4 suffix in proxy URL, got %s", pu)
	}

	req, _ := http.NewRequest(http.MethodGet, pu, nil)
	req.Header.Set("Range", "bytes=16-31")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		release()
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(got) != payload[16:32] {
		release()
		t.Fatalf("range response: status=%d got=%q", resp.StatusCode, string(got))
	}

	// After release, the token must immediately return 404.
	release()
	resp2, err := http.Get(pu)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after release, got %d", resp2.StatusCode)
	}
}

func TestStreamProxyHLSRewriteAndHTTPProxy(t *testing.T) {
	var segReqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://live.douyin.com/" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/live/master.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.key\"\n#EXTINF:2.0,\nseg0.ts\n")
		case "/live/key.key":
			io.WriteString(w, "0123456789abcdef")
		case "/live/seg0.ts":
			segReqs.Add(1)
			io.WriteString(w, "TSDATA")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	pu, release, err := localProxy.register(context.Background(), srv.URL+"/live/master.m3u8", map[string]string{"Referer": "https://live.douyin.com/"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	resp, err := http.Get(pu)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	text := string(body)
	if !strings.Contains(text, "/seg.ts?u=") || !strings.Contains(text, "/seg.key?u=") {
		t.Fatalf("playlist was not rewritten: %s", text)
	}
	// Fetch the rewritten segment URL and verify Referer is injected.
	var segURL string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "http://127.0.0.1:") {
			segURL = strings.TrimSpace(line)
		}
	}
	if segURL == "" {
		t.Fatalf("missing rewritten segment URL in %s", text)
	}
	sresp, err := http.Get(segURL)
	if err != nil {
		t.Fatal(err)
	}
	sdata, _ := io.ReadAll(sresp.Body)
	sresp.Body.Close()
	if string(sdata) != "TSDATA" || segReqs.Load() != 1 {
		t.Fatalf("segment fetch: data=%q reqs=%d", string(sdata), segReqs.Load())
	}

	// Verify HTTP_PROXY routing through environment variables.
	var proxyHits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.Header().Set("Content-Type", "video/mp4")
		io.WriteString(w, "MP4")
	}))
	defer proxySrv.Close()
	t.Setenv("HTTP_PROXY", proxySrv.URL)
	t.Setenv("http_proxy", proxySrv.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	ppu, prelease, err := localProxy.register(context.Background(), "http://mydouyin.test/video.mp4", nil, true)
	if err != nil {
		t.Fatalf("proxied register: %v", err)
	}
	prelease()
	_ = ppu
	if proxyHits.Load() < 1 {
		t.Fatal("proxy server was not used")
	}
}

func TestStreamProxyReleaseAbortsBlockedUpstream(t *testing.T) {
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer blocked.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, rel, err := localProxy.register(ctx, blocked.URL+"/hang.mp4", nil, true)
		if rel != nil {
			rel()
		}
		done <- err
	}()
	time.Sleep(80 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error on cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled register hung")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("cancel took too long: %v", time.Since(start))
	}
}
