package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/secret"
)

const testKey = "test.token"

// setTokenTest:乾淨的 mock keychain + 獨立 config dir(鎖檔落在這裡,不能碰到開發者真正的目錄)。
// 目錄刻意不存在:lockFile 必須自己 MkdirAll。
func setTokenTest(t *testing.T) string {
	t.Helper()
	keyring.MockInit()
	dir := filepath.Join(t.TempDir(), "nested", "capy-music")
	t.Setenv("CAPY_CONFIG_DIR", dir)
	return dir
}

// shortSaveRetry:把寫回重試的等待縮短,免得測試白等半秒(正式值 500ms)。
func shortSaveRetry(t *testing.T) {
	t.Helper()
	orig := saveRetryInterval
	saveRetryInterval = 10 * time.Millisecond
	t.Cleanup(func() { saveRetryInterval = orig })
}

func testConf(tokenURL string) *oauth2.Config {
	return &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{TokenURL: tokenURL}}
}

func freshToken(rt string) *oauth2.Token {
	return &oauth2.Token{AccessToken: "at-fresh", TokenType: "Bearer", RefreshToken: rt, Expiry: time.Now().Add(time.Hour)}
}

// staleToken:30s 後到期,少於 60s 餘裕 → Token() 視為過期。
func staleToken(rt string) *oauth2.Token {
	return &oauth2.Token{AccessToken: "at-stale", TokenType: "Bearer", RefreshToken: rt, Expiry: time.Now().Add(30 * time.Second)}
}

// tokenJSON:token 端點回應;rt 為空就不帶 refresh_token(Google 形狀)。
func tokenJSON(at, rt string) string {
	s := fmt.Sprintf(`{"access_token":%q,"token_type":"Bearer","expires_in":3600`, at)
	if rt != "" {
		s += fmt.Sprintf(`,"refresh_token":%q`, rt)
	}
	return s + "}"
}

// 量的是真正寫進 keychain 的 bytes(不是 struct):Windows Credential Manager 值上限 2560。
// 輸入刻意夾帶 1500 字的 id_token,證明排除是明確的、不是碰巧沒序列化到。
func TestSaveTokenJSONShapeAndSize(t *testing.T) {
	setTokenTest(t)
	tok := (&oauth2.Token{
		AccessToken:  strings.Repeat("a", 1024),
		TokenType:    "Bearer",
		RefreshToken: strings.Repeat("r", 512),
		Expiry:       time.Now().Add(time.Hour),
	}).WithExtra(map[string]any{"id_token": strings.Repeat("i", 1500)})
	if err := SaveToken(testKey, tok); err != nil {
		t.Fatal(err)
	}
	raw, err := secret.Get(testKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 2560 {
		t.Errorf("keychain 值 %d bytes,Windows Credential Manager 上限 2560", len(raw))
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	want := []string{"access_token", "token_type", "refresh_token", "expiry", "issued_at"}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("JSON 缺欄位 %s", k)
		}
	}
	if len(m) != len(want) {
		t.Errorf("JSON 欄位數 %d,want %d:%s", len(m), len(want), raw)
	}
	if strings.Contains(raw, "id_token") {
		t.Error("JSON 不得含 id_token")
	}
}

func TestTokenRoundTripPreservesExpiry(t *testing.T) {
	setTokenTest(t)
	exp := time.Date(2026, 9, 3, 12, 34, 56, 789012345, time.FixedZone("CST", 8*3600))
	in := &oauth2.Token{AccessToken: "at", TokenType: "Bearer", RefreshToken: "rt", Expiry: exp}
	if err := SaveToken(testKey, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadToken(testKey)
	if err != nil {
		t.Fatal(err)
	}
	if out.AccessToken != "at" || out.TokenType != "Bearer" || out.RefreshToken != "rt" {
		t.Errorf("round trip 欄位不符:%+v", out)
	}
	if !out.Expiry.Equal(exp) {
		t.Errorf("expiry = %v, want %v", out.Expiry, exp)
	}
}

func TestLoadTokenMissing(t *testing.T) {
	setTokenTest(t)
	if _, err := LoadToken(testKey); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("無 token 應透傳 ErrNotFound,得到 %v", err)
	}
	if _, err := NewTokenSource(context.Background(), testConf("http://127.0.0.1:0"), testKey); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("NewTokenSource 無 token 應透傳 ErrNotFound,得到 %v", err)
	}
}

