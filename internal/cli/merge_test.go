package cli

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/provider/youtube/youtubetest"
)

// pl link --merge(計畫 2026-10-01 §3.4、§4「新增的 Go 測試」1–9 與 5′;決策 62)。

// mergeWorld:正本「通勤」= spotify:p1 [a b c](已連結、pull 過);apple(假 Spotify fs2)那邊放要合併的清單。
func mergeWorld(t *testing.T) (fs1, fs2 *fakeSpotify, dc *drive.Client) {
	t.Helper()
	fs1, fs2, dc, _ = twoPlatforms(t)
	fs1.set("p1", "通勤", "a", "b", "c")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	return fs1, fs2, dc
}

func (f *fakeSpotify) nameOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists[f.index(id)].Name
}

// stubMergeTTY:當成在終端機裡跑,每個確認都回 answer;回傳問過的提示 key(網頁靠 key 認提示)。
func stubMergeTTY(t *testing.T, answer bool) *[]string {
	t.Helper()
	origTTY, origConfirm := bothTTY, confirmWrite
	keys := &[]string{}
	bothTTY = func(*cobra.Command) bool { return true }
	confirmWrite = func(key, _ string) (bool, error) { *keys = append(*keys, key); return answer, nil }
	t.Cleanup(func() { bothTTY, confirmWrite = origTTY, origConfirm })
	return keys
}

// 1. 正本原有的 item 逐位元不變;那份清單裡正本沒有的歌照它的順序接在尾端、每首一份(清單自己的重複列成 pull skip);
// 那份清單排成正本的樣子、改名;只動這一個平台;之後 sync 零變更。
func TestLinkMergeKeepsMasterOrderAndAppendsInPlaylistOrder(t *testing.T) {
	fs1, fs2, dc := mergeWorld(t)
	catalogISRC(fs2, "b") // apple 目錄查得到 b:合併後推得過去
	fs2.set("q1", "上班聽", "c", "x", "a", "y", "x")
	fs2.retitle("x", "Paper Boat")
	fs2.retitle("y", "Night Drive")
	before := drivePlaylistNamed(t, dc, "通勤")
	out, errs := mustPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--yes")
	after := drivePlaylistNamed(t, dc, "通勤")
	if !slices.Equal(cidsOf(after), []string{fakeCID("a"), fakeCID("b"), fakeCID("c"), fakeCID("x"), fakeCID("y")}) || after.Links["apple"] != "q1" || after.PID != before.PID {
		t.Fatalf("正本 = 原有的 + 尾端照清單順序、每首一份,連上 apple:q1:%v %v\n%s%s", cidsOf(after), after.Links, out, errs)
	}
	for i := range 3 {
		if after.Items[i] != before.Items[i] {
			t.Fatalf("正本原有的 item %d 要逐位元不變:%+v ≠ %+v", i, after.Items[i], before.Items[i])
		}
	}
	wantLine(t, out, "pull\tadd\tapple\t通勤\t3\t"+fakeCID("x")+"\tx\tPaper Boat\tartist\t平台新增\tadded_on_platform")
	wantLine(t, out, "pull\tadd\tapple\t通勤\t4\t"+fakeCID("y")+"\ty\tNight Drive\tartist\t平台新增\tadded_on_platform")
	wantLine(t, out, "pull\tskip\tapple\t通勤\t4\t"+fakeCID("x")+"\tx\tPaper Boat\tartist\t重複:與 pos 1 同 id\tdup_id")
	if strings.Contains(out, "resolve\t") { // a、c 本來就是同一首(同 ISRC),b 對到的是目錄裡的、不在清單裡
		t.Fatalf("沒有新認成同一首的:\n%s", out)
	}
	if !slices.Equal(fs2.tracksOf("q1"), []string{"a", "b", "c", "x", "y"}) || fs2.nameOf("q1") != "通勤" {
		t.Fatalf("清單排成正本的樣子、改成正本的名字:%v %q", fs2.tracksOf("q1"), fs2.nameOf("q1"))
	}
	if len(fs1.written()) != 0 {
		t.Fatalf("只動那一個平台:%+v", fs1.written())
	}
	if !strings.Contains(errs, "已把 apple:q1 併進正本 通勤(") {
		t.Fatalf("收尾:%s", errs)
	}
	if out, errs := mustPull(t, "pl", "sync", "通勤", "--provider", "apple", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("合併之後 sync 零變更:%s%s", out, errs)
	}
}

