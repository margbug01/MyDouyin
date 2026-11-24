package dy

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPublishCommentTextReplyAndImage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	w := &fakeWeb{
		replies: []string{
			reply(`{"status_code":0,"comment":{"cid":"c99","text":"真不错[赞]","create_time":1700000000,"user":{"nickname":"我"},"image_list":[{"medium_url":{"url_list":["m1"]},"origin_url":{"url_list":["o1"]}}]}}`),
		},
		cookies: []Cookie{{Name: "sessionid", Value: "s"}},
	}
	c := &Client{}
	c.SetWeb(w)

	cm, err := c.PublishComment(PublishCommentReq{
		VideoID:        "v123",
		Text:           "真不错[赞]",
		ReplyID:        "c1",
		ReplyToReplyID: "c2",
		Image:          &CommentImage{URI: "tos-cn-i-abc/img1", Width: 800, Height: 600, Format: "jpeg"},
		Pasted:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cm.ID != "c99" || cm.Text != "真不错[赞]" || cm.Author != "我" || !slices.Equal(cm.Pics, []CommentPic{{Thumb: "m1", Origin: "o1"}}) {
		t.Fatalf("unexpected comment: %+v", cm)
	}
	if len(w.scripts) != 1 {
		t.Fatalf("expected 1 publish call, got %d", len(w.scripts))
	}
	js := w.scripts[0]
	if !strings.Contains(js, "/aweme/v1/web/comment/publish") || !strings.Contains(js, "x-secsdk-csrf-token") {
		t.Fatalf("missing publish path or csrf downgrade header: %s", js)
	}
	// Extract the encoded form body from const [bodyStr] = ["..."];
	start := strings.Index(js, `["`)
	end := strings.Index(js, `"]`)
	var arr []string
	if err := json.Unmarshal([]byte(js[start:end+2]), &arr); err != nil || len(arr) != 1 {
		t.Fatalf("cannot parse bodyStr: %v", err)
	}
	form, err := url.ParseQuery(arr[0])
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"aweme_id":               "v123",
		"text":                   "真不错[赞]",
		"reply_id":               "c1",
		"reply_to_reply_id":      "c2",
		"image_uri_list":         "tos-cn-i-abc/img1",
		"image_widths":           "800",
		"image_heights":          "600",
		"image_formats":          "jpeg",
		"paste_edit_method":      "paste",
		"one_level_comment_rank": "-1",
	} {
		if got := form.Get(k); got != want {
			t.Errorf("form[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestPublishCommentErrorsDoNotRetry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &Client{}
	w := &fakeWeb{replies: []string{`{"http":200,"text":"","verify":true}`}}
	c.SetWeb(w)
	if _, err := c.PublishComment(PublishCommentReq{VideoID: "v1", Text: "hi"}); !errors.Is(err, ErrVerify) || len(w.scripts) != 1 {
		t.Fatalf("verify: err=%v calls=%d", err, len(w.scripts))
	}
	w.replies, w.scripts = []string{reply(`{"status_code":8,"status_msg":"用户未登录"}`)}, nil
	if _, err := c.PublishComment(PublishCommentReq{VideoID: "v1", Text: "hi"}); !errors.Is(err, ErrNeedLogin) || len(w.scripts) != 1 {
		t.Fatalf("need login: err=%v calls=%d", err, len(w.scripts))
	}
}

func TestUploadToImageXEndToEnd(t *testing.T) {
	payload := []byte("123456789")
	var steps []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("Action") {
		case "ApplyImageUpload":
			steps = append(steps, "apply")
			if r.Method != http.MethodGet {
				t.Errorf("apply method %s", r.Method)
			}
			authHdr := r.Header.Get("Authorization")
			if !strings.Contains(authHdr, "SignedHeaders=x-amz-date;x-amz-security-token,") {
				t.Errorf("apply signed headers wrong: %s", authHdr)
			}
			if r.Header.Get("X-Amz-Security-Token") != "tok" {
				t.Errorf("missing security token")
			}
			host := strings.TrimPrefix(srv.URL, "http://")
			json.NewEncoder(w).Encode(map[string]any{
				"Result": map[string]any{
					"UploadAddress": map[string]any{
						"UploadHosts":  []string{host},
						"SessionKey":   "SESSION==",
						"UploadHeader": map[string]string{"X-Extra": "1"},
						"StoreInfos":   []map[string]string{{"StoreUri": "tos-cn-i-abc/0123", "Auth": "SpaceKey/sig"}},
					},
				},
			})
		case "CommitImageUpload":
			steps = append(steps, "commit")
			if r.Method != http.MethodPost {
				t.Errorf("commit method %s", r.Method)
			}
			authHdr := r.Header.Get("Authorization")
			if !strings.Contains(authHdr, "SignedHeaders=x-amz-content-sha256;x-amz-date;x-amz-security-token,") {
				t.Errorf("commit signed headers wrong: %s", authHdr)
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"SessionKey":"SESSION=="}` {
				t.Errorf("commit body %s", body)
			}
			if r.Header.Get("X-Amz-Content-Sha256") != sha256Hex(body) {
				t.Errorf("commit sha256 mismatch")
			}
			json.NewEncoder(w).Encode(map[string]any{
				"Result": map[string]any{
					"Results":      []map[string]any{{"Uri": "tos-cn-i-abc/0123"}},
					"PluginResult": []map[string]any{{"ImageUri": "tos-cn-i-abc/0123", "ImageWidth": 640, "ImageHeight": 480, "ImageFormat": "jpeg"}},
				},
			})
		default:
			if r.URL.Path != "/upload/v1/tos-cn-i-abc/0123" {
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
			steps = append(steps, "upload")
			if r.Header.Get("Authorization") != "SpaceKey/sig" {
				t.Errorf("upload auth %q", r.Header.Get("Authorization"))
			}
			if r.Header.Get("Content-CRC32") != "cbf43926" {
				t.Errorf("upload crc32 %q, want cbf43926", r.Header.Get("Content-CRC32"))
			}
			if r.Header.Get("X-Storage-U") != "12345" || r.Header.Get("X-Extra") != "1" {
				t.Errorf("upload headers X-Storage-U=%q X-Extra=%q", r.Header.Get("X-Storage-U"), r.Header.Get("X-Extra"))
			}
			if got := r.Header.Get("Content-Disposition"); got != `attachment; filename="%E8%AF%84%E8%AE%BA%20%E5%9B%BE.jpg"` {
				t.Errorf("content-disposition %q", got)
			}
			if r.Header.Get("Cookie") != "" {
				t.Errorf("upload must not send Douyin cookies")
			}
			w.Write([]byte(`{"code":2000,"message":"Success"}`))
		}
	}))
	defer srv.Close()

	auth := commentUploadAuth{AccessKey: "ak", SecretKey: "sk", SessionToken: "tok", ServiceID: "svc1"}
	got, err := uploadToImageX(srv.Client(), srv.URL, auth, payload, "评论 图.jpg", "12345", CommentImage{Width: 10, Height: 20, Format: "png"}, time.Date(2026, 11, 17, 1, 53, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(steps, []string{"apply", "upload", "commit"}) {
		t.Fatalf("steps = %v", steps)
	}
	want := CommentImage{URI: "tos-cn-i-abc/0123", Width: 640, Height: 480, Format: "jpeg"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
