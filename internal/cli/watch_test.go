package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
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
func (f *watchFake) Seek(_ context.Context, ms int) error {
	f.calls = append(f.calls, fmt.Sprintf("seek:%d", ms))
	return nil
}
func (f *watchFake) SetVolume(_ context.Context, pct int) error {
	f.calls = append(f.calls, fmt.Sprintf("vol:%d", pct))
	return nil
}

func playingState() *provider.PlaybackState {
	return &provider.PlaybackState{
		Playing:    true,
		Track:      &provider.Track{Title: "派對動物", Artists: []string{"五月天"}, Album: "自傳", DurationMS: 249000},
		ProgressMS: 83000,
		Device:     provider.Device{Name: "MacBook Pro", Type: "Computer", VolumePct: 50, VolumeKnown: true},
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

// newTestWatch:now --watch 的 model。spotify 的 controller 已經建好(= pc),其他平台建不起來(= 沒登入);pin 空 = 跟隨。
func newTestWatch(t *testing.T, pc provider.PlaybackController, pin string) watchModel {
	t.Helper()
	return newWatchModel(context.Background(), testTracker(t, "spotify", pc), pin, "spotify", time.Millisecond)
}

// feed:把一則目前這條鏈的結果餵進去。
func feed(m watchModel, msg tuiStateMsg) (watchModel, tea.Cmd) {
	msg.gen = m.gen
	next, cmd := m.Update(msg)
	return next.(watchModel), cmd
}

// watchRound:真的問一輪(走 tracker)並套用;回傳套用後的 model(排的 tick 不跑)。
func watchRound(t *testing.T, m watchModel) watchModel {
	t.Helper()
	msg, ok := runCmd(m.poll()).(tuiStateMsg)
	if !ok {
		t.Fatalf("一輪應回 tuiStateMsg:%#v", msg)
	}
	next, _ := m.Update(msg)
	return next.(watchModel)
}

func TestWatchPollTickLoop(t *testing.T) {
	m := newTestWatch(t, &watchFake{st: playingState()}, "")
	sm, ok := runCmd(m.Init()).(tuiStateMsg) // Init = 立刻問一輪
	if !ok || sm.st == nil || sm.err != nil {
		t.Fatalf("Init 應回一輪的結果:%#v", sm)
	}
	next, cmd := m.Update(sm)
	m = next.(watchModel)
	if m.st == nil || m.st.Track.Title != "派對動物" || cmd == nil {
		t.Fatal("收到狀態應存起來並排下一次 tick")
	}
	tick, ok := runCmd(cmd).(watchTickMsg)
	if !ok || tick.gen != m.gen {
		t.Fatalf("狀態之後應是這條鏈的 tick:%#v", tick)
	}
	_, cmd = m.Update(tick)
	if _, ok := runCmd(cmd).(tuiStateMsg); !ok {
		t.Fatal("tick 之後應再問一輪")
	}
}

func TestWatchKeys(t *testing.T) {
	f := &watchFake{st: playingState()}
	m := watchRound(t, newTestWatch(t, f, ""))
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
		if _, ok := runCmd(cmd).(tuiStateMsg); !ok || len(f.calls) != 1 || f.calls[0] != c.want {
			t.Errorf("%s:calls=%v(控制後應立刻重新問一輪)", c.key, f.calls)
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

// TestWatchKeysDoNotMultiplyPolling:【決策 58 順手修】每個控制鍵都開一條新的輪詢鏈,舊鏈要停:舊鏈的 tick 到了不再問、
// 舊鏈遲到的結果不再排 tick。以前沒有世代號,按五次 n 就有六條鏈一起每 2 秒問(Apple 不快取:每條鏈一支 osascript)。
func TestWatchKeysDoNotMultiplyPolling(t *testing.T) {
	m := watchRound(t, newTestWatch(t, &watchFake{st: playingState()}, ""))
	old := m.gen
	for range 5 {
		next, _ := m.Update(tea.KeyPressMsg{Code: 'n'})
		m = next.(watchModel)
	}
	if m.gen != old+5 {
		t.Fatalf("每個控制鍵開一條新鏈:gen %d → %d", old, m.gen)
	}
	if _, cmd := m.Update(watchTickMsg{gen: old}); cmd != nil {
		t.Error("舊鏈的 tick 不可以再問一輪")
	}
	if _, cmd := m.Update(tuiStateMsg{st: playingState(), gen: old}); cmd != nil {
		t.Error("舊鏈遲到的結果不可以再排 tick")
	}
	// 舊鏈的控制錯誤照樣要說(使用者剛按的鍵失敗了),但不可以替目前這條鏈多排一個 tick——那就是兩條鏈
	if next, cmd := m.Update(tuiStateMsg{err: provider.ErrNoActiveDevice, fromCtl: true, gen: old}); cmd != nil || next.(watchModel).note == "" {
		t.Errorf("舊鏈的控制錯誤:要說、不排 tick(cmd=%v)", cmd != nil)
	}
	if _, cmd := m.Update(watchTickMsg{gen: m.gen}); cmd == nil {
		t.Error("目前這條鏈的 tick 要照常問")
	}
}

func TestWatchConsecutiveFailuresQuit(t *testing.T) {
	m := newTestWatch(t, &watchFake{}, "")
	boom := errors.New("boom")
	var cmd tea.Cmd
	for i := 1; i < watchMaxFails; i++ {
		m, cmd = feed(m, tuiStateMsg{err: boom})
		if m.fatal != nil || isQuit(cmd) || m.fails != i {
			t.Fatalf("第 %d 次失敗不該離開(fails=%d fatal=%v)", i, m.fails, m.fatal)
		}
	}
	m, _ = feed(m, tuiStateMsg{st: playingState()}) // 一次成功就歸零
	if m.fails != 0 || m.err != nil {
		t.Fatal("成功應歸零失敗計數")
	}
	for range watchMaxFails {
		m, cmd = feed(m, tuiStateMsg{err: boom})
	}
	if m.fatal == nil || !isQuit(cmd) || !strings.Contains(m.fatal.Error(), "boom") {
		t.Fatalf("連續 %d 次失敗應離開並帶原因:%v", watchMaxFails, m.fatal)
	}
}

// TestWatchCachedErrorsDoNotCount:【決策 58】tracker 把 Spotify 的錯誤留 15 秒,每 2 秒端出來的那一則是同一件事:
// 只有真的問到的失敗才算,不然一次 502 不到十秒就把畫面關掉。
func TestWatchCachedErrorsDoNotCount(t *testing.T) {
	m := newTestWatch(t, &watchFake{}, "")
	m, _ = feed(m, tuiStateMsg{err: errors.New("502 bad gateway")})
	var cmd tea.Cmd
	for range 3 * watchMaxFails {
		m, cmd = feed(m, tuiStateMsg{err: errors.New("502 bad gateway"), cached: true})
	}
	if m.fatal != nil || isQuit(cmd) || m.fails != 1 {
		t.Fatalf("快取的同一則錯誤不重複算:fails=%d fatal=%v", m.fails, m.fatal)
	}
}

// TestWatchFollowsThePlayingPlatform:【決策 58】沒釘住:預設平台閒置、Apple 在播 → 畫面換到 Apple(上一家的曲目與失敗次數
// 不留);Apple 暫停了也不會被閒置的預設平台拉回去(只有正在播的能把畫面拉走,同決策 51)。
func TestWatchFollowsThePlayingPlatform(t *testing.T) {
	fakeNowClock(t)
	sp := newNowFake()
	sp.set(nil, nil) // Spotify 閒置
	ap := newNowFakeAs("apple", &provider.PlaybackState{Playing: true, Track: &provider.Track{Title: "Sugar", DurationMS: 235000}})
	m := newTestWatch(t, &watchFake{}, "")
	m.trk.now["spotify"], m.trk.now["apple"] = sp, ap
	m.fails = 3
	m = watchRound(t, m)
	if m.provID != "apple" || m.st == nil || m.st.Track.Title != "Sugar" || m.fails != 0 {
		t.Fatalf("要換到正在播的 Apple、失敗次數歸零:%s %#v fails=%d", m.provID, m.st, m.fails)
	}
	ap.set(&provider.PlaybackState{Playing: false, Track: &provider.Track{Title: "Sugar", DurationMS: 235000}}, nil)
	m = watchRound(t, m)
	if m.provID != "apple" || m.st == nil || m.st.Playing {
		t.Fatalf("暫停的 Apple 留在畫面上,不被閒置的 Spotify 拉回去:%s %#v", m.provID, m.st)
	}
	// 換到一家正在出錯的平台(決策 54:預設平台建不起來時退到別家):上一家的曲目不能留著,失敗次數從頭算
	m.fails = 3
	m, _ = feed(m, tuiStateMsg{provider: "spotify", err: errors.New("502 bad gateway")})
	if m.provID != "spotify" || m.st != nil || m.fails != 1 {
		t.Fatalf("換平台:曲目清掉、失敗次數從頭算:%s %#v fails=%d", m.provID, m.st, m.fails)
	}
}

// TestWatchPinnedStaysPut:--provider 釘住就只問那一家,別家在播也不換。
func TestWatchPinnedStaysPut(t *testing.T) {
	fakeNowClock(t)
	sp := newNowFake()
	sp.set(nil, nil)
	ap := newNowFakeAs("apple", &provider.PlaybackState{Playing: true, Track: &provider.Track{Title: "Sugar"}})
	m := newTestWatch(t, &watchFake{}, "spotify")
	m.trk.now["spotify"], m.trk.now["apple"] = sp, ap
	m = watchRound(t, m)
	if m.provID != "spotify" || m.st != nil || ap.calls.Load() != 0 {
		t.Fatalf("釘住的只問 Spotify:%s %#v Apple 被問了 %d 次", m.provID, m.st, ap.calls.Load())
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'n'}) // 按鍵之後重問的那一輪也要釘住,不然之後的鍵都送去 Apple
	m, _ = feed(next.(watchModel), runCmd(cmd).(tuiStateMsg))
	if m.provID != "spotify" || ap.calls.Load() != 0 {
		t.Fatalf("按鍵後的那一輪也要釘住:%s Apple 被問了 %d 次", m.provID, ap.calls.Load())
	}
}

// TestWatchControlsGoToTheFollowedPlatform:【決策 58】跟到 Apple 之後,控制鍵送給畫面上的 Apple,不是預設平台。
func TestWatchControlsGoToTheFollowedPlatform(t *testing.T) {
	sp := &watchFake{st: nowTrack(false, "昨天那首", 1000, 200000)}
	ap := &watchFake{st: nowTrack(true, "Sugar", 30000, 235000)}
	m := newTestWatch(t, sp, "")
	m.trk.now["apple"] = ap
	m = watchRound(t, m)
	if m.provID != "apple" {
		t.Fatalf("前提:跟到 Apple:%q", m.provID)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	runCmd(cmd)
	if !slices.Equal(ap.calls, []string{"pause"}) || len(sp.calls) != 0 {
		t.Errorf("控制鍵要送給畫面上的 Apple:apple=%v spotify=%v", ap.calls, sp.calls)
	}
}

// TestWatchLateControlIsDropped:controller 還沒建好、建構又卡住(等 token 鎖):n 等超過 tuiRoundWait 才建好就不送了,照實說——
// 不然連按五次 n,鎖一放開就一口氣跳五首(#97 review;沒釘住時平台是輪詢中才建的,watch 也碰得到)。
func TestWatchLateControlIsDropped(t *testing.T) {
	shrinkRoundWait(t, 30*time.Millisecond)
	c := &countingNext{nowFake: newNowFake()}
	gate := make(chan struct{})
	m := newTestWatch(t, &watchFake{st: playingState()}, "")
	delete(m.trk.now, "spotify")
	stub := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id == "spotify" {
			<-gate
			return c, nil
		}
		return stub(ctx, id)
	}
	t.Cleanup(func() { newProvider = stub })
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'n'})
	next, pend := next.(watchModel).Update(within(t, 2*time.Second, "按鍵", cmd))
	m = next.(watchModel)
	time.Sleep(60 * time.Millisecond) // 建構卡得比 tuiRoundWait 久
	close(gate)
	next, _ = m.Update(within(t, 5*time.Second, "按鍵的結果", pend))
	if n := c.next.Load(); n != 0 {
		t.Errorf("太晚才建好的 n 不可以送:Next %d 次", n)
	}
	if note := next.(watchModel).note; !strings.Contains(note, "略過") {
		t.Errorf("要照實說略過了:%q", note)
	}
}

// TestWatchControlErrorHasNoAttemptCount:控制鍵失敗不帶輪詢的失敗次數(那是關畫面的預算,跟這個鍵無關)。
func TestWatchControlErrorHasNoAttemptCount(t *testing.T) {
	m := newTestWatch(t, &watchFake{}, "")
	boom := errors.New("502 bad gateway")
	for _, msg := range []tuiStateMsg{{st: playingState()}, {err: boom}, {err: boom, cached: true}, {err: boom}} {
		m, _ = feed(m, msg)
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: 'n'})
	m, _ = feed(next.(watchModel), tuiStateMsg{err: provider.ErrNoActiveDevice, fromCtl: true})
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "⚠ "+provider.ErrNoActiveDevice.Error()+"\n") || strings.Contains(v, "第 2 次") {
		t.Errorf("控制鍵的錯不帶失敗次數:\n%s", v)
	}
}

