package cli

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"charm.land/huh/v2"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 英文介面下 resolve 的說法(resolve.go 的字串)。stdout 的 TSV 列全是 resolve.go 印的,可以斷言沒有中文;
// stderr 也有 withCanonical(pull.go,還在搬)的字,只斷言 resolve.go 自己印的那幾行。

func TestEnglishResolveQueueRowsAndSummary(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a2", Name: "song-a", ISRC: "TW00000000ZZ"})
	fs.addCatalog(fakeCatalogTrack{ID: "ap-b-live", Name: "song-b (Live)", ISRC: "TW00000000ZY"})
	out, errs := mustPull(t, "resolve", "--yes")
	want := "map\t" + fakeCID("a") + "\tapple\tap-a2\t100\tfuzzy\tsong-a\tartist\tfuzzy score 100: song-a — artist (3:20)\tfuzzy\n" +
		"review\t" + fakeCID("b") + "\tapple\tap-b-live\t84\tfuzzy\tsong-b\tartist\tscore 84 < 85: candidate song-b (Live) — artist (3:20)\tlow_score\n"
	if out != want || hasCJK(out) {
		t.Fatalf("got  %q\nwant %q", out, want)
	}
	if !strings.Contains(errs, "Wrote 1 mapping\n") || !strings.Contains(errs, "1 item needs review: run capy resolve --review in a terminal, or use capy resolve pin\n") {
		t.Fatalf("stderr:%s", errs)
	}
}

func TestEnglishResolveISRCAndNothingToWrite(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")})
	fs.addCatalog(fakeCatalogTrack{ID: "ap-b", Name: "song-b", ISRC: fakeISRC("b")})
	out, errs := mustPull(t, "resolve", "--yes")
	if strings.Count(out, "\tISRC lookup\tisrc\n") != 2 || !strings.Contains(errs, "Wrote 2 mappings\n") {
		t.Fatalf("%q\n%s", out, errs)
	}
	if _, errs := mustPull(t, "resolve", "--yes"); !strings.Contains(errs, "No mappings to write automatically\n") {
		t.Fatalf("%s", errs)
	}
}

func TestEnglishResolveErrors(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := resolveWorld(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"resolve", "--provider", "bogus"}, `unknown provider "bogus" (spotify|apple|local)`},
		{[]string{"resolve", "--review", "--dry-run"}, "--review can't be combined with --dry-run (review decisions are always written; to look at the queue first, use capy resolve --dry-run)"},
		{[]string{"resolve", "pin", "i:nope", "apple:none"}, "no cid i:nope in tracks (run capy pl pull first)"},
	} {
		if _, _, err := runPull(t, tc.args...); err == nil || err.Error() != tc.want {
			t.Fatalf("%v:\ngot  %v\nwant %s", tc.args, err, tc.want)
		}
	}
	fs.addCatalog(fakeCatalogTrack{ID: "ap-b-live", Name: "song-b (Live)", ISRC: "TW00000000ZY"})
	out, _, err := runPull(t, "resolve", "--review")
	const tty = "2 items need review: --review needs a terminal (the queue has been printed; in scripts, use capy resolve pin <cid> <provider>:<id|none>)"
	if exitOf(t, err) != 2 || err.Error() != tty || !strings.HasPrefix(out, "review\t"+fakeCID("a")+"\tapple\t\t\t\tsong-a\tartist\tno candidate found\tno_candidate\n") {
		t.Fatalf("%v\n%q", err, out)
	}
	if got := (&ReviewNeedsTTYError{N: 1}).Error(); !strings.HasPrefix(got, "1 item needs review: ") {
		t.Fatalf("單數:%s", got)
	}
}

func TestEnglishResolveReviewLoop(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-b-live", Name: "song-b (Live)", ISRC: "TW00000000ZY"})
	stubReview(t, func(it resolveItem, search func(string) ([]provider.Track, error)) reviewDecision {
		if it.cid == fakeCID("a") {
			return reviewDecision{kind: "none"}
		}
		found, _ := search("song b")
		return reviewDecision{kind: "manual", cand: &found[0]}
	})
	_, errs := mustPull(t, "resolve", "--review")
	for _, want := range []string{"[1/2] song-a: pinned as unavailable\n", "[2/2] song-b: pinned ap-b-live\n", "Recorded 2 review decisions\n"} {
		if !strings.Contains(errs, want) {
			t.Fatalf("缺 %q:\n%s", want, errs)
		}
	}
}

