package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

type writeCall struct {
	method, path string
	body         map[string]any
}

// writeServer:記下每個寫入呼叫;status 非零時全部回它。
func writeServer(t *testing.T, status int) (*Client, *[]writeCall) {
	t.Helper()
	var calls []writeCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(b) > 0 {
			if err := json.Unmarshal(b, &body); err != nil {
				t.Errorf("body 不是 JSON:%s", b)
			}
		}
		calls = append(calls, writeCall{r.Method, r.URL.Path, body})
		if status != 0 {
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"error":{"status":%d,"message":"nope"}}`, status)
			return
		}
		w.Write([]byte(`{"snapshot_id":"x"}`))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.Client(), srv.URL), &calls
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
		c, calls := writeServer(t, 0)
		want := ids(tc.n)
		// current 比 want 多一首,ops 是把它移除(n = 0 時 current = [x]、ops = remove 0 → 清空)
		current := append([]string{"x"}, want...)
		if _, err := c.ApplyOps(context.Background(), "p1", current, []provider.PlaylistOp{{Kind: provider.OpRemove, Pos: 0}}); err != nil {
			t.Fatalf("n=%d:%v", tc.n, err)
		}
		if len(*calls) != len(tc.batch) {
			t.Fatalf("n=%d:呼叫 %d 次,要 %d:%+v", tc.n, len(*calls), len(tc.batch), *calls)
		}
		var got []string
		for i, call := range *calls {
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

// 套完 ops 跟 current 一樣 = 零呼叫;rename 走 PUT /playlists/{id} 且在 items 之前;local file 的 id 本身是 uri。
func TestApplyOpsNoopRenameAndLocalURI(t *testing.T) {
	c, calls := writeServer(t, 0)
	cur := []string{"a", "b"}
	skipped, err := c.ApplyOps(context.Background(), "p1", cur, []provider.PlaylistOp{{Kind: provider.OpMove, From: 0, Pos: 1}, {Kind: provider.OpMove, From: 1, Pos: 0}})
	if err != nil || skipped != nil || len(*calls) != 0 {
		t.Fatalf("互相抵銷的 ops 要零呼叫:%v %v %+v", skipped, err, *calls)
	}
	if _, err := c.ApplyOps(context.Background(), "p1", cur, []provider.PlaylistOp{{Kind: provider.OpRename, Name: "通勤 2026"}}); err != nil || len(*calls) != 1 || (*calls)[0].method != http.MethodPut || (*calls)[0].path != "/playlists/p1" || (*calls)[0].body["name"] != "通勤 2026" {
		t.Fatalf("只改名:%v %+v", err, *calls)
	}
	*calls = nil
	if _, err := c.ApplyOps(context.Background(), "p1", cur, []provider.PlaylistOp{{Kind: provider.OpRename, Name: "n"}, {Kind: provider.OpAdd, ProviderID: "spotify:local:x:y:z:1", Pos: 2}}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[0].path != "/playlists/p1" || !slices.Equal(uris((*calls)[1].body), []string{"spotify:track:a", "spotify:track:b", "spotify:local:x:y:z:1"}) {
		t.Fatalf("rename 先、items 後,local uri 原樣:%+v", *calls)
	}
}

func TestApplyOpsErrors(t *testing.T) {
	c, calls := writeServer(t, http.StatusForbidden)
	_, err := c.ApplyOps(context.Background(), "p1", []string{"a"}, []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "b", Pos: 1}})
	if err == nil || !strings.Contains(err.Error(), "只有自己的或協作的清單可以寫") || len(*calls) != 1 {
		t.Fatalf("403:%v %d", err, len(*calls))
	}
	if _, err := c.ApplyOps(context.Background(), "p1", []string{"a"}, []provider.PlaylistOp{{Kind: provider.OpRemove, Pos: 5}}); err == nil || !strings.Contains(err.Error(), "越界") || len(*calls) != 1 {
		t.Fatalf("ops 不合法不打 API:%v %d", err, len(*calls))
	}
	c2, calls2 := writeServer(t, http.StatusInternalServerError)
	if _, err := c2.ApplyOps(context.Background(), "p1", ids(1), []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "z", Pos: 0}}); err == nil || strings.Contains(err.Error(), "協作") || len(*calls2) != 1 {
		t.Fatalf("500 原樣回:%v", err)
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
}
