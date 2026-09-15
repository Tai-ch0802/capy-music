package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTableNonTTYIsRawTSV(t *testing.T) {
	buf := &bytes.Buffer{}
	Table(buf, false, []string{"A", "B"}, [][]string{{"1", "x"}, {"2", "y"}})
	got := buf.String()
	if got != "1\tx\n2\ty\n" {
		t.Errorf("非 TTY 應為無標題 raw TSV,得到 %q", got)
	}

	buf.Reset()
	Table(buf, false, nil, [][]string{{"a\tb\nc", "d"}})
	if got := buf.String(); got != "a b c\td\n" {
		t.Errorf("儲存格內嵌 tab/newline 應被替換為空白且單列多欄仍為單行,得到 %q", got)
	}
}

func TestTableTTYHasHeaderAndAlignment(t *testing.T) {
	buf := &bytes.Buffer{}
	Table(buf, true, []string{"A", "B"}, [][]string{{"longcell", "x"}})
	got := buf.String()
	if !strings.Contains(got, "A") || !strings.Contains(got, "longcell") {
		t.Errorf("TTY 表格缺內容:%q", got)
	}
	if strings.Contains(got, "\tx") {
		t.Errorf("TTY 應經 tabwriter 對齊(不殘留原始 tab):%q", got)
	}
}

func TestBoldPassthroughNonTTY(t *testing.T) {
	if Bold(false, "hi") != "hi" {
		t.Error("非 TTY 不得帶樣式")
	}
	if !strings.Contains(Bold(true, "hi"), "hi") {
		t.Error("TTY 樣式仍須含原文字")
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[int]string{227000: "3:47", 61000: "1:01", 0: "0:00", 599999: "9:59"}
	for ms, want := range cases {
		if got := FormatDuration(ms); got != want {
			t.Errorf("FormatDuration(%d) = %q, want %q", ms, got, want)
		}
	}
}

// cellOffset:第 col 欄在該行的顯示起點(以 ansi.StringWidth 量,不用 len)。
func cellOffset(t *testing.T, line, cell string) int {
	t.Helper()
	i := strings.Index(line, cell)
	if i < 0 {
		t.Fatalf("行 %q 找不到儲存格 %q", line, cell)
	}
	return ansi.StringWidth(line[:i])
}

func withWidth(t *testing.T, w int) {
	t.Helper()
	orig := TermWidth
	TermWidth = func() int { return w }
	t.Cleanup(func() { TermWidth = orig })
}

func TestTableTTYAlignsCJKAndBold(t *testing.T) {
	withWidth(t, 120)
	buf := &bytes.Buffer{}
	Table(buf, true, []string{"ID", "曲名", "藝人"}, [][]string{
		{"1", "派對動物", "五月天"},
		{"22", "Party Animal", "Mayday"},
		{"333", Bold(true, "粗體"), "x"},
		{"4444", "a\nb\r\tc", "y"}, // 控制字元換成空白後才量寬
	})
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("應為標題 + 4 列,得到 %q", buf.String())
	}
	want := cellOffset(t, lines[1], "五月天")
	for i, cell := range map[int]string{0: "藝人", 2: "Mayday", 3: "x", 4: "y"} {
		if got := cellOffset(t, lines[i], cell); got != want {
			t.Errorf("第 %d 行第 3 欄起點 %d,應與全形列一致 %d:\n%s", i, got, want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "\t") {
		t.Error("TTY 表格不得殘留 tab")
	}
}

// sliceByWidth:取一行裡顯示欄位 [from, to) 的文字(跳脫碼先剝掉;全形字算 2)。測試自己的尺。
func sliceByWidth(line string, from, to int) string {
	var b strings.Builder
	col := 0
	for _, r := range ansi.Strip(line) {
		w := ansi.StringWidth(string(r))
		if col >= from && col < to {
			b.WriteRune(r)
		}
		col += w
	}
	return b.String()
}

// columnText:把某一欄在這幾行裡的碎片接回去(去掉補白),starts 是各欄在表頭的起點。
func columnText(lines []string, starts []int, col int) string {
	to := 1 << 20
	if col+1 < len(starts) {
		to = starts[col+1]
	}
	var parts []string
	for _, l := range lines {
		if s := strings.TrimSpace(sliceByWidth(l, starts[col], to)); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "")
}

func squash(s string) string { return strings.ReplaceAll(s, " ", "") }

// 放不下時儲存格換行、不截斷:沒有 …、每一行不超過終端寬、ID 完整、每欄的碎片接回去等於原文
// (去空白比:英文在字邊界斷會吃掉那個空白)。縮的順序:專輯(最右的文字欄)先、藝人次之、曲名最後。
func TestTableTTYWrapsInsteadOfTruncating(t *testing.T) {
	// 欄寬:ID 22、曲名 28、藝人 22、專輯 32、時長 4,欄距 8 → 全寬 116。
	id := strings.Repeat("a", 22)
	title := "很長很長的歌名很長很長的歌名"
	artist := "Billie Eilish & Khalid"
	album := strings.Repeat("專輯", 8)
	header := []string{"ID", "曲名", "藝人", "專輯", "時長"}
	row := []string{id, title, artist, album, "3:47"}
	render := func(width int) []string {
		t.Helper()
		withWidth(t, width)
		buf := &bytes.Buffer{}
		Table(buf, true, header, [][]string{row})
		out := strings.TrimRight(buf.String(), "\n")
		if strings.Contains(out, "…") {
			t.Errorf("寬 %d:不可截斷:%q", width, out)
		}
		lines := strings.Split(out, "\n")
		if !strings.Contains(lines[1], id) {
			t.Errorf("寬 %d:ID 要完整、不換行:%q", width, lines[1])
		}
		starts := make([]int, len(header))
		for i, h := range header {
			starts[i] = cellOffset(t, lines[0], h)
		}
		for col, want := range row {
			if got := columnText(lines[1:], starts, col); squash(got) != squash(want) {
				t.Errorf("寬 %d 第 %d 欄碎片接回去不等於原文:%q,要 %q\n%s", width, col, got, want, out)
			}
		}
		return lines[1:]
	}
	fits := func(width int, lines []string) {
		t.Helper()
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("寬 %d:第 %d 行寬 %d 超過終端:%q", width, i, w, l)
			}
		}
	}
	lines := render(120) // 放得下:一列一行
	fits(120, lines)
	if len(lines) != 1 {
		t.Errorf("寬 120 放得下就不該換行:%q", lines)
	}
	lines = render(100) // 超 16:只有專輯縮(32→16),折成兩行;曲名與藝人整個在第一行
	fits(100, lines)
	if len(lines) != 2 || !strings.Contains(lines[0], title) || !strings.Contains(lines[0], artist) {
		t.Errorf("寬 100:只有專輯該換行、曲名與藝人在第一行:%q", lines)
	}
	lines = render(60) // 第一輪縮到 12 還超,第二輪縮到 8 才放得下:仍然放得下、仍然完整
	fits(60, lines)
	lines = render(40) // 連下限都放不下:放棄 —— 欄寬全部還原、一列一行讓終端機折行,不截也不半縮
	if len(lines) != 1 || !strings.Contains(lines[0], title) || !strings.Contains(lines[0], album) {
		t.Errorf("寬 40 放不下就該整列原樣印出:%q", lines)
	}
}

