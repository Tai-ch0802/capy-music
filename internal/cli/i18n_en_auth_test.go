package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/config"
)

// zhAppleDisclosure:搬進語系目錄前 auth.go 的原文,一個位元組都不准變(CLAUDE.md:揭露在每個語系都要完整)。
const zhAppleDisclosure = "⚠️ 非 Apple 官方支援。你要貼上的兩個 token 屬於 Apple 網頁播放器(music.apple.com):\n" +
	"  · Apple 可能隨時更換或撤銷 —— 屆時重新執行 capy auth login apple 即可\n" +
	"  · 以第三方工具存取 Apple Music 的服務條款風險由你自行承擔\n" +
	"  · capy 只指導你複製,不會讀取你的瀏覽器資料\n" +
	"  · 寫入時,加進清單的曲目可能會一起加進你的 Apple Music 資料庫(看你的 Apple Music 設定,這是 Apple 的行為);capy 只寫你自己建的清單"

// checkAppleDisclosure:揭露要講的每一件事都在(web token 是使用者自己從網頁播放器複製的、非官方、會失效、風險自負、
// capy 只指導不擷取、加進清單「可能」也進資料庫且看設定、只寫自建清單),而且不能寫成必然。
func checkAppleDisclosure(t *testing.T, lang string) {
	t.Helper()
	d := appleDisclosure()
	switch lang {
	case "zh-TW":
		if d != zhAppleDisclosure {
			t.Fatalf("zh-TW 揭露要與原文一字不差:\n got %q\nwant %q", d, zhAppleDisclosure)
		}
	case "en":
		if hasCJK(d) {
			t.Errorf("英文揭露混了中文:%q", d)
		}
		for _, must := range []string{
			"Not officially supported by Apple",
			"belong to Apple's web player (music.apple.com)",
			"Apple may change or revoke them at any time",
			"they stop working",
			"run capy auth login apple again",
			"at your own risk",
			"only shows you how to copy them by hand",
			"never reads your browser's data",
			"may also be added to your Apple Music library",
			"depending on your Apple Music settings",
			"only writes to playlists you created",
		} {
			if !strings.Contains(d, must) {
				t.Errorf("英文揭露缺 %q:%q", must, d)
			}
		}
		for _, never := range []string{"will be added", "will also", "will add"} {
			if strings.Contains(d, never) {
				t.Errorf("加進資料庫只能寫「可能」,不可寫成 %q:%q", never, d)
			}
		}
	}
}

// TestAuthLoginAppleDisclosureInEveryLanguage:每條印出或拒絕的路徑,在 en 與 zh-TW 都帶著完整揭露。
func TestAuthLoginAppleDisclosureInEveryLanguage(t *testing.T) {
	t.Cleanup(keyring.MockInit) // mock keychain 是 process 全域:種過的 token 不留給下一個測試
	for _, lang := range []string{"en", "zh-TW"} {
		t.Run(lang, func(t *testing.T) {
			withLanguage(t, lang)
			checkAppleDisclosure(t, lang)
			exp := time.Now().Add(24 * time.Hour)
			dev := fakeJWT(t, exp)

			// flag / env 路徑:成功時印到 stderr。
			clearAppleTokens(t)
			appleServer(t, dev, "MUT1")
			t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", dev)
			t.Setenv("CAPY_APPLE_USER_TOKEN", "MUT1")
			out, err := runCLI(t, "auth", "login", "apple", "--i-understand")
			if err != nil || !strings.Contains(out, appleDisclosure()) {
				t.Fatalf("flag/env 路徑要印完整揭露:%v %q", err, out)
			}

			// 沒有 --i-understand:拒絕訊息本身帶完整揭露。
			clearAppleTokens(t)
			if _, err := runCLI(t, "auth", "login", "apple"); err == nil ||
				!strings.Contains(err.Error(), appleDisclosure()) || !strings.Contains(err.Error(), "--i-understand") {
				t.Fatalf("拒絕訊息要帶完整揭露:%v", err)
			}

			// 隱藏的 --auto(非 TTY):沒有 --i-understand 拒絕並帶揭露;有就印揭露再擷取。
			t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "")
			t.Setenv("CAPY_APPLE_USER_TOKEN", "")
			origTTY, origAuto := stdinIsTTY, appleAutoTokens
			t.Cleanup(func() { stdinIsTTY, appleAutoTokens = origTTY, origAuto })
			stdinIsTTY = func() bool { return false }
			appleAutoTokens = func() (apple.WebTokens, error) { return apple.WebTokens{Developer: dev, User: "MUT1"}, nil }
			if _, err := runCLI(t, "auth", "login", "apple", "--auto"); err == nil || !strings.Contains(err.Error(), appleDisclosure()) {
				t.Fatalf("--auto 非 TTY 的拒絕要帶完整揭露:%v", err)
			}
			clearAppleTokens(t)
			appleServer(t, dev, "MUT1")
			if out, err := runCLI(t, "auth", "login", "apple", "--auto", "--i-understand"); err != nil || !strings.Contains(out, appleDisclosure()) {
				t.Fatalf("--auto 非 TTY 要印完整揭露:%v %q", err, out)
			}
		})
	}
}

