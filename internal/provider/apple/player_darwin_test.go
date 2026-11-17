//go:build darwin

package apple

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// stubOSA:每個 osascript 都回 out。runOpen 一起換掉而且不准被呼叫——只換 runOSA 的話,走到打開頁面的測試會在
// 開發機與 CI 上真的 open music://、把 Music.app 叫起來。
func stubOSA(t *testing.T, out string) *[]string {
	t.Helper()
	var scripts []string
	origOSA, origOpen := runOSA, runOpen
	runOSA = func(_ context.Context, script string, _ ...string) (string, error) {
		scripts = append(scripts, script)
		return out, nil
	}
	runOpen = func(u string) error { t.Errorf("這個測試不該打開 %s", u); return nil }
	t.Cleanup(func() { runOSA, runOpen = origOSA, origOpen })
	return &scripts
}

type osaCall struct {
	script string
	args   []string
}

type playStub struct {
	calls  []osaCall
	opened []string
}

// stubPlay:資料庫查詢回 match / matchErr,播放確認回 play / playErr;runOpen 只記下網址。
func stubPlay(t *testing.T, match string, matchErr error, play string, playErr error) *playStub {
	t.Helper()
	ps := &playStub{}
	origOSA, origOpen := runOSA, runOpen
	runOSA = func(_ context.Context, script string, args ...string) (string, error) {
		ps.calls = append(ps.calls, osaCall{script, args})
		switch script {
		case libraryMatchScript:
			return match, matchErr
		case playLibraryScript:
			return play, playErr
		}
		t.Errorf("非預期的腳本:%q", script)
		return "", nil
	}
	runOpen = func(u string) error { ps.opened = append(ps.opened, u); return nil }
	t.Cleanup(func() { runOSA, runOpen = origOSA, origOpen })
	return ps
}

