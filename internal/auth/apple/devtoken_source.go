package apple

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// KeyDeveloperToken、KeyMusicUserToken 兩個 key 常數已搬去 token.go(同 package apple 擁有)。
const refreshMargin = time.Hour

type DevTokenOptions struct {
	P8Path, KID, TeamID string // BYO(P8Path 空 = 不用);KID 空則從檔名推
	Endpoint            string // Worker(空 = 不用)
	InstallID           string
	HTTP                *http.Client
	Now                 func() time.Time
}

type cachedToken struct {
	Token string `json:"token"`
	Exp   int64  `json:"exp"`
}

// LegacyDeveloperToken 回傳可用的 developer token 與來源("cache"|"byo"|"worker")。
// 順序:keychain 快取(剩餘 > 1h)→ BYO .p8 本地簽 → Worker。BYO 與 Worker 同等地位(CLAUDE.md)。
// ponytail: 原名 DeveloperToken;改名純粹讓新 token.go 的 DeveloperToken(now) 不撞名(見 progress.md Ruling 2)。
// 本檔(含這個函式)整份在 Task 2 刪除,改名不留殘跡。
func LegacyDeveloperToken(ctx context.Context, o DevTokenOptions) (string, string, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	if raw, err := secret.Get(KeyDeveloperToken); err == nil {
		var c cachedToken
		if json.Unmarshal([]byte(raw), &c) == nil && c.Token != "" && time.Unix(c.Exp, 0).After(now().Add(refreshMargin)) {
			return c.Token, "cache", nil
		}
	}

	var tok string
	var exp int64
	var src string
	switch {
	case o.P8Path != "":
		kid := o.KID
		if kid == "" {
			kid = KIDFromPath(o.P8Path)
		}
		if kid == "" || o.TeamID == "" {
			return "", "", errors.New("BYO .p8 需要 Key ID(檔名 AuthKey_<KID>.p8 或 CAPY_APPLE_KID)與 CAPY_APPLE_TEAM_ID")
		}
		key, err := LoadP8(o.P8Path)
		if err != nil {
			return "", "", err
		}
		t := now()
		tok, err = SignDeveloperToken(key, kid, o.TeamID, t, DefaultTTL)
		if err != nil {
			return "", "", err
		}
		exp, src = t.Add(DefaultTTL).Unix(), "byo"
	case o.Endpoint != "":
		hc := o.HTTP
		if hc == nil {
			hc = &http.Client{Timeout: 30 * time.Second}
		}
		body, _ := json.Marshal(map[string]string{"install_id": o.InstallID})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Endpoint, bytes.NewReader(body))
		if err != nil {
			return "", "", err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			return "", "", fmt.Errorf("向 Worker 取 developer token 失敗:%w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", "", fmt.Errorf("Worker 回 HTTP %d(endpoint:%s)— 可改用 CAPY_APPLE_P8_PATH 自簽", resp.StatusCode, o.Endpoint)
		}
		var out struct {
			Token     string `json:"token"`
			ExpiresAt int64  `json:"expires_at"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
			return "", "", fmt.Errorf("Worker 回應無法解析:%v", err)
		}
		tok, exp, src = out.Token, out.ExpiresAt, "worker"
	default:
		return "", "", errors.New("沒有 developer token 來源 — 設 CAPY_APPLE_P8_PATH(自備 .p8)或在 config 設 apple_token_endpoint")
	}

	raw, _ := json.Marshal(cachedToken{Token: tok, Exp: exp})
	if err := secret.Set(KeyDeveloperToken, string(raw)); err != nil {
		return "", "", fmt.Errorf("寫入 keychain 失敗:%w", err)
	}
	return tok, src, nil
}
