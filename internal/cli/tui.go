package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/ai"
	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// capy(無參數、在終端機裡)= 互動式介面。形態是**訊息流 + 固定底部區**(Claude Code 那種),
// 不是全螢幕接管:
//
//   - 終端機夠高(tuiAliveMinHeight)時水豚常駐在底部區的上方、一直動(2026-09-21;使用者:「希望 CLI 的水豚
//     也能像 web 一樣一直動」;計畫 docs/superpowers/plans/2026-09-21-capybara-alive.md)。牠**只活在 View 裡、
//     永遠不印進捲動區**,而且那一塊的高度是固定的(選單或抽屜打開時借牠的位置)——下面講的災難全是「畫面變矮」造成的。
//   - 終端機不夠高或不夠寬:照舊,開場水豚照劇本演一遍(約三秒)就定格印進捲動區,之後永不重繪。
//   - 除此之外 TUI 只管底部四行(進度分隔線 / 狀態 / 輸入 / 提示),其餘全部是終端機自己的捲動區。
//   - 命令的回音、錯誤、結束碼都用 tea.Println 推進捲動區:永久、可往回捲、可複製。
//
// 純播放控制(pause / next / prev / 無參 play / seek / vol / sleep)與本機已快取的歌曲 wiki 直接在行程內完成,
// 常駐水豚不閃縮、不重讀 keychain;需要真 TTY 互動(huh 表單、檢視窗格)的子命令仍以 tea.ExecProcess 重新執行 capy。
//
// Update 是純函式(tea.Msg 進、model 出),測試不需要 TTY。

const (
	tuiFrameInterval = 250 * time.Millisecond // 水豚的動作
	// 劇本(capyStory)演完一遍就定格,約三秒;任何按鍵都會提早定格。
	tuiIntro    = time.Duration(len(capyStory)) * tuiFrameInterval
	tuiSeekStep = 10000 // ←/→ 一次 10 秒
	tuiVolStep  = 5     // +/- 一次 5
	tuiMaxFails = 5
	// 每次 State 的上限(now --watch 也用它,決策 58)。輪詢帶 WithoutWait,不會睡在 429 退避裡,
	// 這只是卡住的上限:osascript 或 HTTP 卡住時狀態列最多停這麼久就回錯,不是一分鐘。
	tuiStateTimeout = 10 * time.Second
	tuiMinWidth     = 46 // 窄於此:橫幅換成一行
	// 水豚常駐需要的終端機高度。常駐的畫面是 capyBlockRows + 4 = 15 行;View 比終端機高的話 inline renderer 的
	// 游標上移會被頂端截斷、舊畫面留在上面(PR #45 修掉的「水豚頭重複三次」)。牠最多佔半個畫面,所以是兩倍。
	tuiAliveMinHeight = 2 * (capyBlockRows + 4)
)

type (
	tuiFrameMsg  time.Time
	tuiFreezeMsg struct{} // 開場結束:水豚定格印進捲動區
	tuiSecondMsg struct{ seq int }
	tuiPollMsg   struct{ gen int }
	tuiStateMsg  struct {
		st       *provider.PlaybackState
		err      error
		fromCtl  bool // 控制指令的錯:顯示但不計入 fails(那個預算是給「連不上」用的)
		gen      int
		provider string
		retryAt  time.Time
		cached   bool
		dropped  bool
		slow     bool
		pending  chan tuiStateMsg
		panicked any
	}
	tuiExecMsg struct {
		args []string
		err  error
	}
	tuiWikiCachedMsg struct {
		key  string
		view *cachedWikiView
	}
)

type tuiModel struct {
	ctx       context.Context
	theme     ui.Theme
	themeName string
	exe       string // 重新執行自己用;空 = 取不到,子行程命令停用
	provID    string // 狀態列顯示的平台:明指就是它,否則跟著正在播的那一家走(決策 51;Q64)。控制鍵送給它
	provFlag  string // 使用者在 capy --provider X 明指的平台:釘住狀態列,命令列也要把它一起帶給子命令
	trk       *nowTracker
	sleep     *sleepTimer
	pcErr     error // provID 沒有播放遙控的原因(沒登入、平台不支援):顯示,不致命
	interval  time.Duration
	width     int
	height    int  // 0 = 還沒收到 WindowSizeMsg:當作不夠高(不常駐),等知道了再說
	motion    bool // CAPY_MOTION=never 時是 false:不演開場、不常駐,水豚直接定格印進捲動區
	frame     int
	input     textinput.Model
	typing    bool

	st           *provider.PlaybackState
	stAt         time.Time               // 收到目前 st 的單調時間(0 = 測試直接塞入,不做本地秒數插值)
	optBase      *provider.PlaybackState // 樂觀預覽前的真實狀態,控制失敗時回滾
	optAt        time.Time
	clockRunning bool
	clockSeq     int

	wikiKey  string
	wikiView *cachedWikiView

	errShort string // 狀態列用的短版。完整那段在發生當下就進捲動區了,model 不留
	lastErr  string // 上一則交代過的錯誤:同一則每兩秒印一次會把捲動區洗掉
	fails    int
	gen      int  // 目前的輪詢世代
	stalled  bool // 連續讀不到狀態,輪詢先停下來(按 r 重試);介面不關
	ctrlC    bool // 是按 Ctrl-C 離開的:exit 130(見 runProgram)
	frozen   bool // 水豚已定格印進捲動區,View 只剩底部四行。單向不變式:印過就不再常駐
	running  bool // 子命令執行中:底部區縮成一行空白(原因見 View)
	menuHigh int  // 上次推東西進捲動區之後選單或抽屜佔過的最多列數;View 補空行撐到這個高度

	cmds      []tuiCmdItem
	menuSel   int      // 斜線選單選到第幾列(選單開著才有意義)
	hist      []string // 命令歷史(跨 session 由 state.db 載入與追加,↑↓ 翻)
	histAt    int      // 翻到哪:len(hist) = 還沒開始翻
	histDraft string   // 開始翻之前打到一半的那行,翻回最新時要拿回來

	drawer         tuiDrawerState
	lastCollectPID string
	lastCollectArg string
}

func newTUIModel(ctx context.Context, theme ui.Theme, exe, provID, provFlag string, trk *nowTracker, interval time.Duration) tuiModel {
	in := textinput.New()
	// 提示符刻意用 ASCII:textinput 會把整行填滿到它自己算的 w-1,而 › 是 East Asian Ambiguous,
	// 在 CJK 終端機多佔一欄 = 剛好寫滿最後一欄 = 多換一行,底部就變五行。
	in.Prompt = "> "
	in.CharLimit = 240
	in.Placeholder = tuiPlaceholder(80)
	in.SetWidth(76)
	applyInputTheme(&in, theme)

	timerCtx := ctx
	if trk != nil && trk.ctx != nil {
		timerCtx = trk.ctx
	}
	hist := tuiLoadHistory()
	return tuiModel{
		ctx: ctx, theme: theme, themeName: tuiResolveThemeName(theme),
		exe: exe, provID: provID, provFlag: provFlag, trk: trk,
		sleep: newTrackerSleepTimer(timerCtx, trk),
		interval: interval, width: 80, input: in, motion: os.Getenv("CAPY_MOTION") != "never",
		hist: hist, histAt: len(hist),
	}
}

