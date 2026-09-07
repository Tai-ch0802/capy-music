package apple

import (
	"context"
	"fmt"
	"net/http"

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
	caps := provider.CapSearch | provider.CapISRCExpose | provider.CapPlaylistRead | provider.CapArtistSearch
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

func (p *Provider) GetPlaylistItems(ctx context.Context, id string) ([]provider.Track, error) {
	return p.c.LibraryPlaylistTracks(ctx, id)
}
