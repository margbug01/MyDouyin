package dy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Direct messages come from imapi.douyin.com as protobuf, the cookies their
// only credential: a request is the IM SDK's envelope around a body keyed by
// its command, and so is the reply. The schema is not published; the field
// numbers here are read off the web page's own requests (2026-11-16).

// pb appends protobuf fields to a buffer: varints, and strings or messages.
type pb []byte

func (b pb) varint(v uint64) pb {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func (b pb) uint(field int, v uint64) pb { return b.varint(uint64(field) << 3).varint(v) }

func (b pb) bytes(field int, v []byte) pb {
	return append(b.varint(uint64(field)<<3|2).varint(uint64(len(v))), v...)
}

func (b pb) str(field int, v string) pb { return b.bytes(field, []byte(v)) }

// field is a decoded protobuf field: a varint's value, or a length-delimited
// field's bytes, a string or a message.
type field struct {
	n   int
	v   uint64
	raw []byte
}

// fields decodes one protobuf message's fields, in order.
func fields(b []byte) ([]field, error) {
	var out []field
	for len(b) > 0 {
		k, n := uvarint(b)
		if n == 0 {
			return nil, errors.New("dy: bad protobuf key")
		}
		b = b[n:]
		f := field{n: int(k >> 3)}
		switch k & 7 {
		case 0:
			if f.v, n = uvarint(b); n == 0 {
				return nil, errors.New("dy: bad protobuf varint")
			}
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return nil, io.ErrUnexpectedEOF
			}
			b = b[8:]
		case 5:
			if len(b) < 4 {
				return nil, io.ErrUnexpectedEOF
			}
			b = b[4:]
		case 2:
			l, n := uvarint(b)
			if n == 0 || uint64(len(b)-n) < l {
				return nil, io.ErrUnexpectedEOF
			}
			f.raw, b = b[n:n+int(l)], b[n+int(l):]
		default:
			return nil, fmt.Errorf("dy: protobuf wire type %d", k&7)
		}
		out = append(out, f)
	}
	return out, nil
}

func uvarint(b []byte) (uint64, int) {
	var x uint64
	for i, c := range b {
		if i == 10 {
			return 0, 0
		}
		x |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return x, i + 1
		}
	}
	return 0, 0
}

