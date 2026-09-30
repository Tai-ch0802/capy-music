# web「我的清單」改版:點播、正本與平台清單的關係、就地同步、歌曲 wiki(2026-09-30 計畫)

**狀態:程式與文件全部進 main,等維護者跑 §8 的 R-47–R-55。** 使用者 2026-09-30 定案:Q1–Q9 全部照推薦(§6),T1 開工(T0 不當 gate,同 web mode 的慣例)。定案內容寫成附錄 C 決策 61,進度在 ARCHITECTURE §9 的 P11(T4)。
版面示意圖(四個狀態,按鈕與字級照 app.css 的真實尺寸):https://claude.ai/artifact/NzK4uk8ARBiTWEHzLfHfpc
Q 用計畫內編號 Q1…(同 2026-09-29-youtube-music.md 的寫法,不接全域的 Q82)。定案後寫成附錄 C 決策 61。
**#121 review 後的修正**(2026-09-30):`--strict` 改名 `--new-only`(跟 push / sync 內部的 strict 撞名);§3.5 前置 2 寫明要改 `sameNamePlaylists` / `readable()` 才拿得到曲數、404 歸「曲數不明」;「納入」停在第二步的收尾;web 擋字檢查掃每個參數,wiki 改用 `--title=` 寫法;「在 Apple Music 建一份」補決策 49 的揭露句。

**T1 實作時跟本文不同的地方**(2026-09-30):
- **Apple Music 欄只在這台放得了時出現**(macOS 而且 Apple 沒有「沒登入 / 過期」)。Apple 的格子只有 ▶、沒有連結,照 §3.2「不能用就只留連結」會變成一整欄空格,所以整欄不出現;Spotify 沒登入時照舊只拿掉 ▶、留連結。判斷 macOS 看 `navigator.userAgent`(瀏覽器與 capy 同一台)。
- **讀不到 `auth status`(命令失敗)= 不知道 = ▶ 照給**,讓 CLI 說原因;只有讀到了而且那家不是 ok 才拿掉。
- 進頁先用 `quiet` 跑 `auth status --json`,在它的 `onExit` 裡再用 `quiet` 跑 `export`(不只重讀時才 quiet:整份 JSON 不灌進主控台)。
- 「看 X 上的內容」按鈕、下方「平台上現有的清單」照舊保留到 T2(由連結面板與「各平台上的清單」取代)。T1 的空白態只指到搬家,「納入」的說法等 T2。
- 窄版的位置欄釘在左邊(`position: absolute`),換行後的每一行都從同一條線開始;表格元素改 `display` 之後補上 `role="table" / row / cell / columnheader`,報讀還是表格。
- 表頭「時長」的英文是 Length;T1 自審量到英文的「Length」與「YouTube Music」在原本的欄寬會被截成「Le…」「YouTube M…」,時長欄改 4.5rem、YouTube Music 欄改 7.5rem,表頭改成可以換行。
- 空白態從兩種變三種(見 §3.4):「連了 Google Drive、這台沒有本機資料」不可以說成「沒有清單」。
- 單鍵層放過 `<summary>` 上的空白鍵(app.js;根因修一處,清單頁的說明、側欄的「更多」、搬家精靈的完整表格一起好)。
- 左欄清單是捲動容器,焦點環改畫在項目裡面(`outline-offset: -4px`)。

