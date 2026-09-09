package spotify

import (
	"context"
	"errors"
	"net/http"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// Provider 以 Client 實作 SPI。認證注入自 *http.Client(oauth2)。
type Provider struct {
	c *Client
}

// 介面契約以 compile-time 釘住(P6 的 SPI 通用性驗證前提)。
var (
	_ provider.Provider           = (*Provider)(nil)
	_ provider.Searcher           = (*Provider)(nil)
	_ provider.ArtistSearcher     = (*Provider)(nil)
	_ provider.PlaylistReader     = (*Provider)(nil)
	_ provider.PlaybackController = (*Provider)(nil)
	_ provider.ISRCLookup         = (*Provider)(nil)
	_ provider.TrackGetter        = (*Provider)(nil)
	_ provider.PlaylistWriter     = (*Provider)(nil)
)

func New(hc *http.Client, base string) *Provider {
	return &Provider{c: NewClient(hc, base)}
}

func (p *Provider) ID() string          { return "spotify" }
func (p *Provider) DisplayName() string { return "Spotify" }

func (p *Provider) Caps() provider.Capability {
	return provider.CapSearch | provider.CapISRCExpose | provider.CapISRCLookup | provider.CapPlaylistRead | provider.CapPlaybackControl |
		provider.CapArtistSearch | provider.CapPlayPlaylist | provider.CapPlayQueue |
		provider.CapPlaylistAppend | provider.CapPlaylistRemove | provider.CapPlaylistReorder | provider.CapPlaylistRename // P5 T1:ApplyOps 整批取代
}

func (p *Provider) ApplyOps(ctx context.Context, id string, current []string, ops []provider.PlaylistOp) ([]provider.PlaylistOp, error) {
	return p.c.ApplyOps(ctx, id, current, ops)
}

func (p *Provider) Pushable(id string) bool { return p.c.Pushable(id) }

func (p *Provider) LookupISRC(ctx context.Context, isrc string) ([]provider.Track, error) {
	return p.c.LookupISRC(ctx, isrc)
}

func (p *Provider) GetTrack(ctx context.Context, id string) (provider.Track, error) {
	return p.c.GetTrack(ctx, id)
}

// Health:devices 是最便宜的授權+連線驗證(doctor 用)。
func (p *Provider) Health(ctx context.Context) error {
	_, err := p.c.Devices(ctx)
	return err
}

func (p *Provider) Search(ctx context.Context, q provider.Query) ([]provider.Track, error) {
	return p.c.SearchTracks(ctx, q.Text, q.Limit)
}

func (p *Provider) SearchArtists(ctx context.Context, q provider.Query) ([]provider.Artist, error) {
	return p.c.SearchArtists(ctx, q.Text, q.Limit)
}

func (p *Provider) ArtistTopTracks(ctx context.Context, a provider.Artist) ([]provider.Track, error) {
	return p.c.ArtistTopTracks(ctx, a)
}

func (p *Provider) ListPlaylists(ctx context.Context) ([]provider.PlaylistRef, error) {
	return p.c.MyPlaylists(ctx)
}

func (p *Provider) GetPlaylistItems(ctx context.Context, id string) ([]provider.Track, error) {
	return p.c.PlaylistItems(ctx, id)
}

func (p *Provider) Devices(ctx context.Context) ([]provider.Device, error) { return p.c.Devices(ctx) }
func (p *Provider) State(ctx context.Context) (*provider.PlaybackState, error) {
	return p.c.State(ctx)
}

func (p *Provider) Play(ctx context.Context, req provider.PlayRequest) error {
	if req.PlaylistID != "" {
		if len(req.TrackIDs) > 0 {
			return errors.New("PlaylistID 與 TrackIDs 擇一")
		}
		return p.c.PlayContext(ctx, "spotify:playlist:"+req.PlaylistID, req.DeviceID)
	}
	uris := make([]string, len(req.TrackIDs))
	for i, id := range req.TrackIDs {
		uris[i] = "spotify:track:" + id
	}
	return p.c.Play(ctx, uris, req.DeviceID)
}

func (p *Provider) Pause(ctx context.Context) error { return p.c.Pause(ctx) }
func (p *Provider) Next(ctx context.Context) error  { return p.c.Next(ctx) }
func (p *Provider) Prev(ctx context.Context) error  { return p.c.Prev(ctx) }

func (p *Provider) Seek(ctx context.Context, posMS int) error    { return p.c.Seek(ctx, posMS) }
func (p *Provider) SetVolume(ctx context.Context, pct int) error { return p.c.SetVolume(ctx, pct) }
