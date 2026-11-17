package cli

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/ai"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

func newTestTUI(t *testing.T, f *watchFake) tuiModel {
	t.Helper()
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "spotify", "", testTracker(t, "spotify", f), watchPollSpotify)
	m.width = 100
	m.st = f.st     // 正式路徑是第一次 poll 帶進來的;測試直接給,免得每個案例都要先跑一次輪詢
	m.frozen = true // 開場只有前三秒左右;要驗開場的案例自己把它關掉
	return m
}

// testTracker:id 的 controller 已經建好(= pc);其他平台一律建不起來(= 沒登入),config 是空的暫存目錄(預設 spotify)——
// 沒釘住的那一輪會去問別家,不能碰到真的 Music.app、keychain 或使用者的 config。
func testTracker(t *testing.T, id string, pc provider.PlaybackController) *nowTracker {
	t.Helper()
	setCLITestConfig(t)
	orig := newProvider
	newProvider = func(_ context.Context, id string) (provider.Provider, error) {
		return nil, errors.New(id + ":沒登入")
	}
	t.Cleanup(func() { newProvider = orig })
	ctx, cancel := context.WithCancel(context.Background())
	trk := &nowTracker{ctx: ctx, now: map[string]provider.PlaybackController{id: pc}}
	// 最後註冊、最先執行:bubbletea 不等 Cmd,runProgram 回來時 Init 起的那一輪可能還在跑或還沒開始。收掉 ctx(之後的輪都不問)、
	// 等手上那一輪做完,才還原 newProvider 與 config——不然它讀到下一個測試換上的替身(-race 抓到 webNowClock),
	// 或已經還原的真 newProvider。
	t.Cleanup(func() { cancel(); trk.pollMu.Lock(); trk.pollMu.Unlock() })
	return trk
}

// recordPrintln:把推進捲動區的行記下來(tea.Println 產生的是 bubbletea 的私有型別,認不出來)。
func recordPrintln(t *testing.T) *[]string {
	t.Helper()
	var got []string
	orig := tuiPrintln
	tuiPrintln = func(s string) tea.Cmd { got = append(got, s); return nil }
	t.Cleanup(func() { tuiPrintln = orig })
	return &got
}

// joined:捲動區收到的全部內容接成一段,方便找關鍵字。
func joined(got *[]string) string { return strings.Join(*got, "\n") }

// recordExec:記下「執行了哪些參數」而不真的 fork(tuiExecProcess 的測試替換點)。
// 回傳的 Cmd 假裝子命令馬上成功結束。
func recordExec(t *testing.T) *[][]string {
	t.Helper()
	var got [][]string
	orig := tuiExecProcess
	tuiExecProcess = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		got = append(got, c.Args)
		return func() tea.Msg { return fn(nil) }
	}
	t.Cleanup(func() { tuiExecProcess = orig })
	return &got
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
	m.frozen = false // 開場期間才有動畫
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
	// 定格之後動畫停掉:底部四行只在狀態變動與按鍵時重畫,不再每一幀重繪。
	// (這是不常駐的那條路;終端機夠大時水豚常駐、ticker 一直走,見 tui_alive_test.go。)
	m.frozen = true
	if _, cmd := m.Update(tuiFrameMsg{}); cmd != nil {
		t.Error("定格後不該再排下一幀")
	}
}

