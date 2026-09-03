package apple

import (
	"bytes"
	"context"
	"errors"
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
	// musickitloaded 競態 guard:async 載入的 CDN script 可能搶在 inline script 掛上
	// 事件監聽器前就完成並觸發 musickitloaded,所以要先看 window.MusicKit 是否已存在。
	if !strings.Contains(authorizePage, "window.MusicKit") {
		t.Fatal("應先檢查 window.MusicKit 是否已載入,避免 musickitloaded 競態(事件在監聽器掛上前就觸發)")
	}
}

// fakeBrowserPoster 模擬真實頁面 JS 的行為:GET 頁面 → 從頁面撈 state → POST MUT。
//
// done + t.Cleanup:AuthorizeMUT 收到結果就會 return,defer lb.Close() 可能
// 搶在這個 goroutine 的 http.PostForm 拿到回應之前關掉伺服器,導致此 goroutine
// 在 test 已標記完成後才呼叫 t.Error/t.Errorf(→ panic)。t.Cleanup 在完成標記
// 前執行,drain done 保證 goroutine 收工在 test 存活期間。
func fakeBrowserPoster(t *testing.T, mut string, wantDT string) func(string) error {
	t.Helper()
	return func(pageURL string) error {
		done := make(chan struct{})
		t.Cleanup(func() { <-done })
		go func() {
			defer close(done)
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
		done := make(chan struct{})
		t.Cleanup(func() { <-done })
		go func() {
			defer close(done)
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

// SSH/headless 場景 openBrowser 會失敗,但使用者仍可手動複製 stderr 印出的 URL 完成授權——
// 開瀏覽器失敗不該讓整個 MUT 授權中止(P1 交接便條 #3,對齊 spotify.go 的行為)。
func TestAuthorizeMUTBrowserOpenFailStillCompletes(t *testing.T) {
	stderrBuf := &bytes.Buffer{}
	origStderr := AuthStderr
	AuthStderr = stderrBuf
	t.Cleanup(func() { AuthStderr = origStderr })

	// 仍沿用 fakeBrowserPoster 的 done-channel 模式觸發真正的 GET+POST;
	// 外層 openBrowser 改回錯,模擬 exec.Command("open",...) 失敗。
	poster := fakeBrowserPoster(t, "FAKE_MUT2", "FAKE_DT2")
	failingBrowser := func(pageURL string) error {
		_ = poster(pageURL)
		return errors.New("exec: \"open\": executable file not found in $PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	mut, err := AuthorizeMUT(ctx, "FAKE_DT2", failingBrowser)
	if err != nil {
		t.Fatalf("開瀏覽器失敗不應中止授權:%v", err)
	}
	if mut != "FAKE_MUT2" {
		t.Errorf("mut = %q", mut)
	}
	out := stderrBuf.String()
	if !strings.Contains(out, "/apple/authorize") {
		t.Errorf("應印出授權頁 URL:%q", out)
	}
	if !strings.Contains(out, "無法自動開瀏覽器") {
		t.Errorf("應印出開瀏覽器失敗訊息:%q", out)
	}
}
