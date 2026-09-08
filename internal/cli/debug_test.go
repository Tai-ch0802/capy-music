package cli

import (
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/auth"
)

// TestDebugAppleTokenPrintsKeychain:debug apple-token 只印出 keychain 裡已登入的 token
// (取代舊版現場簽發;登入本身走 capy auth login apple),給 scripts/p0 用。
func TestDebugAppleTokenPrintsKeychain(t *testing.T) {
	dev := setupAppleTokens(t)

	out, err := runCLI(t, "debug", "apple-token")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != dev {
		t.Errorf("應印出 keychain 裡的 dev token:%q want %q", out, dev)
	}

	out, err = runCLI(t, "debug", "apple-token", "--user")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "MUT0" {
		t.Errorf("--user 應印出 user token:%q", out)
	}

	clearAppleTokens(t)
	if _, err := runCLI(t, "debug", "apple-token"); err == nil || !strings.Contains(err.Error(), "capy auth login apple") {
		t.Fatalf("清空應提示 login:%v", err)
	}
}

// TestDebugGoogleClientPrintsOnlyBuiltinID:release 注入檢查——印內建 client ID、絕不印 secret;
// 沒注入(go install)時回錯講明會走 BYO。
func TestDebugGoogleClientPrintsOnlyBuiltinID(t *testing.T) {
	origID, origSec := auth.BuiltinGoogleClientID, auth.BuiltinGoogleClientSecret
	t.Cleanup(func() { auth.BuiltinGoogleClientID, auth.BuiltinGoogleClientSecret = origID, origSec })

	auth.BuiltinGoogleClientID, auth.BuiltinGoogleClientSecret = "", ""
	if _, err := runCLI(t, "debug", "google-client"); err == nil || !strings.Contains(err.Error(), "BYO") {
		t.Fatalf("沒注入應回錯並指向 BYO:%v", err)
	}

	auth.BuiltinGoogleClientID, auth.BuiltinGoogleClientSecret = "ci-test.apps.googleusercontent.com", "GOCSPX-never-print-me"
	out, err := runCLI(t, "debug", "google-client")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ci-test.apps.googleusercontent.com\n" {
		t.Fatalf("應只印 client ID 一行:%q", out)
	}
	if strings.Contains(out, "never-print-me") {
		t.Fatal("secret 絕不能印出來")
	}

	// --secret-state:只回 set / unset(CI 用它驗 BuiltinGoogleClientSecret 的注入路徑沒斷),不印 secret。
	out, err = runCLI(t, "debug", "google-client", "--secret-state")
	if err != nil || out != "set\n" {
		t.Fatalf("secret 有注入應印 set:%q %v", out, err)
	}
	auth.BuiltinGoogleClientSecret = ""
	out, err = runCLI(t, "debug", "google-client", "--secret-state")
	if err != nil || out != "unset\n" {
		t.Fatalf("secret 沒注入應印 unset:%q %v", out, err)
	}
}
