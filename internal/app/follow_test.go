package app

import (
	"errors"
	"testing"
	"time"

	"douyin/internal/dy"
)

func TestFollowFromVideoRailAndProfileHeaderSyncsState(t *testing.T) {
	type call struct {
		uid, secUID string
		on          bool
	}
	var calls []call
	var failNext bool

	p := stub("推荐", 1)
	p.list[0].v = dy.Video{
		ID:        "v1",
		Author:    "博主甲",
		AuthorID:  "sec-author",
		AuthorUID: "uid-author",
	}
	ready(t, p.list[0])

	var a *app
	a = withRoots(&app{
		pages:    []*page{p},
		signedIn: true,
		me:       dy.User{ID: "sec-me", Name: "我自己", Following: 10},
		posted:   make(chan func(), 16),
		setFollowed: func(uid, secUID string, on bool) (int, error) {
			calls = append(calls, call{uid, secUID, on})
			if failNext {
				failNext = false
				return 0, errors.New("网络异常")
			}
			if on {
				return 2, nil // 互相关注
			}
			return 0, nil
		},
		openAuthorFn: func(_ *page, name, id string) {
			u := dy.User{ID: id, UID: "uid-author", Name: name, Followers: 100}
			a.open(&page{title: "@" + name, authorID: id, user: &u, grid: true, done: true})
		},
	})
	pump := func() {
		t.Helper()
		select {
		case fn := <-a.posted:
			fn()
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for follow result")
		}
	}

	tt := newTester(a)
	tt.Frame()
	if err := tt.Click("关注作者"); err != nil {
		t.Fatalf("click rail follow: %v", err)
	}
	pump()
	tt.Frame()
	if len(calls) != 1 || calls[0] != (call{"uid-author", "sec-author", true}) {
		t.Fatalf("unexpected follow call: %+v", calls)
	}
	if a.followStatus("sec-author", 0) != 2 || a.me.Following != 11 {
		t.Fatalf("follow status=%d following=%d", a.followStatus("sec-author", 0), a.me.Following)
	}
	if _, ok := tt.Find("取消关注作者"); !ok {
		t.Fatalf("rail badge did not switch to followed state: %q", tt.Texts())
	}

	// Open the author's profile and verify it immediately reflects 互相关注.
	if err := tt.Click("作者主页"); err != nil {
		t.Fatalf("open author profile: %v", err)
	}
	tt.Frame()
	if !tt.HasText("互相关注") {
		t.Fatalf("profile header missing synced follow status: %q", tt.Texts())
	}

	// Failed unfollow reverts to 互相关注 and keeps follower count intact.
	failNext = true
	if err := tt.Click("取消关注"); err != nil {
		t.Fatalf("click profile unfollow (fail): %v", err)
	}
	pump()
	tt.Frame()
	if a.followStatus("sec-author", 0) != 2 || a.cur().user.Followers != 100 {
		t.Fatalf("failed unfollow did not revert: st=%d followers=%d", a.followStatus("sec-author", 0), a.cur().user.Followers)
	}

	// Successful unfollow on profile updates follower count and syncs back to video rail.
	if err := tt.Click("取消关注"); err != nil {
		t.Fatalf("click profile unfollow: %v", err)
	}
	pump()
	tt.Frame()
	if a.followStatus("sec-author", 0) != 0 || a.cur().user.Followers != 99 || a.me.Following != 10 {
		t.Fatalf("unfollow state wrong: st=%d followers=%d following=%d", a.followStatus("sec-author", 0), a.cur().user.Followers, a.me.Following)
	}
	if !tt.HasText("关注") {
		t.Fatalf("profile button did not revert to 关注: %q", tt.Texts())
	}

	a.back()
	tt.Frame()
	if _, ok := tt.Find("关注作者"); !ok {
		t.Fatalf("video rail did not reflect unfollow from profile: %q", tt.Texts())
	}
}

func TestSelfProfileHasNoFollowButton(t *testing.T) {
	a := withRoots(&app{
		pages:    []*page{stub("推荐", 1)},
		signedIn: true,
		me:       dy.User{ID: "sec-me", Name: "我自己"},
	})
	a.mine = map[string]*page{"作品": {title: "我的", grid: true, done: true}}
	a.mineTab = "作品"
	a.pages = []*page{a.mine["作品"]}
	tt := newTester(a)
	tt.Frame()
	if _, ok := tt.Find("取消关注"); ok {
		t.Fatal("self profile must not show unfollow button")
	}
	// Only the stats label 关注 should exist, no profile-follow button on self.
	if a.followStatus("sec-me", 0) != 0 {
		t.Fatal("unexpected follow state for self")
	}
}
