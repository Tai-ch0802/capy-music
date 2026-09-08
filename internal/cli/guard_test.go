package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
)

// once:hook 只觸發一次(pull 期間會打好幾個平台請求)。
func once(fn func()) func() {
	done := false
	return func() {
		if !done {
			done = true
			fn()
		}
	}
}

// 決策 29:FETCH 到 COMMIT 之間別台裝置改了 pl__ → 一個檔都不傳、本機不動、exit 1;重跑成功且包含對方的變更。
func TestVersionGuardBlocksLostUpdate(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	fs.set("p1", "通勤", "a", "b") // 這次 pull 要傳 pl__ / dev__ / tracks
	exportBefore, _ := mustPull(t, "export")
	var afterOther map[string][]byte
	fs.setHook(once(func() { // 別台裝置改了 pl__ 與 tracks.json 兩個檔
		pl := decodeFile[canon.Playlist](t, driveFiles(t, dc), "pl__")
		pl.Links["apple"] = "ap1"
		putPlaylist(t, dc, pl)
		tr := driveTracks(t, dc)
		ta := tr.Tracks[fakeCID("a")]
		ta.Conflicts = append(ta.Conflicts, canon.Conflict{Provider: "spotify", ProviderID: "a2", Title: "song-a", DurationMS: 1})
		tr.Tracks[fakeCID("a")] = ta
		putTracks(t, dc, tr)
		afterOther = driveFiles(t, dc)
	}))
	_, _, err := runPull(t, "pl", "pull", "通勤", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "pl__") || !strings.Contains(err.Error(), "tracks.json 被別台裝置改過") || !strings.Contains(err.Error(), "零寫入,重跑一次") {
		t.Fatalf("守衛要 exit 1 並列出全部不符的檔:%v", err)
	}
	if afterOther == nil || !sameFiles(afterOther, driveFiles(t, dc)) {
		t.Fatal("零上傳:Drive 要停在對方寫完的樣子")
	}
	if out, _ := mustPull(t, "export"); out != exportBefore {
		t.Fatal("本機快取不動")
	}
	fs.setHook(nil)
	out, _ := mustPull(t, "pl", "pull", "通勤", "--provider", "spotify", "--yes") // 只 pull spotify:假伺服器沒有 ap1
	pl := decodeFile[canon.Playlist](t, driveFiles(t, dc), "pl__")
	if pl.Links["apple"] != "ap1" || len(pl.Items) != 2 || !strings.Contains(out, "add\t") {
		t.Fatalf("重跑要包含對方的 link 與自己的 add:%+v\n%s", pl, out)
	}
}

// 守衛比的是 FETCH 讀過的每個檔,不只這次要傳的:別台裝置只改 tracks.json、這次 pull 沒有要傳 tracks(只改名)→ 仍 exit 1。
func TestVersionGuardCoversUnstagedFiles(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	fs.set("p1", "通勤 2026", "a") // 只改名:傳 pl__ / dev__ / manifest,tracks 位元組不變
	fs.setHook(once(func() {
		tr := driveTracks(t, dc)
		ta := tr.Tracks[fakeCID("a")]
		ta.Conflicts = append(ta.Conflicts, canon.Conflict{Provider: "spotify", ProviderID: "a2", Title: "song-a", DurationMS: 1})
		tr.Tracks[fakeCID("a")] = ta
		putTracks(t, dc, tr)
	}))
	_, _, err := runPull(t, "pl", "pull", "通勤", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "tracks.json") {
		t.Fatalf("沒 stage 的檔也在守衛範圍:%v", err)
	}
}

