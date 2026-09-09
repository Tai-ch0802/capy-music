package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// capy(無參數、在終端機裡)= 互動式介面。形態是**訊息流 + 固定底部區**(Claude Code 那種),
// 不是全螢幕接管:
//
//   - 開場水豚動兩秒,然後定格印進捲動區,之後永不重繪。
//   - TUI 只管底部四行(分隔線 / 狀態 / 輸入 / 提示),其餘全部是終端機自己的捲動區。
//   - 命令的回音、錯誤、結束碼都用 tea.Println 推進捲動區:永久、可往回捲、可複製。
//
// 前一版把 19 行(其中 8 行是水豚)每 350 毫秒整塊重繪,視窗放不下時游標上移被頂端截斷,
// 舊畫面留在上面 —— 使用者看到的是水豚頭重複三次、歷程完全不可讀。核心錯誤是重繪面積,
// 所以這一版把 TUI 管的區域壓到四行(計畫 docs/superpowers/plans/2026-09-09-tui-redesign.md)。
//
// 命令**不在行程內跑**,而是用 tea.ExecProcess 重新執行 capy 自己(os.Executable)。理由:行程內跑的話
// stdout 不是 TTY,每個命令都會退成 TSV,而 huh 的確認提示會搶 stdin 直接卡死。重新執行 = 每個子命令
// 完整保有它原本的行為(表格、挑選器、確認提示),輸出也自然留在上方的捲動區(所以刻意不進 alt screen,
// 與 now --watch 的既有決定一致)。
//
// Update 是純函式(tea.Msg 進、model 出),測試不需要 TTY。

const (
	tuiFrameInterval = 350 * time.Millisecond // 水豚的動作
	tuiIntro         = 2 * time.Second        // 動這麼久就定格。不是「動到第一個命令」:那會讓重繪延續到不確定的時間點
	tuiSeekStep      = 10000                  // ←/→ 一次 10 秒
	tuiVolStep       = 5                      // +/- 一次 5
	tuiMaxFails      = 5
	tuiMinWidth      = 46 // 窄於此:橫幅換成一行
)

type (
	tuiFrameMsg  time.Time
	tuiFreezeMsg struct{} // 開場結束:水豚定格進捲動區
	// tuiPollMsg / tuiStateMsg 帶 gen:輪詢是一條「tick → 讀狀態 → 再排一個 tick」的鏈,而控制鍵
	// 為了讓畫面立刻跟上會另外起一次讀取。沒有世代編號的話那次讀取會長出第二條鏈,而舊鏈沒人取消——
	// 方向鍵會自動重複,按住兩秒就是三十幾條鏈同時打 /me/player,穩定觸發 429(PR #41 review)。
	// 控制鍵讓 gen 前進,舊鏈的 tick 到期時發現世代不符就停下來。
	tuiPollMsg  struct{ gen int }
	tuiStateMsg struct {
		st      *provider.PlaybackState
		err     error
		fromCtl bool // 控制指令的錯:顯示但不計入 fails(那個預算是給「連不上」用的)
		gen     int
	}
	tuiExecMsg struct {
		args []string
		err  error
	}
)

type tuiModel struct {
	ctx      context.Context
	theme    ui.Theme
	exe      string // 重新執行自己用;空 = 取不到,命令列停用
	provID   string
	provFlag string // 使用者在 capy --provider X 明指的平台;命令列要把它一起帶給子命令
	pc       provider.PlaybackController
	pcErr    error // 沒有播放遙控的原因(沒登入、平台不支援):顯示,不致命
	interval time.Duration
	width    int
	frame    int
	input    textinput.Model
	typing   bool
	st       *provider.PlaybackState
	errShort string // 狀態列用的短版。完整那段在發生當下就進捲動區了,model 不留
	lastErr  string // 上一則交代過的錯誤:同一則每兩秒印一次會把捲動區洗掉
	fails    int
	gen      int  // 目前的輪詢世代
	stalled  bool // 連續讀不到狀態,輪詢先停下來(按 r 重試);介面不關
	frozen   bool // 開場結束:水豚已進捲動區,View 只剩底部四行
}

