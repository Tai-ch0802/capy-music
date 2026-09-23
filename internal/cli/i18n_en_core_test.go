package cli

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 英文模式:provider.go / pick.go / root.go 的訊息(T2a core)。只驗這三個檔自己產生的字。

func TestEnglishRootHelpAndFlags(t *testing.T) {
	setCLITestConfig(t)
	withLanguage(t, "en")
	root := newRootCmd()
	if root.Short != "Cross-platform music CLI: search, playback control, playlist sync" {
		t.Errorf("root Short:%q", root.Short)
	}
	for flag, want := range map[string]string{
		"provider": "platform (spotify|apple|local; defaults to default_provider in config)",
		"web":      "use capy from your browser: binds to 127.0.0.1 only and prints a one-time URL at startup",
		"port":     "port for --web (default 0 = any free port; 8888, 80 and 443 are not allowed)",
	} {
		if got := root.Flags().Lookup(flag).Usage; got != want {
			t.Errorf("--%s:%q", flag, got)
		}
	}
	for use, want := range map[string]string{"pause": "Pause playback", "next": "Skip to the next track", "prev": "Go back to the previous track"} {
		c, _, err := root.Find([]string{use})
		if err != nil || c.Short != want {
			t.Errorf("%s Short:%q %v", use, c.Short, err)
		}
	}
	if _, err := runCLI(t, "--port", "1"); err == nil || err.Error() != "--port only works with --web" {
		t.Errorf("--port 沒配 --web:%v", err)
	}
	if got := (&SignalError{Sig: os.Interrupt}).Error(); got != "received signal interrupt" {
		t.Errorf("SignalError:%q", got)
	}
}

func TestEnglishPlaybackDoneAndErrors(t *testing.T) {
	withLanguage(t, "en")
	newPlayFake(t)
	for cmd, want := range map[string]string{"pause": "⏸ Paused\n", "next": "⏭ Next track\n", "prev": "⏮ Previous track\n"} {
		if out, err := runCLI(t, cmd); err != nil || out != want {
			t.Errorf("%s:%q %v", cmd, out, err)
		}
	}
	swapProviderWith(t, fakeProvider{caps: provider.CapSearch})
	_, err := runCLI(t, "pause")
	want := "Fake doesn't support playback control (Apple Music playback only works on macOS; elsewhere use --provider spotify): this platform doesn't support this operation"
	if err == nil || err.Error() != want || !errors.Is(err, provider.ErrNotSupported) {
		t.Errorf("缺播放能力:%v", err)
	}
}