func songProvider(t *testing.T) *Provider {
	t.Helper()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[%s]}`, songJSONFx("s1")) // 派對動物 / 五月天 / 自傳 / 227000 ms
	})
	return &Provider{c: c, storefront: "tw"}
}

func TestStateParsesPlaying(t *testing.T) {
	stubOSA(t, "playing\t派對動物\t五月天\t自傳\t227500\t61200")
	p := &Provider{}
	st, err := p.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Playing || st.Track == nil || st.Track.Title != "派對動物" || st.Track.Artists[0] != "五月天" ||
		st.Track.DurationMS != 227500 || st.ProgressMS != 61200 || st.Device.Name != "Music.app" {
		t.Errorf("state = %+v track=%+v", st, st.Track)
	}
}

func TestStateStoppedIsNil(t *testing.T) {
	stubOSA(t, "stopped")
	st, err := (&Provider{}).State(context.Background())
	if err != nil || st != nil {
		t.Fatalf("stopped 應回 (nil, nil):(%+v, %v)", st, err)
	}
}

// TestStateRejectsUnparsableNumbers:locale 用 "," 當小數點(如 de/fr macOS)時,數值欄位
// 必須回錯,不能被 strconv 靜默吞成 0(review finding 1)。
func TestStateRejectsUnparsableNumbers(t *testing.T) {
	stubOSA(t, "playing\t派對動物\t五月天\t自傳\t227,5\t61200")
	if _, err := (&Provider{}).State(context.Background()); err == nil {
		t.Fatal("數值欄位無法解析時應回錯,不應靜默為 0")
	}
}

func TestPauseNextPrevScripts(t *testing.T) {
	scripts := stubOSA(t, "")
	p := &Provider{}
	_ = p.Pause(context.Background())
	_ = p.Next(context.Background())
	_ = p.Prev(context.Background())
	want := []string{"pause", "next track", "previous track"}
	for i, w := range want {
		if !strings.Contains((*scripts)[i], `tell application "Music" to `+w) {
			t.Errorf("script[%d] = %q, want contains %q", i, (*scripts)[i], w)
		}
	}
}

func TestPlayResume(t *testing.T) {
	scripts := stubOSA(t, "")
	if err := (&Provider{}).Play(context.Background(), provider.PlayRequest{}); err != nil {
		t.Fatal(err)
	}
	if len(*scripts) != 1 || !strings.Contains((*scripts)[0], `tell application "Music" to play`) {
		t.Errorf("resume script = %v", *scripts)
	}
}

// TestPlayTrackOpensSongURLWhenNotInLibrary:【fails-before-fix】資料庫裡沒有這首:用單曲網址在 Music.app 打開並標出那一首,
// 回 OpenedError(帶「歌名 — 歌手」)——不是謊報成功。以前走 AppleScript open location,在 macOS 26 連頁面都不換,capy 照印 ▶。
func TestPlayTrackOpensSongURLWhenNotInLibrary(t *testing.T) {
	ps := stubPlay(t, "", nil, "", nil)
	err := songProvider(t).Play(context.Background(), provider.PlayRequest{TrackIDs: []string{"s1"}})
	var oe *provider.OpenedError
	if !errors.Is(err, provider.ErrOpenedNotPlaying) || !errors.As(err, &oe) || oe.Label != "派對動物 — 五月天" {
		t.Fatalf("要回帶歌名的 OpenedError:%#v", err)
	}
	if !slices.Equal(ps.opened, []string{"music://music.apple.com/tw/song/s1"}) {
		t.Errorf("要用單曲網址打開:%v", ps.opened)
	}
	if len(ps.calls) != 1 || !slices.Equal(ps.calls[0].args, []string{"派對動物", "五月天", "自傳"}) {
		t.Errorf("先查資料庫,值經 argv 傳(不進腳本原始碼):%+v", ps.calls)
	}
	for _, c := range ps.calls {
		if strings.Contains(c.script, "open location") || strings.Contains(c.script, "music://") || strings.Contains(c.script, "派對動物") {
			t.Errorf("腳本裡不可以有 open location、網址或歌名:%q", c.script)
		}
	}
}

// TestPlayTrackPlaysLibraryMatch:資料庫裡剛好一首:用 AppleScript 播它、確認真的在播,回 nil(CLI 印 ▶),不打開頁面。
func TestPlayTrackPlaysLibraryMatch(t *testing.T) {
	ps := stubPlay(t, "PID1\t227000\t1", nil, "pid", nil)
	if err := songProvider(t).Play(context.Background(), provider.PlayRequest{TrackIDs: []string{"s1"}}); err != nil {
		t.Fatalf("資料庫裡的歌要真的播:%v", err)
	}
	if len(ps.calls) != 2 || ps.calls[1].script != playLibraryScript || !slices.Equal(ps.calls[1].args, []string{"PID1"}) {
		t.Errorf("第二個腳本播那一首(persistent ID 經 argv):%+v", ps.calls)
	}
	if len(ps.opened) != 0 {
		t.Errorf("真的播了就不打開頁面:%v", ps.opened)
	}
}

// TestPlayTrackFallsBackToOpen:對到了但沒確認到在播、或 osascript 出錯(沒有自動化權限、Music.app 剛啟動),一律退回打開頁面,
// 不當失敗、也不謊報 ▶。
func TestPlayTrackFallsBackToOpen(t *testing.T) {
	for name, ps := range map[string]func() *playStub{
		"沒確認到在播":  func() *playStub { return stubPlay(t, "PID1\t227000\t1", nil, "unconfirmed", nil) },
		"播放腳本出錯":  func() *playStub { return stubPlay(t, "PID1\t227000\t1", nil, "", errors.New("exit status 1")) },
		"資料庫查詢出錯": func() *playStub { return stubPlay(t, "", errors.New("-1743"), "", nil) },
	} {
		st := ps()
		err := songProvider(t).Play(context.Background(), provider.PlayRequest{TrackIDs: []string{"s1"}})
		if !errors.Is(err, provider.ErrOpenedNotPlaying) || len(st.opened) != 1 {
			t.Errorf("%s:要退回打開頁面:%v %v", name, err, st.opened)
		}
	}
}

// TestPlayTrackNotFoundDoesNothing:id 不存在就照實回錯,不查資料庫、不打開頁面。
func TestPlayTrackNotFoundDoesNothing(t *testing.T) {
	ps := stubPlay(t, "", nil, "", nil)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	err := (&Provider{c: c, storefront: "tw"}).Play(context.Background(), provider.PlayRequest{TrackIDs: []string{"nope"}})
	if !errors.Is(err, provider.ErrNotFound) || len(ps.calls) != 0 || len(ps.opened) != 0 {
		t.Fatalf("找不到要照實回錯、什麼都不做:%v %+v %v", err, ps.calls, ps.opened)
	}
}

// TestLibraryMatchPicks:計畫 §2.2 的兩步篩選。時長差 2 秒內剩一首就播(專輯名可能跟 catalog 不同);剩好幾首才看專輯,
// 剛好一首才算;其他一律交給打開頁面。
func TestLibraryMatchPicks(t *testing.T) {
	tr := provider.Track{Title: "Lost Stars", Artists: []string{"Adam Levine"}, Album: "V (Deluxe)", DurationMS: 267920}
	for _, tc := range []struct {
		name, out, want string
	}{
		{"沒有", "", ""},
		{"一首,差 1 秒", "A\t268920\t0", "A"},
		{"一首,專輯不同(Payphone:《Overexposed》對《… (Deluxe Version)》)", "A\t267920\t0", "A"},
		{"一首,差 2.5 秒", "A\t270420\t1", ""},
		{"三首同長度,一首同專輯(Lost Stars)", "A\t267920\t0\nB\t267920\t0\nC\t267920\t1", "C"},
		{"三首同長度,沒有同專輯", "A\t267920\t0\nB\t267920\t0\nC\t267920\t0", ""},
		{"兩首同長度同專輯(分不開)", "A\t267920\t1\nB\t267920\t1", ""},
		{"長度不對的那首不算", "A\t267920\t0\nB\t240187\t1", "A"},
		{"壞掉的行略過", "garbage\nA\tx\t1\nB\t267920\t0", "B"},
	} {
		stubOSA(t, tc.out)
		if got := libraryMatch(tr); got != tc.want {
			t.Errorf("%s:%q,要 %q", tc.name, got, tc.want)
		}
	}
	for name, bad := range map[string]provider.Track{
		"沒有歌名": {Artists: []string{"a"}, DurationMS: 1},
		"沒有歌手": {Title: "t", DurationMS: 1},
		"歌手空白": {Title: "t", Artists: []string{""}, DurationMS: 1},
		"沒有時長": {Title: "t", Artists: []string{"a"}},
	} {
		scripts := stubOSA(t, "A\t0\t1")
		if got := libraryMatch(bad); got != "" || len(*scripts) != 0 {
			t.Errorf("%s:不查資料庫:%q %v", name, got, *scripts)
		}
	}
}

// TestRunOSAPassesArgsAfterDoubleDash:值經 argv 傳,前面一定有 "--"——沒有的話 "-e" 會被當成更多原始碼、"-x" 會被當成旗標。
// 跑真的 osascript,腳本不碰 Music.app。
func TestRunOSAPassesArgsAfterDoubleDash(t *testing.T) {
	const echo = "on run argv\nreturn item 1 of argv\nend run"
	for _, v := range []string{"-e", "-ing", `a"b\c`, "Déjà Vu"} {
		got, err := runOSA(context.Background(), echo, v)
		if err != nil || got != v {
			t.Errorf("argv %q 要原樣到腳本:%q %v", v, got, err)
		}
	}
}

