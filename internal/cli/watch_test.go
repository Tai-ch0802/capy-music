package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

type watchFake struct {
	playFake
	st    *provider.PlaybackState
	err   error
	calls []string
}

func (f *watchFake) State(context.Context) (*provider.PlaybackState, error) { return f.st, f.err }
func (f *watchFake) Play(_ context.Context, r provider.PlayRequest) error {
	f.calls = append(f.calls, "play")
	return nil
}
func (f *watchFake) Pause(context.Context) error { f.calls = append(f.calls, "pause"); return nil }
func (f *watchFake) Next(context.Context) error  { f.calls = append(f.calls, "next"); return nil }
func (f *watchFake) Prev(context.Context) error  { f.calls = append(f.calls, "prev"); return nil }

func playingState() *provider.PlaybackState {
	return &provider.PlaybackState{
		Playing:    true,
		Track:      &provider.Track{Title: "派對動物", Artists: []string{"五月天"}, Album: "自傳", DurationMS: 249000},
		ProgressMS: 83000,
		Device:     provider.Device{Name: "MacBook Pro", Type: "Computer", VolumePct: 50},
	}
}

// runCmd:執行 tea.Cmd 取回它產生的 Msg(nil-safe)。
func runCmd(c tea.Cmd) tea.Msg {
	if c == nil {
		return nil
	}
	return c()
}

func isQuit(c tea.Cmd) bool {
	_, ok := runCmd(c).(tea.QuitMsg)
	return ok
}

func TestWatchPollTickLoop(t *testing.T) {
	f := &watchFake{st: playingState()}
	m := newWatchModel(context.Background(), f, time.Millisecond)
	msg := runCmd(m.Init()) // Init = 立刻輪詢
	sm, ok := msg.(watchStateMsg)
	if !ok || sm.st == nil || sm.err != nil {
		t.Fatalf("Init 應回輪詢結果:%#v", msg)
	}
	next, cmd := m.Update(sm)
	m = next.(watchModel)
	if m.st == nil || m.st.Track.Title != "派對動物" || cmd == nil {
		t.Fatal("收到狀態應存起來並排下一次 tick")
	}
	if _, ok := runCmd(cmd).(watchTickMsg); !ok {
		t.Fatal("狀態之後應是 tick")
	}
	_, cmd = m.Update(watchTickMsg(time.Now()))
	if _, ok := runCmd(cmd).(watchStateMsg); !ok {
		t.Fatal("tick 之後應再輪詢")
	}
}

func TestWatchKeys(t *testing.T) {
	f := &watchFake{st: playingState()}
	m := newWatchModel(context.Background(), f, time.Millisecond)
	next, _ := m.Update(watchStateMsg{st: f.st})
	m = next.(watchModel)

	for _, c := range []struct {
		key  tea.KeyPressMsg
		want string
	}{
		{tea.KeyPressMsg{Code: tea.KeySpace}, "pause"},
		{tea.KeyPressMsg{Code: 'n'}, "next"},
		{tea.KeyPressMsg{Code: 'p'}, "prev"},
	} {
		f.calls = nil
		_, cmd := m.Update(c.key)
		if _, ok := runCmd(cmd).(watchStateMsg); !ok || len(f.calls) != 1 || f.calls[0] != c.want {
			t.Errorf("%s:calls=%v(控制後應立刻重新輪詢)", c.key, f.calls)
		}
	}
	f.calls = nil
	m.st.Playing = false
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	runCmd(cmd)
	if len(f.calls) != 1 || f.calls[0] != "play" {
		t.Errorf("暫停中按 space 應 Play(resume):%v", f.calls)
	}
	f.calls = nil
	for _, k := range []tea.KeyPressMsg{{Code: 'q'}, {Code: 'c', Mod: tea.ModCtrl}} {
		if _, cmd := m.Update(k); !isQuit(cmd) {
			t.Errorf("%s 應離開", k)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("離開不得改變播放狀態:%v", f.calls)
	}
}

func TestWatchConsecutiveFailuresQuit(t *testing.T) {
	f := &watchFake{err: errors.New("boom")}
	m := newWatchModel(context.Background(), f, time.Millisecond)
	var cmd tea.Cmd
	for i := 1; i < watchMaxFails; i++ {
		var next tea.Model
		next, cmd = m.Update(watchStateMsg{err: f.err})
		m = next.(watchModel)
		if m.fatal != nil || isQuit(cmd) || m.fails != i {
			t.Fatalf("第 %d 次失敗不該離開(fails=%d fatal=%v)", i, m.fails, m.fatal)
		}
	}
	next, _ := m.Update(watchStateMsg{st: playingState()}) // 一次成功就歸零
	m = next.(watchModel)
	if m.fails != 0 || m.err != nil {
		t.Fatal("成功應歸零失敗計數")
	}
	for i := 0; i < watchMaxFails; i++ {
		var next tea.Model
		next, cmd = m.Update(watchStateMsg{err: f.err})
		m = next.(watchModel)
	}
	if m.fatal == nil || !isQuit(cmd) || !strings.Contains(m.fatal.Error(), "boom") {
		t.Fatalf("連續 %d 次失敗應離開並帶原因:%v", watchMaxFails, m.fatal)
	}
}

func TestWatchViewFixedWidth(t *testing.T) {
	f := &watchFake{}
	m := newWatchModel(context.Background(), f, time.Millisecond)
	m.width = 80
	m.st = playingState()
	m.st.Track.Title = strings.Repeat("很長的歌名", 12) // 120 欄寬,必須截斷
	v := ansi.Strip(m.View().Content)
	for _, want := range []string{"▶ 很長的歌名", "…", "五月天 · 自傳", "1:23 / 4:09", "MacBook Pro(Computer) · 音量 50", "space 播放/暫停"} {
		if !strings.Contains(v, want) {
			t.Errorf("畫面缺 %q:\n%s", want, v)
		}
	}
	for _, l := range strings.Split(v, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("行寬 %d 超過 80:%q", w, l)
		}
	}
	m.st, m.err, m.fails = nil, errors.New("timeout"), 2
	v = ansi.Strip(m.View().Content)
	if !strings.Contains(v, "目前沒有播放內容") || !strings.Contains(v, "⚠ timeout(第 2 次)") {
		t.Errorf("無內容與錯誤列:\n%s", v)
	}
}

func TestNowWatchNeedsTTYAndUsesSeam(t *testing.T) {
	newPlayFake(t)
	if _, err := runCLI(t, "now", "--watch"); err == nil || !strings.Contains(err.Error(), "終端機") {
		t.Fatalf("非 TTY 的 --watch 應報錯:%v", err)
	}
	orig := isInteractive
	isInteractive = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { isInteractive = orig })
	origRun := runWatch
	called := false
	runWatch = func(*cobra.Command, provider.Provider, provider.PlaybackController) error { called = true; return nil }
	t.Cleanup(func() { runWatch = origRun })
	if _, err := runCLI(t, "now", "--watch"); err != nil || !called {
		t.Fatalf("互動時應進入 watch:%v called=%v", err, called)
	}
}
