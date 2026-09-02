# capy-music — 跨平台音樂 CLI 架構規劃

> 專案名稱 `capy-music`,binary `capy`
> 文件版本 v0.4 — 2026-09-01(定案版:語言 Go、macOS+Windows、TUI 第一天進場、對外發佈、Worker 掛 capy.taislife.work,見附錄 C;v0.3 專案定名 capy-music;v0.2 新增 §8.5 營運成本與風險)
> 本文件為架構基準,所有「已驗證」標記的事實均於 2026-08 查證。

---

## 0. 專案定位與三條鐵則

**定位**:一個 CLI/TUI,提供跨平台的音樂**搜尋、播放遙控、播放清單同步**。

三條鐵則,後面所有設計都是它們的推論:

| # | 鐵則 | 理由 |
|---|---|---|
| **R1** | **絕不自己解碼串流音訊** | 所有平台的音訊都在 DRM 後面。我們只做「遙控」與「metadata」。 |
| **R2** | **Apple Music 的程式路徑必須物理隔離** | MusicKit ToS 禁止把 MusicKit JS 與其他 JS 重組。Apple 授權頁是獨立靜態 HTML。 |
| **R3** | **canonical 資料在使用者自己的 Drive,不在我們的伺服器** | 這是「不收訂閱費」的前提,也避開了 restricted scope 與資安評估。 |

---

## 1. 現況約束總表(2026-08 已驗證)

這一節是整份文件最重要的部分。**Spotify 在 2026 年 2 月的政策變更大幅改變了可行的分發模式**,務必先讀。

### 1.1 Spotify

| 項目 | 現況 | 影響 |
|---|---|---|
| Development Mode 使用者上限 | **5 人**(2026-02 從 25 降至 5) | 無法公開分發 → 必須採 BYO Client ID |
| Client ID 上限 | 25 個/開發者帳號(2026-07 從 1 調回) | — |
| App owner 需求 | **必須有 Premium 訂閱**,失效則 app 停擺 | 使用者本來就需要 Premium 才能遙控播放,邏輯自洽 |
| Extended Quota Mode | 僅限**組織**申請,需 ≥250k MAU | 個人專案實質不可能 |
| Redirect URI | **禁用 `localhost`**,必須 `http://127.0.0.1:PORT` 或 `http://[::1]:PORT` | loopback IP literal **可動態指派 port**,不必預先註冊 port |
| `GET /search` limit | 最大 **10**(預設 5),原本 50 | 搜尋必須分頁 |
| 批次端點 | `GET /tracks`、`/albums`、`/artists` 等**全部移除** | 同步時只能逐首查 → rate limit 是主要瓶頸 |
| `external_ids`(含 ISRC) | 2026-02 移除,**2026-03 已回復** | ISRC 仍可用 ✅ 但列為需監控項 |
| `GET /playlists/{id}/items` | 只回傳**使用者擁有或協作**的清單內容;他人清單只有 metadata | **無法同步「追蹤的別人的清單」** |
| 播放清單端點改名 | `/tracks` → `/items`,欄位 `tracks` → `items` | 直接用新名 |
| `GET /me` | 移除 `country`、`email`、`product` | **無法從 API 判斷是否 Premium 或所在市場** |
| Player 端點 | 全部保留(play/pause/next/seek/volume/devices/transfer/queue) | 遙控設計成立 ✅ |

### 1.2 Apple Music

| 項目 | 現況 | 影響 |
|---|---|---|
| Developer Token | ES256 JWT,需 Apple Developer Program($99/年)的 MusicKit `.p8` | `.p8` 不可打包進 CLI → 需 token vending service |
| Music User Token | **只能透過 MusicKit 的 `authorize()` 取得**,無純 HTTP OAuth 流程 | 必須做 loopback + 瀏覽器 MusicKit JS 橋接 |
| MUT 生命週期 | 長期有效,**無 refresh token**,過期只能重跑授權 | 需偵測 401 並提示重新授權 |
| Lossless / Hi-Res | MusicKit JS **不支援**;iOS/tvOS MusicKit 遵循使用者設定但無程式控制 | 高音質只能靠 macOS 的 Music.app |
| Library playlist 寫入 | 建立/新增曲目可行;**移除與重排能力待驗證** | ⚠️ 見 §9 P0-2,可能影響 push 策略 |
| ToS | 禁止與其他 JS 重組、禁止對存取收費、內容不可與其他內容 synchronized | 見 §8 |

### 1.3 Google(Drive appData)

| 項目 | 現況 | 影響 |
|---|---|---|
| `drive.appdata` scope | **非敏感 (non-sensitive)** ✅ | 只需 basic verification,**無需 OAuth 審查、無 100 人上限、無警告畫面** |
| `openid` / `email` / `profile` | 非敏感 | 登入識別用這組即可 |
| ⚠️ Gmail scopes | **restricted** | **絕對不要加**。會觸發第三方資安評估(CASA),年費數千美元 |
| Publishing status = Testing | refresh token **7 天過期** | 必須發佈到 Production |
| appDataFolder 特性 | 每 app 獨立、Drive UI 不可見、無法分享、**計入使用者配額** | 需提供 export/import 逃生口 |
| 併發寫入 | Drive **沒有 atomic CAS** | → 用 per-device 檔案分離,見 §6.3 |

> 📌 使用者說的「Gmail OAuth 登入」在實作上是 **Google Sign-In (OIDC)**,不是 Gmail API。這個區別價值好幾千美元/年,務必寫進 Claude Code 的 context。

---

## 2. 系統分層

