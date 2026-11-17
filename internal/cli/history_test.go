package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

type captureTableWriter struct {
	header []string
	rows   [][]string
}

func (w *captureTableWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *captureTableWriter) WriteTable(h []string, r [][]string) error {
	w.header, w.rows = h, r
	return nil
}

func TestHistoryReadOnlyNoDBDoesNotCreateFile(t *testing.T) {
	setCLITestConfig(t)
	dbPath, err := store.Path()
	if err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "history")
	if err != nil || out != "" {
		t.Fatalf("無 state.db 時 history 應輸出空字串且 exit 0:%v %q", err, out)
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Fatalf("唯讀查詢不得建立 state.db:%v", statErr)
	}
}

func TestHistoryListPlayJournalTSVLimitAndClear(t *testing.T) {
	setCLITestConfig(t)
	st, err := store.Open(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t1 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 17, 10, 20, 0, 0, time.UTC)
	t3 := time.Date(2026, 11, 17, 10, 40, 0, 0, time.UTC)
	for _, e := range []store.PlayJournalEntry{
		{PlayedAt: t1, Provider: "spotify", ProviderID: "sp1", Title: "知足", Artists: []string{"五月天"}, Album: "知足最真傑作選"},
		{PlayedAt: t2, Provider: "apple", ProviderID: "ap2", Title: "Stubborn", Artists: []string{"Mayday", "Guest"}, Album: "Poetry"},
		{PlayedAt: t3, Provider: "spotify", ProviderID: "sp3", Title: "派對動物", Artists: []string{"五月天"}, Album: "自傳"},
	} {
		if err := st.RecordPlayJournal(e); err != nil {
			st.Close()
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "history")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("應有 3 列 TSV:%q", out)
	}
	if lines[0] != "2026-11-17T10:40:00Z\tspotify\tsp3\t派對動物\t五月天\t自傳" {
		t.Errorf("最新一筆應在最前(6 欄 TSV):%q", lines[0])
	}
	if lines[1] != "2026-11-17T10:20:00Z\tapple\tap2\tStubborn\tMayday, Guest\tPoetry" {
		t.Errorf("第二筆:%q", lines[1])
	}

	outLim, err := runCLI(t, "history", "list", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimRight(outLim, "\n"), "\n"); len(got) != 2 || got[0] != lines[0] || got[1] != lines[1] {
		t.Errorf("history list --limit 2:%q", outLim)
	}

	if _, err := runCLI(t, "history", "--limit", "0"); err == nil || !strings.Contains(err.Error(), "--limit") {
		t.Errorf("非法 --limit 應報錯:%v", err)
	}

	// TableWriter(--web 表格接縫)應拿到固定英文 6 欄表頭
	root := newRootCmd()
	tw := &captureTableWriter{}
	root.SetOut(tw)
	root.SetArgs([]string{"history", "--limit", "1"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(tw.header, ",") != "PLAYED_AT,PROVIDER,ID,TITLE,ARTISTS,ALBUM" || len(tw.rows) != 1 {
		t.Errorf("TableWriter header=%v rows=%v", tw.header, tw.rows)
	}

	// history clear 100% 相容並一併清除 play_journal
	clearOut, err := runCLI(t, "history", "clear")
	if err != nil || !strings.Contains(clearOut, "3 筆") {
		t.Fatalf("history clear 應回報清除筆數:%v %q", err, clearOut)
	}
	after, err := runCLI(t, "history")
	if err != nil || after != "" {
		t.Fatalf("clear 後應為空:%v %q", err, after)
	}
}

func TestHistoryFallbackToRecentAndEnglishI18n(t *testing.T) {
	setCLITestConfig(t)
	c := cache.Load()
	c.AddRecent(cache.Recent{At: 1_760_000_000, Provider: "spotify", Type: cache.TypeQuery, ID: "q1", Label: "q1"})
	c.AddRecent(cache.Recent{At: 1_760_000_060, Provider: "spotify", Type: cache.TypeTrack, ID: "t1", Label: "派對動物", Detail: "五月天 · 自傳"})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "history")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Unix(1_760_000_060, 0).UTC().Format(time.RFC3339) + "\tspotify\tt1\t派對動物\t五月天\t自傳\n"
	if out != want {
		t.Fatalf("play_journal 為空時退回 recent(跳過 query):got %q, want %q", out, want)
	}

	withLanguage(t, "en")
	for _, args := range [][]string{{"history", "--help"}, {"history", "list", "--help"}} {
		h, err := runCLI(t, args...)
		if err != nil || hasCJK(h) {
			t.Errorf("%v 英文模式不該有 CJK:%v %q", args, err, h)
		}
	}
	if _, err := runCLI(t, "history", "--limit", "-3"); err == nil || hasCJK(err.Error()) {
		t.Errorf("英文錯誤訊息不該有 CJK:%v", err)
	}
}
