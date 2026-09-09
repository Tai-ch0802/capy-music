package cli

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// wantWidth:測試自己的寬度尺。刻意重寫一份、不呼叫 tuiWidth —— 拿被測的函式當尺,尺歪了
// 測試也跟著歪,永遠綠燈(水豚那次就是用 ansi.StringWidth 當尺,量不到 ambiguous 字元)。
func wantWidth(s string) int {
	n := 0
	for _, r := range ansi.Strip(s) {
		switch {
		case r < 32 || r == 127: // 控制字元不佔欄(bubbles 用 NUL 當寬字元的續格)
		case r < 128:
			n++
		default:
			n += 2
		}
	}
	return n
}

// 這次重做的核心指標:TUI 管的區域只有四行。前一版是 19 行,視窗放不下時上緣被截斷,
// 舊畫面留在上面 —— 使用者看到的「水豚頭重複三次、歷程不可讀」就是這麼來的。
func TestTUIViewIsFourLines(t *testing.T) {
	long := strings.Repeat("超長的曲名", 40)
	// 24 / 30 是窄視窗的門檻:輸入行的下限寬度與 placeholder 都會在這裡把底部撐成五行。
	for _, w := range []int{24, 30, 40, 80} {
		for _, name := range []string{"playing", "nostate", "stalled", "typing", "noplayback"} {
			f := &watchFake{st: playingState()}
			f.st.Track.Title = long
			m := newTestTUI(t, f)
			m = step(t, m, tea.WindowSizeMsg{Width: w, Height: 24}, false) // 走真的路徑:它同時設 textinput 的寬度
			switch name {
			case "nostate":
				m.st = nil
			case "stalled":
				m.st, m.stalled, m.errShort = nil, true, "讀不到播放狀態(r 重試)"
			case "typing":
				m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
				m.input.SetValue(long)
			case "noplayback":
				m.pc, m.st, m.pcErr = nil, nil, provider.ErrNotSupported
			}
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != 4 {
				t.Errorf("w=%d %s:%d 行,要 4 行\n%q", w, name, len(lines), m.View().Content)
			}
			for i, l := range lines {
				// 保守寬度:ambiguous 字元(› · ▶ ⚠ ← →)在 CJK 終端機是兩欄,
				// ansi.StringWidth 量不出來 —— 這才是這裡真正要守的東西。
				if got := wantWidth(l); got > w-1 {
					t.Errorf("w=%d %s 第 %d 行寬 %d,超過 w-1:%q", w, name, i, got, l)
				}
			}
		}
	}
}

// 開場動畫兩秒後定格進捲動區:之後 View 裡不再有水豚,也不再有動畫。
func TestTUIFreezesCapybaraIntoScrollback(t *testing.T) {
	m := newTestTUI(t, &watchFake{st: playingState()})
	m.frozen = false
	got := recordPrintln(t)
	if !strings.Contains(m.View().Content, capyChin) {
		t.Fatal("開場要有水豚")
	}
	m = step(t, m, tuiFreezeMsg{}, true)
	if len(*got) != 1 {
		t.Fatalf("定格要正好推一次進捲動區:%d 次", len(*got))
	}
	// 定格姿勢是睜眼的。capybaraFrame(0) 是閉眼的(0 %% capyBlinkEvery == 0),不能拿來當定格。
	if !strings.Contains((*got)[0], capyEyesOpen) || strings.Contains((*got)[0], capyEyesShut) {
		t.Errorf("定格要睜著眼看使用者:%q", (*got)[0])
	}
	if !strings.Contains((*got)[0], capyMouthOut) {
		t.Errorf("定格要叼著草:%q", (*got)[0])
	}
	if strings.Contains(m.View().Content, capyChin) {
		t.Error("定格後 View 不該再有水豚")
	}
	// 再收到一次 freeze 不該重印(tick 與按鍵都會觸發)
	m = step(t, m, tuiFreezeMsg{}, true)
	if len(*got) != 1 {
		t.Errorf("重複的 freeze 不該再印:%d 次", len(*got))
	}
}

// 開場期間按鍵先定格再處理:不然 Enter 會在水豚還在 View 裡時 Exec,子命令的輸出印在它下面,
// 兩秒到再定格印一次 —— 就是使用者回報的「水豚頭重複」。
func TestTUIKeyDuringIntroFreezesFirst(t *testing.T) {
	f := &watchFake{st: playingState()}
	m := newTestTUI(t, f)
	m.frozen = false
	got := recordPrintln(t)
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'n'})
	m = next.(tuiModel)
	if !m.frozen {
		t.Fatal("開場期間的按鍵要先定格")
	}
	if len(*got) != 1 || !strings.Contains((*got)[0], capyChin) {
		t.Fatalf("定格的水豚要進捲動區:%v", *got)
	}
	if cmd == nil {
		t.Fatal("按鍵本身也要照常生效")
	}
	if m2 := step(t, m, tea.KeyPressMsg{Code: 'n'}, true); !m2.frozen {
		t.Error("定格後仍應維持")
	}
}