func TestTUISeekAndVolumeKeys(t *testing.T) {
	f := &watchFake{st: playingState()} // 進度 83000、時長 249000、音量 50
	m := newTestTUI(t, f)
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyRight}, true)
	if m.st.ProgressMS != 93000 {
		t.Fatalf("樂觀預覽應立即推進至 93000,got %d", m.st.ProgressMS)
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyLeft}, true)
	m = step(t, m, tea.KeyPressMsg{Code: '+'}, true)
	if m.st.Device.VolumePct != 55 {
		t.Fatalf("樂觀預覽音量應為 55,got %d", m.st.Device.VolumePct)
	}
	m = step(t, m, tea.KeyPressMsg{Code: '-'}, true)
	want := []string{"seek:93000", "seek:83000", "vol:55", "vol:50"}
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
	if got := m.View().Content; !strings.Contains(got, "音量 0") {
		t.Errorf("靜音要顯示成「音量 0」而不是整行消失:%q", got)
	}
	m = step(t, m, tea.KeyPressMsg{Code: '+'}, true)
	if !slices.Equal(muted.calls, []string{"vol:5"}) {
		t.Fatalf("靜音之後 + 要解得開:%v", muted.calls)
	}
	if got := m.View().Content; !strings.Contains(got, "音量 5") {
		t.Errorf("+ 後樂觀預覽應顯示「音量 5」:%q", got)
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
	got := recordPrintln(t)
	if _, cmd := m.Update(tuiExecMsg{args: []string{"pl", "list"}}); cmd != nil {
		t.Error("執行完子命令不該再排 tick(排隊中的訊息會自己把鏈接回去)")
	}
	if len(*got) != 0 {
		t.Errorf("成功不印:輸出本身就是證據,多一行是雜訊:%q", *got)
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
	if !strings.Contains(view, "r 重試") || !strings.Contains(view, "/ 命令") {
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
	// Esc:有字先清空(打錯一長串不必連按退格),空的再離開輸入模式;兩種情況都不執行
	gotArgs = nil
	m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
	m.input.SetValue("search x")
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, true)
	if !m.typing || m.input.Value() != "" || gotArgs != nil {
		t.Errorf("Esc 要清空而不是執行:typing=%v value=%q args=%v", m.typing, m.input.Value(), gotArgs)
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, true)
	if m.typing || gotArgs != nil {
		t.Errorf("空輸入按 Esc 要離開輸入模式:typing=%v args=%v", m.typing, gotArgs)
	}
	// 空輸入按 Enter:什麼都不做
	m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
	m.input.SetValue("   ")
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, true)
	if gotArgs != nil {
		t.Errorf("空輸入不該執行:%v", gotArgs)
	}
}

// 舊世代的回覆不管走哪一條路徑都不能排 tick——pollTick 抓的是目前的 gen,排了就等於把舊鏈
// 復活成一條合法的新鏈。429 那條尤其糟:鏈愈多愈容易 429,愈常走那條路徑(PR #42 review)。
func TestTUIStaleRepliesNeverReviveTheChain(t *testing.T) {
	f := &watchFake{st: playingState()}
	m := newTestTUI(t, f)
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyRight}, false) // 推進世代
	old := m.gen - 1
	for _, msg := range []tuiStateMsg{
		{err: provider.ErrPlayerNotRunning, gen: old},
		{err: &provider.RateLimitError{Message: "429"}, gen: old},
		{st: f.st, gen: old},
		{err: errors.New("boom"), gen: old},
	} {
		if _, cmd := m.Update(msg); cmd != nil {
			t.Errorf("舊世代的回覆不該排 tick:%+v", msg)
		}
	}
	// 遲到的「播放器未執行」也不該抹掉剛拿回來的狀態
	next, _ := m.Update(tuiStateMsg{err: provider.ErrPlayerNotRunning, gen: old})
	if next.(tuiModel).st == nil {
		t.Error("過期的錯誤不該清掉目前的播放狀態")
	}
	// 停擺之後,連目前世代的訊息也不該把輪詢默默接回去(畫面還在叫使用者按 r)
	m2 := newTestTUI(t, f)
	m2.stalled = true
	for _, msg := range []tuiStateMsg{
		{err: provider.ErrPlayerNotRunning, gen: m2.gen},
		{st: f.st, gen: m2.gen},
	} {
		if _, cmd := m2.Update(msg); cmd != nil {
			t.Errorf("停擺中不該排 tick:%+v", msg)
		}
	}
	// 控制指令自己的錯誤仍然要顯示(使用者剛按的鍵失敗了)
	next, _ = m.Update(tuiStateMsg{err: errors.New("裝置拒絕"), fromCtl: true, gen: old})
	if next.(tuiModel).errShort == "" {
		t.Error("控制指令的錯誤要說,即使已經 stale")
	}
}

