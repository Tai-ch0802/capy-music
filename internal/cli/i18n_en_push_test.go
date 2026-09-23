package cli

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Tai-ch0802/capy-music/internal/drive"
)

// 英文模式:push.go / sync.go 自己產生的字(T2b push)。清單名一律 ASCII;別組的字(canon 的 skip 理由、resolveHint、
// Drive / provider 的錯誤、pull 半邊的閾值句)這一輪可能還是中文,所以只在純 push.go 的路徑上驗「沒有中文」,其餘驗整句英文。

// enPushWorld:Commute = spotify:p1 [ids…],已連結、pull 過(有 base)。
func enPushWorld(t *testing.T, ids ...string) (*fakeSpotify, *drive.Client) {
	t.Helper()
	withLanguage(t, "en")
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "Commute", ids...)
	mustPull(t, "pl", "link", "Commute", "spotify:p1")
	mustPull(t, "pl", "pull", "Commute", "--yes")
	return fs, dc
}

func TestEnglishPushSyncHelp(t *testing.T) {
	withLanguage(t, "en")
	for _, c := range []*cobra.Command{newPlPushCmd(), newPlSyncCmd()} {
		texts := []string{c.Short, c.Long}
		c.Flags().VisitAll(func(f *pflag.Flag) { texts = append(texts, f.Usage) })
		for _, s := range texts {
			if hasCJK(s) {
				t.Errorf("pl %s 的說明有中文:%q", c.Name(), s)
			}
		}
	}
	if s := newPlPushCmd().Short; s != "Master copy → platforms: list the changes, write only after you confirm (spec §6.5.2)" {
		t.Errorf("push Short:%q", s)
	}
	if s := newPlSyncCmd().Flags().Lookup("force").Usage; !strings.HasPrefix(s, "override the removal threshold (checked separately for pull and push)") {
		t.Errorf("sync --force:%q", s)
	}
}

// 參數錯誤:兩句都只來自 push.go,整句英文、沒有中文。
func TestEnglishPushSyncArgErrors(t *testing.T) {
	enPushWorld(t, "a")
	for _, verb := range []string{"push", "sync"} {
		if _, _, err := runPull(t, "pl", verb, "Commute", "--provider", "tidal"); err == nil || err.Error() != `provider must be spotify|apple|local: "tidal"` {
			t.Errorf("pl %s --provider tidal:%v", verb, err)
		}
		want := "--force works with a single playlist only (capy pl " + verb + " <name> --force), not with --all"
		if _, _, err := runPull(t, "pl", verb, "--all", "--force"); err == nil || err.Error() != want {
			t.Errorf("pl %s --all --force:%v", verb, err)
		}
	}
}

// 挑選器標題:pullTargets(pull.go,別組)把呼叫端翻好的動詞填進「Pick a playlist to {verb}」;這裡只驗 push / sync 傳的動詞,
// 繁中整句逐字不變。
func TestPushSyncPickerTitles(t *testing.T) {
	enPushWorld(t, "a")
	for _, c := range []struct{ lang, verb, want string }{
		{"en", "push", "push"}, // 標題的其餘部分是 pull.go 的(別組):只驗結尾是 push 傳的動詞
		{"en", "sync", "sync"},
		{"zh-TW", "push", "選一個清單來推"},
		{"zh-TW", "sync", "選一個清單來同步"},
	} {
		withLanguage(t, c.lang)
		log := stubPickers(t, -1)
		runPull(t, "pl", c.verb)
		if len(log.titles) != 1 || !strings.HasSuffix(log.titles[0], c.want) || (c.lang == "zh-TW" && log.titles[0] != c.want) {
			t.Errorf("%s pl %s 的挑選器標題:%q", c.lang, c.verb, log.titles)
		}
	}
}

