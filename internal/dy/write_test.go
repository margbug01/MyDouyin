package dy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeWeb answers each Eval with the next of replies, and keeps the scripts.
type fakeWeb struct {
	replies []string
	scripts []string
	cookies []Cookie
}

func (w *fakeWeb) Eval(_ context.Context, js string) ([]byte, error) {
	w.scripts = append(w.scripts, js)
	r := w.replies[0]
	if len(w.replies) > 1 {
		w.replies = w.replies[1:]
	}
	return []byte(r), nil
}

func (w *fakeWeb) Load(context.Context, string, bool) error { return nil }
func (w *fakeWeb) Cookies() ([]Cookie, error)               { return w.cookies, nil }

func reply(text string) string {
	b, _ := json.Marshal(map[string]any{"http": 200, "text": text})
	return string(b)
}

// A write goes through the page, once it signs; the page's renewed cookies
// become the client's. Douyin's 用户未登录 is not tried again.
func TestWriteThroughWeb(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // cookies.json is the user's
	c := &Client{}
	if err := c.SetLiked("1", true); !errors.Is(err, errNoWeb) {
		t.Fatalf("without a page: %v", err)
	}
	w := &fakeWeb{replies: []string{reply(`{"status_code":2154}`), reply(`{"status_code":0,"is_digg":0}`)},
		cookies: []Cookie{{Name: "sessionid", Value: "s"}, {Name: "ttwid", Value: "t"}}}
	c.SetWeb(w)
	if err := c.SetLiked("7", true); err != nil {
		t.Fatal(err)
	}
	if len(w.scripts) != 2 || !strings.Contains(w.scripts[0], "/aweme/v1/web/commit/item/digg/") || !strings.Contains(w.scripts[0], `"aweme_id=7\u0026item_type=0\u0026type=1"`) {
		t.Fatalf("scripts: %q", w.scripts)
	}
	if got := c.cookies().cookie; got != "sessionid=s; ttwid=t" {
		t.Fatalf("cookies after: %q", got)
	}
	w.replies, w.scripts = []string{reply(`{"status_code":8,"status_msg":"用户未登录"}`)}, nil
	if err := c.SetSaved("7", true); !errors.Is(err, ErrNeedLogin) || len(w.scripts) != 1 {
		t.Fatalf("logged out: %v after %d tries", err, len(w.scripts))
	}
}
