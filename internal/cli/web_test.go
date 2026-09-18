package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// ── 測試骨架 ──

// startWeb:行程內起一個 web 伺服器(httptest 綁 127.0.0.1 隨機 port,Host 比對用實際位址)、裝接縫、退出還原。
// 清理順序:先取消伺服器 ctx(砍 job)→ 關 httptest → 還原接縫。
func startWeb(t *testing.T) (*webServer, *webClient) {
	t.Helper()
	if os.Getenv("CAPY_CONFIG_DIR") == "" {
		setCLITestConfig(t)
	}
	executableReplaced.Store(false) // update 的測試會真的 replaceExecutable,旗標是 process 全域
	t.Cleanup(func() { executableReplaced.Store(false) })
	ctx, cancel := context.WithCancel(context.Background())
	s, err := newWebServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.fallbackStderr = &lockedBuffer{}
	restore := installWebSeams(s)
	t.Cleanup(restore)
	srv := httptest.NewUnstartedServer(nil)
	s.hostport = srv.Listener.Addr().String()
	srv.Config.Handler = s.handler()
	srv.Start()
	t.Cleanup(srv.Close)
	t.Cleanup(cancel)
	if !strings.HasPrefix(s.hostport, "127.0.0.1:") {
		t.Fatalf("httptest 要綁 127.0.0.1:%s", s.hostport)
	}
	return s, &webClient{t: t, srv: srv, token: s.token}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type webClient struct {
	t     *testing.T
	srv   *httptest.Server
	token string
}

// req:帶 token 的請求;hdr 的 "Host" 特別處理(Go 用 req.Host);值為空 = 刪掉該標頭。
func (c *webClient) req(ctx context.Context, method, path string, body any, hdr map[string]string) *http.Response {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.srv.URL+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("X-Capy-Token", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		switch {
		case k == "Host":
			req.Host = v
		case v == "":
			req.Header.Del(k)
		default:
			req.Header.Set(k, v)
		}
	}
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

func (c *webClient) status(method, path string, body any, hdr map[string]string) int {
	c.t.Helper()
	resp := c.req(context.Background(), method, path, body, hdr)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// run:POST /api/run 並讀完整條 SSE;非 200 回狀態碼與 error 訊息。
func (c *webClient) run(body any) (int, []map[string]any, string) {
	c.t.Helper()
	resp := c.req(context.Background(), http.MethodPost, "/api/run", body, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return resp.StatusCode, nil, e.Error
	}
	return http.StatusOK, readSSE(c.t, resp.Body), ""
}

func readSSE(t *testing.T, r io.Reader) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var ev map[string]any
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatalf("SSE 事件不是 JSON:%q(%v)", data, err)
			}
			out = append(out, ev)
		}
	}
	return out
}

func evText(events []map[string]any, kind string) string {
	var sb strings.Builder
	for _, e := range events {
		if e["type"] == kind {
			sb.WriteString(e["text"].(string))
		}
	}
	return sb.String()
}

func evExit(t *testing.T, events []map[string]any) map[string]any {
	t.Helper()
	if len(events) == 0 || events[len(events)-1]["type"] != "exit" {
		t.Fatalf("最後一個事件要是 exit:%v", events)
	}
	return events[len(events)-1]
}

func evFirst(events []map[string]any, kind string) map[string]any {
	for _, e := range events {
		if e["type"] == kind {
			return e
		}
	}
	return nil
}

// blockingHook:讓假 Spotify 的下一個請求卡在 hook 裡,直到 release;entered 在進入時關閉一次。
func blockingHook(t *testing.T, fs *fakeSpotify) (entered <-chan struct{}, release func()) {
	t.Helper()
	ent, rel := make(chan struct{}), make(chan struct{})
	var once, relOnce sync.Once
	fs.setHook(func() {
		once.Do(func() { close(ent) })
		<-rel
	})
	release = func() { relOnce.Do(func() { close(rel) }) }
	t.Cleanup(release) // 在假 API 的 srv.Close 之前放行(cleanup 是 LIFO,pullWorld 先註冊)
	return ent, release
}

func waitFor(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("等 %s 逾時", what)
	}
}

// ── 執行與串流 ──

func TestWebRunStreamsStdoutStderrTableAndExit(t *testing.T) {
	setCLITestConfig(t)
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tracks":{"items":[` + strings.Replace(cliTrackFx, "%s", "t1", -1) + `],"total":1}}`))
	})
	_, c := startWeb(t)
	code, events, _ := c.run(map[string]any{"args": []string{"search", "x"}})
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if st := evFirst(events, "start"); st == nil || st["path"] != "capy search" || st["job"] == "" {
		t.Errorf("start 事件要帶 path 與 job:%v", st)
	}
	tb := evFirst(events, "table")
	if tb == nil {
		t.Fatalf("要有 table 事件:%v", events)
	}
	hdr, _ := json.Marshal(tb["header"])
	if string(hdr) != `["ID","曲名","藝人","專輯","時長"]` {
		t.Errorf("table 標題就是 ui.Table 的 header 切片:%s", hdr)
	}
	rows := tb["rows"].([]any)
	if len(rows) != 1 || rows[0].([]any)[0] != "t1" || rows[0].([]any)[4] != "227000" {
		t.Errorf("rows 是非 TTY 儲存格(時長毫秒整數):%v", rows)
	}
	if evText(events, "stdout") != "" {
		t.Errorf("表格走 TableWriter,stdout 不該有 TSV:%q", evText(events, "stdout"))
	}
	ex := evExit(t, events)
	if ex["code"] != float64(0) || ex["reason"] != "done" || ex["message"] != "" {
		t.Errorf("exit:%v", ex)
	}

	// 錯誤:exit 1、message 帶「Error: 」前綴(同 main.go 印到 stderr 的那句)。
	code, events, _ = c.run(map[string]any{"args": []string{"nope"}})
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if ex := evExit(t, events); ex["code"] != float64(1) || !strings.HasPrefix(ex["message"].(string), "Error: ") {
		t.Errorf("未知子命令要 exit 1 帶 Error: 前綴:%v", ex)
	}

	// line 版:伺服器端 splitArgs,雙引號內空白保留。
	code, events, _ = c.run(map[string]any{"line": `search "x y"`})
	if code != 200 || evFirst(events, "start")["args"].([]any)[1] != "x y" {
		t.Errorf("line 要經 splitArgs:%d %v", code, evFirst(events, "start"))
	}
}

// TestWebRunArgsNilNeverFallsBackToOsArgs:【fails-before-fix】body 兩者都空要跑 []string{} 不是 nil——
// cobra 對 nil 會退回 os.Args[1:](go test 的 -test.* 旗標)→ 未知旗標 exit 1。
func TestWebRunArgsNilNeverFallsBackToOsArgs(t *testing.T) {
	_, c := startWeb(t)
	code, events, _ := c.run(map[string]any{})
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if ex := evExit(t, events); ex["code"] != float64(0) || !strings.Contains(evText(events, "stdout"), "Usage:") {
		t.Errorf("空 args = bare capy = help、exit 0:%v %q", ex, evText(events, "stdout"))
	}
	// test binary 裡 cobra 不會真的退回 os.Args(它認得 .test),行為看不出來;start 事件看得出來:[] 不是 null。
	if args, ok := evFirst(events, "start")["args"].([]any); !ok || len(args) != 0 {
		t.Errorf("start.args 要是空陣列(SetArgs 絕不 nil),得到 %v", evFirst(events, "start")["args"])
	}
}

