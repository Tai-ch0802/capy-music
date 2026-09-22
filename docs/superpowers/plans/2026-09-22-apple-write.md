# Apple Music 寫入:Spotify → Apple 搬家、雙向同步(2026-09-22 研究與計畫)

使用者:「是時候要來研究 apple music 的 api 了,我們需要把 spotify 歌單搬移至 apple music 的這段也開發時做出來才行。
先徹底研究一下做法和可行性,整理出一份 plan 之後我們再來討論,確定做法以後才進行實作。」

這份文件是研究結論 + 計畫。2026-09-22 使用者拍板 §6 全部建議、R-8 探測跑完(§5 結果)、T1 + T2 同一個 PR 實作完成(§4 表下方)。

---

## 0. 現況(查證過,2026-09-22)

- **憑證**:本機 keychain 已有 Apple web developer token(有效至 2026-10-22)與 Music User Token,storefront `tw`(`capy auth status`)。
  讀端(search / ISRC 反查 / library 清單 / 曲目)P4 真帳號驗過(ISRC 覆蓋 2525/2553)。
- **程式**:`internal/provider/apple/client.go` 的 `do()` 沒有 request body 參數;`Provider` 沒實作 `PlaylistWriter` / `PlaylistCreator`;
  `Caps()` 不含任何寫入能力;`LibraryPlaylistTracks` 把 `ProviderID` 蓋成 catalog id(有 catalog 對應時),**清單列自己的 id(`a.…` / `i.…`)被丟掉**;
  Apple 讀端沒有任何地方設 `Unpushable`(grep 為零;計畫 P5 Q22 說要標,沒做)。
- **CLI 裡把 Apple 當只讀的特例**:`push.go`(`--all` 跳過 Apple 並印「P0-2 待驗證」、`--provider apple` exit 1)、`sync.go`(`--provider apple` 降級成只 pull)、
  `migrate.go`(「目標不能是 Apple」)、`dedup.go`(Apple 只報告)、`provider.go` 的 `asPlaylistWriter` 錯誤訊息。
  這些特例全部靠 `Caps()` / 型別斷言判斷,**Apple 宣告能力後自動消失**,不必逐一拆。
