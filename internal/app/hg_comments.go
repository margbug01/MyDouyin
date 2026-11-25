package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"douyin/internal/hg"
)

const (
	hgDrawerTabEpisodes = 0
	hgDrawerTabComments = 1
	hgDrawerTabDanmaku  = 2

	hgCommentTabEpisode = 0
	hgCommentTabSeries  = 1
)

// hgReplyThread holds the expanded reply list for one Hongguo comment or review.
type hgReplyThread struct {
	open    bool
	items   []hg.Comment
	seen    map[string]bool
	total   int64
	cursor  string
	hasMore bool
	loaded  bool
	loading bool
	err     string
	gen     int
}

func (th *hgReplyThread) appendUnique(items []hg.Comment) {
	if th.seen == nil {
		th.seen = map[string]bool{}
	}
	for _, it := range items {
		if it.ID != "" {
			if th.seen[it.ID] {
				continue
			}
			th.seen[it.ID] = true
		}
		th.items = append(th.items, it)
	}
}

// hgCommentFeed holds a paginated list of Hongguo episode comments (episodeID != "")
// or whole-series reviews (episodeID == ""), plus per-comment reply threads.
type hgCommentFeed struct {
	seriesID   string
	episodeID  string
	items      []hg.Comment
	seen       map[string]bool
	total      int64
	score      string
	scoreCount int64
	cursor     string
	hasMore    bool
	loaded     bool
	loading    bool
	err        string
	gen        int
	threads    map[string]*hgReplyThread
	list       ui.ListState
}

func newHGCommentFeed(seriesID, episodeID string) *hgCommentFeed {
	return &hgCommentFeed{
		seriesID:  seriesID,
		episodeID: episodeID,
		seen:      map[string]bool{},
		threads:   map[string]*hgReplyThread{},
	}
}

func (f *hgCommentFeed) reset(seriesID, episodeID string) {
	f.gen++
	f.seriesID = seriesID
	f.episodeID = episodeID
	f.items = nil
	f.seen = map[string]bool{}
	f.total = 0
	f.score = ""
	f.scoreCount = 0
	f.cursor = ""
	f.hasMore = false
	f.loaded = false
	f.loading = false
	f.err = ""
	f.threads = map[string]*hgReplyThread{}
	f.list = ui.ListState{}
}

func (f *hgCommentFeed) appendUnique(items []hg.Comment) {
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	for _, it := range items {
		if it.ID != "" {
			if f.seen[it.ID] {
				continue
			}
			f.seen[it.ID] = true
		}
		f.items = append(f.items, it)
	}
}

func (f *hgCommentFeed) isSeries() bool {
	return f == nil || f.episodeID == ""
}

func (a *app) hgLoadCommentFeed(f *hgCommentFeed, reset bool) {
	h := a.hg
	if h == nil || h.svc == nil || f == nil || f.seriesID == "" {
		return
	}
	if reset {
		f.reset(f.seriesID, f.episodeID)
	} else if f.loading || (f.loaded && !f.hasMore) {
		return
	}
	a.ensureEmojis()
	f.loading = true
	f.err = ""
	gen := f.gen
	seriesID, episodeID, cursor := f.seriesID, f.episodeID, f.cursor
	svc := h.svc

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		var (
			page hg.CommentPage
			err  error
		)
		if episodeID != "" {
			page, err = svc.ListEpisodeComments(ctx, seriesID, episodeID, cursor)
		} else {
			page, err = svc.ListSeriesComments(ctx, seriesID, cursor)
		}
		cancel()
		a.post(func() {
			if f.gen != gen {
				return
			}
			f.loading = false
			if err != nil {
				f.err = hg.ClassifyError(err).Message
				if f.err == "" {
					f.err = "评论加载失败，请重试"
				}
				return
			}
			f.loaded = true
			f.err = ""
			f.appendUnique(page.Items)
			if page.Total > f.total {
				f.total = page.Total
			}
			if int64(len(f.items)) > f.total {
				f.total = int64(len(f.items))
			}
			if cursor == "" && page.Score != "" {
				f.score = page.Score
				f.scoreCount = page.ScoreCount
			}
			f.hasMore = page.HasMore && page.Cursor != "" && page.Cursor != cursor
			f.cursor = page.Cursor
		})
	}()
}

