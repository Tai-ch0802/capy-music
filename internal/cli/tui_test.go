package cli

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

func newTestTUI(t *testing.T, f *watchFake) tuiModel {
	t.Helper()
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "spotify", f, nil, watchPollSpotify)
	m.width = 100
	m.st = f.st // 正式路徑是第一次 poll 帶進來的;測試直接給,免得每個案例都要先跑一次輪詢
	return m
}

// 送一個訊息、拿回 model 與 cmd;cmd 若非 nil 就執行(這些 cmd 都是同步的 func,不是 tea.Tick——
// tea.Tick 的 timer 建立即啟動,在測試裡跑第二次會永遠卡住)。
func step(t *testing.T, m tuiModel, msg tea.Msg, runCmd bool) tuiModel {
	t.Helper()
	next, cmd := m.Update(msg)
	out := next.(tuiModel)
	if runCmd && cmd != nil {
		cmd()
	}
	return out
}

func TestSplitArgs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"search 派對動物", []string{"search", "派對動物"}},
		{"  pl   list  ", []string{"pl", "list"}},
		{"", nil},
		{"   ", nil},
		{`pl show "上班 通勤"`, []string{"pl", "show", "上班 通勤"}},
		{`pl link "我的 清單" spotify:p1`, []string{"pl", "link", "我的 清單", "spotify:p1"}},
		{`pl show ""`, []string{"pl", "show", ""}}, // 明確的空字串參數保留
		{`a"b"c`, []string{"abc"}},                 // 引號只是分組,不是字元
	} {
		if got := splitArgs(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("splitArgs(%q) = %#v,要 %#v", tc.in, got, tc.want)
		}
	}
}

func TestTUIFrameAdvancesAndBodyStaysPut(t *testing.T) {
	m := newTestTUI(t, &watchFake{st: playingState()})
	if m.frame != 0 {
		t.Fatal("起始幀應為 0")
	}
	for i := 1; i <= 3; i++ {
		next, cmd := m.Update(tuiFrameMsg{})
		m = next.(tuiModel)
		if cmd == nil {
			t.Fatal("每一幀都要排下一次 tick") // 不執行它:tea.Tick 在測試裡只能建立不能跑
		}
		if m.frame != i {
			t.Fatalf("幀 = %d,要 %d", m.frame, i)
		}
	}
}

func TestTUISeekAndVolumeKeys(t *testing.T) {
	f := &watchFake{st: playingState()} // 進度 83000、時長 249000、音量 50
	m := newTestTUI(t, f)
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyRight}, true)
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyLeft}, true)
	m = step(t, m, tea.KeyPressMsg{Code: '+'}, true)
	m = step(t, m, tea.KeyPressMsg{Code: '-'}, true)
	want := []string{"seek:93000", "seek:73000", "vol:55", "vol:45"}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("鍵位送出的值:%v,要 %v", f.calls, want)
	}
	// 夾範圍:接近結尾往前跳不會超過長度,接近 0 往回跳不會變負
	f2 := &watchFake{st: playingState()}
	f2.st.ProgressMS = 245000
	m2 := newTestTUI(t, f2)
	m2 = step(t, m2, tea.KeyPressMsg{Code: tea.KeyRight}, true)
	f2.st.ProgressMS = 3000
	m2.st = f2.st
	m2 = step(t, m2, tea.KeyPressMsg{Code: tea.KeyLeft}, true)
	if !slices.Equal(f2.calls, []string{"seek:249000", "seek:0"}) {
		t.Fatalf("要夾在 [0, 長度]:%v", f2.calls)
	}
	// 音量夾在 [0,100]
	f3 := &watchFake{st: playingState()}
	f3.st.Device.VolumePct = 98
	m3 := newTestTUI(t, f3)
	m3 = step(t, m3, tea.KeyPressMsg{Code: '+'}, true)
	if !slices.Equal(f3.calls, []string{"vol:100"}) {
		t.Fatalf("音量要夾在 100:%v", f3.calls)
	}
}

