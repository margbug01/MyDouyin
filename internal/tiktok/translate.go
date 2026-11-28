package tiktok

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	clients5TranslateEndpoint = "https://clients5.google.com/translate_a/t"
	translateEndpoint         = "https://translate.googleapis.com/translate_a/single"
	googleWebTranslateURL     = "https://translate.google.com/translate_a/single"
	myMemoryTranslateEndpoint = "https://api.mymemory.translated.net/get"
	translateCooldownDur      = 3 * time.Minute
	translateCacheLimit       = 2000
)

type translateProviderKind int

const (
	providerClients5 translateProviderKind = iota
	providerGoogleSingle
	providerMyMemory
)

type translateProvider struct {
	name string
	url  string
	kind translateProviderKind
}

var defaultTranslateChain = []translateProvider{
	{name: "clients5", url: clients5TranslateEndpoint, kind: providerClients5},
	{name: "google-api", url: translateEndpoint, kind: providerGoogleSingle},
	{name: "google-web", url: googleWebTranslateURL, kind: providerGoogleSingle},
	{name: "mymemory", url: myMemoryTranslateEndpoint, kind: providerMyMemory},
}

type translateHTTPError struct {
	provider string
	status   int
	detail   string
}

func (e *translateHTTPError) Error() string {
	if e.detail != "" {
		return fmt.Sprintf("翻译服务返回 HTTP %d (%s)", e.status, e.detail)
	}
	return fmt.Sprintf("翻译服务返回 HTTP %d", e.status)
}

func (e *translateHTTPError) rateLimited() bool {
	return e.status == http.StatusTooManyRequests || e.status == http.StatusForbidden || e.status == http.StatusServiceUnavailable
}

// IsMostlyChinese reports whether s already contains predominantly Chinese characters.
func IsMostlyChinese(s string) bool {
	var han, letters int
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			han++
			letters++
		} else if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters == 0 {
		return true
	}
	return han >= 2 && han*100 >= letters*35
}

