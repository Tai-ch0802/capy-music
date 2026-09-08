package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Tai-ch0802/capy-music/internal/canon"
)

// Canonical 是 Drive 上全部 canon 檔的一份快照:Hydrate 整批寫入、Dump 整批讀出。
type Canonical struct {
	Manifest  *canon.Manifest
	Tracks    *canon.Tracks
	Playlists []canon.Playlist
	Devices   []canon.DeviceState
}

// Hydrate 用 c 取代所有 canonical 鏡像表(一筆交易)。沒有 upsert,T8 需要再加;個人曲庫的量整批寫也只是毫秒級
// (真的上萬列時 tx.Prepare 一次、迴圈重用會差很多)。
func (s *Store) Hydrate(c Canonical) (err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	for _, t := range []string{"tracks", "isrcs", "mappings", "merged", "playlists", "playlist_items", "playlist_links", "devices", "device_base"} {
		if _, err = tx.Exec("DELETE FROM " + t); err != nil {
			return err
		}
	}
	if c.Manifest != nil {
		for _, d := range c.Manifest.Devices {
			if _, err = tx.Exec(`INSERT INTO devices (device_id, name, last_seen, registered, has_state) VALUES (?, ?, ?, 1, 0)
				ON CONFLICT(device_id) DO UPDATE SET name = excluded.name, last_seen = excluded.last_seen, registered = 1`, d.ID, d.Name, d.LastSeen); err != nil {
				return err
			}
		}
	}
	if c.Tracks != nil {
		for cid, t := range c.Tracks.Tracks {
			artists, conflicts := mustJSON(nonNil(t.Artists)), mustJSON(nonNilConflicts(t.Conflicts))
			if _, err = tx.Exec("INSERT INTO tracks (cid, title, artists, album, duration_ms, conflicts) VALUES (?, ?, ?, ?, ?, ?)", cid, t.Title, artists, t.Album, t.DurationMS, conflicts); err != nil {
				return err
			}
			for _, isrc := range t.ISRC {
				if _, err = tx.Exec("INSERT INTO isrcs (cid, isrc) VALUES (?, ?)", cid, isrc); err != nil {
					return err
				}
			}
			for prov, m := range t.Mappings {
				if _, err = tx.Exec("INSERT INTO mappings (cid, provider, provider_id, confidence, pinned, source, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
					cid, prov, m.ID, m.Confidence, m.Pinned, m.Source, m.UpdatedAt); err != nil {
					return err
				}
			}
		}
		for loser, survivor := range c.Tracks.Merged {
			if _, err = tx.Exec("INSERT INTO merged (cid, into_cid) VALUES (?, ?)", loser, survivor); err != nil {
				return err
			}
		}
	}
	for _, p := range c.Playlists {
		if _, err = tx.Exec("INSERT INTO playlists (pid, name, description, updated_at) VALUES (?, ?, ?, ?)", p.PID, p.Name, p.Description, p.UpdatedAt); err != nil {
			return err
		}
		for _, it := range p.Items {
			if _, err = tx.Exec("INSERT INTO playlist_items (pid, iid, cid, rank, added_at) VALUES (?, ?, ?, ?, ?)", p.PID, it.IID, it.CID, it.Rank, it.AddedAt); err != nil {
				return err
			}
		}
		for prov, id := range p.Links {
			if _, err = tx.Exec("INSERT INTO playlist_links (pid, provider, provider_id) VALUES (?, ?, ?)", p.PID, prov, id); err != nil {
				return err
			}
		}
	}
	for _, d := range c.Devices {
		if _, err = tx.Exec(`INSERT INTO devices (device_id, name, last_seen, registered, has_state) VALUES (?, '', 0, 0, 1)
			ON CONFLICT(device_id) DO UPDATE SET has_state = 1`, d.DeviceID); err != nil {
			return err
		}
		for pid, byProv := range d.Base {
			for prov, b := range byProv {
				if _, err = tx.Exec("INSERT INTO device_base (device_id, pid, provider, playlist_id, name, items, cids, observed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
					d.DeviceID, pid, prov, b.Snapshot.ID, b.Snapshot.Name, mustJSON(nonNil(b.Snapshot.Items)), mustJSON(nonNil(b.Snapshot.CIDs)), b.ObservedAt); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

// Dump 由鏡像表重建 canon 物件,順序決定性:playlists 依 pid、items 依 (rank, iid)、devices 依 device_id。
func (s *Store) Dump() (Canonical, error) {
	c := Canonical{Manifest: canon.NewManifest(), Tracks: canon.NewTracks(), Playlists: []canon.Playlist{}, Devices: []canon.DeviceState{}}
	// 整個 Dump 在一個 read transaction 裡:八個 SELECT 之間若讓別的 capy 的 Hydrate commit 進來,export 會讀到前半舊、後半新的
	// 撕裂狀態(輕則被下面的一致性檢查擋下並建議「刪掉重建」——本機是唯一一份時那是最糟的建議;重則靜靜產出對不起來的備份)。
	// SQLite 的 deferred 交易在第一個 SELECT 取得 SHARED 鎖後持有到結束,寫入者的 commit 會等(busy_timeout),讀到的是一致的快照。
	tx, err := s.db.Begin()
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	err = query(tx, "SELECT cid, title, artists, album, duration_ms, conflicts FROM tracks ORDER BY cid", func(r *sql.Rows) error {
		var t canon.Track
		var artists, conflicts string
		if err := r.Scan(&t.CID, &t.Title, &artists, &t.Album, &t.DurationMS, &conflicts); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(artists), &t.Artists); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(conflicts), &t.Conflicts); err != nil {
			return err
		}
		t.Artists, t.Mappings = nonNil(t.Artists), map[string]canon.Mapping{}
		c.Tracks.Tracks[t.CID] = t
		return nil
	})
	if err != nil {
		return c, err
	}
	if err := query(tx, "SELECT cid, isrc FROM isrcs ORDER BY cid, isrc", func(r *sql.Rows) error {
		var cid, isrc string
		if err := r.Scan(&cid, &isrc); err != nil {
			return err
		}
		t, ok := c.Tracks.Tracks[cid]
		if !ok {
			return fmt.Errorf("isrcs 有 tracks 沒有的 cid %s(db 不一致,刪掉重建)", cid)
		}
		t.ISRC = append(t.ISRC, isrc)
		c.Tracks.Tracks[cid] = t
		return nil
	}); err != nil {
		return c, err
	}
	if err := query(tx, "SELECT cid, provider, provider_id, confidence, pinned, source, updated_at FROM mappings", func(r *sql.Rows) error {
		var cid, prov string
		var m canon.Mapping
		if err := r.Scan(&cid, &prov, &m.ID, &m.Confidence, &m.Pinned, &m.Source, &m.UpdatedAt); err != nil {
			return err
		}
		t, ok := c.Tracks.Tracks[cid]
		if !ok {
			return fmt.Errorf("mappings 有 tracks 沒有的 cid %s(db 不一致,刪掉重建)", cid)
		}
		t.Mappings[prov] = m
		return nil
	}); err != nil {
		return c, err
	}
	if err := query(tx, "SELECT cid, into_cid FROM merged", func(r *sql.Rows) error {
		var loser, survivor string
		if err := r.Scan(&loser, &survivor); err != nil {
			return err
		}
		if _, ok := c.Tracks.Tracks[survivor]; !ok { // 敗者本來就不該在 tracks,勝者一定要在(Merge 維持的不變量,這裡是第二道證人)
			return fmt.Errorf("merged 的 into_cid %s 不在 tracks 裡(db 不一致,刪掉重建)", survivor)
		}
		c.Tracks.Merged[loser] = survivor
		return nil
	}); err != nil {
		return c, err
	}
	byPID := map[string]*canon.Playlist{}
	if err := query(tx, "SELECT pid, name, description, updated_at FROM playlists ORDER BY pid", func(r *sql.Rows) error {
		p := canon.Playlist{SchemaVersion: canon.SchemaVersion, Items: []canon.Item{}, Links: map[string]string{}}
		if err := r.Scan(&p.PID, &p.Name, &p.Description, &p.UpdatedAt); err != nil {
			return err
		}
		c.Playlists = append(c.Playlists, p)
		return nil
	}); err != nil {
		return c, err
	}
	for i := range c.Playlists {
		byPID[c.Playlists[i].PID] = &c.Playlists[i]
	}
	if err := query(tx, "SELECT pid, iid, cid, rank, added_at FROM playlist_items ORDER BY pid, rank, iid", func(r *sql.Rows) error {
		var pid string
		var it canon.Item
		if err := r.Scan(&pid, &it.IID, &it.CID, &it.Rank, &it.AddedAt); err != nil {
			return err
		}
		p, ok := byPID[pid]
		if !ok {
			return fmt.Errorf("playlist_items 有 playlists 沒有的 pid %s(db 不一致,刪掉重建)", pid)
		}
		p.Items = append(p.Items, it)
		return nil
	}); err != nil {
		return c, err
	}
	if err := query(tx, "SELECT pid, provider, provider_id FROM playlist_links", func(r *sql.Rows) error {
		var pid, prov, id string
		if err := r.Scan(&pid, &prov, &id); err != nil {
			return err
		}
		p, ok := byPID[pid]
		if !ok {
			return fmt.Errorf("playlist_links 有 playlists 沒有的 pid %s(db 不一致,刪掉重建)", pid)
		}
		p.Links[prov] = id
		return nil
	}); err != nil {
		return c, err
	}
	byDev := map[string]*canon.DeviceState{}
	if err := query(tx, "SELECT device_id, name, last_seen, registered, has_state FROM devices ORDER BY device_id", func(r *sql.Rows) error {
		var d canon.Device
		var registered, hasState bool
		if err := r.Scan(&d.ID, &d.Name, &d.LastSeen, &registered, &hasState); err != nil {
			return err
		}
		if registered {
			c.Manifest.Devices = append(c.Manifest.Devices, d)
		}
		if hasState {
			c.Devices = append(c.Devices, *canon.NewDeviceState(d.ID))
		}
		return nil
	}); err != nil {
		return c, err
	}
	for i := range c.Devices {
		byDev[c.Devices[i].DeviceID] = &c.Devices[i]
	}
	if err := query(tx, "SELECT device_id, pid, provider, playlist_id, name, items, cids, observed_at FROM device_base", func(r *sql.Rows) error {
		var dev, pid, prov, items, cids string
		var b canon.Base
		if err := r.Scan(&dev, &pid, &prov, &b.Snapshot.ID, &b.Snapshot.Name, &items, &cids, &b.ObservedAt); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(items), &b.Snapshot.Items); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(cids), &b.Snapshot.CIDs); err != nil {
			return err
		}
		b.Snapshot.Items, b.Snapshot.CIDs = nonNil(b.Snapshot.Items), nonNil(b.Snapshot.CIDs)
		d, ok := byDev[dev]
		if !ok {
			return fmt.Errorf("device_base 有 devices 沒有的裝置 %s(db 不一致,刪掉重建)", dev)
		}
		if d.Base[pid] == nil {
			d.Base[pid] = map[string]canon.Base{}
		}
		d.Base[pid][prov] = b
		return nil
	}); err != nil {
		return c, err
	}
	sort.Slice(c.Manifest.Devices, func(i, j int) bool { return c.Manifest.Devices[i].ID < c.Manifest.Devices[j].ID })
	// manifest.playlists 不另存:它宣告的就是 Drive 上有的 pl__*.json,而 db 裡的 playlists 表正是那組檔——pull 的閘保證
	// 「manifest 宣告但取不到」的狀態永遠不會被 Hydrate 進來,所以兩者在這裡恆等,由表推回即可。
	for _, p := range c.Playlists {
		c.Manifest.AddPlaylist(p.PID)
	}
	return c, nil
}

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// query:跑一個 SELECT 逐列回呼;db 可以是 *sql.DB 或 *sql.Tx(Dump 整批放在一個交易裡)。
func query(db querier, q string, each func(*sql.Rows) error, args ...any) error {
	rows, err := db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // 只餵 []string / []Conflict,不可能失敗
	}
	return string(b)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilConflicts(s []canon.Conflict) []canon.Conflict {
	if s == nil {
		return []canon.Conflict{}
	}
	return s
}
