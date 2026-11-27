package app

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"douyin/internal/dy"
	"douyin/internal/tiktok"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

var tiktokUnicodeEmojis = []string{
	"😂", "❤️", "🔥", "😍", "😭", "🤣", "👍", "👏",
	"✨", "💀", "🙏", "🥰", "😮", "🎉", "💯", "😎",
	"🤩", "🥺", "😤", "🤔", "👀", "💪", "🙌", "🤯",
	"😅", "😇", "🫶", "💔", "🎶", "⭐", "🌹", "🐱",
}

const (
	commentMaxChars     = 1000
	commentMaxImgEdge   = 10000
	commentReencodeEdge = 2560
	commentPreviewEdge  = 144
)

var readClipboardImage = func() []byte { return mygo.Clipboard.ReadImage() }

type replyTarget struct {
	rootID  string // top-level comment ID (reply_id)
	replyID string // nested reply ID (reply_to_reply_id), empty when replying to top-level
	author  string
}

type draftImage struct {
	data     []byte
	fileName string
	width    int
	height   int
	format   string // "jpeg" or "png"
	pasted   bool
	preview  *ui.Bitmap
}

type commentDraft struct {
	buf          ui.TextBuffer
	input        ui.Handle
	selStart     int
	selEnd       int
	hasSelection bool
	wantFocus    bool
	pendingCaret int // >= 0 moves caret after emoji insertion
	reply        *replyTarget
	image        *draftImage
	emojiOpen    bool
	picking      bool
	sending      bool
	err          string
}

// hgEmojiAliases maps Hongguo / Fanqie danmaku emoji shortcodes that differ in
// wording from Douyin's canonical emoji names onto the matching ByteDance icons.
var hgEmojiAliases = map[string]string{
	"[爱慕]":   "[色]",
	"[哭]":    "[流泪]",
	"[怒]":    "[发怒]",
	"[生气]":   "[发怒]",
	"[酷]":    "[酷拽]",
	"[吃瓜]":   "[吃瓜群众]",
	"[什么]":   "[疑问]",
	"[疑惑]":   "[疑问]",
	"[探究]":   "[暗中观察]",
	"[爽]":    "[好开心]",
	"[开心]":   "[好开心]",
	"[重拳出击]": "[拳头]",
	"[求爆更]":  "[求求了]",
	"[催更]":   "[求求了]",
	"[没看够]":  "[星星眼]",
	"[赶稿中]":  "[奋斗]",
	"[笑]":    "[微笑]",
	"[哈哈]":   "[大笑]",
	"[哈哈哈]":  "[大笑]",
	"[破涕为笑]": "[笑哭]",
	"[捂脸哭]":  "[笑哭]",
	"[哭笑]":   "[笑哭]",
	"[惊呆]":   "[震惊]",
	"[吃惊]":   "[震惊]",
	"[惊讶]":   "[震惊]",
	"[无语]":   "[无语流汗]",
	"[尴尬]":   "[尴尬流汗]",
	"[汗]":    "[擦汗]",
	"[流汗]":   "[擦汗]",
	"[悲伤]":   "[难过]",
	"[抱抱]":   "[抱抱你]",
	"[祈祷]":   "[黄脸祈祷]",
	"[干杯]":   "[黄脸干杯]",
	"[玫瑰花]":  "[玫瑰]",
	"[牛]":    "[强]",
	"[狗头]":   "[二哈]",
	"[加油]":   "[一起加油]",
	"[菜]":    "[菜狗]",
	"[爱了]":   "[爱心]",
	"[心动]":   "[爱心]",
	"[吐舌头]":  "[吐舌]",
	"[嫌弃]":   "[鄙视]",
}

func applyEmojiAliases(m map[string]string) {
	for alias, canonical := range hgEmojiAliases {
		if m[alias] == "" {
			if u := m[canonical]; u != "" {
				m[alias] = u
			}
		}
	}
}

