// Package spotify 實作 Spotify 的 Provider(spec §1.1 的 2026 API 現況)。
// client.go 是薄殼 REST:認證由外部注入的 *http.Client(oauth2)承擔。
package spotify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const DefaultAPIBase = "https://api.spotify.com/v1"

type Client struct {
	hc   *http.Client
	base string
}

func NewClient(hc *http.Client, base string) *Client {
	if base == "" {
		base = DefaultAPIBase
	}
	return &Client{hc: hc, base: base}
}

// apiError 承載 Spotify 的錯誤回應;Reason 用於 player 404 的 NO_ACTIVE_DEVICE 判定。
type apiError struct {
	Status  int
	Reason  string
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("spotify API %d %s %s", e.Status, e.Reason, e.Message)
}

// do 送出請求。429 依 Retry-After 退避重試;401 與 oauth2 refresh 失敗映射
// provider.ErrAuthExpired;其他 >=400 回 *apiError。out 非 nil 且非 204 時解 JSON。
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) (int, error) {
	for attempt := 0; ; attempt++ {
		u := c.base + path
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		var rd io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return 0, err
			}
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return 0, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			var rerr *oauth2.RetrieveError
			if errors.As(err, &rerr) {
				return 0, provider.ErrAuthExpired
			}
			return 0, err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			if err := provider.Backoff(ctx, resp, attempt); err != nil {
				var rl *provider.RateLimitError
				if errors.As(err, &rl) {
					return resp.StatusCode, &apiError{Status: resp.StatusCode, Message: rl.Message}
				}
				return 0, err // ctx 取消原樣透傳
			}
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			return resp.StatusCode, provider.ErrAuthExpired
		}
		if resp.StatusCode >= 400 {
			var eb struct {
				Error struct {
					Message string `json:"message"`
					Reason  string `json:"reason"`
				} `json:"error"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&eb)
			resp.Body.Close()
			return resp.StatusCode, &apiError{Status: resp.StatusCode, Reason: eb.Error.Reason, Message: eb.Error.Message}
		}

		status := resp.StatusCode
		if out == nil || status == http.StatusNoContent {
			resp.Body.Close()
			return status, nil
		}
		derr := json.NewDecoder(resp.Body).Decode(out)
		resp.Body.Close()
		return status, derr
	}
}

// ── JSON 映射 ──

type trackJSON struct {
	ID         string `json:"id"`
	URI        string `json:"uri"`
	IsLocal    bool   `json:"is_local"` // local file:id 是 null、uri 是 spotify:local:…,API 加不回去
	Name       string `json:"name"`
	DurationMS int    `json:"duration_ms"`
	Explicit   bool   `json:"explicit"`
	Album      struct {
		Name string `json:"name"`
	} `json:"album"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	ExternalIDs struct {
		ISRC string `json:"isrc"`
	} `json:"external_ids"`
}

func (t *trackJSON) toTrack() provider.Track {
	artists := make([]string, len(t.Artists))
	for i, a := range t.Artists {
		artists[i] = a.Name
	}
	id := t.ID
	if id == "" { // local file(與沒帶 additional_types 的 podcast episode)的 id 是 null:拿 uri 當 id,cid 才不會是空的 p:spotify:
		id = t.URI
	}
	return provider.Track{
		ProviderID: id,
		ISRC:       t.ExternalIDs.ISRC,
		Title:      t.Name,
		Artists:    artists,
		Album:      t.Album.Name,
		DurationMS: t.DurationMS,
		Explicit:   t.Explicit,
		Unpushable: t.IsLocal,
	}
}

type deviceJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	IsActive  bool   `json:"is_active"`
	VolumePct int    `json:"volume_percent"`
}

func (d *deviceJSON) toDevice() provider.Device {
	return provider.Device{ID: d.ID, Name: d.Name, Type: d.Type, Active: d.IsActive, VolumePct: d.VolumePct}
}

