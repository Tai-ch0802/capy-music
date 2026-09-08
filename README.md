# capy-music

跨平台音樂 CLI:搜尋、播放遙控、播放清單同步(Spotify、Apple Music;播放清單同步到你自己的 Google Drive)。開源、免費,**所有憑證都是你自己的(BYO)** —— 本專案不代持任何 token、不架任何服務。

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

Drive 狀態不確定時,更新前先 `capy export > backup.json`:本機快取 `state.db` 的 schema 升版時舊檔會保留為 `state.db.v<舊版>`,但沒有程式會讀它——那只是「不毀掉」,不是「能復原」。

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

需要 Apple Music 訂閱。播放遙控只在 macOS(透過 Music.app);搜尋與播放清單在 macOS / Windows 皆可用。

## Google Drive 同步(選用):登入 Google

`capy auth login google` 之後,播放清單會同步到你 Google Drive 的應用程式資料夾(其他 app 與你自己都看不到內容,只佔你的 Drive 空間)。只索取三個權限:`openid`、`userinfo.email`、`drive.appdata`。

- **從 GitHub Releases 下載的 binary**:內建專案自己的 Google client,直接執行 `capy auth login google` 就好。
- **`go install` 或自己 build 的**:沒有內建 client,`auth login google` 的精靈會引導你建自己的(免費,約 5 分鐘):
  1. https://console.cloud.google.com → 建立專案 → API 和服務 → 啟用「Google Drive API」
  2. Google Auth platform → Branding:填 app 名稱與 support email;Audience 選 External
  3. Data Access:只加這三個 scope —— `openid`、`userinfo.email`、`drive.appdata`(多加 Gmail 之類會觸發資安評估)
  4. 建立 OAuth client:類型選「桌面應用程式(Desktop app)」;secret 只在建立當下顯示一次,立刻複製
  5. ⚠️ Audience 按「Publish app」切到 In production —— 停在 Testing 的話 refresh token 7 天就過期,你會莫名被登出
  6. 把 Client ID 與 Client secret 貼進精靈。非互動環境用 `--client-id` / `--client-secret` 或 `CAPY_GOOGLE_CLIENT_ID` / `CAPY_GOOGLE_CLIENT_SECRET`。

不管哪一種,`--client-id` / `--client-secret` 永遠可以覆寫內建值。自建 client 的 secret 只進 OS keychain。`capy auth status` 會顯示登入的 Google 帳號 email 與這台裝置的 `device_id`;`capy auth logout google` 刪 token 與自建 client 的 secret。

## 常用命令

```
capy search 派對動物 [--provider apple]
capy play 派對動物                      # 統一搜尋:曲目、藝人熱門歌曲、我的播放清單;歧義時開挑選器
capy play 五月天 / capy play 通勤        # 藝人 = 播熱門歌曲(Spotify 開發模式 app 拿不到 top-tracks,退回依熱門度排序的搜尋);清單名完全相符 = 播清單(先 capy pl list 一次)
capy play --type track 派對動物          # 腳本用:確定性,永遠播第一筆;前綴 artist: / pl: / track: 同義
capy play --pick                        # 直接開挑選器(本機快取的清單與最近項目)
capy pause / next / prev / now / devices
capy pl list / capy pl show <名稱|ID>
capy pl link 通勤 spotify:<清單 ID 或名稱>   # 把 canonical 清單(不存在就建立)連結到平台清單;只認明確 link,不自動配名
capy pl unlink 通勤 spotify
capy pl pull 通勤 [--dry-run] [--yes] [--force] / capy pl pull --all [--provider spotify]   # 平台 → canonical → Drive;變更先列出、確認後才寫(需先 capy auth login google)
capy export > backup.json                 # 逃生口:本機 canonical 資料(Drive 檔的合併形式),不依賴 Drive
capy drive init --from-local [--dry-run] [--yes]   # 逃生口:Drive 空 / 部分遺失時用本機 state.db 補回缺的檔
capy doctor [--provider apple]
capy config set default_provider apple   # 之後不必每次帶 --provider;config get / list
capy update [--dev]                      # 見上方「更新」
```

所有命令在非 TTY(pipe / cron)下輸出純文字 TSV,可直接 `cut -f`;`play` 在非 TTY 遇到歧義會以 exit 2 結束並印出候選(`type\tid\tlabel\tdetail`),不會播、也不會問——腳本請用 `--type` 或前綴。終端機下表格依顯示寬度對齊,超寬時 ID 欄先截斷。設定目錄可用 `CAPY_CONFIG_DIR` 覆寫。

`capy pl pull` 的 exit code 是對外契約(cron 靠它):`0` 無變更或已成功套用、`1` 錯誤、`2` 有待套用的變更(`--dry-run`、非 TTY 沒給 `--yes`、在終端機取消)、`3` 安全閥擋下(Drive appdata 不完整;或單一清單要刪 >10 首、或 >30% 且 >3 首)。`--yes` 只跳過確認、`--force` 只越過刪除閾值且只能配單一清單(`capy pl pull <名稱> --force`,不能配 `--all`:安全閥一次只解除一個清單),兩者都不放行「Drive 不完整」——那條的出口是 `capy drive init --from-local`。變更集在非 TTY 下是無標題 TSV:`action provider playlist pos cid provider_id title artists reason`;「這次動了幾筆」看行數,不佔 exit code。寫入順序固定 Drive 先、本機 `state.db` 後;`state.db` 只是快取,刪掉後下一次 pull 會從 Drive 重建。

兩個逃生口:`capy export` 只讀本機 `state.db`(不碰 Drive、網路、keychain),把 Drive 檔的合併形式輸出到 stdout——鍵是檔名(`manifest.json`、`tracks.json`、`pl__<pid>.json`、`dev__<device_id>.json`)、值是該檔內容的縮排形式(壓回 compact 後與 Drive 上逐位元相同);本機沒資料時 exit 1 且不印東西。它用唯讀方式開 `state.db`:壞檔不刪、版本不符不改名、全新機器不建檔,而且整份匯出是一個一致的快照(與 cron 的 `pl pull` 同時跑也不會撕裂)。`capy drive init --from-local` 是 `pl pull` 以 exit 3 擋下「Drive 不完整」之後的出口:只建 Drive 缺的檔、不覆寫還在的檔、不動本機快取,別台裝置的 `dev__` 檔不代為上傳;先列出要建的檔(非 TTY 是 TSV `action file`),`--yes` 或在終端機確認後才上傳,`--dry-run` 只列不傳。確認訊息會帶目前登入的 Google 帳號:登錯帳號會把整個曲庫傳到別人的 appdata。

## Shell 補全(TAB 列出播放清單名與最近搜尋)

```
# zsh(放進 ~/.zshrc)
source <(capy completion zsh)
# bash
source <(capy completion bash)
# fish
capy completion fish | source
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