```
┌──────────────────────────────────────────────────────────────┐
│  CLI / TUI     cobra + bubbletea                             │
│  capy search · capy play · capy pl sync · capy auth login    │
└────────────────────────┬─────────────────────────────────────┘
                         │
┌────────────────────────▼─────────────────────────────────────┐
│  Application Services                                        │
│  ┌──────────┐ ┌──────────┐ ┌───────────┐ ┌────────────────┐  │
│  │ AuthMgr  │ │ Resolver │ │ SyncEngine│ │ PlaybackRouter │  │
│  └──────────┘ └──────────┘ └───────────┘ └────────────────┘  │
└────────────────────────┬─────────────────────────────────────┘
                         │
┌────────────────────────▼─────────────────────────────────────┐
│  Provider SPI  (capability-based,見 §3)                      │
│  Searcher │ PlaylistReader │ PlaylistWriter │ PlaybackCtrl   │
└──┬──────────────┬──────────────┬──────────────┬──────────────┘
   │              │              │              │
┌──▼────────┐ ┌───▼────────┐ ┌───▼────────┐ ┌───▼────────────┐
│ spotify   │ │ apple      │ │ local      │ │ (未來)          │
│ WebAPI    │ │ AMAPI      │ │ m3u/json   │ │ ytm/tidal/amz  │
│ +Connect  │ │ +osascript │ │ (測試用)    │ │                │
└───────────┘ └────────────┘ └────────────┘ └────────────────┘

┌──────────────────────────────────────────────────────────────┐
│  Persistence                                                 │
│  OS Keychain (tokens) │ SQLite (cache/state) │ Drive appData │
└──────────────────────────────────────────────────────────────┘
```

**關鍵設計:Apple 的「資料能力」與「播放能力」解耦。**
Apple Music API(播放清單、搜尋)在**所有 OS 都可用**;只有播放綁 macOS。所以 Windows 使用者一樣能同步 Apple Music 播放清單,只是不能從 CLI 播 Apple 的歌(播放遙控走 Spotify Connect)。

**TUI 與可腳本化【定案】:** TUI 第一天進場(charmbracelet 全家桶),但**所有命令在非 TTY(pipe/cron)下必須可純文字輸出**——「可納入 cron/script」是 §8.5.6 列的核心價值,不得被 TUI 犧牲。互動場景:無參數執行 `capy` 的儀表板、`now --watch`、`resolve --review`、auth 精靈(huh 表單,含 BYO Client ID onboarding)。

### 語言【已定案】:Go

module path:`github.com/Tai-ch0802/capy-music`

| 需求 | Go 的答案 |
|---|---|
| 單一 binary、跨平台交叉編譯 | `GOOS/GOARCH`,零依賴分發 |
| OS keychain | `zalando/go-keyring`(macOS Keychain / Windows Credential Manager) |
| OAuth + PKCE | `golang.org/x/oauth2` |
| SQLite 無 cgo | `modernc.org/sqlite` |
| CLI / TUI | `spf13/cobra` + charmbracelet 全家桶(bubbletea v2 / lipgloss / bubbles / huh) |
| macOS AppleScript | `exec.Command("osascript", ...)` |

> 2026-09-01 定案:Rust(clap/ratatui)已評估未採用——效能對本專案無感(瓶頸在網路 API 與 rate limit),TUI 生態 charm 全家桶明顯佔優,交叉編譯與個人維護成本較低;Rust 型別系統對同步引擎的優勢改以測試補償。完整理由見附錄 C。Python 不建議——分發成本會吃掉這個工具的價值。

---

## 3. Provider SPI

**用能力介面切分,不要做 god interface。** 各平台能力差異極大,強行統一會逼你到處寫 `ErrNotSupported`。

```go
package provider

type Capability uint32

const (
    CapSearch Capability = 1 << iota
    CapISRCLookup      // 能用 ISRC 反查
    CapISRCExpose      // 回傳的 track 帶 ISRC
    CapPlaylistRead
    CapPlaylistCreate
    CapPlaylistAppend
    CapPlaylistRemove  // ⚠️ Apple 待驗證
    CapPlaylistReorder // ⚠️ Apple 待驗證
    CapLibraryRead
    CapLibraryWrite
    CapPlaybackControl
)

type Provider interface {
    ID() string                     // "spotify" | "apple" | "local"
    DisplayName() string
    Caps() Capability
    Health(ctx context.Context) error
}

type Searcher interface {
    Search(ctx context.Context, q Query) ([]Track, error)
    GetTrack(ctx context.Context, id string) (*Track, error)
    LookupISRC(ctx context.Context, isrc string) ([]Track, error)
}

type PlaylistReader interface {
    ListPlaylists(ctx context.Context) ([]PlaylistRef, error)
    GetPlaylistItems(ctx context.Context, id string) ([]Track, error)
}

type PlaylistWriter interface {
    CreatePlaylist(ctx context.Context, name, desc string) (string, error)
    // ApplyOps 一次套用一批操作,由 provider 決定用什麼端點實現。
    // 不支援的操作回 ErrCapability,SyncEngine 會 fallback 到 rebuild 策略。
    ApplyOps(ctx context.Context, playlistID string, ops []PlaylistOp) error
}

type PlaybackController interface {
    Devices(ctx context.Context) ([]Device, error)
    State(ctx context.Context) (*PlaybackState, error)
    Play(ctx context.Context, req PlayRequest) error
    Pause(ctx context.Context) error
    Next(ctx context.Context) error
    Prev(ctx context.Context) error
    Seek(ctx context.Context, posMS int) error
    SetVolume(ctx context.Context, pct int) error
    Enqueue(ctx context.Context, uri string) error
}

type Track struct {
    ProviderID  string   // provider 內部 ID
    ISRC        string   // 可能為空
    Title       string
    Artists     []string
    Album       string
    DurationMS  int
    Explicit    bool
    Raw         json.RawMessage
}
```

**`ApplyOps` 而非細顆粒方法**是刻意的:讓 provider 自己決定「逐條 API 呼叫」還是「整批 replace」。Spotify 有 `PUT /playlists/{id}/items` 可整批取代;Apple 可能只能重建。這個抽象讓兩者都塞得下。

---

## 4. 認證架構