func TestSaveTokenRejectsEmptyRefreshToken(t *testing.T) {
	setTokenTest(t)
	if err := SaveToken(testKey, &oauth2.Token{AccessToken: "at"}); err == nil {
		t.Fatal("沒有 refresh token 的 token 不該落地(會把好的 refresh token 蓋掉)")
	}
}

// issued_at 記的是 refresh token 的發放時間:access token 換了但 RT 沒換就延用,RT 換了才更新。
// T3 的 ErrGoogleGrant 用它算 token 年齡——Google 的 RT 永不輪替,每次 Save 都重設的話年齡永遠 < 8 天。
func TestSaveTokenIssuedAtFollowsRefreshToken(t *testing.T) {
	setTokenTest(t)
	clock := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	orig := now
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = orig })

	save := func(at, rt string) storedToken {
		t.Helper()
		if err := SaveToken(testKey, &oauth2.Token{AccessToken: at, RefreshToken: rt}); err != nil {
			t.Fatal(err)
		}
		st, err := loadStored(testKey)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	t0 := clock
	if st := save("at1", "rt1"); !st.IssuedAt.Equal(t0) {
		t.Errorf("首次 issued_at = %v, want %v", st.IssuedAt, t0)
	}
	clock = clock.Add(time.Hour)
	if st := save("at2", "rt1"); !st.IssuedAt.Equal(t0) {
		t.Errorf("RT 未換,issued_at 應延用 %v,得到 %v", t0, st.IssuedAt)
	}
	clock = clock.Add(time.Hour)
	if st := save("at3", "rt2"); !st.IssuedAt.Equal(clock) {
		t.Errorf("RT 輪替,issued_at 應更新為 %v,得到 %v", clock, st.IssuedAt)
	}
}

// ⭐ 鎖的互斥:每個 goroutine 各自 lockFile(各開一個 fd)。flock / LockFileEx 都是 per-open-file-description,
// 同 process 兩個 fd 也會互斥;把 flock 換成 no-op 這個測試必掛。config dir 刻意不存在。
func TestLockFileMutualExclusion(t *testing.T) {
	dir := setTokenTest(t)
	var held atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := lockFile(context.Background(), "t.lock")
			if err != nil {
				t.Error(err)
				return
			}
			defer unlock()
			if n := held.Add(1); n != 1 {
				t.Errorf("鎖沒互斥:同時 %d 個持有者", n)
			}
			time.Sleep(5 * time.Millisecond)
			held.Add(-1)
		}()
	}
	wg.Wait()
	if _, err := os.Stat(filepath.Join(dir, "t.lock")); err != nil {
		t.Errorf("鎖檔應建立在 config dir(目錄不存在時自建):%v", err)
	}
}

