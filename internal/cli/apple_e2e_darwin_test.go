//go:build darwin

package cli

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	appleprov "github.com/Tai-ch0802/capy-music/internal/provider/apple"
)

// TestApplePlayByIDDoesNotClaimPlaying:【fails-before-fix】資料庫裡沒有這首(替身的 osascript 一律回空):用單曲網址在 Music.app
// 打開並標出那一首,exit 0、不印 ▶,照實說用平台查到的歌名。以前走 open location,在 macOS 26 連頁面都不換,capy 還印 ▶。
// StubOSAForTest 連 runOpen 一起換掉:不會真的打開 Music.app。
func TestApplePlayByIDDoesNotClaimPlaying(t *testing.T) {
	swapApple(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog/tw/songs/111" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprintf(w, `{"data":[%s]}`, fmt.Sprintf(appleSongFx, "111", "111"))
	})
	calls := appleprov.StubOSAForTest(t)
	out, err := runCLI(t, "play", "--id", "111", "--provider", "apple")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(*calls, "open music://music.apple.com/tw/song/111") {
		t.Errorf("要用單曲網址打開:%v", *calls)
	}
	if strings.Contains(out, "▶") || !strings.Contains(out, "「派對動物 — 五月天」") || !strings.Contains(out, "點兩下") {
		t.Errorf("只打開了:不印 ▶、照實說:%q", out)
	}
}