func TestAuthLoginEnglish(t *testing.T) {
	t.Cleanup(keyring.MockInit)
	withLanguage(t, "en")
	setCLITestConfig(t)
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "")
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")

	// --auto 仍然藏著;說明是英文。
	out, err := runCLI(t, "auth", "login", "--help")
	if err != nil || strings.Contains(out, "--auto") ||
		!strings.Contains(out, "Log in to a platform (Spotify: your own app + PKCE;") ||
		!strings.Contains(out, `confirms you have read the "not officially supported by Apple" disclosure`) {
		t.Errorf("auth login --help:%v %q", err, out)
	}
	if out, _ := runCLI(t, "auth", "--help"); !strings.Contains(out, "Show each platform's authorization status") ||
		!strings.Contains(out, "Log out of a platform") {
		t.Errorf("auth --help:%q", out)
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"auth", "login", "tidal"}, "currently supported: spotify, apple, google"},
		{[]string{"auth", "logout", "tidal"}, "currently supported: spotify, apple, google"},
		{[]string{"auth", "login", "local"}, "local has no credentials (plan §2 A8): just run capy config set local_root <directory>"},
		{[]string{"auth", "login", "spotify", "--client-id", "not-hex"}, "the client ID should be 32 lowercase hex characters (copy it from the dashboard)"},
		{[]string{"auth", "login", "apple", "--user-token", "MUT1"}, "got a user token (--user-token / CAPY_APPLE_USER_TOKEN) but no developer token — pass both, or neither (wizard / --auto)"},
	} {
		if _, err := runCLI(t, c.args...); err == nil || err.Error() != c.want {
			t.Errorf("%v:want %q,got %v", c.args, c.want, err)
		}
	}

	origTTY := stdinIsTTY
	t.Cleanup(func() { stdinIsTTY = origTTY })
	stdinIsTTY = func() bool { return false }
	if _, err := runCLI(t, "auth", "login", "spotify"); err == nil || err.Error() != "not an interactive terminal: pass --client-id (to create the app: https://developer.spotify.com/dashboard, with redirect URI http://127.0.0.1:8888/callback)" {
		t.Errorf("spotify 非 TTY:%v", err)
	}
	if _, err := runCLI(t, "auth", "login", "apple"); err == nil || hasCJK(err.Error()) ||
		!strings.HasPrefix(err.Error(), "not an interactive terminal: set CAPY_APPLE_DEVELOPER_TOKEN") ||
		!strings.HasSuffix(err.Error(), "--i-understand.\n"+appleGuide()) {
		t.Errorf("apple 非 TTY 沒 token:%v", err)
	}

	// Spotify 成功路徑。
	orig := spotifyLogin
	t.Cleanup(func() { spotifyLogin = orig })
	spotifyLogin = fakeLoginOK(t, "0123456789abcdef0123456789abcdef")
	out, err = runCLI(t, "auth", "login", "spotify", "--client-id", "0123456789abcdef0123456789abcdef")
	if err != nil || !strings.Contains(out, "Finish the Spotify authorization in your browser… (within 180s)\n") ||
		!strings.Contains(out, "✅ Spotify authorized (refresh token saved to the keychain)\n") {
		t.Errorf("spotify 成功:%v %q", err, out)
	}
}

