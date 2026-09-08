package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/drive/drivetest"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// ---- 假 Spotify 的寫入端 ----

type fakeWrite struct {
	Method, Path string
	URIs         []string
	Position     int
	Name         string
}

func fakeLocalJSON(id string) string {
	return fmt.Sprintf(`{"id":null,"uri":"spotify:local:x:y:%s:200","is_local":true,"name":"local-%s","duration_ms":200000,"explicit":false,"album":{"name":"A"},"artists":[{"name":"artist"}],"external_ids":{}}`, id, id)
}

// write:PUT /playlists/{id}/items 整批取代、POST …/items 依 position 插入、PUT /playlists/{id} 改名。uri 是 spotify:track:<id>。
func (f *fakeSpotify) write(t *testing.T, w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		URIs     []string `json:"uris"`
		Position *int     `json:"position"`
		Name     string   `json:"name"`
	}
	raw, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Errorf("寫入 body 不是 JSON:%s", raw)
	}
	fw := fakeWrite{Method: r.Method, Path: r.URL.Path, URIs: body.URIs, Name: body.Name, Position: -1}
	if body.Position != nil {
		fw.Position = *body.Position
	}
	f.writes = append(f.writes, fw)
	if f.writeStatus != 0 {
		w.WriteHeader(f.writeStatus)
		fmt.Fprintf(w, `{"error":{"status":%d,"message":"nope"}}`, f.writeStatus)
		return
	}
	ids := make([]string, len(body.URIs))
	for i, u := range body.URIs {
		ids[i] = strings.TrimPrefix(u, "spotify:track:")
	}
	switch {
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/items"):
		f.items[id] = ids
	case r.Method == http.MethodPost:
		if f.postFail > 0 {
			f.postFail--
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"status":500,"message":"boom"}}`))
			return
		}
		pos := len(f.items[id])
		if fw.Position >= 0 {
			pos = fw.Position
		}
		f.items[id] = slices.Insert(slices.Clone(f.items[id]), pos, ids...)
	case r.Method == http.MethodPut:
		if i := f.index(id); i >= 0 {
			f.lists[i].Name = body.Name
		}
	default:
		t.Errorf("非預期寫入:%s %s", r.Method, r.URL.Path)
	}
	w.Write([]byte(`{"snapshot_id":"x"}`))
}

func (f *fakeSpotify) written() []fakeWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes)
}

func (f *fakeSpotify) tracksOf(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.items[id])
}

func (f *fakeSpotify) setWriteStatus(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeStatus = code
}

func (f *fakeSpotify) setLocal(ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		f.local[id] = true
	}
}

// ---- 場景 helper ----

// pushWorld:p1 = [a, b, c] 已連結、pull 過(有 base);回傳 canonical 清單。
func pushWorld(t *testing.T) (*fakeSpotify, *drive.Client, *drivetest.Server, *canon.Playlist) {
	t.Helper()
	fs, dc, srv := pullWorld(t)
	fs.set("p1", "通勤", "a", "b", "c")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	return fs, dc, srv, drivePlaylist(t, dc)
}

func drivePlaylist(t *testing.T, dc *drive.Client) *canon.Playlist {
	t.Helper()
	return decodeFile[canon.Playlist](t, driveFiles(t, dc), "pl__")
}

func drivePlaylistNamed(t *testing.T, dc *drive.Client, name string) *canon.Playlist {
	t.Helper()
	for fname, b := range driveFiles(t, dc) {
		if strings.HasPrefix(fname, "pl__") {
			if pl, err := canon.Decode[canon.Playlist](b); err == nil && pl.Name == name {
				return pl
			}
		}
	}
	t.Fatalf("Drive 沒有叫 %s 的清單", name)
	return nil
}

func cidsOf(pl *canon.Playlist) []string {
	out := make([]string, len(pl.Items))
	for i, it := range pl.Items {
		out[i] = it.CID
	}
	return out
}

// editCanonical:直接改 Drive 上的 pl__(模擬另一個平台 pull 進來的變更):items 依 ids 順序,新 id 的 track 補進 tracks.json 並帶 spotify mapping(mapped 為 false 則沒有 mapping)。
func editCanonical(t *testing.T, dc *drive.Client, pl *canon.Playlist, ids []string, mapped map[string]bool, name string) {
	t.Helper()
	tr := driveTracks(t, dc)
	byCID := map[string]canon.Item{}
	for _, it := range pl.Items {
		byCID[it.CID] = it
	}
	ranks := canon.Ranks(len(ids))
	var items []canon.Item
	for i, id := range ids {
		cid := fakeCID(id)
		it, ok := byCID[cid]
		if !ok {
			it = canon.Item{IID: canon.NewULID(), CID: cid, AddedAt: 1}
			if _, have := tr.Tracks[cid]; !have {
				track := canon.Track{CID: cid, Title: "song-" + id, Artists: []string{"artist"}, DurationMS: 200000, Mappings: map[string]canon.Mapping{}}
				if mapped == nil || mapped[id] {
					track.Mappings["spotify"] = canon.Mapping{ID: id, Confidence: 100, Source: canon.SourceObserved}
				}
				tr.Tracks[cid] = track
			}
		}
		it.Rank = ranks[i]
		items = append(items, it)
	}
	pl.Items = items
	if name != "" {
		pl.Name = name
	}
	pl.UpdatedAt = 2
	putTracks(t, dc, tr)
	putPlaylist(t, dc, pl)
}

func actions(out string) []string {
	var acts []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			acts = append(acts, strings.Split(line, "\t")[0])
		}
	}
	return acts
}

func baseOf(t *testing.T, dc *drive.Client) canon.Snapshot {
	t.Helper()
	dev := decodeFile[canon.DeviceState](t, driveFiles(t, dc), "dev__")
	for _, byProv := range dev.Base {
		if b, ok := byProv["spotify"]; ok {
			return b.Snapshot
		}
	}
	t.Fatal("dev 檔沒有 spotify 的 base")
	return canon.Snapshot{}
}

// ---- 測試 ----

// 主流程:canonical 改成 [c, a, d](刪 b、換序、加 d)並改名 → dry-run 只列(exit 2、平台與 Drive 零寫入)→ --yes 寫平台、base 前進、
// pl__ 不變 → 再 pull 零變更 → 再 push 零變更;非 TTY 沒 --yes 是 exit 2。
func TestPlPushFlow(t *testing.T) {
	fs, dc, _, pl := pushWorld(t)
	editCanonical(t, dc, pl, []string{"c", "a", "d"}, nil, "通勤2")
	before := driveFiles(t, dc)
	out, _, err := runPull(t, "pl", "push", "通勤2", "--dry-run")
	if exitOf(t, err) != 2 || !slices.Equal(actions(out), []string{"remove", "move", "add", "rename"}) {
		t.Fatalf("dry-run:exit %d %v\n%s", exitOf(t, err), err, out)
	}
	if len(fs.written()) != 0 || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("dry-run 不碰平台也不碰 Drive")
	}
	if _, _, err := runPull(t, "pl", "push", "通勤2"); exitOf(t, err) != 2 || len(fs.written()) != 0 {
		t.Fatalf("非 TTY 沒 --yes 要 exit 2、零寫入:%v", err)
	}
	out, errs := mustPull(t, "pl", "push", "通勤2", "--yes")
	if !slices.Equal(actions(out), []string{"remove", "move", "add", "rename"}) || !strings.Contains(errs, "已推送 4 筆") {
		t.Fatalf("push:\n%s%s", out, errs)
	}
	if got := fs.tracksOf("p1"); !slices.Equal(got, []string{"c", "a", "d"}) {
		t.Fatalf("平台要變成 [c a d]:%v", got)
	}
	ws := fs.written()
	if len(ws) != 2 || ws[0].Method != "PUT" || ws[0].Path != "/playlists/p1" || ws[0].Name != "通勤2" || ws[1].Method != "PUT" || ws[1].Path != "/playlists/p1/items" || !slices.Equal(ws[1].URIs, []string{"spotify:track:c", "spotify:track:a", "spotify:track:d"}) {
		t.Fatalf("先改名再整批取代:%+v", ws)
	}
	if b := baseOf(t, dc); b.Name != "通勤2" || !slices.Equal(b.Items, []string{"c", "a", "d"}) {
		t.Fatalf("base 要前進到 L′:%+v", b)
	}
	after := drivePlaylist(t, dc)
	if !slices.Equal(cidsOf(after), cidsOf(pl)) || after.UpdatedAt != 2 {
		t.Fatalf("pl__ 不變:%+v", after)
	}
	if out, _ := mustPull(t, "pl", "pull", "通勤2", "--yes"); strings.TrimSpace(out) != "" {
		t.Fatalf("push 後 pull 零變更:%s", out)
	}
	if out, errs := mustPull(t, "pl", "push", "通勤2", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("再 push 零變更:%s%s", out, errs)
	}
}

// 前提一:沒 pull 過(沒有 base)→ exit 3、零寫入;--yes / --force 都不放行。
func TestPlPushNeedsBase(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	_, _, err := runPull(t, "pl", "push", "通勤", "--yes", "--force")
	if exitOf(t, err) != 3 || !strings.Contains(err.Error(), "還沒 pull 過") || len(fs.written()) != 0 {
		t.Fatalf("無 base 要 exit 3:%v", err)
	}
	if !slices.Equal(fs.tracksOf("p1"), []string{"a"}) {
		t.Fatal("平台不得動")
	}
	_ = dc
}

// 前提二:平台有未 pull 的變更(內容或名稱)→ exit 3、零寫入;pull 之後就能推。
func TestPlPushRefusesUnpulledPlatformChanges(t *testing.T) {
	fs, dc, _, pl := pushWorld(t)
	editCanonical(t, dc, pl, []string{"a", "b"}, nil, "")
	fs.set("p1", "通勤", "a", "b", "c", "x") // 使用者在平台加了 x
	_, _, err := runPull(t, "pl", "push", "通勤", "--yes", "--force")
	if exitOf(t, err) != 3 || !strings.Contains(err.Error(), "未 pull 的變更") || len(fs.written()) != 0 {
		t.Fatalf("未 pull 變更要 exit 3:%v", err)
	}
	mustPull(t, "pl", "pull", "通勤", "--yes") // C 變成 [a, b, x](4′:x 是 base 沒有的新增)
	if out, _ := mustPull(t, "pl", "push", "通勤", "--yes"); !slices.Equal(actions(out), []string{"remove"}) || !slices.Equal(fs.tracksOf("p1"), []string{"a", "b", "x"}) {
		t.Fatalf("pull 後 push 只刪 c:%s %v", out, fs.tracksOf("p1"))
	}
	fs.set("p1", "通勤改", "a", "b", "x") // 只改名也算平台變更
	if _, _, err := runPull(t, "pl", "push", "通勤", "--yes"); exitOf(t, err) != 3 {
		t.Fatalf("平台改名也要先 pull:%v", err)
	}
}

// 前提二只比 id 序列、不比 cid:C 有一首沒 mapping 的 K(從別的平台來)→ push 列 skip;使用者 resolve pin K = spotify:c(c 已在清單上,
// 走合併,K 的 cid 較小所以 c 的 cid 變成墓碑)→ 平台沒動但 c 的觀測 cid 變了 → 再 push 要能過(比 cid 會 exit 3 指向 pull)。
func TestPlPushSkipThenPinThenPush(t *testing.T) {
	fs, dc, _, pl := pushWorld(t)
	editCanonical(t, dc, pl, []string{"a", "b", "c", "0"}, map[string]bool{"0": false}, "")
	out, errs := mustPull(t, "pl", "push", "通勤", "--yes")
	if !slices.Equal(actions(out), []string{"skip"}) || !strings.Contains(errs, "1 首尚未對應到 spotify") || !strings.Contains(errs, "無變更") || len(fs.written()) != 0 {
		t.Fatalf("沒 mapping 的只列 skip、不阻擋、零寫入:\n%s%s", out, errs)
	}
	fs.addCatalog(fakeCatalogTrack{ID: "c", Name: "song-c", ISRC: fakeISRC("c")})
	mustPull(t, "resolve", "pin", fakeCID("0"), "spotify:c", "--yes")
	if tr := driveTracks(t, dc); tr.Merged[fakeCID("c")] != fakeCID("0") {
		t.Fatalf("前提:c 的 cid 要併進 K:%v", tr.Merged)
	}
	out, _ = mustPull(t, "pl", "push", "通勤", "--yes")
	if !slices.Equal(actions(out), []string{"add"}) || !slices.Equal(fs.tracksOf("p1"), []string{"a", "b", "c", "c"}) {
		t.Fatalf("觀測 cid 變了不是平台變更,要推得出去:%s %v", out, fs.tracksOf("p1"))
	}
}

// 刪除閾值:分母是平台曲數;--force 越過、--force 不能配 --all。
func TestPlPushThresholdNeedsForce(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a", "b", "c", "d", "e", "f", "g", "h", "i", "j")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	editCanonical(t, dc, drivePlaylist(t, dc), []string{"a", "b", "c", "d", "e", "f"}, nil, "") // 刪 4 首 = 40% > 30% 且 > 3
	_, _, err := runPull(t, "pl", "push", "通勤", "--yes")
	if exitOf(t, err) != 3 || !strings.Contains(err.Error(), "超過閾值") || len(fs.written()) != 0 {
		t.Fatalf("閾值要 exit 3、零寫入:%v", err)
	}
	if _, _, err := runPull(t, "pl", "push", "--all", "--force"); err == nil || !strings.Contains(err.Error(), "--force 只能配單一清單") {
		t.Fatalf("--force 配 --all 要拒絕:%v", err)
	}
	if out, _ := mustPull(t, "pl", "push", "通勤", "--yes", "--force"); strings.Count(out, "remove\t") != 4 || len(fs.tracksOf("p1")) != 6 {
		t.Fatalf("--force 放行:%s", out)
	}
}

// 含 local file 的 Spotify 清單:有 items 變更就拒絕(exit 3、零寫入);只改名可以。
func TestPlPushRefusesLocalFiles(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.setLocal("l1")
	fs.set("p1", "通勤", "a", "l1", "b")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	pl := drivePlaylist(t, dc)
	if len(pl.Items) != 3 {
		t.Fatalf("local file 也進 C:%+v", pl.Items)
	}
	pl.Name = "通勤2"
	pl.UpdatedAt = 2
	putPlaylist(t, dc, pl)
	if out, _ := mustPull(t, "pl", "push", "通勤2", "--yes"); !slices.Equal(actions(out), []string{"rename"}) || len(fs.written()) != 1 {
		t.Fatalf("只改名可以:%s %+v", out, fs.written())
	}
	pl = drivePlaylist(t, dc)
	pl.Items = pl.Items[:2] // 刪 b
	pl.UpdatedAt = 3
	putPlaylist(t, dc, pl)
	_, _, err := runPull(t, "pl", "push", "通勤2", "--yes", "--force")
	if exitOf(t, err) != 3 || !strings.Contains(err.Error(), "local file") || !strings.Contains(err.Error(), "local-l1") || len(fs.written()) != 1 {
		t.Fatalf("有 local file 的 items 變更要 exit 3:%v", err)
	}
}

// 有 mapping 但推不出去(PR #33 review):local file 從別的清單觀測進 C(mapping 存的是 spotify:local:… uri),推另一個清單時那首列 skip,
// 其餘照推——不是 add 出去被 Spotify 整批拒收。
func TestPlPushSkipsUnpushableMappingFromOtherPlaylist(t *testing.T) {
	fs, dc, _, _ := pushWorld(t)
	fs.setLocal("l1")
	fs.set("p2", "其他", "l1")
	mustPull(t, "pl", "link", "其他", "spotify:p2")
	mustPull(t, "pl", "pull", "其他", "--yes")
	local := drivePlaylistNamed(t, dc, "其他").Items[0]
	if m := driveTracks(t, dc).Tracks[local.CID].Mappings["spotify"]; !strings.HasPrefix(m.ID, "spotify:local:") {
		t.Fatalf("前提:local file 的 mapping 存的是 uri:%+v", m)
	}
	pl := drivePlaylistNamed(t, dc, "通勤")
	editCanonical(t, dc, pl, []string{"a", "b", "c", "d"}, nil, "")
	pl = drivePlaylistNamed(t, dc, "通勤")
	rank, err := canon.RankBetween(pl.Items[len(pl.Items)-1].Rank, "")
	if err != nil {
		t.Fatal(err)
	}
	pl.Items = append(pl.Items, canon.Item{IID: canon.NewULID(), CID: local.CID, Rank: rank, AddedAt: 1})
	putPlaylist(t, dc, pl)
	out, _ := mustPull(t, "pl", "push", "通勤", "--yes")
	if !slices.Equal(actions(out), []string{"add", "skip"}) || !slices.Equal(fs.tracksOf("p1"), []string{"a", "b", "c", "d"}) {
		t.Fatalf("local file 那首列 skip、d 照推:%s %v", out, fs.tracksOf("p1"))
	}
}

// 分批寫到一半失敗(R-7b):150 首、第一個 POST 回 500 → exit 1、訊息說已寫 100 首;base = L′(前 100);下一次 pull 零 remove;下一次 push 補上其餘。
func TestPlPushPartialWriteAdvancesBase(t *testing.T) {
	fs, dc, _, pl := pushWorld(t)
	var ids []string
	for i := 0; i < 150; i++ {
		ids = append(ids, fmt.Sprintf("t%03d", i))
	}
	editCanonical(t, dc, pl, ids, nil, "")
	fs.mu.Lock()
	fs.postFail = 3
	fs.mu.Unlock()
	_, errs, err := runPull(t, "pl", "push", "通勤", "--yes", "--force")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "只有前 100 首") || !strings.Contains(errs, "寫入 通勤 的 spotify 失敗") {
		t.Fatalf("半截寫入要 exit 1 並講明:%v\n%s", err, errs)
	}
	if got := fs.tracksOf("p1"); len(got) != 100 {
		t.Fatalf("平台此刻是前 100 首:%d", len(got))
	}
	if b := baseOf(t, dc); !slices.Equal(b.Items, ids[:100]) {
		t.Fatalf("base 要前進到 L′(前 100 首):%d", len(b.Items))
	}
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); strings.TrimSpace(out) != "" {
		t.Fatalf("自己的半截寫入不是使用者刪歌:%s", out)
	}
	if out, _ := mustPull(t, "pl", "push", "通勤", "--yes"); strings.Count(out, "add\t") != 50 || !slices.Equal(fs.tracksOf("p1"), ids) {
		t.Fatalf("下一次 push 補上其餘 50 首:%d", strings.Count(out, "add\t"))
	}
}

// 半截寫入之後連 L′ 都讀不到:base 記成已寫入的那幾首(want[:written]),下一次 pull 仍零 remove。
func TestPlPushPartialWriteRereadFailureStillAdvancesBase(t *testing.T) {
	fs, dc, _, pl := pushWorld(t)
	var ids []string
	for i := 0; i < 120; i++ {
		ids = append(ids, fmt.Sprintf("t%03d", i))
	}
	editCanonical(t, dc, pl, ids, nil, "")
	fs.mu.Lock()
	fs.postFail = 3
	fs.mu.Unlock()
	gets := 0
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/items") {
			gets++
			if gets >= 3 { // 計畫 1、套用前 2、L′ 3:第 3 次起 GET 回 500
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":{"status":500,"message":"down"}}`))
				return
			}
		}
		fs.handler(t)(w, r)
	})
	_, errs, err := runPull(t, "pl", "push", "通勤", "--yes", "--force")
	if exitOf(t, err) != 1 || !strings.Contains(errs, "base 先記成已寫入的 100 首") {
		t.Fatalf("重讀失敗要記 want[:written]:%v\n%s", err, errs)
	}
	if b := baseOf(t, dc); !slices.Equal(b.Items, ids[:100]) || len(b.CIDs) != 100 || b.CIDs[0] != fakeCID("t000") {
		t.Fatalf("base = 前 100 首(含 cid):%d %d", len(b.Items), len(b.CIDs))
	}
	swapProvider(t, fs.handler(t))
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); strings.Contains(out, "remove\t") {
		t.Fatalf("下一次 pull 零 remove:%s", out)
	}
}

