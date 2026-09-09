package cli

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// typeKeys:一個字一個字打進去。可見字元的 KeyPressMsg 在真的終端機裡 Code 與 Text 都有,
// 而 bubbles 的 textinput 是看 Text 插字的 —— 只給 Code 的話字進不去,測試會靜靜地什麼都沒打。
func typeKeys(t *testing.T, m tuiModel, s string) tuiModel {
	t.Helper()
	for _, r := range s {
		m = step(t, m, tea.KeyPressMsg{Code: r, Text: string(r)}, false)
	}
	return m
}

// 命令清單從 cobra 的命令樹長出來,不另外維護 —— 手寫的那份一定會跟實作走鏢。
func TestTUICommandsComeFromCobra(t *testing.T) {
	got := tuiCommands()
	paths := make(map[string]string, len(got))
	for _, it := range got {
		paths[it.path] = it.short
	}
	for _, want := range []string{"pl list", "pl sync", "search", "doctor", "seek", "vol"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("清單少了 %q", want)
		}
	}
	if paths["pl list"] == "" {
		t.Error("每一列都要有一句說明(cobra 的 Short)")
	}
	for _, unwanted := range []string{"pl", "auth"} {
		if _, ok := paths[unwanted]; ok {
			t.Errorf("%q 不該進清單:它自己不能執行", unwanted)
		}
	}
	// 判準是 Runnable(),不是「葉節點」:resolve 自己吃參數能跑,底下卻掛了 resolve pin。
	// 用葉節點當判準會把它整個丟掉,而且是靜靜地丟——正是這份清單號稱不會發生的走鏢。
	if _, ok := paths["resolve"]; !ok {
		t.Error("resolve 自己可執行(Args: MaximumNArgs(1)),要進清單")
	}
	if _, ok := paths["resolve pin"]; !ok {
		t.Error("resolve pin 也要在(父節點可執行不影響子節點)")
	}
	// debug 是 Hidden,它與它的子命令都不可進選單:debug apple-token 會印 keychain 裡的 token。
	for p := range paths {
		if strings.HasPrefix(p, "debug") {
			t.Errorf("Hidden 的命令不可進選單:%q", p)
		}
	}
	// help / completion 是 cobra 在 Execute 時才掛上去的,新建的樹上還沒有——直接餵一棵掛好的樹,
	// 不然那個過濾條件是死碼,拿掉也不會有人發現。
	root := newRootCmd()
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	var names []string
	for _, c := range root.Commands() {
		names = append(names, c.Name())
	}
	if !slices.Contains(names, "help") || !slices.Contains(names, "completion") {
		t.Fatalf("前置條件:這棵樹上要有 help 與 completion:%v", names)
	}
	for _, it := range tuiCommandsOf(root) {
		if head, _, _ := strings.Cut(it.path, " "); head == "help" || head == "completion" {
			t.Errorf("cobra 自帶的命令不該進選單:%q", it.path)
		}
	}
}

// / 開選單、打字過濾、⏎ 帶進輸入行(不執行)、Esc 收起。
func TestTUISlashMenuFlow(t *testing.T) {
	var ran []string
	orig := tuiExecProcess
	tuiExecProcess = func(c *exec.Cmd, _ tea.ExecCallback) tea.Cmd { ran = c.Args; return nil }
	t.Cleanup(func() { tuiExecProcess = orig })

	m := newTestTUI(t, &watchFake{st: playingState()})
	m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
	if !m.typing || m.input.Value() != "/" {
		t.Fatalf("/ 要進輸入行並帶入 /:typing=%v value=%q", m.typing, m.input.Value())
	}
	items, ok := m.menu()
	if !ok || len(items) < 5 {
		t.Fatalf("選單要開著且列出全部命令:ok=%v n=%d", ok, len(items))
	}
	m = typeKeys(t, m, "pl s") // 打字過濾:pl s → pl show / pl sync
	items, _ = m.menu()
	if len(items) == 0 {
		t.Fatal("pl s 應該還有東西")
	}
	for _, it := range items {
		if !strings.Contains(it.path, "pl s") {
			t.Errorf("過濾沒生效:%q", it.path)
		}
	}
	// ↓ 選下一列,⏎ 帶進輸入行而不是執行
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyDown}, false)
	want := items[1].path
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, false)
	if ran != nil {
		t.Errorf("⏎ 在選單開著時不該執行:%v", ran)
	}
	if m.input.Value() != want+" " {
		t.Errorf("⏎ 要把命令帶進輸入行(後面留空白補參數):%q,want %q", m.input.Value(), want+" ")
	}
	if _, open := m.menu(); open {
		t.Error("帶進輸入行之後選單要收起來(值不再以 / 開頭)")
	}
	// 這時候再按 ⏎ 才真的執行
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, false)
	if len(ran) < 2 || !strings.HasPrefix(strings.Join(ran[1:], " "), want) {
		t.Errorf("第二次 ⏎ 要執行:%v", ran)
	}
}

