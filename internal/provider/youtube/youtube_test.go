package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	ytauth "github.com/Tai-ch0802/capy-music/internal/auth/youtube"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const testChannel = "UCtestchannel000000000000"

var testHeaders = ytauth.Headers{Cookie: "__Secure-3PSID=p3; __Secure-3PAPISID=ap3; __Secure-3PSIDTS=ts", AuthUser: "0"}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeInnerTube:照端點與 body 回 fixture(2026-09-29 真帳號回應去識別化後修剪的),並記下最後一個請求的標頭與 body。
type fakeInnerTube struct {
	t         *testing.T
	lastHdr   http.Header
	lastBody  map[string]any
	lastURL   string
	calls     atomic.Int32
	emptyVL   int32 // 前幾次 VL 回一頁沒有清單的東西(探測看過的偶發延遲)
	badCont   bool  // 續頁回奇怪的形狀(連結構都沒有)
	emptyCont bool  // 續頁有結構但零項目(清單剛好在頁界結束)
}

func (f *fakeInnerTube) handler(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.lastHdr, f.lastBody, f.lastURL = r.Header.Clone(), body, r.URL.String()
	ep := strings.TrimPrefix(r.URL.Path, "/")
	browseID, _ := body["browseId"].(string)
	var name string
	switch {
	case ep == "account/account_menu":
		name = "account_menu.json"
	case ep == "browse" && r.URL.Query().Get("ctoken") != "":
		if r.URL.Query().Get("continuation") == "" || r.URL.Query().Get("type") != "next" {
			f.t.Errorf("grid continuation 要三個查詢參數:%s", r.URL.String())
		}
		if f.badCont {
			_, _ = w.Write([]byte(`{"responseContext":{}}`))
			return
		}
		if f.emptyCont {
			_, _ = w.Write([]byte(`{"continuationContents":{"gridContinuation":{"items":[]}}}`))
			return
		}
		name = "library_cont.json"
	case ep == "browse" && browseID == "FEmusic_liked_playlists":
		name = "library_first.json"
	case ep == "browse" && browseID == "VLPLtest001":
		if atomic.LoadInt32(&f.emptyVL) > 0 {
			atomic.AddInt32(&f.emptyVL, -1)
			_, _ = w.Write([]byte(`{"responseContext":{}}`))
			return
		}
		name = "playlist_first.json"
	case ep == "browse" && body["continuation"] != nil:
		if f.badCont {
			_, _ = w.Write([]byte(`{"responseContext":{}}`))
			return
		}
		if f.emptyCont {
			_, _ = w.Write([]byte(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[]}}]}`))
			return
		}
		name = "playlist_cont.json"
	case ep == "search":
		if body["params"] != songsParams {
			f.t.Errorf("search 要帶歌曲 filter,拿到 %v", body["params"])
		}
		name = "search_songs.json"
	case ep == "next":
		name = "next.json"
	default:
		http.Error(w, "unexpected "+ep+" "+browseID, http.StatusNotFound)
		return
	}
	_, _ = w.Write(fixture(f.t, name))
}

func newTestProvider(t *testing.T, lang string) (*Provider, *fakeInnerTube) {
	t.Helper()
	f := &fakeInnerTube{t: t}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return New(srv.Client(), srv.URL, testHeaders, lang, testChannel), f
}

func TestRequestShape(t *testing.T) {
	p, f := newTestProvider(t, "zh-TW")
	a, err := p.AccountInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a != (Account{Name: "Someone", Handle: "@someone", ChannelID: testChannel}) {
		t.Errorf("account:%+v", a)
	}
	h := f.lastHdr
	if h.Get("Cookie") != testHeaders.Cookie || !strings.HasPrefix(h.Get("Authorization"), "SAPISIDHASH ") ||
		h.Get("X-Goog-AuthUser") != "0" || h.Get("X-Origin") != ytauth.Origin || h.Get("Origin") != ytauth.Origin || h.Get("X-Goog-PageId") != "" {
		t.Errorf("標頭:%v", h)
	}
	client, _ := f.lastBody["context"].(map[string]any)["client"].(map[string]any)
	if client["clientName"] != "WEB_REMIX" || client["hl"] != "zh-TW" || client["gl"] != "TW" || !strings.HasPrefix(client["clientVersion"].(string), "1.20") {
		t.Errorf("context.client:%v", client)
	}
	if !strings.Contains(f.lastURL, "alt=json") {
		t.Errorf("url:%s", f.lastURL)
	}
	// 品牌帳號:X-Goog-PageId 標頭 + context.user.onBehalfOfUser。
	brand := testHeaders
	brand.PageID = "123"
	pb := New(p.c.hc, p.c.base, brand, "en", testChannel)
	if _, err := pb.AccountInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.lastHdr.Get("X-Goog-PageId") != "123" || f.lastBody["context"].(map[string]any)["user"].(map[string]any)["onBehalfOfUser"] != "123" {
		t.Errorf("品牌帳號:%v %v", f.lastHdr, f.lastBody["context"])
	}
	if c := NewClient(nil, "", testHeaders, ""); c.hl != "en" || c.gl != "US" {
		t.Errorf("沒設語系:%s %s", c.hl, c.gl)
	}
}

func TestListPlaylists(t *testing.T) {
	p, _ := newTestProvider(t, "en")
	refs, err := p.ListPlaylists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 第 0 格「新增清單」沒有 browseId、不算;第一頁 4 份 + 續頁 2 份(舊式 continuation 走查詢參數)。
	if len(refs) != 6 {
		t.Fatalf("要 6 份清單,拿到 %d:%+v", len(refs), refs)
	}
	want := []struct {
		id, name, owner string
		total           int
		unwritable      bool
	}{
		{testChannel + "/LM", "Liked Music", "", -1, true},
		{testChannel + "/RDPN", "New Episodes", "", 38, true},
		{testChannel + "/PLtest001", "Road trip", "Someone", 3, false},
		{testChannel + "/PLtest002", "Somebody's mix", "Other Person", 3, true},
		{testChannel + "/PLtest003", "Playlist 1", "Someone", 30, false},
		{testChannel + "/PLtest004", "Playlist 2", "Someone", 64, false},
	}
	for i, w := range want {
		r := refs[i]
		if r.ID != w.id || r.Name != w.name || r.Owner != w.owner || r.Total != w.total || (r.Unwritable != "") != w.unwritable || r.Version != "" {
			t.Errorf("[%d] got %+v want %+v", i, r, w)
		}
	}
	if !strings.Contains(refs[3].Unwritable, "Other Person") {
		t.Errorf("別人的清單要點名擁有者:%q", refs[3].Unwritable)
	}
	if !p.Foreign("UCsomeoneelse/PLx") || p.Foreign(refs[2].ID) {
		t.Error("Foreign:前綴不是自己的頻道才是別人的")
	}
}

func TestGetPlaylistItems(t *testing.T) {
	p, f := newTestProvider(t, "en")
	tracks, err := p.GetPlaylistItems(context.Background(), testChannel+"/PLtest001")
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 6 { // 第一頁 3 列 + 1 列灰掉的(仍有 videoId,留著)+ 續頁 2 列(token 放 body)
		t.Fatalf("要 6 首,拿到 %d", len(tracks))
	}
	if f.lastBody["continuation"] == nil {
		t.Errorf("清單續頁的 token 要放在 body:%v", f.lastBody)
	}
	first := tracks[0]
	if first.ProviderID != "vid00000001" || first.Title != "Song 1" || len(first.Artists) != 1 || first.Artists[0] != "Artist 1" ||
		first.Album != "Album 1" || first.DurationMS != 258000 || first.URL != "https://music.youtube.com/watch?v=vid00000001" || first.ISRC != "" {
		t.Errorf("第一首:%+v", first)
	}
	if len(tracks[2].Artists) != 3 { // 三位歌手是三個 run,中間的「, 」「 & 」是分隔
		t.Errorf("多位歌手:%v", tracks[2].Artists)
	}
	if g := tracks[3]; g.ProviderID != "vid00000004" || g.Title != "Song 4" || len(g.Artists) != 1 || g.Artists[0] != "Artist 4" || g.Album != "Album 4" || g.DurationMS != 237000 {
		t.Errorf("灰掉的列(沒有端點,只看位置):%+v", g)
	}
	if tracks[5].Title != "Song 102" || tracks[5].DurationMS != 266000 {
		t.Errorf("續頁:%+v", tracks[5])
	}
	if len(first.Raw) == 0 {
		t.Error("Raw 要留 renderer")
	}
	if _, err := p.GetPlaylistItems(context.Background(), "UCsomeoneelse/PLtest001"); err == nil || !strings.Contains(err.Error(), "UCsomeoneelse/PLtest001") {
		t.Errorf("別的帳號的清單要拒絕並帶 id:%v", err)
	}
}

func TestEmptyPageRetry(t *testing.T) {
	orig := playlistPageWait
	playlistPageWait = 0
	t.Cleanup(func() { playlistPageWait = orig })
	p, f := newTestProvider(t, "en")
	f.emptyVL = 2 // 前兩次回沒有清單的頁(探測看過的偶發延遲),第三次正常
	tracks, err := p.GetPlaylistItems(context.Background(), testChannel+"/PLtest001")
	if err != nil || len(tracks) != 6 {
		t.Fatalf("重讀後要成功:%v %d", err, len(tracks))
	}
	f.emptyVL = 99
	if _, err := p.GetPlaylistItems(context.Background(), testChannel+"/PLtest001"); err == nil || !strings.Contains(err.Error(), "PLtest001") {
		t.Errorf("一直沒有清單頁要報錯:%v", err)
	}
}

func TestSearchAndGetTrack(t *testing.T) {
	p, f := newTestProvider(t, "en")
	tracks, err := p.Search(context.Background(), provider.Query{Text: "yellow", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if f.lastBody["query"] != "yellow" {
		t.Errorf("query:%v", f.lastBody)
	}
	if len(tracks) != 2 { // 一頁有 3 筆,Limit 2 就截
		t.Fatalf("Limit:%d", len(tracks))
	}
	if h := tracks[0]; h.ProviderID != "vid00000007" || h.Title != "Hit 1" || len(h.Artists) != 1 || h.Artists[0] != "Artist 1" || h.Album != "Album 1" || h.DurationMS != 267000 {
		t.Errorf("搜尋結果(歌手 • 專輯 • 時長都在 flexColumns[1] 的 runs):%+v", h)
	}
	tr, err := p.GetTrack(context.Background(), "vid00000001")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Title != "Song 1" || len(tr.Artists) != 1 || tr.Album != "Album 1" || tr.ReleaseDate != "2013" || tr.DurationMS != 258000 {
		t.Errorf("next 的第一項:%+v", tr)
	}
	if _, err := p.GetTrack(context.Background(), "nope"); !errors.Is(err, provider.ErrNotFound) { // 回的是別首 → 不存在
		t.Errorf("GetTrack 對不上要 ErrNotFound:%v", err)
	}
}

func TestAuthExpired(t *testing.T) {
	status := http.StatusUnauthorized
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"actions":[{"openPopupAction":{"popup":{"multiPageMenuRenderer":{"sections":[]}}}}]}`)) // 200 但沒有帳號
			return
		}
		http.Error(w, "nope", status)
	}))
	t.Cleanup(srv.Close)
	p := New(srv.Client(), srv.URL, testHeaders, "en", testChannel)
	for _, st := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusOK} {
		status = st
		if err := p.Health(context.Background()); !errors.Is(err, provider.ErrAuthExpired) {
			t.Errorf("HTTP %d 要對到 ErrAuthExpired:%v", st, err)
		}
	}
	status = http.StatusBadRequest
	var ae *apiError
	if err := p.Health(context.Background()); !errors.As(err, &ae) || ae.Status != 400 {
		t.Errorf("其他狀態回 apiError:%v", err)
	}
}