// ⭐ 鎖必須吃 context:Ctrl-C(signal.NotifyContext)只取消 ctx、不終止 process,所以等鎖不能用作業
// 系統的阻塞鎖——卡在那個系統呼叫的 goroutine 永遠看不到取消,只剩 SIGKILL 殺得掉。臨界區也不是 refresh
// 的 30s 逾時擋得住的(LoadToken/SaveToken 會 exec /usr/bin/security,keychain 上鎖時停在密碼對話框)。
// 拿掉 lockFile 迴圈裡的 ctx.Done() 分支,這個測試會走到 2 秒那條 t.Fatal。
func TestLockFileGivesUpWhenContextCanceled(t *testing.T) {
	setTokenTest(t)
	unlock, err := lockFile(context.Background(), "t.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		u, err := lockFile(ctx, "t.lock")
		if err == nil {
			u()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "另一個 capy 正持有") {
			t.Fatalf("ctx 取消應回「另一個 capy 正持有」錯誤,得到 %v", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("錯誤應包住 context.Canceled:%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 已取消卻仍卡在等鎖:Ctrl-C 殺不掉這個 process")
	}
}

// ⭐ 同一件事要從真正的入口驗一次:遷移在每次 SpotifyTokenSource 建構都會取鎖,等於 search / play /
// pause / now / devices / pl / doctor 全部都會進這把鎖。只測 lockFile 的話,日後有人在 migrateSpotifyToken
// 裡改傳 context.Background() 不會被抓到。
func TestSpotifyTokenSourceGivesUpWhenContextCanceled(t *testing.T) {
	setTokenTest(t)
	if err := secret.Set(KeySpotifyRefreshToken, "rt-legacy"); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockFile(context.Background(), KeySpotifyToken+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := SpotifyTokenSource(ctx, "cid123")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "另一個 capy 正持有") {
			t.Fatalf("ctx 取消應讓建構失敗並說明鎖被佔用,得到 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 已取消卻仍卡在遷移的鎖上:每個 provider 命令都會卡死且 Ctrl-C 無效")
	}
}

// keychain 內容不是有效 JSON(改用 JSON 儲存後才可能出現的失敗模式:舊格式殘留、外部工具寫壞)。
// 必須是明確的、指得出下一步的錯誤,而且不能被誤判成 ErrNotFound(那會讓遷移拿舊鍵蓋掉它)。
func TestLoadTokenCorruptJSON(t *testing.T) {
	setTokenTest(t)
	if err := secret.Set(testKey, "not json at all"); err != nil {
		t.Fatal(err)
	}
	_, err := LoadToken(testKey)
	if err == nil || !strings.Contains(err.Error(), "不是有效的 token JSON") {
		t.Fatalf("壞掉的 JSON 應回可歸因的錯誤,得到 %v", err)
	}
	if errors.Is(err, secret.ErrNotFound) {
		t.Error("壞掉的 JSON 不得被當成「沒有 token」")
	}
	if _, err := NewTokenSource(context.Background(), testConf("http://127.0.0.1:0"), testKey); err == nil {
		t.Error("NewTokenSource 對壞掉的 JSON 應失敗")
	}
}

// ⭐ 寫回 keychain 失敗要重試一次:此刻舊 RT 在 Spotify 端已作廢、新 RT 只在記憶體裡,而 macOS 的寫入是
// exec /usr/bin/security,一次暫時性失敗(fork 失敗、keychain 剛好忙)就等於永久登出。
// 拿掉重試,第一次失敗就會直接回錯,這個測試掛。
func TestTokenSourceRetriesWriteBackOnce(t *testing.T) {
	setTokenTest(t)
	shortSaveRetry(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, tokenJSON("at-new", "rt2"))
	}))
	defer srv.Close()
	var calls atomic.Int32
	orig := saveToken
	saveToken = func(key string, tok *oauth2.Token) error {
		if calls.Add(1) == 1 {
			return errors.New("暫時性失敗")
		}
		return orig(key, tok)
	}
	t.Cleanup(func() { saveToken = orig })

	if err := SaveToken(testKey, staleToken("rt1")); err != nil {
		t.Fatal(err)
	}
	ts, err := NewTokenSource(context.Background(), testConf(srv.URL), testKey)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("寫回第一次失敗應重試而非直接放棄:%v", err)
	}
	if tok.AccessToken != "at-new" || calls.Load() != 2 {
		t.Errorf("token = %+v,saveToken 呼叫 %d 次(want 2)", tok, calls.Load())
	}
	if stored, err := LoadToken(testKey); err != nil || stored.RefreshToken != "rt2" {
		t.Errorf("重試成功後 keychain 應含輪替後的 rt2:(%+v, %v)", stored, err)
	}
}

