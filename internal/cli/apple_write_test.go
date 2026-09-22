package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	appleprov "github.com/Tai-ch0802/capy-music/internal/provider/apple"
)

// fakeAmp:假 amp-api——library 寫入端點(計畫 2026-09-22-apple-write.md §1 表)加 catalog ISRC 反查(migrate 的 resolve 用)。
// 新清單要等 listLag 次列表之後才出現(iCloud 傳播延遲);DELETE 一律 t.Errorf。
type fakeAmp struct {
	t         *testing.T
	mu        sync.Mutex
	order     []string
	lists     map[string]*ampList
	catalog   map[string]string // ISRC → catalog id
	names     map[string]string // catalog id → 曲名(跟來源一致,不然 Observe 會記 ISRC 衝突)
	seq       int
	created   int
	listCalls int
	listLag   int
	writes    []ampReq
}

type ampList struct {
	Name         string
	CanEdit      bool
	VisibleAfter int // 第幾次列表之後才看得到
	Entries      []ampEntry
}

type ampEntry struct{ ID, Catalog string }

type ampReq struct{ Method, Path, Body string }

func newFakeAmp(t *testing.T) *fakeAmp {
	return &fakeAmp{t: t, lists: map[string]*ampList{}, catalog: map[string]string{}, names: map[string]string{}}
}

func (f *fakeAmp) nameOf(catalog string) string {
	if n, ok := f.names[catalog]; ok {
		return n
	}
	return "song"
}

func (f *fakeAmp) isrcOf(catalog string) string {
	for isrc, c := range f.catalog {
		if c == catalog {
			return isrc
		}
	}
	return ""
}

func (f *fakeAmp) catalogs(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.lists[id].Entries {
		out = append(out, e.Catalog)
	}
	return out
}

func (f *fakeAmp) writesOf(method string) []ampReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ampReq
	for _, w := range f.writes {
		if w.Method == method {
			out = append(out, w)
		}
	}
	return out
}

func (f *fakeAmp) entry(ref struct{ ID, Type string }, l *ampList) ampEntry {
	if ref.Type == "library-songs" {
		for _, e := range l.Entries {
			if e.ID == ref.ID {
				return e
			}
		}
		f.t.Errorf("指到不存在的列 id %s", ref.ID)
		return ampEntry{ID: ref.ID}
	}
	if ref.Type != "songs" || strings.Contains(ref.ID, ".") {
		f.t.Errorf("catalog id 要配 type songs:%+v", ref)
	}
	f.seq++
	return ampEntry{ID: fmt.Sprintf("i.%d", f.seq), Catalog: ref.ID}
}

