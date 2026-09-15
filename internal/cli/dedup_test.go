package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// dedupWorld:spotify:p1 = [a, b, a, c, b],連結 通勤 並 pull 一輪(base 落地,push 前提成立)。
func dedupWorld(t *testing.T) (*fakeSpotify, *drive.Client) {
	t.Helper()
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a", "b", "a", "c", "b")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	return fs, dc
}

// <provider>:<清單> 只報告:同 id 與同 ISRC 都算,pos 從 0 起、指向保留的那份;不碰 Drive、不寫平台;exit 0。
func TestPlDedupReportsPlatformPlaylistWithoutDrive(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.aliasISRC("a2", "a") // a2 是 a 的另一個 id(單曲版 / 專輯版)
	fs.set("p1", "通勤", "a", "b", "a", "c", "b", "a2")
	out, errs := mustPull(t, "pl", "dedup", "spotify:通勤")
	want := "2\ta\tsong-a\tartist\t重複:與 pos 0 同 id\n4\tb\tsong-b\tartist\t重複:與 pos 1 同 id\n5\ta2\tsong-a2\tartist\t重複:與 pos 0 同 ISRC " + fakeISRC("a") + "\n"
	if out != want {
		t.Fatalf("報告:\n%s", out)
	}
	if !strings.Contains(errs, "3 份重複") || !strings.Contains(errs, "capy pl link <名稱> spotify:p1") {
		t.Fatalf("stderr 要指路 link + dedup:%s", errs)
	}
	if len(driveFiles(t, dc)) != 0 || len(fs.written()) != 0 {
		t.Fatal("報告路徑不碰 Drive、不寫平台")
	}
	fs.set("p2", "睡前", "x", "y")
	if out, errs := mustPull(t, "pl", "dedup", "spotify:p2"); out != "" || !strings.Contains(errs, "沒有重複") {
		t.Fatalf("沒有重複:%q %q", out, errs)
	}
	for _, args := range [][]string{{"pl", "dedup", "spotify:p1", "--yes"}, {"pl", "dedup", "spotify:p1", "--force"}, {"pl", "dedup", "spotify:p1", "--provider", "spotify"}} {
		if _, _, err := runPull(t, args...); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "只報告") {
			t.Fatalf("%v 要 exit 1 並說只報告:%v", args, err)
		}
	}
	fs.mu.Lock()
	fs.restricted["p1"] = true
	fs.mu.Unlock()
	if _, _, err := runPull(t, "pl", "dedup", "spotify:p1"); err == nil || !strings.Contains(err.Error(), "無法讀取這個清單的內容") {
		t.Fatalf("restricted 同 pl show 的說法:%v", err)
	}
}

// 只讀的平台只能報告、指路手動刪。
func TestPlDedupReportReadOnlyPlatformSaysManual(t *testing.T) {
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
	fs2.set("q1", "冬日暖調", "a", "a", "b")
	out, errs := mustPull(t, "pl", "dedup", "apple:冬日暖調")
	if out != "1\ta\tsong-a\tartist\t重複:與 pos 0 同 id\n" || !strings.Contains(errs, "apple 目前只讀") || strings.Contains(errs, "capy pl link") {
		t.Fatalf("只讀平台指路手動刪:\n%s%s", out, errs)
	}
}

