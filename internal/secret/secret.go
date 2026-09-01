// Package secret 是 OS keychain 的薄包裝:固定 service 名 + 正規化 not-found 錯誤。
// 憑證只准存這裡(CLAUDE.md 硬約束)。
// 後端:macOS Keychain / Windows Credential Manager。Linux 非目標。
package secret

import (
	"errors"

	"github.com/zalando/go-keyring"
)

const service = "capy-music"

var ErrNotFound = errors.New("secret not found")

func Set(key, value string) error {
	return keyring.Set(service, key, value)
}

func Get(key string) (string, error) {
	v, err := keyring.Get(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func Delete(key string) error {
	err := keyring.Delete(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return err
}