// capy --provider apple 開的介面是 Apple,命令列跑的卻是 config 的預設平台——同一個畫面兩個平台。
func TestTUIPassesProviderFlagToSubcommands(t *testing.T) {
	var got []string
	orig := tuiExecProcess
	tuiExecProcess = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		got = c.Args
		return func() tea.Msg { return tuiExecMsg{} }
	}
	t.Cleanup(func() { tuiExecProcess = orig })

	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "apple", "apple", testTracker(t, "apple", &watchFake{}), watchPollSpotify)
	m.width = 100
	run := func(line string) {
		m.typing = true
		m.input.SetValue(line)
		next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = next.(tuiModel)
		if cmd != nil {
			cmd()
		}
	}
	run("pl list")
	if !slices.Equal(got, []string{"/bin/capy", "pl", "list", "--provider", "apple"}) {
		t.Errorf("吃 --provider 的子命令要帶過去:%#v", got)
	}
	run("auth status") // 沒掛 --provider:多送會 unknown flag 直接退出
	if !slices.Equal(got, []string{"/bin/capy", "auth", "status"}) {
		t.Errorf("不吃 --provider 的子命令不能亂加:%#v", got)
	}
	// 使用者沒明指時完全不附加
	m2 := newTestTUI(t, &watchFake{})
	m2.typing = true
	m2.input.SetValue("pl list")
	if _, cmd := m2.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		cmd()
	}
	if !slices.Equal(got, []string{"/bin/capy", "pl", "list"}) {
		t.Errorf("沒有 --provider 時不該附加:%#v", got)
	}
}

// raw mode 下 ctrl+c 是一個按鍵不是 SIGINT:輸入模式沒攔的話,打到一半想離開會完全沒反應。
func TestTUICtrlCQuitsWhileTyping(t *testing.T) {
	m := newTestTUI(t, &watchFake{st: playingState()})
	m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
	m.input.SetValue("search x")
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("輸入模式下 ctrl+c 要能離開")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("ctrl+c 應該是 Quit(不是 tea.Interrupt:那條路不畫最後一幀,見 runProgram)")
	}
	if !next.(tuiModel).interruptedByKey() {
		t.Error("ctrl+c 離開要記一筆:結束碼 130(見 runProgram)")
	}
}

