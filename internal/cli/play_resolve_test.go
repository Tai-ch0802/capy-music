package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func srcFx(pls []cache.Playlist, artists []provider.Artist, tracks []provider.Track) playSources {
	return playSources{
		playlists: func() []cache.Playlist { return pls },
		artists: func(context.Context, string, int) ([]provider.Artist, error) {
			return artists, nil
		},
		tracks: func(_ context.Context, _ string, limit int) ([]provider.Track, error) {
			if len(tracks) > limit {
				return tracks[:limit], nil
			}
			return tracks, nil
		},
	}
}

func TestParsePlayQuery(t *testing.T) {
	cases := []struct {
		args       []string
		typ, q, wt string
		wantErr    bool
	}{
		{[]string{"五月天"}, "", "五月天", "", false},
		{[]string{"artist:五月天"}, "", "五月天", cache.TypeArtist, false},
		{[]string{"pl:", "通勤"}, "", "通勤", cache.TypePlaylist, false},
		{[]string{"canon:通勤"}, "", "通勤", typeCanon, false},
		{[]string{"canon:", "01JABC"}, "canon", "01JABC", typeCanon, false},
		{[]string{"track:派對動物"}, "track", "派對動物", cache.TypeTrack, false},
		{[]string{"artist:五月天"}, "track", "", "", true},
		{[]string{"canon:通勤"}, "playlist", "", "", true},
		{[]string{"x"}, "album", "", "", true},
	}
	for _, c := range cases {
		q, typ, err := parsePlayQuery(c.args, c.typ)
		if (err != nil) != c.wantErr || q != c.q || typ != c.wt {
			t.Errorf("parsePlayQuery(%v, %q) = (%q, %q, %v), want (%q, %q, err=%v)", c.args, c.typ, q, typ, err, c.q, c.wt, c.wantErr)
		}
	}
}

func TestResolvePlayRules(t *testing.T) {
	pls := []cache.Playlist{{ID: "p1", Name: "通勤", Total: 23}, {ID: "p2", Name: "通勤 2023", Total: 5}}
	mayday := provider.Artist{ProviderID: "a1", Name: "五月天"}
	party := provider.Track{ProviderID: "t1", Title: "派對動物", Artists: []string{"五月天"}, Album: "自傳"}
	partyLive := provider.Track{ProviderID: "t2", Title: "派對動物 Life Live", Artists: []string{"五月天"}}
	cases := []struct {
		name     string
		src      playSources
		q, typ   string
		wantHit  string // 命中的 candidate ID;空 = 無命中
		wantCand int    // 候選數(歧義或找不到)
	}{
		{"3a 清單名完全相符", srcFx(pls, []provider.Artist{mayday}, []provider.Track{party}), "通勤", "", "p1", 0},
		{"3a 大小寫與空白不計", srcFx([]cache.Playlist{{ID: "p9", Name: "Chill Mix"}}, nil, []provider.Track{party, partyLive}), " chill mix ", "", "p9", 0},
		{"3b 藝人名完全相符(清單只有子字串命中)", srcFx(pls, []provider.Artist{mayday}, []provider.Track{party, partyLive}), "五月天", "", "a1", 0},
		{"3c 曲名完全相符恰一", srcFx(nil, []provider.Artist{mayday}, []provider.Track{party, partyLive}), "派對動物", "", "t1", 0},
		{"3c 曲名完全相符兩筆 → 歧義", srcFx(nil, nil, []provider.Track{party, {ProviderID: "t3", Title: "派對動物", Artists: []string{"MxG"}}}), "派對動物", "", "", 2},
		{"3d 無清單無藝人、曲目恰一", srcFx(nil, nil, []provider.Track{partyLive}), "life live", "", "t2", 0},
		{"歧義:清單子字串 2 + 藝人 1(靠曲目藝人欄接上)+ 曲目 0(與「通」無關被濾掉)", srcFx(pls, []provider.Artist{mayday}, []provider.Track{party, partyLive}), "通", "", "", 3},
		{"無關藝人被濾掉、曲目恰一 → 播", srcFx(nil, []provider.Artist{{ProviderID: "x1", Name: "Patrick Brasca"}, {ProviderID: "x2", Name: "neu"}}, []provider.Track{partyLive}), "life live", "", "t2", 0},
		{"五月天 → Spotify 回英文名 Mayday:靠曲目的藝人欄留下,曲目濾光 → 唯一候選直接播", srcFx(nil, []provider.Artist{{ProviderID: "m", Name: "Mayday"}}, []provider.Track{{ProviderID: "t7", Title: "知足", Artists: []string{"Mayday"}, Album: "自傳"}}), "五月天", "", "m", 0},
		{"全部濾光就退回平台原始結果(不比平台更差)", srcFx(nil, []provider.Artist{{ProviderID: "x1", Name: "Patrick Brasca"}}, []provider.Track{{ProviderID: "t8", Title: "Nothing", Artists: []string{"Nobody"}}}), "zzz", "", "", 2},
		{"找不到", srcFx(nil, nil, nil), "zzz", "", "", 0},
		{"--type track = 平台第一筆", srcFx(pls, []provider.Artist{mayday}, []provider.Track{partyLive, party}), "派對動物", cache.TypeTrack, "t2", 0},
		{"--type artist = 平台第一筆", srcFx(nil, []provider.Artist{{ProviderID: "a2", Name: "五月天 Mayday"}, mayday}, nil), "五月天", cache.TypeArtist, "a2", 0},
		{"--type playlist 完全相符優先於子字串", srcFx(pls, nil, nil), "通勤", cache.TypePlaylist, "p1", 0},
		{"--type playlist 子字串恰一", srcFx(pls, nil, nil), "2023", cache.TypePlaylist, "p2", 0},
		{"--type playlist 子字串多筆 → 歧義", srcFx(pls, nil, nil), "通", cache.TypePlaylist, "", 2},
		{"--type playlist 不打網路", playSources{playlists: func() []cache.Playlist { return pls }, tracks: func(context.Context, string, int) ([]provider.Track, error) {
			t.Fatal("--type playlist 不得搜尋曲目")
			return nil, nil
		}}, "通勤", cache.TypePlaylist, "p1", 0},
		{"provider 不支援藝人搜尋(artists nil)仍可解析", playSources{playlists: func() []cache.Playlist { return nil }, tracks: srcFx(nil, nil, []provider.Track{party}).tracks}, "派對動物", "", "t1", 0},
	}
	for _, c := range cases {
		hit, cands, err := resolvePlay(context.Background(), c.src, c.q, c.typ)
		if err != nil {
			t.Errorf("%s:%v", c.name, err)
			continue
		}
		gotHit := ""
		if hit != nil {
			gotHit = hit.ID
		}
		if gotHit != c.wantHit || len(cands) != c.wantCand {
			t.Errorf("%s:hit=%q cands=%d,want hit=%q cands=%d", c.name, gotHit, len(cands), c.wantHit, c.wantCand)
		}
	}
}

