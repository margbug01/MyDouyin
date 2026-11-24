package app

import (
	"slices"
	"strings"
	"testing"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

func TestPinBadgeOnlyOnWorks(t *testing.T) {
	p := stub("@作者", 3)
	p.grid, p.authorID = true, "author"
	p.list[0].v.Pinned, p.list[1].v.Pinned = true, true
	tt := newTester(withRoots(&app{pages: []*page{p}}))
	countBadges := func() int {
		return len(slices.DeleteFunc(tt.Texts(), func(s string) bool { return s != "置顶" }))
	}
	if got := countBadges(); got != 2 {
		t.Fatalf("works grid: %d pin badges, want 2", got)
	}
	p.authorID = "" // the same videos in a search or a feed
	tt.Frame()
	if got := countBadges(); got != 0 {
		t.Fatalf("other grid: %d pin badges, want 0", got)
	}
}

func TestCommentInlineLinkClickAndGroupEscape(t *testing.T) {
	a := withRoots(&app{pages: []*page{stub("推荐", 1)}})
	const target = "https://example.org/comment"
	const text = "看看这个链接，后面还有正文"
	start := strings.Index(text, "链接")
	a.sheet = &sheet{done: true, items: []dy.Comment{{Author: "甲", Text: text, Links: []dy.CommentLink{
		{Start: start, End: start + len("链接"), Kind: dy.CommentURL, Target: target},
	}}}}
	tt := newTester(a)
	if err := tt.Click("链接"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tt.OpenedURLs(), []string{target}) {
		t.Fatalf("opened %v", tt.OpenedURLs())
	}
	if !tt.HasText("，后面还有正文") {
		t.Fatalf("after the link: %q", tt.Texts())
	}

	a.group = groupPreview{open: true, data: &groupDetails{group: dy.CommentGroup{Name: "车友群", Members: 12}}}
	a.group.data.group.Owner.Name, a.group.data.group.Owner.SecUID = "小王", "owner-id"
	tt.Frame()
	if !tt.HasText("群主 · 小王") || !tt.HasText("车友群 · 12 人") {
		t.Fatalf("group preview: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	if a.group.open || a.sheet == nil {
		t.Fatal("Escape should close the invite, leaving comments open")
	}
}

func TestCommentAuthorAvatarAndNameOpenProfile(t *testing.T) {
	var a *app
	a = withRoots(&app{
		pages: []*page{stub("推荐", 1)},
		openAuthorFn: func(_ *page, name, id string) {
			a.open(&page{title: "@" + name, authorID: id, grid: true, done: true})
		},
	})
	sh := &sheet{
		done: true,
		items: []dy.Comment{{
			ID:       "c1",
			Author:   "评论甲",
			AuthorID: "sec-jia",
			Text:     "你好",
			Replies:  1,
		}},
		threads: map[string]*thread{
			"c1": {
				open: true,
				done: true,
				items: []dy.Comment{{
					ID:        "r1",
					Author:    "回复乙",
					AuthorID:  "sec-yi",
					ReplyTo:   "被回复丙",
					ReplyToID: "sec-bing",
					Text:      "回你",
				}},
			},
		},
	}
	a.sheet = sh
	tt := newTester(a)

	if err := tt.Click("评论甲"); err != nil {
		t.Fatalf("click comment author name: %v", err)
	}
	if cur := a.cur(); cur.authorID != "sec-jia" || cur.title != "@评论甲" || a.sheet != nil {
		t.Fatalf("author page not opened from name: cur=%+v sheet=%v", cur, a.sheet != nil)
	}

	// Return and click avatar of a reply.
	a.back()
	a.sheet = sh
	tt.Frame()
	if err := tt.Click("查看 回复乙 主页"); err != nil {
		t.Fatalf("click reply avatar: %v", err)
	}
	if cur := a.cur(); cur.authorID != "sec-yi" || cur.title != "@回复乙" {
		t.Fatalf("author page not opened from reply avatar: cur=%+v", cur)
	}

	// Return and click ReplyTo target name.
	a.back()
	a.sheet = sh
	tt.Frame()
	if err := tt.Click("被回复丙"); err != nil {
		t.Fatalf("click reply-to name: %v", err)
	}
	if cur := a.cur(); cur.authorID != "sec-bing" || cur.title != "@被回复丙" {
		t.Fatalf("author page not opened from reply-to name: cur=%+v", cur)
	}
}
