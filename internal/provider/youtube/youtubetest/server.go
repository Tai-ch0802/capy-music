// Package youtubetest 是記憶體版的假 InnerTube(music.youtube.com/youtubei/v1),給 provider/youtube 與 cli 的測試共用
// (同 drivetest 的做法)。回應照 2026-09-29 真帳號回應的形狀組(renderer 名、browseId 前綴、playlistItemData、兩種 continuation),
// 只實作 capy 會打的端點與 action;playlist/delete 一律讓測試失敗——capy 絕不刪清單(決策 60)。
// 寫入照真平台的語意:一個 edit_playlist 請求整包驗證、整包套用(夾一個壞 id 就 HTTP 400、清單原封不動;探測驗過),
// 不帶 dedupeOption 的重複 ADD 回 STATUS_FAILED;新清單要等 ListLag 次列表之後才出現(真平台約 3 s)。
package youtubetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode"
)

// Track:目錄裡的一首(search 回它、清單列印它的資料)。
type Track struct {
	ID, Title, Artist, Album string
	DurationMS               int
}

// Row:清單裡的一列;同一首兩列的 SetVideoID 不同(真平台如此)。
type Row struct{ VideoID, SetVideoID string }

// Playlist:一份清單。Owner 是頻道 id;不是帳號自己的(別人的、LM / RD… 自動清單)就不可編輯。
type Playlist struct {
	ID, Name, Owner string
	Editable        bool
	Rows            []Row
	visibleAfter    int
}

// Write:一次寫入請求(給測試斷言送了什麼、幾次)。
type Write struct {
	Endpoint string
	Body     map[string]any
}

type Server struct {
	t    testing.TB
	srv  *httptest.Server
	mu   sync.Mutex
	seq  int
	list int // 列表被打了幾次(新清單的可見時機)

	Account struct{ Name, Handle, ChannelID string }
	catalog map[string]Track
	lists   []*Playlist
	writes  []Write

	// ListLag:新建的清單要再列表幾次才出現(真平台約 3 s;0 = 立刻)。PageSize:清單列每頁幾列(測 continuation 用小值);
	// GridPageSize:清單列表每頁幾格。
	ListLag, PageSize, GridPageSize int
}

// New:起一個假伺服器,帳號 Someone / @someone / UCtestchannel…(跟 provider 套件的 fixture 一致)。
func New(t testing.TB) *Server {
	s := &Server{t: t, catalog: map[string]Track{}, PageSize: 100, GridPageSize: 100}
	s.Account.Name, s.Account.Handle, s.Account.ChannelID = "Someone", "@someone", "UCtestchannel000000000000"
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *Server) URL() string          { return s.srv.URL }
func (s *Server) Client() *http.Client { return s.srv.Client() }

// AddTrack:放進目錄(search 找得到)。
func (s *Server) AddTrack(tr Track) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catalog[tr.ID] = tr
}

// AddPlaylist:帳號自己的清單(可編輯);ids 不在目錄裡也照放(清單可以含目錄沒有的影片)。
func (s *Server) AddPlaylist(id, name string, ids ...string) *Playlist {
	return s.addList(id, name, s.Account.ChannelID, true, ids...)
}

// AddForeignPlaylist:別人的清單(存進資料庫的、或 LM / RD… 自動清單):列出來、不可編輯。owner 空 = 自動清單(副標沒有擁有者)。
func (s *Server) AddForeignPlaylist(id, name, owner string, ids ...string) *Playlist {
	return s.addList(id, name, owner, false, ids...)
}

func (s *Server) addList(id, name, owner string, editable bool, ids ...string) *Playlist {
	s.mu.Lock()
	defer s.mu.Unlock()
	pl := &Playlist{ID: id, Name: name, Owner: owner, Editable: editable}
	for _, v := range ids {
		pl.Rows = append(pl.Rows, Row{VideoID: v, SetVideoID: s.newSetID()})
	}
	s.lists = append(s.lists, pl)
	return pl
}

func (s *Server) newSetID() string { s.seq++; return fmt.Sprintf("SET%013d", s.seq) }

// Rows:清單現在的 videoId 序列(測試斷言用);沒有這份清單回 nil。
func (s *Server) Rows(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pl := s.find(id); pl != nil {
		out := make([]string, len(pl.Rows))
		for i, r := range pl.Rows {
			out[i] = r.VideoID
		}
		return out
	}
	return nil
}

