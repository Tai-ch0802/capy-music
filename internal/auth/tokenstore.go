package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// secretGet:loadStored 讀 keychain 的入口。測試替換點(數 keychain 讀取次數——
// go-keyring 的 mock 沒有計數器,而每次讀在 macOS 都是 exec /usr/bin/security)。
var secretGet = secret.Get

// saveRetryInterval:寫回 keychain 失敗後、重試前的等待。測試替換點。
var saveRetryInterval = 500 * time.Millisecond

// refreshTimeout:鎖內 refresh 的上限。oauth2 預設 client 沒有 timeout,鎖內卡死會拖垮所有並行呼叫。測試替換點。
var refreshTimeout = 30 * time.Second

// tokenFreshMargin:記憶體內 token 距到期少於這個餘裕就視為過期。
const tokenFreshMargin = 60 * time.Second

func loadStored(key string) (storedToken, error) {
	var st storedToken
	raw, err := secretGet(key)
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

// providerOf:從 keychain 鍵名取 provider 名(鍵名慣例 <provider>.token,例:spotify.token → spotify)。
// 本檔是 provider-neutral 的,錯誤訊息要指出下一步(R-5)又不能寫死 spotify,只能靠這個慣例。
func providerOf(key string) string { return strings.TrimSuffix(key, ".token") }

// lockRetryInterval:等鎖的輪詢間隔。測試替換點。
var lockRetryInterval = 50 * time.Millisecond

// lockNoticeAfter / lockStderr:等鎖超過這個時間就往 stderr 提醒一次(只印一次)與提醒的目的地。測試替換點。
// 不共用 spotify.go 的 loginStderr:本檔 provider-neutral,Google 之後走同一條路徑。
var (
	lockNoticeAfter           = time.Second
	lockStderr      io.Writer = os.Stderr
)

// lockFile 對 config.Dir()/<name> 取跨程序排他鎖。鎖檔是空檔,不放任何內容。
//
// 鎖被別的 capy 持有時輪詢等待,ctx 取消(Ctrl-C / cron 逾時)就放棄並回錯誤——不能用作業系統的阻塞
// 鎖:那個系統呼叫不吃 context,signal.NotifyContext 只取消 ctx、不終止 process,卡在裡面的 goroutine
// 只剩 SIGKILL 殺得掉。臨界區也不是 30s refresh 逾時擋得住的:LoadToken / SaveToken 會 exec
// /usr/bin/security,keychain 上鎖時會停在密碼對話框等到天荒地老。
// ctx 已取消但鎖是空的仍會取得(第一次嘗試在檢查 ctx 之前):沒人競爭時不該平白失敗。
//
// 等超過 lockNoticeAfter 會往 stderr 提醒一次:等待完全沒有上限,而持有者停在 macOS keychain 授權
// 對話框時,這邊的 capy search 看起來就只是當掉,使用者不會知道該去按「允許」。檔案鎖分不出持有者是
// 卡在對話框還是單純正在換發 token,訊息兩種都要涵蓋。
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
	start, notified := time.Now(), false
	for {
		switch ok, err := tryFlock(f); {
		case err != nil:
			f.Close()
			return nil, fmt.Errorf("取得鎖 %s:%w", name, err)
		case ok:
			return func() { _ = funlock(f); f.Close() }, nil
		}
		if !notified && time.Since(start) >= lockNoticeAfter {
			notified = true
			fmt.Fprintf(lockStderr, "等待另一個 capy 釋放 %s(對方正在存取 keychain,可能停在授權對話框上)——畫面上若有 keychain 對話框,按「允許」即可繼續;要放棄按 Ctrl-C。\n", name)
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

// TODO: s.mu 橫跨整段等檔案鎖的期間,所以同一個 process 內的並行呼叫者會排隊,連 token 還新鮮的快
// 路徑也一起卡。今天碰不到(CLI 一次跑一個命令、單 goroutine 走這條路),等真有並行呼叫者再拆成
// 「快路徑只鎖 mu、慢路徑放掉 mu 再等檔案鎖」。
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
		// ——一次暫時性失敗就等於永久登出,所以隔一段時間重試一次再放棄。
		// 這個間隔救得了的只有瞬時失敗(fork 失敗、keychain 守護程序剛好忙);keychain 被鎖住時
		// security 是停在對話框而不是回錯,退避對那種情況沒有幫助,別高估它。
		// ctx 已取消(Ctrl-C / cron 逾時)就不要把間隔等完,直接帶著第一次的原因放棄。
		select {
		case <-s.ctx.Done():
		case <-time.After(saveRetryInterval):
			err = saveToken(s.key, tok)
		}
		if err != nil {
			// 兩次都失敗:此刻舊 RT 在對方端已作廢、新 RT 只活在這個 return 就會丟掉的變數裡
			// ——使用者是真的登出了,必須講明白並給下一步,不能只說「寫入失敗」。
			return nil, fmt.Errorf("token 已 refresh 但寫不回 keychain,舊的 refresh token 此刻已失效、新的沒能存下來(等於已登出)— 請執行 capy auth login %s 重新登入:%w", providerOf(s.key), err)
		}
	}
	s.tok = tok
	return tok, nil
}
