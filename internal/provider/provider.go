// Package provider 定義 capability-based 的 Provider SPI(spec §3 的 P1 子集)。
// ISRCLookup / TrackGetter 於 P4 T1(2026-09-08)加入,resolver 的 Layer 1 用;PlaylistWriter / PlaylistOp 於 P5 T1 加入,pl push 用(spec §6.5.2)。
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	CapPlaylistRename
	CapDeviceBound // P6 決策 33(bit 15):id 只在本裝置有意義;實作者同時實作 DeviceScoped
)

// DeviceScoped(P6 決策 33):綁裝置的 provider 告訴 CLI 某個 id 是不是別台裝置的——是的話 pull / push / sync 一律跳過,
// 不算 gone、不算 refused、不動 base;pl link 只能連本機的、撞到別台的 link 就接管。
type DeviceScoped interface {
	Foreign(id string) bool
}

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
	// ErrVolumeNotAllowed:這個裝置不給遠端調音量(手機、部分喇叭)。是裝置的限制,不是授權問題,
	// 也不是「平台不支援」——同一個平台換一台裝置就可以。
	ErrVolumeNotAllowed = errors.New("這個裝置不允許遠端調整音量")
)

type Track struct {
	ProviderID string
	ISRC       string // 可能為空
	Title      string
	Artists    []string
	Album      string
	DurationMS int
	Explicit   bool
	Unpushable bool // API 加不回去的曲目(Spotify local file、Apple library-only):push 時只配對、不新增(計畫 Q22)
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
	// Seek:跳到曲目內的絕對位置(毫秒)。超過長度的行為由平台決定(Spotify 跳到下一首、Music.app 停在結尾),
	// 這裡不先攔——各平台不一致,攔了反而要維護一份「這個平台幾毫秒才算超過」的規則。
	Seek(ctx context.Context, posMS int) error
	// SetVolume:0-100。呼叫端負責夾範圍;手機與部分喇叭會拒絕(Spotify 403 VOLUME_CONTROL_DISALLOW)。
	SetVolume(ctx context.Context, pct int) error
}

// PlaylistOp:pl push 的一筆操作(spec §3、§6.5.2)。位置語意:依序套用,Pos / From 指的是前面 ops 套完後的狀態;
// move 是「先從 From 拿出來、再插到 Pos」,Pos 指拿出來之後的序列。
type PlaylistOp struct {
	Kind       string // OpAdd | OpRemove | OpMove | OpRename
	ProviderID string // add:要插入的曲目 id;remove:選填,填了會核對該位置真的是它
	Pos, From  int    // add:插入位置;remove:位置;move:From → Pos
	Name       string // rename
}

const (
	OpAdd    = "add"
	OpRemove = "remove"
	OpMove   = "move"
	OpRename = "rename"
)

// PlaylistWriter:把 ops 寫到平台清單(CapPlaylistAppend / Remove / Reorder / Rename)。current 是呼叫端剛觀測到的
// provider id 序列,ops 相對於它(provider 不再讀一次)。Kind 不支援的 op 跳過、支援的照做,回傳跳過的那些
// (呼叫端列成 manual);平台真的失敗才回 err。
type PlaylistWriter interface {
	ApplyOps(ctx context.Context, playlistID string, current []string, ops []PlaylistOp) (skipped []PlaylistOp, err error)
	// Pushable:這個 id 能不能被 add 進清單(Spotify local file 的 uri、Apple library-only 的 id 不能)。純函式,不打 API;
	// push 算變更集時用它決定「有 mapping 但推不出去」要列 skip,而不是送出去被整批拒收(PR #33 review)。
	Pushable(id string) bool
}

// ApplyPlaylistOps:純函式,把 ops 依序套在 current 上,回傳結果序列與改名後的名稱(空 = 沒改名)。
// 位置越界、remove 核對不符、Kind 未知都回錯。provider 實作與 push 的計畫測試共用它,兩邊對「位置」的理解才會一致。
func ApplyPlaylistOps(current []string, ops []PlaylistOp) (items []string, name string, err error) {
	items = slices.Clone(current)
	if items == nil {
		items = []string{}
	}
	for i, op := range ops {
		switch op.Kind {
		case OpAdd:
			if op.Pos < 0 || op.Pos > len(items) {
				return nil, "", fmt.Errorf("op %d add 位置 %d 越界(長度 %d)", i, op.Pos, len(items))
			}
			items = slices.Insert(items, op.Pos, op.ProviderID)
		case OpRemove:
			if op.Pos < 0 || op.Pos >= len(items) {
				return nil, "", fmt.Errorf("op %d remove 位置 %d 越界(長度 %d)", i, op.Pos, len(items))
			}
			if op.ProviderID != "" && items[op.Pos] != op.ProviderID {
				return nil, "", fmt.Errorf("op %d remove 位置 %d 是 %s 不是 %s", i, op.Pos, items[op.Pos], op.ProviderID)
			}
			items = slices.Delete(items, op.Pos, op.Pos+1)
		case OpMove:
			if op.From < 0 || op.From >= len(items) || op.Pos < 0 || op.Pos >= len(items) {
				return nil, "", fmt.Errorf("op %d move %d → %d 越界(長度 %d)", i, op.From, op.Pos, len(items))
			}
			id := items[op.From]
			items = slices.Delete(items, op.From, op.From+1)
			items = slices.Insert(items, op.Pos, id)
		case OpRename:
			if op.Name == "" { // "" 同時代表「沒 rename」,空名字會無聲消失;幾乎一定是呼叫端掉了 Name
				return nil, "", fmt.Errorf("op %d rename 沒有 Name", i)
			}
			name = op.Name
		default:
			return nil, "", fmt.Errorf("op %d 未知的 Kind %q", i, op.Kind)
		}
	}
	return items, name, nil
}

// PartialWriteError:ApplyOps 分批寫到一半失敗(第一批取代成功、後面的批次失敗),平台清單停在被截短的狀態。
// 呼叫端要讓使用者知道清單現在是半截的、把 base 推到現況、並提示重跑 push 補回其餘(spec §6.5.2 規則 7)。
type PartialWriteError struct {
	PlaylistID    string
	Written, Want int  // 平台現在有的首數 / 目標首數
	Renamed       bool // 改名已先成功(items 之前做的)
	Err           error
}

func (e *PartialWriteError) Error() string {
	msg := fmt.Sprintf("清單 %s 寫到一半失敗:平台現在只有前 %d 首(目標 %d 首),重跑 push 補回其餘", e.PlaylistID, e.Written, e.Want)
	if e.Renamed {
		msg += ";名字已先改好"
	}
	return msg + ":" + e.Err.Error()
}

func (e *PartialWriteError) Unwrap() error { return e.Err }