func applyInputTheme(in *textinput.Model, theme ui.Theme) {
	st := textinput.DefaultDarkStyles()
	st.Focused.Prompt = st.Focused.Prompt.Foreground(theme.Accent)
	st.Blurred.Prompt = st.Blurred.Prompt.Foreground(theme.Muted)
	st.Focused.Placeholder = st.Focused.Placeholder.Foreground(theme.Muted)
	st.Blurred.Placeholder = st.Blurred.Placeholder.Foreground(theme.Muted)
	st.Cursor.Color = theme.Accent
	in.SetStyles(st)
}

func (m *tuiModel) setTheme(theme ui.Theme) {
	m.theme = theme
	applyInputTheme(&m.input, theme)
}

func tuiResolveThemeName(theme ui.Theme) string {
	for _, name := range ui.ThemeNames() {
		if cand, ok := ui.LookupTheme(name); ok && cand == theme {
			return name
		}
	}
	return ui.DefaultThemeName
}

func tuiLoadHistory() []string {
	if !tuiStoreAllowed() {
		return nil
	}
	s, err := store.OpenReadOnly(tuiMenuStoreBusy)
	if err != nil {
		return nil
	}
	defer s.Close()
	hist, err := s.LoadTUIHistory()
	if err != nil {
		return nil
	}
	return hist
}

// tuiHistoryDenied:凡含 webDenyTokens 列管的機密或禁止旗標(--api-key / --header / --client-secret / --developer-token /
// --user-token / --headers-file 等),一律不寫入記憶體歷史與本機 SQLite。
func tuiHistoryDenied(args []string) bool {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		if _, bad := webDenyTokens[name]; bad {
			return true
		}
	}
	return false
}

func tuiAppendHistoryToStore(cmd string) {
	if !tuiStoreAllowed() {
		return
	}
	s, err := store.Open(tuiMenuStoreBusy)
	if err != nil {
		return
	}
	defer s.Close()
	_ = s.AppendTUIHistory(cmd, webNowClock())
}

func (m *tuiModel) recordHistory(raw string, args []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || tuiHistoryDenied(args) {
		m.histAt, m.histDraft = len(m.hist), ""
		return
	}
	if len(m.hist) == 0 || m.hist[len(m.hist)-1] != raw {
		m.hist = append(m.hist, raw)
		if len(m.hist) > store.TUIHistoryMax {
			m.hist = m.hist[len(m.hist)-store.TUIHistoryMax:]
		}
	}
	m.histAt, m.histDraft = len(m.hist), ""
	if testing.Testing() {
		tuiAppendHistoryToStore(raw)
	} else {
		go tuiAppendHistoryToStore(raw)
	}
}

func (m tuiModel) Init() tea.Cmd {
	if !m.motion { // 不要動畫:不排 frame tick,馬上定格
		return tea.Batch(func() tea.Msg { return tuiFreezeMsg{} }, m.poll())
	}
	return tea.Batch(m.frameTick(), tea.Tick(tuiIntro, func(time.Time) tea.Msg { return tuiFreezeMsg{} }), m.poll())
}

func (m tuiModel) frameTick() tea.Cmd {
	return tea.Tick(tuiFrameInterval, func(t time.Time) tea.Msg { return tuiFrameMsg(t) })
}

func (m tuiModel) pollTick() tea.Cmd {
	gen := m.gen
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return tuiPollMsg{gen: gen} })
}

func (m tuiModel) needSecondTick() bool {
	if m.st != nil && m.st.Playing && !m.stalled {
		return true
	}
	if m.sleep != nil && m.sleep.Snapshot().Mode != SleepOff {
		return true
	}
	return false
}

func (m tuiModel) ensureSecondTick() (tuiModel, tea.Cmd) {
	if !m.needSecondTick() || m.clockRunning {
		return m, nil
	}
	m.clockRunning = true
	m.clockSeq++
	seq := m.clockSeq
	return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return tuiSecondMsg{seq: seq} })
}

// tuiRoundWait:畫面等一輪(或一個控制鍵)的上限。測試替換點。
var tuiRoundWait = 3 * time.Second

func tuiAwait(gen int, wait time.Duration, run func() tuiStateMsg) tuiStateMsg {
	done := make(chan tuiStateMsg, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- tuiStateMsg{gen: gen, panicked: fmt.Sprintf("%v\n%s", r, debug.Stack())}
			}
		}()
		done <- run()
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case msg := <-done:
		return msg.repanic()
	case <-timer.C:
		return tuiStateMsg{gen: gen, slow: true, pending: done}
	}
}

func (msg tuiStateMsg) repanic() tuiStateMsg {
	if msg.panicked != nil {
		panic(msg.panicked)
	}
	return msg
}

func (m tuiModel) poll() tea.Cmd { return nowPoll(m.trk, m.provFlag, m.gen) }

func (m tuiModel) round() func() tuiStateMsg { return nowRound(m.trk, m.provFlag, m.gen) }

func (m tuiModel) control(f func(provider.PlaybackController, context.Context) error, once bool) tea.Cmd {
	return nowControl(m.ctx, m.trk, m.provID, m.provFlag, m.gen, f, once)
}

func (m tuiModel) controlOn(prov string, f func(provider.PlaybackController, context.Context) error, once bool) tea.Cmd {
	if prov == "" {
		prov = m.provID
	}
	return nowControl(m.ctx, m.trk, prov, m.provFlag, m.gen, f, once)
}

func tuiIgnoreOpenedErr(err error) error {
	if errors.Is(err, provider.ErrOpenedNotPlaying) {
		return err
	}
	return err
}

func nowPoll(trk *nowTracker, pin string, gen int) tea.Cmd {
	round, wait := nowRound(trk, pin, gen), tuiRoundWait
	return func() tea.Msg { return tuiAwait(gen, wait, round) }
}

func nowRound(trk *nowTracker, pin string, gen int) func() tuiStateMsg {
	return func() tuiStateMsg {
		res, ok := trk.stepRound(pin)
		if !ok {
			return tuiStateMsg{gen: gen, dropped: true}
		}
		return tuiStateMsg{provider: res.provider, st: res.st, err: res.err, retryAt: res.retryAt, cached: !res.fresh, gen: gen}
	}
}

