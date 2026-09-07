// Package provider 定義 capability-based 的 Provider SPI(spec §3 的 P1 子集)。
// PlaylistWriter/PlaylistOp 延後到 P4/P5;Searcher 的 GetTrack/LookupISRC 於 P4 resolver 進場時補。
package provider

import (
	"context"
	"encoding/json"
	"errors"
)

type Capability uint32

const (
	CapSearch Capability = 1 << iota
	CapISRCLookup
	CapISRCExpose
	CapPlaylistRead
	CapPlaylistCreate
	CapPlaylistAppend
	CapPlaylistRemove  // ⚠️ Apple 待驗證(P0-2)
	CapPlaylistReorder // ⚠️ Apple 待驗證(P0-2)
	CapLibraryRead
	CapLibraryWrite
	CapPlaybackControl
	CapArtistSearch // SearchArtists + ArtistTopTracks(UX 計畫 T3)
	CapPlayPlaylist // PlayRequest.PlaylistID
)

// Has 回報 c 是否包含 want 的全部能力位。
func (c Capability) Has(want Capability) bool { return c&want == want }

// 語意化錯誤:provider 實作把傳輸層錯誤映射到這些,CLI 層轉成可行動的訊息。
var (
	ErrAuthExpired    = errors.New("授權已過期")
	ErrNoActiveDevice = errors.New("沒有作用中的播放裝置")
	ErrRestricted     = errors.New("平台不提供此內容")
	ErrNotSupported   = errors.New("此平台不支援這個操作")
	ErrNotFound       = errors.New("找不到資源")
)

type Track struct {
	ProviderID string
	ISRC       string // 可能為空
	Title      string
	Artists    []string
	Album      string
	DurationMS int
	Explicit   bool
	Raw        json.RawMessage
}

// Artist:藝人;熱門歌曲以 ArtistTopTracks 另取。
type Artist struct {
	ProviderID string
	Name       string
}

type Query struct {
	Text  string
	Limit int // 想要的總數;provider 自行處理單次上限與分頁
}

type Device struct {
	ID        string
	Name      string
	Type      string
	Active    bool
	VolumePct int
}

type PlaybackState struct {
	Playing    bool
	Track      *Track // nil = 無播放內容
	ProgressMS int
	Device     Device
}

type PlayRequest struct {
	TrackIDs   []string // provider 內部 ID;空 = 恢復播放
	DeviceID   string   // 空 = 目前作用中裝置
	PlaylistID string   // 非空 = 以播放清單為 context 播放;與 TrackIDs 互斥(需 CapPlayPlaylist)
}

type PlaylistRef struct {
	ID    string
	Name  string
	Owner string
	Total int
}

type Provider interface {
	ID() string // "spotify" | "apple" | "local"
	DisplayName() string
	Caps() Capability
	Health(ctx context.Context) error
}

type Searcher interface {
	Search(ctx context.Context, q Query) ([]Track, error)
}

// ArtistSearcher:藝人搜尋與熱門歌曲(CapArtistSearch)。
type ArtistSearcher interface {
	SearchArtists(ctx context.Context, q Query) ([]Artist, error)
	ArtistTopTracks(ctx context.Context, artist Artist) ([]Track, error) // 收整個 Artist:Spotify 的備案要用名稱
}

type PlaylistReader interface {
	ListPlaylists(ctx context.Context) ([]PlaylistRef, error)
	GetPlaylistItems(ctx context.Context, id string) ([]Track, error)
}

type PlaybackController interface {
	Devices(ctx context.Context) ([]Device, error)
	State(ctx context.Context) (*PlaybackState, error)
	Play(ctx context.Context, req PlayRequest) error
	Pause(ctx context.Context) error
	Next(ctx context.Context) error
	Prev(ctx context.Context) error
}
