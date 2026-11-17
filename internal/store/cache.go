package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// 純快取的兩張表(原本的 config.Dir()/cache.json,UX 計畫 R3 併進來):各 provider 的播放清單與最近項目。
// 順序存 position、讀回依 position。「最新在前、去重、上限 50」的規則在 internal/cache 的記憶體邏輯與 ApplyCache 的
// SQL 各有一份:記憶體那份給呼叫端讀 c.Recent,SQL 那份才是寫進去的(兩者由 internal/cache 的測試釘成一樣)。

type ProviderPlaylist struct {
	ID    string
	Name  string
	Total int
}

type Recent struct {
	At       int64 // unix 秒
	Provider string
	Type     string
	ID       string
	Label    string
	Detail   string
}

// CacheDelta:一個行程這次對快取做的改動(internal/cache 記下來的)。只寫這些、不整批取代:兩個 capy 同時存時,
// 整批取代會蓋掉對方剛寫的、把剛清掉的最近項目寫回來,也會把剛登出的平台的列從一份登出前讀到的快取寫回去。
type CacheDelta struct {
	ClearRecent bool                          // 先清空最近項目(history clear)
	Playlists   map[string][]ProviderPlaylist // 這幾家的清單整個換掉(切片是空的 = 那一家清空)
	Recent      []Recent                      // 依呼叫順序加到最前,同 provider+type+id 的舊列拿掉
	MaxRecent   int                           // 加完之後最近項目最多留幾筆;0 = 不剪(不是剪光)
}

// ApplyCache 在一筆交易裡套用 d,回傳 ClearRecent 刪掉幾列。
// 交易的第一句一定要是寫入:deferred 交易先讀再寫的話,升級成寫鎖那一步 SQLite 不走 busy handler,另一個 capy 正在寫時
// 立刻回 SQLITE_BUSY(量過:4–7 µs),busy_timeout 形同虛設;第一句就寫,才會照 busy_timeout 等對方寫完。
// 所以順序是:清空 → 換清單 → 逐筆加最近項目(每筆先 DELETE 重複的那一列)→ 修剪到上限。
func (s *Store) ApplyCache(d CacheDelta) (cleared int64, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if d.ClearRecent {
		res, err := tx.Exec("DELETE FROM recent")
		if err != nil {
			return 0, err
		}
		cleared, _ = res.RowsAffected()
		for _, tbl := range []string{"tui_history", "play_journal"} {
			if _, err := tx.Exec("DELETE FROM " + tbl); err != nil && !isMissingTable(err) {
				return 0, err
			}
		}
	}
	for _, prov := range slices.Sorted(maps.Keys(d.Playlists)) {
		if _, err = tx.Exec("DELETE FROM provider_playlists WHERE provider = ?", prov); err != nil {
			return 0, err
		}
		for i, p := range d.Playlists[prov] {
			if _, err = tx.Exec("INSERT INTO provider_playlists (provider, position, id, name, total) VALUES (?, ?, ?, ?, ?)", prov, i, p.ID, p.Name, p.Total); err != nil {
				return 0, err
			}
		}
	}
	for _, r := range d.Recent { // 最新的放在目前最小的 position 前面:依呼叫順序,最後加的在最前
		if _, err = tx.Exec("DELETE FROM recent WHERE provider = ? AND type = ? AND id = ?", r.Provider, r.Type, r.ID); err != nil {
			return 0, err
		}
		if _, err = tx.Exec("INSERT INTO recent (position, at, provider, type, id, label, detail) SELECT COALESCE(MIN(position), 0) - 1, ?, ?, ?, ?, ?, ? FROM recent",
			r.At, r.Provider, r.Type, r.ID, r.Label, r.Detail); err != nil {
			return 0, err
		}
	}
	if len(d.Recent) > 0 && d.MaxRecent > 0 {
		if _, err = tx.Exec("DELETE FROM recent WHERE position NOT IN (SELECT position FROM recent ORDER BY position LIMIT ?)", d.MaxRecent); err != nil {
			return 0, err
		}
	}
	return cleared, tx.Commit()
}

