# capy-music — 跨平台音樂 CLI 架構規劃

> 專案名稱 `capy-music`,binary `capy`
> 文件版本 v0.6 — 2026-09-03(P3 對齊:`pl pull` 方向、Drive 扁平佈局、`iid` 與決定性 `cid`、SQLite 移除兩張表、quota units、§4.5 憑證表、附錄 C 決策 9–13;v0.5 — 2026-09-03:Apple 改為使用者自抓 web token BYO,`.p8`/Worker/MusicKit 橋接移除,見附錄 C 決策 8 與附錄 D;v0.4 定案版:語言 Go、macOS+Windows、TUI 第一天進場、對外發佈;v0.3 專案定名 capy-music;v0.2 新增 §8.5 營運成本與風險)
> 本文件為架構基準,所有「已驗證」標記的事實均於 2026-08 查證。

---

## 0. 專案定位與三條鐵則

**定位**:一個 CLI/TUI,提供跨平台的音樂**搜尋、播放遙控、播放清單同步**。

三條鐵則,後面所有設計都是它們的推論:

| # | 鐵則 | 理由 |
|---|---|---|
| **R1** | **絕不自己解碼串流音訊** | 所有平台的音訊都在 DRM 後面。我們只做「遙控」與「metadata」。 |
| **R2** | **Apple 憑證由使用者自己複製,程式只指導、絕不自動擷取** | v0.5 起不載入 MusicKit JS(原 R2「物理隔離」已無對象)。web token 是 Apple 未授權第三方使用的灰色地帶:把「擷取」留在使用者手上、指令內強制揭露,是專案能開源的前提。隱藏 `--auto` 為唯一例外(附錄 C 決策 8)。 |
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
| Developer Token | 官方:ES256 JWT,需 Apple Developer Program($99/年)的 MusicKit `.p8`。**本專案(v0.5 起)改由使用者自己從 Apple 網頁播放器複製 Apple 的 web developer token**(非官方) | 免會籍、$0;代價是完全依賴 Apple 不改網頁播放器機制(§8.5.4 R-6) |
| Music User Token | 官方只能經 MusicKit `authorize()` 取得;**本專案改由使用者從網頁播放器的 `media-user-token` 標頭/cookie 複製** | 不需 MusicKit JS 橋接;與 developer token 一起貼入 `auth login apple` |
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

### 4.3 Apple Music:使用者自抓 web token(BYO,非官方)

> **v0.5 定案(附錄 C 決策 8)**:官方路徑(`.p8` 簽 developer token + Cloudflare Worker 派發 + MusicKit JS 橋接取 MUT)需要 Apple Developer Program 會籍與一個由維護者代持簽章的服務;專案開源、憑證全面 BYO 後,改為**使用者自己從 Apple 網頁播放器複製兩個 token**。官方路徑程式碼已移除,完整快照在 commit `3649b7b`,原設計文字保留於附錄 D。

#### (a) 兩個 token 從哪來

Apple 的網頁播放器(music.apple.com)自己就是一個 MusicKit 用戶端:每個對 `amp-api.music.apple.com` 的請求都帶

- `authorization: Bearer <Apple 的 web developer token>` —— Apple 自家簽的 ES256 JWT,全球網頁播放器共用同一顆,Apple 會定期輪替(觀察到的 exp 約數月)
- `media-user-token: <MUT>` —— 使用者登入後的 Music User Token(同時也是 cookie)

使用者登入後開 DevTools → Network → 篩 `amp-api` → 任一請求的 Request Headers,把這兩個值複製出來。**我們只指導這個動作,程式本身絕不自動擷取**(CLAUDE.md 硬約束;唯一例外見 (d))。

#### (b) `capy auth login apple` 流程

```
1. 揭露頁(huh Confirm,預設「否」,不可跳過):非 Apple 官方支援、token 屬於 Apple 網頁播放器、
   Apple 可隨時更換、ToS 風險使用者自負。不同意即結束。
2. 指引頁:上述 DevTools 步驟。
3. 貼上 developer token(自動去掉 "Bearer " 與空白)與 media-user-token。
4. 三段驗證,各給不同訊息,全部通過才落地:
   ├─ 解析 JWT payload 的 exp(不驗簽)→ 已過期直接擋
   ├─ GET /v1/storefronts/us(只帶 developer token)→ 401 = developer token 貼錯/失效
   └─ GET /v1/me/storefront(帶兩者)→ 403 = media-user-token 貼錯/失效;成功即得 storefront
5. keychain 寫 apple.developer_token(JSON {token, exp})與 apple.music_user_token;config 寫 apple_storefront。
```

- **非 TTY**:`CAPY_APPLE_DEVELOPER_TOKEN` / `CAPY_APPLE_USER_TOKEN` 環境變數 + `--i-understand`(揭露的非互動替代;缺任一即拒絕)。flag 形式 `--developer-token/--user-token` 也收,但 argv 可被 `ps` 看到,文件註明。
- **重登(developer token 輪替是常態)**:keychain 已有 user token 時,精靈第一步問「只更新 developer token(保留 user token)?」;非 TTY 只給 developer token 的環境變數即只更新。**這條路徑的成本必須低到像重新登入一樣——它是 R-6 的唯一緩解。**
- 失效偵測:任何 API 回 401/403 → `friendlyErr` 指向 `capy auth login apple`(訊息帶出 401/403 以分辨是哪個 token);`auth status` 顯示 developer token 到期時間;`doctor --provider apple` 檢查「keychain 有且未過期」。

#### (c) 已知的坑

- developer token 是**標頭不是 cookie**;media-user-token 兩者皆是。這決定了 (d) 的自動擷取只能靠驅動瀏覽器或攔截流量,讀 cookie 資料庫拿不到 developer token。
- Apple 輪替 developer token 時,舊 MUT 通常仍有效(MUT 綁帳號不綁 developer token)→ 重登只換前者。
- DevTools 的欄位名稱與 amp-api 路徑以真實驗收首次觀察為準;本節依社群多年穩定作法寫成。
- developer token 也寫死在網頁播放器的前端 bundle(首頁 HTML 裡的 `assets/index[~-]*.js`)裡,一個未認證的 GET 就拿得到——`--auto` 不走這條(單一機制,見 (d)),預設路徑更不走(鐵則);記在這裡供日後決策。

