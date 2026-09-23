// Package ui: 輸出層。鐵則: 非 TTY(pipe/cron)一律純文字——可腳本化是核心價值(spec §8.5.6)。
package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

// ErrInterrupted:使用者在檢視窗格裡按了 Ctrl-C。命令要跟著結束(exit 130,同 SIGINT),
// 不可再往下問確認、寫入。
var ErrInterrupted = i18n.Errorf("ui.err.interrupted")

// TableOption:Table 的選項。
type TableOption func(*tableConfig)

type tableConfig struct{ noPager bool }

// NoPager:這次不開檢視窗格。給 --yes 的命令用:README 說 --yes「只跳過確認」,它從頭到尾不該碰鍵盤,
// 而且 pl pull 的表是握著 pull.lock 印的,窗格開多久鎖就握多久。
func NoPager(c *tableConfig) { c.noPager = true }

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

// TableWriter:選用介面(io.StringWriter 式)。writer 若實作它,Table 把整張表(含標題)交給它,不走 TSV / 對齊
// (P7 web 模式用:頁面自己渲染表格);終端機的 tty / 非 TTY 兩條既有路徑一個位元組不改。header 已調和到最長列的
// 欄數(多出來的欄用空標題,同 TTY 路徑的不變式:絕不靜默丟資料);儲存格原樣、不跳脫;TableOption 只影響呈現,對它無意義。
type TableWriter interface {
	WriteTable(header []string, rows [][]string) error
}

