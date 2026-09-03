# Apple web token BYO 實作計畫(2026-09-03)

> **給執行者:** 每個 task 都是「先寫會 fail 的測試 → 實作 → 綠 → commit」。gofmt、`go vet ./...`、`go test -race ./...` 全綠才 commit;CI 是 macOS + Windows 矩陣,build tag 檔案兩邊都要能編。

**Goal:** 依 spec v0.5 §4.3 與 CLAUDE.md 新鐵則,把 Apple 認證從「`.p8` 簽章 + Cloudflare Worker 派發 + MusicKit JS 橋接」換成「使用者自己從 Apple 網頁播放器複製兩個 token,程式只指導與驗證」;官方路徑全刪(快照 `3649b7b`);另加一個隱藏的 `--auto`(唯一例外,opt-in、macOS best-effort)。

**Architecture:**
- `internal/auth/apple`:只剩 keychain 存取(`apple.developer_token` JSON `{token,exp}`、`apple.music_user_token`)、JWT `exp` 解析(不驗簽)、`--auto` 的 AppleScript 擷取(darwin 限定)。
- `internal/provider/apple/client.go`:base 改 `https://amp-api.music.apple.com/v1`、每個請求帶 `Origin: https://music.apple.com`、MUT 用 `Media-User-Token` 標頭 —— web token 只在這組條件下有效(見「已查證事實」)。
- `internal/cli`:`auth login apple` 三條入口(flag/env、TTY 精靈、隱藏 `--auto`)全收口到 `applePersist`(三段驗證全過才落地);`status`/`doctor`/`newAppleProvider` 改讀 keychain 並分辨「不存在」與「已過期」。
- config 只剩 `spotify_client_id`、`apple_storefront`。

**Tech Stack:** Go 1.27、cobra、`charm.land/huh/v2`、`zalando/go-keyring`(測試 `MockInit`)、stdlib `encoding/base64` / `encoding/json`、`osascript`(darwin)。

**分支:** `feat/apple-webtoken`(自 `origin/main` `3649b7b`;docs commit `71e4080` 已在上面)。PR 開到 `main`。

## 已查證事實(2026-09-03,以 gamdl `gamdl/api/apple_music.py` 為據;真實 token 驗收前仍是假設 → 附錄 A C-0)

| 項目 | 事實 | 來源 |
|---|---|---|
| base URL | `https://amp-api.music.apple.com` + `/v1/...`,路徑形狀與官方 API 相同(`/v1/catalog/{sf}/search`、`/v1/me/library/...`) | gamdl constants |
| 必帶標頭 | `authorization: Bearer <dev>`、`origin: https://music.apple.com`;gamdl 不改 User-Agent | gamdl `create()` |
| MUT 傳法 | gamdl 用 cookie `media-user-token=<MUT>`;網頁播放器本身則以 `media-user-token` 請求標頭送出 → 本計畫用標頭(README 就是教使用者從 Request Headers 複製它);C-0 驗證,失敗才改 cookie(一行) | gamdl + spec §4.3(a) |
| storefront | gamdl 用 `GET /v1/me/account?meta=subscription` → `meta.subscription.storefront`;本計畫維持既有 `GET /v1/me/storefront`(官方形狀、已有測試),C-0 若 404 才換 | gamdl `get_account_info` |
| developer token 出處 | 網頁播放器前端 bundle 寫死:首頁 HTML 找 `/(assets/index[~-][^/"]+\.js)`,bundle 內找 `"(eyJ…\.eyJ…\.…)"`;`MusicKit.getInstance().developerToken` / `.musicUserToken` 在已登入分頁的 console 也拿得到 | gamdl `get_token()`、Apple 論壇 |

## Task 1:token 層 + amp-api 標頭(純新增/微調,不刪東西)

**Files:**
- Create: `internal/auth/apple/token.go`、`internal/auth/apple/token_test.go`
- Modify: `internal/provider/apple/client.go`(`DefaultAPIBase`、`do()` 標頭)、`internal/provider/apple/client_test.go`、所有斷言 `Music-User-Token` 的測試(`grep -rn "Music-User-Token" internal`:`internal/cli/auth_test.go`、`internal/provider/apple/*_test.go`)

- [ ] **Step 1: 寫 token_test.go(會 fail:package 內尚無這些符號)**

```go
package apple

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// fakeJWT:只有 exp 的假 JWT——本專案不驗簽,夠用。
func fakeJWT(t *testing.T, payload string) string {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"ES256","kid":"WebPlayKid"}`)) + "." + enc([]byte(payload)) + ".c2ln"
}

func TestJWTExp(t *testing.T) {
	exp := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)
	got, err := JWTExp(fakeJWT(t, `{"iss":"AMPWebPlay","exp":`+strconv.FormatInt(exp.Unix(), 10)+`}`))
	if err != nil || !got.Equal(exp) {
		t.Fatalf("exp 應解析:(%v, %v)", got, err)
	}
	if _, err := JWTExp(fakeJWT(t, `{"exp":1.7e9}`)); err != nil { // 浮點 exp 也收
		t.Errorf("float exp:%v", err)
	}
	for name, tok := range map[string]string{
		"沒有 exp":  fakeJWT(t, `{"iss":"x"}`),
		"兩段":     "eyJ.eyJ",
		"payload 非 JSON": "eyJ." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".sig",
		"空字串":    "",
	} {
		if _, err := JWTExp(tok); err == nil {
			t.Errorf("%s 應回錯", name)
		}
	}
}

