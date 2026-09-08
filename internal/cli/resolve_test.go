package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/drive/drivetest"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func fakeISRC(id string) string { return strings.TrimPrefix(fakeCID(id), "i:") }

// resolveWorld:通勤清單從假 Spotify pull 進來(a、b),再連結 apple:p1 但不 pull apple——items 缺 apple mapping,給 resolve 用。
// newProvider 被 swap 成不看 provider id 的同一個假伺服器,所以「apple」的 ISRC 反查 / 搜尋 / 單曲都由 catalog 回。
func resolveWorld(t *testing.T) (*fakeSpotify, *drive.Client, *drivetest.Server) {
	t.Helper()
	fs, dc, srv := pullWorld(t)
	fs.set("p1", "通勤", "a", "b")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	mustPull(t, "pl", "link", "通勤", "apple:p1")
	return fs, dc, srv
}

func driveTracks(t *testing.T, dc *drive.Client) *canon.Tracks {
	t.Helper()
	return decodeFile[canon.Tracks](t, driveFiles(t, dc), "tracks.json")
}

// countUploads:之後每個 Drive 寫入(POST / PATCH)都計數;predicate 回 false 所以不失敗。
func countUploads(srv *drivetest.Server) *int {
	n := 0
	srv.FailOn(func(r *http.Request) bool {
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			n++
		}
		return false
	}, 0, "")
	return &n
}

func TestResolveAutoWritesISRCMappings(t *testing.T) {
	fs, dc, srv := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")})
	fs.addCatalog(fakeCatalogTrack{ID: "ap-b", Name: "song-b", ISRC: fakeISRC("b")})
	before := driveFiles(t, dc)
	out, _, err := runPull(t, "resolve")
	want := "map\t" + fakeCID("a") + "\tapple\tap-a\t95\tisrc\tsong-a\tartist\tISRC 反查\nmap\t" + fakeCID("b") + "\tapple\tap-b\t95\tisrc\tsong-b\tartist\tISRC 反查\n"
	if exitOf(t, err) != 2 || out != want {
		t.Fatalf("非 TTY 沒 --yes → exit 2、TSV:%d %q", exitOf(t, err), out)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("沒確認就零寫入")
	}
	if _, _, err := runPull(t, "resolve", "--dry-run"); exitOf(t, err) != 2 || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("--dry-run:exit 2、零寫入:%v", err)
	}
	orig := canon.Now
	canon.Now = func() time.Time { return time.Unix(1_800_000_000, 0) } // 撥鐘:manifest 的 last_seen 才會動,上傳數才可預測
	t.Cleanup(func() { canon.Now = orig })
	uploads := countUploads(srv)
	out, errs := mustPull(t, "resolve", "--yes")
	if out != want || !strings.Contains(errs, "寫入 2 筆 mapping") {
		t.Fatalf("--yes:%q\n%s", out, errs)
	}
	after := driveFiles(t, dc)
	for name := range after {
		changed := !bytes.Equal(after[name], before[name])
		if (name == "tracks.json") != changed && name != "manifest.json" { // manifest 的 last_seen 隨任何上傳而動
			t.Fatalf("只有 tracks.json(與 manifest)該變:%s changed=%t", name, changed)
		}
	}
	if *uploads != 2 {
		t.Fatalf("上傳 tracks.json + manifest.json = 2 次:%d", *uploads)
	}
	tr := driveTracks(t, dc).Tracks[fakeCID("a")]
	if m := tr.Mappings["apple"]; m.ID != "ap-a" || m.Confidence != 95 || m.Source != canon.SourceISRC || m.Pinned || m.UpdatedAt == 0 {
		t.Fatalf("Layer 1 mapping:%+v", m)
	}
	if !slices.Equal(tr.ISRC, []string{fakeISRC("a")}) {
		t.Fatalf("自動寫入不動 alias set:%v", tr.ISRC)
	}
	canon.Now = func() time.Time { return time.Unix(1_800_000_060, 0) }
	*uploads = 0
	out, errs = mustPull(t, "resolve", "--yes")
	if out != "" || *uploads != 0 || !strings.Contains(errs, "沒有可自動寫入的 mapping") {
		t.Fatalf("第二次:沒東西、零上傳:%q %d\n%s", out, *uploads, errs)
	}
}

