package apple

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func TestProviderIdentityAndCaps(t *testing.T) {
	p := New(http.DefaultClient, "", "DEV", "MUT", "tw")
	if p.ID() != "apple" || p.DisplayName() != "Apple Music" {
		t.Errorf("identity:(%s, %s)", p.ID(), p.DisplayName())
	}
	base := provider.CapSearch | provider.CapISRCExpose | provider.CapPlaylistRead
	if !p.Caps().Has(base) || p.Caps().Has(provider.CapPlaylistRemove) {
		t.Errorf("Caps = %b", p.Caps())
	}
	if p.Caps().Has(provider.CapPlaybackControl) != (runtime.GOOS == "darwin") {
		t.Errorf("CapPlaybackControl 應只在 darwin:GOOS=%s caps=%b", runtime.GOOS, p.Caps())
	}
}

func TestProviderSearchUsesStorefront(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog/jp/search" {
			t.Errorf("應用建構時的 storefront:%s", r.URL.Path)
		}
		fmt.Fprintf(w, `{"results":{"songs":{"data":[%s]}}}`, songJSONFx("s1"))
	})
	p := &Provider{c: c, storefront: "jp"}
	ts, err := p.Search(context.Background(), provider.Query{Text: "x", Limit: 5})
	if err != nil || len(ts) != 1 {
		t.Fatalf("(%d, %v)", len(ts), err)
	}
}

func TestCapsArtistSearchButNoPlaylistPlay(t *testing.T) {
	caps := New(nil, "https://x", "d", "u", "tw").Caps()
	if !caps.Has(provider.CapArtistSearch) {
		t.Error("Apple 應宣告 CapArtistSearch")
	}
	if caps.Has(provider.CapPlayPlaylist) {
		t.Error("Apple 不得宣告 CapPlayPlaylist(R4:清單播放 URL 未驗證)")
	}
}

func TestProviderDeclaresISRCLookupAndUsesStorefront(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(srv.Close)
	p := New(srv.Client(), srv.URL, "DEV", "MUT", "jp")
	if !p.Caps().Has(provider.CapISRCLookup) {
		t.Fatal("P4 T1:Apple 要宣告 CapISRCLookup")
	}
	if _, err := p.LookupISRC(context.Background(), "TWA472400123"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/catalog/jp/songs" {
		t.Fatalf("LookupISRC 要帶 provider 的 storefront:%s", gotPath)
	}
	if _, err := p.GetTrack(context.Background(), "x"); !errors.Is(err, provider.ErrNotFound) && err != nil {
		t.Fatalf("GetTrack 空 data 應是 ErrNotFound:%v", err)
	}
}
