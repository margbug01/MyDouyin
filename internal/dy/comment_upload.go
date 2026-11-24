package dy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	imagexGatewayDefault = "https://imagex.bytedanceapi.com"
	imagexRegion         = "cn-north-1"
	imagexService        = "imagex"
	imagexVersion        = "2018-08-01"
	// MaxCommentImageBytes matches Douyin web's 10 MB per-image comment limit.
	MaxCommentImageBytes = 10 * 1024 * 1024
)

// commentUploadAuth holds temporary STS credentials returned by
// /aweme/v1/web/comment/upload/auth/. Never cached or logged.
type commentUploadAuth struct {
	AccessKey    string
	SecretKey    string
	SessionToken string
	ServiceID    string
	ClockOffset  time.Duration
}

func (a commentUploadAuth) signingTime(now time.Time) time.Time {
	return now.Add(a.ClockOffset)
}

type imagexUploadAddress struct {
	Host         string
	StoreURI     string
	Auth         string
	SessionKey   string
	ExtraHeaders map[string]string
}

func (c *Client) commentUploadAuth() (commentUploadAuth, error) {
	writeGate.lock(true)
	defer writeGate.unlock()
	if c.web == nil {
		return commentUploadAuth{}, errNoWeb
	}
	js := `const u = "https://www.douyin.com/aweme/v1/web/comment/upload/auth/?content_type=0&device_platform=webapp&aid=6383&channel=channel_pc_web";
const r = await fetch(u, { method: "GET", credentials: "include" });
const text = await r.text();
return { http: r.status, text };`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b, err := c.web.Eval(ctx, js)
	if err != nil {
		return commentUploadAuth{}, err
	}
	var resp struct {
		HTTP int
		Text string
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return commentUploadAuth{}, err
	}
	if resp.HTTP != 200 {
		return commentUploadAuth{}, fmt.Errorf("获取传图凭证失败（HTTP %d）", resp.HTTP)
	}
	var d struct {
		Code         *int            `json:"status_code"`
		Msg          string          `json:"status_msg"`
		AccessKey    string          `json:"access_key"`
		SecretKey    string          `json:"secret_key"`
		SessionToken string          `json:"session_token"`
		ServiceID    json.RawMessage `json:"service_id"`
		CurrentTime  float64         `json:"current_time"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &d); err != nil {
		return commentUploadAuth{}, fmt.Errorf("传图凭证解析失败：%w", err)
	}
	if d.Code != nil && *d.Code == 8 {
		return commentUploadAuth{}, ErrNeedLogin
	}
	sid := strings.Trim(strings.TrimSpace(string(d.ServiceID)), `"`)
	if (d.Code != nil && *d.Code != 0) || d.AccessKey == "" || d.SecretKey == "" || d.SessionToken == "" || sid == "" {
		if d.Msg != "" {
			return commentUploadAuth{}, errors.New(d.Msg)
		}
		return commentUploadAuth{}, errors.New("抖音未返回图片上传凭证")
	}
	auth := commentUploadAuth{
		AccessKey:    d.AccessKey,
		SecretKey:    d.SecretKey,
		SessionToken: d.SessionToken,
		ServiceID:    sid,
	}
	if d.CurrentTime > 0 {
		gap := time.Unix(int64(d.CurrentTime), 0).Sub(c.now())
		if gap > time.Minute || gap < -time.Minute {
			auth.ClockOffset = gap
		}
	}
	return auth, nil
}

// UploadCommentImage fetches short-lived ImageX STS credentials through the
// logged-in WebView and uploads the encoded image bytes directly to ImageX
// using AWS4-HMAC-SHA256 without sending Douyin session cookies from Go.
func (c *Client) UploadCommentImage(data []byte, fileName, userID string, measured CommentImage) (CommentImage, error) {
	if len(data) == 0 {
		return CommentImage{}, errors.New("图片内容为空")
	}
	if len(data) > MaxCommentImageBytes {
		return CommentImage{}, errors.New("图片超过 10MB，抖音不支持上传")
	}
	if fileName == "" {
		fileName = "comment.jpg"
	}
	auth, err := c.commentUploadAuth()
	if err != nil {
		return CommentImage{}, err
	}
	httpClient := c.http
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return uploadToImageX(httpClient, imagexGatewayDefault, auth, data, fileName, userID, measured, c.now())
}

func uploadToImageX(httpClient *http.Client, gateway string, auth commentUploadAuth, data []byte, fileName, userID string, measured CommentImage, now time.Time) (CommentImage, error) {
	nonce := randomNonce()
	applyReq, err := buildApplyImageUploadReq(gateway, auth, len(data), nonce, now)
	if err != nil {
		return CommentImage{}, err
	}
	applyBody, err := doImageXReq(httpClient, applyReq, "申请")
	if err != nil {
		return CommentImage{}, err
	}
	addr, err := parseApplyImageUpload(applyBody)
	if err != nil {
		return CommentImage{}, err
	}

	scheme := "https"
	if strings.HasPrefix(gateway, "http://") {
		scheme = "http"
	}
	upReq, err := buildUploadImageReq(scheme, addr, data, fileName, userID)
	if err != nil {
		return CommentImage{}, err
	}
	upBody, err := doImageXReq(httpClient, upReq, "传图")
	if err != nil {
		return CommentImage{}, err
	}
	if err := checkUploadImage(upBody); err != nil {
		return CommentImage{}, err
	}

	commitReq, err := buildCommitImageUploadReq(gateway, auth, addr.SessionKey, now)
	if err != nil {
		return CommentImage{}, err
	}
	commitBody, err := doImageXReq(httpClient, commitReq, "提交")
	if err != nil {
		return CommentImage{}, err
	}
	return parseCommitImageUpload(commitBody, measured)
}

func doImageXReq(httpClient *http.Client, req *http.Request, step string) ([]byte, error) {
	req.Header.Set("Origin", "https://www.douyin.com")
	req.Header.Set("Referer", "https://www.douyin.com/")
	req.Header.Set("User-Agent", ua)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("图片上传失败（%s）：%w", step, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("图片上传失败（%s）：%w", step, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if step == "传图" {
			if err := checkUploadImage(body); err != nil {
				return nil, err
			}
		} else if err := checkImageXMetadataError(body, step); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("图片上传失败（%s HTTP %d）", step, resp.StatusCode)
	}
	return body, nil
}

func buildApplyImageUploadReq(gateway string, auth commentUploadAuth, fileSize int, nonce string, now time.Time) (*http.Request, error) {
	query := map[string]string{
		"Action":       "ApplyImageUpload",
		"Version":      imagexVersion,
		"ServiceId":    auth.ServiceID,
		"NeedFallback": "true",
		"FileSize":     fmt.Sprint(fileSize),
		"s":            nonce,
	}
	return buildSignedImageXReq(gateway, http.MethodGet, query, nil, auth, now)
}

func buildCommitImageUploadReq(gateway string, auth commentUploadAuth, sessionKey string, now time.Time) (*http.Request, error) {
	query := map[string]string{
		"Action":    "CommitImageUpload",
		"Version":   imagexVersion,
		"ServiceId": auth.ServiceID,
	}
	body, err := json.Marshal(struct {
		SessionKey string `json:"SessionKey"`
	}{SessionKey: sessionKey})
	if err != nil {
		return nil, err
	}
	return buildSignedImageXReq(gateway, http.MethodPost, query, body, auth, now)
}

func buildSignedImageXReq(gateway, method string, query map[string]string, body []byte, auth commentUploadAuth, now time.Time) (*http.Request, error) {
	amzDate := auth.signingTime(now).UTC().Format("20060102T150405Z")
	headers := map[string]string{
		"X-Amz-Date":           amzDate,
		"x-amz-security-token": auth.SessionToken,
	}
	if body != nil {
		headers["X-Amz-Content-Sha256"] = sha256Hex(body)
	}
	headers["Authorization"] = aws4Authorization(method, "/", query, headers, auth.AccessKey, auth.SecretKey, imagexRegion, imagexService, amzDate)

	rawURL := strings.TrimRight(gateway, "/") + "/?" + aws4CanonicalQuery(query)
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func buildUploadImageReq(scheme string, addr imagexUploadAddress, data []byte, fileName, userID string) (*http.Request, error) {
	rawURL := fmt.Sprintf("%s://%s/upload/v1/%s", scheme, addr.Host, addr.StoreURI)
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	for k, v := range addr.ExtraHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", addr.Auth)
	req.Header.Set("Content-CRC32", fmt.Sprintf("%08x", crc32.ChecksumIEEE(data)))
	req.Header.Set("X-Storage-U", rfc3986Encode(userID))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, rfc3986Encode(fileName)))
	return req, nil
}

func checkImageXMetadataError(data []byte, step string) error {
	var root struct {
		ResponseMetadata struct {
			Error *struct {
				Code    string `json:"Code"`
				CodeN   int    `json:"CodeN"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"ResponseMetadata"`
	}
	if err := json.Unmarshal(data, &root); err != nil || root.ResponseMetadata.Error == nil {
		return nil
	}
	e := root.ResponseMetadata.Error
	code := e.Code
	if code == "" && e.CodeN != 0 {
		code = fmt.Sprint(e.CodeN)
	}
	if e.Message != "" {
		return fmt.Errorf("图片上传失败（%s %s：%s）", step, code, e.Message)
	}
	return fmt.Errorf("图片上传失败（%s %s）", step, code)
}

func parseApplyImageUpload(data []byte) (imagexUploadAddress, error) {
	if err := checkImageXMetadataError(data, "申请"); err != nil {
		return imagexUploadAddress{}, err
	}
	var root struct {
		Result struct {
			UploadAddress struct {
				UploadHosts  []string          `json:"UploadHosts"`
				SessionKey   string            `json:"SessionKey"`
				UploadHeader map[string]string `json:"UploadHeader"`
				StoreInfos   []struct {
					StoreURI string `json:"StoreUri"`
					Auth     string `json:"Auth"`
				} `json:"StoreInfos"`
			} `json:"UploadAddress"`
		} `json:"Result"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return imagexUploadAddress{}, errors.New("图片上传失败（申请返回的数据无法解析）")
	}
	a := root.Result.UploadAddress
	if len(a.UploadHosts) == 0 || a.UploadHosts[0] == "" || len(a.StoreInfos) == 0 || a.StoreInfos[0].StoreURI == "" || a.StoreInfos[0].Auth == "" || a.SessionKey == "" {
		return imagexUploadAddress{}, errors.New("图片上传失败（申请未返回完整上传地址）")
	}
	return imagexUploadAddress{
		Host:         a.UploadHosts[0],
		StoreURI:     a.StoreInfos[0].StoreURI,
		Auth:         a.StoreInfos[0].Auth,
		SessionKey:   a.SessionKey,
		ExtraHeaders: a.UploadHeader,
	}, nil
}

func checkUploadImage(data []byte) error {
	var root struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return errors.New("图片上传失败（传图返回的数据无法解析）")
	}
	if root.Code != 2000 {
		if root.Message != "" {
			return fmt.Errorf("图片上传失败（传图 %d：%s）", root.Code, root.Message)
		}
		return fmt.Errorf("图片上传失败（传图 %d）", root.Code)
	}
	return nil
}

func parseCommitImageUpload(data []byte, fallback CommentImage) (CommentImage, error) {
	if err := checkImageXMetadataError(data, "提交"); err != nil {
		return CommentImage{}, err
	}
	var root struct {
		Result struct {
			Results []struct {
				URI string `json:"Uri"`
			} `json:"Results"`
			PluginResult []struct {
				ImageURI    string `json:"ImageUri"`
				ImageWidth  int    `json:"ImageWidth"`
				ImageHeight int    `json:"ImageHeight"`
				ImageFormat string `json:"ImageFormat"`
			} `json:"PluginResult"`
		} `json:"Result"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return CommentImage{}, errors.New("图片上传失败（提交返回的数据无法解析）")
	}
	if len(root.Result.Results) == 0 {
		return CommentImage{}, errors.New("图片上传失败（提交未返回图片 URI）")
	}
	out := CommentImage{
		URI:    root.Result.Results[0].URI,
		Width:  fallback.Width,
		Height: fallback.Height,
		Format: fallback.Format,
	}
	if len(root.Result.PluginResult) > 0 {
		p := root.Result.PluginResult[0]
		if p.ImageURI != "" {
			out.URI = p.ImageURI
		}
		if p.ImageWidth > 0 {
			out.Width = p.ImageWidth
		}
		if p.ImageHeight > 0 {
			out.Height = p.ImageHeight
		}
		if p.ImageFormat != "" {
			out.Format = p.ImageFormat
		}
	}
	if out.URI == "" {
		return CommentImage{}, errors.New("图片上传失败（提交未返回图片 URI）")
	}
	return out, nil
}

func randomNonce() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func rfc3986Encode(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	for i := range len(s) {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			out.WriteByte(c)
		} else {
			fmt.Fprintf(&out, "%%%02X", c)
		}
	}
	return out.String()
}

func aws4CanonicalQuery(params map[string]string) string {
	type pair struct{ k, v string }
	ps := make([]pair, 0, len(params))
	for k, v := range params {
		ps = append(ps, pair{rfc3986Encode(k), rfc3986Encode(v)})
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].k == ps[j].k {
			return ps[i].v < ps[j].v
		}
		return ps[i].k < ps[j].k
	})
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

var aws4Unsignable = map[string]bool{
	"authorization":     true,
	"content-type":      true,
	"content-length":    true,
	"user-agent":        true,
	"presigned-expires": true,
	"expect":            true,
	"x-amzn-trace-id":   true,
}

func aws4Authorization(method, path string, query, headers map[string]string, accessKey, secretKey, region, service, amzDate string) string {
	type hdr struct{ k, v string }
	var signed []hdr
	bodyHash := ""
	for k, v := range headers {
		lk := strings.ToLower(k)
		if lk == "x-amz-content-sha256" {
			bodyHash = v
		}
		if strings.HasPrefix(lk, "x-amz-") || !aws4Unsignable[lk] {
			signed = append(signed, hdr{lk, strings.TrimSpace(v)})
		}
	}
	sort.Slice(signed, func(i, j int) bool { return signed[i].k < signed[j].k })
	if bodyHash == "" {
		bodyHash = sha256Hex(nil)
	}
	var canonHeaders strings.Builder
	names := make([]string, len(signed))
	for i, h := range signed {
		names[i] = h.k
		canonHeaders.WriteString(h.k)
		canonHeaders.WriteByte(':')
		canonHeaders.WriteString(strings.Join(strings.Fields(h.v), " "))
		canonHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")
	if path == "" {
		path = "/"
	}
	canonReq := strings.Join([]string{
		strings.ToUpper(method),
		path,
		aws4CanonicalQuery(query),
		canonHeaders.String(),
		signedHeaders,
		bodyHash,
	}, "\n")

	day := amzDate[:8]
	scope := strings.Join([]string{day, region, service, "aws4_request"}, "/")
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex([]byte(canonReq))}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+secretKey), day)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(kSigning, toSign))
	return fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", accessKey, scope, signedHeaders, sig)
}