func TestWebRunRejectsConcurrent409AndDeniedCallDoesNotHoldMutex(t *testing.T) {
	fs, _, _ := pullWorld(t)
	entered, release := blockingHook(t, fs)
	defer release()
	_, c := startWeb(t)

	done := make(chan []map[string]any, 1)
	go func() {
		_, ev, _ := c.run(map[string]any{"args": []string{"search", "x"}})
		done <- ev
	}()
	waitFor(t, "第一個 job 進 hook", entered)
	if code, _, msg := c.run(map[string]any{"args": []string{"search", "y"}}); code != http.StatusConflict || !strings.Contains(msg, "執行中") {
		t.Errorf("第二個命令要 409:%d %q", code, msg)
	}
	release()
	select {
	case ev := <-done:
		if evExit(t, ev)["code"] != float64(0) {
			t.Errorf("放行後第一個 job 正常結束:%v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("第一個 job 沒結束")
	}
	// 403(deny / 允許清單)發生在鎖前或鎖內 defer 之後都不能把鎖留著。
	if code, _, _ := c.run(map[string]any{"args": []string{"auth", "login", "apple", "--auto"}}); code != http.StatusForbidden {
		t.Fatalf("--auto 要 403:%d", code)
	}
	if code, _, _ := c.run(map[string]any{"args": []string{"debug", "apple-token"}}); code != http.StatusForbidden {
		t.Fatalf("debug 要 403:%d", code)
	}
	if code, ev, _ := c.run(map[string]any{"args": []string{}}); code != 200 || evExit(t, ev)["code"] != float64(0) {
		t.Errorf("403 之後鎖要是放開的:%d", code)
	}
}

func TestWebAllowlistTable(t *testing.T) {
	setCLITestConfig(t)
	s, c := startWeb(t)
	root := newRootCmd()
	for _, tc := range []struct {
		args  []string
		allow bool
	}{
		{[]string{"debug", "apple-token"}, false},
		{[]string{"debug", "lookup-isrc", "X"}, false},
		{[]string{"--provider", "spotify", "debug", "apple-token"}, false},
		{[]string{"debug"}, false},
		{[]string{"pl"}, true},
		{[]string{"auth"}, true},
		{[]string{"pl", "show", "x"}, true},
		{[]string{"help", "pl"}, true},
		{[]string{"completion", "zsh"}, true},
		{[]string{"search", "x", "--help"}, true},
		{[]string{"update"}, true},
		{[]string{"update", "--dev"}, true},
		{[]string{}, true},
		{[]string{"nope"}, true}, // 未知子命令 Find 到 root → 放行 → cobra 自己報錯
	} {
		if path, ok := s.allowed(root, tc.args); ok != tc.allow {
			t.Errorf("%v → %q allow=%v,要 %v", tc.args, path, ok, tc.allow)
		}
	}
	for _, tc := range []struct {
		args []string
		deny bool
	}{
		{[]string{"auth", "login", "apple", "--auto"}, true},
		{[]string{"auth", "login", "apple", "--auto=true"}, true},
		{[]string{"--web"}, true},
		{[]string{"auth", "login", "google", "--client-secret", "x"}, true},
		{[]string{"auth", "login", "google", "--client-secret=x"}, true},
		{[]string{"auth", "login", "apple", "--developer-token=eyJ"}, true},
		{[]string{"auth", "login", "apple", "--user-token", "x"}, true},
		{[]string{"auth", "login", "spotify", "--client-id", "x"}, false},
		{[]string{"search", "--autoplay"}, false}, // 只比整個 token,不比前綴
	} {
		if got := webDenied(tc.args) != ""; got != tc.deny {
			t.Errorf("deny %v = %v,要 %v", tc.args, got, tc.deny)
		}
	}
	// 走 HTTP:deny 在鎖前 403、允許清單 403 訊息點名路徑、bare capy 是 help 不是 TUI。
	if code, _, msg := c.run(map[string]any{"args": []string{"auth", "login", "apple", "--auto"}}); code != http.StatusForbidden || !strings.Contains(msg, "--auto") {
		t.Errorf("--auto:%d %q", code, msg)
	}
	if code, _, msg := c.run(map[string]any{"args": []string{"debug", "apple-token"}}); code != http.StatusForbidden || !strings.Contains(msg, "capy debug apple-token") {
		t.Errorf("debug:%d %q", code, msg)
	}
}

func TestWebNoArgsPrintsHelpNotTUI(t *testing.T) {
	setCLITestConfig(t)
	origTUI := runTUI
	runTUI = func(*cobra.Command) error { t.Error("web 不得開 TUI"); return nil }
	t.Cleanup(func() { runTUI = origTUI })
	_, c := startWeb(t)
	code, events, _ := c.run(map[string]any{"args": []string{}})
	if code != 200 || !strings.Contains(evText(events, "stdout"), "Usage:") {
		t.Errorf("bare capy → help:%d %q", code, evText(events, "stdout"))
	}
}

func TestWebNowWatchRefused(t *testing.T) {
	setCLITestConfig(t)
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	_, c := startWeb(t)
	_, events, _ := c.run(map[string]any{"args": []string{"now", "--watch"}})
	if ex := evExit(t, events); ex["code"] != float64(1) || !strings.Contains(ex["message"].(string), "capy now") {
		t.Errorf("now --watch 在 web 要指路單次 capy now:%v", ex)
	}
}

func TestWebResetsDefaultProviderPerRun(t *testing.T) {
	setCLITestConfig(t)
	_, c := startWeb(t)
	_, events, _ := c.run(map[string]any{"args": []string{"--help"}})
	if !strings.Contains(evText(events, "stdout"), `(default "spotify")`) {
		t.Fatalf("預設 spotify:%q", evText(events, "stdout"))
	}
	cfg, _ := config.Load()
	cfg.DefaultProvider = "apple"
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	_, events, _ = c.run(map[string]any{"args": []string{"--help"}})
	if !strings.Contains(evText(events, "stdout"), `(default "apple")`) {
		t.Errorf("每個 run 前 resetDefaultProvider,長駐行程要看到終端機改的 config:%q", evText(events, "stdout"))
	}
}

func TestWebCommandsListExcludesHiddenAndAutoFlag(t *testing.T) {
	setCLITestConfig(t)
	_, c := startWeb(t)
	resp := c.req(context.Background(), http.MethodGet, "/api/commands", nil, nil)
	defer resp.Body.Close()
	var d struct {
		Version         string
		Providers       []string
		DefaultProvider string `json:"default_provider"`
		Commands        []webCommand
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil || resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode, err)
	}
	if d.Version != version || len(d.Providers) != 3 || d.DefaultProvider != "spotify" {
		t.Errorf("version / providers / default_provider:%+v", d)
	}
	var login *webCommand
	for i := range d.Commands {
		if strings.HasPrefix(d.Commands[i].Path, "capy debug") {
			t.Errorf("Hidden 群組不列:%s", d.Commands[i].Path)
		}
		if d.Commands[i].Path == "capy auth login" {
			login = &d.Commands[i]
		}
	}
	if login == nil {
		t.Fatal("要有 capy auth login")
	}
	names := map[string]bool{}
	for _, f := range login.Flags {
		names[f.Name] = true
	}
	if names["auto"] || !names["client-id"] {
		t.Errorf("Hidden flag --auto 不列、--client-id 要列:%v", names)
	}
}

// ── 取消三路 ──

func TestWebExitReasonCancelled(t *testing.T) {
	fs, _, _ := pullWorld(t)
	entered, release := blockingHook(t, fs)
	defer release()
	s, c := startWeb(t)
	done := make(chan []map[string]any, 1)
	go func() {
		_, ev, _ := c.run(map[string]any{"args": []string{"search", "x"}})
		done <- ev
	}()
	waitFor(t, "job 進 hook", entered)
	job := s.current()
	if job == nil {
		t.Fatal("要有目前 job")
	}
	if st := c.status(http.MethodPost, "/api/jobs/"+job.id+"/cancel", nil, nil); st != http.StatusNoContent {
		t.Fatalf("cancel → 204,得到 %d", st)
	}
	select {
	case ev := <-done:
		if ex := evExit(t, ev); ex["reason"] != "cancelled" || ex["code"] != float64(1) {
			t.Errorf("取消:exit 1(context canceled)+ reason cancelled:%v", ex)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消後 job 沒結束")
	}
	release()
	if st := c.status(http.MethodPost, "/api/jobs/"+job.id+"/cancel", nil, nil); st != http.StatusNotFound {
		t.Errorf("已結束的 job cancel → 404,得到 %d", st)
	}
}

// pauseIgnoresCtx:Pause 不吃取消(同 Apple 的 osascript 走 exec.Command、沒有 ctx):卡到 release 才回 nil。
type pauseIgnoresCtx struct {
	*nowFake
	entered, release chan struct{}
}

func (f *pauseIgnoresCtx) Pause(context.Context) error {
	close(f.entered)
	<-f.release
	return nil
}

// TestWebExitReasonDoneWhenCommandFinishedDespiteCancel:中止落在不吃取消的那一段、命令其實做完了(exit 0):
// reason 要是 done——回 cancelled 會讓頁面說「已中止」、還把命令預填回命令列叫人重跑(review)。【fails-before-fix】
func TestWebExitReasonDoneWhenCommandFinishedDespiteCancel(t *testing.T) {
	f := &pauseIgnoresCtx{nowFake: newNowFake(), entered: make(chan struct{}), release: make(chan struct{})}
	s, c := startWeb(t)
	swapProviderWith(t, f)
	done := make(chan []map[string]any, 1)
	go func() {
		_, ev, _ := c.run(map[string]any{"args": []string{"pause"}})
		done <- ev
	}()
	waitFor(t, "pause 進 provider", f.entered)
	job := s.current()
	if job == nil {
		t.Fatal("要有目前 job")
	}
	if st := c.status(http.MethodPost, "/api/jobs/"+job.id+"/cancel", nil, nil); st != http.StatusNoContent {
		t.Fatalf("cancel → 204,得到 %d", st)
	}
	close(f.release)
	select {
	case ev := <-done:
		if ex := evExit(t, ev); ex["code"] != float64(0) || ex["reason"] != "done" {
			t.Errorf("命令做完了(exit 0)就是 done,不是 cancelled:%v", ex)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job 沒結束")
	}
}

// TestWebShutdownCancelsJobAndReleasesPullLock:【fails-before-fix】沒有 context.AfterFunc(伺服器 ctx → job ctx),
// SIGINT 砍不掉卡住的 job,pull.lock 也放不掉。
func TestWebShutdownCancelsJobAndReleasesPullLock(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	entered, release := blockingHook(t, fs)
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := newWebServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.fallbackStderr = &lockedBuffer{}
	t.Cleanup(installWebSeams(s))
	srv := httptest.NewUnstartedServer(nil)
	s.hostport = srv.Listener.Addr().String()
	srv.Config.Handler = s.handler()
	srv.Start()
	t.Cleanup(srv.Close)
	c := &webClient{t: t, srv: srv, token: s.token}

	done := make(chan []map[string]any, 1)
	go func() {
		_, ev, _ := c.run(map[string]any{"args": []string{"pl", "pull", "通勤"}}) // 握著 pull.lock 才打平台
		done <- ev
	}()
	waitFor(t, "pull 進 hook", entered)
	cancel() // = SIGINT / SIGTERM
	select {
	case ev := <-done:
		if ex := evExit(t, ev); ex["reason"] != "shutdown" {
			t.Errorf("伺服器關閉砍 job:reason shutdown:%v", ex)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("伺服器 ctx 取消後 job 沒結束(AfterFunc 沒接?)")
	}
	lctx, lcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer lcancel()
	unlock, err := auth.LockFile(lctx, "pull.lock", "測試")
	if err != nil {
		t.Fatalf("job 結束後 pull.lock 要放掉:%v", err)
	}
	unlock()
	release()
}

// TestWebDisconnectCancelsJobAndPrompt:【fails-before-fix】handler 沒把 body 讀到 EOF,net/http 不會起 background read,
// 關分頁後 r.Context() 不取消,job 卡到天荒地老、鎖不放。T3a 以 hook 卡住 job;T3b 改成真的卡在 prompt。
func TestWebDisconnectCancelsJobAndPrompt(t *testing.T) {
	fs, _, _ := pullWorld(t)
	entered, release := blockingHook(t, fs)
	defer release()
	_, c := startWeb(t)
	reqCtx, disconnect := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		resp := c.req(reqCtx, http.MethodPost, "/api/run", map[string]any{"args": []string{"search", "x"}}, nil)
		_, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		errc <- err
	}()
	waitFor(t, "job 進 hook", entered)
	disconnect() // = 關分頁
	<-errc
	// 序列槽要在幾秒內放開(job 被 r.Context 取消),而不是等 hook。
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, _, _ := c.run(map[string]any{"args": []string{"--help"}})
		if code == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("關分頁後 job 沒被取消,序列槽一直被握著")
		}
		time.Sleep(50 * time.Millisecond)
	}
	release()
}

// ── stderr 全域 ──

// TestWebGlobalStderrRoutedToJobUnderRace:job 執行中,別的 goroutine(這裡是假平台的 handler,模擬面板輪詢 /
// 429 退避)寫 provider.BackoffStderr → 落進這個 job 的 stderr 事件;-race 下 sseWriter.mu 守同一個 ResponseWriter。
func TestWebGlobalStderrRoutedToJobUnderRace(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.setHook(func() { io.WriteString(provider.BackoffStderr, "rate limited,等待 1s 後重試…\n") })
	_, c := startWeb(t)
	_, events, _ := c.run(map[string]any{"args": []string{"search", "x"}})
	if !strings.Contains(evText(events, "stderr"), "rate limited") {
		t.Errorf("全域 stderr 要進當下 job 的 stderr 事件:%v", events)
	}
	if evExit(t, events)["code"] != float64(0) {
		t.Errorf("exit:%v", evExit(t, events))
	}
}

// TestWebGlobalStderrAfterJobEndsFallsBackToOsStderr:【fails-before-fix】沒有 closed 旗標,job 結束後的退避提示
// 會寫到已結束的 ResponseWriter(net/http 的 write after handler)。
func TestWebGlobalStderrAfterJobEndsFallsBackToOsStderr(t *testing.T) {
	fs, _, _ := pullWorld(t)
	entered, release := blockingHook(t, fs)
	defer release()
	s, c := startWeb(t)
	fb := s.fallbackStderr.(*lockedBuffer)
	done := make(chan []map[string]any, 1)
	go func() {
		_, ev, _ := c.run(map[string]any{"args": []string{"search", "x"}})
		done <- ev
	}()
	waitFor(t, "job 進 hook", entered)
	job := s.current()
	release()
	waitFor(t, "job 結束", func() <-chan struct{} { ch := make(chan struct{}); go func() { <-done; close(ch) }(); return ch }())
	if err := job.sse.event(map[string]any{"type": "stderr", "text": "遲到的退避提示"}); err == nil {
		t.Error("job 結束後串流要標 closed,遲到的寫入要回錯而不是寫進已結束的 ResponseWriter")
	}
	io.WriteString(provider.BackoffStderr, "沒有 job 時\n")
	if !strings.Contains(fb.String(), "沒有 job 時") {
		t.Errorf("沒有 job → os.Stderr(測試換成 buffer):%q", fb.String())
	}
	// 串流已關但 s.cur 還指著(關與清 cur 之間的窗):也退回。
	rec := httptest.NewRecorder()
	sse := &sseWriter{w: rec, fl: rec}
	sse.close()
	s.setCur(&webJob{id: "x", sse: sse})
	io.WriteString(auth.LoginStderr, "串流關了\n")
	s.setCur(nil)
	if !strings.Contains(fb.String(), "串流關了") || rec.Body.Len() != 0 {
		t.Errorf("closed 之後不得再寫 ResponseWriter:fallback=%q rec=%q", fb.String(), rec.Body.String())
	}
}

// TestWebLockNoticeNamesPanelPoll:token 鎖等的可能是同一行程的面板 / ISRC 頁在換發 token,整句改寫;
// pull.lock 等的真的是另一個 capy 行程(終端機 / cron),原文不能被改掉——只有 Ctrl-C 換成頁面上有的鈕。
func TestWebLockNoticeNamesPanelPoll(t *testing.T) {
	var b bytes.Buffer
	w := &webLockStderr{&b}
	io.WriteString(w, "等待另一個 capy 釋放 spotify.token.lock(對方正在換發);要放棄按 Ctrl-C。\n")
	if got := b.String(); !strings.Contains(got, "播放面板 / ISRC 頁") || strings.Contains(got, "Ctrl-C") || !strings.Contains(got, "spotify.token.lock") {
		t.Errorf("token 鎖:要點名面板 / ISRC 頁、不講 Ctrl-C:%q", got)
	}
	b.Reset()
	io.WriteString(w, "等待另一個 capy 釋放 pull.lock(對方正在同步播放清單);要放棄按 Ctrl-C。\n")
	got := b.String()
	if !strings.Contains(got, "等待另一個 capy 釋放 pull.lock") || strings.Contains(got, "播放面板") {
		t.Errorf("pull.lock:等的真的是另一個行程,原文不可被改寫:%q", got)
	}
	if strings.Contains(got, "Ctrl-C") || !strings.Contains(got, "要放棄按「中止」") {
		t.Errorf("pull.lock:網頁沒有 Ctrl-C,只換這半句:%q", got)
	}
}

// ── update → stale ──

// TestWebUpdateExitZeroMakesServerStale:update 真的換了 binary(replaceExecutable 設 executableReplaced)的那個 job
// 結束後提示重啟、之後 /api/run 一律 503 直到重啟;【fails-before-fix】舊判準「exit 0 + capy update」會讓「已是最新」
// 的 no-op 也把主控台永久 503(review #59 第 1 點)——前半段釘住 no-op 不 stale。
func TestWebUpdateExitZeroMakesServerStale(t *testing.T) {
	setCLITestConfig(t)
	stubGitHub(t, http.StatusOK, headJSON)
	calls := stubInstall(t)
	stubExecutable(t)
	stubVersion(t, "2026.09.07-0123456") // = main 的 sha 前綴 → 已是最新
	s, c := startWeb(t)
	_, ev, _ := c.run(map[string]any{"args": []string{"update", "--dev"}})
	if ex := evExit(t, ev); ex["code"] != float64(0) || !strings.Contains(evText(ev, "stdout"), "已是最新") || len(*calls) != 0 {
		t.Fatalf("no-op update:%v %q", ex, evText(ev, "stdout"))
	}
	if s.stale.Load() {
		t.Fatal("沒換 binary 不得 stale")
	}
	if code, _, _ := c.run(map[string]any{"args": []string{"--help"}}); code != 200 {
		t.Errorf("已是最新之後還能跑:%d", code)
	}
	// 真的換了 binary:提示重啟、stale、之後 503。
	stubVersion(t, "2026.09.04-77b72b4")
	_, ev, _ = c.run(map[string]any{"args": []string{"update", "--dev"}})
	if ex := evExit(t, ev); ex["code"] != float64(0) || !strings.Contains(evText(ev, "stdout"), "已更新") || !strings.Contains(evText(ev, "stderr"), "請重啟") || !s.stale.Load() {
		t.Fatalf("換了 binary 的 update:%v out=%q err=%q", ex, evText(ev, "stdout"), evText(ev, "stderr"))
	}
	if code, _, _ := c.run(map[string]any{"args": []string{"--help"}}); code != http.StatusServiceUnavailable {
		t.Errorf("stale 後 503:%d", code)
	}
}

// ── 安全 ──

func TestWebTokenRequired401(t *testing.T) {
	_, c := startWeb(t)
	if st := c.status(http.MethodGet, "/api/commands", nil, map[string]string{"X-Capy-Token": ""}); st != http.StatusUnauthorized {
		t.Errorf("沒 token → 401,得到 %d", st)
	}
	if st := c.status(http.MethodGet, "/api/commands", nil, map[string]string{"X-Capy-Token": "wrong"}); st != http.StatusUnauthorized {
		t.Errorf("錯 token → 401,得到 %d", st)
	}
	if st := c.status(http.MethodPost, "/api/run", map[string]any{"args": []string{}}, map[string]string{"X-Capy-Token": "wrong"}); st != http.StatusUnauthorized {
		t.Errorf("run 錯 token → 401,得到 %d", st)
	}
	if st := c.status(http.MethodGet, "/api/commands", nil, nil); st != 200 {
		t.Errorf("對的 token → 200,得到 %d", st)
	}
	if st := c.status(http.MethodGet, "/", nil, map[string]string{"X-Capy-Token": ""}); st != 200 {
		t.Errorf("靜態殼子不需要 token,得到 %d", st)
	}
	if st := c.status(http.MethodGet, "/api/nope", nil, nil); st != http.StatusNotFound {
		t.Errorf("未知 API → 404,得到 %d", st)
	}
}

func TestWebHostAndOriginChecks(t *testing.T) {
	s, c := startWeb(t)
	_, port, _ := strings.Cut(s.hostport, ":")
	for _, tc := range []struct {
		hdr  map[string]string
		want int
	}{
		{map[string]string{"Host": "evil:1"}, http.StatusMisdirectedRequest},
		{map[string]string{"Host": "localhost:" + port}, http.StatusMisdirectedRequest},
		{map[string]string{"Origin": "http://evil"}, http.StatusForbidden},
		{map[string]string{"Origin": "http://127.0.0.1:1"}, http.StatusForbidden},
		{map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://" + s.hostport}, 200},
		{map[string]string{"Sec-Fetch-Site": "none"}, 200},
	} {
		if st := c.status(http.MethodGet, "/api/commands", nil, tc.hdr); st != tc.want {
			t.Errorf("%v → %d,要 %d", tc.hdr, st, tc.want)
		}
		if st := c.status(http.MethodGet, "/", nil, tc.hdr); st != tc.want {
			t.Errorf("靜態頁 %v → %d,要 %d", tc.hdr, st, tc.want)
		}
	}
}

func TestWebResponseHeaders(t *testing.T) {
	_, c := startWeb(t)
	resp := c.req(context.Background(), http.MethodGet, "/api/commands", nil, nil)
	resp.Body.Close()
	h := resp.Header
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "no-referrer" || h.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("API 標頭:%v", h)
	}
	resp = c.req(context.Background(), http.MethodGet, "/", nil, nil)
	resp.Body.Close()
	h = resp.Header
	if !strings.HasPrefix(h.Get("Content-Security-Policy"), "default-src 'none'") || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-cache" || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		t.Errorf("靜態頁標頭:%v", h)
	}
	for _, p := range []string{"/css/app.css", "/js/app.js"} {
		resp = c.req(context.Background(), http.MethodGet, p, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("%s:%d %v", p, resp.StatusCode, resp.Header)
		}
	}
}