// ytMergeWorld:正本「通勤」= spotify:p1 [a(Blue Morning)、b(Night Drive)];YouTube 目錄有 y1(Blue Morning 音訊版)、y2、y9,
// withMV 時多一支 Blue Morning 的 MV y0mv(id 比 y1 小:目錄搜尋同分時會先排到它)。YouTube 沒有 ISRC。
func ytMergeWorld(t *testing.T, withMV bool, playlist ...string) (*youtubetest.Server, *drive.Client) {
	t.Helper()
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a", "b")
	fs.retitle("a", "Blue Morning")
	fs.retitle("b", "Night Drive")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	yt := youtubetest.New(t)
	if withMV {
		yt.AddTrack(youtubetest.Track{ID: "y0mv", Title: "Blue Morning", Artist: "artist", Album: "A", DurationMS: 200000})
	}
	yt.AddTrack(youtubetest.Track{ID: "y1", Title: "Blue Morning", Artist: "artist", Album: "A", DurationMS: 200000})
	yt.AddTrack(youtubetest.Track{ID: "y2", Title: "Night Drive", Artist: "artist", Album: "A", DurationMS: 200000})
	yt.AddTrack(youtubetest.Track{ID: "y9", Title: "Paper Boat", Artist: "artist", Album: "A", DurationMS: 200000})
	yt.AddPlaylist("PLm", "上班聽", playlist...)
	swapYouTube(t, yt)
	return yt, dc
}

// 2. 沒有 ISRC:正本來自 Spotify(cid 是 ISRC),合併一份跟它重疊、還沒對應過的 YouTube 清單——清單裡比對命中的那首不會在正本
// 多出第二份(表上一列 dir=resolve 照實說認成同一首);沒命中的照實接進正本(pull add)。
func TestLinkMergeYouTubeWithoutISRCMatchesInsideThePlaylist(t *testing.T) {
	yt, dc := ytMergeWorld(t, false, "y1", "y9")
	out, _ := mustPull(t, "pl", "link", "--merge", "通勤", "youtube:PLm", "--yes")
	if !strings.Contains(out, "resolve\tmap\tyoutube\t通勤\t\t"+fakeCID("a")+"\ty1\tBlue Morning\tartist\t認成同一首(分數 100)\tfuzzy\n") || strings.Count(out, "resolve\t") != 1 {
		t.Fatalf("清單裡的 y1 認成正本的 a,表上一列:\n%s", out)
	}
	if !strings.Contains(out, "pull\tadd\tyoutube\t通勤\t2\tp:youtube:y9\ty9\tPaper Boat\t") || !strings.Contains(out, "\t平台新增\tadded_on_platform\n") {
		t.Fatalf("沒命中的 y9 照實接進正本:\n%s", out)
	}
	if got := cidsOf(drivePlaylistNamed(t, dc, "通勤")); !slices.Equal(got, []string{fakeCID("a"), fakeCID("b"), "p:youtube:y9"}) {
		t.Fatalf("正本不多出第二份 a:%v", got)
	}
	if got := yt.Rows("PLm"); !slices.Equal(got, []string{"y1", "y2", "y9"}) || yt.Name("PLm") != "通勤" {
		t.Fatalf("清單排成正本的樣子:%v %q", got, yt.Name("PLm"))
	}
}

// 2 之二:正本的那首在 YouTube 目錄搜尋會先命中 MV,清單裡放的是音訊版——先在清單裡比對,所以認的是清單裡那一支,
// 合併後清單不會同時有兩支。
func TestLinkMergeYouTubeKeepsTheVersionInThePlaylist(t *testing.T) {
	yt, _ := ytMergeWorld(t, true, "y1")
	mustPull(t, "pl", "link", "--merge", "通勤", "youtube:PLm", "--yes")
	if got := yt.Rows("PLm"); !slices.Equal(got, []string{"y1", "y2"}) {
		t.Fatalf("清單裡只有音訊版 y1,不會再加 MV y0mv:%v", got)
	}
}

