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

## §3 任務切分

三份獨立架構提案經三位評審評分後,以「可腳本化優先」那份為骨幹(8 / 8 / 5.5),再移植另外兩份的具體點子。評審抓到的三個致命問題已在下列任務中修掉,列在 §5。

依賴順序:**T0 必須最先**(其他任務都照它改後的 spec 做);T1 與 T2 可並行;T3→T4→T5→T6→T7 為主鏈;T8 任何時候都能做。

### T0 — 文件對齊(只改 docs,不動程式)

**產出**:`CLAUDE.md`、`docs/ARCHITECTURE.md`。
- CLAUDE.md 決策 8 措辭:把「credential 全面 BYO」改寫成「**使用者的**憑證全面 BYO;app 自身識別(Google OAuth client)由專案內建但在發行時注入,不進 repo」,並補一句 client secret 只進 keychain。
- spec §4.4:補上決策 9 的內建/BYO 雙路徑、`client_secret` 必送、refresh token **不輪替**、Testing 7 天陷阱與 `drive.appdata` 不在豁免清單。
- spec §6.2:`items[]` 元素加 `iid`(決策 13);`cid` 改為**決定性 ID**(見 T4)。
- spec §6.3:把巢狀目錄樹改成**扁平檔名 + `appProperties`**(Drive 不保證同資料夾檔名唯一);`base` 從共享檔改成寫在各裝置自己的檔案裡。
- spec §7:`playlist_items` 主鍵 `(pid, cid)` → `(pid, iid)`;加一句 migration 策略 = `PRAGMA user_version` 不符就整檔丟棄重建。
- spec §8.5:「Drive API 無用量計費」已過時,改成 quota units 模型並註明「每日 4 億單位以上計畫於 2026 稍後計費」。
- spec 附錄 B 監控表加一列:Drive quota units 計費時程。
- spec 附錄 C 新增決策 9 / 10 / 11 / 13。

**驗收**:`grep` 不到殘留的舊敘述(`PRIMARY KEY (pid, cid)`、`無用量計費`、巢狀 `playlists/<pid>/` 樹);CLAUDE.md 與 spec 對 Google client 歸屬的說法一致。

### T1 — issue #3:token 進 keychain + 跨程序鎖(決策 11)

**產出**:`internal/auth/spotify.go`、新增 `internal/auth/flock_unix.go` / `flock_windows.go`、`internal/cli/doctor.go`、`internal/cli/auth.go`。

- keychain 新鍵 `spotify.token`,值 = `json.Marshal(*oauth2.Token)`(`access_token` / `token_type` / `refresh_token` / `expiry`)。讀不到時回退舊的 `spotify.refresh_token` 種下(升級路徑)。
- `Token()` 改成:先看記憶體;`time.Until(Expiry) > 60s` 就直接用;否則取檔案鎖 → **鎖內重讀 keychain 雙重檢查**(別的 process 可能剛換好)→ 仍過期才 refresh → 寫回 → 釋放。refresh 一律包 `context.WithTimeout(30s)`(oauth2 預設用沒有 timeout 的 client,鎖內卡死會拖垮所有並行呼叫)。
- 檔案鎖:`golang.org/x/sys` 已是間接依賴(v0.47.0),升為 direct 零新下載。**必須用 `unix.Flock` / `windows.LockFileEx`,不可用 fcntl**——前者是 per-fd,同一個 process 開兩個 fd 會互斥,測試才測得到。鎖檔放 `config.Dir()`(空檔,先 `MkdirAll`),不含任何憑證。
- `doctor` ⑤⑥ 目前在同一次執行輪替兩次:⑤ 改呼叫新的 `RefreshSpotify(ctx, clientID)`(明確驗 RT 存活),⑥ 走快取 → 每次 `doctor` 只輪替一次。

