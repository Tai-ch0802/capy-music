# web「我的清單」改版:點播、正本與平台清單的關係、就地同步、歌曲 wiki(2026-09-30 計畫)

**狀態:T0(本文),等使用者確認 §6 的 Q 才實作。** 這一版只有文件,沒有改任何程式。
版面示意圖(三個狀態):https://claude.ai/artifact/NzK4uk8ARBiTWEHzLfHfpc
Q 用計畫內編號 Q1…(同 2026-09-29-youtube-music.md 的寫法,不接全域的 Q82)。定案後寫成附錄 C 決策 61。

## 0. 需求與現況(2026-09-30 讀碼查證)

使用者的五點,對到現在的程式:

| # | 需求 | 現況 | 依據 |
|---|---|---|---|
| 1 | 從清單直接點播,Apple Music / Spotify / YouTube 都要能轉導播放 | 清單頁只有「在 Spotify 上聽」連結(S7),沒有播放鈕;搜尋頁才有 `play --id` | playlists.js:83-90;search.js:44-74 |
| 2 | Drive 正本與各平台清單的關聯看不懂 | lead 一句帶過;左欄晶片是原始 id(`apple ✓`);下方「平台上現有的清單」是另一張 `pl list` 表,跟正本沒有對照 | playlists.js:12-28、71;zh-TW.json:1309、1314 |
| 3 | 無法在這頁引導跨平台同步 | 這頁沒有任何寫入動作;空白態叫人「到『同步』把平台上的清單連起來」,但同步頁沒有 link | zh-TW.json:1305;sync.js:26-31 |
| 4 | 曲目多時整頁被拉長;欄位多到要左右捲 | 表格用沒有高度上限的 `.tbl-wrap`;CID 欄 `nowrap`(`p:local:` 路徑很長)+ 連結欄 `nowrap` | playlists.js:83、116-121;app.css:205、226-228 |
| 5 | 每首歌加 wiki 按鈕,開 dialog 或到 wiki 頁 | 沒有;wiki 頁只能查「正在播」或手打歌名 | wiki.js:116-153 |

會影響設計的既有事實(每條都附出處,細節在讀碼紀錄):

- **資料只有本機的 `capy export`**:它是這台電腦上次成功 COMMIT 時的 Drive 快照,別台之後的改動看不到;沒有「最後同步時間」這種欄位,也沒有 `pl status`(escape.go:26-27;pull.go:159-191、383-392;pl.go:19)。
- **mapping 全域共用**:`tracks.json` 每首歌一組 `mappings`,不分清單;Spotify 存 22 碼 track id,Apple 有 catalog 對應時存數字 id、沒有時存資料庫列 id(`i.` / 協作清單 `a.`),YouTube 存 videoId,local 存相對路徑(canon.go:133-162;apple/client.go:182-191)。`resolve` 只替「清單已連結的平台」找 mapping(resolve/resolve.go:274-310)。
- **點播只有 `capy play --id <id> --provider <p>`**:Spotify 走 Connect(沒有作用中的裝置 = 404 → 可行動的訊息、exit 1;要 Premium 但程式偵測不到);Apple 在 macOS 上資料庫裡找到唯一一首才真的播,否則 `open music://…` 在 Music.app 打開並標出來、照實說、exit 0 不印 ▶(決策 52);local 與 YouTube 沒有播放(spotify/client.go:350-364;apple/player_darwin.go:102-142、157-227;player.go:137-156)。Apple 的 `i.` / `a.` id 餵 `--id` 會打 catalog 端點失敗。
- **Spotify 連回(計畫 2026-09-24 §1.7 S7)**:有 Spotify 對應的每一列都要有看得到的「在 Spotify 上聽」(規範核可的字),只連 22 碼 id(table.js:43-60)。
- **連結是一對一**:一份正本在每個平台最多連一份,一份平台清單也只連一份正本(canon.go:212;pull.go:562-567)。`pl link` 沒有 `--dry-run` 也沒有確認,不檢查 Unwritable(pull.go:473-626、460-471)。
- **把「既有、非空」的平台清單連到「已有曲目」的正本,第一次 pull 會採平台的順序**:沒有 base 時規則 6′ 不清 `moved`(derive.go:114-129),跟決策 38 衝。從正本新增一個平台、不會重排的路是 `pl link <pid> <p> --create` → `resolve <pid> --provider <p>` → `pl sync <pid> --provider <p>`(pull.go:585-606;resolve.go:574 寫入前 `confirmWrite`;sync 的 pull 半邊把剛建的空清單記成 base、push 半邊照正本順序推)。
- **wiki**:沒有 `--cid`;只能 `--title` + `--artist`(單一字串),快取 key 用 `", "` 串起歌手(wiki_run.go:71-74、132),命中不打 AI;沒設定端點 = 第一步 exit 1(wiki_run.go:56-59)。問 AI 期間佔著序列槽 30–60 秒(計畫 2026-09-28 Q71)。
- **web 的規矩**:所有命令走同一個序列槽(`Console.run`);`promptHost` 讓確認提示畫在頁面裡,沒給就跳到主控台(console.js:145、466-484);`quiet` 不進主控台但 `onStdout` / `onExit` 照常(console.js:161-164、202、339);頁面絕不代加 `--yes` / `--force`(決策 46);webui 不可有中文字面、新 key 兩份語系同補(決策 50);CSP 零 inline。
- **鍵盤單鍵層只擋鍵位表那一個 dialog**:別的 dialog 開著時按 1–9 會在背後換頁(app.js:165-175)。
- **node 行為測試綁著現在的清單頁結構**:情境 8k / 8n / 8o 用 children 索引、`cells[0]` 是 CID、晶片字面 `apple ✓`(testdata/webui_console.mjs:584-896)。