// 3. 那份清單排成正本的樣子:有對應的歌順序、份數都等於正本,名字等於正本;正本有、找不到對應的列成 push skip(no_mapping)、
// 平台上沒有;它獨有的歌至少留一份。目標本來就有歌、沒有 base:照樣產出 push 列(前提一、二不擋),dry-run 是 exit 2 不是 3。
func TestLinkMergeShapesThePlaylistLikeTheMaster(t *testing.T) {
	fs1, fs2, dc := mergeWorld(t)
	fs1.set("p1", "通勤", "a", "b", "c", "d")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	catalogISRC(fs2, "b") // d 在 apple 找不到
	fs2.set("q1", "上班聽", "c", "a", "a", "x")
	fs2.retitle("x", "Paper Boat")
	out, _, err := runPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--dry-run")
	acts := dirActions(out)
	for _, want := range []string{"pull add apple", "push remove apple", "push move apple", "push add apple", "push rename apple", "push skip apple"} {
		if !slices.Contains(acts, want) {
			t.Fatalf("沒有 base 也要產出 %q(前提一、二不擋):%v %v\n%s", want, err, acts, out)
		}
	}
	if exitOf(t, err) != 2 || len(fs2.written()) != 0 {
		t.Fatalf("dry-run:exit 2、零寫入:%v", err)
	}
	out, _ = mustPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--yes")
	if !slices.Equal(fs2.tracksOf("q1"), []string{"a", "b", "c", "x"}) || fs2.nameOf("q1") != "通勤" {
		t.Fatalf("有對應的照正本順序、份數同正本,d 沒有、x 留一份,名字同正本:%v %q", fs2.tracksOf("q1"), fs2.nameOf("q1"))
	}
	if !strings.Contains(out, "push\tskip\tapple\t通勤\t\t"+fakeCID("d")+"\t\tsong-d\tartist\t") || !strings.Contains(out, "\tno_mapping\n") {
		t.Fatalf("d 列成 skip(no_mapping):\n%s", out)
	}
	if !strings.Contains(out, "push\tremove\tapple\t通勤\t") || !strings.Contains(out, "\t正本裡只有 1 份,這是平台上多出來的那份\tremoved_in_master\n") {
		t.Fatalf("多出來的那份 a 列成 remove:\n%s", out)
	}
	if got := cidsOf(drivePlaylistNamed(t, dc, "通勤")); !slices.Equal(got, []string{fakeCID("a"), fakeCID("b"), fakeCID("c"), fakeCID("d"), fakeCID("x")}) {
		t.Fatalf("正本:%v", got)
	}
}

// 3 之二:正本有兩首很像的歌(原版 / Remaster,Norm 之後同名、同分),清單裡的那一首只認給一首(同分取正本裡先出現的),表上一列 dir=resolve;
// 另一首在目錄搜尋撞到同一首(已經認給別人 = candidate_taken)進 review,不會自動合併、清單也不會多一份。
func TestLinkMergeRemasterMatchesOnlyOne(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "老歌", "o", "r")
	fs.retitle("o", "Yesterday")
	fs.retitle("r", "Yesterday - Remastered 2009")
	mustPull(t, "pl", "link", "老歌", "spotify:p1")
	mustPull(t, "pl", "pull", "老歌", "--yes")
	yt := youtubetest.New(t)
	yt.AddTrack(youtubetest.Track{ID: "yy", Title: "Yesterday", Artist: "artist", Album: "A", DurationMS: 200000})
	yt.AddPlaylist("PLo", "老歌", "yy")
	swapYouTube(t, yt)
	out, errs := mustPull(t, "pl", "link", "--merge", "老歌", "youtube:PLo", "--yes")
	if strings.Count(out, "resolve\tmap\t") != 1 || !strings.Contains(out, "resolve\tmap\tyoutube\t老歌\t\t"+fakeCID("o")+"\tyy\t") {
		t.Fatalf("yy 只認給先出現的 o:\n%s", out)
	}
	if !strings.Contains(out, "push\tskip\tyoutube\t老歌\t\t"+fakeCID("r")+"\t") || !strings.Contains(errs, "1 首要人裁決") {
		t.Fatalf("r 進 review、這次不推:\n%s%s", out, errs)
	}
	if got := cidsOf(drivePlaylistNamed(t, dc, "老歌")); !slices.Equal(got, []string{fakeCID("o"), fakeCID("r")}) {
		t.Fatalf("正本不多一份:%v", got)
	}
	if got := yt.Rows("PLo"); !slices.Equal(got, []string{"yy"}) {
		t.Fatalf("清單不多一份:%v", got)
	}
}

