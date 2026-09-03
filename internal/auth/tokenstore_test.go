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
			unlock, err := lockFile("t.lock")
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
		unlock, err := lockFile(testKey + ".lock")
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
