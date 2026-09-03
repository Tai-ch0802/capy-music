package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
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

// fakeJWT:只有 exp 的假 developer token(不驗簽)。
func fakeJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"ES256"}`)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + ".sig"
}

// setupAppleTokens:乾淨 config + keychain,預放一顆 24h 有效的 dev token 與 user token "MUT0";回傳 dev token。
func setupAppleTokens(t *testing.T) string {
	t.Helper()
	setCLITestConfig(t)
	exp := time.Now().Add(24 * time.Hour)
	dev := fakeJWT(t, exp)
	if err := apple.SaveDeveloperToken(dev, exp); err != nil {
		t.Fatal(err)
	}
	if err := secret.Set(apple.KeyMusicUserToken, "MUT0"); err != nil {
		t.Fatal(err)
	}
	return dev
}

// clearAppleTokens:清 keychain(首次登入情境)。
func clearAppleTokens(t *testing.T) {
	t.Helper()
	setCLITestConfig(t)
	_ = secret.Delete(apple.KeyDeveloperToken)
	_ = secret.Delete(apple.KeyMusicUserToken)
}

// appleServer:假 amp-api。preflight 只認 dev;storefront 要 Media-User-Token == wantMUT 才回 tw,否則 403。
// 回傳 hits 供「不該打網路」的斷言。
func appleServer(t *testing.T, wantDev, wantMUT string) *int32 {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("Origin") != "https://music.apple.com" {
			t.Errorf("缺 Origin:%v", r.Header)
		}
		if r.Header.Get("Authorization") != "Bearer "+wantDev {
			w.WriteHeader(401)
			w.Write([]byte(`{"errors":[{"status":"401"}]}`))
			return
		}
		switch r.URL.Path {
		case "/storefronts/us":
			w.Write([]byte(`{"data":[{"id":"us"}]}`))
		case "/me/storefront":
			if r.Header.Get("Media-User-Token") != wantMUT {
				w.WriteHeader(403)
				w.Write([]byte(`{"errors":[{"status":"403"}]}`))
				return
			}
			w.Write([]byte(`{"data":[{"id":"tw"}]}`))
		default:
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CAPY_APPLE_API_BASE", srv.URL)
	return &hits
}

// assertAppleNotPersisted:失敗路徑的共用斷言——keychain 兩顆 token 與 config storefront
// 都不該被寫(all-or-nothing persistence)。只適用於從乾淨狀態(clearAppleTokens)出發的測試;
// 「既有 token 沒被覆寫」這種從非空狀態出發的情境,各測試自行比對舊值。
func assertAppleNotPersisted(t *testing.T) {
	t.Helper()
	if _, err := secret.Get(apple.KeyDeveloperToken); !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("developer token 不應被寫入:err=%v", err)
	}
	if _, err := secret.Get(apple.KeyMusicUserToken); !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("user token 不應被寫入:err=%v", err)
	}
	cfg, _ := config.Load()
	if cfg.AppleStorefront != "" {
		t.Errorf("storefront 不應被寫入:%q", cfg.AppleStorefront)
	}
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

// apple 從本 task 起是受支援的 provider(見下方 TestAuthLoginApple*),
// 故不支援清單改測 google(P3 才進場)。
func TestAuthLoginUnsupportedProvider(t *testing.T) {
	setCLITestConfig(t)
	if _, err := runCLI(t, "auth", "login", "google"); err == nil || !strings.Contains(err.Error(), "P3") {
		t.Fatalf("google 應提示 P3:%v", err)
	}
}

func TestAuthLoginAppleEnvPersists(t *testing.T) {
	clearAppleTokens(t)
	exp := time.Now().Add(24 * time.Hour)
	dev := fakeJWT(t, exp)
	appleServer(t, dev, "MUT1")
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "Bearer "+dev)
	t.Setenv("CAPY_APPLE_USER_TOKEN", "MUT1")

	out, err := runCLI(t, "auth", "login", "apple", "--i-understand")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "登入完成") || !strings.Contains(out, "tw") {
		t.Errorf("輸出:%q", out)
	}
	raw, err := secret.Get(apple.KeyDeveloperToken)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Token string `json:"token"`
		Exp   int64  `json:"exp"`
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.Token != dev {
		t.Errorf("keychain token 不應含 Bearer 前綴:%q", c.Token)
	}
	if c.Exp != exp.Unix() {
		t.Errorf("keychain exp = %d, want %d", c.Exp, exp.Unix())
	}
	if user, err := secret.Get(apple.KeyMusicUserToken); err != nil || user != "MUT1" {
		t.Errorf("user token = (%q, %v)", user, err)
	}
	cfg, _ := config.Load()
	if cfg.AppleStorefront != "tw" {
		t.Errorf("storefront = %q", cfg.AppleStorefront)
	}
}

func TestAuthLoginAppleWithoutIUnderstandRefused(t *testing.T) {
	clearAppleTokens(t)
	exp := time.Now().Add(24 * time.Hour)
	dev := fakeJWT(t, exp)
	hits := appleServer(t, dev, "MUT1")
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "Bearer "+dev)
	t.Setenv("CAPY_APPLE_USER_TOKEN", "MUT1")

	_, err := runCLI(t, "auth", "login", "apple")
	if err == nil || !strings.Contains(err.Error(), "--i-understand") || !strings.Contains(err.Error(), "非 Apple 官方支援") {
		t.Fatalf("應拒絕並在指令內帶聲明:%v", err)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("不該打網路,hits = %d", n)
	}
	assertAppleNotPersisted(t)
}

func TestAuthLoginAppleNonTTYMissingEnv(t *testing.T) {
	clearAppleTokens(t)
	hits := appleServer(t, "whatever", "whatever")
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "")
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")
	origTTY := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = origTTY })

	_, err := runCLI(t, "auth", "login", "apple")
	if err == nil || !strings.Contains(err.Error(), "CAPY_APPLE_DEVELOPER_TOKEN") {
		t.Fatalf("應提示設定 env:%v", err)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("hits = %d, want 0", n)
	}
	assertAppleNotPersisted(t)
}

func TestAuthLoginAppleExpiredJWTRejectedBeforeNetwork(t *testing.T) {
	clearAppleTokens(t)
	exp := time.Now().Add(-1 * time.Hour)
	dev := fakeJWT(t, exp)
	hits := appleServer(t, dev, "MUT1")
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", dev)
	t.Setenv("CAPY_APPLE_USER_TOKEN", "MUT1")

	_, err := runCLI(t, "auth", "login", "apple", "--i-understand")
	if err == nil || !strings.Contains(err.Error(), "已於") || !strings.Contains(err.Error(), "過期") {
		t.Fatalf("應回過期訊息:%v", err)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("hits = %d, want 0", n)
	}
	assertAppleNotPersisted(t)
}

func TestAuthLoginAppleMalformedDevToken(t *testing.T) {
	clearAppleTokens(t)
	hits := appleServer(t, "whatever", "MUT1")
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "not-a-jwt")
	t.Setenv("CAPY_APPLE_USER_TOKEN", "MUT1")

	_, err := runCLI(t, "auth", "login", "apple", "--i-understand")
	if err == nil || !strings.Contains(err.Error(), "developer token") {
		t.Fatalf("應回 developer token 格式錯誤:%v", err)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("hits = %d, want 0", n)
	}
	assertAppleNotPersisted(t)
}

func TestAuthLoginApplePreflight401(t *testing.T) {
	clearAppleTokens(t)
	exp := time.Now().Add(24 * time.Hour)
	dev := fakeJWT(t, exp)
	appleServer(t, "OTHER-DEV", "MUT1") // server 認的 dev 與送進來的不同 → 一律 401
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", dev)
	t.Setenv("CAPY_APPLE_USER_TOKEN", "MUT1")

	_, err := runCLI(t, "auth", "login", "apple", "--i-understand")
	if err == nil || !strings.Contains(err.Error(), "developer token") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("應回 developer token 401:%v", err)
	}
	assertAppleNotPersisted(t)
}

func TestAuthLoginAppleStorefront403DoesNotPersistDevToken(t *testing.T) {
	clearAppleTokens(t)
	exp := time.Now().Add(24 * time.Hour)
	dev := fakeJWT(t, exp)
	appleServer(t, dev, "MUTgood") // preflight 過,但送出的 user token 錯
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", dev)
	t.Setenv("CAPY_APPLE_USER_TOKEN", "MUTbad")

	_, err := runCLI(t, "auth", "login", "apple", "--i-understand")
	if err == nil || !strings.Contains(err.Error(), "user token") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("應回 user token 403:%v", err)
	}
	assertAppleNotPersisted(t)
}

func TestAuthLoginAppleOnlyDevTokenKeepsUserToken(t *testing.T) {
	setupAppleTokens(t) // 預放 dev + user "MUT0"
	exp := time.Now().Add(48 * time.Hour)
	newDev := fakeJWT(t, exp)
	appleServer(t, newDev, "MUT0") // 只給新 dev,應以既有 MUT0 打 storefront
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", newDev)
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")

	out, err := runCLI(t, "auth", "login", "apple", "--i-understand")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "登入完成") {
		t.Errorf("輸出:%q", out)
	}
	raw, err := secret.Get(apple.KeyDeveloperToken)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(raw), &c)
	if c.Token != newDev {
		t.Errorf("dev token 應更新為新 JWT:%q", c.Token)
	}
	if user, err := secret.Get(apple.KeyMusicUserToken); err != nil || user != "MUT0" {
		t.Errorf("user token 應仍是 MUT0:(%q, %v)", user, err)
	}
}

func TestAuthLoginAppleOnlyDevTokenWithDeadStoredUser(t *testing.T) {
	origDev := setupAppleTokens(t) // 預放 dev + user "MUT0"
	exp := time.Now().Add(48 * time.Hour)
	newDev := fakeJWT(t, exp)
	appleServer(t, newDev, "OTHER") // 既有 MUT0 對這台 server 而言已失效
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", newDev)
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")

	_, err := runCLI(t, "auth", "login", "apple", "--i-understand")
	if err == nil || !strings.Contains(err.Error(), "既有 user token") {
		t.Fatalf("應提示既有 user token 失效:%v", err)
	}
	raw, err := secret.Get(apple.KeyDeveloperToken)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(raw), &c)
	if c.Token != origDev {
		t.Errorf("dev token 不應更新,應仍是 setup 那顆:got %q want %q", c.Token, origDev)
	}
}

func TestAuthLoginAppleOnlyDevTokenWithoutStoredUserErrors(t *testing.T) {
	clearAppleTokens(t)
	exp := time.Now().Add(24 * time.Hour)
	dev := fakeJWT(t, exp)
	hits := appleServer(t, dev, "")
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", dev)
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")

	_, err := runCLI(t, "auth", "login", "apple", "--i-understand")
	if err == nil || !strings.Contains(err.Error(), "media-user-token") {
		t.Fatalf("應提示補 media-user-token:%v", err)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("hits = %d, want 0", n)
	}
	assertAppleNotPersisted(t)
}

func TestAuthLoginAppleFlagsWork(t *testing.T) {
	clearAppleTokens(t)
	exp := time.Now().Add(24 * time.Hour)
	dev := fakeJWT(t, exp)
	appleServer(t, dev, "MUT1")

	out, err := runCLI(t, "auth", "login", "apple",
		"--developer-token", "Bearer "+dev, "--user-token", "MUT1", "--i-understand")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "登入完成") || !strings.Contains(out, "tw") {
		t.Errorf("輸出:%q", out)
	}
	if user, err := secret.Get(apple.KeyMusicUserToken); err != nil || user != "MUT1" {
		t.Errorf("user token = (%q, %v)", user, err)
	}
}

// TestAuthLoginAppleTTYRunsWizard:TTY 且無 flag/env token → 走精靈,不需 --i-understand
// (揭露頁本身就是同意動作)。落地行為應與 TestAuthLoginAppleEnvPersists 相同。
func TestAuthLoginAppleTTYRunsWizard(t *testing.T) {
	clearAppleTokens(t)
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "") // 避免真實環境變數洩漏,擠掉 dev == "" 判斷
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")
	exp := time.Now().Add(24 * time.Hour)
	dev := fakeJWT(t, exp)
	appleServer(t, dev, "MUT1")

	origTTY := stdinIsTTY
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdinIsTTY = origTTY })

	origConfirm := confirmAppleDisclosure
	confirmed := 0
	confirmAppleDisclosure = func() error {
		confirmed++
		return nil
	}
	t.Cleanup(func() { confirmAppleDisclosure = origConfirm })

	origInputs := runAppleWizardInputs
	runAppleWizardInputs = func(hasUser bool) (string, string, error) {
		if hasUser {
			t.Error("hasUser = true, want false(clearAppleTokens 後不該有既有 user token)")
		}
		return dev, "MUT1", nil
	}
	t.Cleanup(func() { runAppleWizardInputs = origInputs })

	out, err := runCLI(t, "auth", "login", "apple")
	if err != nil {
		t.Fatal(err)
	}
	if confirmed != 1 {
		t.Errorf("confirmAppleDisclosure 呼叫次數 = %d, want 1", confirmed)
	}
	if !strings.Contains(out, "登入完成") || !strings.Contains(out, "tw") {
		t.Errorf("輸出:%q", out)
	}
	raw, err := secret.Get(apple.KeyDeveloperToken)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Token string `json:"token"`
		Exp   int64  `json:"exp"`
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.Token != dev {
		t.Errorf("keychain token = %q, want %q", c.Token, dev)
	}
	if c.Exp != exp.Unix() {
		t.Errorf("keychain exp = %d, want %d", c.Exp, exp.Unix())
	}
	if user, err := secret.Get(apple.KeyMusicUserToken); err != nil || user != "MUT1" {
		t.Errorf("user token = (%q, %v)", user, err)
	}
	cfg, _ := config.Load()
	if cfg.AppleStorefront != "tw" {
		t.Errorf("storefront = %q", cfg.AppleStorefront)
	}
}

// TestAuthLoginAppleTTYWizardOnlyDev:已有 user token 時,精靈回傳空字串 user
// (「只更新 developer token」)→ 沿用既有 MUT0 打 storefront,MUT0 應維持不變。
func TestAuthLoginAppleTTYWizardOnlyDev(t *testing.T) {
	setupAppleTokens(t)                        // 預放 dev + user "MUT0"
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "") // 避免真實環境變數洩漏,擠掉 dev == "" 判斷
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")
	exp := time.Now().Add(48 * time.Hour)
	newDev := fakeJWT(t, exp)
	appleServer(t, newDev, "MUT0")

	origTTY := stdinIsTTY
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdinIsTTY = origTTY })

	origConfirm := confirmAppleDisclosure
	confirmAppleDisclosure = func() error { return nil }
	t.Cleanup(func() { confirmAppleDisclosure = origConfirm })

	origInputs := runAppleWizardInputs
	runAppleWizardInputs = func(hasUser bool) (string, string, error) {
		if !hasUser {
			t.Error("hasUser = false, want true(setupAppleTokens 已放 user token)")
		}
		return newDev, "", nil
	}
	t.Cleanup(func() { runAppleWizardInputs = origInputs })

	out, err := runCLI(t, "auth", "login", "apple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "登入完成") {
		t.Errorf("輸出:%q", out)
	}
	raw, err := secret.Get(apple.KeyDeveloperToken)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(raw), &c)
	if c.Token != newDev {
		t.Errorf("dev token 應更新為新 JWT:%q", c.Token)
	}
	if user, err := secret.Get(apple.KeyMusicUserToken); err != nil || user != "MUT0" {
		t.Errorf("user token 應仍是 MUT0:(%q, %v)", user, err)
	}
}

// TestAuthLoginAppleTTYDisclosureDeclined:揭露頁不同意 → 精靈輸入階段不呼叫、不打網路、不落地
// (CLAUDE.md:揭露不可跳過,拒絕必須在任何輸入頁與網路呼叫之前結束)。
func TestAuthLoginAppleTTYDisclosureDeclined(t *testing.T) {
	clearAppleTokens(t)
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "") // 避免真實環境變數洩漏,擠掉 dev == "" 判斷
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")
	hits := appleServer(t, "whatever", "whatever")

	origTTY := stdinIsTTY
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdinIsTTY = origTTY })

	origConfirm := confirmAppleDisclosure
	confirmAppleDisclosure = func() error { return errors.New("已取消(未同意聲明)") }
	t.Cleanup(func() { confirmAppleDisclosure = origConfirm })

	origInputs := runAppleWizardInputs
	inputsCalled := false
	runAppleWizardInputs = func(hasUser bool) (string, string, error) {
		inputsCalled = true
		return "", "", nil
	}
	t.Cleanup(func() { runAppleWizardInputs = origInputs })

	_, err := runCLI(t, "auth", "login", "apple")
	if err == nil {
		t.Fatal("預期不同意聲明後回傳錯誤")
	}
	if inputsCalled {
		t.Error("不同意聲明後不該呼叫 runAppleWizardInputs")
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("hits = %d, want 0", n)
	}
	assertAppleNotPersisted(t)
}

// TestAuthLoginAppleTTYWithFlagsSkipsWizard:即使在 TTY 下,只要 token 是從 flag/env 來的,
// 仍要求 --i-understand,且完全不進精靈(兩個 seam 都不呼叫)——Task 2 的不變量,TTY 化後仍要守住。
func TestAuthLoginAppleTTYWithFlagsSkipsWizard(t *testing.T) {
	clearAppleTokens(t)
	exp := time.Now().Add(24 * time.Hour)
	dev := fakeJWT(t, exp)
	hits := appleServer(t, dev, "MUT1")

	origTTY := stdinIsTTY
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdinIsTTY = origTTY })

	origConfirm := confirmAppleDisclosure
	confirmAppleDisclosure = func() error {
		t.Error("有 flag/env token 時不該呼叫 confirmAppleDisclosure")
		return nil
	}
	t.Cleanup(func() { confirmAppleDisclosure = origConfirm })

	origInputs := runAppleWizardInputs
	runAppleWizardInputs = func(hasUser bool) (string, string, error) {
		t.Error("有 flag/env token 時不該呼叫 runAppleWizardInputs")
		return "", "", nil
	}
	t.Cleanup(func() { runAppleWizardInputs = origInputs })

	_, err := runCLI(t, "auth", "login", "apple",
		"--developer-token", dev, "--user-token", "MUT1")
	if err == nil || !strings.Contains(err.Error(), "--i-understand") {
		t.Fatalf("應要求 --i-understand:%v", err)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("hits = %d, want 0", n)
	}
	assertAppleNotPersisted(t)
}

func TestAuthStatusApple(t *testing.T) {
	setupAppleTokens(t)
	out, err := runCLI(t, "auth", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "developer token: 有效至 ") || !strings.Contains(out, "user token: 存在") {
		t.Errorf("輸出:%q", out)
	}

	expired := time.Now().Add(-1 * time.Hour)
	if err := apple.SaveDeveloperToken(fakeJWT(t, expired), expired); err != nil {
		t.Fatal(err)
	}
	out, err = runCLI(t, "auth", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已於") || !strings.Contains(out, "過期") {
		t.Errorf("過期輸出:%q", out)
	}

	// 用 logout 清掉(順便覆蓋 apple logout 路徑),而非直接戳 keychain。
	if _, err := runCLI(t, "auth", "logout", "apple"); err != nil {
		t.Fatal(err)
	}
	out, err = runCLI(t, "auth", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "developer token: 不存在(執行 capy auth login apple)") {
		t.Errorf("清掉後輸出:%q", out)
	}
}

// TestNewAppleProviderNeedsLogin:未登入(keychain 空)→「尚未登入」;
// developer token 過期 →訊息含「過期」與下一步指示。
func TestNewAppleProviderNeedsLogin(t *testing.T) {
	clearAppleTokens(t)
	if _, err := newProvider(context.Background(), "apple"); err == nil || !strings.Contains(err.Error(), "尚未登入") {
		t.Fatalf("清空應提示尚未登入:%v", err)
	}

	expired := time.Now().Add(-1 * time.Hour)
	if err := apple.SaveDeveloperToken(fakeJWT(t, expired), expired); err != nil {
		t.Fatal(err)
	}
	_, err := newProvider(context.Background(), "apple")
	if err == nil || !strings.Contains(err.Error(), "過期") || !strings.Contains(err.Error(), "capy auth login apple") {
		t.Fatalf("過期應提示重新登入:%v", err)
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
