package media

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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

// A stream whose server goes quiet without closing (as Media Foundation
// then waits for good, measured on Windows 11, 2026-11-28) ends, so the
// player asks again from where it got to and gets the rest.
func TestStreamProxyEndsAStalledStreamSoThePlayerAsksAgain(t *testing.T) {
	old := upstreamIdle
	upstreamIdle = 200 * time.Millisecond
	t.Cleanup(func() { upstreamIdle = old })

	payload := bytes.Repeat([]byte("0123456789abcdef"), 64<<10) // 1 MiB
	const stallAt = 600 << 10
	var stalled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end := 0, len(payload)-1
		if rg, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes="); ok {
			a, b, _ := strings.Cut(rg, "-")
			start, _ = strconv.Atoi(a)
			if b != "" {
				end, _ = strconv.Atoi(b)
			}
			end = min(end, len(payload)-1)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
			w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
			w.WriteHeader(http.StatusPartialContent)
		}
		if start < stallAt && end >= stallAt && stalled.CompareAndSwap(false, true) {
			w.Write(payload[start:stallAt])
			w.(http.Flusher).Flush()
			<-r.Context().Done() // quiet, the connection open
			return
		}
		w.Write(payload[start : end+1])
	}))
	defer srv.Close()

	pu, release, err := localProxy.register(context.Background(), srv.URL+"/ep.mp4", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	client := &http.Client{Timeout: 5 * time.Second}
	var got []byte
	for asks := 0; len(got) < len(payload); asks++ {
		if asks == 3 {
			t.Fatalf("got %d of %d bytes in 3 asks", len(got), len(payload))
		}
		req, _ := http.NewRequest(http.MethodGet, pu, nil)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", len(got)))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if os.IsTimeout(err) {
			t.Fatalf("the proxy held a stalled stream open (%d bytes in)", len(got)+len(b))
		}
		got = append(got, b...)
	}
	if !stalled.Load() || !bytes.Equal(got, payload) {
		t.Fatalf("stalled=%v, bytes equal=%v", stalled.Load(), bytes.Equal(got, payload))
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

func TestStreamProxyCachesVODHeadAndStreamsTail(t *testing.T) {
	// Payload larger than vodHeadCacheSize so head is served from RAM and tail streams from upstream.
	payload := make([]byte, vodHeadCacheSize+32768)
	for i := range payload {
		payload[i] = byte((i*31 + 7) & 0xff)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.ServeContent(w, r, "clip.mp4", time.Unix(1700000000, 0), strings.NewReader(string(payload)))
	}))
	defer srv.Close()

	pu, release, err := localProxy.register(context.Background(), srv.URL+"/clip.mp4", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if hits.Load() != 1 {
		t.Fatalf("preflight hits = %d, want 1", hits.Load())
	}

	// HEAD and initial Range inside cached head must hit RAM without upstream requests.
	hresp, err := http.Head(pu)
	if err != nil {
		t.Fatal(err)
	}
	hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK || hresp.ContentLength != int64(len(payload)) {
		t.Fatalf("HEAD: status=%d len=%d want=%d", hresp.StatusCode, hresp.ContentLength, len(payload))
	}
	req, _ := http.NewRequest(http.MethodGet, pu, nil)
	req.Header.Set("Range", "bytes=0-4095")
	rresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	headChunk, _ := io.ReadAll(rresp.Body)
	rresp.Body.Close()
	if rresp.StatusCode != http.StatusPartialContent || string(headChunk) != string(payload[:4096]) {
		t.Fatalf("head range mismatch: status=%d len=%d", rresp.StatusCode, len(headChunk))
	}
	if hits.Load() != 1 {
		t.Fatalf("HEAD + cached head range made upstream requests: hits=%d, want 1", hits.Load())
	}

	// Full GET spanning cached head + uncached tail must stitch seamlessly.
	fresp, err := http.Get(pu)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := io.ReadAll(fresp.Body)
	fresp.Body.Close()
	if fresp.StatusCode != http.StatusOK || len(full) != len(payload) || string(full) != string(payload) {
		t.Fatalf("full stitched GET mismatch: status=%d len=%d want=%d", fresp.StatusCode, len(full), len(payload))
	}
	if hits.Load() != 2 {
		t.Fatalf("expected exactly 1 extra upstream request for tail, got hits=%d", hits.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A stream with its own Client (TikTok's, through TikTok's proxy) is fetched
// through it alone; one without goes as before, never through it.
func TestStreamProxyRouteClient(t *testing.T) {
	payload := strings.Repeat("x", vodHeadCacheSize+4096) // past the head cache: the tail goes upstream
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "clip.mp4", time.Now(), strings.NewReader(payload))
	}))
	defer srv.Close()
	var own atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		own.Add(1)
		return http.DefaultTransport.RoundTrip(r)
	})}
	read := func(opts StreamOptions) {
		t.Helper()
		pu, release, err := localProxy.registerOptions(context.Background(), srv.URL+"/clip.mp4", opts, true)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		resp, err := http.Get(pu)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if n, _ := io.Copy(io.Discard, resp.Body); n != int64(len(payload)) {
			t.Fatalf("read %d bytes through the loopback, want %d", n, len(payload))
		}
	}
	read(StreamOptions{})
	if own.Load() != 0 {
		t.Fatalf("a stream without a Client went through another stream's: %d requests", own.Load())
	}
	read(StreamOptions{Client: client})
	if own.Load() != 2 { // the preflight head, then the tail
		t.Fatalf("a stream with a Client made %d requests through it, want 2", own.Load())
	}
}
