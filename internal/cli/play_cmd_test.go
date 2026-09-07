package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// playFake:Searcher + ArtistSearcher + PlaybackController,記錄 Play 請求。
type playFake struct {
	fakeProvider
	artists []provider.Artist
	tracks  []provider.Track
	top     []provider.Track
	played  []provider.PlayRequest
}

func (f *playFake) ID() string { return "spotify" }
func (f *playFake) Search(_ context.Context, q provider.Query) ([]provider.Track, error) {
	if len(f.tracks) > q.Limit {
		return f.tracks[:q.Limit], nil
	}
	return f.tracks, nil
}
func (f *playFake) SearchArtists(context.Context, provider.Query) ([]provider.Artist, error) {
	return f.artists, nil
}
func (f *playFake) ArtistTopTracks(context.Context, provider.Artist) ([]provider.Track, error) {
	return f.top, nil
}
func (f *playFake) Devices(context.Context) ([]provider.Device, error)     { return nil, nil }
func (f *playFake) State(context.Context) (*provider.PlaybackState, error) { return nil, nil }
func (f *playFake) Play(_ context.Context, r provider.PlayRequest) error {
	f.played = append(f.played, r)
	return nil
}
func (f *playFake) Pause(context.Context) error { return nil }
func (f *playFake) Next(context.Context) error  { return nil }
func (f *playFake) Prev(context.Context) error  { return nil }