// TestPlayScriptsCompile:所有 AppleScript 都要編得過(osacompile 不會啟動 Music.app),且反組譯後不得殘留無法解析的 «...» 字典符號。
func TestPlayScriptsCompile(t *testing.T) {
	if _, err := exec.LookPath("osacompile"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("CI 的 macOS 上要有 osacompile")
		}
		t.Skip("沒有 osacompile")
	}
	hasDecompile := false
	if _, err := exec.LookPath("osadecompile"); err == nil {
		hasDecompile = true
	}
	for name, script := range map[string]string{
		"libraryMatchScript": libraryMatchScript,
		"playLibraryScript":  playLibraryScript,
		"stateScript":        stateScript,
		"devicesScript":      devicesScript,
		"transferScript":     transferScript,
		"setShuffleScript":   setShuffleScript,
		"setRepeatScript":    setRepeatScript,
	} {
		scptPath := filepath.Join(t.TempDir(), name+".scpt")
		out, err := exec.Command("osacompile", "-o", scptPath, "-e", script).CombinedOutput()
		if err != nil {
			t.Errorf("%s 編不過:%v %s", name, err, out)
			continue
		}
		if hasDecompile {
			dec, derr := exec.Command("osadecompile", scptPath).CombinedOutput()
			if derr != nil {
				t.Errorf("%s 反組譯失敗:%v %s", name, derr, dec)
			} else if strings.Contains(string(dec), "«") {
				t.Errorf("%s 反組譯含未解析的原始碼符號 «...»:\n%s", name, dec)
			}
		}
	}
}

