package app

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"math"
	"testing"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo/ui"
)

func samplePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{R: 200, G: 100, B: 60, A: 255})
		}
	}
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func drainPosted(t *testing.T, ch chan func()) {
	t.Helper()
	select {
	case fn := <-ch:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async UI callback")
	}
}

func TestCommentComposeEmojiAndReplyFlow(t *testing.T) {
	var sent []dy.PublishCommentReq
	a := withRoots(&app{
		pages:    []*page{stub("推荐", 3)},
		signedIn: true,
		me:       dy.User{Name: "我自己", UID: "u99"},
		posted:   make(chan func(), 16),
		emoji:    map[string]string{"[赞]": "e:zan", "[捂脸]": "e:wl"},
		emojiList: []dy.Emoji{
			{Name: "[赞]", URL: "e:zan"},
			{Name: "[捂脸]", URL: "e:wl"},
		},
		publishComment: func(req dy.PublishCommentReq) (dy.Comment, error) {
			sent = append(sent, req)
			return dy.Comment{ID: "new-" + req.ReplyID, Author: "我自己", Text: req.Text, Time: time.Now()}, nil
		},
	})
	sh := &sheet{
		v:     dy.Video{ID: "vid1"},
		total: 1,
		done:  true,
		items: []dy.Comment{{ID: "c1", Author: "甲", Text: "原评论"}},
	}
	a.sheet = sh
	tt := newTester(a)
	tt.Frame()

	// Type text, open emoji picker, insert emoji, and verify Space does not pause video.
	sh.draft.buf.Set("好看")
	sh.draft.selStart, sh.draft.selEnd, sh.draft.hasSelection = 2, 2, true
	if err := tt.Click("选择表情"); err != nil {
		t.Fatalf("click emoji btn: %v", err)
	}
	tt.Frame()
	if !sh.draft.emojiOpen {
		t.Fatal("emoji popover did not open")
	}
	if err := tt.Click("[赞]"); err != nil {
		t.Fatalf("click emoji item: %v", err)
	}
	tt.Frame()
	if got := sh.draft.buf.String(); got != "好看[赞]" {
		t.Fatalf("draft after emoji insert = %q, want 好看[赞]", got)
	}

	// Escape closes emoji popover first without closing comment panel.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if sh.draft.emojiOpen || a.sheet == nil {
		t.Fatalf("Escape should close only emoji popover: emojiOpen=%v sheet=%v", sh.draft.emojiOpen, a.sheet != nil)
	}

	// While comment input is focused, Space and Down must not pause or step videos.
	tt.Key(0, ui.KeySpace)
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if a.paused || a.cur().idx != 0 {
		t.Fatalf("global video shortcuts leaked into comment input: paused=%v idx=%d", a.paused, a.cur().idx)
	}

	// Send top-level comment.
	if err := tt.Click("发送评论"); err != nil {
		t.Fatalf("click send: %v", err)
	}
	drainPosted(t, a.posted)
	tt.Frame()
	if len(sent) != 1 || sent[0].VideoID != "vid1" || sent[0].Text != "好看[赞]" || sent[0].ReplyID != "" {
		t.Fatalf("unexpected publish req: %+v", sent)
	}
	if sh.draft.buf.String() != "" || sh.total != 2 || len(sh.items) != 2 || sh.items[0].Text != "好看[赞]" {
		t.Fatalf("comment not prepended cleanly: total=%d items=%d draft=%q", sh.total, len(sh.items), sh.draft.buf.String())
	}

	// Click 回复 on original comment c1 and send a reply.
	if err := tt.Click("回复"); err != nil {
		t.Fatalf("click reply: %v", err)
	}
	tt.Frame()
	if sh.draft.reply == nil || sh.draft.reply.rootID != "new-" {
		// First visible comment is the newly prepended one ("new-").
		t.Fatalf("unexpected reply target: %+v", sh.draft.reply)
	}
	sh.draft.buf.Set("我也觉得")
	tt.Frame()
	if err := tt.Click("发送评论"); err != nil {
		t.Fatalf("click send reply: %v", err)
	}
	drainPosted(t, a.posted)
	tt.Frame()
	if len(sent) != 2 || sent[1].ReplyID != "new-" || sent[1].ReplyToReplyID != "" || sent[1].Text != "我也觉得" {
		t.Fatalf("unexpected reply req: %+v", sent)
	}
	th := sh.threads["new-"]
	if th == nil || !th.open || len(th.items) != 1 || th.items[0].Text != "我也觉得" {
		t.Fatalf("reply thread did not open with new reply: %+v", th)
	}
}

