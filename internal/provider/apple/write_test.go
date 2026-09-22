package apple

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// fakeLibrary:假 amp-api 的 library 寫入端點(計畫 2026-09-22-apple-write.md §1 表格的形狀)。記錄每個寫入請求;
// DELETE 一律 t.Errorf——這個套件不該送任何 DELETE …/tracks(不帶 ids 會清空整份、mode=all 會連刪兩列)。
type fakeLibrary struct {
	t        *testing.T
	mu       sync.Mutex
	canEdit  bool
	name     string
	entries  []fakeEntry
	seq      int
	created  bool
	listLag  int  // 建清單後前幾次列表不含新清單(iCloud 傳播延遲)
	listFail bool // 建清單後列表回 500
	lists    int
	infos    int // GET 清單本體的次數
	reads    int // GET /tracks 的次數
	posts    int
	failAt   int    // 第 n 個 POST(從 1 起)回 500;0 = 不失敗
	failMsg  string // 500 的 title
	writes   []fakeReq
}

type fakeEntry struct{ ID, Catalog string }

type fakeReq struct{ Method, Path, Body string }

func newFakeLibrary(t *testing.T, canEdit bool, catalogIDs ...string) *fakeLibrary {
	f := &fakeLibrary{t: t, canEdit: canEdit, name: "通勤", failMsg: "Upstream Service Error"}
	for _, c := range catalogIDs {
		f.entries = append(f.entries, f.newEntry(trackRef{ID: c, Type: "songs"}))
	}
	return f
}

// newEntry:catalog 曲目加進清單變成一列(真帳號:列 id 是 i.…);library-songs 型別的 ref 指的是既有列。
func (f *fakeLibrary) newEntry(ref trackRef) fakeEntry {
	if ref.Type == "library-songs" {
		for _, e := range f.entries {
			if e.ID == ref.ID {
				return e
			}
		}
		f.t.Errorf("PUT / POST 指到不存在的列 id %s", ref.ID)
		return fakeEntry{ID: ref.ID}
	}
	if ref.ID == "" || strings.Contains(ref.ID, ".") {
		f.t.Errorf("songs 型別要是 catalog id:%q", ref.ID)
	}
	f.seq++
	return fakeEntry{ID: fmt.Sprintf("i.%d", f.seq), Catalog: ref.ID}
}

func (f *fakeLibrary) checkType(ref trackRef) {
	want := "songs"
	if strings.Contains(ref.ID, ".") {
		want = "library-songs"
	}
	if ref.Type != want {
		f.t.Errorf("id %s 的 type 應為 %s,得到 %q(漏 type 或 type 錯 Apple 會靜默丟掉曲目)", ref.ID, want, ref.Type)
	}
}

func (f *fakeLibrary) catalogs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.entries))
	for i, e := range f.entries {
		out[i] = e.Catalog
		if e.Catalog == "" {
			out[i] = e.ID
		}
	}
	return out
}

func (f *fakeLibrary) writesOf(method string) []fakeReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeReq
	for _, w := range f.writes {
		if w.Method == method {
			out = append(out, w)
		}
	}
	return out
}