**測試**(`internal/auth/spotify_test.go`;mock keyring 是裸 map,token source 要**序列建構、並行呼叫**):
- **未修前會 fail 的回歸測試**:假 token 端點記住 current RT,舊 RT 一律回 `400 {"error":"invalid_grant"}`;建兩個 token source 後併發 `Token()` → 期望零錯誤且伺服器只收到 **1 次** refresh。修正前第二個必得 `invalid_grant`。
- keychain 有有效 access token 時,HTTP handler 直接 `t.Fatal`(證明零網路)。
- `doctor` 全跑一次只觸發 1 次 refresh。
- 鎖檔目錄不存在時仍能成功。

### T2 — Google 登入

**產出**:`internal/auth/google.go`、`internal/cli/auth.go`(login/status/logout 各加一個 case)、`internal/cli/doctor.go`(googleChecks)、`internal/config/config.go`(`GoogleClientID`)。

- 沿用 `NewLoopback(0, state)`(Google 允許動態 port、不必註冊)+ `oauth2.GenerateVerifier()` + `S256ChallengeOption`;`access_type=offline` + 首次 `prompt=consent`。
- **scope 常數逐字**三個,測試斷言 **slice 與字面三元素完全相等**(不是 `Contains` 子集)——這是 CLAUDE.md「任何人提議加 Gmail scope 都要擋」真正會擋人的地方。
- **downscope 拒絕**:解析 token 回應的 `scope` 欄位,缺 `drive.appdata`(使用者在同意畫面取消勾選)即拒絕落地並提示重登。
- **`id_token` 取出 email 後立刻丟棄**,不寫進 keychain(Windows Credential Manager blob 上限 2560B;目前 `json.Marshal(oauth2.Token)` 不含 id_token 只是因為它藏在未匯出欄位,要顯式化)。
- token 存取形狀**照 T1**(`google.token` 單鍵 JSON + 同一組鎖 helper)。**不要照抄 Spotify 的輪替覆寫邏輯——Google 的 refresh token 不輪替。**
- 錯誤歸因:把 `*oauth2.RetrieveError` 連同 token 的 `issued_at` 包成 `ErrGoogleGrant`;`invalid_grant` 且 token 年齡 < 8 天 → 訊息把「你可能忘了在 Google Cloud Console 按 Publish app(Testing 狀態 refresh token 7 天過期)」列為第一嫌疑。
- 內建 client:`var googleClientID, googleClientSecret string`,由 `-ldflags -X` 注入;為空時精靈走 BYO(huh Note 六步:建專案 → 啟用 Drive API → Branding → Data Access 三個 scope → **Publish app** → 建 Desktop client 並立刻複製 ID 與 secret)。BYO 的 secret 寫 keychain `google.client_secret`,ID 寫 config。
- 非 TTY:`--client-id` / `--client-secret` flag,缺任一即錯誤訊息列出取得步驟。

**測試**:scope slice 逐字相等;downscope 回應被拒且不落地;`invalid_grant` + 新 token → 訊息含「Publish」;非 TTY 缺 flag 的錯誤訊息;登入成功後 keychain 有 `google.token`、config 有 client ID、**keychain 內容不含 `id_token`**。

### T3 — Drive appdata client

**產出**:`internal/drive/`(新套件)。相依:`google.golang.org/api/drive/v3`。

- **扁平命名 + `appProperties`**:檔名 `manifest.json`、`tracks.json`、`pl__<pid>.json`、`dev__<device_id>.json`;`appProperties` 放 `kind` / `pid` / `device_id`,用 `q` 過濾。**不要建巢狀資料夾**——Drive 不保證同資料夾內檔名唯一,兩台裝置同時 resolve-or-create 會生出兩個同名資料夾。
- `files.list` 一定要 `Fields("nextPageToken,files(id,name,version,md5Checksum,modifiedTime,appProperties)")`(預設只回四個欄位),並且一律迴圈 `nextPageToken`。
- 同名檔出現多份時:**取 `modifiedTime` 最新者並印警告**,不要試圖清理(merge 對重複檔無害;寫進註解,免得後人又加 list-before-create)。
- 錯誤映射:401 → `provider.ErrAuthExpired`;403 `storageQuotaExceeded` → 唯讀模式哨兵(可讀可 dry-run,拒絕上傳並明示原因);403 `accessNotConfigured` → 訊息印出啟用 Drive API 的連結;403 `userRateLimitExceeded` 與 429 → 指數退避。**現有的 `provider.Backoff` 只在 429 觸發,Drive 的 403 形式不會觸發,要擴充。**
- 沒有樂觀鎖:v3 移除了 `etag`,`files.update` 沒有任何 precondition 參數 → **不要設計成依賴 CAS**。`version` 只能事後偵測 lost update。

