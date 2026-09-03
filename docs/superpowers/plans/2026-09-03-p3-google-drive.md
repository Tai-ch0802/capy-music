# P3 交接與實作計畫:Google 登入 + Drive 同步基礎設施 + `pl pull`

> **這份文件是交接用的單一入口。** 新 session 開工時先讀 §0(現況)與 §1(已拍板的決策),再照 §3 的任務切分執行。
> 撰寫日期 2026-09-03。基準 commit:`45d9fe3`(main,PR #6 已合併,CI macOS + Windows 全綠)。

---

## §0 現況

### 已完成的階段

| 階段 | 內容 | PR |
|---|---|---|
| P0 | 專案骨架:cobra + config + keychain + loopback server + Provider SPI + 429 退避 | #1 #2 |
| P1 | Spotify 全鏈路:BYO client ID 精靈 + PKCE loopback、`search`、`play/pause/next/prev/now/devices`(Connect)、`pl list/show`、`doctor` | #4 |
| P2 | Apple Music 全鏈路(v0.4:`.p8` 簽章 + Cloudflare Worker 派發 + MusicKit JS 橋接) | #5 |
| P2′ | Apple 改版(v0.5:使用者自抓 web token BYO;`.p8`/Worker/橋接全刪,快照 `3649b7b`) | #6 |

規模:非測試 Go 約 3,150 行,測試 170 個,10 個套件。CI 為 macOS + Windows 雙矩陣(gofmt、`go mod tidy -diff`、vet、build、`go test -race`、`bash -n scripts/p0/*.sh`)。

### 目前的命令表面

```
capy auth login <spotify|apple> / auth status / auth logout <provider>
capy search <query> [--provider spotify|apple] [--limit N]
capy play [query] [--provider P] [--device NAME] / pause / next / prev / now / devices
capy pl list / pl show <名稱|ID> [--provider P]
capy doctor [--provider P]
capy debug apple-token [--user]        # 隱藏,給 scripts/p0 用
```

### P3 可以直接疊上去的既有元件(精確簽名)

| 元件 | 位置 | 簽名 / 要點 |
|---|---|---|
| loopback + PKCE | `internal/auth/loopback.go` | `NewState() (string, error)`;`NewLoopback(preferredPort int, state string) (*Loopback, error)`(傳 `0` = 動態 port,Google Desktop client 允許);`Handle`/`Deliver`/`Start`/`Wait(ctx) (url.Values, error)`/`Port`/`BaseURL`/`Close`;callback 固定 `GET /callback`,state 不符回 400 |
| Spotify 登入範本 | `internal/auth/spotify.go:52` | `LoginSpotify(ctx, clientID string, openBrowser func(string) error) (*oauth2.Token, error)`;`oauth2.GenerateVerifier()` + `S256ChallengeOption` + `VerifierOption`。`spotifyOAuthConfig` 與 `persistingTokenSource` **未匯出**,Google 要仿寫不能直接呼叫 |
| keychain | `internal/secret/secret.go` | `Set/Get/Delete(key string)`;service 名 `capy-music`;`Get` 把 `keyring.ErrNotFound` 正規化成 `secret.ErrNotFound`。既有鍵:`spotify.refresh_token`、`apple.developer_token`、`apple.music_user_token` |
| config | `internal/config/config.go` | `Config{SpotifyClientID, AppleStorefront}`;`Dir()`(`CAPY_CONFIG_DIR` 可覆寫整個目錄)、`Load()`、`Save()`(tmp+rename 原子寫、0600);未知欄位會被忽略(有測試) |
| 輸出層 | `internal/ui/ui.go` | `IsTTY(f)`、`Bold(tty, s)`、`Table(w, tty, header, rows)`(非 TTY = headerless raw TSV,cell 內 tab/newline 換空白)、`FormatDuration(ms)` |
| Provider SPI | `internal/provider/provider.go` | `Capability` 位元 + `Has()`;哨兵 `ErrAuthExpired/ErrNoActiveDevice/ErrRestricted/ErrNotSupported/ErrNotFound`;`Track{ProviderID, ISRC, Title, Artists, Album, DurationMS, Explicit, Raw}`;`Provider/Searcher/PlaylistReader/PlaybackController` |
| 429 退避 | `internal/provider/backoff.go` | `Backoff(ctx, resp, attempt) error`,`MaxRetries=3`、`MaxBackoff=60s`;`RateLimitError`;測試替換點 `Wait`、`BackoffStderr` |
| CLI 測試 helper | `internal/cli/*_test.go` | `runCLI(t, args...)`(stdout+stderr 同一 buffer)、`setCLITestConfig(t)`(MockInit + 暫存 `CAPY_CONFIG_DIR`)、`swapProvider(t, handler)`、`swapProviderWith(t, provider)` |

### 尚不存在(已逐項 grep 驗證)

`internal/` 底下沒有 `google/`、`drive/`、`store/`、`sync/`;`go.mod` 沒有 `google.golang.org/api`,也沒有 `modernc.org/sqlite`;沒有任何 SQLite 程式碼;`pl` 只有 `list`/`show`;沒有 `PlaylistWriter`/`ApplyOps`/`PlaylistOp`(SPI 註解明示延後);`device_id` 無歸屬(舊的 `install_id` 已隨 `3649b7b` 移除)。

### 規格已經定死、不要重新發明的東西

`docs/ARCHITECTURE.md` 已經寫死:§4.4 Google auth(三個 scope、Desktop client、`access_type=offline`+`prompt=consent`)、§6.2 canonical 資料模型(ULID `pid`/`cid`、fractional index `rank`、ISRC alias set)、§6.3 Drive appData 佈局、§6.4 op log 格式與 HLC、§6.5 六步同步流程與衝突規則、§6.6 安全網(dry-run、刪除 >10 首或 >30% 中止)、§7 SQLite 十張表的 schema、附錄 A 命令表面草案。**實作時以 spec 為準,不要另立設計。**

⚠️ 但 spec 有幾處與本次決策衝突、也有幾處自相矛盾(`pl pull` 的方向、`cid` 是不是主鍵、item 去不去重、兩張不可重建的 SQLite 表),**T0 會把它們改掉**。T0 完成前先讀 §3 的 T0 清單,不要照著舊文字寫程式。

---

## §1 本次拍板的決策(2026-09-03,維護者裁定)

### 決策 9:Google client 內建、但在 build 時注入,並新增發行流程

- binary 內建維護者的 Google Desktop client,但 **client ID / secret 絕不 commit 進 repo**,改用 `-ldflags -X` 在發行時注入。Google 政策明文:「You must never commit client credentials into publicly available code repositories.」([policies](https://developers.google.com/identity/protocols/oauth2/policies))
- 因此新增 GoReleaser 發行流程:下載 release binary 的人零設定;用 `go install` 從原始碼建的人拿不到注入值,**自動落回 BYO 路徑**,README 要把這件事寫清楚。
- 一律提供覆寫:`--client-id` / `--client-secret` flag(或精靈輸入);沒有內建值時 `auth login google` 直接走 BYO 精靈。
  **client secret 只進 keychain,不進 config**(鍵名 `google.client_secret`)。Google 說 installed-app 的 secret 非機密,但那不改變 CLAUDE.md「憑證只進 OS keychain,絕不寫入 Drive、SQLite 或設定檔」的字面;client ID 非機密,可以放 config。
- **程式對兩條路徑一視同仁**——內建值只是預設常數,不是分支;所以本計畫的任務不因這個決策而改變,只多一個發行任務。

為什麼 Google 與 Spotify / Apple 不同:Spotify 走 BYO 是被政策逼的(dev mode 只能 5 位使用者),Apple 是被會籍與「代持簽章金鑰」逼的;Google 這三個 scope 全是 non-sensitive(`drive.appdata` 屬 non-sensitive,restricted 的是 `drive` / `drive.readonly` / `drive.metadata*`),不需 verification、沒有 100 人上限,內建 client 不必代持任何**使用者**憑證。

維護者要做、程式無法代勞的事:

1. 建 Google Cloud 專案、啟用 Drive API。
2. Google Auth platform → Branding(app 名稱、support email);Audience 選 External。
3. Data Access **只**加 `openid`、`userinfo.email`、`drive.appdata`。多一個 Gmail scope 就會觸發 CASA 年度資安評估(CLAUDE.md 硬約束)。
4. **按 Publish app 切到 In production**。停在 Testing 的話 refresh token 7 天過期——`drive.appdata` 不在豁免清單,只有 `openid` / `userinfo.email` / `userinfo.profile` 豁免。
5. 建 Desktop app client;**secret 只在建立當下顯示一次**,立刻收好,交給發行流程當 CI secret。
6. 知道兩個風險:OAuth client 若 6 個月無人使用會被 Google 自動刪除(刪除前 30 天通知);client 一旦被停用,所有用 release binary 的人同時壞掉,救援路徑就是請他們改用自己的 client。

### 決策 10:下一階段的範圍 = P3 + P4 前半

spec 的 P3 只有「`auth login google` → appDataFolder 讀寫 → manifest / snapshot 基礎設施」,但那樣 `snapshot.json` 在 P3 沒有任何內容可寫(canonical playlist 要 P4 的 `pl pull` 才誕生),使用者拿到的價值是零,CLAUDE.md 的硬約束「刪掉 SQLite 必須能從 Drive 完整重建、且要有測試」也沒有東西可測。

本階段做到:**Google 登入 → Drive appdata 讀寫 → manifest / device 註冊 → canonical model → `pl pull`(平台 → canonical → Drive)→ SQLite cache 與重建 → `export` / `import` 逃生口**。
不做:resolver 的 fuzzy 比對與 review queue(P4 後半)、op log / HLC / 三方合併 / `pl push`(P5)。

### 決策 11:issue #3 兩案並用

(a) access token + expiry 也存進 keychain,**加上** (b) refresh 路徑加跨程序檔案鎖。Google 的 token source 從第一天就照同一個形狀寫,不要留兩套。細節見任務 1。

### 決策 12:本 session 只產計畫與文件,不寫程式

實作由另一個 session 執行。

### 決策 13:playlist item 保真,不去重

canonical 的 playlist item 用自己的 ULID(`iid`)當鍵、`cid` 當屬性,同一首歌在同一個清單裡可以出現多次。理由:Drive 上的副本是 source of truth,現在去重等於備份永久少掉資訊,而且之後要改模型很貴。
**要改 spec**:§6.2 的 `items[]` 元素加 `iid`;§7 的 `playlist_items` 主鍵從 `(pid, cid)` 改成 `(pid, iid)`。

---

## §1.5 研究查證(2026-09-03,附出處)——會改設計的事實

### Google OAuth

| 事實 | 影響 | 出處 |
|---|---|---|
| loopback `http://127.0.0.1:port` 是桌面平台建議機制,port 動態選、不必註冊 | 直接複用 `NewLoopback(0, state)` | [native-app](https://developers.google.com/identity/protocols/oauth2/native-app) |
| OOB(`urn:ietf:wg:oauth:2.0:oob`)2023-01-31 起全面封鎖 | 不存在「貼授權碼」的備援路徑 | [oob-migration](https://developers.google.com/identity/protocols/oauth2/resources/oob-migration) |
| PKCE 標「Recommended」,`code_challenge_method` 只吃 `S256` / `plain` | 照 Spotify 的寫法用 S256,沒有理由不用 | native-app |
| Desktop client **會發 client secret**,token 交換要送;豁免只列 Android / iOS / Chrome | BYO 使用者要複製**兩個**值,不是一個 | native-app |
| 官方立場:installed app「assumed that these apps cannot keep secrets」 | 內建 client 在技術上站得住;政策面見決策 9 | native-app |
| **Google 的 refresh token 不輪替**——refresh 回應沒有新的 `refresh_token` | **不要照抄 Spotify 的 `persistingTokenSource` 覆寫邏輯** | [oauth2](https://developers.google.com/identity/protocols/oauth2) |
| refresh token 失效條件:使用者撤銷、6 個月未使用、每帳號每 client 上限 100 個(超過淘汰最舊) | `doctor` 的錯誤訊息要能分辨這幾種 | oauth2 |
| OAuth client 6 個月無人使用會被自動刪除 | 維護者風險,寫進決策 9 | [cloud/15549257](https://support.google.com/cloud/answer/15549257) |
| Testing 狀態下 refresh token 7 天過期,`drive.appdata` **不在**豁免清單 | 必須 Publish;BYO 的人漏按這步會在第 7 天莫名被登出 | [cloud/15549945](https://support.google.com/cloud/answer/15549945) |

### Google Drive appData

| 事實 | 影響 | 出處 |
|---|---|---|
| **v3 沒有樂觀鎖**:`etag` 在 v3 已移除,`files.update` 沒有任何 precondition 參數 | 兩台裝置寫同一檔 = 靜默 last-write-wins。spec §6.3 的 per-device 分檔是唯一解,不是可選項 | [v2-to-v3](https://developers.google.com/workspace/drive/api/guides/v2-to-v3-reference)、[files.update](https://developers.google.com/workspace/drive/api/reference/rest/v3/files/update) |
| `version` 是伺服器端單調遞增整數 | 只能**事後**偵測 lost update,不能防止 | [files resource](https://developers.google.com/workspace/drive/api/reference/rest/v3/files) |
| **Drive 不強制同資料夾內檔名唯一** | spec §6.3 畫的巢狀 `playlists/<pid>/` 在兩台裝置同時 resolve-or-create 時會生出兩個同名資料夾 → **改用扁平檔名 + `appProperties` 過濾** | [create-file](https://developers.google.com/workspace/drive/api/guides/create-file) |
| revisions 不是免費回滾:非 `keepForever` 的舊版不可下載,約 30 天或 100 版清除 | `base/` 自建快照是對的,別依賴 Drive 版本 | [manage-revisions](https://developers.google.com/workspace/drive/api/guides/manage-revisions) |
| `changes.list` 支援 `spaces=appDataFolder` | 增量拉取的正路,取代 N 次 get | [changes.list](https://developers.google.com/workspace/drive/api/reference/rest/v3/changes/list) |
| appdata **計入使用者儲存空間**;使用者在「管理應用程式」兩下就能清空,無確認 | **「appdata 是空的」必須當成 bootstrap / ambiguous,絕不可解讀成「使用者刪光了」而反向清空平台清單** | [appdata guide](https://developers.google.com/workspace/drive/api/guides/appdata)、[Drive 說明](https://support.google.com/drive/answer/6374270) |
| 配額改為 quota units:每專案 1,000,000/分、每使用者 325,000/分;`get` 5、`create`/`update` 50、`list` 100、`download` 200;每日 4 億單位以上「計畫於 2026 稍後計費」 | 個人用量遠低於上限,但**spec §8.5 的「Drive API 無用量計費」已過時**,要進附錄 B 季檢表 | [limits](https://developers.google.com/workspace/drive/api/guides/limits) |
| 403 `userRateLimitExceeded` / 429 要指數退避 | 現有 `provider.Backoff` 只在 429 觸發,Drive 的 403 形式不會觸發 → 需擴充或另寫 | [handle-errors](https://developers.google.com/workspace/drive/api/guides/handle-errors) |

### 曲目識別(P4 用得到)

- **ISRC 是候選產生器,不是主鍵**:Apple 的 `filter[isrc]` 明文可回多首;Spotify 同一 ISRC 也可能對到單曲 / 專輯 / 合輯版本。消歧序建議:專輯名相同 > 時長差最小 > 較早發行。正規化:大寫、去連字號、非 12 碼視為缺失。
- 反查成本不對稱:Apple `catalog/{sf}/songs?filter[isrc]=A,B,…` 一次 25 個;Spotify `search?q=isrc:X` 一次一首、limit ≤10 → 先批次解 Apple,Spotify 逐首且必快取。
- **上傳曲 / 純 library 曲沒有 catalog 也沒有 ISRC**:canonical 必須把它們保留成 provider-only,**絕不可當成「已刪除」**。
- 版本標籤(live / acoustic / remix / cover / instrumental / demo / sped up)應該當**硬條件**(兩邊標籤集合相等才配對),不是只調高分數;時長容差 ±3s,超出但其餘全中 → 進 review queue 而不是拒絕。

---

## §2 需要維護者本人操作的驗收(程式無法代勞)

這些都不 gate 在 Apple 開發者會籍上,只需要 **Apple Music 訂閱** 與瀏覽器。

| 編號 | 內容 | 為什麼重要 |
|---|---|---|
| **C-0** | **主機/標頭真值表(前提,壞了後面全免)**:`amp-api.music.apple.com/v1` base、`Origin: https://music.apple.com`、`Media-User-Token` 標頭全是依 gamdl 推定,沒用真 token 打過。preflight 401 → 試 `CAPY_APPLE_API_BASE=https://api.music.apple.com/v1`;storefront 403/404 → MUT 改 cookie 形式或改用 `GET /me/account?meta=subscription`。**歸因檢查**:故意弄壞各一顆 token,訊息要怪對的那顆。定案後把贏家寫死、刪掉 `client.go` 的 `ponytail:` 註解 | 整個 Apple 端建立在這個假設上 |
| C-1 | 手動抓 token + 精靈:記錄 DevTools 實際欄位名、JWT `exp` 距今多久(輪替週期估計);**親眼確認**揭露頁預設停在「取消」、重登頁預設停在「只更新 developer token」(huh 渲染行為,單元測試碰不到) | R-6 的緩解成本估計 |
| C-2 | `search --provider apple`、`pl list`、`pl show`、`play --id`;**play 機制 A/B 決勝**(A = AppleScript `open location`,B = `CAPY_APPLE_PLAY_MECHANISM=open`)。決勝後硬編勝者、刪環境變數、加 `player state` 輪詢讓 ▶ 不再假成功 | ▶ 目前只代表 Music.app 接受了 URL |
| C-3 | 非 TTY:`CAPY_APPLE_DEVELOPER_TOKEN=… CAPY_APPLE_USER_TOKEN=… capy auth login apple --i-understand < /dev/null`;缺 `--i-understand` 要被拒且訊息含聲明 | 可腳本化是核心價值 |
| C-4 | 只更新 developer token:精靈預設「只更新」→ 只貼一顆;`auth status` 的 user token 不變 | Apple 輪替時的常態路徑 |
| C-6 | 過期行為:把 keychain 紀錄的 `exp` 改到過去 → `search --provider apple` 指向 login 且訊息含「過期」;`doctor --provider apple` ❌;`auth status` 顯示已過期 | 誤導訊息會讓使用者亂重登 |
| library 形狀 | 拿一個空清單與一個 >100 首的清單各跑一次 `pl show`:`include=catalog` 是否內嵌 attributes、空清單是否回 404、分頁 `next` 是否如預期 | 決定 `client.go` 的分頁與 404 處理 |
| P0-1 | `scripts/p0/p0-1-isrc.sh`:確認 catalog search 回傳 ISRC。結果回填 spec §9 P0-1 | ISRC 是 P4 resolver 的基礎 |
| **P0-2** | `scripts/p0/p0-2-playlist-ops.sh`:**Apple Music API 能否從 library playlist 移除/重排曲目**。結果決定 `CapPlaylistRemove`/`CapPlaylistReorder` 與 §6.5 是否需要 rebuild fallback | P5 的架構風險點,建議在 P5 開工前跑掉 |

> Spotify 端也有一項:`/playlists/{id}/items` 內層鍵名確認後,可以刪掉 `spotify/client.go` 的雙鍵 decode。

---

## §2.1 Google 端的人工驗收(P3 做完後,需要真帳號)

| 項目 | 怎麼做 | 驗什麼 |
|---|---|---|
| G-0 | **在 T3 開工前**先跑:瀏覽器走一次授權拿 `code`,然後不帶 secret 打 token 端點 `curl -X POST https://oauth2.googleapis.com/token -d client_id=… -d code=… -d code_verifier=… -d grant_type=authorization_code -d redirect_uri=http://127.0.0.1:PORT/callback` | Desktop client 換 token 是否**真的**必須帶 `client_secret`。文件把該欄位標 Optional 而豁免只列 Android/iOS/Chrome,語意矛盾。若不必帶,BYO 精靈少一個欄位、keychain 少一個鍵、T10 少注入一個值 |
| G-1 | 同意畫面上**取消勾選** Drive 那一項 | T3 的 downscope 拒絕真的會擋(token 回應的 `scope` 缺 `drive.appdata` → 不落地) |
| G-2 | 用一個停在 Testing 狀態的專案,放 8 天後再跑 | `ErrGoogleGrant` 的訊息把「忘了按 Publish app」列為第一嫌疑 |
| G-3 | 兩台機器各登入一次 | manifest 的 device 註冊、兩份 `dev__<id>.json` 並存、`base` 的 LWW 合併 |
| G-4 | 在 Google 帳號設定「管理應用程式 → 刪除隱藏的應用程式資料」後跑 `pl pull` | **exit 3、零寫入**,訊息指向 `capy drive init --from-local`。這是最重要的一項:它擋的是靜默清空 |
| G-5 | `files.list` 帶 `spaces=appDataFolder` + `q=appProperties has {...}` 打真 Drive | 扁平佈局的過濾語意成立(整個 T4 靠它) |
| G-6 | 沒啟用 Drive API 的專案跑一次 | `403 accessNotConfigured` 的訊息印得出啟用連結(BYO 最常見的卡點) |

---

## §3 任務切分

三份獨立架構提案經三位評審評分,以「可腳本化優先」那份為骨幹(8 / 8 / 5.5),移植另外兩份的具體點子;之後再由一位批判者逐條審這份計畫,以下已納入它抓到的缺口與矛盾。

依賴鏈:**T0 最先**(其他任務都照它改後的 spec 做)→ T1 →(T2 ∥ T3)→ T4 → T5 → T6 → T7 → T8 → T9。T10 任何時候都能做。

> **開工前必須先有的東西**:§4 的決策 3(閾值公式)與決策 7(`ops` 表存廢)要在 T6 前拍板;`scripts/p0/p0-1-isrc.sh`(ISRC 可得性)要在 T5 前跑,否則 `cid` 的設計前提不成立;G-0 要在 T3 前跑。

### T0 — 文件對齊(只改 docs,不動程式)

**產出**:`CLAUDE.md`、`docs/ARCHITECTURE.md`。

- CLAUDE.md 決策 8:改寫成「**使用者的**憑證全面 BYO;app 自身識別(Google OAuth client)內建但在發行時注入、不進 repo」,並註明 client secret 只進 keychain。
  ⚠️ **這是放寬硬約束,不是通過檢查**:內建 secret 會編進 release binary(`strings capy` 讀得到)。PR 描述要明寫「CLAUDE.md 硬約束依決策 9 放寬」,讓維護者簽字,不可藏在 docs commit 裡。
- §4.4:補內建/BYO 雙路徑、`client_secret` 是否必送(依 G-0 結果)、refresh token **不輪替**、Testing 7 天陷阱與 `drive.appdata` 不在豁免清單。
- §6.1:表格把 `capy pl pull` 對應成 `git fetch`(Drive → 本機),與 §9 P4「`pl pull`(平台 → canonical)」**方向相反**。定案:`pl pull` = **平台 → canonical → Drive**(照 §9);Drive → 本機是 `pull` 內部的 FETCH 步驟,不另立命令。改 §6.1 的表格。
- §6.2:`items[]` 加 `iid`;`cid` 改為決定性 ID(見 T5)。
- §6.3:巢狀目錄樹 → **扁平檔名 + `appProperties`**;`base` 從共享檔移進各裝置自己的檔案。
- §6.5:衝突規則「add vs add 同 cid → 去重」與決策 13(item 保真)相衝,改成「同 `iid` 才去重」。
- §6.6:`pl restore` 從 `base/` 回滾的語意隨扁平化改變(base 進 dev 檔),改寫或標為 P5 再定。
- §5.1 / §5.2 / §5.4:「ISRC 是主鍵」與 §1.5 查證的「ISRC 是候選產生器」相衝,統一為後者;§5.4 的 `Drive tracks/mappings.json` 路徑改扁平命名。
- §7:`playlist_items` 主鍵 `(pid, cid)` → `(pid, iid)`;**刪掉 `ops` 與 `review_queue` 兩張表**(它們不在 Drive,存在就違反「刪 db 可從 Drive 重建」——見 §4 決策 7);補一句 migration = `PRAGMA user_version` 不符就整檔丟棄重建;明寫 db 位置 = `config.Dir()/state.db`,`CAPY_CONFIG_DIR` 一併覆寫。
- §8.5:「Drive API 無用量計費」改成 quota units 模型(依 §4 決策 2 的查證結果)。
- 附錄 A:加 `pl pull/link/unlink/diff`、`export`、`import`、`drive init`、`device list/forget`、`db rebuild`,並標註哪些在 P3、哪些延後。
- 附錄 B:加一列 Drive quota units 計費時程。
- 附錄 C:新增決策 9 / 10 / 11 / 13。

**驗收**:`grep -n "PRIMARY KEY (pid, cid)\|無用量計費\|playlists/<pid>/\|base/<provider>.json\|ISRC.*主鍵" docs/ARCHITECTURE.md` 為空;CLAUDE.md 與 spec 對 Google client 歸屬、`pl pull` 方向的說法一致。

### T1 — 共用單元:檔案鎖 + keychain JSON token source

**產出**:`internal/auth/lock_unix.go`、`lock_windows.go`(build tag)、`internal/auth/tokenstore.go`。

- 鎖:`golang.org/x/sys` 已是間接依賴(v0.47.0),升 direct 零新下載。**必須用 `unix.Flock` / `windows.LockFileEx`,不可用 fcntl**——前者 per-fd,同一 process 開兩個 fd 會互斥,測試才測得到。Windows 的 `LockFileEx` 需要 byte range 與 `Overlapped`,別漏。鎖檔是空檔,放 `config.Dir()`(先 `MkdirAll`)。
- token store:`Load(key) (*oauth2.Token, error)` / `Save(key string, tok *oauth2.Token) error`,值為 JSON。**明確定義欄位**:`access_token` / `token_type` / `refresh_token` / `expiry` / `issued_at`(自己加的,`oauth2.Token` 沒有這個欄位,`ErrGoogleGrant` 要用它算 token 年齡)。**明確排除 `id_token`**(Windows Credential Manager blob 上限 2560 bytes;目前 `json.Marshal(oauth2.Token)` 不含 id_token 只是因為它藏在未匯出欄位,要顯式化)。
- 通用的 `Token()` 包裝:先看記憶體(`time.Until(Expiry) > 60s` 就直接用)→ 取鎖 → **鎖內重讀 keychain 雙重檢查**(別的 process 可能剛換好)→ 仍過期才 refresh → 寫回 → 釋放。refresh 一律包 `context.WithTimeout(30s)`(oauth2 預設用沒有 timeout 的 client,鎖內卡死會拖垮所有並行呼叫)。
- **所有 keychain 存取都必須在鎖內**——`go-keyring` 的 mock 是裸 map 無鎖,CI 跑 `-race`,鎖外存取會被 race detector 抓。

**測試**:序列建構、並行呼叫;有效 token 時 HTTP handler 直接 `t.Fatal`(證明零網路);鎖檔目錄不存在時仍成功;marshal 一個含長 access/refresh token 的樣本斷言 **< 2560 bytes**;JSON 內不含 `id_token`。

### T2 — Spotify 遷移到新 token source(issue #3 本體,決策 11)

**產出**:`internal/auth/spotify.go`、`internal/cli/doctor.go`、`internal/cli/auth.go`、`internal/cli/search.go`(確認 `SpotifyTokenSource` 簽名不變,否則這裡也要改)。

- keychain 新鍵 `spotify.token` 用 T1 的 store;讀不到時回退舊的 `spotify.refresh_token` 種下(升級路徑)。
- `doctor` ⑤⑥ 目前在同一次執行輪替兩次:⑤ 改呼叫新的 `RefreshSpotify(ctx, clientID)`(明確驗 RT 存活),⑥ 走快取 → 每次 `doctor` 只輪替一次。

**測試(未修前會 fail 的回歸測試)**:假 token 端點記住 current RT,舊 RT 一律回 `400 {"error":"invalid_grant"}`;建兩個 token source 後併發 `Token()` → 期望零錯誤且伺服器只收到 **1 次** refresh。修正前第二個必得 `invalid_grant`。另加:`doctor` 全跑一次只觸發 1 次 refresh。

### T3 — Google 登入

**產出**:`internal/auth/google.go`、`internal/cli/auth.go`(login / status / logout 各一個 case)、`internal/config/config.go`。

- 沿用 `NewLoopback(0, state)`(Google 允許動態 port、不必註冊)+ `oauth2.GenerateVerifier()` + `S256ChallengeOption`;`access_type=offline` + 首次 `prompt=consent`。token 存取**照 T1**,不要另寫一套;**不要照抄 Spotify 的輪替覆寫特例**——Google 不發新 refresh token,但寫回路徑完全相同(access token 換了本來就要寫回)。
- **測試替換點**:`googleEndpoint` 為 package var(對照 `spotify.go:36-39` 手寫 `oauth2.Endpoint` 的做法),讓測試把 TokenURL 指到 `httptest`。用手寫 endpoint,不要 import `golang.org/x/oauth2/google`(它會拉進 `cloud.google.com/go/compute/metadata`)。
- **scope 常數逐字**三個;測試斷言 **slice 與字面三元素完全相等**(不是 `Contains`),**並且**斷言授權 URL 的 `scope` query 參數逐字(對照 `spotify_test.go` 的 `TestSpotifyAuthURLParams`)——只測 slice 的話,`oauth2.Config` 組錯仍會漏。
- **downscope 拒絕**:解析 token 回應的 `scope` 欄位,缺 `drive.appdata` 即拒絕落地並提示重登。
- 錯誤歸因:`*oauth2.RetrieveError` 連同 token 年齡包成 `ErrGoogleGrant`;`invalid_grant` 且 token 年齡 < 8 天 → 訊息把「你可能忘了在 Cloud Console 按 Publish app(Testing 狀態 refresh token 7 天過期)」列為第一嫌疑。
- 內建 client:`var googleClientID, googleClientSecret string` 由 `-ldflags -X` 注入;為空時走 BYO 精靈(huh Note 六步:建專案 → 啟用 Drive API → Branding → Data Access 三個 scope → **Publish app** → 建 Desktop client 並立刻複製)。BYO 的 secret 寫 keychain `google.client_secret`,client ID 寫 config。
- **非 TTY**:`--client-id` / `--client-secret`,並提供 `CAPY_GOOGLE_CLIENT_ID` / `CAPY_GOOGLE_CLIENT_SECRET`(與 Apple 的 `CAPY_APPLE_*` 慣例一致;argv 會被 `ps` 看到,flag 說明要註記)。
- **`auth status`**:印 client ID 來源(內建 / config)、token 是否存在與到期時間。**email**:若要顯示「目前登入哪個 Google 帳號」(§5 的「登錯帳號」風險),就把 email 存進 config(非機密);**若決定不顯示,就不要從 `id_token` 取 email——取了不用是死碼**。
- **`auth logout google`**:刪 `google.token` 與 `google.client_secret`;`Use` 字串要一併從 `<spotify|apple>` 改掉。登出後 SQLite cache 的處置(保留 / 清空)要明確,因為它正是 T8 的 exit 3 觸發場景。
- **`device_id`**:在這裡出生。config 加 `device_id` 欄位(不可沿用已刪的 `install_id` 欄位名,`config_test.go` 有測試斷言舊欄位會被丟棄),首次需要時產生 ULID 並 `Save`。註明 `CAPY_CONFIG_DIR` 換目錄 = 新裝置。

**測試**:scope slice 與 URL query 逐字;downscope 被拒且不落地;`invalid_grant` + 新 token → 訊息含「Publish」;非 TTY 缺 flag 的錯誤訊息;keychain 內容不含 `id_token`;`device_id` 產生後穩定不變;`logout` 兩個鍵都刪掉。

### T4 — Drive appdata client

**產出**:`internal/drive/`(新套件)。相依:`google.golang.org/api/drive/v3`。

- **先量相依成本**(這是成本檢查,不是重設計):`go get` 後比對 `go build` 產出大小與 `go mod graph | wc -l`,結果記進本任務的報告。目前只有 6 個直接相依。
- **扁平命名 + `appProperties`**:`manifest.json`、`tracks.json`、`pl__<pid>.json`、`dev__<device_id>.json`;`appProperties` 放 `kind` / `pid` / `device_id`,用 `q` 過濾。**不要建巢狀資料夾**——Drive 不保證同資料夾內檔名唯一,兩台裝置同時 resolve-or-create 會生出兩個同名資料夾。
- `files.list` 一定要 `Fields("nextPageToken,files(id,name,version,md5Checksum,modifiedTime,appProperties)")`(預設只回四個欄位),一律迴圈 `nextPageToken`。
- 同名檔多份:**取 `modifiedTime` 最新者並印警告**,不要清理(merge 對重複檔無害;寫進註解,免得後人又加 list-before-create)。
- 錯誤映射:HTTP 401 **與** transport 層的 `*oauth2.RetrieveError`(refresh 失敗)都要 → `provider.ErrAuthExpired`,否則 cron 收到的是裸 `oauth2: cannot fetch token`;403 `storageQuotaExceeded` → 唯讀哨兵(可讀可 dry-run,拒絕上傳並明示);403 `accessNotConfigured` → 訊息印啟用 Drive API 的連結;403 `userRateLimitExceeded` 與 429 → 指數退避(**現有 `provider.Backoff` 只在 429 觸發,要擴充**)。
- 沒有樂觀鎖:v3 移除 `etag`,`files.update` 無 precondition → **不要設計成依賴 CAS**;`version` 只能事後偵測。
- **cli 層測試替換點**:`newDriveClient` 為 package var(對照 `provider.go` 的 `newProvider`),否則 T6/T8 的 e2e 接不上 fake Drive。

**測試**:`httptest` 假 Drive,要實作 **multipart upload 解析**(`/upload/drive/v3/files` 與 list 不同 base path——這是本任務最大的一塊工):create → download → update → download 的 round-trip;分頁走完;`appProperties` 過濾;同名多份取最新並警告;五種錯誤各自映射;退避有被呼叫。

### T5 — canonical model + Drive 佈局

**產出**:`internal/canon/`。**前置**:`scripts/p0/p0-1-isrc.sh` 要先跑過(見下)。

- **`cid` 的決定性**(這裡有一個必須守住的性質:cid 是 Drive 檔案裡的鍵,不能隨「當時觀測到什麼」而變):
  - 有 ISRC → `i:<正規化 ISRC>`(大寫、去連字號、非 12 碼視為缺失);沒有 → `p:<provider>:<id>`。
  - **不做「同 ISRC 但 metadata 不符就退回 `p:`」的防呆**——那會讓裝置 A(只看到一首)與裝置 B(看到衝突)算出不同的 cid,決定性就沒了。改成:偵測到衝突時**照樣用 `i:` 當鍵**,另外寫一筆 review 記錄,由使用者或 P4 處理。
  - 已知代價:spec §5.2 的「同一錄音可能有多個 ISRC(alias set)」在這個方案下無法收斂成同一個 cid,跨 provider 會產生兩筆 canonical track。**這是 P4 resolver 的工作,P3 不解**,要寫進 spec §5。
  - **前置**:P0-1 要先確認 Apple / Spotify 實際回不回 ISRC。若 library 曲目普遍沒有 ISRC,`cid` 幾乎全退到 `p:` 形式,這個設計的價值歸零,要回頭重議(列入 §4)。
- `iid`:playlist item 自己的 ULID(決策 13),重複曲目保真。
- **測試替換點**:`canon.Now func() time.Time` 與 `canon.NewULID func() string` 為 package var。`observed_at` / `updated_at` / `added_at` / `iid` 全是時間或亂數衍生,沒有替換點的話 T6/T8 的「逐位元相等」測試不可能穩定。
- 每個檔案頂層 `schema_version`;讀時忽略未知欄位;**版本高於 binary 支援即拒寫**。
- `base` 寫在各裝置自己的 `dev__<id>.json` 裡,形狀 `base[pid][provider] = {snapshot, observed_at}`,合併取 `observed_at` 最大者(LWW register)。
- track 加 `observed.{spotify,apple}`:各平台回報的原文;**Apple 的可用性依 storefront,「tw 沒有」不等於「已刪除」**;**上傳曲 / 純 library 曲沒有 ISRC 也沒有 catalog id,必須保留成 provider-only,絕不可當成已刪除**。
- **P3 不做 tombstone GC、不做「非活躍裝置忽略」規則**(評審在另一份提案裡抓到:90 天未更新的裝置檔被忽略 + 強制 re-bootstrap = 無提示的 source of truth 資料遺失)。裝置只能由使用者用 `device forget` 移除。
- **誠實記下的取捨**:`manifest.json` / `tracks.json` / `pl__<pid>.json` 仍是共享檔,兩台裝置同時寫會 last-write-wins(Drive 沒有 CAS)。P3 是單裝置為主,接受這個風險;**P5 前必須重審**。真正做到無衝突的只有 `dev__<id>.json`。

**測試**:cid 決定性(同一輸入永遠同一 cid;衝突情境下 cid 不變、只多一筆 review);`schema_version` 過高拒寫;base LWW 合併;round-trip JSON 位元相等(靠 Now/ULID 替換點)。

### T6 — SQLite cache 與重建

**產出**:`internal/store/`。相依:`modernc.org/sqlite`(spec §2 指定,純 Go 無 cgo)。

- db 位置 `config.Dir()/state.db`(`CAPY_CONFIG_DIR` 一併覆寫,測試靠它隔離)。
- 存:canonical 鏡像(tracks / playlists / items / mappings)、provider `base` 副本、resolution cache。**不存**:憑證、provider 原始 JSON、**任何不在 Drive 的東西**(`ops` 離線佇列與 `review_queue` 見 §4 決策 7;T0 已從 spec §7 刪掉)。
- Migration:`PRAGMA user_version` 不符就**整檔丟棄重建**。
- **Windows**:`os.Remove(state.db)` 在連線未關時會失敗(sharing violation);`-wal` / `-shm` 不一併刪的話舊 WAL 會 replay 進新 db,rebuild 不乾淨。刪除要先 `Close()`、三個檔一起刪,並有 Windows 上會跑到的測試。

**測試**:`TestRebuildFromDrive` —— hydrate → dump、關閉並刪除三個檔、再 hydrate → dump,**逐位元相等**。(更強的 e2e 在 T8,因為它需要 `pl pull`。)

### T7 — `pl pull` 的 DERIVE(獨立套件,fixture 驅動)

**產出**:`internal/sync/`(或 `internal/canon/derive.go`)。這是整個階段唯一沒有規格的演算法,**必須先寫清楚再寫程式**。

要定義的東西:

- **平台清單 ↔ `pid` 的連結規則**。spec 附錄 A 有 `pl link/unlink` 但計畫原本沒排。二選一並寫進 T0 的附錄 A 修訂:(a) 自動連結(依清單名稱正規化後相符)+ `pl link` 手動覆寫;(b) 只有 `pl link` 明確連結,`pull` 對未連結的清單不動作。**建議 (b)**:自動連結一旦猜錯,寫進 source of truth 就很難回頭。
- **位置序列 → `iid` 的對齊**。provider 回傳的是有序曲目位置;canonical 是 `iid` 為鍵、允許重複(決策 13)。同一首歌出現在第 5 位,是「既有 iid 移動了」還是「新的 add」?沒有這條規則就寫不出 DERIVE。建議:以 `(cid, 出現序號)` 對齊(第 n 次出現的同一 cid 對到既有的第 n 個 iid),多出來的算 add、少掉的算 remove。
- **`remove` 只計算「該 provider 有 mapping 的 cid」**。否則 cid 在某 provider 沒有 mapping 時會被讀成「使用者刪除」,每輪 add/remove 抖動。
- 輸出:一個純資料的變更集(add / remove / move / rename),不碰 IO。

**測試**:fixture 驅動的 table test —— 空 base、首次 pull、重複曲目、只換順序、平台端刪一首、平台端清單消失、cid 無 mapping。全部是純函式,沒有網路也沒有 Drive。

### T8 — `pl pull` 的 GATE / exit code / COMMIT

**產出**:`internal/cli/pl.go`(`pull` 子命令)、`cmd/capy/main.go`、`internal/cli/root.go`。

- **GATE 是唯一的寫入閘**:算變更集 → dry-run 呈現 → 閾值檢查 → 才寫。
- 非 TTY dry-run 輸出為無標題 TSV:`action provider playlist pos cid provider_id title artists reason`;TTY 用 `ui.Table` 渲染同一份資料。**首次 pull 在 TTY 下**:spec §6.6 要求「自動先 dry-run 並要求確認」——定案用 huh Confirm,非 TTY 則靠 `--yes`。
- **exit code**:`0` 無變更、`2` 有待套用變更、`3` 安全閥擋下、`1` 錯誤。
  ⚠️ **機制**:`root.go` 目前只有 `SilenceUsage`,沒有 `SilenceErrors`,`main.go` 一律 exit 1。用「回傳 error」表達 exit 2 會讓 cobra 印出 `Error: …`,但那不是錯誤。做法:加 `SilenceErrors`,由 `main.go` 自己判斷哨兵型別 → 決定 exit code 與要不要印。對照表集中在 `main.go`。
- `--yes` 跳過確認,`--force` 才越過刪除閾值;**兩者必須分開**。
- **`--yes` 絕不可放行「Drive 是空的但本機 cache 有清單」**(評審列為第一致命項):這種狀態一律 exit 3 且**零寫入**,出口是 `capy drive init --from-local`(T9)。理由:`--yes` 是 cron 的常態旗標,而「使用者兩下清空 appdata」「登錯 Google 帳號」「已知 file id 回 404」都會走到這個狀態。
- **閾值分母要寫死並測**:用「該 provider 可見的曲數」而非 canonical 總曲數,否則 5 首刪 2 首會誤判(§4 決策 3)。
- **COMMIT 順序寫死:Drive 先、SQLite 後**。反過來(先寫 cache 再上傳、上傳失敗)會讓 cache 領先 source of truth,下一次 rebuild 反而「倒退」。上傳失敗就不寫 cache,下次 pull 重算。
- 同裝置並行守門:COMMIT 用 `BEGIN IMMEDIATE`,交易內重驗 `base.observed_at` 與 FETCH 時相同,否則中止並提示「另一個 capy 正在同步」。可直接複用 T1 的鎖。

**測試**:GATE table test(閾值分母、只算有 mapping 的 remove、`--force` 才越過);四種 exit code(含 cobra 不會多印 `Error:`);Drive 空 + cache 非空 → exit 3 且零寫入;非 TTY TSV 欄位順序;**e2e 等價測試**(從 T6 移來):fake Drive + fake provider 跑兩輪 `pl pull` → 擷取 TSV 與 Drive 檔位元組 → 關閉並刪 db → 再跑兩輪 → 逐位元相等。這證明 db 裡沒有任何 Drive 沒有的資訊,順便鎖住非 TTY 輸出契約。

### T9 — `export` 與 `drive init --from-local`(逃生口)

**產出**:`internal/cli/` 兩個命令。

- **`export` 直接輸出 Drive 檔的合併形式**(不要發明第三種 JSON 形狀);spec §1.3 明列 appdata 計入使用者配額、且使用者可能清空,所以逃生口是硬需求。
- `drive init --from-local`:唯一允許在「Drive 空 / 404」狀態下寫入的命令,**只做重新上傳,永遠不對非空的 cache 做 hydrate-empty**。沒有它,T8 的 exit 3 是死路。
- **兩個命令都要定義非 TTY 行為**(要不要 `--yes`、輸出格式),spec §2 的鐵則要求每個命令都可腳本化。

**測試**:export round-trip;`drive init --from-local` 在 Drive 非空時拒絕;兩者的非 TTY 輸出。

### T10 — 發行流程(決策 9)

**產出**:`.goreleaser.yaml`、`.github/workflows/release.yml`、`README.md`。

- `-ldflags -X` 注入 `googleClientID` / `googleClientSecret`(repo secrets,**維護者要自己去 GitHub 設定**),binary 內建、repo 內不存在。
- README 要改的不只一句:命令表加 `pl pull` / `export` / `drive init` / `auth login google`;寫清楚 exit code(cron 使用者靠這個)、`--yes` 與 `--force` 的差別、Google 六步;**並註明「下載 release 零設定」只對 GitHub Releases 的 binary 成立**——`go install` 與 Homebrew 從原始碼建都沒有注入值,會走 BYO。
- **CI 可自動化的部分**:多一步 `go build -ldflags "-X ...googleClientID=ci-test"`,然後跑一個隱藏的 `capy debug google-client`(對照既有的 `debug apple-token`)斷言非空。真正的「零設定登入」只能人工驗(G-0 系列)。

---

## §4 仍待拍板

| # | 議題 | 選項 | 建議 | 何時要決定 |
|---|---|---|---|---|
| 1 | Desktop client 換 token 是否**真的**必送 `client_secret` | 文件把欄位標 Optional,豁免只列 Android/iOS/Chrome | 跑 G-0 再定 | **T3 前** |
| 2 | Drive quota 是否已改 quota units 模型與 2026 計費 | 只影響 spec 文字 | 開 limits 頁對照數字再改,別改成錯的 | **T0 前** |
| 3 | 刪除閾值公式 | A `>10 或 >30%`(spec 現況)/ B `>10 或 (>30% 且 >3)` / C 只看比例、清單 <10 首不擋 | B。A 會擋掉「5 首刪 2 首」 | **T8 前** |
| 4 | `ops` 離線佇列與 `review_queue` 兩張表 | A 從 spec §7 刪掉(它們不在 Drive,存在就違反硬約束)/ B 保留但明示「未上傳的 ops 隨 db 遺失」 | A | **T0 前** |
| 5 | 平台清單 ↔ `pid` 的連結 | A 自動連結(名稱相符)+ `pl link` 覆寫 / B 只認明確 `pl link` | B(猜錯會寫進 source of truth) | **T7 前** |
| 6 | 平台端清單被使用者刪除時 | A 傳播刪除 / B 自動 unlink + 警告 / C 下次 push 重建 | B | T7 前 |
| 7 | P0-1 若驗出 library 曲目普遍無 ISRC | 決定性 cid 幾乎全退成 `p:` 形式,跨 provider 收斂價值歸零 | 屆時重議 T5 的 cid 設計 | **T5 前** |
| 8 | `auth status` 要不要顯示 Google email | A 顯示(email 進 config,非機密)/ B 不顯示(那就別從 id_token 取) | A(§5 的「登錯帳號」風險沒有其他偵測手段) | T3 前 |
| 9 | P5 的引擎 / 順序合併 | op log + HLC vs state 三方合併 | 延到 P5,用 Drive 是否真的沒有 precondition 的實測結果決定 | P5 |

---

## §5 已知風險

| 風險 | 觸發條件 | 緩解 |
|---|---|---|
| **靜默清空平台清單** | Drive appdata 被使用者兩下清空 / 登錯 Google 帳號 / 已知 file id 回 404,而程式把「Drive 空」讀成「使用者刪光了」 | T8 的 exit 3 + 零寫入 + `drive init --from-local`;`--yes` 不得放行。驗收 G-4 |
| **cache 領先 source of truth** | COMMIT 先寫 SQLite 再上傳 Drive,上傳失敗 | T8 寫死順序:Drive 先、SQLite 後 |
| **共享檔 last-write-wins** | 兩台裝置同時寫 `manifest.json` / `tracks.json` / `pl__*.json`(Drive 沒有 CAS) | P3 單裝置為主,接受並記錄;P5 前必須重審 |
| **source of truth 資料遺失** | 若日後加入「N 天未更新的裝置檔忽略」規則 | T5 明令 P3 不做;只能由 `device forget` 移除 |
| Google client 被停用 / 6 個月無人使用被自動刪除 | 維護者端 | release binary 使用者同時壞掉;救援是請他們改用自己的 client |
| 使用者忘記按 Publish app(BYO) | Testing 狀態 | 第 7 天 `invalid_grant`;T3 用 token 年齡把它列為第一嫌疑。驗收 G-2 |
| 髒 ISRC 把兩首歌塌成同一個 cid | 決定性 cid | 照樣用 `i:` 當鍵以保決定性,另寫 review 記錄;P4 處理 |
| 內建 secret 可被 `strings` 讀出 | 決策 9 | 這是刻意放寬硬約束,PR 要簽字;BYO 路徑永遠可用 |
| Drive quota units 於 2026 稍後計費 | Google 政策 | 進附錄 B 季檢表 |

---

## §6 執行指引

### 如果只能做一半

最小可交付、且守得住硬約束的子集,依序:**T0 → T1 → T3 → T4 → T5 → T6 → T7 → T8 → T9 的 `export` 與 `drive init`**。

可延後,以及延後的代價:
- **T10 發行流程**:BYO 路徑不靠它,`go install` 的使用者已經能用;代價是 README 不能寫「零設定」。
- **T2 Spotify 遷移**:issue #3 是既有 bug,不是 P3 的前提;代價是 Spotify 仍有並行 `invalid_grant`,`pl pull` 的文件要警告「勿與其他 capy 命令並行」。
- `import`、`device forget`、`device list`、`pl diff`、`db rebuild` 命令(有 `TestRebuildFromDrive` 證明可重建即可,命令只是便利)。

### 新 session 怎麼開始

1. 讀本文件 §0 → §1 → §3。spec(`docs/ARCHITECTURE.md`)是設計權威,本文件是排程與決策權威;**兩者衝突時以 §1 的決策為準,並由 T0 把 spec 改過來**。
2. 開分支(不要在 main 上動工),先做 **T0**,單獨一個 docs commit。T0 的 PR 描述要明寫「CLAUDE.md 硬約束依決策 9 放寬」。
3. 先把 §4 標「T0 前 / T3 前 / T5 前 / T8 前」的決策清掉,再開對應任務。
4. T1–T10 用 `superpowers:subagent-driven-development` 執行:每個 task 一個實作 subagent → task review → fix loop → 最後全分支 review。實作者的 model 用 fable(本次已驗證可用)。
5. 每個 task 都要 TDD;bug fix 必附「未修前會 fail」的回歸測試(全域 CLAUDE.md 硬規則)。
6. CI 是 macOS + Windows 雙矩陣;`GOOS=windows go vet ./...` 在本機先跑過再推,但注意 **vet 抓不到 Windows 的檔案 sharing violation**,SQLite 刪檔那條要靠 CI 真的跑。
7. 開 PR 到 main,描述含 `## Summary` 與 `## Test plan`;`gh` 要用全路徑 `/opt/homebrew/bin/gh`。
8. §2 與 §2.1 的驗收要維護者本人跑,不要讓 subagent 假裝跑過。