// TestWatchQuitsWhenNothingCanBuild:沒釘住、哪一家都建不起來(沒登入、不支援):離開,錯誤原樣回(同以前開畫面之前的那個錯,
// 不經 friendlyErr)。
func TestWatchQuitsWhenNothingCanBuild(t *testing.T) {
	m := newWatchModel(context.Background(), testTracker(t, "nowhere", &watchFake{}), "", "spotify", time.Millisecond)
	msg, _ := runCmd(m.Init()).(tuiStateMsg)
	next, cmd := m.Update(msg)
	m = next.(watchModel)
	if !isQuit(cmd) || m.exitErr() == nil || m.exitErr().Error() != "spotify:沒登入" {
		t.Fatalf("建不起來要離開並原樣回錯:%v", m.exitErr())
	}
}

func TestWatchViewFixedWidth(t *testing.T) {
	m := newTestWatch(t, &watchFake{}, "")
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

// TestNowWatchNeedsTTYAndUsesSeam:沒有 TTY 照舊報錯;沒用 --provider 就跟隨(不先建平台);釘住的照舊先建好再開畫面,
// 平台名打錯一開始就失敗。
func TestNowWatchNeedsTTYAndUsesSeam(t *testing.T) {
	newPlayFake(t)
	if _, err := runCLI(t, "now", "--watch"); err == nil || !strings.Contains(err.Error(), "終端機") {
		t.Fatalf("非 TTY 的 --watch 應報錯:%v", err)
	}
	orig := isInteractive
	isInteractive = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { isInteractive = orig })
	origRun := runWatch
	var calls []string
	runWatch = func(_ *cobra.Command, pin string, seed provider.PlaybackController) error {
		calls = append(calls, fmt.Sprintf("%q seed=%v", pin, seed != nil))
		return nil
	}
	t.Cleanup(func() { runWatch = origRun })
	for _, args := range [][]string{{"now", "--watch"}, {"now", "--watch", "--provider", "spotify"}} {
		if _, err := runCLI(t, args...); err != nil {
			t.Fatalf("%v:%v", args, err)
		}
	}
	origNew := newProvider
	newProvider = func(context.Context, string) (provider.Provider, error) { return nil, errors.New("沒登入") }
	t.Cleanup(func() { newProvider = origNew })
	if _, err := runCLI(t, "now", "--watch", "--provider", "spotify"); err == nil || !strings.Contains(err.Error(), "沒登入") {
		t.Errorf("釘住的平台建不起來:開畫面之前就失敗:%v", err)
	}
	if _, err := runCLI(t, "now", "--watch"); err != nil {
		t.Errorf("沒釘住不先建平台(交給 tracker,別家可能建得起來):%v", err)
	}
	if want := []string{`"" seed=false`, `"spotify" seed=true`, `"" seed=false`}; !slices.Equal(calls, want) {
		t.Errorf("跟隨 / 釘住:%q,要 %q", calls, want)
	}
}

