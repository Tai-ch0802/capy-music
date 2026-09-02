package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	appleprov "github.com/Tai-ch0802/capy-music/internal/provider/apple"
)

const appleSongFx = `{"id":"%s","type":"songs","attributes":{"name":"派對動物","artistName":"五月天","albumName":"自傳",
"durationInMillis":227000,"isrc":"TWA1","contentRating":"clean","url":"https://music.apple.com/tw/album/x/1?i=%s"}}`

func swapApple(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id != "apple" {
			t.Errorf("provider = %q", id)
		}
		return appleprov.New(srv.Client(), srv.URL, "DEV", "MUT", "tw"), nil
	}
	t.Cleanup(func() { newProvider = orig })
}

func TestAppleSearchTSV(t *testing.T) {
	swapApple(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"results":{"songs":{"data":[%s]}}}`, fmt.Sprintf(appleSongFx, "111", "111"))
	})
	out, err := runCLI(t, "search", "派對動物", "--provider", "apple")
	if err != nil {
		t.Fatal(err)
	}
	if out != "111\t派對動物\t五月天\t自傳\t227000\n" {
		t.Errorf("TSV 欄序應與 spotify 一致:%q", out)
	}
}

func TestApplePlListShowsDashForUnknownTotal(t *testing.T) {
	swapApple(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/library/playlists":
			w.Write([]byte(`{"data":[{"id":"p.1","attributes":{"name":"通勤"}}]}`))
		default:
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
	})
	out, err := runCLI(t, "pl", "list", "--provider", "apple")
	if err != nil || out != "p.1\t通勤\t-\t\n" {
		t.Fatalf("pl list TSV:%q err=%v", out, err)
	}
}

func TestApplePlShowByLibraryID(t *testing.T) {
	// Apple library playlist ID(p.xxx)不符 spotifyBase62IDRe,resolvePlaylistID
	// 必須能在 ListPlaylists 結果裡直接比對 ID,而不是把它當名稱查。
	swapApple(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/library/playlists":
			w.Write([]byte(`{"data":[{"id":"p.1","attributes":{"name":"通勤"}}]}`))
		case "/me/library/playlists/p.1/tracks":
			w.Write([]byte(`{"data":[{"id":"i.1","attributes":{"name":"派對動物","artistName":"五月天","albumName":"自傳","durationInMillis":227000}}]}`))
		default:
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
	})
	out, err := runCLI(t, "pl", "show", "p.1", "--provider", "apple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "派對動物") {
		t.Errorf("pl show by library id:%q", out)
	}
}

func TestApplePlayByIDOffDarwinIsNotSupported(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin 由 TestApplePlayByIDOnDarwin 覆蓋")
	}
	swapApple(t, func(w http.ResponseWriter, r *http.Request) {})
	_, err := runCLI(t, "play", "--id", "111", "--provider", "apple")
	if err == nil || !strings.Contains(err.Error(), "macOS") {
		t.Fatalf("非 darwin 播放 apple 應給可行動訊息:%v", err)
	}
}
