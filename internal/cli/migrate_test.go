package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// catalogISRC:把這些 id 登進假 Spotify 的目錄(ISRC 反查用),ISRC 跟 fakeTrackJSON 的一致——apple 那邊同 id 的曲目就會對上。
func catalogISRC(fs *fakeSpotify, ids ...string) {
	for _, id := range ids {
		fs.addCatalog(fakeCatalogTrack{ID: id, Name: "song-" + id, ISRC: fakeISRC(id)})
	}
}

// 新建目標:來源的順序原封不動、來源自己的重複只留一份;dry-run / 非 TTY 不建清單;完成後只有目標連著正本;
// 再跑撞同名指路 --to spotify:<id>;指到既有的那個「都已在」零寫入;之後 sync 零變更。
func TestMigrateNewPlaylistKeepsSourceOrderAndSkipsDuplicates(t *testing.T) {
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "a", "b", "c")
	fs2.set("q1", "公路旅行", "a", "b", "a", "c")
	filesBefore := driveFiles(t, dc)
	out, _, err := runPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify", "--dry-run")
	if exitOf(t, err) != 2 || !slices.Equal(dirActions(out), []string{"migrate add apple", "migrate add apple", "migrate add apple"}) {
		t.Fatalf("dry-run:%v\n%s", err, out)
	}
	if !strings.Contains(out, "migrate\tadd\tapple\t公路旅行\t0\t"+fakeCID("a")+"\ta\tsong-a\tartist\t推到 spotify:a(isrc 95)\tpush\n") || !strings.Contains(out, "\t2\t"+fakeCID("c")+"\tc\t") {
		t.Fatalf("migrate 列:pos 是正本位置、reason 說推到哪:\n%s", out)
	}
	if _, _, err := runPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify"); exitOf(t, err) != 2 {
		t.Fatalf("非 TTY 沒 --yes 要 exit 2:%v", err)
	}
	if _, _, err := runPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify", "--dry-run", "--yes"); exitOf(t, err) != 2 { // --dry-run 永遠不寫,--yes 也不放行
		t.Fatalf("--dry-run --yes 仍是 exit 2:%v", err)
	}
	if fs1.createdCount() != 0 || len(fs1.written()) != 0 || !sameFiles(filesBefore, driveFiles(t, dc)) {
		t.Fatal("dry-run / 待確認:不建清單、不寫平台、不寫 Drive")
	}
	out, errs := mustPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify", "--yes")
	if !slices.Equal(dirActions(out), []string{"migrate add apple", "migrate add apple", "migrate add apple"}) || !slices.Equal(fs1.tracksOf("new1"), []string{"a", "b", "c"}) {
		t.Fatalf("新清單 = 來源順序、重複只留一份:%v\n%s", fs1.tracksOf("new1"), out)
	}
	for _, want := range []string{"建立 canonical 清單 公路旅行(", "在 spotify 建立清單 公路旅行(new1)", "已推送 3 筆變更", "已把 apple:q1 的 3 首接進正本 公路旅行(", "推了 3 首到 spotify:new1", "來源沒有連結", "capy pl link \"公路旅行\" apple:q1"} {
		if !strings.Contains(errs, want) {
			t.Fatalf("stderr 少了 %q:\n%s", want, errs)
		}
	}
	pl := drivePlaylistNamed(t, dc, "公路旅行")
	if !slices.Equal(cidsOf(pl), []string{fakeCID("a"), fakeCID("b"), fakeCID("c")}) || len(pl.Links) != 1 || pl.Links["spotify"] != "new1" {
		t.Fatalf("正本 [a b c]、只連 spotify:new1:%v %v", cidsOf(pl), pl.Links)
	}
	if _, _, err := runPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify", "--yes"); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "--to spotify:new1") || fs1.createdCount() != 1 {
		t.Fatalf("再跑撞同名要指路既有的:%v", err)
	}
	files := driveFiles(t, dc)
	if out, errs := mustPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify:new1", "--yes"); out != "" || !strings.Contains(errs, "都已在正本 公路旅行(") || !sameFiles(files, driveFiles(t, dc)) {
		t.Fatalf("都已在:零寫入:%q %q", out, errs)
	}
	if out, errs := mustPull(t, "pl", "sync", "公路旅行", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("之後 sync 零變更:%s%s", out, errs)
	}
}

