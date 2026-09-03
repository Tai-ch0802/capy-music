package apple

import (
	"context"
	"fmt"
	"net/http"
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