func TestCommentComposeImageUploadAndErrorRetention(t *testing.T) {
	pngBytes := samplePNG(t, 400, 300)
	var uploadedName, uploadedUID string
	var publishedReq dy.PublishCommentReq
	failOnce := true

	a := withRoots(&app{
		pages:    []*page{stub("推荐", 1)},
		signedIn: true,
		me:       dy.User{Name: "我", UID: "uid42"},
		posted:   make(chan func(), 16),
		uploadCommentImage: func(data []byte, fileName, userID string, measured dy.CommentImage) (dy.CommentImage, error) {
			uploadedName, uploadedUID = fileName, userID
			return dy.CommentImage{URI: "tos-cn-i-x/pic1", Width: measured.Width, Height: measured.Height, Format: measured.Format}, nil
		},
		publishComment: func(req dy.PublishCommentReq) (dy.Comment, error) {
			publishedReq = req
			if failOnce {
				failOnce = false
				return dy.Comment{}, errors.New("暂时无法评论")
			}
			return dy.Comment{
				ID:     "img-c1",
				Author: "我",
				Text:   req.Text,
				Pics:   []dy.CommentPic{{Thumb: "thumb:1", Origin: "origin:1"}},
			}, nil
		},
	})
	sh := &sheet{v: dy.Video{ID: "vid9"}, done: true}
	a.sheet = sh
	tt := newTester(a)

	a.attachCommentImageBytes(sh, pngBytes, "shot.png", true)
	tt.Frame()
	if sh.draft.image == nil || sh.draft.image.width != 400 || sh.draft.image.height != 300 || !sh.draft.image.pasted {
		t.Fatalf("image not attached: %+v", sh.draft.image)
	}

	// Image-only comment should be sendable; first attempt fails and must retain the image.
	if err := tt.Click("发送评论"); err != nil {
		t.Fatalf("send image comment (1): %v", err)
	}
	drainPosted(t, a.posted)
	tt.Frame()
	if sh.draft.image == nil || sh.draft.err != "暂时无法评论" {
		t.Fatalf("failed publish must keep image and show error: img=%v err=%q", sh.draft.image != nil, sh.draft.err)
	}

	// Second attempt succeeds and clears draft image.
	if err := tt.Click("发送评论"); err != nil {
		t.Fatalf("send image comment (2): %v", err)
	}
	drainPosted(t, a.posted)
	tt.Frame()
	if uploadedName != "shot.png" || uploadedUID != "uid42" {
		t.Fatalf("upload args: name=%q uid=%q", uploadedName, uploadedUID)
	}
	if publishedReq.Image == nil || publishedReq.Image.URI != "tos-cn-i-x/pic1" || !publishedReq.Pasted {
		t.Fatalf("publish image req mismatch: %+v", publishedReq)
	}
	if sh.draft.image != nil || len(sh.items) != 1 || len(sh.items[0].Pics) != 1 {
		t.Fatalf("success did not clear draft or append image comment: img=%v items=%+v", sh.draft.image, sh.items)
	}
}