**測試**:`httptest` 假 Drive:分頁走完、`appProperties` 過濾、同名多份取最新並警告、四種錯誤各自映射正確、退避有被呼叫。

### T4 — canonical model + Drive 佈局

**產出**:`internal/canon/`(資料模型 + JSON schema)、Drive 佈局的讀寫(manifest、device 註冊)。

- **決定性 `cid`**(移植自「正確性」提案,取代 spec 的 ULID):有 ISRC 用 `i:<ISRC>`,否則 `p:<provider>:<id>`。好處:兩台裝置、兩個 provider 不需要 resolver 就會對同一首歌收斂到同一個 cid,讓 P4 後半的 fuzzy 比對可以整塊延後。
  防呆:同一個 `i:<ISRC>` 但 title / duration 明顯不同 → 退回 `p:` 形式並記進 review,避免髒 ISRC 把兩首歌塌成同一個 cid 寫進 source of truth。
- `iid`:playlist item 自己的 ULID(決策 13),重複曲目保真。
- 每個檔案頂層 `schema_version`;讀時忽略未知欄位,**檔案版本高於 binary 支援即拒寫**。
- `base`(上次觀測到的平台狀態)寫在**各裝置自己的 `dev__<id>.json` 裡**,形狀 `base[pid][provider] = {snapshot, observed_at}`,合併時取 `observed_at` 最大者(LWW register)。這樣沒有共享寫入點,也讓「刪掉 db 從 Drive 重建」不會退化成只加不刪。
- track 加 `observed.{spotify,apple}`:各平台回報的原文(title/artists/album/duration_ms/provider_id/storefront),重新解析時不必再打 API;**Apple 的可用性依 storefront,「tw 沒有」不等於「已刪除」**。
- **P3 不做 tombstone GC、不做「非活躍裝置忽略」規則**。評審在「正確性」提案裡抓到:90 天未更新的裝置檔被忽略 + 下次強制 re-bootstrap = 無提示的 source of truth 資料遺失(links / pins / tombstone 消失,tombstone 消失會讓 P5 push 時已刪曲目復活)。裝置只能由使用者用 `capy device forget` 明確移除。

**測試**:cid 決定性(同一首歌兩個 provider → 同一 cid);髒 ISRC 防呆;`schema_version` 過高拒寫;base LWW 合併;round-trip JSON。

### T5 — SQLite cache 與重建

**產出**:`internal/store/`。相依:`modernc.org/sqlite`(spec §2 指定,純 Go、無 cgo)。

- 存:canonical 鏡像(tracks / playlists / items / mappings)、provider `base` 副本、resolution cache。**不存**:任何憑證、provider 原始 JSON。
- Migration:`PRAGMA user_version` 不符就**整檔丟棄重建**——它是 cache,不維護 ALTER 鏈。
- `capy db rebuild` = 刪檔 + 從 Drive hydrate。

**測試(這是 CLAUDE.md 硬約束,必須有)**:
- `TestRebuildFromDrive`:hydrate → dump、`os.Remove(db)`、再 hydrate → dump,**逐位元相等**。
- **更強的 e2e 等價測試**(移植自「極簡」提案):fake Drive + fake provider 跑兩輪 `pl pull` → 擷取非 TTY TSV 輸出與 Drive 檔位元組 → `rm db` → 再跑兩輪 → 兩邊逐位元相等。這證明 db 裡沒有任何 Drive 沒有的資訊,順便把非 TTY 輸出契約一起鎖住。

### T6 — `pl pull` + 安全閘 + `pl diff`

**產出**:`internal/cli/pl.go`(`pull` / `diff` 子命令)、`cmd/capy/main.go`(exit code 對照)。