func TestPlayPlaylistNotSupportedAndNoOSA(t *testing.T) {
	scripts := stubOSA(t, "")
	err := (&Provider{}).Play(context.Background(), provider.PlayRequest{PlaylistID: "p.abc"})
	if !errors.Is(err, provider.ErrNotSupported) || !strings.Contains(err.Error(), "pl show") {
		t.Fatalf("清單播放應回 ErrNotSupported 且指向 pl show:%v", err)
	}
	if len(*scripts) != 0 {
		t.Errorf("不得執行任何 AppleScript:%v", *scripts)
	}
}

func TestStateDoesNotLaunchMusicWhenNotRunning(t *testing.T) {
	stubOSA(t, "not running")
	st, err := (&Provider{}).State(context.Background())
	if st != nil || !errors.Is(err, ErrNotRunning) || !errors.Is(err, provider.ErrPlayerNotRunning) {
		t.Fatalf("Music 未執行應回 ErrNotRunning:%v %v", st, err)
	}
	if i, j := strings.Index(stateScript, `is running`), strings.Index(stateScript, `tell application "Music"`); i < 0 || j < 0 || i > j {
		t.Fatalf("stateScript 必須在進 tell 區塊前先檢查 is running(否則會啟動 Music.app):%q", stateScript)
	}
}

// ── PR B 審查(2026-09-27)──

// TestPlayTrackStopsWhenCancelled:【fails-before-fix】使用者喊停(終端機的 Ctrl-C 會連 osascript 一起殺掉)之後,不再開始播放、
// 不再打開 Music.app,照實回取消——以前 osascript 被殺掉的錯被當成「沒找到」,照樣打開頁面、印「已打開」。
func TestPlayTrackStopsWhenCancelled(t *testing.T) {
	for _, during := range []string{libraryMatchScript, playLibraryScript} {
		ctx, cancel := context.WithCancel(context.Background())
		var scripts []string
		var opened []string
		origOSA, origOpen := runOSA, runOpen
		runOSA = func(_ context.Context, script string, _ ...string) (string, error) {
			scripts = append(scripts, script)
			if script == during {
				cancel() // = Ctrl-C 落在這一步
				return "", errors.New("signal: interrupt")
			}
			return "PID1\t227000\t1", nil
		}
		runOpen = func(u string) error { opened = append(opened, u); return nil }
		err := songProvider(t).Play(ctx, provider.PlayRequest{TrackIDs: []string{"s1"}})
		runOSA, runOpen = origOSA, origOpen
		if !errors.Is(err, context.Canceled) || len(opened) != 0 {
			t.Errorf("取消落在 %.20q:要回取消、不打開頁面:%v %v", during, err, opened)
		}
		if during == libraryMatchScript && len(scripts) != 1 {
			t.Errorf("查資料庫時取消:不再開始播放:%d 個腳本", len(scripts))
		}
	}
}

// TestPlayTrackOpenFailureIsAnError:open 失敗就照實回錯,不能說「已在 Music.app 打開」。
func TestPlayTrackOpenFailureIsAnError(t *testing.T) {
	stubPlay(t, "", nil, "", nil)
	runOpen = func(string) error { return errors.New("LSOpenURLsWithRole() failed with error -10814") }
	err := songProvider(t).Play(context.Background(), provider.PlayRequest{TrackIDs: []string{"s1"}})
	if err == nil || errors.Is(err, provider.ErrOpenedNotPlaying) {
		t.Fatalf("open 失敗要回錯,不是「已打開」:%v", err)
	}
}

