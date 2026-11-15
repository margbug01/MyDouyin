package dy

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"os"
	"testing"
	"time"
)

// The signed feed request returns playable videos, and the first one downloads
// as an mp4. Live: skipped unless LIVE is set.
func TestLiveFeed(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	var vs []Video
	for range 2 { // the first page is short
		more, err := c.Feed()
		if err != nil {
			t.Fatal(err)
		}
		vs = append(vs, more...)
	}
	if len(vs) < 3 {
		t.Fatalf("only %d videos", len(vs))
	}
	// Each has a cover and an author's picture that load as images.
	for _, u := range []string{vs[0].Cover, vs[0].Avatar} {
		b, err := c.Image(u)
		if err != nil {
			t.Fatalf("image %.60s: %v", u, err)
		}
		if _, err := ui.DecodeBitmap(b); err != nil {
			t.Fatalf("image %.60s: %v", u, err)
		}
	}
	if vs[0].Created.Year() < 2016 {
		t.Fatalf("created %v", vs[0].Created)
	}
	for _, v := range vs {
		t.Logf("%s %q %dx%d %v %.1fMB", v.ID[len(v.ID)-5:], v.Author, v.W, v.H, v.Duration, float64(v.Size)/1e6)
	}
	// Pick the shortest so the test is quick.
	v := vs[0]
	for _, x := range vs {
		if x.Duration < v.Duration {
			v = x
		}
	}
	dst := t.TempDir() + "/v.mp4"
	if err := c.Download(v.PlayURL, dst); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	if len(b) < 1000 || string(b[4:8]) != "ftyp" {
		t.Fatalf("not an mp4: %d bytes", len(b))
	}
}

// pager skips pages left empty by dropped entries and stops after the last.
func TestPager(t *testing.T) {
	pages := [][]int{{}, {1, 2}, {}, {3}}
	i := 0
	p := pager(func() ([]int, bool, error) {
		i++
		return pages[i-1], i < len(pages), nil
	})
	var got []int
	for range 4 {
		page, err := p()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page...)
	}
	if fmt.Sprint(got) != "[1 2 3]" || i != len(pages) {
		t.Fatalf("got %v after %d fetches", got, i)
	}
}

// Search (two pages), a user's posts and the logged-in user's following list.
// Live: skipped unless LIVE is set.
func TestLiveLists(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	search := c.Search("猫")
	vs, err := search()
	if err != nil || len(vs) == 0 {
		t.Fatalf("search: %d videos, %v", len(vs), err)
	}
	more, err := search()
	if err != nil || len(more) == 0 || more[0].ID == vs[0].ID {
		t.Fatalf("search page 2: %d videos, %v", len(more), err)
	}
	t.Logf("search: %d + %d videos, first by %q", len(vs), len(more), vs[0].Author)

	posts, err := c.Posts(vs[0].AuthorID)()
	if err != nil || len(posts) == 0 || posts[0].Author != vs[0].Author {
		t.Fatalf("posts: %d videos, %v", len(posts), err)
	}
	t.Logf("posts: %d videos", len(posts))

	comments := c.Comments(vs[0].ID)
	cs, err := comments()
	if err != nil || len(cs) == 0 || cs[0].Text == "" || cs[0].Author == "" || cs[0].Time.Year() < 2016 {
		t.Fatalf("comments: %d, %v", len(cs), err)
	}
	cs2, err := comments()
	if err != nil || len(cs2) == 0 || cs2[0].Text == cs[0].Text {
		t.Fatalf("comments page 2: %d, %v", len(cs2), err)
	}
	t.Logf("comments: %d + %d, first %q by %q (%s, %d likes)", len(cs), len(cs2), cs[0].Text, cs[0].Author, cs[0].Place, cs[0].Likes)

	me, err := c.Self()
	if err != nil || me.ID == "" {
		t.Fatalf("self: %v", err)
	}
	us, err := c.Following(me.ID)()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("following: %d accounts", len(us))
	if len(us) > 0 && (us[0].ID == "" || us[0].Name == "") {
		t.Fatalf("following entry incomplete: %+v", us[0])
	}
}

// 精选's tabs give pages of their own, the next page new videos; a video has
// bullet comments within its length; notifications page back in time, each
// with who did it, and the unread count reads. Live: skipped unless LIVE is set.
func TestLiveTabsDanmakuNotices(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tab := range []int{0, 300205} { // 全部, 游戏
		next := c.Picks(tab)
		for page := range 2 {
			vs, err := next()
			if err != nil || len(vs) < 5 {
				t.Fatalf("tab %d page %d: %d videos, %v", tab, page, len(vs), err)
			}
			fresh := 0
			for _, v := range vs {
				if !seen[v.ID] {
					fresh++
				}
				seen[v.ID] = true
			}
			if fresh < len(vs)/2 {
				t.Fatalf("tab %d page %d: only %d of %d new", tab, page, fresh, len(vs))
			}
		}
	}

	v := pick(t, c)
	ds, err := c.Danmaku(v.ID, v.Duration, 0, 32*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.At < 0 || d.At >= 32*time.Second || d.Text == "" {
			t.Fatalf("danmaku %+v outside 0-32s or empty", d)
		}
	}
	t.Logf("%d danmaku in the first 32s of %s", len(ds), v.ID)

	next := c.Notices()
	var last time.Time
	for page := range 2 {
		ns, err := next()
		if err != nil {
			t.Fatal(err)
		}
		if page == 0 && len(ns) == 0 {
			t.Fatal("no notifications")
		}
		for _, n := range ns {
			if n.User.Name == "" || n.Action == "" || !last.IsZero() && n.Time.After(last) {
				t.Fatalf("page %d: %+v", page, n)
			}
			last = n.Time
		}
	}
	if _, err := c.Unread(); err != nil {
		t.Fatal(err)
	}
}

// pick is a video of 精选's 知识 with bullet comments likely: a long one with many comments.
func pick(t *testing.T, c *Client) Video {
	vs, err := c.Picks(300213)()
	if err != nil {
		t.Fatal(err)
	}
	best := vs[0]
	for _, v := range vs {
		if v.Comments > best.Comments {
			best = v
		}
	}
	return best
}

// The inbox reads without a browser: conversations, latest first, each with
// someone named on the other side and messages in time order, some of them
// the user's own. Live: skipped unless LIVE is set.
func TestLiveChats(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	chats, err := c.Chats()
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) == 0 {
		t.Fatal("no chats")
	}
	mine, named := 0, 0
	for i, ch := range chats {
		if ch.Peer.Name != "" {
			named++
		}
		for j, m := range ch.Msgs {
			if j > 0 && m.Time.Before(ch.Msgs[j-1].Time) {
				t.Fatalf("chat %d: messages out of order", i)
			}
			if m.Mine {
				mine++
			}
		}
		if i > 0 && last(ch).After(last(chats[i-1])) {
			t.Fatalf("chat %d is newer than the one before it", i)
		}
	}
	if named < len(chats)*3/4 || mine == 0 {
		t.Fatalf("%d chats, %d named, %d messages mine", len(chats), named, mine)
	}
	t.Logf("%d chats, %d named, %d of the messages mine; first with %s: %q", len(chats), named, mine, chats[0].Peer.Name, last1(chats[0]))
}

func last1(ch Chat) string {
	if len(ch.Msgs) == 0 {
		return ""
	}
	return ch.Msgs[len(ch.Msgs)-1].Text
}
