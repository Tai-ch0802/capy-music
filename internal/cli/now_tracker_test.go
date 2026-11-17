package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func TestNowTTLAppleTieredThrottlingAndSettleInvalidation(t *testing.T) {
	if got := nowTTL("apple", nil, provider.ErrPlayerNotRunning); got != 4*time.Second {
		t.Errorf("Apple err != nil TTL = %v, want 4s", got)
	}
	if got := nowTTL("apple", nil, errors.New("osascript failed")); got != 4*time.Second {
		t.Errorf("Apple generic err TTL = %v, want 4s", got)
	}
	if got := nowTTL("apple", nil, nil); got != 3*time.Second {
		t.Errorf("Apple st == nil TTL = %v, want 3s", got)
	}
	if got := nowTTL("apple", nowTrack(false, "Paused", 1000, 200000), nil); got != 3*time.Second {
		t.Errorf("Apple !Playing TTL = %v, want 3s", got)
	}
	if got := nowTTL("apple", nowTrack(true, "Playing", 1000, 200000), nil); got != 2*time.Second {
		t.Errorf("Apple Playing TTL = %v, want 2s", got)
	}
	if got := nowTTL("apple", nil, nowBuildErr{errors.New("not logged in")}); got != webNowFailTTL {
		t.Errorf("Apple build err TTL = %v, want %v", got, webNowFailTTL)
	}

	// 驗證快取命中與 settleNow() 立即作廢(含播放中 2s、暫停 3s、錯誤 4s)。
	advance := fakeNowClock(t)
	ap := newNowFakeAs("apple", nowTrack(true, "Sugar", 10000, 200000))
	trk := &nowTracker{ctx: context.Background(), now: map[string]provider.PlaybackController{"apple": ap}}

	res := trk.consult("apple")
	if !res.fresh || ap.calls.Load() != 1 {
		t.Fatalf("首輪應真實查詢:fresh=%v calls=%d", res.fresh, ap.calls.Load())
	}
	advance(1500 * time.Millisecond)
	res = trk.consult("apple")
	if res.fresh || ap.calls.Load() != 1 || res.st.ProgressMS != 11500 {
		t.Fatalf("2s 內應命中快取並推進進度:fresh=%v calls=%d pos=%d", res.fresh, ap.calls.Load(), res.st.ProgressMS)
	}

	// settleNow() 後即使未滿 2s 也必須立即失效重查!
	ap.set(nowTrack(false, "Sugar", 11500, 200000), nil)
	trk.settleNow()
	res = trk.consult("apple")
	if !res.fresh || ap.calls.Load() != 2 || res.st.Playing {
		t.Fatalf("settleNow 後應立即重查:fresh=%v calls=%d playing=%v", res.fresh, ap.calls.Load(), res.st.Playing)
	}

	// 離開安定期後檢查暫停 3s 與錯誤 4s 邊界。
	advance(webNowSettle + time.Second)
	_ = trk.consult("apple") // calls = 3, until = +3s
	advance(2900 * time.Millisecond)
	if r := trk.consult("apple"); r.fresh || ap.calls.Load() != 3 {
		t.Fatalf("暫停 3s 內應命中快取:fresh=%v calls=%d", r.fresh, ap.calls.Load())
	}
	ap.set(nil, provider.ErrPlayerNotRunning)
	advance(100 * time.Millisecond)
	if r := trk.consult("apple"); !r.fresh || ap.calls.Load() != 4 || !errors.Is(r.err, provider.ErrPlayerNotRunning) {
		t.Fatalf("滿 3s 應重查並拿到 ErrPlayerNotRunning:fresh=%v calls=%d err=%v", r.fresh, ap.calls.Load(), r.err)
	}
	advance(3900 * time.Millisecond)
	if r := trk.consult("apple"); r.fresh || ap.calls.Load() != 4 {
		t.Fatalf("錯誤 4s 內應命中快取:fresh=%v calls=%d", r.fresh, ap.calls.Load())
	}
	advance(100 * time.Millisecond)
	if r := trk.consult("apple"); !r.fresh || ap.calls.Load() != 5 {
		t.Fatalf("滿 4s 應重查:fresh=%v calls=%d", r.fresh, ap.calls.Load())
	}
}