func TestParseDuration(t *testing.T) {
	for s, want := range map[string]int{"4:18": 258000, "1:02:03": 3723000, "0:07": 7000, "abc": 0, "": 0, " 3:57 ": 237000} {
		if got := parseDuration(s); got != want {
			t.Errorf("%q → %d, want %d", s, got, want)
		}
	}
}

// zh-TW 的歌手分隔 run 是「、」「和」(hl 跟語系走):同一欄有帶端點的 run 時,純文字就是分隔;整欄沒端點(灰列)才是內容。
func TestParseRowLocalizedSeparators(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"T"}]}}},
	  {"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[
	    {"text":"John","navigationEndpoint":{"browseEndpoint":{"browseId":"UCa"}}},{"text":"、"},
	    {"text":"Brett","navigationEndpoint":{"browseEndpoint":{"browseId":"UCb"}}},{"text":"和"},
	    {"text":"The Sydney Scoring Orchestra"}]}}},
	  {"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"P.S.","navigationEndpoint":{"browseEndpoint":{"browseId":"MPREb_x"}}}]}}}]}`), &v)
	r := parseRow(node{v})
	if len(r.artists) != 3 || r.artists[0] != "John" || r.artists[2] != "The Sydney Scoring Orchestra" || r.album != "P.S." {
		t.Errorf("分隔字不進歌手、沒有頻道頁的樂團要留:%+v", r)
	}
	_ = json.Unmarshal([]byte(`{"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"T"}]}}},
	  {"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"R-chord"}]}}},
	  {"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Some Album"}]}}}],"musicItemRendererDisplayPolicy":"MUSIC_ITEM_RENDERER_DISPLAY_POLICY_GREY_OUT"}`), &v)
	g := parseRow(node{v})
	if len(g.artists) != 1 || g.artists[0] != "R-chord" || g.album != "Some Album" || !g.grey {
		t.Errorf("灰列沒端點才照位置:%+v", g)
	}
}

