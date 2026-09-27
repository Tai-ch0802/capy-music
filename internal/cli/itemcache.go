package cli

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

// 決策 57(計畫 2026-09-24 §1.7 S3):同步每一輪都把每份連結清單的每一頁重讀一次;Spotify 的清單列表本來就附了
// snapshot_id(清單有任何變動就換),所以「上次讀過之後沒變」的清單改用本機快取,不打平台。

// itemCacheTTL:快取最多用這麼久。Spotify Developer Terms IV.3.2 只准為了效能暫時快取 metadata、不可無限期;
// 曲目本身的 metadata(下架、換版本)也可能在清單版本不變時變動,一週重讀一次。
const itemCacheTTL = 7 * 24 * time.Hour

// cachedTrack:快取只放同步要用的欄位——不放 Raw 與封面、試聽這些顯示用的欄位(決策 42:那些不落 SQLite)。
// provider.Track 加欄位時要想清楚同步要不要用它,要就加在這裡(白名單,不是整個結構照存)。
type cachedTrack struct {
	ID         string   `json:"id"`
	ISRC       string   `json:"isrc,omitempty"`
	Title      string   `json:"title"`
	Artists    []string `json:"artists"`
	Album      string   `json:"album,omitempty"`
	DurationMS int      `json:"duration_ms"`
	Explicit   bool     `json:"explicit,omitempty"`
	Unpushable bool     `json:"unpushable,omitempty"`
}

// playlistItems:讀一份連結清單的曲目。平台給版本(ref.Version)、快取的版本相同、曲目數等於列表的 Total、一週內 → 用快取;
// 其他一律真的讀,讀到了就記下來。快取讀寫失敗都不擋這一輪(它只是省呼叫的快取)。s.st 為 nil(沒有本機 db)就不快取。
// 安全網:push 在寫之前會再真的讀一次、跟計畫比(pushPlan.apply),快取就算錯了也寫不出錯的東西。
func playlistItems(ctx context.Context, s *canonState, prov string, r provider.PlaylistReader, ref provider.PlaylistRef) ([]provider.Track, error) {
	if s.st != nil && ref.Version != "" {
		if it, ok, err := s.st.CachedPlaylistItems(prov, ref.ID); err == nil && ok && it.Version == ref.Version && itemCacheNow().Sub(it.FetchedAt) < itemCacheTTL {
			var cached []cachedTrack
			if json.Unmarshal(it.Tracks, &cached) == nil && len(cached) == ref.Total {
				out := make([]provider.Track, len(cached))
				for i, t := range cached {
					out[i] = provider.Track{ProviderID: t.ID, ISRC: t.ISRC, Title: t.Title, Artists: t.Artists, Album: t.Album,
						DurationMS: t.DurationMS, Explicit: t.Explicit, Unpushable: t.Unpushable}
				}
				return out, nil
			}
		}
	}
	tracks, err := r.GetPlaylistItems(ctx, ref.ID)
	if err != nil || s.st == nil || ref.Version == "" {
		return tracks, err
	}
	cached := make([]cachedTrack, len(tracks))
	for i, t := range tracks {
		cached[i] = cachedTrack{ID: t.ProviderID, ISRC: t.ISRC, Title: t.Title, Artists: t.Artists, Album: t.Album,
			DurationMS: t.DurationMS, Explicit: t.Explicit, Unpushable: t.Unpushable}
	}
	if b, err := json.Marshal(cached); err == nil {
		now := itemCacheNow()
		_ = s.st.SavePlaylistItems(prov, ref.ID, store.PlaylistItems{Version: ref.Version, Tracks: b, FetchedAt: now}, now.Add(-itemCacheTTL))
	}
	return tracks, nil
}

// forgetPlaylistItems:這份清單剛被我們寫過,下一輪一定真的讀。
func forgetPlaylistItems(s *canonState, prov, link string) {
	if s.st != nil {
		_ = s.st.ForgetPlaylistItems(prov, link)
	}
}

// itemCacheNow:快取的時鐘。測試替換點。
var itemCacheNow = time.Now
