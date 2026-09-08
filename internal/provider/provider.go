// Package provider 定義 capability-based 的 Provider SPI(spec §3 的 P1 子集)。
// PlaylistWriter/PlaylistOp 延後到 P5;ISRCLookup / TrackGetter 於 P4 T1(2026-09-08)加入,resolver 的 Layer 1 用。
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
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
	CapPlayQueue    // Play 會把 TrackIDs 全部排進佇列;沒有此能力的 provider 只播第一首
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
	// ErrPlayerNotRunning:本機播放器 app 沒開(Apple Music.app)。State 回它而不是把 app 啟動起來;是狀態不是失敗。
	ErrPlayerNotRunning = errors.New("播放器未執行")
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

// ISRCLookup:以 ISRC 反查曲目(CapISRCLookup)。實作先 NormalizeISRC,不合格回 ErrBadISRC 不打 API;
// 回傳可能多筆(Apple 的 filter[isrc] 明文可回多筆,Spotify 的 isrc: 查詢也會把單曲 / 專輯 / 合輯版本都回來),
// 原樣全部回傳,消歧是 resolver 的事(spec §5.1)。沒有命中回空 slice、不是錯誤。
type ISRCLookup interface {
	LookupISRC(ctx context.Context, isrc string) ([]Track, error)
}

// TrackGetter:以 provider 內部 id 取單曲(釘選前確認 id 存在);找不到回 ErrNotFound。
type TrackGetter interface {
	GetTrack(ctx context.Context, id string) (Track, error)
}

// ErrBadISRC:正規化後不是 12 碼英數。
var ErrBadISRC = errors.New("ISRC 格式不對(要 12 碼英數)")

var (
	isrcRe    = regexp.MustCompile(`^[A-Z0-9]{12}$`)
	isrcStrip = strings.NewReplacer("-", "", " ", "") // package 層級:每次 NewReplacer 配置 6 KB,萬首清單就是 60 MB
)

// NormalizeISRC:大寫、去連字號與空白;非 12 碼視為缺失(回空字串)。canon 的 cid 與各 provider 的反查共用同一個定義。
func NormalizeISRC(s string) string {
	s = strings.ToUpper(isrcStrip.Replace(strings.TrimSpace(s)))
	if !isrcRe.MatchString(s) {
		return ""
	}
	return s
}

// ArtistSearcher:藝人搜尋與熱門歌曲(CapArtistSearch)。
// ArtistTopTracks 的實作可以回近似值(例如平台不開放熱門歌曲端點時,改用依熱門度排序的搜尋結果),
// 呼叫端不要把它當精確的「官方熱門榜」。
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