// ansi.Wrap 把連字號當斷點,「字 -」會黏在上一行而超出欄寬("The Question - Single" 在 12 欄折成 14 欄),
// 超出的行要再硬斷:每一行都不可超過終端寬,碎片仍要接得回原文。
func TestTableTTYWrapNeverExceedsWidth(t *testing.T) {
	header := []string{"ID", "曲名", "藝人", "專輯", "時長"}
	for _, album := range []string{"The Question - Single", "Kiss All The Time. Disco, Occasionally.", "ab - cd - ef - gh"} {
		row := []string{strings.Repeat("a", 22), "很長很長的歌名很長很長的歌名", "Billie Eilish & Khalid", album, "3:47"}
		withWidth(t, 60) // 專輯欄會被縮到下限
		buf := &bytes.Buffer{}
		Table(buf, true, header, [][]string{row})
		lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > 60 {
				t.Errorf("%q 第 %d 行寬 %d 超過終端:%q", album, i, w, l)
			}
		}
		starts := make([]int, len(header))
		for i, h := range header {
			starts[i] = cellOffset(t, lines[0], h)
		}
		if got := columnText(lines[1:], starts, 3); squash(got) != squash(album) {
			t.Errorf("專輯碎片接回去不等於原文:%q,要 %q", got, album)
		}
	}
}

// ID 欄永遠完整,在最後一欄也一樣。放不下就放棄,不截、不半縮(名稱不該被切成兩行)。
func TestTableTTYIDNeverShrinks(t *testing.T) {
	withWidth(t, 40)
	buf := &bytes.Buffer{}
	id := strings.Repeat("f", 40)
	Table(buf, true, []string{"名稱", "類型", "ID"}, [][]string{{"MacBook Pro", "Computer", id}})
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "MacBook Pro") || !strings.Contains(lines[1], id) {
		t.Errorf("ID 與名稱都要完整、在同一行:%q", buf.String())
	}
}

func TestTableTTYRowsLongerThanHeaderAndNoHeader(t *testing.T) {
	withWidth(t, 80)
	buf := &bytes.Buffer{}
	Table(buf, true, []string{"A", "B"}, [][]string{{"1", "2", "3"}})
	if line := strings.Split(buf.String(), "\n")[1]; !strings.Contains(line, "3") {
		t.Errorf("列比標題長時多出來的欄不得靜默丟掉:%q", buf.String())
	}
	buf.Reset()
	Table(buf, true, nil, [][]string{{"a", "b"}})
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "a") || !strings.Contains(lines[1], "b") {
		t.Errorf("無標題時列仍要印出來(標題列為空):%q", buf.String())
	}
}
