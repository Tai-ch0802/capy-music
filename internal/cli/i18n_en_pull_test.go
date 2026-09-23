package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
)

// 英文介面下 pl link / unlink / pull 的說法(pull.go 的字串)。resolve / push / sync 等檔還在搬,
// 所以只比對 pull.go 自己印的整句,不對混了別檔字串的整段輸出斷言「沒有中文」。

func enPID(t *testing.T, name string, files map[string][]byte) string {
	t.Helper()
	for fn, b := range files {
		if pl, err := canon.Decode[canon.Playlist](b); strings.HasPrefix(fn, "pl__") && err == nil && pl.Name == name {
			return pl.PID
		}
	}
	t.Fatalf("Drive 上沒有 %s", name)
	return ""
}

func TestEnglishPlLinkPullUnlink(t *testing.T) {
	withLanguage(t, "en")
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "commute", "t1", "t2")
	out, errs := mustPull(t, "pl", "link", "commute", "spotify:p1")
	pid := enPID(t, "commute", driveFiles(t, dc))
	if errs != "Created master copy commute ("+pid+")\n" || out != "Linked commute ("+pid+") ↔ spotify:p1; next, run capy pl pull commute\n" {
		t.Fatalf("link:%q %q", out, errs)
	}
	// 非 TTY 沒 --yes:exit 2。網頁 console.js 靠 exit 2 訊息裡的 --yes 補「未套用:加 --yes 重跑」,每個語系都要留著它。
	if _, _, err := runPull(t, "pl", "pull", "commute"); exitOf(t, err) != 2 || err.Error() != "2 changes pending: pass --yes to apply them, or run this in a terminal to confirm" {
		t.Fatalf("待套用:%v", err)
	}
	if _, errs := mustPull(t, "pl", "pull", "commute", "--yes"); !strings.Contains(errs, "Applied 2 changes\n") {
		t.Fatalf("套用:%q", errs)
	}
	fs.set("p1", "commute", "t1", "t2", "t3")
	if _, _, err := runPull(t, "pl", "pull", "commute", "--dry-run"); exitOf(t, err) != 2 || err.Error() != "1 change pending: pass --yes to apply it, or run this in a terminal to confirm" {
		t.Fatalf("單數:%v", err)
	}
	if _, errs := mustPull(t, "pl", "pull", "commute", "--yes"); !strings.Contains(errs, "Applied 1 change\n") {
		t.Fatalf("單數套用:%q", errs)
	}
	if _, errs := mustPull(t, "pl", "pull", "commute", "--yes"); !strings.Contains(errs, "No changes\n") {
		t.Fatalf("無變更:%q", errs)
	}
	if out, _ := mustPull(t, "pl", "unlink", "commute", "spotify"); out != "Unlinked commute ("+pid+") ↔ spotify:p1\n" {
		t.Fatalf("unlink:%q", out)
	}
	for args, want := range map[string]string{
		"pl pull commute":                  "commute (" + pid + ") isn't linked to any platform playlist — run capy pl link first",
		"pl pull nothing":                  `master copy "nothing" not found — run capy pl link nothing <provider>:<playlist> first`,
		"pl unlink commute apple":          "commute (" + pid + ") isn't linked to apple",
		"pl unlink nothing apple":          `master copy "nothing" not found`,
		"pl unlink commute tidal":          `provider must be spotify|apple|local: "tidal"`,
		"pl pull commute --provider tidal": `provider must be spotify|apple|local: "tidal"`,
		"pl pull --all --yes --force":      "--force works with a single playlist only (capy pl pull <name> --force), not with --all",
	} {
		if _, _, err := runPull(t, strings.Fields(args)...); err == nil || err.Error() != want {
			t.Errorf("%s:\n got %v\nwant %s", args, err, want)
		}
	}
}

