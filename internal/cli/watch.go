package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// now --watch:bubbletea 程式,每 interval 問一輪、畫進度條;鍵位 space / n / p / q。離開不改變播放狀態。
// 一輪走 nowTracker(決策 58,同 TUI 的決策 53):沒用 --provider 釘住時跟著正在播的平台走,Spotify 真的被打幾次由
// tracker 的有效期決定(限流時照 Retry-After 冷卻)。Update 是純函式(tea.Msg 進、model 出),測試不需要 TTY。

const (
	watchMaxFails     = 5
	watchPollSpotify  = 2 * time.Second // 畫面每 2 秒問一輪;真的打 Spotify 幾次看 tracker 的有效期(在播最多 10 s、閒置 15 s)
	watchPollApple    = 2 * time.Second
	watchDefaultWidth = 80
)

// watchTickMsg:帶著排它的那條鏈的世代號。控制鍵開新鏈(gen++),舊鏈的 tick 到了就丟——不然每按一次鍵就多一條
// 永遠停不了的鏈(Apple 的結果不快取,每條鏈每 2 秒一支 osascript)。
type watchTickMsg struct{ gen int }

type watchModel struct {
	ctx      context.Context
	trk      *nowTracker
	pin      string // --provider 釘住的平台;空 = 跟著正在播的平台走
	provID   string // 畫面上的平台:控制鍵送給它、連續失敗算它的
	gen      int
	interval time.Duration
	width    int
	bar      progress.Model
	st       *provider.PlaybackState
	err      error  // 最近一次的狀態或錯誤(顯示在底部,繼續輪詢)
	note     string // 等太久時的「等待平台回應」、控制鍵失敗的原因:蓋過 err 那一行、不帶失敗次數,下一個結果回來就清掉
	fails    int    // 連續失敗:只算真的問到的(快取裡的同一則錯誤不重複算)
	fatal    error  // 連續失敗達上限,或沒有任何平台建得起來:離開並回錯
	ctrlC    bool   // 是按 Ctrl-C 離開的:exit 130(見 runProgram)
}

func newWatchModel(ctx context.Context, trk *nowTracker, pin, provID string, interval time.Duration) watchModel {
	return watchModel{ctx: ctx, trk: trk, pin: pin, provID: provID, interval: interval, width: watchDefaultWidth, bar: progress.New(progress.WithoutPercentage())}
}

func (m watchModel) Init() tea.Cmd { return m.poll() }

func (m watchModel) interruptedByKey() bool { return m.ctrlC }

func (m watchModel) poll() tea.Cmd { return nowPoll(m.trk, m.pin, m.gen) }

func (m watchModel) tick() tea.Cmd {
	gen := m.gen
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return watchTickMsg{gen: gen} })
}

// control:開新鏈、把控制指令送給畫面上的平台(n / p 限時,見 nowControl),送完立刻重問一輪。
func (m watchModel) control(f func(provider.PlaybackController, context.Context) error, once bool) (watchModel, tea.Cmd) {
	m.gen++
	return m, nowControl(m.ctx, m.trk, m.provID, m.pin, m.gen, f, once)
}

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tuiStateMsg:
		return m.applyState(msg)
	case watchTickMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		return m, m.poll()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc":
			return m, tea.Quit
		case "ctrl+c":
			m.ctrlC = true
			return m, tea.Quit
		case "space": // 播放或暫停看畫面上的狀態(可能是快取的、不一定是現在的狀態;送不出去會說,並讓快取過期)
			if m.st != nil && m.st.Playing {
				return m.control(provider.PlaybackController.Pause, false)
			}
			return m.control(func(pc provider.PlaybackController, ctx context.Context) error {
				return pc.Play(ctx, provider.PlayRequest{})
			}, false)
		case "n":
			return m.control(provider.PlaybackController.Next, true)
		case "p":
			return m.control(provider.PlaybackController.Prev, true)
		}
	}
	return m, nil
}

