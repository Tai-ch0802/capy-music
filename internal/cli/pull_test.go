package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/drive/drivetest"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

// fakeSpotify:可變的假平台。lists 是 /me/playlists 的順序,items 是每個清單的曲目 id(平台順序)。
type fakeSpotify struct {
	mu           sync.Mutex
	lists        []fakeList
	items        map[string][]string
	restricted   map[string]bool    // items 回 403(編輯清單)
	missingItems map[string]bool    // 有列出但 items 回 404
	catalog      []fakeCatalogTrack // /search 與 /tracks/{id} 的目錄(resolve 用);同一個 id 可登記多筆(多個 ISRC 都回它)
	searchStatus int                // 非零:/search 一律回這個狀態碼(模擬 429 / 5xx)
	hook         func()             // 非 nil:每個請求進來先呼叫(pull 的 OBSERVE 期間 = FETCH 之後、COMMIT 之前;模擬別台裝置寫 Drive)
}

func (f *fakeSpotify) setHook(h func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hook = h
}

func (f *fakeSpotify) setSearchStatus(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searchStatus = code
}

// fakeCatalogTrack:目錄裡的一首;/search?q=isrc:X 回 ISRC 相同者,一般查詢回名稱含全部查詢字的。
type fakeCatalogTrack struct {
	ID, Name, ISRC, Artist string
	Dur                    int
}

func (c fakeCatalogTrack) json() string {
	return fmt.Sprintf(`{"id":%q,"name":%q,"duration_ms":%d,"explicit":false,"album":{"name":"A"},"artists":[{"name":%q}],"external_ids":{"isrc":%q}}`, c.ID, c.Name, c.Dur, c.Artist, c.ISRC)
}

func (f *fakeSpotify) addCatalog(c fakeCatalogTrack) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.Artist == "" {
		c.Artist = "artist"
	}
	if c.Dur == 0 {
		c.Dur = 200000
	}
	f.catalog = append(f.catalog, c)
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

type fakeList struct{ ID, Name string }

func newFakeSpotify() *fakeSpotify {
	return &fakeSpotify{items: map[string][]string{}, restricted: map[string]bool{}, missingItems: map[string]bool{}}
}

func (f *fakeSpotify) set(id, name string, tracks ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := f.index(id); i >= 0 {
		f.lists[i].Name = name
	} else {
		f.lists = append(f.lists, fakeList{id, name})
	}
	f.items[id] = tracks
}

func (f *fakeSpotify) drop(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := f.index(id); i >= 0 {
		f.lists = append(f.lists[:i], f.lists[i+1:]...)
	}
}

func (f *fakeSpotify) index(id string) int {
	for i, l := range f.lists {
		if l.ID == id {
			return i
		}
	}
	return -1
}

func fakeTrackJSON(id string) string {
	isrc := "TW" + strings.Repeat("0", 10-len(id)) + strings.ToUpper(id)
	return fmt.Sprintf(`{"id":%q,"name":"song-%s","duration_ms":200000,"explicit":false,"album":{"name":"A"},"artists":[{"name":"artist"}],"external_ids":{"isrc":%q}}`, id, id, isrc)
}

func fakeCID(id string) string { return "i:TW" + strings.Repeat("0", 10-len(id)) + strings.ToUpper(id) }