// Table: TTY → 依顯示寬度(ansi.StringWidth,全形字算 2、ANSI 不算)對齊、含粗體標題;放不下時
// 儲存格**換行、不截斷**(資訊要給完整,不要給一半;原子欄永遠完整,見 fitWidths)。表頭跟資料列
// 一視同仁:欄名比縮過的欄寬長也會折(PROVIDER_ID 在 80 欄折成 PROVIDER / _ID)。比終端機寬時先開
// 檢視窗格(pager.go),看完再印換行版;窗格裡按 Ctrl-C 回 ErrInterrupted,什麼都不印。
// 非 TTY → 無標題 raw TSV(cut -f 友善),一個位元組都不改。
func Table(w io.Writer, tty bool, header []string, rows [][]string, opts ...TableOption) error {
	if tw, ok := w.(TableWriter); ok {
		n := len(header)
		for _, r := range rows {
			n = max(n, len(r))
		}
		if n > len(header) { // 同下方 TTY 路徑:列多出來的欄用空標題,絕不靜默丟資料
			padded := make([]string, n)
			copy(padded, header)
			header = padded
		}
		return tw.WriteTable(header, rows)
	}
	var cfg tableConfig
	for _, o := range opts {
		o(&cfg)
	}
	if !tty {
		for _, r := range rows {
			cells := make([]string, len(r))
			for i, c := range r {
				cells[i] = tsvEscaper.Replace(c)
			}
			fmt.Fprintln(w, strings.Join(cells, "\t"))
		}
		return nil
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
	// wrapCell:超過欄寬就換行。ansi.Wrap 英文在字邊界斷、CJK 逐字斷、跳脫碼保留(儲存格帶樣式又換了行,
	// 樣式會延續到同一行後面的補白 —— 目前沒有呼叫端在儲存格上樣式,標題的粗體是這裡自己套的)。
	// ansi.Wrap 把連字號當斷點,卻會讓「字 -」黏在上一行而超出欄寬("The Question - Single" 在 12 欄
	// 折成 14 欄的 "The Question -"),超出的行再硬斷一次,欄寬才守得住。
	wrapCell := func(c string, width int) []string {
		c = tsvEscaper.Replace(c)
		if ansi.StringWidth(c) <= width {
			return []string{c}
		}
		if width < 2 {
			width = 2 // 全形字切不進 1 欄,硬斷也救不了;現在的下限到不了這裡,守著免得哪天調低了靜靜破功
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
				if style != nil && c != "" { // 空儲存格不套:染成粗體的空字串會讓 TrimRight 修不到尾端補白
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
	// 比終端機寬:先開檢視窗格(要有鍵盤、沒被關掉),每列一行、自然寬度;看完再把換行版印進捲動區當紀錄。
	// 窗格開不起來就直接印,不擋輸出;使用者按 Ctrl-C 就是中止,紀錄也不印。
	if natural := over(widths, TermWidth()); natural > 0 && len(rows) > 0 && !cfg.noPager && PagerEnabled() && StdinIsTTY() {
		body := make([]string, 0, len(rows))
		for _, r := range rows {
			body = append(body, lines(r, nil)...) // 還沒縮欄,每列剛好一行
		}
		if err := Pager(w, lines(header, nil)[0], body); errors.Is(err, tea.ErrInterrupted) {
			return ErrInterrupted
		}
	}
	fitWidths(widths, header, TermWidth())
	for _, l := range lines(header, func(s string) string { return boldStyle.Render(s) }) {
		fmt.Fprintln(w, l)
	}
	for _, r := range rows {
		for _, l := range lines(r, nil) {
			fmt.Fprintln(w, l)
		}
	}
	return nil
}

// atomicHeader:這一欄是原子值 —— ID 之類要複製去下一個命令的字串,永遠不縮、不折(折成三段夾在補白裡,
// 使用者會以為複製到的是完整值,比看得出被截的 … 更糟)。靠標題名判斷,規則集中在這裡:標題都是這個 repo
// 自己定的,加新表時對照這條;改成呼叫端逐一宣告要動 12 個呼叫點,換來的只是同一份知識散在各處
// (PR #51 review 的取捨)。
func atomicHeader(h string) bool {
	switch h {
	case "ID", "CID", "PID", "ISRC", "DEVICE":
		return true
	}
	return strings.HasSuffix(h, "_ID")
}

// keepHeader:最該保留寬度的欄,最後才縮 —— 曲目 / 清單的名字(UX 計畫 R2)。
func keepHeader(h string) bool {
	switch h {
	case "TITLE", "NAME":
		return true
	}
	return false
}

// fitWidths 把欄寬縮到 total 以內;縮過的儲存格由 Table 換行,不截斷。原子欄(atomicHeader)不縮;
// 其餘從最右欄往左,名字欄(keepHeader;沒有就第一個非原子欄)最後。下限先用 minCellWidth,還放不下
// 再以 tightCellWidth 縮一輪;還是放不下就**盡力而為** —— 留著縮過的欄寬,剩下的超寬交給終端機折行。
// 全部還原成自然寬度的話,差幾欄會變成差幾十欄,整張表都被軟折行毀掉(PR #51 review:resolve 在 80 欄
// 從 84 變 107)。9–10 欄又帶 22 字 Spotify ID 的 pull / sync / resolve 在 80 欄本來就放不下,那是分頁器的事。
func fitWidths(widths []int, header []string, total int) {
	n := len(widths)
	if n == 0 {
		return
	}
	excess := func() int { return over(widths, total) } // 別叫 over:短變數宣告裡 body 看得到外層那個,改成 var 就是無窮遞迴
	atomic := make([]bool, n)
	keep := -1
	for i, h := range header {
		atomic[i] = atomicHeader(h)
		if keep < 0 && keepHeader(h) {
			keep = i
		}
	}
	if keep < 0 {
		for i := range header {
			if !atomic[i] {
				keep = i
				break
			}
		}
	}
	for _, floor := range []int{minCellWidth, tightCellWidth} {
		shrink := func(i int) {
			if o := excess(); o > 0 && widths[i] > floor {
				widths[i] = max(widths[i]-o, floor)
			}
		}
		for i := n - 1; i >= 0; i-- {
			if !atomic[i] && i != keep {
				shrink(i)
			}
		}
		if keep >= 0 {
			shrink(keep)
		}
		if excess() <= 0 {
			return
		}
	}
}

// over:這組欄寬(含欄距)比 total 寬多少;≤ 0 就是放得下。
func over(widths []int, total int) int {
	sum := (len(widths) - 1) * colGap
	for _, x := range widths {
		sum += x
	}
	return sum - total
}

// FormatDuration: ms → m:ss。
func FormatDuration(ms int) string {
	s := ms / 1000
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