func nowControl(ctx context.Context, trk *nowTracker, id, pin string, gen int, f func(provider.PlaybackController, context.Context) error, once bool) tea.Cmd {
	round, wait := nowRound(trk, pin, gen), tuiRoundWait
	return func() tea.Msg {
		return tuiAwait(gen, wait, func() tuiStateMsg {
			if trk.ctx.Err() != nil {
				return tuiStateMsg{gen: gen, dropped: true}
			}
			start := time.Now()
			pc, err := trk.playback(id)
			if err == nil && once && time.Since(start) > wait {
				err = i18n.Errorf("tui.err.ctl_too_late")
			}
			if err != nil {
				return tuiStateMsg{err: err, fromCtl: true, gen: gen}
			}
			fctx, cancel := provider.WithoutWait(ctx), context.CancelFunc(func() {})
			if once {
				fctx, cancel = context.WithDeadline(fctx, start.Add(wait))
			}
			defer cancel()
			if err := f(pc, fctx); err != nil {
				if errors.Is(err, provider.ErrOpenedNotPlaying) {
					trk.settleNow()
					return tuiStateMsg{err: err, fromCtl: true, gen: gen}
				}
				if fctx.Err() != nil && ctx.Err() == nil {
					err = i18n.Errorf("tui.err.ctl_timeout")
				}
				trk.expireExcept("")
				return tuiStateMsg{err: err, fromCtl: true, gen: gen}
			}
			trk.settleNow()
			return round()
		})
	}
}

func (m *tuiModel) afterExec(args []string) {
	c, _, err := newRootCmd().Find(args)
	if err != nil {
		return
	}
	path := c.CommandPath()
	if webNowInvalidatedBy(path) {
		m.trk.dropNow(webNowResetsShown(path, args))
		if path == "capy config set" && tuiStoreAllowed() {
			if cfg, err := config.Load(); err == nil && cfg.Theme != "" {
				if ui.ApplyTheme(cfg.Theme) {
					m.setTheme(ui.DefaultTheme)
					m.themeName = cfg.Theme
				}
			}
		}
	}
	if webNowSettledByRun(path, args) {
		m.trk.settleNow()
	}
	if path == "capy history clear" {
		m.hist, m.histAt, m.histDraft = nil, 0, ""
	}
}

func (m tuiModel) newChain() tuiModel {
	m.gen++
	m.stalled = false
	return m
}

func (m *tuiModel) saveOptimistic() {
	if m.st != nil && m.optBase == nil {
		m.optBase = clonePlaybackState(m.st)
		m.optAt = m.stAt
	}
}

func (m tuiModel) currentProgressMS() int {
	if m.st == nil || m.st.Track == nil {
		return 0
	}
	prog := m.st.ProgressMS
	if m.st.Playing && !m.stAt.IsZero() {
		if now := webNowClock(); now.After(m.stAt) {
			prog += int(now.Sub(m.stAt) / time.Millisecond)
		}
	}
	if prog < 0 {
		prog = 0
	}
	if d := m.st.Track.DurationMS; d > 0 && prog > d {
		prog = d
	}
	return prog
}

func (m tuiModel) sleepBadge() string {
	if m.sleep == nil {
		return ""
	}
	return tuiSleepBadge(m.sleep.Snapshot())
}

// freeze:開場結束。水豚定格印進捲動區,之後 View 只剩底部四行、frame ticker 停掉。
// 嚴守單向不變式(印過就不再常駐,避免捲動區與 View 各一隻)。
func (m tuiModel) freeze() (tuiModel, tea.Cmd) { return m.freezeNoting("") }

func (m tuiModel) freezeNoting(note string) (tuiModel, tea.Cmd) {
	if m.frozen {
		return m, nil
	}
	m.frozen, m.menuHigh = true, 0
	lines := capybaraStill()
	if !m.bannerFits() {
		lines = []string{capyOneLine}
	}
	banner := m.theme.Accented(strings.Join(lines, "\n")) + "\n\n  " + m.theme.Mutedly(capyTaglineWithTimer(m.provID, m.sleepBadge()))
	if note != "" {
		banner += "\n  " + m.theme.Mutedly(note)
	}
	return m, tuiPrintln(banner)
}

func (m tuiModel) execResult(msg tuiExecMsg) tea.Cmd {
	if msg.err == nil {
		return nil
	}
	head := "capy " + strings.Join(msg.args, " ")
	var ee *exec.ExitError
	if !errors.As(msg.err, &ee) {
		return m.println(tuiSeg{"✗ " + head + ":" + msg.err.Error(), m.theme.Mutedly})
	}
	mark := "✗ "
	if c := ee.ExitCode(); c == 2 || c == 3 || c == 130 {
		mark = "· "
	}
	return m.println(tuiSeg{mark + i18n.T("tui.exit_code", "cmd", head, "code", ee.ExitCode()), m.theme.Mutedly})
}

func (m tuiModel) viewWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

var tuiPrintln = func(s string) tea.Cmd { return tea.Println(s) }

func (m tuiModel) println(segs ...tuiSeg) tea.Cmd {
	return tuiPrintln(tuiJoin(m.viewWidth()-1, segs...))
}

func (m tuiModel) printBlock(s string, style func(string) string) tea.Cmd {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = tuiJoin(m.viewWidth()-1, tuiSeg{ln, style})
	}
	return tuiPrintln(strings.Join(lines, "\n"))
}

func (m tuiModel) echoLine(raw string) tea.Cmd {
	return tuiPrintln("\n" + tuiJoin(m.viewWidth()-1, tuiSeg{"> ", m.theme.Accented}, tuiSeg{raw, m.theme.Strong}))
}

func (m tuiModel) echoAndNote(raw, note string) tea.Cmd {
	w := m.viewWidth() - 1
	echo := "\n" + tuiJoin(w, tuiSeg{"> ", m.theme.Accented}, tuiSeg{raw, m.theme.Strong})
	msg := tuiJoin(w, tuiSeg{note, m.theme.Mutedly})
	return tuiPrintln(echo + "\n" + msg)
}

func (m *tuiModel) printErr(err error) tea.Cmd {
	if err == nil || err.Error() == m.lastErr {
		return nil
	}
	m.lastErr = err.Error()
	m.menuHigh = 0
	return m.println(tuiSeg{"⚠ " + err.Error(), m.theme.Mutedly})
}

var tuiExecProcess = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd { return tea.ExecProcess(c, fn) }

func (m tuiModel) withProviderFlag(args []string) []string {
	c, rest, err := newRootCmd().Find(args)
	if err != nil || c.Flags().Lookup(flagProvider) == nil {
		return args
	}
	id := m.provFlag
	if id == "" {
		path := c.CommandPath()
		follows := (webNowSettledBy(path) || path == "capy now" || path == "capy devices") && !(path == "capy play" && len(rest) > 0)
		if path == "capy wiki" {
			_ = c.Flags().Parse(rest)
			follows = len(c.Flags().Args()) == 0 && !c.Flags().Changed("title")
		}
		if path == "capy pl add" && slices.Contains(rest, "--now") && isProviderID(m.provID) {
			follows = true
		}
		typed := slices.ContainsFunc(args, func(a string) bool { return a == "--"+flagProvider || strings.HasPrefix(a, "--"+flagProvider+"=") })
		if !follows || typed {
			return args
		}
		id = m.provID
	}
	if id == "" {
		return args
	}
	return append(slices.Clone(args), "--"+flagProvider, id)
}

func (m tuiModel) runArgs(args []string) tea.Cmd {
	full := m.withProviderFlag(args)
	c := exec.CommandContext(m.ctx, m.exe, full...)
	return tuiExecProcess(c, func(err error) tea.Msg { return tuiExecMsg{args: args, err: err} })
}

