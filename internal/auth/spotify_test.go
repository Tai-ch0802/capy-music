package auth

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/secret"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func swapTokenURL(t *testing.T, tokenURL string) {
	t.Helper()
	orig := spotifyEndpoint
	spotifyEndpoint.TokenURL = tokenURL
	t.Cleanup(func() { spotifyEndpoint = orig })
}

func TestSpotifyAuthURLParams(t *testing.T) {
	conf := spotifyOAuthConfig("cid123", "http://127.0.0.1:8888/callback")
	v := oauth2.GenerateVerifier()
	raw := conf.AuthCodeURL("st1", oauth2.S256ChallengeOption(v))
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "cid123" ||
		q.Get("redirect_uri") != "http://127.0.0.1:8888/callback" || q.Get("state") != "st1" {
		t.Errorf("授權 URL 參數錯誤:%s", raw)
	}
	scope := q.Get("scope")
	for _, s := range SpotifyScopes {
		if !strings.Contains(scope, s) {
			t.Errorf("缺 scope %s", s)
		}
	}
	if strings.Contains(raw, "localhost") {
		t.Error("禁用 localhost")
	}
}

// 假瀏覽器:從授權 URL 撈 state 與 redirect_uri,直接 GET callback 模擬使用者完成授權。
func fakeAuthBrowser(t *testing.T, code string) func(string) error {
	t.Helper()
	return func(authURL string) error {
		done := make(chan struct{})
		t.Cleanup(func() { <-done })
		go func() {
			defer close(done)
			u, err := url.Parse(authURL)
			if err != nil {
				t.Error(err)
				return
			}
			q := u.Query()
			cb := q.Get("redirect_uri") + "?code=" + code + "&state=" + q.Get("state")
			resp, err := http.Get(cb)
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
		}()
		return nil
	}
}

func TestLoginSpotifyStoresRefreshToken(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code_verifier") == "" {
			t.Errorf("token 請求缺 PKCE 參數:%v", r.PostForm)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at1","token_type":"Bearer","expires_in":3600,"refresh_token":"rt1"}`))
	}))
	defer tokenSrv.Close()
	swapTokenURL(t, tokenSrv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := LoginSpotify(ctx, "cid123", fakeAuthBrowser(t, "code1"))
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at1" {
		t.Errorf("access token = %q", tok.AccessToken)
	}
	got, err := secret.Get(KeySpotifyRefreshToken)
	if err != nil || got != "rt1" {
		t.Fatalf("keychain 應存 refresh token:(%q, %v)", got, err)
	}
}

