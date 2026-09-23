package cli

import (
	"encoding/json"
	"errors"
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
