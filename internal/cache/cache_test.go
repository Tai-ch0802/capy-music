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

func TestBrokenOrMismatchedFileIsEmpty(t *testing.T) {
	d := setDir(t)
	for _, body := range []string{"{not json", `{"schema_version":99,"recent":[{"id":"x"}]}`} {
		if err := os.WriteFile(filepath.Join(d, fileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		c := Load()
		if len(c.Recent) != 0 || c.Playlists == nil || c.SchemaVersion != schemaVersion {
			t.Fatalf("%q 應視同空快取:%+v", body, c)
		}
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