// LoadCache 讀回兩張表;沒有資料時 map 為空、recent 為 nil。
func (s *Store) LoadCache() (map[string][]ProviderPlaylist, []Recent, error) {
	pls := map[string][]ProviderPlaylist{}
	if err := query(s.db, "SELECT provider, id, name, total FROM provider_playlists ORDER BY provider, position", func(r *sql.Rows) error {
		var prov string
		var p ProviderPlaylist
		if err := r.Scan(&prov, &p.ID, &p.Name, &p.Total); err != nil {
			return err
		}
		pls[prov] = append(pls[prov], p)
		return nil
	}); err != nil {
		return nil, nil, err
	}
	var recent []Recent
	if err := query(s.db, "SELECT at, provider, type, id, label, detail FROM recent ORDER BY position", func(r *sql.Rows) error {
		var x Recent
		if err := r.Scan(&x.At, &x.Provider, &x.Type, &x.ID, &x.Label, &x.Detail); err != nil {
			return err
		}
		recent = append(recent, x)
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return pls, recent, nil
}

func isMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// ForgetProvider 刪掉某個 provider 在所有本機快取表裡的列(登出時:Spotify Developer Policy I.1.b 要求使用者中斷連線時刪掉他的資料;
// 計畫 2026-09-24 §1.7 S5)。recent 的 position 會留空號,讀回依 position 排序,不影響順序。
// secure_delete:SQLite 預設只把刪掉的列標成空頁,內容還在檔案裡(strings state.db 讀得到);這裡要的是真的刪掉(#102 review)。
// 跟 DELETE 放在同一次 Exec,確保是同一條連線(PRAGMA 只作用在那條連線上)。
func (s *Store) ForgetProvider(provider string) error {
	if _, err := s.db.Exec(forgetSQL, provider, provider, provider, provider, provider); err != nil {
		if !isMissingTable(err) {
			return err
		}
		for _, q := range forgetProviderQueries {
			var args []any
			if strings.Contains(q, "?") {
				args = []any{provider}
			}
			if _, qerr := s.db.Exec(q, args...); qerr != nil && !isMissingTable(qerr) {
				return qerr
			}
		}
	}
	return nil
}

const forgetSQL = "PRAGMA secure_delete = ON; DELETE FROM provider_playlists WHERE provider = ?; DELETE FROM recent WHERE provider = ?; DELETE FROM playlist_items_cache WHERE provider = ?; DELETE FROM resolve_neg_cache WHERE provider = ?; DELETE FROM play_journal WHERE provider = ?;"

var forgetProviderQueries = []string{
	"PRAGMA secure_delete = ON",
	"DELETE FROM provider_playlists WHERE provider = ?",
	"DELETE FROM recent WHERE provider = ?",
	"DELETE FROM playlist_items_cache WHERE provider = ?",
	"DELETE FROM resolve_neg_cache WHERE provider = ?",
	"DELETE FROM play_journal WHERE provider = ?",
}

// retiredName:schema 升版時留下的舊檔(state.db.v<N>,見 retire);不含它們的 -journal / -wal / -shm。
var retiredName = regexp.MustCompile(`\.v\d+$`)

// ForgetProviderInRetired:舊檔(state.db.v<N>)裡也有這些快取表,登出時連它們一起刪(不然平台的清單名稱、最近項目、
// 清單曲目、負向配對快取與播放日誌還留在這台電腦上)。用原始的 open,不經 OpenAt——那會把舊檔當成版本不符再退役一次。
// 舊檔沒有某張表就跳過;壞掉的舊檔(打不開也讀不出來)直接刪掉——它只是 cache 的保留檔,不刪的話每次登出都會說「沒能清掉」(#102 review)。
func ForgetProviderInRetired(provider string, busy time.Duration) error {
	p, err := Path()
	if err != nil {
		return err
	}
	files, err := filepath.Glob(p + ".v*")
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range files {
		if !retiredName.MatchString(f) {
			continue
		}
		bad := false
		s, err := open(f, busy)
		if err != nil {
			if corrupt(err) {
				errs = append(errs, removeRetired(f))
			} else {
				errs = append(errs, err)
			}
			continue
		}
		for _, q := range forgetProviderQueries {
			var args []any
			if strings.Contains(q, "?") {
				args = []any{provider}
			}
			_, err := s.db.Exec(q, args...)
			switch {
			case err == nil, isMissingTable(err):
			case corrupt(err):
				bad = true
			default:
				errs = append(errs, err)
			}
			if bad {
				break
			}
		}
		errs = append(errs, s.Close())
		if bad {
			errs = append(errs, removeRetired(f))
		}
	}
	return errors.Join(errs...)
}

// removeRetired:刪掉一個壞掉的舊檔與它的 -journal / -wal / -shm。
func removeRetired(f string) error {
	var errs []error
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		if err := os.Remove(f + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// PlaylistItems:上次讀到的某份平台清單的曲目(決策 57)。version 是平台給的清單版本(Spotify 的 snapshot_id),tracks 是呼叫端
// 編好的 JSON(只放同步要用的欄位)。純快取:刪掉只會讓下一輪多讀一次平台。
type PlaylistItems struct {
	Version   string
	Tracks    []byte
	FetchedAt time.Time
}

// CachedPlaylistItems:沒有這份清單的快取回 ok=false。
func (s *Store) CachedPlaylistItems(provider, playlistID string) (PlaylistItems, bool, error) {
	var it PlaylistItems
	var tracks string
	var at int64
	err := s.db.QueryRow("SELECT version, tracks, fetched_at FROM playlist_items_cache WHERE provider = ? AND playlist_id = ?", provider, playlistID).Scan(&it.Version, &tracks, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return it, false, nil
	}
	if err != nil {
		return it, false, err
	}
	it.Tracks, it.FetchedAt = []byte(tracks), time.Unix(at, 0)
	return it, true, nil
}

// SavePlaylistItems 取代這份清單的快取,順便刪掉 expireBefore 之前記的(過期的不再用,也不留著:
// Spotify 只准暫時快取 metadata;解除連結的清單不會再被覆寫)。
func (s *Store) SavePlaylistItems(provider, playlistID string, it PlaylistItems, expireBefore time.Time) error {
	if _, err := s.db.Exec("DELETE FROM playlist_items_cache WHERE fetched_at < ?", expireBefore.Unix()); err != nil {
		return err
	}
	_, err := s.db.Exec("INSERT OR REPLACE INTO playlist_items_cache (provider, playlist_id, version, tracks, fetched_at) VALUES (?, ?, ?, ?, ?)",
		provider, playlistID, it.Version, string(it.Tracks), it.FetchedAt.Unix())
	return err
}

// ForgetPlaylistItems:這份清單剛被我們寫過——下一輪一定真的讀(不拿寫入回傳的版本記快取:寫與重讀之間別人可能又改了)。
func (s *Store) ForgetPlaylistItems(provider, playlistID string) error {
	_, err := s.db.Exec("DELETE FROM playlist_items_cache WHERE provider = ? AND playlist_id = ?", provider, playlistID)
	return err
}

// WikiEntry:capy wiki 的一份回答(決策 59)。純快取、不過期:key 是 ai.WikiCacheKey(prompt 版號 | 母語 | 歌名 | 歌手),
// 不進 Dump / Hydrate,刪掉只會讓下一次多問一次端點。Body 是 AI 的正文(不含 capy 自己印的 MV 連結、來源與免責行)。
type WikiEntry struct {
	Title     string
	Artists   string
	Language  string
	Model     string
	Body      string
	FetchedAt time.Time
}

// CachedWiki:沒有這首的快取回 ok=false。
func (s *Store) CachedWiki(key string) (WikiEntry, bool, error) {
	var e WikiEntry
	var at int64
	err := s.db.QueryRow("SELECT title, artists, language, model, body, fetched_at FROM wiki_cache WHERE key = ?", key).Scan(&e.Title, &e.Artists, &e.Language, &e.Model, &e.Body, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return e, false, nil
	}
	if err != nil {
		return e, false, err
	}
	e.FetchedAt = time.Unix(at, 0)
	return e, true, nil
}

// SaveWiki 取代這首的快取(--refresh 也走這裡)。
func (s *Store) SaveWiki(key string, e WikiEntry) error {
	_, err := s.db.Exec("INSERT OR REPLACE INTO wiki_cache (key, title, artists, language, model, body, fetched_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		key, e.Title, e.Artists, e.Language, e.Model, e.Body, e.FetchedAt.Unix())
	return err
}

// ── 純本機輔助表(v7 冪等擴充:resolve_neg_cache / tui_history / play_journal)──

const (
	ResolveNegCacheTTL     = 7 * 24 * time.Hour
	TUIHistoryMax          = 100
	PlayJournalMax         = 1000
	PlayJournalDedupWindow = 10 * time.Minute
)

// ResolveNegEntry:resolve 未自動配對成功的負向快取(純本機、7 天過期,不進 Drive)。
// TrackSig 為曲目特徵簽章(如 title/artists/duration/isrc 摘要),曲目資料變動時呼叫端可主動忽略舊結果。
type ResolveNegEntry struct {
	CID        string
	Provider   string
	TrackSig   string
	CandID     string
	Score      int
	ReasonCode string
	ReasonData string
	CheckedAt  time.Time
}

// LookupResolveNeg:查詢負向快取;無紀錄或 OpenReadOnly 遇到未補表的舊 v7 檔時回 ok=false, err=nil。
func (s *Store) LookupResolveNeg(cid, provider string) (ResolveNegEntry, bool, error) {
	var e ResolveNegEntry
	var at int64
	err := s.db.QueryRow(
		"SELECT track_sig, cand_id, score, reason_code, reason_data, checked_at FROM resolve_neg_cache WHERE cid = ? AND provider = ?",
		cid, provider,
	).Scan(&e.TrackSig, &e.CandID, &e.Score, &e.ReasonCode, &e.ReasonData, &at)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return ResolveNegEntry{}, false, nil
	}
	if err != nil {
		return ResolveNegEntry{}, false, err
	}
	e.CID = cid
	e.Provider = provider
	e.CheckedAt = time.Unix(at, 0)
	return e, true, nil
}

// SaveResolveNeg:寫入負向快取並順手清理 checked_at < (CheckedAt - 7d) 的過期紀錄。
// 交易第一句即為 DELETE 寫入,確保併發寫入時遵守 busy_timeout。
func (s *Store) SaveResolveNeg(e ResolveNegEntry) (err error) {
	at := e.CheckedAt
	if at.IsZero() {
		at = time.Now()
	}
	expireBefore := at.Add(-ResolveNegCacheTTL).Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec("DELETE FROM resolve_neg_cache WHERE checked_at < ?", expireBefore); err != nil {
		return err
	}
	if _, err = tx.Exec(
		"INSERT OR REPLACE INTO resolve_neg_cache (cid, provider, track_sig, cand_id, score, reason_code, reason_data, checked_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		e.CID, e.Provider, e.TrackSig, e.CandID, e.Score, e.ReasonCode, e.ReasonData, at.Unix(),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteResolveNeg:配對成功後移除單筆負向快取。
func (s *Store) DeleteResolveNeg(cid, provider string) error {
	_, err := s.db.Exec("DELETE FROM resolve_neg_cache WHERE cid = ? AND provider = ?", cid, provider)
	if isMissingTable(err) {
		return nil
	}
	return err
}

// LoadTUIHistory:依時間順序(最舊→最新)回傳 TUI 命令歷史;空表或舊唯讀庫無表時回 nil, nil。
func (s *Store) LoadTUIHistory() ([]string, error) {
	var out []string
	err := query(s.db, "SELECT cmd FROM tui_history ORDER BY id ASC", func(r *sql.Rows) error {
		var cmd string
		if err := r.Scan(&cmd); err != nil {
			return err
		}
		out = append(out, cmd)
		return nil
	})
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AppendTUIHistory:寫入一筆 TUI 命令歷史(連續重複去重、最多保留最新 100 筆)。
// 交易第一句即為 INSERT ... SELECT 寫入敘述,避免先 SELECT 再寫入觸發 SQLITE_BUSY。
func (s *Store) AppendTUIHistory(cmd string, at time.Time) (err error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(
		"INSERT INTO tui_history (cmd, created_at) SELECT ?, ? WHERE COALESCE((SELECT cmd FROM tui_history ORDER BY id DESC LIMIT 1), '') <> ?",
		cmd, at.Unix(), cmd,
	); err != nil {
		return err
	}
	if _, err = tx.Exec(
		"DELETE FROM tui_history WHERE id NOT IN (SELECT id FROM tui_history ORDER BY id DESC LIMIT ?)",
		TUIHistoryMax,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// PlayJournalEntry:純本機播放日誌的一筆紀錄(不進 Drive)。
type PlayJournalEntry struct {
	ID         int64
	PlayedAt   time.Time
	Provider   string
	ProviderID string
	ISRC       string
	Title      string
	Artists    []string
	Album      string
	DurationMS int
}

// RecordPlayJournal:記錄一首播放曲目。
// 交易第一句先 DELETE 10 分鐘內同 (provider, provider_id) 的重複紀錄(若無 provider_id 如 Apple Music State,
// 則比對同 provider + 空 provider_id + title + artists),接著 INSERT 並裁剪保留最新 1000 筆,防止 SQLITE_BUSY。
func (s *Store) RecordPlayJournal(e PlayJournalEntry) (err error) {
	at := e.PlayedAt
	if at.IsZero() {
		at = time.Now()
	}
	artistsJSON := mustJSON(nonNil(e.Artists))
	cutoff := at.Add(-PlayJournalDedupWindow).Unix()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if e.ProviderID != "" {
		if _, err = tx.Exec(
			"DELETE FROM play_journal WHERE provider = ? AND provider_id = ? AND played_at >= ?",
			e.Provider, e.ProviderID, cutoff,
		); err != nil {
			return err
		}
	} else {
		if _, err = tx.Exec(
			"DELETE FROM play_journal WHERE provider = ? AND provider_id = '' AND title = ? AND artists = ? AND played_at >= ?",
			e.Provider, e.Title, artistsJSON, cutoff,
		); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(
		"INSERT INTO play_journal (played_at, provider, provider_id, isrc, title, artists, album, duration_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		at.Unix(), e.Provider, e.ProviderID, e.ISRC, e.Title, artistsJSON, e.Album, e.DurationMS,
	); err != nil {
		return err
	}
	if _, err = tx.Exec(
		"DELETE FROM play_journal WHERE id NOT IN (SELECT id FROM play_journal ORDER BY played_at DESC, id DESC LIMIT ?)",
		PlayJournalMax,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// ListPlayJournal:依播放時間由新到舊回傳播放日誌(limit <= 0 回傳全部);舊唯讀庫無表時回 nil, nil。
func (s *Store) ListPlayJournal(limit int) ([]PlayJournalEntry, error) {
	q := "SELECT id, played_at, provider, provider_id, isrc, title, artists, album, duration_ms FROM play_journal ORDER BY played_at DESC, id DESC"
	var args []any
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	var out []PlayJournalEntry
	err := query(s.db, q, func(r *sql.Rows) error {
		var e PlayJournalEntry
		var at int64
		var artistsJSON string
		if err := r.Scan(&e.ID, &at, &e.Provider, &e.ProviderID, &e.ISRC, &e.Title, &artistsJSON, &e.Album, &e.DurationMS); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(artistsJSON), &e.Artists); err != nil {
			return err
		}
		e.Artists = nonNil(e.Artists)
		e.PlayedAt = time.Unix(at, 0)
		out = append(out, e)
		return nil
	}, args...)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ClearPlayerHistory:供 history clear 一併安全抹除 tui_history 與 play_journal。
func (s *Store) ClearPlayerHistory() error {
	if _, err := s.db.Exec(clearPlayerHistorySQL); err != nil {
		if !isMissingTable(err) {
			return err
		}
		for _, q := range []string{"PRAGMA secure_delete = ON", "DELETE FROM tui_history", "DELETE FROM play_journal"} {
			if _, qerr := s.db.Exec(q); qerr != nil && !isMissingTable(qerr) {
				return qerr
			}
		}
	}
	return nil
}

const clearPlayerHistorySQL = "PRAGMA secure_delete = ON; DELETE FROM tui_history; DELETE FROM play_journal;"