// 加進既有清單:目標原本的順序是前綴(item 逐位元不變)、來源裡目標已有的略過、其餘依來源順序接在後面;沿用目標連著的正本,
// 不多建一個;來源不連結。
func TestMigrateIntoExistingKeepsTargetOrderAsPrefix(t *testing.T) {
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "x", "y", "z", "w", "v")
	fs1.set("p1", "通勤", "x", "y", "z")
	fs2.set("q1", "精選", "z", "w", "x", "v")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	before := drivePlaylistNamed(t, dc, "通勤")
	out, errs := mustPull(t, "migrate", "精選", "--from", "apple", "--to", "spotify:通勤", "--yes")
	if !slices.Equal(dirActions(out), []string{"migrate add apple", "migrate add apple", "push add spotify", "push add spotify"}) || !strings.Contains(errs, "沿用 spotify:p1 連著的 canonical 清單 通勤") {
		t.Fatalf("一輪:\n%s%s", out, errs)
	}
	if !slices.Equal(fs1.tracksOf("p1"), []string{"x", "y", "z", "w", "v"}) {
		t.Fatalf("目標順序是前綴、來源的接在後面:%v", fs1.tracksOf("p1"))
	}
	after := drivePlaylistNamed(t, dc, "通勤")
	if after.PID != before.PID || !slices.Equal(cidsOf(after), []string{fakeCID("x"), fakeCID("y"), fakeCID("z"), fakeCID("w"), fakeCID("v")}) || len(after.Links) != 1 {
		t.Fatalf("同一個正本、只連 spotify:%v %v", cidsOf(after), after.Links)
	}
	for i := range 3 {
		if after.Items[i] != before.Items[i] {
			t.Fatalf("目標原本的 item %d 要逐位元不變:%+v ≠ %+v", i, after.Items[i], before.Items[i])
		}
	}
	if out, errs := mustPull(t, "pl", "sync", "通勤", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("之後 sync 零變更:%s%s", out, errs)
	}
	// 沒東西可接時不退化成 sync:目標又加了一首,migrate 只說留給 sync、零寫入
	fs1.set("p1", "通勤", "x", "y", "z", "w", "v", "u")
	files := driveFiles(t, dc)
	if out, errs := mustPull(t, "migrate", "精選", "--from", "apple", "--to", "spotify:通勤", "--yes"); out != "" || !strings.Contains(errs, "都已在正本 通勤(") || !strings.Contains(errs, "(pull 半邊看到 1 筆平台變更,留給 capy pl sync)") || !sameFiles(files, driveFiles(t, dc)) {
		t.Fatalf("沒東西可接:零寫入、指路 sync:%q %q", out, errs)
	}
}

// push 失敗(平台 403)時 COMMIT 照走(正本已接上、連結已記),但結尾要以那個錯收場:exit 1、不講成功。修之前印成功並 exit 0。
func TestMigratePushFailureExitsNonZero(t *testing.T) {
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "x", "y")
	fs1.set("p1", "通勤", "x")
	fs2.set("q1", "精選", "y")
	fs1.setWriteStatus(403)
	out, errs, err := runPull(t, "migrate", "精選", "--from", "apple", "--to", "spotify:p1", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(errs, "寫入 通勤 的 spotify 失敗") || strings.Contains(errs, "接進正本") || strings.Contains(out, "\tskip\t") {
		t.Fatalf("push 失敗要 exit 1、不講成功:%v\n%s%s", err, out, errs)
	}
	if !slices.Equal(fs1.tracksOf("p1"), []string{"x"}) || !slices.Equal(cidsOf(drivePlaylistNamed(t, dc, "通勤")), []string{fakeCID("x"), fakeCID("y")}) {
		t.Fatalf("平台沒動、正本已接上(下次 sync 補推):%v", fs1.tracksOf("p1"))
	}
	fs1.setWriteStatus(0)
	if out, _ := mustPull(t, "pl", "sync", "通勤", "--yes"); !slices.Equal(dirActions(out), []string{"push add spotify"}) || !slices.Equal(fs1.tracksOf("p1"), []string{"x", "y"}) {
		t.Fatalf("sync 補推:%s %v", out, fs1.tracksOf("p1"))
	}
}

