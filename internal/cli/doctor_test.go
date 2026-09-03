package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

func TestRunChecksRendersAndCounts(t *testing.T) {
	checks := []check{
		{name: "會過的", fn: func(ctx context.Context) (string, error) { return "ok 細節", nil }},
		{name: "會爆的", fn: func(ctx context.Context) (string, error) { return "", errors.New("爆了 — 這樣修") }},
	}
	buf := &strings.Builder{}
	failed := runChecks(context.Background(), buf, checks)
	out := buf.String()
	if failed != 1 {
		t.Errorf("failed = %d", failed)
	}
	if !strings.Contains(out, "✅ 會過的") || !strings.Contains(out, "ok 細節") {
		t.Errorf("成功列渲染錯誤:%q", out)
	}
	if !strings.Contains(out, "❌ 會爆的") || !strings.Contains(out, "這樣修") {
		t.Errorf("失敗列應含修復指示:%q", out)
	}
}

func TestDoctorExitNonZeroOnFailure(t *testing.T) {
	setCLITestConfig(t) // 空 config → 檢查①必敗
	// 寬容 handler:checkAPI 的 Health 會打 /me/player/devices,回空清單即可
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"devices":[]}`))
	})
	if _, err := runCLI(t, "doctor"); err == nil {
		t.Fatal("有檢查失敗時 doctor 應回錯誤(exit 非零)")
	}
}

func TestCheckConfigClientID(t *testing.T) {
	setCLITestConfig(t)
	if _, err := checkConfig(context.Background()); err == nil {
		t.Error("未設定 client ID 應失敗")
	}
	_ = config.Save(&config.Config{SpotifyClientID: "0123456789abcdef0123456789abcdef"})
	if detail, err := checkConfig(context.Background()); err != nil || detail == "" {
		t.Errorf("(%q, %v)", detail, err)
	}
}

func TestCheckKeychainRoundTrip(t *testing.T) {
	if _, err := checkKeychain(context.Background()); err != nil { // MockInit 下必過
		t.Errorf("keychain round-trip:%v", err)
	}
}

func TestPortHintByOS(t *testing.T) {
	if got := portHint("windows"); got != "netstat -ano | findstr :8888" {
		t.Errorf("windows portHint = %q", got)
	}
	if got := portHint("darwin"); got != "lsof -i :8888" {
		t.Errorf("darwin portHint = %q", got)
	}
}

func TestDoctorAppleChecksWithoutLogin(t *testing.T) {
	setupAppleTokens(t)
	_ = secret.Delete(apple.KeyMusicUserToken)
	// Stub checkOSA to avoid real osascript execution
	origCheckOSA := checkOSA
	t.Cleanup(func() { checkOSA = origCheckOSA })
	checkOSA = func(context.Context) (string, error) { return "stub", nil }

	buf := &strings.Builder{}
	failed := runChecks(context.Background(), buf, appleChecks())
	out := buf.String()
	if !strings.Contains(out, "✅ Apple developer token") || !strings.Contains(out, "有效至") {
		t.Errorf("應取得 dev token 並顯示有效期限:%q", out)
	}
	if !strings.Contains(out, "❌ Apple user token") || !strings.Contains(out, "capy auth login apple") {
		t.Errorf("無 MUT 應失敗並指示 login:%q", out)
	}
	if failed == 0 {
		t.Error("應有失敗項")
	}
}

func TestDoctorProviderFlagSelectsAppleSet(t *testing.T) {
	setupAppleTokens(t)
	_ = secret.Delete(apple.KeyMusicUserToken)
	// Stub checkOSA to avoid real osascript execution
	origCheckOSA := checkOSA
	t.Cleanup(func() { checkOSA = origCheckOSA })
	checkOSA = func(context.Context) (string, error) { return "stub", nil }

	_, err := runCLI(t, "doctor", "--provider", "apple")
	if err == nil || !strings.Contains(err.Error(), "檢查未通過") {
		t.Fatalf("未登入 apple 的 doctor 應回錯:%v", err)
	}
}

// TestDoctorAppleChecksKeychainErrorNotReportedAsMissing:keychain 存取失敗(非 ErrNotFound,例如
// 使用者拒絕授權或 keychain 已鎖定)不該被誤報成「沒有 token」——那會讓人誤以為要重新 login,
// 但實際上重新 login 也會卡在同一個 keychain 錯誤(review item 3)。
func TestDoctorAppleChecksKeychainErrorNotReportedAsMissing(t *testing.T) {
	setCLITestConfig(t)
	keyring.MockInitWithError(errors.New("user canceled"))
	t.Cleanup(func() { keyring.MockInit() })
	origCheckOSA := checkOSA
	t.Cleanup(func() { checkOSA = origCheckOSA })
	checkOSA = func(context.Context) (string, error) { return "stub", nil }

	buf := &strings.Builder{}
	runChecks(context.Background(), buf, appleChecks())
	out := buf.String()
	if !strings.Contains(out, "❌ Apple developer token") || !strings.Contains(out, "讀取 keychain 失敗") {
		t.Errorf("developer token 應回讀取失敗,得到 %q", out)
	}
	if !strings.Contains(out, "❌ Apple user token") || !strings.Contains(out, "讀取 keychain 失敗") {
		t.Errorf("user token 應回讀取失敗,得到 %q", out)
	}
	if strings.Contains(out, "沒有") {
		t.Errorf("keychain 讀取失敗不該說「沒有」token:%q", out)
	}
}

// ⭐ issue #3:一次 doctor 只能輪替一次 refresh token。
// 修正前 ⑤「Token 換發」自己建 token source 換一次、⑥「Spotify API」走 newProvider 又建一個再換一次,
// 同一次 doctor 輪替兩次;第二次還可能拿到已失效的舊 RT。假 token 端點只認目前的 RT,舊的回 invalid_grant。
func TestDoctorRotatesRefreshTokenOnce(t *testing.T) {
	setCLITestConfig(t)
	if err := config.Save(&config.Config{SpotifyClientID: "0123456789abcdef0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = secret.Delete(auth.KeySpotifyToken)
		_ = secret.Delete(auth.KeySpotifyRefreshToken)
	})
	if err := secret.Delete(auth.KeySpotifyRefreshToken); err != nil && !errors.Is(err, secret.ErrNotFound) {
		t.Fatal(err)
	}
	// 起始 token 是新鮮的(1 小時後才到期):⑤ 的強制換發是唯一該打 token 端點的地方。
	if err := auth.SaveToken(auth.KeySpotifyToken, &oauth2.Token{
		AccessToken: "at0", TokenType: "Bearer", RefreshToken: "rt0", Expiry: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	var refreshes atomic.Int32
	current := "rt0"
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := refreshes.Add(1)
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.PostForm.Get("refresh_token") != current {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		current = fmt.Sprintf("rt%d", n)
		fmt.Fprintf(w, `{"access_token":"at%d","token_type":"Bearer","expires_in":3600,"refresh_token":%q}`, n, current)
	}))
	defer tokenSrv.Close()
	origEndpoint := auth.SpotifyEndpoint
	auth.SpotifyEndpoint.TokenURL = tokenSrv.URL
	t.Cleanup(func() { auth.SpotifyEndpoint = origEndpoint })

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/player/devices" {
			t.Errorf("非預期 API 路徑:%s", r.URL.Path)
		}
		fmt.Fprint(w, `{"devices":[]}`)
	}))
	defer apiSrv.Close()
	origBase := spotifyAPIBase
	spotifyAPIBase = apiSrv.URL
	t.Cleanup(func() { spotifyAPIBase = origBase })

	buf := &strings.Builder{}
	runChecks(context.Background(), buf, spotifyChecks())
	out := buf.String()
	if !strings.Contains(out, "✅ Token 換發") {
		t.Errorf("⑤ 應通過:%q", out)
	}
	if !strings.Contains(out, "✅ Spotify API") {
		t.Errorf("⑥ 應通過(用 ⑤ 換好的 token,不再輪替):%q", out)
	}
	if n := refreshes.Load(); n != 1 {
		t.Errorf("一次 doctor 只該輪替 1 次 refresh token,實際 %d 次", n)
	}
	stored, err := auth.LoadToken(auth.KeySpotifyToken)
	if err != nil || stored.RefreshToken != "rt1" {
		t.Errorf("輪替後的 RT 必須留在 keychain:(%+v, %v)", stored, err)
	}
}

// TestDoctorAppleExpiredDevToken:keychain 有 developer token 但已過期 → ❌ 並提示過期。
func TestDoctorAppleExpiredDevToken(t *testing.T) {
	clearAppleTokens(t)
	expired := time.Now().Add(-1 * time.Hour)
	if err := apple.SaveDeveloperToken(fakeJWT(t, expired), expired); err != nil {
		t.Fatal(err)
	}
	detail, err := checkAppleDevToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "過期") {
		t.Fatalf("過期應回錯並含「過期」:(%q, %v)", detail, err)
	}
}

// ⭐ ④⑤ 不得把真正的錯誤吞掉印成「沒有 refresh token」/「需要先通過 refresh token 檢查」:
// 本分支起 keychain 內容可能是壞掉的 JSON,也可能整個讀不到(被拒絕存取、已鎖定)。
// 尤其⑤,吞掉後印出的那句會緊接在④的 ✅ 後面自相矛盾,使用者完全無從下手。
// 把兩處還原成無條件的固定字串,這個測試會掛。
func TestDoctorReportsCorruptKeychainInsteadOfMissing(t *testing.T) {
	setCLITestConfig(t)
	if err := config.Save(&config.Config{SpotifyClientID: "0123456789abcdef0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = secret.Delete(auth.KeySpotifyToken)
		_ = secret.Delete(auth.KeySpotifyRefreshToken)
	})
	if err := secret.Delete(auth.KeySpotifyRefreshToken); err != nil && !errors.Is(err, secret.ErrNotFound) {
		t.Fatal(err)
	}
	if err := secret.Set(auth.KeySpotifyToken, "{壞掉的 JSON"); err != nil {
		t.Fatal(err)
	}

	detail, err := checkRefreshToken(context.Background())
	if err == nil {
		t.Fatalf("內容毀損應失敗,得到 %q", detail)
	}
	if strings.Contains(err.Error(), "沒有 refresh token") {
		t.Errorf("內容毀損不是「沒有」:%v", err)
	}
	if !strings.Contains(err.Error(), "不是有效的 token JSON") {
		t.Errorf("應帶出真正的原因:%v", err)
	}

	detail, err = checkTokenRefresh(context.Background())
	if err == nil {
		t.Fatalf("內容毀損應失敗,得到 %q", detail)
	}
	if strings.Contains(err.Error(), "需要先通過 refresh token 檢查") {
		t.Errorf("④ 剛印過 ❌ 的真正原因,⑤ 不該改口說是前一項沒過:%v", err)
	}
	if !strings.Contains(err.Error(), "不是有效的 token JSON") {
		t.Errorf("應帶出真正的原因:%v", err)
	}
}
