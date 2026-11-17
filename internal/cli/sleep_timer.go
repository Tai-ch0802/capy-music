package cli

// sleepTimer:TUI 與 webServer 共用的行程內睡眠/專注定時器。
// 護欄(#9 1B-3):倒數最後 30 秒內僅在 VolumeKnown == true 時最多分 5 階呼叫 SetVolume(必帶 provider.WithoutWait),
// 遇 ErrVolumeNotAllowed 或任何錯誤立即停止降音改等時間到 Pause;到點 Pause 成功後若曾降音則自動呼叫一次 SetVolume 復原音量。

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

type sleepMode string

const (
	SleepOff          sleepMode = "off"
	SleepEndOfTrack   sleepMode = "end_of_track"
	SleepCountdown    sleepMode = "countdown"
	SleepYuzuPomodoro sleepMode = "pomodoro"
)

type sleepPhase string

const (
	SleepPhaseNone  sleepPhase = ""
	SleepPhaseFocus sleepPhase = "focus"
	SleepPhaseBreak sleepPhase = "break"
)

const (
	defaultPomodoroFocus = 25 * time.Minute
	defaultPomodoroBreak = 5 * time.Minute
	sleepFadeWindow      = 30 * time.Second
	sleepFadeSteps       = 5
	defaultSleepTimeout  = 5 * time.Second
)

// sleepSnapshot:Snapshot() 回傳的唯讀狀態(可直接給 TUI 狀態列與 web API 序列化)。
type sleepSnapshot struct {
	Mode           sleepMode  `json:"mode"`
	Phase          sleepPhase `json:"phase,omitempty"`
	RemainingSec   int        `json:"remaining_sec,omitempty"`
	TotalSec       int        `json:"total_sec,omitempty"`
	Cycles         int        `json:"cycles,omitempty"`
	Fading         bool       `json:"fading,omitempty"`
	TargetProvider string     `json:"target_provider,omitempty"`
}

type sleepTimerOptions struct {
	Clock        func() time.Time
	TickInterval time.Duration // >0:啟用時背景自動定時推進;<=0:僅由呼叫端主動呼叫 Tick/Observe(方便決定性測試)
	FocusDur     time.Duration
	BreakDur     time.Duration
	CallTimeout  time.Duration
	Controller   func(provID string) (provider.PlaybackController, error)
	CurrentState func(provID string) (string, *provider.PlaybackState)
	OnSettle     func()
}

type sleepTimer struct {
	ctx  context.Context
	opts sleepTimerOptions

	actMu sync.Mutex // 序列化對 PlaybackController 的實際操作,避免併發重送 Pause / SetVolume
	mu    sync.Mutex // 只守記憶體欄位,絕不在握鎖時呼叫網路或 osascript

	gen      uint64
	stopTick context.CancelFunc

	mode       sleepMode
	phase      sleepPhase
	targetProv string
	deadline   time.Time
	totalDur   time.Duration
	focusDur   time.Duration
	breakDur   time.Duration
	cycles     int

	// 已知的最新播放狀態(由 Observe 或 CurrentState 更新)
	lastProv   string
	lastState  *provider.PlaybackState
	lastSeenAt time.Time

	// SleepEndOfTrack 狀態追蹤
	armedTrack    string
	armedNearEnd  bool
	lastTrackProg int
	lastTrackDur  int

	// 溫柔漸弱狀態(最多 5 階)
	fadeOrigKnown bool
	fadeOrigVol   int
	fadeLastSet   int
	fadeLastStep  int
	faded         bool
	fadeDisabled  bool
}

func newSleepTimer(ctx context.Context, opts sleepTimerOptions) *sleepTimer {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.FocusDur <= 0 {
		opts.FocusDur = defaultPomodoroFocus
	}
	if opts.BreakDur <= 0 {
		opts.BreakDur = defaultPomodoroBreak
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = defaultSleepTimeout
	}
	return &sleepTimer{
		ctx:      ctx,
		opts:     opts,
		mode:     SleepOff,
		focusDur: opts.FocusDur,
		breakDur: opts.BreakDur,
	}
}

