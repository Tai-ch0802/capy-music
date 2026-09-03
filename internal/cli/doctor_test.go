package cli

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

func TestRunChecksRendersAndCounts(t *testing.T) {
	checks := []check{
		{name: "會過的", fn: func(ctx context.Context) (string, error) { return "ok 細節", nil }},
		{name: "會爆的", fn: func(ctx context.Context) (string, error) { return "", errors.New("爆了 — 這樣修") }},
	}
	buf := &strings.Builder{}
	failed := runChecks(context.Background(), buf, checks)
	out := buf.String()
	if failed != 1 {
		t.Errorf("failed = %d", failed)
	}
	if !strings.Contains(out, "✅ 會過的") || !strings.Contains(out, "ok 細節") {
		t.Errorf("成功列渲染錯誤:%q", out)
	}
	if !strings.Contains(out, "❌ 會爆的") || !strings.Contains(out, "這樣修") {
		t.Errorf("失敗列應含修復指示:%q", out)
	}
}

func TestDoctorExitNonZeroOnFailure(t *testing.T) {
	setCLITestConfig(t) // 空 config → 檢查①必敗
	// 寬容 handler:checkAPI 的 Health 會打 /me/player/devices,回空清單即可
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"devices":[]}`))
	})
	if _, err := runCLI(t, "doctor"); err == nil {
		t.Fatal("有檢查失敗時 doctor 應回錯誤(exit 非零)")
	}
}

func TestCheckConfigClientID(t *testing.T) {
	setCLITestConfig(t)
	if _, err := checkConfig(context.Background()); err == nil {
		t.Error("未設定 client ID 應失敗")
	}
	_ = config.Save(&config.Config{SpotifyClientID: "0123456789abcdef0123456789abcdef"})
	if detail, err := checkConfig(context.Background()); err != nil || detail == "" {
		t.Errorf("(%q, %v)", detail, err)
	}
}

func TestCheckKeychainRoundTrip(t *testing.T) {
	if _, err := checkKeychain(context.Background()); err != nil { // MockInit 下必過
		t.Errorf("keychain round-trip:%v", err)
	}
}

func TestPortHintByOS(t *testing.T) {
	if got := portHint("windows"); got != "netstat -ano | findstr :8888" {
		t.Errorf("windows portHint = %q", got)
	}
	if got := portHint("darwin"); got != "lsof -i :8888" {
		t.Errorf("darwin portHint = %q", got)
	}
}

func TestDoctorAppleChecksWithoutLogin(t *testing.T) {
	setupAppleTokens(t)
	_ = secret.Delete(apple.KeyMusicUserToken)
	// Stub checkOSA to avoid real osascript execution
	origCheckOSA := checkOSA
	t.Cleanup(func() { checkOSA = origCheckOSA })
	checkOSA = func(context.Context) (string, error) { return "stub", nil }

	buf := &strings.Builder{}
	failed := runChecks(context.Background(), buf, appleChecks())
	out := buf.String()
	if !strings.Contains(out, "✅ Apple developer token") || !strings.Contains(out, "有效至") {
		t.Errorf("應取得 dev token 並顯示有效期限:%q", out)
	}
	if !strings.Contains(out, "❌ Apple user token") || !strings.Contains(out, "capy auth login apple") {
		t.Errorf("無 MUT 應失敗並指示 login:%q", out)
	}
	if failed == 0 {
		t.Error("應有失敗項")
	}
}

func TestDoctorProviderFlagSelectsAppleSet(t *testing.T) {
	setupAppleTokens(t)
	_ = secret.Delete(apple.KeyMusicUserToken)
	// Stub checkOSA to avoid real osascript execution
	origCheckOSA := checkOSA
	t.Cleanup(func() { checkOSA = origCheckOSA })
	checkOSA = func(context.Context) (string, error) { return "stub", nil }

	_, err := runCLI(t, "doctor", "--provider", "apple")
	if err == nil || !strings.Contains(err.Error(), "檢查未通過") {
		t.Fatalf("未登入 apple 的 doctor 應回錯:%v", err)
	}
}

// TestDoctorAppleChecksKeychainErrorNotReportedAsMissing:keychain 存取失敗(非 ErrNotFound,例如
// 使用者拒絕授權或 keychain 已鎖定)不該被誤報成「沒有 token」——那會讓人誤以為要重新 login,
// 但實際上重新 login 也會卡在同一個 keychain 錯誤(review item 3)。
func TestDoctorAppleChecksKeychainErrorNotReportedAsMissing(t *testing.T) {
	setCLITestConfig(t)
	keyring.MockInitWithError(errors.New("user canceled"))
	t.Cleanup(func() { keyring.MockInit() })
	origCheckOSA := checkOSA
	t.Cleanup(func() { checkOSA = origCheckOSA })
	checkOSA = func(context.Context) (string, error) { return "stub", nil }

	buf := &strings.Builder{}
	runChecks(context.Background(), buf, appleChecks())
	out := buf.String()
	if !strings.Contains(out, "❌ Apple developer token") || !strings.Contains(out, "讀取 keychain 失敗") {
		t.Errorf("developer token 應回讀取失敗,得到 %q", out)
	}
	if !strings.Contains(out, "❌ Apple user token") || !strings.Contains(out, "讀取 keychain 失敗") {
		t.Errorf("user token 應回讀取失敗,得到 %q", out)
	}
	if strings.Contains(out, "沒有") {
		t.Errorf("keychain 讀取失敗不該說「沒有」token:%q", out)
	}
}

// TestDoctorAppleExpiredDevToken:keychain 有 developer token 但已過期 → ❌ 並提示過期。
func TestDoctorAppleExpiredDevToken(t *testing.T) {
	clearAppleTokens(t)
	expired := time.Now().Add(-1 * time.Hour)
	if err := apple.SaveDeveloperToken(fakeJWT(t, expired), expired); err != nil {
		t.Fatal(err)
	}
	detail, err := checkAppleDevToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "過期") {
		t.Fatalf("過期應回錯並含「過期」:(%q, %v)", detail, err)
	}
}
