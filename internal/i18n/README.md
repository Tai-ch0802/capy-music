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
- **Web UI** (`internal/cli/webui`) keys are `webui.<page>.<name>`. JS calls `t('webui.move.title', { count, name })`
  from `js/i18n.js` (key and placeholder names as literals, same static checks as Go); static HTML uses
  `data-i18n`, `data-i18n-aria-label`, `data-i18n-placeholder` or `data-i18n-title`. `GET /api/i18n` serves every
  `webui.*` key plus the few shared keys listed in `webSharedKeys` (`internal/cli/web.go`). No module may compute
  user-facing text at import time: `t()` throws before the catalog has loaded (see the top of `js/i18n.js`).
  Web keys that only restate a sentence Go already prints aren't duplicated: the page gets the CLI key through
  `webSharedKeys`, and the prompt bridge (`web_prompt.go`) uses the same keys as the terminal prompts. The labels a
  page gives the commands it runs (shown in the running-job bar and the Console) are present progressive in English:
  `Checking account connections`, `Switching the interface language`.
- **A value may own its surrounding spacing** when it is spliced into other text: `sep.or` is `" or "` in English
  and `" 或 "` in Chinese; `web.lock.stop` replaces `" Ctrl-C"` *including* the space, so English writes `" Stop"`
  and Chinese writes `「中止」` with none (full-width brackets carry their own spacing). Keep those spaces when you
  translate; the tests pin the composed result.

## Adding a language

1. Copy `en.json` to `locales/<tag>.json`, where `<tag>` is the canonical BCP 47 tag (`ja`, `zh-CN`, `pt-BR`;
   `TestLocaleFilesAreCanonicalTags` rejects `zh_cn.json` or `JA.json`). There is no list of languages to edit:
   the files are embedded (`//go:embed locales/*.json`) and `i18n.Supported()` is whatever is there, so
   `capy config set language <tag>`, the hints that list valid values, and the web UI's language menu all pick the
   new file up.
2. Translate. Every key must stay, and no value may be empty: anything you are unsure about keeps the English text
   (untranslated text still shows; an empty one makes the message disappear). Keep every `{placeholder}` (you may move
   it), and give each plural message the CLDR categories your language uses for whole numbers (`other` is always
   allowed); the test names any that are missing or extra. Set `lang.name` to the language's own name (`日本語`,
   `Português (Brasil)`): the language menu shows it, and a copied `"English"` would appear twice.
3. The Apple disclosure (`auth.apple.disclosure`) must say everything the English one says: the tokens belong to
   Apple's web player and aren't officially supported, they may stop working (the user then runs
   `capy auth login apple` again), the risk is the user's, capy only
   shows how to copy them and never reads the browser, tracks added to a playlist *may* also be added to the user's
   library depending on their Apple Music settings (never "will"), and capy only writes to playlists the user
   created. Add a case for your tag in `checkAppleDisclosure` (`internal/cli/i18n_en_auth_test.go`):
   `TestAuthLoginAppleDisclosureInEveryLanguage` runs every language in `i18n.Supported()` and fails for one
   that has no case.
4. Keep the web UI's lock-notice rewrite working: `auth.lock.waiting` must contain the lock file name and
   `" Ctrl-C"` with the space in front (the web UI swaps that for `web.lock.stop`), and `auth.refresh_in_flight`
   must contain neither, so it isn't rewritten (`internal/auth/tokenstore_test.go` checks every language).
5. Run `go test ./...`. `./internal/i18n/` lists missing or extra keys, empty values, placeholder mismatches and
   plural categories; the tests from steps 3 and 4 run with the rest.
6. Look at it: `capy config set language <tag>`, then a few commands, bare `capy` in a narrow terminal (the width
   tests only cover `en` and `zh-TW`), and `capy --web` (`GET /api/i18n` serves the new file; `<html lang>` and the
   plural rules follow the tag).

The README, the user guide (`docs/guide.html`, `/guide` on the website) and the website itself are translated
separately. They are optional: a language can ship in the program without them.

## Glossary

Use one English term per concept so messages stay consistent.

| 繁體中文 | English | Notes |
|---|---|---|
| 正本 | master copy | capy's own copy of a playlist, kept in the user's Google Drive |
| canonical(資料、紀錄) | canonical | kept as a technical term where the Chinese keeps it too: all of capy's own data on this computer (`capy export`, the ISRC page's card); one playlist of it is a master copy |
| 清單、播放清單 | playlist | |
| 歌、首、曲目 | song (web UI), track (CLI) | every `webui.*` value says "song" (the web UI is the general-user surface: "Matching songs", "(2 songs)", "Next song"); CLI keys keep "track" |
| 平台 | platform | Spotify, Apple Music, local library |
| 本機 | this computer | "this computer's state.db"; not "this machine" |
| 鑰匙圈、keychain | keychain | the macOS Keychain / Windows Credential Manager; lowercase in running text |
| 商店地區(storefront) | storefront (CLI), store region (web UI) | the CLI keeps Apple's term and the `apple_storefront` setting; the web UI says "store region" |
| 本機曲庫 | local library | M3U playlists plus `library.json` on this computer; as the platform's display name (next to Spotify and Apple Music) it's capitalized: `Local library` |
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
| 精靈 | wizard | the step-by-step login flows and the web's move wizard |
| 逃生口 | escape hatch | `capy export`, `capy drive init --from-local` |
| 中止 | Stop | the web UI's button in the running-job bar (Ctrl-C in a terminal); `web.lock.stop` owns its leading space |
| 取消、已取消 | cancel, cancelled | a prompt or picker the user backed out of; spelled `cancelled` in every message |
| 網頁的頁面:搬家、我的清單、同步、搜尋、帳號、主控台、ISRC 查詢、診斷、進階 | Move, My playlists, Sync, Search, Accounts, Console, ISRC lookup, Diagnostics, Advanced | the rail (`webui.rail.*`); refer to a page by this name |
| 挑選器 | picker | |
| 檢視窗格 | pager | |
| 互動式介面 | interactive mode | bare `capy` in a terminal |
| 網頁介面 | web UI | `capy --web` |
| 授權、登入 | authorization, log in | |
| 憑證 | credentials | |
