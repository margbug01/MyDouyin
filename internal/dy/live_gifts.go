package dy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type LiveGift struct {
	ID    int64
	Name  string
	Price int
}

// The gift bar holds the room's current offering, including availability and price.
// shortcut: only ordinary gifts shown in the host's bar, until the full catalog is supported.
const liveGiftCatalog = `
const gifts = [];
const seen = new Set();
for (const el of document.querySelectorAll('[data-e2e="gifts-container"] .gift_item_gift_bar')) {
  let fiber = el[Object.keys(el).find(key => key.startsWith('__reactFiber$'))];
  while (fiber && !fiber.memoizedProps?.gift) fiber = fiber.return;
  const props = fiber?.memoizedProps;
  const gift = props?.gift;
  if (!gift || props.isToAnchor !== true || gift.type !== 1 || gift.lockStatus !== 0 ||
      gift.fansClubLevel || gift.honorLevel || gift.namingAvatar || gift.afterSendAction ||
      !Number.isSafeInteger(gift.id) || gift.id <= 0 || !Number.isSafeInteger(gift.price) || gift.price <= 0 ||
      typeof gift.name !== 'string' || !gift.name.trim() || seen.has(gift.id)) continue;
  const labels = el.innerText.split('\n').map(s => s.trim());
  if (!labels.includes(gift.name) || !labels.includes(gift.price + '钻')) continue;
  seen.add(gift.id);
  gifts.push({id: gift.id, name: gift.name, price: gift.price});
}
`

type liveGiftReply struct {
	Gifts []LiveGift
	Error string
	HTTP  int
	Body  string
}

type roomEval func(context.Context, string) (any, error)

func ReadLiveGifts(ctx context.Context, eval roomEval, room Live) ([]LiveGift, error) {
	result, err := evalLiveGifts(ctx, eval, room, LiveGift{}, `return JSON.stringify({gifts});`)
	if err != nil {
		return nil, err
	}
	if len(result.Gifts) == 0 {
		return nil, errors.New("抖音暂未提供可送礼物")
	}
	return result.Gifts, nil
}

// ErrLiveGiftUnconfirmed means the request may have reached Douyin without a receipt.
var ErrLiveGiftUnconfirmed = errors.New("未收到送礼确认，请核对礼物记录或钻石余额")

// SendLiveGift submits one gift with the price just confirmed by the viewer.
func SendLiveGift(ctx context.Context, eval roomEval, room Live, gift LiveGift) error {
	for _, id := range []string{room.RoomID, room.OwnerID} {
		if n, err := strconv.ParseUint(id, 10, 64); err != nil || n == 0 {
			return errors.New("主播或直播间信息不完整")
		}
	}
	if gift.ID <= 0 || gift.Price <= 0 || gift.Name == "" {
		return errors.New("请选择礼物")
	}
	result, err := evalLiveGifts(ctx, eval, room, gift, `
const current = gifts.find(g => g.id === confirmed.ID);
if (!current || current.price !== confirmed.Price || current.name !== confirmed.Name) {
  return JSON.stringify({error: '礼物或价格已变化，请刷新后重新选择'});
}
if (typeof window.bdms !== 'object') return JSON.stringify({error: '直播间还没准备好，请稍后再试'});
const url = new URL('/webcast/gift/send/', location.origin);
const source = window.__INVISIBLE_QUERY__ ?? {};
url.search = new URLSearchParams({aid: '6383', app_name: 'douyin_web', live_id: '1', device_platform: 'web',
  language: navigator.language, enter_from: source.enter_from_merge ?? '', cookie_enabled: navigator.cookieEnabled,
  screen_width: screen.width, screen_height: screen.height, browser_language: navigator.language,
  browser_platform: navigator.platform});
const body = new URLSearchParams({room_id: roomID, to_room_id: roomID, to_user_id: ownerID,
  room_anchor_id: ownerID, to_user_ids: '', send_type: '4', send_scene: '1', gift_source: '0', buff_level: '0',
  gift_id: String(current.id), price: String(current.price), count: '1', extra: JSON.stringify({total_count: 1}),
  tags_extra: JSON.stringify({gift_source: 'gift_bar', enter_from_merge: source.enter_from_merge ?? '', enter_method: source.enter_method ?? '', is_client: false})});
return await new Promise(resolve => {
  const req = new XMLHttpRequest();
  req.open('POST', url.href);
  req.withCredentials = true;
  req.timeout = 8000;
  req.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded');
  req.setRequestHeader('rpc-persist-webcast-trace-id', crypto.randomUUID());
  req.onload = () => resolve(JSON.stringify({http: req.status, body: req.responseText}));
  req.onerror = req.ontimeout = req.onabort = () => resolve('{}');
  req.send(body.toString());
});`)
	if err != nil {
		return err
	}
	var reply struct {
		Code *int `json:"status_code"`
	}
	if result.HTTP != 200 || json.Unmarshal([]byte(result.Body), &reply) != nil || reply.Code == nil {
		return ErrLiveGiftUnconfirmed
	}
	switch *reply.Code {
	case 0:
		return nil
	case 40001:
		return errors.New("钻石余额不足")
	default:
		return fmt.Errorf("抖音未接受送礼（%d）", *reply.Code)
	}
}

func evalLiveGifts(ctx context.Context, eval roomEval, room Live, gift LiveGift, action string) (liveGiftReply, error) {
	args, _ := json.Marshal([]any{room.WebRID, room.RoomID, room.OwnerID, gift})
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	value, err := eval(ctx, "const [webRID, roomID, ownerID, confirmed] = "+string(args)+`;
if (location.origin !== 'https://live.douyin.com' || location.pathname !== '/' + webRID ||
    document.readyState !== 'complete') return JSON.stringify({error: '直播间还没准备好，请稍后再试'});
`+liveGiftCatalog+action)
	encoded, _ := value.(string)
	var result liveGiftReply
	if err != nil || json.Unmarshal([]byte(encoded), &result) != nil {
		if gift.ID != 0 {
			return result, ErrLiveGiftUnconfirmed
		}
		return result, errors.New("礼物列表暂时无法读取")
	}
	if result.Error != "" {
		return result, errors.New(result.Error)
	}
	return result, nil
}