流程:FETCH(Drive)→ OBSERVE(provider,分頁)→ DERIVE(對 base 算 diff)→ GATE → COMMIT(寫 Drive + SQLite)。

- **GATE 是唯一的寫入閘**:先算變更集 → dry-run 呈現 → 過閾值檢查 → 才寫。
- **`remove` 只計算「該 provider 有 mapping 的 cid」**。否則 cid 在某 provider 沒有 mapping 時會被讀成「使用者刪除」,每輪 add/remove 抖動。
- 非 TTY dry-run 輸出為無標題 TSV:`action provider playlist pos cid provider_id title artists reason`;TTY 用 `ui.Table` 渲染同一份資料。
- **exit code**:`0` 無變更、`2` 有待套用變更(dry-run 或非 TTY 無 `--yes`)、`3` 安全閥擋下(閾值 / ambiguous)、`1` 錯誤。對照表集中在 `cmd/capy/main.go`(目前一律 exit 1,加一個 switch)。cron 只在 `3` 需要找人。
- `--yes` 跳過確認,`--force` 才越過刪除閾值;**兩者必須分開**。
- **`--yes` 絕不可放行「Drive 是空的但本機 cache 有清單」**(評審列為致命):這種狀態一律 exit 3,出口是獨立命令 `capy drive init --from-local`(見 T7)。理由:`--yes` 是 cron 的常態旗標,而「使用者在 Google 帳號設定裡兩下清空 appdata」「登錯 Google 帳號」「已知 file id 回 404」全都會走到這個狀態,不能在沒人看的時候自動續行。
- `pl diff <name>`:唯讀預覽,零 API 寫入、零 Drive 寫入,exit 2。
- 同裝置並行守門:COMMIT 用 `BEGIN IMMEDIATE`,交易內重驗 `base.observed_at` 與 FETCH 時相同,否則中止並提示「另一個 capy 正在同步」。可直接複用 T1 的鎖 helper。

**測試**:GATE 的 table test(閾值分母、只算有 mapping 的 remove、`--force` 才越過);exit code 四種;Drive 空 + cache 非空 → exit 3 且**零寫入**;非 TTY TSV 欄位順序;分頁走完。

### T7 — `export` / `import` / `drive init` / `device forget`

**產出**:`internal/cli/` 四個命令。

- **`export` 直接輸出 Drive 檔的合併形式,`import` 反向拆檔**——不要再發明第三種 JSON 形狀,round-trip 位元相等測試就免費了。
- **`import` 必須走 T6 同一個 GATE**(先與現有 canonical 算 diff;含 remove 就過閾值與 dry-run)。
- `drive init --from-local`:唯一允許在「Drive 空 / 404」狀態下寫入的命令,而且**只做重新上傳,永遠不對非空的 cache 做 hydrate-empty**。
- `device forget <device_id>`:唯一移除裝置檔的路徑。

**測試**:export → import → export 位元相等;import 含刪除時被 GATE 擋下;`drive init --from-local` 在 Drive 非空時拒絕。

### T8 — 發行流程(決策 9)

**產出**:`.goreleaser.yaml`、`.github/workflows/release.yml`、README。

- `-ldflags -X` 注入 `googleClientID` / `googleClientSecret`(CI secret),binary 內建、**repo 內不存在**。
- README 明寫:下載 release 的人零設定;`go install` 從原始碼建的人沒有內建憑證,會走 BYO 精靈。
- 驗收:CI 產出的 binary 跑 `auth login google` 不需要任何 flag;本機 `go build` 的 binary 走 BYO 精靈。

---

## §4 仍待拍板(不擋 T0–T8,但要在對應階段前決定)