// newTrackerSleepTimer:掛載於共用 nowTracker 的建構器(供 Stage 2 TUI 與 webServer 直接使用)。
func newTrackerSleepTimer(ctx context.Context, trk *nowTracker) *sleepTimer {
	return newSleepTimer(ctx, sleepTimerOptions{
		Clock:        func() time.Time { return webNowClock() },
		TickInterval: time.Second,
		Controller: func(provID string) (provider.PlaybackController, error) {
			if trk == nil {
				return nil, provider.ErrNotSupported
			}
			return trk.playback(provID)
		},
		CurrentState: func(provID string) (string, *provider.PlaybackState) {
			if trk == nil {
				return provID, nil
			}
			if snap := trk.lastNow.Load(); snap != nil {
				if provID == "" || snap.res.provider == provID {
					adv := advance(snap.res, webNowClock().Sub(snap.at))
					return adv.provider, adv.st
				}
			}
			if provID != "" {
				return provID, nil
			}
			if p := trk.shown.Load(); p != nil && *p != "" {
				return *p, nil
			}
			return loadDefaultProvider(), nil
		},
		OnSettle: func() {
			if trk != nil {
				trk.settleNow()
			}
		},
	})
}

// Close:停止背景計時 goroutine 並關閉定時器。
func (t *sleepTimer) Close() {
	t.mu.Lock()
	t.gen++
	t.stopTickerLocked()
	t.mode = SleepOff
	t.phase = SleepPhaseNone
	t.resetFadeLocked()
	t.mu.Unlock()
}

// Off:關閉定時器;若正處於漸弱降音中途,盡力將音量設回原本數值。
func (t *sleepTimer) Off() {
	t.Cancel(t.ctx)
}

// Cancel:關閉定時器並(若曾降音)使用給定 ctx 還原音量。
func (t *sleepTimer) Cancel(ctx context.Context) {
	t.mu.Lock()
	restoreVol := -1
	prov := t.resolveProviderLocked()
	if t.faded {
		restoreVol = t.fadeOrigVol
	}
	t.gen++
	t.stopTickerLocked()
	t.mode = SleepOff
	t.phase = SleepPhaseNone
	t.deadline = time.Time{}
	t.totalDur = 0
	t.cycles = 0
	t.armedTrack = ""
	t.armedNearEnd = false
	t.lastTrackProg = 0
	t.lastTrackDur = 0
	t.resetFadeLocked()
	t.mu.Unlock()

	if restoreVol >= 0 && prov != "" {
		t.actMu.Lock()
		defer t.actMu.Unlock()
		if pc, err := t.controller(prov); err == nil && pc != nil {
			cctx, cancel := t.callCtx(ctx)
			err := pc.SetVolume(cctx, restoreVol)
			cancel()
			if err == nil {
				t.settle()
			}
		}
	}
}

// SetCountdownMinutes:設定倒數分鐘數(<=0 等同關閉)。
func (t *sleepTimer) SetCountdownMinutes(provID string, minutes int) {
	if minutes <= 0 {
		t.Off()
		return
	}
	t.SetCountdown(provID, time.Duration(minutes)*time.Minute)
}

// SetCountdown:設定任意時長倒數(<=0 等同關閉)。
func (t *sleepTimer) SetCountdown(provID string, d time.Duration) {
	if d <= 0 {
		t.Off()
		return
	}
	now := t.opts.Clock()
	t.mu.Lock()
	t.gen++
	t.seedStateLocked(provID, now)
	t.mode = SleepCountdown
	t.phase = SleepPhaseNone
	t.targetProv = provID
	t.totalDur = d
	t.deadline = now.Add(d)
	t.cycles = 0
	t.armedTrack = ""
	t.armedNearEnd = false
	t.resetFadeLocked()
	t.ensureTickerLocked()
	t.mu.Unlock()
}

// SetEndOfTrack:設定「播完這首就停」(當前曲目播畢、換首或單曲循環繞回開頭時自動 Pause)。
func (t *sleepTimer) SetEndOfTrack(provID string) {
	now := t.opts.Clock()
	t.mu.Lock()
	t.gen++
	t.seedStateLocked(provID, now)
	t.mode = SleepEndOfTrack
	t.phase = SleepPhaseNone
	t.targetProv = provID
	t.totalDur = 0
	t.deadline = time.Time{}
	t.cycles = 0
	t.armedTrack = ""
	t.armedNearEnd = false
	t.lastTrackProg = 0
	t.lastTrackDur = 0
	if t.lastState != nil && t.lastState.Playing && t.lastState.Track != nil {
		t.armTrackLocked(t.lastState)
	}
	t.resetFadeLocked()
	t.ensureTickerLocked()
	t.mu.Unlock()
}

