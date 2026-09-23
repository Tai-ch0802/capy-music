package cli

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 英文模式:player.go / search.go / play_resolve.go / watch.go 的訊息(T2c player)。只驗這四個檔自己產生的字。

func TestEnglishPlayerHelp(t *testing.T) {
	setCLITestConfig(t)
	withLanguage(t, "en")
	root := newRootCmd()
	for path, want := range map[string]string{
		"play":    "Play a track, an artist's top tracks or one of your playlists (no arguments = resume; opens a picker in a terminal when ambiguous)",
		"seek":    "Jump to a position in the current track",
		"vol":     "Set the playback volume",
		"now":     "Show what's playing (--watch keeps it on screen, with a progress bar and key controls)",
		"devices": "List Spotify Connect devices",
		"search":  "Search for tracks",
	} {
		c, _, err := root.Find([]string{path})
		if err != nil || c.Short != want || hasCJK(c.Long) {
			t.Errorf("%s:Short %q Long %q %v", path, c.Short, c.Long, err)
		}
	}
	seek, _, _ := root.Find([]string{"seek"})
	if seek.Name() != "seek" || seek.Use != "seek <[h:]mm:ss|seconds>" {
		t.Errorf("seek Use:%q", seek.Use)
	}
	play, _, _ := root.Find([]string{"play"})
	if !strings.HasPrefix(play.Long, "Searches tracks, artists and the playlists in the local cache") {
		t.Errorf("play Long:%q", play.Long)
	}
	for _, f := range []struct{ cmd, flag, want string }{
		{"play", "device", "name of the target device (see capy devices)"},
		{"play", "id", "play a track directly by its platform track ID (skips the search)"},
		{"play", "type", "search only this type: track|artist|playlist (same as the prefixes artist: / pl: / track:)"},
		{"play", "pick", "open the picker right away (playlists and recent items from the local cache; needs a terminal)"},
		{"now", "watch", "keep it on screen (bubbletea view; space play/pause, n/p next/previous track, q/esc quit)"},
		{"search", "limit", "number of results (the API returns at most 10 per request; more are fetched page by page)"},
	} {
		c, _, _ := root.Find([]string{f.cmd})
		if got := c.Flags().Lookup(f.flag).Usage; got != f.want {
			t.Errorf("%s --%s:%q", f.cmd, f.flag, got)
		}
	}
}

func TestEnglishPlayLabels(t *testing.T) {
	withLanguage(t, "en")
	f := newPlayFake(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"play", "通勤"}, "▶ Playlist: 通勤 (23 tracks)\n"},
		{[]string{"play", "五月天"}, "▶ Top tracks by 五月天 (2 tracks)\n"},
		{[]string{"play"}, "▶ Resumed playback\n"},
	} {
		if out, err := runCLI(t, c.args...); err != nil || out != c.want {
			t.Errorf("%v:%q %v", c.args, out, err)
		}
	}
	f.top = []provider.Track{{ProviderID: "h1", Title: "Stubborn"}}
	if out, err := runCLI(t, "play", "artist:五月天"); err != nil || out != "▶ Top track by 五月天 (1 track)\n" {
		t.Errorf("一首熱門歌曲:%q %v", out, err)
	}
	f.top = append(f.top, provider.Track{ProviderID: "h2"})
	f.caps &^= provider.CapPlayQueue
	if out, err := runCLI(t, "play", "artist:五月天"); err != nil || out != "▶ 五月天: Stubborn (Fake can't queue tracks, so only the first top track plays; queueing comes in P4)\n" {
		t.Errorf("不能排佇列:%q %v", out, err)
	}
	f.top = nil
	if _, err := runCLI(t, "play", "artist:五月天"); err == nil || err.Error() != "五月天 has no top tracks" {
		t.Errorf("沒有熱門歌曲:%v", err)
	}
}