func (a *app) ensureEmojis() {
	if len(a.emoji) > 0 || a.emojiLoading || a.c == nil {
		return
	}
	a.emojiLoading = true
	go func() {
		list, err := a.c.EmojiList()
		a.post(func() {
			a.emojiLoading = false
			if err != nil {
				return
			}
			m := make(map[string]string, len(list)+len(hgEmojiAliases))
			for _, e := range list {
				m[e.Name] = e.URL
			}
			applyEmojiAliases(m)
			a.emojiList = list
			a.emoji = m
			a.emojiCache = nil // and with it the bullet comments shaped without these emoji
		})
	}()
}

func (a *app) orderedEmojis() []dy.Emoji {
	if len(a.emojiList) > 0 {
		return a.emojiList
	}
	if len(a.emoji) == 0 {
		return nil
	}
	names := make([]string, 0, len(a.emoji))
	for name := range a.emoji {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]dy.Emoji, 0, len(names))
	for _, name := range names {
		out = append(out, dy.Emoji{Name: name, URL: a.emoji[name]})
	}
	return out
}

func (a *app) setReplyTarget(sh *sheet, rootID, replyID, author string) {
	if sh == nil || rootID == "" {
		return
	}
	sh.draft.reply = &replyTarget{rootID: rootID, replyID: replyID, author: author}
	sh.draft.wantFocus = true
	sh.draft.err = ""
	if !a.signedIn {
		a.openLogin()
	}
}

func (a *app) insertCommentEmoji(sh *sheet, name string) {
	if sh == nil || name == "" {
		return
	}
	d := &sh.draft
	n := d.buf.Len()
	start, end := n, n
	if d.hasSelection {
		start, end = d.selStart, d.selEnd
		if start > end {
			start, end = end, start
		}
		if start < 0 {
			start = 0
		}
		if end > n {
			end = n
		}
		if start > end {
			start = end
		}
	}
	d.buf.Replace(start, end, name)
	caret := start + utf8.RuneCountInString(name)
	d.selStart, d.selEnd, d.hasSelection = caret, caret, true
	d.pendingCaret = caret + 1 // stored as +1 so zero value means no pending move
	d.wantFocus = true
	d.err = ""
}