func TestNormalizeDevToken(t *testing.T) {
	for in, want := range map[string]string{
		"  Bearer eyJabc \n": "eyJabc",
		"bearer eyJabc":      "eyJabc",
		"eyJabc":             "eyJabc",
		"Bearer":             "Bearer", // 不是前綴+token 的形狀,原樣留給 JWTExp 去擋
	} {
		if got := NormalizeDevToken(in); got != want {
			t.Errorf("%q → %q,want %q", in, got, want)
		}
	}
}

func TestDeveloperTokenRoundtripAndExpiry(t *testing.T) {
	keyring.MockInit()
	_ = secret.Delete(KeyDeveloperToken)
	now := time.Now()
	if _, _, err := DeveloperToken(now); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("空 keychain 應回 ErrNotFound:%v", err)
	}
	if err := SaveDeveloperToken("eyJtok", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	tok, exp, err := DeveloperToken(now)
	if err != nil || tok != "eyJtok" || !exp.Equal(now.Add(time.Hour).Truncate(time.Second)) {
		t.Fatalf("有效 token:(%q, %v, %v)", tok, exp, err)
	}
	tok, exp, err = DeveloperToken(now.Add(2 * time.Hour))
	if !errors.Is(err, ErrDevTokenExpired) || tok != "" || exp.IsZero() {
		t.Fatalf("過期應回 ErrDevTokenExpired 並附 exp:(%q, %v, %v)", tok, exp, err)
	}
	// 壞掉的紀錄視同不存在(不要讓一筆壞 JSON 永遠卡住使用者)。
	_ = secret.Set(KeyDeveloperToken, "not json")
	if _, _, err := DeveloperToken(now); !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("壞 JSON 應視同不存在:%v", err)
	}
}
```

(import 需要 `strconv`;`encoding/json` 若沒用到就拿掉。)

- [ ] **Step 2: 跑 `go test ./internal/auth/apple/` 確認 fail(undefined: JWTExp …)**

- [ ] **Step 3: 寫 token.go**

```go
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
```

`secret.Get` 找不到時回的 error 要能 `errors.Is(err, secret.ErrNotFound)`(既有 cli 已這樣用);若 `secret` 包不是這樣,先修 `secret`。

- [ ] **Step 4: client.go —— base + 標頭(一個標頭區塊,附錄 A C-0 決定去留)**

```go
// DefaultAPIBase:網頁播放器的私有 API。使用者複製來的 web developer token 只在這裡有效
// (官方 api.music.apple.com 對它的行為未驗證;附錄 A C-0)。CAPY_APPLE_API_BASE 可覆寫。
const DefaultAPIBase = "https://amp-api.music.apple.com/v1"

const webOrigin = "https://music.apple.com"
```

`do()` 內把原本兩行標頭改成:

```go
		req.Header.Set("Authorization", "Bearer "+c.dev)
		// ponytail: web token 綁 origin,缺這行 amp-api 會拒(gamdl 同);MUT 用網頁播放器的標頭名。
		// 這兩行的去留由附錄 A C-0 用真 token 決定。
		req.Header.Set("Origin", webOrigin)
		if c.userTok != "" {
			req.Header.Set("Media-User-Token", c.userTok)
		}
