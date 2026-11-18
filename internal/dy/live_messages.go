package dy

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

// LiveMessage is either a comment (GiftCount is zero) or a gift and its combo count.
type LiveMessage struct {
	ID        uint64
	User      string
	Text      string
	GiftCount uint64
	Badges    []LiveBadge
}

type LiveBadge struct {
	Kind  uint64
	Label string
}

// LiveMessages decodes a binary push from the live page's WebSocket.
func LiveMessages(frame []byte) ([]LiveMessage, error) {
	push, err := fields(frame)
	if err != nil {
		return nil, err
	}
	data := protoField(push, 8).raw
	if len(data) == 0 {
		return nil, nil // heartbeat or acknowledgement
	}
	if bytes.HasPrefix(data, []byte{0x1f, 0x8b}) {
		r, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		const limit = 4 << 20
		data, err = io.ReadAll(io.LimitReader(r, limit+1))
		if err != nil {
			return nil, err
		}
		if len(data) > limit {
			return nil, errors.New("dy: live push exceeds decode limit")
		}
	}
	batch, err := fields(data)
	if err != nil {
		return nil, err
	}
	var out []LiveMessage
	for _, entry := range batch {
		if entry.n != 1 {
			continue
		}
		msg, err := fields(entry.raw)
		if err != nil {
			continue
		}
		kind := string(protoField(msg, 1).raw)
		if kind != "WebcastChatMessage" && kind != "WebcastGiftMessage" {
			continue
		}
		payload, err := fields(protoField(msg, 2).raw)
		if err != nil {
			continue
		}
		m := LiveMessage{ID: protoField(msg, 3).v}
		if kind == "WebcastChatMessage" {
			m.User, m.Badges = liveUser(protoField(payload, 2).raw)
			m.Text = string(protoField(payload, 3).raw)
		} else {
			m.User, m.Badges = liveUser(protoField(payload, 7).raw)
			gift, _ := fields(protoField(payload, 15).raw)
			m.Text = string(protoField(gift, 16).raw)
			m.GiftCount = max(uint64(1), protoField(payload, 6).v)
		}
		if m.Text != "" && utf8.ValidString(m.Text) {
			out = append(out, m)
		}
	}
	return out, nil
}

func liveUser(data []byte) (string, []LiveBadge) {
	user, _ := fields(data)
	name := string(protoField(user, 3).raw)
	var badges []LiveBadge
	add := func(badge LiveBadge) {
		if badge.Label != "" && len(badges) < 4 && !slices.Contains(badges, badge) {
			badges = append(badges, badge)
		}
	}
	if public := protoField(user, 78); public.n != 0 {
		// The public comment area sends a map and a separate, packed display order.
		fs, err := fields(public.raw)
		if err != nil {
			return name, nil
		}
		byID := make(map[uint64]LiveBadge)
		var order []uint64
		for _, f := range fs {
			switch f.n {
			case 1:
				entry, err := fields(f.raw)
				if err == nil {
					byID[protoField(entry, 1).v] = decodeLiveBadge(protoField(entry, 2).raw)
				}
			case 2:
				if f.raw == nil {
					order = append(order, f.v)
					continue
				}
				for data := f.raw; len(data) > 0; {
					id, n := binary.Uvarint(data)
					if n <= 0 {
						break
					}
					order = append(order, id)
					data = data[n:]
				}
			}
		}
		for _, id := range order {
			add(byID[id])
		}
		return name, badges
	}
	for _, f := range user {
		if f.n == 21 {
			add(decodeLiveBadge(f.raw))
		}
	}
	return name, badges
}

func decodeLiveBadge(data []byte) LiveBadge {
	fs, err := fields(data)
	if err != nil {
		return LiveBadge{}
	}
	kind := protoField(fs, 6).v
	content, _ := fields(protoField(fs, 8).raw)
	level := protoField(content, 3).v
	label := strings.TrimSpace(string(protoField(content, 4).raw))
	switch kind {
	case 1:
		if level > 0 {
			label = fmt.Sprintf("荣誉 %d", level)
		}
	case 7, 51:
		kind = 7
		if level > 0 {
			name := strings.TrimSpace(string(protoField(content, 1).raw))
			if name == "" || !utf8.ValidString(name) {
				name = "粉丝团"
			}
			label = fmt.Sprintf("%s %d", name, level)
		}
	}
	if label == "" || !utf8.ValidString(label) || strings.ContainsAny(label, "\r\n\t") {
		return LiveBadge{}
	}
	runes := []rune(label)
	if len(runes) > 16 {
		label = string(runes[:15]) + "…"
	}
	return LiveBadge{Kind: kind, Label: label}
}

func protoField(fs []field, number int) field {
	for _, f := range fs {
		if f.n == number {
			return f
		}
	}
	return field{}
}
