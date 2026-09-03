package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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
	orig := SpotifyEndpoint
	SpotifyEndpoint.TokenURL = tokenURL
	t.Cleanup(func() { SpotifyEndpoint = orig })
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
	setTokenTest(t)
	// 舊鍵預先存在:重新登入等於升級,登入後不該留下第二份真相。
	if err := secret.Set(KeySpotifyRefreshToken, "rt-legacy"); err != nil {
		t.Fatal(err)
	}
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
	stored, err := LoadToken(KeySpotifyToken)
	if err != nil || stored.RefreshToken != "rt1" || stored.AccessToken != "at1" {
		t.Fatalf("keychain 應存完整 token 記錄:(%+v, %v)", stored, err)
	}
	if _, err := secret.Get(KeySpotifyRefreshToken); !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("登入後舊鍵應被清掉(不留兩份真相),得到 %v", err)
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
// 兼升級路徑:keychain 只有舊鍵(裸字串)時,必須種進新鍵而不是把使用者登出;輪替後的 RT 落在新鍵,舊鍵清掉。
func TestSpotifyTokenSourceMigratesLegacyKeyAndRotates(t *testing.T) {
	setTokenTest(t)
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
	stored, err := LoadToken(KeySpotifyToken)
	if err != nil || stored.RefreshToken != "rt-new" || stored.AccessToken != "at2" {
		t.Fatalf("輪替後 keychain 必須被覆寫:(%+v, %v)", stored, err)
	}
	if _, err := secret.Get(KeySpotifyRefreshToken); !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("升級後舊鍵應被刪除(不留兩份真相),得到 %v", err)
	}
}

// ⭐ 硬約束的另一半:refresh 成功但寫回 keychain 失敗時,Token() 必須失敗。
// 舊 RT 在 Spotify 端已隨這次 refresh 作廢,新 RT 若靜默遺失就是永久登出。
// 假 token 端點的 handler 在回 200 之前才把 keyring 弄壞:此時鎖內重讀早已成功,踩到的正是寫回那一步。
func TestSpotifyTokenSourceFailsWhenPersistFails(t *testing.T) {
	setTokenTest(t)
	shortSaveRetry(t)
	t.Cleanup(func() { keyring.MockInit() })
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyring.MockInitWithError(errors.New("boom"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at3","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-new2"}`))
	}))
	defer tokenSrv.Close()
	swapTokenURL(t, tokenSrv.URL)

	if err := secret.Set(KeySpotifyRefreshToken, "rt-old2"); err != nil {
		t.Fatal(err)
	}
	ts, err := SpotifyTokenSource(context.Background(), "cid123") // 建構(含升級)在正常 mock 下先成功
	if err != nil {
		t.Fatal(err)
	}
	// 訊息由本專案的守衛貢獻(tokenstore.go),不是注入的 "boom" ——比對得到才代表真的踩到那條分支。
	_, err = ts.Token()
	if err == nil || !strings.Contains(err.Error(), "寫不回 keychain") {
		t.Fatalf("refresh 成功但寫回失敗必須讓 Token() 失敗,得到 %v", err)
	}
	// 此刻使用者是真的登出了(舊 RT 已作廢、新 RT 隨這個 return 消失),訊息必須講明並給下一步(R-5)。
	if !strings.Contains(err.Error(), "已登出") || !strings.Contains(err.Error(), "capy auth login spotify") {
		t.Errorf("訊息要說明已登出並指出下一步:%v", err)
	}
}

// ⭐ 升級窗口:遷移的「查新鍵 → 讀舊鍵 → 寫新鍵」必須整段在鎖內,而且查新鍵要在鎖內查。
// 情境:B 是換新 binary 後第一次跑(macOS 會為新簽章的 binary 跳 keychain 授權對話框,遷移又剛好只發生
// 這一次),卡住的期間 A 已經完整升級並輪替過(新鍵 = rt2)。B 恢復後若照鎖外看到的狀態寫入,會把 rt2
// 蓋回 Spotify 端已失效的 rt1 —— 憑證真的沒了。測試自己持鎖扮演「正在升級的 A」,結果在起 goroutine 前
// 就放好(新鍵 rt2;舊鍵 rt1 仍在,模擬 A 的刪除尚未生效)。
// 拿掉鎖 → B 不會被擋住,「應該還沒完成」那條斷言掛;拿掉鎖內重查 → B 用 rt1 覆寫 rt2,值的斷言掛。
// 起 goroutine 之後主測試不再碰 keychain,-race 乾淨。
func TestSpotifyTokenMigrationLocksAndRechecks(t *testing.T) {
	setTokenTest(t)
	if err := SaveToken(KeySpotifyToken, freshToken("rt2")); err != nil {
		t.Fatal(err)
	}
	if err := secret.Set(KeySpotifyRefreshToken, "rt1"); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockFile(context.Background(), KeySpotifyToken+".lock")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := migrateSpotifyToken(context.Background())
		done <- err
	}()
	select {
	case <-done:
		unlock()
		t.Fatal("別人持鎖時遷移就跑完了:遷移沒取鎖,升級窗口內會把已輪替的 RT 蓋回死掉的舊 RT")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("放鎖後遷移仍未完成")
	}
	stored, err := LoadToken(KeySpotifyToken)
	if err != nil || stored.RefreshToken != "rt2" {
		t.Fatalf("鎖內重查應發現新鍵已經存在,不得用舊鍵的 rt1 蓋掉:(%+v, %v)", stored, err)
	}
}