```

`do()` 上方的註解維持「401 = developer token 無效、403 = MUT 無效」。

- [ ] **Step 5: client_test.go 加 `TestClientSendsWebPlayerHeaders`**:httptest 斷言 `Origin == "https://music.apple.com"`、`Authorization == "Bearer DEV"`、`Media-User-Token == "MUT"`、`Music-User-Token == ""`;`NewClient(hc, "", …)` 的 base 為 `DefaultAPIBase`(`strings.Contains(DefaultAPIBase, "amp-api")`)。把所有測試裡的 `Music-User-Token` 改成 `Media-User-Token`(`grep -rn "Music-User-Token" internal` 直到為零)。

- [ ] **Step 6: `gofmt -l . && go vet ./... && go test -race ./...` 全綠 → commit**

```
feat(apple): keychain-only token 層(JWT exp 解析、不驗簽)+ client 改 amp-api base 與 Origin/Media-User-Token 標頭
```

## Task 2:拆除官方路徑 + 非互動登入核心(刪除為主)

**Files:**
- Delete: `worker/`(整個目錄;未追蹤的 `worker/node_modules` 也 `rm -rf`)、`internal/auth/apple/{devtoken.go,devtoken_test.go,devtoken_source.go,devtoken_source_test.go,authorize.go,authorize_test.go}`、`internal/auth/apple/web/`(整個目錄)
- Modify: `.github/workflows/ci.yml`(刪 `worker` job)、`.gitignore`(刪 `*.p8`)、`internal/config/config.go` + `config_test.go`、`internal/cli/provider.go`、`internal/cli/auth.go` + `auth_test.go`、`internal/cli/debug.go` + `debug_test.go`、`internal/cli/doctor.go` + `doctor_test.go`、`scripts/p0/p0-1-isrc.sh`、`scripts/p0/p0-2-playlist-ops.sh`
- 跨檔測試 helper 陷阱:`auth_test.go` 的 `setupAppleBYO` 呼叫 `debug_test.go` 的 `writeTestP8`,`doctor_test.go` 又呼叫 `setupAppleBYO`——三個檔一起換成下面的 `setupAppleTokens`。

- [ ] **Step 1: 先寫 auth_test.go 的新測試(會 fail:flag 不存在、行為不同)。** 共用 helper:

```go
// fakeJWT:只有 exp 的假 developer token(不驗簽)。
func fakeJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"ES256"}`)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + ".sig"
}

// setupAppleTokens:乾淨 config + keychain,預放一顆 24h 有效的 dev token 與 user token "MUT0";回傳 dev token。
func setupAppleTokens(t *testing.T) string {
	t.Helper()
	setCLITestConfig(t)
	exp := time.Now().Add(24 * time.Hour)
	dev := fakeJWT(t, exp)
	if err := apple.SaveDeveloperToken(dev, exp); err != nil {
		t.Fatal(err)
	}
	if err := secret.Set(apple.KeyMusicUserToken, "MUT0"); err != nil {
		t.Fatal(err)
	}
	return dev
}

// clearAppleTokens:清 keychain(首次登入情境)。
func clearAppleTokens(t *testing.T) {
	t.Helper()
	setCLITestConfig(t)
	_ = secret.Delete(apple.KeyDeveloperToken)
	_ = secret.Delete(apple.KeyMusicUserToken)
}

// appleServer:假 amp-api。preflight 只認 dev;storefront 要 Media-User-Token == wantMUT 才回 tw,否則 403。
// 回傳 hits 供「不該打網路」的斷言。
func appleServer(t *testing.T, wantDev, wantMUT string) *int32 {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("Origin") != "https://music.apple.com" {
			t.Errorf("缺 Origin:%v", r.Header)
		}
		if r.Header.Get("Authorization") != "Bearer "+wantDev {
			w.WriteHeader(401)
			w.Write([]byte(`{"errors":[{"status":"401"}]}`))
			return
		}
		switch r.URL.Path {
		case "/storefronts/us":
			w.Write([]byte(`{"data":[{"id":"us"}]}`))
		case "/me/storefront":
			if r.Header.Get("Media-User-Token") != wantMUT {
				w.WriteHeader(403)
				w.Write([]byte(`{"errors":[{"status":"403"}]}`))
				return
			}
			w.Write([]byte(`{"data":[{"id":"tw"}]}`))
		default:
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CAPY_APPLE_API_BASE", srv.URL)
	return &hits
}
```

測試(每個都要:成功時 keychain/config 內容正確;失敗時 **dev token、user token、storefront 三者都沒被寫**——用 `secret.Get` 回 ErrNotFound / `config.Load().AppleStorefront == ""` 斷言):

| 測試 | 情境 | 期望 |
|---|---|---|
| `TestAuthLoginAppleEnvPersists` | `clearAppleTokens`;env `CAPY_APPLE_DEVELOPER_TOKEN`(帶 `"Bearer "` 前綴)+ `CAPY_APPLE_USER_TOKEN=MUT1`;`--i-understand`;stdin 非 TTY | 輸出含「登入完成」與 `tw`;keychain dev JSON 的 exp == JWT exp、token 不含 Bearer;user == MUT1;config storefront tw |
| `TestAuthLoginAppleWithoutIUnderstandRefused` | 同上但沒 `--i-understand` | error 含 `--i-understand` **與**「非 Apple 官方支援」(揭露在指令內);server hits == 0;不落地 |
| `TestAuthLoginAppleNonTTYMissingEnv` | 沒 env、沒 flag、非 TTY | error 含 `CAPY_APPLE_DEVELOPER_TOKEN`;hits == 0 |
| `TestAuthLoginAppleExpiredJWTRejectedBeforeNetwork` | dev 的 exp 在過去 | error 含「已於」「過期」;hits == 0 |
| `TestAuthLoginAppleMalformedDevToken` | dev = "not-a-jwt" | error 含「developer token」;hits == 0 |
| `TestAuthLoginApplePreflight401` | server 的 wantDev 與送進來的不同 | error 含「developer token」與 `401`;不落地 |
| `TestAuthLoginAppleStorefront403DoesNotPersistDevToken` | preflight 過、MUT 錯 | error 含「user token」與 `403`;**dev token 也沒寫** |
| `TestAuthLoginAppleOnlyDevTokenKeepsUserToken` | `setupAppleTokens`(user = MUT0);只給 dev env | 用 MUT0 打 storefront;dev 更新為新 JWT;user 仍 MUT0;輸出含「登入完成」 |
| `TestAuthLoginAppleOnlyDevTokenWithDeadStoredUser` | 同上但 server wantMUT = "OTHER" | error 含「既有 user token」;dev 不更新(仍是 setup 那顆) |
| `TestAuthLoginAppleOnlyDevTokenWithoutStoredUserErrors` | `clearAppleTokens`;只給 dev | error 含 `media-user-token`;hits == 0 |
| `TestAuthLoginAppleFlagsWork` | 用 `--developer-token/--user-token --i-understand` | 同 Env 測試 |
| `TestAuthStatusApple` | `setupAppleTokens` | 「developer token: 有效至 」+「user token: 存在」;把 keychain 換成過期紀錄 → 「已於 … 過期」;清掉 → 「不存在(執行 capy auth login apple)」 |
| `TestNewAppleProviderNeedsLogin`(改寫) | 清空 → 「尚未登入」;過期 → 訊息含「過期」與 `capy auth login apple` | |

