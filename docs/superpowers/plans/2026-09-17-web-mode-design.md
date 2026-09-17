# `capy --web` 視覺識別規格(2026-09-17)

> 本文件是 [2026-09-17-web-mode.md](2026-09-17-web-mode.md) 決策 43 的完整規格,T3a / T5 照這份實作。來源:三份獨立提案(相位差主控台 / 冷讀數 HUD / Coldline)+ 三位評審(design-taste 規則稽核、產品可用性、品牌與工程可行性),以總分最高的「相位差主控台」為底,移植另兩案被點名的最佳點子、修掉所有被點名的違規。repo 事實以 `internal/ui/theme.go`、`internal/ui/ui.go:182`、`internal/cli/tui.go:102,185-199`、`internal/cli/tui_capybara.go`、`internal/cli/pull.go:32-63`、`internal/cli/pick.go`、`docs/guide.html` 為準。
> 使用者原話:「畫面採用 /design-taste-frontend 這 skill 去規劃做設計。風格採用 geek 綠,採用一些冷冽的霓虹特效,帶一點 cyberpunk 風格。」那份 skill 自己宣告 dashboard / 資料表 / 多步驟 product UI 不在範圍,所以只套用適用的規則(§14 逐條);霓虹是使用者明說的 override,要「有意圖」。
> 執行模型依計畫決策 40:命令在行程內跑、互動提示以 SSE 事件送到頁面、頁面 POST 回答;本規格裡所有「提示」都是那個橋的渲染,不是頁面自己組 `--yes` 重送。

## 1. 設計讀法(一行)

終端機的第二個視窗:dock 就是 TUI 的底部四行搬進瀏覽器,七個導覽頁只是把同一條 capy 命令的輸出畫得更豐富的渲染器;霓虹不是第二種顏色,是同一個 `#3FB27F` 的兩個相位:進行中、聚焦、正在播的東西發光,落定之後回到平面。

## 2. 三個 dial(0 到 10)

- variance 4:骨架全直角、單一色相,個性靠 mono 字型與相位規則,不靠版面花樣。
- motion 2:三級動效(回饋 / 狀態 / 一次簽名),全部只動 opacity 與 transform。
- density 8:資料密集,表格 30px 列、13px mono,main 不設 max-width。

## 3. 色票

全部定義在 `:root`,單一深色主題(理由見 §11)。對比以 WCAG 相對亮度實算,格式「ink / surface / surface-2」。

| token | hex | 角色 | 對比 |
|---|---|---|---|
| `--ink` | `#0D1117` | 頁面底、dock 底、主要按鈕的文字色 | 在 accent 上 7.11 |
| `--surface` | `#161B22` | 區塊底、表格偶數列斑馬、輸入框底、ISRC 標頭卡、提示區塊底 | |
| `--surface-2` | `#1C232B` | kbd、code chip、骨架條、次要按鈕底。**這一層上只放 `--text` 與 `--accent`,不放 `--muted`**(muted 在此 4.35 不過 AA);hover 不改列底色,所以 surface-2 從不承載表格文字 | |
| `--line` | `#2A323C` | 只當裝飾線:外框、分隔、dock 靜止時的頂線、exit 0 區塊左線 | 1.46,不當控制項邊界 |
| `--line-hi` | `#646E7B` | 控制項邊界:輸入框、range 軌、次要按鈕框、stderr 左 gutter、表格 hover 列左線 | 3.66 / 3.34 / 3.06,三層都過 WCAG 1.4.11 |
| `--accent` | `#3FB27F` | 唯一主色:`>` 提示符、焦點、進行中、播放中、active 導覽左線、表頭、add、✓、水豚 | 7.11 / 6.50 / 5.95 |
| `--accent-dim` | `#2A6E52` | 只當線:表頭底線、ISRC 分段頂線、非焦點的強調框、seek 填充 | 3.11,不當文字 |
| `--accent-rgb` | `63 178 127` | 同一個綠的三元組,只給 glow 的 alpha:`rgb(var(--accent-rgb) / .35)` | |
| `--text` | `#C9D1D9` | 主要文字、stdout、表格格 | 12.26 / 11.21 / 10.27 |
| `--text-hi` | `#E6EDF3` | 頁標題、命令回聲、正在播放的曲名、選中的清單名。取代 guide.html 的 `#fff`(不用純白);單一字重下這是強調的唯一手段 | 16.02 / 14.64 / 13.42 |
| `--muted` | `#7D8794` | 次要文字、stderr、時間 / 單位、exit 0 與 130 的 badge、未登入。採 guide.html 的值而非 theme.go 的 `#6E7681`(後者 4.12 不過 AA);唯一一步有文件的品牌漂移,不再加第三個值 | 5.20 / 4.75 / (surface-2 禁用) |
| `--warn` | `#D9A441` | exit 2(需介入)/ 3(安全閥)badge、閾值擋下的橫幅左線、Apple 揭露左線 | 8.41 / 7.69 / 7.05 |
| `--danger` | `#ED567A` | exit 1、action = remove、表單驗證錯誤、危險按鈕文字與框。沿用 HuhStyles 的紅;永不發光、永不填色 | 5.60 / 5.12 / 4.69 |
| `--glow` | `0 0 0 1px rgb(var(--accent-rgb)/.55), 0 0 14px rgb(var(--accent-rgb)/.35)` | 四處霓虹的 box-shadow 值 | |
| `--glow-text` | `0 0 8px rgb(var(--accent-rgb)/.6)` | `>` 提示符聚焦時的 text-shadow;全站唯一的 text-shadow | |
| `--scan` | `.03` | 掃描線 alpha;`prefers-contrast: more` 時 0 | |