**T2 拆成三個 PR**(2026-09-30):T2a = §3.5 的 CLI 前置(`pl link --new-only`、同名清單依曲數的說法;#123);T2b = 連結面板、「同步這份清單」、「在 X 建一份」、「找對應 / 逐首決定」、收尾規則、回到頁面時重讀;T2c = 「各平台上的清單」與「納入」。拆開是為了讓 web 那部分的 review 好讀,範圍不變。

**T2b 實作時跟本文不同的地方**(2026-09-30):
- 「看 X 上的內容」(`pl show`)從這一頁拿掉,由連結面板取代;Spotify 清單的連結搬到面板的 Spotify 那一列。
- 變更表的畫法沒有搬到 table.js,改成從 sync.js 匯出 `changeTable`(同步頁行為不變,搬家頁已有從別頁 import 的先例)。
- 「各平台上的清單已經讀過、那個平台有同名清單時不給『開始』」要等 T2c 才有資料可比,T2b 先靠 CLI 的同名說法(T2a 已改成只在確定是空的時才建議連)。
- 每個寫入流程收尾後都用 `quiet` 重讀 export;就地流程區只在切到別份清單時清空,重讀後搬進新畫的右欄,收尾那句留著。
- dock 的 notice 仍會同時出現 CLI 原句(主控台頁看不到時 `console.report()` 一律會說,那是所有頁共用的行為),頁面自己的收尾句照 §3.5 的表。

**T2c 實作時跟本文不同的地方**(2026-09-30):
- 「各平台上的清單」只讀 Spotify / Apple Music / YouTube Music 裡這台有登入的;本機不讀(沒有登入可以判斷、要 `local_root`,而且綁裝置),本機的 M3U 要納入請用主控台的 `pl link`。
- 清單名剛好是 web 擋下的旗標字時,不在頁面複製一份擋字清單:伺服器拒絕的原因照收尾規則顯示(refused 的訊息)。
- 讀清單與納入在流程區有寫入命令在跑時一樣擋下(同換清單)。
- 「各平台上的清單」放在兩欄下方的全寬區段(示意圖畫在左欄底下):每一列要放曲數、狀態、Spotify 連結與動作,左欄 16rem 放不下;窄版時兩欄本來就疊成一欄。
- 這台還沒讀到正本(export 失敗)時,讀到的平台清單只列出來、不標狀態也不給納入(分不出來);YouTube 缺帳號資料時那一段說要重新登入;讀取時按中止就不再讀下一家,沒讀的那家照列、說沒讀(#125 review)。
- 「在 X 建一份」的同名預檢不算曲數確定是 0 的平台清單:那多半是上次建好了、連結沒寫進 Drive 的那份,照給「開始」,讓 CLI 停在第一步給接回去的 `pl link`(#125 review)。別人的清單(Spotify 追蹤的)照樣給「納入」:`pl list` 的 OWNER 是顯示名稱、頁面拿不到自己的 Spotify 使用者 id,分不出來;按了會在第一步被 CLI 照實擋下、零寫入。

**T3 實作時跟本文不同的地方**(2026-09-30):
- 「設定 AI 端點」只在命令真的跑了而失敗(exit > 0、不是中止)時給;被伺服器拒絕或序列槽被佔著(-1)命令根本沒跑,設定救不了,只給「查詢」。
- 對話框的按鈕區在回答上面(串流中長出來的字不會把「中止」推出畫面);`wiki setup` 跑的時候按鈕區是空的,✕ 就是停。表單欄位裡的 Esc 是取消那一題(console.js 已經 `preventDefault`,不會連對話框一起關)。
- 收尾區換按鈕時,焦點不在對話框裡了就交給新的第一顆(沒有就 ✕);做完說一句「寫好了。」——dock 的報讀被 `showModal()` 的 inert 蓋住,不說螢幕閱讀器聽不到結束。
- Wiki 欄的表頭是空字串(每顆按鈕的報讀名稱已經帶曲名,同其他動作欄);窄版時 Wiki 靠右。
- 沒有曲名的列(這台沒有這首的資料)格子照留、不給按鈕,欄才對齊。
- 換頁時(瀏覽器上一頁、觸控板往回滑)app.js 的 `showPage()` 會關掉藏起來的頁面裡還開著的對話框:不然它看不見卻還是 modal,整頁 inert、單鍵全失效;關掉 = 中止。node 替身沒有「hidden 就不畫」,這條與 §4 第 7 點的鍵盤單鍵層都由 web_test.go 的靜態比對釘住。
- 精靈的表單被關掉(✕ / Esc)或等太久時,CLI 回的是 huh 的 "user aborted":對話框改說「設定沒有做完,可以再設定一次」,不用警告色,也不說「沒有寫入」(選模型之前端點、金鑰、母語已經存了)。
- 政策不只「補一句來源」(Q5 原本的寫法):Wiki 鈕送的歌名與歌手取自 Drive,屬於 Google 使用者資料,所以 §3 列成一項用途、§5 把它從「不是 Google 使用者資料」「資料從未離開」的論證裡拆出來,明說只為了使用者按下要看的功能、只在按下時、只送到使用者設定的端點、capy 不拿來訓練模型,端點會不會保留或訓練由它自己的政策決定。依據是 Google API Services User Data Policy 的 Limited Use 例外(使用者看得到、顯眼的功能,且經使用者同意)與 Workspace API User Data Policy 對 AI 模型的限制(#126 review)。
- 只有空白的曲名同 CLI 的 `TrimSpace` 當成沒有;對話框的「中止」不理連點的第二下(連點「查詢」的第二下會落在剛換上的這顆)。

**T4 實作時跟本文不同的地方**(2026-09-30):
- 指南除了補「我的清單」一段,「歌曲 wiki」那一條也補了新入口(本來只寫「正在播的歌」);README 兩份的網頁頁面清單也替「我的清單」補一句說明。
- §9 的歷史紀錄(決策 46 那列、2026-09-18-web-consumer.md)只追加日期註記、不改原文;ARCHITECTURE 附錄 A 的 migrate 那行是現況描述,直接改。

本文送出前跑過一輪三視角的對抗式審查(硬約束 / 事實與命令 / 需求與易用性),31 則裡 24 則查證成立,都已改進本文;最重要的是 §3.5 的「只建新的」CLI 旗標。

## 0. 需求與現況(2026-09-30 讀碼查證)

使用者的五點,對到現在的程式:

| # | 需求 | 現況 | 依據 |
|---|---|---|---|
| 1 | 從清單直接點播,Apple Music / Spotify / YouTube 都要能轉導播放 | 清單頁只有「在 Spotify 上聽」連結(S7),沒有播放鈕;搜尋頁才有 `play --id` | playlists.js:83-90;search.js:44-74 |
| 2 | Drive 正本與各平台清單的關聯看不懂 | lead 一句帶過;左欄晶片是原始 id(`apple ✓`);下方「平台上現有的清單」是另一張 `pl list` 表,跟正本沒有對照 | playlists.js:12-28、71;zh-TW.json:1309、1314 |
| 3 | 無法在這頁引導跨平台同步 | 這頁沒有任何寫入動作;空白態叫人「到『同步』把平台上的清單連起來」,但同步頁沒有 link | zh-TW.json:1305;sync.js:26-31 |
| 4 | 曲目多時整頁被拉長;欄位多到要左右捲 | 表格用沒有高度上限的 `.tbl-wrap`;CID 欄 `nowrap`(`p:local:` 路徑很長)+ 連結欄 `nowrap` | playlists.js:83、116-121;app.css:205、226-228 |
| 5 | 每首歌加 wiki 按鈕,開 dialog 或到 wiki 頁 | 沒有;wiki 頁只能查「正在播」或手打歌名 | wiki.js:116-153 |

會影響設計的既有事實:

- **資料只有本機的 `capy export`**:它是這台電腦上次成功 COMMIT 時的 Drive 快照,別台之後的改動看不到;沒有「最後同步時間」這種欄位,也沒有 `pl status`(escape.go:26-27;pull.go:159-191、383-392;pl.go:19)。寫入命令(`pl link` 等)則每次都先讀最新的 Drive(pull.go:159-193)——**頁面用 export 做的任何判斷都可能過時,會影響順序或連結的護欄一律放在 CLI**。
- **mapping 全域共用**:`tracks.json` 每首歌一組 `mappings`,不分清單。Spotify 存 22 碼 track id,Spotify 上的本機檔存 `spotify:local:…` uri(spotify/client.go:178);Apple 有 catalog 對應時存數字 id、沒有時存資料庫列 id(`i.` / 協作清單 `a.`,apple/client.go:371-373、420-432);YouTube 存 videoId;local 存相對路徑。`resolve` 只替「清單已連結的平台」找 mapping(resolve/resolve.go:274-310)。
- **點播只有 `capy play --id <id> --provider <p>`**:Spotify 走 Connect(沒有作用中的裝置 = 404 → 可行動的訊息、exit 1;要 Premium 但程式偵測不到);Apple 只在 macOS,資料庫裡找到唯一一首才真的播,否則 `open music://…` 在 Music.app 打開並標出來、照實說、exit 0 不印 ▶(決策 52);local 與 YouTube 沒有播放(spotify/client.go:350-364;apple/player_darwin.go:102-142、157-227;player.go:137-156)。`--id` 不檢查形狀:Apple 的 `i.` / `a.` 會打 catalog 端點失敗,`spotify:local:…` 會被組成 `spotify:track:spotify:local:…`(player.go:73;spotify/spotify.go:101)。
- **Spotify 連回(計畫 2026-09-24 §1.7 S7)**:顯示 Spotify 的曲目或清單,每一列都要有看得到的「在 Spotify 上聽」(規範核可的字),只連 22 碼 id(table.js:43-60)。
- **連結是一對一**:一份正本在每個平台最多連一份,一份平台清單也只連一份正本(canon.go:212;pull.go:562-567)。`pl link` 沒有 `--dry-run` 也沒有確認、不檢查 Unwritable(pull.go:473-626、460-471);名稱不分大小寫比對(`strings.EqualFold`,pull.go:131-152),找到同名正本就連上去、找不到才建新的;正本在那個平台已經連著**別台電腦或別的 YouTube 帳號**的清單時,`pl link` 直接接管(pull.go:577-584,決策 33、60)。
- **把「既有、非空」的平台清單連到「已有曲目」的正本,第一次 pull 會採平台的順序**:沒有 base 時規則 6′ 不清 `moved`(derive.go:114-129),跟決策 38 衝(ARCHITECTURE.md:1049 決策 39 的理由)。從正本新增一個平台、不會重排的路是 `pl link <pid> <p> --create` → `resolve <pid> --provider <p>` → `pl sync <pid> --provider <p>`(pull.go:585-606;resolve.go:574 寫入前 `confirmWrite`;sync 的 pull 半邊把剛建的空清單記成 base、push 半邊照正本順序推)。
- **`pl link --create` 遇到平台上已有同名清單會停下(exit 1,零寫入),但錯誤原文叫人「要連它就 capy pl link …」**——不管那份是不是空的(pull.go:586-598;zh-TW.json:507-508)。這句話正好引導到上一條的重排路徑。
- **wiki**:沒有 `--cid`;只能 `--title` + `--artist`(單一字串),快取 key 是 `ai.WikiCacheKey`,歌手用 `", "` 串(internal/ai/prompt.go:75-77;wiki_run.go:71-74、96),命中不打 AI;`--title` 是空的就改問正在播的歌(wiki_run.go:69-80);沒設定端點 = 第一步 exit 1(wiki_run.go:56-59),頁面拿不到那句的 key(web.go:250-257)。問 AI 期間佔著序列槽 30–60 秒(計畫 2026-09-28 Q71)。
- **web 的規矩**:所有命令走同一個序列槽(`Console.run`);`promptHost` 只把提示畫在頁面裡,**表格照樣進主控台的區塊**,頁面要自己掛 `onTable`(console.js:158、341-346、467);沒給 `promptHost` 就跳到主控台(console.js:466-484);`quiet` 不進主控台但 `onStdout` / `onExit` 照常(console.js:161-164、202、339);`onExit` 先於喚醒排隊中的 `con.idle`(console.js:185、202、213-223),**連續的命令要在 `onExit` 裡接下一個**,用 `await` 串會被排隊的讀取插隊;用 `args` 陣列時照樣經 cobra 解析,名稱以 `-` 開頭要放在 `--` 後面(web_run.go:202-207、266-267);頁面絕不代加 `--yes` / `--force`(決策 46);webui 不可有中文字面、新 key 兩份語系同補(決策 50);CSP 零 inline。
- **確認提示按 ✕ 或逾時是 exit 1 + `user aborted`,按「取消」才是 exit 2**(web_prompt.go:323-329;pull.go:73);搬家精靈用 `onPromptClosed` 分辨(move.js:202、448-457)。`pl sync` 不會因為 push 的前提回 exit 3(非 strict,push.go:171-177);exit 3 是刪除閾值或 Drive 不完整。
- **鍵盤單鍵層只擋鍵位表那一個 dialog**:別的 dialog 開著時按 1–9 會在背後換頁(app.js:165-175;web_test.go:1351 釘著 `keysDialog.open`)。
- **node 行為測試綁著現在的清單頁結構**:情境 8k / 8n / 8o 用 children 索引、`cells[0]` 是 CID、晶片字面 `apple ✓`、`pl list` 表的 Spotify 清單連結(testdata/webui_console.mjs:584-896,:759)。

## 1. 設計讀法

產品介面,不是 landing page。沿用視覺規格 v2(2026-09-18-web-consumer-design.md:單一深色、三種圓角、一個主色),參照「搬家」頁拿捏資訊量:每個狀態只有一個主要動作、說明用白話、細節收進 `<details>`、寫入前一律先讓人看見會發生什麼。

## 2. 版面

```
┌ 我的清單 ───────────────────────────────────────────────────────────────┐
│ 每份清單的正本存在你自己的 Google Drive。正本可以連到 Spotify、Apple      │
│ Music、YouTube Music 上各一份清單;按「同步」時,兩邊互相帶過去。           │
│ ▸ 正本、連結、同步是什麼意思?(details:四則短說明)                          │
├───────────────┬─────────────────────────────────────────────────────────┤
│ 太好聽    607 │ 太好聽 607 首                          (同步這份清單)   │
│ [Spotify][Apple Music] │ 顯示的是這台電腦上次同步後的內容               │
│ 耳妊辰…   222 │ ┌ Google Drive 正本 ┬ Spotify     已連結  全部都有對應  在 Spotify 上聽 │
│ …             │ │                   ├ Apple Music 已連結  3 首還沒有對應  [找對應][逐首決定] │
│ 各平台上的清單 │ │                   └ YouTube…    沒有連結            [在 YT 建一份]  │
│ (按了才讀)     │ [在這份清單裡找歌]  607 首                               │
│               │ ┌ # │ 歌曲(曲名 / 歌手 · 專輯) │ 時長 │ Spotify │ Apple │ YT │ Wiki ┐ │
│               │ │ 固定高度(60vh)的視窗,表頭黏住,裡面捲;頁面不跟著曲數變長 │ │
└───────────────┴─────────────────────────────────────────────────────────┘
```

**寬度規則**(示意圖用 app.css 的真實按鈕尺寸量過,英文最長字「Listen on Spotify」也量過):

| 條件 | 版型 |
|---|---|
| 頁面容器 ≥ 64rem | 左欄 16rem + 右欄並排(1440 寬的畫面) |
| 頁面容器 < 64rem | 疊成一欄;清單清單在上、限高 15rem 自己捲(1280 以下) |
| 歌曲表容器 ≥ 50rem | 一首一行,`table-layout: fixed`:# 3rem、時長 4.5rem、Spotify 12rem、Apple Music 6.25rem、YouTube Music 7.5rem、Wiki 4.75rem(表頭可以換行、不截字),「歌曲」欄吃剩下的(1440 並排時約 220–250px,1280 疊放時約 320px) |
| 歌曲表容器 < 50rem | 一首拆成多行:第一行 # / 歌曲 / 時長,下面是各平台與 Wiki(放不下就換行),每格前面寫出平台名(沒有表頭可以對;1024 寬與手機) |

量測結果:1440 / 1280 / 1024 寬與 390px 的手機框,頁面與歌曲表都沒有橫向捲動,每一格都沒有溢出。

## 3. 設計

### 3.1 歌曲表:固定高度的視窗、少欄位(需求 4)

- **視窗**:沿用現成的 `.tbl-wrap--tall`(`max-height: 60vh` + 黏住的表頭,app.css:220-221)(Q8)。清單清單超過 8 份時出現篩選框(同搬家精靈第二步)。
- **欄位**:`#`(在清單裡的位置)、**歌曲**(曲名一行;第二行「歌手 · 專輯」,單行省略、完整字放 `title`)、**時長**、**每個平台一欄**(§3.2)、**Wiki**。寬度照 §2 的表。
- **CID 不上畫面**:這一頁的表不再是 TSV 的直譯,不用 `renderTable`,改由 playlists.js 自己畫列;每列掛 `data-cid`(給測試與除錯)。CLI、TSV、主控台的表不變(非 TTY 契約不受影響)。要看某首的對應細節,既有的 ISRC 頁照舊。
- **順序**:照正本 items 的順序(決策 38),同一首出現兩次就兩列;清單內篩選只藏列、不重排(Q7)。敗者 cid 沿 `merged` 找勝者(同現在,playlists.js:56-57)。
- **600 首直接畫**,不做虛擬捲動(ponytail:上萬首才需要)。

### 3.2 點播(需求 1)

平台欄只在「這份清單連著那個平台,或至少一首有那個平台的對應」時出現,順序照 `providers.list`;**本機不佔欄**(沒有播放、沒有網頁)。每一格:

| 平台 | 這首的對應 | 格子裡 | 做的事 |
|---|---|---|---|
| Spotify | 22 碼 id | ▶ + 「在 Spotify 上聽」 | ▶ = `args: ['play','--id',id,'--provider','spotify']`(Spotify Connect,放在你開著的 Spotify 上);連結 = 既有 `spotifyLink('track', …)`,新分頁(S7:看得到、不收進選單) |
| Spotify | `spotify:local:…` | 「Spotify 本機檔」(muted,`title` 說原因) | 不給 ▶、不給連結(判斷重用 table.js 的 `SPOTIFY_ID`,改成 export) |
| Apple Music | 全數字(catalog id) | ▶ | `args: ['play','--id',id,'--provider','apple']`;exit 0 但 stdout 不以 ▶ 開頭 = 只在 Music.app 打開,照抄那句給 notice(同 search.js:60-66,決策 52) |
| Apple Music | `i.` / `a.`(只在資料庫、沒有 catalog 對應) | 「只在資料庫」(muted,`title` 說原因) | 不給 ▶(Q3) |
| YouTube Music | videoId | 「開啟」 | `youtubeLink` 加一個選填的 label 參數,這一頁傳新的短字 `webui.playlists.open`(報讀名稱以看得到的字開頭:「開啟「曲名」(YouTube Music)」);搜尋頁不傳,照舊。新分頁到 `music.youtube.com/watch?v=`(YouTube Music 沒有播放遙控,決策 60) |
| 任一 | 沒有對應 | 「—」(muted) | `title`:「這首在 X 還沒有對應」;被釘成「沒有」(pinned + 空 id)說「你標過 X 沒有這首」 |

- ▶ 用 `btn()`(加一個圖示尺寸的 class,min-width 32px):序列槽被佔著(wiki、同步在跑)時擋下並說明(common.js:32-42)。報讀名稱「用 Spotify 播放「曲名」」。
- **▶ 只在這台電腦能用時出現**:那個平台在 `auth status --json` 裡不是 ok(沒登入 / 過期)就只留連結、不給 ▶;Apple 的 ▶ 另外只在 macOS 出現——瀏覽器與 capy 一定在同一台電腦(只綁 127.0.0.1,決策 40),看 `navigator.userAgent` 就夠。不然 Windows 上每一列都會有一顆必定失敗的鈕。
- 播放後底部播放列怎麼跟,照決策 51 / 54,頁面不另外打 `/api/now`。
- Spotify 失敗:沒有作用中的裝置時 CLI 那句已經可行動(「開一個播放器…」),照顯示;旁邊就是「在 Spotify 上聽」可以退回。非 Premium 的 `PREMIUM_REQUIRED` 原文不在這次處理(Q4)。
- 新寫的命令一律用 `args` 陣列,不再用 `line` + `quote()`。

替代方案見 Q1。

### 3.3 歌曲 wiki(需求 5)

- 每列一顆「Wiki」,打開 `<dialog>`(`showModal()`),dialog 放在 `#page-playlists` 裡面——`promptHost` 的 `reveal()` 才不會把人丟去主控台(console.js:466-484)。
- 命令:`args: ['wiki', `--title=${曲名}`, `--artist=${歌手.join(', ')}`]`(`=` 的寫法:web 的擋字檢查對每個參數做 `strings.Cut(a, "=")`,歌名剛好是 `--header` 這類字時分開寫會被 403,#121 review 第 4 點),`label` 用既有的 `webui.wiki.label.ask`(執行狀態列才不會顯示命令原文)。曲名取正本 `tracks.json` 的 `title`,**取不到(這台電腦沒有這首的資料)就不給 Wiki 鈕**——空的 `--title` 會變成問正在播的那首(wiki_run.go:69-80)。歌手用 `", "` 串,跟「正在播」那條路算快取 key 的方式一樣,同一首歌字串一樣時兩邊共用快取。
- 畫法:重用 wiki.js 已經 export 的 `lineSplitter` / `wikiRenderer`;免責行是命令印的最後一行,照畫(決策 59)。
- **停止**:`showModal()` 會讓 dock 的中止鈕變成 inert,所以 dialog 裡有自己的「停止」(`con.stop()`);問 AI 期間關掉 dialog(✕ / Esc)也算停止(Q2)。成功後有「再問一次」(`--refresh`)。
- **設定 AI 端點**:收尾是 exit ≠ 0、而且不是使用者中止時,dialog 收尾區多一顆「設定 AI 端點」= `args: ['wiki','setup']`,`promptHost` 是 dialog 裡的區塊(表單就地出現,同 wiki 頁)。不比對錯誤字串(頁面拿不到那句的 key;wiki 頁的設定鈕也是常駐、不偵測)。
- **鍵盤**:app.js 的單鍵層從 `keysDialog.open` 改成「有任何 `dialog[open]`」,不然 dialog 開著時 1–9 會在背後換頁;web_test.go:1351 的斷言跟著改成釘新條件。
- **政策**:新入口送出的歌名、歌手來自你 Drive 上的正本(經本機 state.db)。送的欄位沒變(仍是 `ai.SentFields` 的子集),現行政策也已經把正本的歌名、歌手當成「音樂本身的資料」(privacy.html:64、68);但 §5 與 README 把 wiki 描述成「正在播的歌」,要補一句來源(Q5)。

### 3.4 正本與平台清單的關係(需求 2)

用三層把關係講清楚,越往下越具體:

1. **lead 改寫**成一句完整的模型:正本在你的 Drive、每個平台最多連一份、同步時互相帶過去。
2. **`<details>`「正本、連結、同步是什麼意思?」**:四則短說明(正本 / 連結 / 同步 / 搬家和同步不一樣),預設收起,第一眼只有 lead。
3. **選中清單的「連結面板」**(§3.5):「Google Drive 正本」節點 + 一個平台一列,把「這份正本連到哪幾份平台清單」畫成看得到的東西。

另外:

- 左欄晶片改用 `providerName`(Spotify / Apple Music / YouTube Music / 本機),不再顯示原始 id + ✓。
- **空白態分三種**:還沒連 Google Drive(`auth status --json` 的 Google 狀態)→ 連到 `#/account`;連了、但這台電腦還沒有本機資料(`export` 失敗,例如第二台電腦——export 沒辦法知道 Drive 上有沒有清單)→ 連到 `#/sync`,說明取消勾選「只看變更」、按「從平台更新」把正本讀進來(同 CLI 的 `escape.err.nothing_to_export` 叫人先 `pl pull`);讀過了、真的沒有正本 → 「用『搬家』搬一份過來」(T2 再加「或從下面『各平台上的清單』納入一份」)。取代現在那句叫人到「同步」連結的錯誤指引(zh-TW.json:1305)。
- **「平台上現有的清單」改成「各平台上的清單」**:一顆「讀取各平台的清單」,按了才依序對**這台電腦有登入的**每個平台跑 `pl list --provider X`(會連網、每家各佔一次序列槽,在 `onExit` 裡接下一家);每一列標出「連著正本「X」」或「還沒納入」,還沒納入的可以「納入」(§3.5,Q6)。Spotify 的列都放「在 Spotify 上聽」(`spotifyLink('playlist', …)`,是按鈕的兄弟元素、不包進按鈕;S7 原本涵蓋 `pl list` 的表,webui_console.mjs:759)。

### 3.5 就地同步(需求 3)

**T2 的前置:兩個 CLI 改動(Q9)**(頁面讀的是可能過時的 export,會重排正本或換掉連結的護欄只能放在 CLI;不在頁面判斷、不比對錯誤字串):

1. **`pl link --new-only`**(兩份語系補說明與 README ×2 的命令表;原暫名 `--strict` 跟 push / sync 內部既有的 strict 撞名,#121 review 第 2 點):只做「建新的」——第一個參數**以名稱**命中既有正本(不分大小寫)時不連、回錯、零寫入(以 pid 指定照常);正本在那個平台已經有連結時,**不管是不是別台電腦 / 別的帳號的**都不接管、回錯、零寫入。頁面發出的每個 `pl link` 一律帶它。回歸測試:Drive 上有同名(含只差大小寫)的正本而本機 state.db 沒有、以及 Drive 上有別的 YouTube 帳號的連結而本機沒有,帶旗標都要 exit 1 且 Drive 檔位元組不變、沒有呼叫 `CreatePlaylist`(改之前會 fail:現在會連上 / 接管)。
2. **`link.err.same_name_one` / `same_name_many` 改說法**:同名的平台清單是空的(例如上次 `--create` 建好但連結沒寫進 Drive,即 `link.recovery` 那種)才建議 `capy pl link … {platform}:{id}`;不是空的(或曲數不明)改說「先在 {platform} 上把它改名再重跑;要把它的歌加進正本,用搬家」。現有程式拿不到曲數:`sameNamePlaylists`(migrate.go:518)只回 id,`readable()`(pull.go:462-471)讀了 items 就丟掉——要讓它們把曲數一起帶回來(搬家也用 `sameNamePlaylists`,改簽名時搬家的呼叫端跟著改、行為不變);`readable()` 遇到 404 當成讀得到但沒有 items,歸到「曲數不明」那一邊(不建議 `pl link`)。CLI 與網頁一次修好;i18n 單元測試釘住兩種說法。

**連結面板**,每個平台一列(本機只在連著時出現):

| 狀態 | 顯示 | 動作 |
|---|---|---|
| 連著、這台電腦有登入、是這台 / 這個帳號的 | 已連結;有幾首還沒有這個平台的對應(從 export 的 mappings 數,零網路;判斷規則同 §3.2) | 平台上的清單連結(Spotify:`spotifyLink('playlist')`;YouTube:`music.youtube.com/playlist?list=<連結 id 斜線後那段>`,連結 id 是 `<channel_id>/<playlistId>`;Apple 的資料庫清單沒有公開網址,不給)。有缺對應時兩顆:「找對應」= `args: ['resolve',pid,'--provider',p]`(只寫高信心的自動對應);「逐首決定」= `args: ['resolve',pid,'--provider',p,'--review']`(走既有的 web 逐首裁決橋,web_prompt.go:259、353:接受、略過、手動搜尋,或「這個平台沒有這首」= 之後不再算缺) |
| 連著、但這台電腦沒登入那個平台 | 「這台電腦沒有登入 X」;YouTube 的 `channel_id` 是空的也算這一格,不算別的帳號 | 連到 `#/account`;平台上的清單連結照給 |
| 連著、屬於別台電腦(local)或別的 YouTube 帳號 | muted:「連在另一台電腦「名稱」」/「連在另一個 YouTube Music 帳號」 | 無(同 pull / push 的跳過規則,決策 33、60)。判斷用 `auth status --json` 的 `google.device_id` / `youtube.channel_id`(不連網,進頁時用 `quiet` 跑一次)配 manifest.devices 的名稱 |
| 沒連,Spotify / Apple Music / YouTube Music,這台有登入 | 沒有連結;已經知道幾首在那個平台的對應(mappings 全域共用) | 「在 X 建一份」→ 見下 |
| 沒連,這台沒登入 | 沒有連結 · 這台電腦沒有登入 X | 連到 `#/account` |
| 沒連,本機 | 不顯示這一列 | 本機不能 `--create`;連既有的 M3U 會碰到順序問題 |

**「在 X 建一份」**(示意圖狀態三):

1. 按「開始」前,頁面先說清楚:「按『開始』會先在 X 建一份叫「太好聽」的空清單並連上;接著替每一首找 X 上的對應,再照正本的順序把找得到的加進去——找對應與加歌之前,capy 會先列出來給你確認。X 上如果已經有同名的清單,capy 會停下來、不建。」X 是 Apple Music 時再加一句決策 49 的揭露:「加進清單的歌可能也會進你的 Apple Music 資料庫(看你的 Apple Music 設定)。」(不寫成必然;#121 review 第 5 點)。這是頁面自己的說明,「開始」就是對第一步的同意,不是替 CLI 回答確認(第一步本來就沒有 CLI 確認)。「各平台上的清單」已經讀過、而且那個平台有同名(不分大小寫)清單時,直接不給「開始」,改說同一句同名說明。
2. 依序跑三個命令,**每一步在上一步的 `onExit` 裡送出**;步驟條(搬家精靈同款)顯示進度:
   - `args: ['pl','link','--new-only',pid,p,'--create']`
   - `args: ['resolve',pid,'--provider',p]`
   - `args: ['pl','sync',pid,'--provider',p]`
3. 每一步帶 `promptHost`(連結面板裡的區塊),**`onTable` 把變更表畫在同一個區塊、確認提示之上**(表在上、提示在下;確認句寫的是「以上 N 筆」)。頁面絕不代加 `--yes` / `--force`、不替人按確認(決策 46)。
4. 停下時照實說停在哪(照 §3.5 的收尾規則):第一步沒完成 → 「第一步沒有完成,原因見 capy 的說明」(CLI 的同名說法已經在前置 2 改好);停在第二步 → 「已建立並連上;按『找對應』接著做」;停在第三步 → 「對應已寫入;按『同步這份清單』把歌加進去」。停下後連結面板重讀(那一列會變成「已連結、N 首還沒有對應」,動作接得上)。**第一步會留下一份連著的空清單**:capy 不刪 YouTube / Apple 的清單,之後要不要留由你決定。

**「同步這份清單」**(右欄標題旁的主要按鈕):`args: ['pl','sync',pid]`,`promptHost` 與 `onTable` 同上;變更表的畫法把 sync.js 在 `initSync` 裡的 `table()` 搬到 table.js 共用(同步頁行為不變)。沒有任何連結時按鈕停用,旁邊說「先連一個平台」。

**寫入命令的收尾規則**(sync、resolve、pull 共用;照 move.js:202、448-457 的做法,用 `onPromptClosed` 記下提示怎麼收的):

| 收尾 | 頁面說 |
|---|---|
| exit 0,沒有變更 | 已經是最新的 |
| exit 0,有寫入 | 完成 + 重讀 |
| exit 2 | 你按了取消,沒有寫入 |
| exit 1,提示被關掉 / 逾時 | 你關掉了提示 / 等太久沒回答,沒有寫入(不顯示英文的 `user aborted`) |
| exit 3 | 照印 CLI 原文 +「capy 先停下來了,這一頁不會替你越過;照上面的說明到主控台處理」(不暗示一定是 `--force`:exit 3 的原文各自帶了出路) |
| 使用者按了中止 | 已中止 |
| 其他 exit ≠ 0 | CLI 原文(去掉開頭的 `Error: `)+ 連到主控台 |

**「納入」**(各平台上的清單,Q6):`args: ['pl','link','--new-only','--',<平台清單名>,'<平台>:<id>']` → 在它的 `onExit` 裡 `args: ['pl','pull','--',<平台清單名>]`(`--` 讓以 `-` 開頭的清單名不被當成旗標;`--new-only` 保證建的是新的空正本,第一次 pull 採平台順序才是對的;變更表 + 確認就地出現)。頁面自己看到同名正本(不分大小寫,同 move.js:194)時不給「納入」,改說「有同名的正本,要把它的歌加進來請用『搬家』」並連到 `#/move`——這只是提示,真正的護欄是 `--new-only`。清單名剛好是 web 擋下的旗標字(`--yes`、`--force`、`--header`、`--api-key`、`--headers-file` 等;web_run.go:166-173 對**每一個**參數做 `strings.Cut(a, "=")` 比對,`--` 後面也算)時,那一列照實說「這個名稱要到主控台處理」。**停在第二步**(`pl link` 成功、`pl pull` 被取消 / 關掉 / 逾時)會留下一份連著平台清單的空正本;再按「納入」會被 `--new-only` 擋下(撞到剛建的同名正本),所以收尾照實說「已建立並連上一份空的正本「名稱」;按它的『同步這份清單』把歌拉進來」,重讀後那一列變成「連著正本「名稱」」。node 情境 11 釘住這個收尾。

**不引導的路**:把平台上既有、非空的清單連到已有曲目的正本(會採平台順序,derive.go:114-129,決策 38)。要把一份平台清單的歌併進正本,走搬家(加進既有清單 = 只加在尾端)。

### 3.6 資料新鮮度

- 標題下一行固定寫「顯示的是這台電腦上次同步後的內容」(export 的本質,不假裝是即時)。
- 這一頁的任何寫入命令收尾後,用 `quiet` 重跑 `export`(不灌進主控台),選中的清單不變。
- 從別頁回到這一頁時也重讀,用 `con.idle` 排隊(Q7)。

### 3.7 硬約束對照

| 約束 | 這次怎麼守 |
|---|---|
| 決策 38 順序 | 表格照 items 順序、不排序不拖曳;篩選只藏列;頁面發出的 `pl link` 一律 `--new-only`(不連到既有正本);CLI 不再建議連非空的同名清單 |
| 刪除前 dry-run 與閾值 | 所有寫入走 `pl sync` / `resolve` / `pl pull` 自己的變更表與確認;頁面不碰 `--force` |
| 決策 33 / 60 接管 | 頁面發出的 `pl link` 不接管別台 / 別帳號的連結(`--new-only`);接管仍只在主控台明確執行 |
| 決策 40–41、46 web | 全部走 `Console.run` 的序列槽;不加直達端點;`promptHost` 與變更表在本頁;不代加 `--yes` / `--force`;名稱放 `--` 之後 |
| 決策 48 | 平台只用文字名稱 |
| 決策 49 / 60 寫入邊界 | 寫入仍由 CLI 決定:Apple 只寫可編輯的自建清單、YouTube 只寫自建清單;頁面只發命令 |
| 決策 50 i18n | 新字串 `webui.playlists.*` 兩份語系同補,英文 label 用進行式,webui 無中文字面;拿掉的 key 兩份一起刪;CLI 新旗標與改過的 `link.err.same_name_*` 兩份同改 |
| 決策 52 | Apple ▶ 照實說「只打開」 |
| 決策 59 wiki | 送的欄位不變;免責行照畫;AI 端點仍 BYO;政策與 README 同 PR 補來源(Q5) |
| S7 | 每列 Spotify 對應、以及各平台上的清單裡每一份 Spotify 清單,都有看得到的「在 Spotify 上聽」 |
| 隱私權政策 | 外部服務與存進 Drive 的內容都沒變;只有 wiki 的資料來源描述要補(Q5) |
| README ×2 | `pl link --new-only` 進兩份命令表 |

## 4. 測試(每一則都是改之前會 fail 的)

**一定要改的既有測試**(不是「可能要改」):

- node 情境 8k / 8n / 8o(testdata/webui_console.mjs:584-896):children 索引、`cells[0]` 是 CID、晶片字面 `apple ✓`、每列 `[6,6,6,6,6]` 的寬度、「看 Spotify 上的內容」按鈕都會變。改成新結構的等價斷言,不放寬到「會過就好」;:759 的 Spotify 清單連結斷言搬到「各平台上的清單」。
- `TestWebStaticFrontendContracts` 的字串比對(清單頁的部分;web_test.go:1351 的 `keysDialog.open` 改釘 `dialog[open]`)。
- i18n:`TestNoUnusedKeys` 會抓到被拿掉的 `webui.playlists.list` / `list_label` / `view_on` / `view_label` / `platform_heading` 等,兩份一起刪。

**新增的 Go 測試**:

- `pl link --new-only`:以名稱命中 Drive 上既有正本(本機 state.db 沒有、只差大小寫也算)→ exit 1、零寫入;正本在 YouTube 已有別帳號的連結 → exit 1、零寫入、沒有呼叫 `CreatePlaylist`;以 pid 指定、平台沒連 → 照常建立。
- `link.err.same_name_*`:同名清單是空的 → 建議連它;非空 → 不建議 `pl link`、改說改名或搬家(中英)。

**新增的 node 情境**(中英各跑一輪):

1. 列結構:沒有 CID 文字、`data-cid` 在;列序等於 items 順序(同一首兩次 = 兩列);墓碑顯示勝者的曲名。
2. 平台欄出現規則:連著或有對應才出現;本機永遠不是一欄。
3. Spotify 格:22 碼 → ▶ 送出 `['play','--id',id,'--provider','spotify']`、「在 Spotify 上聽」看得到且 `href` 對;`spotify:local:…` → 沒有 ▶、沒有連結、有說明(fixture 用既有的 c3)。
4. Apple 格:數字 id 給 ▶ 並送出正確 args;`i.` / `a.` 不給 ▶;exit 0 且不以 ▶ 開頭時 notice 照抄那句;平台沒登入或非 macOS 時不給 ▶。
5. YouTube 格:「開啟」的字是新 key、`href` 與新分頁對;搜尋頁的字不變。
6. Wiki:送出 `['wiki','--title=<t>','--artist=A, B']` 且帶 label;歌名是 `--header` 時照樣送得出去;沒有曲名的列沒有 Wiki 鈕、不送命令;dialog 在 `#page-playlists` 裡;「停止」與執行中關掉 dialog 都呼叫 `stop()`;失敗收尾有「設定 AI 端點」、送 `['wiki','setup']`、`promptHost` 在 dialog 裡;使用者中止時沒有這顆。
7. 鍵盤:非鍵位表的 dialog 開著時,數字鍵不換頁。
8. 在 X 建一份:按「開始」前零命令;之後依序三個命令、第一個帶 `--new-only`、都帶 pid、`promptHost` 在本頁;收到 table 事件時表格出現在 `#page-playlists`、DOM 順序在提示之前;第一步失敗就不送後兩步;已讀到同名平台清單時不給「開始」;**用真的 Console**:第二步進行中先排一個 `con.idle`,第三步照樣送得出去(參照 webui_console.mjs:254-264);整頁原始碼沒有 `--yes` / `--force`。
9. 同步這份清單:送出 `['pl','sync',pid]`;沒有連結時停用並說原因;收尾規則表的每一列(含「關掉提示 → 沒有寫入、畫面上沒有 `user aborted`」、逾時、exit 3 的白話不提 `--force`)。
10. 找對應 / 逐首決定:送出的 args;表在提示之前。
11. 各平台上的清單:只讀有登入的平台;連著的列標出正本名稱;Spotify 列有清單連結;有同名正本(含只差大小寫)時沒有「納入」;納入依序送 `pl link --new-only -- <名稱> …`、`pl pull -- <名稱>`,名稱以 `-` 開頭時 `--` 在。
12. 空白態:沒連 Google Drive / 連了沒有正本,兩句不同、各自連到對的頁。
13. 寫入收尾後的重讀用 `quiet`(主控台不多一個區塊);連結面板的「沒登入」「別台電腦」「別的帳號」三種狀態各一則(YouTube `channel_id` 空 = 沒登入)。
14. 英文那一輪:頁面與 label 沒有 CJK,label 符合 `/^[A-Z][a-z]*ing /`。

**Go 靜態**:playlists.js 不含 `--yes` / `--force`;清單頁用 `.tbl-wrap--tall`;app.js 的單鍵層看 `dialog[open]`。

**網站**(Q5 選 A 時):`site/site_test.go` 釘住政策新句子的關鍵詞(中英)。

node 替身沒有排版,寬度與捲動留給 §8 人工驗收。

## 5. PR 切法

- **T0**:本文(docs only)。
- **T1 歌曲表 + 點播**:固定高度的視窗、欄位與寬度規則、▶ / 連結 / 「只在資料庫」/「Spotify 本機檔」、左欄晶片與篩選、lead 與 `<details>`、空白態、清單內篩選(Q7)、`youtubeLink` 的 label 參數。需求 1、4,需求 2 的說明部分。
- **T2 CLI 前置 + 連結面板 + 同步**:`pl link --new-only` 與 `link.err.same_name_*` 改說法(Go、i18n、README ×2)→ 連結面板、「同步這份清單」、「在 X 建一份」、「找對應」/「逐首決定」、收尾規則、各平台上的清單與「納入」、寫入後與回到頁面時重讀。需求 2、3。
- **T3 wiki dialog**:dialog、停止、設定入口、鍵盤單鍵層、政策 ×2 / README ×2(Q5)。需求 5。
- **T4 文件收尾**:指南 ×2(`docs/guide.html`、`docs/guide.en.html`)補「我的清單」一段 → `go test ./site/ -run TestGuideOnSiteIsCurrent -update` → 重發兩個指南 Artifact(英文那份的網址不在 repo,要向維護者拿)→ 合併後去看線上 `/guide`、`/en/guide`;附錄 C 決策 61;§9 的文件不一致。

每個 PR 都跑 `go test ./...`、`go mod tidy -diff`,node 情境中英兩輪。

## 6. Q(2026-09-30 全部照推薦定案,推薦選項放在前面)

| Q | 問題 | 選項 |
|---|---|---|
| Q1 | 每一列的點播怎麼長 | **A(推薦)一個平台一欄**:Spotify = ▶ + 在 Spotify 上聽;Apple = ▶;YouTube = 開啟(示意圖)。一眼看得出每首在哪些平台有,一鍵就放;代價是 1440 寬並排時「歌曲」欄約 220–250px,長曲名會省略。/ B 一顆 ▶ + 表格上方選「用哪個平台播」,右邊只留連結:歌曲欄寬一些,但要先選平台。/ C 只給連結、不用 capy 遙控:不佔序列槽、不需要 Premium,但 Spotify / Apple 要到了平台還得自己按播放 |
| Q2 | Wiki 開在哪、關掉時怎麼辦 | **A(推薦)就地 dialog,問 AI 期間關掉 = 停止**(把序列槽還給播放)。/ B dialog,關掉時讓它在背景跑完寫進快取:期間這頁的 ▶ 都會被擋(點了會說正在問 wiki、可按中止),只有底部的執行狀態列看得到它在跑,答案要重開 dialog 才看得到。/ C 不開 dialog,跳到 wiki 頁並帶入歌名(離開清單;路由要能帶參數給 wiki 頁) |
| Q3 | Apple 只在資料庫、沒有 catalog 對應的歌(`i.` / `a.`) | **A(推薦)不給 ▶,標「只在資料庫」並說原因**。/ B 加一條「用資料庫列 id 找 persistent ID 再播」的新 Apple 播放路徑(新行為,要先探測,另開計畫) |
| Q4 | Spotify 非 Premium 的 `PREMIUM_REQUIRED` 原文 | **A(推薦)這次不處理,另開小 PR 補在地化訊息**(影響 CLI 與 web 所有播放入口,不只這頁)。/ B 併進 T1 |
| Q5 | wiki 的新入口與隱私權政策 | **A(推薦)T3 同一個 PR 補政策 §5(中英)與 README ×2 的一句**:歌名與歌手也可能來自你存在 Google Drive 的清單正本,只有你按下時才送、只送到你自己設定的端點;site_test 釘住新句子。現行政策對正本歌名的定性(音樂本身的資料)不變。/ B 不改政策(字面沒限定來源,測試不會紅),只改 README |
| Q6 | 「各平台上的清單」要做到哪 | **A(推薦)讀取 + 標出關係 + 可「納入」**(帶 `--new-only`,有同名正本時不給)。/ B 只讀取與標出關係,不給動作(納入請到主控台)。/ C 拿掉這一段 |
| Q7 | 回到這一頁時自動重讀、清單內篩選 | **A(推薦)兩個都做**:回到頁面就用 `quiet` 重跑 export(別頁寫入後這頁不會過時);超過 20 首時出現「在這份清單裡找歌」。/ B 都不做,只留「重新整理」鈕 |
| Q8 | 固定視窗的高度 | **A(推薦)沿用 `.tbl-wrap--tall` 的 60vh**(同步頁、搬家預覽同一個)。/ B 另訂(例如 `clamp(22rem, 62vh, 44rem)`) |
| Q9 | 要不要動 CLI(§3.5 的 T2 前置,審查後加進來的) | **A(推薦)一個 `pl link --new-only` 同時管「以名稱不沿用既有正本」與「不接管別台 / 別帳號的連結」,加上 `link.err.same_name_*` 改說法**。頁面讀的 export 可能過時,這兩道護欄只能放在 CLI。/ B 同樣的兩道護欄拆成兩個旗標。/ C 不動 CLI:v1 拿掉「在 X 建一份」與「納入」,只留「同步這份清單」「找對應」「逐首決定」(選 C 時 Q6 自動變成 B) |

## 7. 明確不做(要就另開)

- 排序、拖曳、在頁面上直接改正本(沒有這種命令;決策 38)。
- 「最後同步時間」與進頁時自動算每份清單要不要同步:前者要改 Drive 形狀(跳 schema 版 + 政策),後者每份清單都要 `pl sync --dry-run`(連網、拿 pull.lock、佔序列槽)。
- 用 dev__ base 顯示平台那一側的名稱與曲數。
- 點一首接著播完整份清單(`PlayRequest` 沒有 offset;Apple 不支援清單播放)。
- Apple 的 https 歌曲頁連結(要先讀 storefront;▶ 已經涵蓋「在 Music.app 打開」)。
- 連結前警告寫不了的清單(`pl list` 的 TSV 沒有 Unwritable 欄;追蹤的別人清單納入後只會單向拉)。
- 連到平台上既有、非空的清單(順序會被改;請用搬家)。
- 在頁面上取消連結(`pl unlink` 在主控台可用)。
- 新的直達端點(例如唯讀的 `/api/canon`)。
- `capy wiki --album`。
- 搜尋頁對本機也放「播放」鈕、按了必定失敗的既有瑕疵(另開)。
- 虛擬捲動。

## 8. 驗收清單(R-47–R-55;T1–T4 都進 main 後由維護者跑)

- **R-47** 1440 / 1280 / 1024 / 400px,中英兩種語言:頁面與歌曲表都沒有橫向捲動;607 首的清單頁面高度不跟著曲數變長;窄版每個平台格前面看得到平台名。
- **R-48** Spotify ▶(Premium、有開著的 Spotify):開始播、底部播放列跟過去;把 Spotify 關掉再按:訊息看得懂,「在 Spotify 上聽」可以退回。
- **R-49** Apple ▶(macOS):資料庫裡有的歌真的播;只在目錄裡的歌在 Music.app 打開並標出來,頁面照實說;「只在資料庫」的列沒有 ▶。
- **R-50** YouTube「開啟」:新分頁到 music.youtube.com 的那一首並開始播。
- **R-51** Wiki dialog:設定好的端點邊到邊出現;「停止」與關掉都會停、播放鈕恢復;同一首第二次開是快取、立刻出現;沒設定時「設定 AI 端點」表單就地出現。
- **R-52** 「在 YouTube Music 建一份」(拋棄式正本,真帳號寫入要維護者授權):按「開始」後第一步立刻建一份空清單並連上(這一步沒有確認);找對應與加歌兩步,變更表與確認就地出現、表在提示之上;在這兩步取消時該步零寫入,但已建立的空清單與連結會留著,頁面說明停在哪、下一步按哪顆;全部完成後清單順序與正本一致。capy 不刪 YouTube 清單,驗收留下的拋棄式清單要自己到 YouTube Music 刪。
- **R-53** 「同步這份清單」:有變更時就地出現變更表與確認,取消、按 ✕、等到逾時都是零寫入且說法各自正確;沒有變更時說「已經是最新的」。
- **R-54** 各平台上的清單:只讀有登入的平台;連著的標出正本名稱;「納入」建出新正本並照平台順序拉進來;有同名正本時沒有「納入」。
- **R-55** 語言切到 English 整頁走一遍:沒有中文、字不被截斷、按鈕不換行。

## 9. 順手修正的文件不一致(T4)

- ARCHITECTURE.md:963-964、1056,計畫 2026-09-18-web-consumer.md:20,web_test.go:1109 的註解仍寫 Apple 不能當目的地(決策 49 之後可以)。
- 不在這次範圍、另開:README 的 push 前提一寫「這台裝置 pull 過」,程式檢查的是所有裝置合併後的 base(push.go:187-190);project.go:29-30 說 Apple 的 `i.` id 推不出去,但 `Pushable` 收它(apple.go:93-95)。
