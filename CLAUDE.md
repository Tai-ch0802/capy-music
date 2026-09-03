# capy-music

跨平台音樂 CLI:搜尋、播放遙控、播放清單同步。架構、平台約束(2026-08 查證)與開發階段見 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) —— 動 provider 或 auth 相關程式前,先讀它的 §1 現況約束表。

## 定案(2026-09-01,理由見 docs/ARCHITECTURE.md 附錄 C)

- 語言 Go,module `github.com/Tai-ch0802/capy-music`,binary `capy`(無別名)。
- 支援 macOS + Windows;**Linux 非目標** —— keychain 只實作 macOS Keychain 與 Windows Credential Manager,不寫 Secret Service / age fallback。
- TUI 用 charmbracelet(bubbletea v2 / lipgloss / bubbles / huh),但**所有命令在非 TTY(pipe/cron)下必須可純文字輸出** —— 可腳本化是核心價值,不得被 TUI 犧牲。
- 開源、credential 全面 BYO(2026-09-03 定案,附錄 C 決策 8):Spotify 由使用者自建 app;Apple 由使用者自己從 Apple 網頁播放器複製 web token。我們只指導,不代持任何憑證、不架任何派發服務。`.p8`/Cloudflare Worker 官方路徑已移除,完整快照在 commit `3649b7b`。

## 硬約束(違反會出事的那種)

- **Apple 憑證是使用者自己從 Apple 網頁播放器複製的 web token(非 Apple 官方支援)。`auth login apple` 的預設路徑只指導、絕不自動擷取 —— 不開瀏覽器抓 cookie、不讀瀏覽器 cookie 資料庫、不注入 JS。揭露(非官方、會失效、風險自負)必須在指令內且不可跳過,不只在 README。** 唯一例外是隱藏的 `--auto` flag:未文件化、`--help` 不列、opt-in、開發者自負,允許自動擷取;它的存在不改變預設路徑的上述鐵則。
- Spotify redirect URI 必須用 `127.0.0.1`,不可用 `localhost`。
- Google 只准用 `openid` / `userinfo.email` / `drive.appdata` 三個 scope。任何人提議加 Gmail scope 都要擋下來(會觸發 restricted scope 資安評估)。
- 憑證只進 OS keychain,絕不寫入 Drive、SQLite 或設定檔。
- Spotify PKCE 的 refresh token 會輪替,每次 refresh 必須覆寫儲存。
- 任何會刪除使用者播放清單曲目的程式路徑,都必須先過 dry-run 與閾值檢查。
- SQLite 是 cache,不是 source of truth。刪除 db 必須能從 Drive 完整重建(此約束要有測試)。
