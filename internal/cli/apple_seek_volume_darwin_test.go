//go:build darwin

package cli

import (
	"net/http"
	"testing"

	appleprov "github.com/Tai-ch0802/capy-music/internal/provider/apple"
)

// Music.app 的 player position 是秒、sound volume 是 0-100;位置取整到秒(mm:ss 本來就是秒精度)。
func TestAppleSeekAndVolumeScripts(t *testing.T) {
	swapApple(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	scripts := appleprov.StubOSAForTest(t)
	if _, err := runCLI(t, "seek", "1:23", "--provider", "apple"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "vol", "40", "--provider", "apple"); err != nil {
		t.Fatal(err)
	}
	if len(*scripts) != 2 {
		t.Fatalf("兩個 osascript:%v", *scripts)
	}
	// 整行比對:用 Contains 的話 "…to 83000"(忘了把毫秒換算成秒)也會通過
	if (*scripts)[0] != `tell application "Music" to set player position to 83` {
		t.Errorf("seek 腳本:%q", (*scripts)[0])
	}
	if (*scripts)[1] != `tell application "Music" to set sound volume to 40` {
		t.Errorf("vol 腳本:%q", (*scripts)[1])
	}
}
