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

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	setTestDir(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if *c != (Config{}) {
		t.Errorf("檔案不存在應回零值 Config,得到 %+v", c)
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

func TestAppleFieldsRoundtrip(t *testing.T) {
	setTestDir(t)
	if err := Save(&Config{AppleStorefront: "tw"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil || got.AppleStorefront != "tw" {
		t.Fatalf("(%+v, %v)", got, err)
	}
}

// TestLoadIgnoresLegacyFields:舊使用者升級路徑——config.json 裡殘留已移除的
// install_id / apple_token_endpoint 欄位,Load 不該報錯;Save 回去後這兩個 key 不該再出現。
func TestLoadIgnoresLegacyFields(t *testing.T) {
	dir := setTestDir(t)
	sub := filepath.Join(dir, "capy-music")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"install_id":"0123456789abcdef0123456789abcdef","apple_token_endpoint":"https://old.example.com","spotify_client_id":"abc123"}`
	if err := os.WriteFile(filepath.Join(sub, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.SpotifyClientID != "abc123" {
		t.Errorf("仍應讀到現有欄位:%+v", c)
	}
	if err := Save(c); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(sub, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "install_id") || strings.Contains(string(b), "apple_token_endpoint") {
		t.Errorf("舊欄位不應在 Save 後留存:%s", b)
	}
}
