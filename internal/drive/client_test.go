package drive_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/drive/drivetest"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

var ctx = context.Background()

func newClient(t *testing.T) (*drive.Client, *drivetest.Server) {
	t.Helper()
	srv := drivetest.New(t)
	return drive.New(srv.Client(), srv.URL), srv
}

// stubWait 把退避等待換成記錄,並讓提示不進 stderr。
func stubWait(t *testing.T) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	origWait, origErr := provider.Wait, provider.BackoffStderr
	provider.Wait = func(_ context.Context, d time.Duration) error { got = append(got, d); return nil }
	provider.BackoffStderr = io.Discard
	t.Cleanup(func() { provider.Wait, provider.BackoffStderr = origWait, origErr })
	return &got
}

func mustCreate(t *testing.T, c *drive.Client, name string, props map[string]string, content string) *drive.File {
	t.Helper()
	f, err := c.Create(ctx, name, props, []byte(content))
	if err != nil {
		t.Fatalf("create %s:%v", name, err)
	}
	return f
}

func TestRoundTrip(t *testing.T) {
	c, srv := newClient(t)
	if f, err := c.Find(ctx, "manifest.json", nil); err != nil || f != nil {
		t.Fatalf("空 appdata 的 Find 應回 nil, nil:%v %v", f, err)
	}
	created := mustCreate(t, c, "manifest.json", map[string]string{"kind": "manifest"}, `{"v":1}`)
	if created.ID == "" || created.Version != 1 || created.AppProperties["kind"] != "manifest" || created.ModifiedTime.IsZero() {
		t.Fatalf("create 回的 metadata 不完整:%+v", created)
	}
	found, err := c.Find(ctx, "manifest.json", map[string]string{"kind": "manifest"})
	if err != nil || found == nil || found.ID != created.ID {
		t.Fatalf("Find 應找到剛建的檔:%+v %v", found, err)
	}
	if b, err := c.Download(ctx, created.ID); err != nil || string(b) != `{"v":1}` {
		t.Fatalf("download:%q %v", b, err)
	}

	srv.Advance(time.Minute)
	updated, err := c.Update(ctx, created.ID, map[string]string{"pid": "p1"}, []byte(`{"v":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || !updated.ModifiedTime.After(created.ModifiedTime) {
		t.Fatalf("update 後 version 與 modifiedTime 應前進:%+v → %+v", created, updated)
	}
	if updated.AppProperties["kind"] != "manifest" || updated.AppProperties["pid"] != "p1" {
		t.Fatalf("appProperties 應 merge:%v", updated.AppProperties)
	}
	if b, err := c.Download(ctx, created.ID); err != nil || string(b) != `{"v":2}` {
		t.Fatalf("update 後 download:%q %v", b, err)
	}
	if updated.MD5 == created.MD5 {
		t.Fatalf("內容變了 md5 應變")
	}

	if err := c.Delete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if fs, err := c.List(ctx, ""); err != nil || len(fs) != 0 || srv.Len() != 0 {
		t.Fatalf("delete 後應為空:%v %v", fs, err)
	}
	if _, err := c.Download(ctx, created.ID); err == nil {
		t.Fatal("刪掉的檔 download 應失敗")
	}
}

func TestListPagination(t *testing.T) {
	c, srv := newClient(t)
	srv.PageSize = 2
	for i := range 5 {
		mustCreate(t, c, "pl__"+string(rune('a'+i))+".json", map[string]string{"kind": "pl"}, "{}")
	}
	fs, err := c.List(ctx, "")
	if err != nil || len(fs) != 5 {
		t.Fatalf("應走完分頁拿到 5 份:%d %v", len(fs), err)
	}
	if srv.ListCalls != 3 {
		t.Fatalf("5 份、每頁 2 應打 3 次 list,實際 %d", srv.ListCalls)
	}
}

func TestQueryAndFilter(t *testing.T) {
	c, _ := newClient(t)
	mustCreate(t, c, "manifest.json", map[string]string{"kind": "manifest"}, "{}")
	a := mustCreate(t, c, "pl__a.json", map[string]string{"kind": "pl", "pid": "a"}, "{}")
	mustCreate(t, c, "pl__b.json", map[string]string{"kind": "pl", "pid": "b"}, "{}")
	quoted := mustCreate(t, c, "it's.json", map[string]string{"kind": "x'y\\z"}, "{}")

	got, err := c.List(ctx, drive.Query("", map[string]string{"kind": "pl"}))
	if err != nil || len(got) != 2 {
		t.Fatalf("kind=pl 應 2 份:%v %v", got, err)
	}
	if f, err := c.Find(ctx, "pl__a.json", map[string]string{"kind": "pl", "pid": "a"}); err != nil || f == nil || f.ID != a.ID {
		t.Fatalf("name+kind+pid 應唯一命中 a:%v %v", f, err)
	}
	if f, err := c.Find(ctx, "pl__a.json", map[string]string{"pid": "b"}); err != nil || f != nil {
		t.Fatalf("name 對但 pid 不對應找不到:%v %v", f, err)
	}
	if f, err := c.Find(ctx, "it's.json", map[string]string{"kind": "x'y\\z"}); err != nil || f == nil || f.ID != quoted.ID {
		t.Fatalf("含引號與反斜線的值要正確跳脫:%v %v", f, err)
	}
	if q := drive.Query("a'b", map[string]string{"z": "1", "k": "2"}); q != `name = 'a\'b' and appProperties has { key='k' and value='2' } and appProperties has { key='z' and value='1' }` {
		t.Fatalf("Query 形狀:%s", q)
	}
}

func TestFindDuplicatesPicksNewestAndWarns(t *testing.T) {
	c, srv := newClient(t)
	var stderr bytes.Buffer
	orig := drive.Stderr
	drive.Stderr = &stderr
	t.Cleanup(func() { drive.Stderr = orig })

	old := mustCreate(t, c, "manifest.json", map[string]string{"kind": "manifest"}, "old")
	srv.Advance(time.Hour)
	newer := mustCreate(t, c, "manifest.json", map[string]string{"kind": "manifest"}, "new")
	f, err := c.Find(ctx, "manifest.json", map[string]string{"kind": "manifest"})
	if err != nil || f == nil || f.ID != newer.ID || f.ID == old.ID {
		t.Fatalf("同名多份應取 modifiedTime 最新:%+v %v", f, err)
	}
	if !strings.Contains(stderr.String(), "2 份 manifest.json") {
		t.Fatalf("應警告有 2 份:%q", stderr.String())
	}
	if srv.Len() != 2 {
		t.Fatal("Find 不可清理重複檔")
	}
	stderr.Reset()
	if _, err := c.Find(ctx, "manifest.json", map[string]string{"kind": "nope"}); err != nil || stderr.Len() != 0 {
		t.Fatalf("沒命中不該警告:%v %q", err, stderr.String())
	}
}

// 同一毫秒建的兩份:files.list 沒有 orderBy,要用 ID 決勝,所有裝置才會挑同一份。
func TestFindDuplicatesSameTimeTiebreakByID(t *testing.T) {
	c, _ := newClient(t)
	orig := drive.Stderr
	drive.Stderr = io.Discard
	t.Cleanup(func() { drive.Stderr = orig })
	a := mustCreate(t, c, "manifest.json", nil, "a")
	b := mustCreate(t, c, "manifest.json", nil, "b")
	if a.ModifiedTime != b.ModifiedTime || b.ID <= a.ID {
		t.Fatalf("測試前提:同時刻、b 的 ID 較大:%+v %+v", a, b)
	}
	f, err := c.Find(ctx, "manifest.json", nil)
	if err != nil || f.ID != b.ID {
		t.Fatalf("modifiedTime 相同應取 ID 較大者 %s,得 %+v %v", b.ID, f, err)
	}
}

// content 為空會把 Drive 上的 source of truth 清成 0 byte,必須擋在 client。
func TestUpdateRejectsEmptyContent(t *testing.T) {
	c, _ := newClient(t)
	f := mustCreate(t, c, "manifest.json", nil, `{"v":1}`)
	for _, content := range [][]byte{nil, {}} {
		if _, err := c.Update(ctx, f.ID, map[string]string{"pid": "x"}, content); err == nil || !strings.Contains(err.Error(), "清空") {
			t.Fatalf("空內容應被擋:%v", err)
		}
	}
	if b, _ := c.Download(ctx, f.ID); string(b) != `{"v":1}` {
		t.Fatalf("被擋的 Update 不可動到內容:%q", b)
	}
}

func TestErrorMapping(t *testing.T) {
	t.Run("401 → ErrAuthExpired", func(t *testing.T) {
		c, srv := newClient(t)
		srv.Fail(1, http.StatusUnauthorized, "authError", "")
		if _, err := c.List(ctx, ""); !errors.Is(err, provider.ErrAuthExpired) {
			t.Fatalf("得 %v", err)
		}
	})
	t.Run("403 storageQuotaExceeded → ErrStorageQuota", func(t *testing.T) {
		c, srv := newClient(t)
		srv.Fail(1, http.StatusForbidden, "storageQuotaExceeded", "")
		if _, err := c.Create(ctx, "a.json", nil, []byte("{}")); !errors.Is(err, drive.ErrStorageQuota) {
			t.Fatalf("得 %v", err)
		}
	})
	t.Run("403 accessNotConfigured → ErrAPINotEnabled 含啟用連結", func(t *testing.T) {
		c, srv := newClient(t)
		srv.Fail(1, http.StatusForbidden, "accessNotConfigured", "")
		_, err := c.List(ctx, "")
		if !errors.Is(err, drive.ErrAPINotEnabled) || !strings.Contains(err.Error(), "https://console.developers.google.com/apis/api/drive.googleapis.com") {
			t.Fatalf("得 %v", err)
		}
	})
	t.Run("403 userRateLimitExceeded 無 Retry-After → 指數退避後成功", func(t *testing.T) {
		waits := stubWait(t)
		c, srv := newClient(t)
		mustCreate(t, c, "a.json", nil, "{}")
		srv.Fail(2, http.StatusForbidden, "userRateLimitExceeded", "")
		fs, err := c.List(ctx, "")
		if err != nil || len(fs) != 1 {
			t.Fatalf("退避後應成功:%v %v", fs, err)
		}
		if len(*waits) != 2 || (*waits)[0] != time.Second || (*waits)[1] != 2*time.Second {
			t.Fatalf("應等 1s、2s:%v", *waits)
		}
	})
	t.Run("429 帶 Retry-After → 照標頭等", func(t *testing.T) {
		waits := stubWait(t)
		c, srv := newClient(t)
		srv.Fail(1, http.StatusTooManyRequests, "rateLimitExceeded", "3")
		if _, err := c.List(ctx, ""); err != nil {
			t.Fatal(err)
		}
		if len(*waits) != 1 || (*waits)[0] != 3*time.Second {
			t.Fatalf("應等 3s:%v", *waits)
		}
	})
	t.Run("rate limit 超過重試上限 → APIError", func(t *testing.T) {
		waits := stubWait(t)
		c, srv := newClient(t)
		srv.Fail(provider.MaxRetries+1, http.StatusForbidden, "rateLimitExceeded", "")
		var ae *drive.APIError
		if _, err := c.List(ctx, ""); !errors.As(err, &ae) || !strings.Contains(ae.Message, "上限") {
			t.Fatalf("得 %v", err)
		}
		if len(*waits) != provider.MaxRetries {
			t.Fatalf("應等 %d 次:%v", provider.MaxRetries, *waits)
		}
	})
	t.Run("403 dailyLimitExceeded 不重試 → APIError", func(t *testing.T) {
		waits := stubWait(t)
		c, srv := newClient(t)
		srv.Fail(1, http.StatusForbidden, "dailyLimitExceeded", "")
		var ae *drive.APIError
		if _, err := c.List(ctx, ""); !errors.As(err, &ae) || ae.Status != 403 || ae.Reason != "dailyLimitExceeded" || len(*waits) != 0 {
			t.Fatalf("得 %v,等待 %v", err, *waits)
		}
	})
	t.Run("403 insufficientPermissions → ErrAuthExpired 提到 scope", func(t *testing.T) {
		c, srv := newClient(t)
		srv.Fail(1, http.StatusForbidden, "insufficientPermissions", "")
		_, err := c.List(ctx, "")
		if !errors.Is(err, provider.ErrAuthExpired) || !strings.Contains(err.Error(), "drive.appdata") {
			t.Fatalf("得 %v", err)
		}
	})
	t.Run("非 JSON 錯誤頁只留 300 字", func(t *testing.T) {
		page := strings.Repeat("<html>錯誤頁 ", 500)
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, page)
		}))
		t.Cleanup(bad.Close)
		var ae *drive.APIError
		_, err := drive.New(bad.Client(), bad.URL).List(ctx, "")
		if !errors.As(err, &ae) || ae.Status != 502 || !strings.HasPrefix(ae.Message, "<html>錯誤頁") {
			t.Fatalf("得 %v", err)
		}
		if n := len([]rune(ae.Message)); n > 301 {
			t.Fatalf("訊息應截到 300 字,得 %d", n)
		}
	})
	t.Run("500 → APIError", func(t *testing.T) {
		c, srv := newClient(t)
		srv.Fail(1, http.StatusInternalServerError, "backendError", "")
		var ae *drive.APIError
		if _, err := c.Download(ctx, "x"); !errors.As(err, &ae) || ae.Status != 500 {
			t.Fatalf("得 %v", err)
		}
	})
	t.Run("退避中 ctx 取消原樣透傳", func(t *testing.T) {
		origErr := provider.BackoffStderr
		provider.BackoffStderr = io.Discard
		t.Cleanup(func() { provider.BackoffStderr = origErr })
		c, srv := newClient(t)
		srv.Fail(1, http.StatusTooManyRequests, "rateLimitExceeded", "5")
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := c.List(cctx, ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("得 %v", err)
		}
	})
}

func invalidGrantServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 裸 oauth2 transport 的 refresh 失敗(*oauth2.RetrieveError)→ ErrAuthExpired,cron 不會收到裸 oauth2 錯誤。
func TestTransportRefreshErrorIsAuthExpired(t *testing.T) {
	tok := invalidGrantServer(t)
	_, srv := newClient(t)
	conf := &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{TokenURL: tok.URL, AuthStyle: oauth2.AuthStyleInParams}}
	hc := conf.Client(ctx, &oauth2.Token{AccessToken: "at", RefreshToken: "rt", Expiry: time.Now().Add(-time.Hour)})
	c := drive.New(hc, srv.URL)
	_, err := c.List(ctx, "")
	var rerr *oauth2.RetrieveError
	if !errors.Is(err, provider.ErrAuthExpired) || !errors.As(err, &rerr) {
		t.Fatalf("得 %v", err)
	}
	if strings.Contains(err.Error(), "/drive/v3/files") {
		t.Fatalf("授權錯誤不該帶整串請求 URL:%v", err)
	}
}

// 經 auth.GoogleTokenSource 的 refresh 失敗:explain 把 RetrieveError 換成 ErrGoogleGrant,
// drive 仍要映射成 ErrAuthExpired 且保住「Publish app」歸因。
func TestTransportRefreshErrorKeepsExplain(t *testing.T) {
	keyring.MockInit()
	t.Setenv("CAPY_CONFIG_DIR", t.TempDir())
	tok := invalidGrantServer(t)
	orig := auth.GoogleEndpoint
	auth.GoogleEndpoint.TokenURL = tok.URL
	t.Cleanup(func() { auth.GoogleEndpoint = orig })
	if err := auth.SaveToken(auth.KeyGoogleToken, &oauth2.Token{AccessToken: "at", RefreshToken: "rt", Expiry: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	ts, err := auth.GoogleTokenSource(ctx, auth.GoogleClient{ID: "cid", Secret: "sec"})
	if err != nil {
		t.Fatal(err)
	}
	_, srv := newClient(t)
	c := drive.New(oauth2.NewClient(ctx, ts), srv.URL)
	_, err = c.List(ctx, "")
	if !errors.Is(err, provider.ErrAuthExpired) || !errors.Is(err, auth.ErrGoogleGrant) || !strings.Contains(err.Error(), "Publish app") {
		t.Fatalf("explain 歸因被丟掉:%v", err)
	}
	if strings.Contains(err.Error(), "/drive/v3/files") {
		t.Fatalf("授權錯誤不該帶整串請求 URL:%v", err)
	}
}

// 純網路錯誤不映射、且保留 URL(dial 失敗要知道是打哪裡)。
func TestTransportNetworkErrorKeepsURL(t *testing.T) {
	srv := drivetest.New(t)
	dead := srv.URL
	srv.Close()
	_, err := drive.New(srv.Client(), dead).List(ctx, "")
	if err == nil || errors.Is(err, provider.ErrAuthExpired) || !strings.Contains(err.Error(), "/drive/v3/files") {
		t.Fatalf("得 %v", err)
	}
}