不採用:`--accent-hi`(第二種綠,hover 用 opacity .92 即可)、第三個 muted 值、`$` 提示符(TUI 的提示符是 `>`)。HuhStyles 的 blurred 按鈕(text on `#6E7681`)實測 2.98,web 的次要按鈕改為 `--text` on `--surface-2` + 1px `--line-hi`。

## 4. 字體

mono 是聲音,sans 刻意安靜。介面骨架(導覽、表頭、badge、提示符、kbd、ID、數字、TSV、命令回聲、頁標題)全走 mono;sans 只在散文格(曲名、藝人、REASON、說明、表單 label、help)。

**`--mono`**:內嵌 Iosevka Fixed Regular 一支 woff2(OFL;Fixed = 無連字,`->` `!=` 在 provider id 裡保持字面;預設斜線零)。子集離線切到 U+0020-007E、U+00A0-00FF、U+2010-2027(· … ‐)、U+2190-2199(↑↓←→)、U+25CF(●)、U+2713 U+2717(✓ ✗)、U+26A0(⚠)。約 35 KB,佔 14.9 MB binary 的 0.23%。**不含** U+2500-259F:水豚是純 ASCII(`tui_capybara.go` 開頭明寫),進度用 range 元素不用 █。單一字重,`font-synthesis: none`;強調靠 `--text-hi` 與字級,不靠粗體。理由:字幅 0.5em(SF Mono / Consolas 0.6em)讓十欄同步表 1100px 放得下;macOS 與 Windows 的 ID 欄、水豚、ISRC 格長得一樣。堆疊:`"Iosevka Fixed", ui-monospace, "SF Mono", Menlo, "Cascadia Mono", Consolas, "DejaVu Sans Mono", monospace`。刪掉那段 `@font-face` 就退回系統 mono,其餘 CSS 不動(計畫 Q37)。遞送:`<link rel="preload" as="font" type="font/woff2" crossorigin>`(同源也要 crossorigin,否則抓兩次)+ `font-display: block`(本機讀取沒有等待);CSP 要有 `font-src 'self'`。CJK 曲名落在 mono 欄時走系統全形字,接受,終端機也是這樣。

**`--sans`**:`system-ui, -apple-system, "Segoe UI", "PingFang TC", "Noto Sans TC", "Microsoft JhengHei", sans-serif`,與 guide.html 同一串。理由是被迫的:介面是 zh-Hant,CJK 不可能內嵌(一個字重 5 到 10 MB),sans 的拉丁字得跟旁邊的系統 CJK 同一家;`<html lang="zh-Hant">` 讓 Windows 拿 JhengHei 不拿 YaHei。不用 Inter、不另嵌一套 sans。

**尺寸**:mono 13px(表格、主控台、命令列)、12px(表頭、副資訊)、11px(badge、鍵位提示)、14px(命令列輸入)、20px `--text-hi`(頁標題)、24px `--accent`(ISRC 分段);sans 14px(散文格、label)、12px(muted meta、驗證錯誤)、20px `--text-hi`(ISRC 曲名)。行高:主控台 pre 1.5、表格格 1.45、sans 1.6。sans 出現數字的地方一律 `font-variant-numeric: tabular-nums`。字距 0,沒有大寫追蹤標籤。

## 5. 版面

`body { display:grid; grid-template-columns: 13.5rem minmax(0,1fr); grid-template-rows: minmax(0,1fr) auto; height:100dvh }`。

- **rail**(左欄,`--surface`,右側 1px `--line`):wordmark `capy.`(mono 16 `--text-hi`,點是 accent,同 guide)48px;導覽七項各 32px:播放 / 搜尋 / 清單 / 同步 / ISRC / 帳號 / 診斷(ISRC 是一頁,不是搜尋的分頁);底部帳號 chip `spotify ✓  apple ·  google ✓`,mono,狀態由 ✓ / · 字符承載。
- **main**(右欄):`overflow:auto`,padding 24px 32px,不設 max-width;頁標題列 48px:標題 mono 20 `--text-hi`,同基線接該頁的 CLI 等價命令 mono 13 `--muted`(例:`同步` 接 `pl sync --all --dry-run`),點了填進命令列、不執行;主要動作靠右。ISRC 頁是唯一沒有 CLI 等價命令的頁(它走 `/api/isrc`,終端機對應的 `debug lookup-isrc` 在 web 不開放),標題旁空著。
- **dock**(第二列橫跨兩欄,貼滿全寬,它是 TUI 的底部四行):正在播放列 40px、命令列 40px、log 面板(0 / 40vh / 撐滿 main 三段,反引號切換,切換不做動畫)。log 就是 TUI 的捲動區:一個命令一個區塊,新區塊落在最下面並立刻捲到底;使用者往上捲就停住,底部浮一顆 `新輸出 ↓` 次要按鈕。
- 每一頁的表單只做一件事:組出一條 capy 命令,打進命令列、回聲、串流、退出碼、提示全部在 dock;該頁再把解析後的輸出畫成表格或卡片。沒有 toast、沒有 modal 通知;唯一的 `<dialog>` 是 `?` 鍵位表。
- 間距 4px 基數:`--sp-1` 4、`--sp-2` 8、`--sp-3` 12、`--sp-4` 16、`--sp-6` 24、`--sp-8` 32。表格列 30px、格 padding 5px 12px、表頭 28px。區塊:2px 左線 + 12px gutter、頭 28px、間距 16px。
- 圓角:全直角,`--r: 0` 唯一 token,按鈕、輸入框、badge、卡片、封面、range、dialog、kbd 一律引用它。
- 寬度:≥ 64rem 完整 rail;48 到 64rem rail 縮 3.25rem 只留圖示(label 進 title);< 48rem rail 變頂部橫向 tab 條,dock 不變;400px 下 body 不橫捲,表格各在 `overflow-x:auto` wrapper 裡捲。播放清單頁 ≥ 80rem 左清單 20rem + 右內容兩欄,以下上下疊。
- 表格捲動容器:sticky thead 只在有高度上限的 wrapper 裡成立(overflow-x:auto 的 wrapper 不跟著 main 捲)。同步變更表與播放清單內容表:wrapper `overflow:auto; max-height:60vh`,thead sticky,同步表另把 DIR、ACTION 兩欄 sticky left;其餘表格不宣告 sticky。

