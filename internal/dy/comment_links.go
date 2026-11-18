package dy

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

type CommentLinkKind uint8

const (
	CommentMention CommentLinkKind = iota + 1
	CommentURL
	CommentGroupInvite
	CommentSearch
)

// CommentLink covers a byte range in Comment.Text. Target depends on Kind.
type CommentLink struct {
	Start, End int
	Kind       CommentLinkKind
	Target     string
}

type commentExtra struct {
	Start, End int // UTF-16 offsets in Text
	Type       int
	SecUID     string `json:"sec_uid"`
	Search     string `json:"search_text"`
	Link       string
}

var commentURL = regexp.MustCompile(`(?i)https?://[^\s\p{Z}<>"“”‘’。，！？、；：（）《》「」]+`)

func webURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

func (x comment) links() []CommentLink {
	var links []CommentLink
	if len(x.Extra) != 0 {
		// An offset in the middle of a surrogate pair is not a character boundary.
		offsets := make([]int, 0, len(x.Text)+1)
		for i, r := range x.Text {
			offsets = append(offsets, i)
			if r > 0xffff {
				offsets = append(offsets, -1)
			}
		}
		offsets = append(offsets, len(x.Text))
		for _, extra := range x.Extra {
			if extra.Start < 0 || extra.End <= extra.Start || extra.End >= len(offsets) {
				continue
			}
			start, end := offsets[extra.Start], offsets[extra.End]
			if start < 0 || end <= start {
				continue
			}
			text := x.Text[start:end]
			link := CommentLink{Start: start, End: end}
			switch {
			case extra.Type == 0 && extra.SecUID != "" && strings.HasPrefix(text, "@"):
				link.Kind, link.Target = CommentMention, extra.SecUID
			case extra.Type == 16 && inviteForm(extra.Link) != nil:
				link.Kind, link.Target = CommentGroupInvite, extra.Link
			case webURL(extra.Link):
				link.Kind, link.Target = CommentURL, extra.Link
			case extra.Type == 5 && extra.Search != "":
				link.Kind, link.Target = CommentSearch, extra.Search
			default:
				continue
			}
			links = append(links, link)
		}
	}
	structured := len(links)
	for _, match := range commentURL.FindAllStringIndex(x.Text, -1) {
		start := match[0]
		target := strings.TrimRight(x.Text[start:match[1]], ".,!?;:)]}")
		end := start + len(target)
		if webURL(target) && !slices.ContainsFunc(links[:structured], func(l CommentLink) bool {
			return start < l.End && l.Start < end
		}) {
			links = append(links, CommentLink{start, end, CommentURL, target})
		}
	}
	slices.SortStableFunc(links, func(a, b CommentLink) int { return a.Start - b.Start })
	end := 0
	return slices.DeleteFunc(links, func(l CommentLink) bool {
		if l.Start < end {
			return true
		}
		end = l.End
		return false
	})
}

func inviteForm(raw string) url.Values {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "aweme" || u.Host != "im" || u.Path != "/group_invite_dialog" || u.User != nil {
		return nil
	}
	q := u.Query()
	for _, field := range []string{"cid", "ticket", "secret_type"} {
		if q.Get(field) == "" {
			return nil
		}
	}
	ext, _ := json.Marshal(map[string]string{"group_reserve_v2_ab": "2", "join_source": q.Get("source_type")})
	return url.Values{
		"enter_from": {"comment_group_link"}, "enter_method": {"default_enter_method"}, "ext": {string(ext)},
		"group_id": {q.Get("cid")}, "secret": {q.Get("ticket")}, "secret_type": {q.Get("secret_type")},
	}
}

type CommentGroup struct {
	Name    string `json:"group_name"`
	Desc    string `json:"group_desc"`
	Avatar  string `json:"group_avatar"`
	Members int    `json:"group_member_count"`
	Owner   struct {
		Name   string `json:"owner_name"`
		SecUID string `json:"sec_owner_id"`
	} `json:"group_owner_info"`
}

// CommentGroup reads the invite's preview. Douyin uses a separate action to join.
func (c *Client) CommentGroup(invite string) (CommentGroup, error) {
	form := inviteForm(invite)
	if form == nil {
		return CommentGroup{}, errors.New("无效的群聊邀请")
	}
	var r struct {
		status
		Data struct {
			Verification struct {
				Data CommentGroup
			}
		}
	}
	if err := c.callJSON("POST", "/aweme/v1/web/im_group_api/share/verification/", "", form.Encode(), &r); err != nil {
		return CommentGroup{}, err
	}
	v := r.Data.Verification
	// Douyin can return a join restriction (7602) with a valid preview.
	if v.Data.Name == "" {
		return CommentGroup{}, errors.New("抖音未返回群聊详情")
	}
	return v.Data, nil
}