func TestResolvePlaySurfacesSearchError(t *testing.T) {
	src := srcFx(nil, nil, nil)
	src.tracks = func(context.Context, string, int) ([]provider.Track, error) { return nil, provider.ErrAuthExpired }
	if _, _, err := resolvePlay(context.Background(), src, "x", ""); !errors.Is(err, provider.ErrAuthExpired) {
		t.Fatalf("搜尋錯誤要往上傳(不可吞成找不到):%v", err)
	}
}

func TestResolvePlayPlaylistExactHitSkipsNetwork(t *testing.T) {
	src := playSources{
		playlists: func() []cache.Playlist { return []cache.Playlist{{ID: "p1", Name: "通勤"}} },
		artists: func(context.Context, string, int) ([]provider.Artist, error) {
			t.Fatal("清單名完全相符不得搜尋藝人")
			return nil, nil
		},
		tracks: func(context.Context, string, int) ([]provider.Track, error) {
			t.Fatal("清單名完全相符不得搜尋曲目")
			return nil, nil
		},
	}
	hit, _, err := resolvePlay(context.Background(), src, "通勤", "")
	if err != nil || hit == nil || hit.ID != "p1" {
		t.Fatalf("3a 應在打網路前命中:%v %v", hit, err)
	}
}

func TestRememberRecentDropsNote(t *testing.T) {
	setCLITestConfig(t)
	rememberRecent("spotify", candidate{Type: cache.TypePlaylist, ID: "p1", Label: "通勤", Detail: "23 首", Note: "(Apple 暫不支援清單播放)"})
	if r := cache.Load().Recent; len(r) != 1 || r[0].Detail != "23 首" {
		t.Fatalf("Note 不得進快取:%+v", r)
	}
}

func TestResolvePlayCanonicalPlaylists(t *testing.T) {
	canons := []canonPlaylist{
		{PID: "01PIDCOMMUTE", Name: "通勤", Total: 10, Mapped: 8, LinkID: "p1"},
		{PID: "01PIDROAD", Name: "公路旅行", Total: 5, Mapped: 5},
		{PID: "01PIDROAD2", Name: "公路旅行 2026", Total: 3, Mapped: 3},
	}
	noNet := playSources{
		playlists:       func() []cache.Playlist { return []cache.Playlist{{ID: "p1", Name: "通勤", Total: 10}} },
		canons:          func() []canonPlaylist { return canons },
		canPlayPlaylist: true,
		artists: func(context.Context, string, int) ([]provider.Artist, error) {
			t.Fatal("本機正本精確命中不得打網路")
			return nil, nil
		},
		tracks: func(context.Context, string, int) ([]provider.Track, error) {
			t.Fatal("本機正本精確命中不得打網路")
			return nil, nil
		},
	}

	// 1. 未給前綴的一般統一搜尋納入未連結的正本清單(精確命中不打網路)
	hit, _, err := resolvePlay(context.Background(), noNet, "公路旅行", "")
	if err != nil || hit == nil || hit.Type != typeCanon || hit.ID != "01PIDROAD" {
		t.Fatalf("正本名稱精確命中:err=%v hit=%+v", err, hit)
	}

	// 2. PID 精確命中
	hit, _, err = resolvePlay(context.Background(), noNet, "01PIDROAD2", "")
	if err != nil || hit == nil || hit.Type != typeCanon || hit.ID != "01PIDROAD2" {
		t.Fatalf("正本 PID 精確命中:err=%v hit=%+v", err, hit)
	}

	// 3. --type canon 子字串多筆 → 歧義
	hit, cands, err := resolvePlay(context.Background(), noNet, "公路", typeCanon)
	if err != nil || hit != nil || len(cands) != 2 {
		t.Fatalf("--type canon 歧義:err=%v hit=%+v cands=%+v", err, hit, cands)
	}

	// 4. 當平台不支援 CapPlayPlaylist(如 Apple Music)時,未加前綴優先選有對應曲目的正本清單
	appleSrc := noNet
	appleSrc.canPlayPlaylist = false
	hit, _, err = resolvePlay(context.Background(), appleSrc, "通勤", "")
	if err != nil || hit == nil || hit.Type != typeCanon || hit.ID != "01PIDCOMMUTE" {
		t.Fatalf("無 CapPlayPlaylist 時應選可播的 canon:err=%v hit=%+v", err, hit)
	}
}
