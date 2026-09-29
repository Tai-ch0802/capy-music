package youtube

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	ytauth "github.com/Tai-ch0802/capy-music/internal/auth/youtube"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// Provider:T1 是讀端(搜尋、清單列表、清單內容、單曲)+ 帳號範圍的清單 id;寫端(決策 60 的整批取代)在 T2。
//
// 清單 id = <channel_id>/<playlistId>(決策 60,照 local 的 <device_id>/<檔名>):YouTube Music 帳號可以跟 Drive 帳號不同、也可以換,
// 換了帳號之後舊帳號的連結是 Foreign——pull / push / sync 只跳過,不當 gone、不 unlink(pull.go 的 foreignLink)。track id 不帶前綴。
type Provider struct {
	c         *Client
	channelID string
}

var (
	_ provider.Provider       = (*Provider)(nil)
	_ provider.Searcher       = (*Provider)(nil)
	_ provider.TrackGetter    = (*Provider)(nil)
	_ provider.PlaylistReader = (*Provider)(nil)
	_ provider.DeviceScoped   = (*Provider)(nil)
)

// New:channelID 是登入時 account_menu 回的頻道 id(config youtube_account.channel_id)。
func New(hc *http.Client, base string, h ytauth.Headers, lang, channelID string) *Provider {
	return &Provider{c: NewClient(hc, base, h, lang), channelID: channelID}
}

func (p *Provider) ID() string          { return "youtube" }
func (p *Provider) DisplayName() string { return i18n.T("youtube.display_name") }
func (p *Provider) ChannelID() string   { return p.channelID }

// Caps:沒有 ISRC(平台不給)、沒有播放、沒有 ArtistSearch(決策 60 Q8);CapDeviceBound 的範圍是帳號;寫端四個位元 + 建清單(T2,write.go)。
func (p *Provider) Caps() provider.Capability {
	return provider.CapSearch | provider.CapPlaylistRead | provider.CapDeviceBound |
		provider.CapPlaylistCreate | provider.CapPlaylistAppend | provider.CapPlaylistRemove | provider.CapPlaylistReorder | provider.CapPlaylistRename
}

func (p *Provider) Health(ctx context.Context) error {
	_, err := p.c.AccountInfo(ctx)
	return err
}

// AccountInfo:登入時與 doctor 用。
func (p *Provider) AccountInfo(ctx context.Context) (Account, error) { return p.c.AccountInfo(ctx) }

// Foreign:不是這個帳號的清單 id。
func (p *Provider) Foreign(id string) bool { return !strings.HasPrefix(id, p.channelID+"/") }

func (p *Provider) idOf(playlistID string) string { return p.channelID + "/" + playlistID }

func (p *Provider) own(id string) (string, error) {
	if p.Foreign(id) {
		return "", i18n.Errorf("youtube.err.foreign_id", "id", id)
	}
	return strings.TrimPrefix(id, p.channelID+"/"), nil
}

func trackURL(videoID string) string { return "https://music.youtube.com/watch?v=" + videoID }

func toTrack(r row) provider.Track {
	raw, _ := json.Marshal(r.raw)
	return provider.Track{
		ProviderID: r.videoID, Title: r.title, Artists: r.artists, Album: r.album, DurationMS: r.durationMS, Explicit: r.explicit,
		URL: trackURL(r.videoID), ReleaseDate: r.year, Raw: raw,
	}
}