// TestPlayScriptsGuards:兩支腳本裡刻意的寫法(真機量出來的),拿掉編譯照樣過、測試替身也看不出來,所以把結構釘住:
// 確認播放只認 persistent ID(用歌名補判的話,同名的另一首還在播時會誤報 ▶);whose 只放文字條件(加上 media kind 會切成
// 逐碼位比對,NFD 的歌名就對不到);專輯比對忽略變音符號。
func TestPlayScriptsGuards(t *testing.T) {
	if n := strings.Count(playLibraryScript, `return "pid"`); n != 1 {
		t.Fatalf(`playLibraryScript 只能有一個 return "pid":%d`, n)
	}
	for _, line := range strings.Split(playLibraryScript, "\n") {
		if strings.Contains(line, `return "pid"`) && !strings.Contains(line, "if persistent ID of current track is wantPID then") {
			t.Errorf("確認播放只認 persistent ID:%q", line)
		}
	}
	if !strings.Contains(playLibraryScript, "if player state is playing then") {
		t.Error("要先確認播放中")
	}
	for _, line := range strings.Split(libraryMatchScript, "\n") {
		if strings.Contains(line, "whose") && !strings.HasSuffix(strings.TrimSpace(line), "whose name is wantName and artist is wantArtist)") {
			t.Errorf("whose 只放文字條件:%q", line)
		}
	}
	i, j, k := strings.Index(libraryMatchScript, "ignoring diacriticals"), strings.Index(libraryMatchScript, "album of t is wantAlbum"), strings.Index(libraryMatchScript, "end ignoring")
	if i < 0 || !(i < j && j < k) {
		t.Error("專輯比對要在 ignoring diacriticals 裡")
	}
}

// TestRunOSAKilledWhenCtxDone:【#97 審查】ctx 到期就殺掉 osascript。Music.app 卡住時一次 AppleEvent 要等一兩分鐘才自己逾時,
// TUI 與 web 的輪詢拿著單飛鎖等它,所有平台的狀態都跟著停住。跑真的 osascript,腳本只 delay、不碰 Music.app。
func TestRunOSAKilledWhenCtxDone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := runOSA(ctx, "delay 5"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("被 ctx 殺掉要回逾時本身(結束碼 130 / 143、web 的「已中止」才認得),不是 signal: killed:%v", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("ctx 到期就要結束,不是等腳本跑完:%v", el)
	}
}

type ctxMark struct{}

// TestStateAndControlsPassCtx:State 與控制指令把呼叫端的 ctx 交給 runOSA(TUI 的 State 逾時、按鍵的期限才管得到 osascript);
// Play 查資料庫與確認播放的兩支腳本刻意不吃取消(見 Play 的註解)。
func TestStateAndControlsPassCtx(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxMark{}, true)
	var got []bool
	origOSA := runOSA
	runOSA = func(c context.Context, _ string, _ ...string) (string, error) {
		got = append(got, c.Value(ctxMark{}) != nil)
		return "stopped", nil
	}
	t.Cleanup(func() { runOSA = origOSA })
	p := &Provider{}
	_, _ = p.State(ctx)
	_, _ = p.Devices(ctx)
	_ = p.Play(ctx, provider.PlayRequest{})
	_ = p.Pause(ctx)
	_ = p.Next(ctx)
	_ = p.Prev(ctx)
	_ = p.Seek(ctx, 1000)
	_ = p.SetVolume(ctx, 50)
	_ = p.Transfer(ctx, "music.app", false)
	_ = p.SetShuffle(ctx, true)
	_ = p.SetRepeat(ctx, provider.RepeatOff)
	if len(got) != 11 || slices.Contains(got, false) {
		t.Errorf("每一個都要帶呼叫端的 ctx:%v", got)
	}
}

// TestStateSaysNotRespondingOnTimeout:State 等不下去(TUI 的逾時)是 Music.app 沒回應,不是「沒裝或沒授權自動化」。
func TestStateSaysNotRespondingOnTimeout(t *testing.T) {
	origOSA := runOSA
	runOSA = func(c context.Context, _ string, _ ...string) (string, error) {
		<-c.Done()
		return "", fmt.Errorf("osascript: %w", c.Err())
	}
	t.Cleanup(func() { runOSA = origOSA })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := (&Provider{}).State(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), i18n.T("apple.player.err.osascript_failed", "err", "")) {
		t.Errorf("要說沒回應、保留逾時本身:%v", err)
	}
}

