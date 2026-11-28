package tiktok

import (
	"net/http"
	"testing"
)

// The proxy TikTok finds by itself is the system's, read as macOS and
// Windows give it; one set in the settings wins over it.
func TestProxySystemAndSetting(t *testing.T) {
	mac := parseScutilProxy(`<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
  }
  HTTPEnable : 1
  HTTPPort : 10808
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 10809
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 0
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`)
	for scheme, want := range map[string]string{"https": "http://127.0.0.1:10809", "http": "http://127.0.0.1:10808"} {
		if got := mac.pick(scheme); got == nil || got.String() != want {
			t.Fatalf("macOS %s proxy = %v, want %s", scheme, got, want)
		}
	}
	if got := parseScutilProxy("  SOCKSEnable : 1\n  SOCKSProxy : ::1\n  SOCKSPort : 1080\n").pick("https"); got == nil || got.String() != "socks5://[::1]:1080" {
		t.Fatalf("macOS SOCKS-only proxy = %v", got)
	}
	if got := parseScutilProxy("  HTTPEnable : 0\n  HTTPProxy : 127.0.0.1\n  HTTPPort : 8080\n").pick("http"); got != nil {
		t.Fatalf("a proxy switched off was taken: %v", got)
	}

	if got := parseWinProxyServer("127.0.0.1:7890").pick("https"); got == nil || got.String() != "http://127.0.0.1:7890" {
		t.Fatalf("Windows proxy for all = %v", got)
	}
	win := parseWinProxyServer("http=127.0.0.1:7890;https=127.0.0.1:7891;socks=127.0.0.1:7892")
	if got := win.pick("https"); got == nil || got.String() != "http://127.0.0.1:7891" {
		t.Fatalf("Windows https proxy = %v", got)
	}
	if got := parseWinProxyServer("socks=127.0.0.1:7892").pick("https"); got == nil || got.String() != "socks5://127.0.0.1:7892" {
		t.Fatalf("Windows SOCKS-only proxy = %v", got)
	}

	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetProxy("ftp://127.0.0.1:21"); err == nil {
		t.Fatal("accepted an ftp proxy")
	}
	if err := c.SetProxy("socks5://127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://www.tiktok.com/", nil)
	if got, _ := c.proxyFor(req); got == nil || got.String() != "socks5://127.0.0.1:1080" {
		t.Fatalf("proxy set in the settings not taken: %v", got)
	}
}