// imCall sends the IM command cmd with its body, a message, and returns the
// reply's body for it.
func (c *Client) imCall(path string, cmd int, body pb) ([]byte, error) {
	req := pb(nil).uint(1, uint64(cmd)).uint(2, uint64(10000+c.imSeq.Add(1))).str(3, "0.1.8").str(4, "").
		uint(5, 3).uint(6, 1).str(7, "0d50935:feat/pc-im-group").bytes(8, pb(nil).bytes(cmd, body)).
		str(9, "0").str(11, "douyin_pc").str(14, "360000")
	for _, kv := range [][2]string{{"session_aid", "6383"}, {"session_did", "0"}, {"app_name", "douyin_pc"},
		{"priority_region", "cn"}, {"user_agent", ua}, {"cookie_enabled", "true"},
		{"browser_language", "zh-CN"}, {"browser_platform", "MacIntel"}} {
		req = req.bytes(15, pb(nil).str(1, kv[0]).str(2, kv[1]))
	}
	req = req.uint(18, 1).str(21, "douyin_web").str(22, "web_sdk")

	hr, err := http.NewRequest("POST", "https://imapi.douyin.com"+path, bytes.NewReader(req))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/x-protobuf")
	hr.Header.Set("Accept", "application/x-protobuf")
	hr.Header.Set("User-Agent", ua)
	hr.Header.Set("Referer", "https://www.douyin.com/")
	hr.Header.Set("Cookie", c.cookies().cookie)
	resp, err := c.http.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("dy: %s %s", path, resp.Status)
	}
	fs, err := fields(b)
	if err != nil {
		return nil, fmt.Errorf("dy: %s: %w", path, err)
	}
	var status uint64
	var msg string
	for _, f := range fs {
		switch f.n {
		case 3:
			status = f.v
		case 4:
			msg = string(f.raw)
		case 6:
			inner, err := fields(f.raw)
			if err != nil {
				return nil, fmt.Errorf("dy: %s: %w", path, err)
			}
			for _, g := range inner {
				if g.n == cmd {
					return g.raw, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("dy: %s: status %d %s", path, status, msg)
}

// Chat is a conversation of direct messages, with the messages the inbox
// holds of it, oldest first.
type Chat struct {
	ID   string
	Peer User
	Msgs []Message
}

// Message is a direct message. Tip marks Douyin's own notes in a chat, as
// "对方回复或关注你之前，只能发送一条文字消息".
type Message struct {
	Mine, Tip bool
	Text      string
	Time      time.Time
}

// Chats returns the logged-in user's conversations, the latest first, each
// with its recent messages. It marks nothing read.
// shortcut: only the messages the inbox's first sync carries; older ones need get_by_conversation (cmd 301), whose fields past the anchor are not read off yet.
func (c *Client) Chats() ([]Chat, error) {
	me, err := c.Self()
	if err != nil {
		return nil, err
	}
	var chats []Chat
	byID := map[string]int{}
	var cursor uint64
	for range 10 { // pages, which one has held all so far
		b, err := c.imCall("/v1/message/get_message_by_init", 2043, pb(nil).uint(2, cursor))
		if err != nil {
			return nil, err
		}
		fs, err := fields(b)
		if err != nil {
			return nil, err
		}
		more := false
		for _, f := range fs {
			switch f.n {
			case 1:
				ch, err := chat(f.raw, me.ID)
				if err != nil {
					return nil, err
				}
				if i, ok := byID[ch.ID]; ok {
					chats[i].Msgs = append(chats[i].Msgs, ch.Msgs...)
				} else {
					byID[ch.ID] = len(chats)
					chats = append(chats, ch)
				}
			case 2:
				more = f.v != 0
			case 3:
				cursor = f.v
			}
		}
		if !more {
			break
		}
	}
	for i := range chats {
		sort.SliceStable(chats[i].Msgs, func(a, b int) bool { return chats[i].Msgs[a].Time.Before(chats[i].Msgs[b].Time) })
	}
	sort.SliceStable(chats, func(a, b int) bool { return last(chats[a]).After(last(chats[b])) })
	return chats, c.names(chats)
}

func last(ch Chat) time.Time {
	if len(ch.Msgs) == 0 {
		return time.Time{}
	}
	return ch.Msgs[len(ch.Msgs)-1].Time
}

// chat decodes a conversation of the inbox: its info (1), whose
// participants (6.1) hold uids (1) and sec_uids (5), and its messages (2):
// the sender's uid (7), the content (8), the time in ms (10) and the type
// (6): 1 Douyin's tips, 7 text, others shares and cards.
func chat(b []byte, mySecUID string) (Chat, error) {
	var ch Chat
	var myUID uint64
	parts, err := fields(b)
	if err != nil {
		return ch, err
	}
	for _, p := range parts {
		if p.n != 1 {
			continue
		}
		info, err := fields(p.raw)
		if err != nil {
			return ch, err
		}
		for _, x := range info {
			switch x.n {
			case 1:
				ch.ID = string(x.raw)
			case 6:
				people, _ := fields(x.raw)
				for _, who := range people {
					if who.n != 1 {
						continue
					}
					var uid uint64
					var sec string
					ws, _ := fields(who.raw)
					for _, w := range ws {
						switch w.n {
						case 1:
							uid = w.v
						case 5:
							sec = string(w.raw)
						}
					}
					if sec == mySecUID {
						myUID = uid
					} else {
						ch.Peer.ID = sec
					}
				}
			}
		}
	}
	for _, p := range parts {
		if p.n != 2 {
			continue
		}
		ms, err := fields(p.raw)
		if err != nil {
			return ch, err
		}
		var m Message
		var kind, from uint64
		var content []byte
		for _, x := range ms {
			switch x.n {
			case 6:
				kind = x.v
			case 7:
				from = x.v
			case 8:
				content = x.raw
			case 10:
				m.Time = time.UnixMilli(int64(x.v))
			}
		}
		var body struct {
			Text, Tips string
			Template   []struct{ Key, Name string } // fills the tips' {{key}}s
		}
		json.Unmarshal(content, &body)
		for _, t := range body.Template {
			body.Tips = strings.ReplaceAll(body.Tips, "{{"+t.Key+"}}", t.Name)
		}
		switch {
		case kind == 1 || body.Tips != "":
			m.Tip, m.Text = true, body.Tips
		case body.Text != "":
			m.Text = body.Text
		default:
			m.Text = "[分享或卡片，请在抖音里查看]"
		}
		if m.Text == "" {
			continue
		}
		m.Mine = myUID != 0 && from == myUID
		ch.Msgs = append(ch.Msgs, m)
	}
	return ch, nil
}

// names fills in the chats' peers' names and pictures, which the inbox
// leaves out, as the web page asks for them.
func (c *Client) names(chats []Chat) error {
	var ids []string
	for _, ch := range chats {
		if ch.Peer.ID != "" {
			ids = append(ids, ch.Peer.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	list, _ := json.Marshal(ids)
	b, err := c.call("POST", "/aweme/v1/web/im/user/info/", "", "sec_user_ids="+url.QueryEscape(string(list)))
	if err != nil {
		return err
	}
	var r struct {
		status
		Data []struct {
			SecUID   string `json:"sec_uid"`
			Nickname string
			Avatar   addr `json:"avatar_thumb"`
		}
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return fmt.Errorf("dy: im/user/info: %w", err)
	}
	for _, u := range r.Data {
		for i := range chats {
			if chats[i].Peer.ID == u.SecUID {
				chats[i].Peer.Name, chats[i].Peer.Avatar = u.Nickname, u.Avatar.first()
			}
		}
	}
	return nil
}
