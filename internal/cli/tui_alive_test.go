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
			if !m.frozen || len(*got) != 1 {
				t.Fatalf("放不下就定格、正好印一次:frozen=%v %v", m.frozen, *got)
			}
			if n := len(viewLines(m)); n != 4 || strings.Contains(m.View().Content, capyFeet) {
				t.Errorf("定格之後畫面只剩底部四行:%d 行", n)
			}
			m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: tuiAliveMinHeight + 20}, true)
			if m.alive() || len(*got) != 1 || strings.Contains(m.View().Content, capyFeet) {
				t.Errorf("印過就不再常駐(不然捲動區一隻、畫面一隻):alive=%v 印了 %d 次", m.alive(), len(*got))
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