// ⭐ 重試要隔一段時間才有意義(背靠背重試等於同一瞬間再試一次,救不了「security 剛好 fork 失敗 /
// keychain 守護程序剛好忙」),但 ctx 已取消時不能把間隔等完——這個分支換來的正是 Ctrl-C 殺得掉。
// 取消刻意發生在第一次 saveToken 裡(refresh 之後):ctx 若一開始就取消,HTTP 那步就先失敗、根本走不到寫回。
// 間隔設 10s,拿掉 select 的 ctx.Done() 分支的話,Token() 會卡滿 10 秒,這個測試會走到 5 秒那條斷言。
func TestTokenSourceWriteBackRetryGivesUpWhenContextCanceled(t *testing.T) {
	setTokenTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, tokenJSON("at-new", "rt2"))
	}))
	defer srv.Close()
	origInterval := saveRetryInterval
	saveRetryInterval = 10 * time.Second
	t.Cleanup(func() { saveRetryInterval = origInterval })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	origSave := saveToken
	saveToken = func(key string, tok *oauth2.Token) error {
		calls.Add(1)
		cancel() // 使用者在寫回失敗與重試之間按下 Ctrl-C
		return errors.New("暫時性失敗")
	}
	t.Cleanup(func() { saveToken = origSave })

	if err := SaveToken(testKey, staleToken("rt1")); err != nil {
		t.Fatal(err)
	}
	ts, err := NewTokenSource(ctx, testConf(srv.URL), testKey)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = ts.Token()
	if err == nil {
		t.Fatal("兩次寫回都沒成功時 Token() 必須失敗")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("ctx 已取消卻仍把重試間隔等完(%v):Ctrl-C 殺不掉", elapsed)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("ctx 取消後不該再重試,saveToken 呼叫 %d 次", n)
	}
	// 此刻舊 RT 已作廢、新 RT 隨著這個 return 消失 = 使用者真的登出了,訊息必須說明並給下一步(R-5)。
	if !strings.Contains(err.Error(), "已登出") || !strings.Contains(err.Error(), "capy auth login") {
		t.Errorf("訊息要講明已登出並指出下一步:%v", err)
	}
}

// ⭐ 等鎖不能悄無聲息:持有者可能停在 macOS keychain 授權對話框上,而這邊的 capy search 看起來只是
// 當掉,使用者不會知道該去按「允許」。等超過門檻要往 stderr 提醒,而且只提醒一次(每輪都印會洗版)。
// 拿掉 lockFile 裡的提醒區塊,這個測試會看到 0 次。
func TestLockFileWarnsOnceWhileWaiting(t *testing.T) {
	setTokenTest(t)
	var buf strings.Builder
	origW, origAfter, origRetry := lockStderr, lockNoticeAfter, lockRetryInterval
	lockStderr, lockNoticeAfter, lockRetryInterval = &buf, 10*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { lockStderr, lockNoticeAfter, lockRetryInterval = origW, origAfter, origRetry })

	unlock, err := lockFile(context.Background(), "t.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	// 等鎖在本 goroutine 內完成(buf 沒有並行存取);ctx 逾時是唯一的出口。
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if u, err := lockFile(ctx, "t.lock"); err == nil {
		u()
		t.Fatal("鎖仍被持有,不該取得")
	}
	out := buf.String()
	if n := strings.Count(out, "等待另一個 capy 釋放"); n != 1 {
		t.Errorf("等鎖提醒應剛好印 1 次(輪詢每 5ms 一輪),實際 %d 次:%q", n, out)
	}
	if !strings.Contains(out, "允許") {
		t.Errorf("提醒要告訴使用者對話框按「允許」:%q", out)
	}
}

// 有效 token 走記憶體:零網路、連鎖檔都不碰。
func TestTokenSourceFreshTokenNoNetwork(t *testing.T) {
	dir := setTokenTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("記憶體內 token 仍有效,不該打 token 端點:%s", r.URL)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := SaveToken(testKey, freshToken("rt1")); err != nil {
		t.Fatal(err)
	}
	ts, err := NewTokenSource(context.Background(), testConf(srv.URL), testKey)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at-fresh" {
		t.Errorf("access token = %q", tok.AccessToken)
	}
	if _, err := os.Stat(filepath.Join(dir, testKey+".lock")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("快路徑不該建立鎖檔:%v", err)
	}
}