func TestWatchAppleNotRunningIsStatusNotFailure(t *testing.T) {
	m := newTestWatch(t, &watchFake{}, "")
	notRunning := fmt.Errorf("Music.app 未執行:%w", provider.ErrPlayerNotRunning)
	var cmd tea.Cmd
	for range 2 * watchMaxFails {
		m, cmd = feed(m, tuiStateMsg{err: notRunning})
	}
	msg := runCmd(cmd) // tea.Tick 的 cmd 只能執行一次(timer 建立時就啟動,第二次會永遠等)
	if _, ok := msg.(watchTickMsg); !ok || m.fatal != nil || m.fails != 0 {
		t.Fatalf("Music 未執行應持續 tick、不算失敗:msg=%T fatal=%v fails=%d", msg, m.fatal, m.fails)
	}
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "Music.app 未執行") || strings.Contains(v, "第 ") {
		t.Errorf("畫面應顯示未執行、不帶失敗次數:\n%s", v)
	}
}

func TestNowSingleShotAppleNotRunningIsExitZero(t *testing.T) {
	f := &watchFake{err: fmt.Errorf("Music.app 未執行:%w", provider.ErrPlayerNotRunning)}
	setCLITestConfig(t)
	f.playFake.fakeProvider = fakeProvider{caps: provider.CapPlaybackControl}
	swapProviderWith(t, f)
	out, err := runCLI(t, "now")
	if err != nil || !strings.Contains(out, "Music.app 未執行") {
		t.Fatalf("單次 now 遇到 Music 未執行應印訊息並回 0:%v %q", err, out)
	}
}