## 6. 霓虹的意圖規則

glow 由 `::after` 偽元素扛 box-shadow,進出只動偽元素的 opacity;box-shadow 與 filter 永不進 transition 或 keyframes(review 直接擋)。全站發光的地方只有四處,對應 app.css 一個標題為 `glow budget` 的註解塊底下的五條宣告(規則 1 是 `--glow` 加 `>` 提示符的 `--glow-text` 兩條,其餘三處各一條);註解塊之外再出現 `--glow` / `--glow-text` 就是 review finding。同一時間畫面上發光的東西不超過三個(power-on 只在主控台空白時發生,那時沒有進行中)。

有:
1. **焦點**:`:focus-visible` 一律 2px `--accent` outline offset 2px + `--glow`。文字輸入框滑鼠點進去也會拿到 focus-visible,這是對的:命令列亮著 = insert 模式、單鍵快捷鍵關閉;`>` 提示符同時吃 `--glow-text`。
2. **進行中**:dock 頂線從 `--line` 變 `--accent` + `--glow`,區塊狀態字 ● 脈衝;退出碼一落定兩者瞬間回平面。一次只有一個進行中(決策 40 的序列槽保證)。
3. **播放中**:seek range 的拇指(2×14px `--accent`)只在 `[data-playing]` 時帶 `--glow`;暫停就平面。填充用 `--accent-dim`,永不發光。
4. **簽名時刻**:水豚 power-on,每個 session 一次(§7)。

沒有:導覽(active 是 2px 左線,平面)、wordmark、標題、表頭、表格線與列、按鈕任何狀態、badge、kbd、卡片、封面、連結、exit 0 的結果、清單 chip、dialog、骨架、ISRC 格子、`--warn` 與 `--danger`(錯誤是死的不是活的,只有平面文字 + 2px 左線)。掃描線不算霓虹,它是靜態 3% 的 `--text` 色細線。

## 7. 簽名時刻

沿用 TUI 已有的行為(guide.html:「開場的水豚動兩秒後定格」)。主控台空白時顯示 `capybaraStill()` 那八行 ASCII 水豚,`--accent`,底下一行 `capy · spotify` muted。第一次繪製時 power-on:水豚 opacity 用 `steps(3)` 從 0 到 1 走 2s,像終端機分三幀把牠畫出來,期間 `::after` 的 glow 層開著;2s 到,glow 層 400ms `--ease-out` 退場,水豚落在它從此保持的平面 accent。整個論點在這一個手勢裡:霓虹是過渡相位,平面是靜止態;之後每條命令進行中發光、落定變平面都是同一件事。`prefers-reduced-motion: reduce` 直接畫最終幀。ISRC 頁沒有第二個簽名時刻,格子直接以靜止態出現。

## 8. 元件清單與關鍵樣式

視覺文法(全站一致):**2px 左線 = 位置**(active 導覽、選中 / 焦點列 accent;hover 列 `--line-hi`)、**1px 全框 accent = 需要你**(待答的提示區塊、dialog)、**兩者永不同時出現在一個元件上**。