#### (d) 隱藏的 `--auto`(未文件化、opt-in、開發者自負)

`capy auth login apple --auto`:`--help` 不列、README 不提。單一機制:用 AppleScript 找 Safari / Google Chrome 裡第一個已登入的 `https://music.apple.com` 分頁,在頁面執行 `MusicKit.getInstance()` 一次讀出 `developerToken` 與 `musicUserToken`(不讀 cookie 資料庫——developer token 本來就不在 cookie 裡,而驅動瀏覽器一次就能拿到兩個)。前提:瀏覽器已開啟「允許來自 Apple 事件的 JavaScript」(Safari:設定 → 進階 → 顯示「開發」選單 → 開發選單;Chrome:View → Developer),首次執行 macOS 會詢問「終端機想控制 Safari」。任一失敗即說明原因並回退到手動貼上;揭露在 `--auto` 下同樣不可跳過(TTY 為 Confirm,非 TTY 為 `--i-understand`)。它是 CLAUDE.md 鐵則的唯一例外,存在不改變預設路徑「絕不自動擷取」。macOS 限定、best-effort、瀏覽器版本相依,預期會壞。

### 4.4 Google:Authorization Code + PKCE(Desktop app client)

```
Scopes:
  openid
  https://www.googleapis.com/auth/userinfo.email
  https://www.googleapis.com/auth/drive.appdata     ← 非敏感 ✅
```

設定要點:
- Cloud Console → Credentials → **OAuth client type: Desktop app**
- Publishing status **必須設為 "In production"**(Testing 模式 refresh token 7 天過期)。**`drive.appdata` 不在豁免清單**——只有 `openid` / `userinfo.email` / `userinfo.profile` 豁免——BYO 使用者漏按 Publish app 會在第 7 天莫名被登出;`invalid_grant` 且 token 年齡 < 8 天時,錯誤訊息要把「忘了按 Publish app」列為第一嫌疑
- 只用非敏感 scope → 只需 basic verification,不會出現「此應用程式未經驗證」畫面
- `access_type=offline` + `prompt=consent`(首次)才會拿到 refresh_token

**client 歸屬:內建 / BYO 雙路徑(附錄 C 決策 9)**
- release binary 內建維護者的 Desktop client,client ID / secret 由 `-ldflags -X` 在發行時注入,**絕不 commit 進 repo**(Google 政策明文禁止把 client credential 放進公開 repo)。從原始碼 `go install` 的 binary 沒有注入值 → `auth login google` 自動走 BYO 精靈(建專案 → 啟用 Drive API → Branding → Data Access 只加三個 scope → **Publish app** → 建 Desktop client 並立刻複製 secret,它只顯示一次)。
- 程式對兩條路徑一視同仁——內建值只是預設常數,不是分支;`--client-id` / `--client-secret`(或 `CAPY_GOOGLE_CLIENT_ID` / `CAPY_GOOGLE_CLIENT_SECRET`)一律可覆寫。
- BYO 的 client secret **只進 keychain**(`google.client_secret`),不進 config;client ID 非機密,進 config。這是 CLAUDE.md「憑證只進 keychain」的唯一放寬:內建 secret 會編進 release binary,涵蓋的是 app 自身識別,不是任何使用者憑證。
- **`client_secret` 是否必送**:Desktop client 會發 secret,但官方文件把 token 交換的 `client_secret` 標 Optional、豁免清單只列 Android / iOS / Chrome,語意矛盾 → **以實測為準**(不帶 secret 打一次 token 端點;計畫 `docs/superpowers/plans/2026-09-03-p3-google-drive.md` §2.1 G-0,T3 前決定);在此之前 BYO 精靈要求使用者複製兩個值。
- **refresh token 不輪替**:refresh 回應不帶新的 `refresh_token`,不要照抄 Spotify 的輪替覆寫特例(§4.2);但 access token 換了照樣寫回同一筆 keychain 記錄(§4.5)。失效條件:使用者撤銷、6 個月未使用、每帳號每 client 上限 100 個(超過淘汰最舊)——`doctor` 的錯誤訊息要能分辨。

### 4.5 憑證儲存與生命週期

| 憑證 | 儲存 | 生命週期 | 更新方式 |
|---|---|---|---|
| Spotify access token | **OS Keychain**(與 refresh token 同一筆 JSON 記錄,附錄 C 決策 11) | 1h | refresh token;refresh 路徑加跨程序檔案鎖,鎖內重讀 keychain 後才 refresh |
| Spotify refresh token | **OS Keychain**(同上一筆記錄) | 長期 / **會輪替** | 每次 refresh 覆寫 |
| Spotify client_id | config 檔(非機密) | 永久 | 使用者輸入 |
| Apple developer token | **OS Keychain**(JSON 含 exp;使用者貼上) | 依 Apple 輪替(觀察約數月) | 使用者重貼(`auth login apple`,可只更新此項) |
| Apple Music User Token | **OS Keychain**(使用者貼上) | 長期 / 無 refresh | 過期需重貼(§4.3(b)) |
| Google access token | **OS Keychain**(與 refresh token 同一筆 JSON 記錄,形狀與 Spotify 相同) | 1h | refresh token;同一把跨程序檔案鎖 |
| Google refresh token | **OS Keychain**(同上一筆記錄) | 長期 / **不輪替** | 標準 refresh;回應不帶新 refresh_token,寫回同一筆記錄 |
| Google client_id | config 檔(非機密);內建值為 binary 常數 | 永久 | 使用者輸入或發行時注入(§4.4) |
| Google client_secret | **OS Keychain**(`google.client_secret`,BYO);內建值編進 release binary(決策 9 的唯一放寬) | 永久 | 使用者輸入或發行時注入(§4.4) |

Keychain 後端【定案】:**macOS Keychain / Windows Credential Manager**(`zalando/go-keyring` 皆支援)。**Linux 非目標,不實作。**

上表的「跨程序檔案鎖」不是一把共用的鎖:鎖檔是 `config.Dir()/<keychain 鍵名>.lock`(Spotify = `spotify.token.lock`,Google 之後 = `google.token.lock`),**每個鍵各一把**,跟著 `CAPY_CONFIG_DIR` 走。鎖檔一律是空檔,憑證只進 keychain(CLAUDE.md 硬約束)。