func TestResolveFuzzyThresholdAndReviewQueueExitZero(t *testing.T) {
	fs, dc, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a2", Name: "song-a", ISRC: "TW00000000ZZ"})            // ISRC 不同 → Layer 1 miss,fuzzy 100 → map
	fs.addCatalog(fakeCatalogTrack{ID: "ap-b-live", Name: "song-b (Live)", ISRC: "TW00000000ZY"}) // 只有 Live 版 → 84 → review
	out, errs := mustPull(t, "resolve", "--yes")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] != "map\t"+fakeCID("a")+"\tapple\tap-a2\t100\tfuzzy\tsong-a\tartist\tfuzzy 100 分:song-a — artist(3:20)" ||
		!strings.HasPrefix(lines[1], "review\t"+fakeCID("b")+"\tapple\tap-b-live\t84\tfuzzy\tsong-b\tartist\t84 分 < 85:候選 song-b (Live)") {
		t.Fatalf("佇列:%q", out)
	}
	if !strings.Contains(errs, "寫入 1 筆 mapping") || !strings.Contains(errs, "1 筆待人工裁決") {
		t.Fatalf("stderr:%s", errs)
	}
	tracks := driveTracks(t, dc)
	if m := tracks.Tracks[fakeCID("a")].Mappings["apple"]; m.ID != "ap-a2" || m.Confidence != 100 || m.Source != canon.SourceFuzzy {
		t.Fatalf("fuzzy mapping:%+v", m)
	}
	if _, has := tracks.Tracks[fakeCID("b")].Mappings["apple"]; has {
		t.Fatal("<85 不寫")
	}
}

func TestResolveCandidateOwnedByOtherCidGoesToReview(t *testing.T) {
	fs, dc, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")})
	mustPull(t, "resolve", "--yes")
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("b")}) // 平台把 b 的 ISRC 也對到 ap-a:候選已屬 a
	out, _ := mustPull(t, "resolve", "--yes")
	if !strings.HasPrefix(out, "review\t"+fakeCID("b")+"\tapple\tap-a\t95\tisrc\tsong-b\tartist\t候選 song-a — artist(3:20) 已屬 cid "+fakeCID("a")) {
		t.Fatalf("已屬另一 cid → review、不自動合併:%q", out)
	}
	if _, has := driveTracks(t, dc).Tracks[fakeCID("b")].Mappings["apple"]; has {
		t.Fatal("不寫")
	}
}

func TestResolvePinNoneAndPinMerge(t *testing.T) {
	fs, dc, _ := resolveWorld(t)
	cidA, cidB := fakeCID("a"), fakeCID("b")
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")})
	mustPull(t, "resolve", "--yes")
	if _, errs := mustPull(t, "resolve", "pin", cidB, "apple:none"); !strings.Contains(errs, "不可得") {
		t.Fatalf("pin none:%s", errs)
	}
	if m := driveTracks(t, dc).Tracks[cidB].Mappings["apple"]; !m.Pinned || m.ID != "" || m.Source != canon.SourceReview || m.Confidence != 100 {
		t.Fatalf("釘成不可得:%+v", m)
	}
	if out, _ := mustPull(t, "resolve", "--yes"); out != "" {
		t.Fatalf("釘成不可得後 Needs 排除它:%q", out)
	}
	before := driveFiles(t, dc)
	if _, _, err := runPull(t, "resolve", "pin", cidB, "apple:ap-a"); exitOf(t, err) != 2 || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("id 已屬 a、非 TTY 沒 --yes → exit 2 零寫入:%v", err)
	}
	if _, _, err := runPull(t, "resolve", "pin", cidA, "apple:nope"); exitOf(t, err) != 1 || !sameFiles(before, driveFiles(t, dc)) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("不存在的 id → exit 1 零寫入:%v", err)
	}
	_, errs := mustPull(t, "resolve", "pin", cidB, "apple:ap-a", "--yes")
	if !strings.Contains(errs, "已合併,勝者 "+cidA) {
		t.Fatalf("合併:%s", errs)
	}
	files := driveFiles(t, dc)
	tracks := decodeFile[canon.Tracks](t, files, "tracks.json")
	if tracks.Merged[cidB] != cidA {
		t.Fatalf("墓碑:%v", tracks.Merged)
	}
	a := tracks.Tracks[cidA]
	if m := a.Mappings["apple"]; !m.Pinned || m.ID != "ap-a" || m.Source != canon.SourceReview {
		t.Fatalf("釘在勝者上:%+v", m)
	}
	if !slices.Equal(a.ISRC, []string{fakeISRC("a"), fakeISRC("b")}) {
		t.Fatalf("alias set 聯集:%v", a.ISRC)
	}
	if !slices.ContainsFunc(a.Conflicts, func(c canon.Conflict) bool { return c.Provider == "spotify" && c.ProviderID == "b" }) {
		t.Fatalf("敗者的 spotify id 進 conflicts:%+v", a.Conflicts)
	}
	pl := decodeFile[canon.Playlist](t, files, "pl__")
	for _, it := range pl.Items {
		if it.CID != cidA {
			t.Fatalf("清單 item 全指勝者:%+v", pl.Items)
		}
	}
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); out != "" {
		t.Fatalf("合併後平台照舊 → 零變更:%q", out)
	}
}