// 4. --dry-run、非 TTY 沒 --yes、終端機裡取消(含「要不要逐首決定」也答否):exit 2,Drive 與平台位元組都不變、不留連結。
func TestLinkMergeCancelDryRunAndNonTTYWriteNothing(t *testing.T) {
	fs1, fs2, dc := mergeWorld(t) // b 在 apple 目錄找不到 → 1 首要人裁決
	fs2.set("q1", "上班聽", "c", "x")
	fs2.retitle("x", "Paper Boat")
	before := driveFiles(t, dc)
	check := func(label string, err error) {
		t.Helper()
		if exitOf(t, err) != 2 {
			t.Fatalf("%s:exit 2:%v", label, err)
		}
		if !sameFiles(before, driveFiles(t, dc)) || len(fs1.written()) != 0 || len(fs2.written()) != 0 {
			t.Fatalf("%s:Drive 與平台零寫入", label)
		}
	}
	_, _, err := runPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--dry-run")
	check("--dry-run", err)
	_, _, err = runPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--dry-run", "--yes")
	check("--dry-run --yes", err)
	_, _, err = runPull(t, "pl", "link", "--merge", "通勤", "apple:q1")
	check("非 TTY 沒 --yes", err)
	keys := stubMergeTTY(t, false)
	_, _, err = runPull(t, "pl", "link", "--merge", "通勤", "apple:q1")
	check("終端機裡取消", err)
	if !slices.Equal(*keys, []string{"migrate.confirm.review", "link.merge.confirm"}) {
		t.Fatalf("先問要不要逐首決定(同搬家)、再問一次確認:%v", *keys)
	}
}

// 5. 要拿掉的超過閾值:exit 3、零寫入、不留連結;之後的全部同步不受影響;同一情況帶 --force 照常寫入並連上。
func TestLinkMergeThresholdBlocksWithoutLinking(t *testing.T) {
	fs1, fs2, dc := mergeWorld(t)
	catalogISRC(fs2, "b")
	fs2.set("q1", "上班聽", "a", "a", "a", "a", "a", "a", "a", "a", "a", "a", "a", "a", "c") // a 多 11 份
	before := driveFiles(t, dc)
	_, _, err := runPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--yes")
	if exitOf(t, err) != 3 || !strings.Contains(err.Error(), "--force") || !sameFiles(before, driveFiles(t, dc)) || len(fs2.written()) != 0 {
		t.Fatalf("超過閾值:exit 3、零寫入:%v", err)
	}
	if out, errs := mustPull(t, "pl", "sync", "--all", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("沒連上,全部同步不受影響:%s%s", out, errs)
	}
	mustPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--yes", "--force")
	if !slices.Equal(fs2.tracksOf("q1"), []string{"a", "b", "c"}) || drivePlaylistNamed(t, dc, "通勤").Links["apple"] != "q1" || len(fs1.written()) != 0 {
		t.Fatalf("--force 照常寫入並連上:%v", fs2.tracksOf("q1"))
	}
}

// 5′. 確認後平台寫完、Drive COMMIT 被版本守衛擋下:平台已經改了、連結沒寫,訊息不說零寫入;再跑一次 --merge,對那份清單零筆。
func TestLinkMergeDriveGuardAfterPlatformWrite(t *testing.T) {
	_, fs2, dc := mergeWorld(t)
	catalogISRC(fs2, "b")
	fs2.set("q1", "上班聽", "c", "x", "a")
	fs2.retitle("x", "Paper Boat")
	fired := false
	fs2.setHook(func() { // 持鎖中被呼叫:寫進平台之後的下一個請求(重讀 L′)時,別台裝置改了 tracks.json
		if !fired && len(fs2.writes) > 0 {
			fired = true
			tr := driveTracks(t, dc)
			tr.Tracks[fakeCID("zz")] = canon.Track{CID: fakeCID("zz"), Title: "zz", Mappings: map[string]canon.Mapping{}}
			putTracks(t, dc, tr)
		}
	})
	_, _, err := runPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "平台已寫入") || !strings.Contains(err.Error(), "連結沒有記下") || strings.Contains(err.Error(), "零寫入") {
		t.Fatalf("平台已改、Drive 沒寫成:%v", err)
	}
	if !slices.Equal(fs2.tracksOf("q1"), []string{"a", "b", "c", "x"}) || drivePlaylistNamed(t, dc, "通勤").Links["apple"] != "" {
		t.Fatalf("平台已經是正本的樣子、Drive 沒有連結:%v", fs2.tracksOf("q1"))
	}
	fs2.setHook(nil)
	out, _ := mustPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--yes")
	if strings.Contains(out, "push\t") || drivePlaylistNamed(t, dc, "通勤").Links["apple"] != "q1" {
		t.Fatalf("重跑:對那份清單零筆、連上:\n%s", out)
	}
}

