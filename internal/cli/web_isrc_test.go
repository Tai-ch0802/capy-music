package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

// isrcGet:打 /api/isrc/{isrc},回狀態碼與解好的 JSON。
func (c *webClient) isrcGet(path string) (int, map[string]any) {
	c.t.Helper()
	resp := c.req(context.Background(), http.MethodGet, path, nil, nil)
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		c.t.Fatalf("%s 的回應不是 JSON:%v", path, err)
	}
	return resp.StatusCode, m
}

func provTracks(t *testing.T, d map[string]any, id string) []any {
	t.Helper()
	p, ok := d["providers"].(map[string]any)[id].(map[string]any)
	if !ok {
		t.Fatalf("回應缺 providers.%s:%v", id, d["providers"])
	}
	return p["tracks"].([]any)
}

func provError(d map[string]any, id string) string {
	p, ok := d["providers"].(map[string]any)[id].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := p["error"].(string)
	return s
}

func TestWebISRCBadFormat400(t *testing.T) {
	setCLITestConfig(t)
	_, c := startWeb(t)
	for _, bad := range []string{"nope", "TWK23168079", "%20"} {
		code, m := c.isrcGet("/api/isrc/" + bad)
		if code != http.StatusBadRequest || !strings.Contains(m["error"].(string), "12 碼") {
			t.Errorf("%q → %d %v,要 400 帶 ErrBadISRC", bad, code, m["error"])
		}
	}
}

// TestWebISRCUnparsableStillQueriesProviders:isrcPartsRe 比 NormalizeISRC 嚴(年份 / 流水號要數字),
// 拆不出四段**不是** 400(review #58):平台照查,只有 parts 是 null。
func TestWebISRCUnparsableStillQueriesProviders(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "sp1", Name: "song", ISRC: "TWK23A680790"})
	_, c := startWeb(t)
	code, m := c.isrcGet("/api/isrc/TWK23A680790")
	if code != 200 || m["parts"] != nil {
		t.Fatalf("拆不出四段要 200 + parts null:%d %v", code, m["parts"])
	}
	if got := provTracks(t, m, "spotify"); len(got) != 1 {
		t.Errorf("平台仍要照查:%v", got)
	}
}

func TestWebISRCEndpointAllProviders(t *testing.T) {
	fs1, fs2, _, _ := twoPlatforms(t)
	const isrc = "TWK231680790"
	fs1.addCatalog(fakeCatalogTrack{ID: "sp1", Name: "派對動物", ISRC: isrc})
	fs2.addCatalog(fakeCatalogTrack{ID: "ap1", Name: "派對動物", ISRC: isrc})
	origNew := newProvider // local 直接回錯:驗「單一平台失敗不讓整個回應失敗」
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id == "local" {
			return nil, errors.New("先執行 capy config set local_root")
		}
		return origNew(ctx, id)
	}
	t.Cleanup(func() { newProvider = origNew })
	_, c := startWeb(t)
	code, m := c.isrcGet("/api/isrc/twk-2316-80790") // 小寫 + 連字號也要正規化得出同一個
	if code != 200 || m["isrc"] != isrc {
		t.Fatalf("%d %v", code, m["isrc"])
	}
	parts := m["parts"].(map[string]any)
	if parts["country"] != "TW" || parts["registrant"] != "K23" || parts["year_full"].(float64) != 2016 ||
		parts["designation"] != "80790" || parts["geographic"] != true {
		t.Errorf("四段:%v", parts)
	}
	if got := provTracks(t, m, "spotify"); len(got) != 1 || got[0].(map[string]any)["id"] != "sp1" {
		t.Errorf("spotify:%v", got)
	}
	if got := provTracks(t, m, "apple"); len(got) != 1 || got[0].(map[string]any)["id"] != "ap1" {
		t.Errorf("apple:%v", got)
	}
	// local 沒設 local_root:那一格帶 error,但不擋其他平台、也不讓整個回應失敗。
	if !strings.Contains(provError(m, "local"), "local_root") {
		t.Errorf("local 這格應帶 error 而不是擋掉整個回應:%v", m["providers"])
	}
	// 只問一家時只回一家。
	_, one := c.isrcGet("/api/isrc/" + isrc + "?provider=spotify")
	if len(one["providers"].(map[string]any)) != 1 {
		t.Errorf("?provider=spotify 只回一家:%v", one["providers"])
	}
	if code, _ := c.isrcGet("/api/isrc/" + isrc + "?provider=tidal"); code != http.StatusBadRequest {
		t.Errorf("未知 provider → 400,得到 %d", code)
	}
}

