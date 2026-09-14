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
	want := provider.CapSearch | provider.CapISRCExpose | provider.CapISRCLookup | provider.CapPlaylistRead | provider.CapPlaybackControl |
		provider.CapArtistSearch | provider.CapPlayPlaylist | provider.CapPlayQueue |
		provider.CapPlaylistAppend | provider.CapPlaylistRemove | provider.CapPlaylistReorder | provider.CapPlaylistRename |
		provider.CapPlaylistCreate
	if p.Caps() != want {
		t.Errorf("Caps = %b, want %b", p.Caps(), want)
	}
}

// 建清單:POST /me/playlists(舊的 /users/{id}/playlists 已移除)、明講 public:false、回應的 id 就是之後 link 的 id。
// 斷言放在 handler 裡:t.Errorf 可跨 goroutine,共用變數不行。
func TestCreatePlaylist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Method != http.MethodPost || r.URL.Path != "/me/playlists" || body["name"] != "公路旅行" || body["public"] != false {
			t.Errorf("要 POST /me/playlists、帶名字、明講 public:false:%s %s %v", r.Method, r.URL.Path, body)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"new1","name":"公路旅行","owner":{"display_name":"tai"},"items":{"total":0}}`))
	}))
	defer srv.Close()
	ref, err := New(srv.Client(), srv.URL).CreatePlaylist(context.Background(), "公路旅行")
	if err != nil {
		t.Fatal(err)
	}
	if ref != (provider.PlaylistRef{ID: "new1", Name: "公路旅行", Owner: "tai"}) {
		t.Errorf("ref = %+v", ref)
	}
	// 回應沒有 id(形狀變了):不可回一個空 id 讓 CLI 寫進 link。
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	defer empty.Close()
	if _, err := New(empty.Client(), empty.URL).CreatePlaylist(context.Background(), "x"); err == nil {
		t.Fatal("回應沒有 id 要回錯")
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

func TestProviderDeclaresISRCLookup(t *testing.T) {
	p := New(http.DefaultClient, "http://127.0.0.1:0")
	if !p.Caps().Has(provider.CapISRCLookup) {
		t.Fatal("P4 T1:Spotify 要宣告 CapISRCLookup(LookupISRC / GetTrack 已實作)")
	}
}