// TestWebStaticHasCSPAndNoInline:CSP 禁 inline,所以前端一個 inline script / style / on* 都不能有。
func TestWebStaticHasCSPAndNoInline(t *testing.T) {
	scriptTag := regexp.MustCompile(`(?i)<script[^>]*>`)
	onAttr := regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	var checked int
	err := walkEmbedded(t, "webui", func(name string, b []byte) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		checked++
		s := string(b)
		for _, tag := range scriptTag.FindAllString(s, -1) {
			if !strings.Contains(tag, "src=") {
				t.Errorf("%s:inline script:%s", name, tag)
			}
		}
		if strings.Contains(strings.ToLower(s), "<style") || strings.Contains(strings.ToLower(s), " style=") || onAttr.MatchString(s) {
			t.Errorf("%s:inline style / on* 事件屬性違反 CSP", name)
		}
		if !strings.Contains(s, `<script type="module" src="js/app.js">`) {
			t.Errorf("%s:入口 script 要是 module、相對路徑(不用 / 開頭:Host 比對的是 127.0.0.1)", name)
		}
	})
	if err != nil || checked == 0 {
		t.Fatal(err, checked)
	}
	if !strings.Contains(webCSP, "script-src 'self'") || strings.Contains(webCSP, "unsafe-inline") || !strings.Contains(webCSP, "https://p.scdn.co") {
		t.Errorf("CSP:%s", webCSP)
	}
}

