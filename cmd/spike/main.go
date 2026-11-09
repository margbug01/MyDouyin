// spike: can we fetch Douyin's recommend feed with a signed request, no browser?
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"

	"douyin/internal/sign"
)

const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

func main() {
	c := &http.Client{}
	// ttwid is the anonymous device cookie.
	req, _ := http.NewRequest("POST", "https://ttwid.bytedance.com/ttwid/union/register/",
		strings.NewReader(`{"region":"cn","aid":1768,"needFid":false,"service":"www.ixigua.com","migrate_info":{"ticket":"","source":"node"},"cbUrlProtocol":"https","union":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		panic(err)
	}
	var ttwid string
	for _, ck := range resp.Cookies() {
		if ck.Name == "ttwid" {
			ttwid = ck.Value
		}
	}
	resp.Body.Close()
	fmt.Println("ttwid len", len(ttwid))

	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	ms := make([]byte, 107)
	for i := range ms {
		ms[i] = letters[rand.IntN(len(letters))]
	}
	base := "device_platform=webapp&aid=6383&channel=channel_pc_web&pc_client_type=1&version_code=190500&version_name=19.5.0&cookie_enabled=true&screen_width=1920&screen_height=1080&browser_language=zh-CN&browser_platform=Win32&browser_name=Chrome&browser_version=130.0.0.0&browser_online=true&engine_name=Blink&engine_version=130.0.0.0&os_name=Windows&os_version=10&cpu_core_num=8&device_memory=8&platform=PC"
	path, extra := os.Args[1], os.Args[2]
	q := base + "&" + extra + "&msToken=" + url.QueryEscape(string(ms))
	q += "&a_bogus=" + url.QueryEscape(sign.New(ua).Sign(q, ""))
	u := "https://www.douyin.com" + path + "?" + q
	r2, _ := http.NewRequest("GET", u, nil)
	r2.Header.Set("User-Agent", ua)
	r2.Header.Set("Referer", "https://www.douyin.com/")
	r2.Header.Set("Cookie", "ttwid="+ttwid)
	r, err := c.Do(r2)
	if err != nil {
		panic(err)
	}
	body, _ := io.ReadAll(r.Body)
	fmt.Println(r.Status, len(body))
	var out bytes.Buffer
	json.Indent(&out, body, "", " ")
	b := out.Bytes()
	if len(b) > 3000 {
		b = b[:3000]
	}
	fmt.Println(string(b))
	os.WriteFile("/tmp/dy_last.json", body, 0o644)
}
