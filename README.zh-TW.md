[English](README.md) | **繁體中文**

# capy-music

跨平台音樂 CLI:搜尋、播放遙控、播放清單同步(Spotify、Apple Music;播放清單同步到你自己的 Google Drive)。開源、免費,**所有憑證都是你自己的(BYO)** —— 本專案不代持任何 token、不架任何服務。

網站:<https://capy.taislife.work>(給一般使用者的介紹、[隱私權政策](https://capy.taislife.work/privacy)、[服務條款](https://capy.taislife.work/terms);原始檔在 [`site/`](site/);`/guide` 與 `/en/guide` 兩頁分別由 `docs/guide.html`、`docs/guide.en.html` 轉出來,改了任一份指南之後跑 `go test ./site/ -run TestGuideOnSiteIsCurrent -update`;repo 接了 Cloudflare Workers Builds,合併進 main 之後確認線上有更新,沒有的話手動 `cd site && wrangler deploy`)。

完整的使用說明(含互動式介面、網頁介面、命令參考與三個平台的能力差異)在 <https://capy.taislife.work/guide>(英文版:<https://capy.taislife.work/en/guide>)。原始檔是 `docs/guide.html`(英文版 `docs/guide.en.html`):都是單一檔案、不需要伺服器、不連任何外部資源,clone 下來離線也打得開(GitHub 對 repo 裡的 `.html` 只顯示原始碼)。

## 安裝

### A. 從 GitHub Releases 下載(macOS / Windows;Google 登入零設定)