// Spotify 形狀:每次 refresh 都用 keychain 內目前的 RT,回應帶新 RT → 覆寫 keychain;
// 之後走記憶體不再打網路;Refresh() 強制再打一次。
func TestTokenSourceRefreshRotatesAndPersists(t *testing.T) {
	setTokenTest(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != fmt.Sprintf("rt%d", n-1) {
			t.Errorf("第 %d 次 refresh 請求錯誤:%v", n, r.PostForm)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, tokenJSON(fmt.Sprintf("at%d", n), fmt.Sprintf("rt%d", n)))
	}))
	defer srv.Close()
	if err := SaveToken(testKey, staleToken("rt0")); err != nil {
		t.Fatal(err)
	}
	ts, err := NewTokenSource(context.Background(), testConf(srv.URL), testKey)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at1" {
		t.Errorf("access token = %q", tok.AccessToken)
	}
	stored, err := LoadToken(testKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RefreshToken != "rt1" || stored.AccessToken != "at1" || time.Until(stored.Expiry) < 50*time.Minute {
		t.Errorf("refresh 後 keychain 應含新 RT / access token / expiry:%+v", stored)
	}
	if tok, err := ts.Token(); err != nil || tok.AccessToken != "at1" || hits.Load() != 1 {
		t.Errorf("第二次 Token() 應走記憶體:(%v, %v),hits=%d", tok, err, hits.Load())
	}
	if tok, err := ts.Refresh(); err != nil || tok.AccessToken != "at2" || hits.Load() != 2 {
		t.Errorf("Refresh() 應強制 refresh:(%v, %v),hits=%d", tok, err, hits.Load())
	}
	if stored, err := LoadToken(testKey); err != nil || stored.RefreshToken != "rt2" {
		t.Errorf("Refresh() 後 keychain 應含 rt2:(%+v, %v)", stored, err)
	}
}

// Google 形狀:refresh 回應不帶 refresh_token → 沿用舊的寫回,不能寫成空字串。
func TestTokenSourceCarriesRefreshTokenForward(t *testing.T) {
	setTokenTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, tokenJSON("at-new", ""))
	}))
	defer srv.Close()
	if err := SaveToken(testKey, staleToken("rt-google")); err != nil {
		t.Fatal(err)
	}
	ts, err := NewTokenSource(context.Background(), testConf(srv.URL), testKey)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at-new" || tok.RefreshToken != "rt-google" {
		t.Errorf("回傳 token 應帶舊 RT:%+v", tok)
	}
	if stored, err := LoadToken(testKey); err != nil || stored.RefreshToken != "rt-google" || stored.AccessToken != "at-new" {
		t.Errorf("keychain 應沿用舊 RT 並更新 access token:(%+v, %v)", stored, err)
	}
}

// ⭐ issue #3 本體:兩個 token source 各自載入同一個過期 token(rt1)。第一個 refresh 後 RT 輪替成 rt2;
// 第二個進鎖後重讀 keychain 發現已有新 token,直接用、不 refresh。
// 沒有鎖內重讀的話,第二個會拿記憶體裡的 rt1 去 refresh → 伺服器回 invalid_grant(舊 RT 已失效)。
// 兩個 source 序列建構、序列呼叫:mock keychain 是裸 map,並行打會被 -race 抓到(那是假造物的問題)。
func TestTokenSourceDoubleCheckSkipsRefresh(t *testing.T) {
	setTokenTest(t)
	var hits atomic.Int32
	var mu sync.Mutex
	current := "rt1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = r.ParseForm()
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.PostForm.Get("refresh_token") != current {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		current = "rt2"
		fmt.Fprint(w, tokenJSON("at-new", "rt2"))
	}))
	defer srv.Close()
	if err := SaveToken(testKey, staleToken("rt1")); err != nil {
		t.Fatal(err)
	}
	ts1, err := NewTokenSource(context.Background(), testConf(srv.URL), testKey)
	if err != nil {
		t.Fatal(err)
	}
	ts2, err := NewTokenSource(context.Background(), testConf(srv.URL), testKey)
	if err != nil {
		t.Fatal(err)
	}
	tok1, err := ts1.Token()
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := ts2.Token()
	if err != nil {
		t.Fatalf("第二個 source 應用鎖內重讀到的新 token,得到 %v", err)
	}
	if tok1.AccessToken != "at-new" || tok2.AccessToken != "at-new" {
		t.Errorf("access token = %q / %q", tok1.AccessToken, tok2.AccessToken)
	}
	if hits.Load() != 1 {
		t.Errorf("token 端點應只被打 1 次,實際 %d", hits.Load())
	}
}