// 確認之後、寫入之前平台變了(手機同時加歌)→ 那份零寫入、exit 3;平台原樣。
func TestPlPushDetectsChangeBetweenPlanAndApply(t *testing.T) {
	fs, dc, _, pl := pushWorld(t)
	editCanonical(t, dc, pl, []string{"a", "b"}, nil, "")
	reads := 0
	fs.setHook(func() {
		reads++
		if reads == 3 { // 第 3 個請求 = 套用前的重讀(1 = /me/playlists、2 = 計畫的 GET items):hook 在回應前改,重讀就看到
			fs.items["p1"] = []string{"a", "b", "c", "z"}
		}
	})
	_, errs, err := runPull(t, "pl", "push", "通勤", "--yes")
	if exitOf(t, err) != 3 || !strings.Contains(errs, "於確認期間變了") || len(fs.written()) != 0 {
		t.Fatalf("確認期間變動要 exit 3、零寫入:%v\n%s", err, errs)
	}
	if !slices.Equal(fs.tracksOf("p1"), []string{"a", "b", "c", "z"}) {
		t.Fatal("平台原樣")
	}
}

// 平台寫成功、Drive 上傳失敗(R-7a 注入版):exit 1 且訊息說平台已寫;base 沒前進;下一次 pull 把剛 push 的東西讀成零變更(不重複)。
func TestPlPushDriveUploadFailureThenPullDoesNotDuplicate(t *testing.T) {
	fs, dc, srv, pl := pushWorld(t)
	editCanonical(t, dc, pl, []string{"a", "b", "c", "d"}, nil, "")
	srv.FailOn(func(r *http.Request) bool { return r.Method == http.MethodPatch }, http.StatusInternalServerError, "backendError")
	_, _, err := runPull(t, "pl", "push", "通勤", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "平台已寫入 1 筆") || !slices.Equal(fs.tracksOf("p1"), []string{"a", "b", "c", "d"}) {
		t.Fatalf("Drive 失敗要講明平台已寫:%v", err)
	}
	if b := baseOf(t, dc); !slices.Equal(b.Items, []string{"a", "b", "c"}) {
		t.Fatalf("base 沒前進:%v", b.Items)
	}
	srv.FailOn(nil, 0, "")
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); strings.TrimSpace(out) != "" {
		t.Fatalf("下一次 pull 零變更:%s", out)
	}
	if b := baseOf(t, dc); !slices.Equal(b.Items, []string{"a", "b", "c", "d"}) {
		t.Fatalf("pull 把 base 補上:%v", b.Items)
	}
}

