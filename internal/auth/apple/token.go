// Package apple:Apple Music 憑證的 keychain 存取與 web token 解析(spec §4.3)。
// 兩個 token 都是使用者自己從 Apple 網頁播放器複製來的(非官方);這裡只儲存與驗證,
// 絕不自動擷取(CLAUDE.md 鐵則;唯一例外是 auto_darwin.go 的隱藏 --auto)。
package apple

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/secret"
)

const (
	KeyDeveloperToken = "apple.developer_token" // JSON {"token","exp"}
	KeyMusicUserToken = "apple.music_user_token"
)

var ErrDevTokenExpired = errors.New("developer token 已過期")

type storedDevToken struct {
	Token string `json:"token"`
	Exp   int64  `json:"exp"`
}

// NormalizeDevToken:去空白、去 "Bearer "(不分大小寫)——從 DevTools 複製 authorization 標頭常會連前綴一起帶。
func NormalizeDevToken(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 7 && strings.EqualFold(s[:7], "bearer ") {
		s = strings.TrimSpace(s[7:])
	}
	return s
}

// JWTExp 解析 JWT payload 的 exp;不驗簽(token 是 Apple 簽的,我們沒有也不需要公鑰)。
func JWTExp(tok string) (time.Time, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("不是 JWT(應為 eyJ 開頭、以 . 分成三段)")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, fmt.Errorf("JWT payload 不是 base64url:%w", err)
	}
	var p struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return time.Time{}, fmt.Errorf("JWT payload 不是 JSON:%w", err)
	}
	f, err := p.Exp.Float64()
	if err != nil || f <= 0 {
		return time.Time{}, errors.New("JWT 沒有 exp")
	}
	return time.Unix(int64(f), 0), nil
}

func SaveDeveloperToken(tok string, exp time.Time) error {
	raw, _ := json.Marshal(storedDevToken{Token: tok, Exp: exp.Unix()})
	if err := secret.Set(KeyDeveloperToken, string(raw)); err != nil {
		return fmt.Errorf("寫入 keychain 失敗:%w", err)
	}
	return nil
}

// DeveloperToken 回傳 keychain 裡的 developer token 與到期時間。
// 不存在(或紀錄壞掉)→ secret.ErrNotFound;已過期 → ErrDevTokenExpired(exp 仍回傳,供訊息顯示)。
func DeveloperToken(now time.Time) (string, time.Time, error) {
	raw, err := secret.Get(KeyDeveloperToken)
	if err != nil {
		return "", time.Time{}, err
	}
	var c storedDevToken
	if json.Unmarshal([]byte(raw), &c) != nil || c.Token == "" {
		return "", time.Time{}, secret.ErrNotFound
	}
	exp := time.Unix(c.Exp, 0)
	if !exp.After(now) {
		return "", exp, ErrDevTokenExpired
	}
	return c.Token, exp, nil
}
