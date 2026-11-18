package dy

import (
	"bytes"
	"compress/gzip"
	"reflect"
	"slices"
	"testing"
)

func TestLiveMessagesDecodeChatAndGift(t *testing.T) {
	msg := func(kind string, id uint64, payload pb) pb {
		return pb(nil).str(1, kind).bytes(2, payload).uint(3, id)
	}
	person := pb(nil).str(3, "观众甲")
	chat := msg("WebcastChatMessage", 10, pb(nil).bytes(2, person).str(3, "你好 👋"))
	gift := msg("WebcastGiftMessage", 11, pb(nil).uint(6, 3).bytes(7, person).bytes(15, pb(nil).str(16, "小心心")))
	data := pb(nil).bytes(1, chat).bytes(1, gift).bytes(1, msg("WebcastLikeMessage", 12, nil))
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := LiveMessages(pb(nil).bytes(8, compressed.Bytes()))
	want := []LiveMessage{{ID: 10, User: "观众甲", Text: "你好 👋"}, {ID: 11, User: "观众甲", Text: "小心心", GiftCount: 3}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, error %v", got, err)
	}
}

func TestLiveUserUsesPublicBadgeOrder(t *testing.T) {
	honor := pb(nil).uint(6, 1).bytes(8, pb(nil).uint(3, 18))
	fan := pb(nil).uint(6, 51).bytes(8, pb(nil).str(1, "小星星").uint(3, 5))
	admin := pb(nil).uint(6, 27).bytes(8, pb(nil).str(4, "房管"))
	entry := func(id uint64, badge pb) pb { return pb(nil).uint(1, id).bytes(2, badge) }
	public := pb(nil).bytes(1, entry(6, honor)).bytes(1, entry(12, fan)).bytes(1, entry(21, admin)).
		bytes(2, pb(nil).varint(12).varint(6).varint(12)).uint(2, 21)
	legacy := pb(nil).str(3, "观众甲").bytes(21, honor).bytes(21, honor)
	name, badges := liveUser(append(slices.Clone(legacy), pb(nil).bytes(78, public)...))
	want := []LiveBadge{{Kind: 7, Label: "小星星 5"}, {Kind: 1, Label: "荣誉 18"}, {Kind: 27, Label: "房管"}}
	if name != "观众甲" || !slices.Equal(badges, want) {
		t.Fatalf("name %q, badges %v", name, badges)
	}
	if _, badges := liveUser(legacy); !slices.Equal(badges, want[1:2]) {
		t.Fatalf("legacy badges %v", badges)
	}
	if _, badges := liveUser(legacy.bytes(78, nil)); len(badges) != 0 {
		t.Fatalf("explicitly empty public badges: %v", badges)
	}
}
