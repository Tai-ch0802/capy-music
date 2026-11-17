package cli

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

type sleepFakeController struct {
	mu          sync.Mutex
	st          *provider.PlaybackState
	vols        []int
	pauses      int
	plays       int
	volErr      error
	pauseErr    error
	checkNoWait bool
	waited      atomic.Int32
}

func (f *sleepFakeController) Devices(context.Context) ([]provider.Device, error) { return nil, nil }
func (f *sleepFakeController) State(context.Context) (*provider.PlaybackState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return clonePlaybackState(f.st), nil
}
func (f *sleepFakeController) Play(ctx context.Context, _ provider.PlayRequest) error {
	f.verifyWithoutWait(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plays++
	if f.st != nil {
		f.st.Playing = true
	}
	return nil
}
func (f *sleepFakeController) Pause(ctx context.Context) error {
	f.verifyWithoutWait(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauses++
	if f.pauseErr != nil {
		return f.pauseErr
	}
	if f.st != nil {
		f.st.Playing = false
	}
	return nil
}
func (f *sleepFakeController) Next(context.Context) error      { return nil }
func (f *sleepFakeController) Prev(context.Context) error      { return nil }
func (f *sleepFakeController) Seek(context.Context, int) error { return nil }
func (f *sleepFakeController) SetVolume(ctx context.Context, pct int) error {
	f.verifyWithoutWait(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vols = append(f.vols, pct)
	if f.volErr != nil {
		return f.volErr
	}
	if f.st != nil {
		f.st.Device.VolumePct = pct
	}
	return nil
}

func (f *sleepFakeController) verifyWithoutWait(ctx context.Context) {
	if !f.checkNoWait {
		return
	}
	h := http.Header{"Retry-After": []string{"10"}}
	_ = provider.Backoff(ctx, &http.Response{StatusCode: http.StatusTooManyRequests, Header: h}, 0)
}

func newTestSleepEnv(t *testing.T, st *provider.PlaybackState) (*sleepTimer, *sleepFakeController, func(time.Duration), *atomic.Int32) {
	t.Helper()
	var clockMu sync.Mutex
	now := time.Date(2026, 11, 17, 22, 0, 0, 0, time.UTC)
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}
	fc := &sleepFakeController{st: st, checkNoWait: true}
	origWait := provider.Wait
	provider.Wait = func(context.Context, time.Duration) error {
		fc.waited.Add(1)
		return nil
	}
	t.Cleanup(func() { provider.Wait = origWait })

	var settled atomic.Int32
	timer := newSleepTimer(context.Background(), sleepTimerOptions{
		Clock: func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now },
		Controller: func(string) (provider.PlaybackController, error) {
			return fc, nil
		},
		CurrentState: func(provID string) (string, *provider.PlaybackState) {
			if provID == "" {
				provID = "spotify"
			}
			st, _ := fc.State(context.Background())
			return provID, st
		},
		OnSettle: func() { settled.Add(1) },
	})
	t.Cleanup(timer.Close)
	return timer, fc, advance, &settled
}

func TestSleepTimerCountdownGentleFadeMaxFiveStepsAndRestoresVolume(t *testing.T) {
	initSt := &provider.PlaybackState{
		Playing:    true,
		Track:      &provider.Track{ProviderID: "t1", Title: "Night", DurationMS: 300000},
		ProgressMS: 10000,
		Device:     provider.Device{Name: "Desk", VolumePct: 80, VolumeKnown: true},
	}
	timer, fc, advance, settled := newTestSleepEnv(t, initSt)
	timer.SetCountdownMinutes("spotify", 1) // 60s

	snap := timer.Snapshot()
	if snap.Mode != SleepCountdown || snap.RemainingSec != 60 || snap.TotalSec != 60 {
		t.Fatalf("初始快照不符:%+v", snap)
	}

	// 前 29 秒(剩 31s):不應降音。
	for range 29 {
		advance(time.Second)
		st, _ := fc.State(context.Background())
		timer.Observe("spotify", st)
	}
	if len(fc.vols) != 0 {
		t.Fatalf("進入最後 30s 前不得呼叫 SetVolume:%v", fc.vols)
	}

	// 最後 31 秒(從剩 31s 走到 0s)每秒高頻 Tick(甚至同一秒多次 Tick):最多只能分 5 階降音!
	for range 31 {
		advance(time.Second)
		st, _ := fc.State(context.Background())
		timer.Observe("spotify", st)
		timer.Tick(context.Background()) // 驗證同一秒重複 Tick 冪等不超打
	}

	// 到點瞬間:5 階降音(64, 48, 32, 16, 8) + Pause 成功後 1 次復原回 80 = 共 6 次 SetVolume。
	wantVols := []int{64, 48, 32, 16, 8, 80}
	if !slices.Equal(fc.vols, wantVols) {
		t.Fatalf("SetVolume 呼叫序列 = %v, want %v (降音最多 5 階 + Pause 後復原 1 次)", fc.vols, wantVols)
	}
	if fc.pauses != 1 {
		t.Fatalf("Pause 次數 = %d, want 1", fc.pauses)
	}
	if fc.waited.Load() != 0 {
		t.Fatalf("所有控制呼叫都必須包 provider.WithoutWait,實際等待次數 = %d", fc.waited.Load())
	}
	if timer.Snapshot().Mode != SleepOff {
		t.Fatalf("倒數結束後應回到 SleepOff:%+v", timer.Snapshot())
	}
	if settled.Load() == 0 {
		t.Fatal("應觸發 settleNow 回呼")
	}
}

func TestSleepTimerVolumeNotAllowedAndUnknownVolumeGuardrails(t *testing.T) {
	// 1. VolumeKnown == false:零次 SetVolume,時間到直接 Pause。
	stUnknown := &provider.PlaybackState{
		Playing: true,
		Track:   &provider.Track{ProviderID: "t1", Title: "Song", DurationMS: 200000},
		Device:  provider.Device{Name: "Music.app", VolumePct: 0, VolumeKnown: false},
	}
	timer, fc, advance, _ := newTestSleepEnv(t, stUnknown)
	timer.SetCountdown("apple", 45*time.Second)
	for range 45 {
		advance(time.Second)
		timer.Tick(context.Background())
	}
	if len(fc.vols) != 0 || fc.pauses != 1 {
		t.Fatalf("VolumeKnown=false 時不得呼叫 SetVolume:vols=%v pauses=%d", fc.vols, fc.pauses)
	}

	// 2. VolumeKnown == true 但裝置回 ErrVolumeNotAllowed:第 1 階探測失敗後立即停手,不重試後面 4 階、也不還原音量,時間到直接 Pause。
	stDisallowed := &provider.PlaybackState{
		Playing: true,
		Track:   &provider.Track{ProviderID: "t2", Title: "Song", DurationMS: 200000},
		Device:  provider.Device{Name: "iPhone", VolumePct: 70, VolumeKnown: true},
	}
	timer2, fc2, advance2, _ := newTestSleepEnv(t, stDisallowed)
	fc2.volErr = provider.ErrVolumeNotAllowed
	timer2.SetCountdown("spotify", 40*time.Second)
	for range 40 {
		advance2(time.Second)
		timer2.Tick(context.Background())
	}
	if len(fc2.vols) != 1 || fc2.pauses != 1 {
		t.Fatalf("遇 ErrVolumeNotAllowed 應立即停止後續降音並在到點時 Pause:vols=%v pauses=%d", fc2.vols, fc2.pauses)
	}

	// 3. Pause 失敗時不得把已降音的音量突然拉回原本數值(避免在睡眠中暴衝音量)。
	stPauseFail := &provider.PlaybackState{
		Playing: true,
		Track:   &provider.Track{ProviderID: "t3", Title: "Song", DurationMS: 200000},
		Device:  provider.Device{Name: "Speaker", VolumePct: 50, VolumeKnown: true},
	}
	timer3, fc3, advance3, _ := newTestSleepEnv(t, stPauseFail)
	fc3.pauseErr = errors.New("network down")
	timer3.SetCountdown("spotify", 30*time.Second)
	advance3(10 * time.Second) // rem = 20s → step 2 (30)
	timer3.Tick(context.Background())
	advance3(20 * time.Second) // rem = 0s → Pause fails
	timer3.Tick(context.Background())
	if slices.Contains(fc3.vols, 50) {
		t.Fatalf("Pause 失敗時不得把音量拉回原值:%v", fc3.vols)
	}
}

func TestSleepTimerEndOfTrackPausesOnTrackChangeOrWrap(t *testing.T) {
	st := &provider.PlaybackState{
		Playing:    true,
		Track:      &provider.Track{ProviderID: "track-1", Title: "First", DurationMS: 180000},
		ProgressMS: 170000,
		Device:     provider.Device{Name: "Mac"},
	}
	timer, fc, _, _ := newTestSleepEnv(t, st)
	timer.SetEndOfTrack("spotify")
	if snap := timer.Snapshot(); snap.Mode != SleepEndOfTrack || snap.RemainingSec != 10 {
		t.Fatalf("EndOfTrack 快照不符:%+v", snap)
	}

	// 同一首推進到 177s:仍繼續播。
	st.ProgressMS = 177000
	timer.Observe("spotify", st)
	if fc.pauses != 0 || timer.Snapshot().Mode != SleepEndOfTrack {
		t.Fatalf("同一首尚未結束不應 Pause:pauses=%d mode=%s", fc.pauses, timer.Snapshot().Mode)
	}

	// 換到下一首 track-2:立即 Pause 並自動解除定時器!
	st.Track = &provider.Track{ProviderID: "track-2", Title: "Second", DurationMS: 200000}
	st.ProgressMS = 500
	timer.Observe("spotify", st)
	if fc.pauses != 1 || timer.Snapshot().Mode != SleepOff {
		t.Fatalf("換首時應立即 Pause 並回 SleepOff:pauses=%d mode=%s", fc.pauses, timer.Snapshot().Mode)
	}

	// 單曲循環(RepeatTrack)情境:同一首從 178s 繞回 1s 也應偵測播畢並 Pause。
	fc.pauses = 0
	stLoop := &provider.PlaybackState{
		Playing:    true,
		Track:      &provider.Track{ProviderID: "track-loop", Title: "Loop", DurationMS: 180000},
		ProgressMS: 178000,
	}
	fc.st = stLoop
	timer.SetEndOfTrack("spotify")
	stLoop.ProgressMS = 1200
	timer.Observe("spotify", stLoop)
	if fc.pauses != 1 || timer.Snapshot().Mode != SleepOff {
		t.Fatalf("單曲循環繞回開頭也應 Pause:pauses=%d mode=%s", fc.pauses, timer.Snapshot().Mode)
	}
}

func TestSleepTimerYuzuPomodoroCyclesAndConcurrency(t *testing.T) {
	st := &provider.PlaybackState{
		Playing: true,
		Track:   &provider.Track{ProviderID: "yuzu", Title: "Onsen", DurationMS: 240000},
		Device:  provider.Device{Name: "Desk", VolumePct: 60, VolumeKnown: true},
	}
	timer, fc, advance, _ := newTestSleepEnv(t, st)
	// 預設值檢查(0, 0 → 25m focus / 5m break)
	timer.SetPomodoro("spotify", 0, 0)
	if snap := timer.Snapshot(); snap.Mode != SleepYuzuPomodoro || snap.Phase != SleepPhaseFocus || snap.RemainingSec != 25*60 {
		t.Fatalf("預設柚子番茄鐘應為 25m focus:%+v", snap)
	}

	// 推進到最後 15 秒(觸發一次降音)再到 25m 結束 → Pause + 復原音量 60 + 進入 5m Break
	advance(24*time.Minute + 45*time.Second)
	timer.Tick(context.Background())
	advance(15 * time.Second)
	timer.Tick(context.Background())

	snap := timer.Snapshot()
	if snap.Phase != SleepPhaseBreak || snap.RemainingSec != 5*60 || snap.Cycles != 1 {
		t.Fatalf("專注結束應切到 5m Break 且 Cycles=1:%+v", snap)
	}
	if fc.pauses != 1 || fc.vols[len(fc.vols)-1] != 60 {
		t.Fatalf("專注結束應 Pause 並復原音量 60:pauses=%d vols=%v", fc.pauses, fc.vols)
	}

	// 推進滿 5m Break → 自動 Play 恢復播放並進入下一輪 25m Focus
	advance(5 * time.Minute)
	timer.Tick(context.Background())
	snap = timer.Snapshot()
	if snap.Phase != SleepPhaseFocus || snap.RemainingSec != 25*60 || fc.plays != 1 {
		t.Fatalf("休息結束應自動 Play 並回 25m Focus:%+v plays=%d", snap, fc.plays)
	}

	// 併發壓測(-race 驗證):背景自動 ticker + 多執行緒同時讀 Snapshot / Observe / 切換模式。
	live := newSleepTimer(context.Background(), sleepTimerOptions{
		TickInterval: 2 * time.Millisecond,
		FocusDur:     10 * time.Millisecond,
		BreakDur:     10 * time.Millisecond,
		Controller:   func(string) (provider.PlaybackController, error) { return fc, nil },
	})
	defer live.Close()
	live.SetPomodoro("spotify", 0, 0)
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				_ = live.Snapshot()
				live.Observe("spotify", &provider.PlaybackState{Playing: true, Track: &provider.Track{ProviderID: "x", DurationMS: 10000}})
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	live.Off()
	if live.Snapshot().Mode != SleepOff {
		t.Fatalf("Off 後應為 SleepOff:%+v", live.Snapshot())
	}
}
