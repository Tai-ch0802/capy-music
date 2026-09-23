package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

// ── job 與 SSE ──

type webJob struct {
	id     string
	ctx    context.Context
	cancel context.CancelCauseFunc
	sse    *sseWriter

	// 提示橋(web_prompt.go):pendingID 簿記——handleAnswer 只收 id 相符的答案,重複 POST 與遲到的舊答案進不了下一題。
	mu            sync.Mutex
	promptSeq     int
	pending       *pendingPrompt
	answers       chan webAnswer // cap 1
	promptTimeout time.Duration  // 0 = 不逾時(auth login *:Apple 使用者要開 DevTools 抄兩個 token)
}

var (
	errWebCancelled  = i18n.Errorf("web.err.cancelled")      // 使用者按中止 / 關分頁
	errWebShutdown   = i18n.Errorf("web.err.shutdown")       // SIGINT / SIGTERM
	errPromptTimeout = i18n.Errorf("web.err.prompt_timeout") // T3b:job 級提示逾時
	errSSEClosed     = i18n.Errorf("web.err.sse_closed")
)

// sseWriter:同一個 ResponseWriter 會被 handler goroutine 與(經 webGlobalStderr)面板輪詢 goroutine 寫,
// event() 整段(Marshal → 寫 → Flush)在 mu 內;handler 返回前標 closed,之後的寫入回錯——
// 429 退避提示(可拖到 62 s)絕不會寫到已結束的 ResponseWriter(net/http 的 write after handler)。
type sseWriter struct {
	mu     sync.Mutex
	w      http.ResponseWriter
	fl     http.Flusher
	closed bool
}

func (s *sseWriter) event(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errSSEClosed
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return err
	}
	s.fl.Flush()
	return nil
}

func (s *sseWriter) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// webStream:cobra 的 SetOut / SetErr 目的地。每個 Write 一個事件、不做行緩衝(前端拼到 \n 才切);
// 原樣文字含 ✅ ❌ ▶ ⏸ 🔊,無 ANSI(writer 不是 *os.File → stdoutIsTTY 為 false → 不上色、不開 pager、表格走 TSV
// 契約——但表格在到 TSV 之前就被 TableWriter 接走)。
type webStream struct {
	sse  *sseWriter
	kind string // stdout | stderr
}

func (w *webStream) Write(p []byte) (int, error) {
	if err := w.sse.event(map[string]any{"type": w.kind, "text": string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// webStdout:實作 ui.TableWriter——整張表含標題送到瀏覽器(標題就是各命令傳給 ui.Table 的 header 切片)。
type webStdout struct{ webStream }

func (w *webStdout) WriteTable(header []string, rows [][]string) error {
	if header == nil {
		header = []string{}
	}
	if rows == nil {
		rows = [][]string{}
	}
	return w.sse.event(map[string]any{"type": "table", "header": header, "rows": rows})
}

// ── 允許清單與 denylist ──

// webAllowlist:所有非 Hidden(祖先也非 Hidden)、非 help / completion 的 CommandPath,含群組(pl / auth / config /
// drive / history 沒有 RunE,在 CLI 印 help;用 tuiCommandsOf 的 Runnable-only 集合會 403)與 root 自己。
// 整個 debug 群組是 Hidden → 一律 403。
func webAllowlist(root *cobra.Command) map[string]bool {
	allow := map[string]bool{root.CommandPath(): true}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			allow[sub.CommandPath()] = true
			walk(sub)
		}
	}
	walk(root)
	return allow
}

// webCommands:/api/commands 的命令樹(Runnable 的,同 tuiCommandsOf 判準)含各自的 flag;Hidden 的 flag(--auto)不列。
func webCommands(root *cobra.Command) []webCommand {
	out := []webCommand{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			if sub.Runnable() {
				wc := webCommand{Path: sub.CommandPath(), Use: sub.Use, Short: sub.Short, Flags: []webFlag{}}
				sub.Flags().VisitAll(func(f *pflag.Flag) {
					if f.Hidden {
						return
					}
					wc.Flags = append(wc.Flags, webFlag{Name: f.Name, Shorthand: f.Shorthand, Usage: f.Usage, Type: f.Value.Type(), Default: f.DefValue})
				})
				out = append(out, wc)
			}
			walk(sub)
		}
	}
	walk(root)
	return out
}

// webDenyTokens:純字串 deny,鎖前、比對 args 每個 token(等於、或以它加 = 開頭)。五個都是 String / Bool 無短旗標。
// 不擋的話 start 事件回顯 args 就把 secret 送進頁面歷史。值是 i18n.Errorf:package 層級在 init 就建好,印出時才翻。
var webDenyTokens = map[string]error{
	"--auto":            i18n.Errorf("web.deny.auto"),
	"--web":             i18n.Errorf("web.deny.web"),
	"--client-secret":   i18n.Errorf("web.deny.client_secret"),
	"--developer-token": i18n.Errorf("web.deny.token"),
	"--user-token":      i18n.Errorf("web.deny.token"),
}

func webDenied(args []string) string {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		if why, ok := webDenyTokens[name]; ok {
			return i18n.T("web.deny", "flag", name, "why", why)
		}
	}
	return ""
}

// allowed:用這個 job 自己的樹 Find(cobra 的 Flags() 是 lazy init,共用靜態樹在 -race 下會被抓)。
// help pl / completion zsh / 未知子命令都 Find 到 root(這些子命令是 ExecuteC 時才掛的)→ 允許 → cobra 照常處理。
func (s *webServer) allowed(root *cobra.Command, args []string) (string, bool) {
	c, _, err := root.Find(args)
	if err != nil || c == nil {
		c = root
	}
	path := c.CommandPath()
	return path, s.allow[path]
}