// SetPomodoro:啟動水豚柚子湯專注計時(focusDur <= 0 用預設 25m,breakDur <= 0 用預設 5m)。
func (t *sleepTimer) SetPomodoro(provID string, focusDur, breakDur time.Duration) {
	now := t.opts.Clock()
	t.mu.Lock()
	if focusDur <= 0 {
		focusDur = t.opts.FocusDur
	}
	if breakDur <= 0 {
		breakDur = t.opts.BreakDur
	}
	t.gen++
	t.seedStateLocked(provID, now)
	t.mode = SleepYuzuPomodoro
	t.phase = SleepPhaseFocus
	t.targetProv = provID
	t.focusDur = focusDur
	t.breakDur = breakDur
	t.totalDur = focusDur
	t.deadline = now.Add(focusDur)
	t.cycles = 0
	t.armedTrack = ""
	t.armedNearEnd = false
	t.resetFadeLocked()
	t.ensureTickerLocked()
	t.mu.Unlock()
}

// Snapshot:回傳執行緒安全的唯讀狀態快照。
func (t *sleepTimer) Snapshot() sleepSnapshot {
	now := t.opts.Clock()
	t.mu.Lock()
	defer t.mu.Unlock()
	switch t.mode {
	case SleepCountdown:
		return sleepSnapshot{
			Mode:           SleepCountdown,
			RemainingSec:   ceilSeconds(t.deadline.Sub(now)),
			TotalSec:       ceilSeconds(t.totalDur),
			Fading:         t.faded && !t.fadeDisabled,
			TargetProvider: t.resolveProviderLocked(),
		}
	case SleepYuzuPomodoro:
		return sleepSnapshot{
			Mode:           SleepYuzuPomodoro,
			Phase:          t.phase,
			RemainingSec:   ceilSeconds(t.deadline.Sub(now)),
			TotalSec:       ceilSeconds(t.totalDur),
			Cycles:         t.cycles,
			Fading:         t.phase == SleepPhaseFocus && t.faded && !t.fadeDisabled,
			TargetProvider: t.resolveProviderLocked(),
		}
	case SleepEndOfTrack:
		rem, tot := 0, 0
		if st := t.lastState; st != nil && st.Track != nil && st.Track.DurationMS > 0 {
			tot = (st.Track.DurationMS + 999) / 1000
			prog := st.ProgressMS
			if st.Playing && !t.lastSeenAt.IsZero() && now.After(t.lastSeenAt) {
				prog += int(now.Sub(t.lastSeenAt) / time.Millisecond)
			}
			if leftMS := st.Track.DurationMS - prog; leftMS > 0 {
				rem = (leftMS + 999) / 1000
			}
		}
		return sleepSnapshot{
			Mode:           SleepEndOfTrack,
			RemainingSec:   rem,
			TotalSec:       tot,
			TargetProvider: t.resolveProviderLocked(),
		}
	default:
		return sleepSnapshot{Mode: SleepOff}
	}
}

// Observe:餵入最新觀測到的播放狀態(TUI poll / Web /api/now 順手呼叫,零額外 API 開銷),並在需要時觸發結算。
func (t *sleepTimer) Observe(provID string, st *provider.PlaybackState) {
	t.ObserveContext(t.ctx, provID, st)
}

func (t *sleepTimer) ObserveContext(ctx context.Context, provID string, st *provider.PlaybackState) {
	now := t.opts.Clock()
	needEval := false
	t.mu.Lock()
	if t.targetProv == "" || provID == "" || t.targetProv == provID {
		if provID != "" {
			t.lastProv = provID
		}
		t.lastState = clonePlaybackState(st)
		t.lastSeenAt = now
		needEval = t.mode != SleepOff
	}
	t.mu.Unlock()
	if needEval {
		t.Tick(ctx)
	}
}

type sleepActionKind int

const (
	sleepActNone sleepActionKind = iota
	sleepActFade
	sleepActExpirePause   // 倒數或播完這首結束 → Pause(+ 復原音量)→ Off
	sleepActPomodoroBreak // 專注結束 → Pause(+ 復原音量)→ 進入 Break
	sleepActPomodoroFocus // 休息結束 → Play → 進入下一輪 Focus
)

