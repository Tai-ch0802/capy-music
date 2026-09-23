**English** | [繁體中文](README.zh-TW.md)

# capy-music

A cross-platform music CLI: search, playback control and playlist sync (Spotify and Apple Music; playlists sync to your own Google Drive). Open source and free, and **every credential is your own (BYO)** — this project holds no tokens for you and runs no service.

Website: <https://capy.taislife.work/en/> (an introduction for general users, the [privacy policy](https://capy.taislife.work/en/privacy) and the [terms of service](https://capy.taislife.work/en/terms); the source is in [`site/`](site/). The `/guide` and `/en/guide` pages are generated from `docs/guide.html` and `docs/guide.en.html`: after editing either guide, run `go test ./site/ -run TestGuideOnSiteIsCurrent -update`. The repo is connected to Cloudflare Workers Builds; after merging into main, check that the live site has updated, and if it hasn't, deploy by hand with `cd site && wrangler deploy`.)

The full user guide (interactive mode, the web UI, a command reference and what each of the three platforms can do) is at <https://capy.taislife.work/en/guide> (繁體中文: <https://capy.taislife.work/guide>). The sources are `docs/guide.en.html` (English) and `docs/guide.html` (Chinese): each is a single file that needs no server and loads no external resources, so it opens offline from a clone too (GitHub only shows the source of `.html` files in a repo).

## Install

### A. Download from GitHub Releases (macOS / Windows; Google login needs no setup)

Download the file for your platform from [Releases](https://github.com/Tai-ch0802/capy-music/releases), unpack it and put `capy` (`capy.exe` on Windows) on your PATH:

| Platform | File |
|---|---|
| macOS Apple Silicon | `capy_<version>_darwin_arm64.tar.gz` |
| macOS Intel | `capy_<version>_darwin_amd64.tar.gz` |
| Windows x64 | `capy_<version>_windows_amd64.zip` |
| Windows ARM64 | `capy_<version>_windows_arm64.zip` |

- **macOS**: the binary isn't signed by Apple. Gatekeeper blocks files downloaded with a browser ("cannot be opened because the developer cannot be verified"); after unpacking, run `xattr -d com.apple.quarantine capy` once. Files downloaded with `curl -L -O <url>` aren't flagged.
- **Windows**: SmartScreen warns about an "unknown publisher" the first time; choose "More info → Run anyway" once.
- `checksums.txt` has each file's SHA-256: a match means the download is complete, **not** that the source is signed (this project doesn't sign anything).

Release binaries include the project's own Google OAuth client: `capy auth login google` opens the browser for authorization straight away, with nothing to create.

### B. `go install` (needs Go; Google login is BYO)

```bash
go install github.com/Tai-ch0802/capy-music/cmd/capy@latest
```

Binaries built from source (`go install`, your own `go build`, `capy update --dev`) **have no built-in Google client**: `capy auth login google` runs a wizard that walks you through creating your own OAuth client (see the Google section below). Homebrew / Scoop / winget aren't planned.

### Updating

- `capy update`: asks GitHub Releases for the latest release, downloads this platform's file, verifies its SHA-256 against `checksums.txt` and runs the new binary's `--version` to make sure it works, and only then replaces the current binary; if any step fails, the old one is left untouched. Run from a dev build, it switches you to the release (which then has the built-in Google client).
- `capy update --dev`: fetches the latest commit on the main branch, rebuilds it with `go install` and replaces the current binary (needs the Go toolchain; about 20 seconds the first time).

Before updating, run `capy pl pull --dry-run` once: if it exits 3 (Drive incomplete), don't update yet. When the schema of the local cache `state.db` changes, the old file is kept as `state.db.v<old version>`, but the new capy doesn't read it; and the way out that `pl pull` offers when it stops on "Drive incomplete", `capy drive init --from-local`, reads the new, empty cache — so neither side has your data. If you do hit this: get the previous binary from Releases, rename `state.db.v<old version>` back to `state.db`, run `capy drive init --from-local` with the old binary to fill in Drive (the new version understands Drive files written with the old schema), then update again. If in doubt, run `capy export > backup.json` first.

## Language

capy's interface is in English by default (earlier versions were Chinese-only, so upgrading switches you to English). To switch to Traditional Chinese:

```bash
capy config set language zh-TW    # back to English: capy config set language en
```

The language menu at the bottom of the web UI's sidebar (`capy --web`) changes the same setting: it runs this command for you and reloads the page when it succeeds. The setting is `language` in `config.json`; `en` and `zh-TW` are supported for now, and capy doesn't detect your operating system's language. Cobra's own text stays in English in every language: the help headings (`Usage:`, `Available Commands:`, `Flags:`), the closing `Use "capy [command] --help" …` line, the `(default …)` after a flag's description, the built-in `help` and `completion` commands' descriptions, the descriptions of `-h, --help` and `-v, --version`, and cobra's own argument and flag errors (`unknown command …`, `unknown flag: …`, `accepts 1 arg(s), received 0`). Command names, flags, TSV column names, `action` / `dir` values, `reason_code`, exit codes, table headers and the values in `auth status --json` are the same in every language, so scripts don't need to change. To add a language, see [`internal/i18n/README.md`](internal/i18n/README.md).

## Spotify: create your own app (free, about 2 minutes)

Spotify's developer policy limits each app to 5 users, so you use your own app:

1. Open https://developer.spotify.com/dashboard → Create app (any name)
2. Add this Redirect URI, copied exactly (**not localhost**): `http://127.0.0.1:8888/callback`
3. Check Web API → Save → copy the Client ID
4. Run `capy auth login spotify` (the wizard guides you; in non-interactive environments use `--client-id`)

Requires Spotify Premium (both remote playback control and Development Mode require it).

## Apple Music: copy your own web token

> ⚠️ **Not officially supported by Apple.** These tokens belong to Apple's web player, and Apple may change them at any time, which stops them working (when that happens, run `capy auth login apple` again). Using a third-party tool to access Apple Music is at your own risk under Apple's terms of service. capy only guides you and **never extracts** anything from your browser.

1. Open https://music.apple.com in a browser and sign in
2. Open DevTools → Network, filter by `amp-api`, click any request → Request Headers
3. Copy two values: `authorization` (`Bearer eyJ…`) and `media-user-token`
4. Run `capy auth login apple` and paste them when the wizard asks (in non-interactive environments, use the `CAPY_APPLE_DEVELOPER_TOKEN` / `CAPY_APPLE_USER_TOKEN` environment variables and add `--i-understand`)

Requires an Apple Music subscription. Playback control works only on macOS (through the Music app); search and playlists work on both macOS and Windows. capy can create playlists, and it only edits (adds, removes and reorders tracks, renames) playlists you created yourself. Whether tracks added to a playlist are also added to your Apple Music library depends on your settings in Apple Music — that's Apple's behavior, and capy doesn't add them separately. On the account tested on 2026-09-23, the playlists already had many songs that weren't in the library, and the songs capy added to playlists didn't go into the library either.

## Local library (optional): M3U playlists + library.json

```bash
capy config set local_root ~/Music/capy      # holds your *.m3u8 (or .m3u) playlists and library.json
capy pl list --provider local
capy pl link Commute local:Commute.m3u8      # just the file name; the link id carries this device's id
```

You maintain `library.json` yourself (capy doesn't read audio tags): `{"schema_version": 1, "tracks": {"relative/path": {"title", "artists": [], "album", "duration_ms", "isrc"}}}`. Each line of a playlist is a path relative to the playlist file, and `#EXTINF` is used as a fallback title only when the library doesn't have that track. Only tracks with an `isrc` are matched to Spotify / Apple automatically; the rest rely on `capy resolve`'s fuzzy matching (on the local side, search matches within the library and doesn't use the network).

**The local library is tied to a device**: a link records "this file on this device". `pl pull / push / sync` on other devices skip it (stderr says which device it belongs to) and don't delete the link. A playlist can be linked to only one device's M3U at a time — to let another device lead (or after reinstalling), run `capy pl link` again on that device; it takes over and tells you which device had it before. `pl push` / `pl sync` write back to the M3U by **rewriting the whole file** (`#EXTM3U` plus one path per line): `#EXTINF` lines and comments written by other tools are dropped. Renaming the playlist isn't written back to the file name (the file name is its id, and changing it makes it a different playlist — to rename it, rename the file yourself and run `pl link` again).

## Google Drive sync (optional): log in to Google

