package spotify

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func withLanguage(t *testing.T, lang string) {
	t.Helper()
	prev := i18n.Current()
	if !i18n.Set(lang) {
		t.Fatalf("不支援 %s", lang)
	}
	t.Cleanup(func() { i18n.Set(prev) })
}

// statusServer:每個請求都回 status 與 body。
func statusServer(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.Client(), srv.URL)
}

// 英文模式:capy 自己的措辭是英文,Spotify 回的 message / reason 原樣留在後面。
func TestSpotifyErrorsEnglish(t *testing.T) {
	withLanguage(t, "en")
	ctx := context.Background()

	// 推不出去的曲目:依筆數選單複數,每筆「track <n> <id>」以英文分隔符串起來
	c, log := writeServer(t, 0, 0)
	_, err := c.ApplyOps(ctx, "p1", nil, []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "spotify:local:x", Pos: 0}})
	if err == nil || err.Error() != `a track can't be pushed (a local file or an empty id), so nothing was sent: track 1 "spotify:local:x"` {
		t.Errorf("one: %v", err)
	}
	_, err = c.ApplyOps(ctx, "p1", nil, []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "spotify:local:x", Pos: 0}, {Kind: provider.OpAdd, ProviderID: "", Pos: 1}})
	if err == nil || err.Error() != `2 tracks can't be pushed (local files or empty ids), so nothing was sent: track 1 "spotify:local:x", track 2 ""` {
		t.Errorf("other: %v", err)
	}
	if len(log.all()) != 0 {
		t.Fatal("推不出去就一個請求都不送")
	}

	add := []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "b", Pos: 1}}
	fc, _ := writeServer(t, http.StatusForbidden, 0)
	if _, err := fc.ApplyOps(ctx, "p1", []string{"a"}, add); err == nil || err.Error() != "Spotify refused to write playlist p1 (only your own playlists and collaborative playlists can be written): spotify API 403  nope" {
		t.Errorf("403: %v", err)
	}
	nc, _ := writeServer(t, http.StatusNotFound, 0)
	if _, err := nc.ApplyOps(ctx, "p1", []string{"a"}, add); err == nil || err.Error() != "Spotify can't find playlist p1 (or the write endpoint isn't /items; see spec §1.1): spotify API 404  nope" {
		t.Errorf("404: %v", err)
	}

	vc := statusServer(t, http.StatusForbidden, `{"error":{"status":403,"reason":"VOLUME_CONTROL_DISALLOW","message":"Player command failed"}}`)
	err = vc.SetVolume(ctx, 40)
	if !errors.Is(err, provider.ErrVolumeNotAllowed) || err.Error() != "this device doesn't allow remote volume control (phones and some speakers block it) — adjust the volume on the device itself: spotify API 403 VOLUME_CONTROL_DISALLOW Player command failed" {
		t.Errorf("volume: %v", err)
	}
	var ae *apiError
	if !errors.As(err, &ae) {
		t.Error("原始的 apiError 要留在鏈上")
	}

	gc := statusServer(t, http.StatusNotFound, `{"error":{"status":404,"message":"Not found."}}`)
	if _, err := gc.GetTrack(ctx, "t1"); !errors.Is(err, provider.ErrNotFound) || err.Error() != "not found: track t1" {
		t.Errorf("GetTrack: %v", err)
	}
	if _, err := gc.PlaylistItems(ctx, "p9"); !errors.Is(err, provider.ErrNotFound) || err.Error() != "not found: playlist p9" {
		t.Errorf("PlaylistItems: %v", err)
	}
	if _, err := gc.LookupISRC(ctx, "nope"); !errors.Is(err, provider.ErrBadISRC) || err.Error() != `invalid ISRC (must be 12 letters or digits): "nope"` {
		t.Errorf("LookupISRC: %v", err)
	}
	if _, err := statusServer(t, http.StatusCreated, `{"name":"x"}`).CreatePlaylist(ctx, "x"); err == nil || err.Error() != "Spotify's response to creating the playlist had no id" {
		t.Errorf("CreatePlaylist: %v", err)
	}
	p := &Provider{c: gc}
	if err := p.Play(ctx, provider.PlayRequest{PlaylistID: "p", TrackIDs: []string{"t"}}); err == nil || err.Error() != "use either PlaylistID or TrackIDs, not both" {
		t.Errorf("Play: %v", err)
	}
}

// zh-TW:拆成「每筆 + 分隔符 + 整句」之後,組出來的字與拆開前一樣。
func TestUnpushableMessageZhTW(t *testing.T) {
	withLanguage(t, "zh-TW")
	c, _ := writeServer(t, 0, 0)
	_, err := c.ApplyOps(context.Background(), "p1", nil, []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "spotify:local:x", Pos: 0}, {Kind: provider.OpAdd, ProviderID: "", Pos: 1}})
	if err == nil || err.Error() != `推不出去的曲目(local file 或空 id),整批不送:第 1 首 "spotify:local:x"、第 2 首 ""` {
		t.Errorf("%v", err)
	}
}

// top-tracks 403 時退回搜尋:提示是一行英文,藝人名以 Go 引號原樣帶出。
func TestTopTracksFallbackNoticeEnglish(t *testing.T) {
	withLanguage(t, "en")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"tracks":{"items":[],"total":0}}`))
	}))
	t.Cleanup(srv.Close)
	var notice bytes.Buffer
	orig := provider.BackoffStderr
	provider.BackoffStderr = &notice
	t.Cleanup(func() { provider.BackoffStderr = orig })
	if _, err := NewClient(srv.Client(), srv.URL).ArtistTopTracks(context.Background(), provider.Artist{ProviderID: "a1", Name: "Mayday"}); err != nil {
		t.Fatal(err)
	}
	if got := notice.String(); got != "Spotify: top-tracks returned 403 (development-mode apps can't use it); approximating with an artist:\"Mayday\" search\n" {
		t.Errorf("notice = %q", got)
	}
}