- **rail 導覽項**:mono 13 + Phosphor regular 16px;`--muted`,hover `--text`(瞬間換色,無 transition),active `--accent` + 2px 左線;右側 muted 11px 鍵位提示 1 到 7。
- **dock 正在播放列 40px**:播放 / 暫停 Phosphor 圖示 accent(不用 ⏸ 字符);曲名 mono `--text-hi`;藝人 · 專輯 sans muted;seek 是原生 `<input type=range>`:元素高 32px(命中區),軌 2px `--line-hi`、填充 `--accent-dim`、拇指 2×14 `--accent`(播放中才 glow),`change` 事件跑 `seek <秒>`(不是 `input`,避免連發);時間 mono tabular;裝置名 mono muted,點了開原生 `<select>` 跑 `devices` 後的切換;音量 = Phosphor speaker 圖示 + 80px 原生 range 0 到 100,`change` 跑 `vol <n>`。全部回聲進命令列。資料來自 `/api/now` 每 2 s 輪詢(決策 42);伺服器斷線時整列換成 `--warn` 左線的 mono 一句「capy --web 已停止」+ `r 重新連上`(鏡射 TUI 的 r)。
- **dock 命令列 40px**:`>` accent mono 14,輸入框透明底 + 1px `--line-hi` 底線,聚焦即 §6 規則 1;斜線選單向上長、mono 清單(來源 `/api/commands`)、打字即濾、選中列 accent 帶 `>` 前綴、Tab / Enter 補齊、Esc 收起;↑↓ 翻歷史;鍵位照 TUI。執行中輸入框仍可打字,Enter 只印一行 muted `正在執行,Ctrl-C 中止`,不排隊、不並行;右側出現次要按鈕 `中止`(文字 `--danger`,打 `/api/jobs/{id}/cancel`)。
- **主控台區塊(一命令一塊)**:2px 左線依狀態:進行中 accent、exit 0 `--line`、1 danger、2 / 3 warn、130 muted;頭 28px:`>` + 命令原文 mono `--text-hi`,右側耗時 mono muted + 退出碼 badge;狀態字符照 tui.go:1 是 ✗、2 / 3 / 130 是 ·、0 不印、進行中是脈衝 ●;body `<pre>` 13px / 1.5,stdout `--text`,stderr `--muted` + 2px `--line-hi` 左 gutter(stderr 是進度不是失敗,永不預設紅),換行不截斷;`table` 事件直接畫成表格元件(標題就是事件裡的 header),`<details>`「原始輸出」不需要(表格不再是 TSV);`auth login apple` 的揭露 note 永不可收合。命令回聲對 `--developer-token` / `--user-token` / `--client-secret` 的值一律遮成 `***`(這三個 flag 在伺服器端本來就 403,遮罩是第二層)。
- **退出碼 badge**:mono 11、1px 框與文字同色、透明底、padding 1px 6px。永遠帶數字:`exit 0` muted、`exit 1` danger、`exit 2` / `exit 3` warn、`exit 130 已中止` muted、進行中 `running 00:12`(accent,秒數每秒換字);exit 事件的 `reason` 不是 done 時在數字後接「已取消 / 逾時 / 伺服器關閉」。2 與 3 的意思由區塊裡的 stderr 那句原文說明(pull 的 2 是待套用、play 的 2 是歧義、pull / push / migrate 的 3 各有各的擋法),不硬編每個命令的標籤。
- **提示(橋的渲染;決策 40 第 3–4 點)**:`prompt` 事件到達時區塊尾端長出一個提示區(1px accent 全框、`--surface` 底),焦點移進第一個控制項;`prompt_closed` 到達就把它換成一行 muted 紀錄(答了什麼 / 已取消 / 逾時)。四種 kind:
  - `confirm`:title 一行 + 兩顆按鈕(`affirmative` / `negative` 的原字,huh 的「套用」/「取消」),Enter = 肯定、Esc = 送 `cancel:true`(= dismiss,跟終端機 Esc 一樣是 exit 1 而不是「取消」的 exit 2;差別靠 badge 的數字與 stderr 原文說明);同一區塊裡上一個 `table` 事件若 ACTION 有 `remove`,「套用」改危險版(1px `--danger` 框 + 文字)。`note` 存在時(Apple 揭露)先顯示 note:`--warn` 左線、sans 14、不可收合。
  - `select`:`<fieldset>` + 視覺隱藏 `<legend>` 的 listbox,選中 `✓ ` accent 前綴、其餘 `• ` muted(HuhStyles 原樣),↑↓ / j k + Enter,超過 8 筆顯示過濾輸入框,Esc = `cancel:true`。
  - `input`:label 在上(title)、mono 或 sans 輸入、`error` 在下 danger,Enter 送出、Esc = `cancel:true`。
  - `form`:`note` 區塊(title + body,body 原文含換行照排)+ 依 `fields` 逐欄 label 在上、`secret:true` 用 `<input type=password autocomplete=off>`,`error` 在欄位下;送出鈕 + 取消鈕。密碼欄的值送出後立刻清空;任何情況不回顯。