func (a *app) hgToggleCommentReplies(f *hgCommentFeed, cm hg.Comment) {
	if f == nil || cm.ID == "" {
		return
	}
	if f.threads == nil {
		f.threads = map[string]*hgReplyThread{}
	}
	th := f.threads[cm.ID]
	if th == nil {
		th = &hgReplyThread{seen: map[string]bool{}}
		f.threads[cm.ID] = th
	}
	if th.open {
		th.open = false
		return
	}
	th.open = true
	if !th.loaded && !th.loading {
		a.hgLoadCommentReplies(f, cm.ID, false)
	}
}

func (a *app) hgLoadCommentReplies(f *hgCommentFeed, commentID string, reset bool) {
	h := a.hg
	if h == nil || h.svc == nil || f == nil || f.seriesID == "" || commentID == "" {
		return
	}
	if f.threads == nil {
		f.threads = map[string]*hgReplyThread{}
	}
	th := f.threads[commentID]
	if th == nil {
		th = &hgReplyThread{open: true, seen: map[string]bool{}}
		f.threads[commentID] = th
	}
	if reset {
		th.gen++
		th.items = nil
		th.seen = map[string]bool{}
		th.cursor = ""
		th.hasMore = false
		th.loaded = false
		th.err = ""
	} else if th.loading || (th.loaded && !th.hasMore) {
		return
	}
	a.ensureEmojis()
	th.open = true
	th.loading = true
	th.err = ""
	feedGen, thGen := f.gen, th.gen
	seriesID, episodeID, cursor := f.seriesID, f.episodeID, th.cursor
	svc := h.svc

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hgRequestTimeout)
		var (
			page hg.CommentPage
			err  error
		)
		if episodeID != "" {
			page, err = svc.ListEpisodeReplies(ctx, seriesID, episodeID, commentID, cursor)
		} else {
			page, err = svc.ListSeriesReplies(ctx, seriesID, commentID, cursor)
		}
		cancel()
		a.post(func() {
			if f.gen != feedGen || th.gen != thGen {
				return
			}
			th.loading = false
			if err != nil {
				th.err = hg.ClassifyError(err).Message
				if th.err == "" {
					th.err = "回复加载失败"
				}
				return
			}
			th.loaded = true
			th.err = ""
			th.appendUnique(page.Items)
			if page.Total > th.total {
				th.total = page.Total
			}
			th.hasMore = page.HasMore && page.Cursor != "" && page.Cursor != cursor
			th.cursor = page.Cursor
		})
	}()
}

// hgEnsureActiveComments ensures the active sub-tab feed in the player drawer has started loading.
func (a *app) hgEnsureActiveComments(hp *hgPlayer) {
	if hp == nil {
		return
	}
	if hp.commentTab == hgCommentTabSeries {
		if hp.seriesComments == nil || hp.seriesComments.seriesID != hp.series.ID {
			hp.seriesComments = newHGCommentFeed(hp.series.ID, "")
		}
		if !hp.seriesComments.loaded && !hp.seriesComments.loading {
			a.hgLoadCommentFeed(hp.seriesComments, false)
		}
		return
	}
	ep, ok := hp.currentEpisode()
	if !ok {
		return
	}
	if hp.epComments == nil || hp.epComments.seriesID != hp.series.ID || hp.epComments.episodeID != ep.ID {
		hp.epComments = newHGCommentFeed(hp.series.ID, ep.ID)
	}
	if !hp.epComments.loaded && !hp.epComments.loading {
		a.hgLoadCommentFeed(hp.epComments, false)
	}
}

type hgCommentRowKind uint8

const (
	hgRowComment hgCommentRowKind = iota
	hgRowReply
	hgRowReplyActions
	hgRowFooter
)

type hgCommentRow struct {
	kind hgCommentRowKind
	ci   int
	ri   int
}

