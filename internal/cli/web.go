package cli

// capy --web(P7,2026-09-17;計畫 docs/superpowers/plans/2026-09-17-web-mode.md 決策 40–41)。
//
// 不變式(-race 會抓,寫在檔頭給後人):Serve 開始後,newRootCmd()、defaultProvider()、resetDefaultProvider()、
// 任何 var 接縫的**寫入**只在 runMu 內發生;允許清單與 /api/commands 的命令樹在 Serve 前算一次;
// /api/now、/api/isrc 兩個直達端點(T4)不經 cobra、不進 runMu、不呼叫 defaultProvider()。
// 五個 stderr 全域是例外:它們在 Serve 前指派一次(之後不再寫那幾個 var),但**寫入那些 writer** 的 goroutine
// 不只 job——兩個直達端點也會經 BackoffStderr / LockStderr 印退避與等鎖提示(決策 42),所以併發安全靠的是
// sseWriter 的 mu + closed 與 curMu,不是 runMu。
// s.cur 由 curMu 守,是唯一的「目前 job」;橋接接縫(T3b)從它取 job,s.cur == nil 時回明確錯誤而不是掛住。

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/browser"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

//go:embed webui
var webUI embed.FS

// webOpenURL:啟動時開瀏覽器;只在 stdout 是終端機時呼叫(非 TTY 只印網址)。測試替換點。
var webOpenURL = browser.Open

// webCSP:前端零 inline(TestWebStaticHasCSPAndNoInline 讀 index.html 斷言);兩家 CDN host 依常見回應推定
// (Spotify 試聽是 p.scdn.co,review #58),T4 拿真回應驗證再定案。
const webCSP = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; " +
	"img-src 'self' https://i.scdn.co https://*.mzstatic.com; media-src https://p.scdn.co https://audio-ssl.itunes.apple.com https://*.mzstatic.com; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

type webServer struct {
	ctx      context.Context // 伺服器 ctx:SIGINT / SIGTERM(Execute 的 signal.NotifyContext)
	token    string          // 每次啟動一次性;URL fragment → sessionStorage → X-Capy-Token
	hostport string          // r.Host 必須逐字等於它(127.0.0.1:<port>;不收 localhost,同 Spotify redirect 措辭)
	allow    map[string]bool // CommandPath 允許清單,Serve 前算一次
	commands []webCommand    // /api/commands,Serve 前算一次
	static   http.Handler

	runMu sync.Mutex // 單一序列槽:同時只有一個 job;TryLock 失敗回 409
	curMu sync.Mutex
	cur   *webJob
	seq   atomic.Uint64
	stale atomic.Bool // capy update 成功後為 true:/api/run 一律 503,直到重啟

	fallbackStderr io.Writer // webGlobalStderr 沒有 job 時的去處(os.Stderr;測試換 buffer)
}

type webCommand struct {
	Path  string    `json:"path"`
	Use   string    `json:"use"`
	Short string    `json:"short"`
	Flags []webFlag `json:"flags"`
}

type webFlag struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand"`
	Usage     string `json:"usage"`
	Type      string `json:"type"`
	Default   string `json:"default"`
}

func newWebServer(ctx context.Context) (*webServer, error) {
	token, err := auth.NewState()
	if err != nil {
		return nil, err
	}
	sub, err := fs.Sub(webUI, "webui")
	if err != nil {
		return nil, err
	}
	root := newRootCmd()
	return &webServer{
		ctx: ctx, token: token,
		allow: webAllowlist(root), commands: webCommands(root),
		static: http.FileServerFS(sub), fallbackStderr: os.Stderr,
	}, nil
}

// runWeb:root 的 --web。非 TTY 啟動(launchd / nohup)也能開:只印網址、不開瀏覽器;網址連 token 一起
// 印到 stdout,轉向到檔案就在檔案裡(決策 41 的誠實邊界:同一個 OS 使用者)。
func runWeb(cmd *cobra.Command, port int) error {
	switch port {
	case 8888:
		return errors.New("--port 8888 是 Spotify 授權回呼的固定 port,請換一個")
	case 80, 443: // 瀏覽器會把預設埠號從 Host / Origin 拿掉,逐字比對必然不符 → 每個請求都 421,頁面完全打不開
		return errors.New("--port 80 / 443 不能用:瀏覽器會把預設埠號從 Host 標頭拿掉,每個請求都會被擋下")
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("綁定 127.0.0.1:%d 失敗:%w", port, err)
	}
	s, err := newWebServer(cmd.Context())
	if err != nil {
		ln.Close()
		return err
	}
	s.hostport = ln.Addr().String()
	restore := installWebSeams(s)
	defer restore()
	url := "http://" + s.hostport + "/#t=" + s.token
	fmt.Fprintf(cmd.OutOrStdout(), "capy --web 已啟動:%s\n(只綁 127.0.0.1;這個行程結束網址就失效;Ctrl-C 結束)\n", url)
	if stdoutIsTTY(cmd) {
		if err := webOpenURL(url); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "無法自動開瀏覽器:%v\n", err)
		}
	}
	return s.serve(ln)
}

func (s *webServer) serve(ln net.Listener) error {
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second} // 不設 WriteTimeout:要串流
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-s.ctx.Done(): // job 已由 handleRun 的 AfterFunc 砍掉,這裡只等 handler 收尾
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return nil
	case err := <-errc:
		return err
	}
}

func (s *webServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/commands", s.api(s.handleCommands))
	mux.HandleFunc("POST /api/run", s.api(s.handleRun))
	mux.HandleFunc("POST /api/jobs/{job}/cancel", s.api(s.handleCancel))
	mux.HandleFunc("/api/", s.api(func(w http.ResponseWriter, _ *http.Request) { httpErr(w, http.StatusNotFound, "沒有這個端點") }))
	mux.Handle("/", s.page(s.static))
	return mux
}