func TestResolveReviewNonTTYPrintsQueueExit2ZeroWrites(t *testing.T) {
	fs, dc, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-b-live", Name: "song-b (Live)", ISRC: "TW00000000ZY"})
	before := driveFiles(t, dc)
	out, _, err := runPull(t, "resolve", "--review")
	if exitOf(t, err) != 2 || !strings.Contains(err.Error(), "需要終端機") || strings.Count(out, "\n") != 2 || !strings.HasPrefix(out, "review\t"+fakeCID("a")+"\tapple\t\t\t\tsong-a\tartist\t找不到候選\n") {
		t.Fatalf("非 TTY --review:%v %q", err, out)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("零寫入")
	}
}

func stubReview(t *testing.T, decide func(it resolveItem, search func(string) ([]provider.Track, error)) reviewDecision) {
	t.Helper()
	origTTY, origPrompt := reviewIsTTY, reviewPrompt
	reviewIsTTY = func(*cobra.Command) bool { return true }
	reviewPrompt = func(it resolveItem, pos, total int, search func(string) ([]provider.Track, error)) (reviewDecision, error) {
		return decide(it, search), nil
	}
	t.Cleanup(func() { reviewIsTTY, reviewPrompt = origTTY, origPrompt })
}

func TestResolveReviewDecisionsViaSeam(t *testing.T) {
	fs, dc, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-b-live", Name: "song-b (Live)", ISRC: "TW00000000ZY"})
	stubReview(t, func(it resolveItem, search func(string) ([]provider.Track, error)) reviewDecision {
		switch it.cid {
		case fakeCID("a"):
			return reviewDecision{kind: "none"}
		default: // manual:用搜尋拿候選
			found, err := search("song b")
			if err != nil || len(found) != 1 {
				t.Fatalf("manual 搜尋:%v %d", err, len(found))
			}
			return reviewDecision{kind: "manual", cand: &found[0]}
		}
	})
	_, errs := mustPull(t, "resolve", "--review")
	if !strings.Contains(errs, "裁決 2 筆") || !strings.Contains(errs, "song-a:釘成不可得") || !strings.Contains(errs, "song-b:釘選 ap-b-live") {
		t.Fatalf("stderr:%s", errs)
	}
	tracks := driveTracks(t, dc)
	if m := tracks.Tracks[fakeCID("a")].Mappings["apple"]; !m.Pinned || m.ID != "" {
		t.Fatalf("none:%+v", m)
	}
	b := tracks.Tracks[fakeCID("b")]
	if m := b.Mappings["apple"]; !m.Pinned || m.ID != "ap-b-live" || m.Source != canon.SourceReview || m.Confidence != 100 {
		t.Fatalf("manual:%+v", m)
	}
	if !slices.Equal(b.ISRC, []string{fakeISRC("b"), "TW00000000ZY"}) { // 排序後
		t.Fatalf("人工釘選讓 alias set 成長:%v", b.ISRC)
	}
	if out, _ := mustPull(t, "resolve", "--yes"); out != "" {
		t.Fatalf("都 pinned 了:%q", out)
	}
	// accept 對到已屬另一 cid 的 id、非 --yes 且確認不同意 → 當略過,不中斷
	fs.addCatalog(fakeCatalogTrack{ID: "ap-b-live", Name: "song-b (Live)", ISRC: fakeISRC("a")}) // 同一 id 也回應 a 的 ISRC
	mustPull(t, "resolve", "pin", fakeCID("a"), "apple:none")
	stubReview(t, func(it resolveItem, _ func(string) ([]provider.Track, error)) reviewDecision {
		return reviewDecision{kind: "accept", cand: it.cand}
	})
	origConfirm := confirmWrite
	confirmWrite = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { confirmWrite = origConfirm })
	// a 釘成不可得後不在佇列;把 a 的釘選拿掉再讓它進佇列:直接改 Drive
	tr := driveTracks(t, dc)
	ta := tr.Tracks[fakeCID("a")]
	delete(ta.Mappings, "apple")
	tr.Tracks[fakeCID("a")] = ta
	putTracks(t, dc, tr)
	before := driveFiles(t, dc)
	_, errs = mustPull(t, "resolve", "--review")
	if !strings.Contains(errs, "song-a:略過(未合併)") || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("不同意合併 = 略過、零寫入:%s", errs)
	}
}