func flattenHGCommentRows(f *hgCommentFeed) []hgCommentRow {
	rows := make([]hgCommentRow, 0, len(f.items)*2+1)
	for i, cm := range f.items {
		rows = append(rows, hgCommentRow{kind: hgRowComment, ci: i})
		th := f.threads[cm.ID]
		if th != nil && th.open {
			for j := range th.items {
				rows = append(rows, hgCommentRow{kind: hgRowReply, ci: i, ri: j})
			}
		}
		if cm.ReplyCount > 0 || (th != nil && (th.open || len(th.items) > 0)) {
			rows = append(rows, hgCommentRow{kind: hgRowReplyActions, ci: i})
		}
	}
	rows = append(rows, hgCommentRow{kind: hgRowFooter})
	return rows
}

func (a *app) hgCommentsDrawerBody(c *ui.Context, h *hgApp, hp *hgPlayer) {
	a.hgEnsureActiveComments(hp)

	epLabel := "本集评论"
	if hp.epComments != nil && hp.epComments.total > 0 {
		epLabel = fmt.Sprintf("本集评论 %s", count(int(hp.epComments.total)))
	}
	srLabel := "剧评"
	if hp.seriesComments != nil && hp.seriesComments.total > 0 {
		srLabel = fmt.Sprintf("剧评 %s", count(int(hp.seriesComments.total)))
	}

	ui.Row(c.Key("hg-cm-subtabs")).Padding(10, 14, 6, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		if a.hgPill(c, "cmtab:ep", epLabel, hp.commentTab == hgCommentTabEpisode) {
			hp.commentTab = hgCommentTabEpisode
			a.hgEnsureActiveComments(hp)
		}
		if a.hgPill(c, "cmtab:series", srLabel, hp.commentTab == hgCommentTabSeries) {
			hp.commentTab = hgCommentTabSeries
			a.hgEnsureActiveComments(hp)
		}
	})

	feed := hp.epComments
	emptyText := "本集暂无评论"
	if hp.commentTab == hgCommentTabSeries {
		feed = hp.seriesComments
		emptyText = "暂无剧评"
	}
	if feed == nil {
		ui.Column(c.Key("hg-cm-nil")).Grow(1).Center().Padding(20).Children(func() {
			ui.Text(c, emptyText).FontSize(12.5).TextColor(pal.faint)
		})
		return
	}

	if feed.isSeries() && feed.score != "" {
		ui.Row(c.Key("hg-cm-scorebar")).Padding(2, 14, 8, 14).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Height(22).PaddingX(8).Radius(11).AlignItems(ui.Center).Background(pal.accent.Alpha(0.14)).Children(func() {
				ui.Text(c, feed.score+" 分").FontSize(11.5).FontWeight(700).TextColor(pal.accent).SingleLine()
			})
			if feed.scoreCount > 0 {
				ui.Text(c, fmt.Sprintf("%s 人评分", count(int(feed.scoreCount)))).FontSize(11.5).TextColor(pal.faint).SingleLine()
			}
		})
	}

	rows := flattenHGCommentRows(feed)
	ui.List(c.Key(fmt.Sprintf("hg-cm-list:%s:%s:%d", feed.seriesID, feed.episodeID, hp.commentTab)), &feed.list, len(rows), func(row int) {
		r := rows[row]
		switch r.kind {
		case hgRowComment:
			cm := feed.items[r.ci]
			topPad := float32(10)
			if r.ci == 0 {
				topPad = 4
			}
			ui.Row(c.Key("hgcm:"+cm.ID)).Padding(topPad, 14, 4, 14).Gap(10).AlignItems(ui.Start).Children(func() {
				a.hgAvatar(c, h, cm.AvatarURL, cm.Author, 28)
				ui.Column(c).Gap(4).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					a.hgCommentContent(c, cm, 13, false)
				})
			})
		case hgRowReply:
			cm := feed.items[r.ci]
			th := feed.threads[cm.ID]
			if th == nil || r.ri >= len(th.items) {
				return
			}
			rep := th.items[r.ri]
			ui.Row(c.Key("hgrep:"+rep.ID)).Padding(5, 14, 2, 52).Gap(8).AlignItems(ui.Start).Children(func() {
				a.hgAvatar(c, h, rep.AvatarURL, rep.Author, 20)
				ui.Column(c).Gap(3).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					a.hgCommentContent(c, rep, 12, true)
				})
			})
		case hgRowReplyActions:
			cm := feed.items[r.ci]
			ui.Column(c.Key("hgrepa:"+cm.ID)).Padding(2, 14, 8, 52).Gap(3).Children(func() {
				a.hgReplyActions(c, feed, cm)
			})
		case hgRowFooter:
			ui.Row(c.Key("hg-cm-foot")).Padding(12, 14, 18, 14).Justify(ui.Center).Children(func() {
				switch {
				case feed.loading:
					ui.Text(c, "正在加载评论…").FontSize(12).TextColor(pal.faint)
				case feed.err != "":
					ui.Column(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, feed.err).FontSize(12).TextColor(pal.faint)
						if ui.Button(c.Key("hg-cm-retry"), "重试").Clicked() {
							a.hgLoadCommentFeed(feed, len(feed.items) == 0)
						}
					})
				case feed.hasMore:
					if ui.Button(c.Key("hg-cm-more"), "加载更多评论").Clicked() {
						a.hgLoadCommentFeed(feed, false)
					}
				case len(feed.items) == 0:
					ui.Text(c, emptyText).FontSize(12.5).TextColor(pal.faint)
				default:
					ui.Text(c, "已经到底了").FontSize(11.5).TextColor(pal.faint)
				}
			})
		}
	}).Grow(1)
}