func (m tuiModel) startSubcommand(raw string, args []string) (tuiModel, tea.Cmd) {
	if m.exe == "" {
		return m, m.println(tuiSeg{"✗ " + i18n.T("tui.err.no_executable"), m.theme.Mutedly})
	}
	m.running, m.menuHigh = true, 0
	return m, tea.Sequence(m.echoLine(raw), m.runArgs(args))
}

func tuiWikiTrackKey(prov string, tr *provider.Track) string {
	if tr == nil || strings.TrimSpace(tr.Title) == "" {
		return ""
	}
	return prov + "\x00" + tr.Title + "\x00" + strings.Join(tr.Artists, ", ")
}

func lookupWikiAsyncCmd(key string, song ai.Song) tea.Cmd {
	return func() tea.Msg {
		if !tuiStoreAllowed() {
			return tuiWikiCachedMsg{key: key}
		}
		cfg, err := config.Load()
		if err != nil {
			return tuiWikiCachedMsg{key: key}
		}
		v, _ := lookupCachedWiki(*cfg, song)
		return tuiWikiCachedMsg{key: key, view: v}
	}
}

func (m tuiModel) printCachedWiki(v *cachedWikiView) tea.Cmd {
	w := max(2, m.viewWidth()-1)
	var out []string
	out = append(out, "")
	out = append(out, tuiJoin(w, tuiSeg{"> ", m.theme.Accented}, tuiSeg{"wiki", m.theme.Strong}))
	out = append(out, tuiJoin(w, tuiSeg{i18n.T("wiki.cached", "date", v.CreatedAt.Format("2006-01-02")), m.theme.Mutedly}))
	for _, rawLine := range strings.Split(strings.TrimRight(v.Body, "\n"), "\n") {
		heading := strings.HasPrefix(rawLine, "## ")
		for _, ln := range tuiWrap(rawLine, w) {
			if heading {
				out = append(out, m.theme.Strong(ln))
			} else {
				out = append(out, ln)
			}
		}
	}
	out = append(out, "")
	for _, footer := range []string{
		i18n.T("wiki.mv", "url", v.MVURL),
		i18n.T("wiki.source", "model", v.Model, "date", v.CreatedAt.Format("2006-01-02")),
		v.Disclaimer,
	} {
		for _, ln := range tuiWrap(footer, w) {
			out = append(out, m.theme.Mutedly(ln))
		}
	}
	return tuiPrintln(strings.Join(out, "\n"))
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		wasAlive := m.alive()
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(8, msg.Width-4))
		m.input.Placeholder = tuiPlaceholder(msg.Width)
		if wasAlive && !m.alive() {
			return m.freezeNoting(i18n.T("tui.frozen_note"))
		}
		return m, nil
	case tuiFrameMsg:
		if m.frozen {
			return m, nil
		}
		if !m.bannerFits() {
			return m.freeze()
		}
		m.frame++
		return m, m.frameTick()
	case tuiFreezeMsg:
		if m.alive() {
			return m, nil
		}
		return m.freeze()
	case tuiSecondMsg:
		if msg.seq != m.clockSeq || !m.needSecondTick() {
			m.clockRunning = false
			return m, nil
		}
		seq := m.clockSeq
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return tuiSecondMsg{seq: seq} })
	case tuiPollMsg:
		if msg.gen != m.gen || m.stalled {
			return m, nil
		}
		return m, m.poll()
	case tuiStateMsg:
		return m.applyState(msg)
	case tuiWikiCachedMsg:
		if msg.key != "" && msg.key == m.wikiKey {
			m.wikiView = msg.view
		}
		return m, nil
	case tuiDevicesMsg:
		return m.applyDevicesMsg(msg)
	case tuiExecMsg:
		m.running = false
		m.afterExec(msg.args)
		return m, m.execResult(msg)
	case tea.KeyPressMsg:
		if m.cmds == nil {
			m.cmds = tuiCommands()
		}
		next, cmd := m.onKey(msg)
		next.menuHigh = max(next.menuHigh, next.overlayRows())
		return next, cmd
	}
	return m, nil
}

func (m tuiModel) applyState(msg tuiStateMsg) (tea.Model, tea.Cmd) {
	stale := msg.gen != m.gen
	tick := func() tea.Cmd {
		if stale || m.stalled {
			return nil
		}
		return m.pollTick()
	}
	if msg.slow {
		if !stale {
			m.errShort = i18n.T("tui.status.waiting")
		}
		pending := msg.pending
		return m, func() tea.Msg { return (<-pending).repanic() }
	}
	if msg.fromCtl && msg.err != nil {
		if errors.Is(msg.err, provider.ErrOpenedNotPlaying) {
			label := ""
			var oe *provider.OpenedError
			if errors.As(msg.err, &oe) {
				label = oe.Label
			}
			m.menuHigh = 0
			pr := m.println(tuiSeg{"· " + i18n.T("play.opened_not_playing", "label", label), m.theme.Mutedly})
			return m, tea.Batch(pr, tick())
		}
		if !stale && m.optBase != nil {
			m.st = m.optBase
			m.stAt = m.optAt
			m.optBase = nil
		}
		pr := m.printErr(msg.err)
		m.errShort = i18n.T("tui.status.ctl_failed")
		return m, tea.Batch(pr, tick())
	}
	if stale {
		return m, nil
	}
	if msg.dropped {
		return m, tick()
	}
	if msg.provider != "" && msg.provider != m.provID {
		m.provID, m.st, m.stAt, m.optBase, m.fails = msg.provider, nil, time.Time{}, nil, 0
		m.wikiKey, m.wikiView = "", nil
	}
	var be nowBuildErr
	m.pcErr = nil
	if errors.As(msg.err, &be) {
		m.st, m.stAt, m.optBase, m.pcErr, m.errShort, m.fails = nil, time.Time{}, nil, be.error, "", 0
		m.wikiKey, m.wikiView = "", nil
		return m, tick()
	}
	var rl *provider.RateLimitError
	switch {
	case errors.Is(msg.err, provider.ErrPlayerNotRunning):
		m.st, m.stAt, m.optBase, m.errShort, m.fails, m.lastErr = nil, time.Time{}, nil, i18n.T("tui.status.player_not_running"), 0, msg.err.Error()
		m.wikiKey, m.wikiView = "", nil
		m.observeSleepAsync()
		return m, tick()
	case errors.As(msg.err, &rl):
		m.errShort, m.fails, m.lastErr = i18n.T("tui.status.rate_limited", "time", msg.retryAt.Local().Format("15:04:05")), 0, msg.err.Error()
		return m, tick()
	}
	if msg.err != nil {
		if !msg.cached {
			m.fails++
		}
		m.errShort = i18n.T("tui.status.state_unavailable")
		pr := m.printErr(msg.err)
		if m.fails >= tuiMaxFails {
			m.stalled = true
			return m, pr
		}
		return m, tea.Batch(pr, tick())
	}

	m.st, m.stAt, m.optBase, m.errShort, m.fails, m.lastErr = msg.st, webNowClock(), nil, "", 0, ""
	m.observeSleepAsync()

	var wikiCmd, secCmd tea.Cmd
	if !m.stalled {
		if m.st == nil || m.st.Track == nil {
			m.wikiKey, m.wikiView = "", nil
		} else if wk := tuiWikiTrackKey(m.provID, m.st.Track); wk != m.wikiKey {
			m.wikiKey = wk
			m.wikiView = nil
			wikiCmd = lookupWikiAsyncCmd(wk, songOf(m.st.Track))
		}
		m, secCmd = m.ensureSecondTick()
	} else if m.st == nil || m.st.Track == nil {
		m.wikiKey, m.wikiView = "", nil
	}
	return m, tea.Batch(tick(), wikiCmd, secCmd)
}

