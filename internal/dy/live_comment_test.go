package dy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSendLiveCommentRequiresExplicitConfirmation(t *testing.T) {
	room := Live{RoomID: "7709459422975970086", WebRID: "615189692839"}
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"status_code":0,"data":{"msg_id_str":"7700000000000000001","content":"你好吗? 👋","user":{"nickname":"小明"}}}`, ""},
		{`{"status_code":50001,"data":{"prompts":"禁止发言"}}`, "禁止发言"},
		{`{"status_code":50008}`, "评论超出抖音的字数限制"},
		{`{"data":{"status_code":0}}`, "没收到抖音的发送确认"},
	} {
		calls := 0
		msg, err := SendLiveComment(t.Context(), func(_ context.Context, js string) (any, error) {
			calls++
			if !strings.Contains(js, `["615189692839","7709459422975970086","你好吗? 👋"]`) || !strings.Contains(js, "/webcast/room/chat/") {
				t.Fatal("missing room or text in the page request")
			}
			result, _ := json.Marshal(map[string]any{"http": 200, "body": tc.body})
			return string(result), nil
		}, room, " 你好吗? 👋 ")
		if calls != 1 {
			t.Fatalf("sent %d times", calls)
		}
		if tc.want != "" {
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got error %v, want %q", err, tc.want)
			}
		} else if err != nil || msg.ID != 7700000000000000001 || msg.Text != "你好吗? 👋" || msg.User != "小明" {
			t.Fatalf("message %+v, error %v", msg, err)
		}
	}
	_, err := SendLiveComment(t.Context(), func(context.Context, string) (any, error) {
		t.Fatal("an oversized draft should not reach the page")
		return nil, nil
	}, room, strings.Repeat("👋", LiveCommentLimit+1))
	if err == nil {
		t.Fatal("expected a draft length error")
	}
}
