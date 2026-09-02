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
)

// Has 回報 c 是否包含 want 的全部能力位。
func (c Capability) Has(want Capability) bool { return c&want == want }

// 語意化錯誤:provider 實作把傳輸層錯誤映射到這些,CLI 層轉成可行動的訊息。
var (
	ErrAuthExpired    = errors.New("授權已過期")
	ErrNoActiveDevice = errors.New("沒有作用中的播放裝置")
	ErrRestricted     = errors.New("平台不提供此內容")
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
	TrackIDs []string // provider 內部 ID;空 = 恢復播放
	DeviceID string   // 空 = 目前作用中裝置
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
