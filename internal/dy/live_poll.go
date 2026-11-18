package dy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// LivePoll reads public room messages without running the live website. Call it
// serially for one room; each successful response supplies the next cursor.
func (c *Client) LivePoll(room Live) func(context.Context) ([]LiveMessage, time.Duration, error) {
	const origin = "https://live.douyin.com"
	query := url.Values{
		"aid": {"6383"}, "app_name": {"douyin_web"}, "live_id": {"1"}, "endpoint": {"live_pc"},
		"room_id": {room.RoomID}, "resp_content_type": {"protobuf"}, "version_code": {"180800"},
		"did_rule": {"3"}, "device_id": {""}, "user_unique_id": {strconv.FormatInt(time.Now().UnixNano(), 10)},
		"support_wrds": {"1"}, "identity": {"audience"}, "fetch_rule": {"1"}, "need_persist_msg_count": {"15"},
		"insert_task_id": {""}, "live_reason": {""}, "last_rtt": {"0"}, "cursor": {""}, "internal_ext": {""},
		"device_platform": {"web"}, "cookie_enabled": {"true"}, "screen_width": {"1280"}, "screen_height": {"820"},
		"browser_language": {"zh-CN"}, "browser_platform": {"Win32"}, "browser_name": {"Mozilla"},
		"browser_version": {strings.TrimPrefix(ua, "Mozilla/")}, "browser_online": {"true"}, "tz_name": {"Asia/Shanghai"},
	}
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return func(ctx context.Context) ([]LiveMessage, time.Duration, error) {
		query.Set("msToken", randString(107, token))
		encoded := query.Encode()
		address := origin + "/webcast/im/fetch/?" + encoded + "&a_bogus=" + url.QueryEscape(c.signer.Sign(encoded, ""))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Referer", origin+"/"+room.WebRID)
		req.Header.Set("Cookie", c.cookies().cookie)
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, 0, fmt.Errorf("dy: live message HTTP status %d", resp.StatusCode)
		}
		const limit = 4 << 20
		data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, 0, err
		}
		if len(data) > limit {
			return nil, 0, errors.New("dy: live response exceeds decode limit")
		}
		batch, err := fields(data)
		if err != nil {
			return nil, 0, err
		}
		cursor := protoField(batch, 2).raw
		if len(cursor) == 0 {
			return nil, 0, errors.New("dy: live response has no cursor")
		}
		query.Set("cursor", string(cursor))
		query.Set("internal_ext", string(protoField(batch, 5).raw))
		wait := min(max(protoField(batch, 3).v, 800), 10_000)
		return liveMessages(batch), time.Duration(wait) * time.Millisecond, nil
	}
}