// 命令回音先進捲動區、再讓出終端機;失敗才印結束碼,成功不印。
func TestTUIEchoesCommandAndExitCode(t *testing.T) {
	orig := tuiExecProcess
	tuiExecProcess = func(*exec.Cmd, tea.ExecCallback) tea.Cmd { return nil }
	t.Cleanup(func() { tuiExecProcess = orig })
	m := newTestTUI(t, &watchFake{st: playingState()})
	got := recordPrintln(t)
	m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
	m.input.SetValue("pl list")
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, false)
	if len(*got) != 1 || !strings.Contains((*got)[0], "pl list") || !strings.HasPrefix(ansi.Strip((*got)[0]), "> ") {
		t.Fatalf("要先把回音推進捲動區:%v", *got)
	}
	// 結束碼 1 = 壞了;2 / 3 是 capy 設計出來的結束碼(待套用、安全閥),不掛 ✗。
	for _, tc := range []struct {
		code     int
		wantMark string
	}{{1, "✗"}, {2, "·"}, {3, "·"}} {
		*got = nil
		m = step(t, m, tuiExecMsg{args: []string{"pl", "pull"}, err: exitErr(t, tc.code)}, true)
		if len(*got) != 1 || !strings.HasPrefix(ansi.Strip((*got)[0]), tc.wantMark) || !strings.Contains((*got)[0], "結束碼") {
			t.Errorf("結束碼 %d 要以 %s 開頭:%v", tc.code, tc.wantMark, *got)
		}
	}
}

// TestHelperExit:只在被 exitErr 叫起來時生效的空殼,用來當一個「會回指定結束碼」的行程。
func TestHelperExit(t *testing.T) {
	if c := os.Getenv("CAPY_TEST_EXIT"); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil {
			t.Fatal(err)
		}
		os.Exit(n)
	}
}

// exitErr:真的跑一個會回指定結束碼的行程,拿到貨真價實的 *exec.ExitError。
// 用測試自己的執行檔,不用 sh -c —— Windows 上沒有 sh 可以假設(CI 有 windows-latest),
// 現在能過只是因為 runner image 把 Git for Windows 的 sh.exe 放在 PATH 上。
func exitErr(t *testing.T, code int) error {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestHelperExit$")
	c.Env = append(os.Environ(), "CAPY_TEST_EXIT="+strconv.Itoa(code))
	err := c.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != code {
		t.Fatalf("造不出結束碼 %d 的錯誤:%v", code, err)
	}
	return err
}

// 錯誤第一次發生時整段進捲動區(永久可回頭看),狀態列只留短版;同一則不重複印 ——
// 輪詢每兩秒一次,重複印會把捲動區洗掉。恢復之後再發生算新的一件事,要再印。
func TestTUIErrorPrintedOnceUntilRecovered(t *testing.T) {
	boom := errors.New("osascript 失敗(Music.app 未安裝或未授權自動化?):exit status 1")
	f := &watchFake{err: boom}
	m := newTestTUI(t, f)
	got := recordPrintln(t)
	for i := 0; i < 3; i++ {
		m = step(t, m, tuiStateMsg{err: boom, gen: m.gen}, false)
	}
	if len(*got) != 1 {
		t.Fatalf("同一則錯誤只印一次:%v", *got)
	}
	if !strings.Contains((*got)[0], "osascript") {
		t.Errorf("捲動區要留整段:%q", (*got)[0])
	}
	if v := m.View().Content; strings.Contains(v, "osascript") {
		t.Errorf("狀態列只留短版,不該一直佔著整段錯誤:%q", v)
	}
	m = step(t, m, tuiStateMsg{st: playingState(), gen: m.gen}, false) // 恢復
	m = step(t, m, tuiStateMsg{err: boom, gen: m.gen}, false)
	if len(*got) != 2 {
		t.Errorf("恢復之後再發生要再印一次:%v", *got)
	}
}

// 停擺的措辭只在狀態列,不進捲動區(捲動區留的是真正的錯誤內容)。
func TestTUIStalledWordingStaysInStatusLine(t *testing.T) {
	boom := errors.New("dial tcp: no route to host")
	m := newTestTUI(t, &watchFake{err: boom})
	got := recordPrintln(t)
	for i := 0; i < tuiMaxFails; i++ {
		m = step(t, m, tuiStateMsg{err: boom, gen: m.gen}, false)
	}
	if !m.stalled {
		t.Fatal("要進入停擺")
	}
	if !strings.Contains(m.View().Content, "已停止輪詢") {
		t.Errorf("狀態列要說已停止輪詢:%q", m.View().Content)
	}
	if strings.Contains(joined(got), "已停止輪詢") {
		t.Errorf("停擺的措辭不該進捲動區:%v", *got)
	}
}

