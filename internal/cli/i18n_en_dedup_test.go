package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 英文介面下 pl dedup / pl list / pl show 與 canon 的 REASON(dedup.go、pl.go、canon/derive.go、canon/project.go 的字串)。
// pull.go / push.go / sync.go 還在搬:只斷言這四個檔自己產生的字,整段輸出不做「沒有中文」檢查(help 除外,它只含這裡與 root 的字)。

func englishDedupWorld(t *testing.T) *fakeSpotify {
	t.Helper()
	withLanguage(t, "en")
	fs, _, _ := pullWorld(t)
	fs.set("p1", "commute", "a", "b", "a", "c", "b")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "pull", "commute", "--yes")
	return fs
}

// dedupHasLine:stderr 裡的整行(英文斷言比對整句,不怕其他檔還在印中文的行)。
func dedupHasLine(out, line string) bool {
	for _, l := range strings.Split(out, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

func TestEnglishDedupReport(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := pullWorld(t)
	fs.aliasISRC("a2", "a")
	fs.set("p1", "commute", "a", "b", "a", "c", "b", "a2")
	out, errs := mustPull(t, "pl", "dedup", "spotify:commute")
	want := "2\ta\tsong-a\tartist\tduplicate: same id as pos 0\tdup_id\n" +
		"4\tb\tsong-b\tartist\tduplicate: same id as pos 1\tdup_id\n" +
		"5\ta2\tsong-a2\tartist\tduplicate: same ISRC " + fakeISRC("a") + " as pos 0\tdup_isrc\n"
	if out != want {
		t.Fatalf("report:\n%s", out)
	}
	if errs != "3 duplicates; to have capy remove them: capy pl link <name> spotify:p1, then capy pl dedup <name>\n" {
		t.Fatalf("%q", errs)
	}
	fs.set("p2", "sleep", "x", "y")
	if _, errs := mustPull(t, "pl", "dedup", "spotify:p2"); errs != "spotify:p2 has no duplicates\n" {
		t.Fatalf("%q", errs)
	}
	if _, errs := mustPull(t, "pl", "dedup", "spotify:sleep"); errs != "spotify:sleep (p2) has no duplicates\n" {
		t.Fatalf("%q", errs)
	}
	const reportOnly = "spotify:p1 is only reported on, never changed (--yes / --force / --dry-run / --provider mean nothing here); to have capy remove the duplicates: capy pl link <name> spotify:p1, then capy pl dedup <name>"
	if _, _, err := runPull(t, "pl", "dedup", "spotify:p1", "--yes"); exitOf(t, err) != 1 || err.Error() != reportOnly {
		t.Fatalf("%v", err)
	}
	if _, _, err := runPull(t, "pl", "dedup", "commute", "--provider", "nope"); err == nil || err.Error() != "provider must be "+strings.Join(providerIDs, "|")+`: "nope"` {
		t.Fatalf("%v", err)
	}
	if _, _, err := runPull(t, "pl", "dedup", "spotify:nothing"); err == nil || err.Error() != `no playlist named "nothing" — see capy pl list` {
		t.Fatalf("resolvePlaylistID(pl.go):%v", err)
	}
}

// 只讀平台:單數的說法。
func TestEnglishDedupReportReadOnly(t *testing.T) {
	withLanguage(t, "en")
	_, fs2, _, _ := twoPlatforms(t)
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		p, err := orig(ctx, id)
		if id == "apple" && err == nil {
			return readOnlyProvider{p, p.(provider.PlaylistReader)}, nil
		}
		return p, err
	}
	t.Cleanup(func() { newProvider = orig })
	fs2.set("q1", "winter", "a", "a", "b")
	out, errs := mustPull(t, "pl", "dedup", "apple:winter")
	if out != "1\ta\tsong-a\tartist\tduplicate: same id as pos 0\tdup_id\n" || errs != "1 duplicate; apple is read-only for now, so delete it by hand in the app using the table above (pos starts at 0)\n" {
		t.Fatalf("%q %q", out, errs)
	}
}

func TestEnglishDedupCanonicalRound(t *testing.T) {
	fs := englishDedupWorld(t)
	out, _, err := runPull(t, "pl", "dedup", "commute", "--dry-run")
	if exitOf(t, err) != 2 {
		t.Fatalf("%v", err)
	}
	var dedupReasons []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Split(line, "\t"); f[0] == "dedup" {
			dedupReasons = append(dedupReasons, f[len(f)-2])
		}
	}
	if strings.Join(dedupReasons, "|") != "duplicate: same track as pos 0; the earlier copy is kept|duplicate: same track as pos 1; the earlier copy is kept" {
		t.Fatalf("dedup 列的 REASON:%q\n%s", dedupReasons, out)
	}

	origTTY, origConfirm := bothTTY, confirmWrite
	t.Cleanup(func() { bothTTY, confirmWrite = origTTY, origConfirm })
	bothTTY = func(*cobra.Command) bool { return true }
	var asked []string
	confirmWrite = func(_, p string) (bool, error) { asked = append(asked, p); return false, nil }
	runPull(t, "pl", "dedup", "commute")
	runPull(t, "pl", "dedup", "commute", "--force")
	const confirm = "Apply the 4 changes above (duplicates to remove: 2; pulls to Drive: 0; pushes to platforms: 2)?"
	if len(asked) != 2 || asked[0] != confirm || asked[1] != "--force: removals propagate to every platform this playlist is linked to. "+confirm {
		t.Fatalf("確認句:%q", asked)
	}
	bothTTY, confirmWrite = origTTY, origConfirm

	_, errs := mustPull(t, "pl", "dedup", "commute", "--yes")
	if !dedupHasLine(errs, "Pushed 2 changes") || !dedupHasLine(errs, "Removed 2 duplicates") {
		t.Fatalf("%s", errs)
	}
	if _, errs := mustPull(t, "pl", "dedup", "commute", "--yes"); !dedupHasLine(errs, "No duplicates") {
		t.Fatalf("%q", errs)
	}
	fs.set("p1", "commute", "a", "b", "c", "e") // 平台只加了一首:沒有重複,那筆留給 sync(單數)
	if _, errs := mustPull(t, "pl", "dedup", "commute", "--yes"); !dedupHasLine(errs, "No duplicates (the pull half saw 1 platform change; left for capy pl sync)") {
		t.Fatalf("%q", errs)
	}
}