func refsOf(t *testing.T, body string) []trackRef {
	t.Helper()
	var req struct {
		Data []trackRef `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("body 不是 {data:[…]}:%s", body)
	}
	return req.Data
}

func (f *fakeLibrary) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodDelete {
			f.t.Errorf("不得送 DELETE:%s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		if r.Method != http.MethodGet {
			if r.Header.Get("Content-Type") != "application/json" {
				f.t.Errorf("%s %s 缺 Content-Type: application/json", r.Method, r.URL.Path)
			}
			f.writes = append(f.writes, fakeReq{r.Method, r.URL.Path, string(body)})
		}
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && p == "/me/library/playlists":
			var req struct {
				Attributes struct {
					Name     string `json:"name"`
					IsPublic *bool  `json:"isPublic"`
				} `json:"attributes"`
			}
			_ = json.Unmarshal(body, &req)
			if req.Attributes.IsPublic == nil || *req.Attributes.IsPublic {
				f.t.Errorf("建清單要明講 isPublic:false:%s", body)
			}
			f.created = true
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"data":[{"id":"p.new","type":"library-playlists","attributes":{"name":%q,"canEdit":true}}]}`, req.Attributes.Name)
		case r.Method == http.MethodGet && p == "/me/library/playlists":
			f.lists++
			if f.created && f.listFail {
				w.WriteHeader(500)
				w.Write([]byte(`{"errors":[{"status":"500","title":"Upstream Service Error"}]}`))
				return
			}
			items := `{"id":"p.1","attributes":{"name":"通勤","canEdit":true}}`
			if f.created && f.lists > f.listLag {
				items += `,{"id":"p.new","attributes":{"name":"新清單","canEdit":true}}`
			}
			fmt.Fprintf(w, `{"data":[%s]}`, items)
		case r.Method == http.MethodGet && p == "/me/library/playlists/p.1":
			f.infos++
			fmt.Fprintf(w, `{"data":[{"id":"p.1","attributes":{"name":%q,"canEdit":%v,"canDelete":true}}]}`, f.name, f.canEdit)
		case r.Method == http.MethodPatch && p == "/me/library/playlists/p.1":
			var req struct {
				Attributes map[string]string `json:"attributes"`
			}
			_ = json.Unmarshal(body, &req)
			if len(req.Attributes) != 1 || req.Attributes["name"] == "" {
				f.t.Errorf("PATCH 只該送 attributes.name:%s", body)
			}
			f.name = req.Attributes["name"]
			w.WriteHeader(204)
		case p == "/me/library/playlists/p.1/tracks":
			switch r.Method {
			case http.MethodGet:
				f.reads++
				if len(f.entries) == 0 { // Apple 對空清單回 404
					w.WriteHeader(404)
					w.Write([]byte(`{"errors":[{"status":"404","title":"Resource Not Found"}]}`))
					return
				}
				var items []string
				for _, e := range f.entries {
					rel := ""
					if e.Catalog != "" {
						rel = fmt.Sprintf(`,"relationships":{"catalog":{"data":[{"id":%q,"type":"songs","attributes":{"isrc":"TW%s"}}]}}`, e.Catalog, e.Catalog)
					}
					items = append(items, fmt.Sprintf(`{"id":%q,"type":"library-songs","attributes":{"name":"song","artistName":"artist"}%s}`, e.ID, rel))
				}
				fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(items, ","))
			case http.MethodPost:
				f.posts++
				if f.failAt == f.posts {
					w.WriteHeader(500)
					fmt.Fprintf(w, `{"errors":[{"status":"500","title":%q}]}`, f.failMsg)
					return
				}
				for _, ref := range refsOf(f.t, string(body)) {
					f.checkType(ref)
					f.entries = append(f.entries, f.newEntry(ref))
				}
				w.WriteHeader(204)
			case http.MethodPut:
				var next []fakeEntry
				for _, ref := range refsOf(f.t, string(body)) {
					f.checkType(ref)
					next = append(next, f.newEntry(ref))
				}
				f.entries = next
				w.WriteHeader(204)
			default:
				f.t.Errorf("非預期:%s %s", r.Method, p)
				w.WriteHeader(405)
			}
		default:
			f.t.Errorf("非預期:%s %s", r.Method, p)
			w.WriteHeader(404)
		}
	}
}

func writeWorld(t *testing.T, canEdit bool, catalogIDs ...string) (*fakeLibrary, *Provider) {
	t.Helper()
	f := newFakeLibrary(t, canEdit, catalogIDs...)
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return f, New(srv.Client(), srv.URL, "DEV", "MUT", "tw")
}

func add(id string, pos int) provider.PlaylistOp {
	return provider.PlaylistOp{Kind: provider.OpAdd, ProviderID: id, Pos: pos}
}

func ids(refs []trackRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.ID
	}
	return out
}

