package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const devA, devB = "01TESTDEVICE00000000000000", "01TESTDEVICE0000000000000B"

// localWorld:pullWorld(假 Spotify + 假 Drive + 裝置 A)+ 真的本機曲庫目錄;newProvider 對 "local" 走真的 local.New,其餘仍是假 Spotify。
func localWorld(t *testing.T) (*fakeSpotify, *drive.Client, string) {
	t.Helper()
	fs, dc, _ := pullWorld(t)
	root := t.TempDir()
	writeFile(t, root, "library.json", `{"schema_version":1,"tracks":{
	  "a.mp3":{"title":"song-a","artists":["artist"],"duration_ms":200000,"isrc":"TW000000000A"},
	  "b.flac":{"title":"song-b","artists":["artist"],"duration_ms":200000,"isrc":"TW000000000B"},
	  "with space.mp3":{"title":"song-space","artists":["artist"],"duration_ms":200000}}}`)
	writeFile(t, root, "通勤.m3u8", "#EXTM3U\na.mp3\nb.flac\nwith space.mp3\n")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.LocalRoot = root
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id == "local" {
			return newLocalProvider()
		}
		return orig(ctx, id)
	}
	t.Cleanup(func() { newProvider = orig })
	return fs, dc, root
}

func writeFile(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setDevice(t *testing.T, id string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DeviceID = id
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
}

// link + pull:id 帶 device 前綴、使用者只打檔名;cid 含 / 與空白也走得完 pull → export → 刪 db 重建(cid 是不透明字串,計畫 §2 A4)。
func TestPlLocalLinkPullAndRebuild(t *testing.T) {
	_, dc, _ := localWorld(t)
	out, _ := mustPull(t, "pl", "list", "--provider", "local")
	if !strings.Contains(out, devA+"/通勤.m3u8\t通勤\t3") {
		t.Fatalf("pl list:%s", out)
	}
	mustPull(t, "pl", "link", "通勤", "local:通勤.m3u8")
	pl := drivePlaylist(t, dc)
	if pl.Links["local"] != devA+"/通勤.m3u8" {
		t.Fatalf("link id 帶 device 前綴:%v", pl.Links)
	}
	out, _ = mustPull(t, "pl", "pull", "通勤", "--yes")
	if strings.Count(out, "add\t") != 3 {
		t.Fatalf("pull 三首:%s", out)
	}
	want := []string{"i:TW000000000A", "i:TW000000000B", "p:local:with space.mp3"}
	if got := cidsOf(drivePlaylist(t, dc)); !slices.Equal(got, want) {
		t.Fatalf("cid:有 ISRC 用 i:、沒有用 p:local:<路徑>(不帶 device:接管後不分裂):%v", got)
	}
	before, dump := driveFiles(t, dc), dumpBytes(t)
	deleteDB(t)
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); strings.TrimSpace(out) != "" {
		t.Fatalf("重建後 pull 零變更:%s", out)
	}
	if !sameFiles(before, driveFiles(t, dc)) || string(dump) != string(dumpBytes(t)) {
		t.Fatal("刪 db 重建後 Drive 與本機快取都要等價(cid 含 / 與空白也一樣)")
	}
	if b, ok := baseOfProv(t, dc, "local"); !ok || b.ID != devA+"/通勤.m3u8" || len(b.Items) != 3 {
		t.Fatalf("base 記本機清單 id:%+v", b)
	}
}

func baseOfProv(t *testing.T, dc *drive.Client, prov string) (canon.Snapshot, bool) {
	t.Helper()
	for name, b := range driveFiles(t, dc) {
		if strings.HasPrefix(name, "dev__") {
			dev, err := canon.Decode[canon.DeviceState](b)
			if err != nil {
				t.Fatal(err)
			}
			for _, byProv := range dev.Base {
				if s, ok := byProv[prov]; ok {
					return s.Snapshot, true
				}
			}
		}
	}
	return canon.Snapshot{}, false
}

