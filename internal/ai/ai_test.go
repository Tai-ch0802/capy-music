package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

func TestMain(m *testing.M) {
	keyring.MockInit() // 不碰真的 keychain
	os.Exit(m.Run())
}

func TestParseHeadersSkipsJunkTrimsAndCanonicalizes(t *testing.T) {
	h, err := ParseHeaders("CF-Access-Client-Id: abc\r\n  cf-access-client-secret :  s3cr3t  \n\n\t\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Get("Cf-Access-Client-Id"); got != "abc" {
		t.Errorf("Id = %q", got)
	}
	if got := h.Get("cf-access-client-secret"); got != "s3cr3t" {
		t.Errorf("Secret = %q(名稱要正規化、值要 trim)", got)
	}
	if len(h) != 2 {
		t.Errorf("要剛好兩個標頭,得到 %v", h)
	}
	for _, bad := range []string{"no-colon-line", ": empty-name", "empty-value:", "X-A: a\x01b", "X A: b", "X-A: a\nX-B"} {
		if _, err := ParseHeaders(bad); err == nil {
			t.Errorf("%q 要被拒絕", bad)
		}
	}
	if h, err := ParseHeaders(""); err != nil || len(h) != 0 {
		t.Errorf("空字串 = 沒有標頭:%v %v", h, err)
	}
	if err := ValidateHeadersText(strings.Repeat("X-Long: v\n", 300)); err == nil {
		t.Error("超過 MaxHeadersBytes 要被拒絕")
	}
	if err := ValidateHeadersText("X-A: b"); err != nil {
		t.Error(err)
	}
}

// capture:假端點,記下每個請求的 header 與 body,依 handler 回應。
type capture struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (c *capture) record(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.reqs = append(c.reqs, r)
	c.body = append(c.body, string(b))
	c.mu.Unlock()
}

func serve(t *testing.T, h http.HandlerFunc) (*capture, Config) {
	t.Helper()
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return c, Config{BaseURL: srv.URL + "/v1", Model: "m1", APIKey: "sk-testkey-0123456789"}
}

func sse(w http.ResponseWriter, frames ...string) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	for _, f := range frames {
		fmt.Fprintf(w, "data: %s\n\n", f)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func delta(s string) string {
	b, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]string{"content": s}}}})
	return string(b)
}

func TestRequestHeadersPrecedenceAndURL(t *testing.T) {
	c, cfg := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"a"}]}`)
	})
	cfg.Headers, _ = ParseHeaders("CF-Access-Client-Id: abc\nAuthorization: Basic dXNlcg==")
	if _, err := cfg.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := c.reqs[0]
	if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
		t.Errorf("要 GET /v1/models,得到 %s %s", r.Method, r.URL.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Basic dXNlcg==" {
		t.Errorf("使用者自己的 Authorization 要蓋掉 Bearer,得到 %q", got)
	}
	if got := r.Header.Values("Authorization"); len(got) != 1 {
		t.Errorf("Authorization 不可重複:%v", got)
	}
	if got := r.Header.Get("Cf-Access-Client-Id"); got != "abc" {
		t.Errorf("自訂標頭要送:%q", got)
	}
	// 沒有使用者標頭時 Bearer 照送;/models 探測與 chat 用同一組。
	cfg.Headers = nil
	_, _ = cfg.ChatOnce(context.Background(), ChatRequest{User: "x"})
	if got := c.reqs[1].Header.Get("Authorization"); got != "Bearer sk-testkey-0123456789" {
		t.Errorf("Bearer:%q", got)
	}
	if got := c.reqs[1].Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type:%q", got)
	}
}

func TestChatStreamsCompleteLinesAndReturnsAll(t *testing.T) {
	_, cfg := serve(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, delta("## Ba"), delta("sics\nThe so"), delta("ng\n"), `: keep-alive`, delta("tail"))
	})
	var lines []string
	full, err := cfg.Chat(context.Background(), ChatRequest{System: "sys", User: "u", MaxTokens: 5}, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"## Basics", "The song", "tail"}; strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("行要湊到換行才回呼、尾巴最後補上:%q", lines)
	}
	if full != "## Basics\nThe song\ntail" {
		t.Errorf("整段:%q", full)
	}
}

func TestChatBodyShape(t *testing.T) {
	c, cfg := serve(t, func(w http.ResponseWriter, r *http.Request) { sse(w, delta("ok")) })
	if _, err := cfg.Chat(context.Background(), ChatRequest{System: "S", User: "U", MaxTokens: 7}, nil); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(c.body[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "m1" || body["stream"] != true || body["max_tokens"] != float64(7) {
		t.Errorf("body:%v", body)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "U" {
		t.Errorf("messages:%v", msgs)
	}
}

func TestChatStreamErrorFrameIsAnError(t *testing.T) {
	_, cfg := serve(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, delta("half"), `{"error":{"message":"quota exhausted"}}`)
	})
	_, err := cfg.Chat(context.Background(), ChatRequest{User: "u"}, nil)
	if err == nil || !strings.Contains(err.Error(), "quota exhausted") {
		t.Fatalf("200 串流裡的 error 幀要報錯:%v", err)
	}
}

func TestChatFallsBackToJSONWhenNotEventStream(t *testing.T) {
	_, cfg := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"a\nb"}}]}`)
	})
	var lines []string
	full, err := cfg.Chat(context.Background(), ChatRequest{User: "u"}, func(l string) { lines = append(lines, l) })
	if err != nil || full != "a\nb" || strings.Join(lines, "|") != "a|b" {
		t.Fatalf("proxy 無視 stream 時整批解析:%v %q %q", err, full, lines)
	}
}

