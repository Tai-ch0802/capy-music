package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/provider"
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

// fakeISRCProvider:只實作 ISRC 反查的假 provider(debug lookup-isrc 用)。
type fakeISRCProvider struct {
	caps   provider.Capability
	got    string
	tracks []provider.Track
}

func (f *fakeISRCProvider) ID() string                   { return "spotify" }
func (f *fakeISRCProvider) DisplayName() string          { return "Fake" }
func (f *fakeISRCProvider) Caps() provider.Capability    { return f.caps }
func (f *fakeISRCProvider) Health(context.Context) error { return nil }
func (f *fakeISRCProvider) LookupISRC(_ context.Context, isrc string) ([]provider.Track, error) {
	f.got = isrc
	return f.tracks, nil
}

func TestDebugLookupISRCPrintsTracksWithISRC(t *testing.T) {
	setCLITestConfig(t)
	f := &fakeISRCProvider{caps: provider.CapISRCLookup, tracks: []provider.Track{
		{ProviderID: "a1", ISRC: "TWA472400123", Title: "派對動物", Artists: []string{"五月天"}, Album: "自傳", DurationMS: 227000},
	}}
	swapProviderWith(t, f)
	out, err := runCLI(t, "debug", "lookup-isrc", "TWA472400123")
	if err != nil {
		t.Fatal(err)
	}
	if f.got != "TWA472400123" {
		t.Fatalf("要把 ISRC 原樣交給 provider(正規化是 provider 的事):%q", f.got)
	}
	if out != "a1\t派對動物\t五月天\t自傳\t227000\tTWA472400123\n" {
		t.Fatalf("非 TTY 應是含 ISRC 欄的 TSV:%q", out)
	}

	f.tracks = nil
	out, err = runCLI(t, "debug", "lookup-isrc", "TWA472400123")
	if err != nil || !strings.Contains(out, "沒有曲目符合") {
		t.Fatalf("沒有命中要說明、exit 0:%q %v", out, err)
	}

	f.caps = provider.CapSearch // 沒宣告 CapISRCLookup 就不能用,即使型別有這個方法
	if _, err := runCLI(t, "debug", "lookup-isrc", "TWA472400123"); err == nil || !strings.Contains(err.Error(), "不支援ISRC 反查") {
		t.Fatalf("沒有能力位要拒絕:%v", err)
	}
}
