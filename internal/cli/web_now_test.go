package cli

import (
	"context"
	"encoding/json"
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

func (f *nowFake) ID() string { return "spotify" }
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

func TestWebNowReportsStateAndCachesController(t *testing.T) {
	setCLITestConfig(t)
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
	if f.calls.Load() < 3 {
		t.Errorf("每次輪詢都要真的問 State:%d", f.calls.Load())
	}
}

// TestWebNowUsesCachedControllerAndInvalidatesOnlyAfterAuthOrConfigSet:一般命令不作廢;auth / config set 才作廢。
func TestWebNowUsesCachedControllerAndInvalidatesOnlyAfterAuthOrConfigSet(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"capy auth login spotify", true}, {"capy auth logout google", true}, {"capy auth status", true},
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
	f := newNowFake()
	built := swapNow(t, f)
	_, c := startWeb(t)
	c.now("")
	f.set(nil, provider.ErrAuthExpired)
	m := c.now("")
	if m["error"] == nil || m["track"] != nil {
		t.Fatalf("State 回錯要 200 帶 error:%v", m)
	}
	f.set(newNowFake().state, nil)
	c.now("")
	if n := atomic.LoadInt32(built); n != 2 {
		t.Errorf("壞掉的 controller 不留著,下次要重建:%d", n)
	}
}

// TestWebNowPollBoundedWhileStateStuck:State 卡住(等 token 鎖就是這個情況)時 handler 不跟著卡——
// webNowWait 到就回 stale 快照;背景那個 poll 用伺服器 ctx 跑完(refresh 不被腰斬),放開後下一次又是新鮮的。
func TestWebNowPollBoundedWhileStateStuck(t *testing.T) {
	setCLITestConfig(t)
	origWait := webNowWait
	webNowWait = 100 * time.Millisecond
	t.Cleanup(func() { webNowWait = origWait })
	f := newNowFake()
	swapNow(t, f)
	_, c := startWeb(t)
	c.now("") // 先有一份快照

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
	s.dropNow()
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
	go func() { s.dropNow(); close(done) }()
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
