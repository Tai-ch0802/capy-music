package cli

// capy --web(P7,2026-09-17;計畫 docs/superpowers/plans/2026-09-17-web-mode.md 決策 40–41)。
//
// 不變式(-race 會抓,寫在檔頭給後人):Serve 開始後,newRootCmd()、defaultProvider()、resetDefaultProvider()、
// 任何 var 接縫與 i18n.Set 的**寫入**只在 runMu 內發生;允許清單與 /api/commands 的命令樹在 Serve 前算好(後者每個語系一份);
// /api/now、/api/isrc(T4)、/api/i18n(i18n T3)三個直達端點不經 cobra、不進 runMu、不呼叫 defaultProvider()、不 i18n.Set。
// 五個 stderr 全域是例外:它們在 Serve 前指派一次(之後不再寫那幾個 var),但**寫入那些 writer** 的 goroutine
// 不只 job——/api/now 與 /api/isrc 也會經 BackoffStderr / LockStderr 印退避與等鎖提示(決策 42),所以併發安全靠的是
// sseWriter 的 mu + closed 與 curMu,不是 runMu。/api/i18n 只讀 config 與嵌入的語系目錄,不寫任何 stderr。
// s.cur 由 curMu 守,是唯一的「目前 job」;橋接接縫(T3b)從它取 job,s.cur == nil 時回明確錯誤而不是掛住。

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/browser"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

//go:embed webui
var webUI embed.FS

// webOpenURL:啟動時開瀏覽器;只在 stdout 是終端機時呼叫(非 TTY 只印網址)。測試替換點。
var webOpenURL = browser.Open

// webCSP:前端零 inline(TestWebStaticHasCSPAndNoInline 讀 index.html 斷言);兩家 CDN host 依常見回應推定
// (Spotify 封面 i.scdn.co / 試聽 p.scdn.co;Apple is1-ssl.mzstatic.com 被 *.mzstatic.com 蓋到、試聽 audio-ssl.itunes.apple.com)。
// **還沒被真回應驗證過**:T4 的 smoke 是在沒有憑證的乾淨 config 下跑的,沒有任何一張封面真的被載入。
// 驗收點是 R-10 / R-11(真帳號唯讀):查一首有封面與試聽的歌,看 console 有沒有 CSP 違規,不合就放寬到 https:。
const webCSP = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; " +
	"img-src 'self' https://i.scdn.co https://*.mzstatic.com; media-src https://p.scdn.co https://audio-ssl.itunes.apple.com https://*.mzstatic.com; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

type webServer struct {
	ctx      context.Context // 伺服器 ctx:SIGINT / SIGTERM(Execute 的 executeSignalled;serve 回 nil,結束碼 130 / 143 由 Execute 補)
	token    string          // 每次啟動一次性;URL fragment → sessionStorage → X-Capy-Token
	hostport string          // r.Host 必須逐字等於它(127.0.0.1:<port>;不收 localhost,同 Spotify redirect 措辭)
	allow    map[string]bool // CommandPath 允許清單,Serve 前算一次
	// commands:/api/commands,語系 → 命令清單,Serve 前算好(決策 50:切語系不必重建命令樹)
	commands map[string][]webCommand
	static   http.Handler

	runMu sync.Mutex // 單一序列槽:同時只有一個 job;TryLock 失敗回 409
	curMu sync.Mutex
	cur   *webJob
	seq   atomic.Uint64
	stale atomic.Bool // capy update 成功後為 true:/api/run 一律 503,直到重啟

	fallbackStderr io.Writer // webGlobalStderr 沒有 job 時的去處(os.Stderr;測試換 buffer)

	// 播放面板(web_api.go;決策 51):provFlag 是啟動時的 --provider(有明指才算,釘住面板)。
	// now 是每個 provider 的 PlaybackController 快取(伺服器 ctx 建一次);nowCache 是每個 provider 最近一次真的問到的結果
	// 與有效期(Spotify 的配額靠它省);兩者都由 nowMu 守。pollMu 讓同時只有一輪在問平台;lastNow 是上一輪顯示的那份
	// (stale 回應用),shown 是上一輪顯示的平台(跟隨規則的 base;只有換帳號、換預設平台時 dropNow 才清,換語系不該讓面板跳平台);
	// nowGen 由 dropNow 遞增,在飛的那一輪拿舊世代的結果就不寫回快取與快照;settleUntil 是播放命令後的安定期。
	provFlag    string
	nowMu       sync.Mutex
	now         map[string]provider.PlaybackController
	nowCache    map[string]nowEntry
	pollMu      sync.Mutex
	lastNow     atomic.Pointer[nowSnapshot]
	shown       atomic.Pointer[string]
	nowGen      atomic.Uint64
	settleUntil atomic.Pointer[time.Time]
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
	cmds := map[string][]webCommand{}
	lang := i18n.Current()
	for _, l := range i18n.Supported() { // Serve 前、還沒有任何 job:暫時切語系建樹不會跟誰搶
		i18n.Set(l)
		cmds[l] = webCommands(newRootCmd())
	}
	i18n.Set(lang)
	return &webServer{
		ctx: ctx, token: token,
		allow: webAllowlist(root), commands: cmds,
		static: http.FileServerFS(sub), fallbackStderr: os.Stderr,
	}, nil
}

