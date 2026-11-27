package tiktok

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"douyin/internal/dy"
)

var (
	curlCookieHeaderRe = regexp.MustCompile(`(?i)(?:-H|--header)\s+['"]cookie:\s*([^'"]+)['"]`)
	curlCookieFlagRe   = regexp.MustCompile(`(?i)(?:-b|--cookie)\s+['"]([^'"]+)['"]`)
)

// ParseImportedCookies parses TikTok cookies pasted from a browser session.
// It accepts Cookie-Editor JSON arrays, Netscape cookies.txt exports,
// Chrome/Edge DevTools "Copy as cURL" commands, or raw "Cookie: k=v; ..." headers.
func ParseImportedCookies(raw string) ([]dy.Cookie, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, errors.New("请先粘贴 Cookie 或 Copy as cURL 内容")
	}

	var cs []dy.Cookie
	switch {
	case strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{"):
		parsed, err := parseCookieJSON(s)
		if err != nil {
			return nil, fmt.Errorf("JSON Cookie 格式无效：%w", err)
		}
		cs = parsed
	case looksLikeCurl(s):
		header := extractCurlCookie(s)
		if header == "" {
			return nil, errors.New("未在 cURL 命令中找到 cookie（请右键已登录的 tiktok.com 请求选择「以 cURL (bash) 复制」）")
		}
		cs = parseCookieHeader(header)
	case looksLikeNetscape(s):
		cs = parseNetscapeCookies(s)
	default:
		cs = parseCookieHeader(s)
	}

	cs = normalizeTikTokCookies(cs)
	if len(cs) == 0 {
		return nil, errors.New("未解析到有效的 Cookie")
	}
	if !LoggedIn(cs) {
		return nil, errors.New("未检测到 TikTok 登录凭据（缺少 sessionid / sessionid_ss / sid_tt，请确认已在浏览器登录并复制含 HttpOnly 的 Cookie 或使用「以 cURL 复制」）")
	}
	return cs, nil
}

func looksLikeCurl(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(low, "curl ") || strings.HasPrefix(low, "curl.exe ")
}

func extractCurlCookie(s string) string {
	// Join bash/cmd line continuations so regexes see a single line.
	flat := strings.ReplaceAll(s, "\\\r\n", " ")
	flat = strings.ReplaceAll(flat, "\\\n", " ")
	flat = strings.ReplaceAll(flat, "^\r\n", " ")
	flat = strings.ReplaceAll(flat, "^\n", " ")
	if m := curlCookieFlagRe.FindStringSubmatch(flat); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	if m := curlCookieHeaderRe.FindStringSubmatch(flat); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	// Handle PowerShell/cmd double-quoted cookies where inner quotes may be escaped.
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimSpace(line)
		low := strings.ToLower(l)
		if idx := strings.Index(low, "cookie:"); idx >= 0 {
			val := strings.TrimSpace(l[idx+len("cookie:"):])
			val = strings.TrimRight(val, `\"'^ `)
			val = strings.TrimLeft(val, `"' `)
			if val != "" {
				return val
			}
		}
	}
	return ""
}

func looksLikeNetscape(s string) bool {
	if strings.Contains(s, "# Netscape HTTP Cookie File") || strings.Contains(s, "# HTTP Cookie File") {
		return true
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Count(line, "\t") >= 6 {
			return true
		}
		break
	}
	return false
}

func parseNetscapeCookies(s string) []dy.Cookie {
	var out []dy.Cookie
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		httpOnly := false
		if strings.HasPrefix(line, "#HttpOnly_") {
			httpOnly = true
			line = strings.TrimPrefix(line, "#HttpOnly_")
		} else if strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 7 {
			continue
		}
		domain := strings.TrimSpace(parts[0])
		if domain != "" && !strings.Contains(strings.ToLower(domain), "tiktok.com") {
			continue
		}
		exp, _ := strconv.ParseInt(strings.TrimSpace(parts[4]), 10, 64)
		out = append(out, dy.Cookie{
			Name:     strings.TrimSpace(parts[5]),
			Value:    strings.TrimSpace(parts[6]),
			Domain:   domain,
			Path:     strings.TrimSpace(parts[2]),
			Expires:  exp,
			Secure:   strings.EqualFold(strings.TrimSpace(parts[3]), "true"),
			HTTPOnly: httpOnly,
		})
	}
	return out
}

