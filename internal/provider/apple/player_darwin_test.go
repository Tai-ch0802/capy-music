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

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// stubOSA:每個 osascript 都回 out。runOpen 一起換掉而且不准被呼叫——只換 runOSA 的話,走到打開頁面的測試會在
// 開發機與 CI 上真的 open music://、把 Music.app 叫起來。
func stubOSA(t *testing.T, out string) *[]string {
	t.Helper()
	var scripts []string
	origOSA, origOpen := runOSA, runOpen
	runOSA = func(script string, _ ...string) (string, error) { scripts = append(scripts, script); return out, nil }
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
	runOSA = func(script string, args ...string) (string, error) {
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
		got, err := runOSA(echo, v)
		if err != nil || got != v {
			t.Errorf("argv %q 要原樣到腳本:%q %v", v, got, err)
		}
	}
}

// TestPlayScriptsCompile:兩個新腳本要編得過(osacompile 不會啟動 Music.app)。變數叫 st 之類的保留字只有編譯時才抓得到。
func TestPlayScriptsCompile(t *testing.T) {
	if _, err := exec.LookPath("osacompile"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("CI 的 macOS 上要有 osacompile")
		}
		t.Skip("沒有 osacompile")
	}
	for name, script := range map[string]string{"libraryMatchScript": libraryMatchScript, "playLibraryScript": playLibraryScript} {
		out, err := exec.Command("osacompile", "-o", filepath.Join(t.TempDir(), name+".scpt"), "-e", script).CombinedOutput()
		if err != nil {
			t.Errorf("%s 編不過:%v %s", name, err, out)
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