func (m tuiModel) observeSleepAsync() {
	if m.sleep == nil {
		return
	}
	if m.sleep.Snapshot().Mode == SleepOff {
		m.sleep.Observe(m.provID, m.st)
		return
	}
	prov := m.provID
	st := clonePlaybackState(m.st)
	sleep := m.sleep
	if testing.Testing() {
		sleep.Observe(prov, st)
	} else {
		go sleep.Observe(prov, st)
	}
}

// menu:輸入行以 / 開頭時,回過濾後的清單與 true(包含第二階段參數補全)。
func (m tuiModel) menu() ([]tuiCmdItem, bool) {
	q, ok := tuiMenuQuery(m.input.Value())
	if !ok {
		return nil, false
	}
	if argItems, ok := tuiArgMenu(q, m.playTargetProvider()); ok {
		return argItems, true
	}
	if tuiIsCommand(m.cmds, q) {
		return nil, false
	}
	return tuiMenuFilter(m.cmds, q), true
}

func (m tuiModel) menuRows() int {
	items, ok := m.menu()
	if !ok {
		return 0
	}
	return max(1, min(tuiMenuRows, len(items)))
}

func (m tuiModel) overlayRows() int {
	if m.drawerOpen() {
		return len(m.drawerView(m.viewWidth() - 1))
	}
	return m.menuRows()
}

func (m tuiModel) recall(d int) tuiModel {
	if len(m.hist) == 0 {
		return m
	}
	if m.histAt == len(m.hist) {
		m.histDraft = m.input.Value()
	}
	at := min(len(m.hist), max(0, m.histAt+d))
	m.histAt = at
	if at == len(m.hist) {
		m.input.SetValue(m.histDraft)
	} else {
		m.input.SetValue(m.hist[at])
	}
	m.input.CursorEnd()
	return m
}

func (m tuiModel) interruptedByKey() bool { return m.ctrlC }

// tryInProcessCmd:純播放控制命令與 /sleep 直接在 TUI 行程內執行,常駐水豚不閃退、不重讀 keychain。
// 帶任何旗標(--provider / --help 等)時交回子行程處理以完整保留 cobra 旗標語意。
func (m tuiModel) tryInProcessCmd(raw string, args []string) (tuiModel, tea.Cmd, bool) {
	if len(args) == 0 || slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "-") }) {
		return m, nil, false
	}
	switch args[0] {
	case "pause":
		if len(args) != 1 {
			return m, nil, false
		}
		m = m.newChain()
		m.menuHigh = 0
		if m.st != nil {
			m.saveOptimistic()
			cp := clonePlaybackState(m.st)
			cp.Playing = false
			m.st = cp
		}
		return m, tea.Sequence(m.echoLine(raw), m.control(provider.PlaybackController.Pause, false)), true

	case "play":
		if len(args) != 1 {
			return m, nil, false
		}
		m = m.newChain()
		m.menuHigh = 0
		if m.st != nil {
			m.saveOptimistic()
			cp := clonePlaybackState(m.st)
			cp.Playing = true
			m.st = cp
			if !m.stAt.IsZero() {
				m.stAt = webNowClock()
			}
		}
		ctl := m.control(func(pc provider.PlaybackController, ctx context.Context) error {
			return pc.Play(ctx, provider.PlayRequest{})
		}, false)
		return m, tea.Sequence(m.echoLine(raw), ctl), true

	case "next":
		if len(args) != 1 {
			return m, nil, false
		}
		m = m.newChain()
		m.menuHigh = 0
		return m, tea.Sequence(m.echoLine(raw), m.control(provider.PlaybackController.Next, true)), true

	case "prev":
		if len(args) != 1 {
			return m, nil, false
		}
		m = m.newChain()
		m.menuHigh = 0
		return m, tea.Sequence(m.echoLine(raw), m.control(provider.PlaybackController.Prev, true)), true

	case "seek":
		if len(args) != 2 {
			return m, nil, false
		}
		pos, err := parseSeekPos(args[1])
		m.menuHigh = 0
		if err != nil {
			return m, m.echoAndNote(raw, "✗ "+err.Error()), true
		}
		m = m.newChain()
		if m.st != nil && m.st.Track != nil {
			m.saveOptimistic()
			cp := clonePlaybackState(m.st)
			cp.ProgressMS = pos
			m.st = cp
			if !m.stAt.IsZero() {
				m.stAt = webNowClock()
			}
		}
		return m, tea.Sequence(m.echoLine(raw), m.control(func(pc provider.PlaybackController, ctx context.Context) error {
			return pc.Seek(ctx, pos)
		}, false)), true

	case "vol":
		if len(args) != 2 {
			return m, nil, false
		}
		pct, err := parseVolPct(args[1])
		m.menuHigh = 0
		if err != nil {
			return m, m.echoAndNote(raw, "✗ "+err.Error()), true
		}
		m = m.newChain()
		if m.st != nil && m.st.Device.VolumeKnown {
			m.saveOptimistic()
			cp := clonePlaybackState(m.st)
			cp.Device.VolumePct = pct
			m.st = cp
		}
		return m, tea.Sequence(m.echoLine(raw), m.control(func(pc provider.PlaybackController, ctx context.Context) error {
			return pc.SetVolume(ctx, pct)
		}, false)), true

	case "sleep":
		return m.execSleepCommand(raw, args)
	}
	return m, nil, false
}