func TestAuthLoginAppleEnglishErrors(t *testing.T) {
	t.Cleanup(keyring.MockInit)
	withLanguage(t, "en")

	expired := time.Now().Add(-time.Hour)
	if err := validateAppleDevToken(fakeJWT(t, expired)); err == nil || err.Error() != "expired at "+expired.Format(time.RFC3339)+"; copy it again" {
		t.Errorf("validateAppleDevToken 過期:%v", err)
	}

	clearAppleTokens(t)
	appleServer(t, "whatever", "MUT1")
	t.Setenv("CAPY_APPLE_USER_TOKEN", "MUT1")
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", fakeJWT(t, expired))
	if _, err := runCLI(t, "auth", "login", "apple", "--i-understand"); err == nil ||
		err.Error() != "the developer token expired at "+expired.Format(time.RFC3339)+" — copy it again from the web player" {
		t.Errorf("dev token 過期:%v", err)
	}
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "not-a-jwt")
	if _, err := runCLI(t, "auth", "login", "apple", "--i-understand"); err == nil ||
		!strings.HasPrefix(err.Error(), "the developer token is malformed (") {
		t.Errorf("dev token 格式:%v", err)
	}

	exp := time.Now().Add(24 * time.Hour)
	dev := fakeJWT(t, exp)
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", dev)
	appleServer(t, "OTHER-DEV", "MUT1")
	if _, err := runCLI(t, "auth", "login", "apple", "--i-understand"); err == nil ||
		!strings.HasPrefix(err.Error(), "Apple rejected the developer token — copy the authorization header again (Apple may have rotated it): ") {
		t.Errorf("preflight 401:%v", err)
	}
	appleServer(t, dev, "MUTgood")
	if _, err := runCLI(t, "auth", "login", "apple", "--i-understand"); err == nil ||
		!strings.HasPrefix(err.Error(), "Apple rejected the user token — copy media-user-token again: ") {
		t.Errorf("storefront 403:%v", err)
	}
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")
	if _, err := runCLI(t, "auth", "login", "apple", "--i-understand"); err == nil ||
		err.Error() != "there is no user token in the keychain — the first time you log in, provide media-user-token too" {
		t.Errorf("沒有既有 user token:%v", err)
	}

	// 成功:stdout 的完成訊息是英文。
	t.Setenv("CAPY_APPLE_USER_TOKEN", "MUT1")
	appleServer(t, dev, "MUT1")
	out, err := runCLI(t, "auth", "login", "apple", "--i-understand")
	if want := "✅ Logged in to Apple Music (storefront tw; developer token valid until " + exp.Format("2006-01-02") + ")\n"; err != nil || !strings.Contains(out, want) {
		t.Errorf("apple 成功:want %q,got %v %q", want, err, out)
	}

	// --auto 擷取失敗回退手動精靈的提示。
	clearAppleTokens(t)
	appleServer(t, dev, "MUT1")
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "")
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")
	origTTY, origConfirm, origInputs, origAuto := stdinIsTTY, confirmAppleDisclosure, runAppleWizardInputs, appleAutoTokens
	t.Cleanup(func() {
		stdinIsTTY, confirmAppleDisclosure, runAppleWizardInputs, appleAutoTokens = origTTY, origConfirm, origInputs, origAuto
	})
	stdinIsTTY = func() bool { return true }
	confirmAppleDisclosure = func() error { return nil }
	runAppleWizardInputs = func(bool) (string, string, error) { return dev, "MUT1", nil }
	appleAutoTokens = func() (apple.WebTokens, error) { return apple.WebTokens{}, errors.New("no tab") }
	if out, err := runCLI(t, "auth", "login", "apple", "--auto"); err != nil || !strings.Contains(out, "automatic extraction failed, pasting by hand instead: no tab\n") {
		t.Errorf("--auto 回退:%v %q", err, out)
	}
}

