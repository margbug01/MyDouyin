package media

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"
)

const maxPlaylistBytes = 4 << 20

var m3u8URIAttr = regexp.MustCompile(`URI="([^"]+)"`)

// StreamOptions configures custom HTTP headers and optional ISO-BMFF CENC
// (AES-128-CTR) streaming decryption for a media stream.
type StreamOptions struct {
	Headers    map[string]string
	DecryptKey []byte // optional 16-byte AES-128-CTR (cenc) key
}

type proxyRoute struct {
	source  string
	headers map[string]string
	plan    *cencPlan
	ctx     context.Context
	cancel  context.CancelFunc

	// Optional one-shot cached playlist from preflight probe so the first
	// Media Foundation request does not fetch the .m3u8 a second time.
	mu         sync.Mutex
	cachedBody []byte
	cachedType string
	cachedAt   time.Time
}

type streamProxy struct {
	once   sync.Once
	addr   string
	err    error
	client *http.Client

	mu     sync.RWMutex
	routes map[string]*proxyRoute
}

var localProxy = &streamProxy{
	routes: make(map[string]*proxyRoute),
	client: &http.Client{
		Transport: &http.Transport{
			Proxy: func(r *http.Request) (*url.URL, error) {
				return httpproxy.FromEnvironment().ProxyFunc()(r.URL)
			},
			DialContext: (&net.Dialer{
				Timeout:   15 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout: 20 * time.Second,
			MaxIdleConns:          16,
			IdleConnTimeout:       60 * time.Second,
		},
	},
}

func validateHTTPHeaders(headers map[string]string) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		if k == "" || strings.ContainsAny(k, "\r\n:") || strings.ContainsAny(v, "\r\n") {
			return nil, errors.New("media: invalid HTTP header")
		}
		out[k] = v
	}
	return out, nil
}