- [ ] **Step 2: config_test.go**:刪 `TestSaveDropsDefaultEndpoint`、`TestSaveKeepsCustomEndpoint`、`TestEnsureInstallID`;`TestLoadMissingFileReturnsDefaults` → `TestLoadMissingFileReturnsEmpty`(零值 Config);`TestAppleFieldsRoundtrip` 只剩 storefront;新增 `TestLoadIgnoresLegacyFields`:寫一個含 `install_id`、`apple_token_endpoint` 的 config.json → Load 不報錯、Save 後檔案不再含這兩個 key(舊使用者升級路徑)。

- [ ] **Step 3: debug_test.go**:刪三個舊測試與 `writeTestP8`;新增 `TestDebugAppleTokenPrintsKeychain`(`setupAppleTokens` → `debug apple-token` 印 dev;`--user` 印 MUT0;清空 → error 含 `capy auth login apple`)。

- [ ] **Step 4: doctor_test.go**:`setupAppleBYO` → `setupAppleTokens` 後 `secret.Delete(KeyMusicUserToken)`;期望「✅ Apple developer token」+「有效至」、「❌ Apple user token」;新增 `TestDoctorAppleExpiredDevToken`(存過期紀錄 → ❌ 含「過期」)。

- [ ] **Step 5: 跑 `go test ./internal/...` 確認 fail;然後動手拆:**

1. `git rm -r worker internal/auth/apple/devtoken.go internal/auth/apple/devtoken_test.go internal/auth/apple/devtoken_source.go internal/auth/apple/devtoken_source_test.go internal/auth/apple/authorize.go internal/auth/apple/authorize_test.go internal/auth/apple/web` + `rm -rf worker`;ci.yml 刪整個 `worker:` job;`.gitignore` 刪 `*.p8`。
2. `config.go`:刪 `DefaultAppleTokenEndpoint`、`AppleTokenEndpoint`、`InstallID`、`EnsureInstallID`、`withDefaults`、Save 的正規化與 `crypto/rand`/`encoding/hex` import。`Load` 找不到檔案回 `&Config{}`。
3. `provider.go`:刪 `appleAuthorize`、`devTokenOptsFromEnv`;`newAppleProvider`:

```go
	dev, exp, err := apple.DeveloperToken(time.Now())
	switch {
	case errors.Is(err, secret.ErrNotFound):
		return nil, errors.New("尚未登入 Apple Music — 先執行 capy auth login apple")
	case errors.Is(err, apple.ErrDevTokenExpired):
		return nil, fmt.Errorf("Apple developer token 已於 %s 過期(Apple 定期輪替)— 重新執行 capy auth login apple", exp.Format(time.RFC3339))
	case err != nil:
		return nil, err
	}
```
其餘(MUT 缺 → 提示 login、storefront 缺 → 提示 login、`appleAPIBase()`)不動。

4. `auth.go`:`openBrowser` 從 debug.go 搬過來;login `Short` 改「登入平台(Spotify:自己的 app + PKCE;Apple:自抓 web token)」;加三個 flag;`appleLogin` + `applePersist`:

