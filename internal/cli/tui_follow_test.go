package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
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
	// 幾點再試是 tracker 冷卻的終點(第一次被限流的時間 + 120 秒),快取端出來的那一輪也一樣——不是 TUI 自己從現在往後算。
	want := "限流中," + time.Date(2026, 9, 24, 12, 2, 0, 0, time.UTC).Local().Format("15:04:05") + " 再試"
	if m.stalled || m.fails != 0 || !strings.Contains(ansi.Strip(m.statusLine(99)), want) {
		t.Errorf("限流是狀態不是失敗,並說幾點再試(%s):fails=%d stalled=%v %q", want, m.fails, m.stalled, ansi.Strip(m.statusLine(99)))
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
	// 再登入:建得起來了,「沒有播放遙控」要消失、按鍵要回來(以前這個狀態在啟動時就定死)。
	stub := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id == "spotify" {
			return f, nil
		}
		return stub(ctx, id)
	}
	t.Cleanup(func() { newProvider = stub })
	m = step(t, m, tuiExecMsg{args: []string{"auth", "login", "spotify"}}, false)
	if m = pollOnce(t, m); m.pcErr != nil || strings.Contains(m.statusLine(99), "沒有播放遙控") {
		t.Errorf("登入之後要恢復:pcErr=%v %q", m.pcErr, ansi.Strip(m.statusLine(99)))
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

// TestTUIRoundsAreSingleFlight:控制鍵起的那一輪要等舊鏈在飛的那一輪做完,看得到它剛寫的快取——不然按一次鍵就對 Spotify
// 多打一次,晚到的那一輪還會蓋掉 tracker 的 shown / lastNow(web 的 /api/now 用同一把鎖)。
func TestTUIRoundsAreSingleFlight(t *testing.T) {
	f := newNowFake()
	f.set(nil, nil) // 閒置:有效期 15 秒
	blk := make(chan struct{})
	f.blockOn(blk)
	m := newTestTUI(t, &watchFake{})
	m.trk.now["spotify"] = f
	first := make(chan tea.Msg, 1)
	go func() { first <- m.poll()() }()
	for deadline := time.Now().Add(5 * time.Second); f.calls.Load() < 1; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("第一輪沒有進到 State")
		}
	}
	second := make(chan tea.Msg, 1)
	go func() { second <- m.poll()() }()
	time.Sleep(50 * time.Millisecond) // 沒有鎖的話,第二輪這時已經進到 State 了
	close(blk)
	<-first
	<-second
	if n := f.calls.Load(); n != 1 {
		t.Errorf("第二輪要用第一輪剛寫的快取:State %d 次", n)
	}
}

// followingApple:預設 Spotify 沒在播、Apple 在播,狀態列已經跟到 Apple。
func followingApple(t *testing.T) (tuiModel, *watchFake, *watchFake) {
	t.Helper()
	sp := &watchFake{st: nowTrack(false, "昨天那首", 1000, 200000)}
	ap := &watchFake{st: nowTrack(true, "Sugar", 30000, 235000)}
	m := newTestTUI(t, sp)
	m.trk.now["apple"] = ap
	m = pollOnce(t, m)
	if m.provID != "apple" {
		t.Fatalf("前提:跟到 Apple:%q", m.provID)
	}
	return m, sp, ap
}

