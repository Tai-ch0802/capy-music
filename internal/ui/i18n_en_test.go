package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

// 英文的檢視窗格狀態列:80 欄就放得下整行(g/G 與 q 都看得到),窄視窗照舊截到視窗寬。
func TestPagerStatusEnglish(t *testing.T) {
	prev := i18n.Current()
	i18n.Set("en")
	t.Cleanup(func() { i18n.Set(prev) })
	rows := make([]string, 30)
	for i := range rows {
		rows[i] = fmt.Sprintf("%-3d", i+1) + strings.Repeat(".", 116)
	}
	var m tea.Model = newPager(strings.Repeat("H", 120), rows)
	status := func(w int) string {
		m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: 12})
		lines := strings.Split(m.View().Content, "\n")
		return ansi.Strip(lines[len(lines)-1])
	}
	if got, want := status(80), "rows 1–10/30 · cols 1–80/120 · ←→ ↑↓ scroll g/G top/end q quit"; got != want {
		t.Errorf("80 欄:\n got %q\nwant %q", got, want)
	}
	if got := status(40); ansi.StringWidth(got) > 40 || !strings.HasPrefix(got, "rows 1–10/30 · cols 1–40/120") {
		t.Errorf("40 欄:%q", got)
	}
}

// 四位數的列與欄(捲到右下角,每個數字都是四位)在 80 欄仍放得下整行,q 的提示不被截掉。
func TestPagerStatusEnglishFourDigits(t *testing.T) {
	prev := i18n.Current()
	i18n.Set("en")
	t.Cleanup(func() { i18n.Set(prev) })
	rows := make([]string, 3000)
	for i := range rows {
		rows[i] = "."
	}
	var m tea.Model = newPager(strings.Repeat("H", 1200), rows)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'G', Text: "G"})
	for range 200 {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	}
	lines := strings.Split(m.View().Content, "\n")
	got := ansi.Strip(lines[len(lines)-1])
	if want := "rows 2991–3000/3000 · cols 1121–1200/1200 · ←→ ↑↓ scroll g/G top/end q quit"; got != want || ansi.StringWidth(got) > 80 {
		t.Errorf("80 欄、四位數:\n got %q(寬 %d)\nwant %q", got, ansi.StringWidth(got), want)
	}
}
