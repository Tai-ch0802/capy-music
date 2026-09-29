package youtube

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// node:InnerTube 回應是 UI 樹(renderer 套 renderer),欄位靠路徑與端點型別辨認。這個薄包裝讓「路徑不在就零值」
// 與「整棵樹找某個 key」寫得短;所有 parser 都只認 renderer 名與 browseId 的前綴,不認任何會跟語系變的文字。
type node struct{ v any }

func (n node) ok() bool { return n.v != nil }

// get:沿 keys(string = 物件鍵、int = 陣列索引)往下走,任一步不在就回空 node。
func (n node) get(keys ...any) node {
	cur := n.v
	for _, k := range keys {
		switch kk := k.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return node{}
			}
			cur = m[kk]
		case int:
			a, ok := cur.([]any)
			if !ok || kk < 0 || kk >= len(a) {
				return node{}
			}
			cur = a[kk]
		}
	}
	return node{cur}
}

func (n node) str() string { s, _ := n.v.(string); return s }

func (n node) arr() []node {
	a, _ := n.v.([]any)
	out := make([]node, len(a))
	for i := range a {
		out[i] = node{a[i]}
	}
	return out
}

// find:深度優先找所有鍵名為 name 的值(找到的那一支不再往下找:一列不會包著另一列)。
func (n node) find(name string) []node {
	var out []node
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if hit, ok := t[name]; ok {
				out = append(out, node{hit})
				return
			}
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(n.v)
	return out
}

// text:runs 全部串起來(標題只有一個 run;副標可能好幾個)。
func (n node) text() string {
	var b strings.Builder
	for _, r := range n.get("runs").arr() {
		b.WriteString(r.get("text").str())
	}
	return b.String()
}

var (
	durationRe = regexp.MustCompile(`^(\d+):(\d\d)(?::(\d\d))?$`)
	digitsRe   = regexp.MustCompile(`\d[\d,]*`)
)

// parseDuration:「m:ss」/「h:mm:ss」→ 毫秒;不是就 0。
func parseDuration(s string) int {
	m := durationRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	if m[3] == "" {
		return (a*60 + b) * 1000
	}
	c, _ := strconv.Atoi(m[3])
	return ((a*60+b)*60 + c) * 1000
}

// row:一列曲目(清單裡的一列、搜尋結果的一筆、watch 清單的一項)。
type row struct {
	videoID, setVideoID, title, album, videoType, year string
	artists                                            []string
	durationMS                                         int
	explicit, grey                                     bool
	raw                                                any
}

const (
	pageTypeArtist  = "MUSIC_PAGE_TYPE_ARTIST"
	pageTypeChannel = "MUSIC_PAGE_TYPE_USER_CHANNEL"
	pageTypeAlbum   = "MUSIC_PAGE_TYPE_ALBUM"
	greyOut         = "MUSIC_ITEM_RENDERER_DISPLAY_POLICY_GREY_OUT"
	explicitBadge   = "MUSIC_EXPLICIT_BADGE"
)

// classifyRun:副標 / 欄位裡的一個 run 是歌手、專輯、時長還是年份。靠端點型別與 browseId 前綴(UC… 頻道、MPREb… 專輯)辨認;
// 純符號(「•」「、」「&」)一律是分隔。有字的純文字:同一欄的 runs 是「內容、分隔、內容、分隔…」交錯,分隔一定夾在兩個內容之間
// (奇數索引),所以有帶端點的 run 時看它的位置——不看長度(「林俊傑」「蘇打綠」這種 2–3 個字、沒有頻道頁的歌手不能被吃掉;
// 「和」「and」「y」這種有字的分隔跟語系走,不能列舉);整欄都沒有端點(下架的列、上傳的歌)照位置當歌手 / 專輯。
// 時長與四位數年份在任何欄都先認出來。
func classifyRun(r node, col, idx int, hasNav bool, out *row) {
	text := strings.TrimSpace(r.get("text").str())
	if text == "" {
		return
	}
	if strings.IndexFunc(text, func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) }) < 0 {
		return // 純符號(「•」「、」「&」「,」):任何欄都是分隔
	}
	browseID := r.get("navigationEndpoint", "browseEndpoint", "browseId").str()
	pageType := r.get("navigationEndpoint", "browseEndpoint", "browseEndpointContextSupportedConfigs", "browseEndpointContextMusicConfig", "pageType").str()
	switch {
	case pageType == pageTypeArtist || pageType == pageTypeChannel || strings.HasPrefix(browseID, "UC"):
		out.artists = append(out.artists, text)
	case pageType == pageTypeAlbum || strings.HasPrefix(browseID, "MPREb"):
		out.album = text
	case durationRe.MatchString(text):
		out.durationMS = parseDuration(text)
	case browseID == "" && len(text) == 4 && strings.Trim(text, "0123456789") == "":
		out.year = text
	case browseID != "":
		// 別種端點(電台、播放清單):不是我們要的欄位
	case hasNav && idx%2 == 1:
		// 交錯序列的奇數位:有字的分隔(「和」「and」「y」)
	case col <= 1:
		out.artists = append(out.artists, text)
	case out.album == "":
		out.album = text
	}
}

