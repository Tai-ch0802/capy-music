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
	"strconv"
	"time"

	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const DefaultAPIBase = "https://api.spotify.com/v1"

const maxRetries = 3

// wait:429 退避的等待,可被 ctx 取消(Retry-After 無上限,不可讓 Ctrl-C 失效)。測試替換點。
var wait = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

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
			if attempt >= maxRetries {
				return resp.StatusCode, &apiError{Status: resp.StatusCode, Message: "rate limited,重試已達上限"}
			}
			if err := wait(ctx, time.Duration(retryAfterSeconds(resp, 1))*time.Second); err != nil {
				return 0, err
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

func retryAfterSeconds(resp *http.Response, fallback int) int {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

// ── JSON 映射 ──

type trackJSON struct {
	ID         string `json:"id"`
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
	return provider.Track{
		ProviderID: t.ID,
		ISRC:       t.ExternalIDs.ISRC,
		Title:      t.Name,
		Artists:    artists,
		Album:      t.Album.Name,
		DurationMS: t.DurationMS,
		Explicit:   t.Explicit,
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
