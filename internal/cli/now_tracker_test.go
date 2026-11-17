package cli

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
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

func TestNowTrackerPlayJournalAfter30s(t *testing.T) {
	setCLITestConfig(t)
	advance := fakeNowClock(t)

	mkState := func(playing bool, id, isrc, title, artist, album string, durMS int) *provider.PlaybackState {
		return &provider.PlaybackState{
			Playing:    playing,
			ProgressMS: 5000,
			Track: &provider.Track{
				ProviderID: id,
				ISRC:       isrc,
				Title:      title,
				Artists:    []string{artist},
				Album:      album,
				DurationMS: durMS,
			},
			Device: provider.Device{Name: "MacBook", VolumePct: 50, VolumeKnown: true},
		}
	}

	sp := newNowFakeAs("spotify", mkState(true, "sp-1", "TWUM71600001", "派對動物", "五月天", "自傳", 249000))
	trk := testTracker(t, "spotify", sp)

	var writes atomic.Int32
	origWrite := nowWritePlayJournal
	nowWritePlayJournal = func(entry store.PlayJournalEntry) {
		if !trk.pollMu.TryLock() {
			t.Error("RecordPlayJournal 必須在 pollMu 鎖外執行")
		} else {
			trk.pollMu.Unlock()
		}
		writes.Add(1)
		origWrite(entry)
	}
	t.Cleanup(func() { nowWritePlayJournal = origWrite })

	// 1. 未滿 30 秒(0s → 10s → 29.9s):不應寫入任何 play_journal(連 state.db 都不該建立)。
	if _, ok := trk.stepRound(""); !ok {
		t.Fatal("首輪 stepRound 應成功")
	}
	advance(10 * time.Second)
	trk.stepRound("")
	advance(19*time.Second + 900*time.Millisecond) // 累計 29.9s
	trk.stepRound("")
	if got := writes.Load(); got != 0 {
		t.Fatalf("未滿 30s 不應寫入:writes=%d", got)
	}
	if _, err := store.OpenReadOnly(50 * time.Millisecond); !errors.Is(err, store.ErrNoDB) {
		t.Fatalf("未滿 30s 前不應建立 state.db:err=%v", err)
	}

	// 2. 暫停(停止播放)會重置同一輪計數:暫停期間經過 60s 不算播放時間,恢復後再播 20s 仍未滿 30s。
	sp.set(mkState(false, "sp-1", "TWUM71600001", "派對動物", "五月天", "自傳", 249000), nil)
	trk.settleNow()
	advance(time.Second)
	trk.stepRound("")
	advance(60 * time.Second)
	trk.stepRound("")
	if got := writes.Load(); got != 0 {
		t.Fatalf("暫停期間不應累加或寫入:writes=%d", got)
	}

	sp.set(mkState(true, "sp-1", "TWUM71600001", "派對動物", "五月天", "自傳", 249000), nil)
	trk.settleNow()
	trk.stepRound("") // 新一輪起算點
	advance(20 * time.Second)
	trk.stepRound("")
	if got := writes.Load(); got != 0 {
		t.Fatalf("重置後僅播 20s 不應寫入:writes=%d", got)
	}

	// 3. 跨過 30s 門檻(20s + 10s = 30s):寫入 1 筆。
	advance(10 * time.Second)
	trk.stepRound("")
	if got := writes.Load(); got != 1 {
		t.Fatalf("滿 30s 應寫入恰好 1 次:writes=%d", got)
	}

	readJournal := func() []store.PlayJournalEntry {
		t.Helper()
		s, err := store.OpenReadOnly(100 * time.Millisecond)
		if err != nil {
			t.Fatalf("OpenReadOnly:%v", err)
		}
		defer s.Close()
		list, err := s.ListPlayJournal(0)
		if err != nil {
			t.Fatalf("ListPlayJournal:%v", err)
		}
		return list
	}

	entries := readJournal()
	if len(entries) != 1 {
		t.Fatalf("play_journal 筆數 = %d, want 1", len(entries))
	}
	got := entries[0]
	if got.Provider != "spotify" || got.ProviderID != "sp-1" || got.ISRC != "TWUM71600001" ||
		got.Title != "派對動物" || !slices.Equal(got.Artists, []string{"五月天"}) ||
		got.Album != "自傳" || got.DurationMS != 249000 {
		t.Fatalf("play_journal 內容不符:%+v", got)
	}

	// 4. 同一首歌持續播放(含跨過 10 分鐘去重窗):同一輪絕不重複觸發寫入。
	for _, d := range []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 12 * time.Minute} {
		advance(d)
		trk.stepRound("")
	}
	if n := writes.Load(); n != 1 {
		t.Fatalf("同首續播不應重複寫入:writes=%d, want 1", n)
	}
	if entries = readJournal(); len(entries) != 1 {
		t.Fatalf("同首續播後 play_journal 仍應為 1 筆,得 %d", len(entries))
	}

	// 5. 換歌未滿 30s 即再換歌 → 跳過短播曲目;新歌播滿 30s → 寫入第 2 筆。
	sp.set(mkState(true, "sp-2", "TWUM71600002", "頑固", "五月天", "自傳", 265000), nil)
	trk.settleNow()
	trk.stepRound("")
	advance(25 * time.Second)
	trk.stepRound("")

	sp.set(mkState(true, "sp-3", "TWUM71600003", "後來的我們", "五月天", "自傳", 318000), nil)
	trk.settleNow()
	trk.stepRound("")
	advance(31 * time.Second)
	trk.stepRound("")

	if n := writes.Load(); n != 2 {
		t.Fatalf("sp-2 未滿 30s 跳過、sp-3 滿 30s 寫入,writes=%d, want 2", n)
	}
	entries = readJournal()
	if len(entries) != 2 || entries[0].ProviderID != "sp-3" || entries[1].ProviderID != "sp-1" {
		t.Fatalf("預期最新在前 [sp-3, sp-1],得 %+v", entries)
	}
}

func TestNowTrackerPlayJournalSkipsWhenConfigDirUnsetInTests(t *testing.T) {
	advance := fakeNowClock(t)
	sp := newNowFakeAs("spotify", nowTrack(true, "SafeGuard", 0, 200000))
	trk := testTracker(t, "spotify", sp)

	// 模擬個別未設 CAPY_CONFIG_DIR 的測試環境:writePlayJournalToStore 必須安全略過,絕不碰真實家目錄。
	t.Setenv("CAPY_CONFIG_DIR", "")

	var called atomic.Int32
	origWrite := nowWritePlayJournal
	nowWritePlayJournal = func(entry store.PlayJournalEntry) {
		called.Add(1)
		origWrite(entry)
	}
	t.Cleanup(func() { nowWritePlayJournal = origWrite })

	trk.stepRound("")
	advance(35 * time.Second)
	trk.stepRound("")
	if called.Load() != 1 {
		t.Fatalf("門檻仍應觸發 1 次接縫呼叫:called=%d", called.Load())
	}
}
