package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 英文介面下 capy migrate 的說法(migrate.go 的字串)。observeAndDerive、applyPlans、finishPush、PendingError 是 pull.go / push.go 的,
// 還在搬:只斷言 migrate.go 自己印的行,不對整段 stderr 斷言沒有中文。

// stubMigrateTTY:當成在終端機裡跑,確認一律回 answer;回傳收到的提示。
func stubMigrateTTY(t *testing.T, answer bool) *[]string {
	t.Helper()
	origTTY, origConfirm := migrateIsTTY, confirmWrite
	prompts := &[]string{}
	migrateIsTTY = func(*cobra.Command) bool { return true }
	confirmWrite = func(p string) (bool, error) { *prompts = append(*prompts, p); return answer, nil }
	t.Cleanup(func() { migrateIsTTY, confirmWrite = origTTY, origConfirm })
	return prompts
}

// migratePromptsDeclined:來源 [a, unmatched...]、只有 a 對得到;在終端機裡跑 migrate、每個確認都回「否」,回傳提示(TestWebMoveWizardKeysOnMigrateWording 也用)。
func migratePromptsDeclined(t *testing.T, unmatched ...string) []string {
	t.Helper()
	fs1, fs2, _, _ := twoPlatforms(t)
	catalogISRC(fs1, "a")
	fs2.set("q1", "road trip", append([]string{"a"}, unmatched...)...)
	prompts := stubMigrateTTY(t, false)
	if _, _, err := runPull(t, "migrate", "road trip", "--from", "apple", "--to", "spotify"); exitOf(t, err) != 2 || fs1.createdCount() != 0 {
		t.Fatalf("取消:exit 2、不建清單:%v", err)
	}
	return *prompts
}

func wantLine(t *testing.T, out, line string) {
	t.Helper()
	if !strings.Contains(out, line+"\n") {
		t.Errorf("少了這一行:%q\n%s", line, out)
	}
}

func TestEnglishMigrateHelp(t *testing.T) {
	withLanguage(t, "en")
	setCLITestConfig(t)
	out, err := runCLI(t, "migrate", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if c, _, err := newRootCmd().Find([]string{"migrate"}); err != nil || c.Short != "Move a playlist from one platform to another (as a new playlist or into an existing one); keeps the order, never deletes the source, only adds" {
		t.Errorf("Short:%v", err)
	}
	for _, want := range []string{
		"migrate [source-playlist-id-or-name] --from <platform> --to <platform>[:<existing-playlist-id-or-name>]",
		"dir action provider playlist pos cid provider_id title artists reason reason_code; dir ∈ pull / migrate / push",
		"Tracks moved into Apple Music may also be added to your Apple Music library (depending on your Apple Music settings; this is Apple's behavior)",
		"source platform (spotify|apple|local); picked interactively in a terminal if omitted",
		"skip the confirmation (for cron / pipelines); unmatched tracks aren't reviewed on the spot",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help 少了 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "will also be added") || hasCJK(out) {
		t.Errorf("英文 help:Apple 資料庫只能說 may、不能有中文:\n%s", out)
	}
}

func TestEnglishMigrateNewPlaylist(t *testing.T) {
	withLanguage(t, "en")
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "a", "b", "c")
	fs2.set("q1", "road trip", "a", "b", "a", "c")
	out, _, err := runPull(t, "migrate", "road trip", "--from", "apple", "--to", "spotify", "--dry-run")
	if exitOf(t, err) != 2 {
		t.Fatalf("%v", err)
	}
	wantLine(t, out, "migrate\tadd\tapple\troad trip\t0\t"+fakeCID("a")+"\ta\tsong-a\tartist\tpush to spotify:a (isrc 95)\tpush")
	_, errs := mustPull(t, "migrate", "road trip", "--from", "apple", "--to", "spotify", "--yes")
	pid := drivePlaylistNamed(t, dc, "road trip").PID
	for _, line := range []string{
		"Created the master copy road trip (" + pid + ")",
		"resolve: 3 matched on spotify automatically, 0 left for review",
		"Created the playlist road trip (new1) on spotify",
		"Pushed 3 changes",
		"Added 3 tracks from apple:q1 to the master copy road trip (" + pid + ", now 3 in total) and pushed 3 to spotify:new1",
		`The source isn't linked (a one-time copy). To follow later changes on apple: capy pl link "road trip" apple:q1, then capy pl sync "road trip"`,
	} {
		wantLine(t, errs, line)
	}
	if _, _, err := runPull(t, "migrate", "road trip", "--from", "apple", "--to", "spotify", "--yes"); err == nil ||
		err.Error() != `spotify already has a playlist named "road trip" (new1): to add to it, use --to spotify:new1; if you really want another one, create it in the app first, then use --to spotify:<ID>` {
		t.Fatalf("%v", err)
	}
	_, errs = mustPull(t, "migrate", "road trip", "--from", "apple", "--to", "spotify:new1", "--yes")
	wantLine(t, errs, "Using the master copy road trip ("+pid+") linked to spotify:new1")
	wantLine(t, errs, "All 4 tracks in apple:q1 are already in the master copy road trip ("+pid+"); no changes")
}