func newTUIModel(ctx context.Context, theme ui.Theme, exe, provID, provFlag string, pc provider.PlaybackController, pcErr error, interval time.Duration) tuiModel {
	in := textinput.New()
	// 提示符刻意用 ASCII:textinput 會把整行填滿到它自己算的 w-1,而 › 是 East Asian Ambiguous,
	// 在 CJK 終端機多佔一欄 = 剛好寫滿最後一欄 = 多換一行,底部就變五行(設計文件 §1 的第四個問題)。
	in.Prompt = "> "
	in.CharLimit = 240
	in.Placeholder = tuiPlaceholder(80) // WindowSizeMsg 進來前的預設,與下面的 width 一致
	in.SetWidth(76)
	st := textinput.DefaultDarkStyles()
	st.Focused.Prompt = st.Focused.Prompt.Foreground(theme.Accent)
	st.Blurred.Prompt = st.Blurred.Prompt.Foreground(theme.Muted)
	st.Focused.Placeholder = st.Focused.Placeholder.Foreground(theme.Muted)
	st.Blurred.Placeholder = st.Blurred.Placeholder.Foreground(theme.Muted)
	st.Cursor.Color = theme.Accent
	in.SetStyles(st)
	return tuiModel{
		ctx: ctx, theme: theme, exe: exe, provID: provID, provFlag: provFlag, pc: pc, pcErr: pcErr,
		interval: interval, width: 80, input: in,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.frameTick(), tea.Tick(tuiIntro, func(time.Time) tea.Msg { return tuiFreezeMsg{} }), m.poll())
}

func (m tuiModel) frameTick() tea.Cmd {
	return tea.Tick(tuiFrameInterval, func(t time.Time) tea.Msg { return tuiFrameMsg(t) })
}

func (m tuiModel) pollTick() tea.Cmd {
	gen := m.gen
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return tuiPollMsg{gen: gen} })
}

func (m tuiModel) poll() tea.Cmd {
	ctx, pc, interval, gen := m.ctx, m.pc, m.interval, m.gen
	if pc == nil {
		return nil
	}
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, pollTimeout(interval))
		defer cancel()
		st, err := pc.State(c)
		return tuiStateMsg{st: st, err: err, gen: gen}
	}
}

// control:送控制指令後立刻重新輪詢,畫面才會跟上。呼叫端要先把 gen 推進(newChain),
// 這次讀取才會取代舊鏈而不是疊上去。
func (m tuiModel) control(f func(context.Context) error) tea.Cmd {
	ctx, gen := m.ctx, m.gen
	poll := m.poll()
	return func() tea.Msg {
		if err := f(ctx); err != nil {
			return tuiStateMsg{err: err, fromCtl: true, gen: gen}
		}
		if poll == nil {
			return nil
		}
		return poll()
	}
}

// newChain:讓輪詢世代前進(舊鏈的 tick 到期時會被丟掉),回傳推進後的 model。
func (m tuiModel) newChain() tuiModel {
	m.gen++
	m.stalled = false
	return m
}

// freeze:開場結束。水豚定格印進捲動區,之後 View 只剩底部四行、frame ticker 停掉。
// 兩秒到會呼叫,任何一個按鍵也會——不然「開場期間按 Enter」會在水豚還在 View 裡時 Exec,
// 子命令的輸出印在它下面,兩秒到再定格印一次,就又變成使用者回報的「水豚頭重複」。
func (m tuiModel) freeze() (tuiModel, tea.Cmd) {
	if m.frozen {
		return m, nil
	}
	m.frozen = true
	lines := capybaraStill()
	if m.viewWidth() < max(tuiMinWidth, capybaraWidth()) {
		lines = []string{capyOneLine}
	}
	banner := m.theme.Accented(strings.Join(lines, "\n")) + "\n\n  " + m.theme.Mutedly(capyTagline(m.provID))
	return m, tuiPrintln(banner)
}

