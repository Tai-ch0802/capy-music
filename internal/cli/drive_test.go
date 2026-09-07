package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/drive/drivetest"
)

func stubDriveClient(t *testing.T) (*drive.Client, *drivetest.Server) {
	t.Helper()
	srv := drivetest.New(t)
	c := drive.New(srv.Client(), srv.URL)
	orig := newDriveClient
	newDriveClient = func(context.Context) (*drive.Client, error) { return c, nil }
	t.Cleanup(func() { newDriveClient = orig })
	return c, srv
}

func TestDebugDriveLs(t *testing.T) {
	c, _ := stubDriveClient(t)
	ctx := context.Background()
	if _, err := c.Create(ctx, "manifest.json", map[string]string{"kind": "manifest"}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(ctx, "pl__abc.json", map[string]string{"kind": "pl", "pid": "abc"}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "debug", "drive-ls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "manifest.json\tmanifest\t") || !strings.Contains(out, "pl__abc.json\tpl\tabc\t") {
		t.Fatalf("非 TTY 應印 TSV 含 name/kind/pid:\n%s", out)
	}
	out, err = runCLI(t, "debug", "drive-ls", "--q", "appProperties has { key='kind' and value='pl' }")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "manifest.json") || !strings.Contains(out, "pl__abc.json") {
		t.Fatalf("--q 應原樣交給 files.list 過濾:\n%s", out)
	}
}

func TestNewDriveClientNotLoggedIn(t *testing.T) {
	keyring.MockInit()
	t.Setenv("CAPY_CONFIG_DIR", t.TempDir())
	ctx := context.Background()
	if _, err := newDriveClient(ctx); !errors.Is(err, errNotLoggedInGoogle) {
		t.Fatalf("沒有 client 也沒 token 應提示登入:%v", err)
	}
	if err := config.Save(&config.Config{GoogleClientID: "cid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := newDriveClient(ctx); !errors.Is(err, errNotLoggedInGoogle) {
		t.Fatalf("有 client id 但 keychain 沒 token 應提示登入:%v", err)
	}
	if _, err := runCLI(t, "debug", "drive-ls"); err == nil || !strings.Contains(err.Error(), "auth login google") {
		t.Fatalf("drive-ls 未登入應回同一提示:%v", err)
	}
}