func TestTUIViewNarrowFallsBackToOneLine(t *testing.T) {
	m := newTestTUI(t, &watchFake{st: playingState()})
	m.frozen = false // 水豚只出現在開場
	wide := m.View().Content
	if !strings.Contains(wide, capyFeet) { // 腳那行每一幀都一樣,不受眨眼／嚼草影響
		t.Error("寬螢幕要有水豚橫幅")
	}
	m.width = 30
	narrow := m.View().Content
	if strings.Contains(narrow, capyFeet) {
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

// 窄畫面看到的是靜止的一行版,沒有劇本可演:不該陪著等完整個開場(劇本拉長到三秒之後,這等於白等;review #72)。
// 第一個 frame tick 就定格——那時候 WindowSizeMsg 已經到了。
func TestTUINarrowSkipsTheIntro(t *testing.T) {
	m := newTestTUI(t, &watchFake{st: playingState()})
	m.frozen = false
	m.width = 30
	got := recordPrintln(t)
	next, cmd := m.Update(tuiFrameMsg{})
	m = next.(tuiModel)
	if !m.frozen || len(*got) != 1 || !strings.Contains((*got)[0], capyOneLine) {
		t.Fatalf("窄畫面第一個 tick 就要定格、把一行版印進捲動區:frozen=%v %v", m.frozen, *got)
	}
	if cmd != nil { // recordPrintln 的替身回 nil:這裡不是 nil 就代表又排了下一幀
		t.Error("定格的那一個 tick 不該再排下一幀")
	}
}

// 沒有播放遙控(沒登入、平台不支援)不該讓介面開不起來:狀態區說明原因,命令列照樣可用。
func TestTUIWithoutPlaybackStillUsable(t *testing.T) {
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "local", "", testTracker(t, "spotify", &watchFake{}), watchPollSpotify)
	m.width = 100
	m.pcErr = provider.ErrNotSupported
	m.frozen = true
	if got := m.View().Content; !strings.Contains(got, "沒有播放遙控") || !strings.Contains(got, "/ 命令") {
		t.Errorf("要說明原因並保留命令列:%q", got)
	}
	m.frozen = false
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

func TestTUIShuffleRepeatAndOptimisticRollback(t *testing.T) {
	fc := &deckFakeController{watchFake: watchFake{st: playingState()}}
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "spotify", "", testTracker(t, "spotify", fc), watchPollSpotify)
	m.width = 100
	m.st = fc.st
	m.frozen = true
	recordPrintln(t)

	// s 切換隨機播放:樂觀預覽立即亮起 [S],失敗時回滾至 false。
	m = step(t, m, tea.KeyPressMsg{Code: 's'}, true)
	if !slices.Equal(fc.shuffles, []bool{true}) || !m.st.Shuffle || !strings.Contains(ansi.Strip(m.View().Content), "[S]") {
		t.Fatalf("s 應送出 SetShuffle(true) 並顯示 [S]:shuffles=%v view=%q", fc.shuffles, ansi.Strip(m.View().Content))
	}
	m = step(t, m, tuiStateMsg{err: errors.New("裝置拒絕"), fromCtl: true, gen: m.gen}, false)
	if m.st.Shuffle || strings.Contains(ansi.Strip(m.View().Content), "[S]") {
		t.Fatalf("控制失敗後應回滾 Shuffle 樂觀狀態:shuffle=%v view=%q", m.st.Shuffle, ansi.Strip(m.View().Content))
	}
	m.errShort = "" // 清除錯誤後 r 進入循環模式切換

	// r 循環切換 RepeatOff -> RepeatContext ([R]) -> RepeatTrack ([R1]) -> RepeatOff
	m = step(t, m, tea.KeyPressMsg{Code: 'r'}, true)
	if !strings.Contains(ansi.Strip(m.View().Content), "[R]") {
		t.Fatalf("第一次 r 應顯示 [R]:%q", ansi.Strip(m.View().Content))
	}
	m = step(t, m, tea.KeyPressMsg{Code: 'r'}, true)
	if !strings.Contains(ansi.Strip(m.View().Content), "[R1]") {
		t.Fatalf("第二次 r 應顯示 [R1]:%q", ansi.Strip(m.View().Content))
	}
	m = step(t, m, tea.KeyPressMsg{Code: 'r'}, true)
	if strings.Contains(ansi.Strip(m.View().Content), "[R") {
		t.Fatalf("第三次 r 應關閉循環標記:%q", ansi.Strip(m.View().Content))
	}
	wantRepeats := []provider.RepeatMode{provider.RepeatContext, provider.RepeatTrack, provider.RepeatOff}
	if !slices.Equal(fc.repeats, wantRepeats) {
		t.Fatalf("SetRepeat 順序 = %v,要 %v", fc.repeats, wantRepeats)
	}

	// 進度與音量的樂觀預覽與回滾
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyRight}, false)
	if m.st.ProgressMS != 93000 {
		t.Fatalf("Right 樂觀進度 = %d,要 93000", m.st.ProgressMS)
	}
	m = step(t, m, tuiStateMsg{err: errors.New("seek failed"), fromCtl: true, gen: m.gen}, false)
	if m.st.ProgressMS != 83000 {
		t.Fatalf("Seek 失敗應回滾至 83000,got %d", m.st.ProgressMS)
	}

	// R (Shift+R) 無論狀態如何都強制重連輪詢
	beforeGen := m.gen
	m = step(t, m, tea.KeyPressMsg{Code: 'R'}, false)
	if m.gen == beforeGen || m.errShort != "" {
		t.Fatalf("R 應推進世代並清除錯誤:gen %d->%d errShort=%q", beforeGen, m.gen, m.errShort)
	}
}