// execResult:子命令跑完。成功不印;非零結束碼推一行進捲動區。
// capy 的 2(有待套用的變更)與 3(安全閥擋下)是設計出來的結束碼、不是壞掉,所以不掛 ✗。
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
	if c := ee.ExitCode(); c == 2 || c == 3 {
		mark = "· "
	}
	return m.println(tuiSeg{fmt.Sprintf("%s%s 結束碼 %d", mark, head, ee.ExitCode()), m.theme.Mutedly})
}

func (m tuiModel) viewWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

// tuiPrintln:把一行推進捲動區(不歸 TUI 管、不會被重繪蓋掉)。測試替換點——tea.Println 產生的
// printLineMessage 是 bubbletea 的私有型別,外面認不出來,換掉它才能斷言「哪些東西進了捲動區」。
// 一定要夾寬度:insertAbove 用 ansi.StringWidth 算要捲幾行,量不到 ambiguous 字元(見 tui_width.go)。
var tuiPrintln = func(s string) tea.Cmd { return tea.Println(s) }

func (m tuiModel) println(segs ...tuiSeg) tea.Cmd {
	return tuiPrintln(tuiJoin(m.viewWidth()-1, segs...))
}

// printErr:錯誤第一次出現時整段推進捲動區(永久記錄,可以回頭看);狀態列只留短版。
// 同一則不重複印——輪詢每兩秒一次,重複印會把捲動區洗掉。
func (m *tuiModel) printErr(err error) tea.Cmd {
	if err == nil || err.Error() == m.lastErr {
		return nil
	}
	m.lastErr = err.Error()
	return m.println(tuiSeg{"⚠ " + err.Error(), m.theme.Mutedly})
}

// tuiExecProcess:測試替換點——真的 fork 一個行程沒辦法在單元測試裡驗證,換掉它才能斷言「這一行
// 被切成哪些參數」。正式路徑就是 tea.ExecProcess。
var tuiExecProcess = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd { return tea.ExecProcess(c, fn) }

// withProviderFlag:capy --provider apple 開的介面是 Apple,命令列跑的卻是 config 的 default_provider——
// 同一個畫面兩個平台,而且 pause 停的不是上面在播的那首。只在使用者明指時附加,而且要先確認目標子命令
// 真的吃這個 flag:auth / config / export / resolve 沒掛 --provider,多送一個會直接 unknown flag 退出。
func (m tuiModel) withProviderFlag(args []string) []string {
	if m.provFlag == "" {
		return args
	}
	c, _, err := newRootCmd().Find(args)
	if err != nil || c.Flags().Lookup(flagProvider) == nil {
		return args
	}
	return append(slices.Clone(args), "--"+flagProvider, m.provFlag)
}

