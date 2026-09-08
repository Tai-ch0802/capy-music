package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

// exportFiles:跑 capy export,把每個值壓回 canon.Encode 的緊湊形式(MarshalIndent 會重新縮排 RawMessage,要比對得先壓回)。
func exportFiles(t *testing.T) map[string][]byte {
	t.Helper()
	out, _ := mustPull(t, "export")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("export 不是 JSON:%v\n%s", err, out)
	}
	files := map[string][]byte{}
	for name, v := range raw {
		var buf bytes.Buffer
		if err := json.Compact(&buf, v); err != nil {
			t.Fatal(err)
		}
		files[name] = append(buf.Bytes(), '\n')
	}
	return files
}

func wipeDrive(t *testing.T, dc *drive.Client) {
	t.Helper()
	ctx := context.Background()
	fs, err := dc.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if err := dc.Delete(ctx, f.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func deleteDriveFile(t *testing.T, dc *drive.Client, prefix string) {
	t.Helper()
	ctx := context.Background()
	fs, err := dc.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if strings.HasPrefix(f.Name, prefix) {
			if err := dc.Delete(ctx, f.ID); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("Drive 沒有 %s*", prefix)
}

func TestExportIsDriveFilesKeyedByNameAndNeedsNoGoogle(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a", "b")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	want := driveFiles(t, dc)
	if got := exportFiles(t); !sameFiles(want, got) {
		t.Fatalf("export 要與 Drive 檔逐位元同形:\n%v\n%v", keysOf(want), keysOf(got))
	}
	// 不依賴 Drive:Google 沒登入 / 網路壞了照樣匯出。
	orig := newDriveClient
	newDriveClient = func(context.Context) (*drive.Client, error) { return nil, errors.New("no drive") }
	t.Cleanup(func() { newDriveClient = orig })
	if got := exportFiles(t); !sameFiles(want, got) {
		t.Fatal("export 不得碰 Drive")
	}
}

func TestExportEmptyIsErrorNotEmptyJSON(t *testing.T) {
	setCLITestConfig(t)
	keyring.MockInit()
	out, _, err := runPull(t, "export")
	if exitOf(t, err) != 1 || !errors.Is(err, errNothingLocal) || out != "" {
		t.Fatalf("本機沒資料:exit 1、stdout 不印東西(capy export > backup.json 不可靜默寫出空殼):%v %q", err, out)
	}
}

func TestDriveInitRestoresAfterWipe(t *testing.T) {
	fs, dc, srv := pullWorld(t)
	fs.set("p1", "通勤", "a", "b")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	before, dump := driveFiles(t, dc), dumpBytes(t)
	wipeDrive(t, dc)
	if _, _, err := runPull(t, "pl", "pull", "--all", "--yes"); exitOf(t, err) != 3 {
		t.Fatalf("清空後 pull 要 exit 3:%v", err)
	}
	if _, _, err := runPull(t, "drive", "init"); err == nil || !strings.Contains(err.Error(), "--from-local") {
		t.Fatalf("沒有 --from-local 要拒絕:%v", err)
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for k := range before {
			if !yield(k) {
				return
			}
		}
	})
	wantRows := ""
	for _, n := range names {
		wantRows += "create\t" + n + "\n"
	}
	out, _, err := runPull(t, "drive", "init", "--from-local")
	if exitOf(t, err) != 2 || out != wantRows || srv.Len() != 0 {
		t.Fatalf("非 TTY 沒 --yes:列出要建的檔、exit 2、零寫入:%v\n%q\n%q", err, out, wantRows)
	}
	if _, _, err := runPull(t, "drive", "init", "--from-local", "--dry-run", "--yes"); exitOf(t, err) != 2 || srv.Len() != 0 {
		t.Fatalf("dry-run 零寫入:%v", err)
	}
	out, errs := mustPull(t, "drive", "init", "--from-local", "--yes")
	if out != wantRows || !strings.Contains(errs, "已補回 4 個檔") || !strings.Contains(errs, "tai@example.com") {
		t.Fatalf("套用,且 --yes 路徑也要印出目標 Google 帳號:%q %q", out, errs)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("補回的檔要與清空前逐位元相同")
	}
	if !bytes.Equal(dump, dumpBytes(t)) {
		t.Fatal("init 不動本機 cache")
	}
	if out, _ := mustPull(t, "pl", "pull", "--all", "--yes"); out != "" || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("補回後 pull 過閘且零變更:%q", out)
	}
}

func TestDriveInitOnlyFillsMissingNeverOverwrites(t *testing.T) {
	fs, dc, srv := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	before := driveFiles(t, dc)
	ctx := context.Background()
	// 別台裝置改過 tracks.json(Drive 上的版本比本機新),再弄丟 pl 檔。
	srv.Advance(1)
	fl, _ := dc.List(ctx, "")
	changed := []byte(`{"schema_version":1,"tracks":{}}` + "\n")
	for _, f := range fl {
		if f.Name == "tracks.json" {
			if _, err := dc.Update(ctx, f.ID, canon.TracksFile().Props, changed); err != nil {
				t.Fatal(err)
			}
		}
	}
	deleteDriveFile(t, dc, "pl__")
	out, _ := mustPull(t, "drive", "init", "--from-local", "--yes")
	if strings.Count(out, "create\t") != 1 || !strings.Contains(out, "create\tpl__") {
		t.Fatalf("只補缺的那個檔:%q", out)
	}
	after := driveFiles(t, dc)
	if !bytes.Equal(after["tracks.json"], changed) {
		t.Fatal("Drive 上還在的檔不可覆寫(Drive 為準)")
	}
	for name, b := range before {
		if name != "tracks.json" && !bytes.Equal(after[name], b) {
			t.Fatalf("%s 要與原本相同", name)
		}
	}
	// 什麼都不缺:exit 0、零寫入。
	n := srv.Len()
	if _, errs := mustPull(t, "drive", "init", "--from-local", "--yes"); !strings.Contains(errs, "不需要補") || srv.Len() != n {
		t.Fatalf("沒缺就零寫入:%q", errs)
	}
}

func TestDriveInitSkipsOtherDevicesAndReportsLost(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	// 本機 cache 裡有別台裝置的 dev 檔(上次 pull 從 Drive 帶回來的)。
	st, err := store.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.Dump()
	if err != nil {
		t.Fatal(err)
	}
	c.Devices = append(c.Devices, *canon.NewDeviceState("01TESTDEVICEB0000000000000"))
	if err := st.Hydrate(c); err != nil {
		t.Fatal(err)
	}
	st.Close()
	wipeDrive(t, dc)
	out, errs := mustPull(t, "drive", "init", "--from-local", "--yes")
	if strings.Contains(out, "dev__01TESTDEVICEB") || !strings.Contains(out, "create\tdev__01TESTDEVICE00000000000000.json") || !strings.Contains(errs, "dev__01TESTDEVICEB0000000000000.json") {
		t.Fatalf("別台裝置的 dev 檔不代為上傳、只建自己的,且要講明:%q %q", out, errs)
	}
	// manifest 宣告了 Drive 與本機都沒有的清單:補不回,講明出路,零寫入。
	wipeDrive(t, dc)
	ctx := context.Background()
	if _, err := dc.Create(ctx, "manifest.json", canon.ManifestFile().Props, []byte(`{"schema_version":1,"devices":[],"playlists":["01LOSTLOSTLOSTLOSTLOSTLOST"]}`+"\n")); err != nil {
		t.Fatal(err)
	}
	n := len(driveFiles(t, dc))
	_, _, err = runPull(t, "drive", "init", "--from-local", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "pl__01LOSTLOSTLOSTLOSTLOSTLOST.json") || !strings.Contains(err.Error(), "刪除隱藏的應用程式資料") {
		t.Fatalf("補不回要講明出路:%v", err)
	}
	if len(driveFiles(t, dc)) != n {
		t.Fatal("零寫入")
	}
}

func TestDriveInitNeedsLocalData(t *testing.T) {
	_, _, srv := pullWorld(t)
	if _, _, err := runPull(t, "drive", "init", "--from-local", "--yes"); exitOf(t, err) != 1 || !errors.Is(err, errNothingLocal) || srv.Len() != 0 {
		t.Fatalf("本機沒資料:exit 1、零寫入:%v", err)
	}
}