// 剛建的清單不在第二次 list 裡(真帳號沒驗過的路徑):不能靜靜當成功;錯誤要帶 pl link 接回的命令、Drive 零寫入。
func TestMigrateCreatedPlaylistMissingFromListIsRecoverable(t *testing.T) {
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "a")
	fs2.set("q1", "公路旅行", "a")
	fs1.setHook(func() { // 持鎖中被呼叫:直接動 lists,不能再 lock
		if i := fs1.index("new1"); i >= 0 {
			fs1.lists = append(fs1.lists[:i], fs1.lists[i+1:]...)
		}
	})
	_, _, err := runPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "看不到剛建的清單 new1") || !strings.Contains(err.Error(), "capy pl link \"公路旅行\" spotify:new1 接回來") {
		t.Fatalf("要指路接回:%v", err)
	}
	if fs1.createdCount() != 1 || len(driveFiles(t, dc)) != 0 {
		t.Fatal("清單建了一個、Drive 零寫入")
	}
}

// 目標既有但沒連結:正本以目標的名字建(不然 push 會多排一個 rename 把使用者的清單改名);來源的重複只留一份。
func TestMigrateIntoUnlinkedExistingNamesCanonicalAfterTarget(t *testing.T) {
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "x", "y")
	fs1.set("p1", "通勤", "x")
	fs2.set("q1", "精選", "y", "y")
	_, errs := mustPull(t, "migrate", "精選", "--from", "apple", "--to", "spotify:p1", "--yes")
	if !strings.Contains(errs, "建立 canonical 清單 通勤(") || !strings.Contains(errs, "已連結 通勤(") {
		t.Fatalf("正本叫目標的名字:%s", errs)
	}
	if !slices.Equal(fs1.tracksOf("p1"), []string{"x", "y"}) || slices.ContainsFunc(fs1.written(), func(w fakeWrite) bool { return w.Name != "" && w.Path == "/playlists/p1" }) {
		t.Fatalf("[x y]、沒有 rename:%v %+v", fs1.tracksOf("p1"), fs1.written())
	}
	if pl := drivePlaylistNamed(t, dc, "通勤"); pl.Links["spotify"] != "p1" {
		t.Fatalf("連著 spotify:p1:%v", pl.Links)
	}
}

// README 手動流程做到一半(正本已連著來源平台):新建目標時沿用同名正本,不再建第二個同名的。
func TestMigrateAdoptsCanonicalLinkedToSource(t *testing.T) {
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "a", "b")
	fs2.set("q1", "公路旅行", "a", "b")
	mustPull(t, "pl", "link", "公路旅行", "apple:q1")
	mustPull(t, "pl", "pull", "公路旅行", "--yes")
	before := drivePlaylistNamed(t, dc, "公路旅行")
	_, errs := mustPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify", "--yes")
	if !strings.Contains(errs, "沿用既有的 canonical 清單 公路旅行(") || !strings.Contains(errs, "連著 apple:q1") {
		t.Fatalf("要沿用:%s", errs)
	}
	after := drivePlaylistNamed(t, dc, "公路旅行")
	if after.PID != before.PID || after.Links["spotify"] != "new1" || after.Links["apple"] != "q1" || !slices.Equal(fs1.tracksOf("new1"), []string{"a", "b"}) {
		t.Fatalf("同一個正本、多連 spotify:new1、既有的 item 推到新清單:%v %v", after.Links, fs1.tracksOf("new1"))
	}
}

