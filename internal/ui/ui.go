// Package ui: 輸出層。鐵則: 非 TTY(pipe/cron)一律純文字——可腳本化是核心價值(spec §8.5.6)。
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"
)

// IsTTY 用 x/term(Windows console 上 os.Stat trick 不可靠)。
func IsTTY(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// charm v2 調整條款: 若 v2 API 與此處不符, 以 go doc charm.land/lipgloss/v2 為準。
var boldStyle = lipgloss.NewStyle().Bold(true)

// Bold: TTY 才上樣式。不要用在 Table 的儲存格——ANSI 會弄壞 tabwriter 欄寬。
func Bold(tty bool, s string) string {
	if !tty {
		return s
	}
	return boldStyle.Render(s)
}

// Table: TTY → tabwriter 對齊含標題; 非 TTY → 無標題 raw TSV(cut -f 友善)。
func Table(w io.Writer, tty bool, header []string, rows [][]string) {
	if !tty {
		for _, r := range rows {
			fmt.Fprintln(w, strings.Join(r, "\t"))
		}
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

// FormatDuration: ms → m:ss。
func FormatDuration(ms int) string {
	s := ms / 1000
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