func (f *fakeSpotify) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.hook != nil {
			f.hook()
		}
		switch {
		case r.URL.Path == "/me/playlists":
			var items []string
			for _, l := range f.lists {
				items = append(items, fmt.Sprintf(`{"id":%q,"name":%q,"owner":{"display_name":"tai"},"items":{"total":%d}}`, l.ID, l.Name, len(f.items[l.ID])))
			}
			fmt.Fprintf(w, `{"items":[%s],"total":%d}`, strings.Join(items, ","), len(items))
		case strings.HasPrefix(r.URL.Path, "/playlists/") && strings.HasSuffix(r.URL.Path, "/items"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/playlists/"), "/items")
			if f.restricted[id] {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"error":{"status":403,"message":"Forbidden"}}`))
				return
			}
			if f.missingItems[id] {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":{"status":404,"message":"Not found."}}`))
				return
			}
			var its []string
			for _, tid := range f.items[id] {
				its = append(its, `{"item":`+fakeTrackJSON(tid)+`}`)
			}
			fmt.Fprintf(w, `{"items":[%s],"total":%d}`, strings.Join(its, ","), len(its))
		case r.URL.Path == "/search":
			if f.searchStatus != 0 {
				w.WriteHeader(f.searchStatus)
				fmt.Fprintf(w, `{"error":{"status":%d,"message":"nope"}}`, f.searchStatus)
				return
			}
			q := r.URL.Query().Get("q")
			var its []string
			for _, c := range f.catalog {
				if isrc, ok := strings.CutPrefix(q, "isrc:"); ok {
					if c.ISRC == isrc {
						its = append(its, c.json())
					}
					continue
				}
				have := words(c.Name + " " + c.Artist) // FuzzyQuery 是 標題 + 主要藝人
				all := true
				for _, w := range words(q) {
					if !slices.Contains(have, w) {
						all = false
					}
				}
				if all {
					its = append(its, c.json())
				}
			}
			fmt.Fprintf(w, `{"tracks":{"items":[%s],"total":%d}}`, strings.Join(its, ","), len(its))
		case strings.HasPrefix(r.URL.Path, "/tracks/"):
			id := strings.TrimPrefix(r.URL.Path, "/tracks/")
			for _, c := range f.catalog {
				if c.ID == id {
					w.Write([]byte(c.json()))
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"status":404,"message":"Not found."}}`))
		default:
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
	}
}

// pullWorld:隔離的 config 目錄 + 已登入的 device_id + 假 Drive + 假 Spotify。
func pullWorld(t *testing.T) (*fakeSpotify, *drive.Client, *drivetest.Server) {
	t.Helper()
	setCLITestConfig(t)
	keyring.MockInit()
	if err := config.Save(&config.Config{DeviceID: "01TESTDEVICE00000000000000", GoogleEmail: "tai@example.com"}); err != nil {
		t.Fatal(err)
	}
	dc, srv := stubDriveClient(t)
	fs := newFakeSpotify()
	swapProvider(t, fs.handler(t))
	return fs, dc, srv
}

// runPull:stdout 與 stderr 分開(TSV 契約只看 stdout)。
func runPull(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newRootCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

func mustPull(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	out, errs, err := runPull(t, args...)
	if err != nil {
		t.Fatalf("%v:\n%s%s", err, out, errs)
	}
	return out, errs
}

// driveFiles:假 Drive 上全部檔案的位元組(檔名 → 內容)。
func driveFiles(t *testing.T, dc *drive.Client) map[string][]byte {
	t.Helper()
	ctx := context.Background()
	fs, err := dc.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range fs {
		b, err := dc.Download(ctx, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
	}
	return out
}

func sameFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !bytes.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

// dumpBytes:本機 db 的 Dump 編成位元組(重建等價的比對用)。
func dumpBytes(t *testing.T) []byte {
	t.Helper()
	st, err := store.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := st.Dump()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, v := range []any{c.Manifest, c.Tracks} {
		b, err := canon.Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
	}
	for i := range c.Playlists {
		b, _ := canon.Encode(&c.Playlists[i])
		buf.Write(b)
	}
	for i := range c.Devices {
		b, _ := canon.Encode(&c.Devices[i])
		buf.Write(b)
	}
	return buf.Bytes()
}

func deleteDB(t *testing.T) {
	t.Helper()
	st, err := store.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Remove(); err != nil {
		t.Fatal(err)
	}
	if p, _ := store.Path(); fileExists(p) {
		t.Fatal("db 應已刪除")
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func exitOf(t *testing.T, err error) int {
	t.Helper()
	code, _ := ExitCode(err)
	return code
}

func TestExitCodeTable(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{nil, 0}, {errors.New("x"), 1}, {&AmbiguousError{}, 2}, {&PendingError{N: 2}, 2}, {&BlockedError{Msg: "b"}, 3},
		{fmt.Errorf("wrap:%w", &BlockedError{Msg: "b"}), 3},
	} {
		if code, _ := ExitCode(tc.err); code != tc.want {
			t.Errorf("%v → %d(要 %d)", tc.err, code, tc.want)
		}
	}
	if _, msg := ExitCode(errors.New("boom")); msg != "Error: boom" {
		t.Errorf("一般錯誤要帶 Error: 前綴:%q", msg)
	}
	if _, msg := ExitCode(&PendingError{N: 1}); !strings.Contains(msg, "--yes") {
		t.Errorf("待套用要提示 --yes:%q", msg)
	}
}

// Q3 採 B:>10 首,或 >30% 且 >3 首;分母是該 provider 可見曲數。
func TestRemovalBlockedThreshold(t *testing.T) {
	for _, tc := range []struct {
		removes, visible int
		want             bool
	}{
		{11, 100, true}, {10, 100, false}, {2, 5, false}, {3, 3, false}, {4, 10, true}, {4, 4, true}, {4, 20, false}, {0, 0, false},
	} {
		if got := removalBlocked(tc.removes, tc.visible); got != tc.want {
			t.Errorf("removes=%d visible=%d → %v(要 %v)", tc.removes, tc.visible, got, tc.want)
		}
	}
}

func TestPlLinkPullFlow(t *testing.T) {
	fs, dc, srv := pullWorld(t)
	fs.set("p1", "通勤", "t1", "t2")
	out, _ := mustPull(t, "pl", "link", "通勤", "spotify:p1")
	if !strings.Contains(out, "已連結 通勤(") || !strings.Contains(out, "spotify:p1") {
		t.Fatalf("link 輸出:%q", out)
	}
	files := driveFiles(t, dc)
	if _, ok := files["manifest.json"]; !ok || len(files) != 4 { // manifest + tracks + pl__ + 本裝置的 dev 檔(base 還是空的)
		t.Fatalf("link 要建 manifest / tracks / pl__ / dev__:%v", keysOf(files))
	}
	if !strings.Contains(string(files["manifest.json"]), `"playlists":["`) {
		t.Fatalf("manifest 要宣告清單:%s", files["manifest.json"])
	}
	// 非 TTY 沒 --yes:印 TSV、exit 2、Drive 不動。
	out, _, err := runPull(t, "pl", "pull", "通勤")
	if exitOf(t, err) != 2 {
		t.Fatalf("待套用要 exit 2:%v", err)
	}
	want := "add\tspotify\t通勤\t0\t" + fakeCID("t1") + "\tt1\tsong-t1\tartist\t平台新增\nadd\tspotify\t通勤\t1\t" + fakeCID("t2") + "\tt2\tsong-t2\tartist\t平台新增\n"
	if out != want {
		t.Fatalf("TSV 欄位順序 action provider playlist pos cid provider_id title artists reason:\n%q\n%q", out, want)
	}
	if !sameFiles(files, driveFiles(t, dc)) {
		t.Fatal("沒確認不可寫 Drive")
	}
	// --dry-run 同樣 exit 2、零寫入。
	if _, _, err := runPull(t, "pl", "pull", "通勤", "--dry-run"); exitOf(t, err) != 2 || !sameFiles(files, driveFiles(t, dc)) {
		t.Fatalf("dry-run:%v", err)
	}
	// --yes 套用:exit 0、清單有兩首、dev 檔有 base、manifest 註冊了裝置。
	out, errs := mustPull(t, "pl", "pull", "通勤", "--yes")
	if out != want || !strings.Contains(errs, "已套用 2 筆變更") {
		t.Fatalf("套用:%q %q", out, errs)
	}
	files = driveFiles(t, dc)
	pl := decodeFile[canon.Playlist](t, files, "pl__")
	if len(pl.Items) != 2 || pl.Items[0].CID != fakeCID("t1") || pl.Items[1].CID != fakeCID("t2") || pl.Links["spotify"] != "p1" {
		t.Fatalf("清單:%+v", pl)
	}
	dev := decodeFile[canon.DeviceState](t, files, "dev__")
	if b := dev.Base[pl.PID]["spotify"]; b.Snapshot.ID != "p1" || strings.Join(b.Snapshot.Items, ",") != "t1,t2" || len(b.Snapshot.CIDs) != 2 {
		t.Fatalf("base 要記平台清單 id、items、cids:%+v", b)
	}
	if m := decodeFile[canon.Manifest](t, files, "manifest.json"); len(m.Devices) != 1 || m.Devices[0].ID != "01TESTDEVICE00000000000000" {
		t.Fatalf("manifest 裝置註冊:%+v", m)
	}
	// 再 pull:無變更、exit 0、Drive 一個位元組都不動。
	out, errs = mustPull(t, "pl", "pull", "通勤", "--yes")
	if out != "" || !strings.Contains(errs, "無變更") || !sameFiles(files, driveFiles(t, dc)) {
		t.Fatalf("無變更要零寫入:%q %q", out, errs)
	}
	_ = srv
}

