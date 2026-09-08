package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
	_ "modernc.org/sqlite"

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
	wantRows := "" // 列出順序 = 上傳順序:manifest 最後(它宣告的檔要先存在)
	for _, n := range names {
		if n != "manifest.json" {
			wantRows += "create\t" + n + "\n"
		}
	}
	wantRows += "create\tmanifest.json\n"
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
	if fl, _ := dc.List(context.Background(), ""); fl[len(fl)-1].Name != "manifest.json" { // 假 Drive 依建立順序列出
		t.Fatalf("manifest 要最後上傳:%v", fl[len(fl)-1].Name)
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
	if _, errs := mustPull(t, "drive", "init", "--from-local", "--yes"); !strings.Contains(errs, "另有 1 個別台裝置的檔按設計不代傳") {
		t.Fatalf("沒缺時也要講明有東西刻意沒補:%q", errs)
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

// 逃生口不得有副作用:版本不符不改名、壞檔不刪、全新機器不建 db;錯誤訊息要指向保留的舊版快取而不是「先 pl pull」。
func TestExportNeverSelfHeals(t *testing.T) {
	setCLITestConfig(t)
	keyring.MockInit()
	if _, _, err := runPull(t, "export"); exitOf(t, err) != 1 {
		t.Fatal(err)
	}
	p, _ := store.Path()
	if _, err := os.Stat(p); err == nil {
		t.Fatal("全新機器上 export 不該建出 state.db")
	}
	fs, dc, srv := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	p, _ = store.Path()
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	old, _ := os.ReadFile(p)
	out, _, err := runPull(t, "export")
	if exitOf(t, err) != 1 || !errors.Is(err, store.ErrSchemaMismatch) || out != "" {
		t.Fatalf("版本不符:exit 1、不印:%v %q", err, out)
	}
	if b, _ := os.ReadFile(p); !bytes.Equal(b, old) {
		t.Fatal("export 不得改動 state.db")
	}
	if _, err := os.Stat(p + ".v99"); err == nil {
		t.Fatal("export 不得改名保留(那是 Open 的自癒,唯讀開法沒有)")
	}
	if _, _, err := runPull(t, "drive", "init", "--from-local", "--yes"); exitOf(t, err) != 1 || !errors.Is(err, store.ErrSchemaMismatch) {
		t.Fatalf("drive init 走同一個唯讀開法:%v", err)
	}
	garbage := []byte("this is definitely not a sqlite database file, not even close")
	if err := os.WriteFile(p, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runPull(t, "export"); exitOf(t, err) != 1 {
		t.Fatalf("壞檔:exit 1:%v", err)
	}
	if b, _ := os.ReadFile(p); !bytes.Equal(b, garbage) {
		t.Fatal("壞檔不得被刪掉或動到(那是使用者僅剩的一份)")
	}
	// 升版保留的舊版快取要被講出來,而不是叫人去 pl pull。
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p+".v2", []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runPull(t, "export"); err == nil || !strings.Contains(err.Error(), "state.db.v2") || !strings.Contains(err.Error(), "當時版本的 capy binary") {
		t.Fatalf("要指向保留的舊版快取:%v", err)
	}
	_, _ = dc, srv
}

// Drive 上還在的檔比這個 capy 新:init 不得把舊版 manifest 建到它旁邊(spec §6.6:任何檔 schema 太新 → exit 1、零寫入)。
func TestDriveInitRefusesNewerSchemaOnDrive(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	wipeDrive(t, dc)
	if _, err := dc.Create(context.Background(), "tracks.json", canon.TracksFile().Props, []byte(`{"schema_version":99,"tracks":{}}`+"\n")); err != nil {
		t.Fatal(err)
	}
	before := driveFiles(t, dc)
	_, _, err := runPull(t, "drive", "init", "--from-local", "--yes")
	if exitOf(t, err) != 1 || !errors.Is(err, canon.ErrSchemaTooNew) || !strings.Contains(err.Error(), "tracks.json") {
		t.Fatalf("Drive 上有更新版的檔 → exit 1 並點名:%v", err)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Fatal("零寫入")
	}
}
