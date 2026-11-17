package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

// playFake:Searcher + ArtistSearcher + PlaybackController,記錄 Play 請求。
type playFake struct {
	fakeProvider
	artists []provider.Artist
	tracks  []provider.Track
	top     []provider.Track
	topErr  error // ArtistTopTracks 回它(例如 provider.ErrOnlyNamesakes)
	played  []provider.PlayRequest
	playErr error // Play 回它(例如 Apple 只打開頁面時的 *provider.OpenedError)
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
	return f.top, f.topErr
}
func (f *playFake) Devices(context.Context) ([]provider.Device, error)     { return nil, nil }
func (f *playFake) State(context.Context) (*provider.PlaybackState, error) { return nil, nil }
func (f *playFake) Play(_ context.Context, r provider.PlayRequest) error {
	f.played = append(f.played, r)
	return f.playErr
}
func (f *playFake) Pause(context.Context) error          { return nil }
func (f *playFake) Next(context.Context) error           { return nil }
func (f *playFake) Prev(context.Context) error           { return nil }
func (f *playFake) Seek(context.Context, int) error      { return nil }
func (f *playFake) SetVolume(context.Context, int) error { return nil }

func newPlayFake(t *testing.T) *playFake {
	t.Helper()
	setCLITestConfig(t)
	f := &playFake{
		fakeProvider: fakeProvider{caps: provider.CapSearch | provider.CapArtistSearch | provider.CapPlaybackControl | provider.CapPlayPlaylist | provider.CapPlayQueue},
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

func TestPlayFlagCombosAreRejectedNotIgnored(t *testing.T) {
	f := newPlayFake(t)
	for _, args := range [][]string{
		{"play", "--id", "t1", "--pick"},
		{"play", "--id", "t1", "--type", "track"},
		{"play", "--type", "track"},
		{"play", "spotify:track:0123456789abcdefABCDEF", "--type", "artist"},
	} {
		f.played = nil
		if _, err := runCLI(t, args...); err == nil || len(f.played) != 0 {
			t.Errorf("%v 應報錯且不播:err=%v played=%+v", args, err, f.played)
		}
	}
}

func TestPlayPickWithQueryPrefiltersCache(t *testing.T) {
	f := newPlayFake(t)
	orig := isInteractive
	isInteractive = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { isInteractive = orig })
	origPicker := runPlayPicker
	var offered []candidate
	runPlayPicker = func(cs []candidate) (*candidate, error) { offered = cs; return &cs[0], nil }
	t.Cleanup(func() { runPlayPicker = origPicker })

	c := cache.Load()
	c.AddRecent(cache.Recent{Provider: "spotify", Type: cache.TypeArtist, ID: "a1", Label: "五月天", Detail: "熱門歌曲"})
	c.AddRecent(cache.Recent{Provider: "spotify", Type: cache.TypePlaylist, ID: "p1", Label: "通勤", Detail: "舊的曲數"}) // 與清單快取同一筆
	_ = c.Save()

	if _, err := runCLI(t, "play", "--pick", "通"); err != nil || len(offered) != 1 || offered[0].ID != "p1" || offered[0].Detail != "23 首" {
		t.Fatalf("--pick <query> 應只列相關候選、同 type+id 去重且以清單快取那份為準:%v %+v", err, offered)
	}
	if _, err := runCLI(t, "play", "--pick", "zzz"); err == nil || !strings.Contains(err.Error(), "zzz") {
		t.Fatalf("--pick 過濾後沒東西應報錯而不是靜默丟掉搜尋詞:%v", err)
	}
	f.played = nil
	if _, err := runCLI(t, "play", "--pick"); err != nil || len(offered) != 2 {
		t.Fatalf("不帶搜尋詞列全部(去重後 2 筆):%v %+v", err, offered)
	}
}

func TestArtistLabelHonestWhenProviderCannotQueue(t *testing.T) {
	f := newPlayFake(t)
	f.caps &^= provider.CapPlayQueue
	f.top = []provider.Track{{ProviderID: "h1", Title: "知足"}, {ProviderID: "h2", Title: "乾杯"}}
	out, err := runCLI(t, "play", "artist:五月天")
	if err != nil || !strings.Contains(out, "五月天:知足(Fake 不能排佇列") || strings.Contains(out, "2 首") {
		t.Fatalf("不能排佇列的平台,標籤不得說「N 首」:%v %q", err, out)
	}
}

// TestPlayArtistWithoutTracksIsHonest:藝人挑了卻沒有歌可播:照實說找不到、建議直接搜歌名,不改播同名的別人。
// 搜尋結果全是同名的其他藝人時(Spotify 用名稱近似熱門歌曲),說出原因——不是這位藝人沒有歌;以前一律說「沒有熱門歌曲」。
func TestPlayArtistWithoutTracksIsHonest(t *testing.T) {
	f := newPlayFake(t)
	f.top = nil
	_, err := runCLI(t, "play", "artist:五月天")
	if err == nil || err.Error() != "在 Fake 找不到 五月天 的歌。試試直接搜歌名:capy play <歌名>" || len(f.played) != 0 {
		t.Errorf("找不到就照實說、不播:%v %v", err, f.played)
	}
	f.topErr = provider.ErrOnlyNamesakes
	_, err = runCLI(t, "play", "artist:五月天")
	if err == nil || !strings.Contains(err.Error(), "找不到這位 五月天 的歌") || !strings.Contains(err.Error(), "名字相同或相近的其他藝人") ||
		!strings.Contains(err.Error(), "capy play <歌名>") || len(f.played) != 0 {
		t.Errorf("只有同名的別人:說原因、不播:%v %v", err, f.played)
	}
	if code, _ := ExitCode(err); code != 1 {
		t.Errorf("沒有播:exit 1,不是 %d", code)
	}
}

// TestPlayOpenedNotPlayingIsHonest:【fails-before-fix】平台只把歌打開、沒開始播(Apple 資料庫裡沒有的目錄歌曲,決策 52):
// exit 0、不印 ▶,照實說要去 Music.app 點兩下;--id 時用平台查到的歌名,不是 id。以前 Apple 永遠印 ▶。
func TestPlayOpenedNotPlayingIsHonest(t *testing.T) {
	f := newPlayFake(t)
	f.playErr = &provider.OpenedError{Label: "Radioactivity — Kraftwerk"}
	out, err := runCLI(t, "play", "--id", "700050031")
	if err != nil || strings.Contains(out, "▶") {
		t.Fatalf("只打開了:exit 0、不印 ▶:%v %q", err, out)
	}
	if !strings.Contains(out, "「Radioactivity — Kraftwerk」") || !strings.Contains(out, "點兩下") || strings.Contains(out, "700050031") {
		t.Errorf("--id 時要用平台查到的歌名,並說去 Music.app 點兩下:%q", out)
	}
	f.caps &^= provider.CapPlayQueue // = Apple:artist: 的 label 是「藝人:歌名(只取第一首…)」整句
	f.top = []provider.Track{{ProviderID: "h1", Title: "知足"}, {ProviderID: "h2", Title: "乾杯"}}
	f.playErr = &provider.OpenedError{Label: "知足 — 五月天"}
	out, err = runCLI(t, "play", "artist:五月天")
	if err != nil || strings.Contains(out, "▶") || !strings.Contains(out, "「知足 — 五月天」") || strings.Contains(out, "只取第一首") {
		t.Errorf("artist: 也用平台查到的歌名,不要括號套括號(#96 review):%v %q", err, out)
	}
	f.playErr = &provider.OpenedError{} // 平台沒查到歌名:退回 id,不印「」
	if out, err := runCLI(t, "play", "--id", "700050031"); err != nil || !strings.Contains(out, "「700050031」") {
		t.Errorf("沒有歌名時用 id:%v %q", err, out)
	}
	if recent := cache.Load().Recent; len(recent) == 0 || recent[0].ID != "a1" {
		t.Errorf("使用者選了這個藝人:照樣記進最近播放:%+v", recent)
	}
	f.playErr = nil
	if out, err := runCLI(t, "play", "--id", "t1"); err != nil || !strings.HasPrefix(out, "▶ ") {
		t.Errorf("真的播了照舊印 ▶(web 搜尋頁靠這個分辨):%v %q", err, out)
	}
}

type playFullFake struct {
	*playFake
	listRefs   []provider.PlaylistRef
	listCalls  int
	shuffleSet []bool
}

func (f *playFullFake) ListPlaylists(context.Context) ([]provider.PlaylistRef, error) {
	f.listCalls++
	return f.listRefs, nil
}
func (f *playFullFake) GetPlaylistItems(context.Context, string) ([]provider.Track, error) {
	return nil, nil
}
func (f *playFullFake) SetShuffle(_ context.Context, on bool) error {
	f.shuffleSet = append(f.shuffleSet, on)
	return nil
}
func (f *playFullFake) SetRepeat(context.Context, provider.RepeatMode) error { return nil }

func seedCanonForPlay(t *testing.T, pls []canon.Playlist, trs map[string]canon.Track) {
	t.Helper()
	st, err := store.Open(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := canon.NewManifest()
	tracks := canon.NewTracks()
	tracks.Tracks = trs
	for _, pl := range pls {
		m.AddPlaylist(pl.PID)
	}
	if err := st.Hydrate(store.Canonical{Manifest: m, Tracks: tracks, Playlists: pls}); err != nil {
		t.Fatal(err)
	}
}

func TestPlayCanonicalPlaylistLinkedTrackIDsAndShuffleZeroWrite(t *testing.T) {
	base := newPlayFake(t)
	f := &playFullFake{playFake: base}
	swapProviderWith(t, f)

	// 建立 120 首正本清單「最愛」(未 link spotify)、「通勤」(已 link spotify:p1)、「空映射」(僅 apple mapping)
	trs := map[string]canon.Track{
		"i:onlyap": {CID: "i:onlyap", Title: "Apple Only", Artists: []string{"A"}, Mappings: map[string]canon.Mapping{"apple": {ID: "ap1"}}},
	}
	var favItems []canon.Item
	var origOrder []string
	for i := range 120 {
		cid := fmt.Sprintf("i:T%03d", i)
		spID := fmt.Sprintf("sp-%03d", i)
		origOrder = append(origOrder, spID)
		trs[cid] = canon.Track{
			CID:      cid,
			Title:    fmt.Sprintf("Track %03d", i),
			Artists:  []string{"Mayday"},
			Mappings: map[string]canon.Mapping{"spotify": {ID: spID, Confidence: 100}},
		}
		favItems = append(favItems, canon.Item{IID: fmt.Sprintf("iid%03d", i), CID: cid, Rank: fmt.Sprintf("r%03d", i)})
	}
	pls := []canon.Playlist{
		{PID: "01FAV", SchemaVersion: canon.SchemaVersion, Name: "最愛", Items: favItems, Links: map[string]string{}},
		{PID: "01COM", SchemaVersion: canon.SchemaVersion, Name: "通勤", Items: favItems[:3], Links: map[string]string{"spotify": "p1"}},
		{PID: "01UNM", SchemaVersion: canon.SchemaVersion, Name: "尚未解析", Items: []canon.Item{{IID: "u1", CID: "i:onlyap", Rank: "a"}}, Links: map[string]string{}},
		{PID: "01UNM2", SchemaVersion: canon.SchemaVersion, Name: "尚未解析二號", Items: []canon.Item{{IID: "u2", CID: "i:onlyap", Rank: "a"}}, Links: map[string]string{}},
	}
	seedCanonForPlay(t, pls, trs)
	dumpBefore := dumpBytes(t)

	// 1. 已 link 且未帶 --shuffle → 優先走 PlayRequest{PlaylistID: "p1"}
	f.played = nil
	if out, err := runCLI(t, "play", "canon:通勤"); err != nil || len(f.played) != 1 || f.played[0].PlaylistID != "p1" || !strings.Contains(out, "正本清單:通勤") {
		t.Fatalf("已連結正本未帶 --shuffle 應走 PlaylistID:err=%v played=%+v out=%q", err, f.played, out)
	}

	// 2. 已 link 但帶 --shuffle → 改走記憶體洗牌 TrackIDs,且不呼叫持久 SetShuffle
	origRand := playRandIntn
	playRandIntn = func(int) int { return 0 } // 確定性單循環位移(Sattolo:必改變順序)
	t.Cleanup(func() { playRandIntn = origRand })

	f.played, f.shuffleSet = nil, nil
	if out, err := runCLI(t, "play", "canon:通勤", "--shuffle"); err != nil || len(f.played) != 1 || f.played[0].PlaylistID != "" ||
		slices.Equal(f.played[0].TrackIDs, origOrder[:3]) || len(f.shuffleSet) != 0 || !strings.Contains(out, "正本清單(隨機):通勤") {
		t.Fatalf("正本 --shuffle 應在記憶體洗牌 TrackIDs 且不碰 SetShuffle:err=%v played=%+v shuffleSet=%v out=%q", err, f.played, f.shuffleSet, out)
	}

	// 3. 未 link 正本(> 100 首):未帶 --shuffle 依原序截斷前 100 首;帶 --shuffle 先對全 120 首洗牌再取前 100 首
	f.played = nil
	if _, err := runCLI(t, "play", "最愛"); err != nil || len(f.played) != 1 || !slices.Equal(f.played[0].TrackIDs, origOrder[:100]) {
		t.Fatalf("未給前綴應納入正本並依原序取前 100 首:err=%v got=%v", err, f.played)
	}
	f.played = nil
	if _, err := runCLI(t, "play", "--type", "canon", "01FAV", "--shuffle"); err != nil || len(f.played) != 1 || len(f.played[0].TrackIDs) != 100 ||
		!slices.Contains(f.played[0].TrackIDs, "sp-100") {
		t.Fatalf("超過 100 首洗牌應涵蓋第 100 首之後的曲目再截斷 100 首:err=%v played=%v", err, f.played)
	}

	// 決策 38 硬約束:state.db canonical 表逐位元零改動!
	if !bytes.Equal(dumpBefore, dumpBytes(t)) {
		t.Fatal("play --shuffle 絕不可改動 state.db 的 canonical 資料(決策 38)")
	}

	// 4. 正本無當前平台對應曲目 → 清楚提示先跑 capy resolve
	if _, err := runCLI(t, "play", "canon:尚未解析"); err == nil || !strings.Contains(err.Error(), "capy resolve --provider spotify") {
		t.Fatalf("無對應曲目應提示跑 capy resolve:%v", err)
	}

	// 5. 非 TTY 歧義契約:維持 4 欄 TSV 與 exit 2
	ambOut, ambErr := runCLI(t, "play", "--type", "canon", "尚未")
	if code, _ := ExitCode(ambErr); code != 2 {
		t.Fatalf("非 TTY 歧義應 exit 2:err=%v code=%d", ambErr, code)
	}
	for _, line := range strings.Split(strings.TrimRight(ambOut, "\n"), "\n") {
		if cols := strings.Split(line, "\t"); len(cols) != 4 || cols[0] != "canon" {
			t.Errorf("歧義 TSV 必須為 4 欄且首欄為 canon:%q", line)
		}
	}

	// 6. 已快取平台清單帶 --shuffle → 開播後呼叫 SetShuffle(true)
	f.played, f.shuffleSet = nil, nil
	if _, err := runCLI(t, "play", "pl:通勤", "--shuffle"); err != nil || len(f.played) != 1 || f.played[0].PlaylistID != "p1" || !slices.Equal(f.shuffleSet, []bool{true}) {
		t.Fatalf("平台清單 --shuffle 應呼叫 SetShuffle(true):err=%v played=%+v shuffleSet=%v", err, f.played, f.shuffleSet)
	}
}

func TestPlayPickColdCacheFetchesPlaylistsOnce(t *testing.T) {
	setCLITestConfig(t) // 冷快取:無 provider_playlists、無正本、無 recent
	base := &playFake{
		fakeProvider: fakeProvider{caps: provider.CapPlaybackControl | provider.CapPlaylistRead | provider.CapPlayPlaylist},
	}
	f := &playFullFake{
		playFake: base,
		listRefs: []provider.PlaylistRef{{ID: "pl-cold-1", Name: "深夜工作", Total: 12}},
	}
	swapProviderWith(t, f)

	origTTY := isInteractive
	isInteractive = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { isInteractive = origTTY })
	origPicker := runPlayPicker
	var offered []candidate
	runPlayPicker = func(cs []candidate) (*candidate, error) { offered = cs; return &cs[0], nil }
	t.Cleanup(func() { runPlayPicker = origPicker })

	out, err := runCLI(t, "play", "--pick")
	if err != nil {
		t.Fatalf("冷快取 --pick 應自動拉取清單並播放:%v", err)
	}
	if f.listCalls != 1 || len(offered) != 1 || offered[0].ID != "pl-cold-1" || !strings.Contains(out, "深夜工作") {
		t.Fatalf("應呼叫一次 ListPlaylists 並開啟挑選器:calls=%d offered=%+v out=%q", f.listCalls, offered, out)
	}
	if cached := cache.Load().Playlists["spotify"]; len(cached) != 1 || cached[0].ID != "pl-cold-1" {
		t.Fatalf("拉到的清單應寫入本機快取:%+v", cached)
	}
}
