# capy-music — 跨平台音樂 CLI 架構規劃

> 專案名稱 `capy-music`,binary `capy`
> 文件版本 v0.9 — 2026-09-08(P6 計畫:`local` 綁裝置 provider(§1.4、§3 `CapDeviceBound` / `DeviceScoped`、附錄 C 決策 33–37);計畫在 docs/superpowers/plans/2026-09-08-p6-local.md)
> 前版 v0.8 — 2026-09-08(P5 對齊:不建 op log / HLC(§6.4 改寫、附錄 C 決策 26),DERIVE 規則 4′(§6.5.1),`pl push` / `pl sync` 契約(§6.5.2、附錄 A),共享檔版本守衛(§6.3、§6.6),Apple 寫入 gate 與 append-only fallback(決策 30);計畫在 docs/superpowers/plans/2026-09-08-p5-sync.md);v0.7 — 2026-09-08(P4 後半對齊:§5.1 觀測 cid 三段式身分規則、mapping 物件化與 schema 2(T2b 的 merged 再跳 3)、§5.3 review queue 契約、§6.5.1 base 只經 tombstone 重導、§7 schema v5、附錄 C 決策 19–25;計畫在 docs/superpowers/plans/2026-09-08-p4-resolver.md;v0.6 — 2026-09-03(P3 對齊:`pl pull` 方向、Drive 扁平佈局、`iid` 與決定性 `cid`、SQLite 移除兩張表、quota units、§4.5 憑證表、附錄 C 決策 9–13;v0.5 — 2026-09-03:Apple 改為使用者自抓 web token BYO,`.p8`/Worker/MusicKit 橋接移除,見附錄 C 決策 8 與附錄 D;v0.4 定案版:語言 Go、macOS+Windows、TUI 第一天進場、對外發佈;v0.3 專案定名 capy-music;v0.2 新增 §8.5 營運成本與風險)
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
| `GET /artists/{id}/top-tracks` | 開發模式 app **一律 403**(2026-09-07 實測:`market=from_token`、`TW`、不帶、`country=` 全部一樣) | 「藝人熱門歌曲」改用 `GET /search?q=artist:"<name>"&type=track`(依熱門度排序)當備案,程式在 403 時自動退回;Spotify-owned / 他人的編輯清單 `items` 也拿不到(2026-09-07 實測 29 個清單 9 個回「平台不提供此內容」),`pl pull` 只涵蓋 app 讀得到的清單 |

### 1.2 Apple Music

| 項目 | 現況 | 影響 |
|---|---|---|
| Developer Token | 官方:ES256 JWT,需 Apple Developer Program($99/年)的 MusicKit `.p8`。**本專案(v0.5 起)改由使用者自己從 Apple 網頁播放器複製 Apple 的 web developer token**(非官方) | 免會籍、$0;代價是完全依賴 Apple 不改網頁播放器機制(§8.5.4 R-6) |
| Music User Token | 官方只能經 MusicKit `authorize()` 取得;**本專案改由使用者從網頁播放器的 `media-user-token` 標頭/cookie 複製** | 不需 MusicKit JS 橋接;與 developer token 一起貼入 `auth login apple` |
| MUT 生命週期 | 長期有效,**無 refresh token**,過期只能重跑授權 | 需偵測 401 並提示重新授權 |
| Lossless / Hi-Res | MusicKit JS **不支援**;iOS/tvOS MusicKit 遵循使用者設定但無程式控制 | 高音質只能靠 macOS 的 Music.app |
| Library playlist 寫入 | 建立/新增曲目可行;**移除與重排能力待驗證** | ⚠️ 見 §9 P0-2;P5 決策 30:gate 在維護者的寫入探測(計畫 R-8),預期 append-only,不採 rebuild |
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

### 1.4 local(本機曲庫,P6;計畫 docs/superpowers/plans/2026-09-08-p6-local.md)

| 項目 | 現況 | 影響 |
|---|---|---|
| 資料來源 | `local_root` 下的 `*.m3u8` / `*.m3u` 是清單,`library.json` 是曲庫(title / artists / album / duration_ms / isrc);不讀音訊 tag、不加相依 | 幾乎沒有 ISRC → 跟其他平台的對應靠 resolver Layer 2,`Search` 必做 |
| **綁裝置** | 曲庫只在一台機器上,但 `pl__.Links` 是共享檔(一個 provider 一格) | 決策 33:playlist id 帶 device id(track id 不帶,cid 才撐得過重灌)、SPI 加 `CapDeviceBound` + `DeviceScoped.Foreign`;別台裝置的 pull / push / sync 對它只跳過,不算 gone、不 unlink;**一個 canonical 清單同時只連一台裝置的 M3U**,`pl link` 撞到別台的 link 就接管(重灌後 device_id 變了也靠這條接回來) |
| id | 正規化相對路徑(forward slash、NFC);改名 / 搬家 = remove + add | 升級路徑 = 內容 hash(`capy local scan`) |
| 寫入 | 整檔改寫 + 原子 rename,`#EXTINF` 與註解會被丟掉;全部 ops 都支援 | 與 Apple 的 append-only 成對照 |
| 錯誤族 | 只有 NotFound / 權限 / IO / JSON 壞;沒有 auth、rate limit、restricted | 每個 n/a 都是 SPI 假設的發現(計畫 §2) |

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

const ( // 順序 = 位元位置,與 internal/provider/provider.go 一致;新能力一律加在尾端(舊位元不動)
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
    CapArtistSearch    // UX 計畫 T3
    CapPlayPlaylist    // PlayRequest.PlaylistID
    CapPlayQueue       // Play 把 TrackIDs 全排進佇列
    CapPlaylistRename  // P5 T1 加(bit 14;CapSearch 是 bit 0);Spotify 有
    CapDeviceBound     // P6 決策 33(bit 15):id 只在本裝置有意義;實作者同時實作 DeviceScoped
)

// DeviceScoped(P6 決策 33):綁裝置的 provider 告訴 CLI 某個 id 是不是別台裝置的——是的話 pull / push / sync 一律跳過,
// 不算 gone、不算 refused、不動 base;pl link 只能連本機的。
type DeviceScoped interface {
    Foreign(id string) bool
}

type Provider interface {
    ID() string                     // "spotify" | "apple" | "local"
    DisplayName() string
    Caps() Capability
    Health(ctx context.Context) error
}

type Searcher interface {
    Search(ctx context.Context, q Query) ([]Track, error)
}

// P4 後半(2026-09-08,T1):能力介面,不塞進 Searcher;實作者宣告 CapISRCLookup
type ISRCLookup interface {
    LookupISRC(ctx context.Context, isrc string) ([]Track, error)   // 正規化 ISRC;多筆時由 resolver 消歧(§5.1)
}
type TrackGetter interface {
    GetTrack(ctx context.Context, id string) (Track, error)
}

type PlaylistReader interface {
    ListPlaylists(ctx context.Context) ([]PlaylistRef, error)
    GetPlaylistItems(ctx context.Context, id string) ([]Track, error)
}

// PlaylistOp(2026-09-08 P5 T1 已實作,internal/provider/provider.go):位置語意是依序套用、每個 Pos / From 指的是
// 前面 ops 套完後的狀態;move 是「先從 From 拿出來、再插到 Pos」。純函式 ApplyPlaylistOps(current, ops) 是這個語意的
// 唯一定義,provider 實作與 push 的計畫測試都用它。
type PlaylistOp struct {
    Kind       string // OpAdd | OpRemove | OpMove | OpRename
    ProviderID string // add:要插入的曲目 id(Apple 只收 catalog id);remove:選填,填了會核對
    Pos, From  int    // add:插入位置;remove:位置;move:From → Pos
    Name       string // rename
}

type PlaylistWriter interface {
    // ApplyOps 一次套用一批操作,由 provider 決定用什麼端點實現;current 是呼叫端剛觀測到的 provider id 序列,
    // ops 相對於它(provider 不再讀一次:少一次 API、少一個競態窗口)。
    // Kind 不支援的 op 跳過、支援的照做,回傳跳過的那些(呼叫端列成 manual);平台真的失敗才回 err。
    // P5 不做 rebuild fallback(決策 30)。CreatePlaylist 待有需要再加(push 只寫已連結的清單)。
    ApplyOps(ctx context.Context, playlistID string, current []string, ops []PlaylistOp) (skipped []PlaylistOp, err error)
    // Pushable:這個 id 能不能被 add 進清單(Spotify local file 的 spotify:local:… uri、Apple library-only 的 id 不能)。純函式。
    // push 算變更集時用它:有 mapping 但推不出去的 item 列 skip,而不是送出去被整批拒收(T4;PR #33 review)。
    Pushable(id string) bool
}

type PlaybackController interface {
    Devices(ctx context.Context) ([]Device, error)
    State(ctx context.Context) (*PlaybackState, error)
    Play(ctx context.Context, req PlayRequest) error
    Pause(ctx context.Context) error
    Next(ctx context.Context) error
    Prev(ctx context.Context) error
    // 2026-09-09 實作:Spotify 是 PUT /me/player/seek?position_ms= 與 /me/player/volume?volume_percent=
    // (手機與部分喇叭回 403 VOLUME_CONTROL_DISALLOW,訊息要講「這個裝置不給調」而不是授權過期);
    // Apple 是 osascript 的 player position(秒)與 sound volume。超過長度的 seek 不先攔(各平台行為不一致)。
    Seek(ctx context.Context, posMS int) error
    SetVolume(ctx context.Context, pct int) error
}

// 延後、尚未排程:Enqueue(ctx context.Context, uri string) error —— 不在目前的 PlaybackController 裡。
// P1 計畫把它與 Seek / SetVolume 一起延後,後兩者已於 2026-09-09 補上;Enqueue 連 CLI 命令都還沒設計
// (附錄 A 沒有 queue),真要做時先補附錄 A。

type Track struct {
    ProviderID  string   // provider 內部 ID
    ISRC        string   // 可能為空
    Title       string
    Artists     []string
    Album       string
    DurationMS  int
    Explicit    bool
    Unpushable  bool     // P5 T1 已加:Spotify local file(is_local;id 是 null 所以 ProviderID 拿 uri)、Apple library-only 曲目——API 加不回去,push 時只配對不新增(計畫 Q22)
    Raw         json.RawMessage
}
```

**`ApplyOps` 而非細顆粒方法**是刻意的:讓 provider 自己決定「逐條 API 呼叫」還是「整批 replace」。Spotify 有 `PUT /playlists/{id}/items` 可整批取代(P5 決策 28:任何 add / remove / move 都走整批取代,前 100 首 `PUT`(空序列 = 清空)、其後每批 100 `POST`,`rename` 走 `PUT /playlists/{id}` 且在 items 之前;T1 已實作,**寫入端點沿用讀取的 `/items` 路徑,真帳號尚未驗證**——若 404 改成 `/tracks` 是一個常數的事);Apple 預期只能 append(決策 30:remove / move / rename 回 `ErrCapability`,不重建)。這個抽象讓兩者都塞得下。

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

**實作上信心度是 0–100 的整數**(`canon.Encode` 要逐位元決定性,整數沒有浮點格式化歧義):觀測 100、ISRC 反查 95、fuzzy 60–84 進 review / ≥85 自動寫入、人工 100。fuzzy 分數以 `floor` 取整;**時長差 >3 s 的候選上限 84**(時長項歸零時 title + artist 滿分剛好 85,會讓 extended mix / radio edit 自動寫入;時長是標題沒帶關鍵字時唯一的訊號)。

**觀測 cid 的身分規則(2026-09-08,P4 後半,附錄 C 決策 19)。** 跨 ISRC 的 mapping(fuzzy 配對、人工釘選)一旦存在,純觀測的 cid(§6.2 公式)會與 canonical 分裂:cid `i:A` 釘了 `apple:X`,Apple 回報 X 的 ISRC 是 B,下次 pull 會算出 `i:B`,同一首歌兩筆。所以平台現況 L 觀測到的 (provider, id, isrc) 依序決定 cid:(1) `tracks.json` 已有 mapping `(provider, id)` → 該 cid;(2) 正規化 ISRC 在某個 track 的 alias set → 該 cid(多命中取字典序最小並警告);(3) 否則 §6.2 公式;(4) 結果是 `merged` 表裡的敗者 → 沿 tombstone 追到勝者。**alias set 只在人工操作(review accept / `resolve pin` / 合併)時成長且要過所有權檢查(候選的 ISRC 已屬另一 cid → 進 review),`Observe` 與自動 mapping 永不改動它**,由此保證每個 ISRC 至多屬於一個 cid。**base 快照的 cid 只經 `merged` tombstone 重導**——不在表裡就保留原 cid,絕不落公式(base 沒有 ISRC,落公式會算出不存在的 `p:` cid、移除計數歸零、永遠刪不掉)、也不查 mapping(人把 id 從 X 改釘給 Y 時,base 的 X 若被重導成 Y,X 就留成孤兒;保留 X 才會在下次 pull「移除 X、新增 Y」)。給定 `tracks.json` 全部決定性。代價:item 的 cid 依賴觀測當下的 `tracks.json`,共享檔 LWW 的新面向進 P5 重審。

**mapping 的形狀與優先序(決策 20)。** `mappings[provider] = { id, confidence, pinned, source, updated_at }`,`source ∈ observed | isrc | fuzzy | review`;`pinned: true` + `id: ""` = 使用者裁定「這個平台沒有這首」(不可得也是意圖,跟著 Drive 走才不會每次重問)。優先序 **pinned > observed > isrc / fuzzy**:觀測到真實 id 會覆寫非 pinned 的自動 mapping;自動程序永不覆寫 pinned。`updated_at` 只在 `(id, confidence, pinned, source)` 其中之一真的改變時才更新,等價比較不看它——否則每次 pull 都會因位元組不同而重傳整份 `tracks.json`。

**cid 合併(決策 21)。** 「同一錄音、兩個 cid」只在人工 accept / `resolve pin` 時合併;≥85 的自動寫入**絕不合併**——最佳候選 `(provider, id)` 已屬於另一個 cid 時,不論分數一律進 review 並列出那個 cid 的證據。勝者 = 字典序較小的 cid(兩台裝置各自合併同一對也同解);敗者的 items 改寫成勝者、alias set / mappings / `conflicts[]` 聯集、敗者從 `tracks` 移除但在 `tracks.json` 的 `merged` 表留 tombstone `{敗者: 勝者}`(永久保留;鏈在寫入時壓平、讀取沿鏈追)。tombstone 讓這個多檔改寫可重入:COMMIT 先傳 `tracks.json` 再逐檔傳 `pl__*.json`、沒有交易,中途失敗會留下指向已移除 cid 的 item;FETCH 時把 item cid 經 `merged` 重導(`Hydrate` 拿到的已是重導後的狀態,不另做一次),改到的清單下次 COMMIT 自然重傳。兩個 cid 對同一 provider 持有**不同** id 是「不是同一錄音」的證據:人工仍合併時,同 provider 的 mapping 取優先序高的那個(pinned > observed > 其他,同級勝者留),**輸的那邊**的 id 進 `conflicts[]`,不靜默丟棄——敗者是使用者釘的、勝者只是觀測到的,釘選留下(決策 20 的「自動程序永不覆寫 pinned」在合併路徑也成立)。`merged` 是無法從別處重算的狀態,所以 `SchemaVersion` 2 → 3(schema 2 的 binary 會靜默丟掉它再上傳;規則:加不可再生的欄位就跳版,純快取欄位才不跳)。**cid 除合併外永不改寫**,`p:` 開頭的 cid 對到帶 ISRC 的 mapping 之後也維持 `p:`。

### 5.2 已知陷阱(必須寫進測試案例)

| 陷阱 | 表現 | 對策 |
|---|---|---|
| 一首錄音多個 ISRC | 重發、remaster、地區版各有 ISRC | 建 ISRC alias set,任一命中即視為同一錄音。**P3 不解**:`cid` 是決定性 ID(§6.2,`i:<正規化 ISRC>`),alias set 無法收斂成同一個 cid,跨 provider 會產生兩筆 canonical track;收斂是 P4 resolver 的工作。**P4 後半(決策 21)**:收斂 = cid 合併,只由人裁決,自動寫入絕不合併;合併後 alias set 聯集,之後觀測到任一 ISRC 都經 §5.1 身分規則對回同一 cid |
| Apple 部分曲目缺 ISRC | `attributes.isrc` 為空 | 自動降級到 Layer 2 |
| YouTube Music 完全無 ISRC | — | 未來接入時只能 Layer 2 + 人工 |
| Live / Remix / Cover 誤配 | 標題相近、時長相近 | 標題含 live/remix/acoustic/cover 關鍵字時提高門檻 |
| 中文簡繁 / 藝名別名 | 「五月天」vs「Mayday」 | 維護 artist alias 表,可從兩邊 API 的 artist 物件互相學習 |
| 區域下架 | 某平台查得到但不可播 | `available_markets` 已被 Spotify 移除 → 只能靠播放時的錯誤回報 |

### 5.3 Review Queue 與 `capy resolve`(2026-09-08 定案,附錄 C 決策 22)

`capy resolve [<清單>] [--provider P] [--dry-run] [--yes]`:預設掃**全部**已連結的清單(resolve 只增不刪,不像 `pl pull` 需要 `--all`);對每個 (清單, provider) 找出 items 裡缺該 provider mapping 的 cid → Layer 1 → Layer 2;**≥85 自動寫入**(走 `pl pull` 同一套 `withCanonical`:pull.lock、Drive 不完整的閘、Drive 先 SQLite 後),其餘印成 review 佇列。`pl pull` 本身不做 resolve(API 成本與關注點分離),只在結尾提示「N 首尚未對應到 <provider>,跑 capy resolve」。

**exit code**:`0` 無事可寫或已寫入——**review 佇列有東西仍是 0**(它是 TSV 列,不是失敗;cron 的 `resolve --yes` 不能因為永遠有幾首解不開而永遠報錯)、`1` 錯誤、`2` 有待寫入的自動 mapping 但沒有確認(`--dry-run`、非 TTY 沒給 `--yes`、終端機取消)。非 TTY 的 TSV 欄位:`action cid provider provider_id confidence source title artists reason`,`action ∈ map | review | conflict`。

review 佇列的來源:(a) 找不到候選或最佳分數 <85;(b) 最佳候選已屬另一 cid(決策 21);(c) `conflicts[]` 非空且該 provider 的 mapping 不是 pinned(髒 ISRC,§6.2 那句「只印 stderr 太弱」的落點)。

信心度 < 85 的配對**不自動寫入**,進 review queue,`capy resolve --review` 在 TTY 逐筆裁決(非 TTY → 印佇列 TSV、exit 2:明確要求人工裁決卻沒有終端機):

```
$ capy resolve --review

  [1/7]  canonical: 派對動物 — 五月天  (3:47)
         spotify  ✓ 0.97  派對動物 - 五月天
         apple    ? 0.71  派對動物 (Live) - 五月天  (4:12)
         [a]ccept  [s]kip  [m]anual search  [n]ot available
```

決策寫入 `mappings`(`pinned: true`、信心度 100、`source: review`),之後永久沿用:accept = 釘選候選;not available = 釘選空 id;manual search = 用平台搜尋挑一首再釘;skip = 這次不裁決(下次還會出現);conflicts 的 keep = 把現有 mapping 釘住(人確認過)。腳本用 `capy resolve pin <cid> <provider>:<id|none>`。釘選或 accept 對到已屬另一 cid 的 id → 依決策 21 合併(TTY 要確認;非 TTY 要 `--yes`)。

### 5.4 解析快取

同步時逐首查 API 成本極高(Spotify 已移除批次端點!)。三層快取:

1. **Drive `tracks.json`**(扁平命名,§6.3;cid → metadata 與 mapping 物件)— 跨裝置共用解析結果,新裝置首次同步幾乎零 API 呼叫。**P3 已有。**
2. **SQLite `mappings` 表**就是它的本機鏡像,即本機正向快取;不另建 `resolution_cache`。**P3 已有。**
3. **Negative cache 延後**(2026-09-08 決策 24):每次 `resolve` 對尚未解開的 cid 重查(Layer 1 一次 + Layer 2 一次);以真帳號 ~2,500 首、ISRC 覆蓋 99% 估算,穩態每次數十次 API,可接受。觸發條件:單次 `resolve` 超過 200 次 API 呼叫、或有人把 `resolve --yes` 放進 cron;屆時加純快取表(不進 `Dump`,重建只是慢一點,不違反 §7)。

第 1 點很重要:它讓「換一台電腦」不會重跑幾千次 API。

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
  "mappings": {                   // cid → 各平台 id;§7 mappings 表是它的鏡像。P4 後半(決策 20,schema 2)改為物件;讀時相容 P3 的字串舊形(視為 observed / 100 / 不 pinned),寫出永遠是物件
    "spotify": { "id": "6rqhFg...", "confidence": 100, "pinned": false, "source": "observed", "updated_at": 1756500000 },
    "apple":   { "id": "i.abc123",  "confidence": 95,  "pinned": false, "source": "isrc",     "updated_at": 1756600000 }
    // 「這個平台沒有這首」= { "id": "", "confidence": 100, "pinned": true, "source": "review", ... }
  },
  "conflicts": [                  // 同 ISRC 但 title/duration 不符的觀測;P3 只記錄不裁決(見下)
    { "provider": "spotify", "provider_id": "6rqhFg...", "title": "派對動物 (Live)", "duration_ms": 252000 }
  ]
}
```

**排序用 fractional index(`rank` 欄位)而非整數位置。** 兩台裝置同時在中間插入時,整數位置一定衝突;字典序 rank 可以無衝突地在 `"a0"` 和 `"a1"` 之間生出 `"a0V"`。參考 `rocicorp/fractional-indexing` 演算法。

**`iid` 是 playlist item 的鍵,`cid` 只是屬性(附錄 C 決策 13)。** 每個 item 有自己的 ULID `iid`,同一首歌在同一個清單裡可以出現多次,不去重——Drive 上的副本是 source of truth,現在去重等於備份永久少掉資訊。

**`cid` 是決定性 ID,不是 ULID。** 有 ISRC → `i:<正規化 ISRC>`(大寫、去連字號、非 12 碼視為缺失);沒有 → `p:<provider>:<provider_id>`。cid 是 Drive 檔案裡的鍵,不能隨「當時觀測到什麼」而變:**不做「同 ISRC 但 metadata 不符就退回 `p:`」的防呆**——那會讓裝置 A(只看到一首)與裝置 B(看到衝突)算出不同的 cid。偵測到衝突時照樣用 `i:` 當鍵,把衝突事實寫進 Drive `tracks.json` 該 cid 的 `conflicts[]`(上方 JSON:另一個 provider 的 `provider_id` 與觀測到的 `title` / `duration_ms`)並在 stderr 印一行警告;**不落 SQLite**(§7,db 只存 Drive 上有的東西)。真正的 review queue 是 P4 的事(§5.3),這裡只把事實記在 source of truth 上——髒 ISRC 會讓兩首不同的歌塌進同一個 cid,只印 stderr 太弱。上傳曲 / 純 library 曲沒有 ISRC 也沒有 catalog id,保留成 `p:` 形式的 provider-only track,**絕不可當成已刪除**。**cid 一旦建立,除合併(§5.1 決策 21)外永不改寫**;`p:` 開頭的 cid 之後對到帶 ISRC 的 mapping 也維持 `p:`。P4 起,觀測到的 (provider, id, isrc) 對到哪個 cid 依 §5.1 的三段式身分規則(先看 mapping、再看 alias set、最後才是上面的公式)。

### 6.3 Drive appData 佈局

```
appDataFolder/                       # 扁平,不建子資料夾(見下)
├── manifest.json                    # schema_version, devices[], playlists[](pid;2026-09-08 T8 加,pull 的閘用它偵測部分遺失)
├── tracks.json                      # cid → 曲目 metadata + mapping 物件 + conflicts[](§6.2);merged{敗者 cid → 勝者 cid}(P4 決策 21 的 tombstone)
├── pl__<pid>.json                   # canonical playlist(§6.2:name/desc/links + items[])
├── pl__<pid>.json
├── dev__<device_id>.json            # ⭐ 每台裝置只寫自己的檔;含 base[pid][provider] = { snapshot, observed_at }
└── dev__<device_id>.json
```

**扁平檔名 + `appProperties`,不建巢狀資料夾。** Drive **不強制同資料夾內檔名唯一**:v0.5 畫的 `playlists/<pid>/` 巢狀樹在兩台裝置同時 resolve-or-create 時會生出兩個同名資料夾。改為所有檔案平放在 appDataFolder,`appProperties` 放 `kind`(manifest / tracks / playlist / device)、`pid`、`device_id`,用 `files.list` 的 `q` 過濾;`fields` 一定要明列(預設只回四個欄位),`nextPageToken` 一律迴圈。同名檔多份時取 `modifiedTime` 最新者並印警告,不清理(merge 對重複檔無害)。每個檔案頂層有 `schema_version`,讀時忽略未知欄位,版本高於 binary 支援即拒寫。

**⭐ per-device 檔(`dev__<device_id>.json`)是核心設計。**
Drive 沒有 atomic compare-and-swap(v3 已移除 `etag`,`files.update` 沒有任何 precondition 參數),也沒有真正的 append(update 是整檔覆寫)。如果所有裝置寫同一個檔,一定會靜默 last-write-wins。**每台裝置只寫自己的檔 → 這個檔的寫入永不衝突。** `base` 因此從共享檔移進各裝置自己的 `dev__<device_id>.json`,形狀 `base[pid][provider] = { snapshot, observed_at }`,`snapshot = { id, name, items, cids }`(id = 被觀測的平台清單 id,base 只對目前連結的那個平台清單有效,unlink 後改連別的清單,舊 base 不算數,2026-09-08 T8 加;items = 依平台順序的 provider id;cids = 觀測當時依 §6.2 算出的 cid、與 items 對齊、不隨 mapping 變;2026-09-08 T7 加),讀取時合併取 `observed_at` 最大者(LWW register)。

**共享檔的取捨與版本守衛(2026-09-08 P5 重審,附錄 C 決策 29)。** `manifest.json` / `tracks.json` / `pl__<pid>.json` 仍是共享檔,兩台裝置同時寫會 last-write-wins,Drive 沒有 CAS 可以防止。危險的不是互蓋本身,是**互蓋之後對方的變更再也不會被 derive**:A 在 FETCH 與 COMMIT 之間被 B 蓋掉 `pl__`,A 的 `dev__` base 卻已前進,A 下次 pull 看平台與 base 一致、C 卻少了 B 那份。所以 COMMIT 前**再跑一次 FETCH 用的 `files.list`**(`fields` 已含 `version`),對 **FETCH 時讀過的每個檔**(不只這次要上傳的:沒變的檔也參與了決策,例如 item cid 的 tombstone 重導用的是當下的 `tracks.json`)比對記下的 `version`,以及同名檔的份數(別台裝置在我們 FETCH 之後 Create 的算變動);任一不同 → 一個檔都不傳、本機不動、exit 1、訊息叫使用者重跑(重跑 = 重新 FETCH 到對方的結果再 derive 一次,delta 自然疊上去)。殘餘窗口只剩上傳序列本身(tracks → pl__ → dev__ → manifest,秒級)。守衛住在 `withCanonical` 的 COMMIT,`pl pull` / `resolve` / `pl push` / `pl sync` 一體適用。註:每次 COMMIT 都會 `manifest.Touch`,所以兩台裝置**任何**重疊的命令都會互相觸發守衛,即使動的是不同清單——預期行為,重跑就好;守衛不依賴這個巧合(比對的是讀過的每個檔)。真正做到無衝突的仍只有 `dev__<device_id>.json`。

**沒有 op log、沒有 snapshot 檔(2026-09-08 P5 定案,附錄 C 決策 26)。** v0.5 的 `ops/<device_id>.jsonl`、`snapshot.json`、compaction lease 全部不做:per-device base 已經是「本裝置上次跟這個平台對齊時看到的狀態」,DERIVE 對最新的 C 算 delta 就是三方合併(§6.4);而且沒有本機編輯命令,op log 沒有東西可記。

**`export` 只輸出到 stdout,不回存 Drive。** v0.5 的 `export/latest.json` 已刪:逃生口的價值在於「不依賴 Drive」,把它存回 Drive 沒有意義。`capy export` 直接輸出 Drive 檔的合併形式(不發明第三種 JSON 形狀),見 §6.6 與附錄 A。

### 6.4 變更的來源與合併語意(2026-09-08 P5 定案;取代 v0.5 的 op log 草案)

> v0.5 在這裡畫的是 op log(add / remove / move / rename 各帶 HLC、tombstone 保留 30 天、compaction)。P5 定案**不建 op log、不做 HLC**(附錄 C 決策 26)。本節寫新模型;v0.5 原文在 git 歷史(v0.7 之前的版本)。

**變更只有一種來源:觀測。** 沒有本機編輯命令,每一筆 canonical 變更都是「平台現況 L 跟本裝置的 base 不同」,由 DERIVE(§6.5.1)算出來套在**最新的** C 上。觀測是可重做的:這次沒 COMMIT 成,下次 pull 再算一次就是了——這就是為什麼不需要把 op 記下來,也不需要離線佇列(pull / push 都要 Drive,離線就是失敗、下次再跑)。

**三方合併 = per-device base + 對稱的計數規則。** 每台裝置對每個 (清單, 平台) 都有自己的 base(§6.3),DERIVE 用「某 cid 在 base 出現 b 次、在 L 出現 l 次、在 C 配得到幾次」決定:b > l 才移除(規則 5)、l > b 才新增(規則 4′,P5 加;決策 27)。兩台裝置各從不同平台 pull,只要 COMMIT 序列化(版本守衛,§6.3),delta 自然疊加:A 先 commit C+ΔS,B FETCH 到它再疊 ΔA。v0.5 的 tombstone 要擋的「A 刪掉、B 的舊快照加回去」在這裡由規則 4′ 擋住——B 的 base 裡有那首,L 也有,l = b,不算新增;唯一沒涵蓋的是 **B 對那個平台沒有 base**(新裝置、剛連結)的 bootstrap 情境,接受並文件化(計畫 Q15;任一裝置 sync 一次即關窗口,而且復活會以 `add` 列出現在輸出裡)。

**沒有 HLC 可比的衝突:後 COMMIT 者勝。** 改名(規則 7:平台名 ≠ base 名才改 C)與順序(規則 6′:平台順序 ≠ base 順序才採平台順序)都只在「那個平台真的改了」時才動 C,所以只有兩邊真的都改了才會互蓋;兩台裝置各自從兩個平台拉到不同結果時,版本守衛保證後者是看著前者的結果做決定的,不是盲蓋。

**如果將來加 `pl add / remove / move`:** 它們直接改 C,走同一個 `withCanonical`(FETCH → 改 → 版本守衛 → COMMIT),仍然不需要 op log;要做離線編輯佇列時才回頭看 §7 的重建策略(那時 db 裡會出現 Drive 上沒有的東西)。

### 6.5 同步流程

```
capy pl sync 的一輪(pl pull 只有 1–3 + 6;pl push 只有 1–2 + 4–6——前提二比快照不跑 DERIVE;2026-09-08 P5 定案):

1. FETCH
   └─ Drive: 讀 manifest + tracks.json + pl__*.json + 所有 dev__*.json(§6.3),記下每個檔的 version
       → canonical 狀態 C(item cid 經 merged tombstone 重導);本裝置的 base[pid][provider]

2. OBSERVE
   └─ 對每個已連結的 provider,讀取平台實際狀態 L(Spotify: GET /playlists/{id}/items,注意分頁),
       每首依 §5.1 身分規則算 cid,Observe 寫回 tracks(mapping / conflicts)

3. DERIVE(§6.5.1;pull)
   └─ 平台不會給你 ops,只給你 state:C' = C + diff(base[pid][provider], L)
       三方語意:移除只在 b > l、新增只在 l > b(規則 4′ / 5);沒有 base = bootstrap(全新增、永不移除)

4. PROJECT + PUSH(§6.5.2;push)
   ├─ 前提:base 存在,且 L 的快照 = base 的快照(平台沒有未 pull 的變更;比快照,不是比 Derive)——否則 exit 3
   ├─ ops = diff(L 的 cid 序列, C' 的 cid 序列)(對齊鍵是 cid;LCS 配對;remove / add / move / rename;add 缺 mapping 的 item 列 skip)
   ├─ 刪除閾值(決策 18)、--dry-run、確認
   └─ provider.ApplyOps(ops);provider 不支援的 op 回 ErrCapability → 列成 manual(Apple 的 remove / move,決策 30)

5. VERIFY(ApplyOps 成功或失敗都做)
   └─ 重讀 L′,與 want 不同只警告;base[pid][provider] := L′ 的快照(Observe 同一條路;base = 上次看到的平台狀態,不是上次成功同步的狀態)

6. COMMIT(版本守衛 → 上傳 → SQLite)
   ├─ 再 list 一次比對每個要傳的檔的 version(§6.3);不符 → 零上傳、exit 1、重跑
   ├─ 上傳位元組有變的檔:tracks → pl__ → 本裝置 dev__ → manifest
   └─ Hydrate 本機 state.db
```

**`--dry-run` 必須是一等公民。** 這種工具最可怕的失敗是靜默刪掉使用者幾百首歌。pull / push / sync 都先印完整變更集(非 TTY 是 TSV)再確認,`--dry-run` 只印、不碰平台也不碰 Drive;會刪曲目的路徑一律過決策 18 的閾值。

#### 6.5.1 P3 的 DERIVE 規則(2026-09-08 定案,T7;`internal/canon/derive.go`)

純函式、不碰 IO、不改動傳入的物件。輸入:canonical 清單 C(items 依 `(rank, iid)`)、`tracks.json` 的 cid → track(只讀)、本裝置上次觀測 base = `base[pid][provider]`(可能沒有)、平台現況 L(名稱 + 依平台順序的曲目,含 ISRC)。

1. **對齊鍵是 cid,不是 provider id。** L 的每首依 §5.1 的身分規則算 cid(P4 起:先看 `tracks` 的 mapping、再看 alias set、最後才是 §6.2 公式;**base 快照裡的 cid 在計數前只經 `merged` tombstone 重導**(不查 mapping、不落公式,都不是就保留快照原本的 cid),否則合併後緊接的平台刪除永遠刪不掉、P5 push 會把它加回去——決策 19);base 的快照同時存 provider id 與**觀測當時算出的 cid**(§6.3),移除計數直接用快照裡的 cid,不經 mapping 反查——cid 由 (id, ISRC) 決定、不隨 mapping 變,所以平台把曲目重新連結成另一個版本(X → Y,同 ISRC)是**零變更**,而之後再刪除仍然刪得掉(只靠 id 反查時 mapping 還是 X、查不到 Y,會永遠刪不掉)。
2. **配對由 LCS 決定,不是「第 n 次出現」。** 以 cid 序列(C 依 rank、L 依位置,重複照算)求最長共同子序列,對上的 item 留在原位;L 裡沒對上的出現,依 L 順序拿同 cid 剩下的 C item(rank 序最前者)配對並搬動;沒有剩下的才是新增。盲配「第 n 次出現」會在重複曲目換序時這輪搬這份、下輪搬那份,永遠多報一筆 move(2026-09-08 PR #19 review 抓到)。配對不看 mapping——別的 provider 建的 cid 在這個 provider 第一次被觀測到時要能配上(見第 3 點),否則每次 pull 都會多一份。
3. **觀測寫回 tracks**:L 的每首都 `Observe`:`tracks` 沒有這個 cid → 新建 track(含這個 provider 的 mapping);有 cid 但沒這個 provider 的 mapping → 加 mapping(metadata 不符則記 `conflicts[]`,§6.2);已有 mapping → pinned 或 observed 不動(mapping 不抖動);P4 起非 pinned 的 `isrc` / `fuzzy` mapping 會被觀測到的真實 id 覆寫(決策 20 的優先序 pinned > observed > isrc / fuzzy)。
4. **新增(P5 起是 4′,決策 27;2026-09-08 T3 已實作)**:L 裡配對不到的出現 → add,插在它在 L 的前一個元素所配對的 C 位置之後;`iid` = 新 ULID、`added_at` = 現在。C 全空(首次 pull)時 rank 用 `Ranks(n)` 均分,其餘用 `RankBetween`。**base 存在時只有超出 base 計數的部分才是新增**:某 cid 在 L 出現 l 次、在 base 出現 b 次,l > b 才 add、至多 add l − b 個,取 L 順序最後的那幾個(平台新增通常在尾端;鏡像規則 5 的「由後往前移除」);其餘配不到的出現是「平台還沒跟上 C 的移除」,留給 push。沒有 base 全部 add(bootstrap)。**L 尾端的新增(後面沒有配對上的元素)一律接在 C 的尾端**:平台沒重排而 C 順序不同時(6′),使用者在平台尾端加的歌才會在 C 也在尾端,而不是插在「L 前一個元素」在 C 的位置之後(那可能是 C 的開頭);中間的新增仍插在前一個元素所配對的 C 位置之後。沒有這條,使用者在 Spotify 刪的歌會被 Apple 的下一次 pull 加回 C,刪除意圖丟失。**P5 T3 與 push(T4)相鄰出貨**:只有 pull 的世界裡這條會讓 C 與平台脫節而沒有東西去對齊。
5. **移除**:**只在 base 存在時**發生:某 cid 在 base 出現 b 次、在 L 出現 l 次、b > l,才把 C 裡配對不到的該 cid 出現**依 rank 由後往前**移除至多 b − l 個。沒有 base(首次 pull)永不移除。這條同時保護「在 Apple 加入、經 ISRC 對到 Spotify id、但從沒 push 到 Spotify」的曲目:它不在 Spotify 的 base 裡,所以不會被讀成「Spotify 刪了它」。
6. **換序(P5 起是 6′,決策 27;2026-09-08 T3 已實作,`canon.Reordered`)**:配對成功的 item 依 L 的順序排列;LCS 對上的留在原位,其餘配對的 item 搬動並只給它們新 rank(`RankBetween` 於最終整體順序的鄰居之間,鄰居可以是沒配對的 item),其餘 rank 不動;LCS 多解時偏好 C 中較前者。沒配對的 item 留在原 rank。**base 存在時只有平台真的重排了才這樣做**:L 的 id 序列拿掉 base 沒有的、與 base 的序列拿掉 L 沒有的,兩者相同 = 平台沒重排 → 配對上的 item 保留 C 的 rank、不報 move(新增 / 移除照 4′ / 5);兩邊都重排 → 後 pull 者勝。沒有這條,Spotify 的重排會被 Apple 的下一次 pull 翻回舊順序再 push 回 Spotify。沒有 base 維持採 L 順序。LCS 用 Hunt–Szymanski(對配對點求 LIS):時間 O((n + r) log n)、記憶體 O(n + r),萬首清單(Spotify 單一清單上限)不會像 O(n·m) 的 DP 吃掉 855 MB。P3 沒有 HLC,順序以最後一次 pull 的 provider 為準。
7. **改名**:base 存在且 `L.name ≠ base.name` → `C.name := L.name`(C 已經是那個名字就不算變更);首次 pull 不改名,C 的名字由建立者決定。
8. **清單消失**:平台回 404 / 不在清單列表 → 回 gone、零 item 變更;T8 依 Q6(B)自動 unlink 並警告。開發模式讀不到的清單(Spotify 編輯清單)從不會被連結,pull 直接跳過並說明,**不是 gone**。
9. **輸出**:變更集(add / remove / move / rename / unlink,欄位對齊 T8 的 TSV:`action pos cid provider_id title artists reason`)、套用後的 C、新建或更新的 tracks、新的 base 快照(= 平台清單 id、L 的 provider id 原文與觀測當時的 cid;§6.3)、變更前該 provider **可見**的 item 數(cid 有該 provider mapping 的數量,Q3 的閾值分母)。
10. **無變更時 C 逐位元不變**(`updated_at` 不動),T8 的 exit 0「無變更」才不會說謊。rank 資料髒掉(兩個 item 同 rank 又需要插入)→ 回錯,不靜默重排。

#### 6.5.2 P5 的 PUSH 規則(2026-09-08 定案,附錄 C 決策 28;純函式 `canon.PushPlan` T3 實作於 `internal/canon/project.go`,CLI T4 實作於 `internal/cli/push.go`)

1. **前提一:base 存在。** 本裝置對這個 (清單, 平台) 沒 pull 過 → exit 3、零寫入,訊息「先 `capy pl pull`」。不然 diff(L, project(C)) 會把平台上所有不在 C 的曲目刪光——「首次 pull 永不移除」的 push 版。
2. **前提二:平台沒有未 pull 的變更。** L 的快照(清單 id、名稱、依平台順序的 provider id)與 base 的快照直接比對,不同 → exit 3,訊息指向 `pl pull` 或 `pl sync`。**只比 provider id 序列、不比 cid**(T4 定):釘選與合併墓碑會讓同一個平台 id 的觀測 cid 變,那不是平台變更——`push`(列 skip)→ `resolve pin` → `push` 是正常流程,比 cid 會每次都被擋。**不能用 Derive(base, L) 是否為空判**:Derive 是 L 對 C 的差,push 前 C 通常剛被另一個平台改過(那正是要 push 的東西),會把正常的 push 擋掉。push 蓋掉使用者剛在平台改的東西不可接受;`pl sync` 先 pull 後 push,兩個前提由建構保證。
3. **對齊鍵是 cid,不是 provider id**(同規則 1):want = C 的 items 依 rank 的 cid 序列,live = L 經 §5.1 身分規則算出的 cid 序列(OBSERVE 已算)。LCS 配對(重複照算,與規則 2 同一套);**配對上的 item 不動**——「pinned 成不可得、但曲目此刻就在平台上」與「Apple 沒有 catalog 對應的 library-only 曲目(`i.` id)」都會配上、不會被當成 remove(以 provider id 序列 diff 投影會把被 skip 的 item 排進 remove,PR #30 review)。
4. **變更集**:配不到的 L 出現 → `remove`(provider id 取 L 原文);配不到的 C item 要 `tracks[cid].mappings[provider].id` 才能 `add`,沒有(無 mapping、pinned 不可得、Apple 只有 library id)→ 列成 `skip`(reason 指向 `capy resolve`)並從 want 拿掉,不阻擋其他曲目——純 `skip` 只剩「C 有、平台沒有、又推不出去」;配對上但順序不同 → `move`;`rename`(C.name ≠ L.name 且 provider 支援);位置語意是**依序套用、每個位置指的是前面 ops 套完後的狀態**。TSV 欄位同 pull(`action pos cid provider_id title artists reason`)再加 `skip` / `manual`。不可推的項目(Spotify local file、library-only)在 L 裡幾乎一定也在 C(只能經這個平台進 C),配對後不動;Spotify 的整批取代加不回 local file:**含 local file 的 Spotify 清單在最小操作完成前拒絕 push**(exit 3,訊息列出那幾首;計畫 Q19 / Q22)。
5. **安全網**:`remove` 數過決策 18 的閾值(分母 = L 的長度)→ exit 3、`--force` 才越過且只能配單一清單;`--dry-run` 只印、有變更 exit 2、不碰平台不碰 Drive;非 TTY 沒 `--yes` → exit 2;TTY 確認。
6. **套用**:`provider.ApplyOps(ctx, id, current, ops)`(`current` = 剛觀測到的 L);provider 不支援的 op 回在 `skipped` → 那些列標成 `manual`(Apple 的 remove / move / rename,決策 30),其餘照做。**平台端沒有 CAS,整批取代等於放棄偵測**:讀到 L 之後、寫入之前別台裝置(或手機)在同一清單加的歌會被直接覆蓋——跟 Drive 那側的版本守衛不對稱(PR #31 review)。縮小窗口的做法(T4 做):確認之後、**每個目標自己的 `ApplyOps` 之前**再讀一次 L 與 `current` 比對(不是全部先讀再全部寫——那會把第 2 個目標的窗口再撐開前面目標的寫入時間),不一致就**跳過那一份**(零寫入、stderr 說明,`--all` / `sync` 的其他清單照推),收尾以 exit 3 結束、訊息指向 pull;TTY 確認畫面等人按鍵的那段時間正是最大的窗口。殘餘窗口 = 讀與 PUT 之間。exit code 的優先序:任一份 `ApplyOps` 失敗(exit 1)蓋過「確認期間變了」(exit 3)。這次重讀只比 provider id 序列:確認畫面期間在手機上改的**名稱**不會被偵測,push 若帶 rename 會蓋掉它(下一次 pull 也看不出來,因為 base 已記成新名字)。分批寫到一半失敗回 `*provider.PartialWriteError`(已寫 / 目標首數、是否已改名),push 要把它印成「清單現在是半截的、重跑補回」,不是一般錯誤。Spotify:任何 add / remove / move → 整批取代(`PUT /playlists/{id}/items` 前 100 首 + `POST …?position=` 其後每批 100),`rename` → `PUT /playlists/{id}`;只有自己或協作的清單可寫,403 → 友善訊息。
7. **驗證與前進 base——ApplyOps 成功或失敗都做**:重讀 L′,L′ ≠ want(平台拒收、順序被正規化、分批做到一半斷網)→ stderr 警告;base[pid][provider] := L′ 的快照(與 pull 同一條 Observe 路)。base 是「本裝置上次看到的平台狀態」,不是「上次成功同步的狀態」:分批做到一半死掉而 base 不動,下一次 pull 會把我們自己的殘局讀成使用者刪了 150 首(PR #30 review)。**`pl__<pid>.json` 不變**(items / rank / name 都不動);`tracks.json` 可能因 Observe 補 mapping / conflicts 而變。L′ 快照的名稱是**預期**的(改了就是 C 的名字,沒改就是 L 的),不再 list 一次平台;平台若正規化了名稱,下一次 push 的前提二會擋下並指向 pull,pull 之後自然校正。**重讀 L′ 本身也失敗時**(斷網往往一起把 POST 與接下來的 GET 都弄掉):base := want 的前 `written` 首(成功 = 全部、`PartialWriteError.Written`、第一個請求就失敗 = 平台沒動所以 base 不變)——不然 PUT 已落地、base 還停在 L,下一次 pull 會把自己的半截寫入讀成使用者刪了歌。
8. **COMMIT** 走 `withCanonical`;ApplyOps 失敗時**仍然 COMMIT**(base 要落地),COMMIT 完再以 exit 1 結束並講明平台改了幾批,下一次 push 自然補上沒送出去的。全部套用成功、Drive 上傳失敗 → base 沒前進,下次 pull 把剛 push 的東西當平台變更再 derive:LCS 對同 cid 配上、零變更,不會重複。§6.3 的版本守衛在 push 的 COMMIT 擋下時,「零寫入」只對 Drive 成立——push 改口為「平台已寫入 N 筆、Drive 沒動,先 pull 再 push」;半截寫入再遇 COMMIT 失敗兩件事都講,並要使用者先 `pl pull --dry-run`——那正是規則 7 要防的狀態(平台缺一截、base 又沒落地),下一次 pull 會把缺的那截列成移除(計畫 Q24)。stdout 的 `skip` 列不是變更,「動了幾筆看行數」要扣掉它。

### 6.6 安全網

| 機制 | 說明 |
|---|---|
| 變更集先於寫入 | pull / push / sync 都先印完整變更集(非 TTY 是 TSV)再確認;非 TTY 沒 `--yes` → exit 2、零寫入;`--dry-run` 只印(2026-09-08 P5:取代 v0.5「首次 sync 自動先跑 dry-run」——每一次都等於先 dry-run 過) |
| push 的兩個前提 | (清單, provider) 沒有 base(本裝置沒 pull 過)、或平台有未 pull 的變更(L 的快照 ≠ base 的快照)→ `pl push` exit 3、零寫入,`--yes` / `--force` 都不放行;出口是 `pl pull` 或 `pl sync`(§6.5.2,決策 28);含 local file 的 Spotify 清單也 exit 3(整批取代會刪光它們,計畫 Q22) |
| 共享檔版本守衛 | COMMIT 前再 list 一次,FETCH 讀過的任一檔 `version` 變了或同名多了一份 → 零上傳、本機不動、exit 1、重跑(§6.3,決策 29);pull / resolve / push / sync 一體適用;sync 撞上時平台已改、Drive 沒改,訊息講明、重跑補上 |
| 刪除閾值 | 單一 (清單, provider) 在一次 `pl pull` 或 `pl push` 要刪除 **>10 首,或 >30% 且 >3 首**(分母是該 provider 可見的曲數;Q3 採 B,2026-09-08 T8,附錄 C 決策 18)時中止並要求 `--force`(P3 會刪曲目的路徑是 `pl pull --force`,附錄 A;閾值是「任何刪除路徑都要過 dry-run + 閾值」這條硬約束的落點,不限 push;P5 的 `pl push` 走同一個 `removalBlocked`,分母是平台清單 L 的長度)。`--force` 只越過閾值,不放行「Drive 不完整」(下一列) |
| Drive 不完整的閘 | `manifest.playlists` 宣告、或本機 `state.db` 記得的檔在 Drive 取不到(全空只是特例)→ `pl pull` / `pl link` 一律 exit 3、零寫入,`--yes` / `--force` 都不放行;出口是 `capy drive init --from-local`(2026-09-08 T9 已實作:只建 Drive 缺的檔、不覆寫還在的檔(Drive 為準)、不動本機 cache、不代為上傳別台裝置的 `dev__` 檔;沒缺就零寫入;manifest 宣告但兩邊都沒有的清單補不回,訊息講明只能清空 appdata 重建)。任何檔 `schema_version` 高於 binary 支援 → exit 1、零寫入 |
| 快照備份 | 每次 pull 的 COMMIT(§6.5 步驟 6)把該平台狀態存進本裝置 `dev__<device_id>.json` 的 `base[pid][provider]`(§6.3)。`capy pl restore`:base 是 per-device 的觀測快照,「從它回滾」= 把上次看到的平台狀態 push 回去,與 `pl push` 重疊,**延後到有人要為止**(2026-09-08 P5,計畫 Q18) |
| Export 逃生口 | `capy export` 只讀本機 `state.db`(不碰 Drive、網路、keychain;用 `store.OpenReadOnly`:壞檔不刪、版本不符不改名、不建檔,`Dump` 整批在一個 read transaction 裡所以是一致快照),輸出 Drive 檔的合併形式到 stdout——鍵是檔名、值是該檔內容的縮排形式,壓回 compact 後與 Drive 上逐位元相同(2026-09-08 T9;`import` 就是它的反向);本機沒資料 exit 1、stdout 不印,找得到升版保留的 `state.db.v<n>` 就指出來。反向路徑是 `capy drive init --from-local`(上一列;同一個唯讀開法,永不寫本機;上傳順序 manifest 最後;Drive 上還在的每個檔都先檢 schema) |

---

## 7. 本機儲存(SQLite)

db 位置 = `config.Dir()/state.db`:macOS `~/Library/Application Support/capy-music/state.db`;Windows `%AppData%\capy-music\state.db`;`CAPY_CONFIG_DIR` 覆寫整個設定目錄,db 一併跟著走

```sql
-- schema v5(PRAGMA user_version = 5;2026-09-07 T6 實作、2026-09-08 T7 加 device_base.cids、T8 加 device_base.playlist_id、P4 T2a 加 mappings 的 confidence / pinned / source / updated_at(決策 20,v4)、T2b 加 merged 表(決策 21,v5);與 v0.5 草案的差異見下段)
CREATE TABLE tracks (cid TEXT PRIMARY KEY, title TEXT, artists TEXT /* JSON [] */, album TEXT, duration_ms INTEGER, conflicts TEXT /* JSON [],§6.2 */);
CREATE TABLE isrcs (cid TEXT, isrc TEXT, PRIMARY KEY (cid, isrc));
CREATE TABLE mappings (cid TEXT, provider TEXT, provider_id TEXT /* 空 + pinned = 不可得 */, confidence INTEGER /* 0–100 */, pinned INTEGER, source TEXT /* observed|isrc|fuzzy|review */, updated_at INTEGER, PRIMARY KEY (cid, provider));
CREATE TABLE merged (cid TEXT PRIMARY KEY, into_cid TEXT NOT NULL /* tracks.json 的 merged 表鏡像:合併敗者 → 勝者(P4 決策 21) */);
CREATE TABLE playlists (pid TEXT PRIMARY KEY, name TEXT, description TEXT, updated_at INTEGER);
CREATE TABLE playlist_items (pid TEXT, iid TEXT, cid TEXT, rank TEXT, added_at INTEGER, PRIMARY KEY (pid, iid));
CREATE TABLE playlist_links (pid TEXT, provider TEXT, provider_id TEXT, PRIMARY KEY (pid, provider));
CREATE TABLE devices (device_id TEXT PRIMARY KEY, name TEXT, last_seen INTEGER, registered INTEGER /* 在 manifest.devices */, has_state INTEGER /* 有 dev__<id>.json */);
CREATE TABLE device_base (device_id TEXT, pid TEXT, provider TEXT, playlist_id TEXT /* 被觀測的平台清單 id */, name TEXT, items TEXT /* JSON,平台順序的 provider id */, cids TEXT /* JSON,觀測當時的 cid,與 items 對齊 */, observed_at INTEGER, PRIMARY KEY (device_id, pid, provider));
-- manifest.playlists 不另存:Dump 由 playlists 表推回(pull 的閘保證「宣告但取不到」的狀態永遠不會被 Hydrate 進來,兩者恆等)。
-- 純快取(原 cache.json,附錄 C 決策 17):順序存 position,「最新在前、去重、上限 50」在 internal/cache 的記憶體邏輯
CREATE TABLE provider_playlists (provider TEXT, position INTEGER, id TEXT, name TEXT, total INTEGER, PRIMARY KEY (provider, position));
CREATE TABLE recent (position INTEGER PRIMARY KEY, at INTEGER, provider TEXT, type TEXT, id TEXT, label TEXT, detail TEXT);
```

**與 v0.5 草案的差異(2026-09-07,T6)**:`tracks` 多 `artists` / `conflicts`、少 `updated_at`——canon(§6.2)有前兩者、沒有後者,鏡像要無損(`store.Dump` 的輸出必須與 Drive 檔逐位元相同,`TestRebuildFromDrive`)。`device_base` 取代 `sync_state`:每台裝置的 base 是**有序的 provider id 清單**(§6.3),不是一個 hash;`base_hash` 若 T8 需要可由它導出。`devices` 鏡像 `manifest.devices` 並記「有沒有 dev 檔」,否則 base 全空的裝置檔重建時會消失。**不建 `sync_state` 與 `resolution_cache`**:P3 沒有寫入者(resolver 是 P4),migration 政策是整檔丟棄重建,之後加表只是 bump `user_version`(2026-09-08 T9:版本不符的舊檔改名保留為 `state.db.v<舊版>`、不刪——Drive 被清空時本機 cache 是唯一剩下的一份;沒有程式讀保留檔,只是不毀掉)。`store.Hydrate` 整批取代、`store.Dump` 整批讀出(items 依 `(rank, iid)`,與 canon 的 normalize 同序),P3 沒有 upsert。

Migration:`PRAGMA user_version` 不符就**整檔丟棄重建**(從 Drive hydrate),不寫 ALTER。

SQLite 是 **cache**,不是 source of truth。刪掉整個 db 應該能從 Drive 完整重建。這是設計約束,要寫測試驗證。

**P4 後半(2026-09-08,決策 20)已把 `confidence` / `pinned` / `updated_at`(外加 `source`)同時加回 `tracks.json` 與本表;下一段是 P3 當時的理由,留作紀錄。** `mappings` 在 P3 只有 `(cid, provider, provider_id)`,沒有 `confidence` / `pinned` / `updated_at`。 這三欄在 Drive 上沒有來源——`tracks.json` 的 mapping 就是 `{ provider: provider_id }`(§5.4、§6.2、§6.3)——留著就等於「刪 db 可從 Drive 完整重建」這條硬約束在規格層面先天不成立。理由與下一段拒收 ops / review 兩張表**完全相同**:db 裡只能存 Drive 上有的東西。而且 P3 沒有 resolver(§9,resolver 與 review queue 是 P4),沒有任何程式碼會產生信心度或釘選。**P4 做 resolver 時要把這三欄同時加回 `tracks.json` 與本表**(§5.1 Layer 3 的人工釘選是使用者意圖,必須跟著 Drive 走才「永久沿用」);migration policy 是整檔丟棄重建,加欄位不用寫 ALTER,成本為零。只加表不加 Drive 形狀,同一個洞就會在 P4 原樣重現。

**P3 不建 `ops` 離線佇列與 review 佇列兩張表(v0.5 有)。** 理由要讀準:已上傳的 op log **在 Drive 上**(v0.5 §6.3 的 `ops/<device_id>.jsonl`,當時 §6.5 步驟 6 會上傳它),真正不在 Drive 的只有 `synced = 0` 的離線佇列與使用者尚未裁決的 review 項目——這兩樣一旦進 db,「刪 db 可從 Drive 完整重建」就不成立。P3 為了讓這條硬約束成立而不建這兩張表,當時**不是否決 §6.4 的 op log 設計**。**2026-09-08 P5 定案:不建 op log、沒有離線佇列(附錄 C 決策 26),db 不加任何表**——`pl push` 只把 base 前進、寫回本裝置的 `dev__`,刪 db 仍可從 Drive 完整重建;要做離線編輯佇列時才回頭連同重建策略一起加表。db 裡只能存 Drive 上有的東西(canonical 鏡像、各裝置 `base` 副本)與純快取(`resolution_cache`);不存憑證、不存 provider 原始 JSON。

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
| **P0-1** | 用 curl 打通 Apple developer token → `GET /v1/catalog/tw/search`,確認 ISRC 有回傳 | ISRC 是整個 resolver 的基礎(2026-09-07:Spotify 半邊以真帳號驗過 160/160 有 ISRC;Apple 半邊仍待 web token,本項未勾)|
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
`auth login google` → appDataFolder 讀寫 → manifest / device 註冊基礎設施。**範圍依附錄 C 決策 10 擴為 P3 + P4 前半**:canonical model → `pl pull` → SQLite cache 與重建 → `export` / `drive init --from-local` 逃生口;snapshot / op log 於 P5 定案不做(§6.4,決策 26)

### P4 — 單向同步
canonical model → `pl pull`(平台 → canonical)→ resolver(ISRC + fuzzy)→ review queue

> **排程註記(2026-09-08):** 前半(canonical model → `pl pull`)已隨 P3 完成(附錄 C 決策 10);後半(resolver + review queue)依 [docs/superpowers/plans/2026-09-08-p4-resolver.md](superpowers/plans/2026-09-08-p4-resolver.md) 執行(決策 19–25;真帳號 ISRC 覆蓋 Spotify 160/160、Apple 2525/2553)。

### P5 — 雙向同步(2026-09-08 計畫:docs/superpowers/plans/2026-09-08-p5-sync.md)
**不建 op log / HLC**(決策 26):`PlaylistWriter` SPI + Spotify 寫入 → 共享檔版本守衛 → 投影 / push 變更集 / DERIVE 規則 4′ → `pl push` → `pl sync` → Apple 寫入(gate P0-2,預期 append-only)→ 真帳號驗收

### P6 — 抽象驗證 ✅(2026-09-08 計畫與結論:docs/superpowers/plans/2026-09-08-p6-local.md)
接入 `local` provider(讀 M3U/JSON)驗證 SPI 是否夠通用。**這比直接接第三個真實平台好** —— 沒有 ToS 風險、可完全掌控測試資料。SPI 撐得住 local provider 才去接 YouTube Music / Tidal。產出是計畫 §2 的「SPI 偷渡了哪些網路平台假設」清單(A1–A12)與對應修正;第一條就是 provider id / link 被當成全域有意義(決策 33)。**結論(T3):SPI 撐得住**——local 全走原本的能力介面、沒有特例分支,唯一的新語意是 `CapDeviceBound` + `DeviceScoped`。接下一個真實平台前先補:rename 會改 id 的平台要能回新 id(A12)、`Pushable` 要能回「推不出去」的原因而不是 CLI 猜(A10)、`friendlyErr` 第四個平台時改成 provider 自己回訊息(A2)、`Search` 正規化下沉到 `provider`(A6)。

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
capy auth login   <spotify|apple|google>                  # google:BYO client 或內建(release binary);--client-id/--client-secret 或 CAPY_GOOGLE_CLIENT_*
capy auth status
capy auth logout  <provider>

capy search <query> [--provider all|spotify|apple] [--limit N]
capy play                      # 恢復播放
capy play <query...> [--provider P] [--device NAME] [--type track|artist|playlist]
capy play artist:<name> | pl:<name> | track:<name>   # 前綴 = --type 簡寫;歧義:TTY 挑選器 / 非 TTY exit 2 + TSV
capy play --pick               # 直接開挑選器(TTY 限定)
capy play --id <track id>
capy                                                       # 無參數 + 終端機:互動式介面(水豚橫幅、現在播什麼、一行輸入任何子命令);非 TTY 一律印 help
capy pause | capy next | capy prev
capy seek <[h:]mm:ss|秒> | capy vol <0-100>                    # 2026-09-09 實作(P1 計畫原本延後,附錄 A 稽核後補上);Apple 半邊走 Music.app 的 player position / sound volume
capy now [--watch]
capy devices                                              # P1 已實作:列 Spotify Connect 播放裝置(名稱 / 類型 / 狀態 / 音量 / ID);與下面 §6.3 的 device 檔管理無關,只是同名

capy pl list
capy pl show   <name>
capy pl link   <name|pid> <provider>:<playlist_id|name>   # P3(2026-09-08 T8 已實作):只認明確 link(Q5 B);canonical 清單不存在就建立;讀不到的清單不可連結
capy pl unlink <name|pid> <provider>                      # P3(已實作);canonical 內容不動
capy pl diff   <name>                                     # 延後(P3 用 pl pull --dry-run 看差異)
capy pl pull   [name|pid] [--provider P] [--all] [--dry-run] [--yes] [--force]   # P3(已實作):平台 → canonical → Drive(§6.1);exit 0 無變更/已套用、1 錯誤、2 待套用(dry-run / 非 TTY 沒 --yes / 取消)、3 安全閥(§6.6);--yes 跳過確認、--force 才越過刪除閾值且只能配單一清單(不能配 --all),兩者都不放行 Drive 不完整;gone = 不在清單列表(不是 404)→ 自動 unlink(Q6 B);有列出但 items 404 = 空清單(Apple library 端點);讀不到的清單跳過;link 用同一個「在清單列表裡」的存在定義
capy pl push   [name|pid] [--provider P] [--all] [--dry-run] [--yes] [--force]   # P5(§6.5.2,決策 28):canonical → 平台;exit 同 pull(0/1/2/3);前提:base 存在且平台無未 pull 變更(比快照),否則 exit 3;對齊鍵是 cid,add 缺 mapping 的 item 列 skip;含 local file 的 Spotify 清單 exit 3;provider 不支援的 op 列 manual;push 後(成功或失敗)base := 重讀的平台狀態,pl__ 不變
capy pl sync   [name|pid] [--provider P] [--all] [--dry-run] [--yes] [--force]   # P5(決策 31;T5 實作於 internal/cli/sync.go):同一把鎖裡每個清單先 pull 各平台再 push 各平台(provider 字典序;--provider 只走一個);一張表(TSV 最前面多一欄 dir = pull / push)、一次確認;--dry-run 的 push 半邊用套用後的 C;push 半邊重用 pull 的 L
capy pl restore <name> --provider P                       # 延後(決策 31,計畫 Q18):= 把 base 快照 push 回平台,與 pl push 重疊

capy resolve [<name|pid>] [--provider P] [--dry-run] [--yes]   # P4 後半 T4(已實作,決策 22):預設全部已連結清單;缺 mapping 的 cid → ISRC 反查(95)→ fuzzy(0–100);≥85 自動寫入(Drive 先 SQLite 後,不動 alias set),其餘印 review 佇列 TSV(候選已屬另一 cid、或同一輪已配給別的 cid 也進佇列);exit 0 無事/已寫入(佇列有東西仍 0)、1 錯誤、2 待寫入未確認;--dry-run 永不寫入(連 FETCH 自癒的殘留也不上傳);單次 >200 次 API 在 stderr 提醒(決策 24)
capy resolve --review                                     # P4 後半 T4(已實作):TTY 逐筆 accept / skip / manual search / not available / keep(conflict 列);決定寫 pinned / 100 / review;accept 對到已屬另一 cid 的 id → 確認後合併,不同意當略過;非 TTY 印佇列 TSV、exit 2
capy resolve pin <cid> <provider>:<id|none> [--yes]       # P4 後半 T4(已實作):腳本用釘選;none = 這個平台沒有這首;帶 id 時先 GetTrack 驗證(404 → exit 1 零寫入)並把它的 ISRC 加進 alias set;id 或其 ISRC 已屬另一 cid → 合併(決策 21;非 TTY 要 --yes,否則 exit 2 零寫入)
capy export                                               # P3(2026-09-08 T9 已實作):只讀本機 state.db;檔名為鍵、檔內容為值的 JSON;本機空 exit 1
capy import <file.json>                                   # 延後;P3 的反向逃生口是 drive init --from-local
capy drive init --from-local [--dry-run] [--yes]          # P3(已實作):Drive 空 / 部分遺失時唯一允許寫入的命令;只建缺的檔、不覆寫、不動本機;exit 0 完成或沒缺、2 待套用
capy device list | capy device forget <device_id>         # 延後;P3 只在 manifest 註冊 device_id;forget 是唯一移除裝置檔的路徑(§6.3),尚未排程
capy db rebuild                                           # 延後;P3 的重建 = 刪 state.db 後由 hydrate 從 Drive 重建(§7,有測試)
capy config get|set|list       # default_provider;P6 加 local_root
capy config set local_root <目錄>   # P6(決策 35):本機曲庫;之後 capy pl link 通勤 local:<M3U 檔名>(只能連本機的,決策 33)
capy history clear             # 清空本機快取(state.db)的最近搜尋
capy update                    # P3(T10 已實作):GitHub Releases 最新正式版 → 下載本平台檔 + checksums.txt SHA-256 校驗 + 新 binary --version 自檢,才覆蓋自己;沒有簽章,校驗只保證下載完整
capy update --dev              # 從 main 最新節點 go install 重建並覆蓋自己(需 Go toolchain;沒有內建 Google client)
capy completion <shell>        # cobra 內建;候選只讀本機快取
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
| 14 | 補全機制(2026-09-04) | 兩者都做:CLI 內建挑選器為主(不需 shell 設定);shell tab 補全為輔,**只讀本機快取、絕不打網路、絕不碰 keychain**(2026-09-08 T6 改寫:快取從 `cache.json` 搬進 `state.db`,補全會開 SQLite——第一次會建檔寫 schema、等鎖上限 200 ms、逾時視同空快取,仍絕不建構 provider) | 每按一次 TAB 就跑一次;PR #8 之後建構 provider 會取檔案鎖並讀 keychain,補全若打網路會卡住整個 shell |
| 15 | `play` 語意(2026-09-04) | 統一搜尋(曲目 + 藝人 + 我的播放清單);TTY 下唯一明確命中(清單名完全相符 → 藝人名完全相符 → 曲目恰一筆)直接播,否則挑選器;**非 TTY 一律確定性**:`--type`/前綴,歧義回 exit 2 與 TSV 候選;藝人 = 熱門歌曲;無參數維持恢復播放 | 「記得清單名、記不得 ID」是真實使用情境;可腳本化鐵則要求非 TTY 絕不互動 |
| 16 | 播放器畫面(2026-09-04) | 先做 `now --watch`(bubbletea,Spotify 與 Apple 皆支援,Apple 端不得啟動未執行的 Music.app);無參數 `capy` 儀表板留到之後 | 範圍可控、獨立可測;儀表板依賴同一套元件,之後疊 |
| 17 | 順序(2026-09-04) | UX 三個 PR 先於 Google/Drive(P3 T3+);`cache.json` 為暫時性,P3 T6 併入 SQLite 後刪除(2026-09-07 T6 已併入 `state.db`,`internal/cache` 留作門面,舊檔首次 Load 時刪除) | 維護者已能實測工具,UX 摩擦是當下最貴的成本 |
| 18 | 刪除閾值公式與閘(2026-09-08,T8) | Q3 採 B:單一 (清單, provider) 要刪 >10 首、或 >30% 且 >3 首才擋,分母是該 provider 可見曲數(`DeriveResult.VisibleCount`);「Drive 不完整」是獨立的閘,`--yes` / `--force` 都不放行;`--force` 只能配單一清單、不能配 `--all`(一組超標整輪擋下,但解除只能一次一個清單) | A(`>10 或 >30%`)會擋掉「5 首刪 2 首」這種日常操作,C(<10 首不擋)會放過「4 首刪光」;B 兩邊都顧到。閘與閾值分開,是因為閾值的例外(`--force`)是「我知道我在刪」,不是「我知道 Drive 壞了」 |
| 19 | 觀測 cid 的身分規則(2026-09-08,P4 T0) | 三段式:已有 mapping 的 (provider, id) → 該 cid;正規化 ISRC 在某 track 的 alias set → 該 cid;否則 §6.2 公式。結果是 tombstone 敗者就追到勝者;alias set 只在人工操作時成長且過所有權檢查(每個 ISRC 至多屬一個 cid);**base 快照的 cid 只經 `merged` tombstone 重導,不在表裡就保留原 cid,絕不落公式、不查 mapping** | 跨 ISRC 的 mapping(fuzzy、人工釘選)一存在,純觀測 cid 就會把同一首歌分裂成兩筆;base 沒有 ISRC,落公式會算出不存在的 cid 讓移除計數歸零 → 合併後緊接的平台刪除變永久孤兒、P5 push 加回去;查 mapping 在「id 改釘給別的 cid」時同樣製造孤兒(PR #24 review)。代價:item cid 依賴觀測當下的 `tracks.json`,共享檔 LWW 的新面向進 P5 重審 |
| 20 | mapping 物件化與 schema 硬切換(2026-09-08) | `mappings[provider] = {id, confidence 0–100 整數, pinned, source ∈ observed/isrc/fuzzy/review, updated_at}`;pinned + 空 id = 不可得;優先序 pinned > observed > isrc/fuzzy;`updated_at` 只在 (id, confidence, pinned, source) 真的變時才動、等價比較不看它;`SchemaVersion` 1 → 2(所有檔),讀時相容字串舊形;SQLite user_version 3 → 4;不給 `Snapshot` 加 ISRC(tombstone 規則下 base 不需要) | 釘選是使用者意圖,要跟 Drive 走(§7 早就講明);整數信心度避免 Encode 的浮點格式歧義;單一常數一條路;Drive 上目前沒有真資料,硬切換成本零,但 release notes 要寫「每台裝置都要更新」(Q10)。**已知取捨(PR #26 review)**:Drive schema 與 store schema 同一版一起跳時,「Drive 不完整」的救援口 `drive init --from-local` 讀的是升版後的空快取(舊檔只保留、不讀);救援是用上一版 binary + 改回 `state.db` 的舊檔補齊 Drive 再更新。P4 期間 Drive 沒有真資料,成本為零;P5 之後任何 schema 跳版都要先在 release notes 寫「更新前先確認 `pl pull --dry-run` 不是 exit 3」 |
| 21 | cid 合併(2026-09-08) | 只由人裁決(accept / pin),自動寫入絕不合併——候選已屬另一 cid 一律進 review;勝者 = 字典序較小的 cid;兩 cid 對同 provider 持不同 id 時人工仍合併 → 優先序高的 mapping 留下(pinned > observed > 其他)、輸的那邊的 id 進 `conflicts[]`;敗者從 tracks 移除但在 `merged` 留 tombstone;`merged` 不可再生 → `SchemaVersion` 2 → 3(規則:加不可再生欄位就跳版),FETCH 時 item cid 經 tombstone 重導(Hydrate 拿到的已是重導後的狀態);cid 除合併外永不改寫,`p:` 不重鍵 | 合併是改寫 source of truth 的多檔操作,85 分的自動判斷不夠格;字典序讓兩台裝置各自合併同一對也同解;tombstone 讓沒有交易的多檔 COMMIT 中途失敗可自癒(PR #24 review) |
| 22 | `capy resolve` 契約(2026-09-08) | 預設全部已連結清單;≥85 自動寫入走 `withCanonical`;exit 0 無事/已寫入(review 佇列非空仍 0)、1 錯誤、2 待寫入未確認;`--review` TTY 逐筆、非 TTY exit 2;`pin <cid> <provider>:<id|none>`;`pl pull` 不做 resolve 只提示 | resolve 只增不刪所以不需 `--all`;cron 的 `resolve --yes` 不能因永遠有幾首解不開而永遠報錯;API 成本與關注點分離 |
| 23 | Layer 1 / 2 規則(2026-09-08) | ISRC 反查候選只留 ISRC 相同者,多筆消歧:專輯名同 > 時長差最小 > id 字典序;fuzzy = 60·JW(title) + 25·JW(primary artist) + 15·時長項(≤3 s 滿分、≥30 s 零、線性),live/remix/acoustic/cover/demo/instrumental/karaoke 只在一邊 → 上限 84;時長差 >3 s → 上限 84;`floor` 取整;<60 不列;`norm()` 手刻全形轉半形,不引入 `x/text`;artist alias 表延後 | 真帳號 ISRC 覆蓋 Spotify 160/160、Apple 2525/2553,fuzzy 只補 ~1%;`Track` 沒有發行日所以拿掉「較早發行」 |
| 24 | negative cache 延後(2026-09-08) | 每次 resolve 重查未解 cid;cron 的 `resolve --yes` 是支援用法;觸發條件只留單次 >200 次 API(T4 超過時 stderr 提醒)→ 加純快取表(不進 `Dump`) | §7 本就允許純快取;這是成本判斷不是約束判斷;cron 若同時是支援用法又是觸發條件,延後就變成一上線就欠(PR #24 review) |
| 25 | P4 T0 只產計畫與 spec(2026-09-08) | 等維護者 review 後開 T1 | 與 P3 決策 12 同模式 |
| 26 | P5 不建 op log / HLC(2026-09-08,P5 T0) | 同步模型 = per-device base + 三方語意的 DERIVE(規則 4′ / 5)+ 共享檔版本守衛(決策 29);v0.5 §6.4 的 op log、snapshot、compaction 全部不做;沒有離線佇列 | v0.5 的 op log 是在「所有裝置寫同一份狀態」的前提下設計的;P3 把 base 搬進 `dev__` 後,每台裝置對每個平台都有「上次對齊狀態」,DERIVE 對最新 C 算 delta 就是三方合併;沒有本機編輯命令,op log 沒有東西可記。**代價**:沒有 base 的裝置(新裝置、剛連結的平台)bootstrap 時會復活別台裝置刪掉、尚未 push 到這個平台的曲目(計畫 Q15,接受並文件化;任一裝置 sync 一次即關窗口、復活以 `add` 列可見);改名 / 順序**兩邊都改了**時後 COMMIT 者勝、無 HLC(只有一邊改了由規則 7 / 6′ 正確保留) |
| 27 | DERIVE 規則 4 與 6 改 base-aware(2026-09-08) | 4′:base 存在時某 cid 在 L 出現 l 次、base 出現 b 次,l > b 才新增、至多 l − b 個、取 L 順序最後的;6′:base 存在時只有平台真的重排(L 拿掉 base 沒有的 id 後的序列 ≠ base 拿掉 L 沒有的 id 後的序列)才採 L 的順序,否則配對上的 item 保留 C 的 rank;沒有 base 維持原行為;與 push 相鄰出貨 | 規則 5 早就是三方語意,規則 4 / 6 不是:Spotify 刪的歌會被 Apple 的下一次 pull 加回 C、Spotify 的重排會被 Apple 的下一次 pull 翻回舊順序再 push 回 Spotify——刪除與重排意圖都丟失、`pl sync` 結果取決於先 pull 哪邊;這是 v0.5 tombstone / HLC 要擋的情境。只有 pull 的世界裡這兩條會讓 C 與平台脫節,所以必須跟 push 一起 |
| 28 | `pl push` 契約(2026-09-08) | 形狀與 exit 鏡像 pull;前提一 base 存在、前提二 L 的快照 = base 的快照(比快照不比 Derive:Derive 是 L 對 C 的差,C 剛被另一平台改過是 push 的常態;只比 provider id 序列不比 cid——釘選 / 合併會改觀測 cid),否則 exit 3;對齊鍵是 cid(配對上的不動,pinned 不可得 / library-only 在平台上時不會被刪),add 缺 mapping 的 item 列 skip;含 local file 的 Spotify 清單拒絕 push;閾值同決策 18(分母 = L 長度);provider 不支援的 op 列 manual;ApplyOps 成功或失敗都重讀 L′、base := L′、`pl__` 不變(失敗仍 COMMIT 再 exit 1);Spotify 用整批取代(`PUT` 100 + `POST` 分批)、`rename` 走 `PUT /playlists/{id}` | 沒有 base 的 push 會把平台清單刪光;蓋掉未 pull 的平台變更不可接受;整批取代簡單且決定性,代價是 Spotify 端 `added_at` / `added_by`(計畫 Q19,升級路徑 = 最小操作 + `snapshot_id`) |
| 29 | 共享檔版本守衛(2026-09-08) | COMMIT 前再 list 一次比對 FETCH 讀過的每個檔(不只 staged)的 `version` 與同名份數,任一不符 → 零上傳、本機不動、exit 1、重跑;所有走 `withCanonical` 的命令一體適用 | lost update 的真正危害是「base 前進了、C 卻少了對方的變更 → 永遠不再 derive」;Drive 無 CAS,一次 list 涵蓋所有檔是能做到的上限(殘餘窗口 = 上傳序列,秒級);exit 1 讓 cron 看得到兩台裝置常撞在一起 |
| 30 | Apple 寫入策略(2026-09-08) | gate 在 P0-2 寫入探測(維護者在拋棄式 library 清單上跑,計畫 R-8);預期 remove / reorder / rename 無文件化端點 → **append-only**:`add` 用 `POST …/tracks`(catalog id),其餘回 `ErrCapability` 列 manual;**不採 rebuild**;只寫使用者自建的 library 清單 | rebuild 需要刪清單(同樣未驗證)、清單 id 會變、Apple 端封面 / 描述會丟、中途失敗留兩份;append-only 配規則 4′ 不會讓未刪的曲目回流到 C;ToS「不可 synchronized」灰色地帶只寫自建清單 |
| 31 | `pl sync` 與 `pl restore`(2026-09-08) | `sync` = 同一把鎖裡每個清單先 pull 各平台再 push 各平台(`--provider` 可只走一個平台),一張表、一次確認、閾值各算;push 半邊重用 pull 半邊的 L;撞上版本守衛時平台已改、Drive 沒改,訊息講明(重跑時 pull 半邊把已推到平台的變更再吸收一次、零 push——base 沒落地,不是零 pull);`--provider` 指到寫不了的平台時只 pull;某個 (清單, provider) 的 push 前提 / local file 擋下時也只跳過那一格的 push 半邊(pull 照常落地),不擋整輪——只有刪除閾值擋整輪(PR #36 review:cron 的 `--all` 不能被一個清單永久綁死);`restore` 延後 | 兩個 push 前提由「先 pull 後 push」建構保證;Apple append-only 期間「只 sync Spotify」是合理用法;再讀一次 L 多花 quota 又會讓 200 ms 內的平台變動變成中途 exit 3;restore 在 per-device base 下等於 push base 快照,與 push 重疊 |
| 32 | P5 T0 只產計畫與 spec(2026-09-08) | 等維護者 review 後開 T1 | 與決策 12 / 25 同模式;特別因為決策 26 推翻了 v0.5 核可過的 op log 設計 |
| 33 | `local` 是綁裝置的 provider(2026-09-08,P6 T0) | playlist id = `<device_id>/<檔名>`;**track id 不帶 device**(= 相對路徑,cid `p:local:<路徑>`;PR #37 review:接管是重灌的復原路徑,帶 device 的 cid 會讓沒 ISRC 的本機曲目在接管後整批分裂,計畫 Q31);SPI 加 `CapDeviceBound`(bit 15)+ `DeviceScoped.Foreign(id)`;CLI 對別台裝置的 link 一律跳過(不 gone、不 refused、不動 base);`pl link` 只連本機的,撞到別台裝置的 local link 時**接管**(訊息指出原擁有裝置;那台之後變 foreign)——這也是重灌 / 換設定目錄後 device_id 變了的復原路徑;`pl unlink` 任何裝置都可;一個 canonical 清單同時只連一台裝置的 M3U(Q30) | `pl__.Links` 是共享檔而曲庫只在一台機器:沒這條,別台的 `pl sync --all` 會把連結當 gone 刪掉;這是 SPI 的第一個「id 全域有意義」假設 |
| 34 | local 的 id = 正規化相對路徑(2026-09-08) | 去 `./`、NFC(開檔對 NFC / NFD 不分:找不到就掃同目錄比正規化後同名——NTFS 分、APFS 不分);`\` → `/` 只翻 M3U 內容行與 Windows 上的 id(macOS 的 `\` 是合法檔名字元);`C:/…` 也算絕對路徑(`path.IsAbs` 只認 `/`);改名 / 搬家 = remove + add;cid `p:local:<路徑>`;內容 hash(`capy local scan`)是升級路徑,不建 | 驗證 SPI 夠用;跨裝置同路徑 = 同一首(接管後不分裂;同路徑不同歌是接受的取捨,計畫 Q31) |
| 35 | local 讀端(2026-09-08) | `local_root` 一層的 `*.m3u8` / `*.m3u` + `library.json`(不讀 tag、不加相依);`Search` 必做(resolver Layer 2 是 local ↔ Spotify 唯一的橋);不做播放、不做 `auth login local`、不做 Create;gone = 本機檔不存在 | 錯誤族只有 NotFound / 權限 / IO / JSON;auth / rate limit / restricted 的 n/a 都是發現 |
| 36 | local 寫端(2026-09-08;T2 修正;PR #38 review 補守衛與保真) | 整檔改寫(同目錄 temp、抄原檔權限、`fsync`、`os.Rename`;symlink 寫穿;id 必須在 root 底下且是清單檔——這是唯一會覆寫使用者磁碟檔案的路徑),丟 `#EXTINF` 與註解但**路徑用磁碟上的拼法寫回**(NFD 就 NFD)、`#` 開頭補 `./`、含換行拒寫;`Pushable` = 路徑在曲庫或檔案在這台(不含目錄、不含換行);add / remove / move 都支援,沿用 push 套用前重讀、不做 mtime CAS;**rename 回 `skipped`、不宣告 `CapPlaylistRename`**——id 就是檔名,改名會讓 id 變、SPI 沒有「新 id」可回(計畫 §2 A12) | 第一個 add / remove / move 全支援的寫端,與 Apple 成對照;rename 是路徑型 id 的天生限制 |
| 37 | P6 T0 只產計畫與 spec(2026-09-08) | 等維護者 review 後開 T1 | 決策 33 在 SPI 加新語意,不該由一個 PR 順手決定;同決策 12 / 25 / 32 |

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