三個 provider 三種完全不同的流程,但共用同一個 loopback 基礎設施。

### 4.1 共用元件:`internal/auth/loopback.go`

```
1. net.Listen("tcp", "127.0.0.1:0")   → 取得隨機 port
2. 起 http.Server,註冊 /callback
3. 產生 state (32 bytes CSPRNG)
4. 開瀏覽器 (macOS: open / Windows: rundll32)
5. 等待 callback 或 timeout(建議 180s)
6. 驗證 state → 取 code/token → 關 server
7. 回傳成功頁面(自動關閉分頁的 HTML)
```

Spotify 明確允許 loopback IP literal 動態 port,所以 `:0` 是安全的。Google 的 "Desktop app" client type 也允許動態 port。

### 4.2 Spotify:Authorization Code + PKCE

```
GET https://accounts.spotify.com/authorize
  ?client_id={使用者自己的}
  &response_type=code
  &redirect_uri=http://127.0.0.1:{port}/callback
  &code_challenge_method=S256
  &code_challenge={S256(verifier)}
  &state={state}
  &scope={見下}
```

**Scopes:**
```
user-read-playback-state
user-modify-playback-state
user-read-currently-playing
playlist-read-private
playlist-read-collaborative
playlist-modify-private
playlist-modify-public
user-library-read
user-library-modify
```

