package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// storedToken 是寫進 keychain 的 JSON 形狀(spec §4.5、附錄 C 決策 11)。欄位明確列出:
// 不放 id_token(Windows Credential Manager 值上限 2560 bytes);issued_at 是 refresh token 的發放時間,
// oauth2.Token 沒有這個欄位,T3 的 ErrGoogleGrant 用它算 token 年齡(同 package 走 loadStored 讀)。
type storedToken struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
	IssuedAt     time.Time `json:"issued_at"`
}

// now:issued_at 用的時鐘。測試替換點。
var now = time.Now

// saveToken:token() 寫回 keychain 的入口。測試替換點(注入暫時性寫入失敗,驗證重試)。
var saveToken = SaveToken

// refreshTimeout:鎖內 refresh 的上限。oauth2 預設 client 沒有 timeout,鎖內卡死會拖垮所有並行呼叫。測試替換點。
var refreshTimeout = 30 * time.Second

// tokenFreshMargin:記憶體內 token 距到期少於這個餘裕就視為過期。
const tokenFreshMargin = 60 * time.Second

func loadStored(key string) (storedToken, error) {
	var st storedToken
	raw, err := secret.Get(key)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return st, fmt.Errorf("keychain %s 內容不是有效的 token JSON(請重新 auth login):%w", key, err)
	}
	return st, nil
}

// LoadToken 從 keychain 讀出 token。沒有時原樣透傳 secret.ErrNotFound(CLI 轉成「請先 auth login」提示)。
func LoadToken(key string) (*oauth2.Token, error) {
	st, err := loadStored(key)
	if err != nil {
		return nil, err
	}
	return &oauth2.Token{AccessToken: st.AccessToken, TokenType: st.TokenType, RefreshToken: st.RefreshToken, Expiry: st.Expiry}, nil
}

// SaveToken 把 token 以 JSON 寫進 keychain。refresh token 為空一律拒絕——落地會把好的 RT 蓋掉,等於永久登出。
// issued_at:RT 與 keychain 內現有的相同就延用(Google 的 RT 不輪替,access token 卻每小時換),否則取現在。
func SaveToken(key string, tok *oauth2.Token) error {
	if tok.RefreshToken == "" {
		return errors.New("token 沒有 refresh token,拒絕寫入 keychain")
	}
	st := storedToken{AccessToken: tok.AccessToken, TokenType: tok.TokenType, RefreshToken: tok.RefreshToken, Expiry: tok.Expiry, IssuedAt: now()}
	if prev, err := loadStored(key); err == nil && prev.RefreshToken == st.RefreshToken {
		st.IssuedAt = prev.IssuedAt
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return secret.Set(key, string(b))
}

// lockRetryInterval:等鎖的輪詢間隔。測試替換點。
var lockRetryInterval = 50 * time.Millisecond

// lockFile 對 config.Dir()/<name> 取跨程序排他鎖。鎖檔是空檔,不放任何內容。
//
// 鎖被別的 capy 持有時輪詢等待,ctx 取消(Ctrl-C / cron 逾時)就放棄並回錯誤——不能用作業系統的阻塞
// 鎖:那個系統呼叫不吃 context,signal.NotifyContext 只取消 ctx、不終止 process,卡在裡面的 goroutine
// 只剩 SIGKILL 殺得掉。臨界區也不是 30s refresh 逾時擋得住的:LoadToken / SaveToken 會 exec
// /usr/bin/security,keychain 上鎖時會停在密碼對話框等到天荒地老。
// ctx 已取消但鎖是空的仍會取得(第一次嘗試在檢查 ctx 之前):沒人競爭時不該平白失敗。
func lockFile(ctx context.Context, name string) (unlock func(), err error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		switch ok, err := tryFlock(f); {
		case err != nil:
			f.Close()
			return nil, fmt.Errorf("取得鎖 %s:%w", name, err)
		case ok:
			return func() { _ = funlock(f); f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("另一個 capy 正持有 %s 鎖,等待中被中斷或逾時,請稍後再試:%w", name, ctx.Err())
		case <-time.After(lockRetryInterval):
		}
	}
}

// TokenSource 是以 keychain 為後盾的 oauth2.TokenSource(附錄 C 決策 11):記憶體內 token 還有 60s 以上就直接用;
// 否則取跨程序檔案鎖、鎖內重讀 keychain(別的 capy 可能剛換好)、仍過期才 refresh 並寫回。
// Spotify(RT 每次輪替)與 Google(RT 不輪替、refresh 回應不帶 RT)共用同一條路徑。
type TokenSource struct {
	ctx  context.Context
	conf *oauth2.Config
	key  string

	mu  sync.Mutex
	tok *oauth2.Token
}

// NewTokenSource 先從 keychain 載入 key 底下的 token;沒有時原樣透傳 secret.ErrNotFound。
// 鎖檔固定為 config.Dir()/<key>.lock;ctx 是之後每次 refresh 的父 context。
func NewTokenSource(ctx context.Context, conf *oauth2.Config, key string) (*TokenSource, error) {
	tok, err := LoadToken(key)
	if err != nil {
		return nil, err
	}
	return &TokenSource{ctx: ctx, conf: conf, key: key, tok: tok}, nil
}

// Token 實作 oauth2.TokenSource。
func (s *TokenSource) Token() (*oauth2.Token, error) { return s.token(false) }

// Refresh 略過快取,在鎖內以 keychain 內目前的 refresh token 強制 refresh 一次並寫回。
// 給 doctor 這種要明確驗證 RT 還活著的路徑用;一般存取走 Token()。
func (s *TokenSource) Refresh() (*oauth2.Token, error) { return s.token(true) }

func fresh(tok *oauth2.Token) bool {
	return tok != nil && tok.AccessToken != "" && time.Until(tok.Expiry) > tokenFreshMargin
}

func (s *TokenSource) token(force bool) (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && fresh(s.tok) {
		return s.tok, nil
	}
	unlock, err := lockFile(s.ctx, s.key+".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	// 鎖內重讀:另一個 process 可能已經 refresh 並寫回,直接用它的、不再打 token 端點(issue #3)。
	cur, err := LoadToken(s.key)
	if err != nil {
		return nil, err
	}
	if !force && fresh(cur) {
		s.tok = cur
		return cur, nil
	}
	if cur.RefreshToken == "" {
		return nil, fmt.Errorf("keychain %s 內沒有 refresh token,請重新 auth login", s.key)
	}
	rctx, cancel := context.WithTimeout(s.ctx, refreshTimeout)
	defer cancel()
	// 只帶 RT 進去:oauth2 視為無效 token,立刻打 refresh。
	tok, err := s.conf.TokenSource(rctx, &oauth2.Token{RefreshToken: cur.RefreshToken}).Token()
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = cur.RefreshToken // Google 不輪替:回應沒有 RT,沿用舊的
	}
	if err := saveToken(s.key, tok); err != nil {
		// Spotify 的舊 RT 在 refresh 後已失效、新的只在記憶體裡,而 macOS 的寫入是 exec /usr/bin/security
		// ——一次暫時性失敗就等於永久登出,所以重試一次再放棄。
		if err = saveToken(s.key, tok); err != nil {
			// 兩次都失敗:寫回失敗必須讓呼叫失敗,否則下次啟動永久登出。
			return nil, fmt.Errorf("token 已 refresh 但寫入 keychain 失敗:%w", err)
		}
	}
	s.tok = tok
	return tok, nil
}
