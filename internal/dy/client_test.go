package dy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"douyin/internal/sign"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/egoist/mygo/ui"
	"io"
	"math/big"
	"net/http"
	"os"
	"slices"
	"strings"
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
	var clips []Video // live rooms come in the feed too, with no file to fetch
	for _, v := range vs {
		if v.Live == nil {
			clips = append(clips, v)
		}
	}
	vs = clips
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
	if err != nil || len(cs) == 0 || cs[0].Text == "" || cs[0].Author == "" || cs[0].Avatar == "" || cs[0].Time.Year() < 2016 {
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

// The hot search list, the user's likes, a video's related ones and a series'
// episodes all read, the series in episode order. Live: skipped unless LIVE is set.
func TestLiveHotLikedRelatedMix(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	hot, err := c.HotSearch()
	if err != nil || len(hot) < 10 || hot[1].Value <= 0 {
		t.Fatalf("hot: %d words, %v", len(hot), err)
	}
	me, err := c.Self()
	if err != nil {
		t.Fatal(err)
	}
	liked, err := c.Liked(me.ID)()
	if err != nil || len(liked) == 0 {
		t.Fatalf("liked: %d, %v", len(liked), err)
	}
	vs, err := c.Picks(0)()
	if err != nil {
		t.Fatal(err)
	}
	v := vs[0]
	for _, x := range vs {
		if x.Live == nil { // a room has no related videos
			v = x
			break
		}
	}
	rel, err := c.Related(v.ID)()
	if err != nil || len(rel) < 3 {
		t.Fatalf("related: %d, %v", len(rel), err)
	}
	var mix Mix
	for _, tab := range []int{0, 300214, 300215, 300213} {
		vs, _ := c.Picks(tab)()
		for _, v := range vs {
			if v.Mix.ID != "" && v.Mix.Updated >= 2 {
				mix = v.Mix
			}
		}
		if mix.ID != "" {
			break
		}
	}
	if mix.ID == "" {
		t.Skip("no series in 精选 now")
	}
	eps, err := c.MixVideos(mix.ID)()
	if err != nil || len(eps) < 2 || eps[0].Mix.Episode != 1 || eps[1].Mix.Episode != 2 || eps[0].Mix.Name != mix.Name {
		t.Fatalf("series %q: %d videos, first %+v, %v", mix.Name, len(eps), eps[0].Mix, err)
	}
	t.Logf("hot %q; %d liked; %d related; series %q has %d", hot[1].Word, len(liked), len(rel), mix.Name, len(eps))
}

// 精选 now and then carries a live room, which decodes to its owner, its
// title and an https HLS stream that answers as a playlist. Live: skipped
// unless LIVE is set, and when no room comes in ten pages.
func TestLiveRoom(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	next := c.Picks(0)
	for range 10 {
		vs, err := next()
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vs {
			if v.Live == nil {
				continue
			}
			l := v.Live
			if v.Author == "" || v.AuthorID == "" || l.WebRID == "" || v.Cover == "" || !strings.HasPrefix(l.HLS, "https://") {
				t.Fatalf("room %+v %+v", v, *l)
			}
			resp, err := http.Get(l.HLS)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 16))
			resp.Body.Close()
			if !strings.HasPrefix(string(b), "#EXTM3U") {
				t.Fatalf("stream answers %q", b)
			}
			t.Logf("%s: %q, %s watching", v.Author, v.Desc, l.Viewers)
			return
		}
	}
	t.Skip("no live room in ten pages")
}

// The pages of the sidebar read: 关注's feed of followed accounts, newest
// first, over more than a page; the user's saves and watch history; the live
// list's rooms, each on air with a stream; and the user's own counts.
// Live: skipped unless LIVE is set.
func TestLiveSidebarPages(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	me, err := c.Self()
	if err != nil || me.Following == 0 {
		t.Fatalf("self: %+v %v", me, err)
	}
	follows, _ := c.Following(me.ID)()
	followed := map[string]bool{}
	for _, u := range follows {
		followed[u.ID] = true
	}
	next := c.FollowFeed()
	var feed []Video
	for range 2 {
		vs, err := next()
		if err != nil {
			t.Fatal(err)
		}
		feed = append(feed, vs...)
	}
	if len(feed) < 10 {
		t.Fatalf("follow feed: %d videos", len(feed))
	}
	for _, v := range feed {
		if !followed[v.AuthorID] && v.Live == nil {
			t.Logf("follow feed has %q, not among the first %d followed", v.Author, len(follows))
		}
	}
	mine, err := c.MyPosts(me.ID)()
	if err != nil || len(mine) == 0 {
		t.Fatalf("my posts: %d %v", len(mine), err)
	}
	saved, err := c.Collected()()
	if err != nil || len(saved) == 0 {
		t.Fatalf("collected: %d %v", len(saved), err)
	}
	seen, err := c.History()()
	if err != nil || len(seen) == 0 {
		t.Fatalf("history: %d %v", len(seen), err)
	}
	rooms, err := c.LiveRooms()()
	if err != nil || len(rooms) < 5 {
		t.Fatalf("rooms: %d %v", len(rooms), err)
	}
	for _, r := range rooms {
		if r.Live == nil || r.Live.WebRID == "" || !strings.HasPrefix(r.Live.HLS, "https://") || r.Author == "" {
			t.Fatalf("room %+v", r)
		}
	}
	t.Logf("%s follows %d, %d followers, %d likes; feed %d (first %s), %d saved, %d watched, %d rooms (first %s, %s)",
		me.Name, me.Following, me.Followers, me.Liked, len(feed), feed[0].Author, len(saved), len(seen), len(rooms), rooms[0].Author, rooms[0].Live.Viewers)
}