// TestWebISRCCanonicalFromLocalMirror:canonical 全部來自本機鏡像(唯讀、零副作用),含它的清單也走訪得到。
func TestWebISRCCanonicalFromLocalMirror(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1", "t2")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	before := driveFiles(t, dc)
	_, c := startWeb(t)

	code, m := c.isrcGet("/api/isrc/" + fakeISRC("t2"))
	if code != 200 || m["canonical_source"] != "local-cache" {
		t.Fatalf("%d %v", code, m["canonical_source"])
	}
	canon, ok := m["canonical"].(map[string]any)
	if !ok {
		t.Fatalf("canonical 不該是 null:%v", m)
	}
	if canon["cid"] != fakeCID("t2") {
		t.Errorf("cid:%v", canon["cid"])
	}
	if mp := canon["mappings"].(map[string]any)["spotify"].(map[string]any); mp["id"] != "t2" {
		t.Errorf("mappings.spotify:%v", mp)
	}
	pls := canon["playlists"].([]any)
	if len(pls) != 1 {
		t.Fatalf("含它的清單:%v", pls)
	}
	p0 := pls[0].(map[string]any)
	if p0["name"] != "通勤" || p0["pos"].(float64) != 1 || p0["links"].(map[string]any)["spotify"] != "p1" {
		t.Errorf("清單項:%v", p0)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Error("查詢是唯讀的,不得寫 Drive")
	}
}

// TestWebISRCCanonicalMissingCIDIsNull:Resolve 對沒見過的 ISRC 會落 i:<ISRC> 公式(一定回一個 cid),
// 所以存在性檢查不能少——否則會回一個本機根本沒有的 cid。
func TestWebISRCCanonicalMissingCIDIsNull(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	_, c := startWeb(t)
	code, m := c.isrcGet("/api/isrc/TWZZZ9999999")
	if code != 200 || m["canonical"] != nil {
		t.Fatalf("本機沒有這首 → canonical null:%d %v", code, m["canonical"])
	}
	if m["canonical_source"] != "local-cache" {
		t.Errorf("讀得到 db 就要標來源:%v", m["canonical_source"])
	}
}

// TestWebISRCMergedCIDRedirectsAndHitsAliasSet:resolve --review 接受一個已屬別人的候選 → 合併。
// 之後查敗者的 ISRC:(1) 走 alias set 對到勝者 cid(不是公式算出來的 i:<ISRC>),(2) 清單裡的 item cid 先 Redirect 才比得到。
func TestWebISRCMergedCIDRedirectsAndHitsAliasSet(t *testing.T) {
	fs, _, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")})
	mustPull(t, "resolve", "--yes")                                                  // a → ap-a
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("b")}) // b 的候選也是 ap-a(已屬 a)
	stubReview(t, func(it resolveItem, _ func(string) ([]provider.Track, error)) reviewDecision {
		return reviewDecision{kind: "accept", cand: it.cand}
	})
	origConfirm := confirmWrite
	confirmWrite = func(string, string) (bool, error) { return true, nil } // 同意合併
	t.Cleanup(func() { confirmWrite = origConfirm })
	mustPull(t, "resolve", "--review")

	_, c := startWeb(t)
	_, m := c.isrcGet("/api/isrc/" + fakeISRC("b"))
	canon, ok := m["canonical"].(map[string]any)
	if !ok {
		t.Fatalf("合併後查敗者的 ISRC 仍要找得到(alias set):%v", m["canonical_error"])
	}
	if canon["cid"] != fakeCID("a") {
		t.Errorf("要回勝者 cid %s,得到 %v", fakeCID("a"), canon["cid"])
	}
	if canon["cid"] == fakeCID("b") {
		t.Error("不能回敗者 cid(墓碑沒被追)")
	}
	if pls := canon["playlists"].([]any); len(pls) == 0 {
		t.Error("清單裡的 item cid 要先 Redirect 才比得到勝者")
	}
}

