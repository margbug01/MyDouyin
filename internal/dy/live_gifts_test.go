package dy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSendLiveGiftChecksReceiptWithoutRetry(t *testing.T) {
	room := Live{WebRID: "123", RoomID: "7700000000000000001", OwnerID: "7700000000000000002"}
	gift := LiveGift{ID: 463, Name: "小心心", Price: 1}
	for _, tc := range []struct {
		reply liveGiftReply
		want  string
	}{
		{liveGiftReply{HTTP: 200, Body: `{"status_code":0}`}, ""},
		{liveGiftReply{HTTP: 200, Body: `{"status_code":40001}`}, "钻石余额不足"},
		{liveGiftReply{Error: "礼物或价格已变化，请刷新后重新选择"}, "礼物或价格已变化"},
		{liveGiftReply{HTTP: 200, Body: `{"data":{"status_code":0}}`}, ErrLiveGiftUnconfirmed.Error()},
	} {
		calls := 0
		err := SendLiveGift(t.Context(), func(_ context.Context, code string) (any, error) {
			calls++
			if !strings.Contains(code, `["123","7700000000000000001","7700000000000000002",{"ID":463,"Name":"小心心","Price":1}]`) || !strings.Contains(code, "current.price !== confirmed.Price") {
				t.Fatal("missing confirmed gift or room guard")
			}
			out, err := json.Marshal(tc.reply)
			return string(out), err
		}, room, gift)
		if calls != 1 || (tc.want == "" && err != nil) || (tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want))) {
			t.Fatalf("got %d requests and %v, want 1 request and %q", calls, err, tc.want)
		}
	}
	err := SendLiveGift(t.Context(), func(context.Context, string) (any, error) { return nil, context.DeadlineExceeded }, room, gift)
	if !errors.Is(err, ErrLiveGiftUnconfirmed) {
		t.Fatalf("missing uncertain result, got %v", err)
	}
}