func (a *app) commentComposer(c *ui.Context, sh *sheet) {
	d := &sh.draft
	ui.Box(c).Height(1).Background(pal.hover)
	wrap := ui.Column(c.Key("comment-compose")).Padding(8, 12, 12, 12).Gap(6).
		Background(pal.raised)

	if !a.signedIn {
		wrap.Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				hint := onOff(a.isTikTok(), "登录后发表评论和表情", "登录后发表评论、表情和图片")
				ui.Text(c, hint).FontSize(12).TextColor(pal.muted).Grow(1)
				if ui.Button(c.Key("comment-login"), "去登录").Clicked() {
					a.openActiveLogin()
				}
			})
		})
		return
	}

	allowImage := !a.isTikTok()
	if allowImage {
		if wrap.FileDragOver() {
			wrap.Border(1.5, pal.accent)
		}
		if files := wrap.DroppedFiles(); len(files) > 0 && !d.sending {
			a.attachCommentImagePath(sh, files[0])
		}
	}

	wrap.Children(func() {
		if d.reply != nil {
			ui.Row(c.Key("reply-bar")).Gap(6).Padding(4, 8).Radius(7).Background(pal.hover).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "回复 @"+d.reply.author).FontSize(11.5).TextColor(pal.muted).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				if iconButton(c.Key("cancel-reply"), iconClose, 13, pal.muted, "取消回复").Clicked() {
					d.reply = nil
				}
			})
		}

		if img := d.image; img != nil {
			ui.Row(c.Key("image-preview")).Gap(8).Padding(6, 8).Radius(8).Background(pal.hover).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(48, 48).Radius(6).Clip().Background(pal.bg).Center().Children(func() {
					if img.preview != nil {
						ui.Image(c, img.preview).Size(48, 48).Fit(ui.Cover)
					}
				})
				ui.Column(c).Gap(2).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					ui.Text(c, img.fileName).FontSize(12).TextColor(pal.text).SingleLine()
					ui.Text(c, fmt.Sprintf("%d × %d · %.1f KB", img.width, img.height, float64(len(img.data))/1024)).FontSize(11).TextColor(pal.faint).SingleLine()
				})
				if !d.sending && iconButton(c.Key("remove-image"), iconClose, 14, pal.muted, "移除待发图片").Clicked() {
					d.image = nil
					d.err = ""
				}
			})
		}

		placeholder := "善语结善缘，恶言伤人心"
		if d.reply != nil {
			placeholder = "回复 @" + d.reply.author
		}
		text := strings.TrimSpace(d.buf.String())
		charCount := utf8.RuneCountInString(text)
		canSend := !d.sending && (text != "" || d.image != nil) && charCount <= commentMaxChars

		var emojiBtn ui.Element
		ui.Row(c.Key("compose-row")).Gap(4).Padding(5, 6, 5, 10).Radius(10).Background(pal.hover).AlignItems(ui.Center).Children(func() {
			in := ui.TextInputBufferBase(c.Key("comment-input"), &d.buf).Bind(&d.input).
				Grow(1).Shrink(1).MinWidth(0).Height(28).
				FontSize(13).TextColor(pal.text).Placeholder(placeholder).PlaceholderColor(pal.faint).
				Label("评论内容").
				OnPaste(func(pasted string) bool {
					if !allowImage || pasted != "" || d.sending {
						return false
					}
					raw := readClipboardImage()
					if len(raw) == 0 {
						return false
					}
					a.attachCommentImageBytes(sh, raw, "clipboard.png", true)
					return true
				})
			if d.wantFocus {
				in.Focus()
				d.wantFocus = false
			}
			if d.pendingCaret > 0 {
				pos := d.pendingCaret - 1
				in.SetTextSelection(pos, pos)
				d.pendingCaret = 0
			}
			if in.Focused() {
				s, e := in.TextSelection()
				d.selStart, d.selEnd, d.hasSelection = s, e, true
			}
			if in.Changed() {
				d.err = ""
			}
			if in.Submitted() && !in.Composing() && canSend {
				a.sendComment(sh)
			}

			emojiBtn = ui.ButtonBase(c.Key("emoji-btn")).Size(28, 28).Radius(7).Center().FocusRing(false).Label("选择表情")
			emojiColor := onOff(d.emojiOpen, pal.accent, onOff(emojiBtn.Hovered(), pal.text, pal.muted))
			if d.emojiOpen || emojiBtn.Hovered() {
				emojiBtn.Background(pal.bg.Alpha(0.5))
			}
			emojiBtn.Children(func() {
				ui.Icon(c, iconSmile).Size(17, 17).TextColor(emojiColor)
			})
			if emojiBtn.Clicked() {
				d.emojiOpen = !d.emojiOpen
				if d.emojiOpen && !a.isTikTok() {
					a.ensureEmojis()
				}
			}

			if allowImage {
				imgBtn := ui.ButtonBase(c.Key("image-btn")).Size(28, 28).Radius(7).Center().FocusRing(false).Label("添加图片").Disabled(d.sending || d.picking)
				imgColor := onOff(d.image != nil, pal.accent, onOff(imgBtn.Hovered(), pal.text, pal.muted))
				if d.image != nil || imgBtn.Hovered() {
					imgBtn.Background(pal.bg.Alpha(0.5))
				}
				imgBtn.Children(func() {
					ui.Icon(c, iconImage).Size(17, 17).TextColor(imgColor)
				})
				if imgBtn.Clicked() {
					d.emojiOpen = false
					a.pickCommentImage(sh)
				}
			}

			sendBtn := ui.ButtonBase(c.Key("send-btn")).Height(28).PaddingX(11).Radius(7).Center().FocusRing(false).
				Label("发送评论").Disabled(!canSend)
			sendBtn.Background(onOff(canSend, pal.accent, pal.bg.Alpha(0.5)))
			sendBtn.Children(func() {
				label := "发送"
				if d.sending {
					label = "发送中…"
				}
				ui.Text(c, label).FontSize(12.5).FontWeight(600).TextColor(onOff(canSend, white, pal.faint))
			})
			if sendBtn.Clicked() {
				a.sendComment(sh)
			}
		})

		if d.emojiOpen {
			ui.PopoverBase(c.Key("comment-emoji-popover"), emojiBtn, &d.emojiOpen, func(panel ui.Element) {
				popLabel := onOff(a.isTikTok(), "常用表情", "抖音表情")
				panel.AttachTo(wrap, ui.AnchorTop, ui.AnchorBottom).Margin(8, 0, 8, 0).
					Width(336).Height(224).Padding(10).Radius(12).
					Background(pal.raised).Border(1, pal.hover).Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.16)).Label(popLabel)
				if a.isTikTok() {
					ui.Scroll(c.Key("emoji-grid-scroll")).Fill().Children(func() {
						const cols = 8
						ui.Column(c).Gap(6).Children(func() {
							for rowStart := 0; rowStart < len(tiktokUnicodeEmojis); rowStart += cols {
								end := min(rowStart+cols, len(tiktokUnicodeEmojis))
								ui.Row(c.Key(rowStart)).Gap(4).AlignItems(ui.Center).Children(func() {
									for _, em := range tiktokUnicodeEmojis[rowStart:end] {
										eb := ui.ButtonBase(c.Key(em)).Size(34, 34).Radius(7).Center().FocusRing(false).Label(em)
										if eb.Hovered() {
											eb.Background(pal.hover)
										}
										eb.Children(func() {
											ui.Text(c, em).FontSize(18).TextColor(pal.strong).SingleLine()
										})
										if eb.Clicked() {
											a.insertCommentEmoji(sh, em)
										}
									}
								})
							}
						})
					})
					return
				}
				list := a.orderedEmojis()
				if len(list) == 0 {
					ui.Box(c).Fill().Center().Children(func() {
						msg := "正在加载表情…"
						if !a.emojiLoading && a.c != nil {
							msg = "暂时加载不到表情"
						}
						ui.Text(c, msg).FontSize(12).TextColor(pal.muted)
					})
					return
				}
				ui.Scroll(c.Key("emoji-grid-scroll")).Fill().Children(func() {
					const cols = 8
					ui.Column(c).Gap(4).Children(func() {
						for rowStart := 0; rowStart < len(list); rowStart += cols {
							end := min(rowStart+cols, len(list))
							ui.Row(c.Key(rowStart)).Gap(4).AlignItems(ui.Center).Children(func() {
								for _, e := range list[rowStart:end] {
									eb := ui.ButtonBase(c.Key(e.Name)).Size(34, 34).Radius(7).Center().FocusRing(false).Label(e.Name)
									if eb.Hovered() {
										eb.Background(pal.hover)
									}
									eb.Children(func() {
										if bmp := a.img.getSized(e.URL, emojiEdge); bmp != nil {
											ui.Image(c, bmp).Size(24, 24)
										} else {
											ui.Text(c, strings.Trim(e.Name, "[]")).FontSize(10).TextColor(pal.muted).SingleLine()
										}
									})
									if eb.Clicked() {
										a.insertCommentEmoji(sh, e.Name)
									}
								}
							})
						}
					})
				})
			})
		}

		if charCount > commentMaxChars {
			ui.Text(c, fmt.Sprintf("评论最多 %d 字，当前 %d 字", commentMaxChars, charCount)).FontSize(11).TextColor(commentLinkColor)
		} else if d.err != "" {
			ui.Text(c, d.err).FontSize(11).TextColor(commentLinkColor)
		}
	})
}