func keysOf(m map[string][]byte) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func decodeFile[T any](t *testing.T, files map[string][]byte, prefix string) *T {
	t.Helper()
	for name, b := range files {
		if strings.HasPrefix(name, prefix) {
			v, err := canon.Decode[T](b)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Fatalf("Drive 沒有 %s*:%v", prefix, keysOf(files))
	return nil
}

func TestPlLinkRefusesRestrictedAndDuplicate(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("ed", "Today's Top Hits")
	fs.restricted["ed"] = true
	if _, _, err := runPull(t, "pl", "link", "熱門", "spotify:ed"); err == nil || !strings.Contains(err.Error(), "不可連結") {
		t.Fatalf("編輯清單不可連結:%v", err)
	}
	fs.set("p1", "通勤", "t1")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	if _, _, err := runPull(t, "pl", "link", "另一個", "spotify:p1"); err == nil || !strings.Contains(err.Error(), "只能連一個") {
		t.Fatalf("同一平台清單不可連兩個 canonical:%v", err)
	}
	mustPull(t, "pl", "link", "通勤", "spotify:p1") // 同目標重複 link 是 no-op
	fs.set("p2", "通勤2", "t2")
	if _, _, err := runPull(t, "pl", "link", "通勤", "spotify:p2"); err == nil || !strings.Contains(err.Error(), "unlink") {
		t.Fatalf("已連結別的清單要先 unlink:%v", err)
	}
	if _, _, err := runPull(t, "pl", "link", "通勤", "tidal:p2"); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("未知 provider:%v", err)
	}
	if _, _, err := runPull(t, "pl", "pull", "沒有這個"); err == nil || !strings.Contains(err.Error(), "pl link") {
		t.Fatalf("未連結的清單要指路:%v", err)
	}
	// Apple 空清單回 404:link 要放行(新建空清單 → link → 再放歌是正常起手式)。
	fs.set("e", "空的")
	fs.missingItems["e"] = true
	mustPull(t, "pl", "link", "空的", "spotify:e")
	// 讀得到但不在自己列表裡的公開清單(像 base62 的 ID 會被 resolvePlaylistID 直接放行):不可連結,否則第一次 pull 就被當 gone。
	pub := "37i9dQZF1DXcBWIGoYBM5M"
	fs.mu.Lock()
	fs.items[pub] = []string{"z"}
	fs.mu.Unlock()
	if _, _, err := runPull(t, "pl", "link", "公開", "spotify:"+pub); err == nil || !strings.Contains(err.Error(), "不在你的清單列表") {
		t.Fatalf("不在列表裡的清單不可連結:%v", err)
	}
	fs.set(pub, "公開", "z")
	mustPull(t, "pl", "link", "公開", "spotify:"+pub)
	// 26 字元全大寫的清單名不是 pid:要建清單,不是「找不到 pid」。
	fs.set("p9", "mix", "m")
	if _, errs := mustPull(t, "pl", "link", "BEST OF THE YEAR MIX 2026!", "spotify:p9"); !strings.Contains(errs, "建立 canonical 清單 BEST OF THE YEAR MIX 2026!") {
		t.Fatalf("全大寫名字要當名字:%q", errs)
	}
	fs.set("p10", "another", "n")
	if _, _, err := runPull(t, "pl", "link", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "spotify:p10"); err == nil || !strings.Contains(err.Error(), "找不到 pid") {
		t.Fatalf("合法 ULID 卻不存在要報找不到 pid:%v", err)
	}
}

func TestPlPullThresholdNeedsForce(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	ids := make([]string, 12)
	for i := range ids {
		ids[i] = fmt.Sprintf("t%02d", i)
	}
	fs.set("p1", "通勤", ids...)
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	before := driveFiles(t, dc)
	fs.set("p1", "通勤", ids[0]) // 平台刪了 11 首
	out, _, err := runPull(t, "pl", "pull", "通勤", "--yes")
	if exitOf(t, err) != 3 || !strings.Contains(err.Error(), "超過閾值") || strings.Count(out, "remove\t") != 11 {
		t.Fatalf("超過閾值要 exit 3 且仍印出變更集:%v\n%s", err, out)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("被擋下就零寫入")
	}
	if _, _, err := runPull(t, "pl", "pull", "通勤", "--yes", "--force", "--dry-run"); exitOf(t, err) != 2 {
		t.Fatalf("--force 越過閾值後 dry-run 是待套用 exit 2:%v", err)
	}
	if _, _, err := runPull(t, "pl", "pull", "--all", "--yes", "--force"); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("--force 不能配 --all(安全閥一次只解除一個清單):%v", err)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("被拒絕的 --force --all 零寫入")
	}
	mustPull(t, "pl", "pull", "通勤", "--yes", "--force")
	if pl := decodeFile[canon.Playlist](t, driveFiles(t, dc), "pl__"); len(pl.Items) != 1 {
		t.Fatalf("--force 後套用:%d 首", len(pl.Items))
	}
	// 5 首刪 2 首(40%,但 ≤3 首)不擋:Q3 B。
	fs.set("p2", "小清單", "a1", "a2", "a3", "a4", "a5")
	mustPull(t, "pl", "link", "小清單", "spotify:p2")
	mustPull(t, "pl", "pull", "小清單", "--yes")
	fs.set("p2", "小清單", "a1", "a2", "a3")
	if out, _ := mustPull(t, "pl", "pull", "小清單", "--yes"); strings.Count(out, "remove\t") != 2 {
		t.Fatalf("5 首刪 2 首不該被擋:%s", out)
	}
}