func (m tuiModel) execSleepCommand(raw string, args []string) (tuiModel, tea.Cmd, bool) {
	m.menuHigh = 0
	if m.sleep == nil {
		return m, m.echoAndNote(raw, "* "+i18n.T("tui.sleep.off")), true
	}
	if len(args) != 2 {
		val := strings.Join(args[1:], " ")
		return m, m.echoAndNote(raw, "✗ "+i18n.T("tui.sleep.bad_arg", "value", strconv.Quote(val))), true
	}
	arg := strings.ToLower(strings.TrimSpace(args[1]))
	prov := m.playTargetProvider()
	switch arg {
	case "off", "0", "cancel":
		offCmd := m.turnSleepOffCmd()
		return m, tea.Batch(m.echoAndNote(raw, "* "+i18n.T("tui.sleep.off")), offCmd), true
	case "track", "eot", "end":
		m.sleep.SetEndOfTrack(prov)
		var secCmd tea.Cmd
		m, secCmd = m.ensureSecondTick()
		return m, tea.Batch(m.echoAndNote(raw, "* "+i18n.T("tui.sleep.track_set")), secCmd), true
	case "yuzu", "pomodoro":
		m.sleep.SetPomodoro(prov, 0, 0)
		var secCmd tea.Cmd
		m, secCmd = m.ensureSecondTick()
		return m, tea.Batch(m.echoAndNote(raw, "* "+i18n.T("tui.sleep.yuzu_set")), secCmd), true
	default:
		mins, err := strconv.Atoi(strings.TrimSuffix(arg, "m"))
		if err != nil || mins <= 0 {
			return m, m.echoAndNote(raw, "✗ "+i18n.T("tui.sleep.bad_arg", "value", strconv.Quote(args[1]))), true
		}
		m.sleep.SetCountdownMinutes(prov, mins)
		var secCmd tea.Cmd
		m, secCmd = m.ensureSecondTick()
		return m, tea.Batch(m.echoAndNote(raw, "* "+i18n.T("tui.sleep.countdown_set", "count", mins)), secCmd), true
	}
}

func (m tuiModel) turnSleepOffCmd() tea.Cmd {
	sleep := m.sleep
	if sleep == nil {
		return nil
	}
	if testing.Testing() {
		sleep.Off()
		return nil
	}
	return func() tea.Msg {
		sleep.Off()
		return nil
	}
}

func (m tuiModel) cycleSleepTimer() (tuiModel, tea.Cmd) {
	if m.sleep == nil {
		return m, nil
	}
	prov := m.playTargetProvider()
	snap := m.sleep.Snapshot()
	switch snap.Mode {
	case SleepOff:
		m.sleep.SetEndOfTrack(prov)
	case SleepEndOfTrack:
		m.sleep.SetCountdownMinutes(prov, 15)
	case SleepCountdown:
		switch {
		case snap.TotalSec <= 15*60:
			m.sleep.SetCountdownMinutes(prov, 30)
		case snap.TotalSec <= 30*60:
			m.sleep.SetCountdownMinutes(prov, 60)
		default:
			m.sleep.SetPomodoro(prov, 0, 0)
		}
	case SleepYuzuPomodoro:
		return m, m.turnSleepOffCmd()
	}
	return m.ensureSecondTick()
}

func (m tuiModel) cachedModeController() (provider.PlaybackModeController, bool) {
	if m.trk == nil {
		return nil, false
	}
	m.trk.nowMu.Lock()
	pc, ok := m.trk.now[m.provID]
	m.trk.nowMu.Unlock()
	if !ok || pc == nil {
		return nil, false
	}
	mc, ok := pc.(provider.PlaybackModeController)
	return mc, ok
}

func (m tuiModel) reconnectPoll() (tuiModel, tea.Cmd) {
	m = m.newChain()
	m.errShort, m.fails, m.lastErr = "", 0, ""
	m.trk.expireExcept("")
	return m, m.poll()
}

func (m tuiModel) onKey(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	if !m.frozen && !m.alive() {
		var freeze tea.Cmd
		m, freeze = m.freeze()
		next, cmd := m.onKey(msg)
		return next, tea.Sequence(freeze, cmd)
	}
	if m.drawerOpen() {
		return m.onDrawerKey(msg)
	}
	if m.typing {
		items, menuOpen := m.menu()
		switch msg.String() {
		case "up", "down":
			d := -1
			if msg.String() == "down" {
				d = 1
			}
			if menuOpen {
				if len(items) > 0 {
					m.menuSel = min(len(items)-1, max(0, m.menuSel+d))
				}
				return m, nil
			}
			return m.recall(d), nil
		case "ctrl+c":
			m.ctrlC = true
			return m, tea.Quit
		case "esc":
			if m.input.Value() != "" {
				m.input.SetValue("")
				m.menuSel = 0
				return m, nil
			}
			m.typing, m.input = false, blurred(m.input)
			return m, nil
		case "enter", "tab":
			if menuOpen && len(items) > 0 {
				m.input.SetValue(items[min(m.menuSel, len(items)-1)].completion())
				m.input.CursorEnd()
				m.menuSel = 0
				return m, nil
			}
			if msg.String() == "tab" {
				if comp, ok := tuiTabCompleteArg(m.input.Value(), m.playTargetProvider()); ok {
					m.input.SetValue(comp)
					m.input.CursorEnd()
				}
				return m, nil
			}
			raw := m.input.Value()
			if q, ok := tuiMenuQuery(raw); ok {
				raw = strings.TrimSpace(q)
			}
			args := splitArgs(raw)
			m.typing, m.input = false, blurred(m.input)
			m.input.SetValue("")
			if len(args) == 0 {
				return m, nil
			}
			m.recordHistory(raw, args)
			if next, cmd, handled := m.tryInProcessCmd(raw, args); handled {
				return next, cmd
			}
			return m.startSubcommand(raw, args)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if items, ok := m.menu(); ok {
			m.menuSel = min(m.menuSel, max(0, len(items)-1))
		}
		return m, cmd
	}

	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "ctrl+c":
		m.ctrlC = true
		return m, tea.Quit
	case "/":
		m.typing, m.menuSel = true, 0
		m.input.SetValue("/")
		m.input.CursorEnd()
		m.input.Focus()
		return m, textinput.Blink
	case ":":
		m.typing = true
		m.input.Focus()
		return m, textinput.Blink
	case "up", "down":
		if len(m.hist) == 0 {
			return m, nil
		}
		d := -1
		if msg.String() == "down" {
			d = 1
		}
		m.typing = true
		m.input.Focus()
		return m.recall(d), textinput.Blink
	case "?":
		m.menuHigh = 0
		return m, m.printBlock(tuiKeymap(), m.theme.Mutedly)
	case "w":
		view := m.wikiView
		if view == nil && m.st != nil && m.st.Track != nil && tuiStoreAllowed() {
			if cfg, err := config.Load(); err == nil {
				if v, ok := lookupCachedWiki(*cfg, songOf(m.st.Track)); ok {
					view = v
					m.wikiView = v
					m.wikiKey = tuiWikiTrackKey(m.provID, m.st.Track)
				}
			}
		}
		if view != nil {
			m.menuHigh = 0
			return m, m.printCachedWiki(view)
		}
		return m.startSubcommand("wiki", []string{"wiki"})
	case "W":
		return m.startSubcommand("wiki --refresh", []string{"wiki", "--refresh"})
	case "o":
		return m.openQuickDeck(m.drawer.tab)
	case "d":
		return m.openQuickDeck(tuiDeckTabDevices)
	case "f":
		return m.openCollectDrawer()
	case "F":
		return m.collectToLast()
	case "t":
		return m.cycleSleepTimer()
	case "C":
		return m.openThemeDrawer()
	case "R":
		return m.reconnectPoll()
	}

	if msg.String() == "r" {
		if m.pcErr != nil || m.stalled || m.fails > 0 || m.errShort != "" || m.st == nil {
			return m.reconnectPoll()
		}
		if _, ok := m.cachedModeController(); !ok {
			return m.reconnectPoll()
		}
		var nextMode provider.RepeatMode
		switch m.st.Repeat {
		case provider.RepeatContext:
			nextMode = provider.RepeatTrack
		case provider.RepeatTrack:
			nextMode = provider.RepeatOff
		default:
			nextMode = provider.RepeatContext
		}
		m = m.newChain()
		m.saveOptimistic()
		cp := clonePlaybackState(m.st)
		cp.Repeat = nextMode
		m.st = cp
		return m, m.control(func(pc provider.PlaybackController, ctx context.Context) error {
			mc, ok := pc.(provider.PlaybackModeController)
			if !ok {
				return provider.ErrNotSupported
			}
			return mc.SetRepeat(ctx, nextMode)
		}, false)
	}

	if m.pcErr != nil {
		return m, nil
	}
	switch msg.String() {
	case "space":
		m = m.newChain()
		if m.st != nil && m.st.Playing {
			return m, m.control(provider.PlaybackController.Pause, false)
		}
		return m, m.control(func(pc provider.PlaybackController, ctx context.Context) error {
			return pc.Play(ctx, provider.PlayRequest{})
		}, false)
	case "n":
		m = m.newChain()
		return m, m.control(provider.PlaybackController.Next, true)
	case "p":
		m = m.newChain()
		return m, m.control(provider.PlaybackController.Prev, true)
	case "s":
		if m.st == nil {
			return m, nil
		}
		if _, ok := m.cachedModeController(); !ok {
			return m, nil
		}
		want := !m.st.Shuffle
		m = m.newChain()
		m.saveOptimistic()
		cp := clonePlaybackState(m.st)
		cp.Shuffle = want
		m.st = cp
		return m, m.control(func(pc provider.PlaybackController, ctx context.Context) error {
			mc, ok := pc.(provider.PlaybackModeController)
			if !ok {
				return provider.ErrNotSupported
			}
			return mc.SetShuffle(ctx, want)
		}, false)
	case "left", "right":
		pos, ok := m.seekTarget(msg.String() == "right")
		if !ok {
			return m, nil
		}
		m = m.newChain()
		m.saveOptimistic()
		cp := clonePlaybackState(m.st)
		cp.ProgressMS = pos
		m.st = cp
		if !m.stAt.IsZero() {
			m.stAt = webNowClock()
		}
		return m, m.control(func(pc provider.PlaybackController, ctx context.Context) error { return pc.Seek(ctx, pos) }, false)
	case "+", "=", "-":
		pct, ok := m.volTarget(msg.String() != "-")
		if !ok {
			return m, nil
		}
		m = m.newChain()
		m.saveOptimistic()
		cp := clonePlaybackState(m.st)
		cp.Device.VolumePct = pct
		m.st = cp
		return m, m.control(func(pc provider.PlaybackController, ctx context.Context) error { return pc.SetVolume(ctx, pct) }, false)
	}
	return m, nil
}