// runArgs:把輸入的一行拿去重新執行 capy 自己。執行期間 bubbletea 讓出終端機,子命令拿到真的 TTY。
func (m tuiModel) runArgs(args []string) tea.Cmd {
	full := m.withProviderFlag(args)
	c := exec.CommandContext(m.ctx, m.exe, full...)
	return tuiExecProcess(c, func(err error) tea.Msg { return tuiExecMsg{args: args, err: err} })
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		// 下限不能寫死:比終端機還寬的輸入行會換行,底部就從四行變五行 —— 正是這次要修掉的症狀。
		m.input.SetWidth(max(8, msg.Width-4))
		m.input.Placeholder = tuiPlaceholder(msg.Width)
		return m, nil
	case tuiFrameMsg:
		if m.frozen { // 定格後不再有動畫,frame ticker 就此停掉:底部四行只在狀態變動與按鍵時重畫
			return m, nil
		}
		m.frame++
		return m, m.frameTick()
	case tuiFreezeMsg:
		return m.freeze()
	case tuiPollMsg:
		if msg.gen != m.gen || m.stalled { // 舊鏈:停在這裡,不要再排下一個 tick
			return m, nil
		}
		return m, m.poll()
	case tuiStateMsg:
		return m.applyState(msg)
	case tuiExecMsg:
		// 成功不印:子命令的輸出本身就是證據,多一行是雜訊。
		// 不重排 tick:tea.Exec 擋住的是 event loop,不是 tea.Tick 的 timer(各自的 goroutine)。
		// 排隊中的 tuiFrameMsg / tuiPollMsg 回來就會把兩條鏈接上,這裡再排一次會變成兩條(PR #41 review)。
		return m, m.execResult(msg)
	case tea.KeyPressMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m tuiModel) applyState(msg tuiStateMsg) (tea.Model, tea.Cmd) {
	// stale = 舊世代的回覆(控制鍵已經開了新鏈)。判斷要在所有分支之前:pollTick() 抓的是**目前**的
	// gen,所以任何一條路徑只要排了 tick,舊鏈就復活成一條完全合法的新鏈——429 那條還會自我增強
	// (鏈愈多愈容易 429,愈常走那條路徑,鏈又愈多)。停擺中同理:一則遲到的訊息不該把輪詢默默接回去,
	// 畫面卻還在叫使用者按 r(PR #42 review)。
	stale := msg.gen != m.gen
	tick := func() tea.Cmd {
		if stale || m.stalled {
			return nil
		}
		return m.pollTick()
	}
	// 控制指令自己的錯誤要說(那是使用者剛按的鍵失敗了),即使期間又按了一次鍵而變成 stale。
	if msg.fromCtl && msg.err != nil {
		pr := m.printErr(msg.err) // printErr 是指標 receiver,先叫再 return:
		m.errShort = "剛才那個操作失敗了(詳見上方)"
		return m, tea.Batch(pr, tick()) // 寫在 return 的運算式裡,m 有沒有帶到更新是規格未定義的
	}
	if stale { // 過期的輪詢結果沒有價值:不顯示(否則一則遲到的「播放器未執行」會抹掉剛拿回來的狀態)
		return m, nil
	}
	// 這兩條是狀態不是失敗,不進捲動區(Music.app 沒開、被限流都會持續好一陣子),但狀態列要說。
	// lastErr 仍然記帳:換過狀態再換回同一則錯誤,是新的一件事,要能再印一次。
	var rl *provider.RateLimitError
	switch {
	case errors.Is(msg.err, provider.ErrPlayerNotRunning):
		m.st, m.errShort, m.fails, m.lastErr = nil, "播放器未執行", 0, msg.err.Error()
		return m, tick()
	case errors.As(msg.err, &rl):
		m.errShort, m.fails, m.lastErr = "限流中,等待重試", 0, msg.err.Error()
		return m, tick()
	}
	if msg.err != nil {
		m.fails++
		m.errShort = "讀不到播放狀態(r 重試)"
		// 整段錯誤推進捲動區(第一次、或內容變了才印),狀態列只留短版:
		// 前一版把整段留在畫面上,osascript 那種長訊息會一直佔著看不到別的。
		pr := m.printErr(msg.err) // 指標 receiver:一定要在 return 之前叫
		if m.fails >= tuiMaxFails {
			// 不關介面:這裡的定位是「一行可以跑任何子命令」,播放狀態只是其中一格。連不上的時候
			// 使用者最需要的正是那行命令列(auth login、doctor),把整個介面收掉等於把人關在門外
			// (PR #41 review)。輪詢先停,按 r 重試。
			m.stalled = true
			return m, pr
		}
		return m, tea.Batch(pr, tick())
	}
	// 恢復了就把去重的記憶清掉:同一則錯誤在恢復之後再發生,是新的一件事,要再印一次。
	m.st, m.errShort, m.fails, m.lastErr = msg.st, "", 0, ""
	return m, tick()
}