// TestTUITypedPlaybackCommandsFollowTheShownPlatform:【review】沒明指時,命令列打的 pause 也要送給狀態列上的那一家——
// 不然按鍵停的是 Apple、打 /pause 停的卻是 Spotify,同一個畫面兩個平台。搜尋類(play <查詢>、play --pick)與 play --id
// (id 是預設平台的 id 空間)照舊用預設平台,自己打了 --provider 不動(#97 review 第 4 點)。
func TestTUITypedPlaybackCommandsFollowTheShownPlatform(t *testing.T) {
	got := recordExec(t)
	m, _, _ := followingApple(t)
	for _, line := range []string{"pause", "seek 1:00", "now", "now --watch", "play", "play 派對動物", "play --id 1422652341", "play --pick", "pause --provider spotify", "pl list"} {
		m.typing = true
		m.input.SetValue(line)
		m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, true)
	}
	want := [][]string{
		{"/bin/capy", "pause", "--provider", "apple"},
		{"/bin/capy", "seek", "1:00", "--provider", "apple"},
		{"/bin/capy", "now", "--provider", "apple"},
		{"/bin/capy", "now", "--watch"}, // 自己會跟著正在播的平台走(決策 58):附加了反而釘死在按下去那一刻的平台
		{"/bin/capy", "play", "--provider", "apple"},
		{"/bin/capy", "play", "派對動物"},
		{"/bin/capy", "play", "--id", "1422652341"},
		{"/bin/capy", "play", "--pick"},
		{"/bin/capy", "pause", "--provider", "spotify"},
		{"/bin/capy", "pl", "list"},
	}
	if !slices.EqualFunc(*got, want, slices.Equal[[]string]) {
		t.Errorf("執行的參數:\n got %q\nwant %q", *got, want)
	}
}

// TestTUIPinnedStillPinsWatch:TUI 自己被 capy --provider apple 釘住時,命令列打的 now --watch 照舊附加(決策 58:只有沒釘住時
// 才讓 watch 自己跟隨)——不然從釘住的介面開出來的 watch 會跑去看別家。
func TestTUIPinnedStillPinsWatch(t *testing.T) {
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "apple", "apple", testTracker(t, "apple", &watchFake{}), watchPollSpotify)
	if got := m.withProviderFlag([]string{"now", "--watch"}); !slices.Equal(got, []string{"now", "--watch", "--provider", "apple"}) {
		t.Errorf("釘住的介面照舊附加:%q", got)
	}
}

// TestTUISwitchDropsThePreviousPlatformsTrack:【review】換到一家正在出錯(或限流)的平台時,上一家的曲目不能留在狀態列上——
// 出錯與限流的分支不動 st,留下來的話 ←→ 會拿 Apple 的進度去 seek Spotify。
func TestTUISwitchDropsThePreviousPlatformsTrack(t *testing.T) {
	recordPrintln(t)
	m, _, _ := followingApple(t)
	m = step(t, m, tuiStateMsg{provider: "spotify", err: errors.New("502 bad gateway"), gen: m.gen}, false)
	if m.st != nil {
		t.Errorf("Apple 的那首不能掛在 Spotify 名下:%+v", m.st)
	}
	if _, ok := m.seekTarget(true); ok {
		t.Error("沒有基準點就不 seek")
	}
}

// failingPause:Pause 送不出去(沒有作用中的裝置)。
type failingPause struct{ *nowFake }

func (failingPause) Pause(context.Context) error { return provider.ErrNoActiveDevice }

// TestTUIFailedControlExpiresTheCache:【review】按鍵送不出去時,快取的狀態可能就是錯的(以為在播):下一輪要重問,
// 不然十秒內再按空白鍵還是照舊送 Pause。
func TestTUIFailedControlExpiresTheCache(t *testing.T) {
	fakeNowClock(t)
	recordPrintln(t)
	f := newNowFake() // 在播:有效期最多 10 秒
	m := newTestTUI(t, &watchFake{})
	m.trk.now["spotify"] = failingPause{f}
	m = pollOnce(t, m)
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeySpace}, true)
	pollOnce(t, m)
	if n := f.calls.Load(); n != 2 {
		t.Errorf("送不出去之後要重問:State %d 次", n)
	}
}

