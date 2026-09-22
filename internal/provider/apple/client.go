// Package apple 實作 Apple Music 的 Provider(spec §1.2、§4.3)。
// client.go:薄殼 REST。developer token(Authorization)與 Music User Token 由呼叫端注入。
package apple

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
	"time"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// DefaultAPIBase:網頁播放器的私有 API。使用者複製來的 web developer token 只在這裡有效
// (官方 api.music.apple.com 對它的行為未驗證;附錄 A C-0)。CAPY_APPLE_API_BASE 可覆寫。
const DefaultAPIBase = "https://amp-api.music.apple.com/v1"

const webOrigin = "https://music.apple.com"

const (
	searchPageMax = 25  // Apple catalog search 單次上限
	libraryPage   = 100 // library 端點單次上限
)

type Client struct {
	hc                 *http.Client
	base, dev, userTok string
}

func NewClient(hc *http.Client, base, devToken, userToken string) *Client {
	if base == "" {
		base = DefaultAPIBase
	}
	return &Client{hc: hc, base: base, dev: devToken, userTok: userToken}
}

type apiError struct {
	Status int
	Title  string
	Detail string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("apple API %d %s %s", e.Status, e.Title, e.Detail)
}

// do:401 = developer token 無效、403 = MUT 無效——兩者對使用者都是「重跑 auth login apple」(spec §4.3)。
// body 非 nil 時序列化成 JSON 並帶 Content-Type(寫入端點;決策 49)。
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
		req.Header.Set("Authorization", "Bearer "+c.dev)
		// ponytail: web token 綁 origin,缺這行 amp-api 會拒(gamdl 同);MUT 用網頁播放器的標頭名。
		// 這兩行的去留由附錄 A C-0 用真 token 決定。
		req.Header.Set("Origin", webOrigin)
		if c.userTok != "" {
			req.Header.Set("Media-User-Token", c.userTok)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return 0, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			if resp.Header.Get("Retry-After") == "" {
				// amp-api 的 429 不帶 Retry-After,而且窗口是滾動約一小時、配額是所有網頁播放器共用的(計畫 2026-09-22 §1.2 第 3 點):
				// 1 / 2 / 4 秒重試等於白等,還會延長被限流的時間——直接失敗、把原因講清楚。有 Retry-After 才照它等。
				return resp.StatusCode, &apiError{Status: resp.StatusCode, Title: "rate limited", Detail: "Apple 網頁 token 的配額是所有網頁播放器共用的,約一小時後再試"}
			}
			if err := provider.Backoff(ctx, resp, attempt); err != nil {
				var rl *provider.RateLimitError
				if errors.As(err, &rl) {
					return resp.StatusCode, &apiError{Status: resp.StatusCode, Title: "rate limited", Detail: rl.Message}
				}
				return 0, err
			}
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			return resp.StatusCode, fmt.Errorf("developer token 無效(401):%w", provider.ErrAuthExpired)
		}
		if resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			return resp.StatusCode, fmt.Errorf("Music User Token 無效或訂閱失效(403):%w", provider.ErrAuthExpired)
		}
		if resp.StatusCode >= 400 {
			var eb struct {
				Errors []struct {
					Title  string `json:"title"`
					Detail string `json:"detail"`
				} `json:"errors"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&eb)
			resp.Body.Close()
			ae := &apiError{Status: resp.StatusCode}
			if len(eb.Errors) > 0 {
				ae.Title, ae.Detail = eb.Errors[0].Title, eb.Errors[0].Detail
			}
			return resp.StatusCode, ae
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

// Preflight:只帶 developer token 打一個公開端點。err == nil 且 verified → 確認通過;
// 404 → 端點形狀未定(附錄 A C-0 前 base/路徑都是推定),無法驗證:非失敗,但也不算通過,
// 呼叫端據此在後續失敗訊息裡把「API base 可能不對」列為原因。
func (c *Client) Preflight(ctx context.Context) (bool, error) {
	status, err := c.do(ctx, http.MethodGet, "/storefronts/us", nil, nil, nil)
	switch {
	case err == nil:
		return true, nil
	case status == http.StatusNotFound:
		return false, nil
	case status == http.StatusForbidden: // 沒帶 MUT,403 只可能是 developer token / Origin
		return false, fmt.Errorf("developer token 或 Origin 被拒(403):%w", provider.ErrAuthExpired)
	}
	return false, err
}

// ── JSON 映射 ──

type songJSON struct {
	ID         string `json:"id"`
	Attributes struct {
		Name             string   `json:"name"`
		ArtistName       string   `json:"artistName"`
		AlbumName        string   `json:"albumName"`
		DurationInMillis int      `json:"durationInMillis"`
		ISRC             string   `json:"isrc"`
		ContentRating    string   `json:"contentRating"`
		URL              string   `json:"url"`
		ReleaseDate      string   `json:"releaseDate"`
		GenreNames       []string `json:"genreNames"`
		Artwork          struct {
			URL string `json:"url"` // 模板:…/{w}x{h}bb.jpg
		} `json:"artwork"`
		Previews []struct {
			URL string `json:"url"`
		} `json:"previews"`
	} `json:"attributes"`
}

// artworkSize:Apple artwork.url 是含 {w}x{h} 佔位符的模板;頁面用 600。
var artworkSize = strings.NewReplacer("{w}", "600", "{h}", "600")

func (s *songJSON) toTrack() provider.Track {
	var artwork, preview string
	if s.Attributes.Artwork.URL != "" {
		artwork = artworkSize.Replace(s.Attributes.Artwork.URL)
	}
	if len(s.Attributes.Previews) > 0 {
		preview = s.Attributes.Previews[0].URL
	}
	return provider.Track{
		ProviderID:  s.ID,
		ISRC:        s.Attributes.ISRC,
		Title:       s.Attributes.Name,
		Artists:     []string{s.Attributes.ArtistName},
		Album:       s.Attributes.AlbumName,
		DurationMS:  s.Attributes.DurationInMillis,
		Explicit:    s.Attributes.ContentRating == "explicit",
		URL:         s.Attributes.URL,
		ArtworkURL:  artwork,
		PreviewURL:  preview,
		ReleaseDate: s.Attributes.ReleaseDate,
		Genres:      s.Attributes.GenreNames,
	}
}

func (c *Client) Storefront(ctx context.Context) (string, error) {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/me/storefront", nil, nil, &resp); err != nil {
		return "", err
	}
	if len(resp.Data) == 0 {
		return "", errors.New("Apple 未回傳 storefront")
	}
	return resp.Data[0].ID, nil
}

func (c *Client) SearchSongs(ctx context.Context, storefront, term string, limit int) ([]provider.Track, error) {
	var out []provider.Track
	for offset := 0; len(out) < limit; {
		page := limit - len(out)
		if page > searchPageMax {
			page = searchPageMax
		}
		q := url.Values{"types": {"songs"}, "term": {term}, "limit": {strconv.Itoa(page)}, "offset": {strconv.Itoa(offset)}}
		var resp struct {
			Results struct {
				Songs struct {
					Data []songJSON `json:"data"`
				} `json:"songs"`
			} `json:"results"`
		}
		if _, err := c.do(ctx, http.MethodGet, "/catalog/"+url.PathEscape(storefront)+"/search", q, nil, &resp); err != nil {
			return nil, err
		}
		for i := range resp.Results.Songs.Data {
			out = append(out, resp.Results.Songs.Data[i].toTrack())
		}
		if len(resp.Results.Songs.Data) < page {
			break
		}
		offset += len(resp.Results.Songs.Data)
	}
	return out, nil
}

// SongsByISRC:GET /catalog/{sf}/songs?filter[isrc]=<ISRC>。Apple 明文可回多筆;沒有命中回空 data(不是 404)。
func (c *Client) SongsByISRC(ctx context.Context, storefront, isrc string) ([]provider.Track, error) {
	n := provider.NormalizeISRC(isrc)
	if n == "" {
		return nil, fmt.Errorf("%w:%q", provider.ErrBadISRC, isrc)
	}
	var resp struct {
		Data []songJSON `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/catalog/"+url.PathEscape(storefront)+"/songs", url.Values{"filter[isrc]": {n}}, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]provider.Track, len(resp.Data))
	for i := range resp.Data {
		out[i] = resp.Data[i].toTrack()
	}
	return out, nil
}

// GetSong:Song 去掉播放 URL,404 映射成 ErrNotFound(釘選前確認 catalog id 存在)。
func (c *Client) GetSong(ctx context.Context, storefront, id string) (provider.Track, error) {
	t, _, err := c.Song(ctx, storefront, id)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return provider.Track{}, fmt.Errorf("%w:曲目 %s", provider.ErrNotFound, id)
	}
	return t, err
}

