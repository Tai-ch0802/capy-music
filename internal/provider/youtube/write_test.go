package youtube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	ytauth "github.com/Tai-ch0802/capy-music/internal/auth/youtube"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/provider/youtube/youtubetest"
)

func newWriteProvider(t *testing.T) (*Provider, *youtubetest.Server) {
	t.Helper()
	srv := youtubetest.New(t)
	for _, id := range []string{"v1", "v2", "v3", "v4", "v5"} {
		srv.AddTrack(youtubetest.Track{ID: id, Title: "Song " + id, Artist: "Artist", Album: "Album", DurationMS: 200000})
	}
	p := New(srv.Client(), srv.URL(), ytauth.Headers{Cookie: "__Secure-3PSID=p; __Secure-3PAPISID=a; __Secure-3PSIDTS=t", AuthUser: "0"}, "en", srv.Account.ChannelID)
	return p, srv
}

func editWrites(srv *youtubetest.Server) []youtubetest.Write {
	var out []youtubetest.Write
	for _, w := range srv.Writes() {
		if w.Endpoint == "browse/edit_playlist" {
			out = append(out, w)
		}
	}
	return out
}

func actionsOf(w youtubetest.Write) []string {
	var out []string
	for _, a := range w.Body["actions"].([]any) {
		m := a.(map[string]any)
		s, _ := m["action"].(string)
		if v, ok := m["addedVideoId"]; ok {
			s += ":" + v.(string)
			if m["dedupeOption"] != "DEDUPE_OPTION_SKIP" {
				s += "(no-dedupe)"
			}
		}
		if v, ok := m["removedVideoId"]; ok {
			s += ":" + v.(string)
		}
		if v, ok := m["playlistName"]; ok {
			s += ":" + v.(string)
		}
		out = append(out, s)
	}
	return out
}

func TestCapsAndPushable(t *testing.T) {
	p, _ := newWriteProvider(t)
	if c := p.Caps(); !c.Has(provider.CapPlaylistCreate|provider.CapPlaylistAppend|provider.CapPlaylistRemove|provider.CapPlaylistReorder|provider.CapPlaylistRename) || c.Has(provider.CapPlaybackControl) || c.Has(provider.CapISRCLookup) {
		t.Errorf("Caps:%b", c)
	}
	if !p.Pushable("v1") || p.Pushable("") {
		t.Error("Pushable:只有空 id 推不動")
	}
}

// youtubetest 的讀端要跟真 fixture 的 parser 對得上(grid 與清單都翻頁)。
func TestFakeServerReadSide(t *testing.T) {
	p, srv := newWriteProvider(t)
	srv.GridPageSize, srv.PageSize = 2, 2
	srv.AddForeignPlaylist("LM", "Liked Music", "")
	srv.AddPlaylist("PL1", "Road trip", "v1", "v2", "v3", "v1")
	srv.AddPlaylist("PL2", "Second")
	srv.AddForeignPlaylist("PLx", "Somebody's mix", "UCother", "v5")
	refs, err := p.ListPlaylists(context.Background())
	if err != nil || len(refs) != 4 || refs[0].Unwritable == "" || refs[1].Unwritable != "" || refs[1].Total != 4 || refs[3].Unwritable == "" || refs[1].ID != srv.Account.ChannelID+"/PL1" {
		t.Fatalf("ListPlaylists:%v %+v", err, refs)
	}
	tracks, err := p.GetPlaylistItems(context.Background(), refs[1].ID)
	if err != nil || len(tracks) != 4 || tracks[0].Title != "Song v1" || tracks[0].Artists[0] != "Artist" || tracks[0].Album != "Album" || tracks[0].DurationMS != 200000 || tracks[3].ProviderID != "v1" {
		t.Fatalf("GetPlaylistItems:%v %+v", err, tracks)
	}
	hits, err := p.Search(context.Background(), provider.Query{Text: "Song v2 Artist", Limit: 5})
	if err != nil || len(hits) != 1 || hits[0].ProviderID != "v2" || hits[0].Album != "Album" || hits[0].DurationMS != 200000 {
		t.Fatalf("Search:%v %+v", err, hits)
	}
	if tr, err := p.GetTrack(context.Background(), "v3"); err != nil || tr.Title != "Song v3" || tr.ReleaseDate != "2020" {
		t.Fatalf("GetTrack:%v %+v", err, tr)
	}
}