func TestEnglishPlLinkRefusals(t *testing.T) {
	withLanguage(t, "en")
	fs, dc, srv := pullWorld(t)
	fs.set("ed", "Today's Top Hits")
	fs.restricted["ed"] = true
	fs.set("p1", "commute", "t1")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	pid := enPID(t, "commute", driveFiles(t, dc))
	fs.set("p2", "winter", "t2")
	pub := "37i9dQZF1DXcBWIGoYBM5M"
	fs.mu.Lock()
	fs.items[pub] = []string{"z"}
	fs.mu.Unlock()
	fs.set("p10", "another", "n")
	fs.set("p5", "run", "r1")
	fs.set("p6", "run", "r2")
	for _, c := range []struct{ args, want string }{
		{"pl link hits spotify:ed", "can't read the tracks of spotify playlist ed (an app in development mode can't get Spotify's own or other users' playlists), so it can't be linked"},
		{"pl link other spotify:p1", "spotify:p1 is already linked to master copy commute (" + pid + "); a platform playlist can be linked to only one master copy"},
		{"pl link commute spotify:p2", "commute (" + pid + ") is already linked to spotify:p1; run capy pl unlink commute spotify first"},
		{"pl link commute tidal:p2", `the format is <provider>:<playlist-id-or-name>, where provider is spotify|apple|local: "tidal:p2"`},
		{"pl link public spotify:" + pub, "spotify:" + pub + " isn't in your list of playlists (only what capy pl list shows counts): pull treats playlists missing from that list as deleted and unlinks them automatically, so it can't be linked"},
		{"pl link 01ARZ3NDEKTSV4RRFFQ69G5FAV spotify:p10", "no master copy has pid 01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		// --create:平台上已有同名(一個 / 多個)、第二個參數給成 provider:ref,都在建清單之前擋下。
		{"pl link --create winter spotify", `spotify already has a playlist named "winter" (p2): to link it, run capy pl link "winter" spotify:p2; if you really want another one, create it in the app first and link it with spotify:<ID>`},
		{"pl link --create run spotify", `spotify already has 2 playlists named "run" (p5, p6): pick one and link it with capy pl link "run" spotify:<ID>`},
		{"pl link --create night spotify:p1", `"spotify:p1" isn't a platform: with --create, the second argument is just the platform (spotify|apple|local), and the new playlist is named after the master copy`},
	} {
		if _, _, err := runPull(t, strings.Fields(c.args)...); err == nil || err.Error() != c.want {
			t.Errorf("%s:\n got %v\nwant %s", c.args, err, c.want)
		}
	}
	if len(fs.written()) != 0 {
		t.Fatalf("擋下來的不可建清單:%+v", fs.written())
	}
	if _, errs := mustPull(t, "pl", "link", "road", "spotify", "--create"); !strings.Contains(errs, "Created playlist road (new1) on spotify\n") {
		t.Fatalf("--create:%q", errs)
	}
	// 建好之後 COMMIT 失敗:錯誤後面要接上「清單已建好、怎麼接回去」。
	srv.FailOn(func(r *http.Request) bool { return r.Method == http.MethodPatch }, http.StatusInternalServerError, "backendError")
	_, _, err := runPull(t, "pl", "link", "night", "spotify", "--create")
	srv.FailOn(nil, 0, "")
	if exitOf(t, err) != 1 || !strings.HasSuffix(err.Error(), `; the empty playlist new2 was created on spotify, but the link wasn't written to Drive: run capy pl link "night" spotify:new2 to reconnect it`) {
		t.Fatalf("COMMIT 失敗:%v", err)
	}
}

// 安全閥:刪除閾值與 Drive 不完整(exit 3),英文每一句都要完整,包括理由與出口。
func TestEnglishPlPullSafetyChecks(t *testing.T) {
	withLanguage(t, "en")
	fs, dc, _ := pullWorld(t)
	ids := make([]string, 12)
	for i := range ids {
		ids[i] = fmt.Sprintf("t%02d", i)
	}
	fs.set("p1", "commute", ids...)
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "pull", "commute", "--yes")
	fs.set("p1", "commute", ids[0])
	_, _, err := runPull(t, "pl", "pull", "commute", "--yes")
	if want := "commute would remove 11 tracks on spotify (12 visible), over the threshold. Pass --force to override (check what would be removed with --dry-run first)"; exitOf(t, err) != 3 || err.Error() != want {
		t.Fatalf("閾值:\n got %v\nwant %s", err, want)
	}
	pid := enPID(t, "commute", driveFiles(t, dc))
	deleteDriveFile(t, dc, "pl__")
	_, _, err = runPull(t, "pl", "pull", "commute", "--yes", "--force")
	want := "the Drive appdata is incomplete; can't get pl__" + pid + ".json. This is not treated as \"you deleted everything\": nothing was written, and neither --yes nor --force overrides it. " +
		"If Drive really was wiped, or you're logged in to the wrong Google account (currently tai@example.com), the way out is capy drive init --from-local (it only restores the files missing on Drive; this computer's state.db is the only copy left, so don't delete it)"
	if exitOf(t, err) != 3 || err.Error() != want {
		t.Fatalf("Drive 不完整:\n got %v\nwant %s", err, want)
	}
	if err := config.Save(&config.Config{DeviceID: devA}); err != nil { // 沒記 email:講不出是哪個帳號
		t.Fatal(err)
	}
	if _, _, err := runPull(t, "pl", "pull", "commute"); err == nil || !strings.Contains(err.Error(), "(currently unknown account)") {
		t.Fatalf("沒有 email:%v", err)
	}
}