// TestAuthStatusEnglish:auth status 的每一行都出自 auth.go;欄名(client_id、refresh token…)是固定字,只翻值。
func TestAuthStatusEnglish(t *testing.T) {
	withLanguage(t, "en")
	origID := auth.BuiltinGoogleClientID
	auth.BuiltinGoogleClientID = ""
	t.Cleanup(func() { auth.BuiltinGoogleClientID = origID })

	keyring.MockInit() // 前面的測試可能留下別家的 token
	t.Cleanup(keyring.MockInit)
	setupAppleTokens(t)
	_, exp, err := apple.DeveloperToken(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "auth", "status")
	want := "spotify:\n" +
		"  client_id: not set\n" +
		"  refresh token: missing (run capy auth login spotify)\n" +
		"google:\n" +
		"  client: not set (run capy auth login google)\n" +
		"  token: missing (run capy auth login google)\n" +
		"apple:\n" +
		"  developer token: valid until " + exp.Format(time.RFC3339) + "\n" +
		"  user token: present\n" +
		"  storefront: not set\n"
	if err != nil || out != want {
		t.Fatalf("auth status:\n got %q\nwant %q", out, want)
	}

	_ = config.Save(&config.Config{SpotifyClientID: "0123456789abcdef0123456789abcdef", AppleStorefront: "tw"})
	_, _ = fakeLoginOK(t, "")(context.Background(), "", nil)
	gexp := time.Now().Add(time.Hour)
	_ = auth.SaveToken(auth.KeyGoogleToken, &oauth2.Token{AccessToken: "at", RefreshToken: "rt", Expiry: gexp})
	expired := time.Now().Add(-time.Hour)
	if err := apple.SaveDeveloperToken(fakeJWT(t, expired), expired); err != nil {
		t.Fatal(err)
	}
	out, _ = runCLI(t, "auth", "status")
	for _, line := range []string{
		"  client_id: set (0123…cdef)\n",
		"  refresh token: in the keychain\n",
		"  token: in the keychain (access token expires " + gexp.Local().Format(time.RFC3339) + "; the refresh token doesn't rotate)\n",
		"  developer token: expired at " + time.Unix(expired.Unix(), 0).Format(time.RFC3339) + " (run capy auth login apple)\n",
		"  storefront: tw\n",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("缺 %q:%q", line, out)
		}
	}
	if hasCJK(out) {
		t.Errorf("英文的 auth status 混了中文:%q", out)
	}

	_ = config.Save(&config.Config{SpotifyClientID: "abc"})
	if out, _ := runCLI(t, "auth", "status"); !strings.Contains(out, "  client_id: set but malformed (should be 32 hex characters) — run capy auth login spotify again\n") {
		t.Errorf("壞 client ID:%q", out)
	}

	keyring.MockInitWithError(errors.New("user canceled"))
	out, _ = runCLI(t, "auth", "status")
	for _, line := range []string{"  developer token: couldn't read the keychain: ", "  user token: couldn't read the keychain: "} {
		if !strings.Contains(out, line) {
			t.Errorf("keychain 讀取失敗缺 %q:%q", line, out)
		}
	}
	keyring.MockInit()

	if out, err := runCLI(t, "auth", "logout", "apple"); err != nil || out != "logged out of apple\n" {
		t.Errorf("logout:%v %q", err, out)
	}
}
