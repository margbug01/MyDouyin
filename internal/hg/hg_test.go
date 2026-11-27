package hg

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beilunyang/openhongguo/adapters/filestore"
	"github.com/beilunyang/openhongguo/adapters/httptransport"
	"github.com/beilunyang/openhongguo/sdk"
	"github.com/beilunyang/openhongguo/sdk/model"
)

func TestNewNoNetworkAndProfilePaths(t *testing.T) {
	dir := t.TempDir()

	memClient, err := New(Config{})
	if err != nil {
		t.Fatalf("New(mem) error = %v", err)
	}
	if memClient.profilePath != "" {
		t.Fatalf("mem profilePath = %q, want empty", memClient.profilePath)
	}

	builtinClient, err := New(Config{ProfileDir: dir})
	if err != nil {
		t.Fatalf("New(builtin) error = %v", err)
	}
	wantBuiltin := filepath.Join(dir, "hongguo-device.json")
	if builtinClient.profilePath != wantBuiltin {
		t.Fatalf("builtin profilePath = %q, want %q", builtinClient.profilePath, wantBuiltin)
	}

	remoteClient, err := New(Config{
		ProfileDir:  dir,
		SignerURL:   "https://signer.example.test",
		SignerToken: "secret",
	})
	if err != nil {
		t.Fatalf("New(remote) error = %v", err)
	}
	wantRemote := filepath.Join(dir, "hongguo-device-remote.json")
	if remoteClient.profilePath != wantBuiltin {
		t.Fatalf("remoteClient local profilePath = %q, want %q", remoteClient.profilePath, wantBuiltin)
	}
	if remoteClient.remoteProfilePath != wantRemote {
		t.Fatalf("remoteClient remoteProfilePath = %q, want %q", remoteClient.remoteProfilePath, wantRemote)
	}
	if remoteClient.remoteSDK == nil {
		t.Fatal("expected remoteSDK fallback client to be configured")
	}

	// New must not touch the filesystem or make network calls.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("New created unexpected files: %v", entries)
	}

	// Reconfigure rejects invalid signer URLs without clobbering existing config.
	if err := builtinClient.Reconfigure(Config{ProfileDir: dir, SignerURL: "not-a-url"}); err == nil {
		t.Fatal("Reconfigure with invalid SignerURL succeeded, want error")
	}
	if builtinClient.profilePath != wantBuiltin || builtinClient.remoteProfilePath != "" {
		t.Fatalf("profile paths mutated after failed Reconfigure: local=%q remote=%q", builtinClient.profilePath, builtinClient.remoteProfilePath)
	}

	// Reconfigure enables remote fallback on a separate profile file.
	if err := builtinClient.Reconfigure(Config{ProfileDir: dir, SignerURL: "https://signer.example.test"}); err != nil {
		t.Fatalf("Reconfigure(remote) error = %v", err)
	}
	if builtinClient.profilePath != wantBuiltin || builtinClient.remoteProfilePath != wantRemote {
		t.Fatalf("profile paths after Reconfigure = (%q, %q), want (%q, %q)", builtinClient.profilePath, builtinClient.remoteProfilePath, wantBuiltin, wantRemote)
	}
}