// 6. 合併之後另一台裝置跑 pl sync --all --yes:對那份清單零 move、零 remove(base 是寫完的樣子,跨裝置共用)。
func TestLinkMergeThenOtherDeviceSyncDoesNotReorder(t *testing.T) {
	_, fs2, _ := mergeWorld(t)
	catalogISRC(fs2, "b")
	fs2.set("q1", "上班聽", "c", "x", "a", "c")
	fs2.retitle("x", "Paper Boat")
	mustPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--yes")
	setDevice(t, "01OTHERDEVICE0000000000000")
	out, _ := mustPull(t, "pl", "sync", "--all", "--yes")
	if strings.Contains(out, "\tmove\t") || strings.Contains(out, "\tremove\t") || !slices.Equal(fs2.tracksOf("q1"), []string{"a", "b", "c", "x"}) {
		t.Fatalf("別台的 sync 不因為合併而重排或刪:\n%s%v", out, fs2.tracksOf("q1"))
	}
}

// 7. 前置檢查:寫不進的清單、含 Spotify 本機檔、正本不存在、正本已連這個平台、清單已連別的正本、跟 --create / --new-only 一起用、
// 參數不齊——都 exit 1、Drive 與平台零寫入。
func TestLinkMergeRefusesWithoutWriting(t *testing.T) {
	refuse := func(t *testing.T, dc *drive.Client, writes func() int, want string, args ...string) {
		t.Helper()
		before := driveFiles(t, dc)
		_, _, err := runPull(t, append([]string{"pl", "link", "--merge"}, args...)...)
		if exitOf(t, err) != 1 || !strings.Contains(err.Error(), want) {
			t.Fatalf("要 exit 1 並說 %q:%v", want, err)
		}
		if !sameFiles(before, driveFiles(t, dc)) || writes() != 0 {
			t.Fatal("零寫入")
		}
	}
	t.Run("別人的 Spotify 清單", func(t *testing.T) {
		_, fs2, dc := mergeWorld(t)
		fs2.set("q1", "上班聽", "a")
		fs2.setOwner("q1", "someone")
		refuse(t, dc, func() int { return len(fs2.written()) }, "寫不進去", "通勤", "apple:q1")
	})
	t.Run("含 Spotify 本機檔", func(t *testing.T) {
		_, fs2, dc := mergeWorld(t)
		fs2.set("q1", "上班聽", "a", "x")
		fs2.setLocal("x")
		refuse(t, dc, func() int { return len(fs2.written()) }, "加不回去", "通勤", "apple:q1")
	})
	t.Run("別人的 YouTube 清單", func(t *testing.T) {
		yt, dc := ytMergeWorld(t, false)
		yt.AddForeignPlaylist("PLx", "別人的", "UCotherperson00000000000", "y1")
		refuse(t, dc, func() int { return len(yt.Writes()) }, "寫不進去", "通勤", "youtube:PLx")
	})
	t.Run("正本不存在", func(t *testing.T) {
		_, fs2, dc := mergeWorld(t)
		fs2.set("q1", "上班聽", "a")
		refuse(t, dc, func() int { return len(fs2.written()) }, "找不到正本", "不存在的", "apple:q1")
	})
	t.Run("正本已連這個平台", func(t *testing.T) {
		_, fs2, dc := mergeWorld(t)
		fs2.set("q0", "通勤", "a")
		fs2.set("q1", "上班聽", "a")
		mustPull(t, "pl", "link", "通勤", "apple:q0")
		refuse(t, dc, func() int { return len(fs2.written()) }, "不接管", "通勤", "apple:q1")
		refuse(t, dc, func() int { return len(fs2.written()) }, "不接管", "通勤", "apple:q0")
	})
	t.Run("清單已連別的正本", func(t *testing.T) {
		_, fs2, dc := mergeWorld(t)
		fs2.set("q1", "上班聽", "a")
		mustPull(t, "pl", "link", "別的", "apple:q1")
		refuse(t, dc, func() int { return len(fs2.written()) }, "已連結到 canonical 清單 別的", "通勤", "apple:q1")
	})
	t.Run("旗標與參數", func(t *testing.T) {
		_, fs2, dc := mergeWorld(t)
		fs2.set("q1", "上班聽", "a")
		writes := func() int { return len(fs2.written()) }
		refuse(t, dc, writes, "不能跟 --create、--new-only", "--create", "通勤", "apple")
		refuse(t, dc, writes, "不能跟 --create、--new-only", "--new-only", "通勤", "apple:q1")
		refuse(t, dc, writes, "accepts 2 arg(s)", "通勤") // 非 TTY 的參數錯誤沿用 cobra 原句(argsOrPicker)
	})
}

