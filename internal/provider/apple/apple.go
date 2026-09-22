package apple

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

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
	// 所有會讓整輪放棄的檢查(canEdit、重讀對齊)都在第一個寫入之前:不然 rename 已落地、再回「這次不寫」就不準,
	// push.go 會把它當「第一個請求就失敗,平台沒動」(PR #80 review)。
	info, err := p.c.Playlist(ctx, id)
	if err != nil {
		return nil, writeErr(id, err)
	}
	if !info.CanEdit {
		return nil, fmt.Errorf("Apple 只讓你編輯自己建的清單;「%s」(%s)不是——Apple 精選、喜好歌曲、已購買的音樂都不能寫", info.Name, id)
	}
	n := len(current)
	appendOnly := itemsChanged && len(want) > n && slices.Equal(want[:n], current)
	var refs []trackRef
	if itemsChanged && !appendOnly {
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
		// a. 列只出現在協作清單(hasCollaboration:true;其他自建清單全是 i. 列)。2026-09-22 對它原序全量 PUT 222 列回 500「Unable to update tracks」、
		// 零變動(計畫 §5 補測)——分不出是 a. id 不被接受還是協作清單不能經這個端點改,先零寫入、講明原因,不讓使用者從 500 猜。
		if i := slices.IndexFunc(entries, func(e libraryEntry) bool { return strings.HasPrefix(e.ID, "a.") }); i >= 0 {
			return nil, fmt.Errorf("Apple 清單「%s」(%s)是協作清單(列 id 是 a.,例如 %s):Apple 對它的整批取代回 500(2026-09-22 實測),capy 目前無法替它移除 / 換序,這次不寫;請在 Apple Music app 裡手動,或把它複製成一般清單再連結", info.Name, id, entries[i].ID)
		}
		refs = make([]trackRef, len(want))
		for i, tid := range want {
			if eid, ok := byID[tid]; ok {
				refs[i] = trackRef{ID: eid, Type: "library-songs"} // 剛從 /tracks 讀回的列 id,型別確定,不猜(猜錯是靜默掉歌)
			} else {
				refs[i] = refOf(tid) // 來自正本 mapping 的 id:catalog 數字 id 或 library 列 id,看形狀
			}
		}
	}
	renamed := false
	if name != "" {
		if err := p.c.Rename(ctx, id, name); err != nil {
			return nil, writeErr(id, err)
		}
		renamed = true
	}
	switch {
	case !itemsChanged:
		return nil, nil
	case appendOnly:
		// 純尾端 append 走官方端點,不重讀:§6.5.2 規則 6 的併發比對由 push.go 的 apply 進場那次重讀承擔(就在 ApplyOps 之前);
		// append 是純加法、不會刪到東西,最差是手機同時加了同一首而多一份重複(下一次 pull 看得到);多讀一次只會放大讀取量(429 不重試)。
		written, err := p.c.AddTracks(ctx, id, want[n:])
		if err != nil {
			if written > 0 {
				return nil, &provider.PartialWriteError{PlaylistID: id, Written: n + written, Want: len(want), Renamed: renamed, Err: writeErr(id, err)}
			}
			return nil, writeErr(id, err)
		}
		return nil, nil
	default:
		if err := p.c.ReplaceTracks(ctx, id, refs); err != nil {
			return nil, writeErr(id, err)
		}
		return nil, nil
	}
}