// TestTUIRetryAsksAgain:【review】停擺後按 r 要真的重問,不是端出快取裡的同一則錯誤(Spotify 出錯的結果留 15 秒)。
func TestTUIRetryAsksAgain(t *testing.T) {
	fakeNowClock(t)
	recordPrintln(t)
	f := newNowFake()
	f.set(nil, errors.New("502 bad gateway"))
	m := newTestTUI(t, &watchFake{})
	m.trk.now["spotify"] = f
	m = pollOnce(t, m)
	m.stalled = true
	step(t, m, tea.KeyPressMsg{Code: 'r'}, true)
	if n := f.calls.Load(); n != 2 {
		t.Errorf("按 r 要真的重問:State %d 次", n)
	}
}

// TestTUIControlKeyStartsSettle:【review】按鍵送出後進安定期:閒置的結果留 15 秒,不讓快取過期的話按了播放,狀態列要等 15 秒才跟上。
func TestTUIControlKeyStartsSettle(t *testing.T) {
	fakeNowClock(t)
	f := newNowFake()
	f.set(nil, nil)
	m := newTestTUI(t, &watchFake{})
	m.st = nil
	m.trk.now["spotify"] = f
	m = pollOnce(t, m)
	step(t, m, tea.KeyPressMsg{Code: tea.KeySpace}, true)
	if n := f.calls.Load(); n != 2 {
		t.Errorf("按了播放要馬上重問:State %d 次", n)
	}
}

// TestTUIProviderFlagPins:【review】capy --provider spotify:只問 Spotify,Apple 在播也不換過去,也不記 shown。
func TestTUIProviderFlagPins(t *testing.T) {
	sp := &watchFake{st: nowTrack(false, "昨天那首", 1000, 200000)}
	ap := newNowFakeAs("apple", nowTrack(true, "Sugar", 30000, 235000))
	trk := testTracker(t, "spotify", sp)
	trk.now["apple"] = ap
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "spotify", "spotify", trk, watchPollSpotify)
	m = pollOnce(t, m)
	if m.provID != "spotify" || ap.calls.Load() != 0 || trk.shown.Load() != nil {
		t.Errorf("釘住就只問 spotify:provID=%q apple State %d 次 shown=%v", m.provID, ap.calls.Load(), trk.shown.Load())
	}
}

// within:在 d 之內拿到 f 的結果,不然判失敗(被測的東西要是會卡住,測試不能跟著卡到 go test 的十分鐘上限)。
func within(t *testing.T, d time.Duration, what string, f func() tea.Msg) tea.Msg {
	t.Helper()
	got := make(chan tea.Msg, 1)
	go func() { got <- f() }()
	select {
	case msg := <-got:
		return msg
	case <-time.After(d):
		t.Fatalf("%s:%v 內沒有回來", what, d)
		return nil
	}
}

// shrinkRoundWait:把 tuiRoundWait 縮短(正式是 3 秒)。
func shrinkRoundWait(t *testing.T, d time.Duration) {
	t.Helper()
	orig := tuiRoundWait
	tuiRoundWait = d
	t.Cleanup(func() { tuiRoundWait = orig })
}

// applyAll:送一個訊息、套用;回來的是「等太久」就接著等 pending 的那個結果再套用(applyState 回的 Cmd 就是在等它)。
func applyAll(t *testing.T, m tuiModel, msg tea.Msg) tuiModel {
	t.Helper()
	next, cmd := m.Update(msg)
	m = next.(tuiModel)
	if sm, ok := msg.(tuiStateMsg); ok && sm.slow {
		m = applyAll(t, m, within(t, 5*time.Second, "pending 的結果", cmd))
	}
	return m
}

