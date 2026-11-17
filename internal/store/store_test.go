package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func pin(t *testing.T) {
	t.Helper()
	origNow, origULID := canon.Now, canon.NewULID
	clock, seq := time.Unix(1_756_600_000, 0), 0
	canon.Now = func() time.Time { clock = clock.Add(time.Second); return clock }
	canon.NewULID = func() string { seq++; return fmt.Sprintf("01TESTULID%016d", seq) }
	t.Cleanup(func() { canon.Now, canon.NewULID = origNow, origULID })
}

// fixture:涵蓋會咬人的形狀——沒 artists 的曲、有衝突的曲、沒 links 沒 items 的清單、base 全空的裝置、同曲重複出現。
func fixture(t *testing.T) Canonical {
	t.Helper()
	pin(t)
	m := canon.NewManifest()
	m.Touch("devB", "win")
	m.Touch("devA", "mac")
	tr := canon.NewTracks()
	x := canon.NewTrack("spotify", provider.Track{ProviderID: "6rq", ISRC: "TWA472400123", Title: "派對動物", Artists: []string{"五月天"}, Album: "自傳", DurationMS: 227000})
	x.Mappings["tidal"] = canon.Mapping{ID: "t1", Confidence: 72, Source: canon.SourceFuzzy, UpdatedAt: 8}                 // 四個新欄都要能無損來回(決策 20)
	x.Mappings["youtube"] = canon.Mapping{ID: "", Confidence: 100, Pinned: true, Source: canon.SourceReview, UpdatedAt: 9} // 釘成「不可得」
	x.Observe("apple", provider.Track{ProviderID: "i.abc", ISRC: "TWA472400123", Title: "派對動物 (Live)", DurationMS: 252000})
	tr.Tracks[x.CID] = x
	up := canon.NewTrack("apple", provider.Track{ProviderID: "i.lib1", Title: "上傳曲"})
	tr.Tracks[up.CID] = up
	tr.Merged["i:TW000000000X"] = x.CID // 合併墓碑(決策 21)也要無損來回
	pl := canon.NewPlaylist("通勤")
	pl.Description = "上班聽"
	pl.Links["spotify"] = "37i9"
	for _, cid := range []string{x.CID, up.CID, x.CID} {
		if _, err := pl.Append(cid); err != nil {
			t.Fatal(err)
		}
	}
	front, err := canon.RankBetween("", pl.Items[0].Rank) // 之後插到最前:rank 最小但 iid 最大
	if err != nil {
		t.Fatal(err)
	}
	pl.Items = append(pl.Items, canon.Item{IID: canon.NewULID(), CID: up.CID, Rank: front, AddedAt: canon.Now().Unix()})
	empty := canon.NewPlaylist("空的")
	m.AddPlaylist(empty.PID) // manifest 宣告的清單 = fixture 的清單(Dump 由 playlists 表推回,兩者必須恆等)
	m.AddPlaylist(pl.PID)
	devA := canon.NewDeviceState("devA")
	devA.SetBase(pl.PID, "spotify", canon.Snapshot{ID: "37i9", Name: "通勤", Items: []string{"6rq", "x", "6rq"}, CIDs: []string{x.CID, "p:spotify:x", x.CID}})
	devA.SetBase(pl.PID, "apple", canon.Snapshot{Name: "通勤"})
	devB := canon.NewDeviceState("devB") // base 全空,但檔案存在
	devC := canon.NewDeviceState("devC") // 有 dev 檔、manifest 沒登記(登記寫入失敗過)也不能消失
	// Dump 依 pid 排序,fixture 也照這個順序給(pl 的 ULID 先於 empty),比對才是「同一組檔案」。
	return Canonical{Manifest: m, Tracks: tr, Playlists: []canon.Playlist{*pl, *empty}, Devices: []canon.DeviceState{*devA, *devB, *devC}}
}

func encodeAll(t *testing.T, c Canonical) [][]byte {
	t.Helper()
	var out [][]byte
	add := func(v any) {
		b, err := canon.Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	add(c.Manifest)
	add(c.Tracks)
	for i := range c.Playlists {
		add(&c.Playlists[i])
	}
	for i := range c.Devices {
		add(&c.Devices[i])
	}
	return out
}

func mustEqual(t *testing.T, want, got [][]byte, what string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s:檔案數 %d vs %d", what, len(want), len(got))
	}
	for i := range want {
		if !bytes.Equal(want[i], got[i]) {
			t.Fatalf("%s:第 %d 個檔不同\n%s\n%s", what, i, want[i], got[i])
		}
	}
}

