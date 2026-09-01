package config

import (
	"os"
	"path/filepath"
	"testing"
)

func setTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userConfigDir = orig })
	return dir
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	setTestDir(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AppleTokenEndpoint != DefaultAppleTokenEndpoint {
		t.Errorf("預設 endpoint 錯誤:%q", c.AppleTokenEndpoint)
	}
}

func TestSaveThenLoadRoundtrip(t *testing.T) {
	dir := setTestDir(t)
	if err := Save(&Config{SpotifyClientID: "abc123"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.SpotifyClientID != "abc123" {
		t.Errorf("SpotifyClientID = %q, want abc123", got.SpotifyClientID)
	}
	if _, err := os.Stat(filepath.Join(dir, "capy-music", "config.json")); err != nil {
		t.Errorf("設定檔不在預期路徑:%v", err)
	}
}

func TestLoadCorruptFileErrors(t *testing.T) {
	dir := setTestDir(t)
	sub := filepath.Join(dir, "capy-music")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "config.json"), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("壞檔應回錯誤")
	}
}