func TestCapsAndPushable(t *testing.T) {
	p := New(nil, "https://x", "d", "u", "tw")
	for _, c := range []provider.Capability{provider.CapPlaylistCreate, provider.CapPlaylistAppend, provider.CapPlaylistRemove, provider.CapPlaylistReorder, provider.CapPlaylistRename} {
		if !p.Caps().Has(c) {
			t.Errorf("決策 49:Apple 要宣告寫入能力 %b", c)
		}
	}
	if p.Pushable("") || !p.Pushable("i.abc") || !p.Pushable("a.123") || !p.Pushable("1158763996") {
		t.Error("catalog id 與 library 列 id 都推得動,只有空 id 不行")
	}
}

func TestRefOfPicksTypeByIDShape(t *testing.T) {
	for id, want := range map[string]string{"1158763996": "songs", "i.qQd0L4euRmMdxr": "library-songs", "a.1538099572": "library-songs"} {
		if got := refOf(id).Type; got != want {
			t.Errorf("refOf(%s).Type = %s, want %s", id, got, want)
		}
	}
}

// 純尾端 append 走官方 POST,每批 100、順序不變、type 一律 songs;不重讀 /tracks、不 PUT。零 op 時一個請求都不送。
func TestApplyOpsPureAppendPostsInBatches(t *testing.T) {
	for _, n := range []int{0, 1, 100, 101, 250} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f, p := writeWorld(t, true, "c1")
			var ops []provider.PlaylistOp
			var want []string
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("n%d", i)
				ops = append(ops, add(id, 1+i))
				want = append(want, id)
			}
			skipped, err := p.ApplyOps(context.Background(), "p.1", []string{"c1"}, ops)
			if err != nil || skipped != nil {
				t.Fatalf("(%v, %v)", skipped, err)
			}
			posts := f.writesOf(http.MethodPost)
			if n == 0 {
				if len(f.writes) != 0 || f.infos != 0 {
					t.Fatalf("零 op 不該打任何請求:%+v infos=%d", f.writes, f.infos)
				}
				return
			}
			if wantPosts := (n + 99) / 100; len(posts) != wantPosts || len(f.writesOf(http.MethodPut)) != 0 || f.reads != 0 || f.infos != 1 {
				t.Fatalf("要 %d 個 POST、零 PUT、零重讀、查一次 canEdit:posts=%d puts=%d reads=%d infos=%d", wantPosts, len(posts), len(f.writesOf(http.MethodPut)), f.reads, f.infos)
			}
			var got []string
			for _, w := range posts {
				refs := refsOf(t, w.Body)
				if len(refs) > writeBatch {
					t.Errorf("單批 %d 首超過 %d", len(refs), writeBatch)
				}
				got = append(got, ids(refs)...)
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("POST 的 id 順序要等於新增順序:\n%v\n%v", got, want)
			}
			if strings.Join(f.catalogs(), ",") != "c1,"+strings.Join(want, ",") {
				t.Fatalf("平台結果:%v", f.catalogs())
			}
		})
	}
}

func TestApplyOpsRenameThenAppendOrder(t *testing.T) {
	f, p := writeWorld(t, true, "c1")
	ops := []provider.PlaylistOp{{Kind: provider.OpRename, Name: "深夜"}, add("c2", 1)}
	if _, err := p.ApplyOps(context.Background(), "p.1", []string{"c1"}, ops); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 2 || f.writes[0].Method != http.MethodPatch || f.writes[1].Method != http.MethodPost {
		t.Fatalf("先 PATCH 再 POST:%+v", f.writes)
	}
	if !strings.Contains(f.writes[0].Body, `{"attributes":{"name":"深夜"}}`) || f.name != "深夜" {
		t.Fatalf("PATCH 只送 name:%s", f.writes[0].Body)
	}
	// 只改名:不重讀、不 PUT、不 POST
	f2, p2 := writeWorld(t, true, "c1")
	if _, err := p2.ApplyOps(context.Background(), "p.1", []string{"c1"}, ops[:1]); err != nil {
		t.Fatal(err)
	}
	if len(f2.writes) != 1 || f2.writes[0].Method != http.MethodPatch || f2.reads != 0 {
		t.Fatalf("只改名只該有一個 PATCH:%+v reads=%d", f2.writes, f2.reads)
	}
}

