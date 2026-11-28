package tiktok

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// TikTok's requests (API fallbacks, covers, translation, and the player's
// streams through media.StreamOptions.Client) go through a proxy of their
// own, which nothing else in the app takes: the one set in the settings,
// else the system's (macOS's network settings, Windows' Internet Options),
// else the environment's HTTP(S)_PROXY. Go reads only the environment, which
// an app opened from the Dock or the Start menu does not have. The WebView,
// TikTok's own page, follows the system's by itself.

// ParseProxy reads a proxy set in the settings: "" for none, else an
// http, https, socks5 or socks5h URL with a host.
func ParseProxy(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("代理地址 %q 无效", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return u, nil
	}
	return nil, fmt.Errorf("不支持 %s 代理，请用 http:// 或 socks5://", u.Scheme)
}

// SetProxy sets the proxy TikTok's requests go through; "" goes back to
// finding one (proxyFor).
func (c *Client) SetProxy(raw string) error {
	u, err := ParseProxy(raw)
	if err != nil {
		return err
	}
	c.proxy.Store(u)
	if tr, ok := c.http.Transport.(*http.Transport); ok {
		tr.CloseIdleConnections() // kept ones go through the proxy before
	}
	return nil
}

func (c *Client) proxyFor(r *http.Request) (*url.URL, error) {
	if u := c.proxy.Load(); u != nil {
		return u, nil
	}
	if u := systemProxy(r.URL.Scheme); u != nil {
		return u, nil
	}
	return httpproxy.FromEnvironment().ProxyFunc()(r.URL)
}

func newTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy: proxy,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 20 * time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       60 * time.Second,
	}
}

// systemProxies are the system's proxies by what they carry; nil, none.
type systemProxies struct {
	http, https, socks *url.URL
}

// pick is the proxy the system would take for a request of scheme: its
// HTTP or HTTPS proxy, else its SOCKS one.
func (s systemProxies) pick(scheme string) *url.URL {
	if scheme == "https" && s.https != nil {
		return s.https
	}
	if scheme == "http" && s.http != nil {
		return s.http
	}
	return s.socks
}

// The system's proxies are read again once they are this old, so that a
// proxy switched on or off is followed without asking the system each request.
const systemProxyTTL = 5 * time.Second

var sysProxy struct {
	sync.Mutex
	at time.Time
	s  systemProxies
}

func systemProxy(scheme string) *url.URL {
	sysProxy.Lock()
	defer sysProxy.Unlock()
	if time.Since(sysProxy.at) > systemProxyTTL {
		sysProxy.s, sysProxy.at = readSystemProxies(), time.Now()
	}
	return sysProxy.s.pick(scheme)
}

// parseScutilProxy reads `scutil --proxy`, macOS's proxies for the network
// in use: lines "HTTPSEnable : 1", "HTTPSProxy : 127.0.0.1", "HTTPSPort : 7890".
func parseScutilProxy(out string) systemProxies {
	kv := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), " : ")
		if ok {
			kv[k] = strings.TrimSpace(v)
		}
	}
	at := func(kind, scheme string) *url.URL {
		host, port := kv[kind+"Proxy"], kv[kind+"Port"]
		if kv[kind+"Enable"] != "1" || host == "" || port == "" {
			return nil
		}
		return &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port)}
	}
	return systemProxies{http: at("HTTP", "http"), https: at("HTTPS", "http"), socks: at("SOCKS", "socks5")}
}

// parseWinProxyServer reads Windows' ProxyServer, which is "host:port" for
// every scheme or "http=host:port;https=host:port;socks=host:port".
func parseWinProxyServer(server string) systemProxies {
	var s systemProxies
	at := func(scheme, addr string) *url.URL {
		addr = strings.TrimSpace(addr)
		if i := strings.Index(addr, "://"); i >= 0 {
			addr = addr[i+3:]
		}
		if addr == "" {
			return nil
		}
		return &url.URL{Scheme: scheme, Host: addr}
	}
	if !strings.Contains(server, "=") {
		u := at("http", server)
		return systemProxies{http: u, https: u}
	}
	for _, part := range strings.Split(server, ";") {
		k, v, _ := strings.Cut(part, "=")
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "http":
			s.http = at("http", v)
		case "https":
			s.https = at("http", v)
		case "socks":
			s.socks = at("socks5", v)
		}
	}
	return s
}
