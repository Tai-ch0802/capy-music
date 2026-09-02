//go:build !darwin

package apple

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func TestPlaybackNotSupportedOffDarwin(t *testing.T) {
	p := New(http.DefaultClient, "", "DEV", "MUT", "tw")
	if err := p.Pause(context.Background()); !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("非 darwin 應回 ErrNotSupported:%v", err)
	}
	if _, err := p.State(context.Background()); !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("非 darwin 應回 ErrNotSupported:%v", err)
	}
}