> 未來若支援 Linux:桌面走 Secret Service (libsecret);headless 無 Secret Service 時需加密檔 fallback(v0.3 曾規劃 `~/.config/capy-music/creds.age`,XChaCha20-Poly1305 + `CAPY_PASSPHRASE`、權限 `0600`)。目前不實作,此段僅留檔備查。

**❌ 憑證永遠不進 Drive appData。** 每台裝置各自授權,理由見 §4.2 的 refresh token 輪替問題。

---

## 5. 曲目識別與比對(Resolver)

同步的成敗全繫於此。這是整個專案技術風險最高的部分。

### 5.1 三層識別策略

```
Layer 1 — ISRC(候選產生器,不是鍵)
   Spotify: track.external_ids.isrc      (2026-03 已回復 ✅)
   Apple:   attributes.isrc
   同一 ISRC 可回多首:Apple 的 filter[isrc] 明文可回多筆;Spotify 同一 ISRC
   也可能對到單曲 / 專輯 / 合輯版本 → 消歧序:專輯名相同 > 時長差最小 > 較早發行
   正規化:大寫、去連字號、非 12 碼視為缺失
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
| 一首錄音多個 ISRC | 重發、remaster、地區版各有 ISRC | 建 ISRC alias set,任一命中即視為同一錄音。**P3 不解**:`cid` 是決定性 ID(§6.2,`i:<正規化 ISRC>`),alias set 無法收斂成同一個 cid,跨 provider 會產生兩筆 canonical track;收斂是 P4 resolver 的工作 |
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
3. **Drive `tracks.json`**(扁平命名,§6.3;cid → metadata 與 `{ provider: provider_id }` mapping)— 跨裝置共用解析結果,新裝置首次同步幾乎零 API 呼叫

第 3 點很重要:它讓「換一台電腦」不會重跑幾千次 API。

---

## 6. 同步引擎

### 6.1 心智模型:Git

| Git | capy-music |
|---|---|
| `origin` | Google Drive appDataFolder |
| working tree | 各平台上的實際播放清單 |
| `git fetch`(origin → 本機) | `capy pl pull` 內部的 FETCH 步驟(Drive → 本機,§6.5 步驟 1),**不另立命令** |
| `git commit` + `git push`(working tree → origin) | `capy pl pull`(**平台 → canonical → Drive**,方向依 §9 P4) |
| `git checkout`(origin → working tree) | `capy pl push`(canonical → 平台) |
| 3-way merge base | 各裝置自己的 `dev__<device_id>.json` 內 `base[pid][provider]` 上次同步快照(§6.3) |

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
    { "iid": "01J8Y...", "cid": "i:TWA472400123", "rank": "a0", "added_at": 1756500000 },
    { "iid": "01J8Z...", "cid": "p:apple:i.abc123", "rank": "a1", "added_at": 1756500100 },
    { "iid": "01J90...", "cid": "i:TWA472400123", "rank": "a2", "added_at": 1756500200 }   // 同曲第二次出現,合法(決策 13)
  ],
  "links": {
    "spotify": "37i9dQZF1DX...",
    "apple":   "p.LV0PXNvClXm"
  }
}

// canonical track
{
  "cid": "i:TWA472400123",        // 決定性 ID,見下
  "isrc": ["TWA472400123"],       // alias set(P3 不用它收斂 cid,見 §5.2)
  "title": "派對動物",
  "artists": ["五月天"],
  "album": "自傳",
  "duration_ms": 227000,
  "mappings": {                   // cid → 各平台 id;§7 mappings 表就是這個的鏡像,P3 不帶 confidence/pinned
    "spotify": "6rqhFg...",
    "apple":   "i.abc123"
  },
  "conflicts": [                  // 同 ISRC 但 title/duration 不符的觀測;P3 只記錄不裁決(見下)
    { "provider": "spotify", "provider_id": "6rqhFg...", "title": "派對動物 (Live)", "duration_ms": 252000 }
  ]
}
```

**排序用 fractional index(`rank` 欄位)而非整數位置。** 兩台裝置同時在中間插入時,整數位置一定衝突;字典序 rank 可以無衝突地在 `"a0"` 和 `"a1"` 之間生出 `"a0V"`。參考 `rocicorp/fractional-indexing` 演算法。

**`iid` 是 playlist item 的鍵,`cid` 只是屬性(附錄 C 決策 13)。** 每個 item 有自己的 ULID `iid`,同一首歌在同一個清單裡可以出現多次,不去重——Drive 上的副本是 source of truth,現在去重等於備份永久少掉資訊。

**`cid` 是決定性 ID,不是 ULID。** 有 ISRC → `i:<正規化 ISRC>`(大寫、去連字號、非 12 碼視為缺失);沒有 → `p:<provider>:<provider_id>`。cid 是 Drive 檔案裡的鍵,不能隨「當時觀測到什麼」而變:**不做「同 ISRC 但 metadata 不符就退回 `p:`」的防呆**——那會讓裝置 A(只看到一首)與裝置 B(看到衝突)算出不同的 cid。偵測到衝突時照樣用 `i:` 當鍵,把衝突事實寫進 Drive `tracks.json` 該 cid 的 `conflicts[]`(上方 JSON:另一個 provider 的 `provider_id` 與觀測到的 `title` / `duration_ms`)並在 stderr 印一行警告;**不落 SQLite**(§7,db 只存 Drive 上有的東西)。真正的 review queue 是 P4 的事(§5.3),這裡只把事實記在 source of truth 上——髒 ISRC 會讓兩首不同的歌塌進同一個 cid,只印 stderr 太弱。上傳曲 / 純 library 曲沒有 ISRC 也沒有 catalog id,保留成 `p:` 形式的 provider-only track,**絕不可當成已刪除**。

### 6.3 Drive appData 佈局

```
appDataFolder/                       # 扁平,不建子資料夾(見下)
├── manifest.json                    # schema_version, devices[], last_compaction
├── tracks.json                      # cid → 曲目 metadata + { provider: provider_id } mapping + conflicts[](§6.2)
├── pl__<pid>.json                   # canonical playlist(§6.2:name/desc/links + items[])
├── pl__<pid>.json
├── dev__<device_id>.json            # ⭐ 每台裝置只寫自己的檔;含 base[pid][provider] = { snapshot, observed_at }
└── dev__<device_id>.json
```