func (a *app) pickCommentImage(sh *sheet) {
	if sh == nil || sh.draft.picking || sh.draft.sending {
		return
	}
	sh.draft.picking = true
	win := a.win
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: win,
			Title:  "选择评论图片",
			Filters: []mygo.FileFilter{{
				Name:       "图片",
				Extensions: []string{"png", "jpg", "jpeg", "webp", "gif"},
				MIMETypes:  []string{"image/*"},
			}},
		})
		if err != nil || len(paths) == 0 {
			a.post(func() { sh.draft.picking = false })
			return
		}
		raw, rerr := os.ReadFile(paths[0])
		name := filepath.Base(paths[0])
		var prepared *draftImage
		if rerr == nil {
			prepared, rerr = prepareCommentImage(raw, name, false)
		}
		a.post(func() {
			sh.draft.picking = false
			if rerr != nil {
				sh.draft.err = rerr.Error()
				return
			}
			sh.draft.image = prepared
			sh.draft.err = ""
			sh.draft.wantFocus = true
		})
	}()
}

func (a *app) attachCommentImagePath(sh *sheet, path string) {
	if sh == nil || path == "" || sh.draft.sending {
		return
	}
	go func() {
		raw, err := os.ReadFile(path)
		var prepared *draftImage
		if err == nil {
			prepared, err = prepareCommentImage(raw, filepath.Base(path), false)
		}
		a.post(func() {
			if err != nil {
				sh.draft.err = err.Error()
				return
			}
			sh.draft.image = prepared
			sh.draft.err = ""
			sh.draft.wantFocus = true
		})
	}()
}