// TestTUISlowRoundSaysSoThenDelivers:【#97 review 第 1 點】建 provider 要等 token 鎖(沒有上限,持有者可能停在 keychain
// 對話框),那個 ctx 又不能給期限。所以跟 web 一樣只限「等」:等了 tuiRoundWait 還沒回來,狀態列先說「等待平台回應」;
// 結果回來照常套用。按 r 或控制鍵起的那一輪排在後面,等的時候也說;不會另外去問平台。
func TestTUISlowRoundSaysSoThenDelivers(t *testing.T) {
	shrinkRoundWait(t, 50*time.Millisecond)
	f := newNowFake()
	blk := make(chan struct{})
	f.blockOn(blk)
	m := newTestTUI(t, &watchFake{})
	m.st = nil
	m.trk.now["spotify"] = f
	released := false
	t.Cleanup(func() {
		if !released {
			close(blk)
		}
	})

	msg := within(t, 2*time.Second, "第一輪", m.poll())
	next, pend := m.Update(msg)
	m = next.(tuiModel)
	if !strings.Contains(m.statusLine(99), "等待平台回應") || pend == nil {
		t.Fatalf("等太久要說一聲、接著等結果:%q", ansi.Strip(m.statusLine(99)))
	}
	m = step(t, m, tea.KeyPressMsg{Code: 'r'}, false) // r 清掉那句、開新鏈
	msg2 := within(t, 2*time.Second, "r 起的那一輪", m.poll())
	next, pend2 := m.Update(msg2)
	m = next.(tuiModel)
	if !strings.Contains(m.statusLine(99), "等待平台回應") || f.calls.Load() != 1 {
		t.Errorf("排在後面的那一輪也要說,而且不另外問:State %d 次 %q", f.calls.Load(), ansi.Strip(m.statusLine(99)))
	}

	close(blk)
	released = true
	m = applyAll(t, m, within(t, 5*time.Second, "舊鏈的結果", pend)) // 舊世代:丟掉
	m = applyAll(t, m, within(t, 5*time.Second, "新鏈的結果", pend2))
	if st := ansi.Strip(m.statusLine(99)); strings.Contains(st, "等待平台回應") || !strings.Contains(st, "派對動物") {
		t.Errorf("結果回來要照常顯示:%q", st)
	}
}

// TestTUIHungStateStillStalls:【delta 審查】State 卡到逾時(10 s)比畫面等的上限(3 s)久:那一輪的錯誤也要送到、照算失敗,
// 連續幾次就停擺、叫使用者按 r——不能只是一直說「等待平台回應」。Apple 的結果不快取(有效期 0),每輪都是真的問到的。
func TestTUIHungStateStillStalls(t *testing.T) {
	shrinkRoundWait(t, 10*time.Millisecond)
	recordPrintln(t)
	f := newNowFakeAs("apple", nil)
	blk := make(chan struct{})
	t.Cleanup(func() { close(blk) })
	f.blockOn(blk) // 永遠不放:每一輪都卡到 trk.timeout
	trk := testTracker(t, "apple", f)
	trk.timeout = 40 * time.Millisecond
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "apple", "apple", trk, watchPollSpotify)
	m.frozen = true
	for range tuiMaxFails {
		m = applyAll(t, m, within(t, 5*time.Second, "一輪", m.poll()))
	}
	if !m.stalled || m.fails != tuiMaxFails {
		t.Errorf("卡住的平台要照算失敗、停擺:fails=%d stalled=%v %q", m.fails, m.stalled, ansi.Strip(m.statusLine(99)))
	}
}

// countingNext:Next 記次數(看按鍵有沒有被送出去)。
type countingNext struct {
	*nowFake
	next atomic.Int32
}

func (c *countingNext) Next(context.Context) error { c.next.Add(1); return nil }