func (a *app) hgCommentContent(c *ui.Context, cm hg.Comment, size float32, isReply bool) {
	nameSize := size - 1.5
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		who := hgFirstNonEmpty(cm.Author, "红果用户")
		if isReply && cm.ReplyTo != "" {
			who += " ▸ " + cm.ReplyTo
		}
		ui.Text(c, who).FontSize(nameSize).FontWeight(600).TextColor(pal.muted).SingleLine().Shrink(1).MinWidth(0)
		if cm.Score != "" {
			ui.Row(c).Height(18).PaddingX(6).Radius(9).AlignItems(ui.Center).Background(pal.accent.Alpha(0.14)).Children(func() {
				ui.Text(c, cm.Score+"分").FontSize(10.5).FontWeight(700).TextColor(pal.accent).SingleLine()
			})
		}
	})

	if cm.Text != "" {
		spans, pics := a.emojiSpans(cm.Text, size, 0)
		if len(pics) == 0 {
			ui.Text(c, cm.Text).FontSize(size).TextColor(pal.text)
		} else {
			ui.RichText(c, spans...).FontSize(size).TextColor(pal.text).Label(cm.Text).DrawObjects(pics.draw)
		}
	}

	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		when := ago(cm.CreatedAt, time.Now())
		if when != "" {
			ui.Text(c, when).FontSize(11).TextColor(pal.faint).SingleLine().Grow(1)
		} else {
			ui.Spacer(c)
		}
		if cm.Likes > 0 {
			ui.Row(c).Gap(3).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconHeart).Size(11, 11).TextColor(pal.faint)
				ui.Text(c, count(int(cm.Likes))).FontSize(11).TextColor(pal.faint).SingleLine()
			})
		}
	})
}

func (a *app) hgReplyActions(c *ui.Context, f *hgCommentFeed, cm hg.Comment) {
	th := f.threads[cm.ID]
	open := th != nil && th.open
	if open && th.err != "" {
		ui.Text(c, th.err).FontSize(11.5).TextColor(pal.faint)
	}
	link := func(key, label string) bool {
		b := ui.ButtonBase(c.Key(key)).Padding(2, 4).Radius(4).FocusRing(false).Cursor(ui.CursorPointer).Label(label)
		b.Children(func() {
			ui.Text(c, label).FontSize(11.5).FontWeight(600).TextColor(onOff(b.Hovered(), pal.accent, pal.muted)).SingleLine()
		})
		return b.Clicked()
	}
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		switch {
		case !open:
			total := cm.ReplyCount
			if th != nil && th.total > total {
				total = th.total
			}
			label := "展开回复"
			if total > 0 {
				label = fmt.Sprintf("展开 %s 条回复", count(int(total)))
			}
			if link("exp:"+cm.ID, label) {
				a.hgToggleCommentReplies(f, cm)
			}
		case th.loading:
			ui.Text(c, "载入回复中…").FontSize(11.5).TextColor(pal.faint)
		default:
			if th.err != "" {
				if link("rretry:"+cm.ID, "重试") {
					a.hgLoadCommentReplies(f, cm.ID, len(th.items) == 0)
				}
			} else if th.hasMore {
				if link("rmore:"+cm.ID, "展开更多回复") {
					a.hgLoadCommentReplies(f, cm.ID, false)
				}
			}
			if link("rclose:"+cm.ID, "收起") {
				th.open = false
			}
		}
	})
}