// Search:一頁(約 20 筆)的「歌曲」filter;Limit 超過一頁就截(決策 60 Q9,不翻頁)。
func (p *Provider) Search(ctx context.Context, q provider.Query) ([]provider.Track, error) {
	root, err := p.c.post(ctx, "search", map[string]any{"query": q.Text, "params": songsParams}, "")
	if err != nil {
		return nil, err
	}
	var out []provider.Track
	for _, r := range parseSearch(root) {
		if r.videoID == "" {
			continue
		}
		out = append(out, toTrack(r))
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

// GetTrack:next 端點的 watch 清單第一項就是這首(player 端點要 signatureTimestamp,重)。
func (p *Provider) GetTrack(ctx context.Context, id string) (provider.Track, error) {
	root, err := p.c.post(ctx, "next", map[string]any{"videoId": id, "isAudioOnly": true, "tunerSettingValue": "AUTOMIX_SETTING_NORMAL", "enablePersistentPlaylistPanel": true}, "")
	if err != nil {
		return provider.Track{}, err
	}
	items := root.find("playlistPanelVideoRenderer")
	if len(items) == 0 {
		return provider.Track{}, provider.ErrNotFound
	}
	r := parsePanelItem(items[0])
	if r.videoID != id {
		return provider.Track{}, provider.ErrNotFound
	}
	return toTrack(r), nil
}

// ListPlaylists:FEmusic_liked_playlists 全部續頁(舊式 continuation:ctoken / continuation / type=next 查詢參數,
// 回 continuationContents.gridContinuation;2026-09-29 真帳號驗過 69 份)。自動清單(LM 喜歡的音樂、RD… 電台 / 節目)與別人的
// (副標的擁有者頻道不是自己)標 Unwritable:plan 階段就跳過,同 Apple 的 canEdit:false。
func (p *Provider) ListPlaylists(ctx context.Context) ([]provider.PlaylistRef, error) {
	var refs []provider.PlaylistRef
	body, q := map[string]any{"browseId": "FEmusic_liked_playlists"}, ""
	for page := 0; ; page++ {
		if page >= maxListPages { // 靜默截斷會讓沒列到的已連結清單被 pull 當成 gone 而 unlink(PR #117 review 第 4 點):寧可整輪失敗
			return nil, i18n.Errorf("youtube.err.too_many_pages", "what", "FEmusic_liked_playlists", "pages", maxListPages)
		}
		root, err := p.c.post(ctx, "browse", body, q)
		if err != nil {
			return nil, err
		}
		items, token, hasGrid := parseLibraryGrid(root)
		if !hasGrid { // 連 grid 的結構都沒有 = 版面變了;有結構但零項目(最後一頁)照常結束
			return nil, i18n.Errorf("youtube.err.bad_continuation", "what", "FEmusic_liked_playlists", "page", page)
		}
		for _, it := range items {
			ref := provider.PlaylistRef{ID: p.idOf(it.playlistID), Name: it.title, Owner: it.ownerName, Total: it.count}
			switch {
			case it.playlistID == "LM" || strings.HasPrefix(it.playlistID, "RD"):
				ref.Unwritable = i18n.T("youtube.unwritable.auto")
			case it.ownerChannel != p.channelID:
				ref.Unwritable = i18n.T("youtube.unwritable.not_owned", "owner", it.ownerName)
			}
			refs = append(refs, ref)
		}
		if token == "" {
			break
		}
		body, q = map[string]any{}, "ctoken="+token+"&continuation="+token+"&type=next" // token 原樣接上(見 post 的註解)
	}
	return refs, nil
}

// playlistPageRetries / playlistPageWait:剛寫完立刻讀,曾回過一頁沒有列也沒有 header 的東西(2026-09-29 探測 ADD 466 首後);
// 等一下重讀就正常。空清單有 header、只是沒有列,不會被當成這種情況。maxListPages / maxItemPages:續頁上限,到了回錯不截斷。測試替換點。
var (
	playlistPageRetries = 3
	playlistPageWait    = 2 * time.Second
	maxListPages        = 50
	maxItemPages        = 200
)

// GetPlaylistItems:VL<id> + 續頁(新式 continuationItemRenderer:token 放 body 的 continuation)。沒有 videoId 的列丟掉
// (下架 / 刪除;寫端重讀對齊時用同一條規則)。
func (p *Provider) GetPlaylistItems(ctx context.Context, id string) ([]provider.Track, error) {
	rows, _, err := p.playlistRows(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]provider.Track, 0, len(rows))
	for _, r := range rows {
		out = append(out, toTrack(r))
	}
	return out, nil
}

// playlistMeta:第一頁 header 給寫端的訊號——可編輯(musicEditablePlaylistDetailHeaderRenderer)與它子樹裡的擁有者頻道 id。
type playlistMeta struct {
	editable bool
	owners   []string
}

// playlistRows:清單的列(含 setVideoId,寫端的 REMOVE 要用)與第一頁 header 的訊號;已經濾掉沒有 videoId 的(寫端重讀對齊時用同一條規則;
// 這些列不會被 REMOVE,整批取代後它們會集中到清單最前面——capy 看不到、使用者在 app 裡看得到,README 有寫)。
func (p *Provider) playlistRows(ctx context.Context, id string) ([]row, playlistMeta, error) {
	plid, err := p.own(id)
	if err != nil {
		return nil, playlistMeta{}, err
	}
	var first playlistPage
	for attempt := 0; ; attempt++ {
		root, err := p.c.post(ctx, "browse", map[string]any{"browseId": "VL" + plid}, "")
		if err != nil {
			return nil, playlistMeta{}, err
		}
		first = parsePlaylistPage(root)
		if first.hasHeader || len(first.rows) > 0 {
			break
		}
		if attempt >= playlistPageRetries {
			return nil, playlistMeta{}, i18n.Errorf("youtube.err.unexpected_page", "id", plid)
		}
		select {
		case <-ctx.Done():
			return nil, playlistMeta{}, ctx.Err()
		case <-time.After(playlistPageWait):
		}
	}
	rows, token := first.rows, first.token
	for page := 1; token != ""; page++ {
		if page > maxItemPages { // 截斷的清單會被觀測成一堆 remove、整批取代更會拿它對齊:寧可失敗
			return nil, playlistMeta{}, i18n.Errorf("youtube.err.too_many_pages", "what", "VL"+plid, "pages", maxItemPages)
		}
		root, err := p.c.post(ctx, "browse", map[string]any{"continuation": token}, "")
		if err != nil {
			return nil, playlistMeta{}, err
		}
		next := parsePlaylistPage(root)
		if !next.hasShape { // 連續頁的結構都沒有 = 版面變了;有結構但零列照常結束
			return nil, playlistMeta{}, i18n.Errorf("youtube.err.bad_continuation", "what", "VL"+plid, "page", page)
		}
		rows, token = append(rows, next.rows...), next.token
	}
	kept := rows[:0]
	for _, r := range rows {
		if r.videoID != "" {
			kept = append(kept, r)
		}
	}
	return kept, playlistMeta{editable: first.editable, owners: first.owners}, nil
}