// ── search ──

const searchPageMax = 10 // spec §1.1:GET /search 單次上限 10

// SearchTracks 依 spec 上限分頁,取滿 limit 或結果耗盡為止。
func (c *Client) SearchTracks(ctx context.Context, text string, limit int) ([]provider.Track, error) {
	var out []provider.Track
	for offset := 0; len(out) < limit; {
		page := limit - len(out)
		if page > searchPageMax {
			page = searchPageMax
		}
		q := url.Values{
			"type":   {"track"},
			"q":      {text},
			"limit":  {strconv.Itoa(page)},
			"offset": {strconv.Itoa(offset)},
		}
		var resp struct {
			Tracks struct {
				Items []trackJSON `json:"items"`
				Total int         `json:"total"`
			} `json:"tracks"`
		}
		if _, err := c.do(ctx, http.MethodGet, "/search", q, nil, &resp); err != nil {
			return nil, err
		}
		for i := range resp.Tracks.Items {
			out = append(out, resp.Tracks.Items[i].toTrack())
		}
		offset += len(resp.Tracks.Items)
		if len(resp.Tracks.Items) < page || offset >= resp.Tracks.Total {
			break
		}
	}
	return out, nil
}

// LookupISRC:GET /search?q=isrc:<ISRC>&type=track。Spotify 的 isrc: 欄位查詢是精確比對,但同一 ISRC 可對到
// 單曲 / 專輯 / 合輯多個版本,原樣全部回傳(單頁 10 筆),消歧是 resolver 的事(spec §5.1)。
func (c *Client) LookupISRC(ctx context.Context, isrc string) ([]provider.Track, error) {
	n := provider.NormalizeISRC(isrc)
	if n == "" {
		return nil, fmt.Errorf("%w:%q", provider.ErrBadISRC, isrc)
	}
	return c.SearchTracks(ctx, "isrc:"+n, searchPageMax)
}

// GetTrack:GET /tracks/{id};404 → ErrNotFound。
func (c *Client) GetTrack(ctx context.Context, id string) (provider.Track, error) {
	var t trackJSON
	status, err := c.do(ctx, http.MethodGet, "/tracks/"+url.PathEscape(id), nil, nil, &t)
	if err != nil {
		if status == http.StatusNotFound {
			return provider.Track{}, fmt.Errorf("%w:曲目 %s", provider.ErrNotFound, id)
		}
		return provider.Track{}, err
	}
	return t.toTrack(), nil
}

// ── artists ──

type artistJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SearchArtists 只取一頁(挑選器最多列幾個);limit 超過單次上限就截到上限。
func (c *Client) SearchArtists(ctx context.Context, text string, limit int) ([]provider.Artist, error) {
	if limit <= 0 || limit > searchPageMax {
		limit = searchPageMax
	}
	q := url.Values{"type": {"artist"}, "q": {text}, "limit": {strconv.Itoa(limit)}}
	var resp struct {
		Artists struct {
			Items []artistJSON `json:"items"`
		} `json:"artists"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/search", q, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]provider.Artist, len(resp.Artists.Items))
	for i, a := range resp.Artists.Items {
		out[i] = provider.Artist{ProviderID: a.ID, Name: a.Name}
	}
	return out, nil
}

// ArtistTopTracks:先打 /artists/{id}/top-tracks(market=from_token)。開發模式 app 會被 403
// (2026-09-07 實測:from_token / TW / 不帶 / country= 全部 403,不是 market 問題),此時退回
// search q=artist:"<name>" type=track——Spotify 搜尋依熱門度排序,是「熱門歌曲」的可用近似。
func (c *Client) ArtistTopTracks(ctx context.Context, a provider.Artist) ([]provider.Track, error) {
	var resp struct {
		Tracks []trackJSON `json:"tracks"`
	}
	path := "/artists/" + url.PathEscape(a.ProviderID) + "/top-tracks"
	_, err := c.do(ctx, http.MethodGet, path, url.Values{"market": {"from_token"}}, nil, &resp)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusForbidden && a.Name != "" {
		// 留下痕跡:403 也可能是 scope 被撤或地區限制,不能讓人永遠只看到「播了一些歌」。
		fmt.Fprintf(provider.BackoffStderr, "Spotify:top-tracks 回 403(開發模式 app 拿不到),改用 artist:%q 搜尋近似\n", a.Name)
		// 引號包起來就是字面詞組(AND/OR/NOT 與 artist: 這類語法在引號內不解析),只需去掉名稱裡自己的引號。
		name := strings.ReplaceAll(a.Name, `"`, "")
		ts, err := c.SearchTracks(ctx, `artist:"`+name+`"`, searchPageMax)
		if err != nil {
			return nil, err
		}
		// 只留藝人欄真的含這個名字的曲目:同名藝人與翻唱帳號會混進搜尋結果,使用者挑的是具體那一個。
		out := ts[:0]
		for _, t := range ts {
			if slices.ContainsFunc(t.Artists, func(n string) bool { return strings.EqualFold(n, a.Name) || strings.EqualFold(n, name) }) {
				out = append(out, t)
			}
		}
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]provider.Track, len(resp.Tracks))
	for i := range resp.Tracks {
		out[i] = resp.Tracks[i].toTrack()
	}
	return out, nil
}

