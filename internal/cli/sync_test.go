package cli

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/drive/drivetest"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/provider/spotify"
)

// twoPlatforms:spotify → fs1、apple → fs2(兩個都是假 Spotify;apple 這邊當「第二個可寫平台」用——T6 前真的 Apple 只讀)。
func twoPlatforms(t *testing.T) (fs1, fs2 *fakeSpotify, dc *drive.Client, srv *drivetest.Server) {
	t.Helper()
	fs1, dc, srv = pullWorld(t)
	fs2 = newFakeSpotify()
	srv2 := httptest.NewServer(fs2.handler(t))
	t.Cleanup(srv2.Close)
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id == "apple" {
			return spotify.New(srv2.Client(), srv2.URL), nil
		}
		return orig(ctx, id)
	}
	t.Cleanup(func() { newProvider = orig })
	return fs1, fs2, dc, srv
}

// syncWorld:通勤 同時連 spotify:p1 與 apple:q1,都是 [a, b, c],bootstrap 一輪。
func syncWorld(t *testing.T) (fs1, fs2 *fakeSpotify, dc *drive.Client, srv *drivetest.Server) {
	t.Helper()
	fs1, fs2, dc, srv = twoPlatforms(t)
	fs1.set("p1", "通勤", "a", "b", "c")
	fs2.set("q1", "通勤", "a", "b", "c")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "link", "通勤", "apple:q1")
	if out, _ := mustPull(t, "pl", "sync", "通勤", "--yes"); strings.Count(out, "pull\tadd\t") != 3 || strings.Contains(out, "push\t") {
		t.Fatalf("bootstrap:pull 三首、沒有 push:\n%s", out)
	}
	return fs1, fs2, dc, srv
}

func dirActions(out string) []string {
	var acts []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Split(line, "\t"); len(f) >= 3 && line != "" {
			acts = append(acts, f[0]+" "+f[1]+" "+f[2])
		}
	}
	return acts
}

// 決策 27 / 31 的一輪:Spotify 刪 b、Apple 同時重排成 [c, a, b] → sync 後兩邊都是 [c, a](刪除與重排都傳到對面、都不被翻回);再 sync 零變更。
func TestPlSyncRoundConverges(t *testing.T) {
	fs1, fs2, _, _ := syncWorld(t)
	fs1.set("p1", "通勤", "a", "c")
	fs2.set("q1", "通勤", "c", "a", "b")
	r1, r2 := fs1.reads(), fs2.reads()
	out, errs := mustPull(t, "pl", "sync", "通勤", "--yes")
	if fs1.reads()-r1 != 3 || fs2.reads()-r2 != 3 { // pull 1 + 套用前重讀 1 + L′ 1;push 半邊重用 pull 的 L(決策 31),不是 4
		t.Fatalf("每個平台一輪只讀三次 items:%d %d", fs1.reads()-r1, fs2.reads()-r2)
	}
	want := []string{"pull move apple", "pull remove spotify", "push remove apple", "push move spotify"} // provider 依字典序(決策 31)
	if !slices.Equal(dirActions(out), want) {
		t.Fatalf("一輪的變更集:\n%s%s", out, errs)
	}
	if !slices.Equal(fs1.tracksOf("p1"), []string{"c", "a"}) || !slices.Equal(fs2.tracksOf("q1"), []string{"c", "a"}) {
		t.Fatalf("兩邊都要收斂到 [c a]:%v %v", fs1.tracksOf("p1"), fs2.tracksOf("q1"))
	}
	if !strings.Contains(errs, "已推送 2 筆變更") || !strings.Contains(errs, "已套用 2 筆 pull 變更") {
		t.Fatalf("收尾訊息:%s", errs)
	}
	if out, errs := mustPull(t, "pl", "sync", "通勤", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("再 sync 零變更:%s%s", out, errs)
	}
}

