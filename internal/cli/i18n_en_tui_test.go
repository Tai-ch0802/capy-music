package cli

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// 英文模式:tui.go / tui_menu.go(T2c)。英文比中文長、而且一個字母一欄:底部四行、? 鍵位表、選單列在各個測試寬度
// 都不可以撐出 w-1(換行 = 底部變五行,設計文件 §1 的老問題);提示列與鍵位表在 80 欄要完整、不被截成 ".."。
// 尺是 wantWidth(非 ASCII 一律兩欄),不是被測的 tuiWidth。選單的說明欄是 cobra 的 .Short(別的檔),不驗有沒有中文。

func newEnglishTUI(t *testing.T, w int) tuiModel {
	t.Helper()
	withLanguage(t, "en") // 先換語系再建 model:placeholder 在 newTUIModel 裡就算好了
	st := playingState()
	st.Track.Title = "Party Animal"
	m := newTestTUI(t, &watchFake{st: st})
	return step(t, m, tea.WindowSizeMsg{Width: w, Height: 24}, false)
}

func lastLine(m tuiModel) string {
	lines := strings.Split(m.View().Content, "\n")
	return ansi.Strip(lines[len(lines)-1])
}

func TestEnglishTUIBottomAreaFits(t *testing.T) {
	long := strings.Repeat("A Very Long Track Title ", 20)
	for _, w := range []int{24, 30, 40, 80} {
		for _, mode := range []string{"playing", "nostate", "stalled", "typing", "placeholder", "noplayback"} {
			m := newEnglishTUI(t, w)
			m.st.Track.Title = long
			switch mode {
			case "nostate":
				m.st = nil
			case "stalled":
				m.st, m.stalled = nil, true
			case "typing":
				m = step(t, m, tea.KeyPressMsg{Code: ':'}, false)
				m.input.SetValue(long)
			case "placeholder": // 空的輸入行顯示 placeholder:英文那句最長,要靠 tuiPlaceholder 量寬度挑
				m = step(t, m, tea.KeyPressMsg{Code: ':'}, false)
			case "noplayback":
				m.pc, m.st = nil, nil
			}
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != 4 {
				t.Errorf("w=%d %s:%d 行,要 4 行\n%q", w, mode, len(lines), m.View().Content)
			}
			for i, l := range lines {
				if got := wantWidth(l); got > w-1 {
					t.Errorf("w=%d %s 第 %d 行寬 %d,超過 w-1:%q", w, mode, i, got, l)
				}
				if hasCJK(l) {
					t.Errorf("w=%d %s 第 %d 行有中文:%q", w, mode, i, l)
				}
			}
		}
	}
}

// 80 欄(最常見的終端機)每一種提示列都完整,不被截斷。
func TestEnglishTUIHintsAreWholeAt80(t *testing.T) {
	cases := map[string]struct {
		setup func(tuiModel) tuiModel
		want  string
	}{
		"playing":    {func(m tuiModel) tuiModel { return m }, "space play/pause · ←→ ±10 s · / commands · ? keys · q quit"},
		"stalled":    {func(m tuiModel) tuiModel { m.stalled = true; return m }, "r reconnect · / commands · ? keys · q quit"},
		"noplayback": {func(m tuiModel) tuiModel { m.pc = nil; return m }, "/ commands · ? keys · q quit"},
		"typing": {func(m tuiModel) tuiModel { return step(t, m, tea.KeyPressMsg{Code: ':'}, false) },
			"↑↓ history · Enter run · Esc clear · Ctrl-C quit"},
		"menu": {func(m tuiModel) tuiModel { return step(t, m, tea.KeyPressMsg{Code: '/'}, false) },
			"↑↓ select · Tab/⏎ complete · Esc close"},
	}
	for name, c := range cases {
		m := c.setup(newEnglishTUI(t, 80))
		if got := lastLine(m); got != "  "+c.want || wantWidth(got) > 79 {
			t.Errorf("%s 提示列:%q(寬 %d)", name, got, wantWidth(got))
		}
	}
}

// ? 鍵位表:每一行在 80 欄都放得下(printBlock 逐行夾在 w-1,放不下就被截成 ".."、那行的說明就看不到了)。
func TestEnglishTUIKeymapFitsAt80(t *testing.T) {
	m := newEnglishTUI(t, 80)
	got := recordPrintln(t)
	step(t, m, tea.KeyPressMsg{Code: '?'}, true)
	if len(*got) != 1 {
		t.Fatalf("? 要推一次進捲動區:%v", *got)
	}
	want := strings.Split(tuiKeymap(), "\n")
	lines := strings.Split(ansi.Strip((*got)[0]), "\n")
	if len(lines) != len(want) || lines[0] != "Keys:" {
		t.Fatalf("鍵位表 %d 行只印出 %d 行:%q", len(want), len(lines), (*got)[0])
	}
	for i, l := range lines {
		if l != want[i] || wantWidth(l) > 79 || hasCJK(l) {
			t.Errorf("第 %d 行被截斷、超寬或有中文(寬 %d):%q", i, wantWidth(l), l)
		}
	}
	if !strings.Contains(tuiKeymap(), "/pl show Road Trip") {
		t.Error("範例清單名要是英文的")
	}
}