func TestResolvePlaybackFallsBackToRemoteSigner(t *testing.T) {
	dir := t.TempDir()
	var signerCalls atomic.Int32
	var apiLocalHits atomic.Int32
	var apiRemoteHits atomic.Int32

	signerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/feature-hash" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Signer-Token") != "tok-123" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		signerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"headers":{"x-medusa":"remote-medusa-ok","x-gorgon":"rg","x-helios":"rh","x-khronos":"1795580000"}}`))
	}))
	defer signerSrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "device_register"):
			_, _ = w.Write([]byte(`{"device_id":7100000000000001,"install_id":7200000000000001,"device_id_str":"7100000000000001","install_id_str":"7200000000000001"}`))
		case strings.Contains(r.URL.Path, "video_model"):
			if r.Header.Get("X-Medusa") == "remote-medusa-ok" {
				apiRemoteHits.Add(1)
				_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"video_model":"{\"status\":10,\"video_id\":\"v1\",\"media_type\":\"video\",\"video_duration\":60,\"url_expire\":1999999999,\"video_list\":[{\"main_url\":\"https://cdn.example/remote-720.mp4\",\"video_meta\":{\"definition\":\"720p\",\"quality\":\"normal\",\"codec_type\":\"h264\",\"vwidth\":720,\"vheight\":1280,\"bitrate\":1000000,\"size\":1000000}}]}"}}`))
				return
			}
			apiLocalHits.Add(1)
			// Built-in signer gets empty 200 (ErrAnonymousLimited).
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiSrv.Close()

	tr := newDirectTransport()
	c, err := New(Config{ProfileDir: dir, SignerURL: signerSrv.URL, SignerToken: "tok-123", Direct: true})
	if err != nil {
		t.Fatal(err)
	}
	c.sdk, err = sdk.NewClient(sdk.Options{
		Transport:      tr,
		ProfileStore:   filestore.New(filepath.Join(dir, defaultProfileFile)),
		APIOrigin:      apiSrv.URL,
		RegisterOrigin: apiSrv.URL,
		DeviceResets:   sdk.Int(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	c.remoteSDK, err = sdk.NewClient(sdk.Options{
		Transport:      tr,
		ProfileStore:   filestore.New(filepath.Join(dir, remoteProfileFile)),
		APIOrigin:      apiSrv.URL,
		RegisterOrigin: apiSrv.URL,
		SignerURL:      signerSrv.URL,
		SignerToken:    "tok-123",
		DeviceResets:   sdk.Int(0),
	})
	if err != nil {
		t.Fatal(err)
	}

	pb, err := c.ResolvePlayback(context.Background(), "7100000000000201")
	if err != nil {
		t.Fatalf("ResolvePlayback with remote fallback failed: %v", err)
	}
	if len(pb.Streams) != 1 || pb.Streams[0].URL != "https://cdn.example/remote-720.mp4" {
		t.Fatalf("unexpected playback streams: %+v", pb.Streams)
	}
	if apiLocalHits.Load() == 0 || apiRemoteHits.Load() == 0 || signerCalls.Load() == 0 {
		t.Fatalf("expected built-in attempt then remote signer fallback: local=%d remote=%d signer=%d",
			apiLocalHits.Load(), apiRemoteHits.Load(), signerCalls.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, defaultProfileFile)); err != nil {
		t.Fatalf("expected local profile file to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, remoteProfileFile)); err != nil {
		t.Fatalf("expected separate remote profile file to exist: %v", err)
	}
}

func TestDirectTransportBypassesProxyAndMatchesContract(t *testing.T) {
	var proxyHits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via-proxy"))
	}))
	defer proxySrv.Close()

	// Set HTTP_PROXY before creating transports so ProxyFromEnvironment sees it.
	t.Setenv("HTTP_PROXY", proxySrv.URL)
	t.Setenv("http_proxy", proxySrv.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	// Non-direct transport respects HTTP_PROXY for non-loopback destinations.
	nonDirect := httptransport.New()
	proxyResp, err := nonDirect.Do(context.Background(), &sdk.Request{
		Method: http.MethodGet,
		URL:    "http://hg-proxy-check.invalid/probe",
	})
	if err != nil {
		t.Fatalf("non-direct transport through HTTP_PROXY failed: %v", err)
	}
	if proxyResp.StatusCode != http.StatusOK || string(proxyResp.Body) != "via-proxy" {
		t.Fatalf("non-direct proxy response = (%d, %q), want (200, via-proxy)", proxyResp.StatusCode, string(proxyResp.Body))
	}
	if proxyHits.Load() != 1 {
		t.Fatalf("proxyHits after non-direct = %d, want 1", proxyHits.Load())
	}

	// Direct transport must have Proxy == nil and must not send requests to HTTP_PROXY.
	direct := newDirectTransport().(*rawHTTPTransport)
	if direct.client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("direct transport Proxy is non-nil, want nil")
	}

	// Verify direct transport contract against a local target server:
	// raw gzip bytes preserved, HTTP error returned as Response, cookies extracted.
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	_, _ = gz.Write([]byte(`{"ok":true}`))
	_ = gz.Close()
	rawGzip := gzBuf.Bytes()

	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gzip":
			http.SetCookie(w, &http.Cookie{Name: "ttreq", Value: "ticket-123"})
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("X-Test", "yes")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(rawGzip)
		case "/error":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("server boom"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer targetSrv.Close()

	resp, err := direct.Do(context.Background(), &sdk.Request{
		Method: http.MethodGet,
		URL:    targetSrv.URL + "/gzip",
		Headers: map[string]string{
			"Cookie": "passport_csrf_token=; passport_csrf_token_default=; store-region=cn; store-region-src=did; install_id=1; ttreq=abc",
		},
	})
	if err != nil {
		t.Fatalf("direct.Do(/gzip) error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !bytes.Equal(resp.Body, rawGzip) {
		t.Fatalf("gzip body was altered: got %d bytes, want %d raw gzip bytes", len(resp.Body), len(rawGzip))
	}
	if !strings.EqualFold(resp.Headers["Content-Encoding"], "gzip") {
		t.Fatalf("Content-Encoding header = %q, want gzip", resp.Headers["Content-Encoding"])
	}
	if resp.Cookies["ttreq"] != "ticket-123" {
		t.Fatalf("Cookies[ttreq] = %q, want ticket-123", resp.Cookies["ttreq"])
	}
	if _, leaked := resp.Headers["Set-Cookie"]; leaked {
		t.Fatal("Set-Cookie leaked into Response.Headers")
	}

	errResp, err := direct.Do(context.Background(), &sdk.Request{
		Method: http.MethodGet,
		URL:    targetSrv.URL + "/error",
	})
	if err != nil {
		t.Fatalf("direct.Do(/error) returned error %v, want Response with status 500", err)
	}
	if errResp.StatusCode != http.StatusInternalServerError || string(errResp.Body) != "server boom" {
		t.Fatalf("error response = (%d, %q), want (500, server boom)", errResp.StatusCode, string(errResp.Body))
	}

	// Refuses credential headers just like httptransport.
	_, err = direct.Do(context.Background(), &sdk.Request{
		Method:  http.MethodGet,
		URL:     targetSrv.URL + "/gzip",
		Headers: map[string]string{"Authorization": "Bearer secret"},
	})
	if !errors.Is(err, sdk.ErrAuthenticationRequired) {
		t.Fatalf("Authorization header err = %v, want ErrAuthenticationRequired", err)
	}

	// Attempting a non-loopback host on direct transport must not hit proxySrv.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _ = direct.Do(ctx, &sdk.Request{
		Method: http.MethodGet,
		URL:    "http://hg-proxy-check.invalid/probe",
	})
	if proxyHits.Load() != 1 {
		t.Fatalf("proxyHits after direct = %d, want 1 (direct must bypass HTTP_PROXY)", proxyHits.Load())
	}
}