type artistJSON struct {
	ID         string `json:"id"`
	Attributes struct {
		Name string `json:"name"`
	} `json:"attributes"`
}

// SearchArtists 只取一頁(挑選器最多列幾個)。
func (c *Client) SearchArtists(ctx context.Context, storefront, term string, limit int) ([]provider.Artist, error) {
	if limit <= 0 || limit > searchPageMax {
		limit = searchPageMax
	}
	q := url.Values{"types": {"artists"}, "term": {term}, "limit": {strconv.Itoa(limit)}}
	var resp struct {
		Results struct {
			Artists struct {
				Data []artistJSON `json:"data"`
			} `json:"artists"`
		} `json:"results"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/catalog/"+url.PathEscape(storefront)+"/search", q, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]provider.Artist, len(resp.Results.Artists.Data))
	for i, a := range resp.Results.Artists.Data {
		out[i] = provider.Artist{ProviderID: a.ID, Name: a.Attributes.Name}
	}
	return out, nil
}

// ArtistTopSongs:GET /catalog/{sf}/artists/{id}/view/top-songs。
func (c *Client) ArtistTopSongs(ctx context.Context, storefront, id string) ([]provider.Track, error) {
	var resp struct {
		Data []songJSON `json:"data"`
	}
	path := "/catalog/" + url.PathEscape(storefront) + "/artists/" + url.PathEscape(id) + "/view/top-songs"
	if _, err := c.do(ctx, http.MethodGet, path, nil, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]provider.Track, len(resp.Data))
	for i := range resp.Data {
		out[i] = resp.Data[i].toTrack()
	}
	return out, nil
}

// Song 取單曲(含 attributes.url,macOS 播放用;不自己拼 URL)。
func (c *Client) Song(ctx context.Context, storefront, id string) (provider.Track, string, error) {
	var resp struct {
		Data []songJSON `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/catalog/"+url.PathEscape(storefront)+"/songs/"+url.PathEscape(id), nil, nil, &resp); err != nil {
		return provider.Track{}, "", err
	}
	if len(resp.Data) == 0 {
		return provider.Track{}, "", &apiError{Status: 404, Title: "not found", Detail: id}
	}
	return resp.Data[0].toTrack(), resp.Data[0].Attributes.URL, nil
}

