package dy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// CommentImage describes an image already uploaded to ByteDance ImageX,
// ready to attach to a published comment.
type CommentImage struct {
	URI    string
	Width  int
	Height int
	Format string // "jpeg", "png", "webp", "gif"
}

// PublishCommentReq describes a new top-level comment or reply.
type PublishCommentReq struct {
	VideoID        string
	Text           string
	ReplyID        string // parent top-level comment ID when replying
	ReplyToReplyID string // target nested reply ID when replying to a reply
	Image          *CommentImage
	Pasted         bool // true when the image came from clipboard paste
}

// PublishComment posts a comment or reply on a video through the logged-in
// WebView page so Douyin's security SDKs sign it. It never retries automatically
// so a slow or ambiguous response cannot double-post a comment.
func (c *Client) PublishComment(req PublishCommentReq) (Comment, error) {
	if req.VideoID == "" {
		return Comment{}, errors.New("缺少视频 ID")
	}
	text := strings.TrimSpace(req.Text)
	if text == "" && (req.Image == nil || req.Image.URI == "") {
		return Comment{}, errors.New("请输入评论内容或添加图片")
	}
	writeGate.lock(true)
	defer writeGate.unlock()
	if c.web == nil {
		return Comment{}, errNoWeb
	}

	body := url.Values{
		"aweme_id":               {req.VideoID},
		"text":                   {text},
		"text_extra":             {"[]"},
		"comment_send_celltime":  {"0"},
		"comment_video_celltime": {"0"},
	}
	if req.ReplyID != "" && req.ReplyID != "0" {
		body.Set("reply_id", req.ReplyID)
		if req.ReplyToReplyID != "" && req.ReplyToReplyID != "0" {
			body.Set("reply_to_reply_id", req.ReplyToReplyID)
		} else {
			body.Set("reply_to_reply_id", "0")
		}
	}
	if req.Image != nil && req.Image.URI != "" {
		body.Set("image_uri_list", req.Image.URI)
		body.Set("image_widths", fmt.Sprint(req.Image.Width))
		body.Set("image_heights", fmt.Sprint(req.Image.Height))
		body.Set("image_formats", req.Image.Format)
		if req.Pasted {
			body.Set("paste_edit_method", "paste")
		} else {
			body.Set("paste_edit_method", "non_paste")
		}
		body.Set("one_level_comment_rank", "-1")
	}

	args, _ := json.Marshal([]string{body.Encode()})
	js := "const [bodyStr] = " + string(args) + `;
const ck = (n) => {
  const s = (typeof document !== "undefined" && document.cookie) || "";
  const m = s.match(new RegExp("(?:^|;\\s*)" + n + "=([^;]*)"));
  return m ? decodeURIComponent(m[1]) : "";
};
const fp = ck("s_v_web_id") || ck("verifyFp");
const q = new URLSearchParams({
  app_name: "aweme",
  enter_from: "video_detail",
  previous_page: "video_detail",
  device_platform: "webapp",
  aid: "6383",
  channel: "channel_pc_web",
  pc_client_type: "1",
  version_code: "170400",
  version_name: "17.4.0",
});
for (const [k, v] of [["msToken", ck("msToken")], ["webid", ck("webid")], ["verifyFp", fp], ["fp", fp]]) {
  if (v) q.set(k, v);
}
const u = "https://www.douyin.com/aweme/v1/web/comment/publish?" + q.toString();
const capEl = document.getElementById("captcha_container");
const capBefore = capEl ? capEl.childElementCount : 0;
const bodyBefore = document.body ? document.body.childElementCount : 0;
const hasCaptcha = () => {
  if (capEl && capEl.childElementCount > capBefore) return true;
  if (window.__mdSignPage && document.body && document.body.childElementCount > bodyBefore) {
    for (let i = bodyBefore; i < document.body.children.length; i++) {
      const tag = document.body.children[i].tagName;
      if (tag !== "SCRIPT" && tag !== "STYLE" && tag !== "LINK") return true;
    }
  }
  return !!document.querySelector(".captcha_verify_container, .vc-captcha-verify-container, #secsdk-captcha-drag-wrapper, iframe[src*='/verifycenter/captcha/'], #vc_captcha_wrapper");
};
return await new Promise((resolve, reject) => {
  let done = false;
  const finish = (v) => {
    if (done) return;
    done = true;
    clearInterval(iv);
    clearTimeout(to);
    resolve(v);
  };
  const iv = setInterval(() => {
    if (hasCaptcha()) finish({ http: 200, text: "", verify: true });
  }, 200);
  const to = setTimeout(() => finish({ http: 200, text: "", verify: true }), 15000);
  fetch(u, {
    method: "POST",
    credentials: "include",
    headers: {
      "content-type": "application/x-www-form-urlencoded; charset=UTF-8",
      "x-secsdk-csrf-token": "DOWNGRADE",
    },
    body: bodyStr,
  }).then(async (r) => {
    const text = await r.text();
    finish({ http: r.status, text });
  }).catch((e) => {
    if (!done) {
      done = true;
      clearInterval(iv);
      clearTimeout(to);
      reject(e);
    }
  });
});`

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := c.web.Eval(ctx, js)
	if err != nil {
		return Comment{}, err
	}
	var resp struct {
		HTTP   int
		Text   string
		Verify bool
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return Comment{}, err
	}
	if resp.Verify {
		return Comment{}, ErrVerify
	}
	if resp.HTTP != 200 {
		return Comment{}, fmt.Errorf("评论发送失败（HTTP %d）", resp.HTTP)
	}
	if strings.TrimSpace(resp.Text) == "" {
		return Comment{}, errors.New("抖音返回空响应，请稍后刷新评论区确认")
	}
	var parsed struct {
		Code    *int     `json:"status_code"`
		Msg     string   `json:"status_msg"`
		Comment *comment `json:"comment"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &parsed); err != nil {
		return Comment{}, fmt.Errorf("评论返回数据解析失败：%w", err)
	}
	if parsed.Code != nil && *parsed.Code == 8 {
		return Comment{}, ErrNeedLogin
	}
	if parsed.Code == nil || *parsed.Code != 0 {
		if parsed.Msg != "" {
			return Comment{}, errors.New(parsed.Msg)
		}
		code := -1
		if parsed.Code != nil {
			code = *parsed.Code
		}
		return Comment{}, fmt.Errorf("抖音未接受评论（code %d）", code)
	}
	c.syncCookies()
	if parsed.Comment != nil {
		cm := parsed.Comment.comment()
		if cm.Time.Unix() <= 0 {
			cm.Time = time.Now()
		}
		return cm, nil
	}
	return Comment{Text: text, Time: time.Now()}, nil
}
