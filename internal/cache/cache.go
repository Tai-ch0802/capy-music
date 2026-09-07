// Package cache 是非機密的本機快取(config.Dir()/cache.json):各 provider 的播放清單 id/name,
// 與最近 MaxRecent 筆搜尋/挑選。它只是快取——讀不到或壞掉視同空、絕不報錯;不是 source of truth,
// 也絕不存憑證。暫時性:P3 T6 引入 SQLite 後併入並刪除本檔(UX 計畫 R3)。
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/config"
)

const (
	schemaVersion = 1
	fileName      = "cache.json"
	MaxRecent     = 50
)

// Recent.Type 的合法值。
const (
	TypeTrack    = "track"
	TypeArtist   = "artist"
	TypePlaylist = "playlist"
	TypeQuery    = "query"
)

type Playlist struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Total int    `json:"total"`
}

type Recent struct {
	At       int64  `json:"at"` // unix 秒
	Provider string `json:"provider"`
	Type     string `json:"type"`
	ID       string `json:"id"`
	Label    string `json:"label"`
	Detail   string `json:"detail,omitempty"`
}

type Cache struct {
	SchemaVersion int                   `json:"schema_version"`
	Playlists     map[string][]Playlist `json:"playlists,omitempty"` // key = provider id
	Recent        []Recent              `json:"recent,omitempty"`    // 最新在前
}

// Now 是測試替換點。
var Now = time.Now

func path() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// Load 永不失敗:任何問題(不存在、壞 JSON、schema 不符)都回空快取。
func Load() *Cache {
	empty := &Cache{SchemaVersion: schemaVersion, Playlists: map[string][]Playlist{}}
	p, err := path()
	if err != nil {
		return empty
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return empty
	}
	var c Cache
	if json.Unmarshal(b, &c) != nil || c.SchemaVersion != schemaVersion {
		return empty
	}
	if c.Playlists == nil {
		c.Playlists = map[string][]Playlist{}
	}
	return &c
}

// Save 原子寫入(tmp + rename,與 config.Save 同)。
func (c *Cache) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (c *Cache) SetPlaylists(providerID string, pls []Playlist) {
	c.Playlists[providerID] = pls
}

// AddRecent 放到最前;同 provider+type+id 只留這筆;超過 MaxRecent 淘汰最舊。At 為 0 時補現在。
func (c *Cache) AddRecent(r Recent) {
	if r.At == 0 {
		r.At = Now().Unix()
	}
	out := make([]Recent, 0, len(c.Recent)+1)
	out = append(out, r)
	for _, x := range c.Recent {
		if x.Provider == r.Provider && x.Type == r.Type && x.ID == r.ID {
			continue
		}
		out = append(out, x)
	}
	if len(out) > MaxRecent {
		out = out[:MaxRecent]
	}
	c.Recent = out
}

func (c *Cache) ClearRecent() { c.Recent = nil }