// 挑選器的標題整句由 dedup.go 給(不經 pullTargets 拼動詞):英文與繁中都是整句。
func TestDedupPickerTitleIsWholeSentence(t *testing.T) {
	englishDedupWorld(t)
	log := stubPickers(t, 0, 0)
	if _, _, err := runPull(t, "pl", "dedup", "--dry-run"); exitOf(t, err) != 2 {
		t.Fatalf("挑選器選了 commute 之後照常走 dry-run:%v", err)
	}
	withLanguage(t, "zh-TW")
	if _, _, err := runPull(t, "pl", "dedup", "--dry-run"); exitOf(t, err) != 2 {
		t.Fatalf("%v", err)
	}
	if len(log.titles) != 2 || log.titles[0] != "Pick a playlist to dedup" || log.titles[1] != "選一個清單來去重" {
		t.Fatalf("%q", log.titles)
	}
}

func TestEnglishDedupUncheckedAndManual(t *testing.T) {
	withLanguage(t, "en")
	fs1, fs2, _, _ := twoPlatforms(t)
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		p, err := orig(ctx, id)
		if id == "apple" && err == nil {
			return readOnlyProvider{p, p.(provider.PlaylistReader)}, nil
		}
		return p, err
	}
	t.Cleanup(func() { newProvider = orig })
	fs1.set("p1", "commute", "a", "b")
	fs2.set("q1", "commute", "a", "a", "b")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "link", "commute", "apple:q1")
	mustPull(t, "pl", "pull", "commute", "--yes")
	_, errs := mustPull(t, "pl", "dedup", "commute", "--yes")
	manual := "apple:q1 still has 1 duplicate (pos 1); capy can't write to this platform right now or couldn't push to it this time, so delete it by hand in the app; the master copy won't add it back"
	if !dedupHasLine(errs, manual) || !dedupHasLine(errs, "Removed 1 duplicate") {
		t.Fatalf("%s", errs)
	}
	_, errs = mustPull(t, "pl", "dedup", "commute", "--yes", "--provider", "spotify")
	if !dedupHasLine(errs, "apple was not checked this time (not selected by --provider, or couldn't be read), so this result says nothing about duplicates on it") ||
		!dedupHasLine(errs, "No duplicates in the master copy or on the platforms checked this time") || dedupHasLine(errs, "No duplicates") {
		t.Fatalf("%s", errs)
	}
}