func TestPrepareCommentImageReencodesGIFToJPEG(t *testing.T) {
	palImg := image.NewPaletted(image.Rect(0, 0, 64, 48), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, palImg, nil); err != nil {
		t.Fatal(err)
	}
	d, err := prepareCommentImage(buf.Bytes(), "anim.gif", false)
	if err != nil {
		t.Fatal(err)
	}
	if d.format != "jpeg" || d.fileName != "anim.jpg" || d.width != 64 || d.height != 48 || d.preview == nil {
		t.Fatalf("unexpected prepared gif: %+v", d)
	}
}

func TestCommentComposerAlignmentAndEmojiPopoverBounds(t *testing.T) {
	picBytes := samplePNG(t, 480, 320)
	emojiBytes := samplePNG(t, 48, 48)
	done := make(chan func(), 16)
	a := withRoots(&app{
		pages:    []*page{stub("推荐", 1)},
		signedIn: true,
		me:       dy.User{Name: "我", UID: "u1"},
		emoji:    map[string]string{"[赞]": "e:1", "[微笑]": "e:2", "[捂脸]": "e:3", "[玫瑰]": "e:4"},
		emojiList: []dy.Emoji{
			{Name: "[赞]", URL: "e:1"},
			{Name: "[微笑]", URL: "e:2"},
			{Name: "[捂脸]", URL: "e:3"},
			{Name: "[玫瑰]", URL: "e:4"},
		},
	})
	a.img = newImages(func(u string) ([]byte, error) {
		if u[0] == 'e' {
			return emojiBytes, nil
		}
		return picBytes, nil
	}, func(fn func()) { done <- fn })
	sh := &sheet{
		v:     dy.Video{ID: "v1"},
		total: 2,
		done:  true,
		items: []dy.Comment{
			{ID: "c1", Author: "甲", Text: "带图评论[赞]", Pics: []dy.CommentPic{{Thumb: "p:thumb", Origin: "p:origin"}}},
		},
	}
	sh.draft.reply = &replyTarget{rootID: "c1", author: "甲"}
	a.attachCommentImageBytes(sh, picBytes, "preview.png", false)
	a.sheet = sh

	tt := newTester(a)
	tt.Frame()
	for len(done) > 0 {
		(<-done)()
	}
	tt.Frame()

	inRect, ok1 := tt.Find("评论内容")
	emojiRect, ok2 := tt.Find("选择表情")
	imgRect, ok3 := tt.Find("添加图片")
	sendRect, ok4 := tt.Find("发送评论")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		t.Fatalf("missing composer controls: in=%v emoji=%v img=%v send=%v", ok1, ok2, ok3, ok4)
	}
	// All four controls must be vertically centered on the same row and strictly left-to-right without overlap.
	midY := func(r ui.Rect) float32 { return r.Y + r.H/2 }
	for name, r := range map[string]ui.Rect{"emoji": emojiRect, "img": imgRect, "send": sendRect} {
		if diff := math.Abs(float64(midY(inRect) - midY(r))); diff > 2 {
			t.Errorf("%s not vertically aligned with input: inputY=%.1f %sY=%.1f", name, midY(inRect), name, midY(r))
		}
	}
	if inRect.X+inRect.W > emojiRect.X || emojiRect.X+emojiRect.W > imgRect.X || imgRect.X+imgRect.W > sendRect.X {
		t.Errorf("composer controls overlap: in=%+v emoji=%+v img=%+v send=%+v", inRect, emojiRect, imgRect, sendRect)
	}

	// Open emoji popover and verify it sits cleanly above the input row.
	if err := tt.Click("选择表情"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for len(done) > 0 {
		(<-done)()
	}
	tt.Frame()
	popRect, ok := tt.Find("抖音表情")
	if !ok {
		t.Fatal("emoji popover not rendered")
	}
	replyBarRect, ok := tt.Find("回复 @甲")
	if !ok {
		t.Fatal("reply bar not rendered")
	}
	if popRect.Y+popRect.H > replyBarRect.Y+2 {
		t.Errorf("emoji popover overlaps composer header: pop=%+v replyBar=%+v", popRect, replyBarRect)
	}
}
