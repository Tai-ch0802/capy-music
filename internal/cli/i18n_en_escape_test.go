package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

// 英文介面下 export / drive init 的說法(escape.go 的字串;其他檔還在搬,只斷言 escape.go 自己印的東西)。

func TestEnglishExportErrors(t *testing.T) {
	withLanguage(t, "en")
	setCLITestConfig(t)
	keyring.MockInit()
	const nothing = "there is no canonical data on this computer (state.db is missing or empty): nothing to export; run capy pl pull first"
	if _, _, err := runPull(t, "export"); !errors.Is(err, errNothingLocal) || err.Error() != nothing {
		t.Fatalf("%v", err)
	}
	p, _ := store.Path()
	if err := os.WriteFile(p+".v2", []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := nothing + "; also found " + p + ".v2, kept from before an upgrade — to recover from a kept cache, run capy export with the capy binary of that version"
	if _, _, err := runPull(t, "export"); err == nil || err.Error() != want {
		t.Fatalf("got  %v\nwant %s", err, want)
	}
}

func TestEnglishDriveInit(t *testing.T) {
	withLanguage(t, "en")
	fs, dc, _ := pullWorld(t)
	if _, _, err := runPull(t, "drive", "init"); err == nil || err.Error() != "only --from-local is supported for now (restores the files missing on Drive from this computer's state.db)" {
		t.Fatalf("%v", err)
	}
	if _, _, err := runPull(t, "drive", "init", "--from-local", "--yes"); err == nil || err.Error() != "there is no canonical data on this computer (state.db is missing or empty): nothing to restore to Drive" {
		t.Fatalf("%v", err)
	}
	fs.set("p1", "commute", "a", "b")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "pull", "commute", "--yes")
	wipeDrive(t, dc)
	if _, errs := mustPull(t, "drive", "init", "--from-local", "--yes"); errs != "Restored 4 files to the Drive appdata of tai@example.com; next, run capy pl pull --all\n" {
		t.Fatalf("%q", errs)
	}
	deleteDriveFile(t, dc, "pl__")
	if _, errs := mustPull(t, "drive", "init", "--from-local", "--yes"); errs != "Restored 1 file to the Drive appdata of tai@example.com; next, run capy pl pull --all\n" {
		t.Fatalf("單數:%q", errs)
	}
	if _, errs := mustPull(t, "drive", "init", "--from-local", "--yes"); errs != "Drive already has every file this computer knows about; nothing to restore\n" {
		t.Fatalf("%q", errs)
	}
}

func TestEnglishDriveInitOtherDevicesAndLost(t *testing.T) {
	withLanguage(t, "en")
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "commute", "a")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "pull", "commute", "--yes")
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
	const skipped = "Not uploading another device's file on its behalf (each device writes only its own, spec §6.3): dev__01TESTDEVICEB0000000000000.json; the next time that device pulls, it will have no base and will only add, never remove\n"
	if _, errs := mustPull(t, "drive", "init", "--from-local", "--yes"); errs != skipped+"Restored 4 files to the Drive appdata of tai@example.com; next, run capy pl pull --all\n" {
		t.Fatalf("%q", errs)
	}
	if _, errs := mustPull(t, "drive", "init", "--from-local", "--yes"); errs != skipped+"Drive already has every file this computer knows about; nothing to restore. 1 file from another device is deliberately not uploaded\n" {
		t.Fatalf("%q", errs)
	}
	wipeDrive(t, dc)
	if _, err := dc.Create(context.Background(), "manifest.json", canon.ManifestFile().Props, []byte(`{"schema_version":1,"devices":[],"playlists":["01LOSTLOSTLOSTLOSTLOSTLOST"]}`+"\n")); err != nil {
		t.Fatal(err)
	}
	const lost = "the manifest on Drive lists pl__01LOSTLOSTLOSTLOSTLOSTLOST.json, but neither Drive nor this computer has that file: it can't be restored, and pl pull will keep exiting with 3. " +
		"That playlist is lost (it's in neither place); the only way out is to clear the appdata in your Google Account settings (Manage apps → Delete hidden app data) and run capy drive init --from-local again (the manifest is rebuilt from this computer, without it)"
	if _, _, err := runPull(t, "drive", "init", "--from-local", "--yes"); exitOf(t, err) != 1 || err.Error() != lost {
		t.Fatalf("%v", err)
	}
	wipeDrive(t, dc)
	if _, err := dc.Create(context.Background(), "tracks.json", canon.TracksFile().Props, []byte(`{"schema_version":99,"tracks":{}}`+"\n")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runPull(t, "drive", "init", "--from-local", "--yes"); !errors.Is(err, canon.ErrSchemaTooNew) || !strings.HasPrefix(err.Error(), "tracks.json on Drive: the files on Drive use a newer schema") {
		t.Fatalf("%v", err)
	}
}

// 說明文字:只比對 escape.go 的字串(root 的 persistent flag 屬別的檔)。
func TestEnglishEscapeHelp(t *testing.T) {
	withLanguage(t, "en")
	setCLITestConfig(t)
	for args, wants := range map[string][]string{
		"export --help":     {"Reads only the local state.db and never touches Drive", "capy export > backup.json never silently writes an empty file"},
		"drive --help":      {"Maintenance commands for the Google Drive appdata", "Restore the files missing from the Drive appdata using this computer's state.db"},
		"drive init --help": {"The way out after pl pull stops with exit 3", "currently the only mode", "exits 2 if there is anything to create", "skip the confirmation"},
	} {
		out, err := runCLI(t, strings.Fields(args)...)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range wants {
			if !strings.Contains(out, w) {
				t.Errorf("capy %s 缺 %q:\n%s", args, w, out)
			}
		}
	}
}

// TestEnglishDriveInitConfirmWarnsWrongAccount:drive init 在終端機裡問的那一句是這個命令唯一的「登錯帳號」防線。
// 走真的命令路徑(假裝有 TTY、攔下 confirmWrite 並拒絕),英文的單複數與警告都要在,拒絕後 Drive 一個檔都不寫。
func TestEnglishDriveInitConfirmWarnsWrongAccount(t *testing.T) {
	withLanguage(t, "en")
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "commute", "a", "b")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	mustPull(t, "pl", "pull", "commute", "--yes")
	wipeDrive(t, dc)
	origTTY, origConfirm := bothTTY, confirmWrite
	t.Cleanup(func() { bothTTY, confirmWrite = origTTY, origConfirm })
	bothTTY = func(*cobra.Command) bool { return true }
	var asked string
	confirmWrite = func(p string) (bool, error) { asked = p; return false, nil }
	const warn = " to the Drive appdata of tai@example.com? (If this is the wrong account, your playlist data (playlists and track mappings) ends up in someone else's space)"

	_, _, err := runPull(t, "drive", "init", "--from-local")
	var pend *PendingError
	if !errors.As(err, &pend) || asked != "Upload the 4 files above"+warn {
		t.Fatalf("複數:%v\n%q", err, asked)
	}
	if n := len(driveFiles(t, dc)); n != 0 {
		t.Fatalf("拒絕之後 Drive 不該有檔,得 %d 個", n)
	}
	mustPull(t, "drive", "init", "--from-local", "--yes")
	deleteDriveFile(t, dc, "pl__")
	if _, _, err := runPull(t, "drive", "init", "--from-local"); !errors.As(err, &pend) || asked != "Upload the file above"+warn {
		t.Fatalf("單數:%v\n%q", err, asked)
	}
}
