package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// nowFake:可控的 PlaybackController(State 可回值、回錯、或卡住)。
type nowFake struct {
	fakeProvider
	id    string // 空 = spotify
	mu    sync.Mutex
	state *provider.PlaybackState
	err   error
	block chan struct{} // 非 nil:State 卡在這裡直到關閉
	calls atomic.Int32
}

func newNowFake() *nowFake {
	return &nowFake{
		fakeProvider: fakeProvider{caps: provider.CapPlaybackControl},
		state: &provider.PlaybackState{
			Playing:    true,
			Track:      &provider.Track{ProviderID: "t1", Title: "派對動物", Artists: []string{"五月天"}, DurationMS: 227000},
			ProgressMS: 61000,
			Device:     provider.Device{Name: "MacBook", Type: "Computer", VolumePct: 55, VolumeKnown: true},
		},
	}
}

func (f *nowFake) ID() string {
	if f.id != "" {
		return f.id
	}
	return "spotify"
}
func (f *nowFake) State(ctx context.Context) (*provider.PlaybackState, error) {
	f.calls.Add(1)
	f.mu.Lock()
	blk, st, err := f.block, f.state, f.err
	f.mu.Unlock()
	if blk != nil {
		select {
		case <-blk:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return st, err
}
func (f *nowFake) Devices(context.Context) ([]provider.Device, error) { return nil, nil }
func (f *nowFake) Play(context.Context, provider.PlayRequest) error   { return nil }
func (f *nowFake) Pause(context.Context) error                        { return nil }
func (f *nowFake) Next(context.Context) error                         { return nil }
func (f *nowFake) Prev(context.Context) error                         { return nil }
func (f *nowFake) Seek(context.Context, int) error                    { return nil }
func (f *nowFake) SetVolume(context.Context, int) error               { return nil }

func (f *nowFake) set(st *provider.PlaybackState, err error) {
	f.mu.Lock()
	f.state, f.err = st, err
	f.mu.Unlock()
}

func (f *nowFake) blockOn(ch chan struct{}) {
	f.mu.Lock()
	f.block = ch
	f.mu.Unlock()
}

// swapNow:把 newProvider 換成 nowFake,並數它被建了幾次(快取有沒有生效看這個)。
func swapNow(t *testing.T, f *nowFake) *int32 {
	t.Helper()
	var built int32
	orig := newProvider
	newProvider = func(context.Context, string) (provider.Provider, error) {
		atomic.AddInt32(&built, 1)
		return f, nil
	}
	t.Cleanup(func() { newProvider = orig })
	return &built
}

// swapNowByID:每個 id 各一個假物件;fakes 裡沒有的 id 建構失敗(= 沒登入)。回每個 id 的建構次數。
func swapNowByID(t *testing.T, fakes map[string]*nowFake) map[string]*atomic.Int32 {
	t.Helper()
	built := map[string]*atomic.Int32{}
	for _, id := range providerIDs {
		built[id] = &atomic.Int32{}
	}
	orig := newProvider
	newProvider = func(_ context.Context, id string) (provider.Provider, error) {
		built[id].Add(1)
		if f, ok := fakes[id]; ok {
			return f, nil
		}
		return nil, errors.New(id + ":沒登入")
	}
	t.Cleanup(func() { newProvider = orig })
	return built
}

// fakeNowClock:把 webNowClock 換成假時鐘(有效期、安定期、stale_ms 都看它),回一個往前推的函式。
func fakeNowClock(t *testing.T) func(time.Duration) {
	t.Helper()
	var mu sync.Mutex
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	orig := webNowClock
	webNowClock = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	t.Cleanup(func() { webNowClock = orig })
	return func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
}

// nowTrack:在播或暫停、有曲目的狀態。
func nowTrack(playing bool, title string, posMS, durMS int) *provider.PlaybackState {
	return &provider.PlaybackState{Playing: playing, ProgressMS: posMS,
		Track:  &provider.Track{ProviderID: title, Title: title, DurationMS: durMS},
		Device: provider.Device{Name: "Dev"}}
}

func newNowFakeAs(id string, st *provider.PlaybackState) *nowFake {
	f := newNowFake()
	f.id, f.state = id, st
	return f
}

// setDefaultProvider:寫進測試的 config(web 的面板每一輪重讀 default_provider)。
func setDefaultProvider(t *testing.T, id string) {
	t.Helper()
	if _, err := runCLI(t, "config", "set", "default_provider", id); err != nil {
		t.Fatal(err)
	}
}

func (c *webClient) now(query string) map[string]any {
	c.t.Helper()
	resp := c.req(context.Background(), http.MethodGet, "/api/now"+query, nil, nil)
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		c.t.Fatalf("/api/now 的回應不是 JSON:%v", err)
	}
	if resp.StatusCode != 200 {
		c.t.Fatalf("/api/now → %d %v", resp.StatusCode, m)
	}
	return m
}

// TestWebNowReportsStateAndCachesController:【fails-before-fix】Spotify 在播時 10 秒內的輪詢用快取回答(進度照經過時間往前推),
// 過了有效期才再打一次 State——以前每 2.5 秒一輪都打,一分鐘 24 次 Web API(計畫 2026-09-24 §1.3)。
func TestWebNowReportsStateAndCachesController(t *testing.T) {
	setCLITestConfig(t)
	advance := fakeNowClock(t)
	f := newNowFake()
	built := swapNow(t, f)
	_, c := startWeb(t)

	m := c.now("")
	if m["provider"] != "spotify" || m["playing"] != true || m["position_ms"].(float64) != 61000 || m["stale"] != false {
		t.Fatalf("%v", m)
	}
	tr := m["track"].(map[string]any)
	if tr["title"] != "派對動物" || tr["duration_ms"].(float64) != 227000 {
		t.Errorf("track:%v", tr)
	}
	dev := m["device"].(map[string]any)
	if dev["name"] != "MacBook" || dev["volume_pct"].(float64) != 55 || dev["volume_known"] != true {
		t.Errorf("device:%v", dev)
	}
	c.now("")
	c.now("")
	if n := atomic.LoadInt32(built); n != 1 {
		t.Errorf("PlaybackController 只建一次,得到 %d", n)
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("有效期內不再打 State:%d 次", n)
	}
	advance(4 * time.Second)
	if m := c.now(""); m["position_ms"].(float64) != 65000 || m["stale"] != false || f.calls.Load() != 1 {
		t.Errorf("快取回答的那一輪,進度要往前推 4 秒、不算 stale、不打 State:%v(State %d 次)", m, f.calls.Load())
	}
	advance(7 * time.Second)
	c.now("")
	if n := f.calls.Load(); n != 2 {
		t.Errorf("過了 10 秒的有效期要再問一次 State:%d 次", n)
	}
}

// TestWebNowUsesCachedControllerAndInvalidatesOnlyAfterAuthOrConfigSet:一般命令不作廢;auth / config set 才作廢。
func TestWebNowUsesCachedControllerAndInvalidatesOnlyAfterAuthOrConfigSet(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"capy auth login spotify", true}, {"capy auth logout google", true}, {"capy auth status", false},
		{"capy config set", true}, {"capy config list", false}, {"capy search", false}, {"capy pl pull", false},
	} {
		if got := webNowInvalidatedBy(tc.path); got != tc.want {
			t.Errorf("%q → %v,要 %v", tc.path, got, tc.want)
		}
	}
	setCLITestConfig(t)
	f := newNowFake()
	built := swapNow(t, f)
	_, c := startWeb(t)
	c.now("")
	if _, ev, _ := c.run(map[string]any{"args": []string{"config", "list"}}); evExit(t, ev)["code"] != float64(0) {
		t.Fatal(ev)
	}
	c.now("")
	if n := atomic.LoadInt32(built); n != 1 {
		t.Errorf("一般命令跑完不該作廢快取,得到 %d 次建構", n)
	}
	if _, ev, _ := c.run(map[string]any{"args": []string{"config", "set", "default_provider", "spotify"}}); evExit(t, ev)["code"] != float64(0) {
		t.Fatal(ev)
	}
	c.now("")
	if n := atomic.LoadInt32(built); n != 2 {
		t.Errorf("config set 之後要重建,得到 %d 次建構", n)
	}
}