// ParseGoogleTranslateReply decodes the nested JSON array returned by
// translate.googleapis.com/translate_a/single?client=gtx&dt=t.
func ParseGoogleTranslateReply(body []byte) (string, error) {
	var top []any
	if err := json.Unmarshal(body, &top); err != nil {
		return "", err
	}
	if len(top) == 0 {
		return "", fmt.Errorf("tiktok: empty translate response")
	}
	sentences, ok := top[0].([]any)
	if !ok {
		return "", fmt.Errorf("tiktok: unexpected translate payload")
	}
	var b strings.Builder
	for _, item := range sentences {
		seg, ok := item.([]any)
		if !ok || len(seg) == 0 {
			continue
		}
		if part, ok := seg[0].(string); ok {
			b.WriteString(part)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// ParseClients5TranslateReply decodes the JSON array returned by
// clients5.google.com/translate_a/t?client=dict-chrome-ex.
// It supports both `[["translated","srcLang"], ...]` and `["translated1", ...]`.
func ParseClients5TranslateReply(body []byte, expected int) ([]string, error) {
	var raw []any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("tiktok: empty clients5 translate response")
	}
	// Single-item flat shape: ["translated", "en"]
	if expected == 1 {
		if first, ok := raw[0].(string); ok {
			return []string{strings.TrimSpace(first)}, nil
		}
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		switch v := item.(type) {
		case []any:
			if len(v) > 0 {
				if s, ok := v[0].(string); ok {
					out = append(out, strings.TrimSpace(s))
				}
			}
		case string:
			out = append(out, strings.TrimSpace(v))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("tiktok: unexpected clients5 translate payload")
	}
	return out, nil
}

// ParseMyMemoryTranslateReply decodes api.mymemory.translated.net/get JSON.
func ParseMyMemoryTranslateReply(body []byte) (string, error) {
	var payload struct {
		ResponseData struct {
			TranslatedText string `json:"translatedText"`
		} `json:"responseData"`
		QuotaFinished   bool   `json:"quotaFinished"`
		ResponseStatus  any    `json:"responseStatus"`
		ResponseDetails string `json:"responseDetails"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if payload.QuotaFinished {
		return "", &translateHTTPError{provider: "mymemory", status: http.StatusTooManyRequests, detail: "quota"}
	}
	switch st := payload.ResponseStatus.(type) {
	case float64:
		if int(st) != http.StatusOK && int(st) != 0 {
			return "", &translateHTTPError{provider: "mymemory", status: int(st), detail: payload.ResponseDetails}
		}
	case string:
		if st != "" && st != "200" {
			return "", &translateHTTPError{provider: "mymemory", status: http.StatusTooManyRequests, detail: payload.ResponseDetails}
		}
	}
	text := strings.TrimSpace(payload.ResponseData.TranslatedText)
	if strings.Contains(strings.ToUpper(text), "MYMEMORY WARNING") {
		return "", &translateHTTPError{provider: "mymemory", status: http.StatusTooManyRequests, detail: "quota"}
	}
	if text == "" {
		return "", fmt.Errorf("tiktok: empty mymemory translate response")
	}
	return text, nil
}

func normalizeTranslateTarget(targetLang string) string {
	low := strings.ToLower(strings.TrimSpace(targetLang))
	if strings.Contains(low, "hant") || strings.HasSuffix(low, "-tw") || strings.HasSuffix(low, "-hk") {
		return "zh-TW"
	}
	if low == "" || strings.HasPrefix(low, "zh") {
		return "zh-CN"
	}
	return targetLang
}

func normalizeTikTokTarget(targetLang string) string {
	low := strings.ToLower(strings.TrimSpace(targetLang))
	if strings.Contains(low, "hant") || strings.HasSuffix(low, "-tw") || strings.HasSuffix(low, "-hk") {
		return "zh-Hant"
	}
	if low == "" || strings.HasPrefix(low, "zh") {
		return "zh-Hans"
	}
	return strings.TrimSpace(targetLang)
}

func (c *Client) callTikTokTranslateAPI(path, extra string) ([]byte, error) {
	relURL := path + "?" + c.commonQuery(extra)
	fullURL := strings.TrimRight(c.baseURL, "/") + relURL
	// Fast path: direct pure-Go HTTP (works without waking the WebView).
	raw, err := c.callDirect(http.MethodGet, fullURL, "")
	if err == nil {
		return raw, nil
	}
	if w := c.getWeb(); w != nil {
		return c.callViaWeb(w, http.MethodGet, relURL, "")
	}
	return nil, err
}

// TranslateDescription translates a TikTok video's description using TikTok's
// native `/aweme/v1/translation/description/` API (ByteDance AI Lab), falling
// back to TranslateText only when itemID is absent or TikTok declines.
func (c *Client) TranslateDescription(itemID, desc, targetLang string) (string, error) {
	trimmed := strings.TrimSpace(desc)
	if trimmed == "" && strings.TrimSpace(itemID) == "" {
		return "", nil
	}
	ttLang := normalizeTikTokTarget(targetLang)
	cacheKey := "tt-desc:" + ttLang + ":" + itemID
	if itemID != "" {
		if cached, ok := c.cachedTranslation("tt", cacheKey); ok {
			return cached, nil
		}
		extra := "item_id=" + url.QueryEscape(itemID) + "&target_lang=" + url.QueryEscape(ttLang)
		tryParse := func(raw []byte) (string, bool) {
			var resp struct {
				StatusCode        int    `json:"status_code"`
				TranslatedContent string `json:"translated_content"`
			}
			if jerr := json.Unmarshal(raw, &resp); jerr != nil || resp.StatusCode != 0 {
				return "", false
			}
			out := strings.TrimSpace(resp.TranslatedContent)
			return out, out != ""
		}
		if raw, err := c.callTikTokTranslateAPI("/aweme/v1/translation/description/", extra); err == nil {
			if out, ok := tryParse(raw); ok {
				c.storeTranslation("tt", cacheKey, out)
				return out, nil
			}
			if w := c.getWeb(); w != nil {
				relURL := "/aweme/v1/translation/description/?" + c.commonQuery(extra)
				if raw2, err2 := c.callViaWeb(w, http.MethodGet, relURL, ""); err2 == nil {
					if out, ok := tryParse(raw2); ok {
						c.storeTranslation("tt", cacheKey, out)
						return out, nil
					}
				}
			}
		}
	}
	if trimmed == "" {
		return "", fmt.Errorf("TikTok 未返回该视频的文案译文")
	}
	return c.TranslateText(trimmed, targetLang)
}

// TranslateSubtitle translates a TikTok video's captions using TikTok's native
// `/tiktok/cla/subtitle_translation/get/v1/` API (which returns a complete
// translated WebVTT document), falling back to TranslateCues if unavailable.
func (c *Client) TranslateSubtitle(itemID, subtitleID string, rawCues []SubtitleCue, targetLang string) ([]SubtitleCue, error) {
	ttLang := normalizeTikTokTarget(targetLang)
	if strings.TrimSpace(itemID) != "" && strings.TrimSpace(subtitleID) != "" {
		extra := "item_id=" + url.QueryEscape(itemID) +
			"&subtitle_id=" + url.QueryEscape(subtitleID) +
			"&target_language=" + url.QueryEscape(ttLang)
		tryParse := func(raw []byte) ([]SubtitleCue, bool) {
			var resp struct {
				StatusCode     int    `json:"status_code"`
				WebVTTSubtitle string `json:"webvtt_subtitle"`
			}
			if err := json.Unmarshal(raw, &resp); err != nil || resp.StatusCode != 0 {
				return nil, false
			}
			vtt := strings.TrimSpace(resp.WebVTTSubtitle)
			if vtt == "" {
				return nil, false
			}
			cues := ParseWebVTT(vtt)
			if len(cues) == 0 {
				return nil, false
			}
			return cues, true
		}
		if raw, err := c.callTikTokTranslateAPI("/tiktok/cla/subtitle_translation/get/v1/", extra); err == nil {
			if cues, ok := tryParse(raw); ok {
				return cues, nil
			}
			// Direct returned non-zero status (e.g. session/signature needed): retry via WebView if attached.
			if w := c.getWeb(); w != nil {
				relURL := "/tiktok/cla/subtitle_translation/get/v1/?" + c.commonQuery(extra)
				if raw2, err2 := c.callViaWeb(w, http.MethodGet, relURL, ""); err2 == nil {
					if cues, ok := tryParse(raw2); ok {
						return cues, nil
					}
				}
			}
		}
	}
	return c.TranslateCues(rawCues, targetLang)
}

func (c *Client) activeTranslateProviders() []translateProvider {
	if len(c.translateChain) > 0 {
		return c.translateChain
	}
	if c.translateURL != "" {
		return []translateProvider{{name: "custom", url: c.translateURL, kind: providerGoogleSingle}}
	}
	return defaultTranslateChain
}

func (c *Client) cachedTranslation(tl, text string) (string, bool) {
	c.translateMu.Lock()
	defer c.translateMu.Unlock()
	if c.translateCache == nil {
		return "", false
	}
	v, ok := c.translateCache[tl+"\x00"+text]
	return v, ok
}

func (c *Client) storeTranslation(tl, text, translated string) {
	if text == "" || translated == "" {
		return
	}
	c.translateMu.Lock()
	defer c.translateMu.Unlock()
	if c.translateCache == nil {
		c.translateCache = make(map[string]string, 64)
	}
	if len(c.translateCache) >= translateCacheLimit {
		clear(c.translateCache)
	}
	c.translateCache[tl+"\x00"+text] = translated
}

func (c *Client) isProviderCooling(name string) bool {
	c.translateMu.Lock()
	defer c.translateMu.Unlock()
	if c.translateCooldown == nil {
		return false
	}
	until, ok := c.translateCooldown[name]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(c.translateCooldown, name)
		return false
	}
	return true
}

func (c *Client) coolProvider(name string, d time.Duration) {
	c.translateMu.Lock()
	defer c.translateMu.Unlock()
	if c.translateCooldown == nil {
		c.translateCooldown = make(map[string]time.Time)
	}
	c.translateCooldown[name] = time.Now().Add(d)
}

func (c *Client) translateBatchWithProvider(p translateProvider, texts []string, tl string) ([]string, error) {
	switch p.kind {
	case providerClients5:
		return c.callClients5Batch(p, texts, tl)
	case providerGoogleSingle:
		return c.callGoogleSingleBatch(p, texts, tl)
	case providerMyMemory:
		return c.callMyMemoryBatch(p, texts, tl)
	default:
		return nil, fmt.Errorf("tiktok: unknown translate provider %q", p.name)
	}
}

func (c *Client) callClients5Batch(p translateProvider, texts []string, tl string) ([]string, error) {
	endpoint := p.url + "?client=dict-chrome-ex&sl=auto&tl=" + url.QueryEscape(tl)
	form := url.Values{}
	for _, t := range texts {
		form.Add("q", t)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &translateHTTPError{provider: p.name, status: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	out, err := ParseClients5TranslateReply(b, len(texts))
	if err != nil {
		return nil, err
	}
	if len(out) != len(texts) {
		return nil, fmt.Errorf("tiktok: clients5 returned %d items for %d inputs", len(out), len(texts))
	}
	return out, nil
}

func (c *Client) callGoogleSingleBatch(p translateProvider, texts []string, tl string) ([]string, error) {
	joined := strings.Join(texts, "\n")
	q := url.Values{}
	q.Set("client", "gtx")
	q.Set("sl", "auto")
	q.Set("tl", tl)
	q.Set("dt", "t")

	var req *http.Request
	var err error
	if len(joined) <= 500 && !strings.Contains(joined, "\n") {
		q.Set("q", joined)
		req, err = http.NewRequest(http.MethodGet, p.url+"?"+q.Encode(), nil)
	} else {
		body := url.Values{"q": {joined}}.Encode()
		req, err = http.NewRequest(http.MethodPost, p.url+"?"+q.Encode(), strings.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
		}
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &translateHTTPError{provider: p.name, status: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	out, err := ParseGoogleTranslateReply(b)
	if err != nil {
		return nil, err
	}
	if len(texts) == 1 {
		if out == "" {
			return []string{texts[0]}, nil
		}
		return []string{out}, nil
	}
	parts := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	if len(parts) != len(texts) {
		return nil, fmt.Errorf("tiktok: google-single line mismatch (%d vs %d)", len(parts), len(texts))
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
		if parts[i] == "" {
			parts[i] = texts[i]
		}
	}
	return parts, nil
}

func (c *Client) callMyMemoryOne(p translateProvider, text, tl string) (string, error) {
	q := url.Values{}
	q.Set("q", text)
	q.Set("langpair", "autodetect|"+tl)
	req, err := http.NewRequest(http.MethodGet, p.url+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &translateHTTPError{provider: p.name, status: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return ParseMyMemoryTranslateReply(b)
}

func (c *Client) callMyMemoryBatch(p translateProvider, texts []string, tl string) ([]string, error) {
	// MyMemory caps single query around 500 bytes; pack small chunks separated by newline.
	const maxChunkBytes = 380
	out := make([]string, len(texts))
	var chunkIdx []int
	var chunkTexts []string
	chunkBytes := 0

	flush := func() error {
		if len(chunkIdx) == 0 {
			return nil
		}
		joined := strings.Join(chunkTexts, "\n")
		if len(joined) > 480 && len(chunkIdx) == 1 {
			// Trim oversized single caption cleanly on a rune boundary.
			runes := []rune(joined)
			for len(string(runes)) > 480 && len(runes) > 1 {
				runes = runes[:len(runes)-1]
			}
			joined = string(runes)
		}
		tr, err := c.callMyMemoryOne(p, joined, tl)
		if err != nil {
			return err
		}
		if len(chunkIdx) == 1 {
			out[chunkIdx[0]] = tr
		} else {
			parts := strings.Split(strings.ReplaceAll(tr, "\r\n", "\n"), "\n")
			if len(parts) == len(chunkIdx) {
				for k, orig := range chunkIdx {
					if s := strings.TrimSpace(parts[k]); s != "" {
						out[orig] = s
					} else {
						out[orig] = chunkTexts[k]
					}
				}
			} else {
				// Avoid request storms: keep combined translation on first cue or preserve originals.
				for k, orig := range chunkIdx {
					if k < len(parts) && strings.TrimSpace(parts[k]) != "" {
						out[orig] = strings.TrimSpace(parts[k])
					} else {
						out[orig] = chunkTexts[k]
					}
				}
			}
		}
		chunkIdx = chunkIdx[:0]
		chunkTexts = chunkTexts[:0]
		chunkBytes = 0
		return nil
	}

	for i, text := range texts {
		if len(chunkIdx) > 0 && chunkBytes+len(text)+1 > maxChunkBytes {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		chunkIdx = append(chunkIdx, i)
		chunkTexts = append(chunkTexts, text)
		chunkBytes += len(text) + 1
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// translateBatch runs the provider fallback chain with per-provider 429 cooldown.
func (c *Client) translateBatch(texts []string, targetLang string) ([]string, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	tl := normalizeTranslateTarget(targetLang)
	providers := c.activeTranslateProviders()

	// Try non-cooling providers first; if all are cooling, try them all as a last resort.
	var candidates []translateProvider
	for _, p := range providers {
		if !c.isProviderCooling(p.name) {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		candidates = providers
	}

	var lastErr error
	for _, p := range candidates {
		res, err := c.translateBatchWithProvider(p, texts, tl)
		if err == nil && len(res) == len(texts) {
			return res, nil
		}
		lastErr = err
		var httpErr *translateHTTPError
		if errors.As(err, &httpErr) && httpErr.rateLimited() {
			c.coolProvider(p.name, translateCooldownDur)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("翻译服务暂不可用")
	}
	return nil, lastErr
}

// TranslateText translates text into targetLang (default "zh-CN") with
// automatic fallback across multiple zero-key translation endpoints.
func (c *Client) TranslateText(text, targetLang string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", nil
	}
	tl := normalizeTranslateTarget(targetLang)
	if cached, ok := c.cachedTranslation(tl, trimmed); ok {
		return cached, nil
	}
	res, err := c.translateBatch([]string{trimmed}, tl)
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(res[0])
	if out == "" {
		return trimmed, nil
	}
	c.storeTranslation(tl, trimmed, out)
	return out, nil
}

// TranslateCues translates WebVTT cues into targetLang using large aligned
// batches (up to 25 cues per request on clients5) and an in-memory cache so a
// typical video translates in a single HTTP request without triggering 429s.
func (c *Client) TranslateCues(cues []SubtitleCue, targetLang string) ([]SubtitleCue, error) {
	if len(cues) == 0 {
		return nil, nil
	}
	tl := normalizeTranslateTarget(targetLang)
	out := make([]SubtitleCue, len(cues))
	copy(out, cues)

	const maxBatchChars = 3000
	const maxBatchLines = 25

	var batchIdx []int
	var batchLines []string
	batchChars := 0

	flush := func() error {
		if len(batchIdx) == 0 {
			return nil
		}
		translated, err := c.translateBatch(batchLines, tl)
		if err != nil {
			return err
		}
		for k, origIdx := range batchIdx {
			if line := strings.TrimSpace(translated[k]); line != "" {
				out[origIdx].Text = line
				c.storeTranslation(tl, batchLines[k], line)
			}
		}
		batchIdx = batchIdx[:0]
		batchLines = batchLines[:0]
		batchChars = 0
		return nil
	}

	for i, cue := range cues {
		if IsMostlyChinese(cue.Text) {
			continue
		}
		flat := strings.Join(strings.Fields(cue.Text), " ")
		if flat == "" {
			continue
		}
		if cached, ok := c.cachedTranslation(tl, flat); ok {
			out[i].Text = cached
			continue
		}
		if len(batchIdx) > 0 && (batchChars+len(flat)+1 > maxBatchChars || len(batchIdx) >= maxBatchLines) {
			if err := flush(); err != nil {
				return out, err
			}
		}
		batchIdx = append(batchIdx, i)
		batchLines = append(batchLines, flat)
		batchChars += len(flat) + 1
	}
	if err := flush(); err != nil {
		return out, err
	}
	return out, nil
}
