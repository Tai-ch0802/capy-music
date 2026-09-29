# YouTube Music provider:清單搬遷與同步(2026-09-29 研究與計畫)

使用者(2026-09-29):「我想是時候增加另一個音樂平台 provider 的歌曲清單搬遷、同步功能了。這次目標是 youtube music。考量使用者可能有多個帳號,
所以我們的 youtube music 的登入帳號有可能會和現在既有的 google drive 的登入帳號不同,可能要留意一下並且區分開來。請先草擬出完整的 plan,
我們確認定案方向以後再進行開發實作。」

狀態:**草案,待 §8 定案**。T0 = 本文(拍板後同一個 PR 補探測腳本、ARCHITECTURE 與附錄 C 決策 60);之後依 §6 開 T1–T3。

研究方式:讀了 capy 的 Provider SPI、Apple 的憑證與寫入路徑(揭露、精靈、web 接縫、`ApplyOps` 對齊)、P5 / P6 的同步機制(foreign、gone、
`PartialWriteError` 的判準)、web 提示橋、i18n 守門測試、網站測試。YouTube 側對照了 Google 官方的 Data API 配額頁、OAuth 驗證頁、YouTube API
開發者政策,以及 music.youtube.com 網頁播放器自己用的 InnerTube(以社群實作 ytmusicapi 1.12 的原始碼與文件為對照)、yt-dlp 對 YouTube cookie
輪替的說明、YouTube 服務條款。所有「T0 驗」的項目都還沒用真帳號打過。

---

## 0. 現況(查證過,2026-09-29)

