package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

func fakeAppleAuthorize(t *testing.T, mut string) func(context.Context, string, func(string) error) (string, error) {
	t.Helper()
	return func(_ context.Context, devToken string, _ func(string) error) (string, error) {
		if !strings.HasPrefix(devToken, "eyJ") {
			t.Errorf("應以簽好的 developer token 呼叫橋接:%q", devToken)
		}
		return mut, nil
	}
}

func setupAppleBYO(t *testing.T) {
	t.Helper()
	setCLITestConfig(t)
	_ = secret.Delete(apple.KeyDeveloperToken)
	_ = secret.Delete(apple.KeyMusicUserToken)
	t.Setenv("CAPY_APPLE_P8_PATH", writeTestP8(t)) // AuthKey_TESTKID.p8(debug_test 的 helper)
	t.Setenv("CAPY_APPLE_TEAM_ID", "TEAM1")
	t.Setenv("CAPY_APPLE_KID", "")
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

func TestAuthLoginAppleStoresMUTAndStorefront(t *testing.T) {
	setupAppleBYO(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/storefronts/us": // preflight(login 開瀏覽器前的 dev token 驗證,不帶 MUT)
			w.Write([]byte(`{"data":[{"id":"us"}]}`))
		case "/me/storefront":
			if r.Header.Get("Music-User-Token") != "MUT1" {
				t.Errorf("storefront 請求錯誤:%s %v", r.URL.Path, r.Header)
			}
			w.Write([]byte(`{"data":[{"id":"tw"}]}`))
		default:
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CAPY_APPLE_API_BASE", srv.URL) // 測試用 base 覆寫(見 Step 3)
	origA := appleAuthorize
	appleAuthorize = fakeAppleAuthorize(t, "MUT1")
	t.Cleanup(func() { appleAuthorize = origA })

	out, err := runCLI(t, "auth", "login", "apple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Apple Music 授權完成") {
		t.Errorf("輸出:%q", out)
	}
	if mut, err := secret.Get(apple.KeyMusicUserToken); err != nil || mut != "MUT1" {
		t.Errorf("MUT 應入 keychain:(%q, %v)", mut, err)
	}
	cfg, _ := config.Load()
	if cfg.AppleStorefront != "tw" || len(cfg.InstallID) != 32 {
		t.Errorf("config 應存 storefront 與 install_id:%+v", cfg)
	}
}

func TestAuthLoginAppleFailureDoesNotPersist(t *testing.T) {
	setupAppleBYO(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"us"}]}`)) // preflight 通過,才輪到橋接失敗
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CAPY_APPLE_API_BASE", srv.URL)
	origA := appleAuthorize
	appleAuthorize = func(context.Context, string, func(string) error) (string, error) {
		return "", errors.New("使用者取消")
	}
	t.Cleanup(func() { appleAuthorize = origA })
	if _, err := runCLI(t, "auth", "login", "apple"); err == nil {
		t.Fatal("橋接失敗應回錯")
	}
	if _, err := secret.Get(apple.KeyMusicUserToken); !errors.Is(err, secret.ErrNotFound) {
		t.Error("失敗不應留下 MUT")
	}
	cfg, _ := config.Load()
	if cfg.AppleStorefront != "" {
		t.Error("失敗不應存 storefront")
	}
}

// appleLoginFixture:C-1(storefronts/us preflight)+ /me/storefront 都通過的最小 fixture。
func appleLoginFixture(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/storefronts/us":
			w.Write([]byte(`{"data":[{"id":"us"}]}`))
		case "/me/storefront":
			w.Write([]byte(`{"data":[{"id":"tw"}]}`))
		default:
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestAuthLoginAppleClearsStaleDevTokenCache:keychain 裡即使有一顆「看起來還沒過期」的
// developer token 快取,login 也要整條鏈重來——否則被 Apple 端撤銷/輪替的快取值會一直被
// 信任,使用者永遠修不好(review 發現的洞)。
func TestAuthLoginAppleClearsStaleDevTokenCache(t *testing.T) {
	setupAppleBYO(t)
	stale, _ := json.Marshal(struct {
		Token string `json:"token"`
		Exp   int64  `json:"exp"`
	}{Token: "stale", Exp: time.Now().Add(24 * time.Hour).Unix()})
	if err := secret.Set(apple.KeyDeveloperToken, string(stale)); err != nil {
		t.Fatal(err)
	}
	srv := appleLoginFixture(t)
	t.Setenv("CAPY_APPLE_API_BASE", srv.URL)
	origA := appleAuthorize
	appleAuthorize = fakeAppleAuthorize(t, "MUT1") // 內部斷言收到的 dev token 以 "eyJ" 開頭,不是 "stale"
	t.Cleanup(func() { appleAuthorize = origA })

	if _, err := runCLI(t, "auth", "login", "apple"); err != nil {
		t.Fatal(err)
	}
	raw, err := secret.Get(apple.KeyDeveloperToken)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.Token == "stale" {
		t.Errorf("login 應清掉壞快取並重簽,keychain 仍是 stale:%q", raw)
	}
}

// TestAuthLoginApplePreflightFailsFast:preflight 沒過就不該開瀏覽器——省一次 180s 的等待。
func TestAuthLoginApplePreflightFailsFast(t *testing.T) {
	setupAppleBYO(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/storefronts/us" {
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errors":[{"status":"401","title":"Unauthorized"}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CAPY_APPLE_API_BASE", srv.URL)
	origA := appleAuthorize
	appleAuthorize = func(context.Context, string, func(string) error) (string, error) {
		t.Error("preflight 失敗不該呼叫 appleAuthorize(不該開瀏覽器)")
		return "", errors.New("不應被呼叫")
	}
	t.Cleanup(func() { appleAuthorize = origA })

	_, err := runCLI(t, "auth", "login", "apple")
	if err == nil || !strings.Contains(err.Error(), "developer token 被 Apple 拒絕") {
		t.Fatalf("preflight 失敗應快速回錯:%v", err)
	}
}

// TestAuthLoginAppleStorefrontFailureDoesNotPersist:preflight 過、橋接也過,
// 但 storefront 端本身出錯(500)—— MUT/storefront 都不該落地(T7 review)。
func TestAuthLoginAppleStorefrontFailureDoesNotPersist(t *testing.T) {
	setupAppleBYO(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/storefronts/us":
			w.Write([]byte(`{"data":[{"id":"us"}]}`))
		case "/me/storefront":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CAPY_APPLE_API_BASE", srv.URL)
	origA := appleAuthorize
	appleAuthorize = fakeAppleAuthorize(t, "MUT1")
	t.Cleanup(func() { appleAuthorize = origA })

	if _, err := runCLI(t, "auth", "login", "apple"); err == nil {
		t.Fatal("storefront 失敗應回錯")
	}
	if _, err := secret.Get(apple.KeyMusicUserToken); !errors.Is(err, secret.ErrNotFound) {
		t.Error("失敗不應留下 MUT")
	}
	cfg, _ := config.Load()
	if cfg.AppleStorefront != "" {
		t.Error("失敗不應存 storefront")
	}
}

func TestAuthStatusAndLogoutApple(t *testing.T) {
	setupAppleBYO(t)
	_ = secret.Set(apple.KeyMusicUserToken, "MUT1")
	_ = config.Save(&config.Config{AppleStorefront: "tw"})
	out, err := runCLI(t, "auth", "status")
	if err != nil || !strings.Contains(out, "apple:") || !strings.Contains(out, "storefront: tw") || !strings.Contains(out, "user token: 存在") {
		t.Fatalf("status 輸出:%q err=%v", out, err)
	}
	if _, err := runCLI(t, "auth", "logout", "apple"); err != nil {
		t.Fatal(err)
	}
	if _, err := secret.Get(apple.KeyMusicUserToken); !errors.Is(err, secret.ErrNotFound) {
		t.Error("logout 應刪 MUT")
	}
}

func TestNewAppleProviderNeedsLogin(t *testing.T) {
	setupAppleBYO(t)
	if _, err := newProvider(context.Background(), "apple"); err == nil || !strings.Contains(err.Error(), "capy auth login apple") {
		t.Fatalf("無 MUT 應提示 login apple:%v", err)
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