// 音量 0 是「靜音」不是「不知道」:+ 要能把它拉回來。平台沒回報音量(VolumeKnown = false)才不猜。
func TestTUIVolumeDistinguishesMuteFromUnknown(t *testing.T) {
	muted := &watchFake{st: playingState()}
	muted.st.Device.VolumePct = 0 // 真的靜音,平台有回報
	m := newTestTUI(t, muted)
	m = step(t, m, tea.KeyPressMsg{Code: '+'}, true)
	if !slices.Equal(muted.calls, []string{"vol:5"}) {
		t.Fatalf("靜音之後 + 要解得開:%v", muted.calls)
	}
	if got := m.View().Content; !strings.Contains(got, "音量 0") {
		t.Errorf("靜音要顯示成「音量 0」而不是整行消失:%q", got)
	}
	unknown := &watchFake{st: playingState()}
	unknown.st.Device.VolumeKnown = false // Apple 的 State 不帶音量
	m2 := newTestTUI(t, unknown)
	m2 = step(t, m2, tea.KeyPressMsg{Code: '+'}, true)
	if len(unknown.calls) != 0 {
		t.Fatalf("平台沒回報音量時不猜:%v", unknown.calls)
	}
	if strings.Contains(m2.View().Content, "· 音量") { // 提示列也有「音量」兩字,只看裝置那行的寫法
		t.Error("沒回報音量就不要顯示音量")
	}
}

// 輪詢是一條鏈,控制鍵會另外起一次讀取。沒有世代編號的話舊鏈不會停,按幾次就多幾條,方向鍵
// 自動重複時直接撞 429。這條守住「同一時間只有一條鏈在跑」。
func TestTUIPollChainDoesNotMultiply(t *testing.T) {
	f := &watchFake{st: playingState()}
	m := newTestTUI(t, f)
	// 第一條鏈:gen 0 的 tick 到期 → 會排下一次讀取
	if _, cmd := m.Update(tuiPollMsg{gen: m.gen}); cmd == nil {
		t.Fatal("目前世代的 tick 應該接著讀狀態")
	}
	before := m.gen
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyRight}, true) // 控制鍵推進世代
	if m.gen == before {
		t.Fatal("控制鍵要推進輪詢世代")
	}
	// 舊鏈的 tick 現在到期:必須停在這裡,不能再排一次讀取
	if _, cmd := m.Update(tuiPollMsg{gen: before}); cmd != nil {
		t.Error("舊世代的 tick 應該自己停掉,否則鏈會愈積愈多")
	}
	// 舊鏈的狀態回覆同理:不能拿它排新的 tick
	if _, cmd := m.Update(tuiStateMsg{st: f.st, gen: before}); cmd != nil {
		t.Error("舊世代的狀態回覆應該丟掉")
	}
	// 新世代照常運作
	if _, cmd := m.Update(tuiPollMsg{gen: m.gen}); cmd == nil {
		t.Error("目前世代要照常輪詢")
	}
}

// tea.Exec 擋住的是 event loop,不是 tea.Tick 的 timer——回來時再排一次 tick 會讓鏈變兩條。
func TestTUIExecDoesNotReArmTicks(t *testing.T) {
	m := newTestTUI(t, &watchFake{st: playingState()})
	next, cmd := m.Update(tuiExecMsg{args: []string{"pl", "list"}})
	if cmd != nil {
		t.Error("執行完子命令不該再排 tick(排隊中的訊息會自己把鏈接回去)")
	}
	if !strings.Contains(next.(tuiModel).note, "pl list") {
		t.Error("要留下執行過什麼的紀錄")
	}
}

// 讀不到播放狀態不該關掉整個介面:命令列正是這時候最需要的(auth login、doctor)。
func TestTUIStallsInsteadOfQuitting(t *testing.T) {
	f := &watchFake{err: errors.New("dial tcp: no route to host")}
	m := newTestTUI(t, f)
	var cmd tea.Cmd
	for i := 0; i < tuiMaxFails; i++ {
		var next tea.Model
		next, cmd = m.Update(tuiStateMsg{err: f.err, gen: m.gen})
		m = next.(tuiModel)
	}
	if cmd != nil {
		t.Error("達到上限後應該停下輪詢,而不是排下一次(更不是 tea.Quit)")
	}
	if !m.stalled {
		t.Fatal("應該進入停擺狀態")
	}
	view := m.View().Content
	if !strings.Contains(view, "按 r 重試") || !strings.Contains(view, "/ 輸入命令") {
		t.Errorf("要指路且保留命令列:%q", view)
	}
	// r 重新接上
	m = step(t, m, tea.KeyPressMsg{Code: 'r'}, false)
	if m.stalled || m.fails != 0 {
		t.Errorf("r 之後要重新開始:stalled=%v fails=%d", m.stalled, m.fails)
	}
}