// putTracks:把 tracks 直接寫回假 Drive(模擬別台裝置 / 手改)。
func putTracks(t *testing.T, dc *drive.Client, tr *canon.Tracks) {
	t.Helper()
	body, err := canon.Encode(tr)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	files, err := dc.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name == "tracks.json" {
			if _, err := dc.Update(ctx, f.ID, canon.TracksFile().Props, body); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("Drive 沒有 tracks.json")
}

func TestResolveConflictRowsAndKeep(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	tr := driveTracks(t, dc)
	ta := tr.Tracks[fakeCID("a")]
	ta.Conflicts = append(ta.Conflicts, canon.Conflict{Provider: "spotify", ProviderID: "a-live", Title: "song-a (Live)", DurationMS: 250000})
	tr.Tracks[fakeCID("a")] = ta
	putTracks(t, dc, tr)
	out, _ := mustPull(t, "resolve", "--yes")
	if out != "conflict\t"+fakeCID("a")+"\tspotify\ta\t100\tobserved\tsong-a\tartist\t同 ISRC 觀測到不同 id:a-live(song-a (Live),4:10);--review 的 keep 釘住現有 mapping\n" {
		t.Fatalf("來源 (c):%q", out)
	}
	stubReview(t, func(it resolveItem, _ func(string) ([]provider.Track, error)) reviewDecision {
		return reviewDecision{kind: "keep"}
	})
	if _, errs := mustPull(t, "resolve", "--review"); !strings.Contains(errs, "釘住現有 mapping a") {
		t.Fatalf("keep:%s", errs)
	}
	if m := driveTracks(t, dc).Tracks[fakeCID("a")].Mappings["spotify"]; !m.Pinned || m.ID != "a" || m.Source != canon.SourceReview {
		t.Fatalf("keep = 釘住現有:%+v", m)
	}
	if out, _ := mustPull(t, "resolve", "--yes"); out != "" {
		t.Fatalf("pinned 的 conflicts 不再浮現(Q14):%q", out)
	}
}

func TestResolveAPICallHint(t *testing.T) {
	fs, _, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")})
	orig := apiCallHint
	apiCallHint = 1
	t.Cleanup(func() { apiCallHint = orig })
	_, errs, _ := runPull(t, "resolve", "--dry-run")
	if !strings.Contains(errs, "次 API(超過 1)") {
		t.Fatalf("超過門檻要提醒:%s", errs)
	}
}

// T2b 延後的注入測試:pin 觸發合併,COMMIT 傳完 tracks.json、第二個 upload(pl__)失敗 → exit 1;下一次 pull 自癒。
func TestResolvePinMergeUploadFailureThenPullHeals(t *testing.T) {
	fs, dc, srv := resolveWorld(t)
	cidA, cidB := fakeCID("a"), fakeCID("b")
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")})
	mustPull(t, "resolve", "--yes")
	n := 0
	srv.FailOn(func(r *http.Request) bool {
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			n++
			return n == 2
		}
		return false
	}, http.StatusInternalServerError, "boom")
	if _, _, err := runPull(t, "resolve", "pin", cidB, "apple:ap-a", "--yes"); exitOf(t, err) != 1 {
		t.Fatalf("第二個 upload 失敗 → exit 1:%v", err)
	}
	srv.FailOn(func(*http.Request) bool { return false }, 0, "")
	files := driveFiles(t, dc)
	if decodeFile[canon.Tracks](t, files, "tracks.json").Merged[cidB] != cidA {
		t.Fatal("tracks.json 已合併(第一個 upload 成功)")
	}
	if pl := decodeFile[canon.Playlist](t, files, "pl__"); !slices.ContainsFunc(pl.Items, func(it canon.Item) bool { return it.CID == cidB }) {
		t.Fatal("清單還指著敗者(第二個 upload 失敗)")
	}
	out, errs := mustPull(t, "pl", "pull", "通勤", "--yes")
	if out != "" || !strings.Contains(errs, "修復 1 筆") {
		t.Fatalf("下一次 pull 自癒:%q\n%s", out, errs)
	}
	if pl := decodeFile[canon.Playlist](t, driveFiles(t, dc), "pl__"); slices.ContainsFunc(pl.Items, func(it canon.Item) bool { return it.CID != cidA }) {
		t.Fatalf("修好:%+v", pl.Items)
	}
	files2, dump2 := driveFiles(t, dc), dumpBytes(t)
	deleteDB(t)
	mustPull(t, "pl", "pull", "通勤", "--yes")
	if !sameFiles(files2, driveFiles(t, dc)) || !bytes.Equal(dump2, dumpBytes(t)) {
		t.Fatal("刪 db 重建等價")
	}
}

