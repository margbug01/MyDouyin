package dy

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestCommentLinksUseUTF16AndPreservePlainText(t *testing.T) {
	var raw comment
	if err := json.Unmarshal([]byte(`{
		"text":"👋@阿🧈 看这里， https://example.org/a。 [群聊] 车友群",
		"text_extra":[
			{"start":2,"end":6,"type":0,"sec_uid":"sec-test"},
			{"start":7,"end":10,"type":8,"link":"https://example.org/article"},
			{"start":1,"end":2,"type":0,"sec_uid":"inside-a-surrogate"},
			{"start":1,"end":999,"type":5,"search_text":"bad range"},
			{"start":33,"end":35,"type":8,"link":"javascript:alert(1)"},
			{"start":35,"end":43,"type":16,"link":"aweme://im/group_invite_dialog?cid=123&ticket=fixture%2Bticket&secret_type=38&source_type=47"}
		]
	}`), &raw); err != nil {
		t.Fatal(err)
	}
	cm := raw.comment()
	var labels []string
	var kinds []CommentLinkKind
	for _, l := range cm.Links {
		labels = append(labels, cm.Text[l.Start:l.End])
		kinds = append(kinds, l.Kind)
	}
	if !slices.Equal(labels, []string{"@阿🧈", "看这里", "https://example.org/a", "[群聊] 车友群"}) ||
		!slices.Equal(kinds, []CommentLinkKind{CommentMention, CommentURL, CommentURL, CommentGroupInvite}) {
		t.Fatalf("labels %q, kinds %v", labels, kinds)
	}
	form := inviteForm(cm.Links[3].Target)
	if form.Get("group_id") != "123" || form.Get("secret") != "fixture+ticket" || form.Get("ext") != `{"group_reserve_v2_ab":"2","join_source":"47"}` {
		t.Fatal("group preview form did not preserve the invite parameters")
	}
}

func TestVideosKeepPinOrder(t *testing.T) {
	var list awemeList
	if err := json.Unmarshal([]byte(`{"aweme_list":[
		{"aweme_id":"pinned","create_time":10,"is_top":1,"video":{"play_addr":{"url_list":["https://example.org/aweme/v1/play?id=a"]}}},
		{"aweme_id":"newer","create_time":20,"is_top":0,"video":{"play_addr":{"url_list":["https://example.org/aweme/v1/play?id=b"]}}}
	]}`), &list); err != nil {
		t.Fatal(err)
	}
	vs := videos(list.List)
	if len(vs) != 2 || vs[0].ID != "pinned" || !vs[0].Pinned || vs[1].ID != "newer" || vs[1].Pinned {
		t.Fatalf("videos: %+v", vs)
	}
}
