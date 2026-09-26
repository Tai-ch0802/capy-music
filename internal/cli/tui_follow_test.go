package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// TUI 跟著正在播的平台走(Q64):跟隨規則與節流是 web 播放面板的那一套(決策 51,nowTracker)。

// pollOnce:問一輪並套用(poll 的 Cmd 是同步的 func,不是 tea.Tick)。
func pollOnce(t *testing.T, m tuiModel) tuiModel {
	t.Helper()
	return step(t, m, m.poll()(), false)
}

// TestTUIFollowsThePlayingPlatform:【fails-before-fix】預設是 Spotify、沒在播,Music.app 在播:狀態列要換成 Apple 的那首,
// 控制鍵送給 Apple。以前 TUI 一開就綁死 default_provider(使用者 2026-09-24 回報的 web 問題,TUI 也一樣)。
func TestTUIFollowsThePlayingPlatform(t *testing.T) {
	sp := &watchFake{st: nowTrack(false, "昨天那首", 1000, 200000)}
	ap := &watchFake{st: nowTrack(true, "Sugar", 30000, 235000)}
	m := newTestTUI(t, sp)
	m.trk.now["apple"] = ap
	m = pollOnce(t, m)
	if m.provID != "apple" || !strings.Contains(ansi.Strip(m.statusLine(99)), "Sugar") {
		t.Fatalf("要換到正在播的 Apple:provID=%q %q", m.provID, ansi.Strip(m.statusLine(99)))
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeySpace}, true)
	if !slices.Equal(ap.calls, []string{"pause"}) || len(sp.calls) != 0 {
		t.Errorf("控制鍵要送給狀態列上的 Apple:apple=%v spotify=%v", ap.calls, sp.calls)
	}
}

// TestTUIControlsGoToTheShownPlatform:控制鍵送給狀態列上**顯示中**的那一家(applyState 記下的),不是 tracker 當下認定的那一家——
// 在飛的那一輪可能已經換到別家,但畫面還沒顯示;按 n 的人要的是畫面上那首的下一首。
func TestTUIControlsGoToTheShownPlatform(t *testing.T) {
	sp := &watchFake{st: nowTrack(false, "昨天那首", 1000, 200000)}
	ap := &watchFake{st: nowTrack(true, "Sugar", 30000, 235000)}
	m := newTestTUI(t, sp)
	m.trk.now["apple"] = ap
	m = pollOnce(t, m)
	ap.st, sp.st = nowTrack(false, "Sugar", 31000, 235000), nowTrack(true, "昨天那首", 1000, 200000)
	_ = m.poll()() // tracker 已經換到 Spotify,這一輪的結果還沒進畫面
	if p := m.trk.shown.Load(); p == nil || *p != "spotify" {
		t.Fatalf("前提:tracker 已經換到 spotify:%v", p)
	}
	step(t, m, tea.KeyPressMsg{Code: 'n'}, true)
	if !slices.Equal(ap.calls, []string{"next"}) || len(sp.calls) != 0 {
		t.Errorf("要送給畫面上的 Apple:apple=%v spotify=%v", ap.calls, sp.calls)
	}
}

// TestTUICachedErrorsDoNotStall:只有真的問到的失敗才算連續失敗。Spotify 出錯的結果留 15 秒,TUI 每 2 秒問一次——
// 快取端出來的同一則錯誤也算的話,一次 502 不到十秒就停擺、叫使用者按 r。
func TestTUICachedErrorsDoNotStall(t *testing.T) {
	advance := fakeNowClock(t)
	recordPrintln(t)
	f := newNowFake()
	f.set(nil, errors.New("502 bad gateway"))
	m := newTestTUI(t, &watchFake{})
	m.trk.now["spotify"] = f
	for range tuiMaxFails + 1 {
		m = pollOnce(t, m)
		advance(2 * time.Second)
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("前提:12 秒內只真的問了一次:%d", n)
	}
	if m.stalled || m.fails != 1 {
		t.Errorf("快取的錯誤不算:fails=%d stalled=%v", m.fails, m.stalled)
	}
	advance(4 * time.Second) // 過了 15 秒:真的再問,這次算
	if m = pollOnce(t, m); m.fails != 2 {
		t.Errorf("真的再問到的失敗要算:fails=%d", m.fails)
	}
}

