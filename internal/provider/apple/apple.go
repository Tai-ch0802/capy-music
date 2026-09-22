package apple

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// Provider:Apple 的「資料能力」在所有 OS 可用,「播放能力」只在 macOS(spec §2 解耦)。
type Provider struct {
	c          *Client
	storefront string
}

var (
	_ provider.Provider           = (*Provider)(nil)
	_ provider.Searcher           = (*Provider)(nil)
	_ provider.ArtistSearcher     = (*Provider)(nil)
	_ provider.PlaylistReader     = (*Provider)(nil)
	_ provider.PlaybackController = (*Provider)(nil)
	_ provider.ISRCLookup         = (*Provider)(nil)
	_ provider.TrackGetter        = (*Provider)(nil)
	_ provider.PlaylistWriter     = (*Provider)(nil)
	_ provider.PlaylistCreator    = (*Provider)(nil)
)

// ErrNotRunning:Music.app 沒開。State 不會替使用者把它啟動起來(Play 會)。
// 包住 provider.ErrPlayerNotRunning,cli 只認抽象的那個、不必 import 本套件;放在無 build tag 的檔案讓 Windows 也編得過。
var ErrNotRunning = fmt.Errorf("Music.app 未執行(capy play 會把它啟動):%w", provider.ErrPlayerNotRunning)

func New(hc *http.Client, base, devToken, userToken, storefront string) *Provider {
	return &Provider{c: NewClient(hc, base, devToken, userToken), storefront: storefront}
}

func (p *Provider) ID() string          { return "apple" }
func (p *Provider) DisplayName() string { return "Apple Music" }

func (p *Provider) Caps() provider.Capability {
	caps := provider.CapSearch | provider.CapISRCExpose | provider.CapISRCLookup | provider.CapPlaylistRead | provider.CapArtistSearch |
		// 寫入五項(決策 49;2026-09-22 真帳號各打過一次):建 / append 官方端點,remove / reorder 用 PUT 整批取代,rename 用 PATCH。
		provider.CapPlaylistCreate | provider.CapPlaylistAppend | provider.CapPlaylistRemove | provider.CapPlaylistReorder | provider.CapPlaylistRename
	// 不宣告 CapPlayPlaylist:library 清單沒有 catalog URL,music:// 播放清單未驗證(UX 計畫 R4)。
	if playbackSupported {
		caps |= provider.CapPlaybackControl
	}
	return caps
}

// Health:storefront 是最便宜的「dev token + MUT 都有效」驗證。
func (p *Provider) Health(ctx context.Context) error {
	_, err := p.c.Storefront(ctx)
	return err
}

func (p *Provider) Search(ctx context.Context, q provider.Query) ([]provider.Track, error) {
	return p.c.SearchSongs(ctx, p.storefront, q.Text, q.Limit)
}

func (p *Provider) SearchArtists(ctx context.Context, q provider.Query) ([]provider.Artist, error) {
	return p.c.SearchArtists(ctx, p.storefront, q.Text, q.Limit)
}

func (p *Provider) ArtistTopTracks(ctx context.Context, a provider.Artist) ([]provider.Track, error) {
	return p.c.ArtistTopSongs(ctx, p.storefront, a.ProviderID)
}

func (p *Provider) ListPlaylists(ctx context.Context) ([]provider.PlaylistRef, error) {
	return p.c.LibraryPlaylists(ctx)
}

func (p *Provider) LookupISRC(ctx context.Context, isrc string) ([]provider.Track, error) {
	return p.c.SongsByISRC(ctx, p.storefront, isrc)
}

func (p *Provider) GetTrack(ctx context.Context, id string) (provider.Track, error) {
	return p.c.GetSong(ctx, p.storefront, id)
}

func (p *Provider) GetPlaylistItems(ctx context.Context, id string) ([]provider.Track, error) {
	return p.c.LibraryPlaylistTracks(ctx, id)
}

// CreatePlaylist:provider.PlaylistCreator(pl link --create、migrate)。
func (p *Provider) CreatePlaylist(ctx context.Context, name string) (provider.PlaylistRef, error) {
	return p.c.CreatePlaylist(ctx, name)
}

// Pushable:provider.PlaylistWriter。catalog id 與 library 列 id(i.… / a.…)都推得動——官方 type 收 library-songs,
// library-only 曲目在同帳號內加得回去;跟 Spotify local file 不同。只有空 id 不行。
func (p *Provider) Pushable(id string) bool { return id != "" }