// guard:DNS rebinding(Host 逐字比對 → 421)、跨站(Origin 若存在必須同源、Sec-Fetch-Site 只收 same-origin / none → 403);
// 永不回 Access-Control-*。
func (s *webServer) guard(w http.ResponseWriter, r *http.Request) bool {
	if r.Host != s.hostport {
		http.Error(w, "Host 必須是 "+s.hostport, http.StatusMisdirectedRequest)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "http://"+s.hostport {
		http.Error(w, "Origin 不是這個頁面", http.StatusForbidden)
		return false
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		http.Error(w, "跨站請求", http.StatusForbidden)
		return false
	}
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	return true
}

// api:guard + token(常數時間比對,不符 401)+ no-store。
func (s *webServer) api(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.guard(w, r) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Capy-Token")), []byte(s.token)) != 1 {
			httpErr(w, http.StatusUnauthorized, "token 不對或已失效:回到啟動 capy --web 時印的網址")
			return
		}
		h(w, r)
	}
}

// page:靜態殼子不需要 token(裡面沒有祕密),但仍過 guard、帶 CSP。
func (s *webServer) page(h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.guard(w, r) {
			return
		}
		w.Header().Set("Content-Security-Policy", webCSP)
		w.Header().Set("Cache-Control", "no-cache") // 每次重新驗證:binary 更新後不能拿到舊頁
		h.ServeHTTP(w, r)
	}
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *webServer) handleCommands(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version":          version,
		"providers":        providerIDs,
		"default_provider": loadDefaultProvider(), // 每次讀 config,不走 defaultProvider() 的 OnceValue
		"commands":         s.commands,
	})
}

func (s *webServer) setCur(j *webJob) {
	s.curMu.Lock()
	s.cur = j
	s.curMu.Unlock()
}

func (s *webServer) current() *webJob {
	s.curMu.Lock()
	defer s.curMu.Unlock()
	return s.cur
}

// ── 接縫 ──

// installWebSeams:一次安裝、退出還原。T3a 第一版:三家精靈回既有的非互動錯誤(stdinIsTTY = false,不抓 /dev/tty)、
// 五個 stderr 全域改接 webGlobalStderr、bare capy 印 help 不開 TUI、now --watch 指路面板;isInteractive / bothTTY
// 維持 false(七個確認閘回 exit 2「待套用」,前端顯示「加 --yes 重跑」),T3b 的提示橋再翻成 true。
// runMu 保證同時只有一個 job,process 全域 var 沒有競態。
func installWebSeams(s *webServer) (restore func()) {
	origStdin, origTUI, origWatch := stdinIsTTY, runTUI, runWatch
	origBackoff, origStore, origDrive, origLogin, origLock := provider.BackoffStderr, store.Stderr, drive.Stderr, auth.LoginStderr, auth.LockStderr
	stdinIsTTY = func() bool { return false }
	runTUI = func(cmd *cobra.Command) error { return cmd.Help() }
	runWatch = func(*cobra.Command, provider.Provider, provider.PlaybackController) error {
		return errors.New("web 模式請看頁面上的播放狀態面板;單次用 capy now")
	}
	g := &webGlobalStderr{s: s}
	provider.BackoffStderr, store.Stderr, drive.Stderr, auth.LoginStderr = g, g, g, g
	auth.LockStderr = &webLockStderr{g}
	return func() {
		stdinIsTTY, runTUI, runWatch = origStdin, origTUI, origWatch
		provider.BackoffStderr, store.Stderr, drive.Stderr, auth.LoginStderr, auth.LockStderr = origBackoff, origStore, origDrive, origLogin, origLock
	}
}

// webGlobalStderr:五個繞過 cmd.ErrOrStderr() 的全域(429 退避、store 自癒、Drive、授權 URL、等鎖提示)的去處:
// 有 job 就寫進該 job 的 stderr 事件,沒有(或串流已關)就退回 os.Stderr。面板 / ISRC 頁的併發寫入也會落到
// 「當下 job」——內容為真、來源分不出,是接受的 cosmetic 代價(計畫 §5)。
type webGlobalStderr struct{ s *webServer }

func (g *webGlobalStderr) Write(p []byte) (int, error) {
	if j := g.s.current(); j != nil {
		if err := j.sse.event(map[string]any{"type": "stderr", "text": string(p)}); err == nil {
			return len(p), nil
		}
	}
	return g.s.fallbackStderr.Write(p)
}

// webLockStderr:LockFile 的「等待另一個 capy 釋放 …;要放棄按 Ctrl-C」在 web 會落進當下 job。
// <key>.token.lock:等的可能是同一行程的面板 / ISRC 頁在換發 token(review #56 第 1 點),整句改寫;
// pull.lock:等的真的是另一個 capy 行程(終端機 / cron),原文是對的,只把 Ctrl-C 換成頁面上有的鈕。
type webLockStderr struct{ w io.Writer }

var webLockNotice = strings.NewReplacer(
	"等待另一個 capy 釋放 ", "等待 token 鎖釋放(另一個 capy,或本頁面的播放面板 / ISRC 頁正在換發 token):",
	";要放棄按 Ctrl-C", ";要放棄按取消",
)

func (l *webLockStderr) Write(p []byte) (int, error) {
	s := string(p)
	if strings.Contains(s, ".token.lock") {
		s = webLockNotice.Replace(s)
	} else {
		s = strings.ReplaceAll(s, ";要放棄按 Ctrl-C", ";要放棄按取消")
	}
	if _, err := io.WriteString(l.w, s); err != nil {
		return 0, err
	}
	return len(p), nil
}