func TestEnglishResolveReviewCancelAndSearchFailure(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")}) // a 自動 map;b 找不到候選
	stubReview(t, nil)
	reviewPrompt = func(resolveItem, int, int, func(string) ([]provider.Track, error)) (reviewDecision, error) {
		return reviewDecision{}, huh.ErrUserAborted
	}
	if _, errs, err := runPull(t, "resolve", "--review", "--yes"); exitOf(t, err) != 2 ||
		!strings.Contains(errs, "Cancelled: nothing from this run is written (1 automatic mapping, 0 review decision(s))\n") {
		t.Fatalf("%v\n%s", err, errs)
	}
	stubReview(t, func(it resolveItem, search func(string) ([]provider.Track, error)) reviewDecision {
		fs.setSearchStatus(http.StatusServiceUnavailable) // 503 不退避,測試不用等
		search("song b")
		return reviewDecision{kind: "skip"}
	})
	_, errs := mustPull(t, "resolve", "--review", "--yes")
	for _, want := range []string{"Search failed, skipping this one for now: ", "[1/1] song-b: skipped\n", "Recorded 0 review decisions\n", "Wrote 1 mapping\n"} {
		if !strings.Contains(errs, want) {
			t.Fatalf("缺 %q:\n%s", want, errs)
		}
	}
}

func TestEnglishResolveConflictKeep(t *testing.T) {
	withLanguage(t, "en")
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "commute", "a")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "pull", "commute", "--yes")
	tr := driveTracks(t, dc)
	ta := tr.Tracks[fakeCID("a")]
	ta.Conflicts = append(ta.Conflicts,
		canon.Conflict{Provider: "spotify", ProviderID: "a-live", Title: "song-a (Live)", DurationMS: 250000},
		canon.Conflict{Provider: "spotify", ProviderID: "a-demo", Title: "song-a (Demo)", DurationMS: 190000})
	tr.Tracks[fakeCID("a")] = ta
	putTracks(t, dc, tr)
	out, _ := mustPull(t, "resolve", "--yes")
	want := "conflict\t" + fakeCID("a") + "\tspotify\ta\t100\tobserved\tsong-a\tartist\tdifferent ids seen for the same ISRC: a-live (song-a (Live), 4:10), a-demo (song-a (Demo), 3:10); keep in --review pins the current mapping\tisrc_conflict\n"
	if out != want {
		t.Fatalf("got  %q\nwant %q", out, want)
	}
	stubReview(t, func(resolveItem, func(string) ([]provider.Track, error)) reviewDecision {
		return reviewDecision{kind: "keep"}
	})
	if _, errs := mustPull(t, "resolve", "--review"); !strings.Contains(errs, "[1/1] song-a: kept the current mapping a\n") {
		t.Fatalf("%s", errs)
	}
}

func TestEnglishResolveReviewMergeDecline(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")})
	mustPull(t, "resolve", "--yes")
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("b")}) // b 的候選已屬 a
	stubReview(t, func(it resolveItem, _ func(string) ([]provider.Track, error)) reviewDecision {
		return reviewDecision{kind: "accept", cand: it.cand}
	})
	var asked string
	origConfirm := confirmWrite
	confirmWrite = func(q string) (bool, error) { asked = q; return false, nil }
	t.Cleanup(func() { confirmWrite = origConfirm })
	_, errs := mustPull(t, "resolve", "--review")
	wantQ := "This apple id already belongs to cid " + fakeCID("a") + ": merge " + fakeCID("b") + " with it? (The lexicographically smaller cid wins and every playlist item is repointed to it)"
	if asked != wantQ || !strings.Contains(errs, "[1/1] song-b: skipped (not merged)\n") {
		t.Fatalf("got  %q\nwant %q\n%s", asked, wantQ, errs)
	}
}

