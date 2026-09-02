//go:build !darwin

package apple

import (
	"context"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 非 macOS:Apple Music 沒有可程式控制的播放器(spec §1.2)。
const playbackSupported = false

func (p *Provider) Devices(context.Context) ([]provider.Device, error) {
	return nil, provider.ErrNotSupported
}
func (p *Provider) State(context.Context) (*provider.PlaybackState, error) {
	return nil, provider.ErrNotSupported
}
func (p *Provider) Play(context.Context, provider.PlayRequest) error { return provider.ErrNotSupported }
func (p *Provider) Pause(context.Context) error                      { return provider.ErrNotSupported }
func (p *Provider) Next(context.Context) error                       { return provider.ErrNotSupported }
func (p *Provider) Prev(context.Context) error                       { return provider.ErrNotSupported }