// 主流程:四種列的 REASON 英文(REASON_CODE 不變)、推送與無變更的收尾句。
func TestEnglishPushFlowReasonsAndCounts(t *testing.T) {
	fs, dc := enPushWorld(t, "a", "b", "c")
	editCanonical(t, dc, drivePlaylist(t, dc), []string{"c", "a", "d"}, nil, "Commute2")
	out, errs := mustPull(t, "pl", "push", "Commute2", "--yes")
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		got = append(got, f[0]+"|"+f[8]+"|"+f[9])
	}
	want := []string{
		"remove|removed from the master copy|removed_in_master",
		"move|reordered in the master copy|moved_in_master",
		"add|push to the platform|push",
		"rename|renamed in the master copy: Commute → Commute2|renamed_in_master",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("列:%q", got)
	}
	if !strings.Contains(errs, "Pushed 4 changes\n") || !slices.Equal(fs.tracksOf("p1"), []string{"c", "a", "d"}) {
		t.Fatalf("收尾:%s", errs)
	}
	if out, errs := mustPull(t, "pl", "push", "Commute2", "--yes"); out != "" || !strings.Contains(errs, "No changes\n") {
		t.Fatalf("再 push 零變更:%q %q", out, errs)
	}
	editCanonical(t, dc, drivePlaylist(t, dc), []string{"c", "a"}, nil, "")
	if _, errs := mustPull(t, "pl", "push", "Commute2", "--yes"); !strings.Contains(errs, "Pushed 1 change\n") {
		t.Fatalf("單數:%s", errs)
	}
}

// 每一種拒絕都是整句英文、帶下一步;這些錯誤只來自 push.go,所以也驗沒有中文。
func TestEnglishPushRefusals(t *testing.T) {
	t.Run("no base", func(t *testing.T) {
		withLanguage(t, "en")
		fs, _, _ := pullWorld(t)
		fs.set("p1", "Commute", "a")
		mustPull(t, "pl", "link", "Commute", "spotify:p1")
		_, _, err := runPull(t, "pl", "push", "Commute", "--yes", "--force")
		if exitOf(t, err) != 3 || err.Error() != "Commute on spotify hasn't been pulled yet (no base): run capy pl pull Commute first" {
			t.Fatalf("%v", err)
		}
	})
	t.Run("unpulled changes", func(t *testing.T) {
		fs, dc := enPushWorld(t, "a", "b", "c")
		editCanonical(t, dc, drivePlaylist(t, dc), []string{"a", "b"}, nil, "")
		fs.set("p1", "Commute", "a", "b", "c", "x")
		_, _, err := runPull(t, "pl", "push", "Commute", "--yes")
		if exitOf(t, err) != 3 || err.Error() != "Commute has changes on spotify that haven't been pulled: run capy pl pull Commute first (or capy pl sync)" {
			t.Fatalf("%v", err)
		}
	})
	t.Run("playlist missing", func(t *testing.T) {
		fs, _ := enPushWorld(t, "a")
		fs.drop("p1")
		_, _, err := runPull(t, "pl", "push", "Commute", "--yes")
		if exitOf(t, err) != 3 || err.Error() != "playlist p1 (Commute) wasn't found on spotify: run capy pl pull Commute first (it will unlink it)" {
			t.Fatalf("%v", err)
		}
	})
	t.Run("local files", func(t *testing.T) {
		withLanguage(t, "en")
		fs, dc, _ := pullWorld(t)
		fs.setLocal("l1")
		fs.set("p1", "Commute", "a", "l1", "b")
		mustPull(t, "pl", "link", "Commute", "spotify:p1")
		mustPull(t, "pl", "pull", "Commute", "--yes")
		pl := drivePlaylist(t, dc)
		pl.Items = pl.Items[:2]
		pl.UpdatedAt = 2
		putPlaylist(t, dc, pl)
		_, _, err := runPull(t, "pl", "push", "Commute", "--yes", "--force")
		if exitOf(t, err) != 3 || err.Error() != "Commute on spotify has 1 local file (local-l1) that a full replace would lose, so pushing this playlist isn't supported yet" {
			t.Fatalf("%v", err)
		}
	})
	t.Run("threshold", func(t *testing.T) {
		fs, dc := enPushWorld(t, "a", "b", "c", "d", "e", "f", "g", "h", "i", "j")
		editCanonical(t, dc, drivePlaylist(t, dc), []string{"a", "b", "c", "d", "e", "f"}, nil, "")
		_, _, err := runPull(t, "pl", "push", "Commute", "--yes")
		want := "Commute on spotify would remove 4 tracks (the platform has 10), over the threshold. Pass --force to override (check what would be removed with --dry-run first)"
		if exitOf(t, err) != 3 || err.Error() != want || len(fs.written()) != 0 {
			t.Fatalf("%v", err)
		}
	})
	t.Run("collaborative apple playlist", func(t *testing.T) { // Unwritable 的原因文字來自 Apple provider(別組),只驗 push.go 的那半句
		withLanguage(t, "en")
		fs, _, _ := pullWorld(t)
		fs.set("p1", "Commute", "a")
		amp := newFakeAmp(t)
		amp.catalog[fakeISRC("a")] = "1001"
		amp.names["1001"] = "song-a"
		amp.order = append(amp.order, "p.collab")
		amp.lists["p.collab"] = &ampList{Name: "Commute", CanEdit: true, Collab: true, Entries: []ampEntry{{ID: "a.1", Catalog: "1001"}}}
		swapAmp(t, amp)
		mustPull(t, "pl", "link", "Commute", "spotify:p1")
		mustPull(t, "pl", "link", "Commute", "apple:p.collab")
		mustPull(t, "pl", "sync", "Commute", "--yes")
		_, errs := mustPull(t, "pl", "sync", "Commute", "--yes")
		if !strings.Contains(errs, "Skipping the push half for Commute on apple: can't write to Commute on apple (p.collab): ") {
			t.Fatalf("sync 只跳過那一格:%s", errs)
		}
		_, _, err := runPull(t, "pl", "push", "Commute", "--provider", "apple", "--yes")
		if exitOf(t, err) != 3 || !strings.HasPrefix(err.Error(), "can't write to Commute on apple (p.collab): ") {
			t.Fatalf("明說 apple:%v", err)
		}
	})
}

