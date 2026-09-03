// Package apple 實作 Apple Music 的 Provider(spec §1.2、§4.3)。
// client.go:薄殼 REST。developer token(Authorization)與 Music User Token 由呼叫端注入。
package apple

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

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
func (c *Client) do(ctx context.Context, method, path string, q url.Values, out any) (int, error) {
	for attempt := 0; ; attempt++ {
		u := c.base + path
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return 0, err
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

// Preflight 只驗 developer token 是否被 Apple 接受,不需 MUT(呼叫端建 Client 時傳空字串)。
// 404 視為通過——這支端點形狀在驗收前是 provisional(調整條款 b);其餘 4xx/5xx 才算失敗。
func (c *Client) Preflight(ctx context.Context) error {
	status, err := c.do(ctx, http.MethodGet, "/storefronts/us", nil, nil)
	if err == nil || status == http.StatusNotFound {
		return nil
	}
	return err
}

// ── JSON 映射 ──

type songJSON struct {
	ID         string `json:"id"`
	Attributes struct {
		Name             string `json:"name"`
		ArtistName       string `json:"artistName"`
		AlbumName        string `json:"albumName"`
		DurationInMillis int    `json:"durationInMillis"`
		ISRC             string `json:"isrc"`
		ContentRating    string `json:"contentRating"`
		URL              string `json:"url"`
	} `json:"attributes"`
}

func (s *songJSON) toTrack() provider.Track {
	return provider.Track{
		ProviderID: s.ID,
		ISRC:       s.Attributes.ISRC,
		Title:      s.Attributes.Name,
		Artists:    []string{s.Attributes.ArtistName},
		Album:      s.Attributes.AlbumName,
		DurationMS: s.Attributes.DurationInMillis,
		Explicit:   s.Attributes.ContentRating == "explicit",
	}
}

func (c *Client) Storefront(ctx context.Context) (string, error) {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/me/storefront", nil, &resp); err != nil {
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
		if _, err := c.do(ctx, http.MethodGet, "/catalog/"+url.PathEscape(storefront)+"/search", q, &resp); err != nil {
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

// Song 取單曲(含 attributes.url,macOS 播放用;不自己拼 URL)。
func (c *Client) Song(ctx context.Context, storefront, id string) (provider.Track, string, error) {
	var resp struct {
		Data []songJSON `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/catalog/"+url.PathEscape(storefront)+"/songs/"+url.PathEscape(id), nil, &resp); err != nil {
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
					Name string `json:"name"`
				} `json:"attributes"`
			} `json:"data"`
			Next string `json:"next"` // 分頁看這個,不是「回傳數 < limit」——Apple 可能單頁回不滿 limit 仍有下一頁
		}
		if _, err := c.do(ctx, http.MethodGet, "/me/library/playlists", q, &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.Data {
			out = append(out, provider.PlaylistRef{ID: p.ID, Name: p.Attributes.Name, Total: -1}) // library 物件不含曲數
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

func (c *Client) LibraryPlaylistTracks(ctx context.Context, id string) ([]provider.Track, error) {
	var out []provider.Track
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
		status, err := c.do(ctx, http.MethodGet, "/me/library/playlists/"+url.PathEscape(id)+"/tracks", q, &resp)
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
			}
			out = append(out, tr)
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