func (m tuiModel) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// 開場動畫期間按任何鍵都先定格:水豚還在 View 裡時 Exec,子命令的輸出會印在它下面,
	// 兩秒到再定格印一次 = 使用者回報的「水豚頭重複」。順帶也讓人可以跳過開場。
	if !m.frozen {
		var freeze tea.Cmd
		m, freeze = m.freeze()
		next, cmd := m.onKey(msg)
		return next, tea.Sequence(freeze, cmd)
	}
	if m.typing {
		switch msg.String() {
		case "ctrl+c": // raw mode 下 ctrl+c 不是 SIGINT 而是一個按鍵;不攔的話打字打到一半按它毫無反應
			return m, tea.Quit
		case "esc": // 有字先清空,空的再離開輸入(打錯一長串時不必連按退格)
			if m.input.Value() != "" {
				m.input.SetValue("")
				return m, nil
			}
			m.typing, m.input = false, blurred(m.input)
			return m, nil
		case "enter":
			raw := m.input.Value()
			args := splitArgs(raw)
			m.typing, m.input = false, blurred(m.input)
			m.input.SetValue("")
			if len(args) == 0 {
				return m, nil
			}
			if m.exe == "" {
				return m, m.println(tuiSeg{"✗ 找不到 capy 自己的執行檔,命令列停用", m.theme.Mutedly})
			}
			// 回音先進捲動區再讓出終端機:Sequence 保序,而 exec 交出終端機前會 flush 一次
			// (releaseTerminal → stopRenderer(false) → flush),所以回音一定在子命令輸出上面。
			return m, tea.Sequence(
				m.println(tuiSeg{"> ", m.theme.Accented}, tuiSeg{raw, m.theme.Strong}), // 與輸入行的提示符一致
				m.runArgs(args),
			)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "/", ":":
		m.typing = true
		m.input.Focus()
		return m, textinput.Blink
	case "?": // 完整鍵位推進捲動區,不佔底部的行數
		return m, m.println(tuiSeg{tuiKeymap, m.theme.Mutedly})
	}
	if m.pc == nil {
		return m, nil
	}
	switch msg.String() {
	case "r": // 輪詢停下來之後重新接上
		m = m.newChain()
		// lastErr 也清掉:明確的重試是新的一件事,重試又撞到同一則錯誤時要再印一次,
		// 不然使用者按了鍵,十秒內畫面上完全沒有任何事情發生過的痕跡。
		m.errShort, m.fails, m.lastErr = "", 0, ""
		return m, m.poll()
	case "space":
		m = m.newChain()
		if m.st != nil && m.st.Playing {
			return m, m.control(m.pc.Pause)
		}
		return m, m.control(func(ctx context.Context) error { return m.pc.Play(ctx, provider.PlayRequest{}) })
	case "n":
		m = m.newChain()
		return m, m.control(m.pc.Next)
	case "p":
		m = m.newChain()
		return m, m.control(m.pc.Prev)
	case "left", "right":
		pos, ok := m.seekTarget(msg.String() == "right")
		if !ok {
			return m, nil
		}
		m = m.newChain() // 不印提示:下一次輪詢就會把新位置寫進狀態列,那才是真的發生了
		return m, m.control(func(ctx context.Context) error { return m.pc.Seek(ctx, pos) })
	case "+", "=", "-":
		pct, ok := m.volTarget(msg.String() != "-")
		if !ok {
			return m, nil
		}
		m = m.newChain() // 同上:狀態列的「音量 N」會跟著更新
		return m, m.control(func(ctx context.Context) error { return m.pc.SetVolume(ctx, pct) })
	}
	return m, nil
}

// seekTarget:目前位置 ±10 秒,夾在 [0, 長度]。沒有播放內容時不做事(沒有基準點可以加減)。
func (m tuiModel) seekTarget(forward bool) (int, bool) {
	if m.st == nil || m.st.Track == nil {
		return 0, false
	}
	pos := m.st.ProgressMS + tuiSeekStep
	if !forward {
		pos = m.st.ProgressMS - tuiSeekStep
	}
	if pos < 0 {
		pos = 0
	}
	if d := m.st.Track.DurationMS; d > 0 && pos > d {
		pos = d
	}
	return pos, true
}