func TestPlPullHintsUnresolved(t *testing.T) {
	resolveWorld(t)
	_, errs := mustPull(t, "pl", "pull", "通勤", "--provider", "spotify", "--yes")
	if !strings.Contains(errs, "2 首尚未對應到 apple,跑 capy resolve") {
		t.Fatalf("pull 結尾提示:%s", errs)
	}
	if _, errs := mustPull(t, "pl", "pull", "通勤", "--provider", "spotify", "--dry-run"); !strings.Contains(errs, "尚未對應到 apple") {
		t.Fatalf("--dry-run 也提示:%s", errs)
	}
}

var _ = errors.Is

// 同一輪裡兩個 cid 都選到同一個候選:第二個進 review(不然下一次 pull 就是 Identity 警告的多重歸屬)。
func TestResolveSameCandidateClaimedOnceGoesToReview(t *testing.T) {
	fs, dc, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-x", Name: "song-a song-b", ISRC: "TW00000000ZQ"}) // 兩個查詢都命中、fuzzy 都 ≥85
	out, _ := mustPull(t, "resolve", "--yes")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "map\t"+fakeCID("a")+"\tapple\tap-x\t") || !strings.HasPrefix(lines[1], "review\t"+fakeCID("b")+"\tapple\tap-x\t") || !strings.Contains(lines[1], "這一輪已配給 cid "+fakeCID("a")) {
		t.Fatalf("第二個要進 review:%q", out)
	}
	tracks := driveTracks(t, dc)
	if _, has := tracks.Tracks[fakeCID("b")].Mappings["apple"]; has || tracks.Tracks[fakeCID("a")].Mappings["apple"].ID != "ap-x" {
		t.Fatal("只寫第一個")
	}
}

