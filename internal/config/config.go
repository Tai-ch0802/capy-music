// Package config 管理非機密設定。機密一律走 internal/secret(keychain)。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultAppleTokenEndpoint 是 Worker 的 token 派發端點(定案:附錄 C #7)。
// 可被設定檔覆寫(自架 Worker / BYO .p8 場景)。
const DefaultAppleTokenEndpoint = "https://capy.taislife.work/v1/apple/developer-token"

const dirName = "capy-music"

type Config struct {
	SpotifyClientID    string `json:"spotify_client_id,omitempty"`
	AppleTokenEndpoint string `json:"apple_token_endpoint,omitempty"`
	InstallID          string `json:"install_id,omitempty"`
	AppleStorefront    string `json:"apple_storefront,omitempty"`
}

// EnsureInstallID:CLI 首次啟動產生的 uuid(spec §4.3),供 Worker 做 per-install rate limit。
// 回傳是否有變動(呼叫端決定要不要 Save)。
func EnsureInstallID(c *Config) bool {
	if c.InstallID != "" {
		return false
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand 不可用:" + err.Error()) // 系統級故障,不是可恢復錯誤
	}
	c.InstallID = hex.EncodeToString(b)
	return true
}

// Dir 回傳設定目錄(不建立)。CAPY_CONFIG_DIR 可整個覆寫(測試與可攜設定用)。
// macOS → ~/Library/Application Support;Windows → %AppData%(與 spec §7 一致)。
func Dir() (string, error) {
	if d := os.Getenv("CAPY_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, dirName), nil
}

func configPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load 讀取設定;檔案不存在時回傳含預設值的空設定(不落地)。
func Load() (*Config, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return withDefaults(&Config{}), nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("解析 %s:%w", p, err)
	}
	return withDefaults(&c), nil
}

func withDefaults(c *Config) *Config {
	if c.AppleTokenEndpoint == "" {
		c.AppleTokenEndpoint = DefaultAppleTokenEndpoint
	}
	return c
}

// Save 原子寫入。等於預設值的欄位先正規化為空(omitempty 不落地),
// 避免把「當下的預設」釘進使用者檔案(P0 review 便條:P1 wizard 的 Load→Save 路徑)。
func Save(c *Config) error {
	norm := *c
	if norm.AppleTokenEndpoint == DefaultAppleTokenEndpoint {
		norm.AppleTokenEndpoint = ""
	}
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(&norm, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