```go
const appleDisclosure = `⚠️ 非 Apple 官方支援。你要貼上的兩個 token 屬於 Apple 網頁播放器(music.apple.com):
  · Apple 可能隨時更換或撤銷 —— 屆時重新執行 capy auth login apple 即可
  · 以第三方工具存取 Apple Music 的服務條款風險由你自行承擔
  · capy 只指導你複製,不會讀取你的瀏覽器資料`

const appleGuide = `從 Apple 網頁播放器複製 token(約 1 分鐘):
  1. 用瀏覽器開 https://music.apple.com 並登入
  2. 開 DevTools(F12 / ⌥⌘I)→ Network 分頁,篩選 "amp-api"
  3. 隨便點一首歌或播放清單,點任一 amp-api 請求 → Request Headers
  4. 複製 authorization 的值(整串;含不含 "Bearer " 都可)→ developer token
  5. 複製 media-user-token 的值 → user token`

// appleLogin:三條入口(flag/env、TTY 精靈[Task 3]、隱藏 --auto[Task 4])全收口到 applePersist。
// 揭露不可跳過:flag/env 路徑要 --i-understand(拒絕訊息本身就帶聲明);精靈路徑是第一頁的 Confirm。
func appleLogin(cmd *cobra.Command) error {
	dev, _ := cmd.Flags().GetString("developer-token")
	if dev == "" {
		dev = os.Getenv("CAPY_APPLE_DEVELOPER_TOKEN")
	}
	user, _ := cmd.Flags().GetString("user-token")
	if user == "" {
		user = os.Getenv("CAPY_APPLE_USER_TOKEN")
	}
	if dev == "" {
		// Task 3 在這裡插入 TTY 精靈分支。
		return errors.New("請設 CAPY_APPLE_DEVELOPER_TOKEN(首次登入另需 CAPY_APPLE_USER_TOKEN)並加 --i-understand。\n" + appleGuide)
	}
	if ok, _ := cmd.Flags().GetBool("i-understand"); !ok {
		return errors.New(appleDisclosure + "\n\n以 flag / 環境變數提供 token 時,請加 --i-understand 表示已閱讀並同意上述聲明")
	}
	return applePersist(cmd.Context(), cmd.OutOrStdout(), dev, user)
}

// applePersist:三段驗證(JWT exp → preflight 401 → storefront 403)全過才寫 keychain/config——失敗不留半殘狀態。
// user 為空 = 只更新 developer token:用 keychain 既有 user token 跑第三段(順便驗它還活著)。
func applePersist(ctx context.Context, w io.Writer, dev, user string) error {
	dev = apple.NormalizeDevToken(dev)
	exp, err := apple.JWTExp(dev)
	if err != nil {
		return fmt.Errorf("developer token 格式不對(%v)— 應複製 authorization 標頭的值", err)
	}
	if !exp.After(time.Now()) {
		return fmt.Errorf("developer token 已於 %s 過期 — 回網頁播放器重新複製", exp.Format(time.RFC3339))
	}
	user = strings.TrimSpace(user)
	keepUser := user == ""
	if keepUser {
		user, err = secret.Get(apple.KeyMusicUserToken)
		if errors.Is(err, secret.ErrNotFound) {
			return errors.New("keychain 沒有 user token — 首次登入請一併提供 media-user-token")
		}
		if err != nil {
			return err
		}
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	if err := appleprov.NewClient(hc, appleAPIBase(), dev, "").Preflight(ctx); err != nil {
		return fmt.Errorf("developer token 被 Apple 拒絕 — 重新複製 authorization 標頭(Apple 可能已輪替):%w", err)
	}
	sf, err := appleprov.NewClient(hc, appleAPIBase(), dev, user).Storefront(ctx)
	if err != nil {
		if keepUser {
			return fmt.Errorf("既有 user token 已失效 — 請一併提供新的 media-user-token:%w", err)
		}
		return fmt.Errorf("user token 被 Apple 拒絕 — 重新複製 media-user-token:%w", err)
	}
	if err := apple.SaveDeveloperToken(dev, exp); err != nil {
		return err
	}
	if !keepUser {
		if err := secret.Set(apple.KeyMusicUserToken, user); err != nil {
			return fmt.Errorf("寫入 keychain 失敗:%w", err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.AppleStorefront = sf
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(w, "✅ Apple Music 登入完成(storefront %s;developer token 有效至 %s)\n", sf, exp.Format("2006-01-02"))
	return nil
}
```

flag:

```go
	cmd.Flags().String("developer-token", "", "(apple)developer token;等同 CAPY_APPLE_DEVELOPER_TOKEN。argv 可被 ps 看到,建議用環境變數")
	cmd.Flags().String("user-token", "", "(apple)media-user-token;等同 CAPY_APPLE_USER_TOKEN")
	cmd.Flags().Bool("i-understand", false, "(apple)以 flag/環境變數提供 token 時,表示已閱讀「非 Apple 官方支援」聲明")
```

`auth status` 的 apple 段:

```go
	switch _, exp, err := apple.DeveloperToken(time.Now()); {
	case err == nil:
		fmt.Fprintf(w, "  developer token: 有效至 %s\n", exp.Format(time.RFC3339))
	case errors.Is(err, apple.ErrDevTokenExpired):
		fmt.Fprintf(w, "  developer token: 已於 %s 過期(執行 capy auth login apple)\n", exp.Format(time.RFC3339))
	default:
		fmt.Fprintln(w, "  developer token: 不存在(執行 capy auth login apple)")
	}
```

5. `debug.go`:只剩 `apple-token [--user]`(印 keychain 裡的 dev / user token,給 scripts/p0 用);刪 `apple-auth`、`appleKeyFlags`、`resolveAppleKey`、`signLocalDevToken`。
6. `doctor.go` `checkAppleDevToken`:不存在 → 「keychain 沒有 developer token — 執行 capy auth login apple」;過期 → 「已於 %s 過期 — 重新執行 capy auth login apple」;OK → 「有效至 %s」。
7. `scripts/p0/*.sh`:註解改為「前置:`capy auth login apple`(見 README);DT=`capy debug apple-token`;MUT=`capy debug apple-token --user`」;base 改 `${CAPY_APPLE_API_BASE:-https://amp-api.music.apple.com/v1}`;curl 加 `-H "Origin: https://music.apple.com"`,MUT 標頭名改 `Media-User-Token`。CI 的 `bash -n` 要過。

- [ ] **Step 6: `go mod tidy`(刪掉的檔案可能是某些 module 的唯一使用者;CI 有 `go mod tidy -diff`)、`gofmt -l .`、`go vet ./...`、`go test -race ./...`;`grep -rn "InstallID\|AppleTokenEndpoint\|DevTokenOptions\|AuthorizeMUT\|LoadP8\|CAPY_APPLE_P8_PATH\|apple-auth" --include='*.go' --include='*.sh' --include='*.yml' .` 必須為零(docs/ 下的歷史計畫與附錄 D 除外)。commit:**

```
refactor(apple)!: 移除 .p8 簽章、Cloudflare Worker、MusicKit 橋接(快照 3649b7b);auth login apple 改為非互動 token 登入核心(三段驗證、--i-understand 揭露閘)
```

## Task 3:TTY 精靈(huh)

**Files:** Modify `internal/cli/auth.go`、`internal/cli/auth_test.go`

