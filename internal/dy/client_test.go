package dy

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"os"
	"testing"
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
