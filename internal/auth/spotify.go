package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// loginStderr:LoginSpotify 印手動授權 URL / 開瀏覽器失敗訊息的目的地。測試替換點。
var loginStderr io.Writer = os.Stderr

// KeySpotifyToken 是 keychain 內完整 token 記錄(JSON,見 tokenstore.go)的鍵名。唯一真相來源。
const KeySpotifyToken = "spotify.token"

// KeySpotifyRefreshToken 是舊鍵:值是 refresh token 裸字串(非 JSON)。只剩兩個用途——
// 升級既有使用者(migrateSpotifyToken)與 logout 時清乾淨,新程式不再寫入。
const KeySpotifyRefreshToken = "spotify.refresh_token"

// SpotifyScopes:spec §4.2 逐字;一次索取 9 個的取捨見 spec §4.2 決策段。
var SpotifyScopes = []string{
	"user-read-playback-state",
	"user-modify-playback-state",
	"user-read-currently-playing",
	"playlist-read-private",
	"playlist-read-collaborative",
	"playlist-modify-private",
	"playlist-modify-public",
	"user-library-read",
	"user-library-modify",
}

// SpotifyEndpoint 是 Spotify 的 OAuth 端點。可變的套件變數:測試(含 cli 的 doctor 測試)把 TokenURL 指向 httptest。
// AuthStyle 明寫 InParams:PKCE public client 沒有 client secret,Spotify 文件的 PKCE 流程就是把
// client_id 放在 body。留 AutoDetect 的話 oauth2 會先試 HTTP Basic(密碼空字串)再退回 params,
// 等於押 Spotify 對那種形狀的容忍度(未驗證),而且被拒時要多打一次 token 端點。
var SpotifyEndpoint = oauth2.Endpoint{
	AuthURL:   "https://accounts.spotify.com/authorize",
	TokenURL:  "https://accounts.spotify.com/api/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

func spotifyOAuthConfig(clientID, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:    clientID,
		RedirectURL: redirectURL,
		Endpoint:    SpotifyEndpoint,
		Scopes:      SpotifyScopes,
	}
}

// LoginSpotify 執行 spec §4.2 的 Authorization Code + PKCE。
// 成功後 refresh token 已寫入 keychain。8888 被佔用即中止(dashboard 註冊固定值)。
func LoginSpotify(ctx context.Context, clientID string, openBrowser func(string) error) (*oauth2.Token, error) {
	state, err := NewState()
	if err != nil {
		return nil, err
	}
	lb, err := NewLoopback(DefaultSpotifyPort, state)
	if err != nil {
		return nil, err
	}
	defer lb.Close()
	if lb.Port() != DefaultSpotifyPort {
		return nil, fmt.Errorf("port 8888 被佔用 — Spotify dashboard 註冊的是固定 http://127.0.0.1:8888/callback,請先釋放 8888 再試")
	}

	conf := spotifyOAuthConfig(clientID, lb.BaseURL()+"/callback")
	verifier := oauth2.GenerateVerifier()
	lb.Start()
	authURL := conf.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	fmt.Fprintf(loginStderr, "若瀏覽器未自動開啟,請手動前往:\n  %s\n", authURL)
	if err := openBrowser(authURL); err != nil {
		// SSH/headless 場景 browser.Open 必失敗——不中止,使用者可手動貼上面那行 URL 完成授權。
		fmt.Fprintf(loginStderr, "無法自動開瀏覽器:%v\n", err)
	}
	vals, err := lb.Wait(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("180 秒內未收到授權回呼 — 若瀏覽器顯示 INVALID_CLIENT / Invalid redirect URI,請確認 dashboard 的 Redirect URI 是 http://127.0.0.1:8888/callback(完全相同);Client ID 可用 --client-id 重設")
		}
		return nil, fmt.Errorf("等待授權回呼:%w", err)
	}
	if e := vals.Get("error"); e != "" {
		return nil, fmt.Errorf("授權被拒:%s", e)
	}
	tok, err := conf.Exchange(ctx, vals.Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("token 交換失敗:%w", err)
	}
	if tok.RefreshToken == "" {
		return nil, fmt.Errorf("Spotify 未回傳 refresh token")
	}
	// 取鎖的位置刻意在 Exchange 之後、寫入之前:同一把鎖現在每個 provider 命令都會進(遷移),鎖在等
	// 瀏覽器回呼那 180 秒會癱掉整台機器的 capy。不取鎖則有這個交錯:B(capy search)進鎖讀到舊鍵
	// rt-legacy → A(本函式)寫入 rt-new 並刪舊鍵 → B 恢復後把 rt-legacy 寫回新鍵,rt-new 沒了。
	unlock, err := lockFile(ctx, KeySpotifyToken+".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := SaveToken(KeySpotifyToken, tok); err != nil {
		return nil, fmt.Errorf("寫入 keychain 失敗:%w", err)
	}
	_ = secret.Delete(KeySpotifyRefreshToken) // 重新登入等於升級:舊鍵不留(刪不掉也無妨,讀取一律以新鍵為準)
	return tok, nil
}