func TestEnglishProviderErrors(t *testing.T) {
	setCLITestConfig(t)
	withLanguage(t, "en")
	if _, err := newProvider(context.Background(), "tidal"); err == nil || err.Error() != `unknown provider "tidal" (available: spotify, apple, local)` {
		t.Errorf("未知 provider:%v", err)
	}
	for _, tc := range []struct {
		f    func(provider.Provider) error
		want string
	}{
		{func(p provider.Provider) error { _, err := asSearcher(p); return err }, "Fake doesn't support search: "},
		{func(p provider.Provider) error { _, err := asISRCLookup(p); return err }, "Fake doesn't support ISRC lookup: "},
		{func(p provider.Provider) error { _, err := asPlaylistReader(p); return err }, "Fake doesn't support reading playlists: "},
		{func(p provider.Provider) error { _, err := asPlaylistWriter(p); return err }, "Fake doesn't support writing playlists: "},
		{func(p provider.Provider) error { _, err := asPlaylistCreator(p); return err }, "Fake doesn't support creating playlists: "},
	} {
		if err := tc.f(fakeProvider{}); err == nil || err.Error() != tc.want+"this platform doesn't support this operation" || !errors.Is(err, provider.ErrNotSupported) {
			t.Errorf("%q:%v", tc.want, err)
		}
	}

	auth := friendlyErr("spotify", provider.ErrAuthExpired)
	if auth.Error() != "authorization expired or was rejected (authorization expired) — run capy auth login spotify again" {
		t.Errorf("授權過期:%v", auth)
	}
	// 原本是 %v:不包 ErrAuthExpired,第二次 friendlyErr 才不會把提示再套一層
	if errors.Is(auth, provider.ErrAuthExpired) || friendlyErr("spotify", auth).Error() != auth.Error() {
		t.Errorf("friendlyErr 不可包住 ErrAuthExpired:%v", friendlyErr("spotify", auth))
	}
	if err := friendlyErr("spotify", provider.ErrNoActiveDevice); err.Error() != "no active playback device — open a player, or list devices with capy devices --provider spotify and pick one with --device" {
		t.Errorf("沒有裝置:%v", err)
	}
	if err := friendlyErr("local", os.ErrPermission); err.Error() != "no permission to read (permission denied) — check the permissions of the local_root folder and its files" || errors.Is(err, os.ErrPermission) {
		t.Errorf("沒有權限:%v", err)
	}

	if _, err := newLocalProvider(); err == nil || err.Error() != "no local library folder is set — run capy config set local_root <folder> first (it holds your *.m3u8 playlists and library.json)" {
		t.Errorf("沒設 local_root:%v", err)
	}
	if err := config.Save(&config.Config{LocalRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := newLocalProvider(); err == nil || err.Error() != "local playlist ids include this computer's device id, which is created by capy auth login google — log in to Google first" {
		t.Errorf("沒有 device id:%v", err)
	}

	t.Setenv("CAPY_APPLE_API_BASE", "http://example.test/v1")
	if _, err := appleAPIBase(); err == nil || err.Error() != `CAPY_APPLE_API_BASE must use https (got "http://example.test/v1") — the developer token and Media-User-Token travel in the request headers, so a plain-text connection gives them away; use https://<host>/v1` {
		t.Errorf("http base:%v", err)
	}
	t.Setenv("CAPY_APPLE_API_BASE", "https://a b/%zz")
	if _, err := appleAPIBase(); err == nil || !strings.HasPrefix(err.Error(), "CAPY_APPLE_API_BASE is not a valid URL (") || !strings.HasSuffix(err.Error(), ") — use the form https://<host>/v1") {
		t.Errorf("壞 URL:%v", err)
	}
}

func TestEnglishAppleProviderErrors(t *testing.T) {
	withLanguage(t, "en")
	clearAppleTokens(t)
	if _, err := newAppleProvider(context.Background()); err == nil || err.Error() != "not logged in to Apple Music — run capy auth login apple first" {
		t.Errorf("沒登入:%v", err)
	}
	setupAppleTokens(t) // 有 token、config 沒有 storefront
	if _, err := newAppleProvider(context.Background()); err == nil || err.Error() != "the Apple storefront is missing — run capy auth login apple again" {
		t.Errorf("缺 storefront:%v", err)
	}
	past := time.Now().Add(-time.Hour)
	if err := apple.SaveDeveloperToken(fakeJWT(t, past), past); err != nil {
		t.Fatal(err)
	}
	_, err := newAppleProvider(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "the Apple developer token expired at ") || !strings.HasSuffix(err.Error(), " (Apple rotates it regularly) — run capy auth login apple again") {
		t.Errorf("dev token 過期:%v", err)
	}
}

func TestEnglishPickers(t *testing.T) {
	withLanguage(t, "en")
	origTTY := isInteractive
	isInteractive = func(*cobra.Command) bool { return false }
	if err := needTarget(&cobra.Command{}, nil, false, "pull"); err == nil || err.Error() != "specify a playlist (name or pid), or use --all to pull all linked playlists" {
		t.Errorf("needTarget:%v", err)
	}
	isInteractive = origTTY

	if _, err := pickPlatformPlaylist("apple", nil, ""); err == nil || err.Error() != "there are no playlists on apple" {
		t.Errorf("平台沒清單:%v", err)
	}
	log := stubPickers(t, 0, 4)
	refs := []provider.PlaylistRef{{ID: "1", Name: "One", Total: 1}, {ID: "2", Name: "Two", Total: 2}, {ID: "3", Name: "Unknown", Total: -1}}
	if _, err := pickPlatformPlaylist("spotify", refs, ""); err != nil {
		t.Fatal(err)
	}
	if log.titles[0] != "Pick a playlist on spotify" || !slices.Equal(log.nth(0), []string{"One — 1 track", "Two — 2 tracks", "Unknown"}) {
		t.Errorf("平台清單挑選器:%q %q", log.titles[0], log.nth(0))
	}

	s := &canonState{playlists: map[string]*canon.Playlist{
		"pa": {PID: "pa", Name: "A", Links: map[string]string{"spotify": "p1"}},
		"pb": {PID: "pb", Name: "B", Links: map[string]string{"spotify": "p2"}},
		"pc": {PID: "pc", Name: "C", Links: map[string]string{"apple": "x", "local": "y"}},
		"pd": {PID: "pd", Name: "D"},
	}}
	stubNewName(t, "a")
	_, err := pickLinkTarget(s, "spotify", "p1")
	if err == nil || err.Error() != "a playlist with that name already exists: A (pa) — go back and pick it, or choose another name" {
		t.Errorf("名字撞到:%v", err)
	}
	want := []string{"A — already linked to this playlist", "B — linked to spotify:p2; unlink it first", "C — apple:x, local:y", "D", "+ Create a new playlist"}
	if log.titles[1] != "Which playlist should this link to?" || !slices.Equal(log.nth(1), want) {
		t.Errorf("連結目標挑選器:%q %q", log.titles[1], log.nth(1))
	}

	empty := &canonState{playlists: map[string]*canon.Playlist{}}
	if _, err := pickLinkedPlaylist(empty, "spotify", "t"); err == nil || err.Error() != "no playlists are linked to spotify — run capy pl link <name> spotify:<playlist> first" {
		t.Errorf("沒連 spotify:%v", err)
	}
	if _, err := pickLinkedPlaylist(empty, "", "t"); err == nil || err.Error() != "no linked playlists — run capy pl link <name> <provider>:<playlist> first" {
		t.Errorf("沒有已連結:%v", err)
	}
}

// help 行的說明(計畫 Q56):en 就是 huh 的預設;zh-TW 每一個都是中文。鍵名與啟用狀態兩種語系都照 huh 預設,
// 只有 newForm 自己改的三個例外(Esc 取消)。
func TestFormKeyHelpLocalized(t *testing.T) {
	type binding struct {
		name string
		b    key.Binding
	}
	collect := func(km *huh.KeyMap) []binding {
		var out []binding
		eachBinding(km, func(name string, b *key.Binding) { out = append(out, binding{name, *b}) })
		return out
	}
	def := collect(huh.NewDefaultKeyMap())
	if len(def) < 60 {
		t.Fatalf("反射只走到 %d 個鍵位", len(def))
	}
	custom := map[string]bool{"Quit": true, "Select.SetFilter": true, "Select.ClearFilter": true}
	for _, lang := range []string{"en", "zh-TW"} {
		withLanguage(t, lang)
		got := collect(newKeyMap())
		if len(got) != len(def) {
			t.Fatalf("%s:鍵位數 %d,huh 預設 %d", lang, len(got), len(def))
		}
		for i, g := range got {
			d := def[i]
			if custom[g.name] {
				continue
			}
			if !slices.Equal(g.b.Keys(), d.b.Keys()) || g.b.Enabled() != d.b.Enabled() || g.b.Help().Key != d.b.Help().Key {
				t.Errorf("%s %s:鍵名 / 啟用狀態要照 huh 預設:%v %v", lang, g.name, g.b.Keys(), g.b.Enabled())
			}
			switch desc := g.b.Help().Desc; {
			case lang == "en" && desc != d.b.Help().Desc:
				t.Errorf("en %s:%q,huh 預設 %q", g.name, desc, d.b.Help().Desc)
			case lang == "zh-TW" && d.b.Help().Desc != "" && !hasCJK(desc):
				t.Errorf("zh-TW %s 沒翻:%q", g.name, desc)
			}
		}
	}
	withLanguage(t, "zh-TW")
	if km := newKeyMap(); km.Select.Submit.Help().Desc != "送出" || km.Select.Next.Help().Desc != "選擇" || km.Select.SetFilter.Help() != (key.Help{Key: "esc", Desc: "取消"}) || km.Select.SetFilter.Enabled() {
		t.Errorf("zh-TW 抽樣:%+v %+v %+v", km.Select.Submit.Help(), km.Select.Next.Help(), km.Select.SetFilter.Help())
	}
}
