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

func TestTableTTYShrinksIDFirstThenRightToLeft(t *testing.T) {
	// 欄寬:ID 22、曲名 28、藝人 6、專輯 32、時長 4,欄距 8 → 全寬 100。
	id := strings.Repeat("a", 22)
	title := "很長很長的歌名很長很長的歌名"
	album := strings.Repeat("專輯", 8)
	row := [][]string{{id, title, "五月天", album, "3:47"}}
	header := []string{"ID", "曲名", "藝人", "專輯", "時長"}
	render := func(width int) string {
		withWidth(t, width)
		buf := &bytes.Buffer{}
		Table(buf, true, header, row)
		line := strings.Split(buf.String(), "\n")[1]
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("寬 %d:行寬 %d 超過終端:%q", width, w, line)
		}
		return line
	}

	line := render(87) // 超 13:ID 22→9 剛好吸收,其他欄完整
	if !strings.Contains(line, "aaaaaaaa…  "+title) || !strings.Contains(line, album) {
		t.Errorf("寬 87:只有 ID 該縮成 8 字 + …:%q", line)
	}
	line = render(60) // ID 縮到底仍超 27:最右的時長已在底線,專輯 32→5;藝人與曲名完整
	if !strings.Contains(line, title) || !strings.Contains(line, "五月天") || strings.Contains(line, album) {
		t.Errorf("寬 60:專輯應先被截、曲名與藝人完整:%q", line)
	}
	line = render(48) // 其他欄全到底線,曲名才被縮
	if strings.Contains(line, title) || !strings.Contains(line, "很長很長的歌名") {
		t.Errorf("寬 48:曲名應最後才縮、且仍保留開頭:%q", line)
	}
}

func TestTableTTYIDNotFirstColumn(t *testing.T) {
	withWidth(t, 40)
	buf := &bytes.Buffer{}
	Table(buf, true, []string{"名稱", "類型", "ID"}, [][]string{{"MacBook Pro", "Computer", strings.Repeat("f", 40)}})
	line := strings.Split(buf.String(), "\n")[1]
	if !strings.Contains(line, "MacBook Pro") || !strings.Contains(line, "ffffffff…") {
		t.Errorf("ID 在最後一欄也應先縮、名稱保留:%q", line)
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
