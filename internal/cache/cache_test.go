package cache

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func setDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("CAPY_CONFIG_DIR", d)
	return d
}

func TestRoundTrip(t *testing.T) {
	setDir(t)
	c := Load()
	c.SetPlaylists("spotify", []Playlist{{ID: "p1", Name: "通勤", Total: 23}})
	c.AddRecent(Recent{Provider: "spotify", Type: TypeArtist, ID: "a1", Label: "五月天", Detail: "熱門歌曲"})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.Playlists["spotify"][0].Name != "通勤" || len(got.Recent) != 1 || got.Recent[0].Label != "五月天" || got.Recent[0].At == 0 {
		t.Fatalf("round-trip 失敗:%+v", got)
	}
}

func TestAddRecentDedupesNewestFirstAndCaps(t *testing.T) {
	setDir(t)
	c := Load()
	now := int64(1000)
	Now = func() time.Time { now++; return time.Unix(now, 0) }
	t.Cleanup(func() { Now = time.Now })
	for i := 0; i < MaxRecent+10; i++ {
		c.AddRecent(Recent{Provider: "spotify", Type: TypeTrack, ID: strconv.Itoa(i), Label: strconv.Itoa(i)})
	}
	if len(c.Recent) != MaxRecent || c.Recent[0].ID != strconv.Itoa(MaxRecent+9) || c.Recent[MaxRecent-1].ID != "10" {
		t.Fatalf("應保留最新 %d 筆、最新在前:首 %s 末 %s", MaxRecent, c.Recent[0].ID, c.Recent[len(c.Recent)-1].ID)
	}
	c.AddRecent(Recent{Provider: "spotify", Type: TypeTrack, ID: "20", Label: "again"})
	n := 0
	for _, r := range c.Recent {
		if r.ID == "20" {
			n++
		}
	}
	if n != 1 || c.Recent[0].ID != "20" || c.Recent[0].Label != "again" || len(c.Recent) != MaxRecent {
		t.Fatalf("重複項應只留最新一筆並移到最前:%+v", c.Recent[:3])
	}
	c.AddRecent(Recent{Provider: "apple", Type: TypeTrack, ID: "20"}) // 不同 provider 不算重複
	if len(c.Recent) != MaxRecent || c.Recent[1].ID != "20" || c.Recent[1].Provider != "spotify" {
		t.Fatal("同 id 不同 provider 應各自保留")
	}
}

func TestUnusableDBIsEmptyAndLegacyFileRemoved(t *testing.T) {
	d := setDir(t)
	legacy := filepath.Join(d, legacyFile)
	if err := os.WriteFile(legacy, []byte(`{"schema_version":1,"recent":[{"id":"x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Load()
	if len(c.Recent) != 0 || c.Playlists == nil {
		t.Fatalf("舊 cache.json 不搬內容,Load 應為空:%+v", c)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("舊 cache.json 應在首次 Load 被刪")
	}
	// db 路徑被一個檔案佔住(config dir 是檔案)→ 開不了 → 一樣回空,不報錯、不 panic
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPY_CONFIG_DIR", f)
	if c := Load(); len(c.Recent) != 0 || c.Playlists == nil {
		t.Fatalf("db 開不了應視同空快取:%+v", c)
	}
	if err := (&Cache{Playlists: map[string][]Playlist{}}).Save(); err == nil {
		t.Fatal("Save 在 db 開不了時要回錯(呼叫端自己決定要不要靜默)")
	}
}

func TestRecentOrderIsPositionNotTimestamp(t *testing.T) {
	setDir(t)
	c := Load()
	for _, r := range []Recent{{At: 3, ID: "a"}, {At: 1, ID: "b"}, {At: 2, ID: "c"}} { // at 與加入順序故意不同
		r.Provider, r.Type, r.Label = "spotify", TypeTrack, r.ID
		c.AddRecent(r)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if len(got.Recent) != 3 || got.Recent[0].ID != "c" || got.Recent[1].ID != "b" || got.Recent[2].ID != "a" {
		t.Fatalf("順序要靠 position 保住(最新加入在前),不能靠 at 排:%+v", got.Recent)
	}
}

func TestClearRecentKeepsPlaylists(t *testing.T) {
	setDir(t)
	c := Load()
	c.SetPlaylists("apple", []Playlist{{ID: "p.1", Name: "x"}})
	c.AddRecent(Recent{Provider: "apple", Type: TypeQuery, ID: "q", Label: "q"})
	c.ClearRecent()
	if len(c.Recent) != 0 || len(c.Playlists["apple"]) != 1 {
		t.Fatalf("clear 只清 recent:%+v", c)
	}
}
