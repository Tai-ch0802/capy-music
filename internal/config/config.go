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

const dirName = "capy-music"

type Config struct {
	SpotifyClientID string `json:"spotify_client_id,omitempty"`
	AppleStorefront string `json:"apple_storefront,omitempty"`
	DefaultProvider string `json:"default_provider,omitempty"` // spotify|apple;空 = spotify
	GoogleClientID  string `json:"google_client_id,omitempty"` // BYO 才有;內建 client 不落地(決策 9)。secret 只在 keychain
	GoogleEmail     string `json:"google_email,omitempty"`     // 目前登入的 Google 帳號(非機密;§5「登錯帳號」的唯一偵測手段,Q8 採 A)
	DeviceID        string `json:"device_id,omitempty"`        // ULID,首次需要時產生;CAPY_CONFIG_DIR 換目錄 = 新裝置(不可沿用已刪的 install_id)
	LocalRoot       string `json:"local_root,omitempty"`       // P6 決策 35:本機曲庫目錄(*.m3u8 + library.json);非機密
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

// Load 讀取設定;檔案不存在時回傳零值設定(不落地)。
func Load() (*Config, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("解析 %s:%w", p, err)
	}
	return &c, nil
}

// Save 原子寫入。
func Save(c *Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