// runWeb:root 的 --web。非 TTY 啟動(launchd / nohup)也能開:只印網址、不開瀏覽器;網址連 token 一起
// 印到 stdout,轉向到檔案就在檔案裡(決策 41 的誠實邊界:同一個 OS 使用者)。
func runWeb(cmd *cobra.Command, port int) error {
	switch port {
	case 8888:
		return i18n.Errorf("web.err.port_8888")
	case 80, 443: // 瀏覽器會把預設埠號從 Host / Origin 拿掉,逐字比對必然不符 → 每個請求都 421,頁面完全打不開
		return i18n.Errorf("web.err.port_default")
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return i18n.Errorf("web.err.listen", "port", port, "err", err)
	}
	s, err := newWebServer(cmd.Context())
	if err != nil {
		ln.Close()
		return err
	}
	s.hostport = ln.Addr().String()
	if cmd.Flags().Changed(flagProvider) { // 同 runTUI:只有明指才覆蓋 config 的 default_provider
		s.provFlag, _ = cmd.Flags().GetString(flagProvider)
	}
	restore := installWebSeams(s)
	defer restore()
	url := "http://" + s.hostport + "/#t=" + s.token
	fmt.Fprintln(cmd.OutOrStdout(), i18n.T("web.started", "url", url))
	if stdoutIsTTY(cmd) {
		if err := webOpenURL(url); err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), i18n.T("web.open_browser_failed", "err", err))
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
	mux.HandleFunc("GET /api/i18n", s.api(s.handleI18n))
	mux.HandleFunc("GET /api/isrc/{isrc}", s.api(s.handleISRC))
	mux.HandleFunc("GET /api/now", s.api(s.handleNow))
	mux.HandleFunc("POST /api/run", s.api(s.handleRun))
	mux.HandleFunc("POST /api/jobs/{job}/cancel", s.api(s.handleCancel))
	mux.HandleFunc("POST /api/jobs/{job}/answer", s.api(s.handleAnswer))
	mux.HandleFunc("/api/", s.api(func(w http.ResponseWriter, _ *http.Request) {
		httpErr(w, http.StatusNotFound, i18n.T("web.err.no_endpoint"))
	}))
	mux.Handle("/", s.page(s.static))
	return mux
}

// guard:DNS rebinding(Host 逐字比對 → 421)、跨站(Origin 若存在必須同源、Sec-Fetch-Site 只收 same-origin / none → 403);
// 永不回 Access-Control-*。
func (s *webServer) guard(w http.ResponseWriter, r *http.Request) bool {
	if r.Host != s.hostport {
		http.Error(w, i18n.T("web.err.bad_host", "host", s.hostport), http.StatusMisdirectedRequest)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "http://"+s.hostport {
		http.Error(w, i18n.T("web.err.bad_origin"), http.StatusForbidden)
		return false
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		http.Error(w, i18n.T("web.err.cross_site"), http.StatusForbidden)
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
			httpErr(w, http.StatusUnauthorized, i18n.T("web.err.bad_token"))
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
	lang, _ := configLanguage() // 同 default_provider:每次讀 config,終端機切的語系也看得到(不 Set:這裡在 runMu 外)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version":          version,
		"providers":        providerIDs,
		"default_provider": loadDefaultProvider(), // 每次讀 config,不走 defaultProvider() 的 OnceValue
		"commands":         s.commands[lang],
	})
}

// webSharedKeys:網頁也要用、但不在 webui.* 底下的 key(同一句話 Go 那邊也在印)。/api/i18n 只送 webui.* 與這裡列的;
// 頁面用到別的 key 只會拿到 key 本身(TestWebI18nServesEveryKeyThePageUses 會擋)。
var webSharedKeys = []string{
	"web.err.bad_token",        // app.js 的 401 提示:跟 api() 回的是同一句
	"web.err.cancelled",        // console.js 的 exit 行:中止的命令
	"web.err.prompt_timeout",   // console.js 的 exit 行:等待回答逾時
	"changeset.confirm.apply",  // 確認鈕的字(web_prompt.go 送的 affirmative):搬家精靈的白話講到它
	"changeset.confirm.cancel", // 同上(negative);console.js 在確認框沒給否定鈕的字時也用它
	"local.display_name",       // common.js 的 providerName:本機曲庫的顯示名稱
}