func TestWatchControlErrorsDoNotCountAsFailures(t *testing.T) {
	m := newTestWatch(t, &watchFake{}, "")
	m, _ = feed(m, tuiStateMsg{st: playingState()})
	var cmd tea.Cmd
	for range 2 * watchMaxFails { // 播到清單最後一首連按 n
		m, cmd = feed(m, tuiStateMsg{err: provider.ErrNoActiveDevice, fromCtl: true})
	}
	if m.fatal != nil || m.fails != 0 || m.st == nil {
		t.Fatalf("控制指令的錯不得吃掉連線失敗的預算、也不得清掉狀態:fatal=%v fails=%d", m.fatal, m.fails)
	}
	if _, ok := runCmd(cmd).(watchTickMsg); !ok {
		t.Fatal("控制失敗後應繼續 tick")
	}
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, provider.ErrNoActiveDevice.Error()) {
		t.Errorf("控制錯誤要顯示:\n%s", v)
	}
}

// TestWatchRateLimitCooldownIsTheTrackers:【取代 TestRateLimitPollWaitsForRetryAfter】限流是狀態,但冷卻期內不可以再打:
// 以前 watch 自己照 Retry-After 延後下一次輪詢,現在冷卻由 tracker 守(決策 58,同 TUI)。畫面說幾點再試,
// 那是 tracker 冷卻的終點,不是從現在往後算。
func TestWatchRateLimitCooldownIsTheTrackers(t *testing.T) {
	advance := fakeNowClock(t)
	f := newNowFake()
	f.set(nil, &provider.RateLimitError{Seconds: 120, Message: "429"})
	m := newTestWatch(t, &watchFake{}, "")
	m.trk.now["spotify"] = f
	m = watchRound(t, m)
	advance(time.Minute)
	m = watchRound(t, m)
	if n := f.calls.Load(); n != 1 {
		t.Errorf("冷卻期內不可以再打:%d 次", n)
	}
	want := "限流中," + time.Date(2026, 9, 24, 12, 2, 0, 0, time.UTC).Local().Format("15:04:05") + " 再試"
	if v := ansi.Strip(m.View().Content); m.fails != 0 || m.fatal != nil || !strings.Contains(v, want) {
		t.Errorf("限流是狀態不是失敗,並說幾點再試(%s):fails=%d\n%s", want, m.fails, v)
	}
	advance(61 * time.Second)
	watchRound(t, m)
	if n := f.calls.Load(); n != 2 {
		t.Errorf("冷卻結束要再問:%d 次", n)
	}
}

