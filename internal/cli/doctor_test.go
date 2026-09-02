package cli

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/config"
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
