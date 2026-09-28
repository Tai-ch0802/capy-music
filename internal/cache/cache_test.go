package cache

import (
	"os"
	"path/filepath"
	"reflect"
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
	if err := (&Cache{Playlists: map[string][]Playlist{}}).Save(); err != nil {
		t.Fatalf("Load 沒成功的 Cache,Save 應是 no-op:%v", err)
	}
}

func TestSaveAfterFailedLoadDoesNotWipe(t *testing.T) {
	setDir(t)
	c := Load()
	c.SetPlaylists("spotify", []Playlist{{ID: "p1", Name: "通勤"}})
	c.AddRecent(Recent{Provider: "spotify", Type: TypeQuery, ID: "q", Label: "q"})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	stale := &Cache{Playlists: map[string][]Playlist{}} // 模擬 Load 失敗回來的空快取
	stale.AddRecent(Recent{Provider: "spotify", Type: TypeQuery, ID: "x", Label: "x"})
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}
	got := Load() // Save 只寫這次的改動:自己加的那一筆寫進去,既有的一筆都不少
	if len(got.Playlists["spotify"]) != 1 || len(got.Recent) != 2 || got.Recent[0].ID != "x" || got.Recent[1].ID != "q" {
		t.Fatalf("讀失敗後的 Save 不可把既有快取蓋成空的:%+v", got)
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

// 以下四個:兩個 capy 同時讀寫快取(TUI 開出來的子程式、capy --web、另一個終端機、cron)。每一個 Load / Save 各開各關,
// 所以依序交錯就等於兩個行程——不用 goroutine,結果是決定性的(計畫 2026-09-24 §1.7 S5 留下的待辦)。

// TestConcurrentSavesKeepBothWrites:兩個行程都讀了、各加一筆、先後存:兩筆都要在(以前後存的那個整批蓋掉前一個)。
func TestConcurrentSavesKeepBothWrites(t *testing.T) {
	setDir(t)
	a, b := Load(), Load()
	b.AddRecent(Recent{Provider: "spotify", Type: TypeQuery, ID: "from-web", Label: "from-web"})
	b.SetPlaylists("apple", []Playlist{{ID: "p.1", Name: "冬日暖調"}})
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	a.AddRecent(Recent{Provider: "spotify", Type: TypeQuery, ID: "from-term", Label: "from-term"})
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if len(got.Recent) != 2 || got.Recent[0].ID != "from-term" || got.Recent[1].ID != "from-web" || len(got.Playlists["apple"]) != 1 {
		t.Fatalf("兩個行程的寫入都要在、後存的在前:%+v %+v", got.Recent, got.Playlists)
	}
}

// TestStaleSaveDoesNotResurrectForgottenProvider:登出前讀的快取在登出後存(存的只是一筆 Apple 的),Spotify 的列不可以回來
// ——Spotify Developer Policy 要求中斷連線時刪掉使用者的資料。
func TestStaleSaveDoesNotResurrectForgottenProvider(t *testing.T) {
	setDir(t)
	c := Load()
	c.SetPlaylists("spotify", []Playlist{{ID: "p1", Name: "通勤"}})
	c.AddRecent(Recent{Provider: "spotify", Type: TypeQuery, ID: "q", Label: "q"})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	stale := Load() // 例如 capy play --provider apple 在 auth logout spotify 之前讀的
	if err := Forget("spotify"); err != nil {
		t.Fatal(err)
	}
	stale.AddRecent(Recent{Provider: "apple", Type: TypeTrack, ID: "a1", Label: "a1"})
	if err := stale.Save(); err != nil {
		t.Fatal(err)
	}
	got := Load()
	for _, r := range got.Recent {
		if r.Provider == "spotify" {
			t.Errorf("登出的平台的最近項目回來了:%+v", r)
		}
	}
	if len(got.Playlists["spotify"]) != 0 || len(got.Recent) != 1 {
		t.Errorf("登出的平台的清單回來了、或 Apple 那一筆沒寫進去:%+v %+v", got.Playlists, got.Recent)
	}
}

// TestHistoryClearNotUndoneByAnotherSave:history clear 清掉之後,另一個行程(先前讀的)存了清單列表,清掉的不可以回來。
func TestHistoryClearNotUndoneByAnotherSave(t *testing.T) {
	setDir(t)
	c := Load()
	c.AddRecent(Recent{Provider: "spotify", Type: TypeQuery, ID: "secret", Label: "secret"})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	pl, h := Load(), Load()
	h.ClearRecent()
	if err := h.Save(); err != nil {
		t.Fatal(err)
	}
	pl.SetPlaylists("spotify", []Playlist{{ID: "p1", Name: "x"}})
	if err := pl.Save(); err != nil {
		t.Fatal(err)
	}
	if got := Load(); len(got.Recent) != 0 || len(got.Playlists["spotify"]) != 1 {
		t.Fatalf("清掉的最近項目不可以被寫回來:%+v", got.Recent)
	}
}

// TestPlaylistSaveKeepsOthersRecent:只存清單列表的那一次(pl list)不可以把別人剛加的最近項目蓋掉。
func TestPlaylistSaveKeepsOthersRecent(t *testing.T) {
	setDir(t)
	pl, se := Load(), Load()
	se.AddRecent(Recent{Provider: "apple", Type: TypeQuery, ID: "q", Label: "q"})
	if err := se.Save(); err != nil {
		t.Fatal(err)
	}
	pl.SetPlaylists("spotify", []Playlist{{ID: "p1", Name: "x"}})
	if err := pl.Save(); err != nil {
		t.Fatal(err)
	}
	if got := Load(); len(got.Recent) != 1 {
		t.Fatalf("pl list 不可以蓋掉最近項目:%+v", got.Recent)
	}
}

// TestClearThenAddKeepsLaterAdds:清空之前加的一起作廢,清空之後加的照樣寫。
func TestClearThenAddKeepsLaterAdds(t *testing.T) {
	setDir(t)
	c := Load()
	c.AddRecent(Recent{Provider: "spotify", Type: TypeQuery, ID: "a", Label: "a"})
	c.ClearRecent()
	c.AddRecent(Recent{Provider: "spotify", Type: TypeQuery, ID: "b", Label: "b"})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if got := Load(); len(got.Recent) != 1 || got.Recent[0].ID != "b" {
		t.Fatalf("只剩清空之後加的:%+v", got.Recent)
	}
}

// TestSaveTwiceDoesNotRedoChanges:存過就把改動清掉:同一個 Cache 再存一次,不會再清一次別人在這之間加的最近項目。
func TestSaveTwiceDoesNotRedoChanges(t *testing.T) {
	setDir(t)
	h := Load()
	h.ClearRecent()
	if err := h.Save(); err != nil {
		t.Fatal(err)
	}
	other := Load()
	other.AddRecent(Recent{Provider: "apple", Type: TypeQuery, ID: "x", Label: "x"})
	if err := other.Save(); err != nil {
		t.Fatal(err)
	}
	if err := h.Save(); err != nil {
		t.Fatal(err)
	}
	if got := Load(); len(got.Recent) != 1 {
		t.Fatalf("再存一次不可以重做清空:%+v", got.Recent)
	}
}

// TestSetEmptyPlaylistsClearsThatProvider:pl list 回來是空的:那一家的清單清空,別家不動。
func TestSetEmptyPlaylistsClearsThatProvider(t *testing.T) {
	setDir(t)
	c := Load()
	c.SetPlaylists("spotify", []Playlist{{ID: "p1", Name: "x"}})
	c.SetPlaylists("apple", []Playlist{{ID: "p.1", Name: "y"}})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c = Load()
	c.SetPlaylists("spotify", nil)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if got := Load(); len(got.Playlists["spotify"]) != 0 || len(got.Playlists["apple"]) != 1 {
		t.Fatalf("空的清單只清那一家:%+v", got.Playlists)
	}
}

// TestRecentInMemoryMatchesWhatIsSaved:去重、最新在前、上限 50 的規則在記憶體(c.Recent)與寫進 db 的 SQL 各有一份,
// 同一串操作兩邊要一模一樣(沒有別的行程同時寫的時候)。
func TestRecentInMemoryMatchesWhatIsSaved(t *testing.T) {
	setDir(t)
	c := Load()
	for i := range MaxRecent + 10 {
		c.AddRecent(Recent{Provider: "spotify", Type: TypeTrack, ID: strconv.Itoa(i % (MaxRecent + 7)), Label: strconv.Itoa(i), At: int64(i + 1)}) // 超過上限的不同 id,也有重複
	}
	c.AddRecent(Recent{Provider: "apple", Type: TypeTrack, ID: "3", Label: "apple", At: 999})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if got := Load(); !reflect.DeepEqual(got.Recent, c.Recent) {
		t.Fatalf("記憶體與 db 不一致:\n mem %+v\n  db %+v", c.Recent, got.Recent)
	}
}

// TestHistoryClearAfterFailedLoad:Load 失敗(另一個 capy 拿著鎖太久)回來的是空的,history clear 照樣要清,
// 而且說真的刪掉幾筆——不能拿讀到的那一份(空的)算成 0、什麼都沒清。
func TestHistoryClearAfterFailedLoad(t *testing.T) {
	setDir(t)
	c := Load()
	c.AddRecent(Recent{Provider: "spotify", Type: TypeQuery, ID: "a", Label: "a"})
	c.AddRecent(Recent{Provider: "spotify", Type: TypeQuery, ID: "b", Label: "b"})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	failed := &Cache{Playlists: map[string][]Playlist{}}
	failed.ClearRecent()
	if err := failed.Save(); err != nil {
		t.Fatal(err)
	}
	if got := Load(); len(got.Recent) != 0 || failed.Cleared() != 2 {
		t.Fatalf("要清掉、並說刪了 2 筆:剩 %d 筆,說 %d", len(got.Recent), failed.Cleared())
	}
}
