package hg

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCommentOrigin = "https://api5-normal-sinfonlineb.fqnovel.com"
	commentUserAgent     = "com.phoenix.read/73532 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)"
	maxCommentRespBytes  = 2 << 20
	maxCommentItemsPage  = 100
	commentPageSize      = 20
	seriesCommentSize    = 10
	replyPageSize        = 10
)

var (
	fqArgusSecret = mustDecodeHex("ac1adaae95a7af94a5114ab3b3a97dd80050aa0a39314c40528caec95256c28c")
	fqArgusMask   = mustDecodeHex("d04ffdffd04ffdff")
	fqArgusHead   = mustDecodeHex("a6e783ee7001100918")
	fqArgusTail   = mustDecodeHex("567b")
	fqArgusPrefix = mustDecodeHex("3ccc")
	fqArgusSalt   = mustDecodeHex("3ccc567b")
	fqGorgonConst = mustDecodeHex("00010704")
)

const fqSimonSeed uint64 = 0x3dc94c3a046d678b

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func newCommentHTTPClient(direct bool) *http.Client {
	tr := cloneDefaultHTTPTransport()
	if direct {
		tr.Proxy = nil
	}
	return &http.Client{
		Timeout:   20 * time.Second,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (c *Client) commentHTTP() (*http.Client, string) {
	c.mu.RLock()
	hc := c.commentClient
	origin := c.commentOrigin
	c.mu.RUnlock()
	if hc == nil {
		hc = newCommentHTTPClient(true)
	}
	if origin == "" {
		origin = defaultCommentOrigin
	}
	return hc, strings.TrimRight(origin, "/")
}

func (c *Client) commentIdentity() (deviceID, iid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if isNumericID(c.commentDeviceID) && isNumericID(c.commentIID) {
		return c.commentDeviceID, c.commentIID
	}
	// Reuse existing persisted profile's device_id / install_id if available.
	if c.profilePath != "" {
		if raw, err := os.ReadFile(c.profilePath); err == nil {
			var p struct {
				DeviceID  string `json:"device_id"`
				InstallID string `json:"install_id"`
			}
			if json.Unmarshal(raw, &p) == nil {
				if isNumericID(p.DeviceID) {
					c.commentDeviceID = p.DeviceID
				}
				if isNumericID(p.InstallID) {
					c.commentIID = p.InstallID
				}
			}
		}
	}
	if !isNumericID(c.commentDeviceID) {
		c.commentDeviceID = randomNumericID()
	}
	if !isNumericID(c.commentIID) {
		c.commentIID = randomNumericID()
	}
	return c.commentDeviceID, c.commentIID
}

func randomNumericID() string {
	var b [18]byte
	_, _ = rand.Read(b[:])
	var sb strings.Builder
	sb.Grow(19)
	sb.WriteByte('7')
	for _, v := range b {
		sb.WriteByte('0' + (v % 10))
	}
	return sb.String()
}

func isNumericID(s string) bool {
	if len(s) < 6 || len(s) > 30 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isValidAvatarURL(s string) bool {
	if len(s) == 0 || len(s) > 4096 {
		return false
	}
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

func (c *Client) ListEpisodeComments(ctx context.Context, seriesID, episodeID, cursor string) (CommentPage, error) {
	seriesID = strings.TrimSpace(seriesID)
	episodeID = strings.TrimSpace(episodeID)
	cursor = strings.TrimSpace(cursor)
	if !isNumericID(seriesID) || !isNumericID(episodeID) {
		return CommentPage{}, errors.New("hg: invalid series or episode id")
	}
	if _, err := validateJSONCursor(cursor); err != nil {
		return CommentPage{}, err
	}
	payload := map[string]any{
		"aid":               8662,
		"comment_source":    4,
		"server_channel":    18,
		"group_id":          episodeID,
		"group_type":        30,
		"comment_type":      4,
		"sort":              1,
		"need_count":        true,
		"count":             commentPageSize,
		"cursor":            cursor,
		"compliance_status": 0,
		"business_param": map[string]any{
			"book_id":  seriesID,
			"req_type": 0,
		},
	}
	raw, err := c.postCommentAPI(ctx, "comment/list/"+url.PathEscape(episodeID), payload, 4, 18)
	if err != nil {
		return CommentPage{}, err
	}
	return parseCommentListResponse(raw, seriesID, episodeID, cursor, false)
}

func (c *Client) ListSeriesComments(ctx context.Context, seriesID, cursor string) (CommentPage, error) {
	seriesID = strings.TrimSpace(seriesID)
	cursor = strings.TrimSpace(cursor)
	if !isNumericID(seriesID) {
		return CommentPage{}, errors.New("hg: invalid series id")
	}
	if _, err := validateJSONCursor(cursor); err != nil {
		return CommentPage{}, err
	}
	payload := map[string]any{
		"comment_source": 1,
		"server_channel": 34,
		"group_id":       seriesID,
		"group_type":     1,
		"comment_type":   2,
		"sort":           1,
		"count":          seriesCommentSize,
		"business_param": map[string]any{
			"book_id":    seriesID,
			"need_count": true,
			"real_level": 2,
			"req_type":   0,
		},
	}
	if cursor != "" {
		payload["cursor"] = cursor
	}
	raw, err := c.postCommentAPI(ctx, "comment/list/"+url.PathEscape(seriesID), payload, 1, 34)
	if err != nil {
		return CommentPage{}, err
	}
	return parseCommentListResponse(raw, seriesID, seriesID, cursor, true)
}

func (c *Client) ListEpisodeReplies(ctx context.Context, seriesID, episodeID, commentID, cursor string) (CommentPage, error) {
	seriesID = strings.TrimSpace(seriesID)
	episodeID = strings.TrimSpace(episodeID)
	commentID = strings.TrimSpace(commentID)
	cursor = strings.TrimSpace(cursor)
	if !isNumericID(seriesID) || !isNumericID(episodeID) || commentID == "" {
		return CommentPage{}, errors.New("hg: invalid reply parameters")
	}
	if _, err := validateNumCursor(cursor); err != nil {
		return CommentPage{}, err
	}
	payload := map[string]any{
		"aid":               8662,
		"comment_id":        commentID,
		"comment_source":    504,
		"comment_type":      4,
		"compliance_status": 0,
		"count":             replyPageSize,
		"cursor":            cursor,
		"group_id":          episodeID,
		"group_type":        30,
		"server_channel":    18,
		"business_param": map[string]any{
			"book_id":    seriesID,
			"need_count": false,
		},
	}
	raw, err := c.postCommentAPI(ctx, "reply/list/"+url.PathEscape(commentID), payload, 504, 18)
	if err != nil {
		return CommentPage{}, err
	}
	return parseReplyListResponse(raw, seriesID, episodeID, cursor)
}

func (c *Client) ListSeriesReplies(ctx context.Context, seriesID, commentID, cursor string) (CommentPage, error) {
	seriesID = strings.TrimSpace(seriesID)
	commentID = strings.TrimSpace(commentID)
	cursor = strings.TrimSpace(cursor)
	if !isNumericID(seriesID) || commentID == "" {
		return CommentPage{}, errors.New("hg: invalid series reply parameters")
	}
	if _, err := validateNumCursor(cursor); err != nil {
		return CommentPage{}, err
	}
	payload := map[string]any{
		"comment_id":     commentID,
		"comment_source": 501,
		"comment_type":   2,
		"count":          replyPageSize,
		"group_id":       seriesID,
		"group_type":     1,
		"server_channel": 34,
		"business_param": map[string]any{
			"book_id":    seriesID,
			"need_count": cursor == "",
			"real_level": 2,
		},
	}
	if cursor != "" {
		payload["cursor"] = cursor
	}
	raw, err := c.postCommentAPI(ctx, "reply/list/"+url.PathEscape(commentID), payload, 501, 34)
	if err != nil {
		return CommentPage{}, err
	}
	return parseReplyListResponse(raw, seriesID, seriesID, cursor)
}

func (c *Client) postCommentAPI(ctx context.Context, relPath string, payload map[string]any, source, channel int) ([]byte, error) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	nowMs := time.Now().UnixMilli()
	deviceID, iid := c.commentIdentity()
	encodedQuery := buildCommentQuery(nowMs, deviceID, iid)

	var nonce [11]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}

	hc, origin := c.commentHTTP()
	fullURL := fmt.Sprintf("%s/novel/commentapi/%s/v1/?%s", origin, relPath, encodedQuery)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", commentUserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("X-XS-From-Web", "0")
	req.Header.Set("Sdk-Version", "2")
	req.Header.Set("Comment-Source", strconv.Itoa(source))
	req.Header.Set("Server-Channel", strconv.Itoa(channel))
	for k, v := range fqCommentSignHeaders(encodedQuery, nowMs, nonce[:]) {
		req.Header.Set(k, v)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("hg: comment api HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCommentRespBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxCommentRespBytes {
		return nil, fmt.Errorf("hg: comment response exceeds %d bytes", maxCommentRespBytes)
	}
	return raw, nil
}

func buildCommentQuery(nowMs int64, deviceID, iid string) string {
	params := map[string]string{
		"_rticket":              strconv.FormatInt(nowMs, 10),
		"ac":                    "wifi",
		"aid":                   "8662",
		"app_name":              "novelread",
		"channel":               "update_64",
		"device_brand":          "Redmi",
		"device_id":             deviceID,
		"device_platform":       "android",
		"device_type":           "25053RT47C",
		"dpi":                   "520",
		"iid":                   iid,
		"language":              "zh",
		"manifest_version_code": "73532",
		"os":                    "android",
		"os_api":                "36",
		"os_version":            "16",
		"resolution":            "1280*2772",
		"ssmix":                 "a",
		"update_version_code":   "73532",
		"version_code":          "73532",
		"version_name":          "7.3.5.32",
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(url.QueryEscape(params[k]))
	}
	return sb.String()
}

// --- JSON response parsing ---

func decodeJSONObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func mapField(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func sliceField(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	v, _ := m[key].([]any)
	return v
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatInt(int64(v), 10)
	default:
		return ""
	}
}

func int64Field(m map[string]any, key string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n, true
		}
		if f, err := v.Float64(); err == nil {
			return int64(f), true
		}
	case float64:
		return int64(v), true
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func boolField(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	b, _ := m[key].(bool)
	return b
}

func parseTimestamp(ts int64) time.Time {
	if ts <= 0 {
		return time.Time{}
	}
	if ts >= 1e12 {
		return time.UnixMilli(ts)
	}
	return time.Unix(ts, 0)
}

func truncateRunes(s string, maxRunes int) string {
	if len(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

func validateJSONCursor(cursor string) (int64, error) {
	if len(cursor) > 8192 {
		return 0, errors.New("hg: invalid comment cursor")
	}
	if cursor == "" {
		return 0, nil
	}
	obj, err := decodeJSONObject([]byte(cursor))
	if err != nil {
		return 0, errors.New("hg: invalid comment cursor json")
	}
	session := stringField(obj, "session_id")
	if session == "" || len(session) > 200 {
		return 0, errors.New("hg: invalid comment cursor session")
	}
	offset, ok := int64Field(obj, "offset")
	if !ok || offset < 0 || offset > 10_000_000 {
		return 0, errors.New("hg: invalid comment cursor offset")
	}
	return offset, nil
}

func validateNumCursor(cursor string) (int64, error) {
	if len(cursor) > 32 {
		return 0, errors.New("hg: invalid reply cursor")
	}
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || n < 0 || n > 10_000_000 {
		return 0, errors.New("hg: invalid reply cursor value")
	}
	return n, nil
}

func parseCommentListResponse(raw []byte, seriesID, groupID, cursor string, seriesDim bool) (CommentPage, error) {
	root, err := decodeJSONObject(raw)
	if err != nil {
		return CommentPage{}, fmt.Errorf("hg: decode comment response: %w", err)
	}
	code, ok := int64Field(root, "code")
	if !ok {
		code, ok = int64Field(root, "Code")
	}
	if !ok || code != 0 {
		msg := stringField(root, "message")
		if msg == "" {
			msg = stringField(root, "msg")
		}
		return CommentPage{}, fmt.Errorf("hg: comment api rejected request (code=%d %s)", code, msg)
	}

	prevOffset, err := validateJSONCursor(cursor)
	if err != nil {
		return CommentPage{}, err
	}

	data := mapField(root, "data")
	info := mapField(data, "common_list_info")
	if info == nil {
		info = mapField(data, "comment_list_info")
	}
	hasMore := boolField(info, "has_more")
	nextCursor := stringField(info, "cursor")
	if hasMore {
		nextOffset, cerr := validateJSONCursor(nextCursor)
		if cerr != nil || nextCursor == cursor || nextOffset <= prevOffset {
			hasMore = false
			nextCursor = ""
		}
	} else {
		nextCursor = ""
	}

	rows := sliceField(data, "data_list")
	items := make([]Comment, 0, min(len(rows), maxCommentItemsPage))
	seen := make(map[string]struct{}, len(rows))
	for i := 0; i < len(rows) && i < maxCommentItemsPage; i++ {
		row, _ := rows[i].(map[string]any)
		if row == nil {
			continue
		}
		cm := mapField(row, "comment")
		if cm == nil {
			continue
		}
		common := mapField(cm, "common")
		expand := mapField(cm, "expand")
		if common == nil {
			continue
		}
		if gid := stringField(common, "group_id"); gid != "" && gid != groupID {
			return CommentPage{}, errors.New("hg: comment group mismatch")
		}
		if bid := stringField(expand, "book_id"); bid != "" && bid != seriesID {
			return CommentPage{}, errors.New("hg: comment series mismatch")
		}
		if status, hasStatus := int64Field(common, "status"); hasStatus && status != 1 {
			continue
		}
		id := stringField(cm, "comment_id")
		text := stringField(mapField(common, "content"), "text")
		if id == "" || len(id) > 120 || text == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}

		base := mapField(mapField(common, "user_info"), "base_info")
		author := truncateRunes(stringField(base, "user_name"), 200)
		if author == "" {
			author = "红果用户"
		}
		avatar := stringField(base, "user_avatar")
		if avatar == "" {
			avatar = stringField(base, "expand_user_avatar")
		}
		if !isValidAvatarURL(avatar) {
			avatar = ""
		}

		stat := mapField(cm, "stat")
		likes, hasLikes := int64Field(stat, "digg_count")
		if !hasLikes {
			likes, hasLikes = int64Field(mapField(row, "stat"), "digg_count")
		}
		if !hasLikes {
			likes, hasLikes = int64Field(expand, "digg_count")
		}
		if !hasLikes {
			likes, _ = int64Field(common, "digg_count")
		}
		if likes < 0 {
			likes = 0
		}
		replyCount, _ := int64Field(stat, "reply_count")
		if replyCount < 0 {
			replyCount = 0
		}
		createdTS, _ := int64Field(common, "create_timestamp")

		var score string
		if seriesDim {
			score = stringField(expand, "score")
		}

		items = append(items, Comment{
			ID:         id,
			Author:     author,
			AvatarURL:  avatar,
			Text:       truncateRunes(text, 20000),
			CreatedAt:  parseTimestamp(createdTS),
			Likes:      likes,
			ReplyCount: replyCount,
			Score:      score,
		})
	}

	total, _ := int64Field(info, "total")
	if total < 0 {
		total = 0
	}
	page := CommentPage{
		Items:   items,
		Total:   total,
		Cursor:  nextCursor,
		HasMore: hasMore,
	}
	if seriesDim {
		extra := mapField(data, "extra")
		if extra != nil {
			s := stringField(mapField(extra, "book_info"), "score")
			if s == "" {
				s = stringField(extra, "credibility_score")
			}
			page.Score = s
			cnt, _ := int64Field(extra, "score_cnt")
			if cnt <= 0 {
				cnt, _ = int64Field(extra, "credibility_score_count")
			}
			if cnt > 0 {
				page.ScoreCount = cnt
			}
		}
	}
	return page, nil
}

func parseReplyListResponse(raw []byte, seriesID, groupID, cursor string) (CommentPage, error) {
	root, err := decodeJSONObject(raw)
	if err != nil {
		return CommentPage{}, fmt.Errorf("hg: decode reply response: %w", err)
	}
	code, ok := int64Field(root, "code")
	if !ok {
		code, ok = int64Field(root, "Code")
	}
	if !ok || code != 0 {
		return CommentPage{}, fmt.Errorf("hg: reply api rejected request (code=%d)", code)
	}

	prevNum, err := validateNumCursor(cursor)
	if err != nil {
		return CommentPage{}, err
	}

	data := mapField(root, "data")
	if parent := mapField(data, "comment"); parent != nil {
		if bid := stringField(mapField(parent, "expand"), "book_id"); bid != "" && bid != seriesID {
			return CommentPage{}, errors.New("hg: reply series mismatch")
		}
		if gid := stringField(mapField(parent, "common"), "group_id"); gid != "" && gid != groupID {
			return CommentPage{}, errors.New("hg: reply group mismatch")
		}
	}

	info := mapField(data, "comment_list_info")
	hasMore := boolField(info, "has_more")
	nextCursor := stringField(info, "cursor")
	if hasMore {
		nextNum, cerr := validateNumCursor(nextCursor)
		if cerr != nil || nextNum <= prevNum {
			hasMore = false
			nextCursor = ""
		}
	} else {
		nextCursor = ""
	}

	rows := sliceField(data, "reply_list")
	items := make([]Comment, 0, min(len(rows), maxCommentItemsPage))
	seen := make(map[string]struct{}, len(rows))
	for i := 0; i < len(rows) && i < maxCommentItemsPage; i++ {
		row, _ := rows[i].(map[string]any)
		if row == nil {
			continue
		}
		common := mapField(row, "Common")
		if common == nil {
			common = mapField(row, "common")
		}
		if common == nil {
			continue
		}
		id := stringField(row, "reply_id")
		text := stringField(mapField(common, "content"), "text")
		if id == "" || len(id) > 120 || text == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}

		base := mapField(mapField(common, "user_info"), "base_info")
		author := truncateRunes(stringField(base, "user_name"), 200)
		if author == "" {
			author = "红果用户"
		}
		avatar := stringField(base, "user_avatar")
		if avatar == "" {
			avatar = stringField(base, "expand_user_avatar")
		}
		if !isValidAvatarURL(avatar) {
			avatar = ""
		}

		stat := mapField(row, "stat")
		likes, _ := int64Field(stat, "digg_count")
		if likes < 0 {
			likes = 0
		}
		replyCount, _ := int64Field(stat, "reply_count")
		if replyCount < 0 {
			replyCount = 0
		}
		createdTS, _ := int64Field(common, "create_timestamp")

		var replyTo string
		toReplyID := stringField(row, "reply_to_reply_id")
		if toReplyID != "" && toReplyID != "0" {
			toBase := mapField(mapField(row, "reply_to_user_info"), "base_info")
			replyTo = truncateRunes(stringField(toBase, "user_name"), 200)
		}

		items = append(items, Comment{
			ID:         id,
			Author:     author,
			AvatarURL:  avatar,
			Text:       truncateRunes(text, 20000),
			CreatedAt:  parseTimestamp(createdTS),
			Likes:      likes,
			ReplyCount: replyCount,
			ReplyTo:    replyTo,
		})
	}

	total, _ := int64Field(info, "total")
	if total < 0 {
		total = 0
	}
	return CommentPage{
		Items:   items,
		Total:   total,
		Cursor:  nextCursor,
		HasMore: hasMore,
	}, nil
}

// --- Pure-Go fqnovel read-only comment signer (X-Gorgon / X-Khronos / X-Argus / X-Ladon + SM3) ---

func fqCommentSignHeaders(query string, nowMs int64, nonce []byte) map[string]string {
	ts := nowMs / 1000
	return map[string]string{
		"X-Gorgon":        fqSignGorgon(query, ts, nonce),
		"X-Khronos":       strconv.FormatInt(ts, 10),
		"X-SS-Req-Ticket": strconv.FormatInt(nowMs, 10),
		"X-SS-STUB":       "",
		"X-Argus":         fqSignArgus(query, ts, nonce),
		"X-Ladon":         fqSignLadon(ts, nonce[2:6]),
	}
}

func fqSignGorgon(query string, ts int64, nonce []byte) string {
	first := byte(((int(nonce[0])%7 + 1) << 5) & 0xff)
	second := byte(0xe0 | (nonce[1] & 0x0f))

	var input [20]byte
	qmd5 := md5.Sum([]byte(query))
	copy(input[0:4], qmd5[0:4])
	copy(input[12:16], fqGorgonConst)
	binary.BigEndian.PutUint32(input[16:20], uint32(ts))

	key := [8]byte{0x4a, 0, 0x16, second, 0x47, 0x6c, 0, first}
	var box [256]byte
	for i := range box {
		box[i] = byte(i)
	}
	var j byte
	for i := range box {
		j = byte(int(j) + int(box[i]) + int(key[i%8]))
		box[i] = box[j]
	}
	j = 0
	for n := range input {
		i := byte(n + 1)
		j = byte(int(j) + int(box[i]))
		box[i] = box[j]
		input[n] ^= box[byte(int(box[i])+int(box[j]))]
	}
	for i := range input {
		v := input[i]
		swapped := (v << 4) | (v >> 4)
		next := input[(i+1)%len(input)]
		input[i] = bits.Reverse8(swapped^next) ^ 0xeb
	}

	var out [26]byte
	out[0] = 0x84
	out[1] = 0x04
	out[2] = first
	out[3] = second
	copy(out[6:], input[:])
	return hex.EncodeToString(out[:])
}

func fqSignLadon(ts int64, salt []byte) string {
	buf := make([]byte, 0, len(salt)+4)
	buf = append(buf, salt...)
	buf = append(buf, "8662"...)
	sum := md5.Sum(buf)
	ascii := []byte(hex.EncodeToString(sum[:]))

	queue := [2]uint64{
		binary.LittleEndian.Uint64(ascii[16:24]),
		binary.LittleEndian.Uint64(ascii[24:32]),
	}
	qHead := 0
	var qBuf []uint64
	qBuf = append(qBuf, queue[0], queue[1])
	left := binary.LittleEndian.Uint64(ascii[0:8])
	right := binary.LittleEndian.Uint64(ascii[8:16])

	var keys [34]uint64
	for i := range keys {
		keys[i] = left
		next := (bits.RotateLeft64(right, -8) + left) ^ uint64(i)
		qBuf = append(qBuf, next)
		left = next ^ bits.RotateLeft64(left, 3)
		right = qBuf[qHead]
		qHead++
	}

	plain := pkcs7Pad([]byte(fmt.Sprintf("%d-1611921764-3019", ts)), 16)
	out := make([]byte, 0, len(salt)+len(plain))
	out = append(out, salt...)
	for at := 0; at < len(plain); at += 16 {
		a := binary.LittleEndian.Uint64(plain[at : at+8])
		b := binary.LittleEndian.Uint64(plain[at+8 : at+16])
		for _, k := range keys {
			b = k ^ (a + bits.RotateLeft64(b, -8))
			a = b ^ bits.RotateLeft64(a, 3)
		}
		var blk [16]byte
		binary.LittleEndian.PutUint64(blk[0:8], a)
		binary.LittleEndian.PutUint64(blk[8:16], b)
		out = append(out, blk[:]...)
	}
	return base64.StdEncoding.EncodeToString(out)
}

func fqSignArgus(query string, ts int64, nonce []byte) string {
	randomVal := uint64(binary.LittleEndian.Uint32(nonce[6:10]))
	var nested protoBuf
	nested.varint(1, uint64(2+(int(nonce[10])%49)*2))
	nested.varint(2, 1388734)
	nested.varint(3, 1388734)
	nested.varint(4, 1388734)

	zero16 := make([]byte, 16)
	sm3Zero := sm3Sum(zero16)
	sm3Query := sm3Sum([]byte(query))

	var msg protoBuf
	msg.varint(1, 0x40401252)
	msg.varint(2, 2)
	msg.varint(3, randomVal)
	msg.bytes(4, []byte("3019"))
	msg.bytes(6, []byte("1611921764"))
	msg.bytes(7, []byte("6.8.1.32"))
	msg.bytes(8, []byte("v04.07.01-ml-android"))
	msg.varint(9, 135135744)
	msg.bytes(10, make([]byte, 8))
	msg.varint(12, uint64(ts)*2)
	msg.bytes(13, sm3Zero[:6])
	msg.bytes(14, sm3Query[:6])
	msg.bytes(15, nested.buf)
	msg.bytes(20, []byte("none"))
	msg.varint(21, 738)

	deriveInput := make([]byte, 0, len(fqArgusSecret)*2+len(fqArgusSalt))
	deriveInput = append(deriveInput, fqArgusSecret...)
	deriveInput = append(deriveInput, fqArgusSalt...)
	deriveInput = append(deriveInput, fqArgusSecret...)
	derived := sm3Sum(deriveInput)

	var keys [72]uint64
	for i := 0; i < 4; i++ {
		keys[i] = binary.LittleEndian.Uint64(derived[i*8 : i*8+8])
	}
	for i := 4; i < 72; i++ {
		x := bits.RotateLeft64(keys[i-1], -3) ^ keys[i-3]
		keys[i] = (^keys[i-4]) ^ x ^ bits.RotateLeft64(x, -1) ^ ((fqSimonSeed >> ((i - 4) % 62)) & 1) ^ 3
	}

	blocks := pkcs7Pad(msg.buf, 16)
	enc := make([]byte, 0, len(blocks))
	for at := 0; at < len(blocks); at += 16 {
		a := binary.LittleEndian.Uint64(blocks[at : at+8])
		b := binary.LittleEndian.Uint64(blocks[at+8 : at+16])
		for _, k := range keys {
			next := a ^ (bits.RotateLeft64(b, 1) & bits.RotateLeft64(b, 8)) ^ bits.RotateLeft64(b, 2) ^ k
			a = b
			b = next
		}
		var blk [16]byte
		binary.LittleEndian.PutUint64(blk[0:8], a)
		binary.LittleEndian.PutUint64(blk[8:16], b)
		enc = append(enc, blk[:]...)
	}

	joined := make([]byte, len(fqArgusMask)+len(enc))
	copy(joined, fqArgusMask)
	for i, b := range enc {
		joined[len(fqArgusMask)+i] = b ^ fqArgusMask[i%8]
	}
	for i, j := 0, len(joined)-1; i < j; i, j = i+1, j-1 {
		joined[i], joined[j] = joined[j], joined[i]
	}

	container := make([]byte, 0, len(fqArgusHead)+len(joined)+len(fqArgusTail))
	container = append(container, fqArgusHead...)
	container = append(container, joined...)
	container = append(container, fqArgusTail...)

	aesKey := md5.Sum(fqArgusSecret[0:16])
	aesIV := md5.Sum(fqArgusSecret[16:32])
	blkCipher, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return ""
	}
	padded := pkcs7Pad(container, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(blkCipher, aesIV[:]).CryptBlocks(ciphertext, padded)

	final := make([]byte, 0, len(fqArgusPrefix)+len(ciphertext))
	final = append(final, fqArgusPrefix...)
	final = append(final, ciphertext...)
	return base64.StdEncoding.EncodeToString(final)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

type protoBuf struct {
	buf []byte
}

func (p *protoBuf) varint(tag uint64, v uint64) {
	p.rawVarint(tag << 3)
	p.rawVarint(v)
}

func (p *protoBuf) bytes(tag uint64, b []byte) {
	p.rawVarint((tag << 3) | 2)
	p.rawVarint(uint64(len(b)))
	p.buf = append(p.buf, b...)
}

func (p *protoBuf) rawVarint(v uint64) {
	for v >= 0x80 {
		p.buf = append(p.buf, byte(v&0x7f)|0x80)
		v >>= 7
	}
	p.buf = append(p.buf, byte(v))
}

// sm3Sum implements GB/T 32905-2016 (SM3 cryptographic hash).
func sm3Sum(data []byte) [32]byte {
	iv := [8]uint32{
		0x7380166f, 0x4914b2b9, 0x172442d7, 0xda8a0600,
		0xa96f30bc, 0x163138aa, 0xe38dee4d, 0xb0fb0e4e,
	}
	bitLen := uint64(len(data)) * 8
	padZeros := (56 - (len(data)+1)%64 + 64) % 64
	padded := make([]byte, len(data)+1+padZeros+8)
	copy(padded, data)
	padded[len(data)] = 0x80
	binary.BigEndian.PutUint64(padded[len(padded)-8:], bitLen)

	var w [68]uint32
	var w1 [64]uint32
	for off := 0; off < len(padded); off += 64 {
		for i := 0; i < 16; i++ {
			w[i] = binary.BigEndian.Uint32(padded[off+i*4 : off+i*4+4])
		}
		for j := 16; j < 68; j++ {
			x := w[j-16] ^ w[j-9] ^ bits.RotateLeft32(w[j-3], 15)
			w[j] = (x ^ bits.RotateLeft32(x, 15) ^ bits.RotateLeft32(x, 23)) ^ bits.RotateLeft32(w[j-13], 7) ^ w[j-6]
		}
		for j := 0; j < 64; j++ {
			w1[j] = w[j] ^ w[j+4]
		}
		a, b, c, d, e, f, g, h := iv[0], iv[1], iv[2], iv[3], iv[4], iv[5], iv[6], iv[7]
		for j := 0; j < 64; j++ {
			var tj uint32
			if j < 16 {
				tj = 0x79cc4519
			} else {
				tj = 0x7a879d8a
			}
			ss1 := bits.RotateLeft32(bits.RotateLeft32(a, 12)+e+bits.RotateLeft32(tj, j%32), 7)
			ss2 := ss1 ^ bits.RotateLeft32(a, 12)
			var ff, gg uint32
			if j < 16 {
				ff = a ^ b ^ c
				gg = e ^ f ^ g
			} else {
				ff = (a & b) | (a & c) | (b & c)
				gg = (e & f) | (^e & g)
			}
			tt1 := ff + d + ss2 + w1[j]
			tt2 := gg + h + ss1 + w[j]
			d = c
			c = bits.RotateLeft32(b, 9)
			b = a
			a = tt1
			h = g
			g = bits.RotateLeft32(f, 19)
			f = e
			e = tt2 ^ bits.RotateLeft32(tt2, 9) ^ bits.RotateLeft32(tt2, 17)
		}
		iv[0] ^= a
		iv[1] ^= b
		iv[2] ^= c
		iv[3] ^= d
		iv[4] ^= e
		iv[5] ^= f
		iv[6] ^= g
		iv[7] ^= h
	}
	var out [32]byte
	for i, v := range iv {
		binary.BigEndian.PutUint32(out[i*4:i*4+4], v)
	}
	return out
}
