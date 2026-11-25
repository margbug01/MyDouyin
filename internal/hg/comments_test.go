package hg

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSM3StandardVector(t *testing.T) {
	got := sm3Sum([]byte("abc"))
	want := "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0"
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("sm3(abc) = %x, want %s", got, want)
	}
}

func TestCommentListAndRepliesParsing(t *testing.T) {
	var seenPaths []string
	var seenSources []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPaths = append(seenPaths, r.URL.Path)
		seenSources = append(seenSources, r.Header.Get("Comment-Source")+":"+r.Header.Get("Server-Channel"))
		if r.Header.Get("X-Gorgon") == "" || r.Header.Get("X-Argus") == "" || r.Header.Get("X-Ladon") == "" {
			http.Error(w, "missing signature", http.StatusBadRequest)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/novel/commentapi/comment/list/7100000000000201/v1/"):
			_, _ = w.Write([]byte(`{
				"code": 0,
				"data": {
					"common_list_info": {
						"total": 42,
						"has_more": true,
						"cursor": "{\"session_id\":\"s1\",\"offset\":20}"
					},
					"data_list": [
						{
							"comment": {
								"comment_id": 7600000000000001001,
								"common": {
									"status": 1,
									"group_id": "7100000000000201",
									"create_timestamp": 1764000000,
									"content": {"text": "太好看了[爱慕]"},
									"user_info": {
										"base_info": {
											"user_name": "追剧人",
											"user_avatar": "http://p3.fqnovelpic.com/avatar.jpg"
										}
									}
								},
								"expand": {"book_id": "7100000000000101"},
								"stat": {"digg_count": 88, "reply_count": 3}
							}
						}
					]
				}
			}`))
		case strings.HasPrefix(r.URL.Path, "/novel/commentapi/comment/list/7100000000000101/v1/"):
			_, _ = w.Write([]byte(`{
				"code": 0,
				"data": {
					"common_list_info": {"total": 5, "has_more": false, "cursor": ""},
					"extra": {"book_info": {"score": "9.4"}, "score_cnt": 1234},
					"data_list": [
						{
							"comment": {
								"comment_id": "7600000000000002002",
								"common": {
									"status": 1,
									"group_id": "7100000000000101",
									"create_timestamp": 1764001000,
									"content": {"text": "剧情节奏很稳[赞]"},
									"user_info": {"base_info": {"user_name": "影评员", "user_avatar": "https://p3.fqnovelpic.com/a.png"}}
								},
								"expand": {"book_id": "7100000000000101", "score": "10"},
								"stat": {"digg_count": 12, "reply_count": 1}
							}
						}
					]
				}
			}`))
		case strings.HasPrefix(r.URL.Path, "/novel/commentapi/reply/list/7600000000000001001/v1/"):
			_, _ = w.Write([]byte(`{
				"code": 0,
				"data": {
					"comment": {
						"common": {"group_id": "7100000000000201"},
						"expand": {"book_id": "7100000000000101"}
					},
					"comment_list_info": {"total": 3, "has_more": true, "cursor": "10"},
					"reply_list": [
						{
							"reply_id": 7600000000000009001,
							"reply_to_reply_id": "7600000000000008001",
							"reply_to_user_info": {"base_info": {"user_name": "原楼主"}},
							"Common": {
								"create_timestamp": 1764002000,
								"content": {"text": "同感[捂脸]"},
								"user_info": {"base_info": {"user_name": "路人甲", "user_avatar": "https://p3.fqnovelpic.com/r.png"}}
							},
							"stat": {"digg_count": 7}
						}
					]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(Config{Direct: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.commentClient = srv.Client()
	c.commentOrigin = srv.URL

	ctx := context.Background()
	epPage, err := c.ListEpisodeComments(ctx, "7100000000000101", "7100000000000201", "")
	if err != nil {
		t.Fatalf("ListEpisodeComments: %v", err)
	}
	if epPage.Total != 42 || !epPage.HasMore || len(epPage.Items) != 1 {
		t.Fatalf("unexpected episode page: %+v", epPage)
	}
	cm := epPage.Items[0]
	if cm.ID != "7600000000000001001" || cm.Author != "追剧人" || cm.AvatarURL != "http://p3.fqnovelpic.com/avatar.jpg" || cm.Likes != 88 || cm.ReplyCount != 3 {
		t.Fatalf("unexpected comment: %+v", cm)
	}

	srPage, err := c.ListSeriesComments(ctx, "7100000000000101", "")
	if err != nil {
		t.Fatalf("ListSeriesComments: %v", err)
	}
	if srPage.Score != "9.4" || srPage.ScoreCount != 1234 || len(srPage.Items) != 1 || srPage.Items[0].Score != "10" {
		t.Fatalf("unexpected series review page: %+v", srPage)
	}

	repPage, err := c.ListEpisodeReplies(ctx, "7100000000000101", "7100000000000201", "7600000000000001001", "")
	if err != nil {
		t.Fatalf("ListEpisodeReplies: %v", err)
	}
	if !repPage.HasMore || repPage.Cursor != "10" || len(repPage.Items) != 1 {
		t.Fatalf("unexpected reply page: %+v", repPage)
	}
	rep := repPage.Items[0]
	if rep.ID != "7600000000000009001" || rep.ReplyTo != "原楼主" || rep.Text != "同感[捂脸]" || rep.Likes != 7 {
		t.Fatalf("unexpected reply: %+v", rep)
	}

	wantSources := []string{"4:18", "1:34", "504:18"}
	if strings.Join(seenSources, ",") != strings.Join(wantSources, ",") {
		t.Fatalf("sources = %v, want %v", seenSources, wantSources)
	}
}

func TestLiveHongguoComments(t *testing.T) {
	if os.Getenv("HG_LIVE_COMMENTS") != "1" {
		t.Skip("set HG_LIVE_COMMENTS=1 to run live Hongguo comment API verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	c, err := New(Config{ProfileDir: t.TempDir(), Direct: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	disc, err := c.Discover(ctx, "推荐", "")
	if err != nil || len(disc.Items) == 0 {
		t.Fatalf("Discover: %v (len=%d)", err, len(disc.Items))
	}
	var series Series
	var ep Episode
	for _, s := range disc.Items {
		eps, err := c.ListEpisodes(ctx, s.ID)
		if err == nil && len(eps) > 0 {
			series = s
			ep = eps[0]
			break
		}
	}
	if series.ID == "" || ep.ID == "" {
		t.Fatal("failed to locate playable series + episode")
	}
	t.Logf("testing series=%s (%s) ep=%s", series.ID, series.Title, ep.ID)

	epComments, err := c.ListEpisodeComments(ctx, series.ID, ep.ID, "")
	if err != nil {
		t.Fatalf("ListEpisodeComments: %v", err)
	}
	t.Logf("episode comments: total=%d loaded=%d hasMore=%v", epComments.Total, len(epComments.Items), epComments.HasMore)
	if len(epComments.Items) == 0 {
		t.Fatalf("expected non-empty episode comments for %s/%s", series.ID, ep.ID)
	}
	for i, cm := range epComments.Items[:min(3, len(epComments.Items))] {
		t.Logf("  ep[%d] %s (likes=%d replies=%d avatar=%v): %s", i, cm.Author, cm.Likes, cm.ReplyCount, cm.AvatarURL != "", cm.Text)
	}

	// Test replies on the first comment that has replies.
	for _, cm := range epComments.Items {
		if cm.ReplyCount > 0 {
			reps, err := c.ListEpisodeReplies(ctx, series.ID, ep.ID, cm.ID, "")
			if err != nil {
				t.Fatalf("ListEpisodeReplies(%s): %v", cm.ID, err)
			}
			t.Logf("episode replies for %s: total=%d loaded=%d hasMore=%v", cm.ID, reps.Total, len(reps.Items), reps.HasMore)
			if len(reps.Items) > 0 {
				t.Logf("  rep[0] %s -> %q: %s", reps.Items[0].Author, reps.Items[0].ReplyTo, reps.Items[0].Text)
			}
			break
		}
	}

	srComments, err := c.ListSeriesComments(ctx, series.ID, "")
	if err != nil {
		t.Fatalf("ListSeriesComments: %v", err)
	}
	t.Logf("series reviews: score=%s (%d) total=%d loaded=%d hasMore=%v", srComments.Score, srComments.ScoreCount, srComments.Total, len(srComments.Items), srComments.HasMore)
	for i, cm := range srComments.Items[:min(2, len(srComments.Items))] {
		t.Logf("  sr[%d] %s (score=%s likes=%d replies=%d): %s", i, cm.Author, cm.Score, cm.Likes, cm.ReplyCount, cm.Text)
	}
}