func TestSelectStreamAndPlaybackHelpers(t *testing.T) {
	pb := Playback{
		EpisodeID: "1001",
		ExpiresAt: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC),
		Streams: []Stream{
			{
				Definition: "720p",
				Codec:      "bytevc1",
				Height:     720,
				Bitrate:    1200000,
				URL:        "https://cdn.example/720-hevc.mp4",
				BackupURL:  "https://bak.example/720-hevc.mp4",
			},
			{
				Definition: "720p",
				Codec:      "h264",
				Height:     720,
				Bitrate:    1100000,
				URL:        "https://cdn.example/720-h264.mp4",
				BackupURL:  "https://cdn.example/720-h264.mp4", // duplicate of primary
			},
			{
				Definition: "1080p",
				Codec:      "bytevc1",
				Height:     1080,
				Bitrate:    2400000,
				URL:        "https://cdn.example/1080-hevc.mp4",
				BackupURL:  "https://bak.example/1080-hevc.mp4",
			},
			{
				Definition: "1080p",
				Codec:      "avc1",
				Height:     1080,
				Bitrate:    2100000,
				URL:        "https://cdn.example/1080-h264.mp4",
				BackupURL:  "https://bak.example/1080-h264.mp4",
			},
			{
				Definition: "540p",
				Codec:      "h264",
				Height:     540,
				Bitrate:    700000,
				URL:        "https://cdn.example/540-h264.mp4",
			},
		},
	}

	// Explicit quality + prefer H.264 picks 720p h264 and deduplicates primary+backup.
	chosen, candidates := SelectStream(pb, "720P", true)
	if chosen.Definition != "720p" || chosen.Codec != "h264" {
		t.Fatalf("chosen(720P, h264) = (%s, %s), want (720p, h264)", chosen.Definition, chosen.Codec)
	}
	if len(candidates) == 0 || candidates[0] != "https://cdn.example/720-h264.mp4" {
		t.Fatalf("candidates[0] = %v, want 720-h264 primary first", candidates)
	}
	seen := map[string]bool{}
	for _, u := range candidates {
		if seen[u] {
			t.Fatalf("duplicate candidate URL %q in %v", u, candidates)
		}
		seen[u] = true
	}

	// Auto ("智能") + prefer H.264 picks 1080p avc1 with primary then backup first.
	chosen, candidates = SelectStream(pb, "智能", true)
	if chosen.Definition != "1080p" || chosen.Codec != "avc1" {
		t.Fatalf("chosen(智能, h264) = (%s, %s), want (1080p, avc1)", chosen.Definition, chosen.Codec)
	}
	wantPrefix := []string{"https://cdn.example/1080-h264.mp4", "https://bak.example/1080-h264.mp4"}
	if !reflect.DeepEqual(candidates[:2], wantPrefix) {
		t.Fatalf("candidates[:2] = %v, want %v", candidates[:2], wantPrefix)
	}

	// Auto + preferH264=false picks highest bitrate 1080p (bytevc1).
	chosen, _ = SelectStream(pb, "", false)
	if chosen.Definition != "1080p" || chosen.Codec != "bytevc1" {
		t.Fatalf("chosen(auto, any) = (%s, %s), want (1080p, bytevc1)", chosen.Definition, chosen.Codec)
	}

	// Falls back cleanly when only HEVC streams exist and preferH264=true.
	hevcOnly := Playback{
		Streams: []Stream{
			{Definition: "540p", Codec: "bytevc1", Height: 540, URL: "https://cdn.example/540.mp4"},
			{Definition: "1080p", Codec: "hevc", Height: 1080, URL: "https://cdn.example/1080.mp4", BackupURL: "https://bak.example/1080.mp4"},
		},
	}
	chosen, candidates = SelectStream(hevcOnly, "智能", true)
	if chosen.Definition != "1080p" || chosen.Codec != "hevc" {
		t.Fatalf("hevcOnly chosen = (%s, %s), want (1080p, hevc)", chosen.Definition, chosen.Codec)
	}
	wantCand := []string{"https://cdn.example/1080.mp4", "https://bak.example/1080.mp4", "https://cdn.example/540.mp4"}
	if !reflect.DeepEqual(candidates, wantCand) {
		t.Fatalf("hevcOnly candidates = %v, want %v", candidates, wantCand)
	}

	// IsExpired respects 30-second safety margin.
	if IsExpired(pb, pb.ExpiresAt.Add(-31*time.Second)) {
		t.Fatal("IsExpired returned true 31s before expiry")
	}
	if !IsExpired(pb, pb.ExpiresAt.Add(-29*time.Second)) {
		t.Fatal("IsExpired returned false 29s before expiry")
	}
	if IsExpired(Playback{}, time.Now()) {
		t.Fatal("IsExpired returned true for zero ExpiresAt")
	}

	// DecodeKeyHex validates 16-byte AES keys.
	key, err := DecodeKeyHex("00112233445566778899aabbccddeeff")
	if err != nil || len(key) != 16 {
		t.Fatalf("DecodeKeyHex valid = (%d bytes, %v), want (16, nil)", len(key), err)
	}
	if _, err := DecodeKeyHex("001122"); err == nil {
		t.Fatal("DecodeKeyHex short key succeeded, want error")
	}
	if _, err := DecodeKeyHex("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"); err == nil {
		t.Fatal("DecodeKeyHex invalid hex succeeded, want error")
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name            string
		err             error
		wantKind        ErrorKind
		wantRetry       bool
		wantReset       bool
		wantOpenSetting bool
	}{
		{
			name:     "nil",
			err:      nil,
			wantKind: ErrKindNone,
		},
		{
			name:            "anonymous limited",
			err:             fmt.Errorf("wrapped: %w", sdk.ErrAnonymousLimited),
			wantKind:        ErrKindAnonymousLimited,
			wantRetry:       true,
			wantReset:       true,
			wantOpenSetting: true,
		},
		{
			name:      "device rejected",
			err:       sdk.ErrDeviceRejected,
			wantKind:  ErrKindDeviceRejected,
			wantRetry: true,
			wantReset: true,
		},
		{
			name:            "signer unavailable",
			err:             fmt.Errorf("%w: %v", sdk.ErrSignerUnavailable, context.DeadlineExceeded),
			wantKind:        ErrKindSignerUnavailable,
			wantRetry:       true,
			wantOpenSetting: true,
		},
		{
			name:            "signature rejected",
			err:             sdk.ErrSignatureRejected,
			wantKind:        ErrKindSignerUnavailable,
			wantOpenSetting: true,
		},
		{
			name:      "rate limited",
			err:       sdk.ErrRateLimited,
			wantKind:  ErrKindRateLimited,
			wantRetry: true,
		},
		{
			name:     "content unavailable",
			err:      sdk.ErrContentUnavailable,
			wantKind: ErrKindContentUnavailable,
		},
		{
			name:            "transport error",
			err:             sdk.ErrTransport,
			wantKind:        ErrKindNetwork,
			wantRetry:       true,
			wantOpenSetting: true,
		},
		{
			name:            "deadline exceeded",
			err:             context.DeadlineExceeded,
			wantKind:        ErrKindNetwork,
			wantRetry:       true,
			wantOpenSetting: true,
		},
		{
			name:     "other",
			err:      errors.New("unexpected boom"),
			wantKind: ErrKindOther,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := ClassifyError(tc.err)
			if info.Kind != tc.wantKind {
				t.Fatalf("Kind = %v, want %v", info.Kind, tc.wantKind)
			}
			if info.Retryable != tc.wantRetry {
				t.Fatalf("Retryable = %v, want %v", info.Retryable, tc.wantRetry)
			}
			if info.CanResetDevice != tc.wantReset {
				t.Fatalf("CanResetDevice = %v, want %v", info.CanResetDevice, tc.wantReset)
			}
			if info.CanOpenSettings != tc.wantOpenSetting {
				t.Fatalf("CanOpenSettings = %v, want %v", info.CanOpenSettings, tc.wantOpenSetting)
			}
			if tc.err != nil && info.Message == "" {
				t.Fatal("Message is empty for non-nil error")
			}
		})
	}
}