// ── /api/run ──

func (s *webServer) handleRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Args []string `json:"args"`
		Line string   `json:"line"`
	}
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, i18n.T("web.err.bad_json", "err", err))
		return
	}
	// 讀到 EOF:net/http 對有 Content-Length 的 body 要讀完才起 background read、連線斷掉才會 cancel r.Context();
	// 沒有這行「關分頁即放鎖」不成立(TestWebDisconnectCancelsJobAndPrompt)。
	_, _ = io.Copy(io.Discard, body)
	args := req.Args
	if strings.TrimSpace(req.Line) != "" {
		args = splitArgs(req.Line)
	}
	if args == nil {
		args = []string{} // 絕不 nil:cobra 對 nil 會退回 os.Args[1:]
	}
	if msg := webDenied(args); msg != "" {
		httpErr(w, http.StatusForbidden, msg)
		return
	}
	if !s.runMu.TryLock() {
		httpErr(w, http.StatusConflict, i18n.T("web.err.busy"))
		return
	}
	defer s.runMu.Unlock()
	// 鎖內才檢查:換掉 binary 的那個 job 是在鎖內結束時才立旗,鎖外讀會讀到它立旗之前的值,
	// 然後在「磁碟上已是新 binary」的舊行程裡再跑一個命令——正是這道閘要擋的 state.db 互相 retire。
	if s.stale.Load() {
		httpErr(w, http.StatusServiceUnavailable, i18n.T("web.stale"))
		return
	}
	resetDefaultProvider()      // 長駐行程要看到終端機改的 config.json
	langWarn := applyLanguage() // 同上;語系要在建命令樹之前定(決策 50)
	root := newRootCmd()
	path, ok := s.allowed(root, args)
	if !ok {
		httpErr(w, http.StatusForbidden, i18n.T("web.err.not_offered", "command", path))
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		httpErr(w, http.StatusInternalServerError, i18n.T("web.err.no_stream"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	sse := &sseWriter{w: w, fl: fl}

	// 取消三路匯到同一個 ctx:關分頁(r.Context)、伺服器關閉(AfterFunc)、cancel 端點。
	jobCtx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	stop := context.AfterFunc(s.ctx, func() { cancel(errWebShutdown) })
	defer stop()
	job := &webJob{id: strconv.FormatUint(s.seq.Add(1), 10), ctx: jobCtx, cancel: cancel, sse: sse,
		answers: make(chan webAnswer, 1), promptTimeout: webPromptTimeout}
	if strings.HasPrefix(path, "capy auth login") {
		// 5 分鐘對「開 DevTools 抄兩個 token」不夠,但「完全沒有上限」的代價是:分頁被放生就永久占住單一序列槽,
		// 之後每個命令都 409 且永遠不會自己好(auth login 不取 pull.lock,所以只卡 web 自己)。給寬鬆但有限的上限。
		job.promptTimeout = webAuthPromptTimeout
	}
	s.setCur(job)
	defer func() { // defer:命令 panic(net/http 會 recover)也要守「絕不寫到已結束的 ResponseWriter」(review #59)
		sse.close()   // 之後 webGlobalStderr 的寫入退回 os.Stderr
		s.setCur(nil) // 在 closed 之後:沒有任何寫入會落到已結束的 ResponseWriter
	}()
	_ = sse.event(map[string]any{"type": "start", "job": job.id, "args": args, "path": path})

	root.SetOut(&webStdout{webStream{sse, "stdout"}})
	root.SetErr(&webStream{sse, "stderr"})
	if langWarn != "" {
		fmt.Fprintln(root.ErrOrStderr(), langWarn)
	}
	root.SetArgs(args)
	err := root.ExecuteContext(jobCtx)
	code, msg := ExitCode(err)
	if path == "capy update" && executableReplaced.Load() { // update 真的換了 binary(no-op 的「已是最新」不算;review #59)
		s.stale.Store(true)
		_ = sse.event(map[string]any{"type": "stderr", "text": i18n.T("web.stale") + "\n"})
	}
	if webNowInvalidatedBy(path) { // 帳號 / 預設平台變了:快取的 PlaybackController 不再有效
		s.dropNow()
	}
	reason := webExitReason(jobCtx, err)
	// 取消本身(「已取消」、Get …: context canceled)不是訊息:頁面看 reason 就知道(計畫 §2.4 第 5 點),
	// 不必比對跟著語系變的文字。取消時剛好撞上的別的錯誤照送。
	if reason == "cancelled" && (errors.Is(err, errWebCancelled) || errors.Is(err, context.Canceled)) {
		msg = ""
	}
	_ = sse.event(map[string]any{"type": "exit", "code": code, "message": msg, "reason": reason})
}

// webExitReason:命令回 nil 就是做完了,即使中止 / 關分頁在它做完之後才落下(或落在不吃取消的那一段,
// 例如 Apple 的 osascript)——回 cancelled 會讓頁面說「已中止」、叫人重跑一個已經做完的命令(review)。
func webExitReason(ctx context.Context, err error) string {
	cause := context.Cause(ctx)
	switch {
	case err == nil || cause == nil:
		return "done"
	case errors.Is(cause, errWebShutdown):
		return "shutdown"
	case errors.Is(cause, errPromptTimeout):
		return "timeout"
	default:
		return "cancelled"
	}
}

func (s *webServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	j := s.current()
	if j == nil || j.id != r.PathValue("job") {
		httpErr(w, http.StatusNotFound, i18n.T("web.err.no_job"))
		return
	}
	j.cancel(errWebCancelled)
	w.WriteHeader(http.StatusNoContent)
}
