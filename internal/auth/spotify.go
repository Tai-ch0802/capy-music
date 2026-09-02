package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// loginStderr:LoginSpotify 印手動授權 URL / 開瀏覽器失敗訊息的目的地。測試替換點。
var loginStderr io.Writer = os.Stderr

// KeySpotifyRefreshToken 是 keychain 內的 refresh token 鍵名。
const KeySpotifyRefreshToken = "spotify.refresh_token"

// SpotifyScopes:spec §4.2 逐字。P1 只用到讀+播放,但一次要齊免得 P4 重授權。
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

// 測試替換點(TokenURL 指向 httptest)。
var spotifyEndpoint = oauth2.Endpoint{
	AuthURL:  "https://accounts.spotify.com/authorize",
	TokenURL: "https://accounts.spotify.com/api/token",
}

func spotifyOAuthConfig(clientID, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:    clientID,
		RedirectURL: redirectURL,
		Endpoint:    spotifyEndpoint,
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
	if err := secret.Set(KeySpotifyRefreshToken, tok.RefreshToken); err != nil {
		return nil, fmt.Errorf("寫入 keychain 失敗:%w", err)
	}
	return tok, nil
}

// SpotifyTokenSource 從 keychain 載入 refresh token,回傳會在輪替時覆寫 keychain 的 TokenSource。
// keychain 無 token 時原樣透傳 secret.ErrNotFound(CLI 轉成「請先 auth login」提示)。
func SpotifyTokenSource(ctx context.Context, clientID string) (oauth2.TokenSource, error) {
	rt, err := secret.Get(KeySpotifyRefreshToken)
	if err != nil {
		return nil, err
	}
	conf := spotifyOAuthConfig(clientID, "")
	base := conf.TokenSource(ctx, &oauth2.Token{RefreshToken: rt})
	return &persistingTokenSource{src: base, lastRT: rt}, nil
}

// persistingTokenSource:每次 Token() 比對 refresh token,輪替即覆寫 keychain。
// Spotify PKCE 的 refresh token 每次 refresh 都會換新、舊的立即失效(spec §4.2)——
// 寫入失敗必須讓呼叫失敗,否則下次啟動就永久登出。
type persistingTokenSource struct {
	mu     sync.Mutex
	src    oauth2.TokenSource
	lastRT string
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken != "" && tok.RefreshToken != p.lastRT {
		if serr := secret.Set(KeySpotifyRefreshToken, tok.RefreshToken); serr != nil {
			return nil, fmt.Errorf("refresh token 已輪替但寫入 keychain 失敗:%w", serr)
		}
		p.lastRT = tok.RefreshToken
	}
	return tok, nil
}