// TestWatchSlowRoundSaysWaiting:建平台卡在 token 鎖或 keychain 對話框時,畫面先說在等,結果回來照常套用(限等不限做)。
func TestWatchSlowRoundSaysWaiting(t *testing.T) {
	m := newTestWatch(t, &watchFake{}, "")
	m.err, m.fails = errors.New("timeout"), 2 // 之前失敗過:「等待」不是第 3 次失敗,不帶次數
	pending := make(chan tuiStateMsg, 1)
	m, cmd := feed(m, tuiStateMsg{slow: true, pending: pending})
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "⚠ 等待平台回應…\n") {
		t.Errorf("等太久要說一聲、不帶失敗次數:\n%s", v)
	}
	pending <- tuiStateMsg{st: playingState(), gen: m.gen}
	msg, ok := runCmd(cmd).(tuiStateMsg)
	if !ok {
		t.Fatalf("要接著等同一個結果:%#v", msg)
	}
	next, _ := m.Update(msg)
	if m = next.(watchModel); m.st == nil || m.err != nil || m.note != "" {
		t.Errorf("結果回來要照常套用、清掉「等待」:%#v %v %q", m.st, m.err, m.note)
	}
}

// TestRunWatchQuietsStderrAndReturnsBuildErrorsRaw:runWatch 在程式執行期間把換 token 等鎖、429 退避的提示丟掉(輪詢期間也會建
// 平台),結束後還原;沒有任何平台建得起來時錯誤原樣回——包著 ErrAuthExpired 也一樣,不被 friendlyErr 改寫成「請重新登入」。
func TestRunWatchQuietsStderrAndReturnsBuildErrorsRaw(t *testing.T) {
	setCLITestConfig(t)
	var lock, backoff bytes.Buffer
	origLock, origBackoff := auth.LockStderr, provider.BackoffStderr
	auth.LockStderr, provider.BackoffStderr = &lock, &backoff
	t.Cleanup(func() { auth.LockStderr, provider.BackoffStderr = origLock, origBackoff })
	raw := fmt.Errorf("spotify 的 token 讀不到:%w", provider.ErrAuthExpired)
	origRun := tuiRunProgram
	tuiRunProgram = func(_ context.Context, m tea.Model, _ io.Writer, _ ...tea.ProgramOption) (tea.Model, error) {
		if auth.LockStderr != io.Discard || provider.BackoffStderr != io.Discard {
			t.Errorf("程式執行期間兩個提示都要丟掉:lock=%T backoff=%T", auth.LockStderr, provider.BackoffStderr)
		}
		wm := m.(watchModel)
		wm.fatal = nowBuildErr{raw}
		return wm, nil
	}
	t.Cleanup(func() { tuiRunProgram = origRun })
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runWatch(cmd, "", nil); err == nil || err.Error() != raw.Error() {
		t.Errorf("建不起來的錯要原樣回:%v", err)
	}
	if auth.LockStderr != &lock || provider.BackoffStderr != &backoff {
		t.Error("結束後要還原")
	}
}

