// Package config 管理非機密設定。機密一律走 internal/secret(keychain)。
package config

import (
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

// 測試替換點。macOS → ~/Library/Application Support;Windows → %AppData%(與 spec §7 一致)。
var userConfigDir = os.UserConfigDir

type Config struct {
	SpotifyClientID    string `json:"spotify_client_id,omitempty"`
	AppleTokenEndpoint string `json:"apple_token_endpoint,omitempty"`
}

// Dir 回傳設定目錄(不建立)。
func Dir() (string, error) {
	base, err := userConfigDir()
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