func TestParseRowExplicitAndFallbacks(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"T"}]}}},
	  {"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"A"},{"text":" • "},{"text":"3:00"}]}}}],
	  "badges":[{"musicInlineBadgeRenderer":{"icon":{"iconType":"MUSIC_EXPLICIT_BADGE"}}}],
	  "overlay":{"musicItemThumbnailOverlayRenderer":{"content":{"musicPlayButtonRenderer":{"playNavigationEndpoint":{"watchEndpoint":{"videoId":"vX","watchEndpointMusicSupportedConfigs":{"watchEndpointMusicConfig":{"musicVideoType":"MUSIC_VIDEO_TYPE_UGC"}}}}}}}}}`), &v)
	r := parseRow(node{v})
	if r.videoID != "vX" || !r.explicit || r.videoType != "MUSIC_VIDEO_TYPE_UGC" || r.durationMS != 180000 || len(r.artists) != 1 || r.artists[0] != "A" || r.title != "T" {
		t.Errorf("%+v", r)
	}
}

// 頻道 id 是所有清單 id 的前綴、會寫進 Drive:走固定路徑、照陣列順序、要唯一;不只一個或沒有都不准登入(PR #117 review 第 3 點)。
func TestParseAccountChannelID(t *testing.T) {
	menu := func(items ...string) string {
		return `{"actions":[{"openPopupAction":{"popup":{"multiPageMenuRenderer":{"header":{"activeAccountHeaderRenderer":{"accountName":{"runs":[{"text":"S"}]},"channelHandle":{"runs":[{"text":"@s"}]}}},
		  "sections":[{"multiPageMenuSectionRenderer":{"items":[` + strings.Join(items, ",") + `]}}]}}}}]}`
	}
	link := func(id, pt string) string {
		return `{"compactLinkRenderer":{"navigationEndpoint":{"browseEndpoint":{"browseId":"` + id + `","browseEndpointContextSupportedConfigs":{"browseEndpointContextMusicConfig":{"pageType":"` + pt + `"}}}}}}`
	}
	parse := func(js string) (Account, error) {
		var v any
		if err := json.Unmarshal([]byte(js), &v); err != nil {
			t.Fatal(err)
		}
		return parseAccount(node{v})
	}
	if a, err := parse(menu(link("FEmusic_history", ""), link("UCme", "MUSIC_PAGE_TYPE_USER_CHANNEL"), link("UCme", "MUSIC_PAGE_TYPE_USER_CHANNEL"))); err != nil || a.ChannelID != "UCme" {
		t.Errorf("同一個 id 重複算一個:%+v %v", a, err)
	}
	if _, err := parse(menu(link("UCme", "MUSIC_PAGE_TYPE_USER_CHANNEL"), link("UCother", "MUSIC_PAGE_TYPE_USER_CHANNEL"))); !errors.Is(err, errChannelID) {
		t.Errorf("兩個不同的 UC… 要回錯,不能挑一個:%v", err)
	}
	if _, err := parse(menu(link("FEmusic_history", ""))); !errors.Is(err, errChannelID) {
		t.Errorf("沒有 UC… 要回錯:%v", err)
	}
	if _, err := parse(`{"actions":[]}`); !errors.Is(err, errNoAccount) {
		t.Errorf("沒有帳號:%v", err)
	}
	// 走 provider:兩個 UC… 時 AccountInfo 回錯而且不是 ErrAuthExpired(不是「沒登入」)。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(menu(link("UCme", "MUSIC_PAGE_TYPE_USER_CHANNEL"), link("UCother", "MUSIC_PAGE_TYPE_USER_CHANNEL"))))
	}))
	t.Cleanup(srv.Close)
	if _, err := New(srv.Client(), srv.URL, testHeaders, "en", "UCme").AccountInfo(context.Background()); err == nil || errors.Is(err, provider.ErrAuthExpired) || !strings.Contains(err.Error(), "UCother") {
		t.Errorf("AccountInfo:%v", err)
	}
}

// 分隔看交錯位置,不看長度:2–3 個字、沒有頻道頁的 CJK 歌手要留下(PR #117 review 第 6 點)。
func TestParseRowShortArtistsWithoutChannel(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"T"}]}}},
	  {"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[
	    {"text":"A","navigationEndpoint":{"browseEndpoint":{"browseId":"UCa"}}},{"text":"、"},{"text":"林俊傑"},{"text":"和"},{"text":"蘇打綠"},{"text":" & "},
	    {"text":"B","navigationEndpoint":{"browseEndpoint":{"browseId":"UCb"}}}]}}}]}`), &v)
	r := parseRow(node{v})
	if strings.Join(r.artists, "|") != "A|林俊傑|蘇打綠|B" {
		t.Errorf("短的 CJK 歌手不能被當分隔吃掉:%v", r.artists)
	}
	_ = json.Unmarshal([]byte(`{"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"T"}]}}},
	  {"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"蘇打綠"},{"text":"、"},{"text":"B","navigationEndpoint":{"browseEndpoint":{"browseId":"UCb"}}}]}}}]}`), &v)
	if r := parseRow(node{v}); strings.Join(r.artists, "|") != "蘇打綠|B" {
		t.Errorf("沒端點的在前面也一樣:%v", r.artists)
	}
}

