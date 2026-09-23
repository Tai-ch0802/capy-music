package cli

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// 英文模式下 doctor 自己印的字(T2a doctor 組)。只斷言 doctor.go / doctor_darwin.go 產生的字:
// 包進來的錯誤(provider、auth、local)屬於別組,還可能是中文。

func TestDoctorEnglishLocalAllPass(t *testing.T) {
	_, _, root := localWorld(t)
	withLanguage(t, "en")
	out, _, err := runPull(t, "doctor", "--provider", "local")
	want := "✅ local_root setting:" + root + "\n✅ Directory and library.json:1 playlist file\nAll checks passed 🎉\n"
	if err != nil || out != want {
		t.Fatalf("(%v)\n got %q\nwant %q", err, out, want)
	}
}

// 失敗數是複數訊息:2 → checks、1 → check。
func TestDoctorEnglishFailedCount(t *testing.T) {
	setCLITestConfig(t)
	withLanguage(t, "en")
	out, err := runCLI(t, "doctor", "--provider", "local") // 沒設 local_root:兩項都失敗
	if err == nil || err.Error() != "2 checks failed" {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out, "❌ local_root setting:not set — capy config set local_root <directory>\n") {
		t.Errorf("%q", out)
	}

	_, _, root := localWorld(t)
	writeFile(t, root, "library.json", "{壞掉") // local_root 那項過、library.json 那項失敗
	if _, _, err := runPull(t, "doctor", "--provider", "local"); err == nil || err.Error() != "1 check failed" {
		t.Fatalf("%v", err)
	}
}

func TestDoctorEnglishUnknownProvider(t *testing.T) {
	setCLITestConfig(t)
	withLanguage(t, "en")
	if _, err := runCLI(t, "doctor", "--provider", "nope"); err == nil || err.Error() != `unknown provider "nope" (available: spotify, apple, local)` {
		t.Fatalf("%v", err)
	}
	if got := newDoctorCmd().Short; got != "Check your setup and connections (one stop for BYO credential problems)" {
		t.Errorf("Short = %q", got)
	}
}

func TestDoctorEnglishSpotifyChecks(t *testing.T) {
	clearAppleTokens(t) // 乾淨 config + keychain
	_ = secret.Delete(auth.KeySpotifyToken)
	_ = secret.Delete(auth.KeySpotifyRefreshToken)
	withLanguage(t, "en")
	ctx := context.Background()
	for _, c := range []struct {
		name string
		fn   func(context.Context) (string, error)
		want string
	}{
		{"config", checkConfig, "no Spotify client ID set — run capy auth login spotify"},
		{"refresh token", checkRefreshToken, "no refresh token in the keychain — run capy auth login spotify"},
		{"token refresh", checkTokenRefresh, "the config file check has to pass first"},
		{"apple dev token", checkAppleDevToken, "no developer token in the keychain — run capy auth login apple"},
		{"apple user token", checkAppleUserToken, "no Music User Token in the keychain — run capy auth login apple"},
		{"apple storefront", checkAppleStorefront, "not set — run capy auth login apple again"},
	} {
		if _, err := c.fn(ctx); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if detail, err := checkKeychain(ctx); err != nil || detail != "read and write work" {
		t.Errorf("keychain: (%q, %v)", detail, err)
	}

	if err := config.Save(&config.Config{SpotifyClientID: "nope"}); err != nil {
		t.Fatal(err)
	}
	if _, err := checkConfig(ctx); err == nil || err.Error() != `the client ID is malformed (it should be 32 hex digits): "nope"` {
		t.Errorf("%v", err)
	}
	if err := config.Save(&config.Config{SpotifyClientID: "0123456789abcdef0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	if detail, err := checkConfig(ctx); err != nil || detail != "client ID looks valid" {
		t.Errorf("(%q, %v)", detail, err)
	}
	if _, err := checkTokenRefresh(ctx); err == nil || err.Error() != "the refresh token check has to pass first" {
		t.Errorf("%v", err)
	}
}

func TestDoctorEnglishAppleDevToken(t *testing.T) {
	setupAppleTokens(t)
	withLanguage(t, "en")
	ctx := context.Background()
	if detail, err := checkAppleDevToken(ctx); err != nil || !strings.HasPrefix(detail, "valid until ") {
		t.Errorf("(%q, %v)", detail, err)
	}
	if detail, err := checkAppleUserToken(ctx); err != nil || detail != "found in the keychain" {
		t.Errorf("(%q, %v)", detail, err)
	}
	expired := time.Now().Add(-time.Hour)
	if err := apple.SaveDeveloperToken(fakeJWT(t, expired), expired); err != nil {
		t.Fatal(err)
	}
	_, err := checkAppleDevToken(ctx)
	if err == nil || !strings.HasPrefix(err.Error(), "expired on ") || !strings.HasSuffix(err.Error(), " — run capy auth login apple again") {
		t.Errorf("%v", err)
	}
}

// keychain 讀寫失敗:只看 doctor 自己的前綴,後面包的是 secret / auth 的錯誤。
func TestDoctorEnglishKeychainErrors(t *testing.T) {
	setCLITestConfig(t)
	withLanguage(t, "en")
	keyring.MockInitWithError(errors.New("user canceled"))
	t.Cleanup(func() { keyring.MockInit() })
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		fn     func(context.Context) (string, error)
		prefix string
	}{
		{"keychain", checkKeychain, "can't write (is the keychain unavailable?): "},
		{"refresh token", checkRefreshToken, "can't read the keychain (access may have been denied, it's locked, or its contents are corrupted): "},
		{"apple dev token", checkAppleDevToken, "can't read the keychain (access may have been denied, or it's locked): "},
		{"apple user token", checkAppleUserToken, "can't read the keychain (access may have been denied, or it's locked): "},
	} {
		if _, err := c.fn(ctx); err == nil || !strings.HasPrefix(err.Error(), c.prefix) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// 已取消的 context:exec 在啟動 osascript 之前就回錯,不會真的去碰 Music.app。
func TestDoctorEnglishOSAError(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("checkOSA 只在 macOS 有實作")
	}
	withLanguage(t, "en")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := checkOSA(ctx)
	want := "osascript can't control Music.app (it isn't installed, or automation isn't allowed: in System Settings → Privacy & Security → Automation, allow your terminal to control Music): context canceled"
	if err == nil || err.Error() != want || !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}