func TestPlPullDriveIncompleteBlocksEvenWithYesForce(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	ctx := context.Background()
	fl, _ := dc.List(ctx, "")
	for _, f := range fl {
		if strings.HasPrefix(f.Name, "pl__") {
			if err := dc.Delete(ctx, f.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := driveFiles(t, dc)
	_, _, err := runPull(t, "pl", "pull", "通勤", "--yes", "--force") // 閘在目標解析之前,連清單找不找得到都輪不到
	if exitOf(t, err) != 3 || !strings.Contains(err.Error(), "pl__") || !strings.Contains(err.Error(), "drive init --from-local") {
		t.Fatalf("宣告了卻取不到的檔 → exit 3 並指路:%v", err)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("零寫入")
	}
	// link 也走同一個閘:不能靠建清單繞過。
	if _, _, err := runPull(t, "pl", "link", "新的", "spotify:p1"); exitOf(t, err) != 3 {
		t.Fatalf("link 也要被閘擋下:%v", err)
	}
	// Drive 被清空、本機 db 還記得清單:也是不完整(這才是「兩下清空 appdata」的情境)。
	for _, f := range before {
		_ = f
	}
	fl, _ = dc.List(ctx, "")
	for _, f := range fl {
		if err := dc.Delete(ctx, f.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := runPull(t, "pl", "pull", "--all", "--yes"); exitOf(t, err) != 3 || !strings.Contains(err.Error(), "manifest.json") {
		t.Fatalf("Drive 全空 + 本機非空 → exit 3:%v", err)
	}
	if len(driveFiles(t, dc)) != 0 {
		t.Fatal("零寫入")
	}
}

func TestPlPullSchemaTooNewIsErrorZeroWrites(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	if _, err := dc.Create(context.Background(), "manifest.json", canon.ManifestFile().Props, []byte(`{"schema_version":99,"devices":[],"playlists":[]}`+"\n")); err != nil {
		t.Fatal(err)
	}
	before := driveFiles(t, dc)
	_, _, err := runPull(t, "pl", "link", "通勤", "spotify:p1")
	if exitOf(t, err) != 1 || !errors.Is(err, canon.ErrSchemaTooNew) || !strings.Contains(err.Error(), "manifest.json") {
		t.Fatalf("schema 太新 → exit 1 並點名檔案:%v", err)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("零寫入")
	}
}

func TestPlPullGoneUnlinksAndListedBut404IsEmpty(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	// 有列出但 items 404 = 空清單(Apple 的 library 端點對空清單回 404):移除走 GATE,不是 exit 1、更不是 gone。
	fs.missingItems["p1"] = true
	out, _, err := runPull(t, "pl", "pull", "通勤")
	if exitOf(t, err) != 2 || strings.Count(out, "remove\t") != 1 || strings.Contains(out, "unlink") {
		t.Fatalf("列表裡還在但 404 → 當空清單走 GATE:%v\n%s", err, out)
	}
	delete(fs.missingItems, "p1")
	// 不在列表裡:gone → unlink 列為變更,--yes 才套用;canonical 內容不動。
	fs.drop("p1")
	out, errs, err := runPull(t, "pl", "pull", "通勤")
	if exitOf(t, err) != 2 || !strings.HasPrefix(out, "unlink\tspotify\t通勤\t\t") || !strings.Contains(errs, "取消連結") {
		t.Fatalf("gone:%v %q %q", err, out, errs)
	}
	mustPull(t, "pl", "pull", "通勤", "--yes")
	pl := decodeFile[canon.Playlist](t, driveFiles(t, dc), "pl__")
	if len(pl.Links) != 0 || len(pl.Items) != 1 {
		t.Fatalf("unlink 後 links 空、items 不動:%+v", pl)
	}
	if _, _, err := runPull(t, "pl", "pull", "通勤"); err == nil || !strings.Contains(err.Error(), "沒有連結") {
		t.Fatalf("沒連結的清單 pull 要講明:%v", err)
	}
}

func TestPlPullRestrictedIsSkippedNotGone(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	before := driveFiles(t, dc)
	fs.restricted["p1"] = true
	out, errs := mustPull(t, "pl", "pull", "--all", "--yes")
	if out != "" || !strings.Contains(errs, "跳過") || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("讀不到要跳過、不 unlink、零寫入:%q %q", out, errs)
	}
}

// unlink 後改連別的平台清單:舊 base 的 id 不同就不算數 → 只會新增、不會把舊曲目當成「平台刪了」。
func TestPlRelinkIgnoresStaleBase(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a", "b")
	fs.set("p2", "通勤2", "c")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	mustPull(t, "pl", "unlink", "通勤", "spotify")
	mustPull(t, "pl", "link", "通勤", "spotify:p2")
	out, _ := mustPull(t, "pl", "pull", "通勤", "--yes")
	if strings.Contains(out, "remove\t") || strings.Count(out, "add\t") != 1 {
		t.Fatalf("換連結後只新增、不移除:%s", out)
	}
	if pl := decodeFile[canon.Playlist](t, driveFiles(t, dc), "pl__"); len(pl.Items) != 3 {
		t.Fatalf("a、b 留著、c 加進來:%d 首", len(pl.Items))
	}
	// 新 base 記的是 p2,之後 p2 刪一首就刪得掉。
	fs.set("p2", "通勤2")
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); strings.Count(out, "remove\t") != 1 {
		t.Fatalf("新連結的 base 生效:%s", out)
	}
}

// e2e 等價(計畫 T8;從 T6 移來):兩輪 pull → 擷取 Drive 檔位元組與 db Dump → 刪 db → 再 pull:零變更、Drive 逐位元相同、
// Dump 相同。證明 db 裡沒有任何 Drive 沒有的資訊,順便鎖住非 TTY 輸出契約。
func TestPlPullRebuildFromDriveIsEquivalent(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a", "b", "c")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	fs.set("p1", "通勤", "b", "c", "a") // 換序
	round2, _ := mustPull(t, "pl", "pull", "通勤", "--yes")
	if !strings.HasPrefix(round2, "move\tspotify\t通勤\t2\t"+fakeCID("a")) {
		t.Fatalf("第二輪:%s", round2)
	}
	files2, dump2 := driveFiles(t, dc), dumpBytes(t)
	deleteDB(t)
	out, _ := mustPull(t, "pl", "pull", "通勤", "--yes")
	if out != "" || !sameFiles(files2, driveFiles(t, dc)) || !bytes.Equal(dump2, dumpBytes(t)) {
		t.Fatalf("刪 db 後重建要與刪前等價:%q", out)
	}
	fs.set("p1", "通勤", "b", "c", "a", "d") // 之後平台再變,重建過的 db 照常運作
	round4, _ := mustPull(t, "pl", "pull", "通勤", "--yes")
	if !strings.HasPrefix(round4, "add\tspotify\t通勤\t3\t"+fakeCID("d")) {
		t.Fatalf("第四輪:%s", round4)
	}
	files4, dump4 := driveFiles(t, dc), dumpBytes(t)
	deleteDB(t)
	mustPull(t, "pl", "pull", "--all", "--yes")
	if !sameFiles(files4, driveFiles(t, dc)) || !bytes.Equal(dump4, dumpBytes(t)) {
		t.Fatal("再刪一次 db 仍等價")
	}
}

// COMMIT 順序寫死 Drive 先、SQLite 後:上傳失敗就不寫 cache(否則 cache 領先 source of truth,下次 rebuild 反而倒退),下次 pull 重算。
func TestPlPullUploadFailureLeavesCacheUntouched(t *testing.T) {
	fs, dc, srv := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	before, dump := driveFiles(t, dc), dumpBytes(t)
	fs.set("p1", "通勤", "a", "b")
	srv.FailOn(func(r *http.Request) bool { return r.Method == http.MethodPatch }, http.StatusInternalServerError, "backendError")
	_, _, err := runPull(t, "pl", "pull", "通勤", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "上傳") {
		t.Fatalf("上傳失敗要 exit 1 並講明:%v", err)
	}
	if !sameFiles(before, driveFiles(t, dc)) || !bytes.Equal(dump, dumpBytes(t)) {
		t.Fatal("上傳失敗:Drive 與本機快取都不得動")
	}
	srv.FailOn(nil, 0, "")
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); strings.Count(out, "add\t") != 1 {
		t.Fatalf("下次 pull 重算成功:%s", out)
	}
}

// --dry-run 永不寫入:連空 Drive 的 bootstrap 檔(manifest / tracks / dev)都不建;不帶 --dry-run 的無變更 pull 才會建。
func TestPlPullDryRunNeverWrites(t *testing.T) {
	_, dc, srv := pullWorld(t)
	out, errs := mustPull(t, "pl", "pull", "--all", "--dry-run")
	if out != "" || !strings.Contains(errs, "無變更") || srv.Len() != 0 {
		t.Fatalf("dry-run 零寫入:%q %q %d", out, errs, srv.Len())
	}
	mustPull(t, "pl", "pull", "--all")
	if files := driveFiles(t, dc); len(files) != 3 {
		t.Fatalf("無變更但要註冊裝置:manifest / tracks / dev:%v", keysOf(files))
	}
}

func TestPlPullNeedsGoogleLogin(t *testing.T) {
	setCLITestConfig(t)
	keyring.MockInit()
	if _, _, err := runPull(t, "pl", "pull", "--all"); !errors.Is(err, errNotLoggedInGoogle) {
		t.Fatalf("沒登入 Google(沒有 device_id)要指路:%v", err)
	}
}

// TestPlPullSecondRunUploadsNothing:平台無變化的第二次 pull 零上傳——mapping 的 updated_at 若每次觀測都刷新,
// tracks.json 位元組會變、每次 pull 都重傳整份(PR #24 review 第 5 則)。
func TestPlPullSecondRunUploadsNothing(t *testing.T) {
	fs, dc, srv := pullWorld(t)
	fs.set("p1", "通勤", "t1", "t2")
	origNow := canon.Now
	t.Cleanup(func() { canon.Now = origNow })
	canon.Now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	before := driveFiles(t, dc)
	canon.Now = func() time.Time { return time.Unix(1_700_000_060, 0) } // 時鐘往前走:updated_at 若每次觀測都刷新,這裡就會露餡
	writes := 0
	srv.FailOn(func(r *http.Request) bool {
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			writes++
		}
		return false // 只計數,不失敗
	}, 0, "")
	out, errs := mustPull(t, "pl", "pull", "通勤", "--yes")
	if writes != 0 || out != "" || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("第二次 pull 要零上傳、零變更:writes=%d out=%q errs=%q", writes, out, errs)
	}
}