func TestStateParsesNineFieldsAndVolumeZero(t *testing.T) {
	p := &Provider{}
	// 6 欄相容:VolumeKnown 仍為 false
	stubOSA(t, "playing\t派對動物\t五月天\t自傳\t227500\t61200")
	st6, err := p.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st6.Device.VolumeKnown || st6.Shuffle || st6.Repeat != provider.RepeatUnknown {
		t.Errorf("6 欄過渡格式不該誤標 VolumeKnown:%+v", st6)
	}

	// 9 欄:音量 75 + shuffle true + repeat all -> RepeatContext
	stubOSA(t, "playing\t派對動物\t五月天\t自傳\t227500\t61200\t75\ttrue\tall")
	st, err := p.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Device.VolumeKnown || st.Device.VolumePct != 75 || !st.Shuffle || st.Repeat != provider.RepeatContext {
		t.Errorf("9 欄狀態解析錯誤:%+v", st)
	}

	// 靜音 0 也要 VolumeKnown=true;repeat one -> RepeatTrack
	stubOSA(t, "paused\t派對動物\t五月天\t自傳\t227500\t61200\t0\tfalse\tone")
	st0, err := p.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st0.Playing || !st0.Device.VolumeKnown || st0.Device.VolumePct != 0 || st0.Shuffle || st0.Repeat != provider.RepeatTrack {
		t.Errorf("音量 0 與單曲循環解析錯誤:%+v", st0)
	}

	// repeat off -> RepeatOff
	stubOSA(t, "playing\t派對動物\t五月天\t自傳\t227500\t61200\t40\tfalse\toff")
	stOff, err := p.State(context.Background())
	if err != nil || stOff.Repeat != provider.RepeatOff {
		t.Errorf("repeat off 解析錯誤:%+v %v", stOff, err)
	}

	// vol = -1(讀不到音量時降級):VolumeKnown=false,VolumePct=0,不應炸掉整個 State(),且 shuffle/repeat 照常解析
	stubOSA(t, "playing\t派對動物\t五月天\t自傳\t227500\t61200\t-1\ttrue\tone")
	stNegVol, err := p.State(context.Background())
	if err != nil {
		t.Fatalf("vol=-1 應降級為 VolumeKnown=false 而非回錯:%v", err)
	}
	if stNegVol.Device.VolumeKnown || stNegVol.Device.VolumePct != 0 || !stNegVol.Shuffle || stNegVol.Repeat != provider.RepeatTrack {
		t.Errorf("vol=-1 降級狀態錯誤:%+v", stNegVol)
	}

	// 電台或串流緩衝中缺歌手/專輯/時長(missing value 退回空字串與 0)
	stubOSA(t, "playing\tApple Music 1\t\t\t0\t0\t-1\tfalse\toff")
	stStream, err := p.State(context.Background())
	if err != nil {
		t.Fatalf("電台/串流缺欄位不應回錯:%v", err)
	}
	if stStream.Track.Title != "Apple Music 1" || len(stStream.Track.Artists) != 0 || stStream.Track.Album != "" ||
		stStream.Track.DurationMS != 0 || stStream.ProgressMS != 0 || stStream.Device.VolumeKnown {
		t.Errorf("電台/串流欄位降級錯誤:%+v track=%+v", stStream, stStream.Track)
	}

	// 壞掉的音量欄位要回錯
	stubOSA(t, "playing\t派對動物\t五月天\t自傳\t227500\t61200\tbad\tfalse\toff")
	if _, err := p.State(context.Background()); err == nil {
		t.Error("音量欄位無法解析時應回錯")
	}
}

