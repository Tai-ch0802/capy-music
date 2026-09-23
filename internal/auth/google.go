package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// keychain 鍵。google.client_secret 只在 BYO(使用者自建 client)時存在;內建 client 的 secret 在 binary 裡(決策 9)。
const (
	KeyGoogleToken        = "google.token"
	KeyGoogleClientSecret = "google.client_secret"
)

// GoogleScopes:CLAUDE.md 硬約束——逐字這三個、只有這三個(全是 non-sensitive,不需 verification)。
// 任何人提議加 Gmail scope 都要擋:會觸發 restricted scope 的 CASA 資安評估。
var GoogleScopes = []string{
	"openid",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/drive.appdata",
}

// GoogleEndpoint 手寫,不 import golang.org/x/oauth2/google(它會拉進 cloud.google.com/go/compute/metadata)。
// 可變的套件變數:測試把 TokenURL 指向 httptest。
var GoogleEndpoint = oauth2.Endpoint{
	AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:  "https://oauth2.googleapis.com/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

// BuiltinGoogleClientID / BuiltinGoogleClientSecret:專案自己的 Google Desktop client,發行時注入(決策 9):
//
//	-ldflags "-X github.com/Tai-ch0802/capy-music/internal/auth.BuiltinGoogleClientID=<id> \
//	          -X github.com/Tai-ch0802/capy-music/internal/auth.BuiltinGoogleClientSecret=<secret>"
//
// 絕不 commit 進 repo(Google 政策明文禁止);go install 建出來的 binary 兩者為空 → auth login google 走 BYO 精靈。
// 這是 CLAUDE.md「憑證只進 keychain」的唯一放寬,只涵蓋 app 自身識別、不涵蓋任何使用者憑證。
var BuiltinGoogleClientID, BuiltinGoogleClientSecret string

// GoogleClient:這次登入 / refresh 用的 client。Secret 可為空(Q1:Desktop client 的 secret 文件標 Optional;
// 有就送——oauth2 的行為),G-0 驗收才知道 Google 實際要不要。
type GoogleClient struct {
	ID, Secret string
	Builtin    bool
}

var (
	// ErrGoogleScope:Google 回傳的授權少了 drive.appdata(使用者在同意畫面取消勾選)。不落地,提示重登。
	ErrGoogleScope = i18n.Errorf("auth.err.google_scope")
	// ErrGoogleGrant:refresh token 失效(invalid_grant)。訊息由 explainGoogleGrant 依 token 年齡補上最可能的原因。
	ErrGoogleGrant = i18n.Errorf("auth.err.google_grant")
)

func googleOAuthConfig(c GoogleClient, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ID,
		ClientSecret: c.Secret,
		RedirectURL:  redirectURL,
		Endpoint:     GoogleEndpoint,
		Scopes:       GoogleScopes,
	}
}

// googleAuthOptions:每次登入都帶(不只首次)——沒有 prompt=consent 的話,Google 在重複同意時不發 refresh token。
func googleAuthOptions(verifier string) []oauth2.AuthCodeOption {
	return []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier), oauth2.AccessTypeOffline, oauth2.ApprovalForce}
}

// LoginGoogle:Authorization Code + PKCE + loopback(動態 port;Google Desktop client 允許任意 loopback port)。
// 成功後完整 token 記錄已在 keychain(照 T1 的 token store;Google 的 RT 不輪替,寫回路徑相同)。
// 回傳 id_token 裡的 email(只用來顯示「登入的是哪個帳號」,不驗簽)。
func LoginGoogle(ctx context.Context, c GoogleClient, openBrowser func(string) error) (*oauth2.Token, string, error) {
	state, err := NewState()
	if err != nil {
		return nil, "", err
	}
	lb, err := NewLoopback(0, state)
	if err != nil {
		return nil, "", err
	}
	defer lb.Close()
	conf := googleOAuthConfig(c, lb.BaseURL()+"/callback")
	verifier := oauth2.GenerateVerifier()
	lb.Start()
	authURL := conf.AuthCodeURL(state, googleAuthOptions(verifier)...)
	fmt.Fprintln(LoginStderr, i18n.T("auth.loopback.open_manually", "url", authURL))
	if err := openBrowser(authURL); err != nil {
		fmt.Fprintln(LoginStderr, i18n.T("auth.loopback.browser_failed", "err", err))
	}
	vals, err := lb.Wait(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, "", i18n.Errorf("auth.google.err.timeout")
		}
		return nil, "", i18n.Errorf("auth.loopback.err.wait", "err", err)
	}
	if e := vals.Get("error"); e != "" {
		return nil, "", i18n.Errorf("auth.loopback.err.denied", "reason", e)
	}
	tok, err := conf.Exchange(ctx, vals.Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, "", i18n.Errorf("auth.loopback.err.exchange", "err", explainGoogleClient(err))
	}
	if tok.RefreshToken == "" {
		return nil, "", i18n.Errorf("auth.google.err.no_refresh_token")
	}
	// downscope 拒絕:使用者可以在同意畫面取消個別 scope;少了 drive.appdata 這個 token 對本工具沒用,不落地。
	if scope, _ := tok.Extra("scope").(string); !hasScope(scope, GoogleScopes[2]) {
		return nil, "", i18n.Errorf("auth.google.err.scope_granted", "err", ErrGoogleScope, "scope", strconv.Quote(scope))
	}
	email := emailFromIDToken(tok)
	unlock, err := lockFile(ctx, KeyGoogleToken+".lock")
	if err != nil {
		return nil, "", err
	}
	defer unlock()
	if err := SaveToken(KeyGoogleToken, tok); err != nil { // storedToken 沒有 id_token 欄位:不落地
		return nil, "", i18n.Errorf("auth.err.keychain_write", "err", err)
	}
	return tok, email, nil
}