func (f *fakeAmp) handler() http.HandlerFunc {
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
			f.writes = append(f.writes, ampReq{r.Method, r.URL.Path, string(body)})
		}
		var refs struct {
			Data []struct{ ID, Type string } `json:"data"`
		}
		_ = json.Unmarshal(body, &refs)
		p := r.URL.Path
		switch {
		case p == "/catalog/tw/songs":
			isrc := r.URL.Query().Get("filter[isrc]")
			if c, ok := f.catalog[isrc]; ok {
				fmt.Fprintf(w, `{"data":[{"id":%q,"type":"songs","attributes":{"name":%q,"artistName":"artist","albumName":"A","durationInMillis":200000,"isrc":%q}}]}`, c, f.nameOf(c), isrc)
				return
			}
			w.Write([]byte(`{"data":[]}`))
		case p == "/catalog/tw/search":
			w.Write([]byte(`{"results":{}}`))
		case p == "/me/library/playlists" && r.Method == http.MethodGet:
			f.listCalls++
			var items []string
			for _, id := range f.order {
				if l := f.lists[id]; f.listCalls > l.VisibleAfter {
					items = append(items, fmt.Sprintf(`{"id":%q,"attributes":{"name":%q,"canEdit":%v}}`, id, l.Name, l.CanEdit))
				}
			}
			fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(items, ","))
		case p == "/me/library/playlists" && r.Method == http.MethodPost:
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
			f.created++
			id := fmt.Sprintf("p.new%d", f.created)
			f.order = append(f.order, id)
			f.lists[id] = &ampList{Name: req.Attributes.Name, CanEdit: true, VisibleAfter: f.listCalls + f.listLag}
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"data":[{"id":%q,"attributes":{"name":%q,"canEdit":true}}]}`, id, req.Attributes.Name)
		case strings.HasPrefix(p, "/me/library/playlists/"):
			rest := strings.Split(strings.TrimPrefix(p, "/me/library/playlists/"), "/")
			l := f.lists[rest[0]]
			if l == nil {
				w.WriteHeader(404)
				w.Write([]byte(`{"errors":[{"status":"404"}]}`))
				return
			}
			switch {
			case len(rest) == 1 && r.Method == http.MethodGet:
				fmt.Fprintf(w, `{"data":[{"id":%q,"attributes":{"name":%q,"canEdit":%v}}]}`, rest[0], l.Name, l.CanEdit)
			case len(rest) == 1 && r.Method == http.MethodPatch:
				var req struct {
					Attributes map[string]string `json:"attributes"`
				}
				_ = json.Unmarshal(body, &req)
				l.Name = req.Attributes["name"]
				w.WriteHeader(204)
			case len(rest) == 2 && rest[1] == "tracks" && r.Method == http.MethodGet:
				if len(l.Entries) == 0 { // Apple 對空清單回 404
					w.WriteHeader(404)
					w.Write([]byte(`{"errors":[{"status":"404","title":"Resource Not Found"}]}`))
					return
				}
				var items []string
				for _, e := range l.Entries {
					items = append(items, fmt.Sprintf(`{"id":%q,"type":"library-songs","attributes":{"name":%q,"artistName":"artist","albumName":"A","durationInMillis":200000},
"relationships":{"catalog":{"data":[{"id":%q,"type":"songs","attributes":{"name":%q,"artistName":"artist","albumName":"A","durationInMillis":200000,"isrc":%q}}]}}}`, e.ID, f.nameOf(e.Catalog), e.Catalog, f.nameOf(e.Catalog), f.isrcOf(e.Catalog)))
				}
				fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(items, ","))
			case len(rest) == 2 && rest[1] == "tracks" && r.Method == http.MethodPost:
				for _, ref := range refs.Data {
					l.Entries = append(l.Entries, f.entry(ref, l))
				}
				w.WriteHeader(204)
			case len(rest) == 2 && rest[1] == "tracks" && r.Method == http.MethodPut:
				var next []ampEntry
				for _, ref := range refs.Data {
					next = append(next, f.entry(ref, l))
				}
				l.Entries = next
				w.WriteHeader(204)
			default:
				f.t.Errorf("非預期的 Apple 請求:%s %s", r.Method, p)
				w.WriteHeader(405)
			}
		default:
			f.t.Errorf("非預期的 Apple 請求:%s %s", r.Method, p)
			w.WriteHeader(404)
		}
	}
}

// swapAmp:apple → 真的 apple provider 打 fakeAmp;其他平台照舊。建清單後的輪詢不真的等。
func swapAmp(t *testing.T, f *fakeAmp) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id == "apple" {
			return appleprov.New(srv.Client(), srv.URL, "DEV", "MUT", "tw"), nil
		}
		return orig(ctx, id)
	}
	origWait := provider.Wait
	provider.Wait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { newProvider, provider.Wait = orig, origWait })
}

func ampIDs(t *testing.T, body string) (ids, types []string) {
	t.Helper()
	var req struct {
		Data []struct{ ID, Type string } `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("body:%s", body)
	}
	for _, d := range req.Data {
		ids, types = append(ids, d.ID), append(types, d.Type)
	}
	return ids, types
}