func noDBFiles(t *testing.T, path string) {
	t.Helper()
	left, _ := filepath.Glob(path + "*")
	if len(left) != 0 {
		t.Fatalf("Remove 後不該留下任何 db 檔(Windows 上未 Close 就刪會失敗):%v", left)
	}
}

// TestRebuildFromDrive 是 CLAUDE.md「SQLite 是 cache;刪除 db 必須能從 Drive 完整重建」這條硬約束的測試:
// Drive 檔 → Hydrate → Dump 必須無損(位元組等於原檔);Remove 後再來一次必須逐位元相同。
func TestRebuildFromDrive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	in := fixture(t)
	want := encodeAll(t, in)
	pl := in.Playlists[0]
	pl.Items[0], pl.Items[1] = pl.Items[1], pl.Items[0] // 檔案裡 items 亂序也要能吃:順序就是 rank
	in.Playlists[0] = pl

	s, err := OpenAt(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Hydrate(in); err != nil {
		t.Fatal(err)
	}
	out, err := s.Dump()
	if err != nil {
		t.Fatal(err)
	}
	// 先驗順序再 Encode:Encode 會 normalize(原地依 rank 排),之後就看不出 Dump 自己有沒有排。
	if items := out.Playlists[0].Items; len(items) != 4 || items[0].Rank != "F" || items[1].Rank != "V" || items[3].Rank != "s" {
		t.Fatalf("Dump 的 items 要依 (rank, iid),不是 iid:%+v", items)
	}
	first := encodeAll(t, out)
	mustEqual(t, want, first, "Dump(Hydrate(x)) 要等於 x")
	if len(out.Devices) != 3 || out.Devices[2].DeviceID != "devC" || len(out.Manifest.Devices) != 2 {
		t.Fatalf("沒登記但有 dev 檔的裝置要留著、manifest 只回登記的:%+v / %+v", out.Devices, out.Manifest.Devices)
	}

	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	noDBFiles(t, path)
	if s, err = OpenAt(path, time.Second); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if out, err = s.Dump(); err != nil || len(out.Playlists) != 0 || len(out.Tracks.Tracks) != 0 || len(out.Devices) != 0 || len(out.Manifest.Devices) != 0 {
		t.Fatalf("刪掉重開應為空:%v %+v", err, out)
	}
	if err := s.Hydrate(in); err != nil {
		t.Fatal(err)
	}
	if out, err = s.Dump(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, first, encodeAll(t, out), "重建後要逐位元相同")
	if err := s.Hydrate(Canonical{}); err != nil { // Hydrate 是取代,不是累加
		t.Fatal(err)
	}
	if out, _ = s.Dump(); len(out.Playlists) != 0 || len(out.Devices) != 0 {
		t.Fatalf("Hydrate 空快照應清空鏡像:%+v", out)
	}
}

func TestMismatchedSchemaVersionRetiresOldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenAt(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Hydrate(fixture(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var notice bytes.Buffer
	Stderr = &notice
	t.Cleanup(func() { Stderr = os.Stderr })
	if s, err = OpenAt(path, time.Second); err != nil {
		t.Fatal(err)
	}
	out, err := s.Dump()
	if err != nil || len(out.Tracks.Tracks) != 0 {
		t.Fatalf("版本不符應整檔丟棄重建(不寫 ALTER):%v %+v", err, out.Tracks)
	}
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Fatalf("重建後 user_version 應為 %d,得 %d", schemaVersion, v)
	}
	// 舊檔保留為 state.db.v99、位元組原封不動、有提示;再升一次會覆蓋同名保留檔(Windows 的 rename 不會蓋)。
	kept, err := os.ReadFile(path + ".v99")
	if err != nil || !bytes.Equal(kept, old) || !strings.Contains(notice.String(), "state.db.v99") {
		t.Fatalf("版本不符的舊檔要原封保留並提示:%v %q", err, notice.String())
	}
	if _, err := s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s2, err := OpenAt(path, time.Second); err != nil {
		t.Fatalf("同名保留檔已存在時再升版要能覆蓋:%v", err)
	} else {
		s2.Close() // 沒關的話 Windows 的 TempDir 清理會說檔案被佔用
	}
	// 全新的 db(user_version 0)不留 .v0。
	fresh := filepath.Join(t.TempDir(), "state.db")
	if f, err := OpenAt(fresh, time.Second); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
	if _, err := os.Stat(fresh + ".v0"); err == nil {
		t.Fatal("全新的 db 不該留 .v0")
	}
}

// 壞掉的 state.db 要自癒:它是 cache,政策允許我們自己刪;否則補全會永遠靜默回空、canonical 路徑每次同一個錯。
func TestCorruptDBIsRebuilt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenAt(path, time.Second)
	if err != nil {
		t.Fatalf("壞檔應刪掉重建而不是報錯:%v", err)
	}
	defer s.Close()
	out, err := s.Dump()
	if err != nil || len(out.Tracks.Tracks) != 0 {
		t.Fatalf("重建後應為空:%v", err)
	}
	if err := s.Hydrate(fixture(t)); err != nil {
		t.Fatalf("重建後要寫得進去:%v", err)
	}
}