- [ ] **Step 1: 測試(fail:seam 不存在)。** 兩個測試替換點:`confirmAppleDisclosure`(回 error = 不同意)、`runAppleWizardInputs(hasUser bool) (dev, user string, err error)`。

| 測試 | 情境 | 期望 |
|---|---|---|
| `TestAuthLoginAppleTTYRunsWizard` | `clearAppleTokens`;`stdinIsTTY` stub true;confirm stub 記錄被呼叫;inputs stub 斷言 `hasUser == false`、回 (jwt, "MUT1") | 揭露被呼叫一次;落地與 Env 測試相同;不需 `--i-understand` |
| `TestAuthLoginAppleTTYWizardOnlyDev` | `setupAppleTokens`;inputs stub 斷言 `hasUser == true`、回 (jwt, "") | 用 MUT0 驗證;user 仍 MUT0 |
| `TestAuthLoginAppleTTYDisclosureDeclined` | confirm stub 回 error | inputs stub **不被呼叫**;hits == 0;不落地 |
| `TestAuthLoginAppleTTYWithFlagsSkipsWizard` | TTY;給 `--developer-token/--user-token`,沒 `--i-understand` | 精靈兩個 stub 都不被呼叫;error 含 `--i-understand` |

- [ ] **Step 2: 實作。** `appleLogin` 的 `dev == ""` 分支改成:

```go
	if dev == "" {
		if !stdinIsTTY() {
			return errors.New("非互動環境請設 CAPY_APPLE_DEVELOPER_TOKEN(首次登入另需 CAPY_APPLE_USER_TOKEN)並加 --i-understand。\n" + appleGuide)
		}
		if err := confirmAppleDisclosure(); err != nil {
			return err
		}
		_, err := secret.Get(apple.KeyMusicUserToken)
		dev, user, err = runAppleWizardInputs(err == nil)
		if err != nil {
			return err
		}
		return applePersist(cmd.Context(), cmd.OutOrStdout(), dev, user)
	}
```

```go
// 測試替換點(精靈本體需要 TTY;單元測試只測分流,同 Spotify runClientIDWizard 慣例)。
var (
	confirmAppleDisclosure = appleConfirmDisclosure
	runAppleWizardInputs   = appleWizardInputs
)

// appleConfirmDisclosure:揭露頁,Confirm 預設「取消」;不同意即 error。CLAUDE.md:不可跳過。
func appleConfirmDisclosure() error {
	agree := false
	if err := huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("使用前請先閱讀").Description(appleDisclosure),
		huh.NewConfirm().Title("我已閱讀,同意自負風險,繼續?").Affirmative("同意").Negative("取消").Value(&agree),
	)).Run(); err != nil {
		return err
	}
	if !agree {
		return errors.New("已取消(未同意聲明)")
	}
	return nil
}

// appleWizardInputs:已有 user token 時先問「只更新 developer token?」(預設是——Apple 輪替時的常態,R-6 唯一緩解)
// → 指引 → 貼 token。回傳 user 空字串 = 只更新 developer token。
func appleWizardInputs(hasUser bool) (dev, user string, err error) {
	onlyDev := hasUser
	if hasUser {
		if err := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title("keychain 已有 user token。只更新 developer token?").
				Affirmative("只更新 developer token").Negative("兩個都重新貼").Value(&onlyDev),
		)).Run(); err != nil {
			return "", "", err
		}
	}
	fields := []huh.Field{
		huh.NewNote().Title("從網頁播放器複製 token").Description(appleGuide),
		huh.NewInput().Title("developer token(authorization 標頭的值)").Value(&dev).Validate(func(s string) error {
			exp, err := apple.JWTExp(apple.NormalizeDevToken(s))
			if err != nil {
				return err
			}
			if !exp.After(time.Now()) {
				return fmt.Errorf("已於 %s 過期,請重新複製", exp.Format(time.RFC3339))
			}
			return nil
		}),
	}
	if !onlyDev {
		fields = append(fields, huh.NewInput().Title("user token(media-user-token 標頭的值)").
			EchoMode(huh.EchoModePassword).Value(&user).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("不可為空")
			}
			return nil
		}))
	}
	if err := huh.NewForm(huh.NewGroup(fields...)).Run(); err != nil {
		return "", "", err
	}
	return dev, user, nil
}
```

huh v2 API 若名稱不同(`Affirmative`/`Negative`/`EchoMode`/`huh.Field`),以 `go doc charm.land/huh/v2 Confirm` 為準調整,不要自己造輪子。

- [ ] **Step 3: 手動 smoke(可選,需 TTY):`go run ./cmd/capy auth login apple` 看三頁流程;Ctrl-C 要乾淨結束(huh 回 `huh.ErrUserAborted`)。**
- [ ] **Step 4: 全綠 → commit `feat(apple): auth login apple TTY 精靈(揭露 Confirm 預設否、只更新 developer token 路徑、貼上即驗證)`**

## Task 4:隱藏 `--auto`(macOS best-effort)+ spec §4.3(d) 對齊

**Files:**
- Create: `internal/auth/apple/auto.go`(共用型別/錯誤)、`auto_darwin.go` + `auto_darwin_test.go`(`//go:build darwin`)、`auto_other.go` + `auto_other_test.go`(`//go:build !darwin`)
- Modify: `internal/cli/auth.go` + `auth_test.go`、`docs/ARCHITECTURE.md` §4.3 (c)(d)

