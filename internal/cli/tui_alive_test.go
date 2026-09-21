package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// 常駐的水豚(2026-09-21;使用者:「希望 CLI 的水豚也能像 web 一樣一直動」)。這一組測試守的是 PR #45 修掉的那個災難
// 不可以回來:使用者看過「水豚頭重複三次」。所以——牠只活在 View 裡、永遠不印進捲動區;那一塊的高度固定;
// 真的放不下時先印進捲動區才縮,而且從此不再常駐(捲動區一隻、View 一隻就是重複)。
// 真終端機的行為(執行子命令前後、選單開合、縮放視窗)另外用 pty + 終端機模擬器實跑過,見計畫文件。

func newAliveTUI(t *testing.T) (tuiModel, *[]string) {
	t.Helper()
	m := newTestTUI(t, &watchFake{st: playingState()})
	m.frozen = false
	got := recordPrintln(t)
	return step(t, m, tea.WindowSizeMsg{Width: 100, Height: tuiAliveMinHeight}, true), got
}

// drainBatch:把一個 cmd(可能是 tea.Batch)跑完,收集它送出來的訊息。只給「裡面沒有 tea.Tick」的 cmd 用——Tick 會真的等。
func drainBatch(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		out = append(out, drainBatch(c)...)
	}
	return out
}

func viewLines(m tuiModel) []string { return strings.Split(m.View().Content, "\n") }

func TestTUICapybaraStaysAliveOnATallTerminal(t *testing.T) {
	m, got := newAliveTUI(t)
	if !m.alive() {
		t.Fatal("終端機夠高夠寬:水豚要常駐")
	}
	lines := viewLines(m)
	if len(lines) != capyBlockRows+4 || !strings.Contains(m.View().Content, capyFeet) || !strings.Contains(lines[len(lines)-1], "離開") {
		t.Fatalf("常駐的畫面 = 水豚那一塊(%d 行)+ 底部四行:%d 行\n%s", capyBlockRows, len(lines), m.View().Content)
	}
	// 開場的計時器到了、按了鍵:都不定格、都不印。
	m = step(t, m, tuiFreezeMsg{}, true)
	m = step(t, m, tea.KeyPressMsg{Code: '?', Text: "?"}, false)
	if m.frozen {
		t.Fatal("常駐的水豚不定格")
	}
	if strings.Contains(joined(got), capyFeet) {
		t.Fatalf("常駐的水豚永遠不印進捲動區(印了就是兩隻):%v", *got)
	}
	// 劇本演完之後繼續動:ticker 不停,而且往後一分鐘裡畫面真的有變。
	seen := map[string]bool{}
	for i := 0; i < len(capyStory)+240; i++ {
		next, cmd := m.Update(tuiFrameMsg{})
		if m = next.(tuiModel); cmd == nil {
			t.Fatalf("第 %d 幀之後 ticker 停了:常駐的水豚要一直動", i)
		}
		if i >= len(capyStory) {
			seen[m.View().Content] = true
		}
		if n := len(viewLines(m)); n != capyBlockRows+4 {
			t.Fatalf("第 %d 幀畫面高度變成 %d:高度一變就會留殘留", i, n)
		}
	}
	if len(seen) < 4 {
		t.Errorf("劇本演完之後的一分鐘只看到 %d 種畫面:要有眨眼、撥耳朵、吃草", len(seen))
	}
}

// 選單打開時借水豚的位置(貼著分隔線),整個畫面的高度不變;收起來水豚就回來。
func TestTUIAliveMenuBorrowsTheCapybaraBlock(t *testing.T) {
	m, _ := newAliveTUI(t)
	m = typeKeys(t, m, "/pl")
	lines := viewLines(m)
	if len(lines) != capyBlockRows+4 || strings.Contains(m.View().Content, capyFeet) {
		t.Fatalf("選單開著:高度不變、水豚讓位:%d 行\n%s", len(lines), m.View().Content)
	}
	if !strings.Contains(lines[capyBlockRows-1], "pl ") || !strings.HasPrefix(ansi.Strip(lines[capyBlockRows]), "---") {
		t.Errorf("選單要貼著分隔線:\n%s", m.View().Content)
	}
	m = typeKeys(t, m, " s") // 過濾變短:補空行,不縮
	if n := len(viewLines(m)); n != capyBlockRows+4 {
		t.Errorf("選單過濾變短之後高度變成 %d", n)
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, false)
	if lines = viewLines(m); len(lines) != capyBlockRows+4 || !strings.Contains(m.View().Content, capyFeet) {
		t.Errorf("選單收起來水豚要回來、高度不變:%d 行", len(lines))
	}
}