// --dry-run 的 push 半邊用 pull 套用後的 C:Apple 加了 d → 表裡有 pull add(apple)與 push skip(spotify 沒 mapping),exit 2、零寫入;
// 非 TTY 沒 --yes 也是 exit 2;resolve 補上 mapping 後 sync 把 d 推到 Spotify。
func TestPlSyncDryRunShowsWholeRoundAndWritesNothing(t *testing.T) {
	fs1, fs2, dc, _ := syncWorld(t)
	fs2.set("q1", "通勤", "a", "b", "c", "d")
	before := driveFiles(t, dc)
	out, errs, err := runPull(t, "pl", "sync", "通勤", "--dry-run")
	if exitOf(t, err) != 2 || !slices.Equal(dirActions(out), []string{"pull add apple", "push skip spotify"}) || !strings.Contains(errs, "1 首尚未對應到 spotify") {
		t.Fatalf("dry-run 要看到整輪:%v\n%s%s", err, out, errs)
	}
	if _, _, err := runPull(t, "pl", "sync", "通勤"); exitOf(t, err) != 2 {
		t.Fatalf("非 TTY 沒 --yes 要 exit 2:%v", err)
	}
	if len(fs1.written()) != 0 || len(fs2.written()) != 0 || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("dry-run / 待套用:平台與 Drive 都零寫入")
	}
	mustPull(t, "pl", "sync", "通勤", "--yes") // pull d 進 C;spotify 沒 mapping 只列 skip
	fs1.addCatalog(fakeCatalogTrack{ID: "d", Name: "song-d", ISRC: fakeISRC("d")})
	mustPull(t, "resolve", "通勤", "--yes")
	if out, _ := mustPull(t, "pl", "sync", "通勤", "--yes"); !slices.Equal(dirActions(out), []string{"push add spotify"}) || !slices.Equal(fs1.tracksOf("p1"), []string{"a", "b", "c", "d"}) {
		t.Fatalf("mapping 補上後推到 Spotify:%s %v", out, fs1.tracksOf("p1"))
	}
}

// --provider 只走一個平台的一輪:Apple 的變更不 pull、Apple 也不被 push。
func TestPlSyncProviderFilter(t *testing.T) {
	fs1, fs2, _, _ := syncWorld(t)
	fs1.set("p1", "通勤", "a", "c")
	fs2.set("q1", "通勤", "a", "b", "c", "d")
	out, _ := mustPull(t, "pl", "sync", "通勤", "--yes", "--provider", "spotify")
	if !slices.Equal(dirActions(out), []string{"pull remove spotify"}) || len(fs2.written()) != 0 || !slices.Equal(fs2.tracksOf("q1"), []string{"a", "b", "c", "d"}) {
		t.Fatalf("只走 spotify:%s %v", out, fs2.tracksOf("q1"))
	}
}

// pull 半邊撞閾值 → 整輪 exit 3、平台與 Drive 零寫入(push 半邊算好了也不套)。
func TestPlSyncThresholdBlocksWholeRound(t *testing.T) {
	fs1, fs2, dc, _ := twoPlatforms(t)
	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	fs1.set("p1", "通勤", ids...)
	fs2.set("q1", "通勤", ids...)
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "link", "通勤", "apple:q1")
	mustPull(t, "pl", "sync", "通勤", "--yes")
	before := driveFiles(t, dc)
	fs1.set("p1", "通勤", ids[:6]...) // 刪 4 首 = 40%
	_, _, err := runPull(t, "pl", "sync", "通勤", "--yes")
	if exitOf(t, err) != 3 || !strings.Contains(err.Error(), "超過閾值") || len(fs2.written()) != 0 || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("閾值要擋整輪:%v", err)
	}
	if _, _, err := runPull(t, "pl", "sync", "--all", "--force"); err == nil || !strings.Contains(err.Error(), "--force 只能配單一清單") {
		t.Fatalf("--force 配 --all 要拒絕:%v", err)
	}
	if out, _ := mustPull(t, "pl", "sync", "通勤", "--yes", "--force"); strings.Count(out, "pull\tremove\t") != 4 || strings.Count(out, "push\tremove\t") != 4 || len(fs2.tracksOf("q1")) != 6 {
		t.Fatalf("--force 放行整輪:%s", out)
	}
}

