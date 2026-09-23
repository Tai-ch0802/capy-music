package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// TestAuthStatusNeverLeaksSecrets(安全測試,先寫):keychain 裡每一種 token / secret 都種哨兵值,
// auth status --json 與純文字 auth status 的 stdout + stderr 裡一個都不可以出現。
func TestAuthStatusNeverLeaksSecrets(t *testing.T) {
	setCLITestConfig(t)
	t.Cleanup(keyring.MockInit)
	exp := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	dev := strings.TrimSuffix(fakeJWT(t, exp), ".sig") + ".SENTINEL-APPLE-DEV-SIG"
	sentinels := []string{
		"SENTINEL-SPOTIFY-REFRESH", "SENTINEL-SPOTIFY-ACCESS", "SENTINEL-SPOTIFY-LEGACY-REFRESH",
		"SENTINEL-GOOGLE-REFRESH", "SENTINEL-GOOGLE-ACCESS", "SENTINEL-GOOGLE-CLIENT-SECRET",
		dev, "SENTINEL-APPLE-DEV-SIG", "SENTINEL-APPLE-USER",
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(auth.SaveToken(auth.KeySpotifyToken, &oauth2.Token{AccessToken: "SENTINEL-SPOTIFY-ACCESS", RefreshToken: "SENTINEL-SPOTIFY-REFRESH", Expiry: exp}))
	must(secret.Set(auth.KeySpotifyRefreshToken, "SENTINEL-SPOTIFY-LEGACY-REFRESH"))
	must(auth.SaveToken(auth.KeyGoogleToken, &oauth2.Token{AccessToken: "SENTINEL-GOOGLE-ACCESS", RefreshToken: "SENTINEL-GOOGLE-REFRESH", Expiry: exp}))
	must(secret.Set(auth.KeyGoogleClientSecret, "SENTINEL-GOOGLE-CLIENT-SECRET"))
	must(apple.SaveDeveloperToken(dev, exp))
	must(secret.Set(apple.KeyMusicUserToken, "SENTINEL-APPLE-USER"))
	must(config.Save(&config.Config{SpotifyClientID: strings.Repeat("ab", 16), GoogleClientID: "1234-byo.apps.googleusercontent.com",
		GoogleEmail: "me@example.com", DeviceID: "dev1", AppleStorefront: "tw"}))

	for _, args := range [][]string{{"auth", "status", "--json"}, {"auth", "status"}} {
		out, err := runCLI(t, args...) // runCLI 的 stdout 與 stderr 寫進同一個 buffer
		if err != nil {
			t.Fatalf("%v:%v", args, err)
		}
		for _, s := range sentinels {
			if strings.Contains(out, s) {
				t.Errorf("%v 印出了 keychain 裡的秘密 %q:\n%s", args, s, out)
			}
		}
	}

	// 不是空轉:種下去的東西真的被讀到了(每一項都是 ok),非機密的事實照給。
	out, _ := runCLI(t, "auth", "status", "--json")
	var st authStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("--json 要是 JSON:%v\n%s", err, out)
	}
	want := exp.UTC().Format(time.RFC3339)
	if st.Spotify.State != "ok" || st.Spotify.ClientID != "set" ||
		st.Google.State != "ok" || st.Google.Client != "config" || st.Google.Email != "me@example.com" || st.Google.DeviceID != "dev1" || st.Google.AccessTokenExpiry != want ||
		st.Apple.State != "ok" || st.Apple.DeveloperToken != "ok" || st.Apple.UserToken != "ok" || st.Apple.DeveloperTokenExpiry != want || st.Apple.Storefront != "tw" {
		t.Errorf("全部登入:%+v", st)
	}
}

