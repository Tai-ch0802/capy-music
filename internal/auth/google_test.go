package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/secret"
)

func swapGoogleTokenURL(t *testing.T, tokenURL string) {
	t.Helper()
	orig := GoogleEndpoint
	GoogleEndpoint.TokenURL = tokenURL
	t.Cleanup(func() { GoogleEndpoint = orig })
}

func fakeIDToken(email string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(`{"email":"`+email+`","sub":"1"}`)) + ".sig"
}

// 三個 scope 逐字、只有三個(CLAUDE.md 硬約束);slice 與授權 URL 的 scope 參數都要逐字比,只比 slice 的話 Config 組錯仍會漏。
func TestGoogleScopesExactAndAuthURLParams(t *testing.T) {
	want := []string{"openid", "https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/drive.appdata"}
	if len(GoogleScopes) != 3 || GoogleScopes[0] != want[0] || GoogleScopes[1] != want[1] || GoogleScopes[2] != want[2] {
		t.Fatalf("GoogleScopes = %v, want %v", GoogleScopes, want)
	}
	conf := googleOAuthConfig(GoogleClient{ID: "cid.apps.googleusercontent.com", Secret: "sec"}, "http://127.0.0.1:43210/callback")
	u, err := url.Parse(conf.AuthCodeURL("st1", googleAuthOptions("verifier-xyz")...))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("scope") != strings.Join(want, " ") {
		t.Errorf("scope 參數 = %q", q.Get("scope"))
	}
	if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Errorf("授權 URL 缺 offline / consent / PKCE:%s", u.RawQuery)
	}
	if q.Get("redirect_uri") != "http://127.0.0.1:43210/callback" || q.Get("client_id") != "cid.apps.googleusercontent.com" || u.Host != "accounts.google.com" {
		t.Errorf("redirect / client / host 錯:%s", u.String())
	}
	if strings.Contains(u.String(), "sec") {
		t.Error("client secret 不得出現在授權 URL")
	}
}

func googleTokenServer(t *testing.T, scope, email string, assertSecret string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code_verifier") == "" {
			t.Errorf("token 請求缺 PKCE 參數:%v", r.PostForm)
		}
		if got := r.PostForm.Get("client_secret"); got != assertSecret {
			t.Errorf("client_secret = %q, want %q(Q1:有設定就送)", got, assertSecret)
		}
		w.Header().Set("Content-Type", "application/json")
		body := `{"access_token":"at1","token_type":"Bearer","expires_in":3599,"refresh_token":"rt1","scope":"` + scope + `"`
		if email != "" {
			body += `,"id_token":"` + fakeIDToken(email) + `"`
		}
		w.Write([]byte(body + `}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLoginGoogleStoresTokenWithoutIDTokenAndReturnsEmail(t *testing.T) {
	setTokenTest(t)
	srv := googleTokenServer(t, strings.Join(GoogleScopes, " "), "tai@example.com", "sec")
	swapGoogleTokenURL(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, email, err := LoginGoogle(ctx, GoogleClient{ID: "cid", Secret: "sec"}, fakeAuthBrowser(t, "code1"))
	if err != nil || tok.AccessToken != "at1" || email != "tai@example.com" {
		t.Fatalf("(%+v, %q, %v)", tok, email, err)
	}
	raw, err := secret.Get(KeyGoogleToken)
	if err != nil || !strings.Contains(raw, `"refresh_token":"rt1"`) {
		t.Fatalf("keychain 應有完整 token 記錄:%q %v", raw, err)
	}
	if strings.Contains(raw, "id_token") || strings.Contains(raw, "example.com") {
		t.Fatalf("id_token / email 不得進 keychain:%q", raw)
	}
}

func TestLoginGoogleRejectsDownscopeWithoutPersisting(t *testing.T) {
	setTokenTest(t)
	srv := googleTokenServer(t, "openid https://www.googleapis.com/auth/userinfo.email", "x@y", "")
	swapGoogleTokenURL(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := LoginGoogle(ctx, GoogleClient{ID: "cid"}, fakeAuthBrowser(t, "code1"))
	if !errors.Is(err, ErrGoogleScope) {
		t.Fatalf("缺 drive.appdata 應被拒:%v", err)
	}
	if _, err := secret.Get(KeyGoogleToken); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("被拒的 token 不得落地:%v", err)
	}
}

func TestLoginGoogleWithoutRefreshTokenErrors(t *testing.T) {
	setTokenTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at1","token_type":"Bearer","expires_in":3599,"scope":"` + strings.Join(GoogleScopes, " ") + `"}`))
	}))
	t.Cleanup(srv.Close)
	swapGoogleTokenURL(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := LoginGoogle(ctx, GoogleClient{ID: "cid"}, fakeAuthBrowser(t, "c")); err == nil || !strings.Contains(err.Error(), "refresh token") {
		t.Fatalf("沒有 refresh token 應報錯:%v", err)
	}
}

