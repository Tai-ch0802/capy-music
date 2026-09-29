// Package youtube 實作 YouTube Music 的 Provider(spec §1.5、§4.6;附錄 C 決策 60)。
// client.go:music.youtube.com 網頁播放器自己用的 InnerTube(非官方)。憑證是使用者自己複製的 cookie(internal/auth/youtube),
// 每個請求現算 SAPISIDHASH。跟 Google Drive 的登入完全無關。
package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	ytauth "github.com/Tai-ch0802/capy-music/internal/auth/youtube"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// DefaultAPIBase:網頁播放器的 InnerTube。測試用 httptest 覆寫。
const DefaultAPIBase = "https://music.youtube.com/youtubei/v1"

// userAgent:任何現代瀏覽器的 UA 都行;固定一個,不抄使用者貼上的(不需要,也少存一樣東西)。
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

// songsParams:search 的「歌曲」filter(ytmusicapi parsers/search.py 的 get_search_params;2026-09-29 真帳號驗過)。
const songsParams = "EgWKAQIIAWoMEA4QChADEAQQCRAF"

type Client struct {
	hc     *http.Client
	base   string
	h      ytauth.Headers
	hl, gl string
	now    func() time.Time
}

// NewClient:lang 是 config 的 language(決策 50);hl 跟著它(2026-09-29 探測:hl=en 時歌手名是羅馬拼音,zh-TW 才是「米津玄師」,
// 跟 Apple TW 商店對得上),gl 取它的地區(沒有就 US)。
func NewClient(hc *http.Client, base string, h ytauth.Headers, lang string) *Client {
	if base == "" {
		base = DefaultAPIBase
	}
	hl, gl := "en", "US"
	if lang = strings.TrimSpace(lang); lang != "" {
		hl = lang
		if _, region, ok := strings.Cut(lang, "-"); ok && len(region) == 2 {
			gl = strings.ToUpper(region)
		}
	}
	return &Client{hc: hc, base: base, h: h, hl: hl, gl: gl, now: time.Now}
}

type apiError struct {
	Status int
	Detail string
}

func (e *apiError) Error() string { return fmt.Sprintf("youtube API %d %s", e.Status, e.Detail) }

// post:一個 InnerTube 呼叫。body 是端點自己的參數,context(client / user)這裡補;rawQuery 是額外的查詢字串(grid 的 continuation 用),
// **原樣接上、不再編碼**:YouTube 給的 token 本身已經 URL 編碼過(尾端是 %3D),用 url.Values 會把 % 變成 %25——伺服器照樣回這一頁,
// 卻多給一個通往空頁的 token(2026-09-29 真帳號:pl list 因此在「第 2 頁」失敗;ytmusicapi 也是字串直接相接)。
// 401 / 403 → ErrAuthExpired(cookie 貼錯或失效);429 / 5xx 交給 provider.Backoff(有 Retry-After 照它);其他非 200 回 apiError。
func (c *Client) post(ctx context.Context, endpoint string, body map[string]any, rawQuery string) (node, error) {
	payload := map[string]any{"context": map[string]any{
		"client": map[string]any{"clientName": "WEB_REMIX", "clientVersion": "1." + c.now().UTC().Format("20060102") + ".01.00", "hl": c.hl, "gl": c.gl},
		"user":   c.userContext(),
	}}
	for k, v := range body {
		payload[k] = v
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return node{}, err
	}
	u := c.base + "/" + endpoint + "?alt=json&prettyPrint=false"
	if rawQuery != "" {
		u += "&" + rawQuery
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
		if err != nil {
			return node{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Origin", ytauth.Origin)
		req.Header.Set("X-Origin", ytauth.Origin)
		req.Header.Set("Cookie", c.h.Cookie)
		req.Header.Set("Authorization", c.h.Authorization(c.now()))
		req.Header.Set("X-Goog-AuthUser", c.h.AuthUser)
		if c.h.PageID != "" {
			req.Header.Set("X-Goog-PageId", c.h.PageID)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return node{}, err
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			resp.Body.Close()
			if err := provider.Backoff(ctx, resp, attempt); err != nil {
				var rl *provider.RateLimitError
				if errors.As(err, &rl) {
					return node{}, &apiError{Status: resp.StatusCode, Detail: rl.Message}
				}
				return node{}, err
			}
			continue
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			resp.Body.Close()
			return node{}, i18n.Errorf("youtube.client.err.rejected", "status", resp.StatusCode, "err", provider.ErrAuthExpired)
		case resp.StatusCode != http.StatusOK:
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			resp.Body.Close()
			return node{}, &apiError{Status: resp.StatusCode, Detail: strings.TrimSpace(string(b))}
		}
		var v any
		err = json.NewDecoder(resp.Body).Decode(&v)
		resp.Body.Close()
		if err != nil {
			return node{}, i18n.Errorf("youtube.client.err.bad_json", "err", err)
		}
		return node{v}, nil
	}
}

// userContext:品牌帳號(x-goog-pageid)要放進 context.user.onBehalfOfUser(ytmusicapi 同);一般帳號是空物件。
func (c *Client) userContext() map[string]any {
	if c.h.PageID != "" {
		return map[string]any{"onBehalfOfUser": c.h.PageID}
	}
	return map[string]any{}
}

// Account:登入的帳號(account/account_menu)。沒有 email;ChannelID 是 UC… 的頻道 id(清單 id 的前綴,決策 60)。
type Account struct {
	Name, Handle, ChannelID string
}

// AccountInfo:回應裡沒有帳號 = cookie 已失效或抄到未登入的請求(HTTP 仍是 200)→ ErrAuthExpired。
func (c *Client) AccountInfo(ctx context.Context) (Account, error) {
	root, err := c.post(ctx, "account/account_menu", map[string]any{}, "")
	if err != nil {
		return Account{}, err
	}
	a, err := parseAccount(root)
	switch {
	case errors.Is(err, errNoAccount):
		return Account{}, i18n.Errorf("youtube.client.err.not_logged_in", "err", provider.ErrAuthExpired)
	case err != nil: // 頻道 id 不唯一或沒有:不能拿去當清單 id 的前綴,登入就要擋下來(不是靜默挑一個)
		return Account{}, i18n.Errorf("youtube.client.err.channel_id", "err", err)
	}
	return a, nil
}
