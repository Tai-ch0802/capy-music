package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

func fakeGoogleLogin(t *testing.T, wantID, wantSecret, email string) func(context.Context, auth.GoogleClient, func(string) error) (*oauth2.Token, string, error) {
	t.Helper()
	return func(_ context.Context, c auth.GoogleClient, _ func(string) error) (*oauth2.Token, string, error) {
		if c.ID != wantID || c.Secret != wantSecret {
			t.Errorf("client = %+v, want id=%q secret=%q", c, wantID, wantSecret)
		}
		tok := &oauth2.Token{AccessToken: "at", RefreshToken: "rt", Expiry: time.Now().Add(time.Hour)}
		_ = auth.SaveToken(auth.KeyGoogleToken, tok)
		return tok, email, nil
	}
}

func setGoogleTest(t *testing.T) {
	t.Helper()
	setCLITestConfig(t)
	origLogin, origWizard, origTTY := googleLoginFn, googleWizard, stdinIsTTY
	origID, origSecret := auth.BuiltinGoogleClientID, auth.BuiltinGoogleClientSecret
	auth.BuiltinGoogleClientID, auth.BuiltinGoogleClientSecret = "", ""
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() {
		googleLoginFn, googleWizard, stdinIsTTY = origLogin, origWizard, origTTY
		auth.BuiltinGoogleClientID, auth.BuiltinGoogleClientSecret = origID, origSecret
		_ = secret.Delete(auth.KeyGoogleToken)
		_ = secret.Delete(auth.KeyGoogleClientSecret)
	})
}

func TestGoogleLoginNonTTYWithoutClientExplainsFlagsAndEnv(t *testing.T) {
	setGoogleTest(t)
	_, err := runCLI(t, "auth", "login", "google")
	if err == nil {
		t.Fatal("非 TTY 又沒有 client 應報錯")
	}
	for _, want := range []string{"--client-id", "CAPY_GOOGLE_CLIENT_ID", "CAPY_GOOGLE_CLIENT_SECRET", "Publish app"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("錯誤訊息缺 %q:%v", want, err)
		}
	}
}

func TestGoogleLoginBYOPersistsClientEmailDeviceID(t *testing.T) {
	setGoogleTest(t)
	googleLoginFn = fakeGoogleLogin(t, "byo.apps.googleusercontent.com", "s3cret", "tai@example.com")
	out, err := runCLI(t, "auth", "login", "google", "--client-id", "byo.apps.googleusercontent.com", "--client-secret", "s3cret")
	if err != nil || !strings.Contains(out, "tai@example.com") || !strings.Contains(out, "flag/env") {
		t.Fatalf("%v %q", err, out)
	}
	cfg, _ := config.Load()
	if cfg.GoogleClientID != "byo.apps.googleusercontent.com" || cfg.GoogleEmail != "tai@example.com" || len(cfg.DeviceID) != 26 {
		t.Fatalf("config 應落地 client id / email / device_id:%+v", cfg)
	}
	if s, err := secret.Get(auth.KeyGoogleClientSecret); err != nil || s != "s3cret" {
		t.Fatalf("BYO secret 只進 keychain:%q %v", s, err)
	}
	if b, _ := config.Load(); strings.Contains(b.GoogleClientID, "s3cret") {
		t.Fatal("secret 不得進 config")
	}
	// 第二次登入:client 從 config + keychain 來,device_id 不變(它是這台裝置的身分)。
	first := cfg.DeviceID
	googleLoginFn = fakeGoogleLogin(t, "byo.apps.googleusercontent.com", "s3cret", "tai@example.com")
	if out, err := runCLI(t, "auth", "login", "google"); err != nil || !strings.Contains(out, "config") {
		t.Fatalf("第二次應從 config 取 client:%v %q", err, out)
	}
	if cfg2, _ := config.Load(); cfg2.DeviceID != first {
		t.Fatalf("device_id 應穩定:%s → %s", first, cfg2.DeviceID)
	}
}