// 8. 不帶 --merge 的 pl link 輸出一字不變;--dry-run / --yes / --force 只能配 --merge。
func TestPlLinkWithoutMergeUnchanged(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a")
	out, errs := mustPull(t, "pl", "link", "通勤", "spotify:p1")
	pid := drivePlaylistNamed(t, dc, "通勤").PID
	if out != "已連結 通勤("+pid+")↔ spotify:p1;接著 capy pl pull 通勤\n" || errs != "建立 canonical 清單 通勤("+pid+")\n" {
		t.Fatalf("輸出一字不變:%q %q", out, errs)
	}
	for _, flag := range []string{"--dry-run", "--yes", "-y", "--force"} {
		if _, _, err := runPull(t, "pl", "link", "通勤", "spotify:p1", flag); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "只能配 --merge") {
			t.Fatalf("%s 沒配 --merge 要回錯:%v", flag, err)
		}
	}
}

// 正本已經有那份清單的每一首、清單也已經是正本的樣子:照樣確認後才連上(0 筆;沒有「不問就連」的路),連上時記下 base——
// 不然之後的 push 會被前提一擋下、pull 會把它當成第一次連結照平台順序重排。
func TestLinkMergeZeroChangesStillConfirmsAndRecordsBase(t *testing.T) {
	_, fs2, dc := mergeWorld(t)
	fs2.set("q1", "通勤", "a", "b", "c")
	before := driveFiles(t, dc)
	out, _, err := runPull(t, "pl", "link", "--merge", "通勤", "apple:q1")
	if exitOf(t, err) != 2 || out != "" || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("0 筆也要確認(非 TTY 沒 --yes = exit 2、不連上):%v %q", err, out)
	}
	mustPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--yes")
	if b, ok := baseOfProv(t, dc, "apple"); !ok || b.ID != "q1" || !slices.Equal(b.Items, []string{"a", "b", "c"}) {
		t.Fatalf("連上時要記下 base:%+v %v", b, ok)
	}
	if out, errs := mustPull(t, "pl", "push", "通勤", "--provider", "apple", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("之後 push 不被前提擋、零變更:%s%s", out, errs)
	}
}

// 確認後平台一筆都沒寫(確認期間清單變了、第一個寫入請求就失敗):整輪不寫 Drive、不留連結。
func TestLinkMergeNothingWrittenLeavesNoLink(t *testing.T) {
	t.Run("確認期間清單變了", func(t *testing.T) {
		_, fs2, dc := mergeWorld(t)
		catalogISRC(fs2, "b")
		fs2.set("q1", "上班聽", "c", "a")
		fired := false
		fs2.setHook(func() { // 持鎖中被呼叫:讀過一次之後,手機上又加了一首
			if !fired && fs2.itemReads > 0 {
				fired = true
				fs2.items["q1"] = append(slices.Clone(fs2.items["q1"]), "b")
			}
		})
		before := driveFiles(t, dc)
		_, _, err := runPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--yes")
		if exitOf(t, err) != 3 || !strings.Contains(err.Error(), "在確認期間有變動") || !strings.Contains(err.Error(), "沒有連上") {
			t.Fatalf("零寫入、exit 3:%v", err)
		}
		if !sameFiles(before, driveFiles(t, dc)) || len(fs2.written()) != 0 {
			t.Fatal("Drive 與平台零寫入")
		}
	})
	t.Run("第一個寫入請求就失敗", func(t *testing.T) {
		_, fs2, dc := mergeWorld(t)
		catalogISRC(fs2, "b")
		fs2.set("q1", "上班聽", "c", "a")
		fs2.setWriteStatus(403)
		before := driveFiles(t, dc)
		_, _, err := runPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--yes")
		if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "一筆都沒寫、也沒有連上") || !sameFiles(before, driveFiles(t, dc)) {
			t.Fatalf("exit 1、Drive 零寫入、不連上:%v", err)
		}
	})
}

