package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// cli 套件從本 task 起會在測試中觸碰 secret(keychain)——
// 以 TestMain 統一 MockInit(P0 debug_test.go 內的 inline MockInit 冪等,不衝突)。
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func setCLITestConfig(t *testing.T) {
	t.Helper()
	t.Setenv("CAPY_CONFIG_DIR", t.TempDir())
}

func fakeLoginOK(t *testing.T, wantCID string) func(context.Context, string, func(string) error) (*oauth2.Token, error) {
	t.Helper()
	return func(_ context.Context, cid string, _ func(string) error) (*oauth2.Token, error) {
		if cid != wantCID {
			t.Errorf("clientID = %q, want %q", cid, wantCID)
		}
		_ = secret.Set(auth.KeySpotifyRefreshToken, "rt-test")
		return &oauth2.Token{AccessToken: "at"}, nil
	}
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestAuthLoginWithFlagSavesConfigAndLogsIn(t *testing.T) {
	setCLITestConfig(t)
	orig := spotifyLogin
	spotifyLogin = fakeLoginOK(t, "0123456789abcdef0123456789abcdef")
	t.Cleanup(func() { spotifyLogin = orig })

	out, err := runCLI(t, "auth", "login", "spotify", "--client-id", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Spotify 授權完成") {
		t.Errorf("輸出:%q", out)
	}
	cfg, _ := config.Load()
	if cfg.SpotifyClientID != "0123456789abcdef0123456789abcdef" {
		t.Errorf("client ID 未存 config:%q", cfg.SpotifyClientID)
	}
}

func TestAuthLoginRejectsBadClientID(t *testing.T) {
	setCLITestConfig(t)
	if _, err := runCLI(t, "auth", "login", "spotify", "--client-id", "not-hex"); err == nil ||
		!strings.Contains(err.Error(), "小寫十六進位") {
		t.Fatalf("格式錯誤應被擋下:%v", err)
	}
}

// TestAuthLoginFailureDoesNotPersistClientID:登入失敗不該把 client ID 存進 config——
// 否則使用者以為已設定,下次 auth status 顯示「已設定」卻其實從未成功授權。
func TestAuthLoginFailureDoesNotPersistClientID(t *testing.T) {
	setCLITestConfig(t)
	orig := spotifyLogin
	spotifyLogin = func(context.Context, string, func(string) error) (*oauth2.Token, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { spotifyLogin = orig })

	if _, err := runCLI(t, "auth", "login", "spotify", "--client-id", "0123456789abcdef0123456789abcdef"); err == nil {
		t.Fatal("預期登入失敗回傳錯誤")
	}
	cfg, _ := config.Load()
	if cfg.SpotifyClientID != "" {
		t.Errorf("登入失敗不應保存 client ID,得到 %q", cfg.SpotifyClientID)
	}
}

func TestAuthLoginNonTTYWithoutFlagErrors(t *testing.T) {
	setCLITestConfig(t)
	origTTY := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = origTTY })
	if _, err := runCLI(t, "auth", "login", "spotify"); err == nil ||
		!strings.Contains(err.Error(), "--client-id") {
		t.Fatalf("非 TTY 無 flag 應指示 --client-id:%v", err)
	}
}

func TestAuthLoginUnsupportedProvider(t *testing.T) {
	setCLITestConfig(t)
	if _, err := runCLI(t, "auth", "login", "apple"); err == nil || !strings.Contains(err.Error(), "P2") {
		t.Fatalf("apple 應提示 P2:%v", err)
	}
}

func TestAuthStatusAndLogout(t *testing.T) {
	setCLITestConfig(t)
	_ = config.Save(&config.Config{SpotifyClientID: "0123456789abcdef0123456789abcdef"})
	_ = secret.Set(auth.KeySpotifyRefreshToken, "rt1")

	out, err := runCLI(t, "auth", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已設定") || !strings.Contains(out, "存在") {
		t.Errorf("status 輸出:%q", out)
	}

	if _, err := runCLI(t, "auth", "logout", "spotify"); err != nil {
		t.Fatal(err)
	}
	if _, err := secret.Get(auth.KeySpotifyRefreshToken); !errors.Is(err, secret.ErrNotFound) {
		t.Error("logout 後 refresh token 應被刪除")
	}

	out, _ = runCLI(t, "auth", "status")
	if !strings.Contains(out, "不存在") {
		t.Errorf("logout 後 status 輸出:%q", out)
	}
}

func TestAuthStatusCorruptClientIDDoesNotPanic(t *testing.T) {
	setCLITestConfig(t)
	_ = config.Save(&config.Config{SpotifyClientID: "abc"})
	out, err := runCLI(t, "auth", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "格式異常") {
		t.Errorf("壞 client ID 應提示格式異常,得到 %q", out)
	}
}