// Drive 版本守衛在 push 的 COMMIT 擋下:平台已寫、Drive 沒動、訊息不說「零寫入」;pull → 零變更 → push → 零 ops。
func TestPlPushVersionGuardMessage(t *testing.T) {
	fs, dc, _, pl := pushWorld(t)
	editCanonical(t, dc, pl, []string{"a", "b", "c", "d"}, nil, "")
	fs.setHook(once(func() { // 別台裝置在我們 OBSERVE 期間改了 tracks.json
		tr := driveTracks(t, dc)
		tr.Tracks[fakeCID("zz")] = canon.Track{CID: fakeCID("zz"), Title: "zz", Mappings: map[string]canon.Mapping{}}
		putTracks(t, dc, tr)
	}))
	_, _, err := runPull(t, "pl", "push", "通勤", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "平台已寫入 1 筆") || strings.Contains(err.Error(), "零寫入,重跑") || !slices.Equal(fs.tracksOf("p1"), []string{"a", "b", "c", "d"}) {
		t.Fatalf("守衛訊息要改口:%v", err)
	}
	fs.setHook(nil)
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); strings.TrimSpace(out) != "" {
		t.Fatalf("pull 零變更:%s", out)
	}
	if out, errs := mustPull(t, "pl", "push", "通勤", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("push 零 ops:%s%s", out, errs)
	}
}

