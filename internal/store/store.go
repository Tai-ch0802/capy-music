// Package store 是本機 SQLite(config.Dir()/state.db,spec §7)。它是 cache,不是 source of truth:只存 Drive 上有的
// 東西的鏡像(canon 的 manifest / tracks / playlists / 各裝置 base)與純快取(provider 清單、最近項目)。
// 刪掉整個 db 必須能從 Drive 完整重建(CLAUDE.md 硬約束,TestRebuildFromDrive)。不存憑證、不存 provider 原始 JSON。
//
// Migration 政策:PRAGMA user_version 不符就整檔丟棄重建,不寫 ALTER(spec §7)。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // 純 Go、無 cgo(spec §2)

	"github.com/Tai-ch0802/capy-music/internal/config"
)

const (
	fileName      = "state.db"
	schemaVersion = 1
)

// schema v1。與 spec §7 的差異(2026-09-07,理由寫在 spec §7):tracks 多 artists / conflicts(JSON)、少 updated_at
// (canon 沒有來源);device_base 取代 sync_state(每台裝置的 base 是有序 items,不是 hash);devices 鏡像
// manifest.devices 與「有沒有 dev 檔」;不建 sync_state / resolution_cache(P3 沒有寫入者,P4 加時 bump 版本即可)。
const schema = `
CREATE TABLE tracks (cid TEXT PRIMARY KEY, title TEXT NOT NULL, artists TEXT NOT NULL, album TEXT NOT NULL, duration_ms INTEGER NOT NULL, conflicts TEXT NOT NULL);
CREATE TABLE isrcs (cid TEXT NOT NULL, isrc TEXT NOT NULL, PRIMARY KEY (cid, isrc));
CREATE TABLE mappings (cid TEXT NOT NULL, provider TEXT NOT NULL, provider_id TEXT NOT NULL, PRIMARY KEY (cid, provider));
CREATE TABLE playlists (pid TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE playlist_items (pid TEXT NOT NULL, iid TEXT NOT NULL, cid TEXT NOT NULL, rank TEXT NOT NULL, added_at INTEGER NOT NULL, PRIMARY KEY (pid, iid));
CREATE TABLE playlist_links (pid TEXT NOT NULL, provider TEXT NOT NULL, provider_id TEXT NOT NULL, PRIMARY KEY (pid, provider));
CREATE TABLE devices (device_id TEXT PRIMARY KEY, name TEXT NOT NULL, last_seen INTEGER NOT NULL, registered INTEGER NOT NULL, has_state INTEGER NOT NULL);
CREATE TABLE device_base (device_id TEXT NOT NULL, pid TEXT NOT NULL, provider TEXT NOT NULL, name TEXT NOT NULL, items TEXT NOT NULL, observed_at INTEGER NOT NULL, PRIMARY KEY (device_id, pid, provider));
CREATE TABLE provider_playlists (provider TEXT NOT NULL, position INTEGER NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL, total INTEGER NOT NULL, PRIMARY KEY (provider, position));
CREATE TABLE recent (position INTEGER PRIMARY KEY, at INTEGER NOT NULL, provider TEXT NOT NULL, type TEXT NOT NULL, id TEXT NOT NULL, label TEXT NOT NULL, detail TEXT NOT NULL);
`

type Store struct {
	db   *sql.DB
	path string
}

// Path 回 db 路徑(不建立);CAPY_CONFIG_DIR 一併覆寫(測試靠它隔離)。
func Path() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// Open 開(或建)state.db。busyTimeout 是等別的 capy 放鎖的上限:補全路徑要短(TAB 不能卡),canonical 路徑可以長。
func Open(busyTimeout time.Duration) (*Store, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	return OpenAt(p, busyTimeout)
}

// OpenAt:user_version 不是目前版本(含全新的 0)就關閉、刪檔、重建。
func OpenAt(path string, busyTimeout time.Duration) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	s, err := open(path, busyTimeout)
	if err != nil {
		return nil, err
	}
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		s.Close()
		return nil, err
	}
	if v == schemaVersion {
		return s, nil
	}
	if err := s.Remove(); err != nil {
		return nil, err
	}
	if s, err = open(path, busyTimeout); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(schema + fmt.Sprintf("PRAGMA user_version = %d;", schemaVersion)); err != nil {
		s.Close()
		return nil, fmt.Errorf("建 schema:%w", err)
	}
	return s, nil
}

func open(path string, busy time.Duration) (*Store, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("%s?_pragma=busy_timeout(%d)", path, busy.Milliseconds()))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // database/sql 會開多條連線,SQLite 一條寫一條讀就 database is locked
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// Remove 關閉連線並刪掉 db 與所有伴生檔。Windows 上連線未關會 sharing violation,所以先 Close;
// journal / wal / shm 不一併刪的話舊內容會 replay 進新 db(目前是 DELETE journal,一起刪不吃虧)。
func (s *Store) Remove() error {
	if err := s.Close(); err != nil {
		return err
	}
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		if err := os.Remove(s.path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
