# capy-music

跨平台音樂 CLI:搜尋、播放遙控、播放清單同步(Spotify、Apple Music;Google Drive 同步開發中)。開源、免費,**所有憑證都是你自己的(BYO)** —— 本專案不代持任何 token、不架任何服務。

## 安裝

```bash
go install github.com/Tai-ch0802/capy-music/cmd/capy@latest
```

(release 分發規劃中:macOS Homebrew、Windows Scoop/winget。)

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

## 常用命令

```
capy search 派對動物 [--provider apple]
capy play 派對動物                      # 統一搜尋:曲目、藝人熱門歌曲、我的播放清單;歧義時開挑選器
capy play 五月天 / capy play 通勤        # 藝人 = 播熱門歌曲(Spotify 開發模式 app 拿不到 top-tracks,退回依熱門度排序的搜尋);清單名完全相符 = 播清單(先 capy pl list 一次)
capy play --type track 派對動物          # 腳本用:確定性,永遠播第一筆;前綴 artist: / pl: / track: 同義
capy play --pick                        # 直接開挑選器(本機快取的清單與最近項目)
capy pause / next / prev / now / devices
capy pl list / capy pl show <名稱|ID>
capy doctor [--provider apple]
capy config set default_provider apple   # 之後不必每次帶 --provider;config get / list
```

所有命令在非 TTY(pipe / cron)下輸出純文字 TSV,可直接 `cut -f`;`play` 在非 TTY 遇到歧義會以 exit 2 結束並印出候選(`type\tid\tlabel\tdetail`),不會播、也不會問——腳本請用 `--type` 或前綴。終端機下表格依顯示寬度對齊,超寬時 ID 欄先截斷。設定目錄可用 `CAPY_CONFIG_DIR` 覆寫。

## Shell 補全(TAB 列出播放清單名與最近搜尋)

```
# zsh(放進 ~/.zshrc)
source <(capy completion zsh)
# bash
source <(capy completion bash)
# fish
capy completion fish | source
```

候選只來自本機快取(`cache.json`):先跑過 `capy pl list` 才有清單名;`capy search` / `capy play` 會累積最近項目,`capy history clear` 清空。補全不會打網路、不會碰 keychain,所以按 TAB 不會卡。

## 憑證與資料

憑證只存 OS keychain(macOS Keychain / Windows Credential Manager),不進設定檔、不上雲。架構、平台約束與開發階段見 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。