func (a *app) attachCommentImageBytes(sh *sheet, raw []byte, name string, pasted bool) {
	if sh == nil || len(raw) == 0 || sh.draft.sending {
		return
	}
	prepared, err := prepareCommentImage(raw, name, pasted)
	if err != nil {
		sh.draft.err = err.Error()
		return
	}
	sh.draft.image = prepared
	sh.draft.err = ""
}

// prepareCommentImage validates raw image bytes, re-encodes oversized or
// non-JPEG/PNG images to JPEG within Douyin's 10 MB limit, and builds a local
// preview bitmap.
func prepareCommentImage(raw []byte, fileName string, pasted bool) (*draftImage, error) {
	if len(raw) == 0 {
		return nil, errors.New("图片内容为空")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("不支持的图片格式，请选择 JPG、PNG、WebP 或 GIF")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > commentMaxImgEdge || cfg.Height > commentMaxImgEdge {
		return nil, fmt.Errorf("图片尺寸超出限制（%d×%d）", cfg.Width, cfg.Height)
	}
	if fileName == "" {
		fileName = "comment.jpg"
	}

	preview, _ := decodePictureEdge(raw, commentPreviewEdge)
	if (format == "jpeg" || format == "png") && len(raw) <= dy.MaxCommentImageBytes {
		return &draftImage{
			data:     raw,
			fileName: fileName,
			width:    cfg.Width,
			height:   cfg.Height,
			format:   format,
			pasted:   pasted,
			preview:  preview,
		}, nil
	}

	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("图片解码失败")
	}
	type attempt struct {
		edge    int
		quality int
	}
	for _, at := range []attempt{{commentReencodeEdge, 85}, {commentReencodeEdge, 65}, {1600, 72}} {
		w, h := cfg.Width, cfg.Height
		if m := max(w, h); m > at.edge {
			w = max(1, w*at.edge/m)
			h = max(1, h*at.edge/m)
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: at.quality}); err != nil {
			continue
		}
		if buf.Len() <= dy.MaxCommentImageBytes {
			if preview == nil {
				preview, _ = decodePictureEdge(buf.Bytes(), commentPreviewEdge)
			}
			ext := filepath.Ext(fileName)
			base := strings.TrimSuffix(fileName, ext)
			if base == "" {
				base = "comment"
			}
			return &draftImage{
				data:     buf.Bytes(),
				fileName: base + ".jpg",
				width:    w,
				height:   h,
				format:   "jpeg",
				pasted:   pasted,
				preview:  preview,
			}, nil
		}
	}
	return nil, errors.New("图片超过 10MB，压缩后仍超出抖音限制")
}