// ApplyOps:provider.PlaylistWriter(決策 49)。
//   - 先查清單本體的 canEdit:false(Apple 精選、喜好歌曲)一個請求都不送——不然是 500。
//   - rename → PATCH(先做便宜的,同 Spotify)。
//   - 純尾端 append(want 的前綴 = current)→ POST 每批 100,不必重讀;migrate 走這條。
//   - 其他(remove / move / 插到中間)→ 重讀 /tracks 拿列 id,ProviderID 序列對不上 current 就零寫入(這次重讀就是 §6.5.2 規則 6 的併發比對),
//     對得上就一次 PUT:既有列用列 id + library-songs、新曲用 catalog id + songs(R-8 第 1 項驗過混型)。同一首兩列共用列 id,
//     PUT 依出現次數留幾列(R-8 第 2 項),所以 pl dedup 拿掉後面那份是準的。
//
// 失敗語意:PUT 是伺服器端整批、沒有半截;分批 POST 第二批起失敗回 *provider.PartialWriteError(Written = current 加已 append 的數)。
// ⚠️ want 為空的 PUT 是清空整份:CLAUDE.md 硬約束要求呼叫端先過 dry-run、刪除閾值與確認(決策 18、§6.5.2 規則 5),SPI 本身不擋。
// 不送 DELETE …/tracks(見 client.go 寫入段的說明)。
func (p *Provider) ApplyOps(ctx context.Context, id string, current []string, ops []provider.PlaylistOp) ([]provider.PlaylistOp, error) {
	want, name, err := provider.ApplyPlaylistOps(current, ops)
	if err != nil {
		return nil, err
	}
	itemsChanged := !slices.Equal(want, current)
	if !itemsChanged && name == "" {
		return nil, nil
	}
	if itemsChanged {
		for i, tid := range want {
			if tid == "" {
				return nil, fmt.Errorf("第 %d 首的 id 為空,整批不送", i+1)
			}
		}
	}
	info, err := p.c.Playlist(ctx, id)
	if err != nil {
		return nil, writeErr(id, err)
	}
	if !info.CanEdit {
		return nil, fmt.Errorf("Apple 只讓你編輯自己建的清單;「%s」(%s)不是——Apple 精選、喜好歌曲、已購買的音樂都不能寫", info.Name, id)
	}
	renamed := false
	if name != "" {
		if err := p.c.Rename(ctx, id, name); err != nil {
			return nil, writeErr(id, err)
		}
		renamed = true
	}
	if !itemsChanged {
		return nil, nil
	}
	if n := len(current); len(want) > n && slices.Equal(want[:n], current) { // 純尾端 append:官方端點,不重讀
		written, err := p.c.AddTracks(ctx, id, want[n:])
		if err != nil {
			if written > 0 {
				return nil, &provider.PartialWriteError{PlaylistID: id, Written: n + written, Want: len(want), Renamed: renamed, Err: writeErr(id, err)}
			}
			return nil, writeErr(id, err)
		}
		return nil, nil
	}
	entries, err := p.c.libraryPlaylistEntries(ctx, id)
	if errors.Is(err, provider.ErrNotFound) { // 空清單的 /tracks 回 404
		entries, err = nil, nil
	}
	if err != nil {
		return nil, writeErr(id, err)
	}
	live := make([]string, len(entries))
	byID := make(map[string]string, len(entries)) // ProviderID → 列 id(同一首多列共用同一個列 id,取第一個即可)
	for i, e := range entries {
		live[i] = e.Track.ProviderID
		if _, ok := byID[live[i]]; !ok {
			byID[live[i]] = e.ID
		}
	}
	if !slices.Equal(live, current) {
		return nil, fmt.Errorf("Apple 清單 %s 在讀取之後已經變了,這次不寫(先 capy pl pull 再推)", id)
	}
	refs := make([]trackRef, len(want))
	for i, tid := range want {
		if eid, ok := byID[tid]; ok {
			refs[i] = refOf(eid)
		} else {
			refs[i] = refOf(tid)
		}
	}
	if err := p.c.ReplaceTracks(ctx, id, refs); err != nil {
		return nil, writeErr(id, err)
	}
	return nil, nil
}