func TestDevicesAirPlayAndFallback(t *testing.T) {
	p := &Provider{}
	stubOSA(t, "AP1\tMacBook Pro\tcomputer\ttrue\t65\nAP2\tLiving Room HomePod\tHomePod\tfalse\t40")
	devs, err := p.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 ||
		devs[0] != (provider.Device{ID: "AP1", Name: "MacBook Pro", Type: "Computer", Active: true, VolumePct: 65, VolumeKnown: true}) ||
		devs[1] != (provider.Device{ID: "AP2", Name: "Living Room HomePod", Type: "HomePod", Active: false, VolumePct: 40, VolumeKnown: true}) {
		t.Fatalf("AirPlay 裝置解析錯誤:%+v", devs)
	}

	// 首筆無 persistent ID(開頭是 tab)不得被誤裁切,且 vol=-1 應降級為 VolumeKnown=false,VolumePct=0
	stubOSA(t, "\tMacBook Pro\tComputer\ttrue\t65\nAP2\tApple TV\tApple TV\tfalse\t-1")
	devs2, err := p.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs2) != 2 ||
		devs2[0] != (provider.Device{ID: "MacBook Pro", Name: "MacBook Pro", Type: "Computer", Active: true, VolumePct: 65, VolumeKnown: true}) ||
		devs2[1] != (provider.Device{ID: "AP2", Name: "Apple TV", Type: "Apple TV", Active: false, VolumePct: 0, VolumeKnown: false}) {
		t.Fatalf("無 persistent ID 與 vol=-1 裝置解析錯誤:%+v", devs2)
	}

	// 未啟動、空輸出或 osascript 失敗時一律靜默退回預設 Music.app 裝置
	for _, out := range []string{"not running", "", "malformed"} {
		stubOSA(t, out)
		ds, err := p.Devices(context.Background())
		if err != nil || len(ds) != 1 || ds[0].ID != musicDevice || !ds[0].Active {
			t.Errorf("out=%q 應退回預設單一裝置:%+v %v", out, ds, err)
		}
	}
	origOSA := runOSA
	runOSA = func(context.Context, string, ...string) (string, error) { return "", errors.New("-1743") }
	t.Cleanup(func() { runOSA = origOSA })
	if ds, err := p.Devices(context.Background()); err != nil || len(ds) != 1 || ds[0].ID != musicDevice {
		t.Errorf("osascript 失敗應靜默退回預設裝置:%+v %v", ds, err)
	}
}

func TestTransferShuffleRepeatAndRunningGuards(t *testing.T) {
	// 所有非 Play 的新腳本都必須在進 tell application "Music" 前先檢查 is running
	for name, script := range map[string]string{
		"stateScript":      stateScript,
		"devicesScript":    devicesScript,
		"transferScript":   transferScript,
		"setShuffleScript": setShuffleScript,
		"setRepeatScript":  setRepeatScript,
	} {
		i, j := strings.Index(script, `is running`), strings.Index(script, `tell application "Music"`)
		if i < 0 || j < 0 || i > j {
			t.Errorf("%s 必須在 tell 區塊外部先檢查 is running:%q", name, script)
		}
	}

	p := &Provider{}
	var calls []osaCall
	origOSA, origOpen := runOSA, runOpen
	outVal := "ok"
	runOSA = func(_ context.Context, script string, args ...string) (string, error) {
		calls = append(calls, osaCall{script: script, args: args})
		return outVal, nil
	}
	runOpen = func(u string) error { t.Errorf("unexpected open %s", u); return nil }
	t.Cleanup(func() { runOSA, runOpen = origOSA, origOpen })

	if err := p.Transfer(context.Background(), "Living Room", true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetShuffle(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetRepeat(context.Background(), provider.RepeatContext); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 ||
		calls[0].script != transferScript || !slices.Equal(calls[0].args, []string{"Living Room", "1"}) ||
		calls[1].script != setShuffleScript || !slices.Equal(calls[1].args, []string{"1"}) ||
		calls[2].script != setRepeatScript || !slices.Equal(calls[2].args, []string{"context"}) {
		t.Fatalf("argv 傳遞不符預期:%+v", calls)
	}

	// 無效裝置與無效循環模式
	if err := p.Transfer(context.Background(), " ", false); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("空 deviceID 應回 ErrNotFound:%v", err)
	}
	outVal = "not found"
	if err := p.Transfer(context.Background(), "Ghost", false); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("找不到 AirPlay 裝置應回 ErrNotFound:%v", err)
	}
	calls = nil
	if err := p.SetRepeat(context.Background(), provider.RepeatMode("invalid")); !errors.Is(err, provider.ErrNotSupported) || len(calls) != 0 {
		t.Errorf("無效 RepeatMode 應直接回 ErrNotSupported 且不跑 osascript:%v %+v", err, calls)
	}

	// Music.app 未啟動
	outVal = "not running"
	for name, fn := range map[string]func() error{
		"transfer": func() error { return p.Transfer(context.Background(), "AP1", false) },
		"shuffle":  func() error { return p.SetShuffle(context.Background(), false) },
		"repeat":   func() error { return p.SetRepeat(context.Background(), provider.RepeatOff) },
	} {
		if err := fn(); !errors.Is(err, ErrNotRunning) || !errors.Is(err, provider.ErrPlayerNotRunning) {
			t.Errorf("%s 在未啟動時應回 ErrNotRunning:%v", name, err)
		}
	}
}