// TestTUILateControlIsDropped:【delta 審查】controller 還沒建好、建構又卡住(等 token 鎖):按鍵等超過 tuiRoundWait 才建好就不送了,
// 照實說一聲——不然連按五次 n,鎖一放開就一口氣跳五首。等的期間狀態列說「等待平台回應」。
func TestTUILateControlIsDropped(t *testing.T) {
	shrinkRoundWait(t, 30*time.Millisecond)
	recordPrintln(t)
	c := &countingNext{nowFake: newNowFake()}
	gate := make(chan struct{})
	m := newTestTUI(t, &watchFake{st: playingState()})
	delete(m.trk.now, "spotify") // 還沒建(例如剛 dropNow 過)
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
	m = next.(tuiModel)
	msg := within(t, 2*time.Second, "按鍵", cmd)
	next, pend := m.Update(msg)
	m = next.(tuiModel)
	if !strings.Contains(m.statusLine(99), "等待平台回應") {
		t.Errorf("等的期間要說:%q", ansi.Strip(m.statusLine(99)))
	}
	time.Sleep(60 * time.Millisecond) // 建構卡得比 tuiRoundWait 久
	close(gate)
	m = applyAll(t, m, within(t, 5*time.Second, "按鍵的結果", pend))
	if n := c.next.Load(); n != 0 {
		t.Errorf("太晚才建好的按鍵不可以送出去:Next %d 次", n)
	}
	if !strings.Contains(m.statusLine(99), "剛才那個操作失敗") {
		t.Errorf("要照實說這個鍵沒送:%q", ansi.Strip(m.statusLine(99)))
	}
}

// TestTUIAwaitRepanics:【delta 審查】一輪在自己的 goroutine 裡跑;它的 panic 要帶回 Cmd 的 goroutine 再丟,bubbletea 才會還原終端機
// (直接在裸 goroutine 裡 panic,程式死掉、終端機停在 raw mode)。等太久之後才 panic 的也一樣。
func TestTUIAwaitRepanics(t *testing.T) {
	shrinkRoundWait(t, 20*time.Millisecond)
	caught := func(f func()) (r any) {
		defer func() { r = recover() }()
		f()
		return nil
	}
	if r := caught(func() { tuiAwait(0, tuiRoundWait, func() tuiStateMsg { panic("boom") }) }); r == nil || !strings.Contains(fmt.Sprint(r), "boom") {
		t.Errorf("直接回來的那條要再丟:%v", r)
	}
	gate := make(chan struct{})
	msg := tuiAwait(0, tuiRoundWait, func() tuiStateMsg { <-gate; panic("late boom") })
	if !msg.slow {
		t.Fatalf("前提:等太久:%+v", msg)
	}
	close(gate)
	var late tuiStateMsg
	select {
	case late = <-msg.pending:
	case <-time.After(5 * time.Second):
		t.Fatal("panic 之後 pending 要收到東西,不然等它的 Cmd 會永遠卡住")
	}
	if r := caught(func() { late.repanic() }); r == nil || !strings.Contains(fmt.Sprint(r), "late boom") {
		t.Errorf("pending 那條也要再丟:%v", r)
	}
}

// TestRunTUIQuietsStderrWhileRunning:【delta 審查】runTUI 在程式執行期間把兩個提示丟掉,結束後還原——只測 helper 的話,
// runTUI 哪天漏叫它也不會有測試紅。
func TestRunTUIQuietsStderrWhileRunning(t *testing.T) {
	setCLITestConfig(t)
	var lock, backoff bytes.Buffer
	origLock, origBackoff := auth.LockStderr, provider.BackoffStderr
	auth.LockStderr, provider.BackoffStderr = &lock, &backoff
	t.Cleanup(func() { auth.LockStderr, provider.BackoffStderr = origLock, origBackoff })
	origRun := tuiRunProgram
	ran := false
	tuiRunProgram = func(_ context.Context, m tea.Model, _ io.Writer, _ ...tea.ProgramOption) (tea.Model, error) {
		ran = true
		if auth.LockStderr != io.Discard || provider.BackoffStderr != io.Discard {
			t.Errorf("程式執行期間兩個提示都要丟掉:lock=%T backoff=%T", auth.LockStderr, provider.BackoffStderr)
		}
		return m, nil
	}
	t.Cleanup(func() { tuiRunProgram = origRun })
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := runTUI(cmd); err != nil || !ran {
		t.Fatalf("runTUI:%v ran=%v", err, ran)
	}
	if auth.LockStderr != &lock || provider.BackoffStderr != &backoff {
		t.Error("結束後要還原")
	}
}