// ⭐ 遷移不得在「新鍵存在但讀不出來」時退回舊鍵:那會把壞掉(但可能只是 keychain 一時讀不到)的新鍵
// 用舊鍵的值蓋掉。刪掉 migrateSpotifyToken 裡的 `case !errors.Is(err, secret.ErrNotFound): return err`,
// 這個測試會看到新鍵被 rt-legacy 覆寫。
func TestSpotifyTokenMigrationKeepsBrokenNewKey(t *testing.T) {
	setTokenTest(t)
	if err := secret.Set(KeySpotifyToken, "{壞掉的 JSON"); err != nil {
		t.Fatal(err)
	}
	if err := secret.Set(KeySpotifyRefreshToken, "rt-legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateSpotifyToken(context.Background()); err == nil {
		t.Fatal("新鍵存在但讀不出來時,遷移必須失敗而不是拿舊鍵蓋掉它")
	}
	raw, err := secret.Get(KeySpotifyToken)
	if err != nil || raw != "{壞掉的 JSON" {
		t.Fatalf("新鍵不得被覆寫:(%q, %v)", raw, err)
	}
	if got, err := secret.Get(KeySpotifyRefreshToken); err != nil || got != "rt-legacy" {
		t.Errorf("失敗時舊鍵也不該被刪:(%q, %v)", got, err)
	}
}

// ⭐ 登入的寫入必須在同一把鎖內(形狀同 TestSpotifyTokenMigrationLocksAndRechecks)。
// 不在鎖內的交錯:B(capy search)進鎖讀到舊鍵 rt-legacy → A(登入)寫入 rt-new 並刪舊鍵 →
// B 恢復後把 rt-legacy 寫回新鍵,剛拿到的 rt-new 就沒了。
// 鎖刻意在 Exchange 之後才取:測試持鎖期間 LoginSpotify 已經跑完瀏覽器流程,只會卡在寫入前。
func TestLoginSpotifyLocksBeforeWrite(t *testing.T) {
	setTokenTest(t)
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at1","token_type":"Bearer","expires_in":3600,"refresh_token":"rt1"}`))
	}))
	defer tokenSrv.Close()
	swapTokenURL(t, tokenSrv.URL)

	unlock, err := lockFile(context.Background(), KeySpotifyToken+".lock")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := LoginSpotify(ctx, "cid123", fakeAuthBrowser(t, "code1"))
		done <- err
	}()
	select {
	case <-done:
		unlock()
		t.Fatal("別人持鎖時登入就寫完了:登入的寫入沒取鎖,會蓋掉並行 refresh 剛輪替出來的 RT")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("放鎖後登入仍未完成")
	}
	if stored, err := LoadToken(KeySpotifyToken); err != nil || stored.RefreshToken != "rt1" {
		t.Errorf("登入後 keychain 應含 rt1:(%+v, %v)", stored, err)
	}
}

// LogoutSpotify 兩個鍵都要刪(尚未升級的使用者只有舊鍵;已升級的只有新鍵),整段在同一把鎖內。
func TestLogoutSpotifyDeletesBothKeys(t *testing.T) {
	setTokenTest(t)
	if err := SaveToken(KeySpotifyToken, freshToken("rt1")); err != nil {
		t.Fatal(err)
	}
	if err := secret.Set(KeySpotifyRefreshToken, "rt-legacy"); err != nil {
		t.Fatal(err)
	}
	if err := LogoutSpotify(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{KeySpotifyToken, KeySpotifyRefreshToken} {
		if _, err := secret.Get(k); !errors.Is(err, secret.ErrNotFound) {
			t.Errorf("logout 後 %s 應已刪除,得到 %v", k, err)
		}
	}
	// 兩個鍵都不存在時再登出一次不算錯(ErrNotFound 視為已經沒有)。
	if err := LogoutSpotify(context.Background()); err != nil {
		t.Errorf("重複 logout 不該報錯:%v", err)
	}
}

// ⭐ issue #3 回歸測試:兩個 capy 程序(= 兩個 token source)都從 keychain 種下同一顆 RT。
// 假 token 端點只認「目前的」RT,舊的一律 invalid_grant——Spotify 的真實行為(RT 每次輪替、舊的立即失效)。
// 第一個 refresh 後 keychain 已是 rt2;第二個必須在鎖內重讀 keychain 拿到 rt2,而不是拿建構時載進記憶體的 rt1。
// 修正前:第二個送 rt1 → invalid_grant → 被歸成 ErrAuthExpired,使用者被要求重新登入(keychain 其實還是好的)。
// 序列(非並行)呼叫:go-keyring 的 mock 是裸 map,並行會被 -race 抓到,那是假造物的問題、與本 bug 無關。
func TestSpotifyTokenSourceSecondSourceUsesRotatedToken(t *testing.T) {
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
		fmt.Fprint(w, `{"access_token":"at2","token_type":"Bearer","expires_in":3600,"refresh_token":"rt2"}`)
	}))
	defer srv.Close()
	swapTokenURL(t, srv.URL)

	if err := secret.Set(KeySpotifyRefreshToken, "rt1"); err != nil {
		t.Fatal(err)
	}
	ts1, err := SpotifyTokenSource(context.Background(), "cid123")
	if err != nil {
		t.Fatal(err)
	}
	ts2, err := SpotifyTokenSource(context.Background(), "cid123")
	if err != nil {
		t.Fatal(err)
	}
	tok1, err := ts1.Token()
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := ts2.Token()
	if err != nil {
		t.Fatalf("第二個 token source 應重讀到已輪替的 token,不該送失效的舊 RT:%v", err)
	}
	if tok1.AccessToken != "at2" || tok2.AccessToken != "at2" {
		t.Errorf("access token = %q / %q", tok1.AccessToken, tok2.AccessToken)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("一次輪替就夠,token 端點卻被打 %d 次", n)
	}
}