// hasBrowse:這一組 runs 裡有沒有帶 browseEndpoint 的(決定純文字 run 是分隔還是內容)。
func hasBrowse(runs []node) bool {
	for _, r := range runs {
		if r.get("navigationEndpoint", "browseEndpoint", "browseId").str() != "" {
			return true
		}
	}
	return false
}

// parseRow:musicResponsiveListItemRenderer(清單列與搜尋結果共用同一個 renderer)。videoId 在 playlistItemData(清單列與歌曲
// 搜尋結果都有),沒有就看標題的 watchEndpoint、再看播放鈕;setVideoId 只有清單列有(同一首兩列各自不同)。
func parseRow(n node) row {
	out := row{raw: n.v}
	out.videoID = n.get("playlistItemData", "videoId").str()
	out.setVideoID = n.get("playlistItemData", "playlistSetVideoId").str()
	title := n.get("flexColumns", 0, "musicResponsiveListItemFlexColumnRenderer", "text", "runs", 0)
	out.title = strings.TrimSpace(title.get("text").str())
	watch := title.get("navigationEndpoint", "watchEndpoint")
	if !watch.ok() {
		watch = n.get("overlay", "musicItemThumbnailOverlayRenderer", "content", "musicPlayButtonRenderer", "playNavigationEndpoint", "watchEndpoint")
	}
	if out.videoID == "" {
		out.videoID = watch.get("videoId").str()
	}
	out.videoType = watch.get("watchEndpointMusicSupportedConfigs", "watchEndpointMusicConfig", "musicVideoType").str()
	for ci, col := range n.get("flexColumns").arr() {
		if ci == 0 {
			continue
		}
		runs := col.get("musicResponsiveListItemFlexColumnRenderer", "text", "runs").arr()
		nav := hasBrowse(runs)
		for i, r := range runs {
			classifyRun(r, ci, i, nav, &out)
		}
	}
	for _, col := range n.get("fixedColumns").arr() {
		if d := parseDuration(col.get("musicResponsiveListItemFixedColumnRenderer", "text").text()); d > 0 {
			out.durationMS = d
		}
	}
	for _, b := range n.get("badges").arr() {
		if b.get("musicInlineBadgeRenderer", "icon", "iconType").str() == explicitBadge {
			out.explicit = true
		}
	}
	out.grey = n.get("musicItemRendererDisplayPolicy").str() == greyOut
	return out
}

// parsePanelItem:playlistPanelVideoRenderer(next 端點的 watch 清單項目;GetTrack 用)。
func parsePanelItem(n node) row {
	out := row{raw: n.v, videoID: n.get("videoId").str(), title: strings.TrimSpace(n.get("title").text())}
	runs := n.get("longBylineText", "runs").arr()
	nav := hasBrowse(runs)
	for i, r := range runs {
		classifyRun(r, 1, i, nav, &out)
	}
	out.durationMS = parseDuration(n.get("lengthText").text())
	for _, b := range n.get("badges").arr() {
		if b.get("musicInlineBadgeRenderer", "icon", "iconType").str() == explicitBadge {
			out.explicit = true
		}
	}
	return out
}

// playlistPage:browse VL<id> 的第一頁或 continuation 頁。第一頁的列在 musicPlaylistShelfRenderer.contents(下面的「建議」是別的
// shelf,不會混進來),續頁的列在 appendContinuationItemsAction.continuationItems;續頁 token 在 continuationItemRenderer。
// hasHeader 用來分「空清單」與「回應不是清單頁」(2026-09-29 探測:剛寫完立刻讀曾回過沒有列的頁)。
type playlistPage struct {
	rows      []row
	token     string
	hasHeader bool
	hasShape  bool // 第一頁有 musicPlaylistShelfRenderer、續頁有 appendContinuationItemsAction:有結構但零列是清單完了,沒有結構才是版面變了
	editable  bool
	title     string
}

const continuationToken = "continuationItemRenderer"

func parsePlaylistPage(root node) playlistPage {
	var p playlistPage
	take := func(items []node) {
		for _, it := range items {
			if r := it.get("musicResponsiveListItemRenderer"); r.ok() {
				p.rows = append(p.rows, parseRow(r))
			}
			if tok := it.get(continuationToken, "continuationEndpoint", "continuationCommand", "token").str(); tok != "" {
				p.token = tok
			}
		}
	}
	for _, shelf := range root.find("musicPlaylistShelfRenderer") {
		p.hasShape = true
		take(shelf.get("contents").arr())
	}
	for _, action := range root.get("onResponseReceivedActions").arr() {
		if app := action.get("appendContinuationItemsAction"); app.ok() {
			p.hasShape = true
			take(app.get("continuationItems").arr())
		}
	}
	p.editable = len(root.find("musicEditablePlaylistDetailHeaderRenderer")) > 0
	for _, name := range []string{"musicResponsiveHeaderRenderer", "musicDetailHeaderRenderer"} {
		if hs := root.find(name); len(hs) > 0 {
			p.hasHeader = true
			if p.title == "" {
				p.title = strings.TrimSpace(hs[0].get("title").text())
			}
		}
	}
	p.hasHeader = p.hasHeader || p.editable
	return p
}