// 確認之後、寫入之前平台變了:stderr 一句、錯誤一句(exit 3)。
func TestEnglishPushStaleBetweenPlanAndApply(t *testing.T) {
	fs, dc := enPushWorld(t, "a", "b", "c")
	editCanonical(t, dc, drivePlaylist(t, dc), []string{"a", "b"}, nil, "")
	reads := 0
	fs.setHook(func() {
		if reads++; reads == 3 { // 1 = /me/playlists、2 = 計畫的 GET items、3 = 套用前的重讀
			fs.items["p1"] = []string{"a", "b", "c", "z"}
		}
	})
	_, errs, err := runPull(t, "pl", "push", "Commute", "--yes")
	if !strings.Contains(errs, "Commute changed on spotify while waiting for confirmation; not writing it\n") {
		t.Errorf("stderr:%s", errs)
	}
	if exitOf(t, err) != 3 || err.Error() != "Commute on spotify changed while waiting for confirmation, so nothing was written to it; run capy pl pull, then push again" {
		t.Errorf("%v", err)
	}
}

// 平台寫了、Drive 沒寫成:兩件事都講,後面接 push 自己的下一步;Drive 的錯誤文字是別組的,只驗前後兩段。
func TestEnglishPushDriveFailureAfterWrite(t *testing.T) {
	withLanguage(t, "en")
	fs, dc, srv := pullWorld(t)
	fs.set("p1", "Commute", "a", "b", "c")
	mustPull(t, "pl", "link", "Commute", "spotify:p1")
	mustPull(t, "pl", "pull", "Commute", "--yes")
	editCanonical(t, dc, drivePlaylist(t, dc), []string{"a", "b", "c", "d"}, nil, "")
	srv.FailOn(func(r *http.Request) bool { return r.Method == http.MethodPatch }, http.StatusInternalServerError, "backendError")
	_, _, err := runPull(t, "pl", "push", "Commute", "--yes")
	msg := fmt.Sprint(err)
	if exitOf(t, err) != 1 || !strings.HasPrefix(msg, "1 change was already pushed, but writing to Drive failed: ") || !strings.HasSuffix(msg, " (the base didn't advance); run capy pl pull, then capy pl push") {
		t.Fatalf("%v", err)
	}
}