// 正本已連著來源(手動流程做到一半):來源那半也 pull 進正本,新歌落在它在來源的真實位置;結尾說來源連著、不說一次性複製;
// 之後 sync 零變更、來源順序不動。修之前尾端追加 → 正本 [a b n] ≠ 來源 [n a b],sync 把來源重排成 [a b n](review #55 第 1 點)。
func TestMigrateSourceLinkedCanonicalFollowsSource(t *testing.T) {
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "a", "b", "n")
	fs2.set("q1", "公路旅行", "a", "b")
	mustPull(t, "pl", "link", "公路旅行", "apple:q1")
	mustPull(t, "pl", "pull", "公路旅行", "--yes")
	fs2.set("q1", "公路旅行", "n", "a", "b") // 使用者在來源最前面插一首
	out, errs := mustPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify", "--yes")
	if !slices.Equal(dirActions(out), []string{"pull add apple", "push add spotify", "push add spotify", "push add spotify"}) {
		t.Fatalf("表 = 來源的 pull + 推到新清單的三首:\n%s", out)
	}
	if !slices.Equal(fs1.tracksOf("new1"), []string{"n", "a", "b"}) || !slices.Equal(cidsOf(drivePlaylistNamed(t, dc, "公路旅行")), []string{fakeCID("n"), fakeCID("a"), fakeCID("b")}) {
		t.Fatalf("正本與新清單都是來源的順序:%v", fs1.tracksOf("new1"))
	}
	if strings.Contains(errs, "來源沒有連結") || !strings.Contains(errs, "以正本為準、pull 了 1 筆來源變更,推了 3 首到 spotify:new1") || !strings.Contains(errs, "來源 apple:q1 也連著這個正本") {
		t.Fatalf("結尾要照 links 講:%s", errs)
	}
	if out, errs := mustPull(t, "pl", "sync", "公路旅行", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") || !slices.Equal(fs2.tracksOf("q1"), []string{"n", "a", "b"}) {
		t.Fatalf("之後 sync 零變更、來源順序不動:%s%s %v", out, errs, fs2.tracksOf("q1"))
	}
}

// 新建目標時,正本既有、在目標對不到的曲目也要進表(push skip 列)、算進「沒有對應」、結尾要說;
// 修之前表是空的、結尾不說還講錯數字、exit 0(review #55 第 2 點)。
func TestMigrateNewTargetReportsUnmappedExisting(t *testing.T) {
	fs1, fs2, _, _ := twoPlatforms(t)
	catalogISRC(fs1, "a") // b 在 spotify 對不到
	fs2.set("q1", "公路旅行", "a", "b")
	mustPull(t, "pl", "link", "公路旅行", "apple:q1")
	mustPull(t, "pl", "pull", "公路旅行", "--yes")
	out, errs := mustPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify", "--yes")
	if !slices.Equal(dirActions(out), []string{"push add spotify", "push skip spotify"}) || !strings.Contains(out, "\t"+fakeCID("b")+"\t\tsong-b\tartist\tspotify 沒有對應,這次不推\tno_mapping\n") {
		t.Fatalf("既有的 b 要列成 skip:\n%s", out)
	}
	if !strings.Contains(errs, "1 首在 spotify 沒有對應、這次沒推:capy resolve \"公路旅行\" --provider spotify --review") || !strings.Contains(errs, "推了 1 首到 spotify:new1") || strings.Contains(errs, "一起推到新清單") {
		t.Fatalf("結尾要說沒推的那首、數字要是真的:%s", errs)
	}
	if !slices.Equal(fs1.tracksOf("new1"), []string{"a"}) {
		t.Fatalf("new1:%v", fs1.tracksOf("new1"))
	}
}

// 挑選器選到讀不到的清單:同 --to 路徑以 exit 1 擋下、零寫入;修之前落到 push 前提一的 exit 3,指路的 pl pull 一樣讀不到(review #55 第 3 點)。
func TestMigratePickerRefusesUnreadableTarget(t *testing.T) {
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "a")
	fs2.set("q1", "公路旅行", "a")
	fs1.set("p1", "別人的清單", "x")
	fs1.mu.Lock()
	fs1.restricted["p1"] = true
	fs1.mu.Unlock()
	stubPickers(t, 1, 0, 0, 0) // apple → q1 → spotify → p1
	_, _, err := runPull(t, "migrate", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "spotify 清單 p1 讀不到內容") || len(fs1.written()) != 0 || len(driveFiles(t, dc)) != 0 {
		t.Fatalf("挑選器路徑也要過 readable:%v", err)
	}
}

// 新建目標時同名的正本連著來源平台的另一份清單:沿用會把這份的歌推去那份,擋下指路。
func TestMigrateRefusesCanonicalLinkedToOtherSourcePlaylist(t *testing.T) {
	fs1, fs2, _, _ := twoPlatforms(t)
	catalogISRC(fs1, "a", "b")
	fs2.set("q1", "公路旅行", "a")
	fs2.set("q7", "舊版", "b")
	mustPull(t, "pl", "link", "公路旅行", "apple:q7") // 正本「公路旅行」連著 apple:q7
	_, _, err := runPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "連著 apple:q7,不是來源 apple:q1") || fs1.createdCount() != 0 {
		t.Fatalf("要擋:%v", err)
	}
}