**扁平檔名 + `appProperties`,不建巢狀資料夾。** Drive **不強制同資料夾內檔名唯一**:v0.5 畫的 `playlists/<pid>/` 巢狀樹在兩台裝置同時 resolve-or-create 時會生出兩個同名資料夾。改為所有檔案平放在 appDataFolder,`appProperties` 放 `kind`(manifest / tracks / playlist / device)、`pid`、`device_id`,用 `files.list` 的 `q` 過濾;`fields` 一定要明列(預設只回四個欄位),`nextPageToken` 一律迴圈。同名檔多份時取 `modifiedTime` 最新者並印警告,不清理(merge 對重複檔無害)。每個檔案頂層有 `schema_version`,讀時忽略未知欄位,版本高於 binary 支援即拒寫。

**⭐ per-device 檔(`dev__<device_id>.json`)是核心設計。**
Drive 沒有 atomic compare-and-swap(v3 已移除 `etag`,`files.update` 沒有任何 precondition 參數),也沒有真正的 append(update 是整檔覆寫)。如果所有裝置寫同一個檔,一定會靜默 last-write-wins。**每台裝置只寫自己的檔 → 這個檔的寫入永不衝突。** `base` 因此從共享檔移進各裝置自己的 `dev__<device_id>.json`,形狀 `base[pid][provider] = { snapshot, observed_at }`,讀取時合併取 `observed_at` 最大者(LWW register)。

**誠實記下的取捨:** `manifest.json` / `tracks.json` / `pl__<pid>.json` 仍是共享檔,兩台裝置同時寫會 last-write-wins;`version` 欄位只能**事後**偵測 lost update,不能防止。P3 以單裝置為主,接受這個風險;**P5 前必須重審**。真正做到無衝突的只有 `dev__<device_id>.json`。

**op log 與 snapshot 的佈局待 P5 重新設計。** v0.5 的 `ops/<device_id>.jsonl`、`snapshot.json` 與 compaction lease(`manifest.json` 的 `compaction_lease`,過期即可搶佔)在扁平化後**路徑未定**;每台裝置只寫自己的 log 檔、讀取時載入所有 device log 依 HLC 排序 replay 的原則不變(§6.4)。P3 不產生也不上傳 op log(附錄 C 決策 10)。

**`export` 只輸出到 stdout,不回存 Drive。** v0.5 的 `export/latest.json` 已刪:逃生口的價值在於「不依賴 Drive」,把它存回 Drive 沒有意義。`capy export` 直接輸出 Drive 檔的合併形式(不發明第三種 JSON 形狀),見 §6.6 與附錄 A。

### 6.4 操作紀錄格式

> ⚠️ **P5 重新設計;以下為 v0.5 草案。** 扁平化(§6.3)後 op log 與 snapshot 的 Drive 路徑未定;op 定址要改用 `iid`(決策 13,`cid` 不再是 item 的鍵);HLC、tombstone、compaction 的原則保留。P3 不產生也不上傳 op log(附錄 C 決策 10)。

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
   ├─ Drive: 讀 manifest + tracks.json + pl__*.json + 所有 dev__*.json(§6.3;op log 與 snapshot 待 P5,§6.4)
   └─ replay → canonical 狀態 C(P3 沒有 op log,C = 直接載入的 pl__* 狀態)

2. OBSERVE
   └─ 對每個已連結的 provider,讀取平台實際狀態 L
       (Spotify: GET /playlists/{id}/items,注意分頁)

3. DERIVE
   └─ 平台不會給你 ops,只給你 state。
       ops_local = diff(base[pid][provider], L)
       → 轉成 op 追加到本裝置的 op log(佈局待 P5,§6.4)

4. MERGE
   └─ C' = replay(all ops, 依 HLC 排序)
       衝突規則:
         add  vs add  同 iid   → 去重,取較早的 rank(同 cid 不同 iid 是兩筆合法項目,決策 13,不去重)
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
   ├─ 寫回本裝置 dev__<device_id>.json 的 base[pid][provider] = 新的平台狀態
   ├─ 上傳本裝置的 op log(ops/<device_id>.jsonl;扁平化後路徑待 P5,§6.4)
   └─ 更新 manifest