**機制(單一路徑,不讀 cookie 資料庫):** AppleScript 找 Safari / Google Chrome 裡第一個 `https://music.apple.com` 分頁,在頁面跑 `MusicKit.getInstance()` 一次拿到 `developerToken` 與 `musicUserToken`。前提:使用者已登入且該分頁開著;瀏覽器開啟「允許來自 Apple 事件的 JavaScript」(Safari:設定 → 進階 → 顯示「開發」選單 → 開發 → 允許來自 Apple 事件的 JavaScript;Chrome:View → Developer → Allow JavaScript from Apple Events);首次執行 macOS 會問「終端機想控制 Safari」。任一失敗 → 說明原因並回退手動貼上。

- [ ] **Step 1: 測試(fail)。** darwin:seam `runOSA func(script string) (string, error)`;(a) Safari 腳本回 `{"d":"eyJ…","u":"MUT"}` → `WebTokens{Developer, User}`;(b) Safari 回 error、Chrome 回 JSON → 成功且 error 訊息未出現;(c) 兩個都回空字串 → error 含「Apple 事件」與「music.apple.com」;(d) JSON 缺 `u` → error。other:`AutoWebTokens()` 回 `ErrAutoUnsupported`。
  cli(兩個 OS 都跑,靠 seam `appleAutoTokens`):

| 測試 | 情境 | 期望 |
|---|---|---|
| `TestAuthLoginAppleAutoPersists` | 非 TTY;`--auto --i-understand`;seam 回 tokens | 落地同 Env 測試 |
| `TestAuthLoginAppleAutoNeedsDisclosure` | 非 TTY;`--auto` 無 `--i-understand` | error 含 `--i-understand`;seam 不被呼叫 |
| `TestAuthLoginAppleAutoTTYConfirmsThenExtracts` | TTY;confirm stub 記錄;seam 回 tokens | confirm 一次;inputs stub 不被呼叫;落地 |
| `TestAuthLoginAppleAutoFallsBackToWizardInputs` | TTY;seam 回 error;inputs stub 回 tokens | stderr 含「自動擷取失敗」;inputs stub 被呼叫;落地 |
| `TestAuthLoginAppleAutoNonTTYFailureErrors` | 非 TTY;seam 回 error | error 含原因;不落地 |
| `TestAutoFlagHiddenFromHelp` | `auth login --help` | 輸出不含 `--auto`,含 `--i-understand` |

- [ ] **Step 2: 實作。**

`auto.go`:

```go
package apple

import "errors"

// WebTokens:從已登入的網頁播放器分頁讀到的兩個 token(隱藏 --auto 專用)。
type WebTokens struct{ Developer, User string }

var ErrAutoUnsupported = errors.New("--auto 目前只支援 macOS(Safari / Google Chrome)")
```

`auto_darwin.go`(要點;AppleScript 用 `application "X" is running` 先判斷,避免 `tell` 把沒開的瀏覽器啟動起來):

```go
//go:build darwin

package apple

// runOSA:測試替換點(與 provider/apple 的 StubOSAForTest 各自獨立,不跨包戳)。
var runOSA = func(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).Output()
	return strings.TrimSpace(string(out)), err
}

const musicKitJS = `JSON.stringify({d:MusicKit.getInstance().developerToken,u:MusicKit.getInstance().musicUserToken})`

var browserScripts = []struct{ name, script string }{
	{"Safari", `if application "Safari" is running then
	tell application "Safari"
		repeat with w in windows
			repeat with t in tabs of w
				if URL of t starts with "https://music.apple.com" then
					return do JavaScript "` + musicKitJS + `" in t
				end if
			end repeat
		end repeat
	end tell
end if
return ""`},
	{"Google Chrome", `if application "Google Chrome" is running then
	tell application "Google Chrome"
		repeat with w in windows
			repeat with t in tabs of w
				if URL of t starts with "https://music.apple.com" then
					return execute t javascript "` + musicKitJS + `"
				end if
			end repeat
		end repeat
	end tell
end if
return ""`},
}

// AutoWebTokens:依序試 Safari、Chrome;第一個成功的贏。全部失敗 → 一個 error 說明每個瀏覽器的原因與怎麼開啟權限。
func AutoWebTokens() (WebTokens, error) {
	var reasons []string
	for _, b := range browserScripts {
		out, err := runOSA(b.script)
		if err != nil {
			reasons = append(reasons, b.name+":"+err.Error())
			continue
		}
		if out == "" {
			reasons = append(reasons, b.name+":沒開或沒有 music.apple.com 分頁")
			continue
		}
		var v struct{ D, U string }
		if json.Unmarshal([]byte(out), &v) != nil || v.D == "" || v.U == "" {
			reasons = append(reasons, b.name+":頁面沒有回傳兩個 token(未登入?)")
			continue
		}
		return WebTokens{Developer: v.D, User: v.U}, nil
	}
	return WebTokens{}, fmt.Errorf("自動擷取失敗(%s)。前提:已登入的 music.apple.com 分頁開著,且瀏覽器允許來自 Apple 事件的 JavaScript(Safari:開發選單;Chrome:View → Developer)", strings.Join(reasons, ";"))
}
```

`auto_other.go`:`//go:build !darwin`,`func AutoWebTokens() (WebTokens, error) { return WebTokens{}, ErrAutoUnsupported }`。