- **provider 清單只有一處**:`internal/cli/provider.go:31` 的 `providerIDs`(flag 說明、config 驗證、`newProvider` 的 switch 都用它)。
  canon / store / resolve 沒有任何平台硬編碼——`pl__.Links` 是 `map[provider]id`,沒有 ISRC 的曲目 cid 是 `p:<provider>:<id>`(`canon.CID`)。
  新 provider 進來,pull / push / sync / migrate / dedup / resolve 全靠能力位元自動放行(Apple 寫入 PR #80 已證明,一條特例都不用拆)。
- **Apple 是現成模板**:揭露頁不可跳過(`appleConfirmDisclosure`,非 TTY 要 `--i-understand`)、指引 → 貼上 → 驗證 → 落地(`applePersist`)、
  web 走 `installWebSeams` 的 `webAppleDisclosure` / `webAppleWizardInputs`(Secret 欄)、`web_run.go:158` 對 `--developer-token` / `--user-token` 403、
  `checkAppleDisclosure` 每個語系逐句釘、doctor 三項、`auth status --json` 的 `apple` 物件、`apple_e2e_test.go`。
- **綁範圍的 id 也有現成機制**(local,決策 33):id = `<device_id>/<檔名>`、`CapDeviceBound` + `DeviceScoped.Foreign`;pull / push / sync 對別台的
  link 只跳過(`pull.go:842`、`push.go:150`)、`pl link` 撞到就接管(`pull.go:578`)、`resolvePlaylistID` 接受只打後半段(`pl.go:76`)。
- **gone 的後果**(`pull.go:895`):清單不在 `ListPlaylists` 裡 → 刪 link、清自己這台的 base;正本不動。
- **push 對「平台動過沒」的判準**(`push.go:328`):`ApplyOps` 回 nil 或 `*PartialWriteError` 才算動過(base 前進、規則 7 重讀 L′);
  其他 error 一律當「第一個請求就失敗,平台沒動」。
- **keychain**:macOS Keychain / Windows Credential Manager,後者單筆上限 2560 bytes(`tokenstore.go` 註解);`ai.headers` 已有「整包標頭當機密、上限 2 KB」的先例。
- **網站與文件**:`site_test.go` 釘 scope 與 AI 端點欄位;首頁能力句(`site/public/index.html:7 / 14 / 39 / 45 / 149`)、政策 §3「程式只會連到 Google、Spotify、Apple」
  (`privacy.html:64`)、§6 HTTPS 句(`:81`)、商標句(`:107`)每語系一份;條款兩份;README 兩份;指南兩份(+ 網站上的轉換結果)。
- **web 裡的平台硬編碼**:`app.js:108 providers.list`、`move.js:15 CAN_CREATE` / `:16 NO_LOGIN`、`common.js:7 providerName`、`account.js:10` 帳號列、
  `search.js:53` 播放鈕的 Apple 特例。

## 1. 研究結論:YouTube 側的兩條路

### 1.1 官方 YouTube Data API v3(2026-09-29 對照官方頁)

| 項目 | 現況 | 影響 |
|---|---|---|
| 配額 | 每個 GCP 專案每天:**`search.list` 100 次**(2026-06 起獨立預算)、`videos.insert` 100 次、其他端點合計 10,000 units;`playlistItems.insert / update / delete`、`playlists.insert` 各 50 units;list 類 1 unit([Quota and compliance audits](https://developers.google.com/youtube/v3/guides/quota_and_compliance_audits)、[Quota calculator](https://developers.google.com/youtube/v3/determine_quota_cost)) | 搬一份 300 首的清單 = 300 次搜尋 = **3 天**;寫入每天 200 首。提高配額要先過 compliance audit(表單、人工審)——BYO 的每個使用者各自申請,不可能 |
| scope | `youtube` / `youtube.force-ssl` / `youtube.readonly` 都是 **sensitive**:未驗證的 app 出「Google 尚未驗證這個應用程式」畫面、100 人上限;驗證要首頁、政策、**示範影片**、scope 理由([OAuth app verification](https://support.google.com/cloud/answer/13464321)) | **不能掛在內建的 Google client 上**:那個同意畫面現在只有三個 non-sensitive scope(CLAUDE.md 硬約束、`site_test.go` 釘住),加一個 sensitive scope 就整個專案(含 Drive 登入)要送驗。所以官方路 = 使用者**另建一個 GCP 專案**(BYO,同 Spotify),自己面對 Testing 狀態 refresh token 7 天過期的坑(`explainGoogleGrant` 已在處理同一個坑) |
| 開發者政策 | [Developer Policies](https://developers.google.com/youtube/terms/developer-policies) III.E.4.c:存下來的 Authorized Data **30 天內要刷新或刪除**;III.D.2.3.a:使用者撤銷授權後 **7 天內刪光**;III.F.2.a:顯示 YouTube 內容要放 YouTube 品牌標示 | capy 的正本(Drive `tracks.json` / `pl__`)永久保存觀測到的曲目——要多一套「30 天沒同步到就清 YouTube 的 mapping」,決策 57 的 7 天快取只涵蓋 SQLite |
| metadata | 只有 video 層:`snippet.title` + `channelTitle`(「歌手 - Topic」);沒有歌手 / 專輯欄位、**沒有 ISRC**;`search.list` 回的是影片(MV、翻唱、歌詞影片混在一起),不是 YouTube Music 的「歌曲」實體 | resolver Layer 2 的訊號比 InnerTube 差一截 |
| 清單 | YouTube Music 的清單就是 YouTube 清單(同一個 id),Data API 讀寫都看得到 | 官方路唯一的優點:寫入是官方承諾的端點 |

### 1.2 非官方 InnerTube(music.youtube.com 自己用的 API;以 ytmusicapi 1.12 為對照)

| 項目 | 現況 | 佐證 |
|---|---|---|
| 主機與 client | `POST https://music.youtube.com/youtubei/v1/<endpoint>?alt=json`,body 帶 `context.client = {clientName:"WEB_REMIX", clientVersion:"1.<YYYYMMDD>.01.00", hl, gl}` | ytmusicapi `helpers.py` initialize_context |
| 認證 | 使用者從 DevTools 複製 music.youtube.com 任一 `/browse` POST 的 **Request Headers**:必要的只有 `cookie`(要含 `__Secure-3PAPISID`)與 `x-goog-authuser`(瀏覽器裡第幾個帳號);品牌帳號另有 `x-goog-pageid`。程式每次請求自己算 `Authorization: SAPISIDHASH <ts>_<sha1("<ts> <SAPISID> https://music.youtube.com")>`,加 `origin` / `x-origin: https://music.youtube.com` | ytmusicapi `auth/browser.py`(只驗這兩個 header 在不在;`sec-*` / host / content-length 丟掉)、`helpers.py` get_authorization / sapisid_from_cookie |
| 壽命 | ytmusicapi:「跟瀏覽器 session 一樣久(約 2 年,除非登出)」;**但** yt-dlp 文件說 YouTube 會對開著的分頁定期輪替 cookie(`__Secure-*PSIDTS` / `SIDCC`),建議在無痕視窗登入、複製、關掉視窗不登出 | [ytmusicapi browser auth](https://ytmusicapi.readthedocs.io/en/stable/setup/browser.html)、[yt-dlp wiki](https://github.com/yt-dlp/yt-dlp/wiki/Extractors)。**兩者矛盾,T0 實測**(§4 第 2 項) |
| OAuth 替代 | ytmusicapi 的 OAuth 走「TV 裝置」流程,2024-11 起要使用者自建 TV 型 client;文件另註 Google 已對 web client 停用、建議改用瀏覽器認證 → 不採 | [ytmusicapi oauth](https://ytmusicapi.readthedocs.io/en/stable/setup/oauth.html)、[issue #679](https://github.com/sigma67/ytmusicapi/issues/679) |
| 帳號 | `account/account_menu` → `accountName`、`channelHandle`(`@…`)、頭像;**沒有 email** | ytmusicapi `get_account_info` |
| 搜尋 | `search` {query, params:"EgWKAQIIAWoMEA4QChADEAQQCRAF"}(只回「歌曲」)→ `musicShelfRenderer.contents[].musicResponsiveListItemRenderer`:videoId、標題、歌手(runs)、專輯、時長「m:ss」、explicit 徽章。**沒有 ISRC** | ytmusicapi `parsers/search.py` |
| 清單列表 | `browse` {browseId:"FEmusic_liked_playlists"} → grid,每項 browseId `VL<playlistId>`、曲數;grid 第 0 格是「新增清單」的按鈕(ytmusicapi 跳過它);有 continuation | ytmusicapi `mixins/library.py` |
| 清單內容 | `browse` {browseId:"VL<id>"} → `musicPlaylistShelfRenderer.contents[]`,每列 `playlistItemData: {videoId, playlistSetVideoId}`(**列 id,同一首兩列各自不同**)、videoType(`ATV` 歌曲 / `OMV` MV / `UGC` / `PRIVATELY_OWNED_TRACK` 上傳);下架的列 `MUSIC_ITEM_RENDERER_DISPLAY_POLICY_GREY_OUT`、「Song deleted」;自己的清單 header 是 `musicEditablePlaylistDetailHeaderRenderer`(= 可編輯);continuation | ytmusicapi `parsers/playlists.py`、`mixins/playlists.py` |
| 建清單 | `playlist/create` {title, privacyStatus:"PRIVATE"} → playlistId | 同上 |
| 寫入 | `browse/edit_playlist` {playlistId, actions:[…]},一次可多個 action:`ACTION_ADD_VIDEO {addedVideoId, dedupeOption:"DEDUPE_OPTION_SKIP"}`(不帶 dedupeOption 時同一首第二份會被拒)、`ACTION_REMOVE_VIDEO {setVideoId, removedVideoId}`(精準刪一列)、`ACTION_MOVE_VIDEO_BEFORE {setVideoId, movedSetVideoIdSuccessor}`、`ACTION_SET_PLAYLIST_NAME {playlistName}`;回 `status: STATUS_SUCCEEDED`,新增的列在 `playlistEditResults[].playlistEditVideoAddedResultData.setVideoId` | 同上 |
| 刪清單 | `playlist/delete` {playlistId}——只給探測腳本清場 | 同上 |
| 單曲 | `next` {videoId}(watch 清單第一項有歌名 / 歌手 / 專輯 / 時長)或 `player`(要 signatureTimestamp,重)→ GetTrack 用前者 | ytmusicapi get_watch_playlist / get_song |
| 限流 | ytmusicapi FAQ:有 rate limit,「正常使用不會撞到」;形狀(有沒有 Retry-After)沒人寫 | T0 記錄 |
| ToS | YouTube 服務條款禁止「以自動化工具存取本服務」(除公開搜尋引擎或書面許可)。社群工具(ytmusicapi 2020 起、yt-dlp)多年未見封鎖,但**不構成保證**。貼的是使用者 **Google 帳號在 YouTube 的登入 session**(`.youtube.com` 的 cookie):拿到它的人能以你的身分做 YouTube 上的任何事(不只 YouTube Music);要撤銷就到 Google 帳號 → 安全性 → 登出所有裝置。它對 google.com 的其他服務(Gmail / Drive)有沒有效,**T0 第 11 項唯讀驗過才寫進揭露**(揭露文字會被 `checkYouTubeDisclosure` 逐語系釘住,不能放沒驗過的安撫句) | [YouTube Terms of Service](https://www.youtube.com/t/terms) |

### 1.3 會咬人的事

1. **keychain 兩邊都有上限**:Windows Credential Manager 單筆 2560 bytes;macOS 的 go-keyring 先 base64 再塞進 `security -i` 的 4096 字命令,原值約 3 KB 就到頂
   (歌曲 wiki T1 實測:2 KB 原值變 2732 字)。一整段 YouTube cookie 常 2–4 KB(光 `LOGIN_INFO` 就近 1 KB),**兩個平台都要白名單**(候選:`__Secure-3PAPISID`、
   `__Secure-3PSID`、`SID` / `HSID` / `SSID` / `APISID` / `SAPISID`、`LOGIN_INFO`;哪些真的必要 T0 實測),白名單仍超過就拆兩筆 keychain。
2. **cookie 輪替**(§1.2 壽命列):若實測會死,指引改寫成 yt-dlp 的無痕視窗做法;失效偵測要準——InnerTube 對過期 cookie 可能回 401,也可能 200 但內容是未登入的畫面,
   兩種都要對到 `ErrAuthExpired`。
3. **多帳號**:瀏覽器同時登入多個 Google 帳號時 cookie 是共用的,`x-goog-authuser` 才決定是哪一個;品牌帳號還要 `x-goog-pageid`(或 `context.user.onBehalfOfUser`)。
   抄錯索引 = 讀到另一個帳號的清單(ytmusicapi issue #46)→ 貼上後立刻 `account_menu` 顯示帳號、要使用者確認(§3.1)。
4. **重複曲目**:不帶 `dedupeOption` 的 ADD 對已在清單裡的歌回失敗;決策 38 允許重複 → 一律帶 `DEDUPE_OPTION_SKIP`。
5. **下架 / 刪除的列**沒有 videoId:讀端丟掉、寫端重讀對齊時用同一條規則,不然位置全錯(同 Apple 的對齊原則)。
6. **InnerTube 的 JSON 是 UI 樹**(`flexColumns` / `runs` / `navigationEndpoint`),欄位靠位置與端點型別猜,Google 改版面就壞(2024 的 twoColumn 改版讓 ytmusicapi
   重寫 parser)。→ 解析集中在一個檔、每個 renderer 一個小函式、fixture 全部來自 T0 的真回應(去識別化)。
7. **上傳的歌(`PRIVATELY_OWNED_TRACK`)**只在該帳號存在:跨平台只能 fuzzy;能不能 ADD 進清單 T0 驗,不能就讀端標 `Unpushable`。
8. **rename 成功、items 整批失敗要回普通 error(照 Apple `apple.go:187`),不是 `PartialWriteError`**:`PartialWriteError.Written` 的語意是「平台現在是 want 的前 N 首」,
   `push.go:344` 在重讀 L′ 也失敗時會拿 `want[:Written]` 當 base;整批取代失敗時平台還是舊序,`want[:len(current)]` 卻是新序 → 斷網那一輪 base 記成新序,下一輪 pull
   把平台的舊序讀成「使用者在平台上重排」再拉回正本,等於撤銷使用者的修改(決策 57 講的那種災難)。普通 error 的代價只是名字晚一輪(下一輪 pull 讀到平台改名成同一個名字,no-op)。
   `PartialWriteError` 只在純 append 分多個請求、第 2 個請求起失敗時用(前綴語意才成立,同 Apple 的分批 POST)。
9. **同一首歌在 YouTube 上有兩個 videoId**:`ATV`(純音訊的「歌曲」)與 `OMV`(官方 MV)是兩個 id。使用者既有的 YouTube 清單裡放的常是 OMV,而 `Search`(歌曲 filter)把正本對到的是 ATV;
   沒有 ISRC 可以把兩者併成一個 cid,所以 pull 會多出一筆 `p:youtube:<omv>`、push 又把 ATV 當新曲推回去,`pl dedup` 也抓不到(它只認同 id 或同 ISRC)。Spotify 的單曲 / 專輯版
   有同樣形狀,靠 ISRC 合併;YouTube 唯一的出路是決策 21 的人工合併(`resolve --review` accept / pin)。這是 Q1 要一起衡量的同步品質事實,不是實作能繞的。

## 2. 可行性判定與路線

| | A 官方 Data API(使用者自建 GCP 專案) | **B InnerTube + 使用者自抄 cookie(推薦)** | C 混合(A 寫、B 搜) |
|---|---|---|---|
| 搬家 300 首 | 3 天(100 搜尋 / 日),寫入 200 首 / 日 | 一次跑完 | 搜尋一次跑完、寫入 200 首 / 日 |
| 對應品質 | video 標題 + 頻道名;無專輯;結果混 MV / 翻唱 | 歌曲實體:歌名 / 歌手 / 專輯 / 時長 | 同 B |
| 憑證 | 建 GCP 專案、啟用 API、發佈同意畫面(否則 7 天登出)、OAuth 一次(`prompt=select_account` 選帳號) | 切到要用的帳號、複製 request headers 貼上(1 分鐘;跟 Apple 一樣的動作) | 兩套都要 |
| 官方承諾 | 有(但 III.E.4.c 的 30 天刷新義務落到 capy 身上) | 無;Google 改版就壞(R-6 同款);ToS 自動化條款灰色地帶,而且綁的是 Google 帳號 | 兩邊的壞處都要 |
| 工程量 | OAuth 抄 Google 的;Data API JSON 乾淨 | InnerTube parser ~600–900 行 Go + fixture | 最大 |
| 跟專案的一致性 | Spotify 式 BYO app | **Apple 式 BYO web token**:揭露、精靈、web 接縫、守門測試全有模板 | — |

判定:**B 可行,而且是唯一能把「搬遷、同步」做成一次跑完的路**;A 的每天 100 次搜尋不是實作能繞的(唯一出口是 compliance audit)。代價是 Apple 那一類的
存在性風險再加一份、而且綁的是 Google 帳號——所以揭露要比 Apple 的更直白(§3.1);A 不寫程式,§1.1 留著當日後備援的起點(§7)。

## 3. 設計(最懶、不改 SPI)

### 3.1 憑證與帳號區分:`capy auth login youtube`

- **跟 Google Drive 登入完全分開**:keychain 新鍵 `youtube.headers`(JSON `{cookie, authuser, pageid}`);config 新欄 `youtube_account`(`{name, handle, channel_id}`,
  非機密,只給顯示與 §3.4 的前綴)。不讀、不寫、不推導 `google.token`;`auth logout youtube` 只刪 `youtube.*`、config 的 `youtube_account`,與這個平台在本機的快取列(清單列表、最近項目、
  `playlist_items_cache` 的 youtube 列,同 Spotify / Apple 的 logout 承諾)。Drive 帳號與 YouTube Music 帳號**可以不同**,文件明寫。
- 流程(照 Apple 的 `appleLogin` 收口成一個 persist):
  1. 揭露頁(Confirm 預設「否」,不可跳過;非 TTY 要 `--i-understand`):非 Google 官方支援、貼的是你 Google 帳號在 YouTube 的登入 session(拿到它的人能以你的身分
     操作 YouTube,不只 YouTube Music;要撤銷就到 Google 帳號 → 安全性 → 登出所有裝置)、Google 可能隨時讓它失效(重跑一次即可)、自動化存取 YouTube 違反其服務條款、
     風險自負、capy 只指導不擷取、只寫你自己建的清單。對 Gmail / Drive 有沒有效那一句,T0 第 11 項驗過才寫。
  2. 指引頁:**先在 music.youtube.com 右上角切到你要用的帳號**(可以跟 Drive 用的不同)→ DevTools → Network → 篩 `browse` → 任一 POST → Request Headers 整段複製。
  3. 貼上(TTY 用多行 Text;web 用 Secret textarea,同 `wiki setup` 的標頭欄,逾時 30 分鐘)。程式只留 `cookie`、`x-goog-authuser`、`x-goog-pageid`,其他丟掉;
     cookie 只留白名單(§1.3 第 1 項)。
  4. 驗證:`account/account_menu` → 印「偵測到的帳號:<name>(@handle)」→ TTY Confirm「是這個帳號嗎?」(非 TTY 印到 stderr 直接落地)。401 / 未登入畫面 →
     「cookie 貼錯或已失效」。
  5. 落地 keychain + config。
- 非 TTY:`CAPY_YOUTUBE_HEADERS` 環境變數(整段)或 `--headers-file <path>`;不收 argv 直帶(cookie 比 Apple token 長得多,`ps` 看得到)。web 對 `--headers-file` 403
  (進 `web_run.go` 的 deny 表);環境變數 web 行程本來就不看。
- `auth status`:多一段 `youtube:`——headers 在不在、帳號名與 handle;`--json` 加 `youtube: {state: ok|missing|keychain_error, account, handle}`(只增不改)。
  `doctor --provider youtube`:keychain 有、`account_menu` 打得通、回來的帳號跟 config 記的一致。
- 隱藏 `--auto`:**不做**(Apple 的是使用者點名的特例;Q6)。

### 3.2 provider `youtube`(新套件 `internal/provider/youtube`;憑證在 `internal/auth/youtube`)

- `ID() = "youtube"`(永久:進 Drive `Links` 的鍵與 TSV 的 `provider` 欄;Q7)、`DisplayName() = "YouTube Music"`。
- `Caps()`:`CapSearch | CapPlaylistRead | CapPlaylistCreate | CapPlaylistAppend | CapPlaylistRemove | CapPlaylistReorder | CapPlaylistRename`(+ `CapDeviceBound`,見 §3.4)。
  不宣告 ISRC 兩個位元(平台沒有)、不宣告播放、不宣告 ArtistSearch(Q8)。
- track id = `videoId`;cid 一律 `p:youtube:<videoId>`(§5.2 那一列早寫了「YouTube Music 完全無 ISRC → 只能 Layer 2 + 人工」);`Track.URL = https://music.youtube.com/watch?v=<videoId>`
  (表格連回平台,決策 42);`Artists` 來自 runs 的歌手段;`Album`、`DurationMS`(「m:ss」/「h:mm:ss」)、`Explicit`。
- `Search`:一頁(約 20 筆)、只用「歌曲」filter;`Query.Limit` 超過一頁就截(Q9)。
- `ListPlaylists`:`FEmusic_liked_playlists` 全部 continuation;`PlaylistRef{ID, Name, Owner: 帳號名, Total: 曲數, Version: ""}`;`LM`(喜歡的音樂,Q5)與別人的
  (只是儲存進資料庫的)清單標 `Unwritable`(plan 階段就跳過,同 Apple 的 canEdit:false);「可編輯」的判準 T0 決定(grid 有沒有 owned 訊號;沒有就寫入前 `browse` 一次看 header 型別,不在列表時做)。
- `GetPlaylistItems`:`VL<id>` + continuation;沒 videoId 的列丟掉(§1.3 第 5 項);`Raw` 留原 renderer。
- `GetTrack`:`next` {videoId} 取第一項;找不到 → `ErrNotFound`。
- 錯誤映射:401 / 403 / 回未登入畫面 → `ErrAuthExpired`(訊息指向 `capy auth login youtube`);404 / 空 → `ErrNotFound`;429 / 5xx 交給既有 `provider.Backoff`(有 Retry-After 就照它)。
- `Health`:`account_menu`。

### 3.3 寫端:`ApplyOps` / `Pushable` / `CreatePlaylist`

```
ApplyOps(ctx, id, current, ops):
  want, name := provider.ApplyPlaylistOps(current, ops)          // 純函式;越界先錯、零寫入
  可編輯?(header 型別)否 → 錯,零寫入(第二道防線;plan 階段 Unwritable 已擋)
  rows := browse VL<id> 重讀 (videoId, setVideoId)[](丟掉沒 videoId 的列)
  rows 的 videoId 序列 != current → 錯「平台已變」,零寫入          // §6.5.2 規則 6 的併發比對,同 Apple
  name != "" → 一個 edit_playlist:ACTION_SET_PLAYLIST_NAME(先做便宜的;失敗回錯,Renamed=false)
  want == current → 回
  pure append(want[:len(current)] == current)→ 一個 edit_playlist:尾端多出來的每首 ACTION_ADD_VIDEO(+DEDUPE_OPTION_SKIP)
  否則(Q4,推薦 a)→ 一個 edit_playlist:先 ACTION_REMOVE_VIDEO 每一列(setVideoId)、再 ACTION_ADD_VIDEO 照 want 順序
        = Spotify PUT / Apple PUT 同款的「整批取代」;加入日期會重設(README 對 Spotify 已這樣寫)
  失敗語意:一個請求 = 沒有半截(T0 第 5 項驗原子性);rename 成功、items 失敗 → 回普通 error(§1.3 第 8 項;push.go 當「平台沒動」,名字晚一輪 no-op)
        純 append 若超過單請求上限而分批、第 2 批起失敗 → *PartialWriteError{Written: len(current)+已加的}(前綴語意成立,同 Apple)
Pushable(id): id != ""(上傳的歌能不能 ADD 看 T0 第 7 項;不能就讀端標 Unpushable,這裡照 Track 走)
CreatePlaylist(name): playlist/create PRIVATE → 輪詢列表到出現才回(1→2→4→8→15 s,同 Apple;沒延遲就第一次就回)
```

- 備案(b):逐 op 翻譯(add → ADD + MOVE_BEFORE、remove → REMOVE(setVideoId)、move → MOVE_BEFORE),每個 op 一個請求;中途失敗回 `PartialWriteError`,
  `Written` 填「套完前 k 個 op 之後的長度」——它只是 L′ 重讀失敗時的估計值(規則 7 主路徑是重讀)。只在 (a) 被 T0 否決(單請求不原子、或 action 數上限太低)時採用。
- 不做 rebuild(不刪清單重建),同決策 30 / 49。
- **絕不送 `playlist/delete`**(只在探測腳本的清場;測試斷言 fake server 從未收到)。

### 3.4 清單 id 帶帳號(Q2,推薦做)

使用者的情境:Drive 帳號一個、YouTube Music 帳號可能不同,而且可能換。沒有這條時,`pull.go:895` 會把「這台登的帳號看不到的清單」當 gone → 刪 link、清 base
(正本不動)——換個 YouTube 帳號登入、或第二台電腦登了別的帳號,所有 YouTube 連結靜默消失。

做法照 local(決策 33)抄:playlist id = `<channel_id>/<playlistId>`(`channel_id` 從 `account_menu` 取,沒有就用 handle;T0 第 1 項),track id 不帶;
宣告 `CapDeviceBound` + 實作 `Foreign(id)` = 前綴 ≠ 目前帳號 → pull / push / sync 只跳過並點名「屬於 YouTube 帳號 @xxx」、不 gone、不 unlink;`pl link` 撞到別的帳號的
link 就接管;`resolvePlaylistID` 已接受只打後半段。前綴用 channel id(`UC…`,不會變);`account_menu` 沒給就退回 handle——handle 改名會讓每個連結變 foreign,靠接管復原(跟重灌一樣)。
跳過的訊息只印得出前綴本身(config 只記目前帳號的名字;別的帳號的 `UC…` 沒地方查名字),訊息要附「用那個帳號登入後 capy pl link 接回」的指路。要改的:三句 `belongs to device {device}` 的 i18n 改成 `{owner}`、`deviceName()` 改成依 provider 給名字
(local 查 manifest、youtube 用前綴);`CapDeviceBound` 的註解改成「id 只在一個範圍(裝置 / 帳號)有意義」,位元不動(Q10)。約 80 行 + 測試。

不做的代價:文件寫「所有裝置登同一個 YouTube Music 帳號;換帳號後 YouTube 連結會被當成已刪除,重新 `pl link`」。

### 3.5 CLI / TUI / web

- CLI 不加新命令:`providerIDs` 加 `"youtube"`、`newProvider` 加一個 case、`auth login | logout | status` 與 `doctor` 加 youtube;`play --provider youtube` 照舊回「不支援播放」。
  TUI 無事(選單走 providerIDs)。
- web:`app.js providers.list`、`move.js CAN_CREATE`、`common.js providerName`、`account.js` 帳號列(state / 帳號 / 重新連接)、`search.js` 對 youtube 藏播放鈕
  (列上已有連回 YouTube Music 的連結);`auth login youtube` 走提示橋:`webYouTubeDisclosure`(server 端判定)+ `webYouTubeWizardInputs`(Secret textarea + 帳號確認),進 `installWebSeams`。
- i18n:`auth.youtube.*`、`youtube.*`、`platform.*` 兩語系同補;`checkYouTubeDisclosure` 每語系逐句釘(同 Apple;新語系不補就紅)。

### 3.6 文件與政策(**T1 就要,同一個 PR**——T1 已經連到 music.youtube.com)

- CLAUDE.md:新增一條硬約束(照 Apple 那條:使用者自抄、只指導絕不擷取、揭露在指令內不可跳過、只寫自建清單、不送 `playlist/delete`)。
- 隱私權政策 ×2:§3「程式只會連到 …、YouTube(music.youtube.com)」、**送出去的是什麼**——`migrate` / `resolve` 對 YouTube 搜尋時,把你在 Spotify / Apple / 本機清單裡的
  **歌名與歌手**當搜尋字送給 Google;讀寫的是你 YouTube Music 帳號裡的播放清單;cookie 只存 keychain。§6 HTTPS 句、商標句一起改;`site_test.go` 加一條釘
  `capy auth login youtube`、`music.youtube.com`、「歌名與歌手」三者都在(照 `TestPrivacyPolicyDisclosesTheAIEndpoint` 的做法)。條款 ×2:能力句。首頁 ×2:meta 兩處、能力句、憑證段(「非 Google 官方支援」)。
- README ×2:新段「YouTube Music:複製你自己的登入 cookie」(揭露 + 步驟 + 非互動用法)、首句平台清單、命令表、`auth status --json` 表加 `youtube.*`、logout 段、
  搬家段(可當來源與目標)。
- 指南 ×2 + `go test ./site/ -run TestGuideOnSiteIsCurrent -update` + 重發兩個 Artifact(T3)。
- ARCHITECTURE:§1.5 表、§2 方框、§3 Caps 註解、§4.6、§4.5 表兩列、§5.2 那一列、§8 ToS 一列、§8.5.4 R-7、§9 P10、附錄 A(`auth login youtube`)、
  附錄 B(InnerTube 版面 / cookie 輪替)、附錄 C 決策 60。

## 4. T0 探測(需要使用者授權:用你的 YouTube Music 帳號貼一次 headers、建一個拋棄式清單並在結尾刪掉)

腳本 `scripts/p10/youtube-probe.sh`(curl + jq;headers 由使用者貼進環境變數,腳本不碰瀏覽器),照 Apple 的 R-8 寫法:每項印 HTTP 狀態與關鍵欄位,
回應存成去識別化的 fixture 給 parser 測試。

1. `account_menu`:帳號名、handle、有沒有 channel id / email;`x-goog-authuser` 換成別的索引會不會變成另一個帳號(瀏覽器同時登入多個帳號才測得到)。
2. **cookie 白名單與壽命**:只帶白名單 cookie 能不能過;整段與白名單各幾 bytes(對 Windows 2560、macOS 約 3 KB 兩個上限);複製後**繼續正常使用瀏覽器**,第 1、3、7 天各打一次 `account_menu`,
   看會不會死(§1.2 的矛盾)。
3. `FEmusic_liked_playlists`:第 0 格是什麼、`LM` 在不在列表、有沒有 owned / 可編輯訊號、continuation 形狀、曲數欄。
4. `VL<id>` 一份 ≥100 首的清單:continuation、`playlistSetVideoId`、videoType 分布、下架列長什麼樣;同一首兩列的 setVideoId 是否不同。
5. **寫入原子性與上限**(拋棄式清單):`playlist/create` → 立刻 `FEmusic_liked_playlists` 看幾秒出現;ADD 一個請求從 100 首起加倍到失敗、至少打到你最大那份真清單的列數
   (Apple 探測是 538 列才安心);同一請求 REMOVE 全部 + ADD 全部(反序)
   看是否成功且順序正確;故意夾一個壞 videoId 看整包失敗還是部分套用;同一首兩份(帶 / 不帶 dedupeOption);`ACTION_SET_PLAYLIST_NAME`。
6. `search` 歌曲 filter 對中文 / 日文 / 英文各 3 首(拿 Spotify 正本的歌名 + 歌手)看第一頁形狀與命中。
7. 上傳的歌能不能 ADD;`PRIVATELY_OWNED_TRACK` 的列長什麼樣(帳號沒有上傳的歌就略過)。
8. `next` {videoId} 第一項的欄位(GetTrack)。
9. 限流:連打 60 次 `search`,看 429 有沒有 `Retry-After`。
10. 結尾 `playlist/delete`,列表確認消失。
11. **唯讀**:拿同一段 cookie 打一個 google.com 的登入後端點(例如 `https://myaccount.google.com/` 或 Drive API 的 `about`),預期被導去登入 / 401;結果決定揭露能不能寫
    「對 Gmail / Drive 無效」。

## 5. 測試(每一則都是修之前會 fail 的)

- parser:每個 renderer 一個 fixture(來自 §4)+ 邊界(無 videoId 的列、無專輯、時長 `h:mm:ss`、explicit、continuation)。
- auth:headers 解析(只留三個、cookie 白名單、引號 / 大小寫 / 多餘空白)、SAPISIDHASH 對固定時間戳的已知值、Windows 大小上限(超過拆兩筆或報錯)、
  keychain 已存的舊格式壞掉時先報錯不落地(同 `wiki setup` 的做法)。
- `ApplyOps` 對 fake server:純 append 只送 ADD、非 append 送 REMOVE+ADD 一包、對齊不符零寫入、rename 先、rename 成功後 items 失敗回 `PartialWriteError`、
  **從未收到 `playlist/delete`**、Unwritable 零寫入、`DEDUPE_OPTION_SKIP` 一定帶。
- 帳號範圍(§3.4):`Foreign` 判定、pull 跳過不 gone、link 接管、`resolvePlaylistID` 後半段。
- CLI:`auth login youtube` 三條路徑每語系都帶完整揭露(`checkYouTubeDisclosure`)、非 TTY 缺 `--i-understand` 拒絕、web 對 `--headers-file` 403、
  `auth status --json` 只增不改、logout 只刪 youtube 的東西、`--provider youtube` 的 play 回不支援。
- 文件:`site_test.go` 新釘;`go test ./internal/i18n/` 守門;README 兩份的代碼表不動(`reason_code_test.go`)。
- e2e(照 `apple_e2e_test.go`):Spotify(假)→ YouTube(假)migrate + sync 一輪 + dedup。

## 6. PR 切法

| PR | 內容 | 等什麼 |
|---|---|---|
| T0 | 本文;§8 拍板後同 PR 補探測腳本、ARCHITECTURE、附錄 C 決策 60 | §8;§4 的授權與結果 |
| T1 | `internal/auth/youtube` + `internal/provider/youtube` 讀端(search / list / items / GetTrack)+ `auth login | logout | status` / `doctor` + i18n + 揭露守門測試 + **政策 ×2、條款 ×2、首頁 ×2、README ×2、CLAUDE.md、`site_test` 釘**;web 只加帳號頁與登入提示橋 | T0 的 fixture |
| T2 | 寫端(`CreatePlaylist` / `ApplyOps` / `Pushable`)+ §3.4 帳號範圍 + e2e;README 搬家 / push 段補 YouTube | T0 第 5 項 |
| T3 | web 其餘(搬家精靈、搜尋頁)+ 指南 ×2 + 重發 Artifact + ARCHITECTURE P10 收尾 | — |

## 7. 明確不做(要就另開)

- 官方 Data API 路線(A):不寫程式;§1.1 是日後要做時的起點。
- 播放 / 遙控(`capy play --provider youtube`):不做;可能的形狀是決策 52 那種「在 YouTube Music 開啟、照實說沒播」。
- 隱藏 `--auto`。
- 喜歡的音樂(`LM`)寫入(那是 like / unlike 端點)、上傳的歌的管理、podcast、YouTube(非 Music)清單、協作清單寫入。
- 搜尋翻頁、ArtistSearch、豐富欄位(封面 / 試聽 / 發行日期 / 曲風)——InnerTube 有封面與年份,但表格已能連回平台,先不解析。
- 自動比對 Drive 帳號與 YouTube 帳號是否相同(`account_menu` 沒有 email,比不了;也不需要——兩邊各自顯示就夠)。
- Data API 開發者政策的 30 天刷新機制(只有走 A 才需要)。

## 8. 待使用者定案的 Q(推薦選項放前面)

- **Q1 路線**:(a)**B:InnerTube + 使用者自抄 cookie**(§2)/(b)A 官方 Data API + 使用者自建 GCP 專案(接受每天 100 次搜尋、200 首寫入)/(c)C 混合。
- **Q2 帳號範圍的清單 id**(§3.4):(a)**做**(換帳號、多裝置不同帳號都只跳過不 unlink;約 80 行 + 三句 i18n)/(b)不做(文件寫「各裝置登同一帳號」)。
- **Q3 貼什麼**:(a)**整段 Request Headers**(ytmusicapi 同款;程式只留三個 header 與 cookie 白名單)/(b)只貼 `cookie` 一行、另問 authuser 索引(少貼,但多帳號的人容易抄錯)。
- **Q4 ApplyOps 形狀**:(a)**一個請求整批取代**(§3.3;T0 第 5 項驗原子性)/(b)逐 op 翻譯(setVideoId 搬動;保留加入日期,但多請求、半截狀態難描述)。
- **Q5 `LM`(喜歡的音樂)**:(a)**列出、只讀(Unwritable)**——能當來源搬去別的平台 /(b)不列。
- **Q6 隱藏 `--auto`**:(a)**不做** /(b)做(AppleScript 讀已登入分頁的 cookie——只 macOS、更脆)。
- **Q7 provider id**:(a)**`youtube`**(清單本來就是 YouTube 清單、網址也在 youtube.com;顯示名「YouTube Music」)/(b)`ytmusic`。永久:進 Drive `Links` 鍵與 TSV。
- **Q8 能力**:(a)**只宣告搜尋 + 清單讀寫七個位元** /(b)順手宣告 ArtistSearch(InnerTube 有 artist filter)。
- **Q9 搜尋只取第一頁(約 20 筆)**:(a)**是** /(b)翻頁到 Limit。
- **Q10 `CapDeviceBound` 命名**:(a)**位元不動、註解改「id 只在一個範圍有意義」**、CLI 訊息改 `{owner}` /(b)新位元 `CapAccountBound`(多一個位元、多一個 as* 判斷)。
- **Q11 T0 探測授權**:要用你的 YouTube Music 帳號貼一次 headers、建一個拋棄式清單(結尾刪掉),並在接下來 7 天照常用瀏覽器以測 cookie 壽命。
  我不會自己跑任何寫入:你說可以我才跑,或你自己跑腳本把輸出貼給我。

## 9. 決策 60(草稿;§8 定案後寫進附錄 C)

| # | 議題 | 定案 | 摘要理由 |
|---|---|---|---|
| 60 | YouTube Music provider(2026-09-29) | InnerTube(music.youtube.com 自己用的端點)+ 使用者自抄 cookie(BYO,非官方;keychain `youtube.headers`,config `youtube_account`);跟 Google Drive 登入完全分開、可以是不同帳號;清單 id `<channel_id>/<playlistId>`、`CapDeviceBound` 的語意擴成「範圍」;寫入只寫自己建的清單、一次 `edit_playlist` 整批取代、絕不 `playlist/delete`;不做播放、不做 `--auto`;官方 Data API 不採 | 官方 API 每個專案每天只有 100 次搜尋(搬 300 首要 3 天)、寫入 200 首,擴配額要 compliance audit;`youtube` scope 是 sensitive,掛上內建 client 會讓整個專案(含 Drive)送驗,只能 BYO GCP 專案;沒有 ISRC 與歌手 / 專輯欄位。InnerTube 是 Apple 決策 8 同一類的灰色地帶,揭露要明講「這是 Google 帳號在 YouTube 的 session」;帳號範圍的 id 是因為使用者明說多帳號、而 gone 會靜默 unlink |

## 10. 驗收清單(R-39–R-46;T1–T3 都進 main 後才在 ARCHITECTURE P10 打 ✅)

- R-39 `auth login youtube` 三條路徑(精靈 / 環境變數 / web)揭露都在、偵測到的帳號正確。
- R-40 `auth status` 同時顯示 Google email 與 YouTube 帳號,兩者不同也一切正常。
- R-41 `pl list --provider youtube` 看得到清單;`LM` 標 Unwritable。
- R-42 `migrate <清單> --from spotify --to youtube`(拋棄式目標)順序正確、沒對到的進 review、`resolve --review` 能釘。
- R-43 `pl sync` 一輪(YouTube 端加 / 刪 / 搬一首,Spotify 端也改)兩邊收斂、順序照決策 38、重複曲目保留。
- R-44 換 YouTube 帳號重登後,舊帳號的連結只跳過不 unlink(Q2a)。
- R-45 白名單 cookie 存得進 macOS Keychain 與 Windows Credential Manager(兩個上限)。
- R-46 複製後 7 天 cookie 仍有效(或指引已改成無痕做法、失效訊息指向重新登入)。
