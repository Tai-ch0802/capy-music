package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// 英文模式:本套件自己印的字(等鎖、回呼頁、登入流程、token store 的錯誤)逐字釘住;中文的斷言留在各自的測試檔。

func setEnglish(t *testing.T) {
	t.Helper()
	orig := i18n.Current()
	if !i18n.Set("en") {
		t.Fatal("en 不在支援清單")
	}
	t.Cleanup(func() { i18n.Set(orig) })
}

var cjkRE = regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}\x{3000}-\x{303F}\x{FF00}-\x{FFEF}]`)

func TestEnglishLockNoticeAndInterrupt(t *testing.T) {
	setTokenTest(t)
	setEnglish(t)
	origW, origAfter, origRetry := LockStderr, lockNoticeAfter, lockRetryInterval
	t.Cleanup(func() { LockStderr, lockNoticeAfter, lockRetryInterval = origW, origAfter, origRetry })
	lockNoticeAfter, lockRetryInterval = 10*time.Millisecond, 5*time.Millisecond
	var buf bytes.Buffer
	LockStderr = &buf

	unlock, err := lockFile(context.Background(), "t.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = lockFile(ctx, "t.lock")
	if want := "another capy is holding t.lock and the wait was interrupted or timed out; try again later: context deadline exceeded"; err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("要包住 ctx 的錯誤:%v", err)
	}
	if got, want := buf.String(), "Waiting for another capy to release t.lock (the holder is accessing the keychain and may be waiting on an authorization dialog — if a keychain dialog is on screen, click Allow to continue); press Ctrl-C to give up.\n"; got != want {
		t.Errorf("notice:\n got %q\nwant %q", got, want)
	}
}

func TestEnglishCallbackPages(t *testing.T) {
	setEnglish(t)
	ok, denied := callbackPage(false), callbackPage(true)
	for _, want := range []string{`<html lang="en">`, "<title>capy — Authorization complete</title>",
		"<h1>✅ Authorization complete</h1><p>You can close this tab and go back to the terminal.</p>", "<script>window.close()</script>"} {
		if !strings.Contains(ok, want) {
			t.Errorf("成功頁缺 %q:\n%s", want, ok)
		}
	}
	if !strings.Contains(denied, "<h1>❌ Authorization not completed</h1><p>You denied the authorization, or something went wrong. You can close this tab and try again in the terminal.</p>") ||
		strings.Contains(denied, "<script>") {
		t.Errorf("拒絕頁:\n%s", denied)
	}
	for _, p := range []string{ok, denied} {
		if cjkRE.MatchString(p) || strings.Contains(p, "http") {
			t.Errorf("英文回呼頁不該有中文或外部資源:\n%s", p)
		}
	}
}

func TestEnglishLoginSpotifyMessages(t *testing.T) {
	setTokenTest(t)
	setEnglish(t)
	var stderr bytes.Buffer
	origStderr := LoginStderr
	LoginStderr = &stderr
	t.Cleanup(func() { LoginStderr = origStderr })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := LoginSpotify(ctx, "cid123", func(string) error { return errors.New("no display") })
	if want := "no authorization callback within 180 seconds — if the browser shows INVALID_CLIENT / Invalid redirect URI, make sure the dashboard's Redirect URI is exactly http://127.0.0.1:8888/callback; you can reset the client ID with --client-id"; err == nil || err.Error() != want {
		t.Errorf("timeout: %v", err)
	}
	lines := strings.Split(stderr.String(), "\n")
	if len(lines) != 4 || lines[0] != "If your browser doesn't open, go to:" || !strings.HasPrefix(lines[1], "  https://accounts.spotify.com/authorize?") ||
		lines[2] != "Couldn't open the browser: no display" || lines[3] != "" {
		t.Errorf("stderr: %q", stderr.String())
	}

	denied := func(authURL string) error {
		done := make(chan struct{})
		t.Cleanup(func() { <-done })
		go func() {
			defer close(done)
			u, _ := url.Parse(authURL)
			q := u.Query()
			resp, err := http.Get(q.Get("redirect_uri") + "?error=access_denied&state=" + q.Get("state"))
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
		}()
		return nil
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := LoginSpotify(ctx2, "cid123", denied); err == nil || err.Error() != "authorization denied: access_denied" {
		t.Errorf("denied: %v", err)
	}
}

func TestEnglishLoginGoogleDownscope(t *testing.T) {
	setTokenTest(t)
	setEnglish(t)
	origStderr := LoginStderr
	LoginStderr = &bytes.Buffer{}
	t.Cleanup(func() { LoginStderr = origStderr })
	srv := googleTokenServer(t, "openid", "x@y", "")
	swapGoogleTokenURL(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := LoginGoogle(ctx, GoogleClient{ID: "cid"}, fakeAuthBrowser(t, "code1"))
	if !errors.Is(err, ErrGoogleScope) || !strings.HasSuffix(err.Error(), `; run capy auth login google again (granted: "openid")`) {
		t.Errorf("err = %v", err)
	}
}

func TestEnglishGoogleExplanations(t *testing.T) {
	setEnglish(t)
	clock := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	orig := now
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = orig })
	grant := &oauth2.RetrieveError{ErrorCode: "invalid_grant", ErrorDescription: "Token has been expired or revoked."}

	for _, c := range []struct {
		issued time.Time
		want   string
	}{
		{clock.Add(-3 * 24 * time.Hour), "the Google refresh token is no longer valid (issued only 72h0m0s ago): most likely the OAuth consent screen in Google Cloud Console is still in Testing — refresh tokens issued in Testing expire after 7 days. Go to Google Auth platform → Audience, click Publish app, then run capy auth login google again. Other possibilities: you revoked capy at https://myaccount.google.com/permissions, or the same client has more than 100 tokens (the oldest ones are dropped). Original error: Token has been expired or revoked."},
		{time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "the Google refresh token is no longer valid (issued 2026-01-02): Google may have revoked it after 6 months without use, or you revoked access — run capy auth login google again. Original error: Token has been expired or revoked."},
		{time.Time{}, "the Google refresh token is no longer valid: Google may have revoked it after 6 months without use, or you revoked access — run capy auth login google again. Original error: Token has been expired or revoked."},
	} {
		if err := explainGoogleGrant(grant, c.issued); !errors.Is(err, ErrGoogleGrant) || err.Error() != c.want {
			t.Errorf("issued %v:\n got %v\nwant %s", c.issued, err, c.want)
		}
	}
	err := explainGoogleClient(&oauth2.RetrieveError{ErrorCode: "invalid_client", ErrorDescription: "Unauthorized"})
	if want := "Google rejected this client ID / secret (invalid_client): a Desktop client needs its secret to exchange tokens — provide it again with --client-secret / CAPY_GOOGLE_CLIENT_SECRET (logout deletes the secret from the keychain; if you regenerated it in Cloud Console, the old one no longer works). Original error: Unauthorized"; !errors.Is(err, ErrGoogleClient) || err.Error() != want {
		t.Errorf("invalid_client: %v", err)
	}
}

func TestEnglishTokenStoreErrors(t *testing.T) {
	setTokenTest(t)
	setEnglish(t)
	if err := SaveToken(testKey, &oauth2.Token{AccessToken: "at"}); err == nil || err.Error() != "the token has no refresh token; refusing to write it to the keychain" {
		t.Errorf("SaveToken: %v", err)
	}
	if err := secret.Set(testKey, "not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(testKey); err == nil || !strings.HasPrefix(err.Error(), "the keychain entry test.token isn't valid token JSON (run capy auth login again): ") {
		t.Errorf("LoadToken: %v", err)
	}
}

// refresh 成功但寫不回 keychain:使用者等於已登出,英文訊息要講明並給下一步。
func TestEnglishRefreshedButNotSaved(t *testing.T) {
	setTokenTest(t)
	setEnglish(t)
	shortSaveRetry(t)
	t.Cleanup(func() { keyring.MockInit() })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyring.MockInitWithError(errors.New("boom"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at3","token_type":"Bearer","expires_in":3600,"refresh_token":"rt-new"}`))
	}))
	defer srv.Close()
	if err := SaveToken(testKey, staleToken("rt-old")); err != nil {
		t.Fatal(err)
	}
	ts, err := NewTokenSource(context.Background(), testConf(srv.URL), testKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ts.Token()
	if want := "the token was refreshed but couldn't be written back to the keychain: the old refresh token is now invalid and the new one wasn't saved (you are effectively logged out) — run capy auth login test to log in again: boom"; err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
}