- **表格**:表頭 = `table` 事件的 header 原字串(`ID 曲名 藝人 專輯 時長`、`DIR ACTION PROVIDER PLAYLIST POS CID PROVIDER_ID TITLE ARTISTS REASON`、`ACTION CID PROVIDER PROVIDER_ID CONFIDENCE SOURCE TITLE ARTISTS REASON`、`名稱 類型 狀態 音量 ID`),mono 12 accent、1px `--accent-dim` 底線;列 30px,偶數列 `--surface` 斑馬,**hover 不改底色**,改 2px `--line-hi` 左線;焦點 / 選中列 2px `--accent` 左線(roving tabindex、j / k、Enter 開列動作);不畫列線。原子欄照 ui.go:182 的 `atomicHeader`(`ID` `CID` `PID` `ISRC` `DEVICE` 與 `*_ID`)mono + nowrap,永不折行、永不截斷、沒有 ellipsis、沒有 `max-width` + title;`POS` 不是原子欄但靠右 tabular;TITLE / ARTISTS / REASON sans 可折行;`時長` / `DURATION` 欄把毫秒整數轉 m:ss。沒有欄位排序、沒有拖曳把手、連唯讀重排檢視都不給(決策 38)。`<th scope="col">`、視覺隱藏 `<caption>` 帶列數。
- **同步變更表(十欄)**:wrapper `overflow:auto; max-height:60vh; min-width:68rem`,thead sticky,DIR 與 ACTION sticky left。DIR = Phosphor 圖示 + 字,四種:arrow-line-down `pull`、arrow-line-up `push`、arrow-line-right `migrate`、minus `dedup`。ACTION 的字本身上色:add accent、remove danger(+ warning 圖示)、move / rename / skip `--text`(skip 不是變更,計數扣掉)。REASON sans muted。exit 3 擋下 = `--warn` 左線橫幅,內容是 stderr 原文。
- **搜尋頁**:`關鍵字` label 在上,sans 輸入(多為中文曲名);provider mono 分段控制;limit number input。結果表 `ID 曲名 藝人 專輯 時長` + 列動作:`播放`(跑 `play --id <id> --provider <p>`,`--id` 是精確命中、不搜尋)、`查 ISRC`(切到 ISRC 頁並帶入該列的 ISRC;`search` 的表沒有 ISRC 欄時用 `/api/isrc` 查 provider:id 的等價路徑列為 T5 待定,先只在 `debug` 之外的路徑提供)。列動作常駐 muted 圖示鈕,不靠 hover 才出現。空白態 `> search <關鍵字>`。
- **播放清單頁**:資料來源是 `export`(唯讀、只讀本機 state.db,`pl__<pid>.json` 帶 `name` / `links` / `items`,`tracks.json` 給標題),不是 `pl list`(那是平台清單;平台清單在同一頁的第二個分段用 `pl list` / `pl show`)。左清單列(名稱 mono `--text-hi`、連結 chip `spotify ✓ apple ✓ local ·` 由 `links` 的鍵決定、曲數 tabular);右內容表 `ID 曲名 藝人 專輯 時長`,只有清單本來的順序。空白態 `> pl link --create`。
- **ISRC 頁**:label 在上 `ISRC`,mono 輸入;打 `/api/isrc/{isrc}`(決策 42)。標頭卡 `--surface` + 1px `--line`:封面 160px 正方(radius 0、object-fit cover、同尺寸 `--surface-2` 骨架、缺圖時 Phosphor music-note)、曲名 sans 20 `--text-hi`、藝人、專輯、發行日 mono。ISRC 解碼條 = 4 格 grid,每格寬用 `ch`(2ch / 3ch / 2ch / 5ch + padding,Consolas 退路不溢出),上 mono 24 accent 的分段、下 sans 12 muted 的標籤(`TW` 國碼(`Intl.DisplayNames` 給國名)/ `K23` 登記者 / `16 → 2016` 年份 / `80790` 流水號),頂一條 1px `--accent-dim`;資料到達前格子先空著。各平台 = 定義表:provider mono、id mono 完整不截 + copy 圖示鈕、arrow-square-out 外連(**封面出現時外連是必要的,不是選配**:ARCHITECTURE §8 的 cover art 只准用在播放脈絡或連回該平台的脈絡)、試聽 `<audio controls>`(有 `preview_url` 才出現)、popularity / genres mono muted;該平台 `error` 時那一格顯示錯誤原文(指路 `auth login`),不擋其他平台。canonical 區:cid mono、confidence 是 mono 數字 + 4px accent 條(資料不是裝飾)、source、pinned、alias set、conflicts 清單、含它的清單(名稱 + pos + links chip);`canonical: null` 時一行 muted「本機還沒有這首的 canonical 紀錄(先 capy pl pull)」;整區標「本機鏡像,以最近一次 pull 為準」。
- **帳號頁**:每平台一列 48px:名稱 mono、狀態 `✓ 已登入` accent / `· 未登入` muted / `⚠ 已過期` warn、到期 mono、動作鈕(資料來自 `auth status` 的文字,逐行解析三段)。Spotify / Google:`auth login <平台>` 打進主控台,`open_url` 事件渲染成可點連結(`window.open` 盡力)。Apple:`auth login apple` 打進主控台,揭露以 `confirm` 提示的 note 出現在區塊裡(`--warn` 左線、不可收合、預設鍵是「取消」),同意後 `form` 提示收 developer token / user token(`type=password`)。web 不自動擷取任何 token、不開瀏覽器抓 cookie、不注入 JS;`--auto` 在伺服器端 403。
- **診斷頁**:`重新檢查` 主要按鈕跑 `doctor`,輸出原樣 `<pre>`(✅ / ❌ / 🎉 是 CLI 自己印的,不解析、不換字符;純文字契約不為 web 動);進行中顯示三條靜態骨架 + 脈衝 ●;按鈕旁標「可能在這台電腦跳出系統對話框」。
- **按鈕**:mono 13、高 32、padding 0 12px、`--r`;主要 ink on accent,hover opacity .92;次要 `--text` on `--surface-2` + 1px `--line-hi`,hover 底瞬間換 `--line`;危險 = 次要框改 `--danger`;ghost 只有 accent 文字;active `translateY(1px)`;disabled opacity .45;任何狀態不 glow。
- **表單欄位**:label sans 12 `--text` 在上,4px 間距;輸入 mono 或 sans 14、`--surface` 底、1px `--line-hi`、高 32;錯誤 sans 12 `--danger` 在下 + 14px warning 圖示,框變 `--danger`;help 在下 muted;placeholder 是範例值(muted),絕不當 label。原生 `<select>` / range / checkbox / radio 用同一套 token 重畫、全直角。
- **badge**:mono 11、1px 框、padding 1px 6px、`--r`;provider 標籤 `--text` 字 + `--line-hi` 框,不按平台上色。
- **kbd 與 `?` 鍵位表**:kbd 同 guide(mono、accent 字、`--surface-2` 底、1px `--line`)但 `--r`;`?` 開原生 `<dialog>`,mono 兩欄,內容與 guide 的 `.keys` 清單一字不差,加 web 專屬三行(1 到 7 跳頁、反引號切 log、j / k 表格列)。
- **骨架**:靜態 `--surface-2` 條 12px 高(寬 40% / 70% / 55%)或照目標形狀(表格 5 列、ISRC 4 格、封面方塊),opacity .55 到 1 的 1.2s 脈衝;不 shimmer、不轉圈。
- **掃描線**:`body::after { position:fixed; inset:0; pointer-events:none; z-index:var(--z-scan); background: repeating-linear-gradient(0deg, rgb(201 209 217 / var(--scan)) 0 1px, transparent 1px 3px) }`,永不動畫。整個 cyberpunk 材質就這一條,不喜歡就把 `--scan` 設 0。
- **圖示**:Phosphor regular 子集約 24 顆做成一個 sprite:play、pause、skip-back、skip-forward、speaker-high、speaker-low、speaker-x、magnifying-glass、playlist、arrows-clockwise、barcode、user-circle、first-aid、terminal-window、copy、arrow-square-out、arrow-line-down、arrow-line-up、arrow-line-right、minus、caret-down、x、check、warning、music-note、keyboard;`<svg class="i"><use href="#i-play"/></svg>`,`fill: currentColor`,16px 在導覽 / 按鈕、14px 在列動作;資料格裡不放圖示。