// pull 印在 stderr 的旁白:平台上消失、讀不到。
func TestEnglishPlPullGoneAndRestricted(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := pullWorld(t)
	fs.set("p1", "commute", "a")
	fs.set("p2", "winter", "b")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "link", "winter", "spotify:p2")
	mustPull(t, "pl", "pull", "--all", "--yes")
	fs.restricted["p2"] = true
	fs.drop("p1")
	_, errs := mustPull(t, "pl", "pull", "--all", "--yes")
	for _, w := range []string{
		"Warning: playlist p1 can't be found on spotify, so commute will be unlinked from it (Q6); the master copy is left as is\n",
		"Skipping spotify:p2 of winter (an app in development mode can't read Spotify's own or other users' playlists; the playlist hasn't disappeared)\n",
	} {
		if !strings.Contains(errs, w) {
			t.Errorf("stderr 少了 %q:\n%s", w, errs)
		}
	}
}

// 別台裝置的本機清單:pull 跳過並說怎麼接手;在這台 link 就接管並說明(裝置名來自 manifest)。
func TestEnglishPlLocalForeignAndTakeover(t *testing.T) {
	withLanguage(t, "en")
	fs, dc, root := localWorld(t)
	writeFile(t, root, "mix.m3u8", "#EXTM3U\na.mp3\n")
	fs.set("p1", "commute", "a")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "link", "commute", "local:mix.m3u8")
	mustPull(t, "pl", "pull", "commute", "--yes")
	pid := enPID(t, "commute", driveFiles(t, dc))
	host := ""
	for _, d := range decodeFile[canon.Manifest](t, driveFiles(t, dc), "manifest.json").Devices {
		if d.ID == devA {
			host = d.Name
		}
	}
	device := host + " (" + devA + ")"
	setDevice(t, devB)
	_, errs := mustPull(t, "pl", "pull", "commute", "--yes")
	if want := "Skipping local:" + devA + "/mix.m3u8 of commute: it belongs to device " + device + " (to take it over on this computer: capy pl link commute local:<file-name>)\n"; !strings.Contains(errs, want) {
		t.Fatalf("stderr 少了 %q:\n%s", want, errs)
	}
	out, _ := mustPull(t, "pl", "link", "commute", "local:mix.m3u8")
	if want := "commute (" + pid + ") was linked to local on device " + device + "; it now points to this computer, and that device will skip this playlist from now on\n"; !strings.HasPrefix(out, want) {
		t.Fatalf("接管:\n got %q\nwant %q", out, want)
	}
}