## 1. 設計讀法

產品介面,不是 landing page。沿用視覺規格 v2(2026-09-18-web-consumer-design.md:單一深色、三種圓角、一個主色),參照「搬家」頁拿捏資訊量:每個狀態只有一個主要動作、說明用白話、細節收進 `<details>`、寫入前一律先讓人看見會發生什麼。

## 2. 版面(示意圖狀態一)

```
┌ 我的清單 ───────────────────────────────────────────────────────────────┐
│ 每份清單的正本存在你自己的 Google Drive。正本可以連到 Spotify、Apple      │
│ Music、YouTube Music 上各一份清單;按「同步」時,兩邊互相帶過去。           │
│ ▸ 正本、連結、同步是什麼意思?(details:四則短說明)                          │
├───────────────┬─────────────────────────────────────────────────────────┤
│ 太好聽    607 │ 太好聽 607 首                          (同步這份清單)   │
│ [Spotify][Apple Music] │ 顯示的是這台電腦上次同步後的內容               │
│ 耳妊辰…   222 │ ┌ Google Drive 正本 ┬ Spotify     已連結  全部都有對應  在 Spotify 上聽 │
│ …             │ │                   ├ Apple Music 已連結  3 首還沒有對應  [找對應]     │
│ (清單多時可篩選)│ │                   └ YouTube…    沒有連結            [在 YT 建一份]  │
│               │ [在這份清單裡找歌]  607 首                               │
│ 各平台上的清單 │ ┌ # │ 歌曲(曲名 / 歌手 · 專輯) │ 時長 │ Spotify │ Apple │ YT │ Wiki ┐ │
│ (按了才讀)     │ │ 固定高度的視窗,表頭黏住,裡面捲;頁面不跟著曲數變長       │ │
└───────────────┴─────────────────────────────────────────────────────────┘
```