func TestPathWithQuestionMarkRejected(t *testing.T) {
	_, err := OpenAt(filepath.Join(t.TempDir(), "q?mark", "state.db"), time.Second)
	if err == nil || !strings.Contains(err.Error(), "?") {
		t.Fatalf("含 ? 的路徑會被 driver 切開、db 靜默開到別處,要直接拒絕:%v", err)
	}
}

// 孤兒列(T8 的 upsert 順序錯、檔案部分損壞)要回錯讓上層刪檔重建,不能 panic。
func TestDumpRejectsOrphanRows(t *testing.T) {
	for _, c := range []struct{ table, insert string }{
		{"isrcs", "INSERT INTO isrcs (cid, isrc) VALUES ('ghost', 'X')"},
		{"mappings", "INSERT INTO mappings (cid, provider, provider_id, confidence, pinned, source, updated_at) VALUES ('ghost', 'spotify', 'x', 100, 0, 'observed', 0)"},
		{"playlist_items", "INSERT INTO playlist_items (pid, iid, cid, rank, added_at) VALUES ('ghost', 'i', 'c', 'V', 0)"},
		{"playlist_links", "INSERT INTO playlist_links (pid, provider, provider_id) VALUES ('ghost', 'spotify', 'x')"},
		{"device_base", "INSERT INTO device_base (device_id, pid, provider, playlist_id, name, items, cids, observed_at) VALUES ('ghost', 'p', 'spotify', '', '', '[]', '[]', 0)"},
		{"merged", "INSERT INTO merged (cid, into_cid) VALUES ('loser', 'ghost')"}, // 敗者不在 tracks 是正常的,勝者不在才是壞掉
	} {
		s, err := OpenAt(filepath.Join(t.TempDir(), "state.db"), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(c.insert); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Dump(); err == nil || !strings.Contains(err.Error(), c.table) {
			t.Fatalf("%s 的孤兒列應回錯並點名該表:%v", c.table, err)
		}
		s.Close()
	}
}

func TestCacheTablesRoundTrip(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "state.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pls := map[string][]ProviderPlaylist{"spotify": {{ID: "p2", Name: "後", Total: 1}, {ID: "p1", Name: "前", Total: 2}}}
	rec := []Recent{{At: 9, Provider: "spotify", Type: "track", ID: "a", Detail: "d"}, {At: 1, Provider: "spotify", Type: "track", ID: "b"}} // at 與加入順序反向
	if _, err := s.ApplyCache(CacheDelta{Playlists: pls, Recent: rec, MaxRecent: 50}); err != nil {
		t.Fatal(err)
	}
	gotPls, gotRec, err := s.LoadCache()
	if err != nil || gotPls["spotify"][0].ID != "p2" || gotPls["spotify"][1].ID != "p1" || len(gotRec) != 2 || gotRec[0].ID != "b" || gotRec[1].Detail != "d" {
		t.Fatalf("清單照給的順序、最近項目後加的在前(不看 at):%v %v %v", err, gotPls, gotRec)
	}
	n, err := s.ApplyCache(CacheDelta{ClearRecent: true, Playlists: map[string][]ProviderPlaylist{"spotify": nil}})
	if err != nil || n != 2 {
		t.Fatalf("清空要回刪掉幾筆:%d %v", n, err)
	}
	if gotPls, gotRec, _ = s.LoadCache(); len(gotPls) != 0 || gotRec != nil {
		t.Fatalf("清單給空的 = 那一家清空;最近項目清空:%v %v", gotPls, gotRec)
	}
}

// TestApplyCacheDedupesAndTrims:加最近項目時同 provider+type+id 的舊列拿掉、超過上限剪掉最舊的;沒超過上限一筆都不剪
// (LIMIT 寫錯會多剪),上限 0 不剪光。
func TestApplyCacheDedupesAndTrims(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "state.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	add := func(max int, ids ...string) []string {
		t.Helper()
		var rec []Recent
		for _, id := range ids {
			rec = append(rec, Recent{Provider: "spotify", Type: "track", ID: id})
		}
		if _, err := s.ApplyCache(CacheDelta{Recent: rec, MaxRecent: max}); err != nil {
			t.Fatal(err)
		}
		_, got, _ := s.LoadCache()
		var out []string
		for _, r := range got {
			out = append(out, r.ID)
		}
		return out
	}
	if got := add(3, "a", "b"); !slices.Equal(got, []string{"b", "a"}) {
		t.Errorf("沒超過上限不剪:%v", got)
	}
	if got := add(3, "c", "d", "b"); !slices.Equal(got, []string{"b", "d", "c"}) {
		t.Errorf("重複的移到最前、超過上限剪最舊的:%v", got)
	}
	if got := add(0, "e"); !slices.Equal(got, []string{"e", "b", "d", "c"}) {
		t.Errorf("上限是 0 = 不剪,不是剪光:%v", got)
	}
}

