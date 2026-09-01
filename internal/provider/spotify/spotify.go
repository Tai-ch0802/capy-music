package spotify

import (
	"context"
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
	_ provider.PlaylistReader     = (*Provider)(nil)
	_ provider.PlaybackController = (*Provider)(nil)
)

func New(hc *http.Client, base string) *Provider {
	return &Provider{c: NewClient(hc, base)}
}

func (p *Provider) ID() string          { return "spotify" }
func (p *Provider) DisplayName() string { return "Spotify" }

func (p *Provider) Caps() provider.Capability {
	// 寫入能力(playlist modify)於 P4/P5 實作 ApplyOps 時再宣告
	return provider.CapSearch | provider.CapISRCExpose | provider.CapPlaylistRead | provider.CapPlaybackControl
}

// Health:devices 是最便宜的授權+連線驗證(doctor 用)。
func (p *Provider) Health(ctx context.Context) error {
	_, err := p.c.Devices(ctx)
	return err
}

func (p *Provider) Search(ctx context.Context, q provider.Query) ([]provider.Track, error) {
	return p.c.SearchTracks(ctx, q.Text, q.Limit)
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
	uris := make([]string, len(req.TrackIDs))
	for i, id := range req.TrackIDs {
		uris[i] = "spotify:track:" + id
	}
	return p.c.Play(ctx, uris, req.DeviceID)
}

func (p *Provider) Pause(ctx context.Context) error { return p.c.Pause(ctx) }
func (p *Provider) Next(ctx context.Context) error  { return p.c.Next(ctx) }
func (p *Provider) Prev(ctx context.Context) error  { return p.c.Prev(ctx) }
