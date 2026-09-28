// Package cache 是非機密本機快取的門面:各 provider 的播放清單 id/name,與最近 MaxRecent 筆搜尋/挑選。
// 原本存 config.Dir()/cache.json;P3 T6 起改存 state.db(store 套件)的兩張表,呼叫端不變:Load 永不失敗、
// Save 寫穿。它只是快取——讀不到或壞掉視同空、絕不報錯;不是 source of truth,也絕不存憑證。
// Save 只寫這次改了什麼(SetPlaylists / AddRecent / ClearRecent 記下來的),不拿讀到的那一份整批取代:
// 同時有好幾個 capy 在讀寫(TUI 開出來的子程式、capy --web、另一個終端機、cron)。
package cache

import (
	"errors"
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
	Recent    []Recent              // 最新在前(讀到的那一份加上這次的改動;別的行程之後寫的不在裡面)
	delta     store.CacheDelta      // 這次改了什麼:Save 只寫這些
	cleared   int64                 // 最近一次 Save 的 ClearRecent 真的刪掉幾筆
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
	return &Cache{Playlists: pls, Recent: recent}
}

// Save 把這次的改動寫進 state.db(一筆交易,見 store.ApplyCache)。只寫改動,所以別的行程在這之間寫的、
// 剛登出的平台被清掉的、history clear 清掉的都不會被這一份讀到的快取蓋回去;Load 失敗(讀到的是空的)也一樣只寫改動。
// 沒有改動就不開 db;寫成了就把改動清掉——再存一次不會重做(例如不會再清一次別人剛加的最近項目)。
func (c *Cache) Save() error {
	d := c.delta
	if !d.ClearRecent && d.Playlists == nil && len(d.Recent) == 0 {
		return nil
	}
	s, err := store.Open(busy)
	if err != nil {
		return err
	}
	defer s.Close()
	d.MaxRecent = MaxRecent
	n, err := s.ApplyCache(d)
	if err != nil {
		return err
	}
	c.delta, c.cleared = store.CacheDelta{}, n
	return nil
}

// Cleared:最近一次 Save 的 ClearRecent 真的刪掉幾筆(history clear 要說的數字;不能拿讀到的那一份算——Load 失敗時它是空的)。
func (c *Cache) Cleared() int64 { return c.cleared }

func (c *Cache) SetPlaylists(providerID string, pls []Playlist) {
	c.Playlists[providerID] = pls
	if c.delta.Playlists == nil {
		c.delta.Playlists = map[string][]Playlist{}
	}
	c.delta.Playlists[providerID] = pls
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
	c.delta.Recent = append(c.delta.Recent, r)
}

// ClearRecent 清空最近項目;這之前加的一起作廢,之後加的照樣寫。
func (c *Cache) ClearRecent() {
	c.Recent = nil
	c.delta.ClearRecent, c.delta.Recent = true, nil
}

// forgetWait:登出時等別的 capy 放鎖的上限。比 busy 長:這裡不是 TAB 補全,而且等不到就等於沒清掉(要照實說)。
const forgetWait = 5 * time.Second

// Forget 刪掉某個 provider 快取的清單名稱與最近項目(auth logout 時)。跟 Load 不同,失敗照實回錯:
// 呼叫端要說「已登出,但快取沒清掉」,不能讓人以為平台的資料已經從這台電腦上拿掉了。
// 舊檔(schema 升版時留下的 state.db.v<N>)與 T6 之前的 cache.json 也一起清:它們也存著平台的清單名稱與最近項目。
// 舊檔在 live db 之後才清——store.Open 可能剛把 live db 退役成一個新的舊檔。
func Forget(provider string) error {
	var errs []error
	if s, err := store.Open(forgetWait); err != nil {
		errs = append(errs, err)
	} else {
		errs = append(errs, s.ForgetProvider(provider), s.Close())
	}
	errs = append(errs, store.ForgetProviderInRetired(provider, forgetWait))
	if dir, err := config.Dir(); err == nil {
		if err := os.Remove(filepath.Join(dir, legacyFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