// refresh 卡住時逾時(30s,測試縮成 50ms):錯誤回傳且鎖必須釋放——否則所有並行呼叫跟著卡死。
func TestTokenSourceRefreshTimeoutReleasesLock(t *testing.T) {
	setTokenTest(t)
	// handler 卡到測試結束才放行(不能靠 r.Context():handler 沒讀 body,server 不會察覺 client 斷線)。
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	orig := refreshTimeout
	refreshTimeout = 50 * time.Millisecond
	t.Cleanup(func() { refreshTimeout = orig })

	if err := SaveToken(testKey, staleToken("rt1")); err != nil {
		t.Fatal(err)
	}
	ts, err := NewTokenSource(context.Background(), testConf(srv.URL), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Token(); err == nil {
		t.Fatal("refresh 逾時應回錯誤")
	}
	// 漏鎖會阻塞到 test binary timeout;用 2s 上限讓失敗可歸因。
	acquired := make(chan error, 1)
	go func() {
		unlock, err := lockFile(context.Background(), testKey+".lock")
		if err == nil {
			unlock()
		}
		acquired <- err
	}()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("逾時後鎖未釋放")
	}
}

// ⭐ 慢路徑必須真的取鎖:測試自己持鎖扮演「另一個 capy」,Token() 在鎖釋放前不得完成。
// 拿掉 token() 內的 lockFile 呼叫,這個測試必掛。任何時刻只有一個 goroutine 碰 keychain,mock 的裸 map 不會被 -race 抓到。
func TestTokenSlowPathTakesLock(t *testing.T) {
	setTokenTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, tokenJSON("at-new", "rt2"))
	}))
	defer srv.Close()
	if err := SaveToken(testKey, staleToken("rt1")); err != nil {
		t.Fatal(err)
	}
	ts, err := NewTokenSource(context.Background(), testConf(srv.URL), testKey)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockFile(context.Background(), testKey+".lock")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		if _, err := ts.Token(); err != nil {
			t.Error(err)
		}
		close(done)
	}()
	select {
	case <-done:
		unlock()
		t.Fatal("Token() 在別人持鎖時就完成了:慢路徑沒取鎖")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("放鎖後 Token() 仍未完成")
	}
}

// ⭐ refresh 送的必須是鎖內重讀到的 RT,不是建構時載進記憶體的:兩個 source 都拿著 rt1。
// A refresh 後 keychain 是 rt2,但換出的 token 只剩 30s(< 60s 餘裕),逼 B 真的走到 refresh;
// B 送 rt2 才對,送記憶體裡的 rt1 就是 issue #3 的 invalid_grant。
func TestTokenSourceRefreshUsesReloadedRefreshToken(t *testing.T) {
	setTokenTest(t)
	var mu sync.Mutex
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, r.PostForm.Get("refresh_token"))
		n := len(sent)
		w.Header().Set("Content-Type", "application/json")
		if r.PostForm.Get("refresh_token") != fmt.Sprintf("rt%d", n) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		fmt.Fprintf(w, `{"access_token":"at%d","token_type":"Bearer","expires_in":30,"refresh_token":"rt%d"}`, n, n+1)
	}))
	defer srv.Close()
	if err := SaveToken(testKey, staleToken("rt1")); err != nil {
		t.Fatal(err)
	}
	ts1, err := NewTokenSource(context.Background(), testConf(srv.URL), testKey)
	if err != nil {
		t.Fatal(err)
	}
	ts2, err := NewTokenSource(context.Background(), testConf(srv.URL), testKey)
	if err != nil {
		t.Fatal(err)
	}
	if tok, err := ts1.Token(); err != nil || tok.AccessToken != "at1" {
		t.Fatalf("第一個 source:(%v, %v)", tok, err)
	}
	if tok, err := ts2.Token(); err != nil || tok.AccessToken != "at2" {
		t.Fatalf("第二個 source 應送鎖內重讀到的 rt2 並成功:(%v, %v)", tok, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || sent[0] != "rt1" || sent[1] != "rt2" {
		t.Errorf("送出的 refresh token 應為 [rt1 rt2],實際 %v", sent)
	}
	if stored, err := LoadToken(testKey); err != nil || stored.RefreshToken != "rt3" {
		t.Errorf("keychain 應含 rt3:(%+v, %v)", stored, err)
	}
}