// TestAuthStatusJSONStates:沒登入、Apple 過期、keychain 讀不到三種狀態的列舉值(README 的契約)。
func TestAuthStatusJSONStates(t *testing.T) {
	setCLITestConfig(t)
	t.Cleanup(keyring.MockInit)
	keyring.MockInit()
	status := func() (authStatus, string) {
		t.Helper()
		out, err := runCLI(t, "auth", "status", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var st authStatus
		if err := json.Unmarshal([]byte(out), &st); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return st, out
	}
	st, out := status()
	if st.Spotify.State != "missing" || st.Spotify.ClientID != "missing" || st.Google.State != "missing" || st.Google.Client != "none" ||
		st.Apple.State != "missing" || st.Apple.DeveloperToken != "missing" || st.Apple.UserToken != "missing" {
		t.Errorf("什麼都沒有:%+v", st)
	}
	for _, field := range []string{`"email"`, `"device_id"`, `"storefront"`, `"access_token_expiry"`, `"developer_token_expiry"`} {
		if strings.Contains(out, field) {
			t.Errorf("沒有值的欄位不印(%s):%s", field, out)
		}
	}

	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := apple.SaveDeveloperToken(fakeJWT(t, past), past); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(&config.Config{SpotifyClientID: "not-hex"}); err != nil {
		t.Fatal(err)
	}
	if st, _ = status(); st.Apple.State != "expired" || st.Apple.DeveloperToken != "expired" || st.Apple.DeveloperTokenExpiry != past.UTC().Format(time.RFC3339) || st.Spotify.ClientID != "malformed" {
		t.Errorf("Apple 過期、client id 格式不對:%+v", st)
	}

	keyring.MockInitWithError(errors.New("locked"))
	if st, _ = status(); st.Spotify.State != "keychain_error" || st.Google.State != "keychain_error" || st.Apple.State != "keychain_error" ||
		st.Apple.DeveloperToken != "keychain_error" || st.Apple.UserToken != "keychain_error" {
		t.Errorf("keychain 讀不到不是「沒登入」:%+v", st)
	}
}

// TestWebAccountPageKeysOnAuthStatusJSON:帳號頁(搬家精靈也借它的 stateOf)讀 auth status --json 的 state,不再比對跟著語系的
// 文字(計畫 §2.4 第 1 點)。兩邊一起釘:account.js 認的每個 case 都是 CLI 在對應狀態真的給的值、CLI 給的每個值 account.js 都認
// (missing 走預設分支)。CLI 改了列舉值,這裡紅並指向 account.js,而不是讓網頁靜默判成未登入。
func TestWebAccountPageKeysOnAuthStatusJSON(t *testing.T) {
	b, err := webUI.ReadFile("webui/js/pages/account.js")
	if err != nil {
		t.Fatal(err)
	}
	account := string(b)
	if !strings.Contains(account, "con.run('auth status --json'") {
		t.Error("帳號頁要跑 auth status --json")
	}
	for _, prose := range []string{"keychain 存在", "in the keychain", "有效至", "valid until"} {
		if strings.Contains(account, prose) {
			t.Errorf("account.js 不可以再比對 auth status 給人看的文字 %q", prose)
		}
	}
	// 借 parseStatus 的頁面(搬家精靈)跑的也要是 --json:沒帶的話 parseStatus 讀不懂,每個平台都畫成未登入。
	if err := walkEmbedded(t, "webui/js", func(name string, b []byte) {
		if src := string(b); strings.Contains(src, "parseStatus(") && !strings.HasSuffix(name, "/account.js") &&
			!strings.Contains(src, "args: ['auth', 'status', '--json']") {
			t.Errorf("%s 用 parseStatus 讀帳號狀態,跑的 auth status 要帶 --json", name)
		}
	}); err != nil {
		t.Fatal(err)
	}

	setCLITestConfig(t)
	t.Cleanup(keyring.MockInit)
	keyring.MockInit()
	seen := map[string]bool{}
	observe := func() {
		t.Helper()
		out, err := runCLI(t, "auth", "status", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var st authStatus
		if err := json.Unmarshal([]byte(out), &st); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		seen[st.Spotify.State], seen[st.Google.State], seen[st.Apple.State] = true, true, true
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	observe() // 什麼都沒有:missing
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	must(auth.SaveToken(auth.KeySpotifyToken, &oauth2.Token{AccessToken: "a", RefreshToken: "r", Expiry: exp}))
	must(auth.SaveToken(auth.KeyGoogleToken, &oauth2.Token{AccessToken: "a", RefreshToken: "r", Expiry: exp}))
	must(apple.SaveDeveloperToken(fakeJWT(t, exp), exp))
	must(secret.Set(apple.KeyMusicUserToken, "u"))
	observe() // 三家都登入:ok
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	must(apple.SaveDeveloperToken(fakeJWT(t, past), past))
	observe() // Apple 過期:expired
	keyring.MockInitWithError(errors.New("locked"))
	observe() // keychain 讀不到:keychain_error

	for _, s := range []string{"missing", "ok", "expired", "keychain_error"} {
		if !seen[s] {
			t.Errorf("auth status --json 沒給出 state %q(看過 %v):這個測試的四種狀態沒種對", s, seen)
		}
	}
	cases := map[string]bool{"missing": true} // stateOf 的預設分支
	for _, m := range regexp.MustCompile(`case '(\w+)'`).FindAllStringSubmatch(account, -1) {
		cases[m[1]] = true
		if !seen[m[1]] {
			t.Errorf("account.js 認 state %q,但 auth status --json 不會給這個值", m[1])
		}
	}
	for s := range seen {
		if !cases[s] {
			t.Errorf("auth status --json 會給 state %q,account.js 的 stateOf 沒有認它", s)
		}
	}

	// 真的走 /api/run(允許清單、deny、序列槽):頁面送的就是這一行,stdout 要是 parseStatus 讀得懂的 JSON。
	_, c := startWeb(t)
	code, events, msg := c.run(map[string]any{"line": "auth status --json"})
	var st authStatus
	if code != http.StatusOK || evExit(t, events)["code"] != float64(0) || json.Unmarshal([]byte(evText(events, "stdout")), &st) != nil || st.Apple.State == "" {
		t.Errorf("網頁跑 auth status --json:%d %s %v", code, msg, events)
	}
}
