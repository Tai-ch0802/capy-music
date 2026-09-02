//go:build darwin

package apple

import (
	"context"
	"errors"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const playbackSupported = true

var errTODO = errors.New("apple darwin 播放於 T6 實作")

func (p *Provider) Devices(context.Context) ([]provider.Device, error)     { return nil, errTODO }
func (p *Provider) State(context.Context) (*provider.PlaybackState, error) { return nil, errTODO }
func (p *Provider) Play(context.Context, provider.PlayRequest) error       { return errTODO }
func (p *Provider) Pause(context.Context) error                            { return errTODO }
func (p *Provider) Next(context.Context) error                             { return errTODO }
func (p *Provider) Prev(context.Context) error                             { return errTODO }