## 9. 動效規則

三級,只動 opacity 與 transform;曲線一條 `--ease-out: cubic-bezier(.2, 0, 0, 1)`;時長 `--dur-1: 80ms`、`--dur-2: 120ms`、`--dur-3: 1.2s`。

- L1 回饋:glow 偽元素 opacity 0 到 1 `--dur-2`,離焦 `--dur-1` 反向;按鈕 hover opacity .92 `--dur-1`;active `translateY(1px)` 無 transition。顏色屬性(background / color / border-color)一律瞬間換,不進 transition。
- L2 狀態:● 脈衝 opacity .35 到 1 `--dur-3` ease-in-out infinite;dock 頂線 glow 跟著 `--dur-2` 進出;斜線選單 opacity 0 到 1 + translateY(4px 到 0) `--dur-1`;骨架 opacity .55 到 1 `--dur-3`;seek 拇指位置由原生 range 值驅動,不補間。
- L3 簽名:水豚 power-on 2s `steps(3)` + glow 層 400ms 退場,每 session 一次。
- 刻意不動:dock 高度切換、新區塊落地(`scroll-behavior: auto` 立刻捲到底)、表格列出現、頁面切換、退出碼落定(glow 瞬間變平面,那個「啪」就是回饋)、hover 底色、串流每一行、提示區出現。
- `@media (prefers-reduced-motion: reduce)`:脈衝與骨架改靜態 opacity .8、水豚直接最終幀、選單無 transition、`--dur-1` / `--dur-2` 歸 0;靜態 glow box-shadow 保留,它是狀態不是動效。

## 10. 狀態

- **loading**:骨架(靜態 `--surface-2` 條,照目標形狀)+ 區塊 ● 脈衝;封面同尺寸方形骨架;主控台不用骨架,輸出本身就是載入態;不轉圈。
- **empty**:主控台空白 = 水豚 + `capy · spotify`;各頁空白 = 一行 mono muted 可點命令(`> search <關鍵字>`、`> pl link --create`、`> auth login spotify`、ISRC 頁是輸入框本身),點了填進命令列;空白態本身就是下一步。
- **error**:exit 1 區塊左線 danger、✗ 前綴、stderr 原文;表單驗證錯誤在欄位下方 sans 12 danger;`/api/*` 的 4xx / 5xx(401 token 失效、409 忙碌、503 stale)在命令列上方一行 warn 文字,不是 toast;沒有 modal。exit 2 / 3 是 warn 左線 + `·`,不是失敗;stderr 那句原文就是說明,不翻譯成 UI 文案。
- **running**:dock 頂線 glow、● 脈衝、badge `running 00:12`、`中止` 按鈕;一次一個;命令列可打字、Enter 不排隊。
- **prompt pending**:提示區 1px accent 全框,焦點移進第一個控制項;超過 5 分鐘沒答(伺服器逾時,`prompt_closed reason:timeout`)區塊落定為 exit 事件說的樣子。
- **interrupted / cancelled**:`中止` 或 Ctrl-C(焦點在 log 或命令列)打 cancel 端點;exit 事件 reason 是 cancelled 時 badge 接「已取消」,已串出的輸出原樣保留,命令列預填同一條命令供重跑。
- **disconnected**:SSE 斷線或 `/api/now` 連續失敗時正在播放列整列換成 `--warn` 左線「capy --web 已停止」+ `r 重新連上`;命令列仍可打字,送出時提示先重連。
- **stale**(`/api/run` 503):命令列上方一行 warn「binary 已更新,這個 capy --web 仍是舊版,請重啟」,命令列 disabled;面板與 ISRC 頁照常。

## 11. 可及性

- 對比:文字全部 ≥ 4.5(§3 表),控制項邊界 `--line-hi` ≥ 3.06 三層皆過 1.4.11,主要按鈕 ink on accent 7.11。`--muted` 不進 `--surface-2`,`--accent-dim` 只當線。
- 焦點:`:focus-visible` 一律 2px accent outline offset 2px + glow,全站一條規則,含 range、radio、列動作、dialog。
- 鍵盤:完整鏡射 guide 的鍵位表(space、n / p、← → ±10 秒、+ - ±5、/、Tab、↑↓、?、r、q)加 pager 的 g / G / Ctrl-C;web 新增 1 到 7 跳頁、反引號切 log 高度、j / k 表格列;任何可編輯元素有焦點時單鍵全部失效,由一個集中的 `inInput()` 守門(activeElement 是 input / textarea / select / contenteditable / dialog 內),不是每個 handler 各判各的;Esc 回指令模式。
- 語意:log 面板 `role="region" aria-label="主控台"`(不用 role=log,它隱含 live 會被高吞吐淹掉);每個區塊的狀態行 `role="status"`(polite,只播命令回聲、提示標題與退出碼);串流 pre 不是 live region;進度用原生 range 的 `aria-valuetext`(mm:ss、音量 %);挑選器 `<fieldset>` + 隱藏 `<legend>`;`<dialog>` 原生焦點陷阱與 Esc;表格 `<th scope="col">` + 隱藏 `<caption>`。
- 不靠顏色單獨表意:DIR 圖示 + 字、ACTION 有字、退出碼有數字、帳號狀態有 ✓ / · / ⚠、remove 有圖示。觸控目標最小 32px(seek / 音量 range 元素本身 32px 高)。
- `color-scheme: dark`;`prefers-contrast: more` 一條規則:`--scan: 0`、`--line: var(--line-hi)`。
- 單一深色主題的理由:theme.go 只有一套、guide.html 已鎖定深色、頁面只在 127.0.0.1 給同一個終端機使用者;`--accent` 在白底 2.66 不過 AA,亮主題得換第二種綠、等於第二個品牌;而 glow 相位只在深底成立。token 全在 `:root`,將來要加亮主題是一個 selector 的事。