type sleepAction struct {
	kind       sleepActionKind
	gen        uint64
	prov       string
	fadeStep   int
	fadeVol    int
	restoreVol int // >=0 表示 Pause 成功後要還原回此音量
}

// Tick:推進一次狀態機(背景 ticker 或測試直接呼叫)。
func (t *sleepTimer) Tick(ctx context.Context) {
	if ctx == nil {
		ctx = t.ctx
	}
	t.actMu.Lock()
	defer t.actMu.Unlock()

	// 若處於需要觀測狀態但外部許久沒呼叫 Observe(例如瀏覽器分頁在背景),必要時主動輕量補讀一次。
	t.maybeRefreshState(ctx)

	now := t.opts.Clock()
	act := t.planAction(now)
	if act.kind == sleepActNone {
		return
	}
	t.executeAction(ctx, act)
}

func (t *sleepTimer) maybeRefreshState(ctx context.Context) {
	now := t.opts.Clock()
	t.mu.Lock()
	mode := t.mode
	phase := t.phase
	prov := t.resolveProviderLocked()
	stale := t.lastSeenAt.IsZero() || now.Sub(t.lastSeenAt) >= 2*time.Second
	needVolumeForFade := (mode == SleepCountdown || (mode == SleepYuzuPomodoro && phase == SleepPhaseFocus)) &&
		t.totalDur >= sleepFadeWindow && !t.fadeOrigKnown && !t.fadeDisabled &&
		now.Before(t.deadline) && t.deadline.Sub(now) <= sleepFadeWindow && t.lastState == nil
	needTrackPoll := mode == SleepEndOfTrack && stale
	t.mu.Unlock()

	if !needVolumeForFade && !needTrackPoll {
		return
	}
	if t.opts.CurrentState != nil {
		if cp, cst := t.opts.CurrentState(prov); cst != nil {
			t.mu.Lock()
			if cp != "" {
				t.lastProv = cp
			}
			t.lastState = clonePlaybackState(cst)
			t.lastSeenAt = now
			t.mu.Unlock()
			if !needTrackPoll {
				return
			}
		}
	}
	if !needTrackPoll || prov == "" {
		return
	}
	pc, err := t.controller(prov)
	if err != nil || pc == nil {
		return
	}
	cctx, cancel := t.callCtx(ctx)
	st, err := pc.State(cctx)
	cancel()
	if err == nil {
		t.mu.Lock()
		t.lastProv = prov
		t.lastState = clonePlaybackState(st)
		t.lastSeenAt = t.opts.Clock()
		t.mu.Unlock()
	}
}

func (t *sleepTimer) planAction(now time.Time) sleepAction {
	t.mu.Lock()
	defer t.mu.Unlock()

	gen := t.gen
	prov := t.resolveProviderLocked()

	switch t.mode {
	case SleepEndOfTrack:
		st := t.lastState
		if t.armedTrack == "" {
			if st != nil && st.Playing && st.Track != nil {
				t.armTrackLocked(st)
			}
			return sleepAction{}
		}
		// 已經停播或沒有曲目:自然播畢,直接解除定時器,不必多送一次 Pause。
		if st == nil || !st.Playing || st.Track == nil {
			t.disarmLocked()
			return sleepAction{}
		}
		curKey := sleepTrackKey(st.Track)
		// 換到下一首,或單曲循環從尾端繞回開頭:立刻 Pause 並解除。
		wrapped := t.armedNearEnd && st.ProgressMS+5000 < t.lastTrackProg
		if curKey != t.armedTrack || wrapped {
			t.disarmLocked()
			return sleepAction{kind: sleepActExpirePause, gen: gen, prov: prov, restoreVol: -1}
		}
		t.lastTrackProg = st.ProgressMS
		if st.Track.DurationMS > 0 {
			t.lastTrackDur = st.Track.DurationMS
			if st.Track.DurationMS-st.ProgressMS <= 5000 {
				t.armedNearEnd = true
			}
		}
		return sleepAction{}

	case SleepCountdown:
		rem := t.deadline.Sub(now)
		if rem <= 0 {
			restore := -1
			if t.faded {
				restore = t.fadeOrigVol
			}
			t.disarmLocked()
			return sleepAction{kind: sleepActExpirePause, gen: gen, prov: prov, restoreVol: restore}
		}
		return t.planFadeLocked(rem, gen, prov)

	case SleepYuzuPomodoro:
		rem := t.deadline.Sub(now)
		if t.phase == SleepPhaseFocus {
			if rem <= 0 {
				restore := -1
				if t.faded {
					restore = t.fadeOrigVol
				}
				t.cycles++
				t.phase = SleepPhaseBreak
				t.totalDur = t.breakDur
				t.deadline = now.Add(t.breakDur)
				t.resetFadeLocked()
				return sleepAction{kind: sleepActPomodoroBreak, gen: gen, prov: prov, restoreVol: restore}
			}
			return t.planFadeLocked(rem, gen, prov)
		}
		if rem <= 0 {
			t.phase = SleepPhaseFocus
			t.totalDur = t.focusDur
			t.deadline = now.Add(t.focusDur)
			t.resetFadeLocked()
			return sleepAction{kind: sleepActPomodoroFocus, gen: gen, prov: prov, restoreVol: -1}
		}
		return sleepAction{}
	}
	return sleepAction{}
}