// Create 的競態:我們 FETCH 時還沒有的檔(新裝置第一次 pull 要建的 dev__),在 COMMIT 前被建了 → 不建第二份,exit 1。
// (bootstrap 的 manifest 卡不進 hook:pl link 的清單查詢在 withCanonical 之外;dev__ 走的是同一條 Create 路徑。)
func TestVersionGuardRefusesDuplicateCreate(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	const dev2 = "01TESTDEVICE0000000000000B" // 換成新裝置:FETCH 讀不到它的 dev__,COMMIT 要 Create
	if err := config.Save(&config.Config{DeviceID: dev2, GoogleEmail: "tai@example.com"}); err != nil {
		t.Fatal(err)
	}
	ref := canon.DeviceFile(dev2)
	fs.setHook(once(func() {
		body, err := canon.Encode(canon.NewDeviceState(dev2))
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := dc.Create(context.Background(), ref.Name, ref.Props, body); err != nil {
			t.Error(err)
		}
	}))
	_, _, err := runPull(t, "pl", "pull", "通勤", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), ref.Name+" 被別台裝置建立") {
		t.Fatalf("FETCH 後才出現的檔不能再建一份:%v", err)
	}
	n := 0
	for name := range driveFiles(t, dc) {
		if name == ref.Name {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s 要恰好一份,得 %d", ref.Name, n)
	}
}

// FETCH 時就已經有兩份的同名檔不是「這次執行期間被改」:照常採最新一份並 COMMIT。
func TestVersionGuardToleratesPreexistingDuplicates(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	body, err := canon.Encode(driveTracks(t, dc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dc.Create(context.Background(), "tracks.json", canon.TracksFile().Props, body); err != nil {
		t.Fatal(err)
	}
	fs.set("p1", "通勤", "a", "b")
	if _, errs := mustPull(t, "pl", "pull", "通勤", "--yes"); !strings.Contains(errs, "有 2 份 tracks.json") {
		t.Fatalf("重複檔只警告、不擋:%s", errs)
	}
}

// 同名檔在這次執行期間多了一份(別台裝置 Create 了第二份 tracks.json)也算變動:version 沒變、份數變了。
func TestVersionGuardCountsSameNameFiles(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	fs.set("p1", "通勤", "a", "b")
	fs.setHook(once(func() {
		body, err := canon.Encode(driveTracks(t, dc))
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := dc.Create(context.Background(), "tracks.json", canon.TracksFile().Props, body); err != nil {
			t.Error(err)
		}
	}))
	if _, _, err := runPull(t, "pl", "pull", "通勤", "--yes"); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "tracks.json 多了一份") {
		t.Fatalf("份數變了也擋:%v", err)
	}
}

// 同名多份時守衛比的是整個 (ID, Version) 集合:FETCH 選中 A,別台裝置改到 B → 也擋(不然我們寫 A、下次讀到較新的 B,寫入就消失了)。
func TestVersionGuardWatchesEveryDuplicate(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	tr := driveTracks(t, dc)
	body, err := canon.Encode(tr)
	if err != nil {
		t.Fatal(err)
	}
	other, err := dc.Create(context.Background(), "tracks.json", canon.TracksFile().Props, body) // 第二份,比較新 → FETCH 會選它
	if err != nil {
		t.Fatal(err)
	}
	files, err := dc.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var older string
	for _, f := range files {
		if f.Name == "tracks.json" && f.ID != other.ID {
			older = f.ID
		}
	}
	fs.set("p1", "通勤", "a", "b")
	fs.setHook(once(func() { // 別台裝置改的是「另一份」(我們沒選中的那份)
		ta := tr.Tracks[fakeCID("a")]
		ta.Conflicts = append(ta.Conflicts, canon.Conflict{Provider: "spotify", ProviderID: "a2", Title: "song-a", DurationMS: 1})
		tr.Tracks[fakeCID("a")] = ta
		b, err := canon.Encode(tr)
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := dc.Update(context.Background(), older, canon.TracksFile().Props, b); err != nil {
			t.Error(err)
		}
	}))
	if _, _, err := runPull(t, "pl", "pull", "通勤", "--yes"); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "tracks.json 多了一份(或另一份被改過)") {
		t.Fatalf("另一份被改也擋:%v", err)
	}
}