func TestFormatOSAErrAndRunOSAStderr(t *testing.T) {
	if got := formatOSAErr(nil); got != nil {
		t.Errorf("nil 應回 nil:%v", got)
	}
	plain := errors.New("plain")
	if got := formatOSAErr(plain); got != plain {
		t.Errorf("非 ExitError 應原樣回傳:%v", got)
	}
	emptyEE := &exec.ExitError{Stderr: []byte("  \n\t ")}
	if got := formatOSAErr(emptyEE); got != error(emptyEE) {
		t.Errorf("空白 Stderr 應原樣回傳:%v", got)
	}
	multiEE := &exec.ExitError{Stderr: []byte("line 1\n\tCan't make song repeat into type text. (-1700)\n")}
	if got := formatOSAErr(multiEE); got == nil || strings.ContainsAny(got.Error(), "\r\n\t") || !strings.Contains(got.Error(), "(-1700)") {
		t.Errorf("多行 Stderr 應壓成單行:%q", got)
	}

	// 真實 osascript(純腳本,不碰 Music.app):驗證 stderr 併入錯誤且保留首欄空 tab
	if _, err := runOSA(context.Background(), `error "denied" number -1743`); err == nil ||
		!strings.Contains(err.Error(), "exit status 1") ||
		!strings.Contains(err.Error(), "-1743") ||
		strings.ContainsAny(err.Error(), "\r\n") {
		t.Errorf("runOSA 應將單行 stderr 併入 ExitError:%v", err)
	}
	if out, err := runOSA(context.Background(), `return tab & "MacBook Pro"`); err != nil || out != "\tMacBook Pro" {
		t.Errorf("runOSA 不得裁掉首欄空 tab:out=%q err=%v", out, err)
	}
}

// TestScriptsForbidEnumCoercionAndGuardMissingValue:防回歸守門——在 tell application "Music" 區塊內
// 嚴禁任何 `as text` 強制轉型(Music.app 對 enum/boolean/specifier 的 getd 請求帶 utxt 會拋 -1700),
// 且 stateScript 必須用 try 保護 current track 與 missing value 算術。
func TestScriptsForbidEnumCoercionAndGuardMissingValue(t *testing.T) {
	for name, script := range map[string]string{
		"stateScript":        stateScript,
		"devicesScript":      devicesScript,
		"transferScript":     transferScript,
		"setShuffleScript":   setShuffleScript,
		"setRepeatScript":    setRepeatScript,
		"libraryMatchScript": libraryMatchScript,
		"playLibraryScript":  playLibraryScript,
	} {
		start := strings.Index(script, `tell application "Music"`)
		end := strings.LastIndex(script, "end tell")
		if start < 0 || end <= start {
			t.Fatalf("%s 找不到 tell 區塊", name)
		}
		tellBody := script[start:end]
		if strings.Contains(tellBody, "as text") {
			t.Errorf("%s 的 tell 區塊內禁止出現 `as text`(會觸發 -1700 轉型失敗):\n%s", name, tellBody)
		}
	}
	for _, needle := range []string{
		"set t to current track",
		"is missing value",
		"set volNum to -1",
		"if curState is playing then",
		"if repVal is one then",
	} {
		if !strings.Contains(stateScript, needle) {
			t.Errorf("stateScript 缺少必要防禦片段 %q", needle)
		}
	}
}
