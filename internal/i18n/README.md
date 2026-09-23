# internal/i18n — message catalogs

Every user-facing string lives in `locales/<BCP 47 tag>.json`. `en.json` is the source and the template for new
languages; `zh-TW.json` is Traditional Chinese. The package only depends on the standard library and
`golang.org/x/text`; the `cli` package decides the language (config `language`) before it builds the command tree.

## Conventions

- **Keys are semantic and dotted:** `<area>.<name>`, e.g. `config.err.not_dir`. Cobra help text is
  `cmd.<command path>.short`, e.g. `cmd.config.set.short`.
- **Placeholders are named:** `{key}`, `{count}`. Translators may put them anywhere in the sentence.
- **Plurals** are an object keyed by CLDR category (`zero`, `one`, `two`, `few`, `many`, `other`), selected by the
  integer passed as `count`. English needs `one` and `other`; Chinese only `other`.
- **Never translated:** command names and flags (`capy auth login apple`, `--dry-run`), file names (`config.json`),
  platform names, and machine fields — TSV column names, `ACTION` / `DIR` values, exit codes, table headers.
- **Call sites write the key and placeholder names as string literals**, so the tests can check them statically:
  `i18n.T("config.err.not_dir", "key", k, "path", p)`. Never build a key at runtime, and never pass a translated
  string to `fmt` as a format string.
- **Package-level errors** use `i18n.Errorf(key)`: the message is translated when it is printed, not at init.

## Adding a language

1. Copy `en.json` to `<tag>.json`, where `<tag>` is the canonical BCP 47 tag (`ja`, `zh-CN`, `pt-BR`).
2. Translate. Anything you are unsure about can stay in English.
3. Run `go test ./internal/i18n/`. It lists missing or extra keys, placeholder mismatches, and the plural categories
   your language needs.
4. Done: `capy config set language <tag>` and the web UI's language menu pick the new file up automatically.

## Glossary

Use one English term per concept so messages stay consistent.

| 繁體中文 | English | Notes |
|---|---|---|
| 正本 | master copy | capy's own copy of a playlist, kept in the user's Google Drive |
| 清單、播放清單 | playlist | |
| 平台 | platform | Spotify, Apple Music, local library |
| 本機 | this computer | "this computer's state.db"; not "this machine" |
| 本機曲庫 | local library | M3U playlists plus `library.json` on this computer |
| 資料庫(Apple Music) | library | "your Apple Music library" |
| 連結 | link | a master copy linked to a playlist on a platform |
| 對應(名詞) | mapping | a track's id on each platform; REASON texts say "no mapping" for 沒有對應 |
| 對應到、沒對應到 | matched, unmatched | prose about whether a track was found on a platform: "2 tracks haven't been matched on apple yet" |
| 裁決、逐筆裁決 | review | the review queue; "review each match" |
| 變更集 | change set | |
| 閾值 | threshold | |
| 刪除閾值 | removal threshold | the >10 tracks, or >30% and >3 tracks limit; not "deletion threshold" |
| 安全閥 | safety check | |
| 協作清單 | collaborative playlist | |
| Apple 精選 | Apple-curated playlist | |
| 喜好歌曲 | Favorite Songs | Apple's own name |
| 已購買的音樂 | Purchased Music | Apple's own name |
| 已下架 | no longer available | removed from the store |
| 搬家 | move | the web wizard; the command is `capy migrate` |
| 挑選器 | picker | |
| 檢視窗格 | pager | |
| 互動式介面 | interactive mode | bare `capy` in a terminal |
| 網頁介面 | web UI | `capy --web` |
| 授權、登入 | authorization, log in | |
| 憑證 | credentials | |
