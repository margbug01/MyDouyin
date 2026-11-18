package app

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"sync"
	"time"

	"douyin/internal/dy"
	"github.com/egoist/mygo"
)

// The official live page owns the push connection and its acknowledgements.
const liveBridge = `
if (location.origin === 'https://live.douyin.com') {
  const frames = [];
  let opened = false, last = 0;
  window.myDouyinLive = () => ({opened, last, frames: frames.splice(0)});
  window.WebSocket = new Proxy(window.WebSocket, {
    construct(target, args, newTarget) {
      const socket = Reflect.construct(target, args, newTarget);
      const url = new URL(socket.url);
      if (url.protocol !== 'wss:' || !url.hostname.endsWith('.douyin.com') || url.pathname !== '/webcast/im/push/v2/') return socket;
      socket.addEventListener('open', () => { opened = true; last = Date.now(); });
      socket.addEventListener('close', () => { opened = false; });
      socket.addEventListener('message', event => {
        last = Date.now();
        const data = event.data;
        if (typeof data === 'string' || (data.size ?? data.byteLength) > 256 * 1024) return;
        Promise.resolve(data instanceof Blob ? data.arrayBuffer() : data).then(data => {
          const bytes = new Uint8Array(data);
          let bin = '';
          for (let i = 0; i < bytes.length; i += 8192) bin += String.fromCharCode(...bytes.subarray(i, i + 8192));
          frames.push(btoa(bin));
          if (frames.length > 12) frames.shift();
        }).catch(() => {});
      });
      return socket;
    }
  });
}`

var liveRoomCode = regexp.MustCompile(`^[0-9]+$`)

var livePageRules = sync.OnceValues(func() (*mygo.ContentRules, error) {
	// The native player owns all media playback.
	return mygo.CompileContentRules("mydouyin-live-chat", `[
		{"trigger":{"url-filter":".*","resource-type":["media"]},"action":{"type":"block"}},
		{"trigger":{"url-filter":"^https?://pull-"},"action":{"type":"block"}}
	]`)
})

func (chat *liveRoomView) closeWeb() {
	chat.webGen++
	if chat.webIdle != nil {
		chat.webIdle.Stop()
		chat.webIdle = nil
	}
	if chat.view != nil {
		chat.view.Destroy()
		chat.view = nil
	}
}

// Guest HTTP responses omit the message cursor. Keep the official, isolated
// push connection for viewers who have no signed-in session.
func (a *app) readGuestLiveRoom(ctx context.Context, chat *liveRoomView) {
	rules, err := livePageRules()
	var page *mygo.Page
	a.call(func() {
		if a.liveChat != chat || ctx.Err() != nil {
			return
		}
		if err == nil {
			chat.view, err = a.win.NewWebView(mygo.WebViewOptions{URL: liveRoomURL(chat.room.WebRID), Page: mygo.PageOptions{
				Untrusted: true, Session: mygo.NewEphemeralSession(), UserAgentSuffix: safariUA,
				ContentRules: []*mygo.ContentRules{rules}, PreloadScript: liveBridge,
			}})
		}
		if err != nil {
			chat.status = "直播消息暂不可用"
			return
		}
		page = chat.view.Page()
		page.SetAudioMuted(true)
	})
	if page == nil {
		return
	}
	a.readLiveRoom(ctx, chat, func(call context.Context) ([]dy.LiveMessage, time.Duration, error) {
		const interval = 500 * time.Millisecond
		value, err := page.EvalContext(call, `return JSON.stringify(window.myDouyinLive?.() ?? null)`)
		var batch struct {
			Opened bool
			Last   int64
			Frames [][]byte
		}
		if text, ok := value.(string); err == nil && ok {
			_ = json.Unmarshal([]byte(text), &batch)
		}
		var messages []dy.LiveMessage
		for _, frame := range batch.Frames {
			decoded, err := dy.LiveMessages(frame)
			if err == nil {
				messages = append(messages, decoded...)
			}
		}
		if !batch.Opened || time.Since(time.UnixMilli(batch.Last)) >= 40*time.Second {
			return messages, interval, errors.New("dy: live push not connected")
		}
		return messages, interval, nil
	})
}

// Open the signed-in room page only while a write or a gift lookup needs it.
func (a *app) liveWritePage(ctx context.Context, chat *liveRoomView, gifts bool) (func(context.Context, string) (any, error), func(), error) {
	unavailable := errors.New("直播间暂时无法载入，请稍后再试")
	rules, err := livePageRules()
	if err != nil {
		return nil, nil, unavailable
	}
	var page *mygo.Page
	a.call(func() {
		if a.liveChat != chat || ctx.Err() != nil {
			return
		}
		if chat.view == nil {
			chat.view, err = a.win.NewWebView(mygo.WebViewOptions{URL: liveRoomURL(chat.room.WebRID), Page: mygo.PageOptions{
				Untrusted: true, UserAgentSuffix: safariUA, ContentRules: []*mygo.ContentRules{rules},
			}})
			if err != nil {
				return
			}
			chat.view.Page().SetAudioMuted(true)
		}
		chat.webGen++
		if chat.webIdle != nil {
			chat.webIdle.Stop()
			chat.webIdle = nil
		}
		chat.webBusy++
		page = chat.view.Page()
	})
	if page == nil {
		return nil, nil, unavailable
	}
	done := func() {
		a.post(func() {
			chat.webBusy--
			if a.liveChat != chat || chat.webBusy != 0 {
				return
			}
			generation := chat.webGen
			chat.webIdle = time.AfterFunc(webIdle, func() {
				a.post(func() {
					if a.liveChat == chat && chat.webGen == generation && chat.webBusy == 0 {
						chat.closeWeb()
					}
				})
			})
		})
	}
	ready, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	check := `return location.origin === 'https://live.douyin.com' && location.pathname === ` + strconv.Quote("/"+chat.room.WebRID) +
		` && document.readyState === 'complete' && typeof window.bdms === 'object'`
	if gifts {
		check += ` && !!document.querySelector('[data-e2e="gifts-container"] .gift_item_gift_bar')`
	}
	for ready.Err() == nil {
		if value, err := page.EvalContext(ready, check); err == nil && value == true {
			return page.EvalContext, done, nil
		}
		select {
		case <-ready.Done():
		case <-time.After(300 * time.Millisecond):
		}
	}
	done()
	return nil, nil, unavailable
}
