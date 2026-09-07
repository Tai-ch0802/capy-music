package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// forbidProvider:補全期間任何 provider 建構都是違規(會取鎖、讀 keychain、打網路)。
func forbidProvider(t *testing.T) {
	t.Helper()
	orig := newProvider
	newProvider = func(context.Context, string) (provider.Provider, error) {
		t.Fatal("補全不得建構 provider")
		return nil, nil
	}
	t.Cleanup(func() { newProvider = orig })
}

func TestPlayCompletionReadsCacheOnly(t *testing.T) {
	setCLITestConfig(t)
	forbidProvider(t)
	c := cache.Load()
	c.SetPlaylists("spotify", []cache.Playlist{{ID: "p1", Name: "通勤"}, {ID: "p2", Name: "Chill"}})
	c.SetPlaylists("apple", []cache.Playlist{{ID: "p.1", Name: "Apple 清單"}})
	c.AddRecent(cache.Recent{Provider: "spotify", Type: cache.TypeArtist, ID: "a1", Label: "五月天"})
	c.AddRecent(cache.Recent{Provider: "spotify", Type: cache.TypeQuery, ID: "通勤", Label: "通勤"}) // 與清單同名 → 去重
	_ = c.Save()

	out, err := runCLI(t, "__complete", "play", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"通勤\t播放清單", "Chill\t播放清單", "五月天\t最近:藝人", ":4"} { // :4 = NoFileComp
		if !strings.Contains(out, want) {
			t.Errorf("補全缺 %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "通勤") != 1 || strings.Contains(out, "Apple 清單") {
		t.Errorf("應去重且只列目前 provider 的清單:\n%s", out)
	}
	out, _ = runCLI(t, "__complete", "play", "--provider", "apple", "")
	if !strings.Contains(out, "Apple 清單") || strings.Contains(out, "Chill") {
		t.Errorf("--provider apple 應列 Apple 的清單:\n%s", out)
	}
	out, _ = runCLI(t, "__complete", "play", "ch")
	if !strings.Contains(out, "Chill") || strings.Contains(out, "通勤") {
		t.Errorf("前綴過濾(不分大小寫):\n%s", out)
	}
	out, _ = runCLI(t, "__complete", "pl", "show", "")
	if !strings.Contains(out, "通勤\t播放清單") || strings.Contains(out, "五月天") {
		t.Errorf("pl show 只補清單名:\n%s", out)
	}
	out, _ = runCLI(t, "__complete", "play", "--type", "")
	if !strings.Contains(out, "artist") || !strings.Contains(out, "playlist") {
		t.Errorf("--type 應補合法值:\n%s", out)
	}
}

func TestCompletionEmptyCacheIsQuiet(t *testing.T) {
	setCLITestConfig(t)
	forbidProvider(t)
	out, err := runCLI(t, "__complete", "play", "")
	if err != nil || !strings.HasPrefix(out, ":4") {
		t.Fatalf("空快取應回空候選與 NoFileComp:%v %q", err, out)
	}
}
