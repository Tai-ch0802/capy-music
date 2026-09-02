package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/provider/spotify"
)

// fakeProvider:只實作 Provider,沒有任何能力介面。
type fakeProvider struct{ caps provider.Capability }

func (f fakeProvider) ID() string                   { return "fake" }
func (f fakeProvider) DisplayName() string          { return "Fake" }
func (f fakeProvider) Caps() provider.Capability    { return f.caps }
func (f fakeProvider) Health(context.Context) error { return nil }

func swapProviderWith(t *testing.T, p provider.Provider) {
	t.Helper()
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) { return p, nil }
	t.Cleanup(func() { newProvider = orig })
}

func TestUnknownProviderFlag(t *testing.T) {
	_, err := runCLI(t, "search", "x", "--provider", "tidal")
	if err == nil || !strings.Contains(err.Error(), "tidal") || !strings.Contains(err.Error(), "spotify") {
		t.Fatalf("未知 provider 應列出可用值:%v", err)
	}
}

func TestCapabilityGateGivesActionableError(t *testing.T) {
	swapProviderWith(t, fakeProvider{caps: provider.CapSearch})
	_, err := runCLI(t, "pause")
	if err == nil || !errors.Is(err, provider.ErrNotSupported) || !strings.Contains(err.Error(), "Fake") {
		t.Fatalf("缺 CapPlaybackControl 應回 ErrNotSupported 且點名平台:%v", err)
	}
	_, err = runCLI(t, "pl", "list")
	if err == nil || !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("缺 CapPlaylistRead 應回 ErrNotSupported:%v", err)
	}
}

func TestDefaultProviderIsSpotify(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"devices":[]}`))
	}))
	t.Cleanup(srv.Close)
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id != "spotify" {
			t.Errorf("預設 provider 應為 spotify,得到 %q", id)
		}
		return spotify.New(srv.Client(), srv.URL), nil
	}
	t.Cleanup(func() { newProvider = orig })
	if _, err := runCLI(t, "devices"); err != nil {
		t.Fatal(err)
	}
}

func TestFriendlyErrMentionsProvider(t *testing.T) {
	err := friendlyErr("apple", provider.ErrAuthExpired)
	if !strings.Contains(err.Error(), "capy auth login apple") {
		t.Errorf("訊息應指向對應 provider 的 login:%v", err)
	}
}
