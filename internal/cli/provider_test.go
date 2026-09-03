package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/provider/spotify"
)

// fakeProvider:只實作 Provider,沒有任何能力介面。
type fakeProvider struct{ caps provider.Capability }

func (f fakeProvider) ID() string                   { return "fake" }
func (f fakeProvider) DisplayName() string          { return "Fake" }
func (f fakeProvider) Caps() provider.Capability    { return f.caps }
func (f fakeProvider) Health(context.Context) error { return nil }

func swapProviderWith(t *testing.T, p provider.Provider) {
	t.Helper()
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) { return p, nil }
	t.Cleanup(func() { newProvider = orig })
}

func TestUnknownProviderFlag(t *testing.T) {
	_, err := runCLI(t, "search", "x", "--provider", "tidal")
	if err == nil || !strings.Contains(err.Error(), "tidal") || !strings.Contains(err.Error(), "spotify") {
		t.Fatalf("未知 provider 應列出可用值:%v", err)
	}
}

func TestCapabilityGateGivesActionableError(t *testing.T) {
	swapProviderWith(t, fakeProvider{caps: provider.CapSearch})
	_, err := runCLI(t, "pause")
	if err == nil || !errors.Is(err, provider.ErrNotSupported) || !strings.Contains(err.Error(), "Fake") {
		t.Fatalf("缺 CapPlaybackControl 應回 ErrNotSupported 且點名平台:%v", err)
	}
	_, err = runCLI(t, "pl", "list")
	if err == nil || !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("缺 CapPlaylistRead 應回 ErrNotSupported:%v", err)
	}
}

func TestDefaultProviderIsSpotify(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"devices":[]}`))
	}))
	t.Cleanup(srv.Close)
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id != "spotify" {
			t.Errorf("預設 provider 應為 spotify,得到 %q", id)
		}
		return spotify.New(srv.Client(), srv.URL), nil
	}
	t.Cleanup(func() { newProvider = orig })
	if _, err := runCLI(t, "devices"); err != nil {
		t.Fatal(err)
	}
}

func TestFriendlyErrMentionsProvider(t *testing.T) {
	err := friendlyErr("apple", provider.ErrAuthExpired)
	if !strings.Contains(err.Error(), "capy auth login apple") {
		t.Errorf("訊息應指向對應 provider 的 login:%v", err)
	}
}

// TestFriendlyErrIncludesUnderlyingReason:401(dev token)與 403(MUT)訊息不同,
// 使用者看到的最終訊息應帶出底層原因,而不是被 ErrAuthExpired 的統一措辭蓋掉。
func TestFriendlyErrIncludesUnderlyingReason(t *testing.T) {
	underlying := fmt.Errorf("developer token 無效(401):%w", provider.ErrAuthExpired)
	err := friendlyErr("apple", underlying)
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "capy auth login apple") {
		t.Errorf("應同時帶出底層原因與 login 指引:%v", err)
	}
}

// TestFriendlyErrPassesThroughNotFound:ErrNotFound 的訊息已可行動(「清單為空或不存在」),
// friendlyErr 不應再包一層。
func TestFriendlyErrPassesThroughNotFound(t *testing.T) {
	underlying := fmt.Errorf("清單為空或不存在:%w", provider.ErrNotFound)
	if err := friendlyErr("apple", underlying); err != underlying {
		t.Errorf("ErrNotFound 應原樣回傳,得到 %v", err)
	}
}

// seedAppleAPIBase:把 Apple API base 指向 httptest。走套件變數而不是 CAPY_APPLE_API_BASE:
// httptest 是 http://,而環境變數那條路現在只收 https(見 appleAPIBase)。
func seedAppleAPIBase(t *testing.T, base string) {
	t.Helper()
	orig := appleAPIBaseSeed
	appleAPIBaseSeed = base
	t.Cleanup(func() { appleAPIBaseSeed = orig })
}

// ⭐ CAPY_APPLE_API_BASE 只收 https:這條路的請求標頭裡有 developer token 與 Media-User-Token
// (長期有效的帳號憑證),明文送出等於外洩。不做主機白名單——C-0 的前提正是正確主機還沒定案。
func TestAppleAPIBaseRequiresHTTPS(t *testing.T) {
	t.Setenv("CAPY_APPLE_API_BASE", "http://amp-api.music.apple.com/v1")
	base, err := appleAPIBase()
	if err == nil {
		t.Fatalf("http:// 必須被拒,得到 base=%q", base)
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("訊息要指出下一步(改成 https):%v", err)
	}
	t.Setenv("CAPY_APPLE_API_BASE", "https://api.music.apple.com/v1")
	if base, err := appleAPIBase(); err != nil || base != "https://api.music.apple.com/v1" {
		t.Errorf("https 應原樣通過:(%q, %v)", base, err)
	}
	t.Setenv("CAPY_APPLE_API_BASE", "")
	if base, err := appleAPIBase(); err != nil || base != "" {
		t.Errorf("未設定應回空字串(用預設 base):(%q, %v)", base, err)
	}
}
