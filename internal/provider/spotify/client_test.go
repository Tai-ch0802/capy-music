package spotify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

	var stderrBuf bytes.Buffer
	origStderr := backoffStderr
	backoffStderr = &stderrBuf
	t.Cleanup(func() { backoffStderr = origStderr })

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
	if !strings.Contains(stderrBuf.String(), "rate limited") {
		t.Errorf("應向 stderr 提示 rate limited,實際 %q", stderrBuf.String())
	}
}

// TestDoRefusesExcessiveRetryAfter:Retry-After 超過 maxBackoff 時不值得等——
// 立即回可行動錯誤,而不是傻等一小時。
func TestDoRefusesExcessiveRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	var waitCalls int
	orig := wait
	wait = func(context.Context, time.Duration) error { waitCalls++; return nil }
	t.Cleanup(func() { wait = orig })

	c := NewClient(srv.Client(), srv.URL)
	_, err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != http.StatusTooManyRequests {
		t.Fatalf("應立即回 429 apiError,得到 %v", err)
	}
	if !strings.Contains(err.Error(), "3600") {
		t.Errorf("訊息應含 Spotify 要求的秒數,得到 %q", err.Error())
	}
	if waitCalls != 0 {
		t.Errorf("超過上限不應等待,wait 被呼叫 %d 次", waitCalls)
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

// ── SearchTracks, Devices, State, Play 測試 ──

const trackJSONFixture = `{"id":"%s","name":"派對動物","uri":"spotify:track:%s","duration_ms":227000,"explicit":false,
"album":{"name":"自傳"},"artists":[{"name":"五月天"}],"external_ids":{"isrc":"TWA472400123"}}`

func trackFx(id string) string { return fmt.Sprintf(trackJSONFixture, id, id) }

func TestSearchTracksPaginates(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("type") != "track" || q.Get("q") != "五月天" {
			t.Errorf("search 參數錯誤:%s", r.URL.RawQuery)
		}
		if lim := q.Get("limit"); lim != "10" && lim != "5" {
			t.Errorf("單次 limit 應 ≤10:%s", lim)
		}
		offsets = append(offsets, q.Get("offset"))
		n := 10
		if q.Get("offset") == "10" {
			n = 5
		}
		items := make([]string, n)
		for i := range items {
			items[i] = trackFx(fmt.Sprintf("id%s-%02d", q.Get("offset"), i))
		}
		fmt.Fprintf(w, `{"tracks":{"items":[%s],"total":15}}`, strings.Join(items, ","))
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	tracks, err := c.SearchTracks(context.Background(), "五月天", 15)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 15 {
		t.Fatalf("應取回 15 首,得到 %d", len(tracks))
	}
	if len(offsets) != 2 || offsets[0] != "0" || offsets[1] != "10" {
		t.Errorf("分頁 offset 錯誤:%v", offsets)
	}
	tr := tracks[0]
	if tr.Title != "派對動物" || tr.Artists[0] != "五月天" || tr.Album != "自傳" ||
		tr.ISRC != "TWA472400123" || tr.DurationMS != 227000 || tr.ProviderID == "" {
		t.Errorf("track 映射錯誤:%+v", tr)
	}
}

func TestSearchTracksStopsWhenExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tracks":{"items":[%s],"total":1}}`, trackFx("only1"))
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	tracks, err := c.SearchTracks(context.Background(), "x", 10)
	if err != nil || len(tracks) != 1 {
		t.Fatalf("(%d, %v)", len(tracks), err)
	}
}

func TestStateHandles204(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	st, err := c.State(context.Background())
	if err != nil || st != nil {
		t.Fatalf("204 應回 (nil, nil),得到 (%+v, %v)", st, err)
	}
}

func TestStateDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"is_playing":true,"progress_ms":61000,"item":%s,
"device":{"id":"d1","name":"MacBook","type":"Computer","is_active":true,"volume_percent":80}}`, trackFx("t1"))
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	st, err := c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Playing || st.ProgressMS != 61000 || st.Track == nil || st.Track.Title != "派對動物" ||
		st.Device.Name != "MacBook" || !st.Device.Active || st.Device.VolumePct != 80 {
		t.Errorf("state 映射錯誤:%+v", st)
	}
}

func TestPlayMapsNoActiveDevice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"status":404,"message":"Player command failed: No active device found","reason":"NO_ACTIVE_DEVICE"}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	if err := c.Play(context.Background(), []string{"spotify:track:x"}, ""); !errors.Is(err, provider.ErrNoActiveDevice) {
		t.Fatalf("player 404 NO_ACTIVE_DEVICE 應映射,得到 %v", err)
	}
}

func TestPlaySendsURIsAndDevice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/me/player/play" || r.URL.Query().Get("device_id") != "d9" {
			t.Errorf("play 請求形狀錯誤:%s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		var body struct {
			URIs []string `json:"uris"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.URIs) != 1 || body.URIs[0] != "spotify:track:abc" {
			t.Errorf("body 錯誤:%+v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	if err := c.Play(context.Background(), []string{"spotify:track:abc"}, "d9"); err != nil {
		t.Fatal(err)
	}
}

func TestPlayResumeSendsNoBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if len(b) != 0 {
			t.Errorf("resume 不應帶 body:%s", b)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	if err := c.Play(context.Background(), nil, ""); err != nil {
		t.Fatal(err)
	}
}

func TestDevicesDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"devices":[{"id":"d1","name":"手機","type":"Smartphone","is_active":false,"volume_percent":50}]}`))
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	ds, err := c.Devices(context.Background())
	if err != nil || len(ds) != 1 || ds[0].Name != "手機" || ds[0].Type != "Smartphone" {
		t.Fatalf("(%+v, %v)", ds, err)
	}
}

func TestMyPlaylistsPaginatesAndDualKey(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/playlists" {
			t.Errorf("path = %s", r.URL.Path)
		}
		offsets = append(offsets, r.URL.Query().Get("offset"))
		if r.URL.Query().Get("offset") == "0" {
			// 50 筆滿頁:新欄位 items.total
			items := make([]string, 50)
			for i := range items {
				items[i] = fmt.Sprintf(`{"id":"p%02d","name":"清單%02d","owner":{"display_name":"tai"},"items":{"total":3}}`, i, i)
			}
			fmt.Fprintf(w, `{"items":[%s],"total":51}`, strings.Join(items, ","))
			return
		}
		// 第二頁 1 筆:舊欄位 tracks.total(雙鍵防衛)
		w.Write([]byte(`{"items":[{"id":"p50","name":"通勤","owner":{"display_name":"tai"},"tracks":{"total":7}}],"total":51}`))
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	pls, err := c.MyPlaylists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pls) != 51 || len(offsets) != 2 {
		t.Fatalf("分頁錯誤:%d 筆 / offsets %v", len(pls), offsets)
	}
	if pls[0].Total != 3 || pls[50].Total != 7 || pls[50].Name != "通勤" || pls[50].Owner != "tai" {
		t.Errorf("欄位映射(含雙鍵)錯誤:%+v %+v", pls[0], pls[50])
	}
}

func TestPlaylistItemsDualKeyAndPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/playlists/p1/items" {
			t.Errorf("應打新端點 /items:%s", r.URL.Path)
		}
		// 同頁混用新舊內層鍵(防衛雙鍵)
		fmt.Fprintf(w, `{"items":[{"item":%s},{"track":%s}],"total":2}`, trackFx("n1"), trackFx("o1"))
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	ts, err := c.PlaylistItems(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[0].ProviderID != "n1" || ts[1].ProviderID != "o1" {
		t.Fatalf("雙鍵 decode 錯誤:%+v", ts)
	}
}

func TestPlaylistItemsRestricted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"status":403,"message":"Forbidden"}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	if _, err := c.PlaylistItems(context.Background(), "someone-elses"); !errors.Is(err, provider.ErrRestricted) {
		t.Fatalf("403 應映射 ErrRestricted,得到 %v", err)
	}
}
