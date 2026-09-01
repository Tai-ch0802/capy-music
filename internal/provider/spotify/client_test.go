package spotify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func TestDoRetriesOn429ThenSucceeds(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var slept []time.Duration
	orig := wait
	wait = func(ctx context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	t.Cleanup(func() { wait = orig })

	c := NewClient(srv.Client(), srv.URL)
	var out struct{ OK bool }
	status, err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, &out)
	if err != nil || status != http.StatusOK || !out.OK {
		t.Fatalf("do = (%d, %v), out=%+v", status, err, out)
	}
	if calls != 2 {
		t.Errorf("應重試一次,實際呼叫 %d 次", calls)
	}
	if len(slept) != 1 || slept[0] != 7*time.Second {
		t.Errorf("應依 Retry-After 睡 7s,實際 %v", slept)
	}
}

func TestDoGivesUpAfterMaxRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	orig := wait
	wait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { wait = orig })

	c := NewClient(srv.Client(), srv.URL)
	_, err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != http.StatusTooManyRequests {
		t.Fatalf("重試耗盡應回 429 apiError,得到 %v", err)
	}
}

func TestDoMaps401ToErrAuthExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	if _, err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !errors.Is(err, provider.ErrAuthExpired) {
		t.Fatalf("401 應映射 ErrAuthExpired,得到 %v", err)
	}
}

func TestDoParsesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"status":404,"message":"Player command failed","reason":"NO_ACTIVE_DEVICE"}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	_, err := c.do(context.Background(), http.MethodPut, "/me/player/play", nil, nil, nil)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 404 || ae.Reason != "NO_ACTIVE_DEVICE" {
		t.Fatalf("apiError 解析錯誤:%v", err)
	}
}

// oauth2 refresh 失敗(如 refresh token 被撤銷)發生在 Transport 層 —— 也要映射 ErrAuthExpired。
func TestDoMapsOAuthRetrieveError(t *testing.T) {
	ts := oauth2.ReuseTokenSource(nil, failingSource{})
	c := NewClient(oauth2.NewClient(context.Background(), ts), "http://127.0.0.1:0")
	if _, err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil); !errors.Is(err, provider.ErrAuthExpired) {
		t.Fatalf("RetrieveError 應映射 ErrAuthExpired,得到 %v", err)
	}
}

type failingSource struct{}

func (failingSource) Token() (*oauth2.Token, error) {
	return nil, &oauth2.RetrieveError{Response: &http.Response{StatusCode: 400}, Body: []byte("invalid_grant")}
}

func TestDoSendsQueryAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("device_id") != "d1" {
			t.Errorf("query 未帶上:%s", r.URL.RawQuery)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	q := url.Values{"device_id": {"d1"}}
	status, err := c.do(context.Background(), http.MethodPut, "/me/player/play", q, map[string]any{"uris": []string{"spotify:track:x"}}, nil)
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("(%d, %v)", status, err)
	}
}

// TestDoBackoffCancellable 驗證 429 退避可被 ctx 取消:Retry-After 刻意設 30(無上限的
// 真實情境),ctx 50ms 逾時應讓 do 快速返回,而不是傻等伺服器指定的秒數。
func TestDoBackoffCancellable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewClient(srv.Client(), srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.do(ctx, http.MethodGet, "/x", nil, nil, nil)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("應在 ctx timeout 後回 DeadlineExceeded,得到 %v", err)
	}
	if elapsed >= 2*time.Second {
		t.Errorf("應快速回傳(< 2s),實際耗時 %v", elapsed)
	}
}