func TestEnglishPlayAmbiguousTSVAndPicker(t *testing.T) {
	withLanguage(t, "en")
	f := newPlayFake(t)
	out, err := runCLI(t, "play", "派對")
	var amb *AmbiguousError
	want := "3 candidates; set the type with --type track|artist|playlist or a prefix artist: / pl: / track:, or run this in a terminal to open the picker"
	if !errors.As(err, &amb) || err.Error() != want {
		t.Fatalf("歧義:%v", err)
	}
	if code, msg := ExitCode(err); code != 2 || msg != want {
		t.Errorf("exit code:%d %q", code, msg)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || lines[0] != "artist\ta1\t五月天\ttop tracks" || lines[1] != "track\tt1\t派對動物\t五月天 · 自傳" {
		t.Fatalf("TSV:%q", out)
	}
	f.caps &^= provider.CapPlayPlaylist
	if out, _ := runCLI(t, "play", "通"); !strings.Contains(out, "playlist\tp1\t通勤\t23 tracks (Fake can't play playlists yet; use capy pl show)\n") {
		t.Errorf("清單不可播的附註:%q", out)
	}
	f.caps |= provider.CapPlayPlaylist

	orig := isInteractive
	isInteractive = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { isInteractive = orig })
	origPick := pickOne
	var title string
	var labels []string
	pickOne = func(tt string, ls []string) (int, error) { title, labels = tt, ls; return 0, nil }
	t.Cleanup(func() { pickOne = origPick })
	if out, err := runCLI(t, "play", "通"); err != nil || out != "▶ Playlist: 通勤 (23 tracks)\n" {
		t.Fatalf("挑選器選第一個:%q %v", out, err)
	}
	if title != "Pick something to play" || !slices.Equal(labels, []string{"[playlist] 通勤 — 23 tracks", "[artist] 五月天 — top tracks"}) {
		t.Errorf("挑選器:%q %q", title, labels)
	}
	if got := pickerLabel(trackCandidate(provider.Track{Title: "Stubborn", Artists: []string{"Mayday"}, Album: "Poetry"})); got != "[track] Stubborn — Mayday · Poetry" {
		t.Errorf("曲目標籤:%q", got)
	}
	if got := plCandidate(cache.Playlist{ID: "p", Name: "Solo", Total: 1}).Detail; got != "1 track" {
		t.Errorf("一首的清單:%q", got)
	}
}

// devicesFake:有兩個裝置的 playFake,給 --device 找不到的錯誤訊息用。
type devicesFake struct{ *playFake }

func (f devicesFake) Devices(context.Context) ([]provider.Device, error) {
	return []provider.Device{{ID: "d1", Name: "Phone"}, {ID: "d2", Name: "Laptop"}}, nil
}

func TestEnglishPlayErrors(t *testing.T) {
	withLanguage(t, "en")
	f := newPlayFake(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"play", "--id", "t1", "派對"}, "use either --id or a search query, not both"},
		{[]string{"play", "--type", "track"}, "--type only applies to a search query (no arguments resumes playback; a URI / track ID needs no type)"},
		{[]string{"play", "--pick"}, "--pick needs a terminal; elsewhere, give a search query and set the type with --type or a prefix"},
		{[]string{"play", "spotify:album:xyz"}, "only track URIs / IDs are supported for now, not spotify:album:xyz — get the track IDs with capy pl show and play those"},
		{[]string{"play", "artist:五月天", "--type", "track"}, "the prefix artist: conflicts with --type track"},
		{[]string{"play", "--type", "album", "x"}, `--type must be track, artist or playlist, got "album"`},
	} {
		if _, err := runCLI(t, c.args...); err == nil || err.Error() != c.want {
			t.Errorf("%v:%v", c.args, err)
		}
	}
	f.tracks, f.artists = nil, nil
	if _, err := runCLI(t, "play", "zzz"); err == nil || err.Error() != "nothing found for zzz" {
		t.Errorf("零候選:%v", err)
	}
	if _, _, err := resolvePlay(context.Background(), playSources{}, "", ""); err == nil || err.Error() != "missing search query" {
		t.Errorf("空搜尋詞:%v", err)
	}

	swapProviderWith(t, devicesFake{f})
	if _, err := runCLI(t, "play", "通勤", "--device", "Kitchen"); err == nil || err.Error() != `no device named "Kitchen"; available devices: Phone, Laptop` {
		t.Errorf("找不到裝置:%v", err)
	}

	orig := isInteractive
	isInteractive = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { isInteractive = orig })
	if _, err := runCLI(t, "play", "--pick", "zzz"); err == nil || err.Error() != `no playlist or recent item in the local cache matches "zzz"; --pick without a search query lists everything` {
		t.Errorf("--pick 沒有符合的:%v", err)
	}
	c := cache.Load()
	c.AddRecent(cache.Recent{Provider: "spotify", Type: cache.TypeArtist, ID: "a1", Label: "五月天", Detail: "熱門歌曲"}) // 在 zh-TW 下存的
	_ = c.Save()
	origPicker := runPlayPicker
	var shown string
	runPlayPicker = func(cs []candidate) (*candidate, error) { shown = cs[len(cs)-1].Detail; return &cs[len(cs)-1], nil } // 最近的藝人
	t.Cleanup(func() { runPlayPicker = origPicker })
	defer func() {
		if shown != "top tracks" {
			t.Errorf("最近播過的藝人要照目前的語系重翻,不是 cache.json 存的那句:%q", shown)
		}
	}()
	t.Cleanup(func() { runPlayPicker = origPicker })
	f.caps &^= provider.CapArtistSearch
	_, err := runCLI(t, "play", "--pick")
	if err == nil || err.Error() != "Fake doesn't support artist top tracks: this platform doesn't support this operation" || !errors.Is(err, provider.ErrNotSupported) {
		t.Errorf("不支援藝人熱門歌曲:%v", err)
	}

	setCLITestConfig(t) // 空快取
	if _, err := runCLI(t, "play", "--pick"); err == nil || err.Error() != "the local cache is empty: run capy pl list first, or play a few things with capy play <query>" {
		t.Errorf("--pick 空快取:%v", err)
	}
}

