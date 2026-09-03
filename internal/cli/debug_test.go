package cli

import (
	"strings"
	"testing"
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