// 決策 33 的回歸測試:別台裝置跑 cron 的 sync --all 不會把本機清單的連結當 gone 刪掉、不 refused、不動 base;
// 在那台 pl link 同名檔 = 接管(Q30),原裝置之後變 foreign。
func TestPlLocalForeignSkippedAndRelinkTakesOver(t *testing.T) {
	fs, dc, root := localWorld(t)
	fs.set("p1", "通勤", "a", "b")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "link", "通勤", "local:通勤.m3u8")
	mustPull(t, "pl", "sync", "通勤", "--yes")
	if _, _, err := runPull(t, "pl", "push", "通勤", "--yes", "--provider", "local"); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "不支援寫入") {
		t.Fatalf("T1 的 local 沒有寫入端,明說 --provider local 是 exit 1:%v", err)
	}
	cidsA := cidsOf(drivePlaylist(t, dc))
	setDevice(t, devB) // 裝置 B:同一份 Drive,自己的 local_root 也有一份 通勤.m3u8(同名不同機器;沒 ISRC 的 with space 也在——接管後它的 cid 必須跟 A 的同一個)
	writeFile(t, root, "通勤.m3u8", "#EXTM3U\na.mp3\nwith space.mp3\n")
	before := driveFiles(t, dc)
	out, errs := mustPull(t, "pl", "sync", "--all", "--yes")
	if !strings.Contains(errs, "跳過 通勤 的 local:"+devA+"/通勤.m3u8 屬於裝置") || !strings.Contains(errs, "capy pl link 通勤 local:<檔名>") || strings.Contains(errs, "取消連結") {
		t.Fatalf("foreign 只跳過並指路:%s%s", out, errs)
	}
	if strings.Count(errs, "跳過 通勤 的 local") != 1 { // push 半邊不重印(也不印「不支援寫入」)
		t.Fatalf("只說一次:%s", errs)
	}
	if strings.Contains(out, "\tlocal\t") { // provider 欄不會是 local(cid 裡的 p:local: 不算)
		t.Fatalf("foreign 的 local 不出現在變更集:%s", out)
	}
	pl := drivePlaylist(t, dc)
	if pl.Links["local"] != devA+"/通勤.m3u8" {
		t.Fatalf("A 的連結不能被 B 刪掉或改掉:%v", pl.Links)
	}
	if _, ok := baseOfProv(t, dc, "local"); !ok {
		t.Fatal("A 的 base 要還在")
	}
	after := driveFiles(t, dc) // B 會建自己的 dev 檔(spotify 的首次 base)與註冊進 manifest;A 的 dev 檔與 pl__ 一個位元組都不動
	for name := range before {
		if strings.HasPrefix(name, "pl__") || name == "dev__"+devA+".json" {
			if string(before[name]) != string(after[name]) {
				t.Fatalf("B 這輪不得動 %s", name)
			}
		}
	}
	if _, _, err := runPull(t, "pl", "push", "通勤", "--yes", "--provider", "local"); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "屬於裝置") || !strings.Contains(err.Error(), "capy pl link 通勤 local:<檔名>") {
		t.Fatalf("B 上明說 --provider local 推別台的:exit 1 並指路(不是靜靜的無變更):%v", err)
	}
	if _, errs, err := runPull(t, "pl", "push", "通勤", "--yes"); err != nil || !strings.Contains(errs, "屬於裝置") {
		t.Fatalf("沒指定平台的單獨 push:跳過並說明、exit 0(不是 refused):%v %s", err, errs)
	}
	// B 接管
	out, _ = mustPull(t, "pl", "link", "通勤", "local:通勤.m3u8")
	if !strings.Contains(out, "原本連到裝置") || !strings.Contains(out, "已改為本機") {
		t.Fatalf("接管要說明:%s", out)
	}
	if pl := drivePlaylist(t, dc); pl.Links["local"] != devB+"/通勤.m3u8" {
		t.Fatalf("接管後連到 B:%v", pl.Links)
	}
	out, _ = mustPull(t, "pl", "pull", "通勤", "--yes")
	if strings.Contains(out, "remove\t") { // B 的檔沒有 b:沒有 base(A 的不算數)→ 首次 pull 永不移除
		t.Fatalf("接管後第一次 pull 是首次 pull、不移除:%s", out)
	}
	if got := cidsOf(drivePlaylist(t, dc)); !slices.Equal(got, cidsA) { // 回歸(PR #37 review):cid 不帶 device,接管後沒 ISRC 的 with space 不會變成兩首
		t.Fatalf("接管後沒 ISRC 的本機曲目不能分裂成兩個 cid:%v(之前 %v)", got, cidsA)
	}
	setDevice(t, devA)
	if _, errs := mustPull(t, "pl", "sync", "--all", "--yes"); !strings.Contains(errs, "跳過 通勤 的 local:"+devB+"/通勤.m3u8 屬於裝置") {
		t.Fatalf("原裝置 A 之後變 foreign:%s", errs)
	}
}

