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
	want := provider.CapSearch | provider.CapISRCExpose | provider.CapPlaylistRead | provider.CapPlaybackControl |
		provider.CapArtistSearch | provider.CapPlayPlaylist | provider.CapPlayQueue
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

func TestProviderPlayPlaylistUsesContextAndIsExclusive(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	p := New(srv.Client(), srv.URL)
	if !p.Caps().Has(provider.CapArtistSearch | provider.CapPlayPlaylist) {
		t.Fatal("Spotify 應宣告 CapArtistSearch 與 CapPlayPlaylist")
	}
	if err := p.Play(context.Background(), provider.PlayRequest{PlaylistID: "p1"}); err != nil {
		t.Fatal(err)
	}
	if gotBody["context_uri"] != "spotify:playlist:p1" {
		t.Errorf("PlaylistID 應轉成 context_uri:%v", gotBody)
	}
	if err := p.Play(context.Background(), provider.PlayRequest{PlaylistID: "p1", TrackIDs: []string{"t"}}); err == nil {
		t.Fatal("PlaylistID 與 TrackIDs 同時給應報錯")
	}
}