func TestWebNowDropsCacheOnStateError(t *testing.T) {
	setCLITestConfig(t)
	advance := fakeNowClock(t)
	f := newNowFake()
	built := swapNowByID(t, map[string]*nowFake{"spotify": f}) // 共用的假物件會讓 apple / local 也跟著回錯、跟著被建
	_, c := startWeb(t)
	c.now("")
	f.set(nil, provider.ErrAuthExpired)
	advance(11 * time.Second) // 過了在播的有效期
	m := c.now("")
	if m["error"] == nil || m["track"] != nil {
		t.Fatalf("State 回錯要 200 帶 error:%v", m)
	}
	f.set(newNowFake().state, nil)
	advance(webNowFailTTL + time.Second) // 出錯的結果一分鐘內不重問
	c.now("")
	if n := built["spotify"].Load(); n != 2 {
		t.Errorf("token 失效的 controller 不留著,下次要重建:%d", n)
	}
}

// TestWebNowPollBoundedWhileStateStuck:State 卡住(等 token 鎖就是這個情況)時 handler 不跟著卡——
// webNowWait 到就回 stale 快照;背景那個 poll 用伺服器 ctx 跑完(refresh 不被腰斬),放開後下一次又是新鮮的。
func TestWebNowPollBoundedWhileStateStuck(t *testing.T) {
	setCLITestConfig(t)
	origWait := webNowWait
	webNowWait = 100 * time.Millisecond
	t.Cleanup(func() { webNowWait = origWait })
	advance := fakeNowClock(t)
	f := newNowFake()
	swapNow(t, f)
	_, c := startWeb(t)
	c.now("")                 // 先有一份快照
	advance(11 * time.Second) // 過了有效期,下一輪才會真的問(然後卡住)

	blk := make(chan struct{})
	f.blockOn(blk)
	start := time.Now()
	m := c.now("")
	if !m["stale"].(bool) || time.Since(start) > 3*time.Second {
		t.Fatalf("卡住時要在 webNowWait 內回 stale 快照:stale=%v 耗時 %v", m["stale"], time.Since(start))
	}
	if m["track"] == nil {
		t.Error("stale 回的是上一份快照,不是空的")
	}
	// 第二次 poll 還在飛:TryLock 失敗,立刻回 stale,不排隊、不堆 goroutine。
	start = time.Now()
	if m2 := c.now(""); !m2["stale"].(bool) || time.Since(start) > time.Second {
		t.Errorf("單飛:第二次要立刻回 stale,耗時 %v", time.Since(start))
	}
	close(blk)
	f.blockOn(nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if m := c.now(""); !m["stale"].(bool) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("放開之後應該恢復新鮮")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestWebNowNotBlockedByRunningJob:面板輪詢不進 runMu——序列槽被握著時仍要回。
// TestWebNowStaleNeverCrossesProvider:【fails-before-fix】有快照但問的是另一家時,不可以把上一家的歌端出來。
// 走得到的路:面板正在輪詢 spotify → 使用者 config set default_provider apple → dropNow → 下一次 poll 要重建
// provider 又要打平台,超過 webNowWait → handler 回 staleNow("apple"),而快照是 spotify 的。
func TestWebNowStaleNeverCrossesProvider(t *testing.T) {
	setCLITestConfig(t)
	origWait := webNowWait
	webNowWait = 100 * time.Millisecond
	t.Cleanup(func() { webNowWait = origWait })
	f := newNowFake()
	swapNow(t, f)
	s, c := startWeb(t)
	if m := c.now("?provider=spotify"); m["track"] == nil {
		t.Fatal("先要有一份 spotify 的新鮮快照")
	}
	blk := make(chan struct{})
	defer close(blk)
	f.blockOn(blk)
	m := c.now("?provider=apple")
	if m["provider"] != "apple" || !m["stale"].(bool) {
		t.Fatalf("卡住時要回 apple 的 stale:%v", m)
	}
	if m["track"] != nil {
		t.Errorf("不可以把 spotify 的快照當成 apple 的:%v", m["track"])
	}
	// 直接對 staleNow 再釘一次(不經 HTTP):同一家才沿用。
	if got := s.staleNow("apple"); got.Track != nil || got.Provider != "apple" {
		t.Errorf("staleNow(apple):%+v", got)
	}
	if got := s.staleNow("spotify"); got.Track == nil {
		t.Error("同一家還是要沿用上一份快照")
	}
}

// TestWebNowDropAlsoClearsSnapshot:【fails-before-fix】作廢的理由是帳號 / 平台變了,那份快照正好是舊帳號的內容。
func TestWebNowDropAlsoClearsSnapshot(t *testing.T) {
	setCLITestConfig(t)
	f := newNowFake()
	swapNow(t, f)
	s, c := startWeb(t)
	if m := c.now(""); m["track"] == nil {
		t.Fatal("先要有快照")
	}
	s.dropNow(false)
	if got := s.staleNow("spotify"); got.Track != nil {
		t.Errorf("dropNow 之後不可以還端得出已登出帳號的那首歌:%+v", got.Track)
	}
}

// TestWebNowBuildsProviderOutsideLock:【fails-before-fix】newProvider 若在 nowMu 內,等 token 鎖的 poll 會把
// handleRun 收尾的 dropNow() 卡住 —— 使用者看到的是「命令卡住」,而那是 /api/run 的完成事件被面板卡住。
func TestWebNowBuildsProviderOutsideLock(t *testing.T) {
	setCLITestConfig(t)
	building := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var once sync.Once
	orig := newProvider
	newProvider = func(context.Context, string) (provider.Provider, error) {
		once.Do(func() { close(building) })
		<-release // = 等 <key>.token.lock
		return newNowFake(), nil
	}
	t.Cleanup(func() { newProvider = orig })
	s, c := startWeb(t)

	go func() { c.now("") }()
	select {
	case <-building:
	case <-time.After(5 * time.Second):
		t.Fatal("poll 沒進到 newProvider")
	}
	done := make(chan struct{})
	go func() { s.dropNow(false); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("dropNow 被還在建 provider 的 poll 卡住了(建構要在 nowMu 之外)")
	}
}

func TestWebNowNotBlockedByRunningJob(t *testing.T) {
	setCLITestConfig(t)
	f := newNowFake()
	swapNow(t, f)
	s, c := startWeb(t)
	s.runMu.Lock() // = 有一個 job 正在跑
	defer s.runMu.Unlock()

	done := make(chan map[string]any, 1)
	go func() { done <- c.now("") }()
	select {
	case m := <-done:
		if m["provider"] != "spotify" || m["track"] == nil {
			t.Errorf("%v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("/api/now 被序列槽擋住了")
	}
	if code, _, _ := c.run(map[string]any{"args": []string{"--help"}}); code != http.StatusConflict {
		t.Errorf("序列槽握著時 /api/run 要 409,得到 %d", code)
	}
}

func TestWebNowBadProvider400(t *testing.T) {
	setCLITestConfig(t)
	swapNow(t, newNowFake())
	_, c := startWeb(t)
	if st := c.status(http.MethodGet, "/api/now?provider=tidal", nil, nil); st != http.StatusBadRequest {
		t.Errorf("未知 provider → 400,得到 %d", st)
	}
}

// ── 跟隨正在播的平台(決策 51;計畫 docs/superpowers/plans/2026-09-24-web-player-follow-apple-play.md)──

// TestWebNowFollowsPlayingProvider:【fails-before-fix】使用者 2026-09-24 回報:預設 apple、昨天在 Music.app 暫停的歌還掛著,
// 今天改聽 Spotify,面板卻一直停在 Apple(以前只問 default_provider)。接著:Spotify 暫停或變 204 也要留在 Spotify——
// 只有「正在播」能把面板拉走,暫停中的歌不行;Music.app 真的開始播才切回 Apple。
func TestWebNowFollowsPlayingProvider(t *testing.T) {
	setCLITestConfig(t)
	setDefaultProvider(t, "apple")
	advance := fakeNowClock(t)
	apple := newNowFakeAs("apple", nowTrack(false, "昨天那首", 30000, 200000))
	sp := newNowFakeAs("spotify", nowTrack(true, "今天這首", 1000, 200000))
	swapNowByID(t, map[string]*nowFake{"apple": apple, "spotify": sp})
	_, c := startWeb(t)

	if m := c.now(""); m["provider"] != "spotify" || m["playing"] != true {
		t.Fatalf("Spotify 在播、Apple 只是暫停:面板要跟到 Spotify:%v", m)
	}
	sp.set(nowTrack(false, "今天這首", 5000, 200000), nil)
	advance(11 * time.Second)
	if m := c.now(""); m["provider"] != "spotify" || m["playing"] != false {
		t.Errorf("Spotify 暫停了,但 Apple 也只是暫停:要留在 Spotify:%v", m)
	}
	sp.set(nil, nil) // 暫停幾分鐘後 /me/player 變 204
	advance(16 * time.Second)
	if m := c.now(""); m["provider"] != "spotify" {
		t.Errorf("Spotify 變 204 不可以讓 Apple 暫停中的歌把面板搶回去:%v", m)
	}
	apple.set(nowTrack(true, "昨天那首", 31000, 200000), nil)
	advance(2500 * time.Millisecond)
	if m := c.now(""); m["provider"] != "apple" || m["playing"] != true {
		t.Errorf("Music.app 真的開始播了:要切回 Apple:%v", m)
	}
}

// TestWebNowSpotifyNotConsultedWhileBasePlaying:面板顯示的平台正在播時,別家改變不了結果——Spotify 一次都不打。
func TestWebNowSpotifyNotConsultedWhileBasePlaying(t *testing.T) {
	setCLITestConfig(t)
	setDefaultProvider(t, "apple")
	advance := fakeNowClock(t)
	apple := newNowFakeAs("apple", nowTrack(true, "在播", 0, 600000))
	sp := newNowFakeAs("spotify", nil)
	built := swapNowByID(t, map[string]*nowFake{"apple": apple, "spotify": sp})
	_, c := startWeb(t)
	for range 20 {
		c.now("")
		advance(2500 * time.Millisecond)
	}
	if n := sp.calls.Load(); n != 0 || built["spotify"].Load() != 0 {
		t.Errorf("Apple 在播時不該問 Spotify:State %d 次、建構 %d 次", n, built["spotify"].Load())
	}
	if n := apple.calls.Load(); n != 20 {
		t.Errorf("Apple 是本機 osascript,每輪都問:%d 次", n)
	}
}

// TestWebNowSpotifyIdleTTL:哪裡都沒在播時,Spotify 每 15 秒才問一次(Q65),Apple 每輪都問。
func TestWebNowSpotifyIdleTTL(t *testing.T) {
	setCLITestConfig(t)
	setDefaultProvider(t, "apple")
	advance := fakeNowClock(t)
	apple := newNowFakeAs("apple", nil)
	sp := newNowFakeAs("spotify", nil)
	swapNowByID(t, map[string]*nowFake{"apple": apple, "spotify": sp})
	_, c := startWeb(t)
	for range 12 { // 0、2.5 … 27.5 秒
		if m := c.now(""); m["provider"] != "apple" {
			t.Fatalf("都沒在播:留在預設平台:%v", m)
		}
		advance(2500 * time.Millisecond)
	}
	if n := sp.calls.Load(); n != 2 {
		t.Errorf("30 秒內 Spotify 只該問 2 次(0 秒與 15 秒),實際 %d 次", n)
	}
	if n := apple.calls.Load(); n != 12 {
		t.Errorf("Apple 每輪都問:%d 次", n)
	}
}

// TestWebNowBaseStopTriggersProbe:面板顯示的平台從在播變成沒在播,這一輪就重問別家,不等快取過期——
// 「停掉 Music.app、改開 Spotify」約 2.5 秒就切過去。
func TestWebNowBaseStopTriggersProbe(t *testing.T) {
	setCLITestConfig(t)
	setDefaultProvider(t, "apple")
	advance := fakeNowClock(t)
	apple := newNowFakeAs("apple", nowTrack(false, "a", 0, 200000))
	sp := newNowFakeAs("spotify", nil)
	swapNowByID(t, map[string]*nowFake{"apple": apple, "spotify": sp})
	_, c := startWeb(t)
	c.now("") // 兩家都問過:Spotify 的 204 快取到 15 秒
	apple.set(nowTrack(true, "a", 0, 200000), nil)
	advance(2500 * time.Millisecond)
	c.now("")
	apple.set(nowTrack(false, "a", 2500, 200000), nil)
	sp.set(nowTrack(true, "s", 0, 200000), nil)
	advance(2500 * time.Millisecond) // 5 秒:Spotify 的快取還沒過期
	if m := c.now(""); m["provider"] != "spotify" {
		t.Errorf("Apple 剛停、Spotify 在播:這一輪就要切過去:%v", m)
	}
}

// TestWebNowTrackEndExpires:這首快結束時提早重問(約結束後 1 秒),不等滿 10 秒。
func TestWebNowTrackEndExpires(t *testing.T) {
	setCLITestConfig(t)
	advance := fakeNowClock(t)
	f := newNowFake()
	f.set(nowTrack(true, "快結束", 224000, 227000), nil) // 剩 3 秒
	swapNowByID(t, map[string]*nowFake{"spotify": f})
	_, c := startWeb(t)
	c.now("")
	advance(3 * time.Second)
	c.now("")
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("還在這首的有效期內:%d 次", n)
	}
	advance(1500 * time.Millisecond)
	c.now("")
	if n := f.calls.Load(); n != 2 {
		t.Errorf("這首結束約 1 秒後要重問:%d 次", n)
	}
}

// TestWebNowSettleAfterCommand:播放命令跑完後的安定期內一律重問,安定期內讀到的也不留——
// 剛按完播放讀到舊的 ⏸ 又快取 15 秒,再按空白鍵就會送出相反的命令。
func TestWebNowSettleAfterCommand(t *testing.T) {
	setCLITestConfig(t)
	advance := fakeNowClock(t)
	f := newNowFake()
	swapNowByID(t, map[string]*nowFake{"spotify": f})
	_, c := startWeb(t)
	c.now("")
	if _, ev, _ := c.run(map[string]any{"args": []string{"pause"}}); evExit(t, ev)["code"] != float64(0) {
		t.Fatal(ev)
	}
	c.now("")
	c.now("")
	if n := f.calls.Load(); n != 3 {
		t.Errorf("安定期內每一輪都要真的問:%d 次", n)
	}
	advance(webNowSettle + time.Second)
	c.now("")
	c.now("")
	if n := f.calls.Load(); n != 4 {
		t.Errorf("安定期一過:第一輪重問(安定期內讀的不留),之後用快取:%d 次", n)
	}
}

// TestWebNowStaleMSMeansSinceLastRound:stale_ms 是「距離上一輪多久」,不是「距離上次真的打 Spotify 多久」;
// 後者碰上有效期,一次 TryLock 失敗就超過 STALE_DEAD_MS,面板誤判失聯。stale 的進度也要往前推,不倒退。
func TestWebNowStaleMSMeansSinceLastRound(t *testing.T) {
	setCLITestConfig(t)
	origWait := webNowWait
	webNowWait = 100 * time.Millisecond
	t.Cleanup(func() { webNowWait = origWait })
	advance := fakeNowClock(t)
	f := newNowFake() // 61000 / 227000,在播
	swapNowByID(t, map[string]*nowFake{"spotify": f})
	_, c := startWeb(t)
	c.now("")
	advance(8 * time.Second)
	c.now("") // 用快取回答的一輪,也要記下來
	blk := make(chan struct{})
	f.blockOn(blk)
	advance(4 * time.Second) // 12 秒:有效期過了,這一輪真的問、然後卡住
	m := c.now("")
	if !m["stale"].(bool) || m["stale_ms"].(float64) != 4000 {
		t.Errorf("stale_ms 要從上一輪(8 秒那輪)算起 = 4000:%v", m)
	}
	if m["position_ms"].(float64) != 73000 {
		t.Errorf("stale 的進度要往前推到 61+12 秒:%v", m["position_ms"])
	}
	// 等卡住的那一輪收尾再結束:不然它在 Cleanup 換回 webNowClock 時還在讀(-race 會抓)。
	close(blk)
	f.blockOn(nil)
	for deadline := time.Now().Add(5 * time.Second); c.now("")["stale"].(bool); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("放開之後應該恢復新鮮")
		}
	}
}

// TestWebNowRateLimitCooldown:【fails-before-fix】429 照 Retry-After 冷卻:期間不再打 Spotify,Apple 照常每輪都問。
// 以前 Retry-After 超過 60 秒時,Backoff 立刻回錯、pollNow 全域 dropNow,2.5 秒後重建再打——指南明令禁止的緊密重試。
func TestWebNowRateLimitCooldown(t *testing.T) {
	setCLITestConfig(t)
	advance := fakeNowClock(t)
	sp := newNowFakeAs("spotify", nil)
	sp.set(nil, &provider.RateLimitError{Seconds: 120, Message: "rate limited"})
	apple := newNowFakeAs("apple", nil)
	built := swapNowByID(t, map[string]*nowFake{"spotify": sp, "apple": apple})
	_, c := startWeb(t)
	if m := c.now(""); m["provider"] != "spotify" || m["error"] == nil {
		t.Fatalf("限流要照實顯示:%v", m)
	}
	for range 4 {
		advance(15 * time.Second)
		c.now("")
	}
	if n := sp.calls.Load(); n != 1 || built["spotify"].Load() != 1 {
		t.Errorf("120 秒的冷卻內不再打 Spotify、也不重建:State %d 次、建構 %d 次", n, built["spotify"].Load())
	}
	if n := apple.calls.Load(); n != 5 {
		t.Errorf("Apple 照常每輪都問:%d 次", n)
	}
	advance(61 * time.Second)
	c.now("")
	if n := sp.calls.Load(); n != 2 {
		t.Errorf("冷卻過了要再問:%d 次", n)
	}
}

// TestWebNowNotRunningKeepsController:【fails-before-fix】Music.app 沒開是狀態不是錯:不重建 Apple provider。
// 以前每一輪都全域 dropNow,每 2.5 秒重建一次(每次 2–3 次 keychain)。
func TestWebNowNotRunningKeepsController(t *testing.T) {
	setCLITestConfig(t)
	setDefaultProvider(t, "apple")
	advance := fakeNowClock(t)
	apple := newNowFakeAs("apple", nil)
	apple.set(nil, provider.ErrPlayerNotRunning)
	built := swapNowByID(t, map[string]*nowFake{"apple": apple})
	_, c := startWeb(t)
	for range 5 {
		c.now("")
		advance(2500 * time.Millisecond)
	}
	if n := built["apple"].Load(); n != 1 {
		t.Errorf("Music.app 沒開不重建:建構 %d 次", n)
	}
	if n := apple.calls.Load(); n != 5 {
		t.Errorf("Music.app 一打開下一輪就要看得到:每輪都問,%d 次", n)
	}
}

// TestWebNowBuildFailureCached:【fails-before-fix】建不起來(沒登入)的平台一分鐘才重試一次——以前每 2.5 秒就重建一次。
func TestWebNowBuildFailureCached(t *testing.T) {
	setCLITestConfig(t)
	setDefaultProvider(t, "apple")
	advance := fakeNowClock(t)
	built := swapNowByID(t, map[string]*nowFake{"spotify": newNowFakeAs("spotify", nil)}) // apple 沒登入
	_, c := startWeb(t)
	for range 10 {
		if m := c.now(""); m["provider"] != "apple" || m["error"] == nil {
			t.Fatalf("預設平台沒登入要照實說:%v", m)
		}
		advance(2500 * time.Millisecond)
	}
	if n := built["apple"].Load(); n != 1 {
		t.Errorf("25 秒內只試建一次:%d 次", n)
	}
}

// TestWebNowAuthExpiredDropsOnlyThatProvider:【fails-before-fix】一家的 token 失效只重建那一家,別家的 controller 留著。
func TestWebNowAuthExpiredDropsOnlyThatProvider(t *testing.T) {
	setCLITestConfig(t)
	setDefaultProvider(t, "apple")
	advance := fakeNowClock(t)
	apple := newNowFakeAs("apple", nil)
	apple.set(nil, provider.ErrAuthExpired)
	sp := newNowFakeAs("spotify", nil)
	built := swapNowByID(t, map[string]*nowFake{"apple": apple, "spotify": sp})
	_, c := startWeb(t)
	c.now("")
	advance(webNowFailTTL + time.Second)
	c.now("")
	if built["apple"].Load() != 2 || built["spotify"].Load() != 1 {
		t.Errorf("只重建 token 失效的那一家:apple %d 次、spotify %d 次", built["apple"].Load(), built["spotify"].Load())
	}
}

// TestWebNowPodcastCountsAsPlaying:【fails-before-fix】Spotify 播 podcast 或廣告時 item 是 null——那也是正在播,面板要跟過去。
func TestWebNowPodcastCountsAsPlaying(t *testing.T) {
	setCLITestConfig(t)
	setDefaultProvider(t, "apple")
	fakeNowClock(t)
	apple := newNowFakeAs("apple", nowTrack(false, "a", 0, 200000))
	sp := newNowFakeAs("spotify", &provider.PlaybackState{Playing: true, Device: provider.Device{Name: "iPhone"}})
	swapNowByID(t, map[string]*nowFake{"apple": apple, "spotify": sp})
	_, c := startWeb(t)
	if m := c.now(""); m["provider"] != "spotify" || m["playing"] != true || m["track"] != nil {
		t.Errorf("podcast 在播:面板要跟到 Spotify、playing、沒有 track:%v", m)
	}
}

// TestWebNowPinned:有明指(?provider= 與 --web --provider 走同一條)就只問那一家,別家在播也不切。
func TestWebNowPinned(t *testing.T) {
	setCLITestConfig(t)
	fakeNowClock(t)
	apple := newNowFakeAs("apple", nowTrack(false, "a", 0, 200000))
	sp := newNowFakeAs("spotify", nowTrack(true, "s", 0, 200000))
	swapNowByID(t, map[string]*nowFake{"apple": apple, "spotify": sp})
	_, c := startWeb(t)
	if m := c.now("?provider=apple"); m["provider"] != "apple" {
		t.Errorf("釘住 apple:%v", m)
	}
	if n := sp.calls.Load(); n != 0 {
		t.Errorf("釘住時不問別家:Spotify %d 次", n)
	}
}

// TestWebNowDropKeepsShownProvider:dropNow(config set 也會走到,包括換語系)不讓面板跳回預設平台。
func TestWebNowDropKeepsShownProvider(t *testing.T) {
	setCLITestConfig(t)
	setDefaultProvider(t, "apple")
	fakeNowClock(t)
	apple := newNowFakeAs("apple", nowTrack(false, "a", 0, 200000))
	sp := newNowFakeAs("spotify", nowTrack(true, "s", 0, 200000))
	swapNowByID(t, map[string]*nowFake{"apple": apple, "spotify": sp})
	s, c := startWeb(t)
	c.now("")
	sp.set(nowTrack(false, "s", 0, 200000), nil)
	s.dropNow(false)
	if m := c.now(""); m["provider"] != "spotify" {
		t.Errorf("dropNow 之後還是留在上一輪顯示的 Spotify:%v", m)
	}
}

// ── 2026-09-26 對抗式審查補的回歸測試 ──

// TestWebNowCooldownSurvivesAuthStatusAndDrop:【fails-before-fix】限流的冷卻不因唯讀的 auth status(首頁、帳號頁每次載入都會跑)
// 或換帳號(dropNow)而歸零——以前兩者都會把冷卻丟掉,下一輪就在 Retry-After 之內再打一次 Spotify。
func TestWebNowCooldownSurvivesAuthStatusAndDrop(t *testing.T) {
	setCLITestConfig(t)
	advance := fakeNowClock(t)
	sp := newNowFakeAs("spotify", nil)
	sp.set(nil, &provider.RateLimitError{Seconds: 120, Message: "rate limited"})
	swapNowByID(t, map[string]*nowFake{"spotify": sp})
	s, c := startWeb(t)
	c.now("")
	if _, ev, _ := c.run(map[string]any{"args": []string{"auth", "status", "--json"}}); evExit(t, ev) == nil {
		t.Fatal(ev)
	}
	advance(2500 * time.Millisecond)
	c.now("")
	s.dropNow(true) // = auth login / logout 跑完
	advance(2500 * time.Millisecond)
	c.now("")
	if n := sp.calls.Load(); n != 1 {
		t.Errorf("120 秒的冷卻內不再打 Spotify:%d 次", n)
	}
}

// TestWebNowSettleKeepsCooldown:【fails-before-fix】播放命令後的安定期只跳過沒出錯的快取——限流的冷卻照樣守,
// 安定期內讀到的限流也不縮短。以前在 Apple 上按一次暫停,就會在 Spotify 的冷卻期內多打兩三次。
func TestWebNowSettleKeepsCooldown(t *testing.T) {
	setCLITestConfig(t)
	setDefaultProvider(t, "apple")
	advance := fakeNowClock(t)
	apple := newNowFakeAs("apple", nowTrack(false, "a", 0, 200000))
	sp := newNowFakeAs("spotify", nil)
	sp.set(nil, &provider.RateLimitError{Seconds: 120, Message: "rate limited"})
	swapNowByID(t, map[string]*nowFake{"apple": apple, "spotify": sp})
	s, c := startWeb(t)
	c.now("")
	s.settleNow() // = 在 Apple 上按了暫停
	c.now("")
	advance(2500 * time.Millisecond)
	c.now("")
	advance(2500 * time.Millisecond) // 安定期過了
	c.now("")
	if n := sp.calls.Load(); n != 1 {
		t.Errorf("安定期不可以越過限流的冷卻:Spotify %d 次", n)
	}
	if n := apple.calls.Load(); n != 4 {
		t.Errorf("Apple 照常每輪都問:%d 次", n)
	}
}

// TestWebNowSettleExpiresPreCommandCache:【fails-before-fix】命令之前讀到的「在播」在安定期結束後不可以還當新鮮的用——
// 安定期那幾秒剛好沒有一輪(控制鈕的立即輪詢撞到 TryLock)時,以前會再顯示 ▶ 最多 7 秒,空白鍵就送出相反的命令。
func TestWebNowSettleExpiresPreCommandCache(t *testing.T) {
	setCLITestConfig(t)
	advance := fakeNowClock(t)
	f := newNowFake()
	swapNowByID(t, map[string]*nowFake{"spotify": f})
	s, c := startWeb(t)
	c.now("")
	s.settleNow()
	advance(webNowSettle + time.Second) // 安定期內沒有任何一輪
	c.now("")
	if n := f.calls.Load(); n != 2 {
		t.Errorf("命令之前的快取要作廢:State %d 次", n)
	}
}

// TestWebNowDropDuringRoundDiscardsResult:【fails-before-fix】登出(dropNow)時正在飛的那一輪,結果不可以回到快照——
// 不然已登出帳號的那首歌會被當成新鮮的端出來,dropNow 的註解說要防的正是這個。
func TestWebNowDropDuringRoundDiscardsResult(t *testing.T) {
	setCLITestConfig(t)
	origWait := webNowWait
	webNowWait = 100 * time.Millisecond
	t.Cleanup(func() { webNowWait = origWait })
	advance := fakeNowClock(t)
	f := newNowFake()
	swapNowByID(t, map[string]*nowFake{"spotify": f})
	s, c := startWeb(t)
	c.now("")
	advance(11 * time.Second)
	blk := make(chan struct{})
	f.blockOn(blk)
	c.now("") // 這一輪卡在 State,handler 先回 stale
	s.dropNow(true)
	close(blk)
	f.blockOn(nil)
	for deadline := time.Now().Add(5 * time.Second); !s.pollMu.TryLock(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("卡住的那一輪沒有收尾")
		}
	}
	s.pollMu.Unlock()
	if snap := s.lastNow.Load(); snap != nil {
		t.Errorf("dropNow 之前開始的那一輪不可以寫回快照:%+v", snap.resp.Track)
	}
}