// TestTUIRateLimitCooldownIsTheTrackers:限流時 TUI 照常每 2 秒問,冷卻期內 tracker 只端出快取——Retry-After 之前不會真的再打。
// 以前 TUI 自己把下一個 tick 延到 Retry-After(rateLimitDelay);現在冷卻在 tracker,跟 web 同一份。
func TestTUIRateLimitCooldownIsTheTrackers(t *testing.T) {
	advance := fakeNowClock(t)
	f := newNowFake()
	f.set(nil, &provider.RateLimitError{Seconds: 120, Message: "429"})
	m := newTestTUI(t, &watchFake{})
	m.trk.now["spotify"] = f
	m = pollOnce(t, m)
	advance(time.Minute)
	m = pollOnce(t, m)
	if n := f.calls.Load(); n != 1 {
		t.Errorf("冷卻期內不可以再打:%d 次", n)
	}
	if m.stalled || m.fails != 0 || !strings.Contains(m.statusLine(99), "限流") {
		t.Errorf("限流是狀態不是失敗:fails=%d stalled=%v %q", m.fails, m.stalled, ansi.Strip(m.statusLine(99)))
	}
	advance(61 * time.Second)
	pollOnce(t, m)
	if n := f.calls.Load(); n != 2 {
		t.Errorf("冷卻結束要再問:%d 次", n)
	}
}

// TestTUIExecSettlesAndInvalidates:命令列跑完的子命令跟 web 的 /api/run 一樣收尾——play 之後進安定期(狀態列不必等 Spotify
// 閒置的 15 秒有效期),auth logout 之後丟掉快取的 controller(不然會一直用已登出帳號的那一個)。
func TestTUIExecSettlesAndInvalidates(t *testing.T) {
	fakeNowClock(t)
	f := newNowFake()
	f.set(nil, nil) // 閒置:有效期 15 秒
	m := newTestTUI(t, &watchFake{})
	m.trk.now["spotify"] = f
	m = pollOnce(t, m)
	m = pollOnce(t, m)
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("前提:閒置的結果有快取:%d", n)
	}
	m = step(t, m, tuiExecMsg{args: []string{"play", "派對動物"}}, false)
	m = pollOnce(t, m)
	if n := f.calls.Load(); n != 2 {
		t.Errorf("play 之後要馬上重問:%d", n)
	}
	m = step(t, m, tuiExecMsg{args: []string{"auth", "logout", "spotify"}}, false)
	if m = pollOnce(t, m); m.pcErr == nil {
		t.Error("登出之後要重建(testTracker 的建構一律失敗 = 沒登入),不可以繼續用舊的 controller")
	}
}

// TestTUIStateTimesOut:osascript 或 HTTP 卡住時,這一輪要回錯(算一次失敗,連續幾次就停擺、按 r 重試),
// 不是讓狀態列永遠停在上一首——web 不設(它有 webNowWait 與 staleNow),TUI 設。
func TestTUIStateTimesOut(t *testing.T) {
	f := newNowFake()
	blk := make(chan struct{})
	t.Cleanup(func() { close(blk) })
	f.blockOn(blk)
	m := newTestTUI(t, &watchFake{})
	m.trk.now["spotify"] = f
	m.trk.timeout = 50 * time.Millisecond
	got := make(chan tea.Msg, 1)
	go func() { got <- m.poll()() }()
	select {
	case msg := <-got:
		if sm := msg.(tuiStateMsg); sm.err == nil || sm.cached {
			t.Errorf("逾時要回錯,而且是真的問到的:%+v", sm)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("State 卡住時這一輪要逾時")
	}
}

// TestTUIDropDuringRoundDiscardsIt:問的期間 dropNow 過(命令列跑了 auth logout):那一輪是已登出帳號的結果,不顯示,但輪詢鏈照走。
func TestTUIDropDuringRoundDiscardsIt(t *testing.T) {
	f := newNowFake() // 在播「派對動物」
	blk := make(chan struct{})
	f.blockOn(blk)
	m := newTestTUI(t, &watchFake{})
	m.st = nil
	m.trk.now["spotify"] = f
	got := make(chan tea.Msg, 1)
	go func() { got <- m.poll()() }()
	for deadline := time.Now().Add(5 * time.Second); f.calls.Load() < 1; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("這一輪沒有進到 State")
		}
	}
	m.trk.dropNow(true)
	close(blk)
	next, cmd := m.Update(<-got)
	if next.(tuiModel).st != nil {
		t.Errorf("已登出帳號的那首不可以顯示:%+v", next.(tuiModel).st)
	}
	if cmd == nil {
		t.Error("輪詢鏈要照走")
	}
}