func (c *Client) LibraryPlaylists(ctx context.Context) ([]provider.PlaylistRef, error) {
	var out []provider.PlaylistRef
	for offset := 0; ; {
		q := url.Values{"limit": {strconv.Itoa(libraryPage)}, "offset": {strconv.Itoa(offset)}}
		var resp struct {
			Data []struct {
				ID         string `json:"id"`
				Attributes struct {
					Name             string `json:"name"`
					CanEdit          *bool  `json:"canEdit"` // 指標:假伺服器沒給就是「不知道」,不能當 false
					HasCollaboration bool   `json:"hasCollaboration"`
				} `json:"attributes"`
			} `json:"data"`
			Next string `json:"next"` // 分頁看這個,不是「回傳數 < limit」——Apple 可能單頁回不滿 limit 仍有下一頁
		}
		if _, err := c.do(ctx, http.MethodGet, "/me/library/playlists", q, nil, &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.Data {
			out = append(out, provider.PlaylistRef{ID: p.ID, Name: p.Attributes.Name, Total: -1, // library 物件不含曲數
				Unwritable: unwritableReason(p.Attributes.CanEdit, p.Attributes.HasCollaboration)})
		}
		if len(resp.Data) == 0 { // 防呆:有 next 但無資料也視為結束,不重打同一 offset
			return out, nil
		}
		if resp.Next == "" {
			return out, nil
		}
		offset += len(resp.Data)
	}
}

// libraryEntry:library 清單的一列——列 id(PUT 整批取代與重讀對齊要用;真帳號看到 `i.…` / `a.…`)與它算出來的 Track。
// Track.ProviderID 的規則(有 catalog 對應取 catalog id、否則列 id)只在這裡定一次:pull 觀測與 ApplyOps 的重讀對齊必須同一套,
// 不然 library-only 曲目會被誤判成「平台已變」(計畫 2026-09-22 §3.2)。
type libraryEntry struct {
	ID    string
	Track provider.Track
}

func (c *Client) LibraryPlaylistTracks(ctx context.Context, id string) ([]provider.Track, error) {
	es, err := c.libraryPlaylistEntries(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]provider.Track, len(es))
	for i := range es {
		out[i] = es[i].Track
	}
	return out, nil
}

