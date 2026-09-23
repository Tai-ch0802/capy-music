package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

// 英文模式:web.go / web_api.go / web_run.go 的訊息(T2c webserver)。只驗這三個檔自己產生的字。

// webEnglish:config 設成 en(每個 job 的 applyLanguage 讀它)並切到 en(job 之前的路徑、直達端點讀目前語系)。
func webEnglish(t *testing.T) {
	t.Helper()
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	c.Language = "en"
	if err := config.Save(c); err != nil {
		t.Fatal(err)
	}
	withLanguage(t, "en")
}

// errBody:回應的 JSON error 欄位(httpErr)。
func errBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var e struct{ Error string }
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatal(err)
	}
	return e.Error
}

func TestEnglishWebHTTPErrors(t *testing.T) {
	setCLITestConfig(t)
	webEnglish(t)
	s, c := startWeb(t)
	ctx := context.Background()

	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"auth", "login", "apple", "--auto"}, 403, "--auto: --auto is the one exception in CLAUDE.md, for developers in their own terminal only"},
		{[]string{"--web"}, 403, "--web: you can't start another capy --web from the web UI"},
		{[]string{"auth", "login", "google", "--client-secret=x"}, 403, "--client-secret: in the web UI, use the wizard instead of typing the secret on the command line"},
		{[]string{"auth", "login", "apple", "--user-token", "x"}, 403, "--user-token: in the web UI, use the wizard instead of typing the token on the command line"},
		{[]string{"debug", "apple-token"}, 403, "capy debug apple-token isn't available in the web UI"}, // 允許清單在 applyLanguage 之後
	} {
		if code, _, msg := c.run(map[string]any{"args": tc.args}); code != tc.code || msg != tc.want {
			t.Errorf("%v:%d %q", tc.args, code, msg)
		}
	}
	if code, _, msg := c.run("{"); code != 400 || !strings.HasPrefix(msg, "malformed JSON: json: ") {
		t.Errorf("JSON 壞掉:%d %q", code, msg)
	}
	for path, want := range map[string]string{
		"/api/nope":                         "no such endpoint",
		"/api/now?provider=tidal":           "unknown provider tidal",
		"/api/isrc/USRC17607839?provider=x": "unknown provider x",
	} {
		resp := c.req(ctx, http.MethodGet, path, nil, nil)
		if got := errBody(t, resp); got != want {
			t.Errorf("%s:%d %q", path, resp.StatusCode, got)
		}
	}
	if resp := c.req(ctx, http.MethodPost, "/api/jobs/99/cancel", nil, nil); resp.StatusCode != 404 || errBody(t, resp) != "no such job (already finished?)" {
		t.Errorf("cancel 沒有 job:%d", resp.StatusCode)
	}
	if got := errBody(t, c.req(ctx, http.MethodGet, "/api/commands", nil, map[string]string{"X-Capy-Token": "nope"})); got != "wrong or expired token: go back to the URL printed when capy --web started" {
		t.Errorf("401:%q", got)
	}
	// guard 走 http.Error(純文字、結尾換行)。
	for _, tc := range []struct {
		hdr  map[string]string
		code int
		want string
	}{
		{map[string]string{"Host": "localhost"}, 421, "Host must be " + s.hostport + "\n"},
		{map[string]string{"Origin": "http://evil.example"}, 403, "the request's Origin doesn't match this page\n"},
		{map[string]string{"Sec-Fetch-Site": "cross-site"}, 403, "cross-site request\n"},
	} {
		resp := c.req(ctx, http.MethodGet, "/api/commands", nil, tc.hdr)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.code || string(b) != tc.want {
			t.Errorf("%v:%d %q", tc.hdr, resp.StatusCode, b)
		}
	}
	s.stale.Store(true)
	if code, _, msg := c.run(map[string]any{"args": []string{}}); code != 503 || msg != "the capy binary on disk has been updated, but this capy --web is still the old version; restart it" {
		t.Errorf("503:%d %q", code, msg)
	}
}

func TestEnglishWebBusyAndWatch(t *testing.T) {
	fs, _, _ := pullWorld(t)
	webEnglish(t)
	_, c := startWeb(t)
	_, events, _ := c.run(map[string]any{"args": []string{"now", "--watch"}})
	if msg := evExit(t, events)["message"]; msg != "Error: in the web UI, watch the playback panel on the page; for a one-off check, use capy now" {
		t.Errorf("now --watch:%q", msg)
	}

	entered, release := blockingHook(t, fs)
	defer release()
	done := make(chan struct{})
	go func() { defer close(done); c.run(map[string]any{"args": []string{"search", "x"}}) }()
	waitFor(t, "job 進 hook", entered)
	if code, _, msg := c.run(map[string]any{"args": []string{"search", "y"}}); code != http.StatusConflict || msg != "another command is running; wait for it to finish or press Stop" {
		t.Errorf("409:%d %q", code, msg)
	}
	release()
	waitFor(t, "job 結束", done)
}

