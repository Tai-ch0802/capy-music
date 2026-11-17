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
	if err := p.Transfer(context.Background(), "music.app", false); !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("非 darwin Transfer 應回 ErrNotSupported:%v", err)
	}
	if err := p.SetShuffle(context.Background(), true); !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("非 darwin SetShuffle 應回 ErrNotSupported:%v", err)
	}
	if err := p.SetRepeat(context.Background(), provider.RepeatOff); !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("非 darwin SetRepeat 應回 ErrNotSupported:%v", err)
	}
}