// config / doctor / auth login 對 local 的說法。
func TestLocalConfigDoctorAndAuth(t *testing.T) {
	_, _, root := localWorld(t)
	if _, _, err := runPull(t, "config", "set", "local_root", filepath.Join(root, "nope")); err == nil || !strings.Contains(err.Error(), "不是存在的目錄") {
		t.Fatalf("local_root 要是存在的目錄:%v", err)
	}
	mustPull(t, "config", "set", "local_root", root)
	if out, _ := mustPull(t, "config", "get", "local_root"); strings.TrimSpace(out) != root {
		t.Fatalf("config get:%s", out)
	}
	if out, _ := mustPull(t, "doctor", "--provider", "local"); !strings.Contains(out, "1 個清單檔") || !strings.Contains(out, "全部通過") {
		t.Fatalf("doctor:%s", out)
	}
	if _, _, err := runPull(t, "auth", "login", "local"); err == nil || !strings.Contains(err.Error(), "config set local_root") {
		t.Fatalf("auth login local 要指路:%v", err)
	}
	if _, _, err := runPull(t, "play", "--provider", "local", "x"); err == nil || !strings.Contains(err.Error(), "不支援") {
		t.Fatalf("play 對 local 是 ErrNotSupported(Q28):%v", err)
	}
	setDevice(t, "") // 還沒登入 Google 就沒有裝置 id:指路,不產生 /通勤.m3u8 這種 id
	if _, _, err := runPull(t, "pl", "list", "--provider", "local"); err == nil || !strings.Contains(err.Error(), "capy auth login google") {
		t.Fatalf("沒有裝置 id 要指路:%v", err)
	}
	setDevice(t, devA)
	cfg, _ := config.Load()
	cfg.LocalRoot = ""
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runPull(t, "pl", "list", "--provider", "local"); err == nil || !strings.Contains(err.Error(), "capy config set local_root") {
		t.Fatalf("沒設 local_root 要指路:%v", err)
	}
}

// local ↔ Spotify 的一輪(T3 的前哨):Spotify 刪 b → pull 進 C;push 到 local 在 T1 沒有寫入端 → sync 只跳過那一格。
func TestPlLocalSyncWithSpotifyReadOnlyHalf(t *testing.T) {
	fs, dc, _ := localWorld(t)
	fs.set("p1", "通勤", "a", "b")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "link", "通勤", "local:通勤.m3u8")
	mustPull(t, "pl", "sync", "通勤", "--yes") // bootstrap:local 多一首 with space(C = a, b, space)
	fs.set("p1", "通勤", "a")
	out, errs := mustPull(t, "pl", "sync", "通勤", "--yes")
	if !strings.Contains(errs, "跳過 通勤 的 local:") || !strings.Contains(errs, "不支援寫入") || strings.Contains(out, "push\tremove\tlocal") {
		t.Fatalf("local 沒有寫入端就只跳過 push 半邊:%s%s", out, errs)
	}
	if got := cidsOf(drivePlaylist(t, dc)); !slices.Equal(got, []string{"i:TW000000000A", "p:local:with space.mp3"}) { // b 靠 ISRC 是同一首,兩邊都算掉
		t.Fatalf("Spotify 的刪除進 C:%v", got)
	}
}