// TestWebCapybaraMatchesTUI:網頁的空白態與終端機用同一隻水豚。JS 那份是手抄的常數(沒有共用執行期),
// 所以在這裡逐行比對——差一個空格,兩邊的招牌就長得不一樣,而這是使用者第一眼看到的東西。
func TestWebCapybaraMatchesTUI(t *testing.T) {
	b, err := webUI.ReadFile("webui/js/console.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "export const CAPYBARA = [")
	if start < 0 {
		t.Fatal("console.js 要有 CAPYBARA 常數")
	}
	end := strings.Index(src[start:], "];")
	if end < 0 {
		t.Fatal("CAPYBARA 常數沒有結尾")
	}
	var got []string
	for _, ln := range strings.Split(src[start:start+end], "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "'") {
			continue
		}
		ln = strings.TrimSuffix(strings.TrimSuffix(ln, ","), "'")
		got = append(got, strings.ReplaceAll(strings.TrimPrefix(ln, "'"), `\\`, `\`))
	}
	want := capybaraStill()
	if len(got) != len(want) {
		t.Fatalf("行數 %d,終端機是 %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 行不一樣:\n web %q\n tui %q", i+1, got[i], want[i])
		}
	}
}

// TestWebAccountPageKeysOnAuthStatusWording:帳號頁的正確性綁在 auth status 的中文字面上,而純文字契約的測試
// 只保證 CLI 自己不變、不保證網頁跟得上(review #62)。這裡兩邊一起釘:account.js 認的每個字面都要真的出現在
// auth status 的輸出裡。CLI 那邊改一個字,這個測試就會紅並指向 account.js,而不是讓網頁靜默判錯。
func TestWebAccountPageKeysOnAuthStatusWording(t *testing.T) {
	b, err := webUI.ReadFile("webui/js/pages/account.js")
	if err != nil {
		t.Fatal(err)
	}
	account := string(b)

	// (1) Apple 有效:developer token 有效至 … + user token 存在。這正是舊版判成「未登入」的那一種狀態
	// ——「keychain 存在」只出現在 spotify 與 google 段落,apple 段一個字都不中。
	setupAppleTokens(t)
	out, err := runCLI(t, "auth", "status")
	if err != nil {
		t.Fatalf("auth status: %v", err)
	}
	for _, lit := range []string{"developer token: 有效至", "user token: 存在"} {
		if !strings.Contains(out, lit) {
			t.Errorf("auth status 的 apple 段不再印 %q,帳號頁會把有效的 Apple 判成未登入", lit)
		}
		if !strings.Contains(account, lit) {
			t.Errorf("account.js 沒有認 %q", lit)
		}
	}

	// (2) 三家都未登入:apple 段要印「不存在」,而且不可以出現任何一個「已登入」的字面。
	clearAppleTokens(t)
	out, err = runCLI(t, "auth", "status")
	if err != nil {
		t.Fatalf("auth status: %v", err)
	}
	if !strings.Contains(out, "developer token: 不存在") {
		t.Errorf("未登入時 apple 段要印「不存在」:%q", out)
	}
	for _, lit := range []string{"developer token: 有效至", "user token: 存在", "refresh token: keychain 存在"} {
		if strings.Contains(out, lit) {
			t.Errorf("什麼都沒設定時不該出現 %q:%q", lit, out)
		}
	}

	// (3) 另外兩家與錯誤態的字面也要對得上(spotify / google 各自不同,不能用同一個 regex 打天下)。
	for _, lit := range []string{"refresh token: keychain 存在", "token: keychain 存在", "讀取 keychain 失敗"} {
		if !strings.Contains(account, lit) {
			t.Errorf("account.js 沒有認 %q", lit)
		}
	}
	src, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatal(err)
	}
	gsrc, err := os.ReadFile("auth_google.go")
	if err != nil {
		t.Fatal(err)
	}
	both := string(src) + string(gsrc)
	for _, lit := range []string{"refresh token: keychain 存在", "token: keychain 存在", "讀取 keychain 失敗"} {
		if !strings.Contains(both, lit) {
			t.Errorf("auth status 不再印 %q,帳號頁的判斷會靜默失效", lit)
		}
	}
}

func walkEmbedded(t *testing.T, dir string, fn func(name string, b []byte)) error {
	t.Helper()
	entries, err := webUI.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := dir + "/" + e.Name()
		if e.IsDir() {
			if err := walkEmbedded(t, name, fn); err != nil {
				return err
			}
			continue
		}
		b, err := webUI.ReadFile(name)
		if err != nil {
			return err
		}
		fn(name, b)
	}
	return nil
}

// ── root 的 --web / --port ──

func TestWebPortFlagRefuses8888AndRequiresWeb(t *testing.T) {
	setCLITestConfig(t)
	if _, err := runCLI(t, "--port", "1"); err == nil || !strings.Contains(err.Error(), "--web") {
		t.Errorf("--port 沒配 --web 要報錯:%v", err)
	}
	for _, tc := range []struct{ port, why string }{
		{"8888", "Spotify"}, // 授權回呼固定 port
		{"80", "Host"},      // 瀏覽器會從 Host 拿掉預設埠號 → 逐字比對必不符 → 每個請求 421,頁面打不開
		{"443", "Host"},     // 同上;這兩個不能只靠 listen 失敗,沒有權限的錯誤訊息裡也有數字
	} {
		_, err := runCLI(t, "--web", "--port", tc.port)
		if err == nil || !strings.Contains(err.Error(), tc.port) || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("--port %s 要在 listen 前拒絕並說明理由(%s):%v", tc.port, tc.why, err)
		}
		// --help 要跟實際擋的一致(review #63 第 3 點):同一張表同時驅動拒絕與說明。
		if u := newRootCmd().Flags().Lookup("port").Usage; !strings.Contains(u, tc.port) {
			t.Errorf("--port 的說明要列出不能用的 %s:%q", tc.port, u)
		}
	}
}

// TestWebStaticFrontendContracts:前端沒有自動化測試(計畫 §5),但幾條「壞掉不會有人發現」的契約可以在 embed 的
// 位元組上釘住:IME 守衛(拿掉 <form> 後回歸過一次)、log 面板不是 live region、時長對齊終端機、secret 回聲遮罩、
// 發光只准在 glow budget 註解塊裡、長 token 不撐破版面。
func TestWebStaticFrontendContracts(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		b, err := webUI.ReadFile("webui/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	index, app, console, table, css := read("index.html"), read("js/app.js"), read("js/console.js"), read("js/table.js"), read("css/app.css")
	// between:console.js 裡從 start 到其後第一個 end 的那一段(用來看某個方法的本體)。
	between := func(start, end string) string {
		t.Helper()
		_, rest, ok := strings.Cut(console, start)
		body, _, ok2 := strings.Cut(rest, end)
		if !ok || !ok2 {
			t.Fatalf("console.js 找不到 %q … %q", start, end)
		}
		return body
	}

	// 組字中的 Enter 是確認候選字:注音使用者按第一個 Enter 不該把半截命令送出去。
	if !strings.Contains(app, "ev.isComposing") || !strings.Contains(app, "ev.keyCode === 229") {
		t.Error("命令列的 Enter 要擋 IME 組字(isComposing + Safari 的 keyCode 229)")
	}
	// 提示橋的輸入框(清單名字、搜尋字串)最可能打中文:Enter 與 Esc 都要擋組字(review #60)。
	if !strings.Contains(console, "k.isComposing") || !strings.Contains(console, "k.keyCode === 229") {
		t.Error("提示輸入框的 Enter / Esc 也要擋 IME 組字")
	}
	if !strings.Contains(console, "preventScroll: true") {
		t.Error("提示的 focus 不可覆蓋 stick() 的捲動判斷")
	}
	// 設計規格 §11:log 面板 role=region(不是 live region,高吞吐會把螢幕閱讀器淹掉);狀態行才 role=status。
	if !strings.Contains(index, `role="region"`) || strings.Contains(index, "aria-live") {
		t.Error("log 面板要 role=region、不可是 live region")
	}
	if strings.Count(console, `setAttribute('role', 'status')`) < 2 {
		t.Error("命令回聲與退出碼兩個狀態行要 role=status")
	}
	// 時長對齊 ui.FormatDuration 的整數除法(四捨五入會讓同一首歌在網頁與終端機差一秒)。
	if !strings.Contains(table, "Math.floor(ms / 1000)") || strings.Contains(table, "Math.round(ms") {
		t.Error("時長要用整數除法,對齊 ui.FormatDuration")
	}
	// 設計規格 §8:三個 secret flag 的值在回聲裡遮成 ***(伺服器 403 之外的第二層,值不留在 DOM)。
	for _, f := range []string{"--developer-token", "--user-token", "--client-secret"} {
		if !strings.Contains(console, f) {
			t.Errorf("回聲遮罩要涵蓋 %s", f)
		}
	}
	// 視覺規格 v2 §2:v1 的「glow budget 註解塊、四處可數」退役,換成更簡單的規則——發光只有兩處:
	// 鍵盤焦點環與進行中的 dock 頂線。逐條宣告檢查(不是「第一次出現的位置」,那個擋不住註解塊之後再加的)。
	for _, rule := range regexp.MustCompile(`(?m)^([^{}\n]+)\{[^}]*var\(--glow[^)]*\)`).FindAllStringSubmatch(css, -1) {
		if sel := strings.TrimSpace(rule[1]); sel != ":focus-visible" && sel != ".dock::after" {
			t.Errorf("發光只准出現在 :focus-visible 與 .dock::after(視覺規格 v2 §2),多了:%q", sel)
		}
	}
	if strings.Contains(css, "box-shadow") && regexp.MustCompile(`transition:[^;]*box-shadow`).MatchString(css) {
		t.Error("box-shadow 不進 transition(glow 的進出只動偽元素的 opacity)")
	}
	// 授權 URL 約 330 字且沒有可斷點:沒有 overflow-wrap 會讓整個主窗格橫向捲。
	if !strings.Contains(css, "overflow-wrap: anywhere") {
		t.Error("主控台輸出要 overflow-wrap: anywhere")
	}
	// 八頁(決策 45):殼層、rail 的連結、app.js 的 PAGES 三處同一組、同一個順序(鍵位 1–8 靠這個順序);
	// 預設落在搬家頁;主控台 / ISRC / 診斷收在「進階」但一個不少。
	pages := []string{"move", "playlists", "sync", "search", "account", "console", "isrc", "doctor"}
	last := -1
	for _, page := range pages {
		if !strings.Contains(index, `id="page-`+page+`"`) {
			t.Errorf("殼層缺 %s 頁", page)
		}
		i := strings.Index(index, `href="#/`+page+`" data-page="`+page+`"`)
		if i < 0 || i < last {
			t.Errorf("rail 要有 %s 的連結,而且順序與 PAGES 相同", page)
		}
		last = i
	}
	if !strings.Contains(app, "const PAGES = ['"+strings.Join(pages, "', '")+"'];") {
		t.Error("app.js 的 PAGES 要與 rail 同一組、同一個順序")
	}
	if !strings.Contains(app, ": 'move';") || !strings.Contains(app, "location.pathname + '#/move'") {
		t.Error("預設路由與 token 引導後的落點都要是搬家頁")
	}
	if !strings.Contains(index, "<dt>1 – 8</dt>") {
		t.Error("? 鍵位表要寫 1 – 8")
	}
	// label(決策 45):執行狀態列顯示頁面給的白話;沒給 label 的 fallback 仍然要過 maskSecrets(review #66 第 4 點:
	// 重構 label 時最容易掉的就是這一行,掉了 secret 就上了每一頁都看得到的狀態列)。
	if !strings.Contains(console, "const raw = maskSecrets(line || '(help)');") || !strings.Contains(console, "const shown = label || raw;") {
		t.Error("run() 的顯示文字:label 優先,fallback 是 maskSecrets(line)")
	}
	// 命令列只在主控台頁(決策 45):CSS 看 body[data-page],showPage 負責設它;/ 先切到主控台再聚焦。
	if !strings.Contains(css, `body:not([data-page="console"]) .dock__cmd { display: none; }`) || !strings.Contains(app, "document.body.dataset.page = name") {
		t.Error("命令列要只在主控台頁出現")
	}
	if !strings.Contains(app, "const open = ADVANCED.includes(name);") || !strings.Contains(app, "more.open = open;") ||
		!strings.Contains(app, "if (!open && more.open && more.contains(document.activeElement))") {
		t.Error("「進階」跟著路由開合:進去要打開(review #66 第 3 點),收起來之前把還在裡面的焦點搬到新的 active 項目(review #67)")
	}
	// 命令結束之後那一句也要說人話(review #67 第二輪):只看變更 + 有變更(exit 2)不可以貼出「加 --yes」;搜尋沒命中是
	// exit 0 + 空表,要在 onTable 看列數。
	if s := read("js/pages/sync.js"); !strings.Contains(s, "if (code === 2 && wasDry)") || !strings.Contains(s, "const wasDry = dry.checked;") {
		t.Error("同步頁:只看變更的 exit 2 要說這一頁上的下一步,不是 CLI 的「加 --yes」")
	}
	if !strings.Contains(read("js/pages/search.js"), "onTable: (header, rows) => out.replaceChildren(rows.length") {
		t.Error("搜尋頁:沒有命中(空表)要說一句話")
	}
	// 「去除重複」的說明要留著警告那一半:--provider 沒選到的平台這次不會檢查(review #67;會移除曲目的路徑不可以只說讓人安心的半句)。
	if !strings.Contains(read("js/pages/sync.js"), "沒選到的平台這次不會檢查") {
		t.Error("同步頁「去除重複」的說明要講明沒選到的平台這次不會檢查")
	}
	if !strings.Contains(app, "case '/': ev.preventDefault(); location.hash = '#/console'; input.focus(); break;") {
		t.Error("/ 要先切到主控台再聚焦命令列(別頁的命令列是藏起來的)")
	}
	// 每頁的第一眼不出現命令字串(決策 45):頁標題旁的 CLI 等價命令與「空白態是一條命令」都退役。
	common := read("js/pages/common.js")
	if strings.Contains(common, "page__cli") || strings.Contains(common, "empty__cmd") {
		t.Error("pageHead / emptyState 不該再把命令字串放到頁面上")
	}
	// 搬家頁:頁面絕不代加 --yes / --force(決策 46);Apple 當目的地要留在選單裡、不可選、說原因。
	move := read("js/pages/move.js")
	if strings.Contains(move, "--yes") && !strings.Contains(move, "絕不代加 --yes") || strings.Contains(move, "' --yes") || strings.Contains(move, "' --force") {
		t.Error("搬家頁組出來的命令不可以帶 --yes / --force")
	}
	if !strings.Contains(move, "disabled: true") || !strings.Contains(move, "目前只能當來源") {
		t.Error("Apple Music 當目的地要是不可選並說明原因,不是直接消失")
	}
	if strings.Contains(index, "is-disabled") {
		t.Error("rail 不該還有停用的佔位項")
	}
	if !strings.Contains(index, `<dialog id="keys"`) {
		t.Error("? 的鍵位表要是原生 dialog")
	}
	// showIdle 會被叫兩次(route 一次、/api/commands 回來再一次)。第二次重畫會把 power-on 那一幀洗掉,
	// 而簽名時刻一個 session 只有一次——所以它必須是冪等的:已經畫過就只更新招牌。
	// 只看「建新節點之前,先找過 .capy 並 return」,變數怎麼命名、怎麼排版都不管(review #62 第 12 點)。
	if !regexp.MustCompile(`querySelector\('\.capy'\)[\s\S]*return`).MatchString(between("showIdle(", "createElement")) {
		t.Error("showIdle 要冪等:畫新水豚之前先找已經畫好的那隻並 return,否則 power-on 會被第二次呼叫洗掉")
	}
	// 面板:stale 是「伺服器忙」不是「伺服器不在」——用連續次數會把「慢但一直有新資料」判成失聯,而且回不來。
	player := read("js/player.js")
	if strings.Contains(player, "stales") {
		t.Error("失聯判斷不可以用連續 stale 次數,要看上一份快照多久沒更新")
	}
	if !strings.Contains(player, "STALE_DEAD_MS") || !strings.Contains(player, "nowConnected") {
		t.Error("面板要有自己的失聯門檻與連線旗標(body[data-connected] 是 console 在用的)")
	}
	// ISRC 頁:兩次查詢重疊時,舊的那次不可以把結果接在新的後面。
	isrcjs := read("js/pages/isrc.js")
	if !strings.Contains(isrcjs, "mine !== seq") || !strings.Contains(isrcjs, "AbortController") {
		t.Error("重疊的 ISRC 查詢要有序號守衛並取消舊 fetch")
	}
	// hash 路由要有結尾錨點,否則 #/isrcfoo 也會被判成 ISRC 頁。
	if !strings.Contains(app, "]+))?$/") {
		t.Error("hash 路由的正規式要有結尾錨點")
	}
	// 一次一個:進行中再叫 run() 要在碰任何狀態之前就地擋下。以前照送、吃 409,被擋那一次的收尾會把進行中
	// 那次的 job / hooks 清掉(中止、提示回答、頁面結果全失效;連點兩下就撞到)。
	run := between("async run(", "  idle(fn) {")
	guard, mutate := strings.Index(run, "if (this.running) {"), strings.Index(run, "this.running = true")
	if guard < 0 || mutate < 0 || guard > mutate || !strings.Contains(run[guard:mutate], "return busy;") {
		t.Error("run() 開頭要先擋掉進行中的第二次呼叫(在設 running / job / hooks 之前就 return)")
	}
	// 被擋、409 / 401 / 503 也要通知發起的頁面,否則那一頁永遠不會收尾。
	if !strings.Contains(run, "[-1, '另一個命令執行中', 'busy']") || !strings.Contains(run, "hooks.onExit?.(...busy)") || !strings.Contains(run, "this.ex = [-1, msg, 'refused']") {
		t.Error("被擋與被伺服器拒絕都要以 -1 通知頁面的 onExit,否則那一頁會永久空白")
	}
	// onExit 要等串流收尾(伺服器已放開序列槽、running 已歸零)才叫:帳號頁在 onExit 裡接著跑 auth status,
	// 在 exit 事件當下叫會被上面的閘擋掉或撞 409(review #62 第 2 點)。
	if fin, call := strings.Index(run, "} finally {"), strings.Index(run, "hooks.onExit?.(...ex)"); fin < 0 || call < fin {
		t.Error("onExit 要在 run() 的 finally 之後才叫")
	}
	if strings.Contains(between("event(ev, b) {", "prompt(ev, b) {"), "hooks.onExit?.(") {
		t.Error("exit 事件當下不可以叫 onExit(那時伺服器還握著序列槽)")
	}
	// 執行狀態列:run() 一開始就亮、finally 才熄(不是 exit 事件)——頁面按鈕發起的命令也看得到、按得到中止。
	if !strings.Contains(run[mutate:], "this.busyOn(") || !strings.Contains(run[strings.Index(run, "} finally {"):], "this.busyOff()") {
		t.Error("run() 要在開頭 busyOn、在 finally busyOff")
	}
	if !strings.Contains(console, "document.body.dataset.busy = ''") || !strings.Contains(console, "delete document.body.dataset.busy") {
		t.Error("忙碌狀態要切 body[data-busy](CSS 靠它畫頂線、調暗按鈕)")
	}
	// 中止:start 還沒到(不知道 job id)就按了,要記著、start 一到就送;已答應寫入的要按第二次。
	if !strings.Contains(console, "if (this.stopping) this.cancel()") {
		t.Error("start 事件到達時,若已經按過中止要立刻送 cancel")
	}
	if !strings.Contains(run, "if (isCancelled(ex)) ex = [ex[0], '已中止', ex[2]]") {
		t.Error("中止的命令交給頁面的訊息要是「已中止」,不是 context canceled 那串內部錯誤")
	}
	// 中止中的按鈕不可以用 disabled:disabled 會把焦點丟到 body,鍵盤使用者失去位置、收尾也交不回命令列。
	if strings.Contains(console, "stopBtn.disabled") || !strings.Contains(console, "document.activeElement === this.stopBtn") {
		t.Error("中止鈕用 aria-disabled 擋重複按,收尾時把焦點交回命令列")
	}
	if !strings.Contains(console, "if (b.querySelector('table')) this.wrote = true;") {
		t.Error("區塊有變更表又按了肯定,要記下「已答應寫入」(兩段式中止靠它)")
	}
	if !strings.Contains(between("async stop() {", "async cancel() {"), "this.wrote && !this.armed") {
		t.Error("已答應寫入之後的中止要按第二次確認(計畫 Q24 的半套狀態)")
	}
	// 中止鈕與忙碌 UI 歸 Console 管,不歸 submit():頁面按鈕發起的命令以前根本看不到中止鈕。
	if strings.Contains(app, "cancelBtn") || strings.Contains(app, "input.disabled = true") {
		t.Error("submit() 不可以再自己管取消鈕與停用輸入框(設計規格:執行中命令列仍可打字)")
	}
	if !strings.Contains(index, `id="busy"`) || !strings.Contains(index, `id="cancel">中止</button>`) || strings.Contains(index, `id="cancel" hidden`) {
		t.Error("dock 要有執行狀態列,中止鈕在列裡、標籤是「中止」")
	}
	if !strings.Contains(index, `id="busy-time" aria-hidden="true"`) || !strings.Contains(index, `id="busy-sr" role="status"`) {
		t.Error("每秒跳的計時不能被播報;開始 / 結束由另一個 role=status 說一次")
	}
	// Ctrl-C = 中止,但沒有選取文字時才攔(Windows 的 Ctrl-C 是複製);? 鍵位表要列出來。
	if !strings.Contains(app, "con.stop()") || !strings.Contains(app, "input.selectionStart === input.selectionEnd") || !strings.Contains(index, "<dt>Ctrl-C</dt>") {
		t.Error("命令列的 Ctrl-C 要中止(有選取文字時放行複製),並列進鍵位表")
	}
	// 頁面按鈕:執行中點了不呼叫 fn(頁面不先清掉自己的內容),點下去真的開跑的那一顆掛 data-pending。
	// 比對程式碼本身,不是事件名(事件名也出現在註解裡,拿掉閘測試照樣會過;review)。
	if !strings.Contains(common, "b.dataset.run = ''") || !strings.Contains(common, "b.dataset.pending = ''") ||
		!strings.Contains(common, "if (slotTaken()) { document.dispatchEvent(new Event('capy:busy')); return; }") ||
		!strings.Contains(app, "document.addEventListener('capy:busy'") {
		t.Error("btn() 要標 data-run、執行中擋下點擊並說明(app.js 要接 capy:busy)、標出正在跑的那一顆")
	}
	// 搜尋框的 Enter 走按鈕那條路,執行中才會被同一道閘擋下(不先清掉結果)。
	if !strings.Contains(read("js/pages/search.js"), "goBtn.click()") {
		t.Error("搜尋框 Enter 要走按鈕(btn 的閘)")
	}
	// 頁面第一次進來的自動讀取:有命令在跑就等它結束,不要撞上它、畫成「未登入」/「沒有清單」。
	for _, name := range []string{"js/pages/account.js", "js/pages/playlists.js"} {
		// idle(fn) 而不是 await idle() 再 run():兩頁同時等時,後者兩頁都看到空檔、第二頁撞閘(review;行為見 TestWebConsoleBehaviour)。
		if !strings.Contains(read(name), "con.idle(") || strings.Contains(read(name), "con.idle().then(") {
			t.Errorf("%s 的自動讀取要用 con.idle(fn)", name)
		}
	}
	// 播放控制走 con.run 的 quiet 模式:自己打 /api/run 會繞過閘與中止——以前 body.cancel() 讓命令自我取消,
	// 改成讀完之後又因為沒有中止路徑,卡在等 token 鎖時整個介面鎖死(review #65 第 1 點;行為見 TestWebConsoleBehaviour)。
	if strings.Contains(player, "fetch('/api/run'") || !strings.Contains(player, "this.con.run(cmd, {}, { quiet: true })") {
		t.Error("player.control() 要走 con.run(quiet),不可以自己打 /api/run")
	}
	// 佔槽(閘、aria-disabled)當下就做;看得到的調暗與狀態列跟著 busyOn(播放控制過 QUIET_MS 才亮)。
	if !strings.Contains(console, "document.body.dataset.slot = ''") || !strings.Contains(common, "document.body.hasAttribute('data-slot')") {
		t.Error("頁面按鈕的閘要看 body[data-slot](播放控制也算)")
	}
	// 狀態列的樣子:hidden 要真的藏得住(display:flex 會蓋掉 UA 的 [hidden])、脈衝在減少動態時改靜態。
	if !strings.Contains(css, ".dock__busy[hidden] { display: none; }") || !strings.Contains(css, "body[data-busy] .dock::after { opacity: 1; }") {
		t.Error("執行狀態列要能藏起來,進行中 dock 頂線要亮(glow 在 budget 註解塊裡)")
	}
	if reduced := css[strings.Index(css, "prefers-reduced-motion"):]; !strings.Contains(reduced, ".dock__busy-dot") {
		t.Error("prefers-reduced-motion 要把 ● 脈衝改成靜態")
	}
	// dock 是 1fr 軌道裡的 grid item:沒有 min-width:0,一行很長的 stderr(授權網址、等鎖提示)會把整頁撐寬、
	// 把中止推出畫面;中止鈕本身不讓出寬度(CJK 會把「中止」拆成兩行)(review)。
	if !strings.Contains(css, ".dock { grid-area: dock; min-width: 0;") || !strings.Contains(css, ".dock__busy .btn { flex: none; white-space: nowrap; }") {
		t.Error("dock 要 min-width:0,狀態列的中止鈕不換行")
	}
	// 執行中被擋的說明(#notice)要念得出來;按鈕上的 ● 是裝飾,不進無障礙名稱。
	if !strings.Contains(index, `id="notice" role="status"`) || !strings.Contains(css, "content: '● ' / '';") {
		t.Error("#notice 要是 role=status;pending 的 ● 要用 content 替代文字")
	}
	// 回聲遮罩的切法要跟 splitArgs 一樣寬:只切單一空白會漏掉連續空白、tab 與引號(行為見 TestWebConsoleBehaviour)。
	if strings.Contains(between("export function maskSecrets(", "}"), "split(' ')") {
		t.Error("maskSecrets 不可以只切單一空白")
	}
	// pl dedup 沒有 --all:清單留空時不可以送它。
	sync := read("js/pages/sync.js")
	if !strings.Contains(sync, `verb === 'dedup' ? '' : ' --all'`) {
		t.Error("dedup 不可以帶 --all(cobra 會直接退回)")
	}
	// doctor 會彈系統對話框,不可以由切頁動作觸發。
	doctor := read("js/pages/doctor.js")
	// 空白態改成一句白話(決策 45),契約的本意不變:初始化時只畫空白態,go() 只掛在按鈕上。
	if !strings.Contains(doctor, "root.appendChild(idle);") || strings.Contains(doctor, "\n  go();") || strings.Contains(doctor, "con.idle(go)") {
		t.Error("診斷頁要先畫空白態,由使用者按鈕觸發")
	}
	// 鍵位表列出來的鍵要真的有實作,且表開著時單鍵不換頁。
	if !strings.Contains(app, "keysDialog.open") || !strings.Contains(app, "seekBy") || !strings.Contains(app, "volBy") {
		t.Error("? 表列的 ← → 與 + - 要有實作,且對話框開著時不換頁")
	}
	// local 的 id 含空白是常態,不 quote 會被 splitArgs 切斷。
	search := read("js/pages/search.js")
	if !strings.Contains(search, "quote(id)") {
		t.Error("play --id 要 quote")
	}
	// 從別頁按鈕發出的命令,區塊在被 hidden 的主控台頁裡:提示與授權連結不切回主控台就看不到,命令卡到逾時,
	// Apple 的揭露只剩伺服器端「送出過」(review #62 第 5 點)。
	if !strings.Contains(between("prompt(ev, b) {", "reveal() {"), "this.reveal()") { // 結束標記貼著 prompt() 的尾巴(review #64)
		t.Error("prompt() 要 reveal():提示畫在 hidden 的主控台頁裡等於沒畫")
	}
	if !strings.Contains(between("openURL(ev, b) {", "exit(b,"), "this.reveal()") {
		t.Error("openURL() 要 reveal():auth login spotify 的授權連結要看得到")
	}
	if r := between("reveal() {", "focusPrompt()"); !strings.Contains(r, ".page") || !strings.Contains(r, "'#/console'") {
		t.Error("reveal() 要在主控台頁 hidden 時切到 #/console")
	}
	// 斷線後殘留的提示(沒有 prompt_closed)與答案送出後已 disabled 的控制項,都不可以讓 focusPrompt() 回 true,
	// 否則命令列在那個分頁再也拿不到焦點(review #64)。
	if fp := between("focusPrompt() {", "async answer("); !strings.Contains(fp, ".block[data-running]") || !strings.Contains(fp, "f.disabled") {
		t.Error("focusPrompt() 只認還在跑的區塊,且跳過已 disabled 的控制項")
	}
	// 切頁的 hashchange 可能晚於 prompt() 的 setTimeout:route() 不可以再用 input.focus() 把焦點從提示搶走。
	if !strings.Contains(app, "if (!con.focusPrompt()) input.focus();") {
		t.Error("route() 到主控台時,有開著的提示要先把焦點給提示")
	}
	// 按鈕有焦點時空白鍵是「按下它」,單鍵層不可以搶(review #62 第 8 點)。
	if !strings.Contains(app, "ev.key === ' ' && document.activeElement?.tagName === 'BUTTON'") {
		t.Error("單鍵層要放過焦點在按鈕上的空白鍵")
	}
	// z-index 一律走 tokens.css 的 --z-*(設計規格 §8 / §13),字面數字會跟之後加的層打架(review #62 第 15 點)。
	if m := regexp.MustCompile(`z-index:\s*-?\d`).FindString(css); m != "" {
		t.Errorf("app.css 的 z-index 要用 var(--z-*):%q", m)
	}
}

// TestWebConsoleBehaviour:在 node 裡跑真的 console.js(testdata/webui_console.mjs 有最小的 DOM 替身與假的 /api/run)。
// 字串契約證明不了時序:一次一個的閘、onExit 在串流收尾後、兩頁同時等 idle()、start 之前就按中止、收尾那句不被
// 接著自動跑的命令清掉、做完的命令不當成中止、播放控制佔著槽。本機沒有 node 就跳過;CI(GitHub 的 runner 都有
// node)沒有就算失敗,免得它默默不跑。
func TestWebConsoleBehaviour(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("CI 上要有 node 才能跑前端行為測試")
		}
		t.Skip("沒有 node,跳過前端行為測試")
	}
	dir := t.TempDir()
	js := func(name string) string {
		t.Helper()
		b, err := webUI.ReadFile("webui/js/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	// 一律用 .mjs:不靠 node 對 .js 的 ESM 自動偵測(各版本預設不同),import 路徑跟著改。
	console := js("console.js")
	if !strings.Contains(console, "from './table.js'") {
		t.Fatal("console.js 的 import 路徑變了,這個測試的改寫要跟著改")
	}
	harness, err := os.ReadFile(filepath.Join("testdata", "webui_console.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"console.mjs": strings.Replace(console, "from './table.js'", "from './table.mjs'", 1),
		"table.mjs":   js("table.js"),
		"harness.mjs": string(harness),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "harness.mjs")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("前端行為不成立(%v):\n%s", err, out)
	}
}

// TestWebNonTTYPrintsURLAndDoesNotOpenBrowser + 只綁 loopback:buffer stdout(非 TTY)下印出 http://127.0.0.1:<port>/#t=…、
// 不叫 webOpenURL;拿印出的位址真的打一次 GET /,回 200 帶 CSP。
func TestWebNonTTYPrintsURLAndDoesNotOpenBrowser(t *testing.T) {
	setCLITestConfig(t)
	opened := 0
	origOpen := webOpenURL
	webOpenURL = func(string) error { opened++; return nil }
	t.Cleanup(func() { webOpenURL = origOpen })

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
	for deadline := time.Now().Add(5 * time.Second); url == ""; {
		url = urlRe.FindString(out.String())
		if url == "" && time.Now().After(deadline) {
			t.Fatalf("沒印出網址:%q", out.String())
		}
		if url == "" {
			time.Sleep(20 * time.Millisecond)
		}
	}
	base, frag, _ := strings.Cut(url, "/#t=")
	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("印出的位址要真的在聽:%v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Security-Policy") == "" {
		t.Errorf("GET /:%d %v", resp.StatusCode, resp.Header)
	}
	// token 字元集是 app.js 的 bootToken 正規式 [A-Za-z0-9_-]+ 的契約(base64 的 + / = 會被 JS 截斷成永遠 401)。
	if !regexp.MustCompile(`^[A-Za-z0-9_\-]+$`).MatchString(frag) || len(frag) < 32 {
		t.Errorf("token 要是 URL-safe 無 padding、夠長:%q", frag)
	}
	req, _ = http.NewRequest(http.MethodGet, base+"/api/commands", nil)
	req.Header.Set("X-Capy-Token", frag)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Errorf("印出的 fragment 直接當 X-Capy-Token 要 200:%v %v", err, resp)
	} else {
		resp.Body.Close()
	}
	if opened != 0 {
		t.Errorf("非 TTY 不開瀏覽器,叫了 %d 次", opened)
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("ctx 取消 = 正常結束:%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("伺服器沒在 ctx 取消後結束")
	}
}
