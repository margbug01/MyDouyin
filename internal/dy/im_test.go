package dy

import (
	"testing"
	"time"
)

// A conversation as the inbox sends it decodes to its peer and its
// messages: whose, Douyin's tips with their templates filled, text, and
// something else as a placeholder; the user's own sec_uid tells theirs.
func TestChatDecode(t *testing.T) {
	person := func(uid uint64, sec string) []byte { return pb(nil).uint(1, uid).str(5, sec) }
	info := pb(nil).str(1, "0:1:1:2").bytes(6, pb(nil).bytes(1, person(1, "me")).bytes(1, person(2, "them")))
	msg := func(kind, from uint64, content string, ms uint64) []byte {
		return pb(nil).uint(6, kind).uint(7, from).str(8, content).uint(10, ms)
	}
	conv := pb(nil).bytes(1, info).
		bytes(2, msg(1, 1, `{"tips":"请遵守{{0}}","template":[{"key":"0","name":"《公约》"}]}`, 1000)).
		bytes(2, msg(7, 2, `{"text":"你好"}`, 2000)).
		bytes(2, msg(7, 1, `{"text":"在的"}`, 3000)).
		bytes(2, msg(70, 2, `{"actions":{}}`, 4000))
	ch, err := chat(conv, "me")
	if err != nil {
		t.Fatal(err)
	}
	want := []Message{
		{Mine: true, Tip: true, Text: "请遵守《公约》", Time: time.UnixMilli(1000)},
		{Text: "你好", Time: time.UnixMilli(2000)},
		{Mine: true, Text: "在的", Time: time.UnixMilli(3000)},
		{Text: "[分享或卡片，请在抖音里查看]", Time: time.UnixMilli(4000)},
	}
	if ch.ID != "0:1:1:2" || ch.Peer.ID != "them" || len(ch.Msgs) != len(want) {
		t.Fatalf("got %+v", ch)
	}
	for i, m := range ch.Msgs {
		if m != want[i] {
			t.Errorf("message %d: %+v, want %+v", i, m, want[i])
		}
	}
	if _, err := fields([]byte{0x0a, 0x05, 'a'}); err == nil {
		t.Error("a field longer than its message decoded")
	}
}

// A definition is the smallest H.264 mp4 of it, which has the sound: never
// the dash gear beside it, the picture alone, however small.
func TestPlayURLQualities(t *testing.T) {
	type gear = struct {
		Name   string `json:"gear_name"`
		H265   int    `json:"is_h265"`
		Format string
		Addr   addr `json:"play_addr"`
	}
	g := func(name, format string, w, h int, size int64) gear {
		return gear{Name: name, Format: format, Addr: addr{URLs: []string{"https://x/aweme/v1/play/?" + name + format}, W: w, H: h, Size: size}}
	}
	u, _, qs := playURL(addr{}, []gear{
		g("normal_720_0", "mp4", 1280, 720, 40),
		g("low_720_0", "dash", 1280, 720, 39),
		g("lower_540_0", "mp4", 1024, 576, 31),
		g("low_540_0", "dash", 1024, 576, 30),
		g("adapt_low_540_0", "mp4", 1024, 576, 25),
		func() gear { x := g("1080_1_1", "mp4", 1920, 1080, 20); x.H265 = 1; return x }(),
	})
	if u != "https://x/aweme/v1/play/?lower_540_0mp4" {
		t.Fatalf("智能 picked %s", u)
	}
	if len(qs) != 2 || qs[0].Name != "720P" || qs[0].Size != 40 || qs[1].Name != "540P" || qs[1].Size != 31 {
		t.Fatalf("qualities %+v", qs)
	}
}