// 沒對到的曲目這次不推、表裡與結尾都說;之後 resolve pin + pl sync 補上。
func TestMigrateUnmappedTrackSkippedThenResolved(t *testing.T) {
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "a")
	fs2.set("q1", "公路旅行", "a", "n")
	out, errs := mustPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify", "--yes")
	if !strings.Contains(out, "\t"+fakeCID("n")+"\tn\tsong-n\tartist\tspotify 沒有對應,這次不推\tno_mapping\n") || !slices.Equal(fs1.tracksOf("new1"), []string{"a"}) {
		t.Fatalf("n 沒對到、不推:\n%s%v", out, fs1.tracksOf("new1"))
	}
	if !strings.Contains(errs, "resolve:1 首自動對應到 spotify,1 首要人裁決") || !strings.Contains(errs, "1 首在 spotify 沒有對應、這次沒推:capy resolve \"公路旅行\" --provider spotify --review") {
		t.Fatalf("結尾要指路:%s", errs)
	}
	fs1.addCatalog(fakeCatalogTrack{ID: "n2", Name: "song-n", ISRC: "XX0000000000"})
	mustPull(t, "resolve", "pin", fakeCID("n"), "spotify:n2", "--yes")
	if out, _ := mustPull(t, "pl", "sync", "公路旅行", "--yes"); !slices.Equal(dirActions(out), []string{"push add spotify"}) || !slices.Equal(fs1.tracksOf("new1"), []string{"a", "n2"}) {
		t.Fatalf("釘選後 sync 補上:%s %v", out, fs1.tracksOf("new1"))
	}
	_ = dc
}

// 終端機裡:沒對到的先問要不要當場裁決,再問要不要搬;裁決成不可得的那首這次不推。取消確認什麼都不建。
func TestMigrateOffersReviewInTTYAndCancelCreatesNothing(t *testing.T) {
	fs1, fs2, dc, _ := twoPlatforms(t)
	catalogISRC(fs1, "a")
	fs2.set("q1", "公路旅行", "a", "n")
	origTTY, origConfirm, origPrompt := migrateIsTTY, confirmWrite, reviewPrompt
	var prompts []string
	answer := true
	migrateIsTTY = func(*cobra.Command) bool { return true }
	confirmWrite = func(_, p string) (bool, error) { prompts = append(prompts, p); return answer, nil }
	reviewPrompt = func(it resolveItem, pos, total int, search func(string) ([]provider.Track, error)) (reviewDecision, error) {
		return reviewDecision{kind: "none"}, nil
	}
	t.Cleanup(func() { migrateIsTTY, confirmWrite, reviewPrompt = origTTY, origConfirm, origPrompt })
	answer = false
	if _, _, err := runPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify"); exitOf(t, err) != 2 || fs1.createdCount() != 0 || len(driveFiles(t, dc)) != 0 {
		t.Fatalf("取消:exit 2、不建清單、不寫 Drive:%v", err)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[0], "1 首在 spotify 沒有自動對應到,現在逐筆裁決?") || !strings.Contains(prompts[1], "在 spotify 建立清單「公路旅行」,把 apple:q1 的 2 首推過去?(其中 1 首在 spotify 沒有對應,這次不推)") {
		t.Fatalf("兩個提示:%q", prompts)
	}
	prompts, answer = nil, true
	out, errs := mustPull(t, "migrate", "公路旅行", "--from", "apple", "--to", "spotify")
	if !strings.Contains(errs, "裁決 1 筆") || !strings.Contains(out, "\tspotify 沒有對應,這次不推\tno_mapping\n") || !slices.Equal(fs1.tracksOf("new1"), []string{"a"}) {
		t.Fatalf("當場裁決成不可得:\n%s%s", out, errs)
	}
	if m := driveTracks(t, dc).Tracks[fakeCID("n")].Mappings["spotify"]; !m.Pinned || m.ID != "" {
		t.Fatalf("n 釘成不可得:%+v", m)
	}
}