func TestEnglishDedupThreshold(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := pullWorld(t)
	ids := append(strings.Split(strings.Repeat("a ", 12), " ")[:12], "b")
	fs.set("p1", "commute", ids...)
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "pull", "commute", "--yes")
	_, _, err := runPull(t, "pl", "dedup", "commute", "--yes")
	if exitOf(t, err) != 3 || !strings.HasPrefix(err.Error(), "commute: removing 11 duplicates (of 13 tracks) exceeds the threshold") ||
		!strings.HasSuffix(err.Error(), ". Pass --force to override (check what would be removed with --dry-run first)") {
		t.Fatalf("%v", err)
	}
}

// help 只含 dedup.go、pl.go 與 root 的字(都搬完了):整段不該有中文。
func TestEnglishPlDedupHelp(t *testing.T) {
	withLanguage(t, "en")
	pullWorld(t)
	out, _, err := runPull(t, "pl", "dedup", "--help")
	if err != nil || hasCJK(out) || !strings.Contains(out, "capy pl dedup [name|pid | <provider>:<playlist-id-or-name>]") ||
		!strings.Contains(out, "pushed to the playlist's other linked platforms in the same command") {
		t.Fatalf("%v\n%s", err, out)
	}
	out, _, err = runPull(t, "pl", "--help")
	for _, s := range []string{"Playlists", "List my playlists", "Show a playlist's tracks (with no argument in a terminal, opens a picker)", "Remove duplicate tracks from a playlist"} {
		if err != nil || !strings.Contains(out, s) {
			t.Fatalf("%q:%v\n%s", s, err, out)
		}
	}
}

func TestEnglishPlShowNameErrors(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := pullWorld(t)
	if _, _, err := runPull(t, "pl", "show", "nothing"); err == nil || err.Error() != `no playlist named "nothing" — see capy pl list` {
		t.Fatalf("%v", err)
	}
	fs.set("p1", "twin", "a")
	fs.set("p2", "twin", "b")
	_, _, err := runPull(t, "pl", "show", "twin")
	if err == nil || !strings.HasPrefix(err.Error(), "2 playlists share this name; use an ID instead: p1 (") || !strings.Contains(err.Error(), ", p2 (") || hasCJK(err.Error()) {
		t.Fatalf("%v", err)
	}
}

