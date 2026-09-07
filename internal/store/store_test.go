package store

import (
	"bytes"
	"fmt"
	"path/filepath"
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
	devA := canon.NewDeviceState("devA")
	devA.SetBase(pl.PID, "spotify", canon.Snapshot{Name: "通勤", Items: []string{"6rq", "x", "6rq"}})
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

func TestMismatchedSchemaVersionIsDropped(t *testing.T) {
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
	if s, err = OpenAt(path, time.Second); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out, err := s.Dump()
	if err != nil || len(out.Tracks.Tracks) != 0 {
		t.Fatalf("版本不符應整檔丟棄重建(不寫 ALTER):%v %+v", err, out.Tracks)
	}
	var v int
	if s.db.QueryRow("PRAGMA user_version").Scan(&v); v != schemaVersion {
		t.Fatalf("重建後 user_version 應為 %d,得 %d", schemaVersion, v)
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