```

**`--dry-run` 必須是一等公民。** 這種工具最可怕的失敗是靜默刪掉使用者幾百首歌。預設對「刪除 > 10 首」的操作要求確認。

### 6.6 安全網

| 機制 | 說明 |
|---|---|
| Dry-run 預設引導 | 首次 `sync` 自動先跑 `--dry-run` 並要求確認 |
| 刪除閾值 | 單次 `pl pull` 或 `pl push` 刪除 >10 首或 >30% 時中止並要求 `--force`(P3 會刪曲目的路徑是 `pl pull --force`,附錄 A;閾值是「任何刪除路徑都要過 dry-run + 閾值」這條硬約束的落點,不限 push) |
| 快照備份 | 每次 pull 的 COMMIT(§6.5 步驟 6)把該平台狀態存進本裝置 `dev__<device_id>.json` 的 `base[pid][provider]`(§6.3)。`capy pl restore` 從它回滾的語意隨扁平化改變(base 不再是共享檔),**P5 再定** |
| Export 逃生口 | `capy export` 輸出完整 JSON 到 stdout(Drive 檔的合併形式),不依賴 Drive、不回存 Drive;Drive 空 / 404 時的反向路徑是 `capy drive init --from-local`(只重新上傳本機 cache,永不對非空 cache 做 hydrate-empty) |

---

## 7. 本機儲存(SQLite)

db 位置 = `config.Dir()/state.db`:macOS `~/Library/Application Support/capy-music/state.db`;Windows `%AppData%\capy-music\state.db`;`CAPY_CONFIG_DIR` 覆寫整個設定目錄,db 一併跟著走

```sql
CREATE TABLE tracks (
  cid TEXT PRIMARY KEY, title TEXT, artists TEXT, album TEXT,
  duration_ms INTEGER, updated_at INTEGER
);
CREATE TABLE isrcs (cid TEXT, isrc TEXT, PRIMARY KEY (cid, isrc));
CREATE TABLE mappings (
  cid TEXT, provider TEXT, provider_id TEXT,
  PRIMARY KEY (cid, provider)
);
CREATE TABLE playlists (pid TEXT PRIMARY KEY, name TEXT, description TEXT, updated_at INTEGER);
CREATE TABLE playlist_items (pid TEXT, iid TEXT, cid TEXT, rank TEXT, added_at INTEGER, PRIMARY KEY (pid, iid));
CREATE TABLE playlist_links (pid TEXT, provider TEXT, provider_id TEXT, PRIMARY KEY (pid, provider));
CREATE TABLE sync_state (provider TEXT, pid TEXT, base_hash TEXT, last_sync_at INTEGER, PRIMARY KEY (provider, pid));
CREATE TABLE resolution_cache (cid TEXT, provider TEXT, provider_id TEXT, found INTEGER, expires_at INTEGER, PRIMARY KEY (cid, provider));
```

Migration:`PRAGMA user_version` 不符就**整檔丟棄重建**(從 Drive hydrate),不寫 ALTER。

SQLite 是 **cache**,不是 source of truth。刪掉整個 db 應該能從 Drive 完整重建。這是設計約束,要寫測試驗證。

**`mappings` 在 P3 只有 `(cid, provider, provider_id)`,沒有 `confidence` / `pinned` / `updated_at`。** 這三欄在 Drive 上沒有來源——`tracks.json` 的 mapping 就是 `{ provider: provider_id }`(§5.4、§6.2、§6.3)——留著就等於「刪 db 可從 Drive 完整重建」這條硬約束在規格層面先天不成立。理由與下一段拒收 ops / review 兩張表**完全相同**:db 裡只能存 Drive 上有的東西。而且 P3 沒有 resolver(§9,resolver 與 review queue 是 P4),沒有任何程式碼會產生信心度或釘選。**P4 做 resolver 時要把這三欄同時加回 `tracks.json` 與本表**(§5.1 Layer 3 的人工釘選是使用者意圖,必須跟著 Drive 走才「永久沿用」);migration policy 是整檔丟棄重建,加欄位不用寫 ALTER,成本為零。只加表不加 Drive 形狀,同一個洞就會在 P4 原樣重現。

**P3 不建 `ops` 離線佇列與 review 佇列兩張表(v0.5 有)。** 理由要讀準:已上傳的 op log **在 Drive 上**(§6.3 的 `ops/<device_id>.jsonl`,§6.5 步驟 6 會上傳它),真正不在 Drive 的只有 `synced = 0` 的離線佇列與使用者尚未裁決的 review 項目——這兩樣一旦進 db,「刪 db 可從 Drive 完整重建」就不成立。P3 為了讓這條硬約束成立而不建這兩張表,**不是否決 §6.4 的 op log 設計**;P5 要做離線佇列時,得連同它的重建策略一起回頭加表。db 裡只能存 Drive 上有的東西(canonical 鏡像、各裝置 `base` 副本)與純快取(`resolution_cache`);不存憑證、不存 provider 原始 JSON。

---

## 8. ToS 合規檢查表

| 條款 | 我們的做法 |
|---|---|
| MusicKit JS 不可與其他 JS 重組 | v0.5 起不再載入 MusicKit JS(橋接頁已移除),此條無對象 |
| **Apple 網頁播放器 token 供第三方使用** | **Apple 未授權。** 這是灰色地帶中最深的一項:token 由使用者自己複製、程式只指導不擷取、指令內強制揭露、風險由使用者自負(附錄 C 決策 8)。隱藏的 `--auto` 是明確切出的例外,未文件化 |
| 不可對 Apple Music 存取收費 | 永久免費 + 開源。這也正是專案初衷。 |
| MusicKit Content 不可與其他內容 synchronized | ⚠️ 灰色地帶。保守做法:**只同步使用者自建的 library playlist**;不觸碰 Apple 編輯清單/目錄;不把 Apple cover art 用在播放脈絡以外 |
| 不可下載/修改 MusicKit Content | 我們只碰 metadata,不碰音訊 |
| Spotify Developer Policy | BYO Client ID,使用者自負其 app 的合規 |
| Google API Services User Data Policy | 只用非敏感 scope,資料只存使用者自己的 appDataFolder,**我們的伺服器不存任何使用者資料** |

本專案沒有任何伺服器端元件(v0.5 起 Worker 已移除):**沒有任何使用者資料或憑證經過我們**。隱私權政策(Google basic verification 需要的 URL)掛 taislife.work,內容就是這一句。

---

## 8.5 營運成本與風險

> 前提:免費對外、無盈利模式、憑證全面 BYO(Spotify 自建 app;Apple 自抓 web token)。v0.5 起 Apple Developer Program 與 Cloudflare Worker 均不再需要——本節依此重算,v0.4 的原算式見附錄 D。

### 8.5.1 固定成本

| 項目 | 年費 | 說明 |
|---|---|---|
| Apple Developer Program | **$0** | v0.5 起不需要(web token BYO)。若要做 macOS notarization 才需 $99,那是分發議題,與 Apple Music 功能脫鉤 |
| Google Cloud / Drive API | $0 | Drive API 採 quota units 計量(2026-09-03 查證):每專案每分鐘 1,000,000 quota units、每使用者每專案每分鐘 325,000;`files.get` 5、`files.list` 100、`files.update` 50、下載 200(`files.create` 文件未單獨列出);單檔上傳上限 5 TB。每專案每日超過 400,000,000 quota units 的用量「planned to incur charges to your Google Cloud billing account later in 2026」,Google 承諾變更前至少 90 天預告——個人用量遠低於此,實際 $0,但已不是「永遠免費」,列入附錄 B。`drive.appdata` 為非敏感 scope,不需 OAuth 審查 |
| GitHub(repo + Actions + Releases) | $0 | 公開 repo 的標準 GitHub-hosted runner 免費且無分鐘上限,**含 macOS runner**(Free 方案最多 5 個並行 macOS job) |
| 網域 | $0 | 沿用既有網域 `taislife.work`(隱私權政策頁),邊際成本零 |
| Spotify Developer | $0 | BYO Client ID,成本轉嫁使用者 |
| **合計** | **$0 / 年** | 唯一可能的支出是 macOS notarization 的 $99,可獨立決定 |

**分發備註:** macOS binary 不跳 Gatekeeper 警告需要 Developer ID 憑證與 notarization,即 $99/年的 Apple Developer Program。v0.4 時它與 Apple Music 功能是同一筆錢;v0.5 起兩者脫鉤,notarization 成為純分發決策(先走 Homebrew + 使用者自行允許)。

**測試用訂閱(已具備 ✅):** Apple Music 訂閱與 Spotify Premium 均有效(2026-09-01 確認),邊際成本為零。

### 8.5.2 為什麼 Apple 端是 $0 —— 以及它的真正代價

每位使用者貼的 developer token 都是 **Apple 網頁播放器全球共用的同一顆**。我們沒有自己的 Team ID、沒有配額、沒有簽章成本——也沒有任何槓桿:Apple 沒有動機為第三方留餘量,改變網頁播放器認證機制時不會通知我們。這不是成本問題,是 §8.5.4 R-6 的存在性風險。v0.4 的 Worker/KV 設計理由保留於附錄 D。

### 8.5.3 成本 vs. 規模

| 使用者規模 | Apple | Google | GitHub | 年成本 |
|---|---|---|---|---|
| 1 ~ 50,000+ | $0 | $0 | $0 | **$0** |

成本與人數完全無關:每一項隨人數成長的資源都在使用者自己那邊(Spotify 用其 app、Apple 用 Apple 自家 token、Drive 用其空間)。代價換成了 R-6 的存在性風險——見下節。

### 8.5.4 非金錢風險(比成本重要)

| # | 風險 | 說明 | 緩解 |
|---|---|---|---|
| **R-1** | ~~Apple API 配額集中於單一 Team ID~~ **→ v0.5 反轉:所有使用者搭在 Apple 自家 web token 的配額上** | 我們不再有 Team ID 可被限流,但也完全沒有槓桿;Apple 對自家網頁播放器 token 的限流政策即是我們的天花板 | 重度快取(§5.4);各使用者的 MUT 分散了 per-user 端點的壓力 |
| **R-2** | ~~developer token 可被擷取~~ **→ v0.5 反轉:我們沒有 token 可外洩,但 100% 依賴 Apple 不改機制** | 沒有停權風險(不是我們的 Team)、沒有 $99 可損失;風險全部轉成 R-6 | — |
| **R-3** | ~~持續付費的承諾綁定~~ **→ v0.5 消失** | 沒有任何持續支出,維護者可隨時停損而不影響既有使用者 | — |
| **R-4** | **平台政策變動頻率高** | Spotify 半年內改兩次:2026-02 使用者上限 25→5 且強制 owner 持有 Premium;2026-07 Client ID 上限 1→25。Apple 亦有非預告的 MusicKit 更新紀錄 | 附錄 B 的監控清單;CI 週期性 API 斷言 |
| **R-5** | **支援負擔** | BYO 流程(Spotify app、Apple web token)必然產生大量「設定不起來」的 issue,最常見是 redirect URI 寫成 `localhost`、Apple token 貼到過期的 | `capy doctor` 列為一等公民;錯誤訊息直接指出正確值;Apple 登入三段驗證各給不同訊息 |
| **R-6** | **Apple 單方面改變網頁播放器認證即全體失效,無預警、無替代** | 這是 v0.5 用 $99/年 換來的存在性風險:token 格式、取得位置、amp-api 行為都可能一夜改變;社群工具多年未被封鎖不構成保證 | **唯一緩解是把重登成本壓到像重新登入一樣低**(§4.3(b) 只更新 developer token 的路徑);附錄 B 監控;若 Apple 封鎖,恢復官方路徑的快照在 `3649b7b` |

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
| **Apple 改用使用者自抓 web token(v0.5)** | 固定成本 $99 → $0、免會籍、免 Worker;代價是 R-6 與 ToS 灰色地帶(決策 8) |
| **Google brand verification 去做** | 免費、2–3 個工作天。非敏感 scope 本不強制驗證,但完成後同意畫面才會顯示 app 名稱與 logo。需以 Search Console 驗證網域所有權,並在同一網域(taislife.work)掛隱私權政策 |
| **`capy doctor` 列為 P1 而非 nice-to-have** | 直接消化約一半的支援 issue(見 R-5)。已定案進 P1(對外發佈定位) |

---

## 9. 開發階段

### P0 — 驗證與骨架(先做,因為有兩個未知數可能推翻設計)

| # | 任務 | 為什麼是 P0 |
|---|---|---|
| **P0-1** | 用 curl 打通 Apple developer token → `GET /v1/catalog/tw/search`,確認 ISRC 有回傳 | ISRC 是整個 resolver 的基礎 |
| **P0-2** | ⚠️ **驗證 Apple Music API 能否從 library playlist 移除/重排曲目** | 若不行,Apple 端 push 只能用 rebuild 策略,要改 §6.5 |
| **P0-3** | ~~驗證 MusicKit JS 在 `http://127.0.0.1:{隨機port}` 能否成功 `authorize()`~~ | ~~若不行,要改成固定 port 或本地 HTTPS~~(v0.5 作廢,見 2026-09-03 排程註記) |
| **P0-4** | 量測 Spotify Development Mode 的實際 rate limit | 決定同步的併發度與退避策略 |
| P0-5 | 專案骨架:cobra + config + keychain + loopback server | — |

