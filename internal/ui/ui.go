// Package ui: 輸出層。鐵則: 非 TTY(pipe/cron)一律純文字——可腳本化是核心價值(spec §8.5.6)。
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// IsTTY 用 x/term(Windows console 上 os.Stat trick 不可靠)。
func IsTTY(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// charm v2 調整條款: 若 v2 API 與此處不符, 以 go doc charm.land/lipgloss/v2 為準。
var boldStyle = lipgloss.NewStyle().Bold(true)

// Bold: TTY 才上樣式。可用在 Table 儲存格(欄寬以 ansi.StringWidth 計,不受 ANSI 影響)。
func Bold(tty bool, s string) string {
	if !tty {
		return s
	}
	return boldStyle.Render(s)
}

// tsvEscaper:非 TTY 儲存格若內嵌 tab/newline 會偽造出多欄/多列,cut -f 讀出來就錯了——全換成空白。
var tsvEscaper = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")

// TermWidth 回傳終端機欄數;取不到(CI、非終端機)用 80。測試可替換。
var TermWidth = func() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

const (
	colGap       = 2
	minIDWidth   = 9 // 8 字 + …
	minCellWidth = 4 // 3 字 + …
)

// Table: TTY → 依顯示寬度(ansi.StringWidth,全形字算 2、ANSI 不算)對齊、含粗體標題、
// 超出終端寬度時截斷加 …;非 TTY → 無標題 raw TSV(cut -f 友善),一個位元組都不改。
func Table(w io.Writer, tty bool, header []string, rows [][]string) {
	if !tty {
		for _, r := range rows {
			cells := make([]string, len(r))
			for i, c := range r {
				cells[i] = tsvEscaper.Replace(c)
			}
			fmt.Fprintln(w, strings.Join(cells, "\t"))
		}
		return
	}
	n := len(header) // 欄數 = 標題與最長的列取大者:列多出來的欄用空標題,絕不靜默丟資料(非 TTY 路徑本來就全印)
	for _, r := range rows {
		n = max(n, len(r))
	}
	widths := make([]int, n)
	measure := func(r []string) { // 量的必須是渲染的那個字串(跳脫後);\n 是零寬、換成的空白是 1
		for i, c := range r {
			if i >= n {
				break
			}
			if w := ansi.StringWidth(tsvEscaper.Replace(c)); w > widths[i] {
				widths[i] = w
			}
		}
	}
	measure(header)
	for _, r := range rows {
		measure(r)
	}
	fitWidths(widths, header, TermWidth())
	line := func(r []string, style func(string) string) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			c := ""
			if i < len(r) {
				c = ansi.Truncate(tsvEscaper.Replace(r[i]), widths[i], "…")
			}
			pad := widths[i] - ansi.StringWidth(c)
			if style != nil {
				c = style(c)
			}
			b.WriteString(c)
			if i < n-1 {
				b.WriteString(strings.Repeat(" ", pad+colGap))
			}
		}
		return strings.TrimRight(b.String(), " ")
	}
	fmt.Fprintln(w, line(header, func(s string) string { return boldStyle.Render(s) }))
	for _, r := range rows {
		fmt.Fprintln(w, line(r, nil))
	}
}

// fitWidths 把欄寬縮到 total 以內。順序寫死(UX 計畫 R2):標題為 ID 的欄先縮(最少留
// minIDWidth),再從最右欄往左,第一個非 ID 欄(曲名/名稱)最後。終端窄到連底線都放不下就放棄,
// 讓終端機自己折行。
//
// 假設(靠位置、不靠語意):「最該保留的欄 = 第一個標題不是 ID 的欄」。目前四張表都成立;之後若在
// 前面插一個窄欄(例如 # 或 平台),被保護的會變成那個窄欄——屆時把它改成明確指定,別靠這個推斷。
func fitWidths(widths []int, header []string, total int) {
	n := len(widths)
	if n == 0 {
		return
	}
	over := func() int {
		sum := (n - 1) * colGap
		for _, x := range widths {
			sum += x
		}
		return sum - total
	}
	shrink := func(i, floor int) {
		if o := over(); o > 0 && widths[i] > floor {
			widths[i] = max(widths[i]-o, floor)
		}
	}
	id, name := -1, -1
	for i, h := range header {
		if h == "ID" && id < 0 {
			id = i
		} else if name < 0 {
			name = i
		}
	}
	if id >= 0 {
		shrink(id, minIDWidth)
	}
	for i := n - 1; i >= 0; i-- {
		if i != id && i != name {
			shrink(i, minCellWidth)
		}
	}
	if name >= 0 {
		shrink(name, minCellWidth)
	}
}

// FormatDuration: ms → m:ss。
func FormatDuration(ms int) string {
	s := ms / 1000
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
