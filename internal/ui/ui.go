// Package ui: 輸出層。鐵則: 非 TTY(pipe/cron)一律純文字——可腳本化是核心價值(spec §8.5.6)。
package ui

import (
	"fmt"
	"io"
	"os"
	"slices"
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
	colGap         = 2
	minCellWidth   = 12 // 縮欄的下限:一行放得下一個短單字,換行後還讀得順
	tightCellWidth = 8  // 以 minCellWidth 縮完還放不下,再以它縮一輪;還是放不下就放棄,讓終端機自己折行
)

// Table: TTY → 依顯示寬度(ansi.StringWidth,全形字算 2、ANSI 不算)對齊、含粗體標題;放不下時
// 儲存格**換行、不截斷**(資訊要給完整,不要給一半;ID 欄永遠完整,見 fitWidths);
// 非 TTY → 無標題 raw TSV(cut -f 友善),一個位元組都不改。
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
	// wrapCell:超過欄寬就換行。ansi.Wrap 英文在字邊界斷、CJK 逐字斷、跳脫碼保留(儲存格帶樣式又換了行,
	// 樣式會延續到同一行後面的補白 —— 目前沒有呼叫端在儲存格上樣式,標題的粗體是這裡自己套的)。
	// ansi.Wrap 把連字號當斷點,卻會讓「字 -」黏在上一行而超出欄寬("The Question - Single" 在 12 欄
	// 折成 14 欄的 "The Question -"),超出的行再硬斷一次,欄寬才守得住。
	wrapCell := func(c string, width int) []string {
		c = tsvEscaper.Replace(c)
		if ansi.StringWidth(c) <= width {
			return []string{c}
		}
		var out []string
		for _, l := range strings.Split(ansi.Wrap(c, width, ""), "\n") {
			if ansi.StringWidth(l) > width {
				out = append(out, strings.Split(ansi.Hardwrap(l, width, false), "\n")...)
				continue
			}
			out = append(out, l)
		}
		return out
	}
	// lines:一列展開成幾行 —— 各欄行數的最大值;每行各欄補到欄寬,同一列的各欄才對得齊。
	lines := func(r []string, style func(string) string) []string {
		cells := make([][]string, n)
		h := 1
		for i := range cells {
			cells[i] = []string{""}
			if i < len(r) {
				cells[i] = wrapCell(r[i], widths[i])
			}
			h = max(h, len(cells[i]))
		}
		out := make([]string, h)
		for k := range out {
			var b strings.Builder
			for i := 0; i < n; i++ {
				c := ""
				if k < len(cells[i]) {
					c = cells[i][k]
				}
				pad := max(0, widths[i]-ansi.StringWidth(c))
				if style != nil {
					c = style(c)
				}
				b.WriteString(c)
				if i < n-1 {
					b.WriteString(strings.Repeat(" ", pad+colGap))
				}
			}
			out[k] = strings.TrimRight(b.String(), " ")
		}
		return out
	}
	for _, l := range lines(header, func(s string) string { return boldStyle.Render(s) }) {
		fmt.Fprintln(w, l)
	}
	for _, r := range rows {
		for _, l := range lines(r, nil) {
			fmt.Fprintln(w, l)
		}
	}
}

// fitWidths 把欄寬縮到 total 以內;縮過的儲存格由 Table 換行,不截斷。**ID 欄不縮**:ID 是要複製去
// 下一個命令的原子字串,斷成兩行沒意義。其餘順序照舊(UX 計畫 R2):從最右欄往左,第一個非 ID 欄
// (曲名/名稱)最後。下限先用 minCellWidth,還放不下再以 tightCellWidth 縮一輪;還是放不下就放棄 ——
// 欄寬全部還原,讓終端機自己折行(半縮的表格既換了行又超寬,兩邊的壞處都吃到;截掉才是丟資料)。
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
	id, name := -1, -1
	for i, h := range header {
		if h == "ID" && id < 0 {
			id = i
		} else if name < 0 {
			name = i
		}
	}
	orig := slices.Clone(widths)
	for _, floor := range []int{minCellWidth, tightCellWidth} {
		shrink := func(i int) {
			if o := over(); o > 0 && widths[i] > floor {
				widths[i] = max(widths[i]-o, floor)
			}
		}
		for i := n - 1; i >= 0; i-- {
			if i != id && i != name {
				shrink(i)
			}
		}
		if name >= 0 {
			shrink(name)
		}
		if over() <= 0 {
			return
		}
	}
	copy(widths, orig) // 放棄:別留下半縮的表格
}

// FormatDuration: ms → m:ss。
func FormatDuration(ms int) string {
	s := ms / 1000
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
