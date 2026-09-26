# web 播放列跟著正在播的平台 + Apple 搜尋結果的「播放」(2026-09-24)

狀態:**定案,開發中**。使用者 2026-09-24 定案:Q58–Q68 全部照推薦(Q62 授權我跑探測,結果在 §2.2)。PR A(問題一)是第一個;PR B(問題二)接著做。

使用者回報兩件事:

1. web 底部的播放列不會跟著正在播的平台換。昨天聽 Apple Music,今天改聽 Spotify,播放列還停在 Apple Music。
2. 搜尋頁選 Apple Music 搜歌,按結果列的「播放」,本機 Music.app 沒有開始播。

使用者另外要求:先讀 Spotify 的 [Building with AI](https://developer.spotify.com/documentation/web-api/tutorials/building-with-ai) 指南,找比每分鐘打 24 次 API 更有效的做法。這份指南本身沒談輪詢,但它連到的 Rate limits、Quota modes、Developer Terms、Developer Policy 和 OpenAPI 規格都有相關規定。結論在 §1.2 與 §1.5,依指南檢查 capy 整個 Spotify 串接的結果在 §1.7。

**2026-09-26 推 PR 前的對抗式審查**(四個視角、每則再派一個 agent 試著推翻)確認並修掉:TUI 與 `now --watch` 在 `QUOTA_EXCEEDED` / 長 `Retry-After` 時每 2 秒重打(現在照 `Retry-After` 等);唯讀的 `auth status` 與 `dropNow` 會清掉限流的冷卻;安定期越過限流的冷卻、也沒讓命令之前的快取過期;`dropNow` 時在飛的那一輪結果寫回快照;Apple 的 State 錯誤被快取 60 秒;登出後面板一直停在「未登入」;安定期用牆上時鐘比對;曲名連結每 2.5 秒重建而丟掉鍵盤焦點、stale 時不變色、podcast 那行少了 stale 秒數。每一則都有修之前會 fail 的測試。

研究方式:兩輪 workflow。第一輪兩路追程式碼、兩路查 Apple 播放的外部資料,每個 Apple 做法各派一個 agent 試著推翻它。第二輪一路依指南檢查 Spotify 串接、一路找輪詢的替代方案,兩路各有一個驗證 agent 反查。探測(§2.2)是 2026-09-24 在使用者的 Mac 上跑的,macOS 26.5.1,Music.app 的實際畫面由使用者目視回報。

---

## 1. 問題一:播放列不跟著正在播的平台

### 1.1 根因(已查證)

1. **`/api/now` 只問一個平台。** `nowProvider`([web_api.go:241](../../../internal/cli/web_api.go))的順序是 `?provider=` → `capy --web --provider` → config 的 `default_provider`。播放列([player.js:42](../../../internal/cli/webui/js/player.js))不帶參數,所以**永遠只顯示預設平台**。使用者的 config 是 `default_provider: "apple"`,`capy --web` 沒帶 `--provider`,所以播放列一直停在 Apple。這是決策 42 當初定下的行為,不是退化。
2. **控制鈕與快捷鍵不帶 `--provider`。** 按鈕與快捷鍵都經過 `control(cmd)`([player.js:98-105](../../../internal/cli/webui/js/player.js)、app.js:174-181),永遠打到預設平台。所以就算播放列顯示 Spotify,按 ⏸ 暫停的也會是 Apple。web 模式計畫原本寫了要「前端再附加 `--provider`」,結果沒做。
3. **使用者現在就在付代價的 bug。** `pollNow` 碰到任何 State 錯誤都呼叫全域的 `dropNow()`([web_api.go:290](../../../internal/cli/web_api.go))。「Music.app 沒開」是一種狀態,也被當成錯誤處理,所以 Music.app 關著時,每 2.5 秒就重建一次 Apple provider,每次讀 2–3 次 keychain。限流也一樣:`Retry-After` 超過 60 秒時,`Backoff` 會立刻回錯,`dropNow` 把 provider 丟掉,2.5 秒後重建再打一次 Spotify。這變成一個無視 `Retry-After` 的緊密重試迴圈,正是 Spotify 指南明令禁止的做法。
4. **Spotify 在播 podcast 或廣告時,會被當成沒在播。** `pollNow` 在 `st.Track == nil` 時先 return,來不及設 `Playing`(web_api.go:292-294)。`State` 沒帶 `additional_types`,所以播 episode 時 item 是 null;規格也允許 `currently_playing_type` 是 `ad` 或 `unknown`。新的跟隨規則會把這個問題放大。

TUI 與 `capy now --watch` 也只看一個平台,而且每 2 秒輪詢一次,也就是每分鐘 30 次 Spotify 呼叫。Q64 的 follow-up PR 要沿用本節的規則與節流策略。

### 1.2 方案

**跟隨規則:只有「正在播」能把播放列拉走。**

- **有明指就釘住。** 用 `?provider=X` 或 `capy --web --provider X` 時,只看 X,控制鈕也送 X。
- **沒明指就自動跟隨,每一輪這樣決定:**
  1. `base` 是上一輪顯示的平台。還沒有上一輪(剛啟動或 `dropNow` 之後)就用 config 的 `default_provider`。這個值每次重讀,不用 `defaultProvider()`,因為它是 `sync.OnceValue`,只會讀一次。
  2. 問 `base`。它正在播就顯示它,**其他平台一律不問**:照這個規則,它們的答案改變不了結果。
  3. `base` 沒在播,才依序問其他平台:先預設平台,再照 `providerIDs` 的順序。建不起來或沒有播放能力的(沒登入、local、Windows 上的 apple)跟其他錯誤一樣快取 60 秒,不另外寫死名單。第一個正在播的就切過去。都沒在播就留在 `base`,不管它是暫停、閒置還是出錯。

  不讓「暫停中的歌」也有資格搶播放列,原因是它會重現使用者回報的問題:Spotify 暫停幾分鐘後,`/me/player` 會從 200 變成 204;這時 Music.app 還掛著昨天暫停的那首,播放列就會跳回 Apple。

**節流:Spotify 只在答案可能變了的時候才問。** 瀏覽器照樣每 2.5 秒打一次 `/api/now`。這一段只打本機,不花任何配額。真正打 Spotify API 的次數由伺服器控制:

- **每個平台一份快取**:`nowCache map[id]{resp, at, until}`,由 `nowMu` 保護。在 `until` 之前,伺服器用快取回答。如果快取裡是正在播,`position_ms` 依經過的時間往前推,讓進度條繼續走,不必多打 API。
- **每種狀態的有效期**(`nowTTL`):

  | 情況 | 有效期 | 理由 |
  |---|---|---|
  | apple 的 State(含 Music.app 沒開、osascript 回錯) | 0,每輪都問 | osascript 在本機跑,不花配額;Music.app 開始播放 2.5 秒內就看得到 |
  | 任何平台建不起來(沒登入、不支援播放) | 60 秒 | 沒登入 Apple 的人不會每 2.5 秒讀一次 keychain |
  | 其他平台的 State 回錯(5xx) | 60 秒 | 每次都是一次 Web API 呼叫 |
  | 被限流(429) | max(60 秒, `Retry-After`) | 照指南等 `Retry-After`,不重試 |
  | spotify 閒置(204)或暫停中 | **15 秒**(Q65) | 在手機上開始播,最慢 15 秒內會出現 |
  | spotify 正在播 | min(10 秒, 離這首結束的時間 + 1 秒) | 換歌約 1 秒內更新 |

- **播放命令後的安定期。** web 跑完 `play`、`pause`、`next`、`prev`、`seek`、`vol` 後,接下來 3 秒內有效期一律視為 0,而且這段期間抓到的結果在安定期結束時就過期。這個掛在 web_run.go 的 `webNowInvalidatedBy` 旁邊,所以播放列按鈕、快捷鍵、搜尋頁的 `play --id`、主控台都涵蓋到。要這樣做是因為 Spotify 的播放器寫入是最終一致:如果剛按完播放時讀到舊狀態、又快取 15 秒,播放列會一直顯示 ⏸,再按一次空白鍵就會送出相反的命令。
- **免費的觸發點。** 如果 `base` 上一輪還在播、這一輪停了,就讓其他候選的快取立刻過期。Apple 的輪詢不花配額,所以「停掉 Music.app、改開 Spotify」大約 2.5 秒就會切過去,不必等 15 秒。
- **輪詢時遇到 429 不睡。** `pollNow` 的 ctx 帶 `provider.WithoutWait`,`Backoff` 看到它就直接回 `RateLimitError`,不在持有 `pollMu` 時睡最多 3 × 60 秒。現在那樣睡會讓整條播放列凍住,超過 30 秒還會被判定為失聯。
- **Spotify client 的 429**([client.go:81-91](../../../internal/provider/spotify/client.go))。收到 429 時先讀 body 的 `reason`,再把 `*RateLimitError` 包在錯誤裡往上傳。現在 `Seconds` 在這裡就被丟掉了,上層拿不到 `Retry-After`。`reason` 是 `QUOTA_EXCEEDED`(2026-07 起 dev mode 的配額以開發者帳號計,同一個帳號底下的 Client ID 共用)時不重試,改回一個獨立、有翻譯的錯誤訊息,en.json 與 zh-TW.json 都要補。
- **`stale_ms` 的意思不變。** 每一輪都呼叫 `setNow`,包括用快取回答的那幾輪,所以 `stale_ms` 仍然表示「距離上一輪多久」。不這樣做的話,15 秒的有效期碰上一次 `TryLock` 失敗,就會觸發 `STALE_DEAD_MS`,播放列誤判為失聯。自動模式下 `staleNow` 回上一輪的結果,不管它是哪個平台的,而且一樣用經過的時間把 `position_ms` 往前推;不推的話,一次 `TryLock` 失敗就會讓進度條倒退。釘住模式保留現在的跨平台檢查。
- `pollNow` 不再呼叫全域的 `dropNow()`。只有 `ErrAuthExpired` 會丟掉**那一個**平台的 controller;`ErrPlayerNotRunning` 與限流都只是狀態。
- `Playing` 在判斷 `Track == nil` 之前就先設好,所以 podcast 和廣告算正在播。這會讓播放列切到 Spotify,但 `render()` 碰到 `!d.track` 會印「目前沒有播放內容」,而那時其實正在播。所以前端在 `d.playing && !d.track` 時只顯示 `Spotify · ▶`,不新增字串。要顯示 episode 的標題,得在共用的 `State()` 帶 `additional_types=episode` 並另外寫 episode 的對應,會影響 `capy now`、TUI 與 watch,這次不做。
- 這些都不違反 CLAUDE.md 的 web 約束:不進 runMu、不呼叫 `defaultProvider()`、不 `i18n.Set`,stderr 只經 `BackoffStderr` / `LockStderr`。`/api/now` 的 JSON 形狀不變。

**前端(player.js):**

- `control()` 在 `this.last?.provider` 存在時把 ` --provider ${this.last.provider}` 接到 `cmd` 後面,再照原樣呼叫 `this.con.run(cmd, {}, { quiet: true })`(web_test.go 釘住這串字面)。快捷鍵、`seekBy`、`volBy` 都經過這裡,不用另外改。
- 曲目那行前面加平台名稱(Q60),例如 `Spotify · ▶ Song — Artist · 1:01 / 3:20 · iPhone`。閒置與錯誤那兩行改傳品牌名,不傳原始 id。用的是既有的 `providerName()`,不新增 i18n key。
- 有 `track.url` 時,曲名做成連回平台的連結,`target=_blank rel="noopener noreferrer"`(Q67)。Spotify Developer Policy II 要求顯示 Spotify 內容時附連回 Spotify 的連結。那一行會從 `textContent` 改成由幾個 `<span>` 加一個 `<a>` 組成。node 測試的假 DOM 會把子節點的 `textContent` 串起來(webui_console.mjs:18),所以既有「整行字串相等」的斷言照樣能用,只是預期字串因為加了平台名稱而要改。
- `POLL_MS` 維持 2500,因為 Spotify 的配額已經由伺服器控制。開多個分頁也不會讓 Spotify 呼叫變多,因為都從快取回答。

### 1.3 Spotify API 呼叫量(分頁開著時,不管開幾個分頁都是這個上限)

| 情境 | 現在 | 第一版計畫 | 這一版 | 播放列多快發現變化 |
|---|---|---|---|---|
| 1. 預設 Apple,Music.app 在播 | 0 | 24/分 | **0** | Music.app 的變化 ≤ 2.5 秒 |
| 2. 預設 Apple,哪裡都沒在播 | 0 | 24/分 | **4/分** | 停掉 Music.app 改開 Spotify ≈ 2.5 秒;直接在手機上開始播 ≤ 15 秒 |
| 3. 顯示 Spotify 且在播 | 24/分(N 個分頁 × N) | 24/分 | **約 6/分**,每首歌結束 +1,每個播放命令約 +2 | 手機上暫停或換歌 ≤ 10 秒 |
| 4. 顯示 Spotify 且暫停 | 24/分 | 24/分 | **4/分** | Music.app 開始播 ≤ 2.5 秒;手機上繼續播 ≤ 15 秒 |
| 5. 分頁藏起來 | 0 | 0 | **0** | 分頁回來時,快取過期就立刻問 |

在 web 以外下的命令(終端機的 `capy pause`、TUI、手機)沒有安定期,播放列最多延遲一個有效期才反映。這點要寫進決策 51。

### 1.4 已知限制(會寫進文件)

- **macOS 自動化權限對話框。** 預設 Spotify、登入過 Apple、開著 Music.app、但從沒允許終端機控制 Music 的人,播放列第一次問 Apple 時可能會跳出「允許控制 Music」。用 `capy --web --provider spotify` 可以避開。
- **兩邊同時在播。** 播放列會留在原本顯示的平台。按暫停後換成另一個平台,這時再按一次空白鍵,暫停的會是另一個平台。
- **看不到的播放。** Apple 只看得到本機的 Music.app,iPhone 上的播放看不到。Spotify 看得到所有 Connect 裝置。
- **仍然只有一把 `pollMu`。** 如果 osascript 卡住(例如在等權限對話框),整輪都會卡住。之後可以改成每個平台一把鎖,這次不做。

### 1.5 研究過、這次不採用的做法

| 做法 | 為什麼不用 |
|---|---|
| 官方推播(webhook / WebSocket / SSE) | 沒有這種東西。OpenAPI 規格沒有 callbacks,相關的 feature request 沒人回應,那個 repo 也已封存 |
| Web Playback SDK 的 `player_state_changed` | 只回報瀏覽器自己那個裝置的狀態。還需要 `streaming` scope、Premium,也違反網頁的 CSP |
| ETag / `If-None-Match` / 304 | 規格裡 `/me/player` 沒有 ETag。就算回 304,也照樣算一次請求 |
| 改用 `/me/player/currently-playing` | 一樣算一次請求,而且拿不到裝置與音量 |
| 用 AppleScript 讀本機 Spotify.app 的狀態 | 不花配額,但有回報指出 Spotify 在其他 Connect 裝置上播放時,它會回報錯誤的狀態;還會多一個權限對話框。以後可以考慮當輔助訊號,不能當唯一來源 |
| Spotify.app 內附的 `spotify_cli now-playing` | 走沒有公開文件的本機 Partner API |
| Spotify 內部的 dealer WebSocket | 非官方協定 |
| Windows SMTC | 要用 WinRT,而且只看得到本機 |
| 讓暫停中的歌也能搶播放列 | 會重現使用者回報的問題(見 §1.2) |
| 記住「最後一個播放命令用的平台」來決定顯示哪個 | 只有兩邊同時在播時才有用,卻要改成 `ExecuteContextC` 再加一套 CAS |

### 1.6 測試

**修之前會 fail 的新測試**(時間由 `webNowClock` 替換點控制):

- `TestWebNowFollowsPlayingProvider`:預設 apple,apple 暫停中、spotify 在播 → 回 spotify;接著 spotify 暫停、再變成 204,apple 仍暫停 → 還是 spotify;Music.app 真的開始播 → 切回 apple。
- `TestWebNowSpotifyNotConsultedWhileBasePlaying`:apple 在播,輪詢 20 次 → spotify 的 State 被呼叫 0 次。
- `TestWebNowSpotifyIdleTTL`:apple 閒置、spotify 回 204,在假時鐘上每 2.5 秒輪詢一次、共 30 秒 → spotify 最多被呼叫 2 次。
- `TestWebNowBaseStopTriggersProbe`:apple 從在播變成停止 → 同一輪就問 spotify。
- `TestWebNowTrackEndExpires`:這首只剩 3 秒 → +4 秒那一輪會真的呼叫 State。
- `TestWebNowSettleAfterCommand`:跑完 `pause` → 下一輪就算在有效期內也會呼叫 State,安定期結束後快取也不會留著舊狀態。
- `TestWebNowStaleMSMeansSinceLastRound`:用快取回答的那一輪之後,`TryLock` 失敗的回應 `stale_ms` 要小於 5000。
- `TestWebNowRateLimitCooldown`:429 帶 `Retry-After: 120` → 不睡;120 秒內不再呼叫 spotify,apple 照常每輪都問。
- `TestWebNowNotRunningKeepsController`:apple 連續回 `ErrPlayerNotRunning` → 只建構一次。
- `TestWebNowBuildFailureCached`:apple 沒登入 → 60 秒內只呼叫一次 `newProvider("apple")`。
- `TestWebNowAuthExpiredDropsOnlyThatProvider`:apple 回 `ErrAuthExpired` → 只重建 apple。
- `TestWebNowPodcastCountsAsPlaying`:`Playing:true`、`Track:nil` → 算正在播,跟隨規則會選它。
- `TestWebNowPinned`:`?provider=apple` 而 spotify 在播 → 回 apple,spotify 的 State 呼叫 0 次(`--web --provider` 走同一條)。
- `TestWebNowDropKeepsShownProvider`:`dropNow`(換語系的 `config set` 也會走到)之後,面板留在上一輪顯示的平台。
- spotify client:429 的 body 是 `QUOTA_EXCEEDED` → 不重試,回獨立的錯誤;`Retry-After` 的秒數要傳到上層。
- node(webui_console.mjs):`render({provider:'apple'})` 之後 `control('pause')` 要跑 `pause --provider apple`,`seekBy(10)` 要跑 `seek N --provider apple`;沒有快照時不加旗標;有 `track.url` 時曲名是連結。

**要改的既有測試**:

- `TestWebNowReportsStateAndCachesController` 目前斷言「每次輪詢都要真的問 State」,這和有效期的設計正面衝突。改成用假時鐘驗證:有效期內只問一次、4 秒後命中快取時進度 +4000 且 `stale=false`、過了有效期才再問。
- `TestWebNowUsesCachedController…`、`TestWebNowDropsCacheOnStateError` 的 `built==N`:`swapNow` 目前對每個 id 回同一個假物件,要改成每個 id 各一個。
- node 情境 13:平台名稱前綴會改變預期字串。

**不用改、但要帶 `-race` 重跑**:`StaleNeverCrossesProvider`、`DropAlsoClearsSnapshot`、`BuildsProviderOutsideLock`、`NotBlockedByRunningJob`、`BadProvider400`、`PollBoundedWhileStateStuck`。

### 1.7 依 Spotify 指南檢查 capy 整個 Spotify 串接的結果

**已經符合**:PKCE(S256、state 檢查、沒有 secret);redirect 用 `http://127.0.0.1`;token 只存 keychain,refresh token 輪替時每次都覆寫;錯誤碼有對應;讀寫清單都用 `/playlists/{id}/items`,沒用已棄用的 `/tracks`;Drive 正本只存曲名、歌手、專輯、時長、ISRC 與 id 對照,不存封面與試聽(Developer Policy III 明文允許使用者把自己清單的 metadata 搬到別的服務);沒有 ML、沒有遙測。

**本 PR(PR A)一起修**:
- 限流與 `QUOTA_EXCEEDED`(§1.2)。
- podcast 和廣告算正在播(§1.1 第 4 點)。
- 播放列曲名連回平台(§1.2)。

**要另外開 PR 的偏差**(Q68):

| # | 偏差 | 建議 |
|---|---|---|
| S1 | 要了 9 個 scope,其中 `user-library-read`、`user-library-modify`、`user-read-currently-playing` 沒有任何呼叫點。指南寫「不要預先要求寬的 scope」,和 ARCHITECTURE §4.2「一次全要、免得重新授權」的決策衝突 | 拿掉這 3 個。這會推翻 §4.2,需要新決策。拿掉不必重新授權,但只有新登入的人會少拿這 3 個權限 |
| S2 | `GET /artists/{id}/top-tracks` 在 2026-02 已經移除,capy 每次 `play artist:` 還是先打它。只有回 403 才走 fallback,改回 404 或 410 就會直接失敗 | 直接走既有的搜尋 fallback,刪掉 `spotify.top_tracks_fallback` 這個 key;順便拿掉已移除的 `Track.popularity` |
| S3 | pull / sync 每次都重抓每份清單的每一頁 | 記住 `snapshot_id`,沒變的清單就跳過。配額改成以開發者帳號計算之後,用 cron 同步的人最受惠 |
| S4 | refresh token 從授權那天起算 6 個月就失效,refresh 不會延長 | 記錄 `authorized_at`,doctor 與 `auth status` 從第 170 天左右開始提醒 |
| S5 | `auth logout spotify` 沒清掉本機快取裡的 Spotify 資料列(Developer Policy I 要求中斷連線時刪除) | logout 時一起清 |
| S6 | redirect 綁固定的 8888 埠 | 文件說 loopback 可以只註冊不帶埠號的網址,授權時再帶動態埠,但要先在真的 dashboard 上確認可行 |
| S7 | 搜尋結果列沒有連回 Spotify | 加上連結,並對照 Branding Guidelines |

### 1.8 文件(PR A)

- ARCHITECTURE.md 新增**決策 51**,取代決策 42 裡播放面板的部分。內容包括跟隨規則、釘住的語意、有效期表、安定期、限流的處理。
- README.md:255、README.zh-TW.md:256、兩份指南各補一句:播放列跟著正在播的平台,控制鈕作用在它顯示的平台上,`capy --web --provider X` 可以釘住。改了指南就要跑 `go test ./site/ -run TestGuideOnSiteIsCurrent -update`、重發兩個指南 Artifact,合併後確認線上的 `/guide`、`/en/guide`、`/guide.css` 有更新。
- 隱私權政策不用改:沒有新的外部服務。

---

## 2. 問題二:Apple 搜尋結果按「播放」,Music.app 沒播

### 2.1 根因(已查證)

路徑:search.js:53 → `play --id <id> --provider apple` → `Provider.Play`([player_darwin.go:91](../../../internal/provider/apple/player_darwin.go))→ 取 catalog song 的 `attributes.url`(專輯頁網址帶 `?i=<songId>`)→ 把 `https://` 換成 `music://` → `tell application "Music" to open location "…"`。

- AppleScript 的 `play` 只收資料庫裡的曲目或檔案,曲目物件上也沒有 catalog id。純 AppleScript 沒辦法播一首不在資料庫裡的目錄歌曲。這點有好幾個獨立來源一致:Apple Community 8238165、epheterson/applemusic-mcp,還有 Raycast 的 Apple Music 擴充功能——它的目錄結果也只提供「Open in Music」與「Add to Library」。
- capy 還謊報成功:印 `▶ <id>`、exit 0,web 顯示「完成」。
- 機制 A / B 從沒在真機上決勝過(P2 計畫附錄 C-4)。這次的探測就是在補做這件事。

### 2.2 探測結果(2026-09-24,macOS 26.5.1;測試歌曲是 Kraftwerk〈Radioactivity〉,catalog id 700050031,確認不在資料庫)

| # | 做法 | Music.app 畫面(使用者目視) | 有沒有播 |
|---|---|---|---|
| P-1 | `capy play --id 700050031 --provider apple`(機制 A:AppleScript `open location` 專輯網址) | **沒換頁** | 沒有;capy 照樣印 ▶、exit 0 |
| P-2 | `open "music://…/album/radioactivity/700049905?i=700050031"`(機制 B) | 專輯頁,沒標亮 | 沒有 |
| P-3 | `open "music://music.apple.com/tw/song/700050031"`(capy 從沒試過的單曲網址) | **專輯頁,而且那首標亮** | 沒有 |
| P-3′ | 排除干擾後重跑:先把 Music.app 切到 Tom Waits 的專輯,再 `open "music://music.apple.com/tw/song/699902614"`(〈Computer Love〉,專輯第 5 首,不在資料庫) | **跳到《Computer World》,那首標亮** | 沒有 |
| P-5 | 標亮之後再下 AppleScript `play` | — | 播的是原本暫停的〈Freedom〉,不是標亮的那首。`selection` 是空的 |
| P-4 | 讀 macOS 26 的 `current track`(〈Freedom〉不在資料庫,是 `URL track`) | — | 讀得到,26.5.1 上沒有 `-1728` 的 bug,播放列可以顯示目錄歌曲 |

P-5 讓〈Freedom〉實際播了約 4 秒,之後已恢復成暫停在 2:36。整個探測沒有寫入任何資料。

**結論**:在 Music 26 上,現在的機制 A 連頁面都不會換。能做到的最好結果是 P-3:用 shell `open` 開單曲網址,Music.app 會跳到專輯頁並標亮那一首。P-3′ 從別的專輯頁出發,結果一樣,所以 P-3 的結果不是 P-2 留下的畫面。這條路也不需要「控制 Music」的自動化權限。

**選項 2(§2.4)的比對可行性**:在使用者的資料庫裡跑唯讀比對,結果如下:

- 〈Lost Stars〉/ Adam Levine / 268 秒有三份,分屬《Begin Again》、《V (Asia Tour Edition)》、《V (Deluxe)》。歌名、歌手、時長都一樣,只有專輯能分開。
- 〈Payphone (feat. Wiz Khalifa)〉的 catalog 專輯叫《Overexposed》,資料庫裡叫《Overexposed (Deluxe Version)》,專輯名比不上;但歌名 + 歌手 + 時長(231 秒)只有一份。

所以比對規則要分兩步:先用歌名 + 歌手 + 時長 ±2 秒篩,剩一份就播;剩多份再用專輯名篩,剩一份才播;其他情況退回「開啟並標亮」。

### 2.3 可行性總表

| 做法 | 能播到指定那一首? | 結論 |
|---|---|---|
| 深層連結(A / B / 單曲網址) | 不行,最多打開頁面並標亮(P-1~P-3) | 當成「在 Music 開啟」 |
| 網址參數(`autoplay`、`app=music` …) | 不行 | 沒有這種參數 |
| 純 AppleScript(`play` / `URL track` / `reveal` / `selection`) | 不行(P-5) | — |
| 資料庫裡已經有的歌:本機依歌名 + 歌手 + 時長比對,再用 AppleScript `play` | 可以,只限資料庫裡有的歌 | 零寫入,Q63 選項 2 |
| 先加進資料庫再播(`POST /me/library` + 等 iCloud 同步) | 可以,要開「同步資料庫」 | 每按一次播放就永久寫進資料庫;要等 10–90 秒;回 202 不代表真的加成功;是新的寫入類別。不推薦 |
| UI 自動化(System Events 點兩下那一列) | 可能可以,跟 Music 版本綁很死 | 要「輔助使用」權限、要解鎖螢幕、會搶滑鼠;cron 與管線一定失敗。不推薦 |
| MusicKit / SystemMusicPlayer / MediaRemote / 在瀏覽器播 | — | 要付費開發者會籍、原生 macOS 不支援、無法指定曲目,或違反 Apple 硬約束 |

Windows:Apple Music for Windows 沒有腳本介面,本來就不支援播放。

### 2.4 方案

**(a) PR B 一定做:說實話,並用最好的開啟方式**

- 機制寫死成 shell `open "music://music.apple.com/<storefront>/song/<id>"`(P-3 與 P-3′ 驗證過),刪掉機制 A 與 `CAPY_APPLE_PLAY_MECHANISM`。P2 計畫定過「歌曲網址一律取 API 的 `attributes.url`,不自己拼」,理由是其他網址形狀沒驗證過;單曲網址現在驗證過了,把這件事記成決策 52。如果 Q63 選 1,就不再需要 `Song()` 這次 API 請求:以 id 播 Apple 不必打 API,也不花 amp-api 的配額。
- 「有沒有真的開始播」要每次呼叫各自回報,不能用靜態的能力位表達:如果 Q63 選 2,同一個 Apple `Play` 比對到資料庫就真的播,比對不到就只打開頁面。做法是 `provider` 新增一個哨兵值 `ErrOpenedNotPlaying`,Apple 的 `Play` 只打開頁面時回它。
- player.go:`errors.Is(err, provider.ErrOpenedNotPlaying)` 時不印 ▶,改印「已在 Music.app 打開「{label}」並標出那一首。Apple Music 的目錄歌曲 capy 沒辦法替你開始播,請在 Music.app 按播放。」exit 0。其他錯誤照舊。en.json 與 zh-TW.json 同時補這句。這個做法不管 Q63 選 1 還是選 2 都適用,也比新增能力位的改動小。
- 所有以 id 播 Apple 的路徑都拿到這句:`play --id`、`play <查詢>`、`artist:`、`--pick`、TUI、web 主控台、web 搜尋。
- web 搜尋頁:平台是 Apple 時,列上的按鈕改叫「在 Music 開啟」(新 key),並用 `onStdout` / `notice` 把命令印的那句顯示出來。頁首說明「找到了可以直接播」改成不對 Apple 做這個承諾。
- 測試(修之前會 fail):`TestApplePlayByIDDoesNotClaimPlaying`(stdout 不含 ▶、要含新的那句)、`TestApplePlayOpensSongURL`(執行的是 `open music://music.apple.com/tw/song/<id>`、沒有呼叫 osascript)。Spotify `play --id` 仍然要印 ▶。node 驗 Apple 搜尋列的按鈕文字與它送出的命令。更新 apple_e2e_darwin_test.go 與 player_darwin_test.go 裡關於機制 A / B 的斷言。

**(b) Q63:要不要做「真的播」**

- **選項 1**:停在 (a),開啟並標亮,由使用者自己按播放。
- **選項 2**:資料庫裡已經有的歌直接播。保留 `Song()` 拿歌名、歌手、專輯、時長,在本機資料庫做唯讀的 AppleScript 比對,規則見 §2.2 的兩步驟篩選。剩一首就用 AppleScript `play` 播那首,再用前後的播放狀態確認真的換了歌,這時才印 ▶;其他情況一律退回 (a)。零寫入,不需要新決策。
  - 已確認使用者資料庫裡的歌手名是在地化的(「魔力紅樂團」59 首、「Maroon 5」0 首),和 tw storefront 的 API 回傳一致,所以比對得上。
  - 風險:同名、同專輯、同長度的不同版本仍然分不開,這時退回 (a);換了 storefront 或語系也可能比對不到,同樣退回 (a)。
  - 驗收要真的播一首資料庫裡的歌,會改變使用者當下的播放狀態。
- 選項 3(先加進資料庫再播)與 UI 自動化都不推薦,理由見 §2.3。

---

## 3. 決定紀錄與待決

| # | 問題 | 狀態 |
|---|---|---|
| Q58 | 問題一用簡單版的跟隨規則 | ✅ 2026-09-24 照推薦 |
| Q59 | `--web --provider X` 同時釘住播放列與控制鈕 | ✅ 照推薦 |
| Q60 | 播放列前面加平台名稱 | ✅ 照推薦 |
| Q61 | Apple 以 id 播放時改印誠實的那句、不印 ▶、exit 0 | ✅ 照推薦 |
| Q62 | 探測 | ✅ 授權我跑,已完成(§2.2) |
| Q63 | 真的播:選項 1 或 選項 2 | ✅ 選項 2(資料庫裡有的歌直接播,比對不到就退回開啟並標亮) |
| Q64 | TUI 的同一個 bug 另開 PR | ✅ 另開,沿用 §1.2 的規則與節流(TUI 與 `now --watch` 現在每分鐘打 30 次) |
| Q65 | Spotify 閒置或暫停時的有效期 | ✅ 15 秒 |
| Q66 | 429 與 `QUOTA_EXCEEDED` 的處理要做,而且放在問題一的範圍內 | ✅ 做,PR A 不拆 |
| Q67 | 播放列曲名連回平台(Developer Policy II) | ✅ 做 |
| Q68 | §1.7 的 S1–S7 要排哪些 | ✅ S1、S2 接在 PR A、PR B 後面做(S1 需要新決策),S3–S7 放進待辦 |

## 4. PR 切法

- **PR A(問題一)**:跟隨規則、節流、429 處理、podcast 修正、前端、§1.6 的測試、決策 51、README 與指南。本計畫檔也跟著 PR A 一起進 main。
- **PR B(問題二)**:以單曲網址開啟、`ErrOpenedNotPlaying`、說實話的那句、搜尋頁、決策 52;如果 Q63 選 2,也包含資料庫比對後直接播。
- **之後**:TUI 跟隨(Q64)、S1 scope、S2 已移除的端點,以及 Q68 決定要排的其他項目。
