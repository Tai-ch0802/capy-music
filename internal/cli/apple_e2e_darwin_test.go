//go:build darwin

package cli

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	appleprov "github.com/Tai-ch0802/capy-music/internal/provider/apple"
)

func TestApplePlayByIDOnDarwin(t *testing.T) {
	// 機制 A(預設 AppleScript)是本測試斷言的對象;若跑測試的環境剛好設了
	// CAPY_APPLE_PLAY_MECHANISM=open(如附錄 C-4 手動驗收後忘記 unset),
	// Play 會改呼叫 runOpen 而非 runOSA,scripts 留空、斷言失敗——
	// 明確歸零,與 internal/provider/apple/player_darwin_test.go 的既有慣例一致。
	t.Setenv("CAPY_APPLE_PLAY_MECHANISM", "")
	swapApple(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog/tw/songs/111" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprintf(w, `{"data":[%s]}`, fmt.Sprintf(appleSongFx, "111", "111"))
	})
	scripts := appleprov.StubOSAForTest(t) // 見 Step 3:export_test 風格的測試鉤子
	out, err := runCLI(t, "play", "--id", "111", "--provider", "apple")
	if err != nil {
		t.Fatal(err)
	}
	if len(*scripts) != 1 || !strings.Contains((*scripts)[0], "music://music.apple.com") {
		t.Errorf("應以 music:// 交給 Music.app:%v", *scripts)
	}
	if !strings.Contains(out, "▶") {
		t.Errorf("輸出:%q", out)
	}
}
