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

// TestClientSendsWebPlayerHeaders:amp-api(網頁播放器私有 API)要求 Origin 標頭,
// MUT 標頭名是 Media-User-Token(不是官方 API 文件的舊標頭名——client.go 只設這一個 MUT 標頭,
// 下面斷言它等於 "MUT" 已隱含排除了舊名;不另外斷言舊標頭名字串,以維持 grep 可歸零)。
// 同時驗證 NewClient 在 base 空字串時預設到 DefaultAPIBase,且該常數確實指向 amp-api。
func TestClientSendsWebPlayerHeaders(t *testing.T) {
	var gotOrigin, gotAuth, gotMUT string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotOrigin = r.Header.Get("Origin")
		gotAuth = r.Header.Get("Authorization")
		gotMUT = r.Header.Get("Media-User-Token")
		w.Write([]byte(`{"data":[{"id":"tw"}]}`))
	})
	if _, err := c.Storefront(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotOrigin != "https://music.apple.com" {
		t.Errorf("Origin = %q", gotOrigin)
	}
	if gotAuth != "Bearer DEV" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotMUT != "MUT" {
		t.Errorf("Media-User-Token = %q", gotMUT)
	}

	if !strings.Contains(DefaultAPIBase, "amp-api") {
		t.Fatalf("DefaultAPIBase = %q,應指向 amp-api", DefaultAPIBase)
	}
	if def := NewClient(&http.Client{}, "", "DEV", "MUT"); def.base != DefaultAPIBase {
		t.Errorf("base 空字串應預設 DefaultAPIBase,得到 %q", def.base)
	}
}

func TestDoSendsBothTokens(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer DEV" || r.Header.Get("Media-User-Token") != "MUT" {
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
		_, err := c.Storefront(context.Background())
		if !errors.Is(err, provider.ErrAuthExpired) {
			t.Errorf("%d 應映射 ErrAuthExpired,得到 %v", code, err)
		}
		if !strings.Contains(err.Error(), fmt.Sprint(code)) {
			t.Errorf("%d 的錯誤訊息應含狀態碼以便分辨 dev token/MUT,得到 %v", code, err)
		}
	}
}

func TestPreflight(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"200 通過", 200, false},
		{"404 視為通過(端點形狀可能異動)", 404, false},
		{"401 判定 dev token 被拒", 401, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/storefronts/us" {
					t.Errorf("path = %s,應打 /storefronts/us", r.URL.Path)
				}
				if r.Header.Get("Media-User-Token") != "" {
					t.Error("preflight 不該帶 Media-User-Token")
				}
				w.WriteHeader(tc.status)
				if tc.status >= 400 {
					w.Write([]byte(`{"errors":[{"status":"` + fmt.Sprint(tc.status) + `","title":"x"}]}`))
				} else {
					w.Write([]byte(`{"data":[{"id":"us"}]}`))
				}
			}))
			t.Cleanup(srv.Close)
			c := NewClient(srv.Client(), srv.URL, "DEV", "") // preflight 不需 MUT
			err := c.Preflight(context.Background())
			if tc.wantErr && err == nil {
				t.Fatal("預期錯誤,得到 nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("預期通過,得到 %v", err)
			}
			if tc.wantErr && !errors.Is(err, provider.ErrAuthExpired) {
				t.Errorf("失敗應仍映射 ErrAuthExpired,得到 %v", err)
			}
		})
	}
}