func TestCreatePlaylistWaitsUntilListed(t *testing.T) {
	orig := createPollDelays
	createPollDelays = []time.Duration{0, 0, 0}
	t.Cleanup(func() { createPollDelays = orig })
	p, srv := newWriteProvider(t)
	srv.ListLag = 2 // 要再列表兩次才出現(真平台約 3 s)
	ref, err := p.CreatePlaylist(context.Background(), "New one")
	if err != nil || ref.ID != srv.Account.ChannelID+"/PLnew1" || ref.Name != "New one" || ref.Total != 0 {
		t.Fatalf("CreatePlaylist:%v %+v", err, ref)
	}
	if w := srv.Writes()[0]; w.Endpoint != "playlist/create" || w.Body["privacyStatus"] != "PRIVATE" || w.Body["title"] != "New one" {
		t.Errorf("create 請求:%+v", w)
	}
	// 一直不出現:回錯,但 id 在訊息裡(pl link 接得回來)。
	srv.ListLag = 99
	if _, err := p.CreatePlaylist(context.Background(), "Slow"); err == nil || !strings.Contains(err.Error(), "PLnew2") || !strings.Contains(err.Error(), "pl link") {
		t.Errorf("逾時要帶 id 與接回的命令:%v", err)
	}
}

func TestApplyOpsPureAppendIsOneRequestWithDedupe(t *testing.T) {
	p, srv := newWriteProvider(t)
	srv.AddPlaylist("PL1", "Road trip", "v1", "v2")
	id := srv.Account.ChannelID + "/PL1"
	skipped, err := p.ApplyOps(context.Background(), id, []string{"v1", "v2"}, []provider.PlaylistOp{
		{Kind: provider.OpAdd, Pos: 2, ProviderID: "v3"}, {Kind: provider.OpAdd, Pos: 3, ProviderID: "v1"}, // 同一首第二份也要加得進去(決策 38)
	})
	if err != nil || len(skipped) != 0 {
		t.Fatal(err, skipped)
	}
	ws := editWrites(srv)
	if len(ws) != 1 || strings.Join(actionsOf(ws[0]), " ") != "ACTION_ADD_VIDEO:v3 ACTION_ADD_VIDEO:v1" {
		t.Errorf("純 append = 一個請求、每首 ADD 帶 dedupeOption:%v", ws)
	}
	if got := srv.Rows("PL1"); strings.Join(got, ",") != "v1,v2,v3,v1" {
		t.Errorf("清單:%v", got)
	}
}

func TestApplyOpsRemoveMoveIsOneReplace(t *testing.T) {
	p, srv := newWriteProvider(t)
	srv.AddPlaylist("PL1", "Road trip", "v1", "v2", "v3", "v2") // 同一首兩列:REMOVE 要各自帶不同的 setVideoId
	id := srv.Account.ChannelID + "/PL1"
	ops := []provider.PlaylistOp{
		{Kind: provider.OpRename, Name: "Renamed"},
		{Kind: provider.OpRemove, Pos: 0, ProviderID: "v1"},
		{Kind: provider.OpMove, From: 2, Pos: 0}, // [v2 v3 v2] → [v2 v2 v3]
		{Kind: provider.OpAdd, Pos: 1, ProviderID: "v4"},
	}
	if _, err := p.ApplyOps(context.Background(), id, []string{"v1", "v2", "v3", "v2"}, ops); err != nil {
		t.Fatal(err)
	}
	ws := editWrites(srv)
	if len(ws) != 1 {
		t.Fatalf("改名 + 整批取代 = 一個請求(真平台驗過同一請求也原子):%d", len(ws))
	}
	got := actionsOf(ws[0])
	want := "ACTION_SET_PLAYLIST_NAME:Renamed ACTION_REMOVE_VIDEO:v1 ACTION_REMOVE_VIDEO:v2 ACTION_REMOVE_VIDEO:v3 ACTION_REMOVE_VIDEO:v2 ACTION_ADD_VIDEO:v2 ACTION_ADD_VIDEO:v4 ACTION_ADD_VIDEO:v2 ACTION_ADD_VIDEO:v3"
	if strings.Join(got, " ") != want {
		t.Errorf("同一請求:改名、REMOVE 每一列、ADD 照 want:\n got %v\nwant %s", got, want)
	}
	sets := map[string]bool{}
	for _, a := range ws[0].Body["actions"].([]any) {
		if s, ok := a.(map[string]any)["setVideoId"].(string); ok {
			sets[s] = true
		}
	}
	if len(sets) != 4 {
		t.Errorf("四列 REMOVE 要四個不同的 setVideoId:%v", sets)
	}
	if got := srv.Rows("PL1"); strings.Join(got, ",") != "v2,v4,v2,v3" || srv.Name("PL1") != "Renamed" {
		t.Errorf("清單:%v %q", got, srv.Name("PL1"))
	}
}

