package cli

import (
	"context"
	"strings"
	"testing"

	ytauth "github.com/Tai-ch0802/capy-music/internal/auth/youtube"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	youtubeprov "github.com/Tai-ch0802/capy-music/internal/provider/youtube"
	"github.com/Tai-ch0802/capy-music/internal/provider/youtube/youtubetest"
)

// swapYouTube:youtube 走真的 provider 對 youtubetest 的假伺服器;其他平台照 pullWorld 的假 Spotify。
func swapYouTube(t *testing.T, srv *youtubetest.Server) {
	t.Helper()
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id == "youtube" {
			return youtubeprov.New(srv.Client(), srv.URL(), ytauth.Headers{Cookie: "__Secure-3PSID=p; __Secure-3PAPISID=a; __Secure-3PSIDTS=t", AuthUser: "0"}, "en", srv.Account.ChannelID), nil
		}
		return orig(ctx, id)
	}
	t.Cleanup(func() { newProvider = orig })
}

func youtubeEdits(srv *youtubetest.Server) []youtubetest.Write {
	var out []youtubetest.Write
	for _, w := range srv.Writes() {
		if w.Endpoint == "browse/edit_playlist" {
			out = append(out, w)
		}
	}
	return out
}

func youtubeActions(w youtubetest.Write) []string {
	var out []string
	for _, a := range w.Body["actions"].([]any) {
		out = append(out, a.(map[string]any)["action"].(string))
	}
	return out
}

// 跨平台複製(README「跨平台複製清單」):Spotify 清單 → 在 YouTube Music 建同名清單、resolve 靠歌名 / 歌手 / 時長(沒有 ISRC)、
// 一個 ADD 請求推兩首;之後正本換序 → YouTube 端一個請求整批取代(REMOVE 全部 + ADD 照正本順序);沒有 playlist/delete(假伺服器會抓)。
func TestMigrateSpotifyToYouTubeThenReorderIsOneReplace(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "公路旅行", "a", "b")
	yt := youtubetest.New(t)
	yt.ListLag = 1
	yt.AddTrack(youtubetest.Track{ID: "y1", Title: "song-a", Artist: "artist", Album: "A", DurationMS: 200000})
	yt.AddTrack(youtubetest.Track{ID: "y2", Title: "song-b", Artist: "artist", Album: "A", DurationMS: 200000})
	swapYouTube(t, yt)

	out, errs := mustPull(t, "migrate", "公路旅行", "--from", "spotify", "--to", "youtube", "--yes")
	created := yt.Created()
	if len(created) != 1 || yt.Name(created[0]) != "公路旅行" || !strings.Contains(errs, "推了 2 首到 youtube") {
		t.Fatalf("要在 YouTube Music 建一個同名清單並推兩首:created=%v\n%s%s", created, out, errs)
	}
	if got := yt.Rows(created[0]); strings.Join(got, ",") != "y1,y2" {
		t.Fatalf("清單要依來源順序:%v", got)
	}
	edits := youtubeEdits(yt)
	if len(edits) != 1 || strings.Join(youtubeActions(edits[0]), ",") != "ACTION_ADD_VIDEO,ACTION_ADD_VIDEO" {
		t.Fatalf("加歌是一個純 ADD 請求:%+v", edits)
	}
	if strings.Count(out, "migrate\tadd\t") != 2 {
		t.Fatalf("變更集:\n%s", out)
	}
	pl := drivePlaylistNamed(t, dc, "公路旅行")
	if pl.Links["youtube"] != yt.Account.ChannelID+"/"+created[0] {
		t.Fatalf("link 要帶帳號前綴:%v", pl.Links)
	}
	if out, errs := mustPull(t, "pl", "pull", "公路旅行", "--yes"); out != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("搬完 pull 要零變更:%s%s", out, errs)
	}

	// 正本換序 → 一個請求整批取代(REMOVE 每一列 + ADD 照正本順序);零 create、零 delete。
	editCanonical(t, dc, pl, []string{"b", "a"}, nil, "")
	before := len(yt.Writes())
	out, _ = mustPull(t, "pl", "push", "公路旅行", "--provider", "youtube", "--yes")
	if strings.Count(out, "move\t") != 1 {
		t.Fatalf("push 變更集要是一個 move:\n%s", out)
	}
	ws := yt.Writes()
	if len(ws)-before != 1 || strings.Join(youtubeActions(ws[before]), ",") != "ACTION_REMOVE_VIDEO,ACTION_REMOVE_VIDEO,ACTION_ADD_VIDEO,ACTION_ADD_VIDEO" {
		t.Fatalf("換序要是唯一一個請求、整批取代:%+v", ws[before:])
	}
	if got := yt.Rows(created[0]); strings.Join(got, ",") != "y2,y1" {
		t.Fatalf("平台結果:%v", got)
	}
	if out, errs := mustPull(t, "pl", "pull", "公路旅行", "--yes"); out != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("push 後 pull 要零變更:%s%s", out, errs)
	}
}

// pl link --create youtube:建一個跟正本同名的私人空清單再連結(id 帶帳號前綴);清單 --create 之後 pull 是零變更。
func TestPlLinkCreateYouTube(t *testing.T) {
	_, dc, _ := pullWorld(t)
	yt := youtubetest.New(t)
	swapYouTube(t, yt)
	out, errs := mustPull(t, "pl", "link", "通勤", "youtube", "--create")
	created := yt.Created()
	if len(created) != 1 || yt.Name(created[0]) != "通勤" {
		t.Fatalf("要建一個同名清單:%v\n%s%s", created, out, errs)
	}
	if pl := drivePlaylistNamed(t, dc, "通勤"); pl == nil || pl.Links["youtube"] != yt.Account.ChannelID+"/"+created[0] {
		t.Fatalf("要連上帶前綴的 id:%+v", pl)
	}
	if out, errs := mustPull(t, "pl", "pull", "通勤", "--yes"); out != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("空清單 pull 要零變更:%s%s", out, errs)
	}
}

// 別人的清單(從別的頻道存進資料庫的):列表就標 Unwritable,sync 只跳過 push 半邊並講明、零寫入;明說要推是 exit 3。
func TestPlSyncSkipsForeignOwnedYouTubePlaylist(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "通勤", "a", "b")
	yt := youtubetest.New(t)
	yt.AddTrack(youtubetest.Track{ID: "y1", Title: "song-a", Artist: "artist", Album: "A", DurationMS: 200000})
	yt.AddForeignPlaylist("PLx", "通勤", "UCotherperson00000000000", "y1")
	swapYouTube(t, yt)
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "link", "通勤", "youtube:PLx")
	mustPull(t, "pl", "sync", "通勤", "--yes") // bootstrap:兩邊拉進正本
	out, errs := mustPull(t, "pl", "sync", "通勤", "--yes")
	if !strings.Contains(errs, "跳過 通勤 的 youtube 的 push 半邊") || !strings.Contains(errs, "別的頻道") || strings.Contains(out, "push\tadd\tyoutube") {
		t.Fatalf("別人的清單那一格要跳過並講明,exit 0:\n%s%s", out, errs)
	}
	if len(youtubeEdits(yt)) != 0 {
		t.Fatalf("零寫入:%+v", yt.Writes())
	}
	if _, _, err := runPull(t, "pl", "push", "通勤", "--provider", "youtube", "--yes"); exitOf(t, err) != 3 || !strings.Contains(err.Error(), "別的頻道") {
		t.Fatalf("明說要推別人的清單是 exit 3:%v", err)
	}
}