// TestTUIQuietStderr:【#97 review 第 1 點】等 token 鎖的提示(auth.LockStderr)與 429 退避的提示不能印進 TUI 的畫面——
// 以前只在畫面出來之前建一次 provider,跟著正在播的平台走之後,輪詢期間也會建。
func TestTUIQuietStderr(t *testing.T) {
	var lock, backoff bytes.Buffer
	origLock, origBackoff := auth.LockStderr, provider.BackoffStderr
	auth.LockStderr, provider.BackoffStderr = &lock, &backoff
	t.Cleanup(func() { auth.LockStderr, provider.BackoffStderr = origLock, origBackoff })
	restore := tuiQuietStderr()
	if auth.LockStderr != io.Discard || provider.BackoffStderr != io.Discard {
		t.Errorf("TUI 執行期間兩個提示都要丟掉:lock=%T backoff=%T", auth.LockStderr, provider.BackoffStderr)
	}
	restore()
	if auth.LockStderr != &lock || provider.BackoffStderr != &backoff {
		t.Error("離開後要還原")
	}
}

// TestTUIRetryWorksWithoutPlayback:【#97 review 第 2 點】沒有播放遙控(沒登入)時 r 也要能用:在別的終端機跑完
// capy auth login 之後,不必等建不起來的結果過期(一分鐘)。
func TestTUIRetryWorksWithoutPlayback(t *testing.T) {
	fakeNowClock(t)
	m := newTestTUI(t, &watchFake{})
	delete(m.trk.now, "spotify") // 還沒建:testTracker 的建構一律失敗 = 沒登入
	if m = pollOnce(t, m); m.pcErr == nil {
		t.Fatal("前提:沒登入 = 沒有播放遙控")
	}
	f := newNowFake()
	stub := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id == "spotify" {
			return f, nil
		}
		return stub(ctx, id)
	}
	t.Cleanup(func() { newProvider = stub })
	m = step(t, m, tea.KeyPressMsg{Code: 'r'}, false) // r 讓快取過期;它回的 Cmd 就是 poll,下面手動跑一次再套用
	m = pollOnce(t, m)
	if m.pcErr != nil {
		t.Errorf("別處登入後按 r 要馬上恢復:%v", m.pcErr)
	}
}

// ctlFake:Next 由測試決定怎麼回(卡到 ctx 結束、被限流…)。
type ctlFake struct {
	*nowFake
	next func(context.Context) error
}

func (c ctlFake) Next(ctx context.Context) error { return c.next(ctx) }

// TestTUIControlSendHasDeadline:【第三次審查】送出本身也從按鍵起限時:卡住的 Next(token 換發卡在鎖上、Music.app 卡住)
// 到期就放棄、照實說「不一定有生效」——不是等鎖放開才送出去,連按五次就一口氣跳五首。
func TestTUIControlSendHasDeadline(t *testing.T) {
	shrinkRoundWait(t, 30*time.Millisecond)
	got := recordPrintln(t)
	var sent atomic.Int32
	m := newTestTUI(t, &watchFake{st: playingState()})
	m.trk.now["spotify"] = ctlFake{newNowFake(), func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
			sent.Add(1)
			return nil
		}
	}}
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'n'})
	m = applyAll(t, next.(tuiModel), within(t, 2*time.Second, "按鍵", cmd))
	if sent.Load() != 0 || !strings.Contains(joined(got), "不一定有生效") {
		t.Errorf("到期就放棄並照實說:sent=%d %q", sent.Load(), joined(got))
	}
}

