package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func TestProviderIdentityAndCaps(t *testing.T) {
	p := New(http.DefaultClient, "")
	if p.ID() != "spotify" || p.DisplayName() != "Spotify" {
		t.Errorf("identity:(%s, %s)", p.ID(), p.DisplayName())
	}
	want := provider.CapSearch | provider.CapISRCExpose | provider.CapPlaylistRead | provider.CapPlaybackControl
	if p.Caps() != want {
		t.Errorf("Caps = %b, want %b", p.Caps(), want)
	}
	if p.Caps().Has(provider.CapPlaylistRemove) {
		t.Error("P1 不應宣告寫入能力")
	}
}

func TestProviderPlayBuildsURIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URIs []string `json:"uris"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.URIs) != 2 || body.URIs[0] != "spotify:track:aaa" || body.URIs[1] != "spotify:track:bbb" {
			t.Errorf("URIs = %v", body.URIs)
		}
		if r.URL.Query().Get("device_id") != "d1" {
			t.Errorf("device_id 未帶上")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	p := New(srv.Client(), srv.URL)
	err := p.Play(context.Background(), provider.PlayRequest{TrackIDs: []string{"aaa", "bbb"}, DeviceID: "d1"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProviderSearchDelegates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tracks":{"items":[%s],"total":1}}`, trackFx("t1"))
	}))
	defer srv.Close()
	p := New(srv.Client(), srv.URL)
	ts, err := p.Search(context.Background(), provider.Query{Text: "x", Limit: 5})
	if err != nil || len(ts) != 1 {
		t.Fatalf("(%d, %v)", len(ts), err)
	}
}