// canon 的 REASON(derive.go 的 pull 變更、project.go 的 push skip)在 pull / push 的 TSV 裡跟著語系;REASON_CODE 不變。
func TestEnglishCanonReasons(t *testing.T) {
	withLanguage(t, "en")
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "commute", "a", "b", "c")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "pull", "commute", "--yes")
	reasons := func(args ...string) map[string]string {
		t.Helper()
		out, _, err := runPull(t, args...)
		if err != nil && exitOf(t, err) != 2 { // 只有 skip 列的 push 是「無變更」exit 0
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		got := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			f := strings.Split(line, "\t")
			got[f[len(f)-1]] = f[len(f)-2]
		}
		return got
	}
	fs.set("p1", "commute2", "c", "a", "d") // 改名、b 移除、c 換序、d 新增
	got := reasons("pl", "pull", "commute", "--dry-run")
	want := map[string]string{
		"renamed_on_platform": "renamed on the platform: commute → commute2",
		"removed_on_platform": "removed on the platform",
		"moved_on_platform":   "reordered on the platform",
		"added_on_platform":   "added on the platform",
	}
	for code, r := range want {
		if got[code] != r {
			t.Errorf("%s:%q", code, got[code])
		}
	}
	fs.drop("p1")
	if got := reasons("pl", "pull", "commute", "--dry-run"); got["playlist_gone"] != "the playlist no longer exists on the platform (Q6: unlinked automatically)" {
		t.Errorf("%q", got)
	}

	fs.set("p1", "commute", "a", "b", "c")
	editCanonical(t, dc, drivePlaylist(t, dc), []string{"a", "b", "c", "0"}, map[string]bool{"0": false}, "")
	if got := reasons("pl", "push", "commute", "--dry-run"); got["no_mapping"] != "no mapping for this platform (or pinned as unavailable): capy resolve" {
		t.Errorf("%q", got)
	}
}

// canon.err.dirty_rank:CLI 走不到(要 Drive 上的 rank 先髒掉),直接呼叫真的 canon.Derive。%w 語意照舊(errors.Unwrap 拿得到原因)。
func TestEnglishDeriveDirtyRank(t *testing.T) {
	withLanguage(t, "en")
	cid := func(id string) string { return canon.CID("spotify", id, "") }
	tracks := map[string]canon.Track{}
	for _, id := range []string{"a", "b"} {
		tracks[cid(id)] = canon.Track{CID: cid(id), Title: "song-" + id, Mappings: map[string]canon.Mapping{"spotify": {ID: id}}}
	}
	pl := canon.Playlist{PID: "P1", Name: "x", Items: []canon.Item{{IID: "i1", CID: cid("a"), Rank: "a0"}, {IID: "i2", CID: cid("b"), Rank: "a0"}}}
	_, err := canon.Derive(canon.DeriveInput{
		Provider: "spotify", Playlist: pl, Tracks: tracks,
		Base: &canon.Snapshot{Name: "x", Items: []string{"a", "b"}, CIDs: []string{cid("a"), cid("b")}},
		Live: &canon.Observed{ID: "p1", Name: "x", Tracks: []provider.Track{{ProviderID: "a"}, {ProviderID: "c"}, {ProviderID: "b"}}},
	})
	const prefix = "the rank data of playlist P1 is corrupt; not reordering it automatically: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("%v", err)
	}
	var inner []error
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		inner = u.Unwrap()
	}
	if len(inner) != 1 || err.Error() != prefix+inner[0].Error() || !errors.Is(err, inner[0]) {
		t.Fatalf("要包住 RankBetween 的錯:%v %v", err, inner)
	}
}

// 去重與 pull 同一輪都落地:英文把 pull 的筆數組成片語(單複數跟著 pull 的筆數,不是去重的份數),繁中與搬字串前逐位元相同。
func TestDedupRemovedAndPulledWording(t *testing.T) {
	for lang, want := range map[string]string{"en": "Removed 2 duplicates and applied 1 pull change", "zh-TW": "已去除 2 份重複(另套用 1 筆 pull 變更)"} {
		t.Run(lang, func(t *testing.T) {
			withLanguage(t, lang)
			fs, _, _ := pullWorld(t)
			fs.set("p1", "commute", "a", "b", "a", "c", "b")
			mustPull(t, "pl", "link", "commute", "spotify:p1")
			mustPull(t, "pl", "pull", "commute", "--yes")
			fs.set("p1", "commute", "a", "b", "a", "c", "b", "d") // 平台多一首 = pull 半邊 1 筆
			if _, errs := mustPull(t, "pl", "dedup", "commute", "--yes"); !dedupHasLine(errs, want) {
				t.Fatalf("%s", errs)
			}
		})
	}
}