> **決策(2026-09-02,PR #4 review):** P1 只用到讀+播放,但一開始就索取全部 9 個(含三個 modify)。理由:避免 P4 同步進場時全體 BYO 使用者重跑授權(重授權的支援成本高於風險);接受的代價:P1–P3 期間憑證握有尚無程式路徑使用的寫入權限。與 Google 最小權限標準不同的原因:Google 的 scope 分級直接觸發審查與資安評估成本,Spotify 無此機制。緩解:憑證只在 keychain、P4 寫入路徑上線前必過 dry-run 與閾值護欄。

> 不需要 `streaming`(那是 Web Playback SDK 用的,我們走 Connect 遙控)。
> 不需要 `user-read-email` / `user-read-private`(2026-02 後 `GET /me` 已不回傳這些欄位)。

**BYO Client ID 流程** —— 因為 5 人上限,這是唯一可分發的路:

```
$ capy auth login spotify

  Spotify 的開發者政策限制每個 app 只能有 5 位使用者,
  所以你需要建立自己的 app(免費,2 分鐘):

  1. 前往 https://developer.spotify.com/dashboard
  2. Create app,名稱隨意
  3. Redirect URI 填入(完全照抄):
       http://127.0.0.1:8888/callback
  4. 勾選 Web API
  5. 複製 Client ID 貼在下面

  Client ID: ____
```

> 注意:雖然 loopback 可動態 port,但 Dashboard 需要填一個固定值。固定用 `8888` 並在 CLI 端優先嘗試 8888,佔用時才 fallback 動態 port(此時需引導使用者加註冊)。

**⚠️ PKCE refresh token 會輪替。** 每次 refresh 都會拿到新的 refresh_token,舊的失效。**兩台裝置共用同一份 refresh token 會互相踢掉。** → token 絕不進 Drive 同步,每台裝置各自授權。

### 4.3 Apple Music:Developer Token 派發 + MusicKit 橋接

這是三者中最複雜的。分兩段。

#### (a) Developer Token — Cloudflare Worker 派發

`.p8` 私鑰不能進 binary。用 Worker 當簽發端:

```
POST https://capy.taislife.work/v1/apple/developer-token
Body: { "install_id": "<CLI 首次啟動產生的 uuid>" }

→ Worker 用 Secret 裡的 .p8,以 crypto.subtle ECDSA P-256 簽 ES256 JWT
→ 回 { "token": "eyJ...", "expires_at": 1735689600 }
```

> 【定案】Worker 掛 `capy.taislife.work`(沿用既有網域)。此 endpoint 是 binary 內建預設值,**必須可被 config 覆寫**(自架 Worker 或 BYO `.p8` 的人需要)。

JWT 內容:
```jsonc
// header
{ "alg": "ES256", "kid": "<Key ID>" }
// payload
{ "iss": "<Team ID>", "iat": <now>, "exp": <now + 12h> }
```

設計決策:
- **簽短期 token(12h)而非上限的 6 個月** —— 外洩損害可控
- **不加 `origin` claim** —— 因為 loopback port 是動態的。MUT 仍需使用者互動授權,風險可接受
- Worker 做 per-`install_id` 與 per-IP rate limit(用原生 Rate Limiting binding,見 §8.5.2)
- 使用者若自備 Apple Developer 帳號,可設 `CAPY_APPLE_P8_PATH` 走本地自簽,完全繞過 Worker

#### (b) Music User Token — loopback + MusicKit JS 橋接

```
CLI                          瀏覽器                        Apple
 │                              │                            │
 ├─ 起 127.0.0.1:{port}         │                            │
 ├─ 取 developer token          │                            │
 ├─ open browser ──────────────>│                            │
 │                              ├─ GET /apple/authorize      │
 │                              │  (獨立靜態 HTML)            │
 │                              ├─ load musickit.js (v3) ───>│
 │                              ├─ MusicKit.configure({dt})  │
 │                              ├─ [使用者點按鈕]             │
 │                              ├─ music.authorize() ───────>│
 │                              │<─── Music User Token ──────┤
 │<── POST /apple/callback ─────┤                            │
 ├─ 驗證 state → 存 keychain    │                            │
 ├─ 關閉 server                 │                            │
```

**授權頁的硬性要求(ToS R2):**
```html
<!-- internal/auth/apple/web/authorize.html -->
<!-- 這個檔案只准載入 musickit.js。不打包、不 bundle、不 import 任何其他 JS。 -->
<script src="https://js-cdn.music.apple.com/musickit/v3/musickit.js" async></script>
```

**已知的坑:**
- `music.authorize()` **必須由 user gesture 觸發**,不能在 `onload` 自動跑(Safari 會擋)→ 頁面一定要有按鈕
- `http://127.0.0.1` 屬於 secure context(W3C 定義的 potentially trustworthy origin),MusicKit 可運作
- MUT 綁 developer team,不綁單一 token → 輪替 developer token 不會使 MUT 失效
- MUT 無 refresh。收到 401/403 時 → 標記為過期 → 提示 `capy auth login apple`

### 4.4 Google:Authorization Code + PKCE(Desktop app client)

```
Scopes:
  openid
  https://www.googleapis.com/auth/userinfo.email
  https://www.googleapis.com/auth/drive.appdata     ← 非敏感 ✅
```

設定要點:
- Cloud Console → Credentials → **OAuth client type: Desktop app**
- Publishing status **必須設為 "In production"**(Testing 模式 refresh token 7 天過期)
- 只用非敏感 scope → 只需 basic verification,不會出現「此應用程式未經驗證」畫面
- `access_type=offline` + `prompt=consent`(首次)才會拿到 refresh_token

### 4.5 憑證儲存與生命週期

| 憑證 | 儲存 | 生命週期 | 更新方式 |
|---|---|---|---|
| Spotify access token | 記憶體 | 1h | refresh token |
| Spotify refresh token | **OS Keychain** | 長期 / **會輪替** | 每次 refresh 覆寫 |
| Spotify client_id | config 檔(非機密) | 永久 | 使用者輸入 |
| Apple developer token | 記憶體 + 快取檔 | 12h | 向 Worker 重取 |
| Apple Music User Token | **OS Keychain** | 長期 / 無 refresh | 過期需重跑 §4.3(b) |
| Google access token | 記憶體 | 1h | refresh token |
| Google refresh token | **OS Keychain** | 長期 | 標準 refresh |

Keychain 後端【定案】:**macOS Keychain / Windows Credential Manager**(`zalando/go-keyring` 皆支援)。**Linux 非目標,不實作。**

> 未來若支援 Linux:桌面走 Secret Service (libsecret);headless 無 Secret Service 時需加密檔 fallback(v0.3 曾規劃 `~/.config/capy-music/creds.age`,XChaCha20-Poly1305 + `CAPY_PASSPHRASE`、權限 `0600`)。目前不實作,此段僅留檔備查。

**❌ 憑證永遠不進 Drive appData。** 每台裝置各自授權,理由見 §4.2 的 refresh token 輪替問題。

---

## 5. 曲目識別與比對(Resolver)

同步的成敗全繫於此。這是整個專案技術風險最高的部分。

### 5.1 三層識別策略

```
Layer 1 — ISRC(主鍵)
   Spotify: track.external_ids.isrc      (2026-03 已回復 ✅)
   Apple:   attributes.isrc
   信心度 0.95

Layer 2 — 正規化模糊鍵
   key = norm(title) | norm(primary_artist) | round(duration_ms / 1000)
   norm(): NFKC → lower → 去除 (feat. ...) / [Remastered] / - Live 等後綴
           → 全形轉半形 → 去標點 → 中日韓不做斷詞
   時長容差 ±3s
   信心度 0.60 ~ 0.85(依 Jaro-Winkler 相似度加權)

Layer 3 — 人工釘選(最高優先)
   mappings 表:cid → { spotify: "...", apple: "..." }
   信心度 1.00,永不被自動覆寫
```

### 5.2 已知陷阱(必須寫進測試案例)

| 陷阱 | 表現 | 對策 |
|---|---|---|
| 一首錄音多個 ISRC | 重發、remaster、地區版各有 ISRC | 建 ISRC alias set,任一命中即視為同一 cid |
| Apple 部分曲目缺 ISRC | `attributes.isrc` 為空 | 自動降級到 Layer 2 |
| YouTube Music 完全無 ISRC | — | 未來接入時只能 Layer 2 + 人工 |
| Live / Remix / Cover 誤配 | 標題相近、時長相近 | 標題含 live/remix/acoustic/cover 關鍵字時提高門檻 |
| 中文簡繁 / 藝名別名 | 「五月天」vs「Mayday」 | 維護 artist alias 表,可從兩邊 API 的 artist 物件互相學習 |
| 區域下架 | 某平台查得到但不可播 | `available_markets` 已被 Spotify 移除 → 只能靠播放時的錯誤回報 |

### 5.3 Review Queue

信心度 < 0.85 的配對**不自動寫入**,進 review queue:

```
$ capy resolve --review

  [1/7]  canonical: 派對動物 — 五月天  (3:47)
         spotify  ✓ 0.97  派對動物 - 五月天
         apple    ? 0.71  派對動物 (Live) - 五月天  (4:12)
         [a]ccept  [s]kip  [m]anual search  [n]ot available
```

決策寫入 `mappings`(信心度 1.00),之後永久沿用。

### 5.4 解析快取

同步時逐首查 API 成本極高(Spotify 已移除批次端點!)。三層快取:

1. **SQLite `resolution_cache`** — cid × provider → provider_id,TTL 30 天
2. **Negative cache** — 查不到的也要記,TTL 7 天,避免每次同步重打
3. **Drive `tracks/mappings.json`** — 跨裝置共用解析結果,新裝置首次同步幾乎零 API 呼叫

第 3 點很重要:它讓「換一台電腦」不會重跑幾千次 API。

---

## 6. 同步引擎

### 6.1 心智模型:Git

| Git | capy-music |
|---|---|
| `origin` | Google Drive appDataFolder |
| working tree | 各平台上的實際播放清單 |
| `git fetch` | `capy pl pull` |
| `git push` | `capy pl push` |
| 3-way merge base | `base/<provider>.json` 上次同步快照 |

**canonical 是 source of truth,各平台是投影 (projection)。**

### 6.2 資料模型

```jsonc
// canonical playlist
{
  "pid": "01J8X...",              // ULID
  "name": "通勤",
  "description": "",
  "updated_at": 1756600000,
  "items": [
    { "cid": "01J8Y...", "rank": "a0", "added_at": 1756500000 },
    { "cid": "01J8Z...", "rank": "a1", "added_at": 1756500100 }
  ],
  "links": {
    "spotify": "37i9dQZF1DX...",
    "apple":   "p.LV0PXNvClXm"
  }
}

// canonical track
{
  "cid": "01J8Y...",
  "isrc": ["TWA472400123"],       // alias set
  "title": "派對動物",
  "artists": ["五月天"],
  "album": "自傳",
  "duration_ms": 227000
}
```

**排序用 fractional index(`rank` 欄位)而非整數位置。** 兩台裝置同時在中間插入時,整數位置一定衝突;字典序 rank 可以無衝突地在 `"a0"` 和 `"a1"` 之間生出 `"a0V"`。參考 `rocicorp/fractional-indexing` 演算法。

### 6.3 Drive appData 佈局

```
appDataFolder/
├── manifest.json                    # schema_version, devices[], last_compaction
├── playlists/
│   └── <pid>/
│       ├── meta.json                # name, desc, links
│       ├── snapshot.json            # 最近一次壓縮後的完整狀態
│       ├── ops/
│       │   ├── <device_id>.jsonl    # ⭐ 每台裝置只寫自己的檔
│       │   └── ...
│       └── base/
│           ├── spotify.json         # 上次同步時該平台的狀態快照
│           └── apple.json
├── tracks/
│   ├── index.json                   # cid → 曲目 metadata
│   └── mappings.json                # cid → { provider: provider_id }
└── export/
    └── latest.json                  # 人類可讀的完整匯出(逃生口)
```

**⭐ `ops/<device_id>.jsonl` 的分檔是核心設計。**
Drive 沒有 atomic compare-and-swap,也沒有真正的 append(update 是整檔覆寫)。如果所有裝置寫同一個檔,一定會 lost update。**每台裝置只寫自己的 log 檔 → 寫入永不衝突。** 讀取時載入所有 device log,依 HLC 排序 replay。這是無鎖的。

Compaction(合併 ops → snapshot)需要協調,用 `manifest.json` 裡的 lease:
```jsonc
{ "compaction_lease": { "device": "macbook", "expires_at": 1756600300 } }
```
過期即可搶佔。Compaction 不是必要路徑,失敗就下次再做。

### 6.4 操作紀錄格式

```jsonc
{ "op": "add",    "pid": "...", "cid": "...", "rank": "a0V", "hlc": "1756600000:0003:macbook" }
{ "op": "remove", "pid": "...", "cid": "...",                "hlc": "..." }
{ "op": "move",   "pid": "...", "cid": "...", "rank": "b2",  "hlc": "..." }
{ "op": "rename", "pid": "...", "name": "通勤 2026",          "hlc": "..." }
```

HLC(Hybrid Logical Clock)= `物理時間:計數器:device_id`,提供全序且對時鐘偏移有容忍度。

**Tombstone 必須保留。** 否則裝置 A 刪除的曲目,會被裝置 B 的舊快照重新加回去。Tombstone 在 compaction 時保留 30 天再清除。

### 6.5 同步流程

```
capy pl sync 的一輪:

1. FETCH
   ├─ Drive: 讀 manifest + 所有 ops/*.jsonl + snapshot
   └─ replay → canonical 狀態 C

2. OBSERVE
   └─ 對每個已連結的 provider,讀取平台實際狀態 L
       (Spotify: GET /playlists/{id}/items,注意分頁)

3. DERIVE
   └─ 平台不會給你 ops,只給你 state。
       ops_local = diff(base[provider], L)
       → 轉成 op 追加到本機 ops/<device_id>.jsonl

4. MERGE
   └─ C' = replay(all ops, 依 HLC 排序)
       衝突規則:
         add  vs add  同 cid   → 去重,取較早的 rank
         remove vs move       → remove 勝(刪除意圖較明確),記錄到 sync log
         rename vs rename     → HLC 較大者勝
         rank 衝突            → HLC 較大者勝

5. PROJECT
   └─ 對每個 provider:
       ops_push = diff(L, project(C', provider))
       provider.ApplyOps(ops_push)
       ⚠️ 若 provider 缺 CapPlaylistRemove/Reorder
          → fallback: 建新清單 + 整批寫入 + 改名 + 刪舊(rebuild 策略)

6. COMMIT
   ├─ 寫回 base/<provider>.json = 新的平台狀態
   ├─ 上傳 ops/<device_id>.jsonl
   └─ 更新 manifest
```

**`--dry-run` 必須是一等公民。** 這種工具最可怕的失敗是靜默刪掉使用者幾百首歌。預設對「刪除 > 10 首」的操作要求確認。

### 6.6 安全網

| 機制 | 說明 |
|---|---|
| Dry-run 預設引導 | 首次 `sync` 自動先跑 `--dry-run` 並要求確認 |
| 刪除閾值 | 單次 push 刪除 >10 首或 >30% 時中止並要求 `--force` |
| 快照備份 | 每次 push 前,把該平台狀態存到 `base/`,可用 `capy pl restore` 回滾 |
| Export 逃生口 | `capy export` 產生完整 JSON,不依賴 Drive |

---

## 7. 本機儲存(SQLite)

`~/Library/Application Support/capy-music/state.db`(macOS);Windows 用 `%AppData%\capy-music\state.db`;可用 `CAPY_CONFIG_DIR` 覆寫整個設定目錄

```sql
CREATE TABLE tracks (
  cid TEXT PRIMARY KEY, title TEXT, artists TEXT, album TEXT,
  duration_ms INTEGER, updated_at INTEGER
);
CREATE TABLE isrcs (cid TEXT, isrc TEXT, PRIMARY KEY (cid, isrc));
CREATE TABLE mappings (
  cid TEXT, provider TEXT, provider_id TEXT,
  confidence REAL, pinned INTEGER DEFAULT 0, updated_at INTEGER,
  PRIMARY KEY (cid, provider)
);
CREATE TABLE playlists (pid TEXT PRIMARY KEY, name TEXT, description TEXT, updated_at INTEGER);
CREATE TABLE playlist_items (pid TEXT, cid TEXT, rank TEXT, added_at INTEGER, PRIMARY KEY (pid, cid));
CREATE TABLE playlist_links (pid TEXT, provider TEXT, provider_id TEXT, PRIMARY KEY (pid, provider));
CREATE TABLE ops (hlc TEXT PRIMARY KEY, pid TEXT, payload TEXT, synced INTEGER DEFAULT 0);
CREATE TABLE sync_state (provider TEXT, pid TEXT, base_hash TEXT, last_sync_at INTEGER, PRIMARY KEY (provider, pid));
CREATE TABLE resolution_cache (cid TEXT, provider TEXT, provider_id TEXT, found INTEGER, expires_at INTEGER, PRIMARY KEY (cid, provider));
CREATE TABLE review_queue (cid TEXT, provider TEXT, candidates TEXT, created_at INTEGER, PRIMARY KEY (cid, provider));
```

SQLite 是 **cache + 離線佇列**,不是 source of truth。刪掉整個 db 應該能從 Drive 完整重建。這是設計約束,要寫測試驗證。

---

## 8. ToS 合規檢查表

| 條款 | 我們的做法 |
|---|---|
| MusicKit JS 不可與其他 JS 重組 | 授權頁是獨立靜態 HTML,只有一個 `<script src>` 指向 Apple CDN。**不 bundle、不 webpack、不 vite。** |
| 不可對 Apple Music 存取收費 | 永久免費 + 開源。這也正是專案初衷。 |
| MusicKit Content 不可與其他內容 synchronized | ⚠️ 灰色地帶。保守做法:**只同步使用者自建的 library playlist**;不觸碰 Apple 編輯清單/目錄;不把 Apple cover art 用在播放脈絡以外 |
| 不可下載/修改 MusicKit Content | 我們只碰 metadata,不碰音訊 |
| Spotify Developer Policy | BYO Client ID,使用者自負其 app 的合規 |
| Google API Services User Data Policy | 只用非敏感 scope,資料只存使用者自己的 appDataFolder,**我們的伺服器不存任何使用者資料** |

Cloudflare Worker 只做 developer token 簽發,**不經手任何使用者音樂資料**。這點在隱私權政策裡要寫清楚(Google basic verification 會需要隱私權政策 URL;隱私權政策與 Worker 同掛 taislife.work)。

---

## 8.5 營運成本與風險

> 前提:免費對外、無盈利模式、Spotify 採 BYO credential。

### 8.5.1 固定成本

| 項目 | 年費 | 說明 |
|---|---|---|
| **Apple Developer Program** | **US$99** | 唯一無法避免的支出。取得 MusicKit `.p8` 私鑰的門檻 |
| Cloudflare Workers | $0 | 免費方案:100,000 requests/day、10 ms CPU/invocation |
| Google Cloud / Drive API | $0 | Drive API 無用量計費;`drive.appdata` 為非敏感 scope,不需 OAuth 審查 |
| GitHub(repo + Actions + Releases) | $0 | 公開 repo 的標準 GitHub-hosted runner 免費且無分鐘上限,**含 macOS runner**(Free 方案最多 5 個並行 macOS job) |
| 網域 | $0 | 沿用既有網域 `taislife.work`,邊際成本零 |
| Spotify Developer | $0 | BYO Client ID,成本轉嫁使用者 |
| **合計** | **US$99 / 年** | 約 NT$3,200 |

**附帶綜效:** 這筆 $99 同時提供 macOS 的 Developer ID 憑證與免費 notarization。Apple Music 功能與「macOS binary 不跳 Gatekeeper 警告」是同一筆錢買的。

**測試用訂閱(已具備 ✅):** Apple Music 訂閱與 Spotify Premium 均有效(2026-09-01 確認),邊際成本為零。

### 8.5.2 為什麼 Worker 是 $0 —— 以及必須避開的 KV 陷阱

Apple developer token 的 payload 只有 `{iss, iat, exp}`,**沒有任何 per-user claim**。因此:

- **所有使用者共用同一個 token**
- 簽章運算:每 12 小時 1 次,而非每位使用者 1 次
- 請求數:CLI 端快取 token 12h → 每人每天約 2 次

```
100,000 req/day ÷ 2 req/user/day ≈ 50,000 名日活使用者才觸及免費上限
超過後:Workers Paid $5/月,含 1,000 萬 requests/月
```

> **⚠️ 不要用 KV 做 rate limiting。**
> Workers KV 免費層每天只有 **1,000 次寫入**(讀取 100,000 次)。每請求寫一次 KV,約 500 名使用者就會耗盡 —— 而且耗盡的是寫入配額而非請求配額,失敗訊號不直觀,很難 debug。
>
> 正確做法:
> - Token 快取 → **Cache API (`caches.default`)**,不是 KV
> - 速率限制 → Cloudflare 原生 **Rate Limiting binding**(不消耗 KV 配額)

### 8.5.3 成本 vs. 規模

| 使用者規模 | Apple | Worker | Google | GitHub | 年成本 |
|---|---|---|---|---|---|
| 1(自用) | $99 | $0 | $0 | $0 | **$99** |
| 100 | $99 | $0 | $0 | $0 | **$99** |
| 1,000 | $99 | $0 | $0 | $0 | **$99** |
| 10,000 | $99 | $0 | $0 | $0 | **$99** ⚠️ |
| 50,000+ | $99 | $60 | $0 | $0 | **$159** |

成本近乎與人數無關,因為架構已把所有隨人數成長的資源推給使用者自己(Spotify 用其 credential、Drive 用其空間)。⚠️ 標記處的限制**不是金錢,是配額** —— 見下節。

### 8.5.4 非金錢風險(比成本重要)

| # | 風險 | 說明 | 緩解 |
|---|---|---|---|
| **R-1** | **Apple API 配額集中於單一 Team ID** | 所有使用者的 catalog / playlist 請求都掛在同一個 developer token 底下。Apple 未公開 rate limit 數字,但配額以 team 為單位計。**會先撞限流,不是先撞帳單** | 重度快取(§5.4 已設計);`.p8` 做成 BYO-able |
| **R-2** | **developer token 可被擷取** | 這是 MusicKit JS 的固有性質(Apple 自家網頁播放器的 token 長期可從瀏覽器 console 取得)。最壞情況:被濫用導致 Apple 停權,損失不只 $99,而是名下所有 Apple 開發資源 | 短 TTL(12h)、Worker 速率限制、用量監控 |
| **R-3** | **持續付費的承諾綁定** | 一旦有使用者,停繳 $99 → 所有人的 Apple 功能同時失效。這是免費專案裡少見的「無法隨時停損」結構 | **`.p8` BYO-able 是主要解方**,讓專案在不依賴維護者付費的情況下仍可運作 |
| **R-4** | **平台政策變動頻率高** | Spotify 半年內改兩次:2026-02 使用者上限 25→5 且強制 owner 持有 Premium;2026-07 Client ID 上限 1→25。Apple 亦有非預告的 MusicKit 更新紀錄 | 附錄 B 的監控清單;CI 週期性 API 斷言 |
| **R-5** | **支援負擔** | BYO Spotify credential 流程必然產生大量「設定不起來」的 issue,最常見是 redirect URI 寫成 `localhost` | `capy doctor` 列為一等公民;錯誤訊息直接指出正確值 |

### 8.5.5 時間成本(實際上最貴的一項)

| 項目 | 估計 |
|---|---|
| 例行維護 | 4–8 小時 / 月 |
| 平台破壞性變更應對 | 10–20 小時 / 次,約 2–3 次 / 年 |
| 支援與 issue 處理 | 隨使用者數線性成長 |
| **年化合計** | **約 80–150 小時 / 年** |

以任何合理時薪換算,$99 都是整份成本結構裡最小的數字。**決策應以時間預算為準,不是以現金支出為準。**

### 8.5.6 與付費替代方案的對照

| 工具 | 年費 |
|---|---|
| Soundiiz | 約 $24/年(或 $4.50/月) |
| TuneMyMusic | $24/年(年繳)/ $5.50 月繳 |
| FreeYourMusic | $49.99/年,或 €199.99 買斷 |
| **本專案** | **$99/年** |

**⚠️ 誠實的結論:若動機純粹是「不想付同步工具的訂閱費」,本專案在財務上是永久虧損的,不存在回本點。**

專案的實際價值在於現有工具無法提供的部分:

- **CLI 與自動化介面** —— 上述工具皆無終端機介面,無法納入 cron / script
- **資料主權** —— 上述工具皆為雲端處理,音樂庫需經其伺服器;本架構的資料只存在使用者自己的 Drive
- **播放遙控** —— 同步工具完全不涵蓋此領域,為本專案獨有
- **技術作品價值** —— 三種相異的 OAuth 流程、CRDT 式無鎖同步、DRM 邊界處理

> 建議把本專案定位為**技術作品**而非**省錢方案**來評估。

### 8.5.7 降險與省錢決策

| 決策 | 效益 |
|---|---|
| **Windows code signing 憑證跳過不買** | 省 $200–600/年。改走 Scoop / winget 分發,接受 SmartScreen 警告 |
| **Worker token 快取用 Cache API,不用 KV** | 避開每日 1,000 次寫入的免費層天花板 |
| **`.p8` 從第一天就做成 BYO-able** | 同時緩解 R-1 / R-2 / R-3 三項風險 |
| **Google brand verification 去做** | 免費、2–3 個工作天。非敏感 scope 本不強制驗證,但完成後同意畫面才會顯示 app 名稱與 logo。需以 Search Console 驗證網域所有權,並在同一網域(taislife.work)掛隱私權政策 |
| **Cloudflare 帳號設定 spending limit** | 免費方案本不扣款,但升級後需防止流量異常造成帳單 |
| **`capy doctor` 列為 P1 而非 nice-to-have** | 直接消化約一半的支援 issue(見 R-5)。已定案進 P1(對外發佈定位) |

---

## 9. 開發階段

### P0 — 驗證與骨架(先做,因為有兩個未知數可能推翻設計)

| # | 任務 | 為什麼是 P0 |
|---|---|---|
| **P0-1** | 用 curl 打通 Apple developer token → `GET /v1/catalog/tw/search`,確認 ISRC 有回傳 | ISRC 是整個 resolver 的基礎 |
| **P0-2** | ⚠️ **驗證 Apple Music API 能否從 library playlist 移除/重排曲目** | 若不行,Apple 端 push 只能用 rebuild 策略,要改 §6.5 |
| **P0-3** | 驗證 MusicKit JS 在 `http://127.0.0.1:{隨機port}` 能否成功 `authorize()` | 若不行,要改成固定 port 或本地 HTTPS |
| **P0-4** | 量測 Spotify Development Mode 的實際 rate limit | 決定同步的併發度與退避策略 |
| P0-5 | 專案骨架:cobra + config + keychain + loopback server | — |

**P0-2 和 P0-3 是架構風險點,建議會籍生效第一天就打 curl 驗證,不要等到寫完再發現。**

> **排程註記(2026-09-01):** Apple Developer 會籍申請中。P0-1/P0-2/P0-3 全部需要 developer token → **gate 在會籍生效**;P0-4/P0-5 與 P1 先行。就算 P0-2 驗出「不能移除/重排」,§6.5 已備好 rebuild fallback,不會推翻架構。

### P1 — Spotify 全鏈路
`auth login spotify`(BYO client ID,huh 表單精靈)→ `search` → `play/pause/next` via Connect → `pl list/show` → **`doctor`**(定案進 P1,見 §8.5.7)

### P2 — Apple 全鏈路
Worker 部署(`capy.taislife.work`)→ `auth login apple`(MusicKit 橋接)→ Apple Music API search → macOS `osascript` 播放

### P3 — Google + Drive
`auth login google` → appDataFolder 讀寫 → manifest / snapshot 基礎設施

### P4 — 單向同步
canonical model → `pl pull`(平台 → canonical)→ resolver(ISRC + fuzzy)→ review queue

### P5 — 雙向同步
op log → HLC → 三方合併 → `pl push` → `--dry-run` + 安全網

### P6 — 抽象驗證
接入 `local` provider(讀 M3U/JSON)驗證 SPI 是否夠通用。**這比直接接第三個真實平台好** —— 沒有 ToS 風險、可完全掌控測試資料。SPI 撐得住 local provider 才去接 YouTube Music / Tidal。

---

## 10. 交接給 Claude Code 的重點

### 已定案的決策(2026-09-01,詳見附錄 C)

1. **binary 名** — `capy`,不做 `cm` 短別名
2. **語言** — Go,module `github.com/Tai-ch0802/capy-music`
3. **Worker 網域** — `capy.taislife.work`(endpoint 預設值可被 config 覆寫)
4. **平台** — macOS + Windows 第一天支援;**Linux 非目標**
5. **TUI** — 第一天進場(charm 全家桶);非 TTY 純文字輸出為鐵則(見 §2)
6. **發佈定位** — 一開始就對外發佈;`capy doctor` 進 P1

### 必須寫進 CLAUDE.md 的約束

已落地至 repo 根目錄 [`CLAUDE.md`](../CLAUDE.md),**以該檔為準**(單一來源,避免兩份漂移)。

### 建議的第一個 commit

不要從 provider 開始寫。先做 **§4.1 的 loopback server + keychain 抽象 + config**,因為三個 provider 都依賴它,而且它是唯一可以在沒有任何 API 憑證的情況下寫測試的部分。

---

## 附錄 A:CLI 命令表面(草案)

```
capy auth login   <spotify|apple|google>
capy auth status
capy auth logout  <provider>

capy search <query> [--provider all|spotify|apple] [--limit N]
capy play   <query|uri> [--on spotify|apple] [--device NAME]
capy pause | capy next | capy prev | capy seek <mm:ss> | capy vol <0-100>
capy now [--watch]

capy pl list
capy pl show   <name>
capy pl link   <name> <provider>:<playlist_id>
capy pl unlink <name> <provider>
capy pl diff   <name>
capy pl pull   [--provider P] [--all]
capy pl push   [--provider P] [--all] [--dry-run] [--force]
capy pl sync   [--dry-run]
capy pl restore <name> --provider P

capy resolve --review
capy export [--out file.json]
capy import <file.json>
capy doctor
```

## 附錄 B:待監控的外部變數

| 項目 | 風險 | 監控方式 |
|---|---|---|
| Spotify `external_ids` (ISRC) | 2026-02 曾被移除,2026-03 回復 | CI 每週打一次 API 斷言 ISRC 存在 |
| Spotify Development Mode 使用者上限 | 一年內從 25 → 5 | 訂閱 Spotify Developer Changelog |
| Apple MusicKit JS 版本 | Cider 團隊提過「意外的 MusicKit 更新」造成中斷 | 授權頁釘住 v3,並做 smoke test |
| Google `drive.appdata` 敏感度分類 | 目前非敏感,若改分類影響巨大 | 每季檢查 Drive API scopes 文件 |
| Spotify Lossless over Connect | 目前 Connect 端點只給 320k Ogg | 若開放,遙控播放的音質敘述要更新 |

## 附錄 C:定案紀錄(2026-09-01)

與維護者逐項討論後定案。若未來要推翻其中任何一項,先讀對應理由。

| # | 議題 | 定案 | 摘要理由 |
|---|---|---|---|
| 1 | 平台 | macOS + Windows;Linux 非目標 | 維護者實際裝置即此二者。§4.5 的 headless age fallback 自 v1 移除;Windows 上 Apple = 僅資料能力(播放遙控走 Spotify Connect),正是 §2 解耦設計的預期行為 |
| 2 | 訂閱/會籍 | 雙訂閱有效;Apple Developer 會籍申請中 | P0-1/P0-2/P0-3 gate 於會籍生效;P0-4/P0-5 與 P1 先行 |
| 3 | 發佈定位 | 一開始就對外發佈 | Worker 照原計畫進 P2、`doctor` 升進 P1、BYO onboarding 用 huh 表單打磨 |
| 4 | TUI | 第一天進場(cobra + charm 全家桶) | 「酷炫 CLI」為維護者點名的重點項目;以「非 TTY 必可純文字輸出」鐵則保護 §8.5.6 的自動化價值 |
| 5 | 語言 | Go(Rust 已評估未採用) | 效能平手(瓶頸在網路 API,兩者皆原生 binary);TUI 生態 charm 全家桶對「酷炫」目標明顯佔優(bubbletea v2 + lipgloss + bubbles + huh vs 較底層的 ratatui);交叉編譯與個人維護成本 Go 較低;Rust 型別系統對同步引擎的小幅優勢改以測試補償 |
| 6 | 命名 | binary `capy`,不做 `cm` 別名 | 一個名字就夠,使用者自行 alias;少一個安裝器要管的 shim |
| 7 | Worker 網域 | `capy.taislife.work` | 沿用既有網域;隱私權政策與 Google brand verification 掛同一網域;endpoint 為 binary 預設值但可被 config 覆寫(自架/BYO `.p8` 場景) |