// TestTUIControlSendDoesNotSleepOnRateLimit:【第三次審查】按鍵碰到 429 不睡在 Retry-After 裡(睡醒才送就是晚到的那一下):
// 馬上照實說被限流,冷卻交給 tracker。
func TestTUIControlSendDoesNotSleepOnRateLimit(t *testing.T) {
	shrinkRoundWait(t, 1500*time.Millisecond)
	got := recordPrintln(t)
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"2"}}}
	m := newTestTUI(t, &watchFake{st: playingState()})
	m.trk.now["spotify"] = ctlFake{newNowFake(), func(ctx context.Context) error { return provider.Backoff(ctx, resp, 0) }}
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'n'})
	msg := within(t, 5*time.Second, "按鍵", cmd)
	if sm := msg.(tuiStateMsg); sm.slow || !sm.fromCtl {
		t.Fatalf("限流要馬上回,不是睡到截止時間:%+v", sm)
	}
	applyAll(t, next.(tuiModel), msg)
	if s := joined(got); strings.Contains(s, "不一定有生效") || !strings.Contains(s, "2") {
		t.Errorf("要說被限流,不是逾時:%q", s)
	}
}

// TestTUIStaleSlowControlStillReports:【第三次審查】等太久的那個鍵,期間又按了一次(舊的變 stale):它的結果照樣要收——
// 控制指令自己的錯要說,不能因為過期就連同 pending 一起丟掉。
func TestTUIStaleSlowControlStillReports(t *testing.T) {
	shrinkRoundWait(t, 30*time.Millisecond)
	got := recordPrintln(t)
	m := newTestTUI(t, &watchFake{st: playingState()})
	release := make(chan struct{}) // 拿到「等太久」之前不准回來:送出的截止時間與畫面等的上限一樣長,不擋的話誰先到是看排程
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	m.trk.now["spotify"] = ctlFake{newNowFake(), func(ctx context.Context) error { <-ctx.Done(); <-release; return ctx.Err() }}
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'n'})
	m = next.(tuiModel)
	slow := within(t, 2*time.Second, "第一個鍵", cmd)
	once.Do(func() { close(release) })
	next, _ = m.Update(tea.KeyPressMsg{Code: 'n'}) // 再按一次:第一個鍵的結果變 stale
	m = next.(tuiModel)
	next, pend := m.Update(slow)
	if pend == nil {
		t.Fatal("過期的「等太久」也要接著等它的結果")
	}
	if st := ansi.Strip(next.(tuiModel).statusLine(99)); strings.Contains(st, "等待平台回應") {
		t.Errorf("過期的那個不說「等待」(那是舊鏈的事,畫面上的是新鏈):%q", st)
	}
	applyAll(t, next.(tuiModel), within(t, 2*time.Second, "第一個鍵的結果", pend))
	if !strings.Contains(joined(got), "不一定有生效") {
		t.Errorf("過期的控制錯誤照樣要說:%q", joined(got))
	}
}

// TestTUISlowBranchRepanics:【第三次審查】等太久之後才 panic 的:applyState 接著等的那個 Cmd 要把 panic 再丟出來。
func TestTUISlowBranchRepanics(t *testing.T) {
	shrinkRoundWait(t, 20*time.Millisecond)
	m := newTestTUI(t, &watchFake{})
	gate := make(chan struct{})
	msg := tuiAwait(m.gen, tuiRoundWait, func() tuiStateMsg { <-gate; panic("late boom") })
	_, cmd := m.Update(msg)
	close(gate)
	var r any
	func() { // 在這個 goroutine 裡跑(within 的 goroutine 裡 panic 會直接讓測試程式死掉);gate 已經關了,馬上回來
		defer func() { r = recover() }()
		cmd()
	}()
	if r == nil || !strings.Contains(fmt.Sprint(r), "late boom") {
		t.Errorf("applyState 接著等的那個 Cmd 要再丟 panic:%v", r)
	}
}

// playFakeSlow:Play 等 release 才回(模擬 Music.app 冷啟動);ctx 先到期就回錯。
type playFakeSlow struct {
	*nowFake
	release chan struct{}
	played  *atomic.Int32
}