func TestTUISlashMenuEscAndSelectionClamp(t *testing.T) {
	m := newTestTUI(t, &watchFake{st: playingState()})
	m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
	// ↑ 到頂、↓ 到底都要夾住,不可越界
	for i := 0; i < 40; i++ {
		m = step(t, m, tea.KeyPressMsg{Code: tea.KeyDown}, false)
	}
	items, _ := m.menu()
	if m.menuSel != len(items)-1 {
		t.Errorf("↓ 到底要停在最後一列:sel=%d n=%d", m.menuSel, len(items))
	}
	for i := 0; i < 40; i++ {
		m = step(t, m, tea.KeyPressMsg{Code: tea.KeyUp}, false)
	}
	if m.menuSel != 0 {
		t.Errorf("↑ 到頂要停在第一列:sel=%d", m.menuSel)
	}
	// 過濾到剩很少時,選取要夾回範圍內
	m.menuSel = 5
	m.input.SetValue("/doctor")
	if items, _ := m.menu(); len(items) != 1 {
		t.Fatalf("doctor 應該只剩一列:%d", len(items))
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, false)
	if !strings.HasPrefix(m.input.Value(), "doctor") {
		t.Errorf("選取越界時要夾回範圍內,不可 panic 或選到別的:%q", m.input.Value())
	}
	// Esc 收選單(清空輸入),再一次才離開輸入模式
	m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, false)
	if _, open := m.menu(); open || !m.typing {
		t.Errorf("Esc 要收選單但留在輸入模式:open=%v typing=%v", open, m.typing)
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, false)
	if m.typing {
		t.Error("再一次 Esc 才離開輸入模式")
	}
}

// ↑↓ 在選單沒開時翻這次 session 打過的命令。
func TestTUICommandHistory(t *testing.T) {
	orig := tuiExecProcess
	tuiExecProcess = func(*exec.Cmd, tea.ExecCallback) tea.Cmd { return nil }
	t.Cleanup(func() { tuiExecProcess = orig })

	m := newTestTUI(t, &watchFake{st: playingState()})
	for _, cmd := range []string{"pl list", "search 五月天"} {
		m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
		m.input.SetValue(cmd)
		m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, false)
	}
	if len(m.hist) != 2 {
		t.Fatalf("兩個命令都要記進歷史:%v", m.hist)
	}
	// 一般模式按 ↑:進輸入行並帶出最後一個
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyUp}, false)
	if !m.typing || m.input.Value() != "search 五月天" {
		t.Fatalf("↑ 要帶出最後一個:typing=%v value=%q", m.typing, m.input.Value())
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyUp}, false)
	if m.input.Value() != "pl list" {
		t.Errorf("再 ↑ 要更早一個:%q", m.input.Value())
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyUp}, false)
	if m.input.Value() != "pl list" {
		t.Errorf("翻到頂就停住:%q", m.input.Value())
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyDown}, false)
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyDown}, false)
	if m.input.Value() != "" {
		t.Errorf("翻回最新之後再往下是空的輸入行:%q", m.input.Value())
	}
	// 沒有歷史時 ↑ 不該把人拖進輸入模式
	m2 := newTestTUI(t, &watchFake{st: playingState()})
	m2 = step(t, m2, tea.KeyPressMsg{Code: tea.KeyUp}, false)
	if m2.typing {
		t.Error("沒有歷史時 ↑ 不該進輸入模式")
	}
}

