# capy-music

跨平台音樂 CLI:搜尋、播放遙控、播放清單同步。架構、平台約束(2026-08 查證)與開發階段見 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) —— 動 provider 或 auth 相關程式前,先讀它的 §1 現況約束表。

## 定案(2026-09-01,理由見 docs/ARCHITECTURE.md 附錄 C)

- 語言 Go,module `github.com/Tai-ch0802/capy-music`,binary `capy`(無別名)。
- 支援 macOS + Windows;**Linux 非目標** —— keychain 只實作 macOS Keychain 與 Windows Credential Manager,不寫 Secret Service / age fallback。
- TUI 用 charmbracelet(bubbletea v2 / lipgloss / bubbles / huh),但**所有命令在非 TTY(pipe/cron)下必須可純文字輸出** —— 可腳本化是核心價值,不得被 TUI 犧牲。
- Apple developer token Worker 掛 `capy.taislife.work`;endpoint 是 binary 內建預設值,必須可被 config 覆寫。

## 硬約束(違反會出事的那種)

- Apple 授權頁 `internal/auth/apple/web/authorize.html` 是 ToS 硬約束:只准有一個 `<script src="https://js-cdn.music.apple.com/...">`。禁止 bundle、禁止加入任何其他 JS。
- Spotify redirect URI 必須用 `127.0.0.1`,不可用 `localhost`。
- Google 只准用 `openid` / `userinfo.email` / `drive.appdata` 三個 scope。任何人提議加 Gmail scope 都要擋下來(會觸發 restricted scope 資安評估)。
- 憑證只進 OS keychain,絕不寫入 Drive、SQLite 或設定檔。
- Spotify PKCE 的 refresh token 會輪替,每次 refresh 必須覆寫儲存。
- 任何會刪除使用者播放清單曲目的程式路徑,都必須先過 dry-run 與閾值檢查。
- SQLite 是 cache,不是 source of truth。刪除 db 必須能從 Drive 完整重建(此約束要有測試)。
- Worker 的 token 快取用 Cache API(`caches.default`),不可用 KV —— KV 免費層每天只有 1,000 次寫入,per-request 寫入約 500 名使用者就會耗盡。速率限制用 Cloudflare 原生 Rate Limiting binding。
- Apple 的 `.p8` 必須支援 BYO(`CAPY_APPLE_P8_PATH`),且此路徑要與 Worker 路徑同等測試。這不是 nice-to-have —— 它同時緩解 API 配額集中、token 外洩、維護者停繳年費三項風險(見 docs/ARCHITECTURE.md §8.5.4)。