func TestGoogleLoginBuiltinClientDoesNotPersistClient(t *testing.T) {
	setGoogleTest(t)
	auth.BuiltinGoogleClientID, auth.BuiltinGoogleClientSecret = "builtin.apps.googleusercontent.com", "bsec"
	googleLoginFn = fakeGoogleLogin(t, "builtin.apps.googleusercontent.com", "bsec", "me@x")
	out, err := runCLI(t, "auth", "login", "google")
	if err != nil || !strings.Contains(out, "builtin") {
		t.Fatalf("%v %q", err, out)
	}
	cfg, _ := config.Load()
	if cfg.GoogleClientID != "" {
		t.Fatalf("內建 client 不落地 config:%+v", cfg)
	}
	if _, err := secret.Get(auth.KeyGoogleClientSecret); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("內建 secret 不進 keychain:%v", err)
	}
	if out, _ := runCLI(t, "auth", "status"); !strings.Contains(out, "client: 內建") || !strings.Contains(out, "email: me@x") || !strings.Contains(out, "device_id:") || !strings.Contains(out, "token: keychain 存在") {
		t.Fatalf("status 應顯示內建 client / email / device_id / token:%q", out)
	}
}

func TestGoogleLoginTTYUsesWizardWhenNoClient(t *testing.T) {
	setGoogleTest(t)
	stdinIsTTY = func() bool { return true }
	googleWizard = func() (string, string, error) { return "wiz.apps.googleusercontent.com", "", nil }
	googleLoginFn = fakeGoogleLogin(t, "wiz.apps.googleusercontent.com", "", "w@x")
	if _, err := runCLI(t, "auth", "login", "google"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(); cfg.GoogleClientID != "wiz.apps.googleusercontent.com" {
		t.Fatalf("精靈輸入的 client id 應落地:%+v", cfg)
	}
	if _, err := secret.Get(auth.KeyGoogleClientSecret); !errors.Is(err, secret.ErrNotFound) {
		t.Fatal("空的 secret 不該寫 keychain")
	}
}

func TestGoogleLoginFailureLeavesConfigUntouched(t *testing.T) {
	setGoogleTest(t)
	googleLoginFn = func(context.Context, auth.GoogleClient, func(string) error) (*oauth2.Token, string, error) {
		return nil, "", errors.New("授權被拒")
	}
	if _, err := runCLI(t, "auth", "login", "google", "--client-id", "x"); err == nil {
		t.Fatal("應回錯")
	}
	if cfg, _ := config.Load(); cfg.GoogleClientID != "" || cfg.DeviceID != "" {
		t.Fatalf("失敗不得留半殘 config:%+v", cfg)
	}
}

func TestGoogleLogoutClearsKeysAndEmail(t *testing.T) {
	setGoogleTest(t)
	googleLoginFn = fakeGoogleLogin(t, "x", "s", "who@x")
	if _, err := runCLI(t, "auth", "login", "google", "--client-id", "x", "--client-secret", "s"); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "auth", "logout", "google"); err != nil || !strings.Contains(out, "已登出 google") {
		t.Fatalf("%v %q", err, out)
	}
	for _, k := range []string{auth.KeyGoogleToken, auth.KeyGoogleClientSecret} {
		if _, err := secret.Get(k); !errors.Is(err, secret.ErrNotFound) {
			t.Errorf("%s 應被刪:%v", k, err)
		}
	}
	cfg, _ := config.Load()
	if cfg.GoogleEmail != "" || cfg.GoogleClientID != "x" || cfg.DeviceID == "" {
		t.Fatalf("登出應清 email、保留 client id 與 device_id:%+v", cfg)
	}
	if out, _ := runCLI(t, "auth", "status"); !strings.Contains(out, "token: 不存在") || strings.Contains(out, "email:") {
		t.Fatalf("登出後 status:%q", out)
	}
}

func TestGoogleClientEnvVars(t *testing.T) {
	setGoogleTest(t)
	t.Setenv("CAPY_GOOGLE_CLIENT_ID", "env.apps.googleusercontent.com")
	t.Setenv("CAPY_GOOGLE_CLIENT_SECRET", "envsec")
	googleLoginFn = fakeGoogleLogin(t, "env.apps.googleusercontent.com", "envsec", "e@x")
	if _, err := runCLI(t, "auth", "login", "google"); err != nil {
		t.Fatal(err)
	}
}