// TestPreflight403MentionsDeveloperTokenNotMUT:Preflight 沒帶 MUT,403 只可能是 developer token /
// Origin 被拒——不該重用 do() 對一般請求 403 的「Music User Token」措辭,否則 applePersist 組出來的
// 錯誤訊息會自我矛盾(review item 3)。
func TestPreflight403MentionsDeveloperTokenNotMUT(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Media-User-Token") != "" {
			t.Error("preflight 不該帶 Media-User-Token")
		}
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"errors":[{"status":"403","title":"Forbidden"}]}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.Client(), srv.URL, "DEV", "") // preflight 不需 MUT
	err := c.Preflight(context.Background())
	if err == nil {
		t.Fatal("預期錯誤,得到 nil")
	}
	if !errors.Is(err, provider.ErrAuthExpired) {
		t.Errorf("應仍映射 ErrAuthExpired,得到 %v", err)
	}
	if !strings.Contains(err.Error(), "developer token") || !strings.Contains(err.Error(), "403") {
		t.Errorf("錯誤訊息應含 developer token 與 403,得到 %v", err)
	}
	if strings.Contains(err.Error(), "Music User Token") {
		t.Errorf("Preflight 沒帶 MUT,不該提及 Music User Token:%v", err)
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

// TestLibraryPlaylistsAndTracks:playlists 端點用兩頁 fixture,第一頁只回 1 筆但帶 next
// (cap 遠低於 libraryPage=100),證明分頁改看 next 欄位而非「回傳數 < cap」。
func TestLibraryPlaylistsAndTracks(t *testing.T) {
	var playlistCalls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/library/playlists":
			playlistCalls++
			switch r.URL.Query().Get("offset") {
			case "0":
				w.Write([]byte(`{"data":[{"id":"p.1","type":"library-playlists","attributes":{"name":"通勤","canEdit":true}}],"next":"/v1/me/library/playlists?offset=1"}`))
			case "1":
				w.Write([]byte(`{"data":[{"id":"p.2","type":"library-playlists","attributes":{"name":"深夜","canEdit":true}}]}`))
			default:
				t.Errorf("非預期 offset:%s", r.URL.Query().Get("offset"))
			}
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
	if err != nil || len(pls) != 2 || pls[0].ID != "p.1" || pls[0].Name != "通勤" || pls[0].Total != -1 {
		t.Fatalf("(%+v, %v)", pls, err)
	}
	if pls[1].ID != "p.2" || pls[1].Name != "深夜" {
		t.Errorf("cap(100) 遠高於單頁筆數,仍應取到第二頁(next 驅動):%+v", pls)
	}
	if playlistCalls != 2 {
		t.Errorf("應呼叫兩次(第一頁 next 非空):calls=%d", playlistCalls)
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

// TestLibraryPlaylistTracksEmpty404:Apple 對空清單或不存在的清單可能回 404 ——
// 映射成 provider.ErrNotFound(不是 ErrRestricted,那個語意是「他人清單」)。
func TestLibraryPlaylistTracksEmpty404(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errors":[{"status":"404","title":"Not Found"}]}`))
	})
	_, err := c.LibraryPlaylistTracks(context.Background(), "p.missing")
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("404 應映射 provider.ErrNotFound,得到 %v", err)
	}
	if !strings.Contains(err.Error(), "清單為空或不存在") {
		t.Errorf("錯誤訊息應可行動:%v", err)
	}
}

// TestLibraryPaginationStopsOnEmptyPageWithNext:回應 data 是空陣列但 next 非空
// (異常但理論上可能發生的回應)不該一直重打同一個 offset——offset 用 len(resp.Data)
// 累加,空 data 讓 offset 停滯不前,若只看 next 就會卡在同一頁。
// calls>3 的安全網只是避免這個測試在 RED 階段真的卡死整個 test 進程。
func TestLibraryPaginationStopsOnEmptyPageWithNext(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 3 {
			t.Fatal("分頁沒有停止,不斷重打同一個 offset(RED 安全網,避免真的卡死)")
		}
		if calls > 1 {
			t.Error("data 為空的頁應視為結束,不該再打第二次")
		}
		w.Write([]byte(`{"data":[],"next":"/v1/me/library/playlists?offset=0"}`))
	})
	pls, err := c.LibraryPlaylists(context.Background())
	if err != nil || len(pls) != 0 {
		t.Fatalf("(%+v, %v)", pls, err)
	}
	if calls != 1 {
		t.Errorf("應只呼叫一次,得到 %d 次", calls)
	}
}
