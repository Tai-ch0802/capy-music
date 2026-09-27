package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// 純快取的兩張表(原本的 config.Dir()/cache.json,UX 計畫 R3 併進來):各 provider 的播放清單與最近項目。
// 順序存 position、讀回依 position——「最新在前、去重、上限 50」的規則留在 internal/cache 的記憶體邏輯裡,不在 SQL 重做。

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

// SaveCache 整批取代兩張表(一筆交易)。
func (s *Store) SaveCache(pls map[string][]ProviderPlaylist, recent []Recent) (err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec("DELETE FROM provider_playlists; DELETE FROM recent;"); err != nil {
		return err
	}
	for prov, list := range pls {
		for i, p := range list {
			if _, err = tx.Exec("INSERT INTO provider_playlists (provider, position, id, name, total) VALUES (?, ?, ?, ?, ?)", prov, i, p.ID, p.Name, p.Total); err != nil {
				return err
			}
		}
	}
	for i, r := range recent {
		if _, err = tx.Exec("INSERT INTO recent (position, at, provider, type, id, label, detail) VALUES (?, ?, ?, ?, ?, ?, ?)", i, r.At, r.Provider, r.Type, r.ID, r.Label, r.Detail); err != nil {
			return err
		}
	}
	return tx.Commit()
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

// ForgetProvider 刪掉某個 provider 在兩張快取表裡的列(登出時:Spotify Developer Policy I.1.b 要求使用者中斷連線時刪掉他的資料;
// 計畫 2026-09-24 §1.7 S5)。recent 的 position 會留空號,讀回依 position 排序,不影響順序。
// secure_delete:SQLite 預設只把刪掉的列標成空頁,內容還在檔案裡(strings state.db 讀得到);這裡要的是真的刪掉(#102 review)。
// 跟 DELETE 放在同一次 Exec,確保是同一條連線(PRAGMA 只作用在那條連線上)。
func (s *Store) ForgetProvider(provider string) error {
	_, err := s.db.Exec(forgetSQL, provider, provider, provider)
	return err
}

const forgetSQL = "PRAGMA secure_delete = ON; DELETE FROM provider_playlists WHERE provider = ?; DELETE FROM recent WHERE provider = ?; DELETE FROM playlist_items_cache WHERE provider = ?;"

// retiredName:schema 升版時留下的舊檔(state.db.v<N>,見 retire);不含它們的 -journal / -wal / -shm。
var retiredName = regexp.MustCompile(`\.v\d+$`)

// ForgetProviderInRetired:舊檔(state.db.v<N>)裡也有這三張快取表,登出時連它們一起刪(不然平台的清單名稱、最近項目與
// 清單曲目還留在這台電腦上)。用原始的 open,不經 OpenAt——那會把舊檔當成版本不符再退役一次。舊檔沒有某張表就跳過(v5 及更早的沒有清單曲目);
// 壞掉的舊檔(打不開也讀不出來)直接刪掉——它只是 cache 的保留檔,不刪的話每次登出都會說「沒能清掉」(#102 review)。
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
		for _, q := range []string{"PRAGMA secure_delete = ON", "DELETE FROM provider_playlists WHERE provider = ?", "DELETE FROM recent WHERE provider = ?", "DELETE FROM playlist_items_cache WHERE provider = ?"} {
			var args []any
			if strings.Contains(q, "?") {
				args = []any{provider}
			}
			_, err := s.db.Exec(q, args...)
			switch {
			case err == nil, strings.Contains(err.Error(), "no such table"):
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
