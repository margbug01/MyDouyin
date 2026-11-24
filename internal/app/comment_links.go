package app

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

var commentLinkColor = ui.Hex("#f9cc66")

// MyGo lays out a RichText's child links inline, including links that wrap.
func (a *app) commentRichText(c *ui.Context, sh *sheet, cm dy.Comment, size float32) {
	pics := emojiPics{}
	offset := 0
	textSpans := func(text string) []ui.Span {
		spans, images := a.emojiSpans(text, size, 0)
		for pos, bmp := range images {
			pics[offset+pos] = bmp
		}
		for _, span := range spans {
			offset += utf8.RuneCountInString(span.Text)
		}
		return spans
	}
	first := cm.Links[0].Start
	ui.RichText(c, textSpans(cm.Text[:first])...).FontSize(size).TextColor(pal.text).DrawObjects(pics.draw).Children(func() {
		last := first
		for _, link := range cm.Links {
			if last < link.Start {
				ui.RichText(c, textSpans(cm.Text[last:link.Start])...)
			}
			label := cm.Text[link.Start:link.End]
			if link.Kind == dy.CommentGroupInvite {
				label = "群聊 · " + strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(label), "[群聊]"))
			}
			offset += utf8.RuneCountInString(label)
			el := ui.Link(c.Key(fmt.Sprint(link.Start)), label, "").FontSize(size).TextColor(commentLinkColor)
			if el.Clicked() {
				switch link.Kind {
				case dy.CommentMention:
					a.openAuthor(a.cur(), strings.TrimSpace(strings.TrimPrefix(label, "@")), link.Target)
				case dy.CommentURL:
					c.OpenURL(link.Target)
				case dy.CommentGroupInvite:
					a.openCommentGroup(sh, cm.ID, label, link.Target)
				case dy.CommentSearch:
					a.search(link.Target)
				}
			}
			last = link.End
		}
		if last < len(cm.Text) {
			ui.RichText(c, textSpans(cm.Text[last:])...)
		}
	})
}

type groupPreview struct {
	open bool
	data *groupDetails
}

type groupDetails struct {
	label, pageURL  string
	group           dy.CommentGroup
	loading, failed bool
}

func (a *app) openCommentGroup(sh *sheet, cid, label, target string) {
	if a.c == nil {
		return
	}
	d := &groupDetails{label: label, pageURL: videoURL(sh.v), loading: true}
	if cid != "" {
		d.pageURL += "?comment_id=" + url.QueryEscape(cid)
	}
	a.group = groupPreview{open: true, data: d}
	go func() {
		group, err := a.c.CommentGroup(target)
		a.post(func() {
			if a.group.data == d {
				d.group, d.loading, d.failed = group, false, err != nil
			}
		})
	}()
}

func (a *app) commentGroupDialog(c *ui.Context) {
	if !a.group.open {
		return
	}
	d := a.group.data
	ui.DialogBase(c.Key("comment-group"), &a.group.open, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.Width(380).Padding(18, 20).Gap(12).Radius(14).Background(pal.raised).Label("群聊邀请")
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "群聊邀请").FontSize(16).FontWeight(700).Grow(1)
			if iconButton(c.Key("close"), iconClose, 16, pal.muted, "关闭群聊邀请").Clicked() {
				a.group.open = false
			}
		})
		switch {
		case d.loading:
			ui.Text(c, "正在加载群聊…").TextColor(pal.muted)
		case d.failed:
			ui.Text(c, d.label).FontWeight(600)
			ui.Text(c, "暂时读不到群聊详情，可以到抖音网页查看。").TextColor(pal.muted)
		default:
			g := d.group
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				a.avatar(c, pal, g.Avatar, g.Name, 40)
				ui.Column(c).Gap(4).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					ui.Text(c, fmt.Sprintf("%s · %d 人", g.Name, g.Members)).FontWeight(700)
					if g.Owner.Name != "" {
						label := "群主 · " + g.Owner.Name
						if g.Owner.SecUID == "" {
							ui.Text(c, label).FontSize(13).TextColor(pal.muted)
						} else if ui.Link(c.Key("owner"), label, "").FontSize(13).TextColor(commentLinkColor).Clicked() {
							a.group.open = false
							a.openAuthor(a.cur(), g.Owner.Name, g.Owner.SecUID)
						}
					}
				})
			})
			if g.Desc != "" {
				ui.Text(c, g.Desc).MaxLines(4).FontSize(13).TextColor(pal.muted)
			}
		}
		if ui.Button(c.Key("official"), "在抖音网页查看").AlignSelf(ui.End).Clicked() {
			c.OpenURL(d.pageURL)
		}
	})
}
