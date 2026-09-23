package ui

import (
	"bytes"
	"errors"
	"slices"
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
	render(40) // 連下限都放不下:盡力縮,剩下的交給終端機折行;仍然不截、ID 仍完整、碎片仍接得回去(render 裡驗)
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

// ID 欄永遠完整,在最後一欄也一樣;放不下時其餘欄盡力縮(名稱可以折),但不截。
func TestTableTTYIDNeverShrinks(t *testing.T) {
	withWidth(t, 40)
	buf := &bytes.Buffer{}
	id := strings.Repeat("f", 40)
	header := []string{"名稱", "類型", "ID"}
	Table(buf, true, header, [][]string{{"MacBook Pro", "Computer", id}})
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if !strings.Contains(lines[1], id) {
		t.Errorf("ID 要完整、在第一行:%q", buf.String())
	}
	starts := make([]int, len(header))
	for i, h := range header {
		starts[i] = cellOffset(t, lines[0], h)
	}
	if got := columnText(lines[1:], starts, 0); squash(got) != squash("MacBook Pro") {
		t.Errorf("名稱碎片接回去要等於原文:%q", got)
	}
}

// 原子欄(ID / *_ID / CID / PID / ISRC / DEVICE / REASON_CODE)在最寬的幾張表上也永遠不折;放不下時盡力縮,不會比
// 自然寬度更糟(PR #51 review:resolve 在 80 欄不可以從 84 退化成 107)。上限用測試自己算的尺:
// 原子欄自然寬 + 其餘欄 min(自然寬, 8) + 欄距。
func TestTableTTYAtomicColumnsAndWideTables(t *testing.T) {
	spotifyID := "3n3Ppam7vgaVa1iaRUc9Lp"
	cid := "c_01H8XQZ4K7"
	pull := []string{"ACTION", "PROVIDER", "PLAYLIST", "POS", "CID", "PROVIDER_ID", "TITLE", "ARTISTS", "REASON", "REASON_CODE"}
	pullRow := []string{"add", "spotify", "冬日暖調", "12", cid, spotifyID, "Mr. Brightside", "The Killers", "new in drive", "added_on_platform"}
	cases := []struct {
		name   string
		header []string
		row    []string
		atomic []int // 哪幾欄是原子欄(測試自己列,不問被測的函式)
	}{
		{"pull", pull, pullRow, []int{4, 5, 9}},
		{"sync", append([]string{"DIR"}, pull...), append([]string{"push"}, pullRow...), []int{5, 6, 10}},
		{"resolve", []string{"ACTION", "CID", "PROVIDER", "PROVIDER_ID", "CONFIDENCE", "SOURCE", "TITLE", "ARTISTS", "REASON", "REASON_CODE"},
			[]string{"review", cid, "spotify", spotifyID, "0.98", "isrc", "Mr. Brightside", "The Killers", "candidate already taken", "candidate_assigned"}, []int{1, 3, 9}},
		{"dedup", []string{"POS", "ID", "TITLE", "ARTISTS", "REASON", "REASON_CODE"},
			[]string{"3", spotifyID, "Mr. Brightside", "The Killers", "same ISRC as pos 0", "dup_isrc"}, []int{1, 5}},
		{"debug", []string{"ID", "TITLE", "ARTISTS", "ALBUM", "DURATION", "ISRC"},
			[]string{spotifyID, "Mr. Brightside", "The Killers", "Hot Fuss", "3:42", "USIR20400274"}, []int{0, 5}},
		{"drive", []string{"ID", "NAME", "KIND", "PID", "DEVICE", "VER", "MODIFIED"},
			[]string{"1AbCdEfGhIjKlMnOpQrStUvWxYz012345", "pl__p_01H8XQ.json", "playlist", "p_01H8XQ", "dev-9f8e7d6c5b4a", "3", "2026-09-14T06:29:23Z"}, []int{0, 3, 4}},
	}
	for _, c := range cases {
		isAtomic := map[int]bool{}
		for _, i := range c.atomic {
			isAtomic[i] = true
		}
		bound := 2 * (len(c.header) - 1)
		for i := range c.header {
			nat := max(ansi.StringWidth(c.header[i]), ansi.StringWidth(c.row[i]))
			if isAtomic[i] {
				bound += nat
			} else {
				bound += min(nat, 8)
			}
		}
		for _, width := range []int{80, 120} {
			withWidth(t, width)
			buf := &bytes.Buffer{}
			Table(buf, true, c.header, [][]string{c.row})
			out := strings.TrimRight(buf.String(), "\n")
			if strings.Contains(out, "…") {
				t.Errorf("%s 寬 %d:不可截斷:%q", c.name, width, out)
			}
			lines := strings.Split(out, "\n")
			for _, i := range c.atomic {
				if !strings.Contains(out, c.row[i]) {
					t.Errorf("%s 寬 %d:原子欄 %q 不可折:%q", c.name, width, c.row[i], out)
				}
			}
			limit := max(width, bound) // 放得下就要放得下;放不下也不可比「盡力縮」的上限寬
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > limit {
					t.Errorf("%s 寬 %d:第 %d 行寬 %d 超過 %d(盡力縮的上限):%q", c.name, width, i, w, limit, l)
				}
			}
		}
	}
	// 名字欄最後才縮:TITLE 在 PLAYLIST 之前縮的話,「Mr. Brightside」會被折而 PLAYLIST 完整
	row := []string{"add", "spotify", "上班路上聽的長清單名稱", "12", cid, spotifyID, "Mr. Brightside", "Sycco", "isrc"}
	withWidth(t, 108) // 自然寬 116,超 8:REASON / ARTISTS 已短於下限,PLAYLIST 要吸收
	buf := &bytes.Buffer{}
	Table(buf, true, pull[:9], [][]string{row}) // 不含 REASON_CODE:這裡量的是名字欄的縮放順序
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if !strings.Contains(lines[1], "Mr. Brightside") || strings.Contains(lines[1], "上班路上聽的長清單名稱") {
		t.Errorf("TITLE 要最後才縮、PLAYLIST 先折:%q", lines)
	}
}

