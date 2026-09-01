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
