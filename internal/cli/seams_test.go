package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// TestValidatorsMatchHuhClosures:P7 T1 把 huh 表單的 Validate 閉包抽成具名函式(web 的表單提示橋共用);
// 錯誤訊息要與原閉包一字不差、TrimSpace 行為不變。
func TestValidatorsMatchHuhClosures(t *testing.T) {
	const bad = "Client ID 應為 32 位小寫十六進位字串(從 dashboard 複製)"
	if err := validateSpotifyClientID(" 0123456789abcdef0123456789abcdef \n"); err != nil {
		t.Errorf("前後空白要剝掉(原閉包 TrimSpace):%v", err)
	}
	for _, s := range []string{"", "xyz", "0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef0123456789abcde"} {
		if err := validateSpotifyClientID(s); err == nil || err.Error() != bad {
			t.Errorf("%q:訊息要與原閉包一字不差,得到 %v", s, err)
		}
	}

	exp := time.Now().Add(-time.Hour)
	want := fmt.Sprintf("已於 %s 過期,請重新複製", exp.Format(time.RFC3339))
	if err := validateAppleDevToken(fakeJWT(t, exp)); err == nil || err.Error() != want {
		t.Errorf("過期 JWT:want %q,got %v", want, err)
	}
	if err := validateAppleDevToken(fakeJWT(t, time.Now().Add(time.Hour))); err != nil {
		t.Errorf("未過期的 JWT 要過:%v", err)
	}
	if err := validateAppleDevToken("not-a-jwt"); err == nil {
		t.Error("壞 token 要回 JWTExp 的錯")
	}
}

// TestBothTTYAndClientIDWizardAreSeams:【fails-before-fix】bothTTY 與 runClientIDWizard 在 T1 前是普通 func,
// 覆寫不會編譯。覆寫後呼叫路徑要走替身:pl pull 在 buffer stdout(stdoutIsTTY=false)下 bothTTY=true →
// 走 confirmWrite 而不是直接 PendingError;auth login spotify 無 client id 在 stdinIsTTY=true 下走替身精靈。
func TestBothTTYAndClientIDWizardAreSeams(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1", "t2")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	before := driveFiles(t, dc)

	origBoth, origConfirm := bothTTY, confirmWrite
	bothTTY = func(*cobra.Command) bool { return true }
	asked := ""
	confirmWrite = func(_, prompt string) (bool, error) { asked = prompt; return false, nil }
	t.Cleanup(func() { bothTTY, confirmWrite = origBoth, origConfirm })

	// review / migrate 兩個閘委派到 bothTTY(review #57):覆寫一處就全對齊,不是複製函式值。
	probe := newRootCmd()
	probe.SetOut(&bytes.Buffer{})
	if !reviewIsTTY(probe) || !migrateIsTTY(probe) {
		t.Fatal("reviewIsTTY / migrateIsTTY 要委派到 bothTTY,不是複製函式值")
	}

	_, _, err := runPull(t, "pl", "pull", "通勤")
	if exitOf(t, err) != 2 || !strings.Contains(asked, "套用以上 2 筆") {
		t.Fatalf("bothTTY 替身為 true 時要走 confirmWrite(取消 → exit 2):err=%v asked=%q", err, asked)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("取消不可寫 Drive")
	}
	confirmWrite = func(string, string) (bool, error) { return true, nil }
	if _, _, err := runPull(t, "pl", "pull", "通勤"); err != nil {
		t.Fatalf("確認後要套用:%v", err)
	}
	if sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("確認後 Drive 要有變更")
	}

	origTTY, origWizard := stdinIsTTY, runClientIDWizard
	stdinIsTTY = func() bool { return true }
	called := false
	runClientIDWizard = func() (string, error) { called = true; return "", errors.New("替身精靈") }
	t.Cleanup(func() { stdinIsTTY, runClientIDWizard = origTTY, origWizard })
	if _, err := runCLI(t, "auth", "login", "spotify"); err == nil || !strings.Contains(err.Error(), "替身精靈") || !called {
		t.Fatalf("stdinIsTTY=true 且無 client id 要呼叫 runClientIDWizard 替身:called=%v err=%v", called, err)
	}
}