// hgDetailReviewsSection renders whole-series reviews on the Series Detail page.
func (a *app) hgDetailReviewsSection(c *ui.Context, h *hgApp, p *hgPage) {
	if p == nil || p.reviews == nil {
		return
	}
	f := p.reviews
	ui.Column(c.Key("hg-detail-reviews:"+p.seriesID)).Gap(12).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "剧评").FontSize(15).FontWeight(700).TextColor(pal.strong)
			if f.score != "" {
				ui.Row(c).Height(22).PaddingX(8).Radius(11).AlignItems(ui.Center).Background(pal.accent.Alpha(0.14)).Children(func() {
					ui.Text(c, f.score+" 分").FontSize(11.5).FontWeight(700).TextColor(pal.accent).SingleLine()
				})
			}
			var meta []string
			if f.scoreCount > 0 {
				meta = append(meta, fmt.Sprintf("%s 人评分", count(int(f.scoreCount))))
			}
			if f.total > 0 {
				meta = append(meta, fmt.Sprintf("%s 条剧评", count(int(f.total))))
			}
			if len(meta) > 0 {
				ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(pal.muted).SingleLine()
			}
		})

		if f.err != "" && len(f.items) == 0 {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(12).Radius(12).Background(pal.raised).Border(1, pal.hairline).Children(func() {
				ui.Text(c, f.err).FontSize(12.5).TextColor(pal.muted).Grow(1)
				if ui.Button(c.Key("hg-rev-retry"), "重试").Clicked() {
					a.hgLoadCommentFeed(f, true)
				}
			})
			return
		}
		if f.loading && len(f.items) == 0 {
			ui.Text(c, "正在加载剧评…").FontSize(12.5).TextColor(pal.faint)
			return
		}
		if len(f.items) == 0 {
			ui.Text(c, "暂无剧评").FontSize(12.5).TextColor(pal.faint)
			return
		}

		ui.Column(c.Key("hg-rev-cards")).Gap(10).Children(func() {
			for _, cm := range f.items {
				ui.Column(c.Key("hgrev:"+cm.ID)).FillWidth().Padding(14).Gap(8).Radius(12).
					Background(pal.raised).Border(1, pal.hairline).Children(func() {
					ui.Row(c).Gap(10).AlignItems(ui.Start).Children(func() {
						a.hgAvatar(c, h, cm.AvatarURL, cm.Author, 32)
						ui.Column(c).Gap(4).Grow(1).Shrink(1).MinWidth(0).Children(func() {
							a.hgCommentContent(c, cm, 13, false)
						})
					})
					th := f.threads[cm.ID]
					if th != nil && th.open && len(th.items) > 0 {
						ui.Column(c).Padding(6, 0, 2, 42).Gap(8).Children(func() {
							for _, rep := range th.items {
								ui.Row(c.Key("hgrevrep:"+rep.ID)).Gap(8).AlignItems(ui.Start).Children(func() {
									a.hgAvatar(c, h, rep.AvatarURL, rep.Author, 22)
									ui.Column(c).Gap(3).Grow(1).Shrink(1).MinWidth(0).Children(func() {
										a.hgCommentContent(c, rep, 12.5, true)
									})
								})
							}
						})
					}
					if cm.ReplyCount > 0 || (th != nil && (th.open || len(th.items) > 0)) {
						ui.Column(c).Padding(0, 0, 0, 42).Children(func() {
							a.hgReplyActions(c, f, cm)
						})
					}
				})
			}
		})

		if f.loading {
			ui.Text(c, "正在加载更多剧评…").FontSize(12).TextColor(pal.faint)
		} else if f.hasMore {
			ui.Row(c).Children(func() {
				if ui.Button(c.Key("hg-rev-more"), "查看更多剧评").Clicked() {
					a.hgLoadCommentFeed(f, false)
				}
			})
		}
	})
}