// LogoutSpotify 刪掉 Spotify 的兩個 keychain 鍵(新鍵 + 尚未升級的舊鍵),兩者都在同一把跨程序鎖內:
// 鎖外刪的話,並行中的 refresh 可能在刪除之後才把輪替出來的 RT 寫回去,登出等於沒登乾淨。
// 「有幾個鍵」是本 package 的內部知識,CLI 不該知道。ErrNotFound 視為已經沒有,其餘錯誤照實回報。
func LogoutSpotify(ctx context.Context) error {
	unlock, err := lockFile(ctx, KeySpotifyToken+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	for _, k := range []string{KeySpotifyToken, KeySpotifyRefreshToken} {
		if err := secret.Delete(k); err != nil && !errors.Is(err, secret.ErrNotFound) {
			return err
		}
	}
	return nil
}

// SpotifyTokenSource 回傳 keychain 為後盾的 token source(輪替後的 RT 一定寫回,跨程序以檔案鎖互斥)。
// keychain 兩個鍵都沒有時原樣透傳 secret.ErrNotFound(CLI 轉成「請先 auth login」提示)。
// 回傳具體型別:doctor 需要 Refresh() 明確驗 RT 存活,一般存取仍走 Token()(oauth2.TokenSource)。
func SpotifyTokenSource(ctx context.Context, clientID string) (*TokenSource, error) {
	tok, err := migrateSpotifyToken(ctx)
	if err != nil {
		return nil, err
	}
	// 直接組 TokenSource 而不是走 NewTokenSource:遷移已經在鎖內把同一顆 token 讀出來了,再讀一次
	// 等於每個 provider 命令都多 exec 一次 /usr/bin/security。放鎖到這裡之間別的 capy 可能已經輪替,
	// 但無妨:tok 只當快取用(過期就走慢路徑,慢路徑一定在鎖內重讀),refresh 送的永遠是重讀到的 RT。
	return &TokenSource{ctx: ctx, conf: spotifyOAuthConfig(clientID, ""), key: KeySpotifyToken, tok: tok}, nil
}

// migrateSpotifyToken:舊版把 refresh token 以裸字串寫在 KeySpotifyRefreshToken,直接餵給 LoadToken 會
// JSON 解析失敗、等於把既有使用者全部登出。新鍵不存在時把舊鍵種成完整 token 記錄(access token 留空
// → 首次 Token() 會 refresh,與升級前的行為一致),成功落地後刪掉舊鍵,不留兩份真相。
//
// 整段(查新鍵 → 讀舊鍵 → 寫新鍵 → 刪舊鍵)必須在 TokenSource 用的同一把跨程序鎖內,而且查新鍵要在鎖內:
// 升級只發生一次,而 macOS 在新簽章的 binary 第一次碰 keychain item 時會跳授權對話框——B 卡在對話框等
// 使用者按 Allow 的期間,A 可能已經完整跑完遷移 + refresh(新鍵 = rt2、舊鍵已刪)。B 恢復後若還照鎖外
// 讀到的舊值寫入,會把 rt2 蓋回 Spotify 端已失效的 rt1,憑證真的沒了(比 issue #3 原本的病徵更糟);
// 同一把鎖也擋掉另一個變體:B 的 Get(舊鍵) 落在 A 刪除之後 → 對已登入的使用者誤報「尚未登入」。
// 每次建構都取一次鎖(不做鎖外快路徑,那正是上面那個窗口):成本是一次 open/flock/close,而且
// TokenSource.Token() 的慢路徑本來就要取同一把鎖。不會自我死鎖:defer unlock 在本函式 return 就釋放,
// TokenSource 要到之後呼叫 Token() 走慢路徑時才會再取。
// 順便把鎖內讀到的 token 回傳給 SpotifyTokenSource 當初始快取,省掉緊接著再讀一次 keychain。
func migrateSpotifyToken(ctx context.Context) (*oauth2.Token, error) {
	unlock, err := lockFile(ctx, KeySpotifyToken+".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	switch tok, err := LoadToken(KeySpotifyToken); {
	case err == nil:
		return tok, nil // 已升級,或等鎖期間別的 capy 剛升級並輪替過——絕不可再用舊鍵覆寫
	case !errors.Is(err, secret.ErrNotFound):
		return nil, err // 新鍵存在但壞掉 / keychain 讀不到:不要退回舊鍵蓋掉它
	}
	rt, err := secret.Get(KeySpotifyRefreshToken)
	if err != nil {
		return nil, err // 含 ErrNotFound:兩個鍵都沒有 = 尚未登入
	}
	tok := &oauth2.Token{RefreshToken: rt} // 與剛寫進去的記錄等價(access token 空 → 首次 Token() 會 refresh)
	if err := SaveToken(KeySpotifyToken, tok); err != nil {
		return nil, err
	}
	_ = secret.Delete(KeySpotifyRefreshToken)
	return tok, nil
}

// SpotifyStored 回報 keychain 是否有 Spotify 憑證:新鍵優先,退回尚未升級的舊鍵(auth status / doctor 的
// 離線檢查用,不觸發升級也不打網路)。都沒有時回 secret.ErrNotFound。
func SpotifyStored() error {
	switch _, err := LoadToken(KeySpotifyToken); {
	case err == nil:
		return nil
	case !errors.Is(err, secret.ErrNotFound):
		return err
	}
	_, err := secret.Get(KeySpotifyRefreshToken)
	return err
}