// The user search finds accounts, whose profile reads with its counts, and
// the search box's suggestions come for what is typed.
func TestLiveSearchProfile(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	us, err := c.SearchUsers("美食")
	if err != nil || len(us) == 0 || us[0].ID == "" || us[0].Name == "" {
		t.Fatalf("users: %d, %v", len(us), err)
	}
	p, err := c.Profile(us[0].ID)
	if err != nil || p.Name != us[0].Name || p.Followers == 0 || p.Handle == "" {
		t.Fatalf("profile: %+v, %v", p, err)
	}
	ws, err := c.Suggest("美食")
	if err != nil || len(ws) == 0 || !strings.Contains(ws[0], "美食") {
		t.Fatalf("suggest: %q, %v", ws, err)
	}
}

// A comment's replies read, signed with the ticket guard's material the
// app's page saved (guard.json). Live: skipped unless LIVE is set.
func TestLiveReplies(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	rs, err := c.Replies("7694319481270619427", "7694583560485045041")()
	if err != nil || len(rs) == 0 || rs[0].Text == "" || rs[0].Author == "" || rs[0].Avatar == "" {
		t.Fatalf("replies: %d, %v", len(rs), err)
	}
	t.Logf("%d replies, first %q by %q", len(rs), rs[0].Text, rs[0].Author)
}

// A comment's images and sticker, as the lists give them, are its pictures.
func TestCommentPics(t *testing.T) {
	var x comment
	err := json.Unmarshal([]byte(`{"cid":"1","text":"好[赞]","image_list":[{"medium_url":{"url_list":["i1","i2"]}},{"medium_url":{"url_list":[]}}],
		"sticker":{"static_url":{"url_list":["s1"]},"animate_url":{"url_list":["a1"]}}}`), &x)
	if err != nil {
		t.Fatal(err)
	}
	if cm := x.comment(); cm.Text != "好[赞]" || !slices.Equal(cm.Pics, []string{"i1", "s1"}) {
		t.Fatalf("comment: %+v", cm)
	}
	if err := json.Unmarshal([]byte(`{"cid":"2","text":"t","sticker":null,"image_list":null}`), &x); err != nil || x.comment().Pics != nil {
		t.Fatalf("plain comment: %v %v", err, x.comment().Pics)
	}
}

func TestLiveEmojis(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE not set")
	}
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.Emojis()
	if err != nil || len(m) < 100 || m["[赞]"] == "" || m["[捂脸]"] == "" {
		t.Fatalf("%d emoji, [赞] %q: %v", len(m), m["[赞]"], err)
	}
}

// verifyRT answers every request empty, as Douyin does one it holds back
// for its verification, and counts them.
type verifyRT struct{ n *int }

func (v verifyRT) RoundTrip(*http.Request) (*http.Response, error) {
	*v.n++
	h := http.Header{"X-Vc-Bdturing-Parameters": {"eyJ0eXBlIjoidmVyaWZ5In0="}}
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
}

// An answer held back for Douyin's verification is ErrVerify. Replies held
// back are asked of the page, not its material again: ErrVerify while the
// page is held back too, its replies once it passed, and the page is asked
// from then on, the client not.
func TestVerifyHeldBack(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // the guard material and cookies are the user's
	n := 0
	c := &Client{http: &http.Client{Transport: verifyRT{&n}}, signer: sign.New(ua)}
	c.login.Store(&login{cookie: "sessionid=s"})
	testGuard(t)
	if _, err := c.call("GET", "/aweme/v1/web/comment/list/", "", ""); !errors.Is(err, ErrVerify) {
		t.Fatalf("call: %v", err)
	}
	w := &fakeWeb{replies: []string{reply(""), reply(`{"status_code":0,"comments":[{"cid":"r1","text":"回复一"}],"cursor":1,"has_more":0}`)}}
	c.SetWeb(w)
	sent := n
	if _, err := c.Replies("1", "2")(); !errors.Is(err, ErrVerify) || len(w.scripts) != 1 || !strings.Contains(w.scripts[0], "/comment/list/reply/") {
		t.Fatalf("held back in the page too: %v, after %d scripts", err, len(w.scripts))
	}
	rs, err := c.Replies("1", "2")()
	if err != nil || len(rs) != 1 || rs[0].Text != "回复一" || n != sent+1 {
		t.Fatalf("passed in the page: %v %v, the client asked %d times more", rs, err, n-sent)
	}
}

// testGuard saves ticket guard material of a key and certificate made up.
func testGuard(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotAfter: time.Now().Add(time.Hour)}
	cert, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(guard{Ticket: "t", TsSign: "ts.2.x", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		ServerCert: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}))})
	f, err := guardFile()
	if err == nil {
		err = writePrivate(f, b)
	}
	if err != nil {
		t.Fatal(err)
	}
}