func (c *Client) libraryPlaylistEntries(ctx context.Context, id string) ([]libraryEntry, error) {
	var out []libraryEntry
	for offset := 0; ; {
		q := url.Values{"include": {"catalog"}, "limit": {strconv.Itoa(libraryPage)}, "offset": {strconv.Itoa(offset)}}
		var resp struct {
			Data []struct {
				ID         string `json:"id"`
				Attributes struct {
					Name             string `json:"name"`
					ArtistName       string `json:"artistName"`
					AlbumName        string `json:"albumName"`
					DurationInMillis int    `json:"durationInMillis"`
				} `json:"attributes"`
				Relationships struct {
					Catalog struct {
						Data []songJSON `json:"data"`
					} `json:"catalog"`
				} `json:"relationships"`
			} `json:"data"`
			Next string `json:"next"` // 分頁看這個,不是「回傳數 < limit」
		}
		status, err := c.do(ctx, http.MethodGet, "/me/library/playlists/"+url.PathEscape(id)+"/tracks", q, nil, &resp)
		if err != nil {
			if status == http.StatusNotFound { // Apple 對空清單或不存在的清單可能回 404
				return nil, fmt.Errorf("清單為空或不存在:%w", provider.ErrNotFound)
			}
			return nil, err
		}
		for _, it := range resp.Data {
			tr := provider.Track{
				ProviderID: it.ID,
				Title:      it.Attributes.Name,
				Artists:    []string{it.Attributes.ArtistName},
				Album:      it.Attributes.AlbumName,
				DurationMS: it.Attributes.DurationInMillis,
			}
			if cd := it.Relationships.Catalog.Data; len(cd) > 0 { // 有 catalog 對應:P4 resolver 要 catalog id 與 ISRC
				tr.ProviderID, tr.ISRC = cd[0].ID, cd[0].Attributes.ISRC
				// 豐富欄位只搬純 catalog 才有的那幾個;Name / ArtistName / AlbumName 保留 library 的值(使用者自己 library 裡的,
				// 可能與 catalog 不同),所以刻意不整個換成 toTrack()(review #58)。
				c := cd[0].toTrack()
				tr.URL, tr.ArtworkURL, tr.PreviewURL, tr.ReleaseDate, tr.Genres = c.URL, c.ArtworkURL, c.PreviewURL, c.ReleaseDate, c.Genres
			}
			out = append(out, libraryEntry{ID: it.ID, Track: tr})
		}
		if len(resp.Data) == 0 { // 防呆:有 next 但無資料也視為結束,不重打同一 offset
			return out, nil
		}
		if resp.Next == "" {
			return out, nil
		}
		offset += len(resp.Data)
	}
}

// ── 寫入(計畫 docs/superpowers/plans/2026-09-22-apple-write.md;決策 49;全部端點 2026-09-22 真帳號各打過一次)──
//
// 建清單與 append 是 Apple 官方文件化的端點;PUT 整批取代、PATCH 改名是網頁播放器自己打的 amp-api 私有端點(多個開源客戶端同形,
// Apple 未承諾)。⚠️ 這個套件不送 DELETE …/tracks:不帶 ids 會清空整份清單,帶 ids 的 mode=all 會把同一首的兩列一起刪。

// playlistInfo:清單本體。CanEdit 是「使用者自建」的機器判準(真帳號:25 個清單裡恰好 7 個自建為 true,Apple 精選、喜好歌曲、
// 已購買的音樂為 false);false 的清單寫入會回 500「Unable to update tracks」,所以寫之前先查。
type playlistInfo struct {
	ID, Name         string
	CanEdit          bool
	HasCollaboration bool
}

