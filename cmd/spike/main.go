// spike: probe Douyin endpoints with a signed request, no browser.
// usage: spike METHOD URL-without-query "k=v&k=v" [body]
package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"douyin/internal/sign"
)

const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

func rnd(n int, set string) string {
	b := make([]byte, n)
	for i := range b {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		b[i] = set[x.Int64()]
	}
	return string(b)
}

func main() {
	jar, _ := os.ReadFile("/tmp/dy_cookie.txt")
	cookie := strings.TrimSpace(string(jar))
	if cookie == "" {
		req, _ := http.NewRequest("POST", "https://ttwid.bytedance.com/ttwid/union/register/",
			strings.NewReader(`{"region":"cn","aid":1768,"needFid":false,"service":"www.ixigua.com","migrate_info":{"ticket":"","source":"node"},"cbUrlProtocol":"https","union":true}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			panic(err)
		}
		for _, ck := range resp.Cookies() {
			if ck.Name == "ttwid" {
				cookie = "ttwid=" + ck.Value
			}
		}
		csrf := rnd(32, "0123456789abcdef")
		cookie += "; passport_csrf_token=" + csrf + "; passport_csrf_token_default=" + csrf
		os.WriteFile("/tmp/dy_cookie.txt", []byte(cookie), 0o600)
	}
	csrf := ""
	for _, p := range strings.Split(cookie, "; ") {
		if v, ok := strings.CutPrefix(p, "passport_csrf_token="); ok {
			csrf = v
		}
	}

	method, base, extra := os.Args[1], os.Args[2], os.Args[3]
	fp := "verify_" + strconv.FormatInt(time.Now().UnixMilli(), 36) + "_" + rnd(8, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") + "_" + rnd(4, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") + "_" + rnd(4, "abcdef0123456789") + "_" + rnd(4, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") + "_" + rnd(12, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789")
	body := ""
	if len(os.Args) > 4 {
		body = os.Args[4]
	}
	q := "device_platform=webapp&aid=6383&channel=channel_pc_web&pc_client_type=1&version_code=190500&version_name=19.5.0&cookie_enabled=true&screen_width=1920&screen_height=1080&browser_language=zh-CN&browser_platform=Win32&browser_name=Chrome&browser_version=130.0.0.0&browser_online=true&engine_name=Blink&engine_version=130.0.0.0&os_name=Windows&os_version=10&cpu_core_num=8&device_memory=8&platform=PC&verifyFp=" + fp + "&fp=" + fp
	if extra != "" {
		q += "&" + extra
	}
	q += "&msToken=" + url.QueryEscape(rnd(107, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"))
	q += "&a_bogus=" + url.QueryEscape(sign.New(ua).Sign(q, body))
	r, _ := http.NewRequest(method, base+"?"+q, strings.NewReader(body))
	if os.Getenv("CSRF") != "" {
		h, _ := http.NewRequest("HEAD", base+"?"+q, nil)
		h.Header.Set("User-Agent", ua)
		h.Header.Set("Cookie", cookie)
		h.Header.Set("Referer", "https://www.douyin.com/")
		h.Header.Set("x-secsdk-csrf-request", "1")
		h.Header.Set("x-secsdk-csrf-version", "1.2.22")
		hr, err := http.DefaultClient.Do(h)
		if err != nil {
			panic(err)
		}
		parts := strings.Split(hr.Header.Get("x-ware-csrf-token"), ",")
		for _, c := range hr.Cookies() {
			cookie += "; " + c.Name + "=" + c.Value
		}
		if len(parts) > 1 {
			r.Header.Set("x-secsdk-csrf-token", parts[1])
		}
		fmt.Println("csrf parts", len(parts))
	}
	r.Header.Set("User-Agent", ua)
	r.Header.Set("Referer", "https://www.douyin.com/")
	r.Header.Set("Origin", "https://www.douyin.com")
	r.Header.Set("Cookie", cookie)
	r.Header.Set("x-tt-passport-csrf-token", csrf)
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		panic(err)
	}
	b, _ := io.ReadAll(resp.Body)
	fmt.Println(resp.Status, len(b))
	for _, c := range resp.Cookies() {
		fmt.Println("set-cookie", c.Name)
	}
	os.WriteFile("/tmp/dy_last.json", b, 0o644)
	if len(b) > 1500 {
		b = b[:1500]
	}
	fmt.Println(string(b))
}