func TestEnglishMigrateIntoExisting(t *testing.T) {
	withLanguage(t, "en")
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "x", "y", "z", "w")
	fs1.set("p1", "commute", "x", "y")
	fs2.set("q1", "picks", "y", "z", "w")
	prompts := stubMigrateTTY(t, true)
	_, errs := mustPull(t, "migrate", "picks", "--from", "apple", "--to", "spotify:p1")
	pid := drivePlaylistNamed(t, dc, "commute").PID
	wantLine(t, errs, "Created the master copy commute ("+pid+")")
	wantLine(t, errs, "Linked commute ("+pid+") ↔ spotify:p1")
	if len(*prompts) != 1 || (*prompts)[0] != "Add 2 tracks from apple:q1 to spotify:p1?" {
		t.Fatalf("%q", *prompts)
	}
	fs1.set("p1", "commute", "x", "y", "z", "w", "u")
	_, errs = mustPull(t, "migrate", "picks", "--from", "apple", "--to", "spotify:p1", "--yes")
	wantLine(t, errs, "All 3 tracks in apple:q1 are already in the master copy commute ("+pid+"); no changes (the pull half saw 1 platform change; leaving it to capy pl sync)")
	mustPull(t, "pl", "sync", "commute", "--yes") // 把 u 吸進正本,下一輪就沒有 pull 半邊的變更
	fs2.set("q2", "one", "x")
	_, errs = mustPull(t, "migrate", "one", "--from", "apple", "--to", "spotify:p1", "--yes")
	wantLine(t, errs, "The only track in apple:q2 is already in the master copy commute ("+pid+"); no changes")
}

func TestEnglishMigrateReviewPrompts(t *testing.T) {
	withLanguage(t, "en")
	prompts := migratePromptsDeclined(t, "n")
	if len(prompts) != 2 ||
		prompts[0] != `1 track has no automatic match on spotify. Review now, one by one? (Cancel = push the matched tracks now and review later with capy resolve "road trip" --provider spotify --review)` ||
		prompts[1] != `Create the playlist "road trip" on spotify and push 2 tracks from apple:q1? (1 of them has no match on spotify and won't be pushed this time)` {
		t.Fatalf("%q", prompts)
	}
}

func TestEnglishMigrateReviewPromptsPlural(t *testing.T) {
	withLanguage(t, "en")
	prompts := migratePromptsDeclined(t, "n", "m")
	if len(prompts) != 2 ||
		prompts[0] != `2 tracks have no automatic match on spotify. Review now, one by one? (Cancel = push the matched tracks now and review later with capy resolve "road trip" --provider spotify --review)` ||
		prompts[1] != `Create the playlist "road trip" on spotify and push 3 tracks from apple:q1? (2 of them have no match on spotify and won't be pushed this time)` {
		t.Fatalf("%q", prompts)
	}
}