func (c *Client) Playlist(ctx context.Context, id string) (playlistInfo, error) {
	var resp struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Name             string `json:"name"`
				CanEdit          bool   `json:"canEdit"`
				HasCollaboration bool   `json:"hasCollaboration"`
			} `json:"attributes"`
		} `json:"data"`
	}
	status, err := c.do(ctx, http.MethodGet, "/me/library/playlists/"+url.PathEscape(id), nil, nil, &resp)
	if status == http.StatusNotFound || (err == nil && len(resp.Data) == 0) {
		return playlistInfo{}, fmt.Errorf("%w:清單 %s", provider.ErrNotFound, id)
	}
	if err != nil {
		return playlistInfo{}, err
	}
	a := resp.Data[0].Attributes
	return playlistInfo{ID: resp.Data[0].ID, Name: a.Name, CanEdit: a.CanEdit, HasCollaboration: a.HasCollaboration}, nil
}

// unwritableReason:光看清單屬性就知道寫不了的原因(PlaylistRef.Unwritable 與 ApplyOps 第二道防線共用同一句):
//   - canEdit:false = Apple 精選、喜好歌曲、已購買的音樂(寫入會 500);canEdit 不明(nil)不算。
//   - hasCollaboration:true = 協作清單。2026-09-22 對它原序全量 PUT 222 列回 500「Unable to update tracks」、零變動(計畫 §5 補測);
//     分不出是它的 a. 列 id、協作本身、還是列數(R-8 只送過 7 列),所以先整份不寫。
func unwritableReason(canEdit *bool, collab bool) string {
	switch {
	case canEdit != nil && !*canEdit:
		return "不是你自己建的清單(Apple 精選、喜好歌曲、已購買的音樂),Apple 不讓寫"
	case collab:
		return "協作清單:Apple 對它的整批取代回 500(2026-09-22 實測),capy 目前不寫;請在 Apple Music app 裡手動,或先複製成一般清單再連結(未實測,通常可以)"
	}
	return ""
}

// trackRef:寫入 body 的一筆。type 依 id 形狀:catalog id 是純數字 → songs;帶「.」的是 library 列 id(i.… / a.…)→ library-songs。
// 官方 LibraryPlaylistTracksRequest.Data 兩種都收;漏 type 會 2xx 但曲目靜默丟掉(kopuz),所以永遠帶。
type trackRef struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

func refOf(id string) trackRef {
	if strings.Contains(id, ".") {
		return trackRef{ID: id, Type: "library-songs"}
	}
	return trackRef{ID: id, Type: "songs"}
}

// writeBatch:POST …/tracks 每批上限(社群實測值;R-8 只測到 3 首)。
const writeBatch = 100

// AddTracks:POST …/tracks 尾端 append,每批 100、依序(R-8 第 7 項:跨批順序正確);官方回 204。
// 回傳已成功 append 的首數:第二批起失敗時前面的批已落地,呼叫端拿它組 PartialWriteError。
func (c *Client) AddTracks(ctx context.Context, id string, ids []string) (written int, err error) {
	path := "/me/library/playlists/" + url.PathEscape(id) + "/tracks"
	for pos := 0; pos < len(ids); pos += writeBatch {
		end := min(pos+writeBatch, len(ids))
		refs := make([]trackRef, 0, end-pos)
		for _, tid := range ids[pos:end] {
			refs = append(refs, refOf(tid))
		}
		if _, err := c.do(ctx, http.MethodPost, path, nil, map[string]any{"data": refs}, nil); err != nil {
			return pos, err
		}
	}
	return len(ids), nil
}

// ReplaceTracks:PUT …/tracks 整批取代——網頁播放器自己的重排端點(R-8:反序 / 去重 / 混型 / 移除全 204)。
// 陣列順序 = 新順序;沒列的就沒了;同一個列 id 出現幾次就留幾列。⚠️ 空陣列 = 清空整份(真帳號未驗;閘在 CLI 端:dry-run、閾值、確認)。
func (c *Client) ReplaceTracks(ctx context.Context, id string, refs []trackRef) error {
	if refs == nil {
		refs = []trackRef{} // 序列化成 [] 而不是 null
	}
	_, err := c.do(ctx, http.MethodPut, "/me/library/playlists/"+url.PathEscape(id)+"/tracks", nil, map[string]any{"data": refs}, nil)
	return err
}

// Rename:PATCH …/{id} 只送 name(R-8 第 5 項:description 保留)。
func (c *Client) Rename(ctx context.Context, id, name string) error {
	_, err := c.do(ctx, http.MethodPatch, "/me/library/playlists/"+url.PathEscape(id), nil, map[string]any{"attributes": map[string]string{"name": name}}, nil)
	return err
}

