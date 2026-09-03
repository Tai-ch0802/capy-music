package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/provider/spotify"
)

const cliTrackFx = `{"id":"%s","name":"派對動物","duration_ms":227000,"explicit":false,
"album":{"name":"自傳"},"artists":[{"name":"五月天"}],"external_ids":{"isrc":"TW1"}}`

// swapProvider 把 CLI 的 provider 指向 httptest 假 API(繞過 config/keychain)。
func swapProvider(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		return spotify.New(srv.Client(), srv.URL), nil
	}
	t.Cleanup(func() { newProvider = orig })
	return srv
}

func TestSearchOutputsTSVWhenNotTTY(t *testing.T) {
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "五月天 派對動物" {
			t.Errorf("query 應合併多個 arg:%q", r.URL.Query().Get("q"))
		}
		fmt.Fprintf(w, `{"tracks":{"items":[%s],"total":1}}`, fmt.Sprintf(cliTrackFx, "t1"))
	})
	out, err := runCLI(t, "search", "五月天", "派對動物")
	if err != nil {
		t.Fatal(err)
	}
	// runCLI 的 buffer 非 TTY → raw TSV,欄序:id, title, artists, album, duration_ms
	want := "t1\t派對動物\t五月天\t自傳\t227000\n"
	if out != want {
		t.Errorf("TSV 輸出:%q, want %q", out, want)
	}
}

func TestSearchLimitFlagPropagates(t *testing.T) {
	var gotLimit string
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		fmt.Fprint(w, `{"tracks":{"items":[],"total":0}}`)
	})
	if _, err := runCLI(t, "search", "x", "--limit", "3"); err != nil {
		t.Fatal(err)
	}
	if gotLimit != "3" {
		t.Errorf("limit 未傳遞:%q", gotLimit)
	}
}