## 12. CSS 架構

`internal/cli/webui/`,`//go:embed webui`,`http.FileServerFS(fs.Sub(webFS, "webui"))`。零 Node、零打包、零 @import、零 CDN、零 utility class、零 inline style / script(CSP `style-src 'self'; script-src 'self'`)。

- `index.html`:單一 shell;`<html lang="zh-Hant">`;head:`<link rel="preload" as="font" crossorigin>`、`<link>` tokens.css、`<link>` app.css;body 開頭內嵌 Phosphor sprite `<svg hidden><symbol id="i-play" viewBox="0 0 256 256">…`(inline SVG 不是 inline style,CSP 允許);七頁各一個 `<template>`,hash router 換頁;`<script type="module" src="js/app.js">`。
- `css/tokens.css`:`:root` 只放 token,約 60 行;顏色沿用 guide.html 的名字(`--ink --surface --surface-2 --line --accent --accent-dim --text --muted --warn --mono --sans`),新增 `--line-hi --text-hi --danger --accent-rgb --glow --glow-text --scan`;間距 `--sp-1..8`;字級 `--t-11 --t-12 --t-13 --t-14 --t-20 --t-24`;`--r: 0`;`--dur-1 --dur-2 --dur-3 --ease-out`;`--z-rail: 10 --z-dock: 20 --z-menu: 30 --z-scan: 40`(原生 `<dialog>` 走 `showModal()` 的 top layer,在任何 z-index 之上);`color-scheme: dark`。guide.html 之後可以直接 `<link>` 同一個檔(Q36 之外的另一個純文件 PR),兩個網頁一套字彙。
- `css/app.css`:單檔,註解分段 `/* ── 段名 ── */`(reset / base / layout / rail / dock / console / prompts / tables / forms / pages / states / glow budget / reduced-motion / contrast),BEM-lite 類名 `.block .block__head`,狀態全走 data 屬性(`[data-exit="1"]`、`[data-running]`、`[data-playing]`、`[data-connected="false"]`、`[data-stale]`),JS 只切屬性、CSS 決定長相。超過 1500 行才拆第三個檔。
- `fonts/IosevkaFixed-Regular.woff2` + `fonts/OFL.txt`:離線切一次、產物 commit(計畫 Q37);一段 `@font-face` 是內嵌字型的全部成本。
- `icons.svg`:sprite 原檔,從 Phosphor regular 的 SVG 拼出,commit;shell 直接 inline。
- `js/app.js`(router、token(讀 fragment → sessionStorage → `history.replaceState`)、fetch 包裝帶 `X-Capy-Token`、SSE 客戶端(`fetch` + `ReadableStream` 切 `\n\n`,約 30 行)、`inInput()`)、`js/console.js`(區塊、提示渲染、答案 POST、取消)、`js/player.js`(面板輪詢、控制鈕)、`js/table.js`(table 事件 → 表格,時長轉換與 atomicHeader 規則各一份)、`js/pages/*.js`(七頁)。

## 13. 不做

- 不拖曳重排、不欄位排序、不給唯讀重排檢視(清單順序是使用者的記憶)。
- 不截斷任何原子欄(ID / CID / PID / ISRC / DEVICE / *_ID);沒有 ellipsis、沒有 `max-width` + title。
- 不解析 doctor 的 ✅ / ❌;不為 web 改任何 CLI 純文字輸出。
- 不做子行程 / PTY;不做 job registry / 事件重播(計畫 Q34)。
- 不加第二種綠、第三個 muted、按平台上色的標籤、任何新語意色。
- 不 transition 顏色屬性、不 transition box-shadow / filter / height / width。
- 不用 toast、不用通知 modal、不用轉圈 spinner、不用 shimmer。
- 不用純黑、純白、Inter、大寫追蹤標籤、章節編號 eyebrow、裝飾彩色圓點、手畫 SVG、假 UI 截圖、AI 紫漸層、每列上下畫線的表格、em-dash / en-dash 當分隔(CLI stderr 原文 passthrough 除外,web 逐字呈現)。
- 不做亮主題、不做 theme 切換。
- 不做虛擬捲動;log 100 個區塊上限,超過剪最早的並印一行「已裁掉較早的 N 個」。
- 不內嵌 CJK 字型、不內嵌第二支 mono 字重。
- 不代加 `--force`、不代加 `--yes`;寫入命令的確認就是橋接的 confirm 提示,由使用者在區塊裡按。
- 不動 guide.html(圓角 4 / 6 / 8、`#fff` 標題、muted 值另開純文件 PR 對齊 tokens.css)。

## 14. Pre-flight(逐條對照設計規則)