// Name:清單現在的名字。
func (s *Server) Name(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pl := s.find(id); pl != nil {
		return pl.Name
	}
	return ""
}

// Writes:到目前為止的寫入請求。
func (s *Server) Writes() []Write {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Write(nil), s.writes...)
}

// Created:playlist/create 建出來的清單 id(照順序)。
func (s *Server) Created() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, pl := range s.lists {
		if strings.HasPrefix(pl.ID, "PLnew") {
			out = append(out, pl.ID)
		}
	}
	return out
}

func (s *Server) find(id string) *Playlist {
	for _, pl := range s.lists {
		if pl.ID == id {
			return pl
		}
	}
	return nil
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !strings.HasPrefix(r.Header.Get("Authorization"), "SAPISIDHASH ") || r.Header.Get("Cookie") == "" || r.Header.Get("X-Origin") != "https://music.youtube.com" {
		http.Error(w, "missing auth headers", http.StatusUnauthorized)
		return
	}
	ep := strings.TrimPrefix(r.URL.Path, "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	var resp any
	switch {
	case ep == "account/account_menu":
		resp = s.accountMenu()
	case ep == "browse" && r.URL.Query().Get("ctoken") != "":
		off, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Query().Get("ctoken"), "grid:"))
		resp = map[string]any{"continuationContents": map[string]any{"gridContinuation": s.grid(off)}}
	case ep == "browse" && body["browseId"] == "FEmusic_liked_playlists":
		s.list++
		resp = map[string]any{"contents": map[string]any{"singleColumnBrowseResultsRenderer": map[string]any{"tabs": []any{map[string]any{"tabRenderer": map[string]any{"content": map[string]any{"sectionListRenderer": map[string]any{"contents": []any{map[string]any{"gridRenderer": s.grid(0)}}}}}}}}}}
	case ep == "browse" && body["continuation"] != nil:
		id, off, _ := strings.Cut(strings.TrimPrefix(body["continuation"].(string), "rows:"), ":")
		o, _ := strconv.Atoi(off)
		pl := s.find(id)
		if pl == nil {
			http.Error(w, "no such playlist", http.StatusNotFound)
			return
		}
		items, token := s.rowsPage(pl, o)
		if token != "" {
			items = append(items, map[string]any{"continuationItemRenderer": map[string]any{"continuationEndpoint": map[string]any{"continuationCommand": map[string]any{"token": token}}}})
		}
		resp = map[string]any{"onResponseReceivedActions": []any{map[string]any{"appendContinuationItemsAction": map[string]any{"continuationItems": items}}}}
	case ep == "browse":
		bid, _ := body["browseId"].(string)
		pl := s.find(strings.TrimPrefix(bid, "VL"))
		if pl == nil || !strings.HasPrefix(bid, "VL") {
			resp = map[string]any{"responseContext": map[string]any{}} // 真平台對不存在的清單也是 200,只是沒有清單
			break
		}
		items, token := s.rowsPage(pl, 0)
		if token != "" {
			items = append(items, map[string]any{"continuationItemRenderer": map[string]any{"continuationEndpoint": map[string]any{"continuationCommand": map[string]any{"token": token}}}})
		}
		header := map[string]any{"musicResponsiveHeaderRenderer": map[string]any{"title": runs(pl.Name)}}
		if pl.Editable {
			header["musicEditablePlaylistDetailHeaderRenderer"] = map[string]any{"header": map[string]any{"musicResponsiveHeaderRenderer": map[string]any{"title": runs(pl.Name)}}}
		}
		resp = map[string]any{"contents": map[string]any{"twoColumnBrowseResultsRenderer": map[string]any{
			"tabs":              []any{map[string]any{"tabRenderer": map[string]any{"content": map[string]any{"sectionListRenderer": map[string]any{"contents": []any{header}}}}}},
			"secondaryContents": map[string]any{"sectionListRenderer": map[string]any{"contents": []any{map[string]any{"musicPlaylistShelfRenderer": map[string]any{"contents": items}}}}},
		}}}
	case ep == "search":
		resp = s.search(body["query"].(string))
	case ep == "next":
		id, _ := body["videoId"].(string)
		resp = s.next(id)
	case ep == "playlist/create":
		s.writes = append(s.writes, Write{Endpoint: ep, Body: body})
		s.seq++
		id := fmt.Sprintf("PLnew%d", s.seq)
		title, _ := body["title"].(string)
		pl := &Playlist{ID: id, Name: title, Owner: s.Account.ChannelID, Editable: true, visibleAfter: s.list + s.ListLag}
		s.lists = append(s.lists, pl)
		resp = map[string]any{"playlistId": id}
	case ep == "browse/edit_playlist":
		s.writes = append(s.writes, Write{Endpoint: ep, Body: body})
		code, r := s.edit(body)
		if code != 200 {
			http.Error(w, "bad request", code)
			return
		}
		resp = r
	case ep == "playlist/delete":
		s.t.Errorf("youtubetest: playlist/delete must never be sent (decision 60), got %v", body) // 字串字面不用中文:i18n 的守門測試也掃這個套件
		http.Error(w, "never", http.StatusForbidden)
		return
	default:
		http.Error(w, "unexpected endpoint "+ep, http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func runs(texts ...string) map[string]any {
	var rs []any
	for _, t := range texts {
		rs = append(rs, map[string]any{"text": t})
	}
	return map[string]any{"runs": rs}
}

func browse(id, pageType string) map[string]any {
	return map[string]any{"browseEndpoint": map[string]any{"browseId": id, "browseEndpointContextSupportedConfigs": map[string]any{"browseEndpointContextMusicConfig": map[string]any{"pageType": pageType}}}}
}

func (s *Server) accountMenu() any {
	link := map[string]any{"compactLinkRenderer": map[string]any{"navigationEndpoint": browse(s.Account.ChannelID, "MUSIC_PAGE_TYPE_USER_CHANNEL")}}
	header := map[string]any{"activeAccountHeaderRenderer": map[string]any{"accountName": runs(s.Account.Name), "channelHandle": runs(s.Account.Handle)}}
	return map[string]any{"actions": []any{map[string]any{"openPopupAction": map[string]any{"popup": map[string]any{"multiPageMenuRenderer": map[string]any{
		"header": header, "sections": []any{map[string]any{"multiPageMenuSectionRenderer": map[string]any{"items": []any{link, map[string]any{"compactLinkRenderer": map[string]any{"navigationEndpoint": map[string]any{"browseEndpoint": map[string]any{"browseId": "FEmusic_history"}}}}}}}}}}}}}}
}

// grid:清單列表(第 0 格是「新增清單」按鈕、沒有 browseId);舊式 continuation。visibleAfter 還沒到的新清單不列。
func (s *Server) grid(off int) map[string]any {
	var visible []*Playlist
	for _, pl := range s.lists {
		if pl.visibleAfter <= s.list {
			visible = append(visible, pl)
		}
	}
	items := []any{}
	if off == 0 {
		items = append(items, map[string]any{"musicTwoRowItemRenderer": map[string]any{"title": runs("New playlist")}})
	}
	end := min(off+s.GridPageSize, len(visible))
	for _, pl := range visible[off:end] {
		var sub []any
		if pl.Owner == "" {
			sub = []any{map[string]any{"text": "Auto playlist"}}
		} else {
			sub = []any{map[string]any{"text": s.ownerName(pl.Owner), "navigationEndpoint": browse(pl.Owner, "MUSIC_PAGE_TYPE_USER_CHANNEL")}, map[string]any{"text": " • "}, map[string]any{"text": strconv.Itoa(len(pl.Rows)) + " tracks"}}
		}
		items = append(items, map[string]any{"musicTwoRowItemRenderer": map[string]any{"title": runs(pl.Name), "subtitle": map[string]any{"runs": sub}, "navigationEndpoint": browse("VL"+pl.ID, "MUSIC_PAGE_TYPE_PLAYLIST")}})
	}
	g := map[string]any{"items": items}
	if end < len(visible) {
		g["continuations"] = []any{map[string]any{"nextContinuationData": map[string]any{"continuation": "grid:" + strconv.Itoa(end)}}}
	}
	return g
}

func (s *Server) ownerName(channel string) string {
	if channel == s.Account.ChannelID {
		return s.Account.Name
	}
	return "Other Person"
}

func (s *Server) rowsPage(pl *Playlist, off int) (items []any, token string) {
	items = []any{}
	end := min(off+s.PageSize, len(pl.Rows))
	for _, row := range pl.Rows[off:end] {
		items = append(items, map[string]any{"musicResponsiveListItemRenderer": s.rowRenderer(row.VideoID, row.SetVideoID)})
	}
	if end < len(pl.Rows) {
		token = "rows:" + pl.ID + ":" + strconv.Itoa(end)
	}
	return items, token
}

// rowRenderer:清單列 / 搜尋結果共用的 musicResponsiveListItemRenderer(標題在 flexColumns[0] 帶 watchEndpoint、歌手 flexColumns[1]
// 帶 UC… 端點、專輯 flexColumns[2] 帶 MPREb… 端點、時長在 fixedColumns)。目錄沒有的影片給個占位資料。
func (s *Server) rowRenderer(videoID, setVideoID string) map[string]any {
	tr, ok := s.catalog[videoID]
	if !ok {
		tr = Track{ID: videoID, Title: "Song " + videoID, Artist: "Artist", Album: "Album", DurationMS: 200000}
	}
	pid := map[string]any{"videoId": videoID}
	if setVideoID != "" {
		pid["playlistSetVideoId"] = setVideoID
	}
	title := map[string]any{"text": tr.Title, "navigationEndpoint": map[string]any{"watchEndpoint": map[string]any{"videoId": videoID, "watchEndpointMusicSupportedConfigs": map[string]any{"watchEndpointMusicConfig": map[string]any{"musicVideoType": "MUSIC_VIDEO_TYPE_ATV"}}}}}
	col := func(rs ...any) map[string]any {
		return map[string]any{"musicResponsiveListItemFlexColumnRenderer": map[string]any{"text": map[string]any{"runs": rs}}}
	}
	return map[string]any{
		"playlistItemData": pid,
		"flexColumns": []any{col(title),
			col(map[string]any{"text": tr.Artist, "navigationEndpoint": browse("UCartist"+videoID, "MUSIC_PAGE_TYPE_ARTIST")}),
			col(map[string]any{"text": tr.Album, "navigationEndpoint": browse("MPREb_"+videoID, "MUSIC_PAGE_TYPE_ALBUM")})},
		"fixedColumns": []any{map[string]any{"musicResponsiveListItemFixedColumnRenderer": map[string]any{"text": runs(duration(tr.DurationMS))}}},
	}
}

func duration(ms int) string { return fmt.Sprintf("%d:%02d", ms/60000, ms%60000/1000) }

func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// search:目錄裡歌名出現在查詢字裡(或查詢字出現在歌名裡)的都回,照 id 順序。兩邊都先正規化(小寫、非字母數字變空白):
// resolve 的查詢字是 Norm 過的(「song-a」→「song a」),跟真平台一樣不在乎標點。
func (s *Server) search(q string) any {
	q = normalize(q)
	var rows []any
	for _, tr := range s.catalogOrdered() {
		title := normalize(tr.Title)
		if title != "" && (strings.Contains(q, title) || strings.Contains(title, q)) {
			rows = append(rows, map[string]any{"musicResponsiveListItemRenderer": s.searchRow(tr)})
		}
	}
	return map[string]any{"contents": map[string]any{"tabbedSearchResultsRenderer": map[string]any{"tabs": []any{map[string]any{"tabRenderer": map[string]any{"content": map[string]any{"sectionListRenderer": map[string]any{"contents": []any{map[string]any{"musicShelfRenderer": map[string]any{"title": runs("Songs"), "contents": rows}}}}}}}}}}}
}

func (s *Server) catalogOrdered() []Track {
	var out []Track
	for _, tr := range s.catalog {
		out = append(out, tr)
	}
	// map 順序隨機:照 id 排,測試才穩
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// searchRow:搜尋結果的一列(歌手 • 專輯 • 時長都在 flexColumns[1] 的 runs 裡,沒有 setVideoId)。
func (s *Server) searchRow(tr Track) map[string]any {
	r := s.rowRenderer(tr.ID, "")
	r["flexColumns"] = []any{r["flexColumns"].([]any)[0], map[string]any{"musicResponsiveListItemFlexColumnRenderer": map[string]any{"text": map[string]any{"runs": []any{
		map[string]any{"text": tr.Artist, "navigationEndpoint": browse("UCartist"+tr.ID, "MUSIC_PAGE_TYPE_ARTIST")}, map[string]any{"text": " • "},
		map[string]any{"text": tr.Album, "navigationEndpoint": browse("MPREb_"+tr.ID, "MUSIC_PAGE_TYPE_ALBUM")}, map[string]any{"text": " • "},
		map[string]any{"text": duration(tr.DurationMS)}}}}}}
	delete(r, "fixedColumns")
	return r
}

func (s *Server) next(id string) any {
	tr, ok := s.catalog[id]
	if !ok {
		return map[string]any{"responseContext": map[string]any{}}
	}
	item := map[string]any{"playlistPanelVideoRenderer": map[string]any{"videoId": id, "title": runs(tr.Title), "lengthText": runs(duration(tr.DurationMS)),
		"longBylineText": map[string]any{"runs": []any{map[string]any{"text": tr.Artist, "navigationEndpoint": browse("UCartist"+id, "MUSIC_PAGE_TYPE_ARTIST")}, map[string]any{"text": " • "}, map[string]any{"text": tr.Album, "navigationEndpoint": browse("MPREb_"+id, "MUSIC_PAGE_TYPE_ALBUM")}, map[string]any{"text": " • "}, map[string]any{"text": "2020"}}}}}
	return map[string]any{"contents": map[string]any{"singleColumnMusicWatchNextResultsRenderer": map[string]any{"tabbedRenderer": map[string]any{"watchNextTabbedResultsRenderer": map[string]any{"tabs": []any{map[string]any{"tabRenderer": map[string]any{"content": map[string]any{"musicQueueRenderer": map[string]any{"content": map[string]any{"playlistPanelRenderer": map[string]any{"contents": []any{map[string]any{"playlistPanelVideoWrapperRenderer": map[string]any{"primaryRenderer": item}}}}}}}}}}}}}}}
}

// edit:整包先驗證再套用(真平台:夾一個壞 id 是 HTTP 400、零變動)。videoId 必須在目錄或已在某份清單裡;REMOVE 的 setVideoId 必須存在;
// 不帶 dedupeOption 的重複 ADD → STATUS_FAILED(零變動);不可編輯的清單 → STATUS_FAILED。
func (s *Server) edit(body map[string]any) (int, any) {
	id, _ := body["playlistId"].(string)
	pl := s.find(id)
	if pl == nil {
		return 400, nil
	}
	actions, _ := body["actions"].([]any)
	next := append([]Row(nil), pl.Rows...)
	name := pl.Name
	var results []any
	if !pl.Editable {
		return 200, map[string]any{"status": "STATUS_FAILED"}
	}
	for _, a := range actions {
		act, _ := a.(map[string]any)
		switch act["action"] {
		case "ACTION_ADD_VIDEO":
			v, _ := act["addedVideoId"].(string)
			if !s.known(v) {
				return 400, nil
			}
			if act["dedupeOption"] != "DEDUPE_OPTION_SKIP" {
				for _, r := range next {
					if r.VideoID == v {
						return 200, map[string]any{"status": "STATUS_FAILED"}
					}
				}
			}
			row := Row{VideoID: v, SetVideoID: s.newSetID()}
			next = append(next, row)
			results = append(results, map[string]any{"playlistEditVideoAddedResultData": map[string]any{"setVideoId": row.SetVideoID, "videoId": v}})
		case "ACTION_REMOVE_VIDEO":
			set, _ := act["setVideoId"].(string)
			i := -1
			for j, r := range next {
				if r.SetVideoID == set {
					i = j
					break
				}
			}
			if i < 0 {
				return 400, nil
			}
			next = append(next[:i], next[i+1:]...)
		case "ACTION_SET_PLAYLIST_NAME":
			name, _ = act["playlistName"].(string)
		default:
			return 400, nil
		}
	}
	pl.Rows, pl.Name = next, name
	return 200, map[string]any{"status": "STATUS_SUCCEEDED", "playlistEditResults": results}
}

func (s *Server) known(v string) bool {
	if _, ok := s.catalog[v]; ok {
		return true
	}
	for _, pl := range s.lists {
		for _, r := range pl.Rows {
			if r.VideoID == v {
				return true
			}
		}
	}
	return false
}