// TestRunWatchWiring:runWatch 把釘住的平台、RunE 事先建好的那一家與 State 的逾時交給 model 與 tracker——少了哪一個,
// 釘住會變成跟隨、同一家建兩次、卡住的 osascript 永遠不回錯。
func TestRunWatchWiring(t *testing.T) {
	setCLITestConfig(t)
	var got watchModel
	origRun := tuiRunProgram
	tuiRunProgram = func(_ context.Context, m tea.Model, _ io.Writer, _ ...tea.ProgramOption) (tea.Model, error) {
		got = m.(watchModel)
		return m, nil
	}
	t.Cleanup(func() { tuiRunProgram = origRun })
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	seed := &watchFake{}
	if err := runWatch(cmd, "apple", seed); err != nil {
		t.Fatal(err)
	}
	if got.pin != "apple" || got.provID != "apple" || got.trk.now["apple"] != provider.PlaybackController(seed) || got.trk.timeout != tuiStateTimeout {
		t.Errorf("釘住:pin=%q provID=%q seed=%v timeout=%v", got.pin, got.provID, got.trk.now["apple"] == provider.PlaybackController(seed), got.trk.timeout)
	}
	if err := runWatch(cmd, "", nil); err != nil {
		t.Fatal(err)
	}
	if got.pin != "" || got.provID != loadDefaultProvider() || len(got.trk.now) != 0 || got.trk.timeout != tuiStateTimeout {
		t.Errorf("跟隨:pin=%q provID=%q now=%v timeout=%v", got.pin, got.provID, got.trk.now, got.trk.timeout)
	}
}