cli:`cmd.Flags().Bool("auto", false, "")` + `_ = cmd.Flags().MarkHidden("auto")`;seam `var appleAutoTokens = apple.AutoWebTokens`;`appleLogin` 在 `dev == ""` 且 `--auto` 時:

```go
	if auto, _ := cmd.Flags().GetBool("auto"); auto && dev == "" {
		// 唯一例外(CLAUDE.md):隱藏、opt-in、開發者自負。揭露照樣不可跳過。
		if stdinIsTTY() {
			if err := confirmAppleDisclosure(); err != nil {
				return err
			}
		} else if ok, _ := cmd.Flags().GetBool("i-understand"); !ok {
			return errors.New(appleDisclosure + "\n\n--auto 在非互動環境需加 --i-understand")
		}
		wt, err := appleAutoTokens()
		if err == nil {
			return applePersist(cmd.Context(), cmd.OutOrStdout(), wt.Developer, wt.User)
		}
		if !stdinIsTTY() {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "自動擷取失敗,改用手動貼上:%v\n", err)
		_, gerr := secret.Get(apple.KeyMusicUserToken)
		dev, user, err = runAppleWizardInputs(gerr == nil)
		if err != nil {
			return err
		}
		return applePersist(cmd.Context(), cmd.OutOrStdout(), dev, user)
	}
```

- [ ] **Step 3: docs/ARCHITECTURE.md §4.3**:(c) 加一條「developer token 也寫死在網頁播放器的 JS bundle 裡(`assets/index[~-]*.js`);`--auto` 不走這條,只從已登入分頁的 `MusicKit.getInstance()` 一次拿兩個」;(d) 改寫成上面的單一機制(刪「cookie 資料庫」句),保留「唯一例外、揭露不可跳過、macOS 優先、best-effort、預期會壞」。README 不提(隱藏)。
- [ ] **Step 4: 手動 smoke(darwin,可選):開 Safari 登入 music.apple.com、開啟允許 Apple 事件 JS,`go run ./cmd/capy auth login apple --auto`。** 全綠 → commit `feat(apple): 隱藏 --auto(AppleScript 讀已登入分頁的 MusicKit.getInstance();失敗回退手動)+ spec §4.3(c)(d) 對齊`

## 附錄 A:驗收(需 Apple Music 訂閱與一個瀏覽器;**不需要 Apple Developer 會籍** —— 這正是本次改版的目的)

依序做;C-0 是前提,壞了後面全免。每項記錄結果到 PR 留言。

- **C-0 主機/標頭真值表**(先於一切):照 README 抓 token 後 `capy auth login apple`。
  - preflight 401 → 先試 `CAPY_APPLE_API_BASE=https://api.music.apple.com/v1`;仍 401 → 檢查 token 是否貼到 `authorization` 的值(不是 cookie);
  - storefront 403/404 → 把 client 的 MUT 改成 cookie 形式(`Cookie: media-user-token=…`,gamdl 用法),或 storefront 端點改 `GET /me/account?meta=subscription` → `meta.subscription.storefront`;
  - **歸因檢查**:刻意弄壞 developer token → 訊息必須怪 developer token(401);弄壞 user token → 怪 user token(403)。amp-api 若對 Origin 問題也回 403,`friendlyErr` 會怪錯 token,要修訊息。
  - 定案後:把贏家寫死、刪輸家(`ponytail:` 註解那一塊),補進 spec §4.3(a) 的「已知的坑」。
- **C-1 手動抓 token + 精靈**:記錄 DevTools 實際欄位名、JWT `exp` 距今多久(→ R-6 的輪替週期估計)。`auth status` 顯示到期日。
- **C-2 功能**:`search --provider apple`、`pl list`、`pl show <名稱|p.xxx>`、`play --id`(macOS);決定 play 機制 A/B(`CAPY_APPLE_PLAY_MECHANISM=open` 對照),寫死贏家 + 之後加 `player state` 輪詢(P2 遺留)。
- **C-3 非 TTY**:`CAPY_APPLE_DEVELOPER_TOKEN=… CAPY_APPLE_USER_TOKEN=… capy auth login apple --i-understand < /dev/null`;缺 `--i-understand` 被拒且訊息含聲明。
- **C-4 只更新 developer token**:`capy auth login apple` → 精靈預設「只更新」→ 只貼一顆;`auth status` 的 user token 未變。
- **C-5 `--auto`**:Safari 與 Chrome 各一次(先開權限);未開權限時的失敗訊息要能照著做。
- **C-6 過期行為**:用 `security` 或改 keychain 紀錄把 exp 改到過去 → `capy search --provider apple` 訊息指向 login 並含「過期」;`doctor --provider apple` ❌;`auth status` 顯示「已於 … 過期」。

## 附錄 B:便條

- 舊使用者 config 內的 `install_id` / `apple_token_endpoint`:Load 忽略、下次 Save 消失(Task 2 有測試)。keychain 內舊格式的 `apple.developer_token`(同 JSON 形狀)沿用;舊 Worker 派發的 token 到期後自然走「已過期 → 重新登入」。
- `docs/superpowers/plans/2026-09-02-p2-apple.md` 是歷史文件,不改。
- issue #3(Spotify refresh token 多程序輪替)與本計畫無關,維持待決。
