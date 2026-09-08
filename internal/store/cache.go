package store

import "database/sql"

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