// 子命令執行中整個畫面縮成一行空白(原因在 View 的註解):水豚也要讓開,不然 bubbletea 收回終端機時會上移十四行、
// 蓋掉子命令的輸出。
func TestTUIAliveShrinksToOneLineWhileACommandRuns(t *testing.T) {
	m, got := newAliveTUI(t)
	exec := recordExec(t)
	m = typeKeys(t, m, "/pl list")
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, false)
	if len(*exec) != 1 || !m.running || m.View().Content != " " {
		t.Fatalf("執行中要縮成一行空白:exec=%v running=%v view=%q", *exec, m.running, m.View().Content)
	}
	if strings.Contains(joined(got), capyFeet) {
		t.Errorf("執行命令不可以把水豚留在捲動區:%v", *got)
	}
	m = step(t, m, tuiExecMsg{args: []string{"pl", "list"}}, true)
	if !strings.Contains(m.View().Content, capyFeet) || len(viewLines(m)) != capyBlockRows+4 {
		t.Error("命令跑完水豚要回來")
	}
}

// 視窗縮到放不下:先印進捲動區(推東西進去之後縮才清得乾淨)、畫面剩底部四行;之後視窗再變大也不再常駐。
func TestTUIAliveFreezesOnceWhenTheTerminalGetsTooSmall(t *testing.T) {
	for name, size := range map[string]tea.WindowSizeMsg{
		"太矮": {Width: 100, Height: tuiAliveMinHeight - 1},
		"太窄": {Width: tuiMinWidth - 1, Height: tuiAliveMinHeight},
	} {
		t.Run(name, func(t *testing.T) {
			m, got := newAliveTUI(t)
			// 常駐時開過選單:menuHigh 記著八列。常駐時用不到它,但定格之後的四行畫面會照它補空行——
			// 定格本身就推了東西進捲動區,所以要照「推進去之後歸零」的規矩歸零,不然底部區上面多出八行空白。
			m = typeKeys(t, m, "/pl")
			m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, false)
			m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, false)
			m = step(t, m, size, true)
			if !m.frozen || strings.Count(joined(got), capyOneLine)+strings.Count(joined(got), capyFeet) != 1 {
				t.Fatalf("放不下就定格、水豚正好印一次:frozen=%v %v", m.frozen, *got)
			}
			// 定格是單向的:要交代為什麼牠不動了、怎麼讓牠回來。而且必須跟橫幅同一次推進捲動區——分兩次推的話
			// renderer 拿舊的畫面高度算位置,那句話會蓋掉橫幅的下半截、行程還會卡住(pty 實跑重現過)。
			if len(*got) != 1 || !strings.Contains((*got)[0], "重開 capy") {
				t.Errorf("縮小視窗造成的定格要交代一句,而且跟橫幅同一次推:%d 次 %v", len(*got), *got)
			}
			printed := len(*got)
			if n := len(viewLines(m)); n != 4 || strings.Contains(m.View().Content, capyFeet) {
				t.Errorf("定格之後畫面只剩底部四行:%d 行", n)
			}
			m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: tuiAliveMinHeight + 20}, true)
			if m.alive() || len(*got) != printed || strings.Contains(m.View().Content, capyFeet) {
				t.Errorf("印過就不再常駐(不然捲動區一隻、畫面一隻):alive=%v 又印了 %d 次", m.alive(), len(*got)-printed)
			}
			if _, cmd := m.Update(tuiFrameMsg{}); cmd != nil {
				t.Error("定格後不該再排下一幀")
			}
		})
	}
}

// 還不知道終端機多高(WindowSizeMsg 還沒到)或不夠高:照舊——開場演一遍、定格印進捲動區。
func TestTUIShortTerminalKeepsTheOneShotIntro(t *testing.T) {
	m := newTestTUI(t, &watchFake{st: playingState()})
	m.frozen = false
	got := recordPrintln(t)
	if m.alive() {
		t.Fatal("還不知道高度:不可以當成夠高")
	}
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 24}, true)
	if m.alive() || m.frozen || len(viewLines(m)) != capyBlockRows {
		t.Fatalf("24 行放不下常駐的水豚:開場只有水豚與招牌(%d 行)", len(viewLines(m)))
	}
	m = step(t, m, tuiFreezeMsg{}, true)
	if !m.frozen || len(*got) != 1 || !strings.Contains((*got)[0], capyFeet) {
		t.Errorf("開場演完要定格印進捲動區:frozen=%v %v", m.frozen, *got)
	}
}

// 閒著的水豚:大部分時間站著不動(每一幀都在動的吉祥物很吵),但一分鐘裡看得到眨眼、撥耳朵、吃完一根草再叼一根。
func TestCapybaraIdleIsCalmButAlive(t *testing.T) {
	still := strings.Join(capybaraStill(), "\n")
	straw := func(f []string) int { return len(strings.ReplaceAll(f[4]+f[5], " ", "")) }
	full, bare := straw(capybaraStill()), straw(capybaraLines(capyPose{})) // 整根草、嘴裡沒有草
	var calm, blinks, ears, emptied, refilled int
	const minute = 240
	for n := 0; n < minute; n++ {
		f := capybaraFrame(len(capyStory) + n)
		if strings.Join(f, "\n") == still {
			calm++
		}
		if !strings.Contains(strings.Join(f, ""), "o") {
			blinks++
		}
		if f[0] != capybaraStill()[0] {
			ears++
		}
		if straw(f) == bare {
			emptied++
		} else if emptied > 0 && straw(f) == full && refilled == 0 {
			refilled = n
		}
		for _, i := range []int{3, 6, 7, 8} {
			if f[i] != capybaraStill()[i] {
				t.Fatalf("第 %d 幀第 %d 行不該變(身體不動)", n, i)
			}
		}
	}
	if strings.Join(capybaraFrame(len(capyStory)), "\n") != still {
		t.Error("劇本演完接的第一幀要是定格幀:不常駐的時候 ticker 可能多跳一幀,不可以跳")
	}
	if calm < minute*3/4 {
		t.Errorf("一分鐘 %d 幀裡只有 %d 幀站著不動:太吵了", minute, calm)
	}
	if blinks < 10 || ears < 10 || emptied == 0 || refilled == 0 {
		t.Errorf("一分鐘裡要看得到眨眼(%d)、撥耳朵(%d)、吃完一根草(空嘴 %d 幀)再叼一根(第 %d 幀)", blinks, ears, emptied, refilled)
	}
}

