package dy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const LiveCommentLimit = 50

// SendLiveComment submits one plain-text comment through the official room
// page, whose scripts sign the same-origin request. It never retries a write.
func SendLiveComment(ctx context.Context, eval func(context.Context, string) (any, error), room Live, text string) (LiveMessage, error) {
	if _, err := strconv.ParseUint(room.RoomID, 10, 64); err != nil || room.RoomID == "0" {
		return LiveMessage{}, errors.New("直播间信息还没准备好")
	}
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > LiveCommentLimit {
		return LiveMessage{}, fmt.Errorf("请输入 1 到 %d 个字", LiveCommentLimit)
	}
	args, _ := json.Marshal([]string{room.WebRID, room.RoomID, text})
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	value, err := eval(ctx, "const [webRID, roomID, text] = "+string(args)+`;
if (location.origin !== 'https://live.douyin.com' || location.pathname !== '/' + webRID ||
    document.readyState !== 'complete' || typeof window.bdms !== 'object') {
  return JSON.stringify({error: '直播间还没准备好，请稍后再试'});
}
const url = new URL('/webcast/room/chat/', location.origin);
url.search = new URLSearchParams({
  aid: '6383', app_name: 'douyin_web', live_id: '1', device_platform: 'web',
  language: navigator.language, enter_from: window.__INVISIBLE_QUERY__?.enter_from_merge ?? '',
  cookie_enabled: navigator.cookieEnabled, screen_width: screen.width, screen_height: screen.height,
  browser_language: navigator.language, browser_platform: navigator.platform,
  room_id: roomID, content: text, type: '0'
});
return await new Promise(resolve => {
  const req = new XMLHttpRequest();
  req.open('GET', url.href);
  req.withCredentials = true;
  req.timeout = 8000;
  req.setRequestHeader('Content-Type', 'application/json');
  req.onload = () => resolve(JSON.stringify({http: req.status, body: req.responseText}));
  req.onerror = req.ontimeout = req.onabort = () => resolve('{}');
  req.send();
});`)
	const uncertain = "没收到抖音的发送确认，请先查看互动栏再重试"
	encoded, _ := value.(string)
	var result struct {
		HTTP  int
		Body  string
		Error string
	}
	if err != nil || json.Unmarshal([]byte(encoded), &result) != nil {
		return LiveMessage{}, errors.New(uncertain)
	}
	if result.Error != "" {
		return LiveMessage{}, errors.New(result.Error)
	}
	var reply struct {
		Code *int `json:"status_code"`
		Data struct {
			ID      string `json:"msg_id_str"`
			Content string
			Prompts string
			Toast   string
			User    struct{ Nickname string }
		}
	}
	if result.HTTP != 200 || json.Unmarshal([]byte(result.Body), &reply) != nil || reply.Code == nil {
		return LiveMessage{}, errors.New(uncertain)
	}
	if *reply.Code != 0 {
		for _, reason := range []string{reply.Data.Prompts, reply.Data.Toast} {
			if reason = strings.TrimSpace(reason); reason != "" {
				chars := []rune(reason)
				return LiveMessage{}, errors.New(string(chars[:min(len(chars), 100)]))
			}
		}
		switch *reply.Code {
		case 50001:
			return LiveMessage{}, errors.New("你在这个直播间被禁言了")
		case 50008:
			return LiveMessage{}, errors.New("评论超出抖音的字数限制")
		default:
			return LiveMessage{}, fmt.Errorf("抖音未接受评论（%d）", *reply.Code)
		}
	}
	id, _ := strconv.ParseUint(reply.Data.ID, 10, 64)
	return LiveMessage{ID: id, User: reply.Data.User.Nickname, Text: reply.Data.Content}, nil
}