func TestEnglishMigrateReviewedAndUnmapped(t *testing.T) {
	withLanguage(t, "en")
	fs1, fs2, _, _ := twoPlatforms(t)
	catalogISRC(fs1, "a")
	fs2.set("q1", "road trip", "a", "n")
	stubMigrateTTY(t, true)
	origPrompt := reviewPrompt
	reviewPrompt = func(resolveItem, int, int, func(string) ([]provider.Track, error)) (reviewDecision, error) {
		return reviewDecision{kind: "none"}, nil
	}
	t.Cleanup(func() { reviewPrompt = origPrompt })
	out, errs := mustPull(t, "migrate", "road trip", "--from", "apple", "--to", "spotify")
	wantLine(t, out, "\t"+fakeCID("n")+"\tn\tsong-n\tartist\tno mapping for spotify; not pushed this time\tno_mapping")
	wantLine(t, errs, "resolve: 1 matched on spotify automatically, 1 left for review")
	wantLine(t, errs, "Recorded 1 review decision")
	wantLine(t, errs, "Pushed 1 change")
	wantLine(t, errs, `1 track has no match on spotify and wasn't pushed: review it with capy resolve "road trip" --provider spotify --review, then push it with capy pl sync "road trip" --provider spotify`)
}

func TestEnglishMigrateSourceLinked(t *testing.T) {
	withLanguage(t, "en")
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "a", "b", "n")
	fs2.set("q1", "road trip", "a", "b")
	mustPull(t, "pl", "link", "road trip", "apple:q1")
	mustPull(t, "pl", "pull", "road trip", "--yes")
	fs2.set("q1", "road trip", "n", "a", "b")
	_, errs := mustPull(t, "migrate", "road trip", "--from", "apple", "--to", "spotify", "--yes")
	pid := drivePlaylistNamed(t, dc, "road trip").PID
	wantLine(t, errs, "Using the existing master copy road trip ("+pid+"; linked to apple:q1)")
	wantLine(t, errs, "The master copy road trip ("+pid+") is linked to apple:q1, so the master copy wins: pulled the source's changes into it first (1 pulled), then pushed 3 tracks to spotify:new1")
	wantLine(t, errs, `The source apple:q1 is linked to this master copy too (not a one-time copy): from now on, capy pl sync "road trip" keeps both sides in step with the master copy`)
}

func TestEnglishMigrateCreatedPlaylistMissingFromList(t *testing.T) {
	withLanguage(t, "en")
	fs1, fs2, _, _ := twoPlatforms(t)
	catalogISRC(fs1, "a")
	fs2.set("q1", "road trip", "a")
	fs1.setHook(func() {
		if i := fs1.index("new1"); i >= 0 {
			fs1.lists = append(fs1.lists[:i], fs1.lists[i+1:]...)
		}
	})
	_, _, err := runPull(t, "migrate", "road trip", "--from", "apple", "--to", "spotify", "--yes")
	if err == nil || err.Error() != `the playlist new1 that was just created doesn't show up in the spotify playlist list; the link was removed; `+
		`the playlist road trip (new1) on spotify was created, but its link wasn't saved to Drive: reconnect it with capy pl link "road trip" spotify:new1, then capy pl sync "road trip" --provider spotify` {
		t.Fatalf("%v", err)
	}
}

