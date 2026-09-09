package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// capy(無參數、在終端機裡)= 互動式介面:上面是水豚橫幅,中間是現在播什麼,下面一行輸入任何 capy 子命令。
//
// 命令**不在行程內跑**,而是用 tea.ExecProcess 重新執行 capy 自己(os.Executable)。理由:行程內跑的話
// stdout 不是 TTY,每個命令都會退成 TSV,而 huh 的確認提示會搶 stdin 直接卡死。重新執行 = 每個子命令
// 完整保有它原本的行為(表格、挑選器、確認提示),輸出也自然留在上方的捲動區(所以刻意不進 alt screen,
// 與 now --watch 的既有決定一致)。
//
// Update 是純函式(tea.Msg 進、model 出),測試不需要 TTY。

const (
	tuiFrameInterval = 350 * time.Millisecond // 水豚的動作
	tuiSeekStep      = 10000                  // ←/→ 一次 10 秒
	tuiVolStep       = 5                      // +/- 一次 5
	tuiMaxFails      = 5
	tuiMinWidth      = 46 // 窄於此:橫幅換成一行
)

type (
	tuiFrameMsg time.Time
	tuiPollMsg  time.Time
	tuiStateMsg struct {
		st      *provider.PlaybackState
		err     error
		fromCtl bool // 控制指令的錯:顯示但不計入 fails(那個預算是給「連不上」用的)
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
	pc       provider.PlaybackController
	pcErr    error // 沒有播放遙控的原因(沒登入、平台不支援):顯示,不致命
	interval time.Duration
	width    int
	frame    int
	bar      progress.Model
	input    textinput.Model
	typing   bool
	st       *provider.PlaybackState
	err      error
	note     string
	fails    int
	fatal    error
}

func newTUIModel(ctx context.Context, theme ui.Theme, exe, provID string, pc provider.PlaybackController, pcErr error, interval time.Duration) tuiModel {
	in := textinput.New()
	in.Prompt = "› "
	in.Placeholder = "輸入任何 capy 子命令,例如 search 派對動物"
	in.CharLimit = 240
	in.SetWidth(76) // WindowSizeMsg 進來前的預設;不設會被截成一個字
	st := textinput.DefaultDarkStyles()
	st.Focused.Prompt = st.Focused.Prompt.Foreground(theme.Accent)
	st.Blurred.Prompt = st.Blurred.Prompt.Foreground(theme.Muted)
	st.Focused.Placeholder = st.Focused.Placeholder.Foreground(theme.Muted)
	st.Blurred.Placeholder = st.Blurred.Placeholder.Foreground(theme.Muted)
	st.Cursor.Color = theme.Accent
	in.SetStyles(st)
	return tuiModel{
		ctx: ctx, theme: theme, exe: exe, provID: provID, pc: pc, pcErr: pcErr,
		interval: interval, width: 80,
		bar:   progress.New(progress.WithoutPercentage(), progress.WithColors(theme.Accent)), // 單色;給兩個顏色會變成漸層,俐落度輸給純色
		input: in,
	}
}

func (m tuiModel) Init() tea.Cmd { return tea.Batch(m.frameTick(), m.poll()) }

func (m tuiModel) frameTick() tea.Cmd {
	return tea.Tick(tuiFrameInterval, func(t time.Time) tea.Msg { return tuiFrameMsg(t) })
}

func (m tuiModel) pollTick() tea.Cmd {
	return tea.Tick(m.interval, func(t time.Time) tea.Msg { return tuiPollMsg(t) })
}

func (m tuiModel) poll() tea.Cmd {
	ctx, pc, interval := m.ctx, m.pc, m.interval
	if pc == nil {
		return nil
	}
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, pollTimeout(interval))
		defer cancel()
		st, err := pc.State(c)
		return tuiStateMsg{st: st, err: err}
	}
}

