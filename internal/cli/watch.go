package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	appleprov "github.com/Tai-ch0802/capy-music/internal/provider/apple"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// now --watch:bubbletea 程式,輪詢 State() 畫進度條;鍵位 space / n / p / q。
// 離開不改變播放狀態。Update 是純函式(tea.Msg 進、model 出),測試不需要 TTY。

const (
	watchMaxFails     = 5
	watchPollSpotify  = time.Second
	watchPollApple    = 2 * time.Second
	watchDefaultWidth = 80
)

type (
	watchTickMsg  time.Time
	watchStateMsg struct {
		st  *provider.PlaybackState
		err error
	}
)

type watchModel struct {
	ctx      context.Context
	pc       provider.PlaybackController
	interval time.Duration
	width    int
	bar      progress.Model
	st       *provider.PlaybackState
	err      error // 最近一次輪詢錯誤(顯示在底部,繼續輪詢)
	fails    int
	fatal    error // 連續失敗達上限:離開並回錯
}

func newWatchModel(ctx context.Context, pc provider.PlaybackController, interval time.Duration) watchModel {
	return watchModel{ctx: ctx, pc: pc, interval: interval, width: watchDefaultWidth, bar: progress.New(progress.WithoutPercentage())}
}

func (m watchModel) Init() tea.Cmd { return m.poll() }

func (m watchModel) poll() tea.Cmd {
	ctx, pc := m.ctx, m.pc
	return func() tea.Msg {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		st, err := pc.State(c)
		return watchStateMsg{st: st, err: err}
	}
}

func (m watchModel) tick() tea.Cmd {
	return tea.Tick(m.interval, func(t time.Time) tea.Msg { return watchTickMsg(t) })
}

// control:送控制指令後立刻重新輪詢,畫面才會跟上。
func (m watchModel) control(f func(context.Context) error) tea.Cmd {
	ctx := m.ctx
	poll := m.poll()
	return func() tea.Msg {
		if err := f(ctx); err != nil {
			return watchStateMsg{err: err}
		}
		return poll()
	}
}

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case watchStateMsg:
		if errors.Is(msg.err, appleprov.ErrNotRunning) { // 狀態,不是失敗:留在畫面上、繼續輪詢,Music 開了畫面就活過來
			m.st, m.err, m.fails = nil, msg.err, 0
			return m, m.tick()
		}
		if msg.err != nil {
			m.err, m.fails = msg.err, m.fails+1
			if m.fails >= watchMaxFails {
				m.fatal = fmt.Errorf("連續 %d 次讀不到播放狀態:%w", m.fails, msg.err)
				return m, tea.Quit
			}
			return m, m.tick()
		}
		m.st, m.err, m.fails = msg.st, nil, 0
		return m, m.tick()
	case watchTickMsg:
		return m, m.poll()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "space":
			if m.st != nil && m.st.Playing {
				return m, m.control(m.pc.Pause)
			}
			return m, m.control(func(ctx context.Context) error { return m.pc.Play(ctx, provider.PlayRequest{}) })
		case "n":
			return m, m.control(m.pc.Next)
		case "p":
			return m, m.control(m.pc.Prev)
		}
	}
	return m, nil
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
		line("目前沒有播放內容")
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
			dev := fmt.Sprintf("  %s(%s)", st.Device.Name, st.Device.Type)
			if st.Device.VolumePct > 0 {
				dev += fmt.Sprintf(" · 音量 %d", st.Device.VolumePct)
			}
			line(dev)
		}
	}
	if m.err != nil && m.fails == 0 {
		line("⚠ " + m.err.Error())
	} else if m.err != nil {
		line(fmt.Sprintf("⚠ %v(第 %d 次)", m.err, m.fails))
	}
	line("  space 播放/暫停 · n 下一首 · p 上一首 · q 離開")
	return tea.NewView(b.String())
}

// runWatch:跑到使用者離開或連續失敗。測試替換點(RunE 的 TTY 閘門獨立可測)。
var runWatch = func(cmd *cobra.Command, p provider.Provider, pc provider.PlaybackController) error {
	interval := watchPollSpotify
	if p.ID() == "apple" {
		interval = watchPollApple
	}
	m := newWatchModel(cmd.Context(), pc, interval)
	origStderr := provider.BackoffStderr // 429 退避的提示不能印進 TUI 畫面
	provider.BackoffStderr = io.Discard
	defer func() { provider.BackoffStderr = origStderr }()
	final, err := tea.NewProgram(m, tea.WithContext(cmd.Context()), tea.WithOutput(cmd.OutOrStdout())).Run()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, tea.ErrProgramKilled) {
		return err
	}
	if fm, ok := final.(watchModel); ok && fm.fatal != nil {
		return friendlyErr(p.ID(), fm.fatal)
	}
	return nil
}