func TestTUIInProcessFastPathAndCachedWiki(t *testing.T) {
	fc := &deckFakeController{watchFake: watchFake{st: playingState()}}
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "spotify", "", testTracker(t, "spotify", fc), watchPollSpotify)
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 40}, false) // 常駐水豚 (15 行)
	m.st = fc.st
	ran := recordExec(t)
	got := recordPrintln(t)

	// 1. 純播放控制命令走行程內 Fast Path:零 fork、常駐水豚保持 15 行不閃退縮高!
	for _, cmdLine := range []string{"pause", "play", "next", "prev", "seek 1:30", "vol 65"} {
		m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
		m.input.SetValue(cmdLine)
		m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, true)
		if m.running {
			t.Fatalf("%q 應走行程內 Fast Path,不該進入 running 縮高狀態", cmdLine)
		}
		if n := len(strings.Split(m.View().Content, "\n")); n != capyBlockRows+4 {
			t.Fatalf("%q 後常駐水豚高度應維持 %d 行,got %d", cmdLine, capyBlockRows+4, n)
		}
	}
	if len(*ran) != 0 {
		t.Fatalf("行程內命令不應 fork 子行程:%v", *ran)
	}
	wantCalls := []string{"pause", "play", "next", "prev", "seek:90000", "vol:65"}
	if !slices.Equal(fc.calls, wantCalls) {
		t.Fatalf("行程內控制呼叫 = %v,要 %v", fc.calls, wantCalls)
	}

	// 2. 快取未命中時按 w 仍走子行程執行 capy wiki(自動跟隨當前顯示平台)
	m = step(t, m, tea.KeyPressMsg{Code: 'w'}, true)
	if len(*ran) != 1 || !slices.Equal((*ran)[0], []string{"/bin/capy", "wiki", "--provider", "spotify"}) {
		t.Fatalf("快取未命中時 w 應執行 capy wiki:%v", *ran)
	}
	m = step(t, m, tuiExecMsg{args: []string{"wiki"}}, false)

	// 3. 寫入本機 wiki_cache 後,曲目狀態推進亮起 [wiki];按 w 行程內零延遲印出正文、MV、來源與免責行!
	song := ai.Song{Title: "派對動物", Artists: []string{"五月天"}}
	stDB, err := store.Open(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := stDB.SaveWiki(ai.WikiCacheKey("zh-TW", song), store.WikiEntry{
		Title:     song.Title,
		Artists:   "五月天",
		Language:  "zh-TW",
		Model:     "capy-llm",
		Body:      "## 創作背景\n水豚導聆內頁測試正文。",
		FetchedAt: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		stDB.Close()
		t.Fatal(err)
	}
	if err := stDB.Close(); err != nil {
		t.Fatal(err)
	}

	m.wikiKey = ""
	next, _ := m.Update(tuiStateMsg{st: playingState(), gen: m.gen})
	m = next.(tuiModel)
	wikiMsg := lookupWikiAsyncCmd(m.wikiKey, song)()
	m = step(t, m, wikiMsg, false)
	if !strings.Contains(ansi.Strip(m.View().Content), "[wiki]") {
		t.Fatalf("命中本機 wiki 快取時狀態列應亮起 [wiki]:%q", ansi.Strip(m.View().Content))
	}

	*ran = nil
	*got = nil
	m = step(t, m, tea.KeyPressMsg{Code: 'w'}, false)
	if len(*ran) != 0 {
		t.Fatalf("快取命中時按 w 絕不應 fork 子行程:%v", *ran)
	}
	if n := len(strings.Split(m.View().Content, "\n")); n != capyBlockRows+4 {
		t.Fatalf("按 w 共讀內頁時常駐水豚不得閃退:%d 行", n)
	}
	printed := ansi.Strip(joined(got))
	for _, wantSub := range []string{
		"水豚導聆內頁測試正文",
		"https://www.youtube.com/results?search_query=",
		"capy-llm",
		i18n.T("wiki.disclaimer"),
	} {
		if !strings.Contains(printed, wantSub) {
			t.Errorf("行程內內頁導聆缺了 %q:\n%s", wantSub, printed)
		}
	}

	// 4. 按 W (Shift+W) 強制走子行程執行 wiki --refresh
	m = step(t, m, tea.KeyPressMsg{Code: 'W'}, true)
	if len(*ran) != 1 || !slices.Equal((*ran)[0], []string{"/bin/capy", "wiki", "--refresh", "--provider", "spotify"}) {
		t.Fatalf("W 應執行 capy wiki --refresh:%v", *ran)
	}
}

func TestTUIHistoryPersistsAcrossSessionsAndFiltersSecrets(t *testing.T) {
	orig := tuiExecProcess
	tuiExecProcess = func(*exec.Cmd, tea.ExecCallback) tea.Cmd { return nil }
	t.Cleanup(func() { tuiExecProcess = orig })
	recordPrintln(t)

	// Session 1:設定同一個 CAPY_CONFIG_DIR
	m1 := newTestTUI(t, &watchFake{st: playingState()})
	cfgDir := t.TempDir()
	t.Setenv("CAPY_CONFIG_DIR", cfgDir)

	runLine := func(m tuiModel, line string) tuiModel {
		m = step(t, m, tea.KeyPressMsg{Code: ':'}, false)
		m.input.SetValue(line)
		return step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, false)
	}
	m1 = runLine(m1, "pl list")
	m1 = runLine(m1, "wiki setup --api-key sk-super-secret")
	m1 = runLine(m1, "wiki setup --header Authorization:Bearer-xyz")
	m1 = runLine(m1, "auth login --client-secret top-secret")
	m1 = runLine(m1, "auth login --developer-token dev-tok --user-token usr-tok")
	m1 = runLine(m1, "wiki --headers-file /tmp/h.txt")
	m1 = runLine(m1, "search 派對動物")

	// Session 2:共用同一個 CAPY_CONFIG_DIR 建立新 model(不呼叫 testTracker 以免重設 CAPY_CONFIG_DIR)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	trk2 := &nowTracker{ctx: ctx, now: map[string]provider.PlaybackController{"spotify": &watchFake{st: playingState()}}}
	m2 := newTUIModel(ctx, ui.DefaultTheme, "/bin/capy", "spotify", "", trk2, watchPollSpotify)

	wantPersisted := []string{"pl list", "search 派對動物"}
	if !slices.Equal(m2.hist, wantPersisted) {
		t.Fatalf("跨 session 載入的歷史 = %#v,要 %#v (含機密旗標的命令絕不可寫入 SQLite!)", m2.hist, wantPersisted)
	}
}