func hasScope(granted, want string) bool {
	for _, s := range strings.Fields(granted) {
		if s == want {
			return true
		}
	}
	return false
}

// emailFromIDToken:只解 payload、不驗簽——這個值只用來顯示,授權真偽由 token 端點決定。取不到回空字串。
func emailFromIDToken(tok *oauth2.Token) string {
	idt, _ := tok.Extra("id_token").(string)
	parts := strings.Split(idt, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return ""
	}
	return claims.Email
}

// ErrGoogleClient:client id / secret 不符(invalid_client)。精靈允許 secret 留空、logout 會刪 secret、
// Cloud Console 也能重新產生 secret——這三條路都會走到這裡,原始訊息完全看不出下一步。
var ErrGoogleClient = i18n.Errorf("auth.err.google_client")

// explainGoogleClient:invalid_client → 講明下一步;其他錯誤原樣回傳。
func explainGoogleClient(err error) error {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) || re.ErrorCode != "invalid_client" {
		return err
	}
	return i18n.Errorf("auth.google.err.client_hint", "err", ErrGoogleClient, "detail", re.ErrorDescription)
}

// explainGoogleGrant:invalid_grant 依 refresh token 年齡歸因(invalid_client 交給 explainGoogleClient)。Testing 狀態的 client 發的 RT 7 天過期,
// 而且 drive.appdata 不在豁免清單——BYO 使用者漏按 Publish app 就會在第 7 天莫名被登出,列為第一嫌疑。
func explainGoogleGrant(err error, issuedAt time.Time) error {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) || re.ErrorCode != "invalid_grant" {
		return explainGoogleClient(err)
	}
	if !issuedAt.IsZero() && now().Sub(issuedAt) < 8*24*time.Hour {
		return i18n.Errorf("auth.google.err.grant_recent", "err", ErrGoogleGrant, "age", now().Sub(issuedAt).Round(time.Hour), "detail", re.ErrorDescription)
	}
	if issuedAt.IsZero() { // 掛鉤是 provider-neutral 的:舊格式或別的 provider 可能沒有 issued_at,零值不要印成 0001-01-01
		return i18n.Errorf("auth.google.err.grant_old", "err", ErrGoogleGrant, "detail", re.ErrorDescription)
	}
	return i18n.Errorf("auth.google.err.grant_old_dated", "err", ErrGoogleGrant, "date", issuedAt.Format("2006-01-02"), "detail", re.ErrorDescription)
}

// GoogleTokenSource:keychain 為後盾的 token source(跨程序檔案鎖,與 Spotify 同一套)。
// 沒登入時原樣透傳 secret.ErrNotFound。
func GoogleTokenSource(ctx context.Context, c GoogleClient) (*TokenSource, error) {
	ts, err := NewTokenSource(ctx, googleOAuthConfig(c, ""), KeyGoogleToken)
	if err != nil {
		return nil, err
	}
	ts.explain = explainGoogleGrant
	return ts, nil
}

// GoogleStored 回傳 keychain 內的 Google token(auth status 顯示到期時間用;不打網路)。沒有時 secret.ErrNotFound。
func GoogleStored() (*oauth2.Token, error) { return LoadToken(KeyGoogleToken) }

// LogoutGoogle 在鎖內刪 token 與 BYO 的 client secret(計畫 T3;secret 一起刪,BYO 使用者下次要重貼——
// 與 Spotify 保留 client_id 不同,那邊 client_id 非機密且在 config)。ErrNotFound 視為已經沒有。
func LogoutGoogle(ctx context.Context) error {
	unlock, err := lockFile(ctx, KeyGoogleToken+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	for _, k := range []string{KeyGoogleToken, KeyGoogleClientSecret} {
		if err := secret.Delete(k); err != nil && !errors.Is(err, secret.ErrNotFound) {
			return err
		}
	}
	return nil
}
