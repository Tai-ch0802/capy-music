package apple

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 英文模式(T2d):這個套件自己產生的字。清單名一律 ASCII;provider.err.* 哨兵已經搬完,所以整句斷言是安全的。

func withEnglish(t *testing.T) {
	t.Helper()
	prev := i18n.Current()
	if !i18n.Set("en") {
		t.Fatal("en 目錄不在")
	}
	t.Cleanup(func() { i18n.Set(prev) })
}

const (
	enNotEditable   = "it isn't a playlist you created (an Apple-curated playlist, Favorite Songs or Purchased Music), and Apple doesn't allow writing to it"
	enCollaborative = "it's a collaborative playlist: Apple returns 500 when its tracks are replaced in one batch (tested 2026-09-22), so capy doesn't write to it; make the change by hand in the Apple Music app, or copy it into a regular playlist and link that instead (untested, usually works)"
)

// Unwritable 在列清單的當下翻好(push 的拒絕訊息直接接它),不是 init 時的語系。
func TestEnglishUnwritableReasons(t *testing.T) {
	withEnglish(t)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
{"id":"p.apple","attributes":{"name":"Curated","canEdit":false,"hasCollaboration":false}},
{"id":"p.collab","attributes":{"name":"Shared","canEdit":true,"hasCollaboration":true}}]}`))
	})
	pls, err := c.LibraryPlaylists(context.Background())
	if err != nil || len(pls) != 2 {
		t.Fatalf("(%d, %v)", len(pls), err)
	}
	if pls[0].Unwritable != enNotEditable || pls[1].Unwritable != enCollaborative {
		t.Fatalf("Unwritable:\n%q\n%q", pls[0].Unwritable, pls[1].Unwritable)
	}
}

func TestEnglishApplyOpsRefusals(t *testing.T) {
	withEnglish(t)
	ctx := context.Background()
	check := func(what string, err error, want string) {
		t.Helper()
		if err == nil || err.Error() != want {
			t.Errorf("%s:\n got %v\nwant %s", what, err, want)
		}
	}

	f, p := writeWorld(t, false, "c1")
	f.name = "Commute"
	_, err := p.ApplyOps(ctx, "p.1", []string{"c1"}, []provider.PlaylistOp{add("c2", 1)})
	check("canEdit:false", err, `can't write to Apple playlist "Commute" (p.1): `+enNotEditable)

	f, p = writeWorld(t, true, "c1")
	f.name, f.collab = "Commute", true
	_, err = p.ApplyOps(ctx, "p.1", []string{"c1"}, []provider.PlaylistOp{add("c2", 1)})
	check("collaborative", err, `can't write to Apple playlist "Commute" (p.1): `+enCollaborative)

	f, p = writeWorld(t, true)
	f.name, f.entries = "Commute", []fakeEntry{{ID: "a.1", Catalog: "c1"}}
	_, err = p.ApplyOps(ctx, "p.1", []string{"c1"}, []provider.PlaylistOp{add("c9", 0)})
	check("a. rows", err, `Apple playlist "Commute" (p.1) isn't a collaborative playlist, yet its row ids start with a. (for example a.1): as of 2026-09-22 such rows had only been seen in collaborative playlists, where replacing the tracks returns 500, so capy isn't writing this time; please report this`)

	_, p = writeWorld(t, true, "c1", "c2")
	_, err = p.ApplyOps(ctx, "p.1", []string{"c1"}, []provider.PlaylistOp{add("c9", 0)})
	check("changed since read", err, "Apple playlist p.1 changed after capy read it, so nothing was written this time (run capy pl pull, then push again)")

	_, p = writeWorld(t, true, "c1")
	_, err = p.ApplyOps(ctx, "p.1", []string{"c1"}, []provider.PlaylistOp{add("", 1)})
	check("empty id", err, "track 2 has an empty id; nothing was sent")

	f, p = writeWorld(t, true, "c1")
	f.failAt, f.failMsg = 1, "Unable to update tracks"
	_, err = p.ApplyOps(ctx, "p.1", []string{"c1"}, []provider.PlaylistOp{add("c2", 1)})
	check("500 Unable to update", err, "Apple refused to modify playlist p.1 (500 Unable to update tracks): it isn't a playlist you created (an Apple-curated playlist, Favorite Songs or Purchased Music), or it's a collaborative playlist (row ids start with a.; tested 2026-09-22): apple API 500 Unable to update tracks ")
}