// control:送控制指令後立刻重新輪詢,畫面才會跟上(同 now --watch)。
func (m tuiModel) control(f func(context.Context) error) tea.Cmd {
	ctx := m.ctx
	poll := m.poll()
	return func() tea.Msg {
		if err := f(ctx); err != nil {
			return tuiStateMsg{err: err, fromCtl: true}
		}
		if poll == nil {
			return nil
		}
		return poll()
	}
}

// tuiExecProcess:測試替換點——真的 fork 一個行程沒辦法在單元測試裡驗證,換掉它才能斷言「這一行
// 被切成哪些參數」。正式路徑就是 tea.ExecProcess。
var tuiExecProcess = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd { return tea.ExecProcess(c, fn) }

// runArgs:把輸入的一行拿去重新執行 capy 自己。執行期間 bubbletea 讓出終端機,子命令拿到真的 TTY。
func (m tuiModel) runArgs(args []string) tea.Cmd {
	c := exec.CommandContext(m.ctx, m.exe, args...)
	return tuiExecProcess(c, func(err error) tea.Msg { return tuiExecMsg{args: args, err: err} })
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.input.SetWidth(max(20, msg.Width-4)) // 不設的話 textinput 用預設寬度,placeholder 會被截成一個字
		return m, nil
	case tuiFrameMsg:
		m.frame++
		return m, m.frameTick()
	case tuiPollMsg:
		return m, m.poll()
	case tuiStateMsg:
		return m.applyState(msg)
	case tuiExecMsg:
		if msg.err != nil {
			m.note = fmt.Sprintf("capy %s:%v", strings.Join(msg.args, " "), msg.err)
		} else {
			m.note = "capy " + strings.Join(msg.args, " ") + " 執行完畢"
		}
		return m, tea.Batch(m.frameTick(), m.poll()) // Exec 期間 tick 停了,回來要接上
	case tea.KeyPressMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m tuiModel) applyState(msg tuiStateMsg) (tea.Model, tea.Cmd) {
	var rl *provider.RateLimitError
	switch {
	case errors.Is(msg.err, provider.ErrPlayerNotRunning): // 狀態,不是失敗
		m.st, m.err, m.fails = nil, msg.err, 0
		return m, m.pollTick()
	case errors.As(msg.err, &rl):
		m.err, m.fails = fmt.Errorf("rate limited,等待中…(%s)", rl.Message), 0
		return m, m.pollTick()
	case msg.fromCtl && msg.err != nil:
		m.err = msg.err
		return m, m.pollTick()
	}
	if msg.err != nil {
		m.err, m.fails = msg.err, m.fails+1
		if m.fails >= tuiMaxFails {
			m.fatal = fmt.Errorf("連續 %d 次讀不到播放狀態:%w", m.fails, msg.err)
			return m, tea.Quit
		}
		return m, m.pollTick()
	}
	m.st, m.err, m.fails = msg.st, nil, 0
	return m, m.pollTick()
}

