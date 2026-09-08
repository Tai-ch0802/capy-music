package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

type writeCall struct {
	method, path string
	body         map[string]any
}

// callLog:handler goroutine 寫、測試 goroutine 讀,鎖住免得哪天測試改成併發就變 race。
type callLog struct {
	mu    sync.Mutex
	calls []writeCall
}

func (l *callLog) add(c writeCall) { l.mu.Lock(); defer l.mu.Unlock(); l.calls = append(l.calls, c) }
func (l *callLog) all() []writeCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}
func (l *callLog) reset() { l.mu.Lock(); defer l.mu.Unlock(); l.calls = nil }

// writeServer:記下每個寫入呼叫;status 非零時從第 failFrom 個呼叫(1-based;0 = 全部)起回它;failCount 非零時只回那麼多次。
func writeServer(t *testing.T, status, failFrom int, failCount ...int) (*Client, *callLog) {
	t.Helper()
	log := &callLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(b) > 0 {
			if err := json.Unmarshal(b, &body); err != nil {
				t.Errorf("body 不是 JSON:%s", b)
			}
		}
		log.add(writeCall{r.Method, r.URL.Path, body})
		n := len(log.all())
		if status != 0 && n >= failFrom && (len(failCount) == 0 || n < failFrom+failCount[0]) {
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"error":{"status":%d,"message":"nope"}}`, status)
			return
		}
		w.Write([]byte(`{"snapshot_id":"x"}`))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.Client(), srv.URL), log
}

func ids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("t%03d", i)
	}
	return out
}

func uris(body map[string]any) []string {
	raw, _ := body["uris"].([]any)
	out := make([]string, len(raw))
	for i, u := range raw {
		out[i] = u.(string)
	}
	return out
}

// 整批取代的分批邊界:0 / 1 / 100 首只 PUT;101 首 PUT 100 + POST 1(position 100);250 首 PUT + POST 100 + POST 50。
func TestApplyOpsBatches(t *testing.T) {
	for _, tc := range []struct {
		n     int
		batch []int // 每個呼叫的 uris 數
		pos   []int // POST 的 position(第一個是 PUT,填 -1)
	}{{0, []int{0}, []int{-1}}, {1, []int{1}, []int{-1}}, {100, []int{100}, []int{-1}}, {101, []int{100, 1}, []int{-1, 100}}, {250, []int{100, 100, 50}, []int{-1, 100, 200}}} {
		c, log := writeServer(t, 0, 0)
		want := ids(tc.n)
		// current 比 want 多一首,ops 是把它移除(n = 0 時 current = [x]、ops = remove 0 → 清空)
		current := append([]string{"x"}, want...)
		if _, err := c.ApplyOps(context.Background(), "p1", current, []provider.PlaylistOp{{Kind: provider.OpRemove, Pos: 0}}); err != nil {
			t.Fatalf("n=%d:%v", tc.n, err)
		}
		calls := log.all()
		if len(calls) != len(tc.batch) {
			t.Fatalf("n=%d:呼叫 %d 次,要 %d:%+v", tc.n, len(calls), len(tc.batch), calls)
		}
		var got []string
		for i, call := range calls {
			method := http.MethodPost
			if i == 0 {
				method = http.MethodPut
			}
			if call.method != method || call.path != "/playlists/p1/items" || len(uris(call.body)) != tc.batch[i] {
				t.Fatalf("n=%d 第 %d 個呼叫:%s %s %d 首", tc.n, i, call.method, call.path, len(uris(call.body)))
			}
			if tc.pos[i] >= 0 {
				if p, _ := call.body["position"].(float64); int(p) != tc.pos[i] {
					t.Fatalf("n=%d 第 %d 個 POST position=%v,要 %d", tc.n, i, call.body["position"], tc.pos[i])
				}
			} else if _, has := call.body["position"]; has {
				t.Fatalf("PUT 不帶 position")
			}
			if i == 0 && call.body["uris"] == nil {
				t.Fatalf("n=%d:PUT 的 uris 要是 [] 不是 null(清空語意)", tc.n)
			}
			got = append(got, uris(call.body)...)
		}
		for i, u := range got {
			if u != "spotify:track:"+want[i] {
				t.Fatalf("n=%d 第 %d 首 uri %q", tc.n, i, u)
			}
		}
	}
}

// 套完 ops 跟 current 一樣 = 零呼叫;rename 走 PUT /playlists/{id} 且在 items 之前;episode uri 照送、local file 整批不送。
func TestApplyOpsNoopRenameAndURIs(t *testing.T) {
	c, log := writeServer(t, 0, 0)
	cur := []string{"a", "b"}
	skipped, err := c.ApplyOps(context.Background(), "p1", cur, []provider.PlaylistOp{{Kind: provider.OpMove, From: 0, Pos: 1}, {Kind: provider.OpMove, From: 1, Pos: 0}})
	if err != nil || skipped != nil || len(log.all()) != 0 {
		t.Fatalf("互相抵銷的 ops 要零呼叫:%v %v %+v", skipped, err, log.all())
	}
	if _, err := c.ApplyOps(context.Background(), "p1", cur, []provider.PlaylistOp{{Kind: provider.OpRename, Name: "通勤 2026"}}); err != nil || len(log.all()) != 1 || log.all()[0].method != http.MethodPut || log.all()[0].path != "/playlists/p1" || log.all()[0].body["name"] != "通勤 2026" {
		t.Fatalf("只改名:%v %+v", err, log.all())
	}
	log.reset()
	if _, err := c.ApplyOps(context.Background(), "p1", cur, []provider.PlaylistOp{{Kind: provider.OpRename, Name: "n"}, {Kind: provider.OpAdd, ProviderID: "spotify:episode:e1", Pos: 2}}); err != nil {
		t.Fatal(err)
	}
	if calls := log.all(); len(calls) != 2 || calls[0].path != "/playlists/p1" || !slices.Equal(uris(calls[1].body), []string{"spotify:track:a", "spotify:track:b", "spotify:episode:e1"}) {
		t.Fatalf("rename 先、items 後,episode uri 原樣:%+v", calls)
	}
	// 推不出去的曲目:local file、空 id → 一個請求都不送(連 rename 也不送),訊息列出位置
	log.reset()
	for _, ops := range [][]provider.PlaylistOp{
		{{Kind: provider.OpRename, Name: "n"}, {Kind: provider.OpAdd, ProviderID: "spotify:local:x:y:z:1", Pos: 2}},
		{{Kind: provider.OpAdd, ProviderID: "", Pos: 0}},
	} {
		_, err := c.ApplyOps(context.Background(), "p1", cur, ops)
		if err == nil || !strings.Contains(err.Error(), "整批不送") || !strings.Contains(err.Error(), "第 ") || len(log.all()) != 0 {
			t.Fatalf("%v:%v %+v", ops, err, log.all())
		}
	}
	if _, err := c.ApplyOps(context.Background(), "p1", []string{"spotify:local:q"}, nil); err != nil || len(log.all()) != 0 {
		t.Fatalf("current 裡有 local file 但沒變 = 零呼叫、不算錯:%v", err)
	}
	if _, err := c.ApplyOps(context.Background(), "p1", []string{"spotify:local:q"}, []provider.PlaylistOp{{Kind: provider.OpRename, Name: "n"}}); err != nil || len(log.all()) != 1 {
		t.Fatalf("只改名不碰 items,local file 不擋:%v %d", err, len(log.all()))
	}
}

// PUT 之後的 POST 失敗 = 平台停在被截短的狀態:回 PartialWriteError 帶已寫 / 目標首數與是否已改名;PUT 本身失敗則不是。
func TestApplyOpsPartialWriteError(t *testing.T) {
	c, log := writeServer(t, http.StatusInternalServerError, 3) // rename、PUT 成功,第一個 POST 失敗
	_, err := c.ApplyOps(context.Background(), "p1", ids(250), []provider.PlaylistOp{{Kind: provider.OpRename, Name: "n"}, {Kind: provider.OpAdd, ProviderID: "z", Pos: 0}})
	var pw *provider.PartialWriteError
	if !errors.As(err, &pw) || pw.PlaylistID != "p1" || pw.Written != 100 || pw.Want != 251 || !pw.Renamed || len(log.all()) != 5 { // rename + PUT + POST 三次都 500
		t.Fatalf("要 PartialWriteError{100/251, renamed}:%v %d", err, len(log.all()))
	}
	if !strings.Contains(err.Error(), "只有前 100 首(目標 251 首)") || !strings.Contains(err.Error(), "名字已先改好") {
		t.Fatalf("訊息:%v", err)
	}
	c2, _ := writeServer(t, http.StatusInternalServerError, 1)
	if _, err := c2.ApplyOps(context.Background(), "p1", ids(250), []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "z", Pos: 0}}); errors.As(err, &pw) || err == nil {
		t.Fatalf("PUT 本身失敗 = 平台沒動,不是 partial:%v", err)
	}
	// POST 對 5xx 多試兩次:第 2 個呼叫(第一批 POST)500 兩次後成功 → 整體成功,呼叫數 = PUT 1 + POST 3 + POST 1
	c3, log3 := writeServer(t, http.StatusInternalServerError, 2, 2)
	if _, err := c3.ApplyOps(context.Background(), "p1", ids(250), []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "z", Pos: 0}}); err != nil || len(log3.all()) != 5 {
		t.Fatalf("POST 重試:%v %d", err, len(log3.all()))
	}
	// 4xx 不重試:第一批 POST 400 → 立刻 partial,呼叫數 2
	c4, log4 := writeServer(t, http.StatusBadRequest, 2)
	if _, err := c4.ApplyOps(context.Background(), "p1", ids(250), []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "z", Pos: 0}}); !errors.As(err, &pw) || len(log4.all()) != 2 {
		t.Fatalf("4xx 不重試:%v %d", err, len(log4.all()))
	}
}

func TestApplyOpsErrors(t *testing.T) {
	c, log := writeServer(t, http.StatusForbidden, 0)
	_, err := c.ApplyOps(context.Background(), "p1", []string{"a"}, []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "b", Pos: 1}})
	if err == nil || !strings.Contains(err.Error(), "只有自己的或協作的清單可以寫") || len(log.all()) != 1 {
		t.Fatalf("403:%v %d", err, len(log.all()))
	}
	if _, err := c.ApplyOps(context.Background(), "p1", []string{"a"}, []provider.PlaylistOp{{Kind: provider.OpRemove, Pos: 5}}); err == nil || !strings.Contains(err.Error(), "越界") || len(log.all()) != 1 {
		t.Fatalf("ops 不合法不打 API:%v %d", err, len(log.all()))
	}
	c2, log2 := writeServer(t, http.StatusInternalServerError, 0)
	if _, err := c2.ApplyOps(context.Background(), "p1", ids(1), []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "z", Pos: 0}}); err == nil || strings.Contains(err.Error(), "協作") || len(log2.all()) != 1 {
		t.Fatalf("500 原樣回:%v", err)
	}
	c3, _ := writeServer(t, http.StatusNotFound, 0)
	if _, err := c3.ApplyOps(context.Background(), "p1", ids(1), []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "z", Pos: 0}}); err == nil || !strings.Contains(err.Error(), "寫入端點不是 /items") {
		t.Fatalf("404 要指向端點路徑:%v", err)
	}
}

// local file:id null、is_local true、uri spotify:local:… → ProviderID = uri、Unpushable。
func TestPlaylistItemsMarksLocalFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"items":[{"item":%s},{"item":{"id":null,"uri":"spotify:local:me:home:song:200","is_local":true,"name":"home","duration_ms":200000,"artists":[{"name":"me"}],"album":{"name":""},"external_ids":{}}}],"total":2}`, trackFx("a"))
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	got, err := c.PlaylistItems(context.Background(), "p1")
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %d", err, len(got))
	}
	if got[0].Unpushable || got[0].ProviderID != "a" {
		t.Fatalf("一般曲目:%+v", got[0])
	}
	if !got[1].Unpushable || got[1].ProviderID != "spotify:local:me:home:song:200" || got[1].Title != "home" {
		t.Fatalf("local file:%+v", got[1])
	}
	// is_local=false 但 id 是 null(沒帶 additional_types 的 podcast episode):也拿 uri,推得出去所以不標 Unpushable
	ep := trackJSON{URI: "spotify:episode:e1", Name: "ep"}
	if tr := ep.toTrack(); tr.ProviderID != "spotify:episode:e1" || tr.Unpushable {
		t.Fatalf("episode:%+v", tr)
	}
}