// TestApplyCacheWaitsForAnotherWriter:另一個 capy 正拿著寫鎖時,要照 busy_timeout 等它寫完再寫,不是立刻 SQLITE_BUSY。
// 交易的第一句是寫入才會這樣;第一句若是讀,升級寫鎖那一步 SQLite 不走 busy handler(見 ApplyCache 的註解)。
func TestApplyCacheWaitsForAnotherWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	a, err := OpenAt(path, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenAt(path, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tx, err := a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("DELETE FROM recent"); err != nil { // A 拿著寫鎖
		t.Fatal(err)
	}
	done := make(chan error, 1)
	time.AfterFunc(150*time.Millisecond, func() { done <- tx.Commit() })
	start := time.Now()
	_, err = b.ApplyCache(CacheDelta{Recent: []Recent{{Provider: "spotify", Type: "query", ID: "q"}}, MaxRecent: 50})
	if err != nil || time.Since(start) < 100*time.Millisecond {
		t.Errorf("要等對方寫完再寫(%v 後):%v", time.Since(start), err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, got, _ := b.LoadCache(); len(got) != 1 {
		t.Errorf("等完之後要寫進去:%v", got)
	}
}

// TestSavePlaylistItemsDropsExpired:存一份清單的曲目時順便刪掉過期的——解除連結的清單不會再被覆寫,不刪就永遠留著
// (Spotify 只准暫時快取 metadata,決策 57)。沒過期的別家清單不動。
func TestSavePlaylistItemsDropsExpired(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "state.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t0 := time.Unix(1_790_000_000, 0)
	for _, c := range []struct {
		id string
		at time.Time
	}{{"old", t0}, {"fresh", t0.Add(6 * 24 * time.Hour)}} {
		if err := s.SavePlaylistItems("spotify", c.id, PlaylistItems{Version: "v", Tracks: []byte("[]"), FetchedAt: c.at}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	now := t0.Add(8 * 24 * time.Hour)
	if err := s.SavePlaylistItems("spotify", "new", PlaylistItems{Version: "v", Tracks: []byte("[]"), FetchedAt: now}, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"old": false, "fresh": true, "new": true} {
		if _, ok, err := s.CachedPlaylistItems("spotify", id); err != nil || ok != want {
			t.Errorf("%s:留著=%v,要 %v(%v)", id, ok, want, err)
		}
	}
}

// 唯讀開法:不建檔、版本不符不改名、壞檔不刪——逃生口不得有副作用。
func TestOpenReadOnlyNeverTouchesFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if _, err := OpenReadOnlyAt(path, time.Second); !errors.Is(err, ErrNoDB) {
		t.Fatalf("沒有 db 要回 ErrNoDB:%v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("唯讀開法不建檔")
	}
	s, err := OpenAt(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Hydrate(fixture(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	old, _ := os.ReadFile(path)
	if _, err := OpenReadOnlyAt(path, time.Second); !errors.Is(err, ErrSchemaMismatch) || !strings.Contains(err.Error(), "v99") {
		t.Fatalf("版本不符要回 ErrSchemaMismatch 並講版本:%v", err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, old) {
		t.Fatal("版本不符:檔案要原封不動")
	}
	if _, err := os.Stat(path + ".v99"); err == nil {
		t.Fatal("唯讀開法不改名")
	}
	garbage := []byte("this is definitely not a sqlite database file, not even close")
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnlyAt(path, time.Second); err == nil {
		t.Fatal("壞檔要回錯")
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, garbage) {
		t.Fatal("壞檔不刪、不動")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if s, err = OpenAt(path, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Hydrate(fixture(t)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	ro, err := OpenReadOnlyAt(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if c, err := ro.Dump(); err != nil || len(c.Playlists) != 2 {
		t.Fatalf("正常的 db 要能 Dump:%v", err)
	}
}

// Dump 必須是一致的快照:與另一個 capy 的 Hydrate 並行時不能讀到前半舊、後半新(capy export 與 cron 的 capy pl pull 同時跑正是它的用法)。
func TestDumpIsConsistentUnderConcurrentHydrate(t *testing.T) {
	pin(t)
	path := filepath.Join(t.TempDir(), "state.db")
	w, err := OpenAt(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r, err := OpenAt(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	mk := func(tag string, n int) Canonical {
		m := canon.NewManifest()
		m.Touch("dev"+tag, tag)
		tr := canon.NewTracks()
		pl := canon.NewPlaylist(tag)
		for i := 0; i < n; i++ {
			x := canon.NewTrack("spotify", provider.Track{ProviderID: fmt.Sprintf("%s%d", tag, i), Title: tag})
			tr.Tracks[x.CID] = x
			if _, err := pl.Append(x.CID); err != nil {
				t.Fatal(err)
			}
		}
		m.AddPlaylist(pl.PID)
		return Canonical{Manifest: m, Tracks: tr, Playlists: []canon.Playlist{*pl}, Devices: []canon.DeviceState{*canon.NewDeviceState("dev" + tag)}}
	}
	a, b := mk("A", 1), mk("B", 2)
	if err := w.Hydrate(a); err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			c := a
			if i%2 == 1 {
				c = b
			}
			if err := w.Hydrate(c); err != nil {
				done <- err
				return
			}
		}
	}()
	// 讀到夠多次才收工,不是固定牆鐘視窗:慢的 runner(CI 的 windows-latest)在 400 ms 內只跑得完兩次就會假性失敗。
	// 仍留一個上限,真的卡住時不會無限跑。
	deadline, hard, n := time.Now().Add(400*time.Millisecond), time.Now().Add(30*time.Second), 0
	for (time.Now().Before(deadline) || n < 3) && time.Now().Before(hard) {
		c, err := r.Dump()
		if err != nil {
			close(stop)
			t.Fatalf("Dump 讀到撕裂狀態:%v", err)
		}
		n++
		if len(c.Playlists) != 1 {
			close(stop)
			t.Fatalf("撕裂:%d 個清單", len(c.Playlists))
		}
		want, tag := 1, "A"
		if c.Playlists[0].Name == "B" {
			want, tag = 2, "B"
		}
		if len(c.Playlists[0].Items) != want || len(c.Tracks.Tracks) != want || len(c.Manifest.Devices) != 1 || c.Manifest.Devices[0].ID != "dev"+tag || len(c.Devices) != 1 || c.Devices[0].DeviceID != "dev"+tag {
			close(stop)
			t.Fatalf("撕裂:清單 %s 但 items=%d tracks=%d manifest=%v devices=%d", c.Playlists[0].Name, len(c.Playlists[0].Items), len(c.Tracks.Tracks), c.Manifest.Devices, len(c.Devices))
		}
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n < 3 {
		t.Fatalf("只讀了 %d 次,測不到並行", n)
	}
}

func TestV7InvariantsAndLegacyUpgradeWithoutRetire(t *testing.T) {
	if schemaVersion != 7 {
		t.Fatalf("schemaVersion 必須維持 7,目前為 %d", schemaVersion)
	}
	path := filepath.Join(t.TempDir(), "state.db")
	// 模擬尚未補上三張新表的舊 v7 資料庫
	legacy, err := open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.Exec(schema + "PRAGMA user_version = 7;"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Hydrate(fixture(t)); err != nil {
		t.Fatal(err)
	}
	var jm string
	if err := legacy.db.QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(jm, "delete") {
		t.Fatalf("journal_mode 必須維持 delete(禁開 WAL):%q", jm)
	}
	legacy.Close()

	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// OpenReadOnlyAt 遇缺表的舊 v7 庫:不改動檔案、查詢三張新表視同空結果回 nil, nil
	ro, err := OpenReadOnlyAt(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ro.LookupResolveNeg("c1", "spotify"); err != nil || ok {
		t.Errorf("舊 v7 唯讀查詢 resolve_neg_cache 應回空:%v %v", ok, err)
	}
	if hist, err := ro.LoadTUIHistory(); err != nil || hist != nil {
		t.Errorf("舊 v7 唯讀查詢 tui_history 應回 nil, nil:%v %v", hist, err)
	}
	if j, err := ro.ListPlayJournal(10); err != nil || j != nil {
		t.Errorf("舊 v7 唯讀查詢 play_journal 應回 nil, nil:%v %v", j, err)
	}
	ro.Close()
	if afterBytes, _ := os.ReadFile(path); !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("OpenReadOnlyAt 不得改動舊 v7 檔案任何位元組")
	}

	// OpenAt 開啟舊 v7 庫:不退役 .v7、既有 canonical 資料完好、自動補齊三張新表
	s, err := OpenAt(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(path + ".v7"); err == nil {
		t.Fatal("OpenAt 對既有 v7 庫不得觸發 retire 產生 .v7")
	}
	if c, err := s.Dump(); err != nil || len(c.Playlists) != 2 {
		t.Fatalf("既有 v7 庫的 canonical 資料應完好:%v %+v", err, c)
	}
	if err := s.AppendTUIHistory("now", time.Unix(100, 0)); err != nil {
		t.Fatalf("補表後應可正常寫入 tui_history:%v", err)
	}
}

func TestResolveNegCacheRoundTripAndExpiry(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "state.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	t0 := time.Unix(1_790_000_000, 0)
	old := ResolveNegEntry{
		CID: "c_old", Provider: "spotify", TrackSig: "sig1", CandID: "sp1",
		Score: 68, ReasonCode: "below_threshold", ReasonData: `{"score":68}`, CheckedAt: t0,
	}
	keep := ResolveNegEntry{
		CID: "c_keep", Provider: "apple", TrackSig: "sig2", CandID: "",
		Score: 0, ReasonCode: "no_candidate", ReasonData: "", CheckedAt: t0.Add(5 * 24 * time.Hour),
	}
	if err := s.SaveResolveNeg(old); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveResolveNeg(keep); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.LookupResolveNeg("c_old", "spotify")
	if err != nil || !ok || got != old {
		t.Fatalf("LookupResolveNeg round-trip:%+v %v %v", got, ok, err)
	}

	// 8 天後寫入新紀錄,順手清掉超過 7 天的 old,保留 3 天前的 keep
	fresh := ResolveNegEntry{
		CID: "c_new", Provider: "spotify", TrackSig: "sig3", CandID: "sp3",
		Score: 71, ReasonCode: "ambiguous", ReasonData: "{}", CheckedAt: t0.Add(8 * 24 * time.Hour),
	}
	if err := s.SaveResolveNeg(fresh); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LookupResolveNeg("c_old", "spotify"); err != nil || ok {
		t.Errorf("超過 7 天的負向快取應被順手清除:%v %v", ok, err)
	}
	if _, ok, err := s.LookupResolveNeg("c_keep", "apple"); err != nil || !ok {
		t.Errorf("未滿 7 天的負向快取應保留:%v %v", ok, err)
	}
	if err := s.DeleteResolveNeg("c_new", "spotify"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.LookupResolveNeg("c_new", "spotify"); ok {
		t.Error("DeleteResolveNeg 後不應再查到")
	}
}

func TestTUIHistoryDedupAndCap(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "state.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	t0 := time.Unix(1_790_000_000, 0)
	for _, cmd := range []string{"", "  ", "play a", "play a", "pause", "play a"} {
		if err := s.AppendTUIHistory(cmd, t0); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LoadTUIHistory()
	if err != nil || !slices.Equal(got, []string{"play a", "pause", "play a"}) {
		t.Fatalf("連續重複應去重、非連續應保留:%v %v", got, err)
	}

	// 灌超過 100 筆,只保留最新 100 筆(依舊→新順序)
	for i := 0; i < 120; i++ {
		if err := s.AppendTUIHistory(fmt.Sprintf("cmd-%03d", i), t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	got, err = s.LoadTUIHistory()
	if err != nil || len(got) != TUIHistoryMax || got[0] != "cmd-020" || got[99] != "cmd-119" {
		t.Fatalf("應保留最新 100 筆:len=%d first=%q last=%q err=%v", len(got), got[0], got[len(got)-1], err)
	}
}

func TestPlayJournalDedupCapAndBusyWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenAt(path, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	t0 := time.Unix(1_790_000_000, 0)
	// 10 分鐘內同 (provider, provider_id) 去重更新時間;超過 10 分鐘保留兩筆
	if err := s.RecordPlayJournal(PlayJournalEntry{
		PlayedAt: t0, Provider: "spotify", ProviderID: "sp1", ISRC: "TWA472400123",
		Title: "Song 1", Artists: []string{"Artist A"}, Album: "Album 1", DurationMS: 200000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlayJournal(PlayJournalEntry{
		PlayedAt: t0.Add(5 * time.Minute), Provider: "spotify", ProviderID: "sp1", ISRC: "TWA472400123",
		Title: "Song 1", Artists: []string{"Artist A"}, Album: "Album 1", DurationMS: 200000,
	}); err != nil {
		t.Fatal(err)
	}
	// Apple Music State 沒有 provider_id:不同歌曲在 10 分鐘內不得互刪,同首歌在 10 分鐘內會去重
	if err := s.RecordPlayJournal(PlayJournalEntry{
		PlayedAt: t0.Add(6 * time.Minute), Provider: "apple", Title: "Lost Stars", Artists: []string{"Adam Levine"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlayJournal(PlayJournalEntry{
		PlayedAt: t0.Add(7 * time.Minute), Provider: "apple", Title: "Sugar", Artists: []string{"Maroon 5"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlayJournal(PlayJournalEntry{
		PlayedAt: t0.Add(8 * time.Minute), Provider: "apple", Title: "Lost Stars", Artists: []string{"Adam Levine"},
	}); err != nil {
		t.Fatal(err)
	}
	// 15 分鐘後再聽 sp1:超過 10 分鐘視窗,應保留舊紀錄與新紀錄共 2 筆 sp1
	if err := s.RecordPlayJournal(PlayJournalEntry{
		PlayedAt: t0.Add(20 * time.Minute), Provider: "spotify", ProviderID: "sp1", Title: "Song 1", Artists: []string{"Artist A"},
	}); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListPlayJournal(0)
	if err != nil || len(list) != 4 {
		t.Fatalf("應為 4 筆(sp1 兩筆 + Apple 兩首各一筆):len=%d %+v err=%v", len(list), list, err)
	}
	if list[0].ProviderID != "sp1" || !list[0].PlayedAt.Equal(t0.Add(20*time.Minute)) || list[1].Title != "Lost Stars" || list[2].Title != "Sugar" {
		t.Errorf("排序或去重結果不符預期:%+v", list)
	}

	// 併發寫鎖下 RecordPlayJournal 的第一句是 DELETE,必須照 busy_timeout 等待而非回 SQLITE_BUSY
	other, err := OpenAt(path, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("DELETE FROM recent"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	time.AfterFunc(120*time.Millisecond, func() { done <- tx.Commit() })
	if err := other.RecordPlayJournal(PlayJournalEntry{
		PlayedAt: t0.Add(30 * time.Minute), Provider: "spotify", ProviderID: "sp-busy", Title: "Busy",
	}); err != nil {
		t.Fatalf("併發寫入應等待鎖釋放而非 SQLITE_BUSY:%v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// 上限 1000 筆裁剪
	for i := 0; i < 1010; i++ {
		if err := s.RecordPlayJournal(PlayJournalEntry{
			PlayedAt: t0.Add(time.Hour + time.Duration(i)*time.Second), Provider: "spotify",
			ProviderID: fmt.Sprintf("cap-%04d", i), Title: "Cap",
		}); err != nil {
			t.Fatal(err)
		}
	}
	capped, err := s.ListPlayJournal(0)
	if err != nil || len(capped) != PlayJournalMax || capped[0].ProviderID != "cap-1009" {
		t.Fatalf("應裁剪至 1000 筆:len=%d err=%v", len(capped), err)
	}
}

func TestAuxTablesIsolatedFromDumpAndClearedByForgetAndHistoryClear(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAPY_CONFIG_DIR", dir)
	path := filepath.Join(dir, "state.db")
	s, err := OpenAt(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	in := fixture(t)
	want := encodeAll(t, in)
	if err := s.Hydrate(in); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_790_000_000, 0)
	if err := s.SaveResolveNeg(ResolveNegEntry{CID: "c1", Provider: "spotify", ReasonData: "SECRET_SPOTIFY_NEG_TOKEN", CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveResolveNeg(ResolveNegEntry{CID: "c1", Provider: "apple", ReasonData: "KEEP_APPLE_NEG", CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTUIHistory("play secret-query", now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlayJournal(PlayJournalEntry{PlayedAt: now, Provider: "spotify", ProviderID: "sp1", Title: "SECRET_SPOTIFY_JOURNAL_SONG"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPlayJournal(PlayJournalEntry{PlayedAt: now, Provider: "apple", ProviderID: "ap1", Title: "KEEP_APPLE_SONG"}); err != nil {
		t.Fatal(err)
	}

	// 三張純本機表絕不進 Dump,Hydrate 也不會抹掉它們
	out, err := s.Dump()
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, want, encodeAll(t, out), "純本機表不得污染 Dump")
	if err := s.Hydrate(in); err != nil {
		t.Fatal(err)
	}
	if hist, _ := s.LoadTUIHistory(); len(hist) != 1 {
		t.Fatalf("Hydrate 不得清空純本機表:%v", hist)
	}

	// 建立一份退役檔 .v6 驗證 ForgetProviderInRetired 同步清除新表
	retiredPath := path + ".v6"
	rawCopy, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retiredPath, rawCopy, 0o600); err != nil {
		t.Fatal(err)
	}

	// ForgetProvider("spotify") 搭配 secure_delete 徹底抹除該 provider 在 resolve_neg_cache 與 play_journal 的列
	if err := s.ForgetProvider("spotify"); err != nil {
		t.Fatal(err)
	}
	if err := ForgetProviderInRetired("spotify", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.LookupResolveNeg("c1", "spotify"); ok {
		t.Error("ForgetProvider 應刪除 spotify 的 resolve_neg_cache")
	}
	if _, ok, _ := s.LookupResolveNeg("c1", "apple"); !ok {
		t.Error("ForgetProvider 不應刪除其他 provider 的 resolve_neg_cache")
	}
	if j, _ := s.ListPlayJournal(0); len(j) != 1 || j[0].Provider != "apple" {
		t.Errorf("ForgetProvider 應只刪除 spotify 的 play_journal:%+v", j)
	}
	for _, f := range []string{path, retiredPath} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("SECRET_SPOTIFY_NEG_TOKEN")) || bytes.Contains(b, []byte("SECRET_SPOTIFY_JOURNAL_SONG")) {
			t.Errorf("%s 應以 secure_delete 覆寫抹除已刪除的字串", f)
		}
	}

	// 真實 capy history clear 路徑(ApplyCache ClearRecent: true)計入新表筆數並以 secure_delete 覆寫抹除磁碟頁
	cleared, err := s.ApplyCache(CacheDelta{ClearRecent: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleared != 2 {
		t.Errorf("ApplyCache(ClearRecent) 應回報刪除 2 筆(tui_history 1 + play_journal 1),得 %d", cleared)
	}
	if hist, _ := s.LoadTUIHistory(); len(hist) != 0 {
		t.Errorf("ApplyCache(ClearRecent) 後 tui_history 應為空:%v", hist)
	}
	if j, _ := s.ListPlayJournal(0); len(j) != 0 {
		t.Errorf("ApplyCache(ClearRecent) 後 play_journal 應為空:%+v", j)
	}
	afterClear, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(afterClear, []byte("play secret-query")) || bytes.Contains(afterClear, []byte("KEEP_APPLE_SONG")) {
		t.Errorf("%s 在 ApplyCache(ClearRecent) 後應以 secure_delete 覆寫抹除 tui_history 與 play_journal 字串", path)
	}
}