// webI18n:/api/i18n 的回應。messages 的值是字串,複數訊息是「CLDR 類別 → 字串」;supported 的 name 是語系自己的名稱。
func webI18n(lang string) map[string]any {
	supported := []map[string]string{}
	for _, code := range i18n.Supported() {
		supported = append(supported, map[string]string{"code": code, "name": i18n.LocaleName(code)})
	}
	messages := i18n.Messages(lang, func(k string) bool { return strings.HasPrefix(k, "webui.") || slices.Contains(webSharedKeys, k) })
	return map[string]any{"lang": lang, "supported": supported, "messages": messages}
}

// handleI18n:GET /api/i18n——網頁的語系目錄。直達端點:同 handleCommands 每次讀 config 的 language(終端機切的也看得到),
// 只讀嵌入的目錄、不 i18n.Set(這裡在 runMu 外)。
func (s *webServer) handleI18n(w http.ResponseWriter, _ *http.Request) {
	lang, _ := configLanguage()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(webI18n(lang))
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

// installWebSeams:一次安裝、退出還原。五個 stderr 全域改接 webGlobalStderr、bare capy 印 help 不開 TUI、
// now --watch 指路面板;互動閘與每個 huh 接縫由 installWebPromptSeams(web_prompt.go)換成提示橋。
// runMu 保證同時只有一個 job,process 全域 var 沒有競態。
func installWebSeams(s *webServer) (restore func()) {
	origTUI, origWatch, origProgress := runTUI, runWatch, reportProgress
	origBackoff, origStore, origDrive, origLogin, origLock := provider.BackoffStderr, store.Stderr, drive.Stderr, auth.LoginStderr, auth.LockStderr
	restorePrompts := installWebPromptSeams(s)
	runTUI = func(cmd *cobra.Command) error { return cmd.Help() }
	runWatch = func(*cobra.Command, provider.Provider, provider.PlaybackController) error {
		return i18n.Errorf("web.err.no_watch")
	}
	// 真實進度(P8 決策 47):送給當下的 job;沒有 job(或串流已關)就丟掉——進度不是輸出,不退回 stderr。
	reportProgress = func(stage string, done, total int) {
		if j := s.current(); j != nil {
			_ = j.sse.event(map[string]any{"type": "progress", "stage": stage, "done": done, "total": total})
		}
	}
	g := &webGlobalStderr{s: s}
	provider.BackoffStderr, store.Stderr, drive.Stderr, auth.LoginStderr = g, g, g, g
	auth.LockStderr = &webLockStderr{g}
	return func() {
		restorePrompts()
		runTUI, runWatch, reportProgress = origTUI, origWatch, origProgress
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
// <key>.token.lock:等的可能是同一行程的面板 / ISRC 頁在換發 token(review #56 第 1 點),檔名之前的半句改寫;
// pull.lock:等的真的是另一個 capy 行程(終端機 / cron),原文是對的,只把 Ctrl-C 換成頁面上有的鈕(dock 的「中止」)。
// 不比對 auth 的措辭(它跟著語系):只認檔名與「Ctrl-C」這兩個任何語系都不翻的字(README 的慣例)。
//
// 給 internal/auth/tokenstore.go 的契約(T2d 把那句等鎖提示搬進語系目錄時,每個語系都要守):
//   - 傳給 LockFile 的鎖檔名原樣出現(spotify.token.lock、pull.lock;token 鎖靠 webTokenLockName 認),
//     token 鎖的整句從檔名那裡開始保留、前半句換成 web.lock.token;
//   - 按鍵寫成 " Ctrl-C"(半形空白 + Ctrl-C,連同空白換成 web.lock.stop;譯文自己帶要不要空白,見 README)。
//
// 違反了不會報錯、只是網頁上照樣叫人按 Ctrl-C——auth 的 TestLockNoticeKeepsWebContract 在每個語系釘住這兩點。
type webLockStderr struct{ w io.Writer }

// webTokenLockName:等的是 <key>.token.lock(Spotify / Google 的 TokenSource);pull.lock 不符。
var webTokenLockName = regexp.MustCompile(`[A-Za-z0-9_.-]+\.token\.lock`)

func (l *webLockStderr) Write(p []byte) (int, error) {
	// web.lock.stop 自帶前面的空白(en " Stop"、zh 「中止」不要空白),所以連 Ctrl-C 前的空白一起換掉。
	s := strings.ReplaceAll(string(p), " Ctrl-C", i18n.T("web.lock.stop"))
	if loc := webTokenLockName.FindStringIndex(s); loc != nil {
		s = i18n.T("web.lock.token", "rest", s[loc[0]:])
	}
	if _, err := io.WriteString(l.w, s); err != nil {
		return 0, err
	}
	return len(p), nil
}