// 決策 21 的自癒:tracks.json 已合併、清單還指著敗者(COMMIT 傳完 tracks.json 就斷掉的殘留)→ 下一次 pull 在 FETCH 修回、
// 隨 COMMIT 上傳;之後零上傳、刪 db 重建等價;平台刪掉敗者那首時 base 的敗者 cid 經墓碑重導,一次 pull 就移除。
func TestPlPullHealsMergedTombstoneLeftovers(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a", "b")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	ctx := context.Background()
	tracks := decodeFile[canon.Tracks](t, driveFiles(t, dc), "tracks.json")
	s, err := canon.Merge(tracks, nil, fakeCID("a"), fakeCID("b")) // 人工合併,清單刻意不改
	if err != nil || s != fakeCID("a") {
		t.Fatalf("勝者字典序小:%s %v", s, err)
	}
	body, err := canon.Encode(tracks)
	if err != nil {
		t.Fatal(err)
	}
	files, err := dc.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name == "tracks.json" {
			if _, err := dc.Update(ctx, f.ID, canon.TracksFile().Props, body); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := driveFiles(t, dc)
	out, errs := mustPull(t, "pl", "pull", "通勤", "--dry-run") // 自癒在 FETCH 就發生,但 --dry-run 零寫入:訊息不能承諾「這次上傳」
	if out != "" || !strings.Contains(errs, "修復 1 筆") || !strings.Contains(errs, "(通勤;") || !strings.Contains(errs, "下次寫入時一併上傳") || strings.Contains(errs, "這次") {
		t.Fatalf("--dry-run:stderr 講事實、列清單名、不承諾這次:%q\n%s", out, errs)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("--dry-run 零寫入,自癒也不例外")
	}
	out, errs = mustPull(t, "pl", "pull", "通勤", "--yes")
	if out != "" || !strings.Contains(errs, "修復 1 筆") {
		t.Fatalf("平台照舊 → 零列;stderr 說修了 1 筆:%q\n%s", out, errs)
	}
	pl := decodeFile[canon.Playlist](t, driveFiles(t, dc), "pl__")
	if len(pl.Items) != 2 || pl.Items[0].CID != s || pl.Items[1].CID != s {
		t.Fatalf("Drive 上的清單不再指著敗者:%v", pl.Items)
	}
	_, errs = mustPull(t, "pl", "pull", "通勤", "--yes")
	if strings.Contains(errs, "修復") {
		t.Fatalf("修過就不再修:%s", errs)
	}
	files2, dump2 := driveFiles(t, dc), dumpBytes(t)
	deleteDB(t)
	mustPull(t, "pl", "pull", "通勤", "--yes")
	if !sameFiles(files2, driveFiles(t, dc)) || !bytes.Equal(dump2, dumpBytes(t)) {
		t.Fatal("刪 db 後重建要與刪前等價(merged 表也在鏡像裡)")
	}
	fs.set("p1", "通勤", "a") // 平台刪掉 b:它的 cid 是敗者,只剩 base 記得
	out, _ = mustPull(t, "pl", "pull", "通勤", "--yes")
	if !strings.HasPrefix(out, "remove\tspotify\t通勤\t1\t"+s+"\t") || strings.Count(out, "\n") != 1 {
		t.Fatalf("恰好一筆 remove、cid 是勝者:%q", out)
	}
	if pl := decodeFile[canon.Playlist](t, driveFiles(t, dc), "pl__"); len(pl.Items) != 1 {
		t.Fatalf("清單剩一個 item:%v", pl.Items)
	}
}