// 版本守衛、重複檔警告、合併殘留自癒:pull.go 印的整句。
func TestEnglishPlPullDriveNotices(t *testing.T) {
	withLanguage(t, "en")
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "commute", "a", "b")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "pull", "commute", "--yes")
	pid := enPID(t, "commute", driveFiles(t, dc))
	body, err := canon.Encode(driveTracks(t, dc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dc.Create(context.Background(), "tracks.json", canon.TracksFile().Props, body); err != nil {
		t.Fatal(err)
	}
	fs.set("p1", "commute", "a", "b", "c")
	fs.setHook(once(func() {
		pl := decodeFile[canon.Playlist](t, driveFiles(t, dc), "pl__")
		pl.Links["apple"] = "ap1"
		putPlaylist(t, dc, pl)
	}))
	_, errs, err := runPull(t, "pl", "pull", "commute", "--yes")
	fs.setHook(nil)
	if !strings.Contains(errs, "Warning: the Drive appdata has 2 copies of tracks.json; using the newest one\n") {
		t.Errorf("重複檔:%q", errs)
	}
	if want := "files on Drive changed while this was running (pl__" + pid + ".json was changed by another device); nothing was written, run it again"; exitOf(t, err) != 1 || err.Error() != want {
		t.Fatalf("守衛:\n got %v\nwant %s", err, want)
	}

	// 自癒:人工合併 a、b,清單刻意還指著敗者。
	fs2, dc2, _ := pullWorld(t)
	fs2.set("p1", "commute", "a", "b")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "pull", "commute", "--yes")
	tracks := driveTracks(t, dc2)
	if _, err := canon.Merge(tracks, nil, fakeCID("a"), fakeCID("b")); err != nil {
		t.Fatal(err)
	}
	putTracks(t, dc2, tracks)
	_, errs = mustPull(t, "pl", "pull", "commute", "--dry-run")
	if want := "Repaired 1 playlist item that still pointed at a merged-away cid after an interrupted merge (in commute; the last write merged tracks.json but stopped before updating the playlist data); it will be uploaded with the next write\n"; !strings.Contains(errs, want) {
		t.Fatalf("自癒:%q", errs)
	}
}

// 挑選器的標題與選項(pull 一段、unlink 兩段、link 三段)。
func TestEnglishPullPickers(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := pullWorld(t)
	fs.set("p1", "commute", "t1")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	log := stubPickers(t, 0)
	if _, _, err := runPull(t, "pl", "pull", "--dry-run"); err != nil && exitOf(t, err) == 1 {
		t.Fatal(err)
	}
	if len(log.titles) != 1 || log.titles[0] != "Pick a playlist to pull" {
		t.Errorf("pull:%q", log.titles)
	}
	log = stubPickers(t, 0, 0)
	mustPull(t, "pl", "unlink")
	if len(log.titles) != 2 || log.titles[0] != "Pick a playlist to unlink" || log.titles[1] != "Unlink which platform?" {
		t.Errorf("unlink:%q", log.titles)
	}
	log = stubPickers(t, 0, 0, -1) // spotify → commute → 第三段取消
	if _, _, err := runPull(t, "pl", "link"); err == nil {
		t.Fatal("第三段取消要回錯")
	}
	if len(log.titles) != 3 || log.titles[0] != "Which platform should it link to?" {
		t.Fatalf("link:%q", log.titles)
	}
	if got := log.nth(1); len(got) == 0 || got[len(got)-1] != "+ Create a new empty playlist on spotify (named after the playlist you pick next)" {
		t.Errorf("link 第二段:%q", got)
	}
}

// 說明文字:pl pull / link / unlink 的 help 只有 pull.go 與 root(已搬)的字串。
func TestEnglishPullHelp(t *testing.T) {
	withLanguage(t, "en")
	setCLITestConfig(t)
	for args, wants := range map[string][]string{
		"pl pull --help":   {"Platform → master copy → Drive (spec §6.1, §6.5)", "reason_code is a fixed code for scripts", "pull all linked playlists", "override the removal threshold"},
		"pl link --help":   {"Link a master copy to a platform playlist", "link [name|pid] [provider]:[playlist-id|name] (or [provider] --create)", "works with Spotify and Apple Music, not local"},
		"pl unlink --help": {"Unlink a master copy from a platform playlist"},
	} {
		out, err := runCLI(t, strings.Fields(args)...)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range wants {
			if !strings.Contains(out, w) {
				t.Errorf("%s 少了 %q:\n%s", args, w, out)
			}
		}
		if hasCJK(out) {
			t.Errorf("%s 還有中文:\n%s", args, out)
		}
	}
}