**P0-2 和 P0-3 是架構風險點,建議會籍生效第一天就打 curl 驗證,不要等到寫完再發現。**

> ~~**排程註記(2026-09-01):** Apple Developer 會籍申請中。P0-1/P0-2/P0-3 全部需要 developer token → **gate 在會籍生效**;P0-4/P0-5 與 P1 先行。就算 P0-2 驗出「不能移除/重排」,§6.5 已備好 rebuild fallback,不會推翻架構。~~(v0.5 作廢,見 2026-09-03 排程註記)

### P1 — Spotify 全鏈路
`auth login spotify`(BYO client ID,huh 表單精靈)→ `search` → `play/pause/next` via Connect → `pl list/show` → **`doctor`**(定案進 P1,見 §8.5.7)

### P2 — Apple 全鏈路
`auth login apple`(web token BYO 精靈,§4.3)→ Apple Music API search → macOS `osascript` 播放

> **排程註記(2026-09-03):** v0.4 版 P2(`.p8` + Worker + MusicKit 橋接)於 PR #5 完成後,依附錄 C 決策 8 改為 web token BYO(`feat/apple-webtoken`)。真實驗收**不再 gate 於會籍**:拿到 web token 即可跑 search / pl / 播放驗收;P0-3(MusicKit 動態 port)隨橋接移除而作廢;P0-1(ISRC)與 macOS 播放機制 A/B 決勝仍待真跑。

