package ui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// 檢視窗格是純函式:Update 進、model 出,不需要 TTY。
func TestPagerKeysAndView(t *testing.T) {
	// 表頭 120 欄:A 在最左、Z 在最右,橫向捲到底才看得到 Z;資料列刻意只有 119 欄(列尾補白會被修掉),
	// 橫向上限要以表頭為準,不然 Z 永遠捲不到
	header := "A" + strings.Repeat(" ", 118) + "Z"
	rows := make([]string, 30)
	for i := range rows {
		rows[i] = fmt.Sprintf("%-3d", i+1) + strings.Repeat(".", 115) + "z"
	}
	m := newPager(header, rows)
	step := func(msg tea.Msg) tea.Cmd {
		t.Helper()
		next, cmd := m.Update(msg)
		m = next.(pagerModel)
		return cmd
	}
	step(tea.WindowSizeMsg{Width: 40, Height: 12})
	v := m.View()
	if !v.AltScreen {
		t.Error("窗格要在 alt screen:關了之後捲動區要回到原樣")
	}
	lines := strings.Split(v.Content, "\n")
	if len(lines) != 12 {
		t.Fatalf("表頭 + 10 列 + 狀態列 = 12 行,得到 %d:%q", len(lines), v.Content)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("第 %d 行寬 %d 超過視窗:%q", i, w, l)
		}
	}
	plain := func(s string) string { return ansi.Strip(s) }
	if !strings.HasPrefix(plain(lines[0]), "A") || strings.Contains(lines[0], "Z") {
		t.Errorf("一開始看最左邊:%q", lines[0])
	}
	if s := plain(lines[11]); !strings.Contains(s, "第 1–10 列 / 30") || !strings.Contains(s, "欄 1–40 / 120") {
		t.Errorf("狀態列要說看到哪:%q", s)
	}
	// 寬一點的視窗要看得到 g/G 的提示(它是唯一跳很遠的鍵,不寫出來沒人發現)
	step(tea.WindowSizeMsg{Width: 100, Height: 12})
	if s := plain(strings.Split(m.View().Content, "\n")[11]); !strings.Contains(s, "g/G") {
		t.Errorf("狀態列要提 g/G:%q", s)
	}
	step(tea.WindowSizeMsg{Width: 40, Height: 12})
	// → 捲 8 欄,表頭跟著捲
	step(tea.KeyPressMsg{Code: tea.KeyRight})
	lines = strings.Split(m.View().Content, "\n")
	if !strings.Contains(plain(lines[11]), "欄 9–48 / 120") || strings.HasPrefix(plain(lines[0]), "A") {
		t.Errorf("→ 之後表頭與狀態列都要往右 8 欄:%q %q", lines[0], lines[11])
	}
	// 捲到底夾住:最右邊的 Z / z 要看得到
	for i := 0; i < 30; i++ {
		step(tea.KeyPressMsg{Code: tea.KeyRight})
	}
	lines = strings.Split(m.View().Content, "\n")
	if !strings.HasSuffix(strings.TrimRight(plain(lines[0]), " "), "Z") || !strings.HasSuffix(strings.TrimRight(plain(lines[1]), " "), "z") {
		t.Errorf("捲到底要看得到最右欄:%q %q", lines[0], lines[1])
	}
	if !strings.Contains(plain(lines[11]), "欄 81–120 / 120") {
		t.Errorf("捲到底的狀態列:%q", lines[11])
	}
	// ← 回來
	step(tea.KeyPressMsg{Code: tea.KeyLeft})
	if !strings.Contains(plain(strings.Split(m.View().Content, "\n")[11]), "欄 73–112 / 120") {
		t.Errorf("← 要回 8 欄:%q", m.View().Content)
	}
	// ↓ 一列、PgDn 一頁、G 到底、g 回頂
	step(tea.KeyPressMsg{Code: tea.KeyDown})
	if !strings.Contains(plain(strings.Split(m.View().Content, "\n")[11]), "第 2–11 列 / 30") {
		t.Errorf("↓ 要往下一列:%q", m.View().Content)
	}
	step(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if !strings.Contains(plain(strings.Split(m.View().Content, "\n")[11]), "第 12–21 列 / 30") {
		t.Errorf("PgDn 要往下一頁:%q", m.View().Content)
	}
	step(tea.KeyPressMsg{Code: 'G', Text: "G"})
	if !strings.Contains(plain(strings.Split(m.View().Content, "\n")[11]), "第 21–30 列 / 30") {
		t.Errorf("G 要到底:%q", m.View().Content)
	}
	step(tea.KeyPressMsg{Code: 'g', Text: "g"})
	if !strings.Contains(plain(strings.Split(m.View().Content, "\n")[11]), "第 1–10 列 / 30") {
		t.Errorf("g 要回頂:%q", m.View().Content)
	}
	// q / Esc 是「看完了」;Ctrl-C 是中斷,命令要跟著結束(不可再問確認)
	for _, k := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: tea.KeyEscape}} {
		cmd := step(k)
		if cmd == nil {
			t.Fatalf("%s 要離開", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s 要是 Quit", k)
		}
	}
	if cmd := step(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd == nil {
		t.Fatal("Ctrl-C 要有反應")
	} else if _, ok := cmd().(tea.InterruptMsg); !ok {
		t.Error("Ctrl-C 要是 Interrupt,不是 Quit")
	}
	// 視窗變小:高度跟著變
	step(tea.WindowSizeMsg{Width: 30, Height: 5})
	if n := len(strings.Split(m.View().Content, "\n")); n != 5 {
		t.Errorf("視窗 5 行就畫 5 行:%d", n)
	}
}

// 表格比終端機寬、而且 stdin / stdout 都是 TTY 時才開窗格;窗格拿到的是自然寬度的行(每列一行、不截不折);
// 看完照樣把換行版印進捲動區。放得下、非 TTY、stdin 是管線、沒有列:都不開。
func TestTableOpensPagerOnlyWhenWiderThanTerminal(t *testing.T) {
	origPager, origStdin, origEnabled := Pager, StdinIsTTY, PagerEnabled
	t.Cleanup(func() { Pager, StdinIsTTY, PagerEnabled = origPager, origStdin, origEnabled })
	var got struct {
		calls  int
		w      io.Writer
		header string
		rows   []string
	}
	Pager = func(w io.Writer, h string, rows []string) error {
		got.calls++
		got.w, got.header, got.rows = w, h, rows
		return nil
	}
	StdinIsTTY = func() bool { return true }
	PagerEnabled = func() bool { return true }

	id := strings.Repeat("a", 22)
	album := strings.Repeat("專輯", 8)
	header := []string{"ID", "曲名", "藝人", "專輯", "時長"}
	rows := [][]string{{id, "很長很長的歌名很長很長的歌名", "Billie Eilish & Khalid", album, "3:47"}, {"b", "短", "x", "y", "0:01"}}

	withWidth(t, 60)
	buf := &bytes.Buffer{}
	if err := Table(buf, true, header, rows); err != nil {
		t.Fatalf("看完就繼續,不該有錯:%v", err)
	}
	if got.calls != 1 || got.w != buf {
		t.Fatalf("比終端機寬要開一次窗格、畫在同一個 writer:%d %v", got.calls, got.w == buf)
	}
	if len(got.rows) != 2 || !strings.Contains(got.rows[0], album) || !strings.Contains(got.rows[0], id) || strings.Contains(got.rows[0], "…") {
		t.Errorf("窗格要拿到每列一行、完整不截的行:%q", got.rows)
	}
	if !strings.Contains(got.header, "專輯") || ansi.StringWidth(got.header) <= 60 {
		t.Errorf("窗格的表頭是自然寬度的一行:%q", got.header)
	}
	out := buf.String()
	if strings.Contains(out, "…") || !strings.Contains(out, id) || !strings.Contains(out, "專輯專輯") {
		t.Errorf("窗格關了之後換行版要留在捲動區:%q", out)
	}
	for i, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := ansi.StringWidth(l); w > 60 {
			t.Errorf("紀錄的第 %d 行寬 %d 超過終端:%q", i, w, l)
		}
	}
	// 放得下就不開
	got.calls = 0
	withWidth(t, 200)
	Table(&bytes.Buffer{}, true, header, rows)
	if got.calls != 0 {
		t.Error("放得下不該開窗格")
	}
	// 非 TTY 不開(TSV 照舊)
	withWidth(t, 60)
	buf.Reset()
	Table(buf, false, header, rows)
	if got.calls != 0 || !strings.HasPrefix(buf.String(), id+"\t") {
		t.Errorf("非 TTY 不該開窗格、輸出仍是 TSV:%d %q", got.calls, buf.String())
	}
	// stdin 是管線就不開,但換行版照印
	StdinIsTTY = func() bool { return false }
	buf.Reset()
	Table(buf, true, header, rows)
	if got.calls != 0 || strings.Contains(buf.String(), "…") || !strings.Contains(buf.String(), id) {
		t.Errorf("stdin 不是 TTY 不該開窗格、表格照印:%d %q", got.calls, buf.String())
	}
	// 沒有列就不開
	StdinIsTTY = func() bool { return true }
	Table(&bytes.Buffer{}, true, []string{strings.Repeat("H", 70)}, nil)
	if got.calls != 0 {
		t.Error("沒有列不該開窗格")
	}
	// --yes 的命令(NoPager)不開:README 說 --yes 只跳過確認,從頭到尾不碰鍵盤;變更集又是握著 pull.lock 印的
	buf.Reset()
	Table(buf, true, header, rows, NoPager)
	if got.calls != 0 || !strings.Contains(buf.String(), id) {
		t.Errorf("NoPager 不該開窗格、表格照印:%d %q", got.calls, buf.String())
	}
	// CAPY_PAGER=never 不開:有 TTY 但沒有人的情況(script / expect、CI 給了 pty)要有逃生口
	PagerEnabled = func() bool { return false }
	buf.Reset()
	Table(buf, true, header, rows)
	if got.calls != 0 || !strings.Contains(buf.String(), id) {
		t.Errorf("PagerEnabled=false 不該開窗格、表格照印:%d %q", got.calls, buf.String())
	}
	PagerEnabled = func() bool { return true }
}

