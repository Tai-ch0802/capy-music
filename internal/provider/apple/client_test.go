package apple

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const songFx = `{"id":"%s","type":"songs","attributes":{"name":"派對動物","artistName":"五月天","albumName":"自傳",
"durationInMillis":227000,"isrc":"TWA472400123","contentRating":"clean","url":"https://music.apple.com/tw/album/x/1?i=%s"}}`

func songJSONFx(id string) string { return fmt.Sprintf(songFx, id, id) }

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewClient(srv.Client(), srv.URL, "DEV", "MUT")
}

func TestDoSendsBothTokens(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer DEV" || r.Header.Get("Music-User-Token") != "MUT" {
			t.Errorf("headers = %v", r.Header)
		}
		w.Write([]byte(`{"data":[{"id":"tw"}]}`))
	})
	sf, err := c.Storefront(context.Background())
	if err != nil || sf != "tw" {
		t.Fatalf("(%q, %v)", sf, err)
	}
}

func TestDoMapsAuthErrors(t *testing.T) {
	for _, code := range []int{401, 403} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(`{"errors":[{"status":"` + fmt.Sprint(code) + `","title":"Unauthorized"}]}`))
		})
		if _, err := c.Storefront(context.Background()); !errors.Is(err, provider.ErrAuthExpired) {
			t.Errorf("%d 應映射 ErrAuthExpired,得到 %v", code, err)
		}
	}
}

func TestDoParsesAppleError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"errors":[{"status":"404","title":"Resource Not Found","detail":"no such song"}]}`))
	})
	_, _, err := c.Song(context.Background(), "tw", "nope")
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 404 || ae.Detail != "no such song" {
		t.Fatalf("apiError 解析錯誤:%v", err)
	}
}

func TestDoRetriesOn429(t *testing.T) {
	orig := provider.Wait
	provider.Wait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { provider.Wait = orig })
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.Write([]byte(`{"data":[{"id":"tw"}]}`))
	})
	if _, err := c.Storefront(context.Background()); err != nil || calls != 2 {
		t.Fatalf("應重試一次:calls=%d err=%v", calls, err)
	}
}

func TestSearchSongsPaginatesWithin25(t *testing.T) {
	var limits, offsets []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if !strings.HasPrefix(r.URL.Path, "/catalog/tw/search") || q.Get("types") != "songs" || q.Get("term") != "五月天" {
			t.Errorf("search 請求錯誤:%s?%s", r.URL.Path, r.URL.RawQuery)
		}
		limits = append(limits, q.Get("limit"))
		offsets = append(offsets, q.Get("offset"))
		n := 25
		if q.Get("offset") == "25" {
			n = 5
		}
		items := make([]string, n)
		for i := range items {
			items[i] = songJSONFx(fmt.Sprintf("s%s-%02d", q.Get("offset"), i))
		}
		fmt.Fprintf(w, `{"results":{"songs":{"data":[%s]}}}`, strings.Join(items, ","))
	})
	tracks, err := c.SearchSongs(context.Background(), "tw", "五月天", 30)
	if err != nil || len(tracks) != 30 {
		t.Fatalf("(%d, %v)", len(tracks), err)
	}
	if limits[0] != "25" || limits[1] != "5" || offsets[1] != "25" {
		t.Errorf("分頁參數:limits=%v offsets=%v", limits, offsets)
	}
	tr := tracks[0]
	if tr.Title != "派對動物" || tr.Artists[0] != "五月天" || tr.Album != "自傳" || tr.ISRC != "TWA472400123" || tr.DurationMS != 227000 || tr.Explicit {
		t.Errorf("映射錯誤:%+v", tr)
	}
}

func TestSearchSongsEmptyResults(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":{}}`)) // Apple 無結果時 songs 鍵缺席
	})
	tracks, err := c.SearchSongs(context.Background(), "tw", "zzz", 10)
	if err != nil || len(tracks) != 0 {
		t.Fatalf("(%d, %v)", len(tracks), err)
	}
}

func TestSongReturnsURL(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog/tw/songs/s1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprintf(w, `{"data":[%s]}`, songJSONFx("s1"))
	})
	tr, u, err := c.Song(context.Background(), "tw", "s1")
	if err != nil || tr.ProviderID != "s1" || !strings.HasPrefix(u, "https://music.apple.com/") {
		t.Fatalf("(%+v, %q, %v)", tr, u, err)
	}
}

func TestLibraryPlaylistsAndTracks(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/library/playlists":
			w.Write([]byte(`{"data":[{"id":"p.1","type":"library-playlists","attributes":{"name":"通勤","canEdit":true}}]}`))
		case "/me/library/playlists/p.1/tracks":
			if r.URL.Query().Get("include") != "catalog" {
				t.Errorf("應帶 include=catalog")
			}
			w.Write([]byte(`{"data":[
{"id":"i.1","type":"library-songs","attributes":{"name":"派對動物","artistName":"五月天","albumName":"自傳","durationInMillis":227000},
 "relationships":{"catalog":{"data":[{"id":"c1","type":"songs","attributes":{"isrc":"TWA472400123"}}]}}},
{"id":"i.2","type":"library-songs","attributes":{"name":"本機檔","artistName":"我","albumName":"","durationInMillis":1000}}]}`))
		default:
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
	})
	pls, err := c.LibraryPlaylists(context.Background())
	if err != nil || len(pls) != 1 || pls[0].ID != "p.1" || pls[0].Name != "通勤" || pls[0].Total != -1 {
		t.Fatalf("(%+v, %v)", pls, err)
	}
	ts, err := c.LibraryPlaylistTracks(context.Background(), "p.1")
	if err != nil || len(ts) != 2 {
		t.Fatalf("(%d, %v)", len(ts), err)
	}
	if ts[0].ProviderID != "c1" || ts[0].ISRC != "TWA472400123" {
		t.Errorf("有 catalog 對應時應用 catalog id/ISRC:%+v", ts[0])
	}
	if ts[1].ProviderID != "i.2" || ts[1].ISRC != "" {
		t.Errorf("無 catalog 對應時用 library id:%+v", ts[1])
	}
}
