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
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite "modernc.org/sqlite" // 純 Go、無 cgo(spec §2)

	"github.com/Tai-ch0802/capy-music/internal/config"
)

const (
	fileName      = "state.db"
	schemaVersion = 4 // v2(2026-09-08,T7):device_base 加 cids;v3(T8):加 playlist_id;v4(P4 T2a,決策 20):mappings 加 confidence / pinned / source / updated_at。升版舊檔改名保留(T9)
)

// schema v1。與 spec §7 的差異(2026-09-07,理由寫在 spec §7):tracks 多 artists / conflicts(JSON)、少 updated_at
// (canon 沒有來源);device_base 取代 sync_state(每台裝置的 base 是有序 items,不是 hash);devices 鏡像
// manifest.devices 與「有沒有 dev 檔」;不建 sync_state / resolution_cache(P3 沒有寫入者,P4 加時 bump 版本即可)。
const schema = `
CREATE TABLE tracks (cid TEXT PRIMARY KEY, title TEXT NOT NULL, artists TEXT NOT NULL, album TEXT NOT NULL, duration_ms INTEGER NOT NULL, conflicts TEXT NOT NULL);
CREATE TABLE isrcs (cid TEXT NOT NULL, isrc TEXT NOT NULL, PRIMARY KEY (cid, isrc));
CREATE TABLE mappings (cid TEXT NOT NULL, provider TEXT NOT NULL, provider_id TEXT NOT NULL, confidence INTEGER NOT NULL, pinned INTEGER NOT NULL, source TEXT NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY (cid, provider));
CREATE TABLE playlists (pid TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE playlist_items (pid TEXT NOT NULL, iid TEXT NOT NULL, cid TEXT NOT NULL, rank TEXT NOT NULL, added_at INTEGER NOT NULL, PRIMARY KEY (pid, iid));
CREATE TABLE playlist_links (pid TEXT NOT NULL, provider TEXT NOT NULL, provider_id TEXT NOT NULL, PRIMARY KEY (pid, provider));
CREATE TABLE devices (device_id TEXT PRIMARY KEY, name TEXT NOT NULL, last_seen INTEGER NOT NULL, registered INTEGER NOT NULL, has_state INTEGER NOT NULL);
CREATE TABLE device_base (device_id TEXT NOT NULL, pid TEXT NOT NULL, provider TEXT NOT NULL, playlist_id TEXT NOT NULL, name TEXT NOT NULL, items TEXT NOT NULL, cids TEXT NOT NULL, observed_at INTEGER NOT NULL, PRIMARY KEY (device_id, pid, provider));
CREATE TABLE provider_playlists (provider TEXT NOT NULL, position INTEGER NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL, total INTEGER NOT NULL, PRIMARY KEY (provider, position));
CREATE TABLE recent (position INTEGER PRIMARY KEY, at INTEGER NOT NULL, provider TEXT NOT NULL, type TEXT NOT NULL, id TEXT NOT NULL, label TEXT NOT NULL, detail TEXT NOT NULL);
`

type Store struct {
	db   *sql.DB
	path string
}

// Stderr:升版保留舊檔時的提示(測試可換掉)。package-level 可變全域,所以這個 package 的測試不能 t.Parallel()。
var Stderr io.Writer = os.Stderr

var (
	ErrNoDB           = errors.New("本機沒有 state.db")
	ErrSchemaMismatch = errors.New("本機 state.db 的 schema 版本與這個 capy 不同")
)

// OpenReadOnly:唯讀開法,給逃生口(export、drive init)用——不建檔、不自癒:壞檔不刪、版本不符不改名,任何不對就回錯、
// 檔案原封不動。Open 的自癒對 cache 是對的,但 Drive 空掉 + 本機 db 有點壞正是最需要逃生口的組合,不能讓 export 把僅剩的一份毀掉。
func OpenReadOnly(busyTimeout time.Duration) (*Store, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	return OpenReadOnlyAt(p, busyTimeout)
}

func OpenReadOnlyAt(path string, busyTimeout time.Duration) (*Store, error) {
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("db 路徑不可含 ?(CAPY_CONFIG_DIR 換一個目錄):%s", path)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoDB
	} else if err != nil {
		return nil, err
	}
	s, err := open(path, busyTimeout)
	if err != nil {
		return nil, fmt.Errorf("開啟 %s:%w", path, err)
	}
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		s.Close()
		return nil, fmt.Errorf("讀取 %s:%w", path, err)
	}
	if v != schemaVersion {
		s.Close()
		return nil, fmt.Errorf("%w:檔案是 v%d、這個 capy 支援 v%d;要匯出請用當時版本的 capy binary", ErrSchemaMismatch, v, schemaVersion)
	}
	return s, nil
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

// OpenAt:user_version 不是目前版本(含全新的 0)、或檔案本身壞掉(SQLITE_NOTADB / SQLITE_CORRUPT),就關閉、重建——
// 它是 cache,政策本來就允許我們自己刪。但**版本不符的舊檔不刪,改名成 state.db.v<舊版> 留著**(2026-09-08 T9):
// Drive 被清空時本機 cache 是唯一剩下的一份,升版一刀刪掉它,drive init --from-local 就沒東西可補。
// 目前沒有任何程式會讀保留檔——這是「不毀掉」,不是「能復原」;真要跨版本復原走舊 binary 的 capy export。
// 壞檔(讀不出版本)與全新的 0 照樣直接刪。「暫時打不開」(SQLITE_BUSY、權限)照樣往上丟,不刪。
// ponytail: 路徑含 ? 直接拒絕——driver 在第一個 ? 切開 DSN,會把 db 靜默開到別的檔;真要支援再改 file: URI 加跳脫。
func OpenAt(path string, busyTimeout time.Duration) (*Store, error) {
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("db 路徑不可含 ?(CAPY_CONFIG_DIR 換一個目錄):%s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	s, err := open(path, busyTimeout)
	if err == nil {
		var v int
		if err = s.db.QueryRow("PRAGMA user_version").Scan(&v); err == nil && v == schemaVersion {
			return s, nil
		}
		if err != nil && !corrupt(err) {
			s.Close()
			return nil, err
		}
		if err == nil && v > 0 { // 版本不符:留舊檔
			if err := s.retire(v); err != nil {
				return nil, err
			}
		} else if err := s.Remove(); err != nil { // 壞檔或全新的 0
			return nil, err
		}
	} else {
		if !corrupt(err) {
			return nil, err
		}
		if err := (&Store{path: path}).Remove(); err != nil {
			return nil, err
		}
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

// corrupt:檔案本身壞掉(斷電、寫一半、磁碟壞塊)。SQLITE_NOTADB = 26、SQLITE_CORRUPT = 11;取低 8 位是主碼。
func corrupt(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	code := se.Code() & 0xff
	return code == 26 || code == 11
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

// retire 關閉連線,把舊版 db 改名成 <path>.v<version>(已有同名保留檔就覆蓋——Windows 的 rename 不會蓋,先刪),
// 伴生檔照 Remove 的理由刪掉。
func (s *Store) retire(version int) error {
	if err := s.Close(); err != nil {
		return err
	}
	kept := fmt.Sprintf("%s.v%d", s.path, version)
	if err := os.Remove(kept); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(s.path, kept); err != nil {
		return err
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if err := os.Remove(s.path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	fmt.Fprintf(Stderr, "本機快取 schema 從 v%d 變成 v%d:舊檔保留為 %s(沒有程式會讀它;確定不需要再自行刪除),快取會在下一次 pull 從 Drive 重建\n", version, schemaVersion, kept)
	return nil
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