func TestChatRetriesWithMaxCompletionTokensOnce(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if strings.Contains(string(b), `"max_tokens"`) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Unsupported parameter: 'max_tokens'. Use 'max_completion_tokens' instead."}}`)
			return
		}
		sse(w, delta("fine"))
	}))
	t.Cleanup(srv.Close)
	cfg := Config{BaseURL: srv.URL, Model: "m"}
	full, err := cfg.Chat(context.Background(), ChatRequest{User: "u", MaxTokens: 3}, nil)
	if err != nil || full != "fine" {
		t.Fatalf("要換參數重試:%v %q", err, full)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[1], `"max_completion_tokens":3`) || strings.Contains(bodies[1], `"max_tokens"`) {
		t.Fatalf("第二個請求要帶 max_completion_tokens、不帶 max_tokens:%q", bodies)
	}
	// 換了參數還是 400:不再重試,原樣回。
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"max_completion_tokens too large"}}`)
	}))
	t.Cleanup(srv2.Close)
	cfg2 := Config{BaseURL: srv2.URL, Model: "m"}
	var he *HTTPError
	if _, err := cfg2.Chat(context.Background(), ChatRequest{User: "u", MaxTokens: 3}, nil); !errors.As(err, &he) || he.Status != 400 {
		t.Fatalf("兩次都 400 要回 HTTPError:%v", err)
	}
}

func TestHTTPErrorScrubsKeyShapedTokensAndTruncates(t *testing.T) {
	_, cfg := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"invalid api key sk-testkey-0123456789 provided"}`+strings.Repeat("x", 500))
	})
	_, err := cfg.Models(context.Background())
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 401 {
		t.Fatalf("要 HTTPError 401:%v", err)
	}
	if strings.Contains(err.Error(), "sk-testkey") || !strings.Contains(err.Error(), "sk-***") {
		t.Errorf("金鑰要遮掉:%v", err)
	}
	if len([]rune(he.Body)) > 200 {
		t.Errorf("body 要截到 200 字:%d", len([]rune(he.Body)))
	}
}

func TestModelsListAndNoListFallback(t *testing.T) {
	_, cfg := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-a"},{"id":""},{"id":"gpt-b"}]}`)
	})
	ids, err := cfg.Models(context.Background())
	if err != nil || strings.Join(ids, ",") != "gpt-a,gpt-b" {
		t.Fatalf("要照順序列 id、跳過空的:%v %v", ids, err)
	}
	for _, code := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		_, cfg := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
		if _, err := cfg.Models(context.Background()); !errors.Is(err, ErrNoModelList) {
			t.Errorf("%d 要回 ErrNoModelList:%v", code, err)
		}
	}
	_, cfg403 := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	var he *HTTPError
	if _, err := cfg403.Models(context.Background()); !errors.As(err, &he) || he.Status != 403 {
		t.Errorf("403 要原樣回 HTTPError:%v", err)
	}
}