func TestSpotifyTokenSourceMissingToken(t *testing.T) {
	setTokenTest(t)
	if _, err := SpotifyTokenSource(context.Background(), "cid123"); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("兩個鍵都沒有應透傳 ErrNotFound,得到 %v", err)
	}
	if err := SpotifyStored(); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("SpotifyStored 應透傳 ErrNotFound,得到 %v", err)
	}
	// 尚未升級的使用者(只有舊鍵):離線檢查要看得到,而且不該順手升級(auth status 不動 keychain)。
	if err := secret.Set(KeySpotifyRefreshToken, "rt-legacy"); err != nil {
		t.Fatal(err)
	}
	if err := SpotifyStored(); err != nil {
		t.Fatalf("只有舊鍵時 SpotifyStored 應回 nil,得到 %v", err)
	}
	if _, err := LoadToken(KeySpotifyToken); !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("SpotifyStored 不該寫入新鍵,得到 %v", err)
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

// ⭐ 每個 provider 命令(search / play / pause / now / devices / pl / doctor)只該讀一次 keychain:
// 遷移已經在鎖內把 token 讀出來了,建構 TokenSource 時再讀一次,等於每次執行都多 exec 一次
// /usr/bin/security(在 macOS 還可能多跳一次授權對話框)。go-keyring 的 mock 沒有計數器,所以數 secretGet。
// 把 SpotifyTokenSource 改回呼叫 NewTokenSource,這個測試會看到 2 次。
func TestSpotifyTokenSourceReadsKeychainOnce(t *testing.T) {
	setTokenTest(t)
	// 先種好再裝計數器:SaveToken 自己也會讀一次(算 issued_at)。
	if err := SaveToken(KeySpotifyToken, freshToken("rt1")); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	orig := secretGet
	secretGet = func(key string) (string, error) {
		if key == KeySpotifyToken {
			reads.Add(1)
		}
		return orig(key)
	}
	t.Cleanup(func() { secretGet = orig })

	ts, err := SpotifyTokenSource(context.Background(), "cid123")
	if err != nil {
		t.Fatal(err)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("建構只該讀一次 %s,實際 %d 次", KeySpotifyToken, n)
	}
	// 遷移讀到的那顆必須真的被當成初始快取帶進來,否則省下的讀取會在第一次 Token() 補回去。
	if tok, err := ts.Token(); err != nil || tok.AccessToken != "at-fresh" {
		t.Fatalf("Token() 應直接回鎖內讀到的那顆:(%+v, %v)", tok, err)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("token 仍新鮮,Token() 不該再讀 keychain,累計 %d 次", n)
	}
}

// ⭐ PKCE public client 沒有 client secret,client_id 要放在 request body(Spotify 文件的 PKCE 流程)。
// AuthStyle 留預設的 AutoDetect 時,oauth2 會先試 HTTP Basic(密碼是空字串)、失敗才退回 params:
// 那是在賭 Spotify 對未文件化形狀的容忍度,而且被拒時多打一次 token 端點。
// handler 對任何帶 Authorization 的請求一律 401:少了 AuthStyleInParams 就會看到 2 次請求。
func TestSpotifyTokenExchangeUsesParamsAuthStyle(t *testing.T) {
	setTokenTest(t)
	var reqs, withClientID atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"invalid_client"}`)
			return
		}
		_ = r.ParseForm()
		if r.PostForm.Get("client_id") == "cid123" {
			withClientID.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, tokenJSON("at1", "rt2"))
	}))
	defer srv.Close()
	swapTokenURL(t, srv.URL)

	if err := SaveToken(KeySpotifyToken, staleToken("rt1")); err != nil {
		t.Fatal(err)
	}
	ts, err := SpotifyTokenSource(context.Background(), "cid123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Token(); err != nil {
		t.Fatal(err)
	}
	if reqs.Load() != 1 || withClientID.Load() != 1 {
		t.Errorf("token 端點應只被打 1 次且 client_id 在 body,實際 reqs=%d、body 帶 client_id=%d 次", reqs.Load(), withClientID.Load())
	}
}