func TestTUISleepTimerCycleAndInProcessCommand(t *testing.T) {
	fc := &deckFakeController{watchFake: watchFake{st: playingState()}}
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "spotify", "", testTracker(t, "spotify", fc), watchPollSpotify)
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 40}, false) // alive 模式顯示招牌列
	m.st = fc.st
	got := recordPrintln(t)

	// 按 t 六段循環:Off -> EndOfTrack -> 15m -> 30m -> 60m -> Yuzu -> Off
	m = step(t, m, tea.KeyPressMsg{Code: 't'}, false)
	if m.sleep.Snapshot().Mode != SleepEndOfTrack || !strings.Contains(ansi.Strip(m.View().Content), "(o) track") {
		t.Fatalf("第 1 次 t 應為 EndOfTrack 並在招牌顯示 (o) track:%+v\n%s", m.sleep.Snapshot(), ansi.Strip(m.View().Content))
	}
	m = step(t, m, tea.KeyPressMsg{Code: 't'}, false)
	if snap := m.sleep.Snapshot(); snap.Mode != SleepCountdown || snap.TotalSec != 15*60 {
		t.Fatalf("第 2 次 t 應為 15m:%+v", snap)
	}
	m = step(t, m, tea.KeyPressMsg{Code: 't'}, false)
	if snap := m.sleep.Snapshot(); snap.Mode != SleepCountdown || snap.TotalSec != 30*60 {
		t.Fatalf("第 3 次 t 應為 30m:%+v", snap)
	}
	m = step(t, m, tea.KeyPressMsg{Code: 't'}, false)
	if snap := m.sleep.Snapshot(); snap.Mode != SleepCountdown || snap.TotalSec != 60*60 {
		t.Fatalf("第 4 次 t 應為 60m:%+v", snap)
	}
	m = step(t, m, tea.KeyPressMsg{Code: 't'}, false)
	if snap := m.sleep.Snapshot(); snap.Mode != SleepYuzuPomodoro || !strings.Contains(ansi.Strip(m.View().Content), "(o) 2") {
		t.Fatalf("第 5 次 t 應為柚子湯專注計時:%+v\n%s", snap, ansi.Strip(m.View().Content))
	}
	m = step(t, m, tea.KeyPressMsg{Code: 't'}, false)
	if snap := m.sleep.Snapshot(); snap.Mode != SleepOff || strings.Contains(ansi.Strip(m.View().Content), "(o)") {
		t.Fatalf("第 6 次 t 應回到 Off:%+v", snap)
	}

	// 行程內 /sleep 命令
	runSleep := func(line string) {
		m = step(t, m, tea.KeyPressMsg{Code: ':'}, false)
		m.input.SetValue(line)
		m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, false)
	}
	runSleep("sleep 20")
	if snap := m.sleep.Snapshot(); snap.Mode != SleepCountdown || snap.TotalSec != 20*60 {
		t.Fatalf("/sleep 20 應設 20 分鐘倒數:%+v", snap)
	}
	runSleep("sleep yuzu")
	if snap := m.sleep.Snapshot(); snap.Mode != SleepYuzuPomodoro {
		t.Fatalf("/sleep yuzu 應設柚子湯計時:%+v", snap)
	}
	runSleep("sleep off")
	if snap := m.sleep.Snapshot(); snap.Mode != SleepOff {
		t.Fatalf("/sleep off 應關閉定時器:%+v", snap)
	}
	if !strings.Contains(ansi.Strip(joined(got)), "20") {
		t.Errorf("捲動區應印出定時設定回饋:%q", ansi.Strip(joined(got)))
	}
}