// capyBlockRows 是手抄的常數(tuiAliveMinHeight 要拿它當常數算):跟畫對不上的話,「水豚在」與「選單開著」兩種畫面
// 會差一行——就是畫面變矮留殘留的那個成因(review #73)。
func TestCapyBlockRowsMatchesTheArt(t *testing.T) {
	if want := len(capybaraStill()) + 2; capyBlockRows != want { // 水豚 + 空行 + 招牌
		t.Errorf("capyBlockRows = %d,畫出來是 %d 行", capyBlockRows, want)
	}
	if tuiAliveMinHeight != 2*(capyBlockRows+4) {
		t.Errorf("常駐的畫面最多佔半個終端機:tuiAliveMinHeight = %d", tuiAliveMinHeight)
	}
}

// 夠寬但很矮的終端機(編輯器底部的面板、tmux 上下分割):整隻水豚比終端機還高——畫面比終端機高正是 PR #45 的成因。
// 以前 model 不知道高度、沒辦法守;現在知道了:用一行版,而且一行版沒有劇本可演,第一個 tick 就定格(review #73)。
func TestTUIVeryShortTerminalUsesTheOneLiner(t *testing.T) {
	m := newTestTUI(t, &watchFake{st: playingState()})
	m.frozen = false
	got := recordPrintln(t)
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: capyBlockRows}, true)
	if v := m.View().Content; strings.Contains(v, capyFeet) || !strings.Contains(v, capyOneLine) || len(viewLines(m)) > 3 {
		t.Fatalf("%d 行的終端機畫不下整隻(%d 行):開場要用一行版\n%s", capyBlockRows, capyBlockRows, v)
	}
	m = step(t, m, tuiFrameMsg{}, true)
	if !m.frozen || len(*got) != 1 || strings.Contains((*got)[0], capyFeet) || !strings.Contains((*got)[0], capyOneLine) {
		t.Errorf("一行版第一個 tick 就定格、印進捲動區的也是一行版:frozen=%v %v", m.frozen, *got)
	}
	// 高一行就畫得下:照舊。
	m2 := newTestTUI(t, &watchFake{st: playingState()})
	m2.frozen = false
	m2 = step(t, m2, tea.WindowSizeMsg{Width: 100, Height: capyBlockRows + 1}, true)
	if !strings.Contains(m2.View().Content, capyFeet) {
		t.Error("畫得下就要畫整隻")
	}
}

// CAPY_MOTION=never:不演開場、不常駐,水豚直接定格印進捲動區。終端機沒有 prefers-reduced-motion,
// 一個每 250 毫秒可能變動的區塊對前庭敏感或用螢幕閱讀器的人是實打實的干擾;SSH 上它也是持續的往返(review #73)。
func TestTUIMotionNeverFreezesImmediately(t *testing.T) {
	t.Setenv("CAPY_MOTION", "never")
	m := newTestTUI(t, &watchFake{st: playingState()})
	m.frozen = false
	got := recordPrintln(t)
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: tuiAliveMinHeight + 10}, true)
	if m.alive() {
		t.Fatal("關掉動畫就不常駐,終端機再大也一樣")
	}
	// Init 不排 frame tick、馬上送定格:跑一遍它回傳的 cmd,裡面要有 tuiFreezeMsg、不可以有 tuiFrameMsg。
	var sawFreeze bool
	for _, msg := range drainBatch(m.Init()) {
		switch msg.(type) {
		case tuiFreezeMsg:
			sawFreeze = true
		case tuiFrameMsg:
			t.Error("關掉動畫不該排 frame tick")
		}
	}
	if !sawFreeze {
		t.Fatal("關掉動畫:Init 要馬上送定格")
	}
	m = step(t, m, tuiFreezeMsg{}, true)
	if !m.frozen || len(*got) != 1 || !strings.Contains((*got)[0], capyFeet) || len(viewLines(m)) != 4 {
		t.Errorf("定格幀印進捲動區一次、畫面剩底部四行:frozen=%v 印了 %d 次、%d 行", m.frozen, len(*got), len(viewLines(m)))
	}
}
