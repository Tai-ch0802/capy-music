package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

// TestTUIRoundsAreSingleFlight:舊鏈的那一輪還在飛時,控制鍵起的那一輪不另外問(TryLock 拿不到就讓,稍後再試)——
// 不然按一次鍵就對 Spotify 多打一次,晚到的那一輪還會蓋掉 tracker 的 shown / lastNow(web 的 /api/now 用同一把鎖)。
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
	for _, line := range []string{"pause", "seek 1:00", "now", "play", "play 派對動物", "play --id 1422652341", "play --pick", "pause --provider spotify", "pl list"} {
		m.typing = true
		m.input.SetValue(line)
		m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, true)
	}
	want := [][]string{
		{"/bin/capy", "pause", "--provider", "apple"},
		{"/bin/capy", "seek", "1:00", "--provider", "apple"},
		{"/bin/capy", "now", "--provider", "apple"},
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

// TestTUISlowRoundSaysSoAndDoesNotQueue:【#97 review 第 1 點】建 provider 要等 token 鎖(沒有上限,持有者可能停在 keychain
// 對話框),那個 ctx 又不能給期限。所以跟 web 一樣只限「等」:等了 tuiRoundWait 還沒回來,狀態列說「等待平台回應」;
// 之後的輪詢不排隊(拿不到鎖就讓),卡住的那一輪做完後恢復正常。
func TestTUISlowRoundSaysSoAndDoesNotQueue(t *testing.T) {
	orig := tuiRoundWait
	tuiRoundWait = 50 * time.Millisecond
	t.Cleanup(func() { tuiRoundWait = orig })
	f := newNowFake()
	blk := make(chan struct{})
	f.blockOn(blk)
	m := newTestTUI(t, &watchFake{})
	m.trk.now["spotify"] = f
	released := false
	t.Cleanup(func() {
		if !released {
			close(blk)
		}
		for deadline := time.Now().Add(5 * time.Second); !m.trk.pollMu.TryLock(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Error("卡住的那一輪沒有收尾")
				return
			}
		}
		m.trk.pollMu.Unlock()
	})

	msg := within(t, 2*time.Second, "第一輪", m.poll())
	next, cmd := m.Update(msg)
	m = next.(tuiModel)
	if !strings.Contains(m.statusLine(99), "等待平台回應") || cmd == nil {
		t.Fatalf("等太久要說一聲、鏈照走:%q cmd=%v", ansi.Strip(m.statusLine(99)), cmd)
	}
	msg = within(t, 2*time.Second, "第二輪(不可以排隊等鎖)", m.poll())
	next, cmd = m.Update(msg)
	m = next.(tuiModel)
	if n := f.calls.Load(); n != 1 || cmd == nil || m.fails != 0 {
		t.Errorf("上一輪還在飛:不另外問、鏈照走、不算失敗:State %d 次 cmd=%v fails=%d", n, cmd, m.fails)
	}
	if !strings.Contains(m.statusLine(99), "等待平台回應") {
		t.Errorf("真的結果回來之前,那句話要留著:%q", ansi.Strip(m.statusLine(99)))
	}

	close(blk)
	released = true
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if m.trk.pollMu.TryLock() {
			m.trk.pollMu.Unlock()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("卡住的那一輪沒有收尾")
		}
	}
	if m = pollOnce(t, m); strings.Contains(m.statusLine(99), "等待平台回應") || !strings.Contains(m.statusLine(99), "派對動物") {
		t.Errorf("恢復後顯示真的結果:%q", ansi.Strip(m.statusLine(99)))
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