// 403(別人的清單)→ exit 1、友善訊息;base 不動。
func TestPlPushForbidden(t *testing.T) {
	fs, dc, _, pl := pushWorld(t)
	editCanonical(t, dc, pl, []string{"a", "b"}, nil, "")
	fs.setWriteStatus(http.StatusForbidden)
	_, _, err := runPull(t, "pl", "push", "通勤", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "拒絕寫入") {
		t.Fatalf("403 要友善訊息:%v", err)
	}
	if b := baseOf(t, dc); !slices.Equal(b.Items, []string{"a", "b", "c"}) {
		t.Fatalf("平台沒動、base 不變:%v", b.Items)
	}
}

// Apple 在 T6 前寫不了:--all 只跳過(stderr 說明),明說 --provider apple 是 exit 1。
func TestPlPushSkipsAppleUntilWritable(t *testing.T) {
	fs, dc, _, pl := pushWorld(t)
	orig := newProvider // 測試把兩個 provider 都換成假 Spotify;apple 要是「讀得到、寫不了」的那種
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		p, err := orig(ctx, id)
		if id == "apple" {
			return readOnlyProvider{p}, err
		}
		return p, err
	}
	t.Cleanup(func() { newProvider = orig })
	pl.Links["apple"] = "ap1"
	editCanonical(t, dc, pl, []string{"a", "b"}, nil, "")
	out, errs := mustPull(t, "pl", "push", "--all", "--yes")
	if !strings.Contains(errs, "跳過 通勤 的 apple") || !slices.Equal(actions(out), []string{"remove"}) || !slices.Equal(fs.tracksOf("p1"), []string{"a", "b"}) {
		t.Fatalf("Apple 跳過、Spotify 照推:%s%s", out, errs)
	}
	if _, _, err := runPull(t, "pl", "push", "通勤", "--provider", "apple", "--yes"); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "不支援寫入播放清單") {
		t.Fatalf("明說 apple 要 exit 1:%v", err)
	}
}