// 平台寫到一半失敗:照 push 的規矩,base 記實際狀態、連結照樣記下,訊息講明;下一次 pl sync 接著做完。
func TestLinkMergePartialWriteKeepsLinkAndSyncFinishes(t *testing.T) {
	fs1, fs2, dc := mergeWorld(t)
	var ids []string
	for i := range 150 {
		ids = append(ids, fmt.Sprintf("t%03d", i))
	}
	fs1.set("p1", "通勤", ids...)
	mustPull(t, "pl", "pull", "通勤", "--yes", "--force") // a b c 換成 150 首
	catalogISRC(fs2, ids...)
	fs2.set("q1", "通勤", "t001", "t000") // 要整份重寫:PUT 前 100 首、POST 其餘
	fs2.mu.Lock()
	fs2.postFail = 3
	fs2.mu.Unlock()
	_, _, err := runPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "只有前 100 首") || !strings.Contains(err.Error(), "連結已經記下") {
		t.Fatalf("寫到一半:exit 1、講明連結已記下:%v", err)
	}
	if b, ok := baseOfProv(t, dc, "apple"); !ok || len(b.Items) != 100 || drivePlaylistNamed(t, dc, "通勤").Links["apple"] != "q1" {
		t.Fatalf("base = 實際狀態(前 100 首)、連結已記下:%d %v", len(b.Items), ok)
	}
	out, _ := mustPull(t, "pl", "sync", "通勤", "--yes")
	if strings.Contains(out, "\tremove\t") || !slices.Equal(fs2.tracksOf("q1"), ids) {
		t.Fatalf("sync 接著做完、不刪:%d\n%s", len(fs2.tracksOf("q1")), out)
	}
}

// 3 之三(自審第 4 則):一對一照分數,不是先到先得——後出現、但分數較高的正本歌拿走清單裡那首;先出現的那首落到目錄搜尋(這裡搜不到)。
func TestLinkMergeHigherScoreTakesThePlaylistTrack(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "通勤", "a", "b")
	fs.retitle("a", "Blue Mornin")
	fs.retitle("b", "Blue Morning")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	yt := youtubetest.New(t)
	yt.AddTrack(youtubetest.Track{ID: "y1", Title: "Blue Morning", Artist: "artist", Album: "A", DurationMS: 200000})
	yt.AddPlaylist("PLm", "通勤", "y1")
	swapYouTube(t, yt)
	out, _, _ := runPull(t, "pl", "link", "--merge", "通勤", "youtube:PLm", "--dry-run")
	if strings.Count(out, "resolve\tmap\t") != 1 || !strings.Contains(out, "resolve\tmap\tyoutube\t通勤\t\t"+fakeCID("b")+"\ty1\t") {
		t.Fatalf("分數 100 的 b 拿走 y1:\n%s", out)
	}
	if !strings.Contains(out, "push\tskip\tyoutube\t通勤\t\t"+fakeCID("a")+"\t") {
		t.Fatalf("a 落到目錄搜尋、搜不到,列成 skip:\n%s", out)
	}
}

// 自審第 2 則(實驗 A):清單裡那首已經是另一份正本從 YouTube 觀測過的曲目(有自己的 cid)。修之前它不進清單內比對,目錄搜尋先排到
// MV 就自動對上:PLm 同時有 MV 與音訊版、正本多一份、而且不問人。現在列成一筆要人裁決(candidate_taken),也不再搜目錄。
func TestLinkMergeOwnedTrackInPlaylistGoesToReview(t *testing.T) {
	yt, _ := ytMergeWorld(t, true, "y1")
	yt.AddPlaylist("PLother", "其他", "y1")
	mustPull(t, "pl", "link", "其他", "youtube:PLother")
	mustPull(t, "pl", "pull", "其他", "--yes")
	keys := stubMergeTTY(t, false)
	if _, _, err := runPull(t, "pl", "link", "--merge", "通勤", "youtube:PLm"); exitOf(t, err) != 2 || !slices.Equal(*keys, []string{"migrate.confirm.review", "link.merge.confirm"}) {
		t.Fatalf("終端機裡先問要不要逐首決定:%v %v", err, *keys)
	}
	_, errs := mustPull(t, "pl", "link", "--merge", "通勤", "youtube:PLm", "--yes")
	if got := yt.Rows("PLm"); slices.Contains(got, "y0mv") || !strings.Contains(errs, "1 首要人裁決") {
		t.Fatalf("不自動對到 MV、留給人裁決:%v\n%s", got, errs)
	}
}