// ── player ──

// mapPlayerErr:player 端點的 404 + NO_ACTIVE_DEVICE 是語意,不是 URL 打錯。
func mapPlayerErr(err error) error {
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Reason == "NO_ACTIVE_DEVICE" {
		return provider.ErrNoActiveDevice
	}
	return err
}

func (c *Client) Devices(ctx context.Context) ([]provider.Device, error) {
	var resp struct {
		Devices []deviceJSON `json:"devices"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/me/player/devices", nil, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]provider.Device, len(resp.Devices))
	for i := range resp.Devices {
		out[i] = resp.Devices[i].toDevice()
	}
	return out, nil
}

// State 回傳目前播放狀態;204(無播放內容)回 (nil, nil)。
func (c *Client) State(ctx context.Context) (*provider.PlaybackState, error) {
	var resp struct {
		IsPlaying  bool       `json:"is_playing"`
		ProgressMS int        `json:"progress_ms"`
		Item       *trackJSON `json:"item"`
		Device     deviceJSON `json:"device"`
	}
	status, err := c.do(ctx, http.MethodGet, "/me/player", nil, nil, &resp)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	st := &provider.PlaybackState{Playing: resp.IsPlaying, ProgressMS: resp.ProgressMS, Device: resp.Device.toDevice()}
	if resp.Item != nil {
		tr := resp.Item.toTrack()
		st.Track = &tr
	}
	return st, nil
}

func deviceQuery(deviceID string) url.Values {
	if deviceID == "" {
		return nil
	}
	return url.Values{"device_id": {deviceID}}
}

// Play:uris 空 → 無 body(resume)。
func (c *Client) Play(ctx context.Context, uris []string, deviceID string) error {
	var body any
	if len(uris) > 0 {
		body = map[string]any{"uris": uris}
	}
	_, err := c.do(ctx, http.MethodPut, "/me/player/play", deviceQuery(deviceID), body, nil)
	return mapPlayerErr(err)
}

// PlayContext:以 context_uri(播放清單、專輯)播放。
func (c *Client) PlayContext(ctx context.Context, contextURI, deviceID string) error {
	body := map[string]any{"context_uri": contextURI}
	_, err := c.do(ctx, http.MethodPut, "/me/player/play", deviceQuery(deviceID), body, nil)
	return mapPlayerErr(err)
}

func (c *Client) Pause(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodPut, "/me/player/pause", nil, nil, nil)
	return mapPlayerErr(err)
}

func (c *Client) Next(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodPost, "/me/player/next", nil, nil, nil)
	return mapPlayerErr(err)
}

func (c *Client) Prev(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodPost, "/me/player/previous", nil, nil, nil)
	return mapPlayerErr(err)
}

// ── playlists ──
// 2026-02 改名:端點 /tracks→/items、欄位 tracks→items(spec §1.1)。內層形狀 spec 未載明,
// 雙鍵 decode 防衛;附錄 B-4 真實驗收確認後可簡化。

const playlistPageSize = 50

type playlistJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner struct {
		DisplayName string `json:"display_name"`
	} `json:"owner"`
	Items *struct {
		Total int `json:"total"`
	} `json:"items"`
	Tracks *struct {
		Total int `json:"total"`
	} `json:"tracks"`
}

func (p *playlistJSON) toRef() provider.PlaylistRef {
	total := 0
	switch {
	case p.Items != nil:
		total = p.Items.Total
	case p.Tracks != nil:
		total = p.Tracks.Total
	}
	return provider.PlaylistRef{ID: p.ID, Name: p.Name, Owner: p.Owner.DisplayName, Total: total}
}

func (c *Client) MyPlaylists(ctx context.Context) ([]provider.PlaylistRef, error) {
	var out []provider.PlaylistRef
	for offset := 0; ; {
		q := url.Values{"limit": {strconv.Itoa(playlistPageSize)}, "offset": {strconv.Itoa(offset)}}
		var resp struct {
			Items []playlistJSON `json:"items"`
			Total int            `json:"total"`
		}
		if _, err := c.do(ctx, http.MethodGet, "/me/playlists", q, nil, &resp); err != nil {
			return nil, err
		}
		for i := range resp.Items {
			out = append(out, resp.Items[i].toRef())
		}
		offset += len(resp.Items)
		if len(resp.Items) < playlistPageSize || offset >= resp.Total {
			return out, nil
		}
	}
}

func (c *Client) PlaylistItems(ctx context.Context, id string) ([]provider.Track, error) {
	var out []provider.Track
	for offset := 0; ; {
		q := url.Values{"limit": {strconv.Itoa(playlistPageSize)}, "offset": {strconv.Itoa(offset)}}
		var resp struct {
			Items []struct {
				Track *trackJSON `json:"track"` // 舊內層鍵
				Item  *trackJSON `json:"item"`  // 新內層鍵
			} `json:"items"`
			Total int `json:"total"`
		}
		_, err := c.do(ctx, http.MethodGet, "/playlists/"+url.PathEscape(id)+"/items", q, nil, &resp)
		if err != nil {
			var ae *apiError
			if errors.As(err, &ae) && ae.Status == http.StatusForbidden {
				return nil, provider.ErrRestricted
			}
			if errors.As(err, &ae) && ae.Status == http.StatusNotFound { // 清單已刪:T8 用「不在清單列表」當 gone,這裡只是讓錯誤可辨認
				return nil, fmt.Errorf("%w:清單 %s", provider.ErrNotFound, id)
			}
			return nil, err
		}
		for i := range resp.Items {
			tj := resp.Items[i].Item
			if tj == nil {
				tj = resp.Items[i].Track
			}
			if tj != nil {
				out = append(out, tj.toTrack())
			}
		}
		offset += len(resp.Items)
		if len(resp.Items) < playlistPageSize || offset >= resp.Total {
			return out, nil
		}
	}
}

// playlistWriteBatch:PUT / POST /playlists/{id}/items 單次上限 100 首。
const playlistWriteBatch = 100

// ApplyOps:整批取代(spec 決策 28)。ops 套在 current 上得到目標序列;變了就 PUT 前 100 首(空序列 = 清空)、
// 其後每 100 首 POST 到 position;改名走 PUT /playlists/{id},在 items 之前(先做便宜的)。Spotify 全部 Kind 都支援,
// skipped 恆為 nil。寫入端點沿用讀取的 /items 路徑(spec §1.1 的改名);真帳號尚未驗證寫入也吃 /items(計畫 T1)。
// 送出前先驗整份 uris:空 id、或不是 track / episode 的 uri(local file 加不回去,靜默丟掉就是沒過閾值的刪除)→ 一個請求都不送。
// 不是原子的:PUT 之後的 POST 失敗,平台停在被截短的狀態 → 回 *provider.PartialWriteError,呼叫端要讓使用者知道、
// 把 base 推到現況、提示重跑補回(§6.5.2 規則 7)。
func (c *Client) ApplyOps(ctx context.Context, id string, current []string, ops []provider.PlaylistOp) ([]provider.PlaylistOp, error) {
	want, name, err := provider.ApplyPlaylistOps(current, ops)
	if err != nil {
		return nil, err
	}
	itemsChanged := !slices.Equal(want, current)
	var uris []string
	if itemsChanged { // 只改名、或 ops 互相抵銷:current 裡有 local file 也不算錯(沒有要送 items)
		if uris, err = trackURIs(want); err != nil {
			return nil, err
		}
	}
	renamed := false
	if name != "" {
		if _, err := c.do(ctx, http.MethodPut, "/playlists/"+url.PathEscape(id), nil, map[string]string{"name": name}, nil); err != nil {
			return nil, writeErr(id, err)
		}
		renamed = true
	}
	if !itemsChanged {
		return nil, nil
	}
	path := "/playlists/" + url.PathEscape(id) + "/items"
	head := min(len(uris), playlistWriteBatch)
	if _, err := c.do(ctx, http.MethodPut, path, nil, map[string]any{"uris": uris[:head]}, nil); err != nil {
		return nil, writeErr(id, err)
	}
	for pos := head; pos < len(uris); pos += playlistWriteBatch {
		end := min(pos+playlistWriteBatch, len(uris))
		if _, err := c.do(ctx, http.MethodPost, path, nil, map[string]any{"uris": uris[pos:end], "position": pos}, nil); err != nil {
			return nil, &provider.PartialWriteError{PlaylistID: id, Written: pos, Want: len(uris), Renamed: renamed, Err: writeErr(id, err)}
		}
	}
	return nil, nil
}

// trackURIs:want 的每個 id 變成 uri,並擋掉推不出去的——空 id(呼叫端漏填、或 id 是 null 的項目)與非 track / episode
// 的 uri(spotify:local:… 加不回去)。Spotify 的 add / replace 只吃 spotify:track: 與 spotify:episode:。
func trackURIs(want []string) ([]string, error) {
	uris := make([]string, len(want))
	var bad []string
	for i, tid := range want {
		uris[i] = tid
		if !strings.HasPrefix(tid, "spotify:") {
			uris[i] = "spotify:track:" + tid
		}
		if tid == "" || !(strings.HasPrefix(uris[i], "spotify:track:") || strings.HasPrefix(uris[i], "spotify:episode:")) {
			bad = append(bad, fmt.Sprintf("第 %d 首 %q", i+1, tid))
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("推不出去的曲目(local file 或空 id),整批不送:%s", strings.Join(bad, "、"))
	}
	return uris, nil
}

// writeErr:403 = 不是自己的、也不是協作的清單(Spotify 只讓這兩種可寫);404 = 清單不存在,或寫入端點其實不是 /items(未驗證)。
func writeErr(id string, err error) error {
	var ae *apiError
	switch {
	case errors.As(err, &ae) && ae.Status == http.StatusForbidden:
		return fmt.Errorf("Spotify 拒絕寫入清單 %s(只有自己的或協作的清單可以寫):%w", id, err)
	case errors.As(err, &ae) && ae.Status == http.StatusNotFound:
		return fmt.Errorf("Spotify 找不到清單 %s(或寫入端點不是 /items,見 spec §1.1):%w", id, err)
	}
	return err
}