After `capy auth login google`, your playlists sync to the app data folder in your Google Drive (neither other apps nor you can see what's in it; it only uses your Drive storage). capy asks for only three permissions: `openid`, `userinfo.email` and `drive.appdata`. What each one is used for, where the data is stored, and how to revoke access and delete the data are in the [privacy policy](https://capy.taislife.work/en/privacy) — the English version of the page Google's consent screen links to.

- **Binaries downloaded from GitHub Releases** include the project's own Google client: just run `capy auth login google`.
- **`go install` or your own build** has no built-in client, and the `auth login google` wizard walks you through creating your own (free, about 5 minutes):
  1. https://console.cloud.google.com → create a project → APIs & Services → enable "Google Drive API"
  2. Google Auth platform → Branding: fill in the app name and support email; set Audience to External
  3. Data Access: add only these three scopes — `openid`, `userinfo.email`, `drive.appdata` (adding Gmail or similar triggers a security assessment)
  4. Create an OAuth client of type "Desktop app"; the secret is shown only once, when you create it, so copy it right away
  5. ⚠️ Under Audience, click "Publish app" to switch to In production — if it stays in Testing, the refresh token expires after 7 days and you get logged out for no obvious reason
  6. Paste the Client ID and Client secret into the wizard. In non-interactive environments, use `--client-id` / `--client-secret` or `CAPY_GOOGLE_CLIENT_ID` / `CAPY_GOOGLE_CLIENT_SECRET`.

Either way, `--client-id` / `--client-secret` always override the built-in values. Your own client's secret goes only into the OS keychain. `capy auth status` shows the email of the Google account you're logged in with and this device's `device_id`; `capy auth logout google` deletes the token and your own client's secret.

## Login status for scripts: `capy auth status --json`

`capy auth status` is for people (it follows the interface language); scripts should use `--json`. Fields are only ever added, never changed; enum values are fixed English strings and **never translated**; times are UTC in RFC 3339; fields without a value are left out. **The output never contains the value of any token or secret**, and the client ID is reported only by status or source (`internal/cli/auth_status_json_test.go` seeds every kind of credential with sentinel values and asserts that none of them appear).

```json
{
  "spotify": { "state": "ok", "client_id": "set" },
  "google": { "state": "ok", "client": "builtin", "access_token_expiry": "2026-09-23T09:00:00Z", "email": "you@example.com", "device_id": "…" },
  "apple": { "state": "ok", "developer_token": "ok", "developer_token_expiry": "2026-11-01T00:00:00Z", "user_token": "ok", "storefront": "tw" }
}
```

| Field | Values |
|---|---|
| `spotify.state` | `ok` logged in / `missing` not logged in / `keychain_error` the keychain can't be read (needs fixing; it doesn't mean logged out) |
| `spotify.client_id` | `set` / `missing` / `malformed` (the value in the config isn't 32 hex characters) |
| `google.state` | `ok` / `missing` / `keychain_error` |
| `google.client` | `config` your own client / `builtin` built into the release / `none` no client yet |
| `google.access_token_expiry` | when the stored access token expires; capy renews it by itself, so this is **not when your login expires** |
| `google.email`, `google.device_id` | the Google account you're logged in with, and this device's id |
| `apple.state` | both tokens combined: `keychain_error` > `expired` > `missing` > `ok` (the first one that applies) |
| `apple.developer_token` | `ok` / `missing` / `expired` / `keychain_error`; `developer_token_expiry` is given when it's `ok` or `expired` |
| `apple.user_token` | `ok` / `missing` / `keychain_error` |
| `apple.storefront` | e.g. `tw` |

## Common commands

```
capy search yellow [--provider apple]
capy play yellow                        # unified search: tracks, artists' top tracks, my playlists; opens a picker when ambiguous
capy play coldplay / capy play Commute  # artist = play their top tracks (Spotify Development Mode apps can't get top-tracks, so capy falls back to a search sorted by popularity); exact playlist name = play the playlist (run capy pl list once first)
capy play --type track yellow           # for scripts: deterministic, always plays the first result; the prefixes artist: / pl: / track: do the same
capy play --pick                        # open the picker right away (playlists and recent items from the local cache)
capy                    # plain capy (in a terminal) = interactive mode; under a pipe / cron it still prints help
capy --web              # use capy from your browser: binds to 127.0.0.1 only and prints a one-time URL at startup; every command can run on the page
capy pause / next / prev / now / devices
capy seek 1:23          # jump to a position in the track; also accepts h:mm:ss (1:05:30) and plain seconds (83)
capy vol 40             # volume 0-100
capy pl list / capy pl show <name|ID>
capy pl link Commute spotify:<playlist ID or name>   # link a master copy (created if it doesn't exist) to a platform playlist; only explicit links count, no automatic name matching
capy pl link Commute spotify --create                # create an empty private playlist with the master copy's name on Spotify / Apple Music, then link it (not for local); for copying playlists, see below
capy pl unlink Commute spotify
capy pl show / link / unlink / pull / push / sync / dedup   # with no playlist name in a terminal = open a picker (link in three steps, and the second step can also create a new empty playlist on Spotify / Apple Music; unlink in two); under a pipe / cron you still get the usual argument error
capy pl pull Commute [--dry-run] [--yes] [--force] / capy pl pull --all [--provider spotify]   # platform → master copy → Drive; changes are listed first and written only after you confirm (log in with capy auth login google first)
capy pl push Commute [--dry-run] [--yes] [--force] / capy pl push --all [--provider spotify]   # master copy → platforms (Spotify, Apple Music, local library); needs an earlier pull and no unpulled changes on the platform
capy pl sync Commute [--dry-run] [--yes] [--force] / capy pl sync --all [--provider spotify]   # one round of pull, then push: one table, one confirmation; this is the one for cron
capy pl dedup Commute [--dry-run] [--yes] [--force]   # remove duplicate tracks from the master copy (same platform id or same ISRC; keeps the first copy, order unchanged), then push to the writable platforms; writes nothing when there are no duplicates
capy pl dedup apple:Chill                            # read the platform playlist directly and only report which tracks are duplicated (doesn't touch Drive, needs no link); to have capy remove them, use the form above
capy migrate Roadtrip --from spotify --to apple[:existing-playlist] [--dry-run] [--yes]   # move a playlist to another platform (either direction; new, or into an existing one; order unchanged, only adds, source untouched); see "Copy a playlist across platforms" below
capy resolve [Commute] [--provider apple] [--dry-run] [--yes]   # map tracks to their ids on each platform: ISRC lookup → fuzzy match; ≥85 is written automatically, the rest go to the review queue
capy resolve --review                     # review the queue item by item in a terminal (accept / skip / search manually / pin as unavailable / keep the current one)
capy resolve pin <cid> apple:<id|none> [--yes]   # pinning for scripts; none = this platform doesn't have the track; merges when the id already belongs to another cid (needs --yes without a TTY)
capy export > backup.json                 # escape hatch: this computer's canonical data (the Drive files merged into one), without depending on Drive
capy drive init --from-local [--dry-run] [--yes]   # escape hatch: when Drive is empty or partly lost, restore the missing files from this computer's state.db
capy doctor [--provider apple]
capy config set default_provider apple   # no need to pass --provider every time after this; also config get / list
capy config set language zh-TW           # interface language (default en); see "Language" above
capy update [--dev]                      # see "Updating" above
```

### Interactive mode

Running `capy` in a terminal with no arguments opens interactive mode. When the terminal is big enough (at least 30 rows tall), the capybara stays at the bottom of the screen and keeps moving (blinking now and then, flicking its ears, and eating a stalk of hay every so often; it steps aside when you open the menu with `/`). In a smaller terminal it plays for about three seconds at the start (press any key to skip) and then freezes into the scrollback. If you don't want any animation (screen readers, slow SSH connections, screen recordings), set `CAPY_MOTION=never`: the capybara is drawn still and never moves.
Apart from the capybara, the TUI only manages the **bottom four lines**: a divider, what's playing, the input line and key hints.

```
-------------------------------------------------------------------------------
  ▶ Yellow · 1:23 / 4:09 · MacBook Pro · volume 60
> pl sync Chill
  space play/pause · ←→ ±10 s · / commands · ? keys · q quit
```

Command echoes, errors and non-zero exit codes go into the **scrollback**, where they stay: you can scroll back and copy them. Everything above those four lines is the terminal's own scrollback, and the TUI never redraws it. Press `?` to print the full key list there too.

Press `/` to open the command menu: type to filter, `↑↓` to select, `Tab` or `⏎` to **put the command on the input line** so you can add arguments, `Esc` to close it.
Once you've typed a complete command (e.g. `/pl show Chill`), the menu closes and `⏎` runs it — the leading `/` is removed for you.
The menu grows upward, drawn over the scrollback, and isn't inserted into the history. Its list comes straight from cobra's command tree, so it can't drift from the implementation.
When the menu isn't open, `↑↓` steps through the commands you typed this session (kept in memory only; gone when you quit).

Commands you type **don't run in the same process**: capy runs itself again, so each subcommand keeps all its usual behavior
(tables, pickers, confirmation prompts). Put playlist names with spaces in double quotes: `pl show "Morning Commute"`.

You can open it before logging in: the status area explains what's missing, and you can type `auth login spotify` right on that line.

Interactive mode opened with `capy --provider apple` passes `--provider` on to the subcommands you type on that line (only to commands that accept it),
so the whole screen stays on one platform.

The color is a cool geek green (`GeekGreen` in `internal/ui/theme.go`); the seam for changing colors is that `Theme` struct,
which is where switching will be added later.


Without a TTY (pipe / cron), every command prints plain-text TSV that you can `cut -f` directly. When `play` gets an ambiguous query without a TTY, it exits 2 and prints the candidates (`type\tid\tlabel\tdetail`) — it doesn't play and doesn't ask — so scripts should use `--type` or a prefix. In a terminal, tables are aligned by display width, and cells that don't fit **wrap instead of being truncated** (the ID column is always complete, and titles shrink last). When a table is wider than the terminal, a **pager** opens first (one row per line; `←→` scroll sideways, `↑↓` up and down, `g`/`G` first/last, `q` quit; `Ctrl-C` aborts: the command exits 130 and asks nothing further), and after you leave it the table stays in the scrollback in wrapped form. Commands run with `--yes` don't open the pager; `CAPY_PAGER=never` never opens it (for script / expect, or CI that provides a pty — "a TTY but no person"). The config directory can be overridden with `CAPY_CONFIG_DIR`.

A command ended by SIGINT / SIGTERM exits `130` / `143` (the shell convention 128+n), so `capy now --watch; echo $?` can tell "killed" from "finished". In interactive mode and `now --watch`, leaving with `q` / `Esc` is `0` and with Ctrl-C is `130` (in those two screens Ctrl-C is a key press, not a signal, but the exit code matches a real SIGINT and the pager), and being `kill`ed is `143`. `capy --web` is meant to be stopped with Ctrl-C, so its normal shutdown is 130 (launchd / `kill` gives 143). Other commands interrupted midway print the same stderr message as before; only the exit code changes from 1 to 130 / 143. capy exits with 130 / 143 by itself (`$?` looks the same as being killed by the signal, but to `waitpid` it's a normal exit), so when you run `capy --web` under a supervisor such as launchd, its shutdown exit code isn't 0 — don't set "restart on non-zero" on it. Exit codes a command already uses to say something aren't overridden: exit `2` / `3` below, and the exit `1` for "the platform was partly written" (the message says how many tracks were written).

The exit codes of `capy pl pull` are an external contract (cron relies on them): `0` no changes, or applied successfully; `1` error; `2` changes pending (`--dry-run`, no TTY without `--yes`, or cancelled in the terminal); `3` stopped by a safety check (the Drive appdata is incomplete, or a single playlist would lose >10 tracks, or >30% and >3 tracks). `--yes` only skips the confirmation; `--force` only overrides the removal threshold and works only with a single playlist (`capy pl pull <name> --force`, not with `--all`: the safety check is lifted for one playlist at a time). Neither lets "Drive incomplete" through — the way out of that one is `capy drive init --from-local`. Without a TTY the change set is headerless TSV: `action provider playlist pos cid provider_id title artists reason reason_code` (`reason` is for people and follows the interface language; scripts should read `reason_code`, a fixed code such as `added_on_platform` or `removed_on_platform` — the full table is [below](#reason_code-table)). "How many changes this time" is the number of lines, not the exit code. Writes always go to Drive first, then to the local `state.db`; `state.db` is only a cache, and if you delete it, the next pull rebuilds it from Drive.

`capy pl push` goes the other way (master copy → platforms), with the same shape and exit codes as `pl pull`, plus two preconditions that neither `--yes` nor `--force` overrides: this device has pulled that platform playlist before (otherwise the platform playlist could be wiped), and the platform has no changes that haven't been pulled (otherwise it would overwrite what you just changed on the platform) — run `capy pl pull` first. The change set's `action` adds `skip` (in the master copy but not on the platform, and there's no id for this platform: run `capy resolve` first); it isn't a change, so leave it out when counting lines. Writing to Spotify replaces the whole playlist (the first 100 tracks in one request, then batches of 100), so the platform's "date added" is reset; Spotify playlists containing local files can't be pushed yet (local files can't be added back). If a write fails partway, capy exits 1 and says how many tracks were written; run it again to write the rest. If the platform changes after you confirm but before capy writes (your phone adding songs at the same moment), that playlist isn't written and capy exits 3. Writing to Apple Music: when the changes only add tracks, capy uses Apple's documented add endpoint (batches of 100, appended at the end); when there are removals or reorders, it replaces the whole playlist (through an endpoint Apple's web player itself uses, which Apple makes no official commitment to); a rename changes only the name and keeps the description. Only playlists you created yourself can be written — Apple-curated playlists, Favorite Songs and Purchased Music are refused with zero writes. capy currently doesn't write **collaborative playlists** at all (Apple answers a whole-playlist replace on them with a 500): push / sync skip that cell when listing changes and say why, and explicitly pushing one with `--provider apple` exits 3; edit it by hand in the app, or copy it to a regular playlist first and link that (untested; it usually works). Whether tracks added to a playlist are also added to your Apple Music library depends on your Apple Music settings. Removals and reorders also replace the whole playlist, so the order always follows the master copy. The Music app on a Mac can show when each song was added to a playlist ("Date Added"); whether that gets reset as a result isn't verified yet (Spotify's date added is, see above). For songs no longer available in the Apple Music store, the add request still succeeds but nothing is actually added; capy re-reads the playlist after writing and warns when the content differs from what it expected.

`capy pl sync` is one round under a single lock, "for each playlist, first pull from each platform, then push to each platform" (providers in lexical order), with one table and one confirmation; exit codes as above. Pulling first automatically meets push's two preconditions, and the push half reuses the platform playlists the pull half just read instead of reading them again. The TSV has one more column in front than pull / push: `dir` (`pull` / `push`). With `--dry-run`, the push half is computed from the master copy as it would be after the pull is applied, so you see the whole round; `--provider spotify` runs one platform only; pointed at a platform that can't be written (e.g. another device's local playlist), it runs only the pull half (stderr says so). The removal threshold is checked per (playlist, platform), and if any of them stops, the whole round writes nothing (if you override it with `--force`, the removals absorbed by the pull are pushed in the same command to every platform linked to that playlist — run `--dry-run` first). But when one playlist can't be pushed to one platform (e.g. it contains local files), only that cell's push half is skipped (stderr says so) and the rest goes ahead — so `capy pl sync --all --yes` in cron won't get stuck on one playlist; when it exits 2 / 3, look at it in a terminal.

`capy pl dedup` removes duplicate tracks from a playlist. A duplicate is the same platform id or the same ISRC (a single and its album version count as one track); the first occurrence is kept and later ones are removed, and **the relative order of everything else doesn't change — a playlist's order is your memory of adding the songs, and no path in capy sorts or shuffles it** (dedup only removes the later copies; sync only reorders when the platform itself was reordered). The form `capy pl dedup apple:Chill` reads the platform playlist directly and only prints a report (without a TTY it's TSV: `pos id title artists reason reason_code`, where `pos` (0-based) is the position of the duplicate — the copy to remove — and `reason` names the position of the copy that's kept, and `reason_code` is `dup_id` or `dup_isrc`; the exit code is 0 whether or not there are duplicates). It doesn't touch Drive and needs no link — to have capy remove the duplicates, link the playlist to a master copy and use the next form; `--yes` / `--force` / `--dry-run` / `--provider` with this form are an error (they're flags for the master-copy path). Given a master copy's name (`capy pl dedup Commute`), it's a `pl sync` round with one more step in the middle: pull first, dedup the master copy, then push to remove the extra copies from the writable platforms. One table (`dir` has one more value, `dedup`, and in those rows `pos` is the position in the master copy), one confirmation, exit codes as for `pl sync`. Nothing is written when neither the master copy nor the platforms checked this time have duplicates (other changes the pull half sees are left for `pl sync`, and stderr says so; platforms not selected by `--provider`, or that couldn't be read, aren't checked this time — stderr says that too — and aren't counted as "no duplicates"). The removal threshold is checked separately for dedup and push; `--force` overrides it (the removed copies are pushed in the same command to every writable platform the playlist is linked to). Copies left on platforms capy can't write to are listed on stderr for you to delete by hand, and the next pull won't add them back to the master copy. When the same ISRC has different ids, the master copy keeps the first one, and which id stays on the platform depends on how the copies pair up (of two adjacent copies, the later one stays).

`capy resolve` does what `pl pull` doesn't: when a playlist is linked to two platforms and its tracks were pulled in from only one side, it finds the other side's ids — first by ISRC lookup (confidence 95), otherwise by fuzzy matching on title + artists + duration (0–100; capped at 84 when one of the titles has live / remix / acoustic / cover or similar, or the durations differ by more than 3 seconds). ≥85 is written automatically, through the same lock, gate and write order as `pl pull`; the rest are printed as a review queue — a candidate that already belongs to another track always goes to the queue, because **only a person decides a merge**. Exit codes: `0` nothing to write, or written (still 0 when the queue has items, so `capy resolve --yes` in cron doesn't fail just because a few tracks never resolve); `1` error; `2` there are mappings to write automatically but they weren't confirmed (`--dry-run`, no TTY without `--yes`, cancelled). TSV without a TTY: `action cid provider provider_id confidence source title artists reason reason_code` (`action` ∈ `map` to be written / `review` needs a person / `conflict` different ids observed for the same ISRC). `--review` reviews the queue item by item in a terminal and writes your decisions as pins (automatic runs don't change them afterwards); without a TTY it only prints the queue and exits 2 — scripts use `capy resolve pin`. When one resolve run makes more than 200 API calls, it warns on stderr (unresolved tracks are looked up again every time; there's no negative cache yet). When a platform's authorization no longer works, only that platform is skipped (stderr says so) and the others are resolved as usual; a track whose lookup fails is listed as `review` with the reason, and is looked up again next time. At the end, `pl pull` tells you when "N tracks haven't been matched on <provider> yet".

Two escape hatches. `capy export` reads only the local `state.db` (it touches neither Drive, the network nor the keychain) and prints the Drive files merged into one to stdout: the keys are file names (`manifest.json`, `tracks.json`, `pl__<pid>.json`, `dev__<device_id>.json`) and the values are those files' contents, indented (compacted again, they're byte-for-byte what's on Drive); with no local data it exits 1 and prints nothing. It opens `state.db` read-only: a broken file isn't deleted, a version mismatch isn't renamed, a fresh machine gets no new file, and the whole export is one consistent snapshot (running it alongside a cron `pl pull` won't tear it). `capy drive init --from-local` is the way out after `pl pull` stops with exit 3 on "Drive incomplete": it creates only the files missing on Drive, never overwrites the ones still there, leaves the local cache alone, and doesn't upload other devices' `dev__` files on their behalf. It lists the files to create first (without a TTY, TSV `action file`) and uploads only with `--yes` or after you confirm in a terminal; `--dry-run` only lists. The confirmation shows the Google account you're logged in to: logged in to the wrong account, it would upload your whole library to someone else's appdata.

### reason_code table

The last column of the TSV from `pl pull` / `push` / `sync` / `migrate` / `dedup` / `resolve` is always `reason_code`: a fixed English code that is **never translated, only added to, never changed**. The column before it, `reason`, says the same thing for people and follows the interface language. Scripts that need to know why should read this column. The same code can appear in different commands (for example `push`). The TSV of `pl sync` / `migrate` / `pl dedup` has an extra `dir` column in front; look up each row by its `dir`: `dir=pull` rows as in `pl pull`, `dir=push` rows as in `pl push`. When you add a code, update this table and the one in [README.zh-TW.md](README.zh-TW.md#reason_code-代碼表) in the same PR (`internal/cli/reason_code_test.go` checks both).

| Command (rows) | action | reason_code | Meaning |
|---|---|---|---|
| `pl pull` (and `dir=pull` rows) | `add` | `added_on_platform` | Added on the platform; added to the master copy |
| | `remove` | `removed_on_platform` | Removed on the platform; removed from the master copy |
| | `move` | `moved_on_platform` | Moved on the platform |
| | `rename` | `renamed_on_platform` | The playlist was renamed on the platform |
| | `unlink` | `playlist_gone` | The playlist is gone from the platform; unlinked |
| `pl push` (and `dir=push` rows) | `add` | `push` | In the master copy but not on the platform; pushed |
| | `remove` | `removed_in_master` | Removed from the master copy; removed from the platform |
| | `move` | `moved_in_master` | Moved in the master copy |
| | `rename` | `renamed_in_master` | The master copy was renamed |
| | `skip` | `no_mapping` | No id for this platform, so not pushed this time: run `capy resolve` first |
| | `skip` | `unpushable` | Has an id but can't be pushed (a local file, only in the library, or the file isn't on this computer); add it on the platform by hand |
| `migrate`'s `dir=migrate` rows (`action` is always `add`; the code says whether the track can be moved) | `add` | `push` | Can be moved (the web UI's move wizard relies on this) |
| | `add` | `no_mapping` | Not matched on the target platform; not moved this time |
| | `add` | `unpushable` | Has an id on the target platform but can't be pushed; not moved this time |
| `pl dedup <master copy>`'s `dir=dedup` rows | `remove` | `duplicate` | A later duplicate in the master copy (the first copy is kept) |
| `pl dedup <platform>:<playlist>` (report only, no action column) | | `dup_id` | Same platform id |
| | | `dup_isrc` | Same ISRC |
| `resolve` | `map` | `isrc` | Found by ISRC lookup; written automatically |
| | `map` | `fuzzy` | Fuzzy match ≥85; written automatically |
| | `review` | `no_candidate` | No candidate found |
| | `review` | `low_score` | The candidate scored below 85 |
| | `review` | `candidate_taken` | The candidate already belongs to another track (only a person decides a merge) |
| | `review` | `candidate_assigned` | The candidate was already given to another track in this run |
| | `review` | `lookup_failed` | The lookup failed; it's tried again next time |
| | `conflict` | `isrc_conflict` | Different ids observed for the same ISRC |

### Web UI

```bash
capy --web
```

Starts a web UI on your computer, bound only to `127.0.0.1`, and prints one line with its URL (carrying a one-time token) at startup; in a terminal it also opens the browser for you, and in a pipeline or in the background it only prints the URL.

**It opens on "Move": moving a playlist from one platform to another, in three steps.** Choose the source and the destination (each platform's connection status is visible at a glance, and you can connect one on the spot) → pick a playlist and decide whether to create a new one or add to an existing one → review which songs will be moved; nothing is written until you confirm. Matching the songs shows real progress ("Matching songs 37 / 120"), and songs that can't be moved are listed one by one. It never deletes the source, only adds, and keeps the order; it's free with no song limit, because it runs on your own computer with your own accounts.

The limits, up front: moving into the local library can only add to an existing M3U file (Spotify and Apple Music can both **create a new playlist** at the destination); whether songs moved into Apple Music are also added to your Apple Music library depends on your Apple Music settings, and they can only go into playlists you created yourself; Spotify needs your own app and Apple needs a token you copy from the web player yourself, so connecting your accounts the first time takes a few minutes — in exchange, nobody holds your credentials for you. M3U playlists on your disk can be moved to Spotify or Apple Music too.

The other pages: **My playlists**, **Sync**, **Search** and **Accounts**; under "Advanced" are the **Console** (run any subcommand, just like in a terminal), **ISRC lookup** (asks all three platforms at once) and **Diagnostics**. The bottom of every page always has the **now-playing bar** (the playback panel): what's playing, progress and playback controls — everything `capy now` shows. Every action on the page is a capy command underneath, and the Console keeps the full log; anything that needs confirming is always asked by the command itself, and the page never answers for you. The sidebar also has the language menu (see [Language](#language)).

```bash
capy --web --port 43117   # pick the port (random by default; 8888, 80 and 443 aren't allowed)
```

Good to know:

- **It's only on your computer.** It binds only to `127.0.0.1` and isn't a LAN service; each start generates a one-time token, and the URL stops working when the process ends. The page uses no cookies.
- **One command at a time.** A second command is refused (the page says another command is running), because it shares the same Drive and local data as the terminal. While a command runs, an extra status row appears at the bottom: what it's doing, how long it's been running and the latest line it printed (while matching songs, how far it has got); the **Stop** button next to it stops it at any time (Ctrl-C on the Console's command line works too). It stops at the same points as Ctrl-C in a terminal; a command you've already allowed to write needs two presses, because stopping halfway may leave it partly written.
- **Credentials never pass through the page.** Secrets you enter in a wizard go only from the browser into the process and then into the keychain, never into events, logs or URLs; token values in command echoes are always masked as `***`.
- **A few commands aren't offered on the web**: the `debug` group, `--client-secret` / `--developer-token` / `--user-token` (use the wizard instead) and `now --watch` (just watch the panel). `capy update` can run, but after updating, this web process is still the old version and asks you to restart it.
- **On Windows, the first start** may show a firewall prompt. It listens only on `127.0.0.1`, so choosing "Cancel" doesn't affect local connections.

Interactive mode in a terminal (`capy` with no arguments) **runs capy itself again**, so every subcommand keeps its usual form; the web UI runs commands **in the same process**, and the page answers their prompts. The two paths differ because a terminal already has a TTY to hand to a child process and a browser doesn't; and the web UI's tables and prompts have to reach the browser in structured form, so it plugs into the same seams the commands use for tables and prompts.

### Copy a playlist across platforms (either direction; e.g. Apple Music → Spotify)

One command (log in to Spotify, Apple Music and Google first — `capy auth status` shows all three; put playlist names with spaces in double quotes):

```
capy migrate Roadtrip --from apple --to spotify            # create a private playlist with the same name on Spotify and move the Apple tracks into it
capy migrate Roadtrip --from apple --to spotify:Driving    # or add to an existing Spotify playlist, after the tracks it already has
capy migrate Roadtrip --from spotify --to apple            # the other direction: create a playlist with the same name on Apple Music and move the Spotify tracks into it
capy migrate                                               # no arguments in a terminal: pick the source platform, the playlist, the target platform, and an existing playlist or a new one, step by step
```

It does the seven steps of the manual flow below in one go: read the source (not linked, not touched) → settle the master copy and the target (an existing target is pulled into the master copy first) → append, in the source's order, the source tracks the target doesn't have yet (tracks with the same platform id or the same ISRC are skipped, and the source's own duplicates are kept only once) → find each track's id on the target platform (ISRC lookup → fuzzy match; in a terminal you can review the unmatched ones on the spot) → one table (`dir` has three values: `pull` / `migrate` / `push`), one confirmation → create the playlist on the target only when needed → push. To look without doing anything, use `--dry-run`; scripts need `--yes` (without a TTY and without it, capy exits 2 and creates no playlist). **Order**: the target's existing order is the prefix, and the source's tracks follow in the source's order. **Only adds**: the source is never touched, and nothing is removed from the target either; when the target has unsynced removals / reorders / renames, capy stops with exit 3 — run `capy pl sync` first. Unmatched tracks aren't pushed this time; the table says so, and the last lines give you the `capy resolve --review` and `capy pl sync` commands to finish them. Afterwards only the target is linked to capy's master copy (the source isn't linked: it's a one-time copy); to follow the source's changes later, the last lines also give you the `capy pl link` + `capy pl sync` commands. The exception: when the master copy is already linked to the source (you were halfway through the manual flow below), the source half is pulled into the master copy too, the master copy wins, and the last lines say that both are linked. local can only add to an existing file (`--to local:<file-name>`); whether tracks moved into Apple Music are also added to your Apple Music library depends on your Apple Music settings (Apple's behavior), and they can only go into playlists you created yourself (not Apple-curated ones); songs no longer available in the Apple Music store can't be moved: the table may still list them, and capy warns when it re-reads the playlist after writing.

> ⚠️ Creating playlists (`POST /me/playlists`) and pushing tracks (`PUT /playlists/{id}/items`) are implemented from Spotify's official documentation as of 2026-02 and **haven't been verified on a real account yet**. If you get a 404, or a created playlist shows up as public in the app, please report it.

**What happens behind the scenes (the manual flow; use it when you want both sides to stay in sync)**: link both platforms to the same master copy, then push. The example copies the Apple Music playlist "Roadtrip" to Spotify.

```
capy pl link Roadtrip apple:Roadtrip                # 1. link a master copy (created if it doesn't exist) to the Apple playlist
capy pl link Roadtrip spotify --create              # 2. create an empty private playlist with the same name on Spotify and link it to the same master copy
capy pl pull Roadtrip                               # 3. without --provider: the Apple tracks come into the master copy; the empty Spotify playlist gets its base recorded
capy resolve Roadtrip --provider spotify            # 4. find each track's id on Spotify (ISRC lookup → fuzzy match)
capy resolve --review                               # 5. only needed when the previous step listed a review queue: review item by item
capy pl push Roadtrip --provider spotify --dry-run  # 6. see what would be pushed first
capy pl push Roadtrip --provider spotify            # 7. push for real
```

- **Don't skip step 3.** push requires that this device has pulled that Spotify playlist; the playlist just created in step 2 has no base yet, and pushing straight away stops with exit 3.
- **Nothing gets deleted.** It's the Spotify side's first pull (no base means no removals), everything pushed is an addition, and the removal threshold won't trigger.
- **Not everything necessarily comes across.** Apple tracks with a catalog match carry an ISRC and are looked up exactly on Spotify (confidence 95, written automatically); tracks you uploaded yourself that exist only in your library have no ISRC and rely on fuzzy matching of title, artists and duration, and below 85 they go into the review queue; songs Spotify doesn't have at all get pinned as unavailable in `--review`. The `skip` rows in step 6's table are the tracks that won't be copied this time.
- **The playlist `--create` makes has the master copy's name**, so push won't add a `rename` row. If you already have your own playlist with the same name on Spotify (e.g. you created it in the app earlier), it stops and gives you the command to link that one instead; playlists by other people that you follow can't be linked and don't count. In a terminal you can also just type `capy pl link` and choose "Create a new empty playlist on spotify" at the second step.
- **Afterwards both sides stay linked.** When either side changes, run `capy pl sync Roadtrip` to carry the change over. For a one-time copy, run `capy pl unlink Roadtrip apple` when you're done.
- **The other direction (Spotify → Apple) works the same way**: `capy migrate Roadtrip --from spotify --to apple`, or swap the two platforms in the manual flow (`capy pl link Roadtrip apple --create`). On the Apple side, adding tracks uses Apple's documented endpoint, and removals and reorders use the endpoint the web player itself uses (Apple makes no official commitment to it; details in docs/ARCHITECTURE.md §1.2). capy writes only playlists you created yourself, and whether tracks added to a playlist also go into your library depends on your Apple Music settings.

## Shell completion (TAB lists playlist names and recent searches)

```
# zsh (put this in ~/.zshrc)
source <(capy completion zsh)
# bash
source <(capy completion bash)
# fish
capy completion fish | source
# PowerShell (put this in $PROFILE)
capy completion powershell | Out-String | Invoke-Expression
```

Candidates come only from the local cache (`state.db`, no network): playlist names show up after you've run `capy pl list`; `capy search` / `capy play` collect recent items, and `capy history clear` clears them. Completion never touches the network or the keychain, so pressing TAB never hangs.

## Credentials and data

Your credentials are stored only in the OS keychain (macOS Keychain / Windows Credential Manager), never in config files or the cloud. The one exception is the project's own Google client: release binaries have it compiled in (`strings capy` can read it; a native app distributed to users can't hide it anyway, RFC 8252 §8.5). That's the app's own identity, not anyone's account credentials. For the architecture, platform constraints and development phases, see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) (in Traditional Chinese).

## Releasing (maintainers)

1. One-time setup: in the repo's Settings → Secrets and variables → Actions, set `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET` (the project's own Google Desktop client; they exist only in GitHub secrets and in the released binaries, never in the repo).
2. `git tag v1.2.3 && git push origin v1.2.3`: `.github/workflows/release.yml` uses GoReleaser to build the files for the four platforms, `checksums.txt` and the GitHub Release; if a secret isn't set, it fails before building. A tag with `-rc1` or similar is marked as a pre-release, and `capy update` won't pick it up.
3. Every PR's CI runs `goreleaser release --snapshot` once with fake values and runs the resulting binary to check that the injection worked, so the release config has already been tested before you push a tag.

## License

MIT, see [LICENSE](LICENSE). The release archives include a copy too.