func TestApplyOpsGuardsWriteNothing(t *testing.T) {
	p, srv := newWriteProvider(t)
	srv.AddPlaylist("PL1", "Road trip", "v1", "v2")
	srv.AddForeignPlaylist("PLx", "Somebody's mix", "UCother", "v5")
	srv.AddCollaborativePlaylist("PLc", "Shared", "UCother", "v5") // header 可編輯,但擁有者是別人:不寫(決策 60)
	ch := srv.Account.ChannelID
	rm := []provider.PlaylistOp{{Kind: provider.OpRemove, Pos: 0}, {Kind: provider.OpRename, Name: "X"}}
	for name, tc := range map[string]struct {
		id      string
		current []string
		want    string
	}{
		"別人的清單":    {ch + "/PLx", []string{"v5"}, "PLx"},
		"協作清單":     {ch + "/PLc", []string{"v5"}, "PLc"},
		"別的帳號的 id": {"UCother/PL1", []string{"v1", "v2"}, "UCother/PL1"},
		"讀過之後變了":   {ch + "/PL1", []string{"v1", "v2", "v3"}, "PL1"},
	} {
		_, err := p.ApplyOps(context.Background(), tc.id, tc.current, rm)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s:%v", name, err)
		}
	}
	if _, err := p.ApplyOps(context.Background(), ch+"/PL1", []string{"v1", "v2"}, []provider.PlaylistOp{{Kind: provider.OpAdd, Pos: 2, ProviderID: ""}}); err == nil {
		t.Error("空 id 要擋")
	}
	if len(editWrites(srv)) != 0 || srv.Name("PL1") != "Road trip" || srv.Name("PLc") != "Shared" {
		t.Errorf("以上全部零寫入(連 rename 都不能先落地):%v", srv.Writes())
	}
	// 沒動到 items、也沒改名:不打任何請求。
	if _, err := p.ApplyOps(context.Background(), ch+"/PL1", []string{"v1", "v2"}, nil); err != nil || len(srv.Writes()) != 0 {
		t.Errorf("no-op:%v %v", err, srv.Writes())
	}
}

// 夾一個壞 id(目錄沒有、清單也沒有的影片):真平台整包 HTTP 400、清單原封不動;這裡回錯、不動 base 的前提是 push.go 看到普通 error。
func TestApplyOpsBadIDIsAtomic(t *testing.T) {
	p, srv := newWriteProvider(t)
	srv.AddPlaylist("PL1", "Road trip", "v1", "v2")
	_, err := p.ApplyOps(context.Background(), srv.Account.ChannelID+"/PL1", []string{"v1", "v2"}, []provider.PlaylistOp{{Kind: provider.OpMove, From: 1, Pos: 0}, {Kind: provider.OpAdd, Pos: 2, ProviderID: "zzz"}})
	var pw *provider.PartialWriteError
	if err == nil || errors.As(err, &pw) || !strings.Contains(err.Error(), "PL1") {
		t.Errorf("整批失敗是普通 error(不是 PartialWriteError):%v", err)
	}
	if got := srv.Rows("PL1"); strings.Join(got, ",") != "v1,v2" {
		t.Errorf("清單要原封不動:%v", got)
	}
}

// want 為空 = 清空整份(REMOVE 全部、沒有 ADD):SPI 不擋,呼叫端(dry-run、閾值、確認)擋。
func TestApplyOpsEmptyWantRemovesAll(t *testing.T) {
	p, srv := newWriteProvider(t)
	srv.AddPlaylist("PL1", "Road trip", "v1", "v2")
	if _, err := p.ApplyOps(context.Background(), srv.Account.ChannelID+"/PL1", []string{"v1", "v2"}, []provider.PlaylistOp{{Kind: provider.OpRemove, Pos: 1}, {Kind: provider.OpRemove, Pos: 0}}); err != nil {
		t.Fatal(err)
	}
	if got := srv.Rows("PL1"); len(got) != 0 {
		t.Errorf("要清空:%v", got)
	}
	if ws := editWrites(srv); len(ws) != 1 || len(actionsOf(ws[0])) != 2 {
		t.Errorf("一個請求兩個 REMOVE:%v", ws)
	}
}