// 沒有播放內容時,seek / vol 沒有基準點可以加減——不做事,而不是跳到 10 秒或把音量設成 5。
func TestTUISeekAndVolumeNoopWithoutState(t *testing.T) {
	f := &watchFake{}
	m := newTestTUI(t, f)
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyRight}, {Code: tea.KeyLeft}, {Code: '+'}, {Code: '-'}} {
		m = step(t, m, k, true)
	}
	if len(f.calls) != 0 {
		t.Fatalf("沒有狀態時不該送任何指令:%v", f.calls)
	}
}

func TestTUICommandLine(t *testing.T) {
	var gotArgs []string
	orig := tuiExecProcess
	tuiExecProcess = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		gotArgs = c.Args
		return func() tea.Msg { return tuiExecMsg{args: c.Args[1:]} }
	}
	t.Cleanup(func() { tuiExecProcess = orig })

	f := &watchFake{st: playingState()}
	m := newTestTUI(t, f)
	m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
	if !m.typing {
		t.Fatal("/ 要進入輸入模式")
	}
	// 輸入模式下,n 是打字不是「下一首」
	m = step(t, m, tea.KeyPressMsg{Code: 'n'}, false)
	if len(f.calls) != 0 {
		t.Fatalf("輸入模式下不該觸發播放控制:%v", f.calls)
	}
	m.input.SetValue(`pl show "上班 通勤"`)
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, true)
	if m.typing {
		t.Error("Enter 之後要離開輸入模式")
	}
	if m.input.Value() != "" {
		t.Errorf("Enter 之後要清空輸入:%q", m.input.Value())
	}
	if !slices.Equal(gotArgs, []string{"/bin/capy", "pl", "show", "上班 通勤"}) {
		t.Errorf("執行的參數:%#v", gotArgs)
	}
	// Esc 取消:不執行、離開輸入模式
	gotArgs = nil
	m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
	m.input.SetValue("search x")
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, true)
	if m.typing || gotArgs != nil {
		t.Errorf("Esc 要取消而不是執行:typing=%v args=%v", m.typing, gotArgs)
	}
	// 空輸入按 Enter:什麼都不做
	m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
	m.input.SetValue("   ")
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, true)
	if gotArgs != nil {
		t.Errorf("空輸入不該執行:%v", gotArgs)
	}
}

func TestTUIViewNarrowFallsBackToOneLine(t *testing.T) {
	m := newTestTUI(t, &watchFake{st: playingState()})
	wide := m.View().Content
	if !strings.Contains(wide, capyChin) { // 下巴那行每一幀都一樣,不受眨眼／嚼草影響
		t.Error("寬螢幕要有水豚橫幅")
	}
	m.width = 30
	narrow := m.View().Content
	if strings.Contains(narrow, capyChin) {
		t.Error("窄螢幕不該畫整隻水豚")
	}
	if !strings.Contains(narrow, capyOneLine) {
		t.Error("窄螢幕要用一行的替代品")
	}
	if !strings.Contains(narrow, "capy") {
		t.Errorf("窄螢幕仍要有一行招牌:%q", narrow)
	}
	for _, l := range strings.Split(narrow, "\n") {
		if w := ansi.StringWidth(l); w > 30 { // 量顯示寬度:顏色碼不算,中文算 2
			t.Errorf("窄螢幕的行不該超過寬度(%d):%q", w, l)
		}
	}
}

// 沒有播放遙控(沒登入、平台不支援)不該讓介面開不起來:狀態區說明原因,命令列照樣可用。
func TestTUIWithoutPlaybackStillUsable(t *testing.T) {
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "local", nil, provider.ErrNotSupported, watchPollSpotify)
	m.width = 100
	if got := m.View().Content; !strings.Contains(got, "沒有播放遙控") || !strings.Contains(got, "/ 輸入命令") {
		t.Errorf("要說明原因並保留命令列:%q", got)
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyRight}, true) // 不該 panic
	if m.Init() == nil {
		t.Error("即使沒有播放遙控,水豚還是要動")
	}
}

func TestRootNonTTYPrintsHelpAndTTYOpensTUI(t *testing.T) {
	origInteractive, origRun := isInteractive, runTUI
	t.Cleanup(func() { isInteractive, runTUI = origInteractive, origRun })

	isInteractive = func(*cobra.Command) bool { return false }
	called := false
	runTUI = func(*cobra.Command) error { called = true; return nil }
	out, err := runCLI(t)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("非 TTY 不能開互動式介面")
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("非 TTY 應印 help:%q", out)
	}

	isInteractive = func(*cobra.Command) bool { return true }
	if _, err := runCLI(t); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("TTY 應開互動式介面")
	}
}