// volTarget:目前音量 ±5,夾在 [0, 100]。平台沒回音量(Apple 的 State 不帶)時不猜——
// 但音量真的是 0(靜音)要能被 + 拉回來,所以看的是 VolumeKnown 而不是 VolumePct > 0。
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

// splitArgs:把輸入的一行切成參數。以空白分隔,雙引號內的空白保留——清單名有空白很常見
// (capy pl show "上班 通勤")。沒有跳脫、沒有單引號:再多就該用真的 shell 了。
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

// View 只回兩種畫面:開場的水豚(兩秒),之後永遠是底部四行。
// 四行 = 分隔線 / 狀態 / 輸入 / 提示。每一行都夾在 w-1 欄:寫滿最後一欄時某些終端機會多換一行,
// 底部就多佔一行、上緣被頂掉(前一版 19 行畫面崩掉的成因之一)。
func (m tuiModel) View() tea.View {
	w := m.viewWidth()
	if !m.frozen {
		return tea.NewView(m.intro(w))
	}
	lines := []string{
		// 分隔線刻意用 ASCII:U+2500 那排方框繪製字元是 East Asian Ambiguous,
		// 在 CJK 終端機會變兩欄,一條滿版的線剛好翻倍成兩行(水豚踩過同一個坑)。
		m.theme.Mutedly(strings.Repeat("-", max(1, w-1))),
		m.statusLine(w - 1),
		// 輸入行不進 tuiClip:它已經帶著樣式,而 tuiClip 只會量純文字的寬度
		// (跳脫碼會被逐位元組算進去,整行被截成一小截)。textinput 自己依 SetWidth 收邊,
		// 而 SetWidth 給的是 w-4,留了餘裕吸收 › 這個 ambiguous 字元可能多佔的一欄。
		m.input.View(),
		tuiJoin(w-1, tuiSeg{"  " + m.hints(), m.theme.Mutedly}),
	}
	v := tea.NewView(strings.Join(lines, "\n")) // 不留結尾換行:那會被當成第五行
	if c := m.input.Cursor(); c != nil {
		c.Y += 2 // 分隔線與狀態列在輸入行上面
		v.Cursor = c
	}
	return v
}

// intro:開場的兩秒。只有水豚與招牌,底部四行還沒出現(定格之後它才是常駐的畫面)。
func (m tuiModel) intro(w int) string {
	var lines []string
	if w >= tuiMinWidth && w >= capybaraWidth() {
		for _, l := range capybaraFrame(m.frame) {
			lines = append(lines, m.theme.Accented(l))
		}
	} else {
		lines = append(lines, m.theme.Accented(capyOneLine))
	}
	return strings.Join(append(lines, "", "  "+m.theme.Mutedly(capyTagline(m.provID))), "\n")
}

// statusLine:一行講完「現在怎麼了」。錯誤只留短版——整段在發生當下已經進捲動區了。
// 短版要能接在曲目後面:限流時上一首歌還在播,只換掉整行的話畫面看起來一切正常,實際上正在被限流。
func (m tuiModel) statusLine(w int) string {
	t := m.theme
	if m.pc == nil {
		return tuiJoin(w, tuiSeg{"  沒有播放遙控:" + errText(m.pcErr, "這個平台不支援"), t.Mutedly})
	}
	short := m.errShort
	if m.stalled {
		short = "已停止輪詢(r 重試)"
	}
	if m.st == nil || m.st.Track == nil {
		if short == "" {
			short = "沒有播放內容"
		}
		return tuiJoin(w, tuiSeg{"  " + short, t.Mutedly})
	}
	st := m.st
	mark := "⏸"
	if st.Playing {
		mark = "▶"
	}
	tail := " · " + ui.FormatDuration(st.ProgressMS) + " / " + ui.FormatDuration(st.Track.DurationMS)
	if st.Device.Name != "" {
		tail += " · " + st.Device.Name
	}
	if st.Device.VolumeKnown { // 靜音要看得到「音量 0」,+/- 也才有可見的回饋
		tail += fmt.Sprintf(" · 音量 %d", st.Device.VolumePct)
	}
	if short != "" { // 有曲目也可能同時有狀況(限流最典型):接在後面,不要蓋掉曲目
		tail += " · " + short
	}
	// 曲名是唯一沒有上限的段,留給它剩下的空間;tuiJoin 會在它那一段截斷。
	return tuiJoin(w,
		tuiSeg{"  " + mark + " ", t.Accented},
		tuiSeg{st.Track.Title, t.Strong},
		tuiSeg{tail, t.Mutedly},
	)
}