// 窗格裡按 Ctrl-C 是中止:Table 回 ErrInterrupted、什麼都不印,呼叫端才不會接著問「套用?」(pl pull 看到
// 要刪 8 首按 Ctrl-C 的人已經表態了)。窗格開不起來(其他錯)就當沒開,表格照印。
func TestTableInterruptedPagerStopsTheCommand(t *testing.T) {
	origPager, origStdin, origEnabled := Pager, StdinIsTTY, PagerEnabled
	t.Cleanup(func() { Pager, StdinIsTTY, PagerEnabled = origPager, origStdin, origEnabled })
	StdinIsTTY = func() bool { return true }
	PagerEnabled = func() bool { return true }
	header := []string{"ID", "曲名", "藝人", "專輯", "時長"}
	rows := [][]string{{strings.Repeat("a", 22), "很長很長的歌名很長很長的歌名", "Billie Eilish & Khalid", strings.Repeat("專輯", 8), "3:47"}}
	withWidth(t, 60)

	Pager = func(io.Writer, string, []string) error { return tea.ErrInterrupted }
	buf := &bytes.Buffer{}
	err := Table(buf, true, header, rows)
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("Ctrl-C 要回 ErrInterrupted:%v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("中止就什麼都不印:%q", buf.String())
	}
	Pager = func(io.Writer, string, []string) error { return errors.New("no tty") }
	buf.Reset()
	if err := Table(buf, true, header, rows); err != nil || !strings.Contains(buf.String(), "aaaa") {
		t.Errorf("窗格開不起來就直接印、不回錯:%v %q", err, buf.String())
	}
}