窄版(主區 < 64rem):兩欄疊成一欄、連結面板的正本節點移到上面;歌曲表容器 < 44rem 時每列變兩行(第一行 # / 歌曲 / 時長,第二行各平台與 Wiki),400px 寬不橫向捲動。

## 3. 設計

### 3.1 歌曲表:固定高度的視窗、少欄位(需求 4)

- **視窗**:沿用現成的 `.tbl-wrap--tall`(`max-height: 60vh` + 黏住的表頭,app.css:220-221),不另訂樣式(Q8)。左欄清單清單同高、自己捲;超過 8 份時出現篩選框(同搬家精靈第二步)。
- **欄位**:`#`(在清單裡的位置)、**歌曲**(曲名一行;第二行「歌手 · 專輯」,單行省略、完整字放 `title`)、**時長**、**每個平台一欄**(§3.2)、**Wiki**。`table-layout: fixed`,只有「歌曲」欄吃剩下的寬度。示意圖在 1440 與 1024 寬量過:表格寬度等於視窗寬度,沒有橫向捲動。
- **CID 不上畫面**:這一頁的表不再是 TSV 的直譯,不用 `renderTable`,改由 playlists.js 自己畫列;每列掛 `data-cid`(給測試與除錯)。CLI、TSV、主控台的表不變(非 TTY 契約不受影響)。要看某首的對應細節,既有的 ISRC 頁照舊。
- **順序**:照正本 items 的順序(決策 38),同一首出現兩次就兩列;篩選只藏列、不重排(Q7)。敗者 cid 沿 `merged` 找勝者(同現在,playlists.js:56-57)。
- **600 首直接畫**,不做虛擬捲動(ponytail:上萬首才需要)。

### 3.2 點播(需求 1)

平台欄只在「這份清單連著那個平台,或至少一首有那個平台的對應」時出現,順序照 `providers.list`;**本機不佔欄**(沒有播放、沒有網頁)。每一格:

| 平台 | 這首的對應 | 格子裡 | 做的事 |
|---|---|---|---|
| Spotify | 22 碼 id | ▶ + 「在 Spotify 上聽」 | ▶ = `args: ['play','--id',id,'--provider','spotify']`(Spotify Connect,放在你開著的 Spotify 上);連結 = 既有 `spotifyLink('track', …)`,新分頁(S7:看得到、不收進選單) |
| Apple Music | 全數字(catalog id) | ▶ | `args: ['play','--id',id,'--provider','apple']`;exit 0 但 stdout 不以 ▶ 開頭 = 只在 Music.app 打開,照抄那句給 notice(同 search.js:60-66,決策 52) |
| Apple Music | `i.` / `a.`(只在資料庫、沒有 catalog 對應) | 「只在資料庫」(muted,`title` 說原因) | 不給 ▶(Q3) |
| YouTube Music | videoId | 「開啟」 | 既有 `youtubeLink`,新分頁到 `music.youtube.com/watch?v=`(YouTube Music 沒有播放遙控,決策 60) |
| 任一 | 沒有對應 | 「—」(muted) | `title`:「這首在 X 還沒有對應」;被釘成「沒有」(pinned + 空 id)說「你標過 X 沒有這首」 |

- ▶ 用 `btn()`:序列槽被佔著(wiki、同步在跑)時擋下並說明(common.js:32-42)。▶ 的報讀名稱「用 Spotify 播放「曲名」」。
- 播放後底部播放列怎麼跟,照決策 51 / 54,頁面不另外打 `/api/now`。
- Spotify 失敗:沒有作用中的裝置時 CLI 那句已經可行動(「開一個播放器…」),照顯示;旁邊就是「在 Spotify 上聽」可以退回。非 Premium 的 `PREMIUM_REQUIRED` 原文不在這次處理(Q4)。
- Apple 在非 macOS:CLI 自己回「只在 macOS 可用」,頁面照顯示(頁面拿不到 OS)。
- 新寫的命令一律用 `args` 陣列,不再用 `line` + `quote()`(把決策 46 的做法用在這頁的新程式;既有的搜尋頁不在這次改)。

替代方案見 Q1。

### 3.3 歌曲 wiki(需求 5)

- 每列一顆「Wiki」,打開 `<dialog>`(`showModal()`),dialog 放在 `#page-playlists` 裡面——`promptHost` 的 `reveal()` 才不會把人丟去主控台(console.js:466-484)。
- 命令:`args: ['wiki','--title',曲名,'--artist',歌手.join(', ')]`。歌手用 `", "` 串,跟「正在播」那條路的快取 key 同一種(wiki_run.go:132),同一首歌字串一樣時兩邊共用快取。不加 `--album`(§7)。
- 畫法:重用 wiki.js 已經 export 的 `lineSplitter` / `wikiRenderer`;免責行是命令印的最後一行,照畫(決策 59)。
- **停止**:`showModal()` 會讓 dock 的中止鈕變成 inert,所以 dialog 裡有自己的「停止」(`con.stop()`);問 AI 期間關掉 dialog(✕ / Esc)也算停止(Q2)。成功後有「再問一次」(`--refresh`)。
- **沒設定 AI 端點**:dialog 顯示 CLI 那句,加一顆「設定 AI 端點」= `args: ['wiki','setup']`,`promptHost` 是 dialog 裡的區塊(表單就地出現,同 wiki 頁)。
- **鍵盤**:app.js 的單鍵層從 `keysDialog.open` 改成「有任何 `dialog[open]`」,不然 dialog 開著時 1–9 會在背後換頁。
- **政策**:新入口送出的歌名、歌手來自你 Drive 上的正本(經本機 state.db)。送的欄位沒變(仍是 `ai.SentFields` 的子集),但隱私權政策 §5 那句「是音樂本身的資料,不是你的 Google 使用者資料」與 README 的「正在播的歌」需要改寫(Q5)。

### 3.4 正本與平台清單的關係(需求 2)

用三層把關係講清楚,越往下越具體:

1. **lead 改寫**成一句完整的模型:正本在你的 Drive、每個平台最多連一份、同步時互相帶過去。
2. **`<details>`「正本、連結、同步是什麼意思?」**:四則短說明(正本 / 連結 / 同步 / 搬家和同步不一樣),預設收起,第一眼只有 lead。
3. **選中清單的「連結面板」**(§3.5):左邊一個「Google Drive 正本」節點,右邊一個平台一列,把「這份正本連到哪幾份平台清單」畫成看得到的東西。

另外:

- 左欄晶片改用 `providerName`(Spotify / Apple Music / YouTube Music / 本機),不再顯示原始 id + ✓。
- **「平台上現有的清單」改成「各平台上的清單」**:一顆「讀取各平台的清單」,按了才依序對每個平台跑 `pl list --provider X`(會連網、每家各佔一次序列槽);每一列標出「連著正本「X」」或「還沒納入」,還沒納入的可以「納入 capy」(§3.5,Q6)。

### 3.5 就地同步(需求 3)

**連結面板**,每個平台一列(本機只在連著時出現):

| 狀態 | 顯示 | 動作 |
|---|---|---|
| 連著、是這台電腦 / 這個帳號的 | 已連結;有幾首還沒有這個平台的對應(從 export 的 mappings 數,零網路) | 平台上的清單連結(Spotify:`spotifyLink('playlist')`;YouTube:`music.youtube.com/playlist?list=<清單 id 斜線後那段>`,連結 id 是 `<channel_id>/<playlistId>`;Apple 的資料庫清單沒有公開網址,不給);有缺對應時「找對應」= `args: ['resolve',pid,'--provider',p]`(寫入前 CLI 自己問確認) |
| 連著、但屬於別台電腦(local)或別的 YouTube 帳號 | muted:「連在另一台電腦「名稱」」/「連在另一個 YouTube Music 帳號」 | 無(同 pull / push 的跳過規則,決策 33、60)。判斷用 `auth status --json` 的 `google.device_id` / `youtube.channel_id`(不連網,進頁時用 `quiet` 跑一次)配 manifest.devices 的名稱 |
| 沒連,Spotify / Apple Music / YouTube Music | 沒有連結;已經知道幾首在那個平台的對應(mappings 全域共用) | 「在 X 建一份」→ 見下 |
| 沒連,本機 | 不顯示這一列 | 本機不能 `--create`;連既有的 M3U 會碰到順序問題(Q6) |

**「在 X 建一份」**(示意圖狀態三):

1. 先在頁面上說清楚會發生什麼(「會在 X 建一份叫「太好聽」的空清單,替每一首找 X 上的對應,再照正本的順序把找得到的加進去;寫入前會先列出來給你確認」),按「開始」才跑。這是頁面自己的說明,不是替 CLI 回答確認。
2. 依序跑三個命令,步驟條(搬家精靈同款)顯示進度,任何一步 exit ≠ 0 就停,並說明停在哪、現在的狀態(例如「清單已建立並連上;之後按『同步這份清單』就會補上」):
   - `args: ['pl','link',pid,p,'--create']`
   - `args: ['resolve',pid,'--provider',p]`(有新對應時 CLI 問確認)
   - `args: ['pl','sync',pid,'--provider',p]`(變更表 + 確認)
3. 三個命令都帶 `promptHost`(連結面板裡的區塊),確認提示就地出現;頁面絕不代加 `--yes` / `--force`、不替人按確認(決策 46)。

**「同步這份清單」**(右欄標題旁的主要按鈕):`args: ['pl','sync',pid]`,`promptHost` 同上;變更表沿用同步頁的畫法(把 sync.js 在 `initSync` 裡的 `table()` 搬到 table.js 共用,同步頁行為不變)。收尾:exit 0 沒有變更 =「已經是最新的」;exit 2 = 你按了取消、沒有寫入;exit 3(刪除閾值或前提不成立)= 照印 CLI 原文,再補一句白話:「capy 先停下來了。這一頁不會替你加 --force;確認沒問題的話到主控台照上面的命令執行」。沒有任何連結時按鈕停用,旁邊說「先連一個平台」。

**「納入 capy」**(各平台上的清單,Q6):只在「沒有同名正本」時給(同名時 `pl link` 會連到那份既有正本,第一次 pull 會重排它);跑 `args: ['pl','link',<平台清單名>,'<平台>:<id>']` → `args: ['pl','pull',<平台清單名>]`(新正本是空的,第一次 pull 採平台順序是對的;變更表 + 確認就地出現)。有同名正本時那一列說「有同名的正本,要合併請用『搬家』」,連到 `#/move`。

**不引導的路**:把平台上既有、非空的清單連到已有曲目的正本(會採平台順序,derive.go:114-129,決策 38)。要把一份平台清單的歌併進正本,走搬家(加進既有清單 = 只加在尾端)。

### 3.6 資料新鮮度

- 標題下一行固定寫「顯示的是這台電腦上次同步後的內容」(export 的本質,不假裝是即時)。
- 這一頁的任何寫入命令收尾後,用 `quiet` 重跑 `export`(不灌進主控台),選中的清單不變。
- 從別頁回到這一頁時也重讀(Q7)。

### 3.7 硬約束對照

| 約束 | 這次怎麼守 |
|---|---|
| 決策 38 順序 | 表格照 items 順序、不排序不拖曳;篩選只藏列;不引導會重排的連結路徑 |
| 刪除前 dry-run 與閾值 | 所有寫入走 `pl sync` / `resolve` 自己的變更表與確認;頁面不碰 `--force` |
| 決策 40–41、46 web | 全部走 `Console.run` 的序列槽;不加直達端點;`promptHost` 在本頁;不代加 `--yes` / `--force` |
| 決策 48 | 平台只用文字名稱 |
| 決策 49 / 60 寫入邊界 | 寫入仍由 CLI 決定:Apple 只寫可編輯的自建清單、YouTube 只寫自建清單;頁面只發命令 |
| 決策 50 i18n | 新字串 `webui.playlists.*` 兩份語系同補,英文 label 用進行式,webui 無中文字面;拿掉的 key 兩份一起刪 |
| 決策 52 | Apple ▶ 照實說「只打開」 |
| 決策 59 wiki | 送的欄位不變;免責行照畫;AI 端點仍 BYO;政策與 README 同 PR 改(Q5) |
| S7 | 每列 Spotify 對應都有看得到的「在 Spotify 上聽」 |
| 隱私權政策 | 外部服務與存進 Drive 的內容都沒變;只有 wiki 的資料來源描述要改(Q5) |

## 4. 測試(每一則都是改之前會 fail 的)

**一定要改的既有測試**(不是「可能要改」):

- node 情境 8k / 8n / 8o(testdata/webui_console.mjs:584-896):children 索引、`cells[0]` 是 CID、晶片字面 `apple ✓`、每列 `[6,6,6,6,6]` 的寬度、「看 Spotify 上的內容」按鈕都會變。改成新結構的等價斷言,不放寬到「會過就好」。
- `TestWebStaticFrontendContracts` 的字串比對(清單頁的部分)。
- i18n:`TestNoUnusedKeys` 會抓到被拿掉的 `webui.playlists.list` / `list_label` / `view_on` / `view_label` / `platform_heading` 等,兩份一起刪。

**新增**(node 情境,中英各跑一輪):

1. 列結構:沒有 CID 文字、`data-cid` 在;列序等於 items 順序(同一首兩次 = 兩列);墓碑顯示勝者的曲名。
2. 平台欄出現規則:連著或有對應才出現;本機永遠不是一欄。
3. Spotify 格:▶ 送出 `['play','--id',id,'--provider','spotify']`;「在 Spotify 上聽」看得到、`href` 對;非 22 碼不連。
4. Apple 格:數字 id 給 ▶ 並送出正確 args;`i.` / `a.` 不給 ▶;exit 0 且不以 ▶ 開頭時 notice 照抄那句。
5. YouTube 格:「開啟」的 `href` 與新分頁。
6. Wiki:送出 `['wiki','--title',t,'--artist','A, B']`;dialog 在 `#page-playlists` 裡;「停止」與執行中關掉 dialog 都呼叫 `stop()`;沒設定時「設定 AI 端點」送 `['wiki','setup']`,`promptHost` 在 dialog 裡。
7. 鍵盤:非鍵位表的 dialog 開著時,數字鍵不換頁。
8. 在 X 建一份:按「開始」前零命令;之後依序三個命令、都帶 pid、`promptHost` 在本頁;任一步失敗就停;整頁原始碼沒有 `--yes` / `--force`。
9. 同步這份清單:送出 `['pl','sync',pid]`;沒有連結時停用並說原因;exit 3 顯示 CLI 原文 + 那句白話。
10. 各平台上的清單:連著的列標出正本名稱;有同名正本時沒有「納入」;納入依序送 link、pull。
11. 寫入收尾後的重讀用 `quiet`(主控台不多一個區塊)。
12. 英文那一輪:頁面與 label 沒有 CJK,label 符合 `/^[A-Z][a-z]*ing /`。

**Go 靜態**:playlists.js 不含 `--yes` / `--force`;清單頁用 `.tbl-wrap--tall`;app.js 的單鍵層看 `dialog[open]`。

**網站**(Q5 選 A 時):`site/site_test.go` 釘住政策新句子的關鍵詞(中英)。

量不到版面(node 替身沒有排版),寬度與捲動留給 §8 人工驗收。

## 5. PR 切法

- **T0**:本文(docs only)。
- **T1 歌曲表 + 點播**:固定高度的視窗、四欄 + 平台欄、▶ / 連結 / 「只在資料庫」、左欄晶片與篩選、lead 與 `<details>`、清單內篩選(Q7)。需求 1、4,需求 2 的說明部分。
- **T2 連結面板 + 同步**:連結面板、「同步這份清單」、「在 X 建一份」、「找對應」、各平台上的清單與「納入」、寫入後與回到頁面時重讀。需求 2、3。
- **T3 wiki dialog**:dialog、停止、設定入口、鍵盤單鍵層、政策 ×2 / README ×2(Q5)。需求 5。
- **T4 文件收尾**:指南 ×2(`docs/guide.html`、`docs/guide.en.html`)補「我的清單」一段 → `go test ./site/ -run TestGuideOnSiteIsCurrent -update` → 重發兩個指南 Artifact(英文那份的網址不在 repo,要向維護者拿)→ 合併後去看線上 `/guide`、`/en/guide`;附錄 C 決策 61;順手修 §9 的文件不一致。

每個 PR 都跑 `go test ./...`、`go mod tidy -diff`,node 情境中英兩輪。

## 6. 待定案的 Q(推薦選項放前面)

| Q | 問題 | 選項 |
|---|---|---|
| Q1 | 每一列的點播怎麼長 | **A(推薦)一個平台一欄**:Spotify = ▶ + 在 Spotify 上聽;Apple = ▶;YouTube = 開啟(示意圖)。一眼看得出每首在哪些平台有,一鍵就放。/ B 一顆 ▶ + 表格上方選「用哪個平台播」,右邊只留連結:列比較乾淨,但要先選平台。/ C 只給連結、不用 capy 遙控:不佔序列槽、不需要 Premium,但 Spotify / Apple 要到了平台還得自己按播放 |
| Q2 | Wiki 開在哪、關掉時怎麼辦 | **A(推薦)就地 dialog,問 AI 期間關掉 = 停止**(把序列槽還給播放)。/ B dialog,關掉時讓它在背景跑完寫進快取(期間這頁的 ▶ 都會被擋,而且看不到它在跑)。/ C 不開 dialog,跳到 wiki 頁並帶入歌名(離開清單;路由要能帶參數給 wiki 頁) |
| Q3 | Apple 只在資料庫、沒有 catalog 對應的歌(`i.` / `a.`) | **A(推薦)不給 ▶,標「只在資料庫」並說原因**。/ B 加一條「用資料庫列 id 找 persistent ID 再播」的新 Apple 播放路徑(新行為,要先探測,另開計畫) |
| Q4 | Spotify 非 Premium 的 `PREMIUM_REQUIRED` 原文 | **A(推薦)這次不處理,另開小 PR 補在地化訊息**(影響 CLI 與 web 所有播放入口,不只這頁)。/ B 併進 T1 |
| Q5 | wiki 的新入口與隱私權政策 | **A(推薦)T3 同一個 PR 改政策 §5(中英)與 README ×2**:說明歌名與歌手也可能來自你存在 Google Drive 的清單正本,只有你按下時才送、只送到你自己設定的端點;site_test 釘住新句子。/ B 不改政策(字面沒限定來源,測試不會紅),只改 README |
| Q6 | 「各平台上的清單」要做到哪 | **A(推薦)讀取 + 標出關係 + 沒有同名正本時可「納入 capy」**。/ B 只讀取與標出關係,不給動作(納入請到主控台)。/ C 拿掉這一段 |
| Q7 | 回到這一頁時自動重讀、清單內篩選 | **A(推薦)兩個都做**:回到頁面就用 `quiet` 重跑 export(別頁寫入後這頁不會過時);超過 20 首時出現「在這份清單裡找歌」。/ B 都不做,只留「重新整理」鈕 |
| Q8 | 固定視窗的高度 | **A(推薦)沿用 `.tbl-wrap--tall` 的 60vh**(同步頁、搬家預覽同一個)。/ B 另訂(例如 `clamp(22rem, 62vh, 44rem)`) |

## 7. 明確不做(要就另開)

- 排序、拖曳、在頁面上直接改正本(沒有這種命令;決策 38)。
- 「最後同步時間」與進頁時自動算每份清單要不要同步:前者要改 Drive 形狀(跳 schema 版 + 政策),後者每份清單都要 `pl sync --dry-run`(連網、拿 pull.lock、佔序列槽)。
- 用 dev__ base 顯示平台那一側的名稱與曲數。
- 點一首接著播完整份清單(`PlayRequest` 沒有 offset;Apple 不支援清單播放)。
- Apple 的 https 歌曲頁連結(要先讀 storefront;▶ 已經涵蓋「在 Music.app 打開」)。
- 連結前警告寫不了的清單(`pl list` 的 TSV 沒有 Unwritable 欄)。
- 連到平台上既有、非空的清單(順序會被改;請用搬家)。
- 新的直達端點(例如唯讀的 `/api/canon`)。
- `capy wiki --album`。
- 搜尋頁對本機也放「播放」鈕、按了必定失敗的既有瑕疵(另開)。
- 虛擬捲動。

## 8. 驗收清單(R-47–R-55;T1–T4 都進 main 後由維護者跑)

- **R-47** 1440 / 1280 / 1024 / 400px:頁面與歌曲表都沒有橫向捲動;607 首的清單頁面高度不跟著曲數變長。
- **R-48** Spotify ▶(Premium、有開著的 Spotify):開始播、底部播放列跟過去;把 Spotify 關掉再按:訊息看得懂,「在 Spotify 上聽」可以退回。
- **R-49** Apple ▶(macOS):資料庫裡有的歌真的播;只在目錄裡的歌在 Music.app 打開並標出來,頁面照實說;「只在資料庫」的列沒有 ▶。
- **R-50** YouTube「開啟」:新分頁到 music.youtube.com 的那一首並開始播。
- **R-51** Wiki dialog:設定好的端點邊到邊出現;「停止」與關掉都會停、播放鈕恢復;同一首第二次開是快取、立刻出現;沒設定時「設定 AI 端點」表單就地出現。
- **R-52** 「在 YouTube Music 建一份」(拋棄式正本,真帳號寫入要維護者授權):三步都就地確認;建出來的清單順序與正本一致;取消任一確認時零寫入。
- **R-53** 「同步這份清單」:有變更時就地出現變更表與確認,取消 = 零寫入;沒有變更時說「已經是最新的」。
- **R-54** 各平台上的清單:連著的標出正本名稱;「納入 capy」建出新正本並照平台順序拉進來;有同名正本時沒有「納入」。
- **R-55** 語言切到 English 整頁走一遍:沒有中文、字不被截斷、按鈕不換行。

## 9. 順手修正的文件不一致(T4)

- `webui.playlists.empty`(zh-TW.json:1305 / en.json 同 key)叫人到「同步」連結清單,但同步頁沒有連結;T1 改寫時一併修掉。
- ARCHITECTURE.md:963-964、1056,計畫 2026-09-18-web-consumer.md:20,web_test.go:1109 的註解仍寫 Apple 不能當目的地(決策 49 之後可以)。
- 不在這次範圍、另開:README 的 push 前提一寫「這台裝置 pull 過」,程式檢查的是所有裝置合併後的 base(push.go:187-190);project.go:29-30 說 Apple 的 `i.` id 推不出去,但 `Pushable` 收它(apple.go:93-95)。