// createPollDelays:建清單後輪詢列表的間隔——退避 1 → 2 → 4 → 8 → 15 s(共 30 s、最多 6 次列表 GET;R-8 第 3 項量到 9 s)。
// 不用 1 Hz × 30 次去撞所有網頁播放器共用的配額(PR #79 review)。測試替換點。
var createPollDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second}

// CreatePlaylist:POST /me/library/playlists(官方端點;isPublic:false、不帶 tracks、不帶 description),再輪詢列表直到出現才回傳——
// pull 的 gone 判準是「不在 ListPlaylists 裡」,不等的話 migrate / pl link --create 剛建的清單會被當成已刪。
// POST 之後任何一條失敗(列表查不到、逾時、被中斷)都要帶著新清單的 id 與接回命令回錯:兩個 CLI 呼叫端只取 err,不然使用者的曲庫會留下一個
// 連不回來的孤兒清單,而且下次重跑會被 sameNamePlaylists 擋住(PR #80 review)。逾時不能默默回傳成功:migrate 接著 observe 會把不在
// 列表的清單當 gone 並取消連結,pl link --create 會連上一個 pull 視為已刪的 id。
func (c *Client) CreatePlaylist(ctx context.Context, name string) (provider.PlaylistRef, error) {
	var resp struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Name string `json:"name"`
			} `json:"attributes"`
		} `json:"data"`
	}
	body := map[string]any{"attributes": map[string]any{"name": name, "isPublic": false}}
	if _, err := c.do(ctx, http.MethodPost, "/me/library/playlists", nil, body, &resp); err != nil {
		return provider.PlaylistRef{}, err
	}
	if len(resp.Data) == 0 || resp.Data[0].ID == "" {
		return provider.PlaylistRef{}, errors.New("Apple 建立清單的回應沒有 id")
	}
	ref := provider.PlaylistRef{ID: resp.Data[0].ID, Name: resp.Data[0].Attributes.Name, Total: -1}
	if ref.Name == "" {
		ref.Name = name
	}
	recover := fmt.Sprintf("稍後用 capy pl link <名稱> apple:%s 接上", ref.ID)
	var total time.Duration
	for _, d := range createPollDelays {
		total += d
	}
	for attempt := 0; ; attempt++ {
		pls, err := c.LibraryPlaylists(ctx)
		if err != nil {
			return ref, fmt.Errorf("Apple 已建立清單「%s」(%s),但查清單列表失敗(%w);%s", ref.Name, ref.ID, err, recover)
		}
		if slices.ContainsFunc(pls, func(p provider.PlaylistRef) bool { return p.ID == ref.ID }) {
			return ref, nil
		}
		if attempt >= len(createPollDelays) {
			return ref, fmt.Errorf("Apple 已建立清單「%s」(%s),但 %v 內還沒出現在清單列表(iCloud 傳播延遲);%s", ref.Name, ref.ID, total, recover)
		}
		if attempt == 0 { // 最長 30 秒的安靜等待要有一句話;走 BackoffStderr 接縫,web 模式也看得到(stderr 不污染 TSV)
			fmt.Fprintf(provider.BackoffStderr, "等待 Apple 把新清單 %s 放進清單列表(通常幾秒)…\n", ref.ID)
		}
		if err := provider.Wait(ctx, createPollDelays[attempt]); err != nil {
			return ref, fmt.Errorf("Apple 已建立清單「%s」(%s),等待它出現在列表時被中斷(%w);%s", ref.Name, ref.ID, err, recover)
		}
	}
}

// writeErr:寫入端點的錯誤翻成可行動的句子。500「Unable to update」= canEdit:false 的清單(第二道防線;ApplyOps 寫之前已查過);404 = 清單不存在。
func writeErr(id string, err error) error {
	var ae *apiError
	switch {
	case errors.As(err, &ae) && ae.Status == http.StatusInternalServerError && strings.Contains(ae.Title+" "+ae.Detail, "Unable to update"):
		return fmt.Errorf("Apple 拒絕修改清單 %s(500 Unable to update tracks):不是你自己建的清單(Apple 精選、喜好歌曲、已購買的音樂),或是協作清單(列 id 是 a.;2026-09-22 實測):%w", id, err)
	case errors.As(err, &ae) && ae.Status == http.StatusNotFound:
		return fmt.Errorf("%w:Apple 找不到清單 %s(%v)", provider.ErrNotFound, id, err)
	}
	return err
}