### P3 — Google + Drive
`auth login google` → appDataFolder 讀寫 → manifest / device 註冊基礎設施。**範圍依附錄 C 決策 10 擴為 P3 + P4 前半**:canonical model → `pl pull` → SQLite cache 與重建 → `export` / `drive init --from-local` 逃生口;snapshot 佈局待 P5(§6.4)

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
3. ~~**Worker 網域** — `capy.taislife.work`~~(v0.5 作廢,Worker 已移除;見附錄 C 決策 8 與本節第 9 項)
4. **平台** — macOS + Windows 第一天支援;**Linux 非目標**
5. **TUI** — 第一天進場(charm 全家桶);非 TTY 純文字輸出為鐵則(見 §2)
6. **發佈定位** — 一開始就對外發佈;`capy doctor` 進 P1
7. **`--provider` 統一** — 不採附錄 A 的 `play --on`;所有讀/播放命令一致用 `--provider`(預設 `spotify`)
8. **Apple `pl list/show`** — 納入 P2
9. **Apple 憑證 = 使用者自抓 web token(BYO,非官方)** — 取代 `.p8`/Worker/MusicKit 橋接;預設路徑絕不自動擷取、指令內強制揭露;隱藏 `--auto` 為唯一例外(2026-09-03,附錄 C 決策 8)

### 必須寫進 CLAUDE.md 的約束

已落地至 repo 根目錄 [`CLAUDE.md`](../CLAUDE.md),**以該檔為準**(單一來源,避免兩份漂移)。

### 建議的第一個 commit

不要從 provider 開始寫。先做 **§4.1 的 loopback server + keychain 抽象 + config**,因為三個 provider 都依賴它,而且它是唯一可以在沒有任何 API 憑證的情況下寫測試的部分。

---

## 附錄 A:CLI 命令表面(草案)

```
capy auth login   <spotify|apple|google>                  # google:P3
capy auth status
capy auth logout  <provider>

capy search <query> [--provider all|spotify|apple] [--limit N]
capy play   <query|uri> [--on spotify|apple] [--device NAME]
capy pause | capy next | capy prev | capy seek <mm:ss> | capy vol <0-100>
capy now [--watch]

capy pl list
capy pl show   <name>
capy pl link   <name> <provider>:<playlist_id>            # P3;連結規則(自動配名 vs 僅認明確 link)T7 前定
capy pl unlink <name> <provider>                          # P3
capy pl diff   <name>                                     # 延後(P3 用 pl pull --dry-run 看差異)
capy pl pull   [--provider P] [--all] [--dry-run] [--yes] [--force]   # P3:平台 → canonical → Drive(§6.1);--yes 跳過確認、--force 才越過刪除閾值,兩者分開
capy pl push   [--provider P] [--all] [--dry-run] [--force]           # P5
capy pl sync   [--dry-run]                                # P5
capy pl restore <name> --provider P                       # P5(語意待定,§6.6)

capy resolve --review                                     # P4 後半
capy export                                               # P3:Drive 檔的合併形式輸出到 stdout,不回存 Drive
capy import <file.json>                                   # 延後;P3 的反向逃生口是 drive init --from-local
capy drive init --from-local                              # P3:Drive 空 / 404 時唯一允許寫入的命令,只重新上傳本機 cache
capy device list | capy device forget <device_id>         # 延後;P3 只在 manifest 註冊 device_id;forget 是唯一移除裝置檔的路徑(§6.3),尚未排程
capy db rebuild                                           # 延後;P3 的重建 = 刪 state.db 後由 hydrate 從 Drive 重建(§7,有測試)
capy doctor
```

## 附錄 B:待監控的外部變數

| 項目 | 風險 | 監控方式 |
|---|---|---|
| Spotify `external_ids` (ISRC) | 2026-02 曾被移除,2026-03 回復 | CI 每週打一次 API 斷言 ISRC 存在 |
| Spotify Development Mode 使用者上限 | 一年內從 25 → 5 | 訂閱 Spotify Developer Changelog |
| Apple 網頁播放器認證機制 | web developer token 格式/位置、`media-user-token`、amp-api 行為皆可能無預警改變(R-6) | 定期重跑 `auth login apple` 的手動流程;issue 回報即為監控 |
| Google `drive.appdata` 敏感度分類 | 目前非敏感,若改分類影響巨大 | 每季檢查 Drive API scopes 文件 |
| Drive API quota units 計費時程 | 每專案每日 >400,000,000 quota units「planned to incur charges … later in 2026」(§8.5.1),Google 承諾至少 90 天預告;個人用量遠低於門檻,但一旦開始計費,§8.5.3「成本與人數無關」就多一個條件 | 每季對照 developers.google.com/workspace/drive/api/guides/limits;訂閱 Google Workspace 開發者公告 |
| Spotify Lossless over Connect | 目前 Connect 端點只給 320k Ogg | 若開放,遙控播放的音質敘述要更新 |

## 附錄 C:定案紀錄(2026-09-01;決策 8–13 為 2026-09-03)

與維護者逐項討論後定案。若未來要推翻其中任何一項,先讀對應理由。

