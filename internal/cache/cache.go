// Package cache 是非機密本機快取的門面:各 provider 的播放清單 id/name,與最近 MaxRecent 筆搜尋/挑選。
// 原本存 config.Dir()/cache.json;P3 T6 起改存 state.db(store 套件)的兩張表,呼叫端不變:Load 永不失敗、
// Save 寫穿。它只是快取——讀不到或壞掉視同空、絕不報錯;不是 source of truth,也絕不存憑證。
package cache

import (
	"os"
	"path/filepath"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

const (
	MaxRecent = 50
	// busy:等別的 capy 放鎖的上限。shell 補全走這裡,TAB 不能卡;另一個 capy 正在寫就當作空快取。
	busy = 200 * time.Millisecond
	// legacyFile:T6 之前的檔,首次 Load 順手刪(只是快取,不搬內容;UX 計畫 R3)。
	legacyFile = "cache.json"
)

// Recent.Type 的合法值。
const (
	TypeTrack    = "track"
	TypeArtist   = "artist"
	TypePlaylist = "playlist"
	TypeQuery    = "query"
)

type (
	Playlist = store.ProviderPlaylist
	Recent   = store.Recent
)

type Cache struct {
	Playlists map[string][]Playlist // key = provider id
	Recent    []Recent              // 最新在前
	loaded    bool                  // Load 成功才 true;false 時 Save 是 no-op——讀不到就不能拿空的去蓋掉別人的(它只是快取)
}

// Now 是測試替換點。
var Now = time.Now

// Load 永不失敗:任何問題(db 開不了、被鎖、壞掉)都回空快取。
func Load() *Cache {
	empty := &Cache{Playlists: map[string][]Playlist{}}
	if dir, err := config.Dir(); err == nil {
		_ = os.Remove(filepath.Join(dir, legacyFile))
	}
	s, err := store.Open(busy)
	if err != nil {
		return empty
	}
	defer s.Close()
	pls, recent, err := s.LoadCache()
	if err != nil {
		return empty
	}
	return &Cache{Playlists: pls, Recent: recent, loaded: true}
}

// Save 寫穿到 state.db(一筆交易)。Load 沒成功的 Cache 直接回 nil 不寫:一次暫時性的讀失敗(200 ms 內等不到鎖)
// 不能變成整批取代成空的永久遺失。
func (c *Cache) Save() error {
	if !c.loaded {
		return nil
	}
	s, err := store.Open(busy)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.SaveCache(c.Playlists, c.Recent)
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