// 自審第 6 則:清單裡的歌已經屬於別的既有曲目時不自動合併(決策 21 只由人決定):沒有 dir=resolve 列、正本那首的 mapping 不動、
// 列成要人裁決;沒決定就當成不同的歌接進正本。
func TestLinkMergeDoesNotAutoMergeTrackOwnedByAnotherSong(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a")
	fs.retitle("a", "Blue Morning")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	yt := youtubetest.New(t)
	yt.AddTrack(youtubetest.Track{ID: "yX", Title: "Blue Morning", Artist: "artist", Album: "A", DurationMS: 200000})
	yt.AddPlaylist("PLo", "別的", "yX")
	yt.AddPlaylist("PLm", "上班聽", "yX")
	swapYouTube(t, yt)
	mustPull(t, "pl", "link", "別的", "youtube:PLo")
	mustPull(t, "pl", "pull", "別的", "--yes")
	out, errs := mustPull(t, "pl", "link", "--merge", "通勤", "youtube:PLm", "--yes")
	if strings.Contains(out, "resolve\tmap\t") || !strings.Contains(out, "pull\tadd\tyoutube\t通勤\t1\tp:youtube:yX\tyX\t") || !strings.Contains(errs, "1 首要人裁決") {
		t.Fatalf("不自動合併、列成要人裁決、當成不同的歌接進來:\n%s%s", out, errs)
	}
	if m, has := driveTracks(t, dc).Tracks[fakeCID("a")].Mappings["youtube"]; has {
		t.Fatalf("a 的 youtube mapping 不動:%+v", m)
	}
}

// 自審第 6 則之二:正本裡釘成不可得的歌(resolve pin … none),合併後不被清單裡同名的那首蓋掉;它列成 skip,清單裡那首當成不同的歌。
func TestLinkMergeKeepsPinnedUnavailable(t *testing.T) {
	_, dc := ytMergeWorld(t, false, "y1", "y9")
	mustPull(t, "resolve", "pin", fakeCID("a"), "youtube:none")
	out, _ := mustPull(t, "pl", "link", "--merge", "通勤", "youtube:PLm", "--yes")
	if m := driveTracks(t, dc).Tracks[fakeCID("a")].Mappings["youtube"]; !m.Pinned || m.ID != "" {
		t.Fatalf("a 仍釘成不可得:%+v", m)
	}
	if strings.Contains(out, "resolve\tmap\tyoutube\t通勤\t\t"+fakeCID("a")) || !strings.Contains(out, "push\tskip\tyoutube\t通勤\t\t"+fakeCID("a")+"\t") || !strings.Contains(out, "\tno_mapping\n") {
		t.Fatalf("a 不被認成 y1、列成 skip:\n%s", out)
	}
}

// 自審第 3、8 則:逐首決定在接進正本之前生效——接受清單裡那首,就認回正本、不另接一份,mapping 釘成那首;
// 提示依序是要不要逐首決定、一次確認。終端機裡帶 --dry-run 不問逐首決定(答了也一定被丟掉)。
func TestLinkMergeReviewAcceptsTrackInPlaylist(t *testing.T) {
	_, fs2, dc := mergeWorld(t) // a、b 都不在 apple 目錄:兩首都要人裁決
	fs2.set("q1", "上班聽", "c", "x")
	fs2.retitle("x", "Paper Boat")
	keys := stubMergeTTY(t, true)
	orig := reviewPrompt
	reviewPrompt = func(it resolveItem, _, _ int, _ func(string) ([]provider.Track, error)) (reviewDecision, error) {
		if it.cid != fakeCID("b") {
			return reviewDecision{kind: "skip"}, nil
		}
		return reviewDecision{kind: "manual", cand: &provider.Track{ProviderID: "x", Title: "Paper Boat", Artists: []string{"artist"}}}, nil
	}
	t.Cleanup(func() { reviewPrompt = orig })
	if _, _, err := runPull(t, "pl", "link", "--merge", "通勤", "apple:q1", "--dry-run"); exitOf(t, err) != 2 || len(*keys) != 0 {
		t.Fatalf("--dry-run 不問逐首決定、也不問確認:%v %v", err, *keys)
	}
	out, _ := mustPull(t, "pl", "link", "--merge", "通勤", "apple:q1")
	if got := cidsOf(drivePlaylistNamed(t, dc, "通勤")); !slices.Equal(got, []string{fakeCID("a"), fakeCID("b"), fakeCID("c")}) || strings.Contains(out, "pull\tadd") {
		t.Fatalf("接受清單裡那首 = 認回正本、不另接一份:%v\n%s", got, out)
	}
	if m := driveTracks(t, dc).Tracks[fakeCID("b")].Mappings["apple"]; m.ID != "x" || !m.Pinned || !slices.Equal(fs2.tracksOf("q1"), []string{"x", "c"}) {
		t.Fatalf("mapping 釘成 x、清單排成正本的樣子:%+v %v", m, fs2.tracksOf("q1"))
	}
	if !slices.Equal(*keys, []string{"migrate.confirm.review", "link.merge.confirm"}) {
		t.Fatalf("提示順序:%v", *keys)
	}
}