func TestTUIQuestionMarkPrintsKeymap(t *testing.T) {
	m := newTestTUI(t, &watchFake{st: playingState()})
	got := recordPrintln(t)
	m = step(t, m, tea.KeyPressMsg{Code: '?'}, true)
	if len(*got) != 1 || !strings.Contains((*got)[0], "播放/暫停") {
		t.Fatalf("? 要把完整鍵位推進捲動區:%v", *got)
	}
}

// 一般模式維持熱鍵、不吃打字:設計文件 §3 原本寫「打字直接進入輸入」,但 pl / play / pause /
// prev / push 這些最常打的命令開頭就是 p,那會變成「按了下一首,然後 l sync 進輸入行」——
// 靜靜改動播放狀態。進輸入一律走 /(PR #44 review 的判斷,計畫 §10 有記)。
func TestTUINormalModeKeysStayHotkeys(t *testing.T) {
	f := &watchFake{st: playingState()}
	m := newTestTUI(t, f)
	for _, k := range []rune{'n', 'p'} {
		m = step(t, m, tea.KeyPressMsg{Code: k}, true)
		if m.typing {
			t.Fatalf("%c 是熱鍵,不該進入輸入模式", k)
		}
	}
	if len(f.calls) != 2 {
		t.Errorf("n / p 要真的送出控制指令:%v", f.calls)
	}
	if m.input.Value() != "" {
		t.Errorf("熱鍵不該把字元帶進輸入行:%q", m.input.Value())
	}
}

// 有曲目時也可能同時有狀況。限流最典型:上一首歌還在播,只換掉整行的話畫面看起來一切正常,
// 實際上正在被限流 —— 前一版無條件印 ⚠ m.err 看得到,壓成四行之後差點漏掉。
func TestTUIStatusLineCarriesErrorAlongsideTrack(t *testing.T) {
	m := newTestTUI(t, &watchFake{st: playingState()})
	got := recordPrintln(t)
	rl := &provider.RateLimitError{Seconds: 30, Message: "retry after 30s"}
	m = step(t, m, tuiStateMsg{st: playingState(), err: rl, gen: m.gen}, false)
	v := m.View().Content
	if !strings.Contains(v, "派對動物") || !strings.Contains(v, "限流") {
		t.Errorf("曲目與限流狀態要同時看得到:%q", v)
	}
	if len(*got) != 0 {
		t.Errorf("限流會持續一陣子,不該每兩秒往捲動區印一次:%v", *got)
	}
	// 播放器未執行是狀態不是失敗:用它自己的措辭,不要退回通用的「讀不到播放狀態(r 重試)」。
	m2 := newTestTUI(t, &watchFake{st: playingState()})
	m2 = step(t, m2, tuiStateMsg{err: provider.ErrPlayerNotRunning, gen: m2.gen}, false)
	v2 := m2.View().Content
	if !strings.Contains(v2, "播放器未執行") || strings.Contains(v2, "r 重試") {
		t.Errorf("播放器未執行要有自己的說法,r 也不是解法:%q", v2)
	}
}

// 明確按 r 重試是新的一件事:又撞到同一則錯誤時要再印一次,不然使用者按了鍵,
// 十秒內畫面上完全沒有任何事情發生過的痕跡。
func TestTUIRetryClearsErrorMemory(t *testing.T) {
	boom := errors.New("dial tcp: no route to host")
	m := newTestTUI(t, &watchFake{err: boom})
	got := recordPrintln(t)
	for i := 0; i < tuiMaxFails; i++ {
		m = step(t, m, tuiStateMsg{err: boom, gen: m.gen}, false)
	}
	if len(*got) != 1 || !m.stalled {
		t.Fatalf("前置條件:%v stalled=%v", *got, m.stalled)
	}
	m = step(t, m, tea.KeyPressMsg{Code: 'r'}, false)
	m = step(t, m, tuiStateMsg{err: boom, gen: m.gen}, false)
	if len(*got) != 2 {
		t.Errorf("r 之後再撞到同一則錯誤要再印一次:%v", *got)
	}
}

// tuiJoin 說「後面的段丟掉」就要真的丟掉:某段被截在 w-1 時,下一段會拿到 1 格預算
// 再吐一顆點出來,變成三個點。
func TestTUIJoinStopsAtTruncation(t *testing.T) {
	long := strings.Repeat("曲", 30)
	for w := 12; w <= 40; w++ {
		out := tuiJoin(w, tuiSeg{"  ▶ ", nil}, tuiSeg{long, nil}, tuiSeg{" · 1:23 / 4:09", nil})
		if strings.HasSuffix(out, "...") {
			t.Errorf("w=%d 截斷後多漏一顆點:%q", w, out)
		}
		if wd := wantWidth(out); wd > w {
			t.Errorf("w=%d 寬 %d 超出:%q", w, wd, out)
		}
	}
	// 沒截斷時每一段都要在
	if out := tuiJoin(40, tuiSeg{"a", nil}, tuiSeg{"b", nil}, tuiSeg{"c", nil}); out != "abc" {
		t.Errorf("放得下就不該丟段:%q", out)
	}
}