func (m tuiModel) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.typing {
		switch msg.String() {
		case "esc":
			m.typing, m.input = false, blurred(m.input)
			return m, nil
		case "enter":
			args := splitArgs(m.input.Value())
			m.typing, m.input = false, blurred(m.input)
			m.input.SetValue("")
			if len(args) == 0 {
				return m, nil
			}
			if m.exe == "" {
				m.note = "找不到 capy 自己的執行檔,命令列停用"
				return m, nil
			}
			m.note = ""
			return m, m.runArgs(args)
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
	}
	if m.pc == nil {
		return m, nil
	}
	switch msg.String() {
	case "space":
		if m.st != nil && m.st.Playing {
			return m, m.control(m.pc.Pause)
		}
		return m, m.control(func(ctx context.Context) error { return m.pc.Play(ctx, provider.PlayRequest{}) })
	case "n":
		return m, m.control(m.pc.Next)
	case "p":
		return m, m.control(m.pc.Prev)
	case "left", "right":
		pos, ok := m.seekTarget(msg.String() == "right")
		if !ok {
			return m, nil
		}
		m.note = "跳到 " + ui.FormatDuration(pos)
		return m, m.control(func(ctx context.Context) error { return m.pc.Seek(ctx, pos) })
	case "+", "=", "-":
		pct, ok := m.volTarget(msg.String() != "-")
		if !ok {
			return m, nil
		}
		m.note = fmt.Sprintf("音量 %d", pct)
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

// volTarget:目前音量 ±5,夾在 [0, 100]。平台沒回音量(Apple 的 State 不帶)時不猜。
func (m tuiModel) volTarget(up bool) (int, bool) {
	if m.st == nil || m.st.Device.VolumePct <= 0 {
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

func (m tuiModel) View() tea.View {
	w := m.width
	if w <= 0 {
		w = 80
	}
	t := m.theme
	var b strings.Builder
	line := func(s string) { b.WriteString(ansi.Truncate(s, w, "…")); b.WriteByte('\n') }

	if w >= tuiMinWidth && w >= capybaraWidth() {
		for _, l := range capybaraFrame(m.frame) {
			line(t.Accented(l))
		}
		line("")
	} else {
		line(t.Accented(capyOneLine))
	}
	line(t.Mutedly(capyTagline(m.provID)))
	line("")

	switch {
	case m.pc == nil:
		line(t.Mutedly("沒有播放遙控:" + errText(m.pcErr, "這個平台不支援")))
	case m.st == nil || m.st.Track == nil:
		line(t.Mutedly("目前沒有播放內容"))
	default:
		st := m.st
		mark := "⏸"
		if st.Playing {
			mark = "▶"
		}
		line(t.Accented(mark) + " " + t.Strong(st.Track.Title))
		line(t.Mutedly("  " + strings.Join(st.Track.Artists, ", ") + " · " + st.Track.Album))
		pct := 0.0
		if st.Track.DurationMS > 0 {
			pct = min(1, float64(st.ProgressMS)/float64(st.Track.DurationMS))
		}
		times := fmt.Sprintf(" %s / %s", ui.FormatDuration(st.ProgressMS), ui.FormatDuration(st.Track.DurationMS))
		bar := m.bar
		bar.SetWidth(max(10, w-2-ansi.StringWidth(times)))
		line("  " + bar.ViewAs(pct) + t.Mutedly(times))
		if st.Device.Name != "" {
			dev := fmt.Sprintf("  %s(%s)", st.Device.Name, st.Device.Type)
			if st.Device.VolumePct > 0 {
				dev += fmt.Sprintf(" · 音量 %d", st.Device.VolumePct)
			}
			line(t.Mutedly(dev))
		}
	}
	line("")
	if m.err != nil {
		line(t.Mutedly("⚠ " + m.err.Error()))
	}
	if m.note != "" {
		line(t.Mutedly("· " + m.note))
	}
	line(m.input.View())
	line(t.Mutedly(m.hints()))
	return tea.NewView(b.String())
}

func (m tuiModel) hints() string {
	if m.typing {
		return "  Enter 執行 · Esc 取消"
	}
	if m.pc == nil {
		return "  / 輸入命令 · q 離開"
	}
	return "  space 播放/暫停 · n/p 上下首 · ←/→ ±10 秒 · +/- 音量 · / 輸入命令 · q 離開"
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
	m := newTUIModel(ctx, ui.DefaultTheme, exe, provID, pc, pcErr, interval)
	origStderr := provider.BackoffStderr // 429 退避的提示不能印進畫面
	provider.BackoffStderr = io.Discard
	defer func() { provider.BackoffStderr = origStderr }()
	final, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(cmd.OutOrStdout())).Run()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, tea.ErrProgramKilled) {
		return err
	}
	if fm, ok := final.(tuiModel); ok && fm.fatal != nil {
		return friendlyErr(provID, fm.fatal)
	}
	return nil
}