// TestWebISRCNoDBIsNotAnError:【fails-before-fix】還沒有 state.db 是第一次 pl pull 之前的正常狀態,
// 不是錯誤——帶 canonical_error 會讓頁面畫成紅字,而且蓋掉「本機還沒有這首的紀錄(先 capy pl pull)」那句指路。
func TestWebISRCNoDBIsNotAnError(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "sp1", Name: "song", ISRC: "TWK231680790"})
	deleteDB(t)
	_, c := startWeb(t)
	code, m := c.isrcGet("/api/isrc/TWK231680790")
	if code != 200 || m["canonical"] != nil {
		t.Fatalf("%d %v", code, m["canonical"])
	}
	if e, ok := m["canonical_error"]; ok {
		t.Errorf("沒有 db 不是錯誤,不該帶 canonical_error:%v", e)
	}
	if len(provTracks(t, m, "spotify")) != 1 {
		t.Error("平台結果照常")
	}
}

// TestWebISRCPlaylistWalkFollowsTombstone:清單裡的 item cid 還指著墓碑(COMMIT 中途斷掉、tracks.json 已寫但
// 清單沒寫到的自癒情境)時,走訪要先 Redirect 才比得到——直接比 it.CID == cid 會說「沒有清單含這首」。
// 走 CLI 合併出來的狀態測不到這條:resolve 的 COMMIT 會順手把 item 重導,所以這裡直接把鏡像做成那個樣子。
func TestWebISRCPlaylistWalkFollowsTombstone(t *testing.T) {
	setCLITestConfig(t)
	const isrc = "TWAAA0000001"
	winner, loser := "i:"+isrc, "i:TWBBB0000002"
	c := store.Canonical{Manifest: canon.NewManifest(), Tracks: canon.NewTracks(), Playlists: []canon.Playlist{}, Devices: []canon.DeviceState{}}
	c.Tracks.Tracks[winner] = canon.Track{
		CID: winner, ISRC: []string{isrc}, Title: "勝者", Artists: []string{"artist"},
		DurationMS: 200000, Mappings: map[string]canon.Mapping{},
	}
	c.Tracks.Merged[loser] = winner // 敗者的墓碑
	pl := canon.NewPlaylist("通勤")
	pl.Items = []canon.Item{{IID: "iid1", CID: loser, Rank: "a"}} // 清單裡還指著墓碑
	c.Playlists = append(c.Playlists, *pl)

	st, err := store.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Hydrate(c); err != nil {
		t.Fatal(err)
	}
	st.Close()

	_, cli := startWeb(t)
	_, m := cli.isrcGet("/api/isrc/" + isrc)
	canonical, ok := m["canonical"].(map[string]any)
	if !ok {
		t.Fatalf("canonical 不該是 null:%v", m["canonical_error"])
	}
	if canonical["cid"] != winner {
		t.Errorf("cid:%v", canonical["cid"])
	}
	pls := canonical["playlists"].([]any)
	if len(pls) != 1 || pls[0].(map[string]any)["name"] != "通勤" {
		t.Fatalf("item cid 指著墓碑時也要找得到那張清單:%v", pls)
	}
}

// TestWebISRCReadOnlyDoesNotWaitPullLockNorRunningJob:查詢不進 runMu、也不取 pull.lock——
// 直接把序列槽與 pull.lock 都握住,查詢仍要照常回(用假平台的 hook 擋不出這件事:那會連查詢自己的 HTTP 一起擋)。
func TestWebISRCReadOnlyDoesNotWaitPullLockNorRunningJob(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "sp1", Name: "song", ISRC: "TWK231680790"})
	s, c := startWeb(t)

	lctx, lcancel := context.WithCancel(context.Background())
	defer lcancel()
	unlock, err := auth.LockFile(lctx, "pull.lock", "測試握著")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	s.runMu.Lock() // = 有一個 job 正在跑
	defer s.runMu.Unlock()

	done := make(chan int, 1)
	go func() { code, _ := c.isrcGet("/api/isrc/TWK231680790"); done <- code }()
	select {
	case code := <-done:
		if code != 200 {
			t.Errorf("查詢應照常回 200,得到 %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("查詢被 pull.lock 或序列槽擋住了")
	}
	// 對照:同一時間 /api/run 確實被序列槽擋著(409),證明上面那把鎖是真的握著的。
	if code, _, _ := c.run(map[string]any{"args": []string{"--help"}}); code != http.StatusConflict {
		t.Errorf("序列槽握著時 /api/run 要 409,得到 %d", code)
	}
}