// 不帶參數 + 終端機:逐段挑選(來源平台 → 清單 → 目標平台 → 既有的或建新的)。
func TestMigrateWizard(t *testing.T) {
	fs1, fs2, _, _ := twoPlatforms(t)
	catalogISRC(fs1, "a", "b")
	fs2.set("q1", "公路旅行", "a", "b")
	fs1.set("p1", "通勤", "x")
	log := stubPickers(t, 1, 0, 0, 1) // apple → q1 → spotify → 「建一個跟來源同名的新清單」
	mustPull(t, "migrate", "--yes")
	if len(log.titles) != 4 || !strings.Contains(log.titles[0], "從哪個平台搬") || !strings.Contains(log.titles[2], "搬到哪個平台") || !strings.Contains(log.labels[3][1], "建一個跟來源同名的新清單") {
		t.Fatalf("四段挑選:%q %q", log.titles, log.labels)
	}
	if !slices.Equal(fs1.tracksOf("new1"), []string{"a", "b"}) {
		t.Fatalf("搬到新清單:%v", fs1.tracksOf("new1"))
	}
}

func TestMigrateRefusals(t *testing.T) {
	fs1, fs2, _, _ := twoPlatforms(t)
	catalogISRC(fs1, "a", "x", "y", "z")
	fs2.set("q1", "公路旅行", "a")
	fs1.set("p1", "公路旅行", "a") // 目標平台上已經有同名、連得上的清單
	fs2.set("q9", "空")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"migrate", "公路旅行", "--from", "apple", "--to", "spotify"}, "--to spotify:p1"},
		{[]string{"migrate", "公路旅行"}, "--from 與 --to 必填"},
		{[]string{"migrate", "公路旅行", "--from", "apple", "--to", "nope"}, "--to 為"},
		{[]string{"migrate", "公路旅行", "--from", "nope", "--to", "spotify"}, "--from 為"},
		{[]string{"migrate", "沒這個清單", "--from", "apple", "--to", "spotify"}, "找不到名為"},
		{[]string{"migrate", "公路旅行", "--from", "apple", "--to", "spotify:沒這個"}, "找不到名為"},
	} {
		if _, _, err := runPull(t, c.args...); exitOf(t, err) != 1 || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%v 要 exit 1 含 %q:%v", c.args, c.want, err)
		}
	}
	if _, _, err := runPull(t, "migrate"); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "accepts 1 arg(s)") { // 非 TTY 沒有挑選器
		t.Fatalf("非 TTY 不帶參數:%v", err)
	}
	if _, errs := mustPull(t, "migrate", "空", "--from", "apple", "--to", "spotify", "--yes"); !strings.Contains(errs, "apple:q9 是空的") || fs1.createdCount() != 0 {
		t.Fatalf("空來源:%s", errs)
	}
	// 只讀的平台不能當目標
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		p, err := orig(ctx, id)
		if id == "apple" && err == nil {
			return readOnlyProvider{p, p.(provider.PlaylistReader)}, nil
		}
		return p, err
	}
	t.Cleanup(func() { newProvider = orig })
	fs1.set("p2", "通勤", "x")
	if _, _, err := runPull(t, "migrate", "通勤", "--from", "spotify", "--to", "apple"); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "apple 目前只讀,不能當 migrate 的目標") {
		t.Fatalf("只讀目標:%v", err)
	}
	newProvider = orig
	// 目標有還沒同步的移除:migrate 只做新增,擋下、指路 pl sync
	fs1.set("p3", "混音", "x", "y", "z")
	fs2.set("q3", "混音", "x", "y", "z")
	fs2.set("q4", "來源", "a")
	mustPull(t, "pl", "link", "混音", "spotify:p3")
	mustPull(t, "pl", "link", "混音", "apple:q3")
	mustPull(t, "pl", "sync", "混音", "--yes")
	fs2.set("q3", "混音", "x", "y") // apple 刪了 z
	mustPull(t, "pl", "pull", "混音", "--yes", "--provider", "apple")
	if _, _, err := runPull(t, "migrate", "來源", "--from", "apple", "--to", "spotify:混音", "--yes"); exitOf(t, err) != 3 || !strings.Contains(err.Error(), "有還沒同步的 remove(migrate 只做新增)") || !slices.Equal(fs1.tracksOf("p3"), []string{"x", "y", "z"}) {
		t.Fatalf("待同步的移除要擋、平台不動:%v %v", err, fs1.tracksOf("p3"))
	}
}