// tuiPlaceholder:挑最長的、放得下的那句提示。textinput 不會把 placeholder 收得比它自己短,
// 放不下就整行撐出終端機外 —— 底部從四行變五行。窄到連最短的都放不下就不放。
func tuiPlaceholder(w int) string {
	for _, s := range []string{"輸入 capy 子命令,例如 pl list", "capy 子命令", "pl list"} {
		if tuiWidth(s)+4 <= w { // +4:提示符 "> " 與右邊的餘裕
			return s
		}
	}
	return ""
}

// tuiKeymap:? 印進捲動區的完整鍵位。底部只放最常用的幾個,其餘查這裡。
const tuiKeymap = `按鍵:
  space 播放/暫停    n / p 下一首 / 上一首
  <- / ->  +-10 秒   + / -  音量 +-5
  /      輸入命令    r     停擺後重新連上
  ?      這張表      q     離開(輸入中用 Ctrl-C)`

func (m tuiModel) hints() string {
	if m.typing {
		return "Enter 執行 · Esc 清空 · Ctrl-C 離開"
	}
	if m.pc == nil {
		return "/ 命令 · ? 按鍵 · q 離開"
	}
	if m.stalled {
		return "r 重新連上 · / 命令 · ? 按鍵 · q 離開"
	}
	return "space 播放/暫停 · ←→ ±10 秒 · / 命令 · ? 按鍵 · q 離開"
}

func errText(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	return err.Error()
}

// runTUI:互動式介面的進入點。取不到 provider 或播放遙控都不致命——介面照開,使用者可以在命令列
// 跑 capy auth login。測試替換點。
var runTUI = func(cmd *cobra.Command) error {
	ctx := cmd.Context()
	exe, err := os.Executable()
	if err != nil {
		exe = "" // 命令列停用,其餘照常
	}
	provID, pcErr := "", error(nil)
	var pc provider.PlaybackController
	interval := watchPollSpotify
	if p, err := getProvider(cmd); err != nil {
		pcErr = err
	} else {
		provID = p.ID()
		if p.ID() == "apple" {
			interval = watchPollApple
		}
		if c, err := asPlayback(p); err != nil {
			pcErr = err
		} else {
			pc = c
		}
	}
	provFlag := ""
	if cmd.Flags().Changed(flagProvider) {
		provFlag, _ = cmd.Flags().GetString(flagProvider)
	}
	m := newTUIModel(ctx, ui.DefaultTheme, exe, provID, provFlag, pc, pcErr, interval)
	origStderr := provider.BackoffStderr // 429 退避的提示不能印進畫面
	provider.BackoffStderr = io.Discard
	defer func() { provider.BackoffStderr = origStderr }()
	// 讀不到播放狀態不會讓程式結束(見 applyState 的 stalled),所以這裡沒有 fatal 要轉譯:
	// 離開一律是使用者按 q / Ctrl-C。
	// 三種「使用者要離開」都不是錯誤:ctx 取消、程式被砍、SIGINT 從 raw mode 以外的地方進來。
	if _, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(cmd.OutOrStdout())).Run(); err != nil &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, tea.ErrProgramKilled) && !errors.Is(err, tea.ErrInterrupted) {
		return err
	}
	return nil
}