// canEdit:false(Apple 精選、喜好歌曲)一個寫入請求都不送——不然 Apple 是 500。
func TestApplyOpsRefusesNotEditable(t *testing.T) {
	f, p := writeWorld(t, false, "c1")
	_, err := p.ApplyOps(context.Background(), "p.1", []string{"c1"}, []provider.PlaylistOp{add("c2", 1)})
	if err == nil || !strings.Contains(err.Error(), "自己建的清單") || !strings.Contains(err.Error(), "通勤") {
		t.Fatalf("要講明只有自建清單能寫:%v", err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("零寫入:%+v", f.writes)
	}
	// 第二道防線:清單本體說可以、寫入卻 500 Unable to update → 同一句話
	f2, p2 := writeWorld(t, true, "c1")
	f2.failAt, f2.failMsg = 1, "Unable to update tracks"
	_, err = p2.ApplyOps(context.Background(), "p.1", []string{"c1"}, []provider.PlaylistOp{add("c2", 1)})
	if err == nil || !strings.Contains(err.Error(), "自己建的清單") {
		t.Fatalf("500 Unable to update 要翻成可行動的句子:%v", err)
	}
}

// remove / move / 插到中間(+ rename):重讀 /tracks 對齊 current,PATCH 再一次 PUT——既有列用列 id + library-songs、新曲用 catalog id + songs;
// 沒有 POST、沒有 DELETE。重讀在 PATCH 之前(所有會放棄的檢查都在第一個寫入之前)。
func TestApplyOpsRemoveMoveInsertIsOnePut(t *testing.T) {
	f, p := writeWorld(t, true, "c1", "c2", "c3") // 列 i.1 i.2 i.3
	ops := []provider.PlaylistOp{
		{Kind: provider.OpRemove, Pos: 0, ProviderID: "c1"}, // [c2 c3]
		add("c9", 0),                             // [c9 c2 c3]
		{Kind: provider.OpMove, From: 2, Pos: 1}, // [c9 c3 c2]
		{Kind: provider.OpRename, Name: "新名"},
	}
	if _, err := p.ApplyOps(context.Background(), "p.1", []string{"c1", "c2", "c3"}, ops); err != nil {
		t.Fatal(err)
	}
	puts := f.writesOf(http.MethodPut)
	if len(f.writes) != 2 || f.writes[0].Method != http.MethodPatch || f.writes[1].Method != http.MethodPut || f.reads != 1 || f.name != "新名" {
		t.Fatalf("PATCH 再一次 PUT、零 POST、重讀一次:%+v reads=%d name=%s", f.writes, f.reads, f.name)
	}
	refs := refsOf(t, puts[0].Body)
	if got := fmt.Sprint(refs); got != fmt.Sprint([]trackRef{{"c9", "songs"}, {"i.3", "library-songs"}, {"i.2", "library-songs"}}) {
		t.Fatalf("PUT body 順序與型別:%s", got)
	}
	if strings.Join(f.catalogs(), ",") != "c9,c3,c2" {
		t.Fatalf("平台結果:%v", f.catalogs())
	}
}

// 重讀對不上 current(確認期間平台被動過)→ 零寫入——連同一輪的 rename 也不送(不然「這次不寫」不準;PR #80 review)。
func TestApplyOpsReReadMismatchWritesNothing(t *testing.T) {
	f, p := writeWorld(t, true, "c1", "c2", "c3")
	ops := []provider.PlaylistOp{{Kind: provider.OpRemove, Pos: 0, ProviderID: "c1"}, {Kind: provider.OpRename, Name: "新名"}}
	_, err := p.ApplyOps(context.Background(), "p.1", []string{"c1", "c2"}, ops)
	if err == nil || !strings.Contains(err.Error(), "已經變了") {
		t.Fatalf("要回「平台已變」:%v", err)
	}
	if len(f.writes) != 0 || strings.Join(f.catalogs(), ",") != "c1,c2,c3" || f.name != "通勤" {
		t.Fatalf("零寫入(連 PATCH 也不送):%+v %v name=%s", f.writes, f.catalogs(), f.name)
	}
}

// want 為空 = 清空整份:PUT {"data":[]}(不是 null、不是 DELETE);閘在 CLI 端。
func TestApplyOpsEmptyWantPutsEmptyArray(t *testing.T) {
	f, p := writeWorld(t, true, "c1")
	if _, err := p.ApplyOps(context.Background(), "p.1", []string{"c1"}, []provider.PlaylistOp{{Kind: provider.OpRemove, Pos: 0, ProviderID: "c1"}}); err != nil {
		t.Fatal(err)
	}
	puts := f.writesOf(http.MethodPut)
	if len(puts) != 1 || puts[0].Body != `{"data":[]}` || len(f.catalogs()) != 0 {
		t.Fatalf("清空要是 PUT 空陣列:%+v %v", f.writes, f.catalogs())
	}
}

// R-8 第 2 項:同一首的兩列共用列 id;PUT 依出現次數留幾列。插到中間的重複沿用既有列 id。
func TestApplyOpsDuplicateInsertSharesEntryID(t *testing.T) {
	f, p := writeWorld(t, true, "c1", "c2")
	if _, err := p.ApplyOps(context.Background(), "p.1", []string{"c1", "c2"}, []provider.PlaylistOp{add("c1", 0)}); err != nil {
		t.Fatal(err)
	}
	refs := refsOf(t, f.writesOf(http.MethodPut)[0].Body)
	if strings.Join(ids(refs), ",") != "i.1,i.1,i.2" {
		t.Fatalf("重複那份沿用既有列 id:%v", refs)
	}
	if strings.Join(f.catalogs(), ",") != "c1,c1,c2" {
		t.Fatalf("平台結果:%v", f.catalogs())
	}
}

// library-only 曲目(無 catalog 對應)的 ProviderID 就是列 id:重讀對齊要用同一套規則,PUT 時直接當列 id。
func TestApplyOpsLibraryOnlyEntryAlignsAndKeepsID(t *testing.T) {
	f, p := writeWorld(t, true, "c2")
	f.entries = append([]fakeEntry{{ID: "i.only"}}, f.entries...) // [i.only(無 catalog), i.1(c2)]
	if _, err := p.ApplyOps(context.Background(), "p.1", []string{"i.only", "c2"}, []provider.PlaylistOp{{Kind: provider.OpMove, From: 1, Pos: 0}}); err != nil {
		t.Fatal(err)
	}
	refs := refsOf(t, f.writesOf(http.MethodPut)[0].Body)
	if got := fmt.Sprint(refs); got != fmt.Sprint([]trackRef{{"i.1", "library-songs"}, {"i.only", "library-songs"}}) {
		t.Fatalf("PUT body:%s", got)
	}
}

// 分批 POST 第二批起失敗 → PartialWriteError(Written = current + 已 append 的);第一批就失敗 → 一般錯誤(平台沒動)。
func TestApplyOpsPartialAppendReportsWritten(t *testing.T) {
	f, p := writeWorld(t, true, "c1")
	f.failAt = 2
	var ops []provider.PlaylistOp
	for i := 0; i < 250; i++ {
		ops = append(ops, add(fmt.Sprintf("n%d", i), 1+i))
	}
	_, err := p.ApplyOps(context.Background(), "p.1", []string{"c1"}, ops)
	var pw *provider.PartialWriteError
	if !errors.As(err, &pw) || pw.Written != 101 || pw.Want != 251 || pw.PlaylistID != "p.1" || pw.Renamed {
		t.Fatalf("要是 PartialWriteError{Written:101, Want:251}:%v", err)
	}
	if len(f.catalogs()) != 101 {
		t.Fatalf("平台停在 101 首:%d", len(f.catalogs()))
	}
	f2, p2 := writeWorld(t, true, "c1")
	f2.failAt = 1
	_, err = p2.ApplyOps(context.Background(), "p.1", []string{"c1"}, ops)
	if err == nil || errors.As(err, &pw) {
		t.Fatalf("第一批就失敗是一般錯誤:%v", err)
	}
}

// 建清單:POST isPublic:false,然後輪詢列表直到出現才回傳(pull 的 gone 判準看列表);超過上限回錯並帶 id 與接回的命令。
func TestCreatePlaylistWaitsUntilListed(t *testing.T) {
	orig, origDelays, origErr := provider.Wait, createPollDelays, provider.BackoffStderr
	var waited []time.Duration
	var said strings.Builder
	provider.Wait = func(_ context.Context, d time.Duration) error { waited = append(waited, d); return nil }
	provider.BackoffStderr = &said
	t.Cleanup(func() { provider.Wait, createPollDelays, provider.BackoffStderr = orig, origDelays, origErr })

	f, p := writeWorld(t, true)
	f.listLag = 2
	ref, err := p.CreatePlaylist(context.Background(), "公路旅行")
	if err != nil || ref.ID != "p.new" || ref.Name != "公路旅行" || ref.Total != -1 {
		t.Fatalf("(%+v, %v)", ref, err)
	}
	if f.lists != 3 || fmt.Sprint(waited) != fmt.Sprint(createPollDelays[:2]) { // 前兩次列表沒有、第三次有;間隔是退避 1 s、2 s(不是 1 Hz)
		t.Fatalf("lists=%d waited=%v", f.lists, waited)
	}
	if strings.Count(said.String(), "等待 Apple") != 1 { // 等待有一句話、只說一次
		t.Fatalf("等待要說一次:%q", said.String())
	}
	if body := f.writesOf(http.MethodPost)[0].Body; strings.Contains(body, "tracks") || strings.Contains(body, "description") {
		t.Fatalf("建清單不帶 tracks / description:%s", body)
	}

	createPollDelays = createPollDelays[:1] // 只剩一次退避 → 第二次列表還沒有就逾時
	f2, p2 := writeWorld(t, true)
	f2.listLag = 99
	_, err = p2.CreatePlaylist(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "p.new") || !strings.Contains(err.Error(), "capy pl link") || f2.lists != 2 {
		t.Fatalf("超過上限要回錯、帶 id 與接回命令、只查 2 次:%v lists=%d", err, f2.lists)
	}
}

// POST 之後的每條失敗都要帶著新清單的 id 與接回命令(PR #80 review):列表查不到、被中斷——不然使用者的曲庫留下一個連不回來的孤兒清單。
func TestCreatePlaylistFailuresKeepTheID(t *testing.T) {
	orig := provider.Wait
	t.Cleanup(func() { provider.Wait = orig })

	provider.Wait = func(context.Context, time.Duration) error { return nil }
	f, p := writeWorld(t, true)
	f.listFail = true
	_, err := p.CreatePlaylist(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "p.new") || !strings.Contains(err.Error(), "capy pl link") || !strings.Contains(err.Error(), "500") {
		t.Fatalf("列表查不到也要帶 id、接回命令與原因:%v", err)
	}

	provider.Wait = func(context.Context, time.Duration) error { return context.Canceled }
	f2, p2 := writeWorld(t, true)
	f2.listLag = 99
	_, err = p2.CreatePlaylist(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "p.new") || !strings.Contains(err.Error(), "capy pl link") || !errors.Is(err, context.Canceled) {
		t.Fatalf("被中斷也要帶 id 與接回命令,且仍是 context.Canceled(結束碼要算得對):%v", err)
	}
}

// amp-api 的 429 不帶 Retry-After、窗口滾動約一小時:直接失敗、講明原因;不重試(有 Retry-After 才照它等,見 TestDoRetriesOn429)。
func TestDo429WithoutRetryAfterFailsFast(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(429)
	})
	_, err := c.Storefront(context.Background())
	if err == nil || !strings.Contains(err.Error(), "一小時") || calls != 1 {
		t.Fatalf("要一次就失敗並講明約一小時:calls=%d err=%v", calls, err)
	}
}
