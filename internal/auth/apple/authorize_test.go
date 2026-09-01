package apple

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestAuthorizePageToSConstraint(t *testing.T) {
	if got := strings.Count(authorizePage, "<script src="); got != 1 {
		t.Fatalf("授權頁只准一個外部 script,發現 %d 個", got)
	}
	if !strings.Contains(authorizePage, `src="https://js-cdn.music.apple.com/musickit/v3/musickit.js"`) {
		t.Fatal("外部 script 必須指向 Apple CDN 的 musickit v3")
	}
}

// fakeBrowserPoster 模擬真實頁面 JS 的行為:GET 頁面 → 從頁面撈 state → POST MUT。
func fakeBrowserPoster(t *testing.T, mut string, wantDT string) func(string) error {
	t.Helper()
	return func(pageURL string) error {
		go func() {
			resp, err := http.Get(pageURL)
			if err != nil {
				t.Error(err)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !strings.Contains(string(body), wantDT) {
				t.Errorf("頁面應含 developer token %q", wantDT)
			}
			m := regexp.MustCompile(`STATE = "([A-Za-z0-9_-]+)"`).FindSubmatch(body)
			if m == nil {
				t.Error("頁面沒有渲染 state")
				return
			}
			base := strings.TrimSuffix(pageURL, "/apple/authorize")
			_, err = http.PostForm(base+"/apple/callback", url.Values{
				"state":            {string(m[1])},
				"music_user_token": {mut},
			})
			if err != nil {
				t.Error(err)
			}
		}()
		return nil
	}
}

func TestAuthorizeMUTFullLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	mut, err := AuthorizeMUT(ctx, "FAKE_DT", fakeBrowserPoster(t, "FAKE_MUT", "FAKE_DT"))
	if err != nil {
		t.Fatal(err)
	}
	if mut != "FAKE_MUT" {
		t.Errorf("mut = %q", mut)
	}
}

func TestAuthorizeMUTRejectsBadState(t *testing.T) {
	badPoster := func(pageURL string) error {
		go func() {
			base := strings.TrimSuffix(pageURL, "/apple/authorize")
			resp, err := http.PostForm(base+"/apple/callback", url.Values{
				"state":            {"WRONG"},
				"music_user_token": {"EVIL"},
			})
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("錯誤 state 應回 400,得到 %d", resp.StatusCode)
			}
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := AuthorizeMUT(ctx, "DT", badPoster); err == nil {
		t.Fatal("錯誤 state 不應成功,應逾時")
	}
}