// TestWebNowAppleStateErrorRetriedNextRound:【fails-before-fix】Apple 的 State 回錯(osascript 失敗、串流沒有時長)下一輪就再問——
// osascript 在本機、不花配額;一分鐘的有效期只給建不起來的(那要讀 keychain)。
func TestWebNowAppleStateErrorRetriedNextRound(t *testing.T) {
	setCLITestConfig(t)
	setDefaultProvider(t, "apple")
	advance := fakeNowClock(t)
	apple := newNowFakeAs("apple", nil)
	apple.set(nil, errors.New("osascript: missing value"))
	swapNowByID(t, map[string]*nowFake{"apple": apple})
	_, c := startWeb(t)
	c.now("")
	apple.set(nowTrack(true, "a", 0, 200000), nil)
	advance(2500 * time.Millisecond)
	if m := c.now(""); m["provider"] != "apple" || m["playing"] != true {
		t.Errorf("Apple 回錯後下一輪就要再問:%v", m)
	}
}

// TestWebNowLogoutResetsShown:【fails-before-fix】登出面板正在顯示的平台之後,面板回到預設平台——以前會一直停在「Spotify 未登入」。
// 換語系(config set language)不算。
func TestWebNowLogoutResetsShown(t *testing.T) {
	for _, tc := range []struct {
		path string
		args []string
		want bool
	}{
		{"capy auth logout", []string{"auth", "logout", "spotify"}, true},
		{"capy auth login", []string{"auth", "login", "apple"}, true},
		{"capy config set", []string{"config", "set", "default_provider", "spotify"}, true},
		{"capy config set", []string{"config", "set", "language", "en"}, false},
	} {
		if got := webNowResetsShown(tc.path, tc.args); got != tc.want {
			t.Errorf("%v → %v,要 %v", tc.args, got, tc.want)
		}
	}
	setCLITestConfig(t)
	setDefaultProvider(t, "apple")
	fakeNowClock(t)
	apple := newNowFakeAs("apple", nowTrack(false, "a", 0, 200000))
	sp := newNowFakeAs("spotify", nowTrack(true, "s", 0, 200000))
	swapNowByID(t, map[string]*nowFake{"apple": apple, "spotify": sp})
	s, c := startWeb(t)
	if m := c.now(""); m["provider"] != "spotify" {
		t.Fatalf("%v", m)
	}
	sp.set(nil, errors.New("spotify:沒登入"))
	s.dropNow(true) // = auth logout spotify 跑完
	if m := c.now(""); m["provider"] != "apple" {
		t.Errorf("登出之後面板回到預設平台:%v", m)
	}
}