// applyState:順序同 TUI 的 applyState(決策 53)。差別:連續失敗到上限就離開(watch 沒有命令列可以留下來用),
// 沒有任何平台建得起來也離開(以前是開畫面之前就失敗)。
func (m watchModel) applyState(msg tuiStateMsg) (tea.Model, tea.Cmd) {
	stale := msg.gen != m.gen
	tick := func() tea.Cmd {
		if stale {
			return nil
		}
		return m.tick()
	}
	if msg.slow { // 等太久:先說一聲,接著等同一個結果(過期的也要等:可能是控制指令自己的錯,或要再丟的 panic)
		if !stale {
			m.note = i18n.T("tui.status.waiting")
		}
		pending := msg.pending
		return m, func() tea.Msg { return (<-pending).repanic() }
	}
	if msg.fromCtl && msg.err != nil { // 使用者剛按的鍵失敗了:即使期間又按了一次而變成 stale 也要說;不計入 fails
		m.note = msg.err.Error() // 不放 err:那一行會帶上輪詢的失敗次數(那是關畫面的預算,跟這個鍵無關)
		return m, tick()
	}
	if stale {
		return m, nil
	}
	m.note = ""
	if msg.dropped {
		return m, tick()
	}
	if msg.provider != "" && msg.provider != m.provID { // 換平台:上一家的曲目與失敗次數不能掛在這一家名下
		m.provID, m.st, m.fails = msg.provider, nil, 0
	}
	var be nowBuildErr
	if errors.As(msg.err, &be) { // 沒有任何平台建得起來(沒登入、這台電腦不支援):同以前開畫面之前的那個錯,原樣回
		m.fatal = msg.err
		return m, tea.Quit
	}
	var rl *provider.RateLimitError
	switch {
	case errors.Is(msg.err, provider.ErrPlayerNotRunning): // 狀態,不是失敗:app 開了畫面就活過來
		m.st, m.err, m.fails = nil, msg.err, 0
		return m, tick()
	case errors.As(msg.err, &rl): // 限流也是狀態:照常每 2 秒問,冷卻期內 tracker 只端出快取(Retry-After 由它守)
		m.err, m.fails = i18n.Errorf("tui.status.rate_limited", "time", msg.retryAt.Local().Format("15:04:05")), 0
		return m, tick()
	}
	if msg.err != nil {
		m.err = msg.err
		if !msg.cached { // Spotify 出錯的結果留 15 秒:每 2 秒算一次的話,一次 502 不到十秒就把畫面關掉
			m.fails++
		}
		if m.fails >= watchMaxFails {
			m.fatal = i18n.Errorf("watch.err.consecutive_failures", "count", m.fails, "err", msg.err)
			return m, tea.Quit
		}
		return m, tick()
	}
	m.st, m.err, m.fails = msg.st, nil, 0
	return m, tick()
}

func (m watchModel) View() tea.View {
	w := m.width
	if w <= 0 {
		w = watchDefaultWidth
	}
	var b strings.Builder
	line := func(s string) { b.WriteString(ansi.Truncate(s, w, "…")); b.WriteByte('\n') }
	switch {
	case m.st == nil || m.st.Track == nil:
		line(i18n.T("player.nothing_playing"))
	default:
		st := m.st
		mark := "⏸"
		if st.Playing {
			mark = "▶"
		}
		line(mark + " " + ui.Bold(true, st.Track.Title))
		line("  " + strings.Join(st.Track.Artists, ", ") + " · " + st.Track.Album)
		pct := 0.0
		if st.Track.DurationMS > 0 {
			pct = min(1, float64(st.ProgressMS)/float64(st.Track.DurationMS))
		}
		times := fmt.Sprintf(" %s / %s", ui.FormatDuration(st.ProgressMS), ui.FormatDuration(st.Track.DurationMS))
		bar := m.bar
		bar.SetWidth(max(10, w-2-ansi.StringWidth(times)))
		line("  " + bar.ViewAs(pct) + times)
		if st.Device.Name != "" {
			dev := "  " + i18n.T("player.device", "name", st.Device.Name, "type", st.Device.Type)
			if st.Device.VolumePct > 0 {
				dev += i18n.T("watch.volume", "pct", st.Device.VolumePct)
			}
			line(dev)
		}
	}
	switch {
	case m.note != "":
		line("⚠ " + m.note)
	case m.err != nil && m.fails == 0:
		line("⚠ " + m.err.Error())
	case m.err != nil:
		line(i18n.T("watch.error_attempt", "err", m.err, "attempt", m.fails))
	}
	line(i18n.T("watch.keys"))
	return tea.NewView(b.String())
}

// runWatch:跑到使用者離開、連續失敗,或沒有任何平台建得起來。測試替換點(RunE 的 TTY 閘門獨立可測)。
// pin 是 --provider 釘住的平台,seed 是 RunE 事先建好的那一家(沒釘住時兩者都是空的,tracker 自己建)。
var runWatch = func(cmd *cobra.Command, pin string, seed provider.PlaybackController) error {
	provID := pin
	if provID == "" {
		provID = loadDefaultProvider()
	}
	trk := &nowTracker{ctx: cmd.Context(), timeout: tuiStateTimeout}
	if seed != nil {
		trk.now = map[string]provider.PlaybackController{pin: seed}
	}
	defer tuiQuietStderr()() // 換 token 等鎖、429 退避的提示不能印進畫面
	final, err := tuiRunProgram(cmd.Context(), newWatchModel(cmd.Context(), trk, pin, provID, watchPollSpotify), cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if fm, ok := final.(watchModel); ok {
		return fm.exitErr()
	}
	return nil
}

// exitErr:離開時回的錯。沒有任何平台建得起來的,原樣回(同以前開畫面之前的那個錯);連續失敗的,照那個平台說人話。
func (m watchModel) exitErr() error {
	if m.fatal == nil {
		return nil
	}
	var be nowBuildErr
	if errors.As(m.fatal, &be) {
		return be.error
	}
	return friendlyErr(m.provID, m.fatal)
}
