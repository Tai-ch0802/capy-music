package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CAPY_CONFIG_DIR", filepath.Join(dir, "capy-music"))
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

func TestSaveDropsDefaultEndpoint(t *testing.T) {
	dir := setTestDir(t)
	c, err := Load() // 空設定,withDefaults 會填入預設 endpoint
	if err != nil {
		t.Fatal(err)
	}
	c.SpotifyClientID = "abc123"
	if err := Save(c); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "capy-music", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "apple_token_endpoint") {
		t.Errorf("預設 endpoint 不應落地,檔案內容:%s", b)
	}
	// Save 不應污染呼叫端手上的物件
	if c.AppleTokenEndpoint != DefaultAppleTokenEndpoint {
		t.Errorf("Save 改動了呼叫端的 Config:%q", c.AppleTokenEndpoint)
	}
}

func TestDirHonorsEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAPY_CONFIG_DIR", dir)
	got, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("Dir() = %q, want %q", got, dir)
	}
}

func TestSaveKeepsCustomEndpoint(t *testing.T) {
	setTestDir(t)
	if err := Save(&Config{AppleTokenEndpoint: "https://my-worker.example.com/token"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.AppleTokenEndpoint != "https://my-worker.example.com/token" {
		t.Errorf("自訂 endpoint 應存活:%q", got.AppleTokenEndpoint)
	}
}

func TestEnsureInstallID(t *testing.T) {
	c := &Config{}
	if !EnsureInstallID(c) || len(c.InstallID) != 32 {
		t.Fatalf("空 InstallID 應產生 32 字元 hex:%q", c.InstallID)
	}
	for _, ch := range c.InstallID {
		if !strings.ContainsRune("0123456789abcdef", ch) {
			t.Fatalf("非 hex:%q", c.InstallID)
		}
	}
	prev := c.InstallID
	if EnsureInstallID(c) || c.InstallID != prev {
		t.Error("已有 InstallID 不應改動")
	}
}

func TestAppleFieldsRoundtrip(t *testing.T) {
	setTestDir(t)
	if err := Save(&Config{InstallID: "0123456789abcdef0123456789abcdef", AppleStorefront: "tw"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil || got.InstallID != "0123456789abcdef0123456789abcdef" || got.AppleStorefront != "tw" {
		t.Fatalf("(%+v, %v)", got, err)
	}
}