type rawJSONCookie struct {
	Name           string  `json:"name"`
	Value          string  `json:"value"`
	Domain         string  `json:"domain"`
	Path           string  `json:"path"`
	Expires        float64 `json:"expires"`
	ExpirationDate float64 `json:"expirationDate"`
	Secure         bool    `json:"secure"`
	HTTPOnly       bool    `json:"httpOnly"`
}

func parseCookieJSON(s string) ([]dy.Cookie, error) {
	var raw []rawJSONCookie
	if strings.HasPrefix(s, "[") {
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return nil, err
		}
	} else {
		var obj struct {
			Cookies []rawJSONCookie `json:"cookies"`
		}
		if err := json.Unmarshal([]byte(s), &obj); err != nil {
			return nil, err
		}
		raw = obj.Cookies
	}
	out := make([]dy.Cookie, 0, len(raw))
	for _, r := range raw {
		if r.Domain != "" && !strings.Contains(strings.ToLower(r.Domain), "tiktok.com") {
			continue
		}
		exp := r.Expires
		if exp == 0 {
			exp = r.ExpirationDate
		}
		out = append(out, dy.Cookie{
			Name:     strings.TrimSpace(r.Name),
			Value:    strings.TrimSpace(r.Value),
			Domain:   strings.TrimSpace(r.Domain),
			Path:     strings.TrimSpace(r.Path),
			Expires:  int64(math.Trunc(exp)),
			Secure:   r.Secure,
			HTTPOnly: r.HTTPOnly,
		})
	}
	return out, nil
}

func parseCookieHeader(s string) []dy.Cookie {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "cookie:") {
		s = strings.TrimSpace(s[len("cookie:"):])
	}
	s = strings.Trim(s, `"'`)
	parts := strings.Split(s, ";")
	out := make([]dy.Cookie, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(part[:eq])
		val := strings.TrimSpace(part[eq+1:])
		if name == "" || strings.ContainsAny(name, " \t\r\n") {
			continue
		}
		out = append(out, dy.Cookie{
			Name:     name,
			Value:    val,
			Domain:   ".tiktok.com",
			Path:     "/",
			Secure:   true,
			HTTPOnly: isLikelyHTTPOnly(name),
		})
	}
	return out
}

func isLikelyHTTPOnly(name string) bool {
	switch name {
	case "sessionid", "sessionid_ss", "sid_tt", "sid_guard", "uid_tt", "uid_tt_ss", "ttwid", "tt_chain_token", "passport_csrf_token", "passport_csrf_token_default":
		return true
	default:
		return false
	}
}

func normalizeTikTokCookies(in []dy.Cookie) []dy.Cookie {
	defaultExp := time.Now().Add(90 * 24 * time.Hour).Unix()
	seen := make(map[string]int, len(in))
	out := make([]dy.Cookie, 0, len(in))
	for _, ck := range in {
		ck.Name = strings.TrimSpace(ck.Name)
		ck.Value = strings.TrimSpace(ck.Value)
		if ck.Name == "" || ck.Value == "" {
			continue
		}
		if ck.Domain == "" || !strings.Contains(strings.ToLower(ck.Domain), "tiktok.com") {
			ck.Domain = ".tiktok.com"
		}
		if ck.Path == "" {
			ck.Path = "/"
		}
		if ck.Expires <= 0 {
			ck.Expires = defaultExp
		}
		ck.Secure = true
		if isLikelyHTTPOnly(ck.Name) {
			ck.HTTPOnly = true
		}
		if idx, exists := seen[ck.Name]; exists {
			out[idx] = ck
		} else {
			seen[ck.Name] = len(out)
			out = append(out, ck)
		}
	}
	return out
}