| # | 議題 | 選項 | 建議 |
|---|---|---|---|
| 1 | Desktop client 換 token 是否**真的**必須帶 `client_secret` | 官方文件把該欄位標 Optional,豁免只列 Android/iOS/Chrome,語意矛盾。**一個 curl 就能測** | 先測。若不必帶,BYO 摩擦少一半、keychain 少一個鍵 |
| 2 | Drive `files.update` 是否真的不吃 `If-Match` | 文件已移除 etag,但**一個 curl 能確認**有沒有 412 | 先測;若意外可用,P5 的分裝置檔可以簡化 |
| 3 | 刪除閾值公式 | A `>10 或 >30%`(spec 現況)/ B `>10 或 (>30% 且 >3)` / C 只看比例、清單 <10 首不擋 | B。A 會擋掉「5 首刪 2 首」這種正常操作 |
| 4 | 平台端清單被使用者刪除時 | A 傳播刪除到 canonical 與另一平台 / B 自動 unlink + 警告 / C 下次 push 重建 | B(預設最不傷) |
| 5 | P5 的引擎 | A 分裝置 op log + HLC(spec §6.3)/ B state 三方合併 + tombstone 集合 / C 先只做 P4,等真的有第二台裝置再說 | C,然後在 P5 開工時用 §4-2 的結果決定 A/B |
| 6 | 順序合併 | A fractional rank 逐曲 / B 整份順序 LWW / C 集合合併、順序取最後寫者 | 延到 P5 |
| 7 | 離線佇列(`ops.synced=0`)要不要留 | A 移除(provider 本來就要網路)/ B 保留但明示「未上傳的 ops 隨 db 遺失」 | A。它是 SQLite 裡唯一不可從 Drive 重建的東西,留著就破壞硬約束 |

---

## §5 已知風險

| 風險 | 觸發條件 | 緩解 |
|---|---|---|
| **靜默清空平台清單** | Drive appdata 被使用者兩下清空 / 登錯 Google 帳號 / 已知 file id 回 404,而程式把「Drive 空」讀成「使用者刪光了」 | T6 的 exit 3 + `drive init --from-local`;`--yes` 不得放行。這是評審列的第一致命項 |
| **source of truth 資料遺失** | 若日後加入「N 天未更新的裝置檔忽略」規則 | T4 明令 P3 不做;裝置只能由 `device forget` 移除 |
| Google client 被停用 / 6 個月無人使用被自動刪除 | 維護者端 | 所有 release binary 使用者同時壞掉;救援路徑是請他們改用自己的 client(BYO 路徑一直都在) |
| 使用者忘記按 Publish app(BYO) | Testing 狀態 | 第 7 天 `invalid_grant`;T2 的 `ErrGoogleGrant` 用 token 年齡把這件事列為第一嫌疑 |
| 髒 ISRC 把兩首歌塌成同一個 cid | 決定性 cid + 錯誤的 ISRC metadata | T4 的 title/duration 防呆 + 退回 `p:` 形式 |
| 上傳曲 / 純 library 曲沒有 ISRC 也沒有 catalog id | Apple library | canonical 保留成 provider-only,**絕不可當成已刪除** |
| Drive quota units 於 2026 稍後開始計費 | Google 政策 | 已進附錄 B 季檢表;個人用量遠低於門檻 |

---

## §6 新 session 怎麼開始

1. 讀這份文件的 §0 → §1 → §3。spec(`docs/ARCHITECTURE.md`)是設計權威,這份是排程與決策權威;**兩者衝突時以本文件 §1 的決策為準,並在 T0 把 spec 改過來**。
2. 開分支(不要在 main 上動工),先做 **T0**,單獨一個 docs commit。
3. T1–T8 用 `superpowers:subagent-driven-development` 執行:每個 task 一個實作 subagent → task review → fix loop → 最後全分支 review。實作者的 model 用 fable(本次驗證可用)。
4. 每個 task 都要 TDD,bug fix 必附「未修前會 fail」的回歸測試(全域 CLAUDE.md 硬規則)。
5. CI 是 macOS + Windows 雙矩陣;`GOOS=windows go vet ./...` 在本機先跑過再推。
6. 開 PR 到 main,描述含 `## Summary` 與 `## Test plan`;`gh` 要用全路徑 `/opt/homebrew/bin/gh`。
7. §2 的驗收要維護者本人跑,不要讓 subagent 假裝跑過。