func TestEnglishResolvePin(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := resolveWorld(t)
	cidA, cidB := fakeCID("a"), fakeCID("b")
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")})
	mustPull(t, "resolve", "--yes")
	if _, errs := mustPull(t, "resolve", "pin", cidB, "apple:none"); !strings.Contains(errs, "Pinned: "+cidB+" is unavailable on apple\n") {
		t.Fatalf("%s", errs)
	}
	if _, _, err := runPull(t, "resolve", "pin", cidA, "apple:nope"); err == nil || !strings.HasPrefix(err.Error(), "can't read track nope on apple (wrong id?): ") {
		t.Fatalf("%v", err)
	}
	notice := "apple:ap-a already belongs to cid " + cidA + ": pinning it merges " + cidB + " with " + cidA + " (the lexicographically smaller cid wins and every playlist item is repointed to it)\n"
	if _, errs, err := runPull(t, "resolve", "pin", cidB, "apple:ap-a"); exitOf(t, err) != 2 || !strings.Contains(errs, notice) {
		t.Fatalf("%v\n%s", err, errs)
	}
	_, errs := mustPull(t, "resolve", "pin", cidB, "apple:ap-a", "--yes")
	if !strings.Contains(errs, "Merged; the winner is "+cidA+"\n") || !strings.Contains(errs, "Pinned: "+cidA+" on apple = ap-a\n") {
		t.Fatalf("%s", errs)
	}
}

func TestEnglishResolveHintsAndPlanDegradation(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := pullWorld(t)
	fs.set("p1", "commute", "a", "b")
	fs.set("p2", "commute", "c")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "pull", "commute", "--yes")
	mustPull(t, "pl", "link", "commute", "apple:p2")
	_, errs := mustPull(t, "pl", "pull", "commute", "--yes")
	if !strings.Contains(errs, "2 tracks aren't mapped to apple yet; run capy resolve\n") || !strings.Contains(errs, "1 track isn't mapped to spotify yet; run capy resolve\n") {
		t.Fatalf("%s", errs)
	}
	orig, origHint := apiCallHint, newProvider
	apiCallHint = 0
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id == "apple" {
			return nil, provider.ErrAuthExpired
		}
		return origHint(ctx, id)
	}
	t.Cleanup(func() { apiCallHint, newProvider = orig, origHint })
	_, errs, _ = runPull(t, "resolve", "--dry-run")
	if !strings.Contains(errs, "Skipping apple this run: ") ||
		!strings.Contains(errs, "This resolve made 2 API calls (more than 0): unresolved cids are looked up again every time; if you run it from cron, please report it — it's time to add the negative cache that decision 24 postponed\n") {
		t.Fatalf("%s", errs)
	}
	newProvider = origHint
	fs.setSearchStatus(http.StatusServiceUnavailable)
	if out, _, _ := runPull(t, "resolve", "--dry-run"); strings.Count(out, "\tlookup failed: ") != 3 { // 冒號後是 provider 的錯誤(T2d 的檔),不斷言它的語言
		t.Fatalf("%q", out)
	}
}

// reviewPrompt 的 huh 表單要 TTY、測試跑不到;第一層的標題與選項由純函式 reviewMenu 產生,從它看英文。
func TestEnglishReviewMenu(t *testing.T) {
	withLanguage(t, "en")
	cand := &provider.Track{ProviderID: "ap-b-live", Title: "song-b (Live)", Artists: []string{"artist"}, DurationMS: 250000}
	it := resolveItem{action: "conflict", prov: "apple", score: 84, cand: cand, reason: "r", current: canon.Mapping{ID: "ap-b"},
		track: canon.Track{Title: "song-b", Artists: []string{"x", "y"}, DurationMS: 200000}}
	title, opts := reviewMenu(it, 2, 3)
	if title != "[2/3] song-b — x, y (3:20)\napple: r" {
		t.Fatalf("%q", title)
	}
	var got []string
	for _, o := range opts {
		got = append(got, o.Key+"="+o.Value)
	}
	want := []string{
		"Accept the candidate (score 84): song-b (Live) — artist (4:10)=accept",
		"Keep the current mapping ap-b (you confirmed it; won't ask again)=keep",
		"Skip (ask again next time)=skip", "Search manually=manual", "This platform doesn't have it (pin as unavailable)=none",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if _, _, err := applyDecision(&canonState{tracks: canon.NewTracks()}, resolveItem{}, reviewDecision{kind: "bogus"}, nil); err == nil || err.Error() != `unknown decision "bogus"` {
		t.Fatalf("%v", err)
	}
	if _, _, err := applyDecision(&canonState{tracks: canon.NewTracks()}, resolveItem{}, reviewDecision{kind: "accept"}, nil); err == nil || err.Error() != "no candidate to accept" {
		t.Fatalf("%v", err)
	}
}

func TestEnglishResolveHelp(t *testing.T) {
	withLanguage(t, "en")
	for _, args := range [][]string{{"resolve", "--help"}, {"resolve", "pin", "--help"}} {
		out, err := runCLI(t, args...)
		if err != nil || hasCJK(out) {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
}
