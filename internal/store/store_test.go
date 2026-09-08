package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	x.Observe("apple", provider.Track{ProviderID: "i.abc", ISRC: "TWA472400123", Title: "派對動物 (Live)", DurationMS: 252000})
	tr.Tracks[x.CID] = x
	up := canon.NewTrack("apple", provider.Track{ProviderID: "i.lib1", Title: "上傳曲"})
	tr.Tracks[up.CID] = up
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
		{"mappings", "INSERT INTO mappings (cid, provider, provider_id) VALUES ('ghost', 'spotify', 'x')"},
		{"playlist_items", "INSERT INTO playlist_items (pid, iid, cid, rank, added_at) VALUES ('ghost', 'i', 'c', 'V', 0)"},
		{"playlist_links", "INSERT INTO playlist_links (pid, provider, provider_id) VALUES ('ghost', 'spotify', 'x')"},
		{"device_base", "INSERT INTO device_base (device_id, pid, provider, playlist_id, name, items, cids, observed_at) VALUES ('ghost', 'p', 'spotify', '', '', '[]', '[]', 0)"},
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
	rec := []Recent{{At: 1, Provider: "spotify", Type: "track", ID: "b"}, {At: 9, Provider: "spotify", Type: "track", ID: "a", Detail: "d"}} // at 與 position 反向
	if err := s.SaveCache(pls, rec); err != nil {
		t.Fatal(err)
	}
	gotPls, gotRec, err := s.LoadCache()
	if err != nil || gotPls["spotify"][0].ID != "p2" || gotPls["spotify"][1].ID != "p1" || len(gotRec) != 2 || gotRec[0].ID != "b" || gotRec[1].Detail != "d" {
		t.Fatalf("順序要照存入的 position:%v %v %v", err, gotPls, gotRec)
	}
	if err := s.SaveCache(nil, nil); err != nil {
		t.Fatal(err)
	}
	if gotPls, gotRec, _ = s.LoadCache(); len(gotPls) != 0 || gotRec != nil {
		t.Fatalf("SaveCache 是取代:%v %v", gotPls, gotRec)
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
	deadline, n := time.Now().Add(400*time.Millisecond), 0
	for time.Now().Before(deadline) {
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