// planFadeLocked:在倒數最後 30 秒內規劃最多 5 階平滑降音(6s 一階:80% → 60% → 40% → 20% → 10%)。
func (t *sleepTimer) planFadeLocked(rem time.Duration, gen uint64, prov string) sleepAction {
	if t.fadeDisabled || rem <= 0 || rem > sleepFadeWindow || t.totalDur < sleepFadeWindow || prov == "" {
		return sleepAction{}
	}
	if !t.fadeOrigKnown {
		st := t.lastState
		if st == nil || !st.Playing || !st.Device.VolumeKnown || st.Device.VolumePct <= 0 {
			return sleepAction{}
		}
		t.fadeOrigKnown = true
		t.fadeOrigVol = min(100, st.Device.VolumePct)
		t.fadeLastSet = t.fadeOrigVol
	}
	stepDur := sleepFadeWindow / sleepFadeSteps // 6s
	// rem in (24s,30s] → step 1; (18s,24s] → step 2; ...; (0s,6s] → step 5
	elapsedInWindow := sleepFadeWindow - rem
	step := int(elapsedInWindow/stepDur) + 1
	if step < 1 {
		step = 1
	}
	if step > sleepFadeSteps {
		step = sleepFadeSteps
	}
	if step <= t.fadeLastStep {
		return sleepAction{}
	}
	var wantVol int
	if step < sleepFadeSteps {
		wantVol = t.fadeOrigVol * (sleepFadeSteps - step) / sleepFadeSteps
	} else {
		wantVol = t.fadeOrigVol / 10
	}
	if wantVol < 1 {
		wantVol = 1
	}
	if wantVol >= t.fadeLastSet {
		t.fadeLastStep = step
		return sleepAction{}
	}
	return sleepAction{
		kind:     sleepActFade,
		gen:      gen,
		prov:     prov,
		fadeStep: step,
		fadeVol:  wantVol,
	}
}

func (t *sleepTimer) executeAction(ctx context.Context, act sleepAction) {
	if act.prov == "" {
		return
	}
	pc, err := t.controller(act.prov)
	if err != nil || pc == nil {
		if act.kind == sleepActFade {
			t.mu.Lock()
			if t.gen == act.gen {
				t.fadeDisabled = true
			}
			t.mu.Unlock()
		}
		return
	}

	switch act.kind {
	case sleepActFade:
		cctx, cancel := t.callCtx(ctx)
		err := pc.SetVolume(cctx, act.fadeVol)
		cancel()
		t.mu.Lock()
		if t.gen == act.gen {
			if err != nil {
				// 遇 ErrVolumeNotAllowed 或任何錯誤立即停止後續降音,改等時間到直接 Pause。
				t.fadeDisabled = true
			} else {
				t.faded = true
				t.fadeLastStep = act.fadeStep
				t.fadeLastSet = act.fadeVol
			}
		}
		t.mu.Unlock()
		if err == nil {
			t.settle()
		}

	case sleepActExpirePause, sleepActPomodoroBreak:
		cctx, cancel := t.callCtx(ctx)
		err := pc.Pause(cctx)
		cancel()
		if err == nil {
			// 到點 Pause 成功後,若曾降音則自動呼叫一次 SetVolume 將音量設回原本數值。
			if act.restoreVol >= 0 {
				rctx, rcancel := t.callCtx(ctx)
				_ = pc.SetVolume(rctx, act.restoreVol)
				rcancel()
			}
			t.mu.Lock()
			if t.lastState != nil && t.lastProv == act.prov {
				t.lastState.Playing = false
				if act.restoreVol >= 0 {
					t.lastState.Device.VolumePct = act.restoreVol
				}
			}
			t.mu.Unlock()
			t.settle()
		}

	case sleepActPomodoroFocus:
		cctx, cancel := t.callCtx(ctx)
		err := pc.Play(cctx, provider.PlayRequest{})
		cancel()
		if err == nil {
			t.mu.Lock()
			if t.lastState != nil && t.lastProv == act.prov {
				t.lastState.Playing = true
			}
			t.mu.Unlock()
			t.settle()
		}
	}
}

