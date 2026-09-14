package cli

import (
	"context"
	"errors"
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

// 跨平台複製清單(README 與使用指南的步驟):Apple 清單 → canonical → 用 --create 建的 Spotify 清單。
// 建出來的清單跟 canonical 同名,所以 push 不會多排一個 rename;第二次 push 無變更。
func TestCopyApplePlaylistToSpotify(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	for _, id := range []string{"t1", "t2"} {
		fs.addCatalog(fakeCatalogTrack{ID: id, Name: "song-" + id, ISRC: "TW00000000" + strings.ToUpper(id)})
	}
	// library 曲目帶 catalog 對應 = 有 ISRC:Spotify 那邊靠它精確反查
	libTrack := func(lib, cat, isrc string) string {
		return fmt.Sprintf(`{"id":%q,"attributes":{"name":"song","artistName":"artist","albumName":"A","durationInMillis":200000},
"relationships":{"catalog":{"data":[{"id":%q,"type":"songs","attributes":{"name":"song","artistName":"artist","albumName":"A","durationInMillis":200000,"isrc":%q}}]}}}`, lib, cat, isrc)
	}
	apple := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/library/playlists":
			w.Write([]byte(`{"data":[{"id":"p.1","attributes":{"name":"公路旅行"}}]}`))
		case "/me/library/playlists/p.1/tracks":
			fmt.Fprintf(w, `{"data":[%s,%s]}`, libTrack("i.1", "111", "TW00000000T1"), libTrack("i.2", "222", "TW00000000T2"))
		default:
			t.Errorf("非預期的 Apple 請求:%s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(apple.Close)
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id == "apple" {
			return appleprov.New(apple.Client(), apple.URL, "DEV", "MUT", "tw"), nil
		}
		return orig(ctx, id)
	}
	t.Cleanup(func() { newProvider = orig })

	before := driveFiles(t, dc)
	if _, _, err := runPull(t, "pl", "link", "公路旅行", "apple", "--create"); !errors.Is(err, provider.ErrNotSupported) || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("Apple 還不能建清單,要在碰 Drive 之前擋:%v", err)
	}

	mustPull(t, "pl", "link", "公路旅行", "apple:公路旅行")
	mustPull(t, "pl", "link", "公路旅行", "spotify", "--create")
	mustPull(t, "pl", "pull", "公路旅行", "--yes") // 不帶 --provider:Apple 的曲目拉進來,空的 Spotify 清單記下 base
	mustPull(t, "resolve", "公路旅行", "--provider", "spotify", "--yes")
	mustPull(t, "pl", "push", "公路旅行", "--provider", "spotify", "--yes")
	if got := fs.tracksOf("new1"); strings.Join(got, ",") != "t1,t2" {
		t.Fatalf("Spotify 清單要依 Apple 的順序有 t1、t2:%v", got)
	}
	for _, w := range fs.written() {
		if w.Method == http.MethodPut && w.Path == "/playlists/new1" {
			t.Fatalf("清單建的時候就跟 canonical 同名,不該再改名:%+v", fs.written())
		}
	}
	if out, errs := mustPull(t, "pl", "push", "公路旅行", "--provider", "spotify", "--yes"); out != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("第二次 push 要無變更:%q %q", out, errs)
	}
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