func TestHistoryStorePersistenceFavoritesAndPruning(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "hg", "history.json")
	store, err := NewHistoryStore(storePath)
	if err != nil {
		t.Fatalf("NewHistoryStore error = %v", err)
	}

	baseTime := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	step := 0
	store.now = func() time.Time {
		step++
		return baseTime.Add(time.Duration(step) * time.Second)
	}

	s1 := Series{ID: "s1", Title: "逆袭短剧", CoverURL: "https://img/1.jpg", EpisodeCount: 80}
	ep1 := Episode{ID: "e1", Index: 1, Title: "第1集"}
	ep2 := Episode{ID: "e2", Index: 2, Title: "第2集"}

	if err := store.RecordProgress(s1, ep1, 45*time.Second); err != nil {
		t.Fatalf("RecordProgress(ep1) error = %v", err)
	}
	if err := store.RecordProgress(s1, ep2, 90*time.Second); err != nil {
		t.Fatalf("RecordProgress(ep2) error = %v", err)
	}

	got, ok := store.Get("s1")
	if !ok {
		t.Fatal("Get(s1) not found")
	}
	if got.EpisodeID != "e2" || got.EpisodeIndex != 2 || got.Position != 90*time.Second {
		t.Fatalf("Get(s1) = (%s, %d, %v), want (e2, 2, 90s)", got.EpisodeID, got.EpisodeIndex, got.Position)
	}
	if got.Positions["e1"] != 45*time.Second || got.Positions["e2"] != 90*time.Second {
		t.Fatalf("Positions = %v, want e1=45s e2=90s", got.Positions)
	}

	if err := store.ClearEpisodePosition("s1", "e2"); err != nil {
		t.Fatalf("ClearEpisodePosition error = %v", err)
	}
	got, _ = store.Get("s1")
	if got.Position != 0 {
		t.Fatalf("Position after clear = %v, want 0", got.Position)
	}
	if _, exists := got.Positions["e2"]; exists {
		t.Fatal("e2 still present in Positions after clear")
	}

	fav, err := store.ToggleFavorite(s1)
	if err != nil || !fav || !store.IsFavorited("s1") {
		t.Fatalf("ToggleFavorite(s1) = (%v, %v), IsFavorited = %v", fav, err, store.IsFavorited("s1"))
	}

	// Reload from disk and verify persistence.
	reloaded, err := NewHistoryStore(storePath)
	if err != nil {
		t.Fatalf("reload NewHistoryStore error = %v", err)
	}
	if !reloaded.IsFavorited("s1") {
		t.Fatal("favorite flag lost across reload")
	}
	reloadedEntry, ok := reloaded.Get("s1")
	if !ok || reloadedEntry.EpisodeID != "e2" || reloadedEntry.Positions["e1"] != 45*time.Second {
		t.Fatalf("reloaded entry = (%+v, %v)", reloadedEntry, ok)
	}

	// RemoveHistory on a favorited series clears watch progress but keeps favorite.
	if err := reloaded.RemoveHistory("s1"); err != nil {
		t.Fatalf("RemoveHistory(s1) error = %v", err)
	}
	if !reloaded.IsFavorited("s1") {
		t.Fatal("RemoveHistory removed favorite flag")
	}
	if len(reloaded.Recent()) != 0 {
		t.Fatalf("Recent after RemoveHistory = %v, want empty", reloaded.Recent())
	}
	if len(reloaded.Favorites()) != 1 {
		t.Fatalf("Favorites after RemoveHistory = %d, want 1", len(reloaded.Favorites()))
	}

	// Add > 200 non-favorited history entries and verify favorites survive pruning.
	reloaded.now = func() time.Time {
		step++
		return baseTime.Add(time.Duration(step) * time.Second)
	}
	for i := 1; i <= maxNonFavoriteHistory+15; i++ {
		id := fmt.Sprintf("hist-%03d", i)
		err := reloaded.RecordProgress(
			Series{ID: id, Title: id},
			Episode{ID: id + "-ep1", Index: 1},
			10*time.Second,
		)
		if err != nil {
			t.Fatalf("RecordProgress(%s) error = %v", id, err)
		}
	}

	if !reloaded.IsFavorited("s1") {
		t.Fatal("favorite s1 was pruned when history grew past limit")
	}
	recent := reloaded.Recent()
	if len(recent) != maxNonFavoriteHistory {
		t.Fatalf("len(Recent) = %d, want %d", len(recent), maxNonFavoriteHistory)
	}
	if recent[0].SeriesID != fmt.Sprintf("hist-%03d", maxNonFavoriteHistory+15) {
		t.Fatalf("newest recent = %q, want newest entry", recent[0].SeriesID)
	}
	if _, ok := reloaded.Get("hist-001"); ok {
		t.Fatal("oldest non-favorite entry hist-001 was not pruned")
	}
}