- **文件裡宣稱 Apple 只讀的地方**(CLAUDE.md 硬約束:網站能力描述要跟程式一致,同一個 PR 改):
  README 六處(§ 命令表 push / dedup、push 段、dedup 段、migrate 段、「反方向做不到」)、`docs/guide.html:588`「Apple Music 目前只讀不寫」整節、
  `site/public/index.html` 與 `site/public/en/index.html` 首頁能力句、ARCHITECTURE §1.2 表、§3 註解、§6.5.2 規則 6、附錄 C 決策 30、§9 P5。
  **條款兩份也要改**(PR #79 review):`site/public/{,en/}terms.html:42`「Apple Music 目前只能讀取」、`:52`「只讀寫你帳號裡的播放清單與播放狀態」要補「加進 Apple Music 清單的曲目會同時進資料庫」;
  首頁每語系兩句(`:45` 能力句、`:149` 憑證段)加 `:7` / `:14` meta 的單向措辭一起改;指南不只 `:588` 一節,`:427` dedup、`:517` migrate、`:552` 反方向也要。
  隱私權政策不必動(沒有新的外部服務、沒有新的 Google scope)。`site/site_test.go` 只釘得住 scope,這些句子漏了 CI 不會叫,只能靠這份清單。
- **既有探測腳本** `scripts/p0/p0-2-playlist-ops.sh`(2026-09-01 寫的)三個候選端點有兩個是猜的(`DELETE …/tracks/{id}`、帶 body 的 DELETE),
  又叫維護者手動刪測試清單——本計畫 §5 重寫它。

## 1. 研究結論:amp-api 的寫入端點

主機 `https://amp-api.music.apple.com/v1`、標頭 `Authorization: Bearer <web dev token>` + `Media-User-Token` + `Origin: https://music.apple.com`,
**與現在讀端完全同一組**;不多要任何憑證、不多接任何服務,風險類別與決策 8 相同。

| 操作 | 方法與路徑 | body / query | 回應 | 佐證 |
|---|---|---|---|---|
| 建清單 | `POST /me/library/playlists` | `{"attributes":{"name","description","isPublic":false},"relationships":{"tracks":{"data":[{"id":"<catalog>","type":"songs"}]}}}`(tracks 選填) | 201,`data[0].id` = `p.…`、`canEdit:true` | **Apple 官方文件**;Z-Han-Z、Cider 1.x、kopuz、resonance 四個獨立實作同形 |
| 加曲目(尾端 append) | `POST /me/library/playlists/{id}/tracks` | `{"data":[{"id":"<catalog>","type":"songs"}]}`;`type` 也可 `library-songs`(`i.…` / `a.…`);**漏 `type` 會 2xx 但曲目靜默丟掉**(kopuz) | 204;帶 `?representation=resources` 回 200 含新列(kopuz) | **Apple 官方文件**(允許 type:`songs` / `library-songs` / `music-videos` / `library-music-videos`);社群上限 100 首/次;Z-Han-Z 實測順序正確 |
| 整批取代 / 重排 | `PUT /me/library/playlists/{id}/tracks` | `{"data":[{"id":"<清單列 id>","type":"library-songs"},…]}`,陣列順序 = 新順序,**全量取代**(沒列的就沒了) | 204 | 非官方:網頁播放器自己的呼叫;resonance 用它做重排與逐列移除、ai-ecoverse endpoints 參考、Parachord 註解「Cider 靠它做 full-replace」 |
| 移除單列 | `DELETE /me/library/playlists/{id}/tracks?ids[library-songs]=<列 id>&mode=all` | 無 body;`mode=all` 必帶(缺了 400「No mode supplied」) | 204 | 非官方:epheterson、kopuz、resonance、Sonora、Cider 外掛五個實作同形;**本計畫不用它**(見 §3) |
| 改名 | `PATCH /me/library/playlists/{id}` | `{"attributes":{"name":"…"}}`(resonance 連 description、isPublic 一起送;Cider 1.x 只送 name) | 204 | 非官方:Cider 1.x、epheterson、resonance |
| 刪清單 | `DELETE /me/library/playlists/{id}` | — | 204(amp-api);官方主機 401 | 非官方多來源;**只用於探測腳本清場**(決策 30 不採 rebuild 不變) |

### 1.1 真帳號唯讀探測(2026-09-22,只打 GET,零寫入)

- 25 個 library 清單,**7 個 `canEdit:true`,恰好是全部使用者自建的**(4 個沒有 globalId = Music.app 建的、3 個 `pl.u-…`);
  18 個 `false` = Apple 精選(globalId `pl.<hash>`、有 Apple 的描述)+「喜好歌曲」+「已購買的音樂」。
  → 對 web token 來說 `canEdit` 就是「是不是你自己的清單」,**正是 §8 ToS 保守做法「只寫自建 library 清單」的機器判準**。
  (epheterson 說「canEdit 只看建立者、Music.app 建的寫不了」是**自簽 developer token** 的情況;web token 是網頁播放器本人,不受此限。)
- 清單本體 attributes 有 `canEdit`、`canDelete`、`hasCollaboration`、`playParams.versionHash`(每次內容變動可能會變——R-8 觀察項)。
- `/tracks` 的列 id 全是 `a.<catalogId>`(100/100),`relationships.catalog` 有 catalog id;`meta.total` 有值(現在 `PlaylistRef.Total` 填 -1,可順手補)。
  這代表**同一首加兩次,列 id 可能相同**——與 kopuz 註解「兩列兩個 id(`i.…`)」矛盾,Cider #1915 又報「刪一份會全刪」。→ R-8 判定項(§5 第 2 項)。

### 1.2 會咬人的事(全部有來源)

1. **`canEdit:false` 的清單寫入回 500「Unable to update tracks」**(epheterson 實測),不是 403。寫入前要自己查,給明確訊息。
2. **新清單有 iCloud 傳播延遲**:建完後幾秒到幾十秒才出現在 `/me/library/playlists` 列表(Z-Han-Z)。`pull.go:824` 的 gone 判準是「不在 ListPlaylists 裡」
   → `pl link --create` / `migrate` 建完 30 秒內跑 `pl pull` 會把新清單當成已刪。修法在 §3。
3. **429 沒有 `Retry-After`、滾動約 60 分鐘窗口**(epheterson、Z-Han-Z;撞到的都是模糊搜尋)。現有 `Backoff` 沒標頭時退避 1/2/4 秒再放棄——對這種窗口等於白等;
   Apple 的 429 應快速失敗、訊息講「約一小時後再試」。校準:P4 真帳號 2553 次 `filter[isrc]` 反查沒撞過。
4. **不要送 `x-apple-client-version`**(amp-api 回 500);回應可能 gzip(Go 的 Transport 自己加 `Accept-Encoding` 時會自動解,現有讀端已證明沒事)。
5. **`DELETE …/tracks` 不帶 `ids[…]` 會清空整個清單**(kopuz 測試註解)。本計畫連帶 ids 的版本都不用,測試斷言 fake server **從未**收到 `DELETE …/tracks`。
6. **加進清單的曲目可能同時進曲庫**(Z-Han-Z:「建清單也會把曲目加進資料庫」)。是否成立、id 是 `i.`(進曲庫)還是 `a.`(只在清單)→ R-8 第 4 項;成立就寫進揭露。
7. **官方主機 `api.music.apple.com` 對 DELETE / PUT / PATCH 一律 401**(論壇 107807、813068;Parachord 因此宣告 Apple 不支援移除)。我們一直用 amp-api,不受影響;
   但這也說明:**移除 / 重排 / 改名走的是 Apple 沒承諾的私有端點**,Apple 改了會壞、測試看不出來,只有真帳號驗收看得到(同 R-6 的風險模式)。

## 2. 可行性判定

- **Spotify → Apple 搬家(使用者要的那條路)**:`migrate` 的契約是「對目標只做新增、接在尾端」,只需 **建清單 + append**,兩者都是 **Apple 官方文件化**的端點,四個獨立實作同形。
  **可行,且不必等任何寫入探測。**
- **雙向同步(remove / move / rename)**:走網頁播放器自己的私有端點(PUT 整批取代 + PATCH),多來源佐證。**可行**,但兩件事必須先由 R-8 判定:
  PUT 能否混用 `songs`(新曲 catalog id)與 `library-songs`(既有列)、以及重複曲目在 PUT 下的行為。
- **不採 rebuild**(決策 30 這半句保留):現在有 PUT,rebuild 沒有存在理由。
- **ToS**:只寫 `canEdit:true` 的清單 = 只寫使用者自建的清單;不碰 Apple 精選、不碰喜好歌曲。灰色地帶沒有變深,只是從「讀」擴到「讀寫自己的清單」,揭露文字要跟上。

## 3. 設計(最懶、不改 SPI)

### 3.1 `client.go`

- `do()` 加 `body any` 參數(有 body 就 `Content-Type: application/json`),簽名照 Spotify 抄;`out == nil || 204` 不 decode。
- 新增:`CreatePlaylist(name)`(`isPublic:false`、不帶 tracks、不帶 description)、`AddTracks(id, ids)`(每批 100、依 id 前綴選 `songs` / `library-songs`)、
  `ReplaceTracks(id, entryIDs)`(PUT,`library-songs`)、`Rename(id, name)`(PATCH,**只送 name**;R-8 第 5 項確認 description 不會被清)、
  `Playlist(id)`(GET 本體:`canEdit` / `versionHash` / name)、`playlistEntries(id)`(重讀 `/tracks`,回列 id 與 catalog id 兩欄)。
- 錯誤映射:`canEdit:false` → 寫入前擋下,訊息「Apple 只讓你編輯自己建的清單(這是 Apple 精選 / 喜好歌曲 / 已購買)」;500 且訊息含「Unable to update」→ 同一句(第二道防線);
  429 → 不重試,訊息講明「Apple 網頁 token 的配額是共用的,約一小時後再試」。

### 3.2 `apple.go`:`ApplyOps` / `Pushable` / `CreatePlaylist` / `Caps`

```
ApplyOps(ctx, id, current, ops):
  want, name := provider.ApplyPlaylistOps(current, ops)        // 純函式,越界 / 核對不符先錯、零寫入
  pl := GET 本體;!canEdit → 錯,零寫入
  name != "" → PATCH(先做便宜的;失敗回錯,renamed=false)
  want == current → 回
  pure append(want[:len(current)] == current)→ POST 尾端多出來的,每批 100        // migrate 走這條;T1
        // 這條不重讀:規則 6 的併發比對由 push.go 的 apply 進場那次重讀承擔(就在 ApplyOps 之前);append 是純加法、不會刪到東西,
        // 最差是手機同時加了同一首而多一份重複(下一次 pull 看得到);多讀一次只會放大讀取量(429 不重試)
  否則 → 重讀 /tracks 取列 id;ProviderID 序列 != current → 錯「平台已變」零寫入   // 這次重讀就是 §6.5.2 規則 6 的併發比對
        // 對齊用的 ProviderID 必須是 LibraryPlaylistTracks 同一套規則(有 catalog 取 catalog id、否則列 id),library-only 曲目才不會被誤判成「平台已變」
        → 一次 PUT:既有列用列 id + `library-songs`、新曲用 catalog id + `songs`(R-8 第 1 項已證明混型可過);T2
  失敗語意:PUT 是伺服器端整批、沒有半截 → Apple 不會回 *PartialWriteError;pure append 分批 POST 才會(第 2 批起失敗,Written = 已 append 的數)
Pushable(id): id != ""(catalog 數字 id 與 i./a. 列 id 都推得動——官方 type 允許 library-songs;跟 Spotify local file 不同)
CreatePlaylist(name): POST → 只輪詢列表直到出現才回傳(gone 判準看列表,pull.go:824;本體 2 s 就 200、不必查);間隔退避 1 → 2 → 4 → 8 → 15 s(共 30 s、最多 6 次列表 GET,
        不用 1 Hz × 30 次去撞共用配額;R-8 量到 9 s);逾時回錯並帶 id 與 pl link 接回命令——不能默默回傳成功:migrate 接著 observe 會把不在列表的清單當 gone 並取消連結,
        pl link --create 會連上一個 pull 視為已刪的 id → CLI 一行不改也不會把新清單當 gone
Caps(): T1 加 CapPlaylistCreate|CapPlaylistAppend;T2 依 R-8 加 Remove|Reorder|Rename
```

- **T1 期間** remove / move / rename 回 `skipped`(CLI 已會印「手動」),`Caps()` 不宣告 → `planPush` 不會排 rename 給 Apple(A13)。
- **PUT 空序列 = 清空**:與 Spotify 一樣是唯一一條刪光的路;沿用同一段警語,閘(dry-run、`removalBlocked`、確認)在 CLI 端已在。
- 讀端順手:`Track` 加 `EntryID`?**不加**(SPI 不動;`ApplyOps` 自己重讀)。`PlaylistRef.Total` 改填 `meta.total`(小、獨立、可不做)。
- `Unpushable`:Apple 不設。library-only 曲目(`i.…` 無 catalog)在同帳號內推得動;它只是「跨平台沒 ISRC」,那是 resolver 的事。

### 3.3 CLI

- 不新增命令。`migrate --to apple` / `pl link <名> apple --create` / `pl push --provider apple` / `pl sync` / `pl dedup` 全由能力位元自動放行。
- 改的是文字:`asPlaylistWriter` 的「P0-2 待驗證」、push / sync 的 Apple 註解、migrate Long 的「目標不能是 Apple」、dedup 的「Apple 只讀」。
- **`canEdit:false` 要不要提早擋**(Q42):最省是寫入時報錯(零寫入、exit 1);較好是 `PlaylistRef` 加 `ReadOnly bool`,migrate 挑選器 / `pl link` 直接標「(Apple 精選,不能寫)」。

### 3.4 傳播延遲與 gone

`CreatePlaylist` 內部等到列表出現才回傳(3.2)。殘餘風險:使用者在另一台裝置立刻 pull——同 Q15 的窗口,接受並文件化。

## 4. 任務切分

| # | 內容 | gate | 測試 |
|---|---|---|---|
| **T0** | 本計畫 + 重寫 `scripts/p0/p0-2-playlist-ops.sh`(§5)+ ARCHITECTURE 決策 49 草稿 | 使用者拍板 §6 | `bash -n`(CI 已有) |
| **T1** | 搬家可用:`do()` body、`CreatePlaylist`(含等待出現)、pure-append `ApplyOps`、`canEdit` 閘、`Pushable`、`Caps` Create/Append、429 快速失敗;文字與文件(README / 指南 / 網站首頁 / ARCHITECTURE)同 PR | **不等 R-8**(官方端點) | fake amp-api 加 POST 建清單 / POST tracks / GET 本體;斷言:分批邊界 0 / 1 / 100 / 101 / 250、body 的 `type`、`canEdit:false` 零寫入、非 pure-append 回 skipped、建清單後列表未出現時會輪詢、**從未收到 `DELETE …/tracks`**;CLI e2e:`migrate --from spotify --to apple` 走完(建清單 → 推 → 再 pull 零變更)、`pl sync` 兩個假伺服器對調角色 |
| **T2** | remove / move / rename:PATCH、重讀對齊、一次 PUT(混型)、`Caps` Remove/Reorder/Rename | **R-8 已過(§5 結果)**,可與 T1 同 PR 或緊接;`a.` 列的 PUT 未自驗,R-9 補(備案見 §5) | fake 加 PUT / PATCH;斷言 PUT body 的列 id 順序 = want、重讀不一致零寫入、PUT 空序列只在 want 為空時發生、兩段式失敗的 Written;`pl dedup` 在 Apple 上真的拿掉後面那份;`TestSyncModel*` 讓 apple 角色的假伺服器也接 PUT |
| T3(可選) | Apple 批次 ISRC 反查(`filter[isrc]=a,b,…` 25 個一次):resolver 對大清單 25× 省請求 | — | SPI 加選用介面 `BatchISRCLookup`;resolver 有就用 |
| T4 | 真帳號驗收:P5 計畫 R-1、R-2、R-8、R-9 + P8 R-14(Spotify → Apple 真搬一次) | T1 / T2 合併 | 維護者跑;完成才在 P5 標題打 ✅ |

順序:T0 → (使用者授權後跑 §5 探測) → T1 立刻開工(不等探測結果) → T2 依探測結果 → T4。T3 看使用者要不要。
**2026-09-22 探測已跑完(§5 結果),T1 / T2 都沒有 gate 了。T1 + T2 同一個 PR 實作完成(feat/apple-write):`internal/provider/apple/{client,apple}.go` 加 `do()` body、`Playlist` / `AddTracks` / `ReplaceTracks` / `Rename` / `CreatePlaylist`(輪詢列表,上限 30 × 1 s)、`ApplyOps` / `Pushable`、五個寫入能力;429 無 Retry-After 快速失敗;CLI 一個命令都沒新增,只改文字與文件;web 搬家精靈 `READ_ONLY = []`、`CAN_CREATE` 加 apple;測試:apple 套件 12 個(fake amp-api 斷言從未收到 DELETE)、CLI e2e `TestMigrateSpotifyToAppleThenReorderIsOnePut` / `TestMigrateToAppleCuratedPlaylistWritesNothing`。**

## 5. R-8 寫入探測(step 0;需要使用者授權,會在你的音樂庫建一個拋棄式清單並在結尾刪掉)

`scripts/p0/p0-2-playlist-ops.sh`(本 PR 重寫)只打 §1 表格裡的端點,**每個變數非空才進 URL、絕不送不帶 `ids[…]` 的 `DELETE …/tracks`**,結尾 `DELETE /me/library/playlists/{id}` 自己清場。
執行:`capy auth status` 確認 Apple 已登入 → `bash scripts/p0/p0-2-playlist-ops.sh`(可用 `TERM_QUERY=<搜尋詞>` 換測試素材)。要記錄的判定項:

| # | 觀察 | 決定什麼 |
|---|---|---|
| 1 | `PUT …/tracks` 混用 `{"id":"<catalog>","type":"songs"}` 與 `{"id":"a.…","type":"library-songs"}` 是否 2xx、新曲是否落在指定位置 | T2 一次 PUT 或兩段式 |
| 2 | 同一首 POST 兩次:`/tracks` 回同一個 id 兩列還是兩個 id?PUT 一份含該 id 兩次 → 留幾列?PUT 只含一次 → 留幾列? | Apple 端能否表達重複曲目;`pl dedup` 與「C 有重複」的行為 |
| 3 | 建清單後 `GET …/playlists/{id}` 幾秒 200?列表幾秒出現? | `CreatePlaylist` 的等待上限 |
| 4 | POST 加入的列 id 前綴 `a.` 或 `i.`;`?representation=resources` 是否回新列 | 揭露要不要寫「會進曲庫」;兩段式能否免重讀 |
| 5 | `PATCH` 只帶 `name`:description 是否保留 | `Rename` 送不送 description |
| 6 | `playParams.versionHash` 在 add 前後是否改變 | 能否當免費的「確認期間有沒有人動過」檢查(§6.5.2 規則 6 的窗口) |
| 7 | 兩批各 3 首 append 後尾端順序 | migrate 的順序保證 |
| 8 | 每個請求的 HTTP 狀態(建 / POST / PATCH / PUT / 刪) | `Caps()` 宣告哪幾個 |

### 結果(2026-09-22 真帳號,使用者授權;測試清單 `p.eoGxB3EFQWGXR2` 結尾已 DELETE,HTTP 204)

| # | 觀察 | 結果 | 決定 |
|---|---|---|---|
| 1 | PUT 混型 | **204**;`{"id":"<catalog>","type":"songs"}` 放位置 0 + 既有列 `library-songs` → 讀回第一列就是新曲 | **T2 一次 PUT**,不必兩段式 |
| 2 | 重複曲目 | 同一首 POST 兩次 → `/tracks` 回**同一個列 id 兩列**(`i.qQd0L4euRmMdxr` ×2);PUT 含該 id 兩次 → 留 2 列;PUT 只含一次 → 留 1 列(7 → 6) | Apple 端能表達重複;PUT 依出現次數精準取代,`pl dedup` 可用;`DELETE …&mode=all` 果然會兩列一起消失(Cider #1915),不用它是對的 |
| 3 | 傳播延遲 | 建清單後 `GET …/playlists/{id}` **2 s** 200;列表 **9 s** 出現;`/tracks` 在 POST 後**立刻**反映(6 列) | `CreatePlaylist` 輪詢列表,上限 30 s;`finishPush` 重讀 L′ 不受影響 |
| 4 | 列 id 前綴 | POST 加入的 6 首全是 **`i.`**(曲庫 id)→ 曲目同時進了資料庫;`?representation=resources` 回 200 含新列 id,但**順序與請求不同** | Q46 = **要加揭露**;resources 回應不能拿來定位置(反正一次 PUT 不需要) |
| 5 | PATCH 只帶 name | 204;name 改了、description **保留** | `Rename` 只送 name |
| 6 | versionHash | 新清單本體沒有這個欄位(`-`),add 前後都沒有(舊清單才有) | **不用它**;併發比對維持 `ApplyOps` 內重讀對齊 |
| 7 | 跨批 append 順序 | 兩批各 3 首 → 尾端順序 = A 後 B ✅ | migrate 順序保證成立 |
| 8 | HTTP 狀態 | 建 201 / POST 200(resources)與 204 / PATCH 204 / PUT 反序 204、去重 204、混型 204、移除 204 / 刪清單 204 | `Caps()` 宣告 Create、Append、Remove、Reorder、Rename 全部 |

未量到的:POST 每批 100 的上限(只測 3 首;沿用社群值)、PUT 大清單(只測 7 列;網頁播放器自己就用它重排整個清單)、
**列 id 是 `a.<catalogId>` 的既有清單**(使用者 Music.app 建的清單全是這種;R-8 的拋棄式清單是 API 加的、列 id 全是 `i.`,所以 PUT / POST 帶 `a.` + `library-songs` 沒在真帳號打過——
這正是對既有清單 `pl sync` 換序或 `pl dedup` 會送的 body;維護者驗收 R-9 要挑一個 `a.` 列的清單。若 Apple 回 4xx,備案是 `refOf` 把 `a.<catalogId>` 換成 `{"id":"<catalogId>","type":"songs"}`——它本來就是 catalog id 的包裝,一個分支的事)。

⚠️ 副作用:這次探測把 7 首五月天加進了使用者的 Apple Music 資料庫(刪清單不會連帶移除)。

### 補測:`a.` 列的無損 PUT(2026-09-22,使用者授權;PR #79 / #80 review 第 3 點)

先唯讀掃過 7 個自建清單:**只有「耳妊辰養護保養」(222 列,`hasCollaboration:true`)是 `a.` 列,其他 6 個(25–543 列,非協作)全是 `i.` 列**——`a.` 是協作清單的列,不是「Music.app 建的清單」的列。
對它原序全量 PUT 222 列(`a.` id + `library-songs`,先備份):**HTTP 500 `Upstream Service Error / Unable to update tracks`(code 50001),讀回 222 列與備份逐列相同——零變動**。
分不出是 `a.` id 不被接受、還是協作清單本來就不能經這個端點改(兩者在這個帳號完全重疊);要分,得對同一份清單送 catalog id + `songs`(會把列變成 `i.`、曲目進資料庫,是有副作用的寫入)或找一個非協作的 `a.` 清單。
**決定**:`ApplyOps` 重讀到任何 `a.` 列就零寫入、明講原因(協作清單目前不能由 capy 移除 / 換序,請在 app 裡手動或複製成一般清單);500 的翻譯補上這個原因。非協作、`i.` 列的既有清單仍是 R-9 的驗收對象(這次沒動它們)。

## 6. 要請使用者拍板的問題

**使用者 2026-09-22 定案:Q41–Q46 全部採建議**;Q46 依探測第 4 項 = 要加揭露;Q43 探測已跑(§5)。

| # | 問題 | 建議 |
|---|---|---|
| Q41 | T1(搬家:建 + append,官方端點,不等探測)/ T2(remove / move / rename,等 R-8)分兩層? | **分**。搬家是目標,它不需要任何未驗證的端點 |
| Q42 | `canEdit:false` 的清單:寫入時報錯就好,還是 `PlaylistRef` 加 `ReadOnly` 讓 migrate 挑選器 / `pl link` 提早擋? | 寫入時報錯先做(T1);`ReadOnly` 留給 T2 一併看(挑選器標記是體驗問題不是安全問題,寫入端已零寫入) |
| Q43 | R-8 探測何時跑、用什麼搜尋詞當素材(預設「五月天」取 7 首) | 拍板後立刻跑一次(約 2 分鐘),T2 才有依據;T1 不等它 |
| Q44 | 批次 ISRC 反查(T3)要不要排 | 先不排。真帳號 2553 次單筆反查沒撞 429;真的撞了再做,一個選用介面的事 |
| Q45 | 兩個預設:建清單 `isPublic:false`(與 Spotify `public:false` 對齊)、PATCH 只改名不碰描述 | 兩個都採;R-8 第 5 項若證明 PATCH 只帶 name 會清掉描述,改成先 GET 再連 description 一起送 |
| Q46 | 揭露文字要不要加「加進清單的曲目可能同時進你的 Apple Music 資料庫」 | 依 R-8 第 4 項;成立就加在 README 揭露段與 `auth login apple` 的揭露頁 |

### 決策 49(2026-09-22 定案;取代決策 30 的「預期 append-only」,保留「不採 rebuild」;已寫進 ARCHITECTURE 附錄 C)

> Apple 寫入走網頁播放器自己的端點:建清單 / append 是官方文件化的;remove / move 用一次 `PUT …/tracks` 整批取代(既有列用列 id + `library-songs`、新曲用 catalog id + `songs`,R-8 驗過混型)、rename 用 `PATCH`(只送 name,描述保留),
> 是 amp-api 私有端點(多個開源客戶端同形,Apple 未承諾)。只寫 `canEdit:true` 的清單(= 使用者自建;Apple 精選與喜好歌曲一律拒絕,這是 §8「只同步自建清單」的機器判準)。
> 不用 `DELETE …/tracks`(`mode=all` 會把同一首的兩列一起刪、不帶 ids 會清空)。不採 rebuild。加進清單的曲目會同時進使用者的資料庫,揭露要寫。
> 風險類別同決策 8:同一 host、同一組 token,Apple 改了端點就壞,只有真帳號驗收看得到。

## 7. 產出

- T0(本 PR):本文件、`scripts/p0/p0-2-playlist-ops.sh` 重寫。
- T1 / T2 各一個 PR,每個 PR 同時改:程式 + 測試 + README + `docs/guide.html`(跑 `go test ./site/ -run TestGuideOnSiteIsCurrent -update`、重發 Artifact)+
  `site/public/{,en/}index.html` 首頁能力句與 meta + `site/public/{,en/}terms.html`(:42「只能讀取」、:52 資料庫副作用)+ ARCHITECTURE(§1.2 表、§3 註解、§6.5.2 規則 6、附錄 C 決策 30 → 49、§9 P5 狀態)+ CLAUDE.md 定案段一句(Apple 寫入範圍)。
  合併後**實際去看** capy.taislife.work 有沒有更新。
- 參考來源(研究用,不進 repo):Apple 官方 docc JSON(Create a New Library Playlist、Add Tracks to a Library Playlist、LibraryPlaylistTracksRequest.Data);
  Z-Han-Z/apple-music-playlists `docs/apple-music-api-notes.md`;epheterson/applemusic-mcp `amp_api.py`、CHANGELOG(canEdit 與 500);Kopuz-org/kopuz `api.rs`(ids 未加範圍會清空、`representation=resources`);
  itsnebulalol/resonance-addons `mutations.ts`(PUT 重排 / PATCH);ciderapp/Cider `vueapp.js`(PATCH / DELETE 清單);Parachord `applemusic.js`(官方主機 401);
  Apple 開發者論壇 107807、707759、805461、813068、122460;Cider-2 issue #1915(刪一份全刪)。