func (t *sleepTimer) callCtx(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = t.ctx
	}
	return context.WithTimeout(provider.WithoutWait(parent), t.opts.CallTimeout)
}

func (t *sleepTimer) controller(prov string) (provider.PlaybackController, error) {
	if t.opts.Controller != nil {
		return t.opts.Controller(prov)
	}
	p, err := newProvider(t.ctx, prov)
	if err != nil {
		return nil, err
	}
	return asPlayback(p)
}

func (t *sleepTimer) settle() {
	if t.opts.OnSettle != nil {
		t.opts.OnSettle()
	}
}

func (t *sleepTimer) seedStateLocked(provID string, now time.Time) {
	if t.opts.CurrentState != nil {
		if cp, st := t.opts.CurrentState(provID); st != nil {
			if cp != "" {
				t.lastProv = cp
			}
			t.lastState = clonePlaybackState(st)
			t.lastSeenAt = now
		} else if cp != "" && t.lastProv == "" {
			t.lastProv = cp
		}
	}
	if provID != "" {
		t.lastProv = provID
	}
}

func (t *sleepTimer) resolveProviderLocked() string {
	if t.targetProv != "" {
		return t.targetProv
	}
	if t.lastProv != "" {
		return t.lastProv
	}
	if t.opts.CurrentState != nil {
		if cp, _ := t.opts.CurrentState(""); cp != "" {
			return cp
		}
	}
	return loadDefaultProvider()
}

func (t *sleepTimer) armTrackLocked(st *provider.PlaybackState) {
	t.armedTrack = sleepTrackKey(st.Track)
	t.lastTrackProg = st.ProgressMS
	t.lastTrackDur = st.Track.DurationMS
	t.armedNearEnd = st.Track.DurationMS > 0 && st.Track.DurationMS-st.ProgressMS <= 5000
}

func (t *sleepTimer) disarmLocked() {
	t.stopTickerLocked()
	t.mode = SleepOff
	t.phase = SleepPhaseNone
	t.deadline = time.Time{}
	t.totalDur = 0
	t.armedTrack = ""
	t.armedNearEnd = false
	t.lastTrackProg = 0
	t.lastTrackDur = 0
	t.resetFadeLocked()
}

func (t *sleepTimer) resetFadeLocked() {
	t.fadeOrigKnown = false
	t.fadeOrigVol = 0
	t.fadeLastSet = 0
	t.fadeLastStep = 0
	t.faded = false
	t.fadeDisabled = false
}

func (t *sleepTimer) ensureTickerLocked() {
	t.stopTickerLocked()
	if t.opts.TickInterval <= 0 {
		return
	}
	loopCtx, cancel := context.WithCancel(t.ctx)
	t.stopTick = cancel
	interval := t.opts.TickInterval
	go func() {
		tk := time.NewTicker(interval)
		defer tk.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-tk.C:
				t.Tick(loopCtx)
			}
		}
	}()
}

func (t *sleepTimer) stopTickerLocked() {
	if t.stopTick != nil {
		t.stopTick()
		t.stopTick = nil
	}
}

func sleepTrackKey(tr *provider.Track) string {
	if tr == nil {
		return ""
	}
	if tr.ProviderID != "" {
		return "id:" + tr.ProviderID
	}
	return "meta:" + tr.Title + "\x00" + strings.Join(tr.Artists, "\x00") + "\x00" + strconv.Itoa(tr.DurationMS)
}

func clonePlaybackState(st *provider.PlaybackState) *provider.PlaybackState {
	if st == nil {
		return nil
	}
	cp := *st
	if st.Track != nil {
		tr := *st.Track
		cp.Track = &tr
	}
	return &cp
}

func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}