func TestExplainGoogleGrantByTokenAge(t *testing.T) {
	clock := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	orig := now
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = orig })
	grant := &oauth2.RetrieveError{ErrorCode: "invalid_grant", ErrorDescription: "Token has been expired or revoked."}

	young := explainGoogleGrant(grant, clock.Add(-3*24*time.Hour))
	if !errors.Is(young, ErrGoogleGrant) || !strings.Contains(young.Error(), "Publish app") {
		t.Fatalf("8 天內失效應把 Publish app 列為第一嫌疑:%v", young)
	}
	old := explainGoogleGrant(grant, clock.Add(-200*24*time.Hour))
	if !errors.Is(old, ErrGoogleGrant) || strings.Contains(old.Error(), "Publish app") || !strings.Contains(old.Error(), "6 個月") {
		t.Fatalf("老 token 失效應指向回收/撤銷:%v", old)
	}
	other := errors.New("dial tcp: timeout")
	if explainGoogleGrant(other, clock) != other {
		t.Fatal("非 invalid_grant 應原樣回傳")
	}
	if e := explainGoogleGrant(&oauth2.RetrieveError{ErrorCode: "invalid_client"}, clock); errors.Is(e, ErrGoogleGrant) {
		t.Fatal("invalid_client 不是 RT 失效")
	}
}

// refresh 時 Google 不回 refresh_token → 沿用舊的寫回;invalid_grant → 經 explain 變成 ErrGoogleGrant。
func TestGoogleTokenSourceRefreshAndGrantError(t *testing.T) {
	setTokenTest(t)
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "rt1" || r.PostForm.Get("client_secret") != "sec" {
			t.Errorf("refresh 請求形狀錯:%v", r.PostForm)
		}
		w.Header().Set("Content-Type", "application/json")
		if mode == "grant" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
			return
		}
		w.Write([]byte(`{"access_token":"at2","token_type":"Bearer","expires_in":3599}`))
	}))
	t.Cleanup(srv.Close)
	swapGoogleTokenURL(t, srv.URL)
	if err := SaveToken(KeyGoogleToken, &oauth2.Token{AccessToken: "at1", RefreshToken: "rt1", Expiry: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	ts, err := GoogleTokenSource(context.Background(), GoogleClient{ID: "cid", Secret: "sec"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ts.Token()
	if err != nil || tok.AccessToken != "at2" || tok.RefreshToken != "rt1" {
		t.Fatalf("refresh 應沿用舊 RT:%+v %v", tok, err)
	}
	if st, _ := loadStored(KeyGoogleToken); st.RefreshToken != "rt1" || st.AccessToken != "at2" {
		t.Fatalf("寫回的記錄錯:%+v", st)
	}
	mode = "grant"
	if _, err := ts.Refresh(); !errors.Is(err, ErrGoogleGrant) || !strings.Contains(err.Error(), "Publish app") {
		t.Fatalf("invalid_grant 應經 explain 歸因:%v", err)
	}
}

func TestLogoutGoogleDeletesTokenAndSecret(t *testing.T) {
	setTokenTest(t)
	_ = SaveToken(KeyGoogleToken, &oauth2.Token{AccessToken: "a", RefreshToken: "r"})
	_ = secret.Set(KeyGoogleClientSecret, "sec")
	if err := LogoutGoogle(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{KeyGoogleToken, KeyGoogleClientSecret} {
		if _, err := secret.Get(k); !errors.Is(err, secret.ErrNotFound) {
			t.Errorf("%s 應被刪:%v", k, err)
		}
	}
	if err := LogoutGoogle(context.Background()); err != nil {
		t.Fatalf("重複登出應無事:%v", err)
	}
}

func TestEmailFromIDTokenIsLenient(t *testing.T) {
	if emailFromIDToken(&oauth2.Token{}) != "" {
		t.Error("沒有 id_token 應回空")
	}
	tok := (&oauth2.Token{}).WithExtra(map[string]any{"id_token": "not.a.jwt.at.all"})
	if emailFromIDToken(tok) != "" {
		t.Error("壞 JWT 應回空,不 panic")
	}
}

func TestLoginGoogleExplainsInvalidClient(t *testing.T) {
	setTokenTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_client","error_description":"Unauthorized"}`))
	}))
	t.Cleanup(srv.Close)
	swapGoogleTokenURL(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := LoginGoogle(ctx, GoogleClient{ID: "cid"}, fakeAuthBrowser(t, "c"))
	if !errors.Is(err, ErrGoogleClient) || !strings.Contains(err.Error(), "--client-secret") {
		t.Fatalf("invalid_client 要講下一步:%v", err)
	}
	if _, err := secret.Get(KeyGoogleToken); !errors.Is(err, secret.ErrNotFound) {
		t.Fatal("失敗不得落地")
	}
	// refresh 路徑同樣要歸因(secret 輪替後 refresh 也會 invalid_client)
	e := explainGoogleGrant(&oauth2.RetrieveError{ErrorCode: "invalid_client"}, time.Time{})
	if !errors.Is(e, ErrGoogleClient) {
		t.Fatalf("explainGoogleGrant 應把 invalid_client 交給 explainGoogleClient:%v", e)
	}
}

func TestExplainGoogleGrantZeroIssuedAt(t *testing.T) {
	e := explainGoogleGrant(&oauth2.RetrieveError{ErrorCode: "invalid_grant", ErrorDescription: "x"}, time.Time{})
	if !errors.Is(e, ErrGoogleGrant) || strings.Contains(e.Error(), "0001") || strings.Contains(e.Error(), "Publish app") {
		t.Fatalf("issued_at 零值不得印成 0001 年、也不該猜 Publish:%v", e)
	}
}
