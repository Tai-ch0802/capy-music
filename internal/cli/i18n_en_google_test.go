package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/config"
)

// 英文模式:auth_google.go / drive.go / history.go / completion.go 產生的字串(T2a google 組)。

func TestGoogleLoginEnglish(t *testing.T) {
	withLanguage(t, "en")
	setGoogleTest(t)

	_, err := runCLI(t, "auth", "login", "google")
	if err == nil || !strings.HasPrefix(err.Error(), "this binary has no built-in Google client (builds from go install never include one); ") ||
		!strings.Contains(err.Error(), `click "Publish app"`) || hasCJK(err.Error()) {
		t.Fatalf("非 TTY 沒有 client 的英文錯誤(含整段指南):%v", err)
	}
	_, err = runCLI(t, "auth", "login", "google", "--client-secret", "s")
	if err == nil || err.Error() != "got a client secret but no client ID (pass --client-id / CAPY_GOOGLE_CLIENT_ID, or log in once so it's saved in config)" {
		t.Fatalf("只給 secret 的英文錯誤:%v", err)
	}

	googleLoginFn = fakeGoogleLogin(t, "byo.apps.googleusercontent.com", "s3cret", "tai@example.com")
	out, err := runCLI(t, "auth", "login", "google", "--client-id", "byo.apps.googleusercontent.com", "--client-secret", "s3cret")
	want := "Finish the Google authorization in your browser… (within 180s; tick all three permissions)\n" +
		"✅ Google authorization complete: tai@example.com (client source: flag/env; token saved in the keychain)\n"
	if err != nil || out != want {
		t.Fatalf("英文的登入輸出:%v\n%q", err, out)
	}
	if strings.Contains(out, "s3cret") {
		t.Fatal("secret 不得印出")
	}
}

func TestGoogleLoginWizardSourceEnglish(t *testing.T) {
	withLanguage(t, "en")
	setGoogleTest(t)
	stdinIsTTY = func() bool { return true }
	googleWizard = func() (string, string, error) { return "wiz", "", nil }
	googleLoginFn = fakeGoogleLogin(t, "wiz", "", "")
	out, err := runCLI(t, "auth", "login", "google")
	if err != nil || !strings.Contains(out, "✅ Google authorization complete: (no email in the id_token) (client source: setup wizard; token saved in the keychain)\n") {
		t.Fatalf("精靈來源與沒有 email 的英文:%v %q", err, out)
	}
}

func TestGoogleLoginConfigSaveFailureEnglish(t *testing.T) {
	withLanguage(t, "en")
	setGoogleTest(t)
	dir, _ := config.Dir()
	if err := os.MkdirAll(filepath.Join(dir, "config.json.tmp"), 0o700); err != nil { // 同 TestGoogleLoginConfigSaveFailureSaysAuthSucceeded
		t.Fatal(err)
	}
	googleLoginFn = fakeGoogleLogin(t, "x", "s", "a@b")
	_, err := runCLI(t, "auth", "login", "google", "--client-id", "x", "--client-secret", "s")
	if err == nil || !strings.HasPrefix(err.Error(), "Google authorization succeeded and the token is in the keychain, but writing the config failed (") {
		t.Fatalf("config 寫失敗的英文要講明授權已成功:%v", err)
	}
}

func TestMaskGoogleClientIDEnglish(t *testing.T) {
	withLanguage(t, "en")
	if got := maskGoogleClientID("abc.apps.googleusercontent.com"); got != "set" {
		t.Errorf("短 id:%q", got)
	}
	if got := maskGoogleClientID("1234567890-abc.apps.googleusercontent.com"); got != "set, 123456…" {
		t.Errorf("長 id:%q", got)
	}
}

func TestGoogleClientFromConfigKeychainErrorEnglish(t *testing.T) {
	withLanguage(t, "en")
	boom := errors.New("user canceled")
	keyring.MockInitWithError(boom)
	t.Cleanup(func() { keyring.MockInit() })
	_, _, err := googleClientFromConfig(&config.Config{GoogleClientID: "x"})
	if err == nil || err.Error() != "reading google.client_secret from the keychain: user canceled" || !errors.Is(err, boom) {
		t.Fatalf("keychain 讀取失敗的英文(且要包住原錯誤):%v", err)
	}
}

func TestHistoryClearEnglish(t *testing.T) {
	withLanguage(t, "en")
	setCLITestConfig(t)
	c := cache.Load()
	c.AddRecent(cache.Recent{Provider: "spotify", Type: cache.TypeArtist, ID: "a1", Label: "x"})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "history", "clear"); err != nil || out != "Cleared 1 recent entry\n" {
		t.Fatalf("單數:%v %q", err, out)
	}
	if out, err := runCLI(t, "history", "clear"); err != nil || out != "Cleared 0 recent entries\n" {
		t.Fatalf("複數:%v %q", err, out)
	}
	if out, err := runCLI(t, "history", "--help"); err != nil || !strings.Contains(out, "Recent searches and plays (local cache, used by completion and the picker)") ||
		!strings.Contains(out, "Clear recent history (the playlist cache is kept)") {
		t.Fatalf("history 的英文說明:%v %q", err, out)
	}
}

func TestCompletionEnglish(t *testing.T) {
	withLanguage(t, "en")
	setCLITestConfig(t)
	forbidProvider(t)
	c := cache.Load()
	c.SetPlaylists("spotify", []cache.Playlist{{ID: "p1", Name: "Commute"}})
	c.AddRecent(cache.Recent{Provider: "spotify", Type: cache.TypeArtist, ID: "a1", Label: "Mayday"})
	c.AddRecent(cache.Recent{Provider: "spotify", Type: cache.TypeQuery, ID: "lofi", Label: "lofi"})
	_ = c.Save()
	out, err := runCLI(t, "__complete", "play", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Commute\tplaylist\n", "Mayday\trecent artist\n", "lofi\trecent search\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("補全缺 %q:\n%s", want, out)
		}
	}
	if hasCJK(out) {
		t.Errorf("英文模式的補全說明不該有中文:%q", out)
	}
	for typ, want := range map[string]string{cache.TypePlaylist: "recent playlist", cache.TypeTrack: "recent track", "?": "recent"} {
		if got := recentDesc(typ); got != want {
			t.Errorf("recentDesc(%q) = %q, want %q", typ, got, want)
		}
	}
}