func TestLoginSpotifyDenied(t *testing.T) {
	deniedBrowser := func(authURL string) error {
		done := make(chan struct{})
		t.Cleanup(func() { <-done })
		go func() {
			defer close(done)
			u, _ := url.Parse(authURL)
			q := u.Query()
			cb := q.Get("redirect_uri") + "?error=access_denied&state=" + q.Get("state")
			resp, err := http.Get(cb)
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := LoginSpotify(ctx, "cid123", deniedBrowser); err == nil || !strings.Contains(err.Error(), "授權被拒") {
		t.Fatalf("拒絕授權應回明確錯誤,得到 %v", err)
	}
}

// Dashboard 的 Redirect URI 填錯時,Spotify 在瀏覽器顯示 INVALID_CLIENT 且永不 callback——
// fake browser 什麼都不做(不打 callback),逾時錯誤必須指向 Redirect URI 這個最可能的病灶。
func TestLoginSpotifyTimeoutMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	doNothingBrowser := func(string) error { return nil }
	_, err := LoginSpotify(ctx, "cid123", doNothingBrowser)
	if err == nil || !strings.Contains(err.Error(), "Redirect URI") {
		t.Fatalf("逾時應提示檢查 Redirect URI,得到 %v", err)
	}
}

// SSH/headless 場景 openBrowser 會失敗,但使用者仍可手動複製 stderr 印出的 URL 完成授權——
// 開瀏覽器失敗不該讓整個登入中止。
func TestLoginSpotifyBrowserOpenFailStillCompletes(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at9","token_type":"Bearer","expires_in":3600,"refresh_token":"rt9"}`))
	}))
	defer tokenSrv.Close()
	swapTokenURL(t, tokenSrv.URL)

	stderrBuf := &bytes.Buffer{}
	origStderr := loginStderr
	loginStderr = stderrBuf
	t.Cleanup(func() { loginStderr = origStderr })

	failingBrowser := func(authURL string) error {
		done := make(chan struct{})
		t.Cleanup(func() { <-done })
		go func() {
			defer close(done)
			u, err := url.Parse(authURL)
			if err != nil {
				t.Error(err)
				return
			}
			q := u.Query()
			cb := q.Get("redirect_uri") + "?code=code9&state=" + q.Get("state")
			resp, err := http.Get(cb)
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
		}()
		return errors.New("exec: \"xdg-open\": executable file not found in $PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := LoginSpotify(ctx, "cid123", failingBrowser)
	if err != nil {
		t.Fatalf("開瀏覽器失敗不應中止登入:%v", err)
	}
	if tok.AccessToken != "at9" {
		t.Errorf("access token = %q", tok.AccessToken)
	}
	out := stderrBuf.String()
	if !strings.Contains(out, "手動前往") {
		t.Errorf("應印出手動前往提示:%q", out)
	}
	if !strings.Contains(out, "accounts.spotify.com/authorize") || !strings.Contains(out, "client_id=cid123") {
		t.Errorf("應印出實際授權 URL:%q", out)
	}
	if !strings.Contains(out, "無法自動開瀏覽器") {
		t.Errorf("應印出開瀏覽器失敗訊息:%q", out)
	}
}

// ⭐ load-bearing test(CLAUDE.md 硬約束):refresh token 輪替必覆寫 keychain。
func TestPersistingTokenSourceRotates(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "rt-old" {
			t.Errorf("refresh 請求錯誤:%v", r.PostForm)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at2","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-new"}`))
	}))
	defer tokenSrv.Close()
	swapTokenURL(t, tokenSrv.URL)

	if err := secret.Set(KeySpotifyRefreshToken, "rt-old"); err != nil {
		t.Fatal(err)
	}
	ts, err := SpotifyTokenSource(context.Background(), "cid123")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at2" {
		t.Errorf("access token = %q", tok.AccessToken)
	}
	got, err := secret.Get(KeySpotifyRefreshToken)
	if err != nil || got != "rt-new" {
		t.Fatalf("輪替後 keychain 必須被覆寫:(%q, %v)", got, err)
	}
}

// 硬約束的另一半:輪替後寫入 keychain 失敗,Token() 必須失敗(否則新 token 靜默遺失=永久登出)。
func TestPersistingTokenSourceFailsWhenKeychainWriteFails(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at3","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-new2"}`))
	}))
	defer tokenSrv.Close()
	swapTokenURL(t, tokenSrv.URL)

	if err := secret.Set(KeySpotifyRefreshToken, "rt-old2"); err != nil {
		t.Fatal(err)
	}
	ts, err := SpotifyTokenSource(context.Background(), "cid123") // Get 在正常 mock 下先成功
	if err != nil {
		t.Fatal(err)
	}
	keyring.MockInitWithError(errors.New("keychain 掛了"))
	t.Cleanup(func() { keyring.MockInit() })

	if _, err := ts.Token(); err == nil || !strings.Contains(err.Error(), "keychain") {
		t.Fatalf("keychain 寫入失敗必須讓 Token() 失敗,得到 %v", err)
	}
}

func TestSpotifyTokenSourceMissingToken(t *testing.T) {
	_ = secret.Delete(KeySpotifyRefreshToken)
	if _, err := SpotifyTokenSource(context.Background(), "cid123"); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("無 refresh token 應透傳 ErrNotFound,得到 %v", err)
	}
}

func TestLoginSpotifyPortBusy(t *testing.T) {
	occupied, err := netListen8888(t)
	if err != nil {
		t.Skipf("8888 已被其他程序佔用,略過:%v", err)
	}
	defer occupied.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := LoginSpotify(ctx, "cid123", func(string) error { return nil }); err == nil || !strings.Contains(err.Error(), "8888") {
		t.Fatalf("8888 佔用應中止並指出 port,得到 %v", err)
	}
}

func netListen8888(t *testing.T) (interface{ Close() error }, error) {
	t.Helper()
	return net.Listen("tcp", "127.0.0.1:8888")
}