| # | 規則 | 過 | 證據 |
|---|---|---|---|
| 1 | 一個主色鎖死全頁(綠) | ✓ | §3 只有 `--accent`;不採 `--accent-hi`;warn / danger 只背退出碼與 remove |
| 2 | 霓虹有意圖:只在焦點 / 進行中 / 即時狀態 / 一個簽名時刻 | ✓ | §6 四處,glow budget 註解塊可數,第五處 = review finding |
| 3 | 不用純黑 #000000 | ✓ | 底 `--ink #0D1117`;掃描線 tint 是 `rgb(201 209 217 / .03)` |
| 4 | 不用純白 | ✓ | `--text-hi #E6EDF3` 取代 guide 的 `#fff` |
| 5 | 圓角只選一種、全頁一致 | ✓ | §5 `--r: 0` 唯一 token,全直角 |
| 6 | 表單 label 在上、錯誤在下、placeholder 不當 label | ✓ | §8 表單欄位;搜尋 / ISRC / Apple token 表單皆如此 |
| 7 | 每個互動狀態有 loading(骨架不轉圈)/ empty / error | ✓ | §10 八態;骨架靜態 `--surface-2` |
| 8 | 動效有理由;> 3 級尊重 prefers-reduced-motion | ✓ | §9 三級各有理由;motion 2 仍實作 reduced-motion |
| 9 | 只動 transform / opacity | ✓ | §9 顏色屬性瞬間換、box-shadow / filter / height 不進 transition |
| 10 | 禁 em-dash / en-dash 當分隔 | ✓ | UI 文案以 `、` `:` `;` `/` 分隔;CLI stderr 原文逐字呈現不在此限 |
| 11 | 禁裝飾性彩色圓點 | ✓ | 帳號狀態用 ✓ / · / ⚠ 字符;唯一的圓點是進行中的 ● 狀態字(綁 `[data-running]`、落定即消失),是狀態不是裝飾 |
| 12 | 禁章節編號 eyebrow、大寫追蹤標籤 | ✓ | §4 字距 0、無大寫標籤;表頭用 CLI 原字串 |
| 13 | 禁假 UI 截圖、手畫裝飾 SVG | ✓ | 圖示全部 Phosphor sprite;ISRC 解碼條純 HTML / CSS;沒有插圖 |
| 14 | 不用 Inter 當預設;sans 指名系統堆疊順序 | ✓ | §4 指名 `system-ui → Segoe UI → PingFang TC → JhengHei`,個性由內嵌 mono 承載,理由:zh-Hant 無法內嵌 CJK |
| 15 | 禁 AI 紫色漸層 | ✓ | 無任何漸層(掃描線是單色細線材質) |
| 16 | 禁每列上下都畫線的長表格 | ✓ | §8 表格不畫列線,斑馬 + 左線 |
| 17 | mono 用在數字 / 代碼 / TSV,介面字用 sans | ✓ | §4 分工;sans 出現數字處 tabular-nums |
| 18 | 顆粒 / 掃描線只在 fixed、pointer-events:none 的偽元素 | ✓ | §8 `body::after`,永不動畫,`--scan` 可歸零 |
| 19 | 深色鎖定要說明理由 | ✓ | §11 |
| 20 | 文字對比 AA、控制項邊界 3:1(1.4.11) | ✓ | §3 實算;`--line-hi` 3.06 起;`--muted` 不進 surface-2 |
| 21 | 品牌值:Accent #3FB27F、Text #C9D1D9、`>` 提示符、水豚 accent、✗ / · 退出碼字、✓ / • 挑選器前綴 | ✓ | §3 / §7 / §8 逐一照 theme.go、tui.go、HuhStyles;Muted 採 guide 值是唯一有文件的漂移 |
| 22 | 原子欄永不截斷(ui.go:182 atomicHeader;README「ID 欄永遠完整」) | ✓ | §8 表格 |
| 23 | 清單順序是使用者的記憶:web 不拖曳、不排序 | ✓ | §8 表格、§13 |
| 24 | 非 TTY 純文字契約不受影響 | ✓ | 行程內 writer 非 `*os.File` 自動非 TTY(決策 40);doctor 原樣 pre、不為 web 改字符;表頭來自 `table` 事件 |
| 25 | Go binary 內嵌、vanilla HTML / CSS / ES modules、無 Node / React / Tailwind | ✓ | §12 |
| 26 | 一個 woff2 或系統堆疊,附理由與大小 | ✓ | §4 一支 Iosevka Fixed Regular 約 35 KB(0.23%),退路是系統 mono |
| 27 | Phosphor SVG 子集 sprite、不手畫 | ✓ | §8 圖示清單、§12 icons.svg |
| 28 | 所有 CLI 命令可從主控台執行,含挑選器 / 確認 / 逐筆裁決 | ✓ | §8 提示 = 決策 40 橋的四種 kind 渲染;例外清單在計畫 §1 決策 40 第 5 點 |
| 29 | ISRC 頁有入口與輸入欄,呈現封面 / 藝人 / 專輯 / 發行日 / 各平台 id 與連結 / canonical / 結構解碼 | ✓ | §5 第七導覽項 + §8 ISRC 頁;資料來自 `/api/isrc`(決策 42) |
| 30 | Apple 憑證路徑只指導、不自動擷取、揭露不可跳過 | ✓ | §8 帳號頁:揭露是伺服器判定的 confirm note、不可收合;token 走 `form` 提示的密碼欄,不進 argv / 回聲 / log;`--auto` 403 |
| 31 | 會刪曲目的路徑先過 dry-run 與閾值 | ✓ | 閾值 `removalBlocked` 在 confirm 之前就 exit 3;頁面不代加 `--force` / `--yes` |
| 32 | 本機伺服器不等於安全 | ✓ | 決策 41:Host / Origin / Sec-Fetch-Site 檢查 + 一次性 token |

## 15. 實作前要實測的兩件事

- Windows 1x 螢幕上 13px Iosevka Fixed 400 的粗細與 3px pitch 掃描線的 moiré;不過就退回系統 mono(刪一段 `@font-face`)或 `--scan: 0`。
- CSP `img-src` / `media-src` 的 CDN host 拿真回應驗證(T4)。