func TestUpcomingSeriesAndSeasonsBoundary(t *testing.T) {
	// Zero-episode series detail maps to ReleaseUpcoming and hides "更新至0集"/"全0集".
	upUpdating := mapSeriesDetail(&model.Series{
		SeriesSummary: model.SeriesSummary{ID: "s-up1", Title: "新季预告", EpisodeCount: 0},
		Status:        model.SeriesUpdating,
		RightText:     "更新至0集",
	})
	if !upUpdating.IsUpcoming() || upUpdating.Finished || upUpdating.DisplayStatus() != "暂未上线" {
		t.Fatalf("upcoming updating detail = %+v, want ReleaseUpcoming/Finished=false/暂未上线", upUpdating)
	}

	upEnd := mapSeriesDetail(&model.Series{
		SeriesSummary: model.SeriesSummary{ID: "s-up2", Title: "预告新剧", EpisodeCount: 0},
		Status:        model.SeriesEnd,
		RightText:     "全0集",
	})
	if !upEnd.IsUpcoming() || upEnd.Finished || upEnd.DisplayStatus() != "暂未上线" {
		t.Fatalf("upcoming end detail = %+v, want ReleaseUpcoming/Finished=false/暂未上线", upEnd)
	}

	released := mapSeriesDetail(&model.Series{
		SeriesSummary: model.SeriesSummary{ID: "s-rel", Title: "已播剧", EpisodeCount: 80},
		Status:        model.SeriesEnd,
		RightText:     "全80集",
	})
	if released.IsUpcoming() || !released.Finished || released.ReleaseStatus != ReleaseFinished || released.DisplayStatus() != "全80集" {
		t.Fatalf("released detail = %+v, want ReleaseFinished/Finished=true/全80集", released)
	}

	nrPage := mapNewReleasePage(&model.NewReleasePage{
		Date: "20261204",
		Items: []model.NewRelease{
			{SeriesID: "nr-up", Title: "待播新剧", Online: false, Note: "预计12月4日上线"},
			{SeriesID: "nr-on", Title: "已播新剧", Online: true, EpisodeCount: 60},
		},
	}, "20261204")
	if len(nrPage.Items) != 2 {
		t.Fatalf("len(nrPage.Items) = %d, want 2", len(nrPage.Items))
	}
	if !nrPage.Items[0].IsUpcoming() || nrPage.Items[0].DisplayStatus() != "预计12月4日上线" {
		t.Fatalf("nr upcoming = %+v", nrPage.Items[0])
	}
	if nrPage.Items[1].IsUpcoming() || nrPage.Items[1].ReleaseStatus != ReleaseReleased {
		t.Fatalf("nr online = %+v", nrPage.Items[1])
	}

	// Verify Client.ListEpisodes and Client.Seasons against an httptest API server:
	// - 0-episode series returns nil, nil (not ErrAnonymousLimited)
	// - >0-episode series with empty video_list still returns ErrAnonymousLimited
	// - Seasons marks 0-episode seasons as ReleaseUpcoming ("待上线")
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "device_register"):
			http.SetCookie(w, &http.Cookie{Name: "ttreq", Value: "ticket-ok"})
			_, _ = w.Write([]byte(`{"device_id":7100000000000001,"install_id":7200000000000001,"device_id_str":"7100000000000001","install_id_str":"7200000000000001"}`))
		case strings.Contains(r.URL.Path, "video_detail"):
			bodyBytes, _ := io.ReadAll(r.Body)
			body := string(bodyBytes)
			switch {
			case strings.Contains(body, "7100000000000044"):
				_, _ = w.Write([]byte(`{"code":0,"data":{"video_data":{"series_id_str":"7100000000000044","series_title":"第四季","episode_cnt":0,"episode_right_text":"更新至0集","series_status":0,"is_multi_season":true,"season_index":4,"video_list":[]}}}`))
			case strings.Contains(body, "7100000000000099"):
				_, _ = w.Write([]byte(`{"code":0,"data":{"video_data":{"series_id_str":"7100000000000099","series_title":"受限剧","episode_cnt":60,"episode_right_text":"全60集","series_status":1,"video_list":[]}}}`))
			default:
				_, _ = w.Write([]byte(`{"code":0,"data":{"video_data":{"series_id_str":"7100000000000033","series_title":"第三季","episode_cnt":100,"episode_right_text":"全100集","series_status":1,"is_multi_season":true,"season_index":3,"video_list":[{"vid":"7100000000001001","title":"第1集","vid_index":1}]}}}`))
			}
		case strings.Contains(r.URL.Path, "/reading/bookapi/plan/"):
			_, _ = w.Write([]byte(`{"code":0,"data":[{"show_type":547,"video_data":[{"series_id":"7100000000000033","title":"第三季","episode_cnt":100,"season_index":3},{"series_id":"7100000000000044","title":"第四季","episode_cnt":0,"season_index":4}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiSrv.Close()

	c, err := New(Config{Direct: true})
	if err != nil {
		t.Fatal(err)
	}
	c.sdk, err = sdk.NewClient(sdk.Options{
		Transport:      newDirectTransport(),
		ProfileStore:   filestore.New(filepath.Join(t.TempDir(), defaultProfileFile)),
		APIOrigin:      apiSrv.URL,
		RegisterOrigin: apiSrv.URL,
		DeviceResets:   sdk.Int(0),
	})
	if err != nil {
		t.Fatal(err)
	}

	eps, err := c.ListEpisodes(context.Background(), "7100000000000044")
	if err != nil || len(eps) != 0 {
		t.Fatalf("ListEpisodes(upcoming 0-ep) = (%v, %v), want (nil, nil)", eps, err)
	}

	if _, err := c.ListEpisodes(context.Background(), "7100000000000099"); !errors.Is(err, sdk.ErrAnonymousLimited) {
		t.Fatalf("ListEpisodes(60-ep empty video_list) err = %v, want ErrAnonymousLimited", err)
	}

	seasons, err := c.Seasons(context.Background(), "7100000000000033")
	if err != nil || len(seasons) != 2 {
		t.Fatalf("Seasons() = (%+v, %v), want 2 seasons", seasons, err)
	}
	if seasons[0].IsUpcoming() || !seasons[0].Current || seasons[0].ReleaseStatus != ReleaseFinished {
		t.Fatalf("season[0] = %+v, want current finished", seasons[0])
	}
	if !seasons[1].IsUpcoming() || seasons[1].Current || seasons[1].DisplayStatus() != "待上线" {
		t.Fatalf("season[1] = %+v, want upcoming 待上线", seasons[1])
	}
}