// canonical 路徑 = sync 的一輪中間多一步:dry-run 看得到 dedup 與 push 兩半、exit 2、零寫入;--yes 之後平台與正本都是 [a, b, c],
// 保留的三份 item(iid / rank / added_at)逐位元不變——順序是使用者的記憶;再跑一次「沒有重複」、零寫入。
func TestPlDedupRoundKeepsFirstCopyAndOrder(t *testing.T) {
	fs, dc := dedupWorld(t)
	before := drivePlaylistNamed(t, dc, "通勤")
	filesBefore := driveFiles(t, dc)
	out, _, err := runPull(t, "pl", "dedup", "通勤", "--dry-run")
	want := []string{"dedup remove ", "dedup remove ", "push remove spotify", "push remove spotify"}
	if exitOf(t, err) != 2 || !slices.Equal(dirActions(out), want) {
		t.Fatalf("dry-run:%v\n%s", err, out)
	}
	if !strings.Contains(out, "dedup\tremove\t\t通勤\t2\t"+fakeCID("a")+"\t\tsong-a\tartist\t重複:與 pos 0 同一首") || !strings.Contains(out, "\t4\t"+fakeCID("b")+"\t") {
		t.Fatalf("dedup 列的 pos 是正本位置、指向保留的那份:\n%s", out)
	}
	if _, _, err := runPull(t, "pl", "dedup", "通勤"); exitOf(t, err) != 2 {
		t.Fatalf("非 TTY 沒 --yes 要 exit 2:%v", err)
	}
	if len(fs.written()) != 0 || !sameFiles(filesBefore, driveFiles(t, dc)) {
		t.Fatal("dry-run / 待套用:平台與 Drive 都零寫入")
	}
	out, errs := mustPull(t, "pl", "dedup", "通勤", "--yes")
	if !slices.Equal(dirActions(out), want) || !strings.Contains(errs, "已推送 2 筆變更") || !strings.Contains(errs, "已去除 2 份重複") || strings.Contains(errs, "手動刪除") {
		t.Fatalf("套用(可寫的平台由 push 拿掉,不列手動):\n%s%s", out, errs)
	}
	if !slices.Equal(fs.tracksOf("p1"), []string{"a", "b", "c"}) {
		t.Fatalf("平台留第一份、順序不動:%v", fs.tracksOf("p1"))
	}
	after := drivePlaylistNamed(t, dc, "通勤")
	if !slices.Equal(cidsOf(after), []string{fakeCID("a"), fakeCID("b"), fakeCID("c")}) {
		t.Fatalf("正本:%v", cidsOf(after))
	}
	for i, j := range []int{0, 1, 3} { // 保留的 item 是原本的第 0、1、3 份,連 rank 都沒動
		if after.Items[i] != before.Items[j] {
			t.Fatalf("item %d 要逐位元等於原本的第 %d 份:%+v ≠ %+v", i, j, after.Items[i], before.Items[j])
		}
	}
	filesAfter := driveFiles(t, dc)
	if out, errs := mustPull(t, "pl", "dedup", "通勤", "--yes"); out != "" || !strings.Contains(errs, "沒有重複") || !sameFiles(filesAfter, driveFiles(t, dc)) {
		t.Fatalf("再跑:沒有重複、零寫入:%q %q", out, errs)
	}
	if out, errs := mustPull(t, "pl", "sync", "通勤", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("之後 sync 零變更:%s%s", out, errs)
	}
}

// 正本已去重、平台還有多的份:dedup 不退化成 sync(pull 半邊看到的其他變更留給 sync)——但平台上那份由 push 拿掉。
// 平台把重複加回來(pull 半邊吸收)也一樣一輪解決。
func TestPlDedupPlatformReaddsDuplicate(t *testing.T) {
	fs, dc := dedupWorld(t)
	mustPull(t, "pl", "dedup", "通勤", "--yes")
	fs.set("p1", "通勤", "a", "b", "c", "d", "b") // 平台加了 d 與第二份 b
	out, _ := mustPull(t, "pl", "dedup", "通勤", "--yes")
	if !slices.Equal(dirActions(out), []string{"pull add spotify", "pull add spotify", "dedup remove ", "push remove spotify"}) {
		t.Fatalf("pull 吸收 d 與 b、正本去掉 b、push 拿掉平台那份:\n%s", out)
	}
	if !slices.Equal(fs.tracksOf("p1"), []string{"a", "b", "c", "d"}) || !slices.Equal(cidsOf(drivePlaylistNamed(t, dc, "通勤")), []string{fakeCID("a"), fakeCID("b"), fakeCID("c"), fakeCID("d")}) {
		t.Fatalf("兩邊都是 [a b c d]:%v", fs.tracksOf("p1"))
	}
	// 平台只加了一首(沒有重複):dedup 說沒有重複、零寫入,那首留給 sync
	fs.set("p1", "通勤", "a", "b", "c", "d", "e")
	files := driveFiles(t, dc)
	if out, errs := mustPull(t, "pl", "dedup", "通勤", "--yes"); out != "" || !strings.Contains(errs, "沒有重複") || !sameFiles(files, driveFiles(t, dc)) || len(fs.tracksOf("p1")) != 5 {
		t.Fatalf("沒有重複就不退化成 sync:%q %q", out, errs)
	}
	if out, _ := mustPull(t, "pl", "sync", "通勤", "--yes"); !slices.Equal(dirActions(out), []string{"pull add spotify"}) {
		t.Fatalf("e 留給 sync:%s", out)
	}
}