// 翻歷史不該把打到一半的字吃掉(bash / zsh 會把它留在「最新」那格),
// 連續重複的命令也不該各記一筆(不然 ↑ 要多按幾次才走得回去)。
func TestTUIHistoryKeepsDraftAndDedupes(t *testing.T) {
	orig := tuiExecProcess
	tuiExecProcess = func(*exec.Cmd, tea.ExecCallback) tea.Cmd { return nil }
	t.Cleanup(func() { tuiExecProcess = orig })

	m := newTestTUI(t, &watchFake{st: playingState()})
	run := func(m tuiModel, cmd string) tuiModel {
		m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
		m.input.SetValue(cmd)
		return step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, false)
	}
	m = run(m, "pl list")
	m = run(m, "pl list") // 連續重複
	if len(m.hist) != 1 {
		t.Fatalf("連續重複的命令只記一筆:%v", m.hist)
	}
	m = run(m, "doctor")
	m = run(m, "pl list") // 不連續的重複要記
	if len(m.hist) != 3 {
		t.Fatalf("不連續的重複要各記一筆:%v", m.hist)
	}

	// 打到一半按 ↑ 查歷史,再 ↓ 翻回來要拿得回原本那串
	m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
	m.input.SetValue("search 派對")
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyUp}, false)
	if m.input.Value() != "pl list" {
		t.Fatalf("↑ 要帶出最後一個:%q", m.input.Value())
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyDown}, false)
	if m.input.Value() != "search 派對" {
		t.Errorf("翻回最新要拿回打到一半的那串:%q", m.input.Value())
	}
}

// 選單向上長,壓在四行之上;每一列都夾在 w-1,最多 tuiMenuRows 列。
func TestTUIMenuViewWidthAndRows(t *testing.T) {
	for _, w := range []int{30, 40, 80} {
		m := newTestTUI(t, &watchFake{st: playingState()})
		m = step(t, m, tea.WindowSizeMsg{Width: w, Height: 24}, false)
		m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
		items, _ := m.menu()
		lines := strings.Split(m.View().Content, "\n")
		rows := len(lines) - 4 // 底部四行不變
		if want := min(tuiMenuRows, len(items)); rows != want {
			t.Errorf("w=%d 選單 %d 列,要 %d", w, rows, want)
		}
		for i, l := range lines {
			if got := wantWidth(l); got > w-1 {
				t.Errorf("w=%d 第 %d 行寬 %d:%q", w, i, got, l)
			}
		}
		// 底部四行的順序不變(分隔線 / 狀態 / 輸入 / 提示),選單長在分隔線上面而不是插進捲動區
		if !strings.Contains(lines[len(lines)-4], "--") {
			t.Errorf("w=%d 分隔線該在倒數第四行:%q", w, lines[len(lines)-4])
		}
		if !strings.Contains(lines[len(lines)-2], ">") {
			t.Errorf("w=%d 輸入行該在倒數第二行:%q", w, lines[len(lines)-2])
		}
	}
	// 沒有符合的命令時給一行說明,不是空白
	got := tuiMenuView(nil, 0, 40, ui.DefaultTheme)
	if len(got) != 1 || !strings.Contains(got[0], "沒有符合") {
		t.Errorf("空清單要說話:%v", got)
	}
}

// 清單比可視列數長時,視窗要跟著選取捲動,而且永遠是滿的。
func TestTUIMenuWindowFollowsSelection(t *testing.T) {
	const n = 20
	for sel := 0; sel < n; sel++ {
		lo, hi := tuiMenuWindow(n, sel)
		if hi-lo != tuiMenuRows {
			t.Fatalf("sel=%d 視窗 %d 列,要 %d", sel, hi-lo, tuiMenuRows)
		}
		if sel < lo || sel >= hi {
			t.Fatalf("sel=%d 不在視窗 [%d,%d) 裡", sel, lo, hi)
		}
		if lo < 0 || hi > n {
			t.Fatalf("sel=%d 視窗越界 [%d,%d)", sel, lo, hi)
		}
	}
	if lo, hi := tuiMenuWindow(3, 2); lo != 0 || hi != 3 {
		t.Errorf("清單比可視列數短時不捲:[%d,%d)", lo, hi)
	}
}