func TestEnglishCreatePlaylistMessages(t *testing.T) {
	withEnglish(t)
	orig, origDelays, origErr := provider.Wait, createPollDelays, provider.BackoffStderr
	var said strings.Builder
	provider.Wait = func(context.Context, time.Duration) error { return nil }
	provider.BackoffStderr = &said
	createPollDelays = []time.Duration{time.Second}
	t.Cleanup(func() { provider.Wait, createPollDelays, provider.BackoffStderr = orig, origDelays, origErr })

	f, p := writeWorld(t, true)
	f.listLag = 99
	_, err := p.CreatePlaylist(context.Background(), "Road Trip")
	if want := `Apple created the playlist "Road Trip" (p.new), but it still wasn't in your playlists after 1s (iCloud sync delay); link it later with capy pl link <name> apple:p.new`; err == nil || err.Error() != want {
		t.Errorf("逾時:\n got %v\nwant %s", err, want)
	}
	if want := "Waiting for Apple to add the new playlist p.new to your playlists (usually a few seconds)…\n"; said.String() != want {
		t.Errorf("等待提示:%q", said.String())
	}

	f, p = writeWorld(t, true)
	f.listFail = true
	_, err = p.CreatePlaylist(context.Background(), "Road Trip")
	if err == nil || !strings.HasPrefix(err.Error(), `Apple created the playlist "Road Trip" (p.new), but listing playlists failed (`) ||
		!strings.HasSuffix(err.Error(), "); link it later with capy pl link <name> apple:p.new") {
		t.Errorf("列表失敗:%v", err)
	}

	provider.Wait = func(context.Context, time.Duration) error { return context.Canceled }
	f, p = writeWorld(t, true)
	f.listLag = 99
	_, err = p.CreatePlaylist(context.Background(), "Road Trip")
	if want := `Apple created the playlist "Road Trip" (p.new), but waiting for it to appear in your playlists was interrupted (context canceled); link it later with capy pl link <name> apple:p.new`; err == nil || err.Error() != want || !errors.Is(err, context.Canceled) {
		t.Errorf("中斷:\n got %v\nwant %s", err, want)
	}
}

func TestEnglishClientErrors(t *testing.T) {
	withEnglish(t)
	ctx := context.Background()
	status := func(code int) *Client {
		return newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
	}
	for _, tc := range []struct {
		what string
		err  error
		want string
	}{
		{"401", func() error { _, err := status(401).Storefront(ctx); return err }(), "developer token is invalid (401): authorization expired"},
		{"403", func() error { _, err := status(403).Storefront(ctx); return err }(), "Music User Token is invalid or the subscription has lapsed (403): authorization expired"},
		{"preflight 403", func() error { _, err := status(403).Preflight(ctx); return err }(), "developer token or Origin was rejected (403): authorization expired"},
		{"429", func() error { _, err := status(429).Storefront(ctx); return err }(), "apple API 429 rate limited (the Apple web token's quota is shared by every web player; try again in about an hour)"},
		{"no storefront", func() error {
			_, err := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":[]}`)) }).Storefront(ctx)
			return err
		}(), "Apple returned no storefront"},
		{"create no id", func() error {
			_, err := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":[]}`)) }).CreatePlaylist(ctx, "Road Trip")
			return err
		}(), "Apple's response to creating the playlist had no id"},
		{"bad ISRC", func() error { _, err := status(200).SongsByISRC(ctx, "tw", "nope"); return err }(), `invalid ISRC (must be 12 letters or digits): "nope"`},
		{"track 404", func() error { _, err := status(404).GetSong(ctx, "tw", "42"); return err }(), "not found: track 42"},
		{"tracks 404", func() error { _, err := status(404).LibraryPlaylistTracks(ctx, "p.1"); return err }(), "the playlist is empty or doesn't exist: not found"},
		{"playlist 404", func() error { _, err := status(404).Playlist(ctx, "p.1"); return err }(), "not found: playlist p.1"},
		{"write 404", writeErr("p.1", &apiError{Status: 404, Title: "Resource Not Found"}), "not found: Apple can't find playlist p.1 (apple API 404 Resource Not Found )"},
	} {
		if tc.err == nil || tc.err.Error() != tc.want {
			t.Errorf("%s:\n got %v\nwant %s", tc.what, tc.err, tc.want)
		}
	}
	// write 404 的細節只是字串(原本是 %v):不包住 apiError,只包住 ErrNotFound。
	err := writeErr("p.1", &apiError{Status: 404})
	var ae *apiError
	if !errors.Is(err, provider.ErrNotFound) || errors.As(err, &ae) {
		t.Errorf("write 404 只包 ErrNotFound:%v", err)
	}
}