func (m tuiModel) seekTarget(forward bool) (int, bool) {
	if m.st == nil || m.st.Track == nil {
		return 0, false
	}
	pos := m.currentProgressMS() + tuiSeekStep
	if !forward {
		pos = m.currentProgressMS() - tuiSeekStep
	}
	if pos < 0 {
		pos = 0
	}
	if d := m.st.Track.DurationMS; d > 0 && pos > d {
		pos = d
	}
	return pos, true
}

func (m tuiModel) volTarget(up bool) (int, bool) {
	if m.st == nil || !m.st.Device.VolumeKnown {
		return 0, false
	}
	pct := m.st.Device.VolumePct + tuiVolStep
	if !up {
		pct = m.st.Device.VolumePct - tuiVolStep
	}
	return min(100, max(0, pct)), true
}

func blurred(in textinput.Model) textinput.Model {
	in.Blur()
	return in
}

func splitArgs(line string) []string {
	var out []string
	var cur strings.Builder
	inQuote, started := false, false
	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range line {
		switch {
		case r == '"':
			inQuote, started = !inQuote, true
		case (r == ' ' || r == '\t') && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	flush()
	return out
}

func (m tuiModel) bannerFits() bool {
	return m.viewWidth() >= max(tuiMinWidth, capybaraWidth()) && (m.height == 0 || m.height > capyBlockRows)
}

func (m tuiModel) alive() bool {
	return !m.frozen && m.motion && m.height >= tuiAliveMinHeight && m.bannerFits()
}

// progressLine:底部 4 行的第 1 行。無可計長度的播放曲目時畫純 ASCII 連字號分隔線;
// 有曲目時渲染純 ASCII 雙色進度軸(已播 "=" + 游標 "o" 用 Accent,未播 "-" 用 Muted),嚴格夾在 w 欄。
func (m tuiModel) progressLine(w int) string {
	n := max(1, w)
	if m.st == nil || m.st.Track == nil || m.st.Track.DurationMS <= 0 {
		return m.theme.Mutedly(strings.Repeat("-", n))
	}
	prog := m.currentProgressMS()
	dur := m.st.Track.DurationMS
	pct := min(1.0, max(0.0, float64(prog)/float64(dur)))
	pos := 0
	if n > 1 {
		pos = min(n-1, max(0, int(pct*float64(n-1))))
	}
	played := m.theme.Accented(strings.Repeat("=", pos) + "o")
	rem := n - 1 - pos
	if rem <= 0 {
		return played
	}
	return played + m.theme.Mutedly(strings.Repeat("-", rem))
}

func (m tuiModel) View() tea.View {
	w := m.viewWidth()
	alive := m.alive()
	if !m.frozen && !alive {
		return tea.NewView(m.intro())
	}
	if m.running {
		return tea.NewView(" ")
	}
	var lines []string
	if m.drawerOpen() {
		lines = append(lines, m.drawerView(w-1)...)
	} else if items, ok := m.menu(); ok {
		lines = append(lines, tuiMenuView(items, m.menuSel, w-1, m.theme)...)
	}
	if pad := m.menuHigh - len(lines); pad > 0 && !alive {
		lines = append(make([]string, pad), lines...)
	}
	if alive {
		block := strings.Split(m.intro(), "\n")
		if len(lines) > 0 {
			block = append(make([]string, max(0, capyBlockRows-len(lines))), lines...)
		}
		lines = block
	}
	lines = append(lines,
		m.progressLine(w-1),
		m.statusLine(w-1),
		m.input.View(),
		tuiJoin(w-1, tuiSeg{"  " + m.hints(), m.theme.Mutedly}),
	)
	v := tea.NewView(strings.Join(lines, "\n"))
	if c := m.input.Cursor(); c != nil {
		c.Y += len(lines) - 2
		v.Cursor = c
	}
	return v
}

func (m tuiModel) intro() string {
	var lines []string
	playing := m.motion && m.alive() && m.st != nil && m.st.Playing
	if m.bannerFits() {
		for _, l := range capybaraLiveFrame(m.frame, playing) {
			lines = append(lines, m.theme.Accented(l))
		}
	} else {
		lines = append(lines, m.theme.Accented(capyOneLine))
	}
	tagline := capyTaglineWithTimer(m.provID, m.sleepBadge())
	return strings.Join(append(lines, "", tuiJoin(m.viewWidth()-1, tuiSeg{"  " + tagline, m.theme.Mutedly})), "\n")
}

// statusLine:底部第 2 行。先依優先級扣留右側高價值短資訊(錯誤/限流短訊 > 進度時間 > 平台與模式標記 > 音量 > 裝置),
// 再將剩餘欄數交給曲名與歌手裁切,確保長歌名絕不會把限流提示與時間擠出畫面外。
func (m tuiModel) statusLine(w int) string {
	t := m.theme
	if m.pcErr != nil {
		return tuiJoin(w, tuiSeg{"  " + i18n.T("tui.status.no_playback", "reason", m.pcErr.Error()), t.Mutedly})
	}
	short := m.errShort
	if m.stalled {
		short = i18n.T("tui.status.stalled")
	}
	sb := ""
	if m.frozen {
		sb = m.sleepBadge()
	}
	if m.st == nil || m.st.Track == nil {
		if short == "" {
			short = i18n.T("tui.status.nothing_playing")
		}
		if sb != "" {
			short += " · " + sb
		}
		return tuiJoin(w, tuiSeg{"  " + short, t.Mutedly})
	}

	st := m.st
	mark := "⏸"
	if st.Playing {
		mark = "▶"
	}
	prefix := "  " + mark + " "
	pw := tuiWidth(prefix)
	if w <= pw {
		return tuiJoin(w, tuiSeg{prefix, t.Accented})
	}
	avail := w - pw
	minSongW := min(16, max(6, avail/3))
	if avail <= 8 {
		minSongW = max(1, avail/2)
	}
	maxTailW := max(0, avail-minSongW)

	type tailPiece struct {
		order int
		prio  int
		text  string
		keep  bool
	}
	var pieces []tailPiece

	var badges []string
	if m.provID != "" {
		badges = append(badges, "["+m.provID+"]")
	}
	if st.Shuffle {
		badges = append(badges, "[S]")
	}
	switch st.Repeat {
	case provider.RepeatContext:
		badges = append(badges, "[R]")
	case provider.RepeatTrack:
		badges = append(badges, "[R1]")
	}
	if m.wikiView != nil {
		badges = append(badges, "[wiki]")
	}
	if sb != "" {
		badges = append(badges, "["+sb+"]")
	}
	if len(badges) > 0 {
		pieces = append(pieces, tailPiece{order: 0, prio: 3, text: " · " + strings.Join(badges, " ")})
	}

	pieces = append(pieces, tailPiece{
		order: 1,
		prio:  2,
		text:  " · " + ui.FormatDuration(m.currentProgressMS()) + " / " + ui.FormatDuration(st.Track.DurationMS),
	})
	if st.Device.Name != "" {
		pieces = append(pieces, tailPiece{order: 2, prio: 5, text: " · " + st.Device.Name})
	}
	if st.Device.VolumeKnown {
		pieces = append(pieces, tailPiece{order: 3, prio: 4, text: " · " + i18n.T("tui.status.volume", "pct", st.Device.VolumePct)})
	}
	if short != "" {
		pieces = append(pieces, tailPiece{order: 4, prio: 1, text: " · " + short})
	}

	byPrio := make([]int, len(pieces))
	for i := range pieces {
		byPrio[i] = i
	}
	slices.SortFunc(byPrio, func(a, b int) int { return pieces[a].prio - pieces[b].prio })

	usedTail := 0
	for _, idx := range byPrio {
		tw := tuiWidth(pieces[idx].text)
		if usedTail+tw <= maxTailW {
			pieces[idx].keep = true
			usedTail += tw
		} else if pieces[idx].prio == 1 && usedTail == 0 && maxTailW >= 4 {
			pieces[idx].text = tuiClip(pieces[idx].text, maxTailW)
			pieces[idx].keep = true
			usedTail += tuiWidth(pieces[idx].text)
		}
	}

	var tailBuilder strings.Builder
	for i := range pieces {
		if pieces[i].keep {
			tailBuilder.WriteString(pieces[i].text)
		}
	}
	tail := tailBuilder.String()

	songBudget := max(0, avail-tuiWidth(tail))
	titleClip := tuiClip(st.Track.Title, songBudget)
	var line strings.Builder
	line.WriteString(t.Accented(prefix))
	if titleClip != "" {
		line.WriteString(t.Strong(titleClip))
	}
	remSong := songBudget - tuiWidth(titleClip)
	if tuiWidth(st.Track.Title) <= songBudget && len(st.Track.Artists) > 0 && remSong >= 6 {
		artistClip := tuiClip(" - "+strings.Join(st.Track.Artists, ", "), remSong)
		if artistClip != "" {
			line.WriteString(t.Mutedly(artistClip))
		}
	}
	if tail != "" {
		line.WriteString(t.Mutedly(tail))
	}
	return line.String()
}

func tuiPlaceholder(w int) string {
	for _, s := range []string{i18n.T("tui.placeholder.long"), i18n.T("tui.placeholder.short"), "pl list"} {
		if tuiWidth(s)+4 <= w {
			return s
		}
	}
	return ""
}

func tuiKeymap() string { return i18n.T("tui.keymap") }

func (m tuiModel) hints() string {
	if m.drawerOpen() {
		if m.drawer.mode == tuiDrawerDeck {
			return i18n.T("tui.hint.deck")
		}
		return i18n.T("tui.hint.drawer")
	}
	if _, ok := m.menu(); ok {
		return i18n.T("tui.hint.menu")
	}
	if m.typing {
		return i18n.T("tui.hint.typing")
	}
	if m.pcErr != nil {
		return i18n.T("tui.hint.no_playback")
	}
	if m.stalled {
		return i18n.T("tui.hint.stalled")
	}
	return i18n.T("tui.hint.playing")
}

func tuiQuietStderr() func() {
	origBackoff, origLock := provider.BackoffStderr, auth.LockStderr
	provider.BackoffStderr, auth.LockStderr = io.Discard, io.Discard
	return func() { provider.BackoffStderr, auth.LockStderr = origBackoff, origLock }
}

var tuiRunProgram = runProgram

var runTUI = func(cmd *cobra.Command) error {
	ctx := cmd.Context()
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	if cfg, err := config.Load(); err == nil && cfg.Theme != "" {
		ui.ApplyTheme(cfg.Theme)
	}
	provFlag := ""
	if cmd.Flags().Changed(flagProvider) {
		provFlag, _ = cmd.Flags().GetString(flagProvider)
	}
	provID := provFlag
	if provID == "" {
		provID = loadDefaultProvider()
	}
	trk := &nowTracker{ctx: ctx, timeout: tuiStateTimeout}
	m := newTUIModel(ctx, ui.DefaultTheme, exe, provID, provFlag, trk, watchPollSpotify)
	if m.sleep != nil {
		defer m.sleep.Close()
	}
	defer tuiQuietStderr()()
	_, err = tuiRunProgram(ctx, m, cmd.OutOrStdout())
	return err
}