func TestPingAcceptsEmptyContentAndChatDoesNot(t *testing.T) {
	c, cfg := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":""}}]}`)
	})
	if err := cfg.Ping(context.Background()); err != nil {
		t.Fatalf("1-token 探測回空字串也算通:%v", err)
	}
	if !strings.Contains(c.body[0], `"max_tokens":1`) || strings.Contains(c.body[0], `"stream"`) {
		t.Errorf("Ping 要 max_tokens 1、不串流:%s", c.body[0])
	}
	if _, err := cfg.ChatOnce(context.Background(), ChatRequest{User: "u"}); err == nil {
		t.Fatal("正式的 chat 回空字串是錯")
	}
}

func TestChatHonoursContextCancel(t *testing.T) {
	_, cfg := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := cfg.Chat(ctx, ChatRequest{User: "u"}, nil)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("ctx 到期要立刻回錯:%v(%s)", err, time.Since(start))
	}
}

func TestNormalizeBaseURLAndInsecureRemote(t *testing.T) {
	for in, want := range map[string]string{
		"  https://api.openai.com/v1/  ": "https://api.openai.com/v1",
		"http://localhost:11434/v1":      "http://localhost:11434/v1",
		"https://ai.example.com":         "https://ai.example.com",
	} {
		if got, err := NormalizeBaseURL(in); err != nil || got != want {
			t.Errorf("%q → %q %v,要 %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "api.openai.com/v1", "ftp://x/v1", "http://", "https:///v1"} {
		if _, err := NormalizeBaseURL(bad); err == nil {
			t.Errorf("%q 要被拒絕", bad)
		}
	}
	for in, want := range map[string]bool{
		"http://192.168.1.10:11434/v1": true,
		"http://ai.example.com/v1":     true,
		"http://localhost:11434/v1":    false,
		"http://127.0.0.1:8080/v1":     false,
		"http://[::1]:8080/v1":         false,
		"http://ollama.localhost/v1":   false,
		"https://ai.example.com/v1":    false,
	} {
		if got := InsecureRemote(in); got != want {
			t.Errorf("InsecureRemote(%q) = %v,要 %v", in, got, want)
		}
	}
}

func TestLanguageLabel(t *testing.T) {
	for in, want := range map[string][2]string{
		"ja":    {"Japanese (ja)", "ja"},
		"zh-TW": {"Chinese (Taiwan) (zh-TW)", "zh-TW"},
		"zh_tw": {"Chinese (Taiwan) (zh-TW)", "zh-TW"},
		" en ":  {"English (en)", "en"},
		"pt-BR": {"Brazilian Portuguese (pt-BR)", "pt-BR"},
	} {
		label, tag, err := LanguageLabel(in)
		if err != nil || label != want[0] || tag != want[1] {
			t.Errorf("%q → %q %q %v,要 %v", in, label, tag, err, want)
		}
	}
	for _, bad := range []string{"", "xx-YY", "not a language", "und"} {
		if _, _, err := LanguageLabel(bad); err == nil {
			t.Errorf("%q 要被拒絕", bad)
		}
	}
}

func TestLoadReadsConfigAndKeychain(t *testing.T) {
	t.Setenv("CAPY_CONFIG_DIR", t.TempDir())
	if _, err := Load(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("沒設定要回 ErrNotConfigured:%v", err)
	}
	if err := config.Save(&config.Config{AIBaseURL: "https://x/v1", AIModel: "m"}); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil || c.APIKey != "" || len(c.Headers) != 0 {
		t.Fatalf("keychain 沒有金鑰與標頭 = 空:%+v %v", c, err)
	}
	_ = secret.Set(KeyAPIKey, "k")
	_ = secret.Set(KeyHeaders, "X-A: 1\nX-B: 2")
	t.Cleanup(func() { _ = secret.Delete(KeyAPIKey); _ = secret.Delete(KeyHeaders) })
	c, err = Load()
	if err != nil || c.APIKey != "k" || c.Headers.Get("X-B") != "2" || c.BaseURL != "https://x/v1" || c.Model != "m" {
		t.Fatalf("Load:%+v %v", c, err)
	}
	if got := SentFields; strings.Join(got, ",") != "title,artist,album,release date,genre" {
		t.Errorf("SentFields 是政策的用詞,改了政策要一起改:%v", got)
	}
}
