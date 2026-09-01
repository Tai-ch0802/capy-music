package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestTableNonTTYIsRawTSV(t *testing.T) {
	buf := &bytes.Buffer{}
	Table(buf, false, []string{"A", "B"}, [][]string{{"1", "x"}, {"2", "y"}})
	got := buf.String()
	if got != "1\tx\n2\ty\n" {
		t.Errorf("非 TTY 應為無標題 raw TSV,得到 %q", got)
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