func normalizeLocalSource(source string) (string, bool, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return source, false, nil
	}
	if strings.HasPrefix(source, "file://") {
		raw := strings.ReplaceAll(strings.TrimPrefix(source, "file://"), `\`, "/")
		if len(raw) >= 2 && raw[1] == ':' {
			return filepath.FromSlash(raw), true, nil
		}
		u, err := url.Parse("file://" + raw)
		if err != nil {
			return "", false, err
		}
		p := u.Path
		if runtime.GOOS == "windows" {
			p = strings.TrimPrefix(p, "/")
		}
		if u.Host != "" {
			p = "//" + u.Host + u.Path
		}
		return filepath.FromSlash(p), true, nil
	}
	return source, true, nil
}

func (p *streamProxy) ensureStarted() error {
	p.once.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			p.err = err
			return
		}
		p.addr = ln.Addr().String()
		srv := &http.Server{Handler: p}
		go func() { _ = srv.Serve(ln) }()
	})
	return p.err
}

func newRouteToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func urlExt(rawURL, fallback string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fallback
	}
	ext := strings.ToLower(path.Ext(u.Path))
	switch ext {
	case ".mp4", ".m4v", ".mov", ".m3u8", ".ts", ".m4s", ".aac", ".mp3", ".webm", ".mkv", ".key":
		return ext
	default:
		return fallback
	}
}

func isPlaylistResponse(rawURL, contentType string) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "mpegurl") || strings.Contains(ct, "x-mpegurl") {
		return true
	}
	return urlExt(rawURL, "") == ".m3u8"
}

// register exposes an HTTP(S) media stream through a loopback URL that injects
// the required headers and rewrites HLS playlists without touching disk.
// A lightweight preflight check validates reachability and fails fast on 4xx/5xx.
func (p *streamProxy) register(ctx context.Context, source string, headers map[string]string, vod bool) (string, func(), error) {
	return p.registerOptions(ctx, source, StreamOptions{Headers: headers}, vod)
}

func (p *streamProxy) registerOptions(ctx context.Context, source string, opts StreamOptions, vod bool) (string, func(), error) {
	if len(opts.DecryptKey) != 0 && len(opts.DecryptKey) != 16 {
		return "", nil, errors.New("media: decrypt key must be 16 bytes")
	}
	hdrs, err := validateHTTPHeaders(opts.Headers)
	if err != nil {
		return "", nil, err
	}
	u, err := url.Parse(source)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", nil, errors.New("media: unsupported stream URL")
	}
	if err := p.ensureStarted(); err != nil {
		return "", nil, err
	}
	token, err := newRouteToken()
	if err != nil {
		return "", nil, err
	}

	routeCtx, routeCancel := context.WithCancel(context.Background())
	route := &proxyRoute{
		source:  source,
		headers: hdrs,
		ctx:     routeCtx,
		cancel:  routeCancel,
	}

	fallbackExt := ".mp4"
	if !vod {
		fallbackExt = ".m3u8"
	}
	ext := urlExt(source, fallbackExt)

	if len(opts.DecryptKey) == 16 {
		keyCopy := append([]byte(nil), opts.DecryptKey...)
		probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
		defer probeCancel()
		plan, perr := newCENCPlan(probeCtx, p.client, source, hdrs, keyCopy)
		if perr != nil {
			routeCancel()
			return "", nil, perr
		}
		route.plan = plan
		if ext == ".m3u8" || ext == "" {
			ext = ".mp4"
		}
	} else {
		// Preflight check: verify the stream is reachable before starting the native
		// player, and cache the initial HLS playlist so the player's first GET is free.
		probeCtx, probeCancel := context.WithTimeout(ctx, 8*time.Second)
		defer probeCancel()
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, source, nil)
		if err != nil {
			routeCancel()
			return "", nil, err
		}
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		wantPlaylist := ext == ".m3u8"
		if !wantPlaylist {
			req.Header.Set("Range", "bytes=0-0")
		}
		resp, err := p.client.Do(req)
		if err != nil {
			routeCancel()
			return "", nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			routeCancel()
			return "", nil, fmt.Errorf("media: HTTP %d", resp.StatusCode)
		}
		ct := resp.Header.Get("Content-Type")
		if isPlaylistResponse(resp.Request.URL.String(), ct) {
			ext = ".m3u8"
			body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxPlaylistBytes+1))
			resp.Body.Close()
			if rerr != nil {
				routeCancel()
				return "", nil, rerr
			}
			if len(body) > maxPlaylistBytes {
				routeCancel()
				return "", nil, errors.New("media: playlist too large")
			}
			if !strings.HasPrefix(strings.TrimSpace(string(body)), "#EXTM3U") {
				routeCancel()
				return "", nil, errors.New("media: invalid HLS playlist")
			}
			rewritten := rewriteM3U8(string(body), resp.Request.URL, p.addr, token)
			route.cachedBody = []byte(rewritten)
			route.cachedType = "application/vnd.apple.mpegurl"
			route.cachedAt = time.Now()
		} else {
			resp.Body.Close()
		}
	}

	p.mu.Lock()
	p.routes[token] = route
	p.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.routes, token)
			p.mu.Unlock()
			routeCancel()
		})
	}
	return fmt.Sprintf("http://%s/s/%s/stream%s", p.addr, token, ext), release, nil
}

func (p *streamProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Expected path: /s/<token>/<file>
	trimmed := strings.TrimPrefix(r.URL.Path, "/s/")
	slash := strings.IndexByte(trimmed, '/')
	if slash <= 0 || trimmed == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	token := trimmed[:slash]
	leaf := trimmed[slash+1:]

	p.mu.RLock()
	route := p.routes[token]
	p.mu.RUnlock()
	if route == nil {
		http.NotFound(w, r)
		return
	}

	var target string
	isRoot := strings.HasPrefix(leaf, "stream")
	if isRoot {
		if route.plan != nil {
			reqCtx, cancel := context.WithCancel(r.Context())
			defer cancel()
			stop := context.AfterFunc(route.ctx, cancel)
			defer stop()
			route.plan.serve(reqCtx, w, r)
			return
		}
		target = route.source
		if r.Method == http.MethodGet {
			route.mu.Lock()
			body, ct, at := route.cachedBody, route.cachedType, route.cachedAt
			route.cachedBody = nil
			route.mu.Unlock()
			if len(body) > 0 && time.Since(at) < 5*time.Second {
				w.Header().Set("Content-Type", ct)
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
				return
			}
		}
	} else {
		enc := r.URL.Query().Get("u")
		raw, err := base64.RawURLEncoding.DecodeString(enc)
		if err != nil {
			http.Error(w, "bad target", http.StatusBadRequest)
			return
		}
		target = string(raw)
	}

	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		http.Error(w, "forbidden scheme", http.StatusForbidden)
		return
	}

	reqCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(route.ctx, cancel)
	defer stop()

	method := r.Method
	if method != http.MethodHead {
		method = http.MethodGet
	}
	upReq, err := http.NewRequestWithContext(reqCtx, method, target, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, v := range route.headers {
		upReq.Header.Set(k, v)
	}
	for _, h := range []string{"Range", "If-Range", "If-Modified-Since", "If-None-Match"} {
		if v := r.Header.Get(h); v != "" {
			upReq.Header.Set(h, v)
		}
	}

	resp, err := p.client.Do(upReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if method == http.MethodGet && resp.StatusCode >= 200 && resp.StatusCode < 300 && isPlaylistResponse(resp.Request.URL.String(), ct) {
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxPlaylistBytes+1))
		if rerr != nil || len(body) > maxPlaylistBytes {
			http.Error(w, "playlist read failed", http.StatusBadGateway)
			return
		}
		rewritten := rewriteM3U8(string(body), resp.Request.URL, p.addr, token)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, rewritten)
		return
	}

	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	if method == http.MethodGet {
		_, _ = io.Copy(w, resp.Body)
	}
}

func rewriteM3U8(body string, base *url.URL, proxyAddr, token string) string {
	rewriteURL := func(raw string) string {
		ref, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return raw
		}
		abs := base.ResolveReference(ref)
		if abs.Scheme != "http" && abs.Scheme != "https" {
			return raw
		}
		ext := urlExt(abs.String(), ".ts")
		enc := base64.RawURLEncoding.EncodeToString([]byte(abs.String()))
		return fmt.Sprintf("http://%s/s/%s/seg%s?u=%s", proxyAddr, token, ext, enc)
	}

	var out strings.Builder
	out.Grow(len(body) + 256)
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 64*1024), maxPlaylistBytes)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			out.WriteByte('\n')
		case strings.HasPrefix(trimmed, "#"):
			if strings.Contains(line, `URI="`) {
				line = m3u8URIAttr.ReplaceAllStringFunc(line, func(m string) string {
					inner := m[5 : len(m)-1]
					return `URI="` + rewriteURL(inner) + `"`
				})
			}
			out.WriteString(line)
			out.WriteByte('\n')
		default:
			out.WriteString(rewriteURL(trimmed))
			out.WriteByte('\n')
		}
	}
	return out.String()
}