// 分批寫到一半、連 L′ 都讀不到:stderr 的警告與寫入失敗兩句。
func TestEnglishPushPartialWriteRereadFailure(t *testing.T) {
	fs, dc := enPushWorld(t, "a", "b", "c")
	var ids []string
	for i := 0; i < 120; i++ {
		ids = append(ids, fmt.Sprintf("t%03d", i))
	}
	editCanonical(t, dc, drivePlaylist(t, dc), ids, nil, "")
	fs.mu.Lock()
	fs.postFail = 3
	fs.mu.Unlock()
	gets := 0
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/items") {
			if gets++; gets >= 3 {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":{"status":500,"message":"down"}}`))
				return
			}
		}
		fs.handler(t)(w, r)
	})
	_, errs, err := runPull(t, "pl", "push", "Commute", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(errs, "Writing Commute on spotify failed: ") {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(errs, "); for now the base is recorded as the 100 tracks written, and the next pull will correct it\n") || !strings.Contains(errs, "Warning: re-reading Commute on spotify failed (") {
		t.Fatalf("stderr:%s", errs)
	}
}

// sync:一輪的收尾(推送 + pull 套用,複數)與「只跳過那一格的 push 半邊」。
func TestEnglishSyncRound(t *testing.T) {
	withLanguage(t, "en")
	fs1, fs2, _, _ := twoPlatforms(t)
	fs1.set("p1", "Commute", "a", "b", "c")
	fs2.set("q1", "Commute", "a", "b", "c")
	mustPull(t, "pl", "link", "Commute", "spotify:p1")
	mustPull(t, "pl", "link", "Commute", "apple:q1")
	if _, errs := mustPull(t, "pl", "sync", "Commute", "--yes"); !strings.Contains(errs, "Applied 3 pull changes\n") {
		t.Fatalf("bootstrap:%s", errs)
	}
	fs1.set("p1", "Commute", "a", "c")
	fs2.set("q1", "Commute", "c", "a", "b")
	_, errs := mustPull(t, "pl", "sync", "Commute", "--yes")
	if !strings.Contains(errs, "Pushed 2 changes\n") || !strings.Contains(errs, "Applied 2 pull changes\n") {
		t.Fatalf("收尾:%s", errs)
	}
	if _, errs := mustPull(t, "pl", "sync", "Commute", "--yes"); !strings.Contains(errs, "No changes\n") {
		t.Fatalf("零變更:%s", errs)
	}

	fs1.setLocal("lf1")
	fs1.set("p2", "Bedtime", "x", "lf1")
	fs2.set("q2", "Bedtime", "x")
	mustPull(t, "pl", "link", "Bedtime", "spotify:p2")
	mustPull(t, "pl", "link", "Bedtime", "apple:q2")
	mustPull(t, "pl", "sync", "--all", "--yes")
	fs2.set("q2", "Bedtime", "x", "c")
	_, errs = mustPull(t, "pl", "sync", "--all", "--yes")
	want := "Skipping the push half for Bedtime on spotify: Bedtime on spotify has 1 local file (local-lf1) that a full replace would lose, so pushing this playlist isn't supported yet\n"
	if !strings.Contains(errs, want) {
		t.Fatalf("只跳過那一格:%s", errs)
	}
}

// 終端機裡才問的那一句(push.confirm、sync.confirm、sync.confirm_force):假裝有 TTY、攔下 confirmWrite 並拒絕,
// 整句英文、單複數都對,措辭不看有幾個平台;拒絕後平台一首都不動。
func TestEnglishPushSyncConfirmPrompts(t *testing.T) {
	fs, dc := enPushWorld(t, "a", "b", "c")
	origTTY, origConfirm := bothTTY, confirmWrite
	t.Cleanup(func() { bothTTY, confirmWrite = origTTY, origConfirm })
	bothTTY = func(*cobra.Command) bool { return true }
	var asked []string
	confirmWrite = func(p string) (bool, error) { asked = append(asked, p); return false, nil }

	fs.set("p1", "Commute", "a", "b", "c", "x") // 平台多一首:sync 只有 pull 半邊
	runPull(t, "pl", "sync", "Commute")
	runPull(t, "pl", "sync", "Commute", "--force")
	fs.set("p1", "Commute", "a", "b", "c")
	editCanonical(t, dc, drivePlaylist(t, dc), []string{"a", "b", "c", "d"}, nil, "")
	runPull(t, "pl", "push", "Commute")
	runPull(t, "pl", "sync", "Commute")
	editCanonical(t, dc, drivePlaylist(t, dc), []string{"a", "b", "c", "d", "e"}, nil, "")
	runPull(t, "pl", "push", "Commute")
	runPull(t, "pl", "sync", "Commute")
	want := []string{
		"Apply the 1 change above (1 pulled to Drive, 0 pushed to platforms)?",
		"--force: removals propagate to every platform this playlist is linked to. Apply the 1 change above (1 pulled to Drive, 0 pushed to platforms)?",
		"Push the 1 change above?",
		"Apply the 1 change above (0 pulled to Drive, 1 pushed to platforms)?",
		"Push the 2 changes above?",
		"Apply the 2 changes above (0 pulled to Drive, 2 pushed to platforms)?",
	}
	if !slices.Equal(asked, want) {
		t.Fatalf("確認句:\n got %q\nwant %q", asked, want)
	}
	if got := fs.tracksOf("p1"); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("拒絕之後平台不該變:%q", got)
	}
}