// 表頭折行時右邊是空儲存格:不可拖著補白與「染成粗體的空字串」(PR #51 review)。
func TestTableTTYHeaderLinesNoTrailingPadding(t *testing.T) {
	withWidth(t, 80)
	buf := &bytes.Buffer{}
	header := []string{"ACTION", "CID", "PROVIDER", "PROVIDER_ID", "CONFIDENCE", "SOURCE", "TITLE", "ARTISTS", "REASON"}
	Table(buf, true, header, [][]string{{"link", "c_01H8XQZ4K7", "spotify", "3n3Ppam7vgaVa1iaRUc9Lp", "0.98", "isrc", "Mr. Brightside", "The Killers", "isrc exact"}})
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if strings.TrimSpace(ansi.Strip(lines[1])) != "CE" { // CONFIDENCE(10)不是原子欄,縮到 8 折成 CONFIDEN / CE
		t.Fatalf("前置條件:CONFIDENCE 在 80 欄要折成兩行:%q", lines)
	}
	for i := 0; i < 2; i++ {
		plain := ansi.Strip(lines[i])
		if strings.TrimRight(plain, " ") != plain || strings.Contains(lines[i], "\x1b[1m\x1b[m") {
			t.Errorf("表頭第 %d 行不可拖著補白或空的粗體:%q", i, lines[i])
		}
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

// tableSink:實作 TableWriter 的 writer(P7 web 模式的頁面端)。
type tableSink struct {
	bytes.Buffer
	header []string
	rows   [][]string
	calls  int
	err    error
}

func (s *tableSink) WriteTable(header []string, rows [][]string) error {
	s.calls++
	s.header, s.rows = header, rows
	return s.err
}

// TestTableWriterBypassesTSV:writer 實作 TableWriter → 整張表(含標題、儲存格不跳脫)原樣交給它,
// 底層 buffer 零位元組;tty 與非 TTY 一樣;它回的 error 原樣往上。不實作的 writer 走既有路徑
// (TestTableNonTTYIsRawTSV 與 TTY 系列不改而過 = 零行為變更)。
func TestTableWriterBypassesTSV(t *testing.T) {
	for _, tty := range []bool{false, true} {
		s := &tableSink{}
		if err := Table(s, tty, []string{"A", "B"}, [][]string{{"1", "x\ty"}, {"2", "y"}}); err != nil {
			t.Fatalf("tty=%v: %v", tty, err)
		}
		if s.calls != 1 || len(s.header) != 2 || s.header[0] != "A" || len(s.rows) != 2 || s.rows[0][1] != "x\ty" {
			t.Errorf("tty=%v:應原樣收到 header 與 rows,得到 calls=%d header=%q rows=%q", tty, s.calls, s.header, s.rows)
		}
		if s.Len() != 0 {
			t.Errorf("tty=%v:實作 TableWriter 的 writer 不該收到任何位元組,得到 %q", tty, s.String())
		}
	}
	// 欄數調和:列比 header 長時 header 補空標題(TTY 路徑的不變式,頁面端才不會照 header 畫 <td> 而吃掉多出來的欄)。
	long := &tableSink{}
	if err := Table(long, false, []string{"A"}, [][]string{{"1", "2", "3"}, {"x"}}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"A", "", ""}; !slices.Equal(long.header, want) || len(long.rows[0]) != 3 {
		t.Errorf("header 要補到最長列的欄數:header=%q rows=%q", long.header, long.rows)
	}
	s := &tableSink{err: errors.New("頁面已關閉")}
	if err := Table(s, false, []string{"A"}, [][]string{{"1"}}); err == nil || err.Error() != "頁面已關閉" {
		t.Errorf("WriteTable 的 error 要原樣回傳,得到 %v", err)
	}
}