func (p playFakeSlow) Play(ctx context.Context, _ provider.PlayRequest) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
		p.played.Add(1)
		return nil
	}
}

// TestTUIIdempotentKeysWait:【第四次審查】播放 / 暫停、±10 秒、音量是冪等的,晚到也是同一個結果:照樣等,不受 n / p 那個截止時間限制——
// Music.app 沒開時按空白鍵要等它冷啟動完(可能超過三秒),那時殺掉 osascript,app 開了卻沒播。
func TestTUIIdempotentKeysWait(t *testing.T) {
	shrinkRoundWait(t, 30*time.Millisecond)
	got := recordPrintln(t)
	var played atomic.Int32
	f := playFakeSlow{newNowFake(), make(chan struct{}), &played}
	m := newTestTUI(t, &watchFake{})
	m.st = nil // 沒在播:空白鍵 = 播放
	m.trk.now["spotify"] = f
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	msg := within(t, 2*time.Second, "空白鍵", cmd)
	go func() { time.Sleep(100 * time.Millisecond); close(f.release) }() // 比截止時間久得多
	applyAll(t, next.(tuiModel), msg)
	if played.Load() != 1 || strings.Contains(joined(got), "不一定有生效") {
		t.Errorf("冪等的鍵照樣等到送出:played=%d %q", played.Load(), joined(got))
	}
}

// TestTUIFallsBackWhenDefaultCannotBuild:【#97 review 第 3 點】預設平台(Spotify)沒登入,Apple 登入了、只是暫停:狀態列顯示 Apple、
// 按鍵能用——不是整個介面只剩「沒有播放遙控」。
func TestTUIFallsBackWhenDefaultCannotBuild(t *testing.T) {
	m := newTestTUI(t, &watchFake{})
	delete(m.trk.now, "spotify") // testTracker 的建構一律失敗 = 沒登入
	m.trk.now["apple"] = &watchFake{st: nowTrack(false, "Sugar", 0, 235000)}
	if m = pollOnce(t, m); m.provID != "apple" || m.pcErr != nil || m.st == nil {
		t.Errorf("預設平台建不起來:改顯示 Apple:provID=%q pcErr=%v", m.provID, m.pcErr)
	}
}

// TestTUIRoundAfterQuitAsksNothing:TUI 結束(ctx 取消)之後才輪到的那一輪不再問平台——bubbletea 不等 Cmd,結束時排在 pollMu
// 後面的輪詢還會跑,不該再建 provider(可能跳 keychain 對話框)或打 API。測試的 testTracker 也靠這個在收尾時擋住遺留的那一輪。
func TestTUIRoundAfterQuitAsksNothing(t *testing.T) {
	f := newNowFake()
	m := newTestTUI(t, &watchFake{})
	m.trk.now["spotify"] = f
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.trk.ctx = ctx
	if msg := m.round()(); !msg.dropped || f.calls.Load() != 0 {
		t.Errorf("結束之後不該再問:dropped=%v State %d 次", msg.dropped, f.calls.Load())
	}
}

// TestTUIControlAfterQuitSendsNothing:【#104 review】控制鍵的 Cmd 在 ctx 取消後才跑到(按鍵後馬上收到訊號):不建 provider、不送。
func TestTUIControlAfterQuitSendsNothing(t *testing.T) {
	var sent atomic.Int32
	m := newTestTUI(t, &watchFake{st: playingState()})
	m.trk.now["spotify"] = ctlFake{newNowFake(), func(context.Context) error { sent.Add(1); return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.trk.ctx = ctx
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'n'})
	if msg, ok := within(t, 2*time.Second, "n", cmd).(tuiStateMsg); !ok || !msg.dropped || sent.Load() != 0 {
		t.Errorf("結束之後不該再送:%#v 送了 %d 次", msg, sent.Load())
	}
}