// TestEnglishWebLockNotice:改寫只認檔名與「Ctrl-C」(任何語系都不翻),不比對 auth 的措辭——
// 這裡的英文原句是假設的寫法,換成別的句子只要還有檔名與 " Ctrl-C" 結果就一樣。
func TestEnglishWebLockNotice(t *testing.T) {
	withLanguage(t, "en")
	var b bytes.Buffer
	w := &webLockStderr{&b}
	io.WriteString(w, "Waiting for another capy to release spotify.token.lock (it's using the keychain); press Ctrl-C to give up.\n")
	if got, want := b.String(), "Waiting for a token lock to be released (another capy, or this page's playback panel / ISRC page is refreshing the token): spotify.token.lock (it's using the keychain); press Stop to give up.\n"; got != want {
		t.Errorf("token 鎖:\n got %q\nwant %q", got, want)
	}
	b.Reset()
	io.WriteString(w, "Waiting for another capy to release pull.lock (it is syncing playlists); press Ctrl-C to give up.\n")
	if got, want := b.String(), "Waiting for another capy to release pull.lock (it is syncing playlists); press Stop to give up.\n"; got != want {
		t.Errorf("pull.lock:只換 Ctrl-C:\n got %q\nwant %q", got, want)
	}
}

// TestWebLockNoticeZhUnchanged:改成只認檔名與 Ctrl-C 之後,zh-TW 的輸出跟舊的逐字替換一個位元組都不差。
func TestWebLockNoticeZhUnchanged(t *testing.T) {
	withLanguage(t, "zh-TW")
	for in, want := range map[string]string{
		"等待另一個 capy 釋放 spotify.token.lock(對方正在換發);要放棄按 Ctrl-C。\n": "等待 token 鎖釋放(另一個 capy,或本頁面的播放面板 / ISRC 頁正在換發 token):spotify.token.lock(對方正在換發);要放棄按「中止」。\n",
		"等待另一個 capy 釋放 pull.lock(對方正在同步播放清單);要放棄按 Ctrl-C。\n":      "等待另一個 capy 釋放 pull.lock(對方正在同步播放清單);要放棄按「中止」。\n",
	} {
		var b bytes.Buffer
		io.WriteString(&webLockStderr{&b}, in)
		if b.String() != want {
			t.Errorf("\n got %q\nwant %q", b.String(), want)
		}
	}
	// web.deny 的分隔是舊碼的半形 ":"、沒有空白(唯一不是從原字面抄進目錄的一則)。
	if got := webDenied([]string{"--auto=1"}); got != "--auto:"+i18n.T("web.deny.auto") {
		t.Errorf("deny:%q", got)
	}
}

func TestEnglishWebStartupAndPortErrors(t *testing.T) {
	setCLITestConfig(t)
	withLanguage(t, "en")
	for port, want := range map[string]string{
		"8888": "--port 8888 is reserved for the Spotify authorization callback; pick another port",
		"443":  "--port 80 / 443 can't be used: browsers drop the default port from the Host header, so every request would be rejected",
	} {
		if _, err := runCLI(t, "--web", "--port", port); err == nil || err.Error() != want {
			t.Errorf("--port %s:%v", port, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &lockedBuffer{}
	cmd := newRootCmd()
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"--web"})
	errc := make(chan error, 1)
	go func() { errc <- cmd.ExecuteContext(ctx) }()
	urlRe := regexp.MustCompile(`http://127\.0\.0\.1:\d+/#t=[A-Za-z0-9_\-]+`)
	var url string
	for deadline := time.Now().Add(5 * time.Second); url == ""; time.Sleep(20 * time.Millisecond) {
		if url = urlRe.FindString(out.String()); url == "" && time.Now().After(deadline) {
			t.Fatalf("沒印出網址:%q", out.String())
		}
	}
	// 網址原樣(機器讀得到),後面的說明是英文。
	if got, want := out.String(), "capy --web is running: "+url+"\n(bound to 127.0.0.1 only; the URL stops working when this process exits; press Ctrl-C to quit)\n"; got != want {
		t.Errorf("啟動行:\n got %q\nwant %q", got, want)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Errorf("ctx 取消 = 正常結束:%v", err)
	}
}

// TestWebConsoleHidesCancelledInEveryLanguage:console.js 靠比對 web.err.cancelled 的文字藏掉重複的「已取消」
// (計畫 §2.4 第 5 點):每個語系的那句都要在它的正規式裡,新增語系或改譯文時這裡會先壞。
func TestWebConsoleHidesCancelledInEveryLanguage(t *testing.T) {
	b, err := webUI.ReadFile("webui/js/console.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`\^\(Error: \)\?\(([^)]*)\)\$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("console.js 找不到藏掉取消訊息的正規式")
	}
	alts := strings.Split(string(m[1]), "|")
	prev := i18n.Current()
	t.Cleanup(func() { i18n.Set(prev) })
	for _, l := range i18n.Supported() {
		i18n.Set(l)
		if msg := errWebCancelled.Error(); !slices.Contains(alts, msg) {
			t.Errorf("%s 的 web.err.cancelled %q 不在 console.js 的 %v 裡", l, msg, alts)
		}
	}
}