func TestEnglishMigrateRefusals(t *testing.T) {
	withLanguage(t, "en")
	fs1, fs2, _, _ := twoPlatforms(t)
	catalogISRC(fs1, "a", "b", "x", "y", "z")
	fs2.set("q1", "road trip", "a")
	fs2.set("q7", "old", "b")
	fs2.set("q9", "empty")
	ids := strings.Join(providerIDs, "|")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"migrate", "road trip"}, "--from and --to are required (there is no picker outside a terminal): capy migrate <playlist> --from apple --to spotify[:<existing-playlist>]"},
		{[]string{"migrate", "road trip", "--from", "nope", "--to", "spotify"}, `--from must be ` + ids + `: "nope"`},
		{[]string{"migrate", "road trip", "--from", "apple", "--to", "nope"}, `--to must be <platform> or <platform>:<existing-playlist>, where the platform is ` + ids + `: "nope"`},
	} {
		if _, _, err := runPull(t, c.args...); err == nil || err.Error() != c.want {
			t.Errorf("%v:%v", c.args, err)
		}
	}
	if _, errs := mustPull(t, "migrate", "empty", "--from", "apple", "--to", "spotify", "--yes"); !strings.Contains(errs, "apple:q9 is empty; nothing to move\n") {
		t.Errorf("%q", errs)
	}
	mustPull(t, "pl", "link", "road trip", "apple:q7")
	if _, _, err := runPull(t, "migrate", "road trip", "--from", "apple", "--to", "spotify", "--yes"); err == nil || !strings.HasPrefix(err.Error(), "the master copy with the same name, road trip (") ||
		!strings.HasSuffix(err.Error(), `), is linked to apple:q7, not the source apple:q1: if that's the one you want to move, use it; otherwise first run capy pl unlink "road trip" apple`) {
		t.Errorf("%v", err)
	}
	// 只讀的平台不能當目標:包著的 ErrNotSupported 照樣認得出來
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		p, err := orig(ctx, id)
		if id == "apple" && err == nil {
			return readOnlyProvider{p, p.(provider.PlaylistReader)}, nil
		}
		return p, err
	}
	fs1.set("p2", "commute", "x")
	_, _, err := runPull(t, "migrate", "commute", "--from", "spotify", "--to", "apple")
	newProvider = orig
	if err == nil || !strings.HasPrefix(err.Error(), "apple is read-only for now, so it can't be a migrate target: ") || !errors.Is(err, provider.ErrNotSupported) {
		t.Errorf("%v", err)
	}
	// 目標有還沒同步的移除
	fs1.set("p3", "mix", "x", "y", "z")
	fs2.set("q3", "mix", "x", "y", "z")
	fs2.set("q4", "src", "a")
	mustPull(t, "pl", "link", "mix", "spotify:p3")
	mustPull(t, "pl", "link", "mix", "apple:q3")
	mustPull(t, "pl", "sync", "mix", "--yes")
	fs2.set("q3", "mix", "x", "y")
	mustPull(t, "pl", "pull", "mix", "--yes", "--provider", "apple")
	if _, _, err := runPull(t, "migrate", "src", "--from", "apple", "--to", "spotify:mix", "--yes"); exitOf(t, err) != 3 ||
		err.Error() != `mix has an unsynced remove on spotify (migrate only adds): run capy pl sync "mix" --provider spotify first, then migrate` {
		t.Errorf("%v", err)
	}
}

func TestEnglishMigratePicker(t *testing.T) {
	withLanguage(t, "en")
	fs1, fs2, _, _ := twoPlatforms(t)
	catalogISRC(fs1, "a")
	fs2.set("q1", "road trip", "a")
	fs1.set("p1", "someone else's", "x")
	fs1.mu.Lock()
	fs1.restricted["p1"] = true
	fs1.mu.Unlock()
	log := stubPickers(t, 1, 0, 0, 0) // apple → q1 → spotify → p1(讀不到)
	_, _, err := runPull(t, "migrate", "--yes")
	if err == nil || err.Error() != "can't read the contents of spotify playlist p1 (an app in development mode can't read Spotify's own or other users' playlists), so it can't be the target" {
		t.Fatalf("%v", err)
	}
	if len(log.titles) != 4 || !strings.Contains(log.titles[0], "Move from which platform?") || !strings.Contains(log.titles[2], "Move to which platform?") ||
		!strings.Contains(strings.Join(log.labels[3], "\n"), "+ Create a new playlist on spotify named after the source") {
		t.Fatalf("%q %q", log.titles, log.labels)
	}
}