| # | 議題 | 定案 | 摘要理由 |
|---|---|---|---|
| 1 | 平台 | macOS + Windows;Linux 非目標 | 維護者實際裝置即此二者。§4.5 的 headless age fallback 自 v1 移除;Windows 上 Apple = 僅資料能力(播放遙控走 Spotify Connect),正是 §2 解耦設計的預期行為 |
| 2 | 訂閱/會籍 | 雙訂閱有效;Apple Developer 會籍申請中 | P0-1/P0-2/P0-3 gate 於會籍生效;P0-4/P0-5 與 P1 先行 |
| 3 | 發佈定位 | 一開始就對外發佈 | Worker 照原計畫進 P2、`doctor` 升進 P1、BYO onboarding 用 huh 表單打磨 |
| 4 | TUI | 第一天進場(cobra + charm 全家桶) | 「酷炫 CLI」為維護者點名的重點項目;以「非 TTY 必可純文字輸出」鐵則保護 §8.5.6 的自動化價值 |
| 5 | 語言 | Go(Rust 已評估未採用) | 效能平手(瓶頸在網路 API,兩者皆原生 binary);TUI 生態 charm 全家桶對「酷炫」目標明顯佔優(bubbletea v2 + lipgloss + bubbles + huh vs 較底層的 ratatui);交叉編譯與個人維護成本 Go 較低;Rust 型別系統對同步引擎的小幅優勢改以測試補償 |
| 6 | 命名 | binary `capy`,不做 `cm` 別名 | 一個名字就夠,使用者自行 alias;少一個安裝器要管的 shim |
| 7 | Worker 網域 | `capy.taislife.work` | 沿用既有網域;隱私權政策與 Google brand verification 掛同一網域;endpoint 為 binary 預設值但可被 config 覆寫(自架/BYO `.p8` 場景)。**v0.5 隨決策 8 作廢** |
| 8 | Apple 憑證(2026-09-03) | 使用者自抓 web token BYO;`.p8`/Worker/MusicKit 橋接全刪(快照 `3649b7b`);隱藏 `--auto` | 會籍審核未決 + 專案開源憑證全面 BYO:官方路徑需維護者代持 `.p8` 與付費,與 BYO 精神相悖。代價是 R-6(完全依賴 Apple 不改機制)與 ToS 灰色地帶,以指令內強制揭露、預設絕不自動擷取(只指導)承擔。`--auto`(AppleScript 驅動已登入的 Safari / Chrome 分頁,不讀 cookie DB,見 §4.3(d))應維護者要求做為未文件化 opt-in、開發者自負,不改變預設路徑鐵則;維護者在充分知悉技術限制(developer token 是標頭非 cookie)與法律面差異後定案 |
| 9 | Google client 歸屬(2026-09-03) | binary 內建維護者的 Google Desktop client,但 client ID / secret **絕不 commit 進 repo**,以 `-ldflags -X` 在發行時注入(新增 GoReleaser 發行流程);`--client-id` / `--client-secret` 一律可覆寫;沒有內建值(`go install`)自動落回 BYO 精靈;BYO 的 secret 只進 keychain `google.client_secret`,client ID 進 config;程式對兩條路徑一視同仁 | Google 政策明文禁止把 client credential commit 進公開 repo。與 Spotify(dev mode 5 人上限)/ Apple(會籍與代持 `.p8`)不同:三個 scope 全是 non-sensitive,不需 verification、無 100 人上限,內建 client 不代持任何**使用者**憑證。**這是放寬 CLAUDE.md「憑證只進 keychain」硬約束**(內建 secret 會編進 release binary,`strings capy` 讀得到),只涵蓋 app 自身識別。RFC 8252 §8.5 明講:散佈給使用者的原生 app,其內嵌 secret 任何使用者都能從自己那份取出,本來就不具機密性——這是已知且被接受的風險模型,不是本專案的疏漏。放寬也**只能**停在這裡:使用者憑證沒有這個性質(拿到的是別人的東西,不是自己那份的複製品),所以這個例外不得被援引到任何使用者憑證上。維護者風險:client 6 個月無人使用會被自動刪除(前 30 天通知);client 被停用時所有 release binary 使用者同時壞,救援路徑 = 改用自己的 client |
| 10 | 下一階段範圍(2026-09-03) | P3 + P4 前半:Google 登入 → Drive appdata 讀寫 → manifest / device 註冊 → canonical model → `pl pull`(平台 → canonical → Drive)→ SQLite cache 與重建 → `export` / `drive init --from-local` 逃生口。不做:fuzzy resolver 與 review queue(P4 後半)、op log / HLC / 三方合併 / `pl push`(P5) | 原 P3 只有 manifest / snapshot 基礎設施,但 canonical playlist 要 `pl pull` 才誕生:P3 沒東西可寫、使用者拿到的價值是零、「刪 db 可從 Drive 重建」的硬約束也沒東西可測 |
| 11 | issue #3:token 並行 refresh(2026-09-03) | 兩案並用:(a) access token + expiry 也存進 keychain(與 refresh token 同一筆 JSON 記錄),**加上** (b) refresh 路徑加跨程序檔案鎖,鎖內重讀 keychain 雙重檢查後才 refresh。Google 的 token source 從第一天照同一形狀寫,不留兩套 | 只存 refresh token 時,兩個並行 `capy` 各自 refresh,Spotify 輪替後的 RT 會互相踢掉;Google 雖不輪替,但 access token 換了本來就要寫回,寫回路徑相同 |
| 13 | playlist item 保真(2026-09-03) | item 以自己的 ULID `iid` 為鍵、`cid` 為屬性,同一首歌可在同一清單重複出現,不去重;`cid` 改為決定性 ID(`i:<正規化 ISRC>` / `p:<provider>:<id>`)。§6.2 `items[]` 加 `iid`,§7 `playlist_items` 鍵改 `(pid, iid)` | Drive 上的副本是 source of truth,現在去重等於備份永久少掉資訊,而且之後要改模型很貴。(決策 12「該 session 只產計畫不寫程式」為流程事項,不列) |

## 附錄 D:已移除的官方路徑(v0.4 原文,供恢復時參考)

> 2026-09-03 依決策 8 移除。完整程式碼快照:commit `3649b7b`(`worker/`、`internal/auth/apple/devtoken*.go`、`internal/auth/apple/authorize.go` 與 `web/authorize.html`、`.github/workflows/ci.yml` 的 worker job、config 的 `install_id`/`apple_token_endpoint`)。若日後取得 Apple Developer 會籍要恢復,從該 commit 還原這些檔案,並把 §4.3 換回下文。

### D.1(原 §4.3)Apple Music:Developer Token 派發 + MusicKit 橋接

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
- Worker 做 per-`install_id` rate limit(原生 Rate Limiting binding,見 §8.5.2);不做 per-IP —— 文件建議避 IP,且 client 自選 install_id 的弱點依 R-2 接受
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


### D.2(原 §8.5.2)為什麼 Worker 是 $0 —— 以及必須避開的 KV 陷阱


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