// sync 撞上版本守衛(決策 31):平台已改、Drive 沒改,訊息講明;重跑一次收斂、第三次零變更。
func TestPlSyncVersionGuardMessageAndRerun(t *testing.T) {
	fs1, fs2, dc, _ := syncWorld(t)
	fs2.set("q1", "通勤", "a", "c") // Apple 刪 b
	fs1.setHook(once(func() {
		tr := driveTracks(t, dc)
		tr.Tracks[fakeCID("zz")] = canon.Track{CID: fakeCID("zz"), Title: "zz", Mappings: map[string]canon.Mapping{}}
		putTracks(t, dc, tr)
	}))
	_, errs, err := runPull(t, "pl", "sync", "通勤", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "平台已寫入 1 筆") || strings.Contains(err.Error(), "零寫入,重跑") || !slices.Equal(fs1.tracksOf("p1"), []string{"a", "c"}) {
		t.Fatalf("守衛訊息要改口、Spotify 已被改:%v %v", err, fs1.tracksOf("p1"))
	}
	if strings.Contains(errs, "已套用") || !strings.Contains(errs, "已推送 1 筆") { // Drive 沒寫成就不能說 pull 套用了;平台寫入是既成事實
		t.Fatalf("stderr 不得聲稱 pull 已套用:%s", errs)
	}
	fs1.setHook(nil)
	// base 沒落地(dev__ 在 Drive),重跑的 pull 半邊會把剛推到 Spotify 的刪除當平台變更再吸收一次——不是零 pull、是零 push(決策 31 措辭已改)
	if out, _ := mustPull(t, "pl", "sync", "通勤", "--yes"); strings.Contains(out, "push\t") || !strings.Contains(out, "pull\tremove\t") {
		t.Fatalf("重跑:pull 半邊吸收(apple 先 pull 就把 b 拿掉,spotify 那半邊變無事)、零 push:%s", out)
	}
	if out, errs := mustPull(t, "pl", "sync", "通勤", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("第三次零變更:%s%s", out, errs)
	}
}

// restricted 清單(開發模式 app 讀不到):pull 半邊跳過後 push 半邊不再讀一次、不再印一次(lives 的 nil 哨兵)。
func TestPlSyncRestrictedReadOnceAndSaidOnce(t *testing.T) {
	fs1, _, _, _ := syncWorld(t)
	fs1.mu.Lock()
	fs1.restricted["p1"] = true
	fs1.mu.Unlock()
	r := fs1.reads()
	out, errs := mustPull(t, "pl", "sync", "通勤", "--yes")
	if fs1.reads()-r != 1 || strings.Count(errs, "跳過 通勤 的 spotify") != 1 || strings.Contains(out, "spotify") {
		t.Fatalf("只讀一次、只說一次:reads=%d\n%s%s", fs1.reads()-r, out, errs)
	}
}