func TestEnglishSeekVolNow(t *testing.T) {
	withLanguage(t, "en")
	newPlayFake(t)
	for _, c := range []struct {
		args []string
		out  string
		err  string
	}{
		{[]string{"seek", "1:23"}, "⏩ Jumped to 1:23\n", ""},
		{[]string{"vol", "30"}, "🔊 Volume 30\n", ""},
		{[]string{"now"}, "Nothing is playing\n", ""},
		{[]string{"seek", "coffee"}, "", `the position must be [h:]mm:ss or plain seconds (e.g. 1:23, 1:05:30 or 83), not "coffee"`},
		{[]string{"vol", "300"}, "", `the volume must be a whole number 0-100, not "300"`},
		{[]string{"now", "--watch"}, "", "--watch needs a terminal; elsewhere use capy now (one-shot plain text)"},
	} {
		out, err := runCLI(t, c.args...)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("%v:%v", c.args, err)
			}
			continue
		}
		if err != nil || out != c.out {
			t.Errorf("%v:%q %v", c.args, out, err)
		}
	}
}

func TestEnglishWatchView(t *testing.T) {
	withLanguage(t, "en")
	m := newWatchModel(context.Background(), &watchFake{}, time.Millisecond)
	m.st = &provider.PlaybackState{
		Playing:    true,
		Track:      &provider.Track{Title: "Stubborn", Artists: []string{"Mayday"}, Album: "Poetry", DurationMS: 249000},
		ProgressMS: 83000,
		Device:     provider.Device{Name: "MacBook Pro", Type: "Computer", VolumePct: 50, VolumeKnown: true},
	}
	v := ansi.Strip(m.View().Content)
	for _, want := range []string{"MacBook Pro (Computer) · volume 50\n", "  space play/pause · n next · p previous · q/esc quit\n"} {
		if !strings.Contains(v, want) {
			t.Errorf("畫面缺 %q:\n%s", want, v)
		}
	}
	if hasCJK(v) {
		t.Errorf("英文畫面有中文:\n%s", v)
	}
	m.st, m.err, m.fails = nil, errors.New("timeout"), 2
	if v := ansi.Strip(m.View().Content); !strings.HasPrefix(v, "Nothing is playing\n⚠ timeout (attempt 2)\n") {
		t.Errorf("無內容與錯誤列:\n%s", v)
	}

	next, _ := m.Update(watchStateMsg{err: &provider.RateLimitError{Seconds: 90, Message: "retry in 90 s"}})
	if v := ansi.Strip(next.(watchModel).View().Content); !strings.Contains(v, "⚠ rate limited, waiting… (retry in 90 s)\n") {
		t.Errorf("限流:\n%s", v)
	}

	boom := errors.New("boom")
	m = newWatchModel(context.Background(), &watchFake{}, time.Millisecond)
	for range watchMaxFails {
		next, _ := m.Update(watchStateMsg{err: boom})
		m = next.(watchModel)
	}
	if m.fatal == nil || m.fatal.Error() != "couldn't read the playback state 5 times in a row: boom" || !errors.Is(m.fatal, boom) {
		t.Errorf("連續失敗:%v", m.fatal)
	}
}

func TestEnglishSpotifyProviderErrors(t *testing.T) {
	setCLITestConfig(t)
	keyring.MockInit()
	withLanguage(t, "en")
	if _, err := runCLI(t, "search", "x", "--provider", "spotify"); err == nil || err.Error() != "Spotify is not set up — run capy auth login spotify first" {
		t.Errorf("沒設 Spotify:%v", err)
	}
	if err := config.Save(&config.Config{SpotifyClientID: "cid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "search", "x", "--provider", "spotify"); err == nil || err.Error() != "not logged in to Spotify — run capy auth login spotify first" {
		t.Errorf("沒登入 Spotify:%v", err)
	}
}
