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

// Hydrate 用 c 取代所有 canonical 鏡像表(一筆交易)。ponytail: 沒有 upsert,T8 需要再加;個人曲庫的量整批寫也只是毫秒級。
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
	for _, t := range []string{"tracks", "isrcs", "mappings", "playlists", "playlist_items", "playlist_links", "devices", "device_base"} {
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
			for prov, id := range t.Mappings {
				if _, err = tx.Exec("INSERT INTO mappings (cid, provider, provider_id) VALUES (?, ?, ?)", cid, prov, id); err != nil {
					return err
				}
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
				if _, err = tx.Exec("INSERT INTO device_base (device_id, pid, provider, name, items, observed_at) VALUES (?, ?, ?, ?, ?, ?)",
					d.DeviceID, pid, prov, b.Snapshot.Name, mustJSON(nonNil(b.Snapshot.Items)), b.ObservedAt); err != nil {
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
	err := s.query("SELECT cid, title, artists, album, duration_ms, conflicts FROM tracks ORDER BY cid", func(r *sql.Rows) error {
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
		t.Artists, t.Mappings = nonNil(t.Artists), map[string]string{}
		c.Tracks.Tracks[t.CID] = t
		return nil
	})
	if err != nil {
		return c, err
	}
	if err := s.query("SELECT cid, isrc FROM isrcs ORDER BY cid, isrc", func(r *sql.Rows) error {
		var cid, isrc string
		if err := r.Scan(&cid, &isrc); err != nil {
			return err
		}
		t := c.Tracks.Tracks[cid]
		t.ISRC = append(t.ISRC, isrc)
		c.Tracks.Tracks[cid] = t
		return nil
	}); err != nil {
		return c, err
	}
	if err := s.query("SELECT cid, provider, provider_id FROM mappings", func(r *sql.Rows) error {
		var cid, prov, id string
		if err := r.Scan(&cid, &prov, &id); err != nil {
			return err
		}
		c.Tracks.Tracks[cid].Mappings[prov] = id
		return nil
	}); err != nil {
		return c, err
	}
	byPID := map[string]*canon.Playlist{}
	if err := s.query("SELECT pid, name, description, updated_at FROM playlists ORDER BY pid", func(r *sql.Rows) error {
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
	if err := s.query("SELECT pid, iid, cid, rank, added_at FROM playlist_items ORDER BY pid, rank, iid", func(r *sql.Rows) error {
		var pid string
		var it canon.Item
		if err := r.Scan(&pid, &it.IID, &it.CID, &it.Rank, &it.AddedAt); err != nil {
			return err
		}
		byPID[pid].Items = append(byPID[pid].Items, it)
		return nil
	}); err != nil {
		return c, err
	}
	if err := s.query("SELECT pid, provider, provider_id FROM playlist_links", func(r *sql.Rows) error {
		var pid, prov, id string
		if err := r.Scan(&pid, &prov, &id); err != nil {
			return err
		}
		byPID[pid].Links[prov] = id
		return nil
	}); err != nil {
		return c, err
	}
	byDev := map[string]*canon.DeviceState{}
	if err := s.query("SELECT device_id, name, last_seen, registered, has_state FROM devices ORDER BY device_id", func(r *sql.Rows) error {
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
	if err := s.query("SELECT device_id, pid, provider, name, items, observed_at FROM device_base", func(r *sql.Rows) error {
		var dev, pid, prov, items string
		var b canon.Base
		if err := r.Scan(&dev, &pid, &prov, &b.Snapshot.Name, &items, &b.ObservedAt); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(items), &b.Snapshot.Items); err != nil {
			return err
		}
		b.Snapshot.Items = nonNil(b.Snapshot.Items)
		d, ok := byDev[dev]
		if !ok {
			return fmt.Errorf("device_base 有 devices 沒有的裝置 %s", dev)
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
	return c, nil
}

func (s *Store) query(q string, each func(*sql.Rows) error, args ...any) error {
	rows, err := s.db.Query(q, args...)
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