// --provider 指到寫不了的平台(Apple,T6 前):sync 降級成只 pull(stderr 說明),不像 push 那樣 exit 1——cron 放 sync,「sync Apple = pull Apple」才誠實。
func TestPlSyncReadOnlyProviderDegradesToPull(t *testing.T) {
	_, fs2, dc, _ := syncWorld(t)
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		p, err := orig(ctx, id)
		if id == "apple" {
			return readOnlyProvider{p, p.(provider.PlaylistReader)}, err
		}
		return p, err
	}
	t.Cleanup(func() { newProvider = orig })
	fs2.set("q1", "通勤", "a", "b", "c", "d")
	out, errs := mustPull(t, "pl", "sync", "通勤", "--yes", "--provider", "apple")
	if !slices.Equal(dirActions(out), []string{"pull add apple"}) || !strings.Contains(errs, "跳過 通勤 的 apple") || len(fs2.written()) != 0 {
		t.Fatalf("只 pull 不 push、stderr 說明:%s%s", out, errs)
	}
	if !slices.Equal(cidsOf(drivePlaylistNamed(t, dc, "通勤")), []string{fakeCID("a"), fakeCID("b"), fakeCID("c"), fakeCID("d")}) {
		t.Fatal("pull 半邊要落地")
	}
	if _, _, err := runPull(t, "pl", "push", "通勤", "--yes", "--provider", "apple"); exitOf(t, err) != 1 {
		t.Fatalf("push 明說 apple 仍是 exit 1:%v", err)
	}
}

// 一個清單的 push 半邊 refused(含 local file)不擋整輪(PR #36 review):cron 的 sync --all 照常 pull 兩個清單、push 另一個;
// 那一格只 pull 不 push、stderr 說明;pl push 對同一個清單仍是 exit 3。
func TestPlSyncRefusedPairOnlySkipsThatPush(t *testing.T) {
	fs1, fs2, dc, _ := syncWorld(t)
	fs1.setLocal("lf1")
	fs1.set("p2", "睡前", "x", "lf1")
	fs2.set("q2", "睡前", "x")
	mustPull(t, "pl", "link", "睡前", "spotify:p2")
	mustPull(t, "pl", "link", "睡前", "apple:q2")
	mustPull(t, "pl", "sync", "--all", "--yes")
	fs2.set("q2", "睡前", "x", "c") // 睡前 的 Apple 加 c(c 已有 spotify mapping)→ push 到 spotify 踩 local file
	fs2.set("q1", "通勤", "a", "c") // 通勤 的 Apple 刪 b → 一個完全正常的 pull + push
	out, errs := mustPull(t, "pl", "sync", "--all", "--yes")
	if !strings.Contains(errs, "跳過 睡前 的 spotify 的 push 半邊") || !strings.Contains(errs, "local file") {
		t.Fatalf("那一格只跳過並說明:%s", errs)
	}
	// 睡前 的 spotify 側沒有列(跳過);apple 側的 skip 是 local file 那首在 apple 沒 mapping,照常
	if acts := dirActions(out); !slices.Equal(acts, []string{"pull add apple", "pull remove apple", "push skip apple", "push remove spotify"}) {
		t.Fatalf("其餘照常:%v", acts)
	}
	if !slices.Equal(fs1.tracksOf("p1"), []string{"a", "c"}) || !slices.Equal(fs1.tracksOf("p2"), []string{"x", "lf1"}) {
		t.Fatalf("通勤 推到 Spotify、睡前 的 Spotify 不動:%v %v", fs1.tracksOf("p1"), fs1.tracksOf("p2"))
	}
	if !slices.Equal(cidsOf(drivePlaylistNamed(t, dc, "睡前")), []string{fakeCID("x"), "p:spotify:spotify:local:x:y:lf1:200", fakeCID("c")}) {
		t.Fatalf("睡前 的 pull 半邊要落地(含 c):%v", cidsOf(drivePlaylistNamed(t, dc, "睡前")))
	}
	if _, _, err := runPull(t, "pl", "push", "睡前", "--yes"); exitOf(t, err) != 3 {
		t.Fatalf("pl push 對同一個清單仍是 exit 3:%v", err)
	}
}

func TestPlSyncArgs(t *testing.T) {
	pullWorld(t)
	for _, args := range [][]string{{"pl", "sync"}, {"pl", "sync", "x", "--all"}, {"pl", "sync", "x", "--provider", "nope"}} {
		if _, _, err := runPull(t, args...); exitOf(t, err) != 1 {
			t.Fatalf("%v 要 exit 1:%v", args, err)
		}
	}
}