// Spotify → Apple 搬家(使用者 2026-09-22 的目標;決策 49):migrate 在 Apple 建一個同名清單(isPublic:false、等到列表出現)、
// 以 ISRC 對到 catalog id、一次 POST 依來源順序接上;之後 pull 零變更。接著正本換序 → push 到 Apple 是一次 PUT(列 id、順序 = 正本),
// 沒有 POST、沒有 DELETE;再 pull 零變更。
func TestMigrateSpotifyToAppleThenReorderIsOnePut(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "公路旅行", "a", "b")
	amp := newFakeAmp(t)
	amp.listLag = 1
	amp.catalog[fakeISRC("a")], amp.catalog[fakeISRC("b")] = "1001", "1002"
	amp.names["1001"], amp.names["1002"] = "song-a", "song-b"
	swapAmp(t, amp)

	out, errs := mustPull(t, "migrate", "公路旅行", "--from", "spotify", "--to", "apple", "--yes")
	if amp.created != 1 || amp.lists["p.new1"].Name != "公路旅行" || !strings.Contains(errs, "在 apple 建立清單 公路旅行") {
		t.Fatalf("要在 Apple 建一個同名清單:created=%d\n%s%s", amp.created, out, errs)
	}
	posts := amp.writesOf(http.MethodPost)
	if len(posts) != 2 || posts[1].Path != "/me/library/playlists/p.new1/tracks" || len(amp.writesOf(http.MethodPut)) != 0 || len(amp.writesOf(http.MethodPatch)) != 0 {
		t.Fatalf("建清單一個 POST、加歌一個 POST、零 PUT / PATCH:%+v", amp.writes)
	}
	if ids, types := ampIDs(t, posts[1].Body); strings.Join(ids, ",") != "1001,1002" || strings.Join(types, ",") != "songs,songs" {
		t.Fatalf("加歌要依來源順序、type songs:%v %v", ids, types)
	}
	if strings.Join(amp.catalogs("p.new1"), ",") != "1001,1002" || !strings.Contains(errs, "推了 2 首到 apple") || strings.Count(out, "migrate\tadd\t") != 2 || strings.Contains(errs, "ISRC 衝突") {
		t.Fatalf("平台結果與輸出:%v\n%s%s", amp.catalogs("p.new1"), out, errs)
	}
	if out, errs := mustPull(t, "pl", "pull", "公路旅行", "--yes"); out != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("搬完 pull 要零變更:%s%s", out, errs)
	}

	// 正本換序 → Apple 端一次 PUT(列 id + library-songs,順序 = 正本);沒有 POST、沒有 DELETE。
	pl := drivePlaylistNamed(t, dc, "公路旅行")
	editCanonical(t, dc, pl, []string{"b", "a"}, nil, "")
	before := len(amp.writes)
	out, _ = mustPull(t, "pl", "push", "公路旅行", "--provider", "apple", "--yes")
	if strings.Count(out, "move\t") != 1 {
		t.Fatalf("push 變更集要是一個 move:\n%s", out)
	}
	puts := amp.writesOf(http.MethodPut)
	if len(amp.writes)-before != 1 || len(puts) != 1 || puts[0].Path != "/me/library/playlists/p.new1/tracks" {
		t.Fatalf("換序要是唯一一個寫入請求、而且是 PUT:%+v", amp.writes[before:])
	}
	if ids, types := ampIDs(t, puts[0].Body); strings.Join(ids, ",") != "i.2,i.1" || strings.Join(types, ",") != "library-songs,library-songs" {
		t.Fatalf("PUT body 要是既有列 id、正本的順序:%v %v", ids, types)
	}
	if strings.Join(amp.catalogs("p.new1"), ",") != "1002,1001" {
		t.Fatalf("平台結果:%v", amp.catalogs("p.new1"))
	}
	if out, errs := mustPull(t, "pl", "pull", "公路旅行", "--yes"); out != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("push 後 pull 要零變更:%s%s", out, errs)
	}
}

// Apple 精選(canEdit:false)不能當 migrate 的目標:確認之後才會發現,但一個寫入請求都不送、exit 1、訊息講明只有自建清單能寫。
func TestMigrateToAppleCuratedPlaylistWritesNothing(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "公路旅行", "a")
	amp := newFakeAmp(t)
	amp.catalog[fakeISRC("a")] = "1001"
	amp.names["1001"] = "song-a"
	amp.order = append(amp.order, "p.apple")
	amp.lists["p.apple"] = &ampList{Name: "冬日暖調", CanEdit: false, Entries: []ampEntry{{ID: "i.9", Catalog: "9009"}}}
	amp.catalog["TW9009"] = "9009"
	swapAmp(t, amp)
	_, _, err := runPull(t, "migrate", "公路旅行", "--from", "spotify", "--to", "apple:冬日暖調", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "自己建的清單") {
		t.Fatalf("要 exit 1 並講明只有自建清單能寫:%v", err)
	}
	if len(amp.writes) != 0 || strings.Join(amp.catalogs("p.apple"), ",") != "9009" {
		t.Fatalf("零寫入:%+v %v", amp.writes, amp.catalogs("p.apple"))
	}
}