// --dry-run 永不寫入:連 FETCH 自癒的合併殘留也不上傳(只有 review 列時走 errSkipCommit,不是 nil)。
func TestResolveDryRunNeverCommitsEvenWithHealResidue(t *testing.T) {
	_, dc, _ := resolveWorld(t)
	tr := driveTracks(t, dc)
	if _, err := canon.Merge(tr, nil, fakeCID("a"), fakeCID("b")); err != nil { // 清單不動 = 殘留
		t.Fatal(err)
	}
	putTracks(t, dc, tr)
	before := driveFiles(t, dc)
	_, errs, err := runPull(t, "resolve", "--dry-run")
	if exitOf(t, err) != 0 || !strings.Contains(errs, "修復 1 筆") || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("--dry-run 零寫入:%v\n%s", err, errs)
	}
}

// 同一輪 --review 裡同一個 cid 有兩筆(apple 的 review、spotify 的 conflict):第一筆 accept 把它合併掉之後,第二筆的 keep 要釘在勝者上,
// 不能寫進已經不存在的敗者(那會是 nil map panic,或把敗者復活成幽靈 track)。
func TestResolveReviewSecondDecisionFollowsTombstone(t *testing.T) {
	fs, dc, _ := resolveWorld(t)
	cidA, cidB := fakeCID("a"), fakeCID("b")
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")})
	mustPull(t, "resolve", "--yes")                                                  // a → ap-a
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("b")}) // b 的候選也是 ap-a(已屬 a)
	tr := driveTracks(t, dc)
	tb := tr.Tracks[cidB]
	tb.Conflicts = append(tb.Conflicts, canon.Conflict{Provider: "spotify", ProviderID: "b-live", Title: "song-b (Live)", DurationMS: 250000})
	tr.Tracks[cidB] = tb
	putTracks(t, dc, tr)
	stubReview(t, func(it resolveItem, _ func(string) ([]provider.Track, error)) reviewDecision {
		if it.action == "conflict" {
			return reviewDecision{kind: "keep"}
		}
		return reviewDecision{kind: "accept", cand: it.cand}
	})
	_, errs := mustPull(t, "resolve", "--review", "--yes")
	if !strings.Contains(errs, "並與另一個 cid 合併為 "+cidA) || !strings.Contains(errs, "釘住現有 mapping a") {
		t.Fatalf("先合併、再把 keep 釘在勝者上:%s", errs)
	}
	tracks := driveTracks(t, dc)
	if _, ghost := tracks.Tracks[cidB]; ghost || tracks.Merged[cidB] != cidA {
		t.Fatalf("敗者不能被復活:%v %v", ghost, tracks.Merged)
	}
	if m := tracks.Tracks[cidA].Mappings["spotify"]; !m.Pinned || m.ID != "a" || m.Source != canon.SourceReview {
		t.Fatalf("keep 釘的是勝者現在的 spotify mapping:%+v", m)
	}
}

// 同上,但第二筆是 none:pinMapping 自己要追墓碑,不然「釘成不可得」會寫進已刪除的敗者、把它復活成幽靈 track。
func TestResolveReviewNoneAfterMergeFollowsTombstone(t *testing.T) {
	fs, dc, _ := resolveWorld(t)
	cidA, cidB := fakeCID("a"), fakeCID("b")
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")})
	mustPull(t, "resolve", "--yes")
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("b")})
	tr := driveTracks(t, dc)
	tb := tr.Tracks[cidB]
	tb.Conflicts = append(tb.Conflicts, canon.Conflict{Provider: "spotify", ProviderID: "b-live", Title: "song-b (Live)", DurationMS: 250000})
	tr.Tracks[cidB] = tb
	putTracks(t, dc, tr)
	stubReview(t, func(it resolveItem, _ func(string) ([]provider.Track, error)) reviewDecision {
		if it.action == "conflict" {
			return reviewDecision{kind: "none"}
		}
		return reviewDecision{kind: "accept", cand: it.cand}
	})
	mustPull(t, "resolve", "--review", "--yes")
	tracks := driveTracks(t, dc)
	if _, ghost := tracks.Tracks[cidB]; ghost || tracks.Merged[cidB] != cidA {
		t.Fatalf("敗者不能被復活:%v %v", ghost, tracks.Merged)
	}
	if m := tracks.Tracks[cidA].Mappings["spotify"]; !m.Pinned || m.ID != "" {
		t.Fatalf("none 釘在勝者上:%+v", m)
	}
}