// 刪 db 之後從 Drive 重建,push 的結果(base、tracks)與原本等價。
func TestPlPushRebuildFromDriveIsEquivalent(t *testing.T) {
	_, dc, _, pl := pushWorld(t)
	editCanonical(t, dc, pl, []string{"c", "a"}, nil, "")
	mustPull(t, "pl", "push", "通勤", "--yes")
	before, dump := driveFiles(t, dc), dumpBytes(t)
	deleteDB(t)
	mustPull(t, "pl", "pull", "通勤", "--yes")
	if !sameFiles(before, driveFiles(t, dc)) || !bytes.Equal(dump, dumpBytes(t)) {
		t.Fatal("刪 db 重建後 Drive 與本機快取都要等價")
	}
}

// readOnlyProvider:只露出 Provider 介面(型別斷言不到 PlaylistWriter)。
type readOnlyProvider struct{ provider.Provider }

func TestPlPushArgs(t *testing.T) {
	pullWorld(t)
	for _, args := range [][]string{{"pl", "push"}, {"pl", "push", "x", "--all"}, {"pl", "push", "x", "--provider", "nope"}} {
		if _, _, err := runPull(t, args...); exitOf(t, err) != 1 {
			t.Fatalf("%v 要 exit 1:%v", args, err)
		}
	}
	_ = config.Load
}