到 [Releases](https://github.com/Tai-ch0802/capy-music/releases) 下載對應的檔,解開後把 `capy`(Windows 是 `capy.exe`)放進 PATH:

| 平台 | 檔名 |
|---|---|
| macOS Apple Silicon | `capy_<版本>_darwin_arm64.tar.gz` |
| macOS Intel | `capy_<版本>_darwin_amd64.tar.gz` |
| Windows x64 | `capy_<版本>_windows_amd64.zip` |
| Windows ARM64 | `capy_<版本>_windows_arm64.zip` |

- **macOS**:binary 沒有 Apple 簽章。用瀏覽器下載的檔會被 Gatekeeper 擋(「無法打開,因為無法驗證開發者」),解開後跑一次 `xattr -d com.apple.quarantine capy` 即可;用 `curl -L -O <網址>` 下載的檔不會被標記。
- **Windows**:SmartScreen 第一次會警告「未知的發行者」,「其他資訊 → 仍要執行」一次即可。
- `checksums.txt` 是每個檔的 SHA-256:對得上代表下載完整,**不代表**來源經過簽章(本專案沒有簽章)。

Releases 的 binary 內建專案自己的 Google OAuth client:`capy auth login google` 直接開瀏覽器授權,不用建任何東西。

### B. `go install`(需要 Go;Google 登入走 BYO)

```bash
go install github.com/Tai-ch0802/capy-music/cmd/capy@latest
```

從原始碼建的 binary(`go install`、自己 `go build`、`capy update --dev`)**沒有內建 Google client**:`capy auth login google` 會用精靈引導你建自己的 OAuth client(見下方 Google 一節)。Homebrew / Scoop / winget 不在計畫內。

### 更新

- `capy update`:問 GitHub Releases 最新正式版,下載本平台的檔、用 `checksums.txt` 做 SHA-256 校驗、跑一次新 binary 的 `--version` 確認能動,才覆蓋目前這顆;任何一步失敗,舊的原封不動。從 dev 版執行會換成正式版(之後 Google 登入就有內建 client)。
- `capy update --dev`:抓 main 分支最新 commit、用 `go install` 重建並覆蓋(需要 Go toolchain,第一次約 20 秒)。

更新前先跑一次 `capy pl pull --dry-run`:exit 3(Drive 不完整)就先別更新。本機快取 `state.db` 的 schema 升版時舊檔會保留為 `state.db.v<舊版>`,但新版 capy 不讀它;而 `pl pull` 擋下「Drive 不完整」時給的出口 `capy drive init --from-local` 讀的是新的空快取——兩邊都沒資料。真的撞上了:從 Releases 拿上一版 binary,把 `state.db.v<舊版>` 改回 `state.db`,用舊版跑 `capy drive init --from-local` 把 Drive 補齊(新版讀得懂舊 schema 的 Drive 檔),再更新回來。不確定就先 `capy export > backup.json`。

## 介面語言:預設英文,切成繁體中文

capy 的預設語言是英文(之前的版本預設中文,升級上來會直接變英文)。切成繁體中文:

```bash
capy config set language zh-TW    # 切回英文:capy config set language en
```

網頁介面(`capy --web`)側欄最下面的語言選單改的是同一個設定(它就是替你跑這條命令,成功後整頁重新載入)。設定存在 `config.json` 的 `language`,目前支援 `en` 與 `zh-TW`;不會自動偵測作業系統的語言。cobra 自己的文字在每個語系都是英文:說明的標題(`Usage:`、`Available Commands:`、`Flags:`)、結尾那行 `Use "capy [command] --help" …`、flag 說明後面的 `(default …)`、內建的 `help` 與 `completion` 命令的說明,`-h, --help`、`-v, --version` 的說明,以及 cobra 自己的參數與 flag 錯誤(`unknown command …`、`unknown flag: …`、`accepts 1 arg(s), received 0`);命令名、flag、TSV 欄名、`action` / `dir` 的值、`reason_code`、exit code、表頭與 `auth status --json` 的值在每個語系都一樣,腳本不必跟著改。想新增一個語言,步驟在 [`internal/i18n/README.md`](internal/i18n/README.md)。

## Spotify:自建 app(免費,約 2 分鐘)

Spotify 的開發者政策限制每個 app 只能有 5 位使用者,所以要用你自己的 app:

1. 開 https://developer.spotify.com/dashboard → Create app(名稱隨意)
2. Redirect URI 填入(完全照抄,**不可用 localhost**):`http://127.0.0.1:8888/callback`
3. 勾選 Web API → Save → 複製 Client ID
4. 執行 `capy auth login spotify`(精靈會引導;非互動環境用 `--client-id`)

需要 Spotify Premium(遙控播放與 Development Mode 皆要求)。

## Apple Music:複製你自己的 web token

> ⚠️ **非 Apple 官方支援。** 這組 token 屬於 Apple 網頁播放器,Apple 可能隨時更換(屆時重跑一次 `capy auth login apple` 即可)。以第三方工具存取 Apple Music 的服務條款風險由你自行承擔;本工具只指導,**不會自動擷取**你的瀏覽器資料。

1. 瀏覽器開 https://music.apple.com 並登入
2. 開 DevTools → Network,篩選 `amp-api`,點任一請求 → Request Headers
3. 複製 `authorization`(`Bearer eyJ…`)與 `media-user-token` 兩個值
4. 執行 `capy auth login apple`,依精靈貼上(非互動環境用 `CAPY_APPLE_DEVELOPER_TOKEN` / `CAPY_APPLE_USER_TOKEN` 環境變數並加 `--i-understand`)

需要 Apple Music 訂閱。播放遙控只在 macOS(透過 Music.app),而且 capy 只能替你播資料庫裡有的歌:Music.app 的腳本介面播不了其他的歌。capy 會在你電腦上的 Music 資料庫裡找那一首(只讀不寫),剛好對到一首就直接播,並確認真的開始播了。其他的歌,`capy play`(以及 `capy --web` 搜尋頁的「**在 Music.app 開啟**」按鈕)會在 Music.app 打開那一首並標出來,請你在那裡對它點兩下;這時命令一樣回 exit 0,但不印 ▶。搜尋與播放清單在 macOS / Windows 皆可用。寫入播放清單(建清單、加歌、移除、換序、改名)只對你自己建的清單;加進清單的曲目會不會同時加進你的 Apple Music 資料庫,看你在 Apple Music 裡的設定——這是 Apple 的行為,capy 不另外加;2026-09-23 實測的帳號,清單裡本來就有很多歌不在資料庫,capy 加進清單的歌也沒有進資料庫。

## 本機曲庫(local,選用):M3U 清單 + library.json

```bash
capy config set local_root ~/Music/capy      # 裡面放 *.m3u8(或 .m3u)清單與 library.json
capy pl list --provider local
capy pl link 通勤 local:通勤.m3u8            # 只打檔名;連結 id 會帶這台裝置的 id
```

`library.json` 由你自己維護(capy 不讀音訊 tag):`{"schema_version": 1, "tracks": {"相對路徑": {"title", "artists": [], "album", "duration_ms", "isrc"}}}`;清單裡每一行是相對於清單檔的路徑,`#EXTINF` 只在曲庫沒有那首時當備援標題。有 `isrc` 的曲目才會跟 Spotify / Apple 自動對上,沒有的靠 `capy resolve` 的模糊比對(local 這邊的搜尋是曲庫內比對,不用網路)。

**本機曲庫綁裝置**:連結記的是「這台裝置的這個檔」,別台裝置的 `pl pull / push / sync` 會跳過它(stderr 會說是哪台的),不會把連結刪掉;一個清單同時只能連一台裝置的 M3U——想換一台主導(或重灌之後)就在那台重跑 `capy pl link`,它會接管並說明原本是哪台。`pl push` / `pl sync` 寫回 M3U 是**整檔重寫**(`#EXTM3U` + 每行一個路徑):別的工具寫在裡面的 `#EXTINF` 與註解會被丟掉;清單改名不會寫回檔名(檔名就是它的 id,改了等於另一個清單——要改名請自己改檔名再 `pl link`)。

## Google Drive 同步(選用):登入 Google

`capy auth login google` 之後,播放清單會同步到你 Google Drive 的應用程式資料夾(其他 app 與你自己都看不到內容,只佔你的 Drive 空間)。只索取三個權限:`openid`、`userinfo.email`、`drive.appdata`。每個權限用來做什麼、資料存在哪裡、怎麼撤銷與刪除,寫在[隱私權政策](https://capy.taislife.work/privacy)——也就是 Google 授權畫面上連過去的那一頁。

- **從 GitHub Releases 下載的 binary**:內建專案自己的 Google client,直接執行 `capy auth login google` 就好。
- **`go install` 或自己 build 的**:沒有內建 client,`auth login google` 的精靈會引導你建自己的(免費,約 5 分鐘):
  1. https://console.cloud.google.com → 建立專案 → API 和服務 → 啟用「Google Drive API」
  2. Google Auth platform → Branding:填 app 名稱與 support email;Audience 選 External
  3. Data Access:只加這三個 scope —— `openid`、`userinfo.email`、`drive.appdata`(多加 Gmail 之類會觸發資安評估)
  4. 建立 OAuth client:類型選「桌面應用程式(Desktop app)」;secret 只在建立當下顯示一次,立刻複製
  5. ⚠️ Audience 按「Publish app」切到 In production —— 停在 Testing 的話 refresh token 7 天就過期,你會莫名被登出
  6. 把 Client ID 與 Client secret 貼進精靈。非互動環境用 `--client-id` / `--client-secret` 或 `CAPY_GOOGLE_CLIENT_ID` / `CAPY_GOOGLE_CLIENT_SECRET`。

不管哪一種,`--client-id` / `--client-secret` 永遠可以覆寫內建值。自建 client 的 secret 只進 OS keychain。`capy auth status` 會顯示登入的 Google 帳號 email 與這台裝置的 `device_id`;`capy auth logout google` 刪 token 與自建 client 的 secret。

## 登入狀態給腳本讀:`capy auth status --json`

`capy auth status` 是給人看的(跟著語系);腳本請用 `--json`。欄位只增不改,列舉值是固定的英文、**永不翻譯**;時間是 UTC 的 RFC 3339;沒有值的欄位不印。**輸出絕不含任何 token 或 secret 的值**,client ID 也只給來源(`internal/cli/auth_status_json_test.go` 把每一種憑證種成哨兵值、斷言它們不出現)。

```json
{
  "spotify": { "state": "ok", "client_id": "set" },
  "google": { "state": "ok", "client": "builtin", "access_token_expiry": "2026-09-23T09:00:00Z", "email": "you@example.com", "device_id": "…" },
  "apple": { "state": "ok", "developer_token": "ok", "developer_token_expiry": "2026-11-01T00:00:00Z", "user_token": "ok", "storefront": "tw" }
}
```

| 欄位 | 值 |
|---|---|
| `spotify.state` | `ok` 已登入 / `missing` 沒登入 / `keychain_error` 讀不到 keychain(要處理,不是沒登入) |
| `spotify.client_id` | `set` / `missing` / `malformed`(config 裡的不是 32 碼十六進位) |
| `google.state` | `ok` / `missing` / `keychain_error` |
| `google.client` | `config` 自建 client / `builtin` release 內建 / `none` 還沒有 |
| `google.access_token_expiry` | 存著的 access token 何時到期;capy 會自己換發,**不是登入的期限** |
| `google.email`、`google.device_id` | 登入的 Google 帳號、這台裝置的 id |
| `apple.state` | 兩個 token 合起來:`keychain_error` > `expired` > `missing` > `ok`(依序取第一個成立的) |
| `apple.developer_token` | `ok` / `missing` / `expired` / `keychain_error`;`developer_token_expiry` 在 `ok` 與 `expired` 時給 |
| `apple.user_token` | `ok` / `missing` / `keychain_error` |
| `apple.storefront` | 例如 `tw` |

## 常用命令

```
capy search 派對動物 [--provider apple]
capy play 派對動物                      # 統一搜尋:曲目、藝人熱門歌曲、我的播放清單;歧義時開挑選器
capy play 五月天 / capy play 通勤        # 藝人 = 播熱門歌曲(Spotify 開發模式 app 拿不到 top-tracks,退回依熱門度排序的搜尋);清單名完全相符 = 播清單(先 capy pl list 一次)
capy play --type track 派對動物          # 腳本用:確定性,永遠播第一筆;前綴 artist: / pl: / track: 同義
capy play --pick                        # 直接開挑選器(本機快取的清單與最近項目)
capy                    # 直接打 capy(在終端機裡)= 互動式介面;pipe / cron 下仍是印 help
capy --web              # 在瀏覽器操作:只綁 127.0.0.1、啟動時印一次性網址;所有命令都能在頁面上跑
capy pause / next / prev / now / devices
capy seek 1:23          # 跳到曲目內的位置;也吃 h:mm:ss(1:05:30)與純秒數(83)
capy vol 40             # 音量 0-100
capy pl list / capy pl show <名稱|ID>
capy pl link 通勤 spotify:<清單 ID 或名稱>   # 把 canonical 清單(不存在就建立)連結到平台清單;只認明確 link,不自動配名
capy pl link 通勤 spotify --create          # 在 Spotify / Apple Music 建一個跟 canonical 同名的私人空清單再連上(local 不行);複製清單見下方
capy pl unlink 通勤 spotify
capy pl show / link / unlink / pull / push / sync / dedup   # 不帶清單名且在終端機裡 = 開挑選器(link 三段,第二段也能選在 Spotify / Apple Music 建新的空清單;unlink 兩段);pipe / cron 維持原本的參數錯誤
capy pl pull 通勤 [--dry-run] [--yes] [--force] / capy pl pull --all [--provider spotify]   # 平台 → canonical → Drive;變更先列出、確認後才寫(需先 capy auth login google)
capy pl push 通勤 [--dry-run] [--yes] [--force] / capy pl push --all [--provider spotify]   # canonical → 平台(Spotify、Apple Music、本機曲庫);要先 pull 過、平台沒有未 pull 的變更
capy pl sync 通勤 [--dry-run] [--yes] [--force] / capy pl sync --all [--provider spotify]   # 先 pull 再 push 的一輪:一張表、一次確認;cron 放這個
capy pl dedup 通勤 [--dry-run] [--yes] [--force]   # 去掉正本裡重複的曲目(同平台 id 或同 ISRC;保留第一份、順序不動),再推到可寫的平台;沒有重複就零寫入
capy pl dedup apple:冬日暖調                     # 直接讀平台清單、只報告哪幾首重複(不碰 Drive、不需要連結);要由 capy 去掉就用上一行的寫法
capy migrate 公路旅行 --from spotify --to apple[:既有清單] [--dry-run] [--yes]   # 把清單搬到另一個平台(兩個方向都行;新建或加進既有;順序不動、只新增、不動來源);見下方「跨平台複製清單」
capy resolve [通勤] [--provider apple] [--dry-run] [--yes]   # 把曲目對應到各平台 id:ISRC 反查 → 模糊比對;≥85 自動寫入、其餘列成 review 佇列
capy resolve --review                     # 終端機逐筆裁決佇列(接受 / 略過 / 手動搜尋 / 釘成不可得 / 釘住現有)
capy resolve pin <cid> apple:<id|none> [--yes]   # 腳本用釘選;none = 這個平台沒有這首;id 已屬另一 cid 時合併(非 TTY 要 --yes)
capy export > backup.json                 # 逃生口:本機 canonical 資料(Drive 檔的合併形式),不依賴 Drive
capy drive init --from-local [--dry-run] [--yes]   # 逃生口:Drive 空 / 部分遺失時用本機 state.db 補回缺的檔
capy doctor [--provider apple]
capy config set default_provider apple   # 之後不必每次帶 --provider;config get / list
capy config set language zh-TW           # 介面語言(預設 en);見上方「介面語言」
capy update [--dev]                      # 見上方「更新」
```

### 互動式介面

在終端機直接執行 `capy`(不帶任何參數)會開互動式介面。終端機夠大(至少 30 行高)時水豚常駐在畫面底部、一直動(偶爾眨眼、撥耳朵,隔一陣子吃一根牧草;打 `/` 開選單時牠先讓位);終端機比較小的話,牠開場動個三秒左右(按任何鍵可以跳過)就定格印進捲動區。不想要任何動畫(螢幕閱讀器、SSH 連線很慢、要錄畫面)就設 `CAPY_MOTION=never`:水豚直接定格,之後一格都不動。
除了水豚,TUI 只管**底部四行**:分隔線、現在在播什麼、輸入行、按鍵提示。

```
-------------------------------------------------------------------------------
  ▶ 派對動物 · 1:23 / 4:09 · MacBook Pro · 音量 60
> pl sync 冬日暖調
  space 播放/暫停 · ←→ ±10 秒 · / 命令 · ? 按鍵 · q 離開
```

命令的回音、錯誤與非零結束碼都推進**捲動區**,永久留著、可以往回捲、可以複製——上面那一大片
全部是終端機自己的捲動區,TUI 不會重繪它。按 `?` 把完整鍵位也印進去。

按 `/` 開命令選單:打字即時過濾,`↑↓` 選,`Tab` 或 `⏎` 把命令**帶進輸入行**讓你補參數,`Esc` 收起。
打到完整命令(例如 `/pl show 冬日暖調`)選單就收起,`⏎` 直接執行——開頭的 `/` 會自動拿掉。
選單向上長、壓在捲動區前面,不插進歷程。清單直接來自 cobra 的命令樹,不會跟實作走鏢。
`↑↓` 在選單沒開時翻這次 session 打過的命令(只在記憶體裡,離開就沒了)。

輸入的命令**不是在同一個行程裡跑**,而是重新執行 `capy` 自己——所以每個子命令完整保有它原本的行為
(表格、挑選器、確認提示)。清單名有空白時用雙引號:`pl show "上班 通勤"`。

還沒登入也可以開:狀態區會說明原因,你可以直接在那行輸入 `auth login spotify`。

`capy --provider apple` 開的介面會把 `--provider` 一起帶給你在那行輸入的子命令(只對吃這個參數的命令加),
所以整個畫面是同一個平台。

顏色是冷冽的 geek 綠(`internal/ui/theme.go` 的 `GeekGreen`);換色的接縫就是那個 `Theme` struct,
之後開放切換時從那裡加。


所有命令在非 TTY(pipe / cron)下輸出純文字 TSV,可直接 `cut -f`;`play` 在非 TTY 遇到歧義會以 exit 2 結束並印出候選(`type\tid\tlabel\tdetail`),不會播、也不會問——腳本請用 `--type` 或前綴。終端機下表格依顯示寬度對齊,放不下時儲存格**換行、不截斷**(ID 欄永遠完整,曲名最後才縮);比終端機還寬時會先開一個**檢視窗格**(每列一行,`←→` 橫向、`↑↓` 上下、`g`/`G` 頭尾、`q` 離開;`Ctrl-C` 是中止,命令以 exit 130 結束、不再往下問),離開後表格以換行的形式留在捲動區。帶 `--yes` 的命令不開窗格;`CAPY_PAGER=never` 一律不開(script / expect、CI 給了 pty 那種「有 TTY 但沒有人」的情況)。設定目錄可用 `CAPY_CONFIG_DIR` 覆寫。

被 SIGINT / SIGTERM 結束的命令以 exit `130` / `143` 結束(shell 慣例 128+n),`capy now --watch; echo $?` 分得出「被砍」跟「做完」:互動式介面、`now --watch` 按 `q` / `Esc` 離開是 `0`,按 Ctrl-C 離開是 `130`(在那兩個畫面裡 Ctrl-C 是按鍵不是訊號,但結束碼跟真的 SIGINT、跟檢視窗格一致),被 `kill` 是 `143`;`capy --web` 本來就是用 Ctrl-C 結束的,所以它正常收掉是 130(launchd / `kill` 是 143);其餘命令中途被砍時 stderr 的訊息照舊、只是結束碼從 1 變成 130 / 143。這是 capy 自己以 130 / 143 結束(`$?` 同被訊號殺掉,但對 `waitpid` 來說是正常結束);用 launchd 之類的 supervisor 跑 `capy --web` 時,它收掉的結束碼不是 0,別把「非 0 就重啟」開在它身上。命令自己已經有話要說的不被蓋掉——下面的 exit `2` / `3`,以及「平台寫到一半」那種 exit `1`(訊息會說已寫幾首)。

`capy pl pull` 的 exit code 是對外契約(cron 靠它):`0` 無變更或已成功套用、`1` 錯誤、`2` 有待套用的變更(`--dry-run`、非 TTY 沒給 `--yes`、在終端機取消)、`3` 安全閥擋下(Drive appdata 不完整;或單一清單要刪 >10 首、或 >30% 且 >3 首)。`--yes` 只跳過確認、`--force` 只越過刪除閾值且只能配單一清單(`capy pl pull <名稱> --force`,不能配 `--all`:安全閥一次只解除一個清單),兩者都不放行「Drive 不完整」——那條的出口是 `capy drive init --from-local`。變更集在非 TTY 下是無標題 TSV:`action provider playlist pos cid provider_id title artists reason reason_code`(`reason` 給人看、跟著語系;腳本請看 `reason_code`,它是固定的代碼,例如 `added_on_platform`、`removed_on_platform`,完整的表見[下方](#reason_code-代碼表));「這次動了幾筆」看行數,不佔 exit code。寫入順序固定 Drive 先、本機 `state.db` 後;`state.db` 只是快取,刪掉後下一次 pull 會從 Drive 重建。

`capy pl push` 是反方向(canonical → 平台),形狀與 exit code 同 `pl pull`,多兩個 `--yes` / `--force` 都不放行的前提:這台裝置對那個平台清單 pull 過(不然會把平台清單刪光),而且平台上沒有還沒 pull 的變更(不然會蓋掉你剛在平台改的)——先 `capy pl pull`。變更集的 `action` 多了 `skip`(canonical 有、平台沒有、又沒有這個平台的 id:先 `capy resolve`),它不是變更,數行數時要扣掉。寫入 Spotify 是整批取代(前 100 首一次、其後每批 100),所以平台端的「加入時間」會重設;含 local file 的 Spotify 清單暫不支援 push(local file 加不回去)。寫到一半失敗會以 exit 1 結束並講明已寫幾首,重跑一次補回其餘;確認之後寫入之前平台又變了(手機同時在加歌)那份不寫、exit 3。寫入 Apple Music:只加歌走 Apple 文件化的新增端點(每批 100、接在尾端);有移除或換序時是整批取代(網頁播放器自己用的端點,Apple 沒有正式承諾);改名只改名字、描述保留。只有你自己建的清單能寫——Apple 精選、喜好歌曲、已購買的音樂會擋下、零寫入;**協作播放清單**目前 capy 完全不寫(Apple 對它的整批取代回 500):push / sync 在列變更時就跳過那一格並說明,明說 `--provider apple` 要推它是 exit 3;請在 app 裡手動,或先複製成一般清單再連結(未實測,通常可以);加進清單的曲目會不會同時加進你的 Apple Music 資料庫,看你的 Apple Music 設定。移除與換序同樣是整批取代:清單順序一定照正本;Mac 的 Music app 能顯示每首歌加進清單的「加入日期」,它會不會因此被重設還沒驗證(Spotify 的加入時間會,見上)。Apple 商店裡已經下架的歌,加歌請求照樣成功但實際上不會加入;capy 寫完會重讀,內容和預期不同時會警告。

`capy pl sync` 是同一把鎖裡「每個清單先 pull 各平台、再 push 各平台」的一輪(provider 依字典序),一張表、一次確認,exit code 同上;push 的兩個前提由「先 pull 後 push」自動滿足,push 半邊直接用 pull 半邊剛讀到的平台清單、不再讀一次。TSV 比 pull / push 多一欄在最前面:`dir`(`pull` / `push`)。`--dry-run` 的 push 半邊是用 pull 套用後的 canonical 算的,所以看得到完整一輪;`--provider spotify` 只走一個平台;指到寫不了的平台(例如別台裝置的本機清單)時只做 pull 半邊(stderr 會說)。刪除閾值對每個 (清單, 平台) 各算,任一個擋下整輪就零寫入(`--force` 放行的話,pull 吸收進來的刪除會在同一個指令裡推到這個清單連結的每一個平台——先跑 `--dry-run`);但某個清單的某個平台推不了(例如含 local file)只會跳過那一格的 push 半邊(stderr 會說),其餘照常——cron 放 `capy pl sync --all --yes` 不會被一個清單綁死,exit 2 / 3 時再到終端機看。

`capy pl dedup` 去掉清單裡重複的曲目。重複 = 同平台 id、或同 ISRC(單曲版 / 專輯版算同一首);保留第一次出現的那份、拿掉後面的,**剩下的相對順序一個都不動——清單順序是你加歌的記憶,capy 沒有任何路徑會排序或打亂它**(去重只拿掉後出現的份;同步只在平台自己重排時才跟著動)。`capy pl dedup apple:冬日暖調` 這種寫法直接讀平台清單、只印報告(非 TTY 是 TSV:`pos id title artists reason reason_code`,`pos` 從 0 起、是重複的那份(該拿掉的)的位置,保留的那份寫在 `reason` 裡,`reason_code` 是 `dup_id` 或 `dup_isrc`;有沒有重複 exit code 都是 0),不碰 Drive、不需要連結——要由 capy 去掉,就把清單連到正本再用下一種寫法;這種寫法配 `--yes` / `--force` / `--dry-run` / `--provider` 是錯誤(它們是 canonical 那條路的 flag)。給 canonical 清單名(`capy pl dedup 通勤`)則是 `pl sync` 的一輪中間多一步:先 pull、正本去重、再 push 把多出來的份從可寫的平台拿掉;一張表(`dir` 多一種 `dedup`,那些列的 `pos` 是正本裡的位置)、一次確認,exit code 同 `pl sync`;正本與這次檢查的平台都沒有重複時零寫入(pull 半邊看到的其他變更留給 `pl sync`,stderr 會說;`--provider` 沒選到或讀不到的平台這次沒檢查,stderr 也會說,不會被算成「沒重複」)。刪除閾值去重與 push 各算,`--force` 越過(去掉的份會在同一個指令裡推到清單連結的每個可寫平台)。寫不了的平台上還留著的份會列在 stderr 請你手動刪,下一次 pull 不會把它們加回正本。同 ISRC 不同 id 時正本記第一份,平台上留下哪個 id 由配對決定(相鄰兩份時留後面那個)。

`capy resolve` 補 `pl pull` 不做的事:清單連結了兩個平台、曲目只從其中一邊 pull 進來時,另一邊的 id 由它找——先用 ISRC 反查(信心 95),沒有再用標題 + 藝人 + 時長模糊比對(0–100;標題一邊有 live / remix / acoustic / cover 之類、或時長差 >3 秒,上限 84)。≥85 自動寫入,走 `pl pull` 同一套鎖、閘與寫入順序;其餘印成 review 佇列——候選已屬另一首的一律進佇列,**合併只由人決定**。exit code:`0` 無事可寫或已寫入(佇列有東西仍是 0,cron 放 `capy resolve --yes` 不會因為永遠有幾首解不開而報錯)、`1` 錯誤、`2` 有可自動寫入的 mapping 但沒確認(`--dry-run`、非 TTY 沒 `--yes`、取消)。非 TTY 的 TSV:`action cid provider provider_id confidence source title artists reason reason_code`(`action` ∈ `map` 待寫入 / `review` 要人裁決 / `conflict` 同 ISRC 觀測到不同 id)。`--review` 在終端機逐筆裁決,決定寫成釘選(之後自動程序不再改);非 TTY 只印佇列並以 exit 2 結束——腳本用 `capy resolve pin`。單次 resolve 打超過 200 次 API 會在 stderr 提醒(未解開的曲目每次都會重查,目前沒有 negative cache)。某個平台授權失效時只跳過那個平台(stderr 會說),別的平台照解;單次查詢失敗的那首列成 `review` 並在 reason 寫明,下次再查。`pl pull` 結尾會提示「N 首尚未對應到 <provider>」。

兩個逃生口:`capy export` 只讀本機 `state.db`(不碰 Drive、網路、keychain),把 Drive 檔的合併形式輸出到 stdout——鍵是檔名(`manifest.json`、`tracks.json`、`pl__<pid>.json`、`dev__<device_id>.json`)、值是該檔內容的縮排形式(壓回 compact 後與 Drive 上逐位元相同);本機沒資料時 exit 1 且不印東西。它用唯讀方式開 `state.db`:壞檔不刪、版本不符不改名、全新機器不建檔,而且整份匯出是一個一致的快照(與 cron 的 `pl pull` 同時跑也不會撕裂)。`capy drive init --from-local` 是 `pl pull` 以 exit 3 擋下「Drive 不完整」之後的出口:只建 Drive 缺的檔、不覆寫還在的檔、不動本機快取,別台裝置的 `dev__` 檔不代為上傳;先列出要建的檔(非 TTY 是 TSV `action file`),`--yes` 或在終端機確認後才上傳,`--dry-run` 只列不傳。確認訊息會帶目前登入的 Google 帳號:登錯帳號會把整個曲庫傳到別人的 appdata。

### reason_code 代碼表

`pl pull` / `push` / `sync` / `migrate` / `dedup` / `resolve` 的 TSV 最後一欄都是 `reason_code`:固定的英文代碼,**永不翻譯、只增不改**;前一欄 `reason` 是同一件事給人看的說法,跟著語系。腳本判斷原因請看這一欄。同一個代碼可以出現在不同命令(例如 `push`)。`pl sync` / `migrate` / `pl dedup` 的 TSV 最前面多一欄 `dir`,每一列照它的 `dir` 對下表:`dir=pull` 同 `pl pull`、`dir=push` 同 `pl push`。新增代碼時同一個 PR 補這張表與英文 [README](README.md#reason_code-table) 的同一張表(`internal/cli/reason_code_test.go` 兩份都比)。

| 命令(列) | action | reason_code | 意思 |
|---|---|---|---|
| `pl pull`(與 `dir=pull` 列) | `add` | `added_on_platform` | 平台上新加的曲目,加進正本 |
| | `remove` | `removed_on_platform` | 平台上拿掉的曲目,從正本移除 |
| | `move` | `moved_on_platform` | 平台上換了位置 |
| | `rename` | `renamed_on_platform` | 平台上的清單改了名 |
| | `unlink` | `playlist_gone` | 平台上的清單不見了,取消連結 |
| `pl push`(與 `dir=push` 列) | `add` | `push` | 正本有、平台沒有,推上去 |
| | `remove` | `removed_in_master` | 正本拿掉了,從平台移除 |
| | `move` | `moved_in_master` | 正本換了位置 |
| | `rename` | `renamed_in_master` | 正本改了名 |
| | `skip` | `no_mapping` | 沒有這個平台的 id,這次不推:先 `capy resolve` |
| | `skip` | `unpushable` | 有 id 但推不上去(local file、只在資料庫裡、檔案不在這台電腦),請在平台手動加 |
| `migrate` 的 `dir=migrate` 列(`action` 一律是 `add`,搬不搬得過去看代碼) | `add` | `push` | 搬得過去(網頁的搬家精靈靠它認) |
| | `add` | `no_mapping` | 目標平台沒對應到,這次不搬 |
| | `add` | `unpushable` | 目標平台有 id 但推不上去,這次不搬 |
| `pl dedup <正本>` 的 `dir=dedup` 列 | `remove` | `duplicate` | 正本裡後出現的重複份(保留第一份) |
| `pl dedup <平台>:<清單>`(只報告,沒有 action 欄) | | `dup_id` | 同平台 id |
| | | `dup_isrc` | 同 ISRC |
| `resolve` | `map` | `isrc` | ISRC 反查到,自動寫入 |
| | `map` | `fuzzy` | 模糊比對 ≥85,自動寫入 |
| | `review` | `no_candidate` | 找不到候選 |
| | `review` | `low_score` | 候選分數不到 85 |
| | `review` | `candidate_taken` | 候選已屬於另一首(合併只由人決定) |
| | `review` | `candidate_assigned` | 候選在這一輪已經分給另一首 |
| | `review` | `lookup_failed` | 查詢失敗,下次再查 |
| | `conflict` | `isrc_conflict` | 同 ISRC 觀測到不同 id |

### 網頁介面

```bash
capy --web
```

在你這台電腦上起一個只綁 `127.0.0.1` 的網頁介面,啟動時印一行網址(帶一次性 token),在終端機裡會順便替你開瀏覽器;放進管線或背景執行時只印網址。

**打開就是「搬家」:把一個平台的播放清單搬到另一個平台,三步做完。** 選來源與目的地(每個平台的連接狀態一眼看得到,沒連的可以當場連)→ 挑一個清單、決定建新的還是加進既有的 → 看過要搬哪些歌、確認了才寫入。比對每一首歌的時候有真的進度(「比對歌曲 37 / 120」),搬不過去的歌會逐首列出來。它不刪來源、只新增、順序不動;不用付費、沒有曲數上限,因為它在你自己的電腦上用你自己的帳號跑。

先說限制:搬進本機曲庫只能加進既有的 M3U 檔(Spotify 與 Apple Music 都能在目的地**建新清單**);搬進 Apple Music 的歌會不會同時加進你的 Apple Music 資料庫,看你的 Apple Music 設定,而且只能搬進你自己建的清單;Spotify 要用你自己建的 app、Apple 要自己從網頁播放器複製 token,所以第一次連接帳號要花幾分鐘——換來的是沒有人替你代管憑證。硬碟裡的 M3U 播放清單也可以搬到 Spotify 或 Apple Music。

其他頁面:**我的清單**、**同步**、**搜尋**、**帳號**;「進階」裡是**主控台**(跟終端機一樣可以打任何子命令)、**ISRC 查詢**(一次問三個平台)與**診斷**。每一頁的底部一直有**正在播放列**(播放狀態面板):現在放什麼、進度、播放控制,`capy now` 的內容都在那裡。它跟著真正在播的平台走(只有正在播的平台會把它拉走,暫停中的不會),控制鈕作用在它顯示的那個平台上;`capy --web --provider X` 可以把它釘在一個平台。頁面上的每一個動作背後都是一條 capy 命令,主控台留著完整紀錄;要確認的事一律由命令自己問,頁面不會替你按。側欄最下面還有語言選單(見上方「介面語言」)。

```bash
capy --web --port 43117   # 指定 port(預設隨機;不可用 8888、80、443)
```

幾件先知道的事:

- **只在你這台電腦上。** 只綁 `127.0.0.1`,不是區網服務;每次啟動產生一次性 token,行程結束網址就失效。頁面不用 cookie。
- **一次跑一個命令。** 第二個命令會被擋(頁面會說「另一個命令執行中」),因為它跟終端機一樣共用同一份 Drive 與本機資料。跑著的時候,底部會多一列執行狀態:現在在做什麼、跑了多久、它最新印的那一行(比對歌曲時是做到第幾首),旁邊的 **中止** 隨時可以停(在主控台的命令列按 Ctrl-C 也行)。停下來的時機跟終端機按 Ctrl-C 一樣;已經答應寫入的命令要按兩次,因為停在一半可能只寫了一部分。
- **憑證不會經過頁面。** 精靈輸入的 secret 只從瀏覽器送進行程再進 keychain,不會出現在事件、log 或網址裡;命令回聲裡的 token 值一律遮成 `***`。
- **有幾個命令在網頁上不提供**:`debug` 群組、`--client-secret` / `--developer-token` / `--user-token`(請走精靈)、`now --watch`(看面板就好)。`capy update` 可以跑,但更新完這個網頁行程還是舊版,會要你重啟。
- **Windows 第一次啟動**可能跳防火牆提示。它只聽 `127.0.0.1`,選「取消」也不影響本機連線。

終端機的互動式介面(`capy` 無參數)是**重新執行 capy 自己**,所以每個子命令都保有它原本的樣子;網頁介面則在**同一個行程裡**跑並由頁面回答提示。兩條路不同是因為終端機已經有 TTY 可以讓給子行程,瀏覽器沒有;而網頁的表格與提示要能結構化送到瀏覽器,所以接的是同一組接縫。

### 跨平台複製清單(兩個方向都行;例:Apple Music → Spotify)

一個命令(Spotify、Apple Music、Google 三個都要先登入,`capy auth status` 看得到;清單名有空白要加雙引號):

```
capy migrate 公路旅行 --from apple --to spotify              # 在 Spotify 建一個同名的私人清單,把 Apple 的曲目搬過去
capy migrate 公路旅行 --from apple --to spotify:開車歌單      # 或加進 Spotify 既有的清單:接在它原本的曲目後面
capy migrate 公路旅行 --from spotify --to apple                # 反方向:在 Apple Music 建一個同名清單,把 Spotify 的曲目搬過去
capy migrate                                                # 終端機裡不帶參數:逐段挑選來源平台、清單、目標平台、既有清單或建新的
```

它把下面手動流程的七步一次做完:讀來源(不連結、不動它)→ 決定正本與目標(既有的目標先拉進正本)→ 來源裡目標還沒有的依來源順序接在尾端(同平台 id 或同 ISRC 的略過,來源自己的重複也只留一份)→ 替每一首找目標平台的 id(ISRC 反查 → 模糊比對;沒對到的在終端機可以當場逐筆裁決)→ 一張表(`dir` 有 `pull` / `migrate` / `push` 三種)、一次確認 → 需要時才在目標建清單 → 推。先看不做用 `--dry-run`;腳本裡要 `--yes`(非 TTY 沒給以 exit 2 結束、不建清單)。**順序**:目標原本的順序是前綴,來源的曲目依來源的順序接在後面。**只新增**:永遠不動來源,對目標也不移除;目標有還沒同步的移除 / 換序 / 改名時以 exit 3 擋下,先 `capy pl sync`。沒對到的曲目這次不推,表裡會說,結尾給你 `capy resolve --review` 與 `capy pl sync` 的命令補上。完成後只有目標連著 capy 的正本(來源不連結,一次性複製);要之後跟著來源的變動,結尾也會給 `capy pl link` + `capy pl sync` 的命令。例外:正本本來就連著來源(下面的手動流程做到一半)時,來源那半也一起拉進正本、以正本為準,結尾會說兩邊都連著。local 只能加進既有檔(`--to local:<檔名>`);搬進 Apple Music 的曲目會不會同時加進你的 Apple Music 資料庫,看你的 Apple Music 設定(Apple 的行為),而且只能搬進你自己建的清單(Apple 精選不行);Apple 商店裡已經下架的歌搬不過去:表裡可能照樣列出,寫完重讀時 capy 會警告。

> ⚠️ 建清單(`POST /me/playlists`)與推曲目(`PUT /playlists/{id}/items`)是照 Spotify 2026-02 的官方文件實作,**還沒在真帳號上驗證過**。遇到 404,或建出來的清單在 app 裡是公開的,請回報。

**背後在做什麼(手動流程;想讓兩邊持續同步時用這個)**:讓兩個平台連到同一個 canonical 清單,再推過去。下面以 Apple Music 的「公路旅行」複製到 Spotify 為例。

```
capy pl link 公路旅行 apple:公路旅行                  # 1. canonical 清單(不存在就建立)連到 Apple 的清單
capy pl link 公路旅行 spotify --create                # 2. 在 Spotify 建一個同名的私人空清單,連到同一個 canonical
capy pl pull 公路旅行                                 # 3. 不帶 --provider:Apple 的曲目拉進 canonical;空的 Spotify 清單記下 base
capy resolve 公路旅行 --provider spotify              # 4. 替每一首找 Spotify 上的 id(ISRC 反查 → 模糊比對)
capy resolve --review                                 # 5. 上一步列出 review 佇列時才需要:逐筆裁決
capy pl push 公路旅行 --provider spotify --dry-run    # 6. 先看要推什麼
capy pl push 公路旅行 --provider spotify              # 7. 真的推
```

- **第 3 步不能省。** push 的前提是這台裝置對那個 Spotify 清單 pull 過;第 2 步剛建的清單還沒有 base,直接 push 會以 exit 3 擋下。
- **不會刪到任何東西。** Spotify 那邊是第一次 pull(沒有 base 不產生 remove),push 全部是新增,刪除閾值不會觸發。
- **不一定 100% 複製得過去。** Apple 上有 catalog 對應的曲目帶 ISRC,在 Spotify 精確反查(信心 95、自動寫入);你自己上傳、只在資料庫裡的曲目沒有 ISRC,只能靠標題、藝人、時長模糊比對,分數不到 85 進 review 佇列;Spotify 上根本沒有的歌,在 `--review` 裡釘成不可得。第 6 步表裡的 `skip` 列,就是這次複製不過去的曲目。
- **`--create` 建的清單跟 canonical 同名**,所以 push 不會多一列 `rename`。Spotify 上已經有你自己的同名清單(例如之前先在 app 裡建好了)時會擋下,並給你連它的命令;追蹤的別人的清單連不了,不算。在終端機裡也可以直接打 `capy pl link`,第二段選「在 spotify 建一個新的空清單」。
- **之後兩邊保持連結。** 任一邊有變動時跑 `capy pl sync 公路旅行` 就會帶到另一邊。只要一次性複製的話,完成後 `capy pl unlink 公路旅行 apple`。
- **反方向(Spotify → Apple)一樣**:`capy migrate 公路旅行 --from spotify --to apple`,或把手動流程裡的兩個平台對調(`capy pl link 公路旅行 apple --create`)。Apple 這一側的加歌走 Apple 文件化的端點,移除與換序走網頁播放器自己用的端點(Apple 沒有正式承諾;細節見 docs/ARCHITECTURE.md §1.2),只寫你自己建的清單,加進清單的曲目會不會同時進資料庫看你的 Apple Music 設定。

## Shell 補全(TAB 列出播放清單名與最近搜尋)

```
# zsh(放進 ~/.zshrc)
source <(capy completion zsh)
# bash
source <(capy completion bash)
# fish
capy completion fish | source
# PowerShell(放進 $PROFILE)
capy completion powershell | Out-String | Invoke-Expression
```

候選只來自本機快取(`state.db`,不打網路):先跑過 `capy pl list` 才有清單名;`capy search` / `capy play` 會累積最近項目,`capy history clear` 清空。補全不會打網路、不會碰 keychain,所以按 TAB 不會卡。

## 憑證與資料

你的憑證只存 OS keychain(macOS Keychain / Windows Credential Manager),不進設定檔、不上雲。唯一例外是專案自己的 Google client:Releases 的 binary 把它編在裡面(`strings capy` 讀得到,散佈給使用者的原生 app 本來就藏不住,RFC 8252 §8.5),那是 app 自身的識別,不是任何人的帳號憑證。架構、平台約束與開發階段見 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。

## 發版(維護者)

1. 一次性:repo Settings → Secrets and variables → Actions 設 `GOOGLE_CLIENT_ID` 與 `GOOGLE_CLIENT_SECRET`(專案自己的 Google Desktop client;它們只存在 GitHub secrets 與發出去的 binary 裡,不進 repo)。
2. `git tag v1.2.3 && git push origin v1.2.3`:`.github/workflows/release.yml` 用 GoReleaser 建四個平台的檔、`checksums.txt` 與 GitHub Release;secret 沒設會在建置前就失敗。tag 帶 `-rc1` 之類會標成 pre-release,`capy update` 不會抓到它。
3. 每個 PR 的 CI 都會用假值跑一次 `goreleaser release --snapshot`、執行建出來的 binary 確認注入到位,所以推 tag 前 release 設定已經被驗過。

## 授權

MIT,見 [LICENSE](LICENSE)。Releases 的壓縮檔裡也附一份。