func newPlayFake(t *testing.T) *playFake {
	t.Helper()
	setCLITestConfig(t)
	f := &playFake{
		fakeProvider: fakeProvider{caps: provider.CapSearch | provider.CapArtistSearch | provider.CapPlaybackControl | provider.CapPlayPlaylist},
		artists:      []provider.Artist{{ProviderID: "a1", Name: "五月天"}},
		tracks: []provider.Track{
			{ProviderID: "t1", Title: "派對動物", Artists: []string{"五月天"}, Album: "自傳"},
			{ProviderID: "t2", Title: "派對動物 Life Live", Artists: []string{"五月天"}, Album: "Live"},
		},
		top: []provider.Track{{ProviderID: "h1"}, {ProviderID: "h2"}},
	}
	swapProviderWith(t, f)
	c := cache.Load()
	c.SetPlaylists("spotify", []cache.Playlist{{ID: "p1", Name: "通勤", Total: 23}})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPlayNonTTYAmbiguousPrintsTSVAndExit2Error(t *testing.T) {
	f := newPlayFake(t)
	out, err := runCLI(t, "play", "派對")
	var amb *AmbiguousError
	if !errors.As(err, &amb) || len(amb.Candidates) != 3 {
		t.Fatalf("非 TTY 歧義應回 AmbiguousError(3 候選):%v", err)
	}
	if len(f.played) != 0 {
		t.Fatal("歧義不得播放")
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || lines[0] != "artist\ta1\t五月天\t熱門歌曲" || !strings.HasPrefix(lines[1], "track\tt1\t派對動物\t五月天 · 自傳") {
		t.Fatalf("候選應為 TSV(type, id, label, detail):%q", out)
	}
	if strings.Contains(out, "Error") {
		t.Fatalf("SilenceErrors:cobra 不得自己印錯:%q", out)
	}
}

func TestPlayUniqueHitsAndPrefixes(t *testing.T) {
	f := newPlayFake(t)
	cases := []struct {
		args []string
		want provider.PlayRequest
		out  string
	}{
		{[]string{"play", "通勤"}, provider.PlayRequest{PlaylistID: "p1"}, "清單:通勤(23 首)"},
		{[]string{"play", "五月天"}, provider.PlayRequest{TrackIDs: []string{"h1", "h2"}}, "五月天 的熱門歌曲(2 首)"},
		{[]string{"play", "artist:五月天"}, provider.PlayRequest{TrackIDs: []string{"h1", "h2"}}, "熱門歌曲"},
		{[]string{"play", "--type", "track", "派對"}, provider.PlayRequest{TrackIDs: []string{"t1"}}, "派對動物 — 五月天"},
		{[]string{"play", "track:派對"}, provider.PlayRequest{TrackIDs: []string{"t1"}}, "派對動物 — 五月天"},
		{[]string{"play", "pl:通"}, provider.PlayRequest{PlaylistID: "p1"}, "清單:通勤"},
		{[]string{"play"}, provider.PlayRequest{}, "恢復播放"},
	}
	for _, c := range cases {
		f.played = nil
		out, err := runCLI(t, c.args...)
		if err != nil || len(f.played) != 1 || !strings.Contains(out, c.out) {
			t.Errorf("%v:err=%v played=%+v out=%q", c.args, err, f.played, out)
			continue
		}
		got := f.played[0]
		if got.PlaylistID != c.want.PlaylistID || strings.Join(got.TrackIDs, ",") != strings.Join(c.want.TrackIDs, ",") {
			t.Errorf("%v:PlayRequest = %+v, want %+v", c.args, got, c.want)
		}
	}
	rec := cache.Load().Recent
	if len(rec) == 0 || rec[0].Type != cache.TypePlaylist || rec[0].ID != "p1" {
		t.Fatalf("成功播放應記到最近(最新在前):%+v", rec)
	}
}

func TestPlayPickNeedsTTYAndUsesPickerWhenInteractive(t *testing.T) {
	f := newPlayFake(t)
	if _, err := runCLI(t, "play", "--pick"); err == nil || !strings.Contains(err.Error(), "終端機") {
		t.Fatalf("非 TTY 的 --pick 應報錯:%v", err)
	}
	orig := isInteractive
	isInteractive = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { isInteractive = orig })
	origPicker := runPlayPicker
	var offered []candidate
	runPlayPicker = func(cs []candidate) (*candidate, error) { offered = cs; return &cs[len(cs)-1], nil }
	t.Cleanup(func() { runPlayPicker = origPicker })

	out, err := runCLI(t, "play", "派對") // 歧義 + 互動 → 挑選器
	if err != nil || len(offered) != 3 || len(f.played) != 1 || f.played[0].TrackIDs[0] != "t2" || !strings.Contains(out, "派對動物 Life Live") {
		t.Fatalf("互動歧義應開挑選器並播所選:err=%v offered=%d played=%+v out=%q", err, len(offered), f.played, out)
	}
	f.played = nil
	_, err = runCLI(t, "play", "--pick") // 快取:1 清單 + 剛才播的 1 筆最近
	if err != nil || len(offered) != 2 || offered[0].Type != cache.TypePlaylist || len(f.played) != 1 {
		t.Fatalf("--pick 應只列快取候選:err=%v offered=%+v played=%+v", err, offered, f.played)
	}
	runPlayPicker = func([]candidate) (*candidate, error) { return nil, errors.New("已取消") }
	if _, err := runCLI(t, "play", "派對"); err == nil || !strings.Contains(err.Error(), "已取消") {
		t.Fatalf("取消挑選器應回錯且不播:%v", err)
	}
}

func TestPlayNotFoundAndAppleStylePlaylistError(t *testing.T) {
	f := newPlayFake(t)
	f.tracks, f.artists = nil, nil
	if _, err := runCLI(t, "play", "zzz"); err == nil || !strings.Contains(err.Error(), "找不到") {
		t.Fatalf("零候選應回找不到:%v", err)
	}
}

func TestHistoryClear(t *testing.T) {
	newPlayFake(t)
	c := cache.Load()
	c.AddRecent(cache.Recent{Provider: "spotify", Type: cache.TypeQuery, ID: "q", Label: "q"})
	_ = c.Save()
	out, err := runCLI(t, "history", "clear")
	if err != nil || !strings.Contains(out, "1 筆") {
		t.Fatalf("history clear:%v %q", err, out)
	}
	if c := cache.Load(); len(c.Recent) != 0 || len(c.Playlists["spotify"]) != 1 {
		t.Fatalf("clear 只清 recent、清單保留:%+v", c)
	}
}

func TestPlaylistCandidatesMarkedWhenProviderCannotPlayThem(t *testing.T) {
	f := newPlayFake(t)
	out, _ := runCLI(t, "play", "通") // 清單子字串 + 藝人/曲目 → 歧義 TSV
	if strings.Contains(out, "暫不支援清單播放") {
		t.Fatalf("有 CapPlayPlaylist 的 provider 不該標記:%q", out)
	}
	f.caps &^= provider.CapPlayPlaylist
	out, _ = runCLI(t, "play", "通")
	if !strings.Contains(out, "playlist\tp1\t通勤\t23 首(Fake 暫不支援清單播放,用 capy pl show)") {
		t.Fatalf("無 CapPlayPlaylist 時清單候選應標明不可播:%q", out)
	}
}