func (a *app) sendComment(sh *sheet) {
	if sh == nil || sh.draft.sending {
		return
	}
	if !a.signedIn {
		a.openActiveLogin()
		return
	}
	publish := a.publishComment
	if publish == nil && a.c != nil {
		publish = a.c.PublishComment
	}
	if publish == nil {
		return
	}
	text := strings.TrimSpace(sh.draft.buf.String())
	if text == "" && sh.draft.image == nil {
		return
	}
	if utf8.RuneCountInString(text) > commentMaxChars {
		sh.draft.err = fmt.Sprintf("评论最多 %d 字", commentMaxChars)
		return
	}

	var reply *replyTarget
	if sh.draft.reply != nil {
		cp := *sh.draft.reply
		reply = &cp
	}
	img := sh.draft.image
	videoID := sh.v.ID
	uid := a.me.UID
	meID, meName, meAvatar := a.me.ID, a.me.Name, a.me.Avatar
	upload := a.uploadCommentImage
	if upload == nil && a.c != nil {
		upload = a.c.UploadCommentImage
	}

	sh.draft.sending = true
	sh.draft.emojiOpen = false
	sh.draft.err = ""

	go func() {
		var uploaded *dy.CommentImage
		if img != nil {
			if upload == nil {
				a.post(func() {
					sh.draft.sending = false
					sh.draft.err = "当前无法上传图片"
				})
				return
			}
			if uid == "" && a.c != nil {
				if me, err := a.c.Self(); err == nil {
					uid = me.UID
				}
			}
			res, err := upload(img.data, img.fileName, uid, dy.CommentImage{
				Width:  img.width,
				Height: img.height,
				Format: img.format,
			})
			if err != nil {
				a.post(func() {
					sh.draft.sending = false
					if errors.Is(err, dy.ErrNeedLogin) {
						a.signedIn = false
						a.openLogin()
					}
					sh.draft.err = err.Error()
				})
				return
			}
			uploaded = &res
		}

		req := dy.PublishCommentReq{
			VideoID: videoID,
			Text:    text,
			Image:   uploaded,
			Pasted:  img != nil && img.pasted,
		}
		if reply != nil {
			req.ReplyID = reply.rootID
			req.ReplyToReplyID = reply.replyID
		}
		cm, err := publish(req)
		a.post(func() {
			sh.draft.sending = false
			if err != nil {
				switch {
				case errors.Is(err, tiktok.ErrNeedLogin):
					a.signedIn = false
					a.openTikTokLogin()
					sh.draft.err = err.Error()
				case errors.Is(err, tiktok.ErrVerify):
					sh.draft.err = "TikTok 需要完成验证，验证后请再点发送"
					a.verifyTikTok(videoID, func() {
						sh.draft.err = "验证已完成，请再点发送"
					})
				case errors.Is(err, dy.ErrNeedLogin):
					a.signedIn = false
					a.openLogin()
					sh.draft.err = err.Error()
				case errors.Is(err, dy.ErrVerify):
					sh.draft.err = "需要完成验证，验证后请再点发送"
					a.verify("/aweme/v1/web/comment/list/?device_platform=webapp&aid=6383&channel=channel_pc_web&aweme_id="+videoID+
						"&cursor=0&count=20&item_type=0&insert_ids=&whale_cut_token=&cut_version=1&rcFT=", func() {
						sh.draft.err = "验证已完成，请再点发送"
					}, verifyWrite)
				default:
					sh.draft.err = err.Error()
				}
				return
			}

			if cm.Author == "" {
				cm.Author = meName
			}
			if cm.AuthorID == "" {
				cm.AuthorID = meID
			}
			if cm.Avatar == "" {
				cm.Avatar = meAvatar
			}
			if cm.Text == "" {
				cm.Text = text
			}
			if cm.Time.IsZero() {
				cm.Time = time.Now()
			}
			if reply != nil && reply.replyID != "" && cm.ReplyTo == "" {
				cm.ReplyTo = reply.author
			}

			sh.draft.buf.Set("")
			sh.draft.selStart, sh.draft.selEnd, sh.draft.hasSelection = 0, 0, false
			sh.draft.image = nil
			sh.draft.reply = nil
			sh.draft.err = ""
			sh.total++

			if reply == nil {
				sh.items = append([]dy.Comment{cm}, sh.items...)
			} else {
				if sh.threads == nil {
					sh.threads = map[string]*thread{}
				}
				th := sh.threads[reply.rootID]
				if th == nil {
					var next dy.Pager[dy.Comment]
					if a.replies != nil {
						next = a.replies(videoID, reply.rootID)
					}
					th = &thread{video: videoID, cid: reply.rootID, next: next, open: true}
					sh.threads[reply.rootID] = th
				} else {
					th.open = true
				}
				th.items = append([]dy.Comment{cm}, th.items...)
				for i := range sh.items {
					if sh.items[i].ID == reply.rootID {
						sh.items[i].Replies++
						break
					}
				}
			}
			a.toast = "评论已发送"
		})
	}()
}