// 續頁上限與空續頁都回錯:靜默截斷會讓 pull 把沒列到的清單當 gone(PR #117 review 第 4 點)。
func TestContinuationNeverTruncates(t *testing.T) {
	origList, origItems := maxListPages, maxItemPages
	t.Cleanup(func() { maxListPages, maxItemPages = origList, origItems })
	p, f := newTestProvider(t, "en")
	maxListPages = 1 // 第一頁有續頁 token,但不准再翻 → 錯
	if _, err := p.ListPlaylists(context.Background()); err == nil || !strings.Contains(err.Error(), "FEmusic_liked_playlists") {
		t.Errorf("清單列表到上限要回錯:%v", err)
	}
	maxListPages = origList
	maxItemPages = 0
	if _, err := p.GetPlaylistItems(context.Background(), testChannel+"/PLtest001"); err == nil || !strings.Contains(err.Error(), "PLtest001") {
		t.Errorf("清單內容到上限要回錯:%v", err)
	}
	maxItemPages = origItems
	// 續頁回奇怪的形狀(一列都沒解析出來)→ 錯,不當成最後一頁。
	f.badCont = true
	if _, err := p.GetPlaylistItems(context.Background(), testChannel+"/PLtest001"); err == nil || !strings.Contains(err.Error(), "VLPLtest001") {
		t.Errorf("空續頁要回錯:%v", err)
	}
	if _, err := p.ListPlaylists(context.Background()); err == nil || !strings.Contains(err.Error(), "FEmusic_liked_playlists") {
		t.Errorf("清單列表的空續頁要回錯:%v", err)
	}
	// 有結構但零項目 = 清單剛好在頁界結束(YouTube 會這樣回):照常結束,只有第一頁的東西。
	f.badCont, f.emptyCont = false, true
	if tracks, err := p.GetPlaylistItems(context.Background(), testChannel+"/PLtest001"); err != nil || len(tracks) != 4 {
		t.Errorf("有結構的空續頁 = 結尾:%v %d", err, len(tracks))
	}
	if refs, err := p.ListPlaylists(context.Background()); err != nil || len(refs) != 4 {
		t.Errorf("清單列表有結構的空續頁 = 結尾:%v %d", err, len(refs))
	}
}

// continuation token 原樣接進查詢字串:YouTube 給的 token 尾端是 %3D(已經 URL 編碼),再編一次會變 %253D。
func TestContinuationTokenNotReencoded(t *testing.T) {
	p, f := newTestProvider(t, "en")
	if _, err := p.ListPlaylists(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.lastURL, "ctoken=CONTINUATION-TOKEN-") || strings.Contains(f.lastURL, "%25") {
		t.Errorf("token 不可以再編碼:%s", f.lastURL)
	}
}