// gridItem:清單列表(FEmusic_liked_playlists)的一格。第 0 格「新增清單」沒有 browseId、不算;副標的第一個 run 是擁有者
// (有 UC… 的頻道端點才算擁有者;LM / RD… 這類自動清單沒有),最後一個 run 是曲數。
type gridItem struct {
	playlistID, title, ownerChannel, ownerName string
	count                                      int
}

// parseLibraryGrid:第一頁與 gridContinuation 都適用;舊式 continuation 在 continuations[0].nextContinuationData.continuation。
// hasGrid:回應裡有 grid 的結構(gridRenderer / gridContinuation)——有結構但零項目是清單完了,沒有結構才是版面變了。
func parseLibraryGrid(root node) (items []gridItem, token string, hasGrid bool) {
	hasGrid = len(root.find("gridRenderer")) > 0 || len(root.find("gridContinuation")) > 0
	for _, n := range root.find("musicTwoRowItemRenderer") {
		browseID := n.get("navigationEndpoint", "browseEndpoint", "browseId").str()
		if !strings.HasPrefix(browseID, "VL") {
			continue
		}
		it := gridItem{playlistID: strings.TrimPrefix(browseID, "VL"), title: strings.TrimSpace(n.get("title").text()), count: -1}
		for _, r := range n.get("subtitle", "runs").arr() {
			if id := r.get("navigationEndpoint", "browseEndpoint", "browseId").str(); strings.HasPrefix(id, "UC") {
				it.ownerChannel, it.ownerName = id, strings.TrimSpace(r.get("text").str())
				continue
			}
			if m := digitsRe.FindString(r.get("text").str()); m != "" {
				if c, err := strconv.Atoi(strings.ReplaceAll(m, ",", "")); err == nil {
					it.count = c
				}
			}
		}
		items = append(items, it)
	}
	if c := root.find("nextContinuationData"); len(c) > 0 {
		token = c[0].get("continuation").str()
	}
	return items, token, hasGrid
}

// parseSearch:歌曲 filter 的結果只有一個 musicShelfRenderer;還是把每個 shelf 的列都收(版面多一個 shelf 也不會漏)。
func parseSearch(root node) []row {
	var rows []row
	for _, shelf := range root.find("musicShelfRenderer") {
		for _, it := range shelf.get("contents").arr() {
			if r := it.get("musicResponsiveListItemRenderer"); r.ok() {
				rows = append(rows, parseRow(r))
			}
		}
	}
	return rows
}

// errNoAccount:回應裡沒有帳號(cookie 失效或抄到未登入的請求);errChannelID:選單裡找不到唯一的頻道 id。
var (
	errNoAccount = errors.New("no account in account_menu")
	errChannelID = errors.New("channel id not unique in account_menu")
)

// parseAccount:account_menu 的 activeAccountHeaderRenderer;頻道 id 走固定路徑
// actions[*].openPopupAction.popup.multiPageMenuRenderer.sections[*].multiPageMenuSectionRenderer.items[*].compactLinkRenderer
// 的 browseEndpoint(pageType MUSIC_PAGE_TYPE_USER_CHANNEL、UC… 開頭;2026-09-29 真回應就一個),照陣列順序、去重——它是所有清單 id 的前綴、
// 會寫進 Drive,不能靠 find 走 map 的隨機順序挑;不只一個或一個都沒有就回 errChannelID(PR #117 review 第 3 點)。
func parseAccount(root node) (Account, error) {
	hs := root.find("activeAccountHeaderRenderer")
	if len(hs) == 0 {
		return Account{}, errNoAccount
	}
	a := Account{Name: strings.TrimSpace(hs[0].get("accountName").text()), Handle: strings.TrimSpace(hs[0].get("channelHandle").text())}
	if a.Name == "" {
		return Account{}, errNoAccount
	}
	var ids []string
	for _, action := range root.get("actions").arr() {
		for _, sec := range action.get("openPopupAction", "popup", "multiPageMenuRenderer", "sections").arr() {
			for _, it := range sec.get("multiPageMenuSectionRenderer", "items").arr() {
				ep := it.get("compactLinkRenderer", "navigationEndpoint", "browseEndpoint")
				id := ep.get("browseId").str()
				pt := ep.get("browseEndpointContextSupportedConfigs", "browseEndpointContextMusicConfig", "pageType").str()
				if strings.HasPrefix(id, "UC") && (pt == "" || pt == pageTypeChannel) && !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
		}
	}
	if len(ids) != 1 {
		return a, fmt.Errorf("%w: %v", errChannelID, ids)
	}
	a.ChannelID = ids[0]
	return a, nil
}