// 同 ISRC 不同 id(單曲版 / 專輯版):正本記第一份;平台上相鄰兩份時 push 的 LCS 配對留後面那個 id(釘住行為,曲目與順序不變)。
func TestPlDedupSameISRCDifferentID(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.aliasISRC("a2", "a")
	fs.set("p1", "通勤", "a", "a2", "b")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	out, _ := mustPull(t, "pl", "dedup", "通勤", "--yes")
	if !slices.Equal(dirActions(out), []string{"dedup remove ", "push remove spotify"}) || !strings.Contains(out, "dedup\tremove\t\t通勤\t1\t"+fakeCID("a")) {
		t.Fatalf("同 ISRC 算同一首:\n%s", out)
	}
	if !slices.Equal(fs.tracksOf("p1"), []string{"a2", "b"}) || !slices.Equal(cidsOf(drivePlaylistNamed(t, dc, "通勤")), []string{fakeCID("a"), fakeCID("b")}) {
		t.Fatalf("平台留一份、正本一個 cid:%v", fs.tracksOf("p1"))
	}
	if out, errs := mustPull(t, "pl", "sync", "通勤", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("之後零變更:%s%s", out, errs)
	}
}

// 寫不了的平台(T6 前的 Apple):正本去重、平台那份列成手動;之後的 sync 不會把它加回正本(規則 4′);使用者手動刪掉後也零變更。
func TestPlDedupReadOnlyPlatformIsManual(t *testing.T) {
	_, fs2, dc, _ := twoPlatforms(t)
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		p, err := orig(ctx, id)
		if id == "apple" && err == nil {
			return readOnlyProvider{p, p.(provider.PlaylistReader)}, nil
		}
		return p, err
	}
	t.Cleanup(func() { newProvider = orig })
	fs2.set("q1", "通勤", "a", "a", "b")
	mustPull(t, "pl", "link", "通勤", "apple:q1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	out, errs := mustPull(t, "pl", "dedup", "通勤", "--yes")
	if !slices.Equal(dirActions(out), []string{"dedup remove "}) || !strings.Contains(errs, "apple:q1 還有 1 份重複(pos 1)") || !strings.Contains(errs, "手動刪除") {
		t.Fatalf("正本去重、平台手動:\n%s%s", out, errs)
	}
	if !slices.Equal(cidsOf(drivePlaylistNamed(t, dc, "通勤")), []string{fakeCID("a"), fakeCID("b")}) || len(fs2.written()) != 0 || !slices.Equal(fs2.tracksOf("q1"), []string{"a", "a", "b"}) {
		t.Fatal("正本 [a b]、平台不動")
	}
	if out, errs := mustPull(t, "pl", "sync", "通勤", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("sync 不把平台那份加回來:%s%s", out, errs)
	}
	if out, errs := mustPull(t, "pl", "dedup", "通勤", "--yes"); out != "" || !strings.Contains(errs, "apple:q1 還有 1 份重複") || !strings.Contains(errs, "無變更") {
		t.Fatalf("再跑 dedup:只提醒手動:%q %q", out, errs)
	}
	fs2.set("q1", "通勤", "a", "b") // 使用者在 app 裡手動刪了
	if out, errs := mustPull(t, "pl", "sync", "通勤", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") || !slices.Equal(cidsOf(drivePlaylistNamed(t, dc, "通勤")), []string{fakeCID("a"), fakeCID("b")}) {
		t.Fatalf("手動刪掉後收斂、正本不動:%s%s", out, errs)
	}
}

// 去重的份數過閾值 → exit 3、零寫入;--force 放行(去重與 push 各算,同一個 --force)。
func TestPlDedupThresholdNeedsForce(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	ids := append(slices.Repeat([]string{"a"}, 12), "b")
	fs.set("p1", "通勤", ids...)
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	before := driveFiles(t, dc)
	_, _, err := runPull(t, "pl", "dedup", "通勤", "--yes")
	if exitOf(t, err) != 3 || !strings.Contains(err.Error(), "要去除 11 份重複(共 13 首),超過閾值") || len(fs.written()) != 0 || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("閾值要擋、零寫入:%v", err)
	}
	if out, _ := mustPull(t, "pl", "dedup", "通勤", "--yes", "--force"); strings.Count(out, "dedup\tremove\t") != 11 || !slices.Equal(fs.tracksOf("p1"), []string{"a", "b"}) {
		t.Fatalf("--force 放行:%s %v", out, fs.tracksOf("p1"))
	}
}

func TestPlDedupArgsAndHelp(t *testing.T) {
	pullWorld(t)
	for _, args := range [][]string{{"pl", "dedup"}, {"pl", "dedup", "x", "--provider", "nope"}, {"pl", "dedup", "spotify:"}, {"pl", "dedup", "沒這個清單"}} {
		if _, _, err := runPull(t, args...); exitOf(t, err) != 1 {
			t.Fatalf("%v 要 exit 1:%v", args, err)
		}
	}
	out, _, err := runPull(t, "pl", "dedup", "--help")
	if err != nil || !strings.Contains(out, "推到清單連結的其他平台") || !strings.Contains(out, "只報告") {
		t.Fatalf("help 要講 --force 會傳播、平台寫法只報告:%v\n%s", err, out)
	}
}