func TestEnglishTUIMenuRowsFit(t *testing.T) {
	for _, w := range []int{30, 40, 80} {
		m := newEnglishTUI(t, w)
		m = step(t, m, tea.KeyPressMsg{Code: '/'}, false)
		items, _ := m.menu()
		lines := strings.Split(m.View().Content, "\n")
		if rows := len(lines) - 4; rows != min(tuiMenuRows, len(items)) {
			t.Errorf("w=%d 選單 %d 列", w, rows)
		}
		for i, l := range lines {
			if got := wantWidth(l); got > w-1 {
				t.Errorf("w=%d 第 %d 行寬 %d:%q", w, i, got, l)
			}
		}
		if !strings.HasPrefix(ansi.Strip(lines[len(lines)-4]), "--") || !strings.HasPrefix(ansi.Strip(lines[len(lines)-2]), ">") {
			t.Errorf("w=%d 分隔線 / 輸入行的位置不對:%q", w, lines)
		}
	}
	withLanguage(t, "en")
	if got := tuiMenuView(nil, 0, 40, ui.DefaultTheme); len(got) != 1 || ansi.Strip(got[0]) != "  No matching commands" {
		t.Errorf("空清單:%q", got)
	}
}

func TestEnglishTUIStatusLine(t *testing.T) {
	boom := errors.New("dial tcp: no route to host")
	cases := []struct {
		name  string
		setup func(tuiModel) tuiModel
		want  string
	}{
		{"playing", func(m tuiModel) tuiModel { return m },
			"  ▶ Party Animal · 1:23 / 4:09 · MacBook Pro · volume 50"},
		{"nostate", func(m tuiModel) tuiModel { m.st = nil; return m }, "  Nothing playing"},
		{"stalled", func(m tuiModel) tuiModel { m.st, m.stalled = nil, true; return m }, "  Polling stopped (r to retry)"},
		{"noplayback", func(m tuiModel) tuiModel { m.pc, m.pcErr = nil, nil; return m },
			"  No playback control: not supported by this platform"},
		{"player not running", func(m tuiModel) tuiModel {
			return step(t, m, tuiStateMsg{err: provider.ErrPlayerNotRunning, gen: m.gen}, false)
		}, "  Player not running"},
		{"unreadable", func(m tuiModel) tuiModel {
			m.st = nil
			return step(t, m, tuiStateMsg{err: boom, gen: m.gen}, false)
		}, "  Can't read playback state (r to retry)"},
		{"rate limited", func(m tuiModel) tuiModel {
			return step(t, m, tuiStateMsg{st: m.st, err: &provider.RateLimitError{Seconds: 30}, gen: m.gen}, false)
		}, "  ▶ Party Animal · 1:23 / 4:09 · MacBook Pro · volume 50 · Rate limited; waiting to retry"},
		{"control failed", func(m tuiModel) tuiModel {
			return step(t, m, tuiStateMsg{err: boom, fromCtl: true, gen: m.gen}, false)
		}, "  ▶ Party Animal · 1:23 / 4:09 · MacBook Pro · volume 50 · That action failed (see above)"},
	}
	for _, c := range cases {
		recordPrintln(t)
		m := c.setup(newEnglishTUI(t, 100))
		if got := ansi.Strip(m.statusLine(99)); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestEnglishTUIScrollbackLines(t *testing.T) {
	m := newEnglishTUI(t, 100)
	got := recordPrintln(t)
	step(t, m, tuiExecMsg{args: []string{"pl", "pull"}, err: exitErr(t, 1)}, true)
	step(t, m, tuiExecMsg{args: []string{"pl", "pull"}, err: exitErr(t, 3)}, true)
	m.exe = ""
	m = step(t, m, tea.KeyPressMsg{Code: ':'}, false)
	m.input.SetValue("config list")
	step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, true)
	want := []string{
		"✗ capy pl pull exited with code 1",
		"· capy pl pull exited with code 3",
		"✗ Can't find capy's own executable, so the command line is disabled",
	}
	if len(*got) != len(want) {
		t.Fatalf("推進捲動區的行:%q", *got)
	}
	for i := range want {
		if ansi.Strip((*got)[i]) != want[i] {
			t.Errorf("第 %d 行:%q,要 %q", i, ansi.Strip((*got)[i]), want[i])
		}
	}
}

// 視窗縮到太窄(tuiMinWidth-1 欄)而定格時交代的那句:英文要在那個寬度放得下,不換行。
// (這一句沒有夾寬度——跟橫幅同一次推進捲動區;繁中版在這個寬度本來就會換行,不動它。)
func TestEnglishTUIFrozenNoteFitsWhenTooNarrow(t *testing.T) {
	withLanguage(t, "en")
	m, got := newAliveTUI(t)
	w := tuiMinWidth - 1
	step(t, m, tea.WindowSizeMsg{Width: w, Height: tuiAliveMinHeight}, true)
	if len(*got) != 1 {
		t.Fatalf("定格要推一次:%v", *got)
	}
	var note string
	for _, l := range strings.Split(ansi.Strip((*got)[0]), "\n") {
		if strings.Contains(l, "back on restart") {
			note = l
		}
	}
	if note != "  Capybara froze (no room); back on restart." || wantWidth(note) > w-1 {
		t.Errorf("定格的說明:%q(寬 %d,上限 %d)", note, wantWidth(note), w-1)
	}
}

// placeholder 依當下語系的譯文量寬度挑:英文那句比中文長(36 欄 vs 29),門檻跟著變,不沿用中文的。
func TestEnglishTUIPlaceholder(t *testing.T) {
	withLanguage(t, "en")
	for w, want := range map[int]string{80: "Type a capy subcommand, e.g. pl list", 40: "Type a capy subcommand, e.g. pl list",
		39: "capy subcommand", 19: "capy subcommand", 18: "pl list", 10: ""} {
		if got := tuiPlaceholder(w); got != want {
			t.Errorf("w=%d:%q,要 %q", w, got, want)
		}
	}
}
