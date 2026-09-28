package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"charm.land/huh/v2"

	"github.com/Tai-ch0802/capy-music/internal/ai"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// fakeAI:假的 OpenAI 相容端點。models 是 GET /models 的清單(modelsStatus 非 0 就只回那個狀態碼);
// chat 回 chatStatus(0 = 200 帶一個字)。記下每個請求的 header 與 body。
type fakeAI struct {
	srv          *httptest.Server
	mu           sync.Mutex
	reqs         []*http.Request
	bodies       []string
	models       []string
	modelsStatus int
	chatStatus   int
}

func newFakeAI(t *testing.T, models ...string) *fakeAI {
	t.Helper()
	f := &fakeAI{models: models}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, r)
		f.bodies = append(f.bodies, string(b))
		f.mu.Unlock()
		switch r.URL.Path {
		case "/v1/models":
			if f.modelsStatus != 0 {
				w.WriteHeader(f.modelsStatus)
				return
			}
			var data []map[string]string
			for _, m := range f.models {
				data = append(data, map[string]string{"id": m})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/v1/chat/completions":
			if f.chatStatus != 0 {
				w.WriteHeader(f.chatStatus)
				fmt.Fprint(w, `{"error":{"message":"nope"}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAI) base() string { return f.srv.URL + "/v1" }

func (f *fakeAI) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.URL.Path)
	}
	return out
}

// setWikiTest:乾淨 config + keychain,stdinIsTTY 預設 false(非互動),接縫還原。
func setWikiTest(t *testing.T) {
	t.Helper()
	setCLITestConfig(t)
	origTTY, origForm, origPick, origName := stdinIsTTY, wikiSetupForm, pickOne, promptNewName
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() {
		stdinIsTTY, wikiSetupForm, pickOne, promptNewName = origTTY, origForm, origPick, origName
		_ = secret.Delete(ai.KeyAPIKey)
		_ = secret.Delete(ai.KeyHeaders)
	})
}

func keychain(t *testing.T, key string) string {
	t.Helper()
	v, err := secret.Get(key)
	if errors.Is(err, secret.ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestWikiSetupFlagsSaveEverythingAndProbeCarriesHeaders(t *testing.T) {
	setWikiTest(t)
	f := newFakeAI(t, "gpt-a", "gpt-b")
	out, err := runCLI(t, "wiki", "setup", "--base-url", f.base()+"/", "--model", "gpt-b", "--api-key", "sk-secret-0123456789",
		"--header", "CF-Access-Client-Id: cid", "--header", "CF-Access-Client-Secret: csec", "--native-language", "zh_tw")
	if err != nil {
		t.Fatalf("setup:%v\n%s", err, out)
	}
	c, _ := config.Load()
	if c.AIBaseURL != f.base() || c.AIModel != "gpt-b" || c.NativeLanguage != "zh-TW" {
		t.Fatalf("config:%+v(base URL 去尾端 /、母語存正規化的代碼)", c)
	}
	if keychain(t, ai.KeyAPIKey) != "sk-secret-0123456789" || keychain(t, ai.KeyHeaders) != "CF-Access-Client-Id: cid\nCF-Access-Client-Secret: csec" {
		t.Fatalf("keychain:key=%q headers=%q", keychain(t, ai.KeyAPIKey), keychain(t, ai.KeyHeaders))
	}
	if strings.Contains(out, "sk-secret") || strings.Contains(out, "csec") {
		t.Fatalf("金鑰與標頭值不得印出:%s", out)
	}
	if !strings.Contains(out, "✅") || !strings.Contains(out, "gpt-b") || !strings.Contains(out, "zh-TW") {
		t.Fatalf("要印總結:%s", out)
	}
	if p := f.paths(); len(p) != 1 || p[0] != "/v1/models" {
		t.Fatalf("旗標給了 model、清單有它:只探測 /models,不打 chat:%v", p)
	}
	r := f.reqs[0]
	if r.Header.Get("Authorization") != "Bearer sk-secret-0123456789" || r.Header.Get("Cf-Access-Client-Secret") != "csec" {
		t.Fatalf("探測要帶 Bearer 與自訂標頭:%v", r.Header)
	}
}

func TestWikiSetupRejectsModelNotListedButKeepsEndpoint(t *testing.T) {
	setWikiTest(t)
	f := newFakeAI(t, "gpt-a")
	_, err := runCLI(t, "wiki", "setup", "--base-url", f.base(), "--model", "gpt-zzz")
	if err == nil || !strings.Contains(err.Error(), "gpt-zzz") || !strings.Contains(err.Error(), "gpt-a") {
		t.Fatalf("要說清單裡沒有、並列出有的:%v", err)
	}
	if c, _ := config.Load(); c.AIBaseURL != f.base() || c.AIModel != "" {
		t.Fatalf("端點要存、model 不動:%+v", c)
	}
	// 清單是空的:照收旗標給的 model。
	setWikiTest(t)
	f2 := newFakeAI(t)
	if _, err := runCLI(t, "wiki", "setup", "--base-url", f2.base(), "--model", "anything"); err != nil {
		t.Fatalf("空清單要照收:%v", err)
	}
	if c, _ := config.Load(); c.AIModel != "anything" {
		t.Fatalf("model:%+v", c)
	}
}

func TestWikiSetupWithoutTerminalExplainsFlags(t *testing.T) {
	setWikiTest(t)
	_, err := runCLI(t, "wiki", "setup")
	if err == nil || !strings.Contains(err.Error(), "--base-url") || !strings.Contains(err.Error(), "--model") {
		t.Fatalf("非 TTY 沒旗標要指路:%v", err)
	}
	if c, _ := config.Load(); c.AIBaseURL != "" {
		t.Fatal("不得落地")
	}
	f := newFakeAI(t, "gpt-a", "gpt-b")
	_, err = runCLI(t, "wiki", "setup", "--base-url", f.base())
	if err == nil || !strings.Contains(err.Error(), "--model") || !strings.Contains(err.Error(), "gpt-a") || !strings.Contains(err.Error(), "gpt-b") {
		t.Fatalf("非 TTY 沒 --model 要把清單印在錯誤裡:%v", err)
	}
	if c, _ := config.Load(); c.AIBaseURL != f.base() || c.AIModel != "" {
		t.Fatalf("端點存了、model 沒有:%+v", c)
	}
}

func TestWikiSetupKeepsSecretsUnlessFlagGiven(t *testing.T) {
	setWikiTest(t)
	f := newFakeAI(t, "m")
	if _, err := runCLI(t, "wiki", "setup", "--base-url", f.base(), "--model", "m", "--api-key", "k1", "--header", "X-A: 1"); err != nil {
		t.Fatal(err)
	}
	// 只換 model:金鑰與標頭要留著(最容易退化的一條)。
	if _, err := runCLI(t, "wiki", "setup", "--model", "m"); err != nil {
		t.Fatal(err)
	}
	if keychain(t, ai.KeyAPIKey) != "k1" || keychain(t, ai.KeyHeaders) != "X-A: 1" {
		t.Fatalf("沒給旗標要沿用:key=%q headers=%q", keychain(t, ai.KeyAPIKey), keychain(t, ai.KeyHeaders))
	}
	if f.reqs[len(f.reqs)-1].Header.Get("X-A") != "1" {
		t.Fatal("沿用的標頭要跟著探測送出")
	}
	// 空字串 = 清掉。
	if _, err := runCLI(t, "wiki", "setup", "--model", "m", "--api-key", "", "--header", ""); err != nil {
		t.Fatal(err)
	}
	if keychain(t, ai.KeyAPIKey) != "" || keychain(t, ai.KeyHeaders) != "" {
		t.Fatalf("空字串要清掉:key=%q headers=%q", keychain(t, ai.KeyAPIKey), keychain(t, ai.KeyHeaders))
	}
	if r := f.reqs[len(f.reqs)-1]; r.Header.Get("Authorization") != "" || r.Header.Get("X-A") != "" {
		t.Fatal("清掉之後探測不帶")
	}
}

func TestWikiSetupWizardThenPicker(t *testing.T) {
	setWikiTest(t)
	stdinIsTTY = func() bool { return true }
	f := newFakeAI(t, "gpt-a", "gpt-b", "gpt-c")
	var seen wikiSetupInput
	wikiSetupForm = func(in wikiSetupInput) (wikiSetupInput, error) {
		seen = in
		in.BaseURL, in.APIKey, in.HeadersText, in.Language = f.base(), "wizkey", "X-W: 1", "ja"
		return in, nil
	}
	var pickTitle string
	var pickLabels []string
	pickOne = func(title string, labels []string) (int, error) { pickTitle, pickLabels = title, labels; return 1, nil }
	promptNewName = func(string) (string, error) {
		t.Fatal("清單夠短、沒選手動輸入:不該問名字")
		return "", nil
	}
	out, err := runCLI(t, "wiki", "setup")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if seen.HadKey || seen.HadHeaders || seen.BaseURL != "" {
		t.Fatalf("第一次:表單要拿到「沒有已存的值」:%+v", seen)
	}
	if pickTitle == "" || len(pickLabels) != 4 || pickLabels[3] == "" || pickLabels[1] != "gpt-b" {
		t.Fatalf("挑選器:清單 + 最後一項手動輸入:%q %v", pickTitle, pickLabels)
	}
	c, _ := config.Load()
	if c.AIModel != "gpt-b" || c.NativeLanguage != "ja" || keychain(t, ai.KeyAPIKey) != "wizkey" || keychain(t, ai.KeyHeaders) != "X-W: 1" {
		t.Fatalf("精靈的值要存:%+v key=%q", c, keychain(t, ai.KeyAPIKey))
	}
	// 第二次:表單要知道已經有金鑰與標頭;留空 = 沿用;選「手動輸入」→ promptNewName。
	wikiSetupForm = func(in wikiSetupInput) (wikiSetupInput, error) {
		seen = in
		in.APIKey, in.HeadersText = "", ""
		return in, nil
	}
	pickOne = func(_ string, labels []string) (int, error) { return len(labels) - 1, nil }
	promptNewName = func(string) (string, error) { return "typed-model", nil }
	if _, err := runCLI(t, "wiki", "setup"); err != nil {
		t.Fatal(err)
	}
	if !seen.HadKey || !seen.HadHeaders || seen.BaseURL != f.base() || seen.Language != "ja" {
		t.Fatalf("第二次:表單要預填現值並知道有已存的秘密:%+v", seen)
	}
	c, _ = config.Load()
	if c.AIModel != "typed-model" || keychain(t, ai.KeyAPIKey) != "wizkey" || keychain(t, ai.KeyHeaders) != "X-W: 1" {
		t.Fatalf("留空要沿用、手打的 model 要存:%+v key=%q", c, keychain(t, ai.KeyAPIKey))
	}
	// 清單太長:不開挑選器、直接問名字。
	f.models = make([]string, ai.MaxModelsForPicker+1)
	for i := range f.models {
		f.models[i] = fmt.Sprintf("m%d", i)
	}
	pickOne = func(string, []string) (int, error) { t.Fatal("超過上限不該開挑選器"); return 0, nil }
	promptNewName = func(title string) (string, error) {
		if !strings.Contains(title, fmt.Sprint(ai.MaxModelsForPicker+1)) {
			t.Errorf("手打的標題要說有幾個:%q", title)
		}
		return "m7", nil
	}
	if _, err := runCLI(t, "wiki", "setup"); err != nil {
		t.Fatal(err)
	}
	if c, _ := config.Load(); c.AIModel != "m7" {
		t.Fatalf("model:%+v", c)
	}
	// 取消精靈:什麼都不動。
	wikiSetupForm = func(in wikiSetupInput) (wikiSetupInput, error) { return in, huh.ErrUserAborted }
	if _, err := runCLI(t, "wiki", "setup"); !errors.Is(err, huh.ErrUserAborted) {
		t.Fatalf("取消要原樣回:%v", err)
	}
	if c, _ := config.Load(); c.AIModel != "m7" {
		t.Fatal("取消不得改設定")
	}
}

func TestWikiSetupNoModelListPingsTheTypedModel(t *testing.T) {
	setWikiTest(t)
	f := newFakeAI(t)
	f.modelsStatus = http.StatusNotFound
	if _, err := runCLI(t, "wiki", "setup", "--base-url", f.base(), "--model", "local-llm"); err != nil {
		t.Fatalf("404 要退回 1-token chat 驗:%v", err)
	}
	if p := f.paths(); len(p) != 2 || p[1] != "/v1/chat/completions" || !strings.Contains(f.bodies[1], `"model":"local-llm"`) || !strings.Contains(f.bodies[1], `"max_tokens":1`) {
		t.Fatalf("要用旗標給的 model 打 1 token:%v %q", p, f.bodies)
	}
	if c, _ := config.Load(); c.AIModel != "local-llm" {
		t.Fatalf("model:%+v", c)
	}
	// 沒有 --model、沒有終端機:指路。
	setWikiTest(t)
	f2 := newFakeAI(t)
	f2.modelsStatus = http.StatusMethodNotAllowed
	if _, err := runCLI(t, "wiki", "setup", "--base-url", f2.base()); err == nil || !strings.Contains(err.Error(), "--model") {
		t.Fatalf("要指路 --model:%v", err)
	}
	// 1-token 失敗:端點存了、model 沒有。
	f2.chatStatus = http.StatusUnauthorized
	_, err := runCLI(t, "wiki", "setup", "--base-url", f2.base(), "--model", "x")
	if err == nil || !strings.Contains(err.Error(), "x") || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("要說測試請求失敗:%v", err)
	}
	if c, _ := config.Load(); c.AIBaseURL != f2.base() || c.AIModel != "" {
		t.Fatalf("端點存了、model 沒有:%+v", c)
	}
}

func TestWikiSetupProbeRefusedKeepsSettings(t *testing.T) {
	setWikiTest(t)
	f := newFakeAI(t, "m")
	f.modelsStatus = http.StatusForbidden
	_, err := runCLI(t, "wiki", "setup", "--base-url", f.base(), "--model", "m", "--api-key", "k")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "capy wiki setup") {
		t.Fatalf("要照實回並指路:%v", err)
	}
	if c, _ := config.Load(); c.AIBaseURL != f.base() || c.AIModel != "" {
		t.Fatalf("端點存了、model 沒有:%+v", c)
	}
	if keychain(t, ai.KeyAPIKey) != "k" {
		t.Fatal("金鑰要存")
	}
	// 標頭壞掉在存之前就講。
	_, err = runCLI(t, "wiki", "setup", "--base-url", f.base(), "--model", "m", "--header", "no-colon")
	if err == nil || !strings.Contains(err.Error(), "Name: value") {
		t.Fatalf("壞標頭:%v", err)
	}
	_, err = runCLI(t, "wiki", "setup", "--base-url", f.base(), "--model", "m", "--header", "X-Big: "+strings.Repeat("v", ai.MaxHeadersBytes))
	if err == nil || !strings.Contains(err.Error(), fmt.Sprint(ai.MaxHeadersBytes)) {
		t.Fatalf("太大的標頭:%v", err)
	}
	if keychain(t, ai.KeyHeaders) != "" {
		t.Fatal("壞標頭不得落地")
	}
	// keychain 裡的舊標頭壞掉(別的版本寫的):在存任何東西之前就講,ai_base_url 不動;給 --header 換掉它就好。
	setWikiTest(t)
	_ = secret.Set(ai.KeyHeaders, "garbage without colon")
	_, err = runCLI(t, "wiki", "setup", "--base-url", f.base(), "--model", "m")
	if err == nil || !strings.Contains(err.Error(), "Name: value") {
		t.Fatalf("舊標頭壞掉要講:%v", err)
	}
	if c, _ := config.Load(); c.AIBaseURL != "" {
		t.Fatal("舊標頭壞掉時不得先把端點存下來")
	}
	f.modelsStatus = 0 // 上面把探測設成 403 了;這一段看的是標頭,讓探測過
	if _, err := runCLI(t, "wiki", "setup", "--base-url", f.base(), "--model", "m", "--header", "X-Fixed: 1"); err != nil {
		t.Fatalf("給新標頭就換掉壞的:%v", err)
	}
	if keychain(t, ai.KeyHeaders) != "X-Fixed: 1" {
		t.Fatal("新標頭要存")
	}
}

func TestConfigSetWikiKeys(t *testing.T) {
	setCLITestConfig(t)
	out, err := runCLI(t, "config", "set", "ai_base_url", "http://10.0.0.7:11434/v1/")
	if err != nil || !strings.Contains(out, "ai_base_url = http://10.0.0.7:11434/v1\n") {
		t.Fatalf("要正規化(去尾端 /):%v %q", err, out)
	}
	if !strings.Contains(out, "http://10.0.0.7:11434/v1") || strings.Count(out, "http://10.0.0.7:11434/v1") < 2 {
		t.Fatalf("明文 http 打到別台電腦要警告一行(stderr):%q", out)
	}
	out, err = runCLI(t, "config", "set", "ai_base_url", "http://localhost:11434/v1")
	if err != nil || strings.Count(out, "localhost") != 1 {
		t.Fatalf("本機不警告:%v %q", err, out)
	}
	if _, err := runCLI(t, "config", "set", "ai_base_url", "api.openai.com/v1"); err == nil {
		t.Fatal("沒有 scheme 要拒絕")
	}
	if _, err := runCLI(t, "config", "set", "ai_model", "  "); err == nil {
		t.Fatal("空的 model 要拒絕")
	}
	if out, err := runCLI(t, "config", "set", "ai_model", " gpt-x "); err != nil || out != "ai_model = gpt-x\n" {
		t.Fatalf("model trim:%v %q", err, out)
	}
	if out, err := runCLI(t, "config", "set", "native_language", "zh_tw"); err != nil || out != "native_language = zh-TW\n" {
		t.Fatalf("母語存正規化的代碼:%v %q", err, out)
	}
	if _, err := runCLI(t, "config", "set", "native_language", "xx-YY"); err == nil {
		t.Fatal("不存在的語言要拒絕")
	}
	if out, err := runCLI(t, "config", "set", "native_language", ""); err != nil || out != "native_language = \n" {
		t.Fatalf("空字串 = 清掉:%v %q", err, out)
	}
	if out, _ := runCLI(t, "config", "get", "ai_model"); out != "gpt-x\n" {
		t.Fatalf("get:%q", out)
	}
	c, _ := config.Load()
	if c.AIBaseURL != "http://localhost:11434/v1" || c.AIModel != "gpt-x" || c.NativeLanguage != "" {
		t.Fatalf("config:%+v", c)
	}
}

// TestWebWikiSetupUsesPromptBridge:web 的 wiki setup 走提示橋——表單(金鑰與標頭是 secret、標頭是 multiline)→ 選 model;
// 金鑰與標頭的值只在 answer body 裡進行程,任何事件都不能帶著它們;--api-key / --header 在 HTTP 上 403。
func TestWebWikiSetupUsesPromptBridge(t *testing.T) {
	setWikiTest(t)
	f := newFakeAI(t, "gpt-a", "gpt-b")
	_, c := startWeb(t)
	if code := c.status(http.MethodPost, "/api/run", map[string]any{"args": []string{"wiki", "setup", "--api-key", "k"}}, nil); code != http.StatusForbidden {
		t.Fatalf("--api-key 要 403,得到 %d", code)
	}
	if code := c.status(http.MethodPost, "/api/run", map[string]any{"args": []string{"wiki", "setup", "--header", "X: y"}}, nil); code != http.StatusForbidden {
		t.Fatalf("--header 要 403,得到 %d", code)
	}
	var form map[string]any
	events := c.runInteractive(map[string]any{"args": []string{"wiki", "setup"}}, func(n int, _ string, p map[string]any) *promptReply {
		switch n {
		case 1:
			form = p
			return reply(map[string]string{"base_url": f.base(), "api_key": "sk-web-0123456789", "headers": "X-A: 1\nX-B: 2", "language": "ko"})
		case 2:
			if p["kind"] != "select" {
				t.Errorf("第二題要選 model:%v", p)
			}
			return reply(1)
		}
		return dismiss()
	})
	if form == nil || form["kind"] != "form" {
		t.Fatalf("第一題要是表單:%v", form)
	}
	fields := form["fields"].([]any)
	byName := map[string]map[string]any{}
	for _, x := range fields {
		fm := x.(map[string]any)
		byName[fm["name"].(string)] = fm
	}
	if byName["api_key"]["secret"] != true || byName["headers"]["secret"] != true || byName["headers"]["multiline"] != true || byName["base_url"]["secret"] == true {
		t.Fatalf("欄位形狀:%v", byName)
	}
	if byName["api_key"]["filled"] == true || byName["headers"]["filled"] == true {
		t.Fatalf("第一次、keychain 什麼都沒有:secret 欄不可以標 filled:%v", byName)
	}
	all, _ := json.Marshal(events)
	if strings.Contains(string(all), "sk-web") || strings.Contains(string(all), "X-B: 2") {
		t.Fatalf("金鑰與標頭值不得出現在任何事件裡:%s", all)
	}
	if !strings.Contains(evText(events, "stdout"), "✅") {
		t.Fatalf("要印總結:%s", evText(events, "stdout"))
	}
	cfg, _ := config.Load()
	if cfg.AIBaseURL != f.base() || cfg.AIModel != "gpt-b" || cfg.NativeLanguage != "ko" {
		t.Fatalf("config:%+v", cfg)
	}
	if keychain(t, ai.KeyAPIKey) != "sk-web-0123456789" || keychain(t, ai.KeyHeaders) != "X-A: 1\nX-B: 2" {
		t.Fatalf("keychain:key=%q headers=%q", keychain(t, ai.KeyAPIKey), keychain(t, ai.KeyHeaders))
	}
	// 再跑一次:secret 欄要標「已填」、留空 = 沿用。
	events = c.runInteractive(map[string]any{"args": []string{"wiki", "setup"}}, func(n int, _ string, p map[string]any) *promptReply {
		if n == 1 {
			for _, x := range p["fields"].([]any) {
				fm := x.(map[string]any)
				if fm["secret"] == true && fm["filled"] != true {
					t.Errorf("已存的 secret 欄要標 filled:%v", fm)
				}
				if fm["name"] == "base_url" && fm["value"] != f.base() {
					t.Errorf("非 secret 欄要預填:%v", fm)
				}
				if fm["name"] == "language" && fm["value"] != "ko" {
					t.Errorf("母語也要預填:%v", fm)
				}
			}
			return reply(map[string]string{"base_url": f.base(), "api_key": "", "headers": "", "language": ""})
		}
		return reply(0)
	})
	if !strings.Contains(evText(events, "stdout"), "gpt-a") || keychain(t, ai.KeyAPIKey) != "sk-web-0123456789" || keychain(t, ai.KeyHeaders) != "X-A: 1\nX-B: 2" {
		t.Fatalf("留空要沿用:%s key=%q", evText(events, "stdout"), keychain(t, ai.KeyAPIKey))
	}
	// 壞的 base URL:同一題帶 Error 重問。
	asked := 0
	_ = c.runInteractive(map[string]any{"args": []string{"wiki", "setup"}}, func(n int, _ string, p map[string]any) *promptReply {
		asked = n
		if n == 1 {
			return reply(map[string]string{"base_url": "not a url", "api_key": "", "headers": "", "language": ""})
		}
		if n == 2 && p["error"] == nil {
			t.Errorf("重問要帶 error:%v", p)
		}
		return dismiss()
	})
	if asked < 2 {
		t.Fatal("驗證失敗要重問")
	}
}

func TestWikiSetupEnglish(t *testing.T) {
	setWikiTest(t)
	withLanguage(t, "en")
	f := newFakeAI(t, "gpt-a")
	out, err := runCLI(t, "wiki", "setup", "--base-url", f.base(), "--model", "gpt-a", "--api-key", "k", "--header", "X-A: 1")
	if err != nil {
		t.Fatal(err)
	}
	want := "Checking the endpoint " + f.base() + "…\n" +
		"✅ AI endpoint saved: " + f.base() + " · model gpt-a · native language not set (follows the interface language, " + i18n.Default() + ") · API key in the keychain · custom headers 1 line\n" // 測試二進位的預設語系是 zh-TW(i18n.Default)
	if out != want || hasCJK(out) {
		t.Fatalf("英文輸出:\n%q\n要\n%q", out, want)
	}
	_, err = runCLI(t, "wiki", "setup", "--base-url", f.base(), "--model", "nope")
	if err == nil || err.Error() != "the endpoint doesn't list a model named nope (it offers: gpt-a)" {
		t.Fatalf("英文錯誤:%v", err)
	}
	if _, err := runCLI(t, "wiki", "setup", "--base-url", f.base(), "--model", "gpt-a", "--native-language", "xx-YY"); err == nil || hasCJK(err.Error()) {
		t.Fatalf("英文的母語錯誤:%v", err)
	}
}
