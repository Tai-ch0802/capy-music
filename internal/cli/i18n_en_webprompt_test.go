package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/config"
)

// 英文模式:web_prompt.go 的提示橋(T2c)。提示的標題、按鈕、欄位名與 CLI 的 huh 表單共用同一批 key,兩邊不會漂。

func noteTitle(p map[string]any) string {
	n, _ := p["note"].(map[string]any)
	s, _ := n["title"].(string)
	return s
}

func fieldLabels(p map[string]any) map[string]string {
	out := map[string]string{}
	for _, f := range p["fields"].([]any) {
		m := f.(map[string]any)
		out[m["name"].(string)] = m["label"].(string)
	}
	return out
}

func TestWebPromptValidateAnswerEnglish(t *testing.T) {
	withLanguage(t, "en")
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	for _, c := range []struct {
		p    webPrompt
		v    string
		want string
	}{
		{webPrompt{Kind: "confirm"}, `"x"`, "a confirm answer must be true or false"},
		{webPrompt{Kind: "select", Options: []string{"a", "b", "c"}}, `3`, "a select answer must be an integer from 0 to 2"},
		{webPrompt{Kind: "input"}, `1`, "an input answer must be a string"},
		{webPrompt{Kind: "form"}, `[]`, "a form answer must be an object mapping field names to strings"},
		{webPrompt{Kind: "form", Fields: []webField{{Name: "dev"}}}, `{}`, "the form answer is missing field dev"},
		{webPrompt{Kind: "nope"}, `1`, "unknown prompt kind"},
	} {
		if err := validateAnswer(c.p, webAnswer{Value: raw(c.v)}); err == nil || err.Error() != c.want {
			t.Errorf("%s %s:%v,要 %q", c.p.Kind, c.v, err, c.want)
		}
	}
}

func TestWebPromptNoJobErrorsEnglish(t *testing.T) {
	withLanguage(t, "en")
	s, _ := startWeb(t)
	if _, err := s.ask(webPrompt{Kind: "confirm"}); err == nil || err.Error() != "web prompts can only appear while a command is running (no current job)" {
		t.Errorf("ask:%v", err)
	}
	if err := s.webOpenBrowser("https://example.com"); err == nil || err.Error() != "no current job" {
		t.Errorf("openBrowser:%v", err)
	}
	for _, c := range []struct {
		body string
		code int
		want string
	}{
		{`{`, http.StatusBadRequest, "malformed JSON: unexpected EOF"},
		{`{"id":1}`, http.StatusNotFound, "no such job (already finished?)"},
	} {
		w := httptest.NewRecorder()
		s.handleAnswer(w, httptest.NewRequest(http.MethodPost, "/api/jobs/x/answer", strings.NewReader(c.body)))
		var e struct{ Error string }
		_ = json.NewDecoder(w.Body).Decode(&e)
		if w.Code != c.code || e.Error != c.want {
			t.Errorf("%s → %d %q,要 %d %q", c.body, w.Code, e.Error, c.code, c.want)
		}
	}
}

// pl pull 的確認閘:按鈕與 confirmWrite(pull.go)同一組 key;id 不符的 409 訊息也是英文。
func TestWebConfirmWriteEnglish(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "commute", "t1")
	mustPull(t, "pl", "link", "commute", "spotify:p1")
	webEnglish(t)
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"pl", "pull", "commute"}}, func(n int, job string, p map[string]any) *promptReply {
		if p["kind"] != "confirm" || p["affirmative"] != "Apply" || p["negative"] != "Cancel" || hasCJK(p["title"].(string)) {
			t.Errorf("第 %d 題:%v", n, p)
		}
		resp := c.req(context.Background(), http.MethodPost, "/api/jobs/"+job+"/answer", map[string]any{"id": 999, "value": true}, nil)
		var e struct{ Error string }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict || e.Error != "no prompt is waiting, or the id doesn't match (late answers are rejected)" {
			t.Errorf("id 不符:%d %q", resp.StatusCode, e.Error)
		}
		return reply(false)
	})
	if len(prompts(ev)) != 1 {
		t.Fatalf("只問一次:%v", ev)
	}
}

func TestWebAppleWizardEnglish(t *testing.T) {
	clearAppleTokens(t)
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "")
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")
	dev := fakeJWT(t, time.Now().Add(24*time.Hour))
	expired := fakeJWT(t, time.Now().Add(-time.Hour))
	appleServer(t, dev, "MUT1")
	webEnglish(t)
	_, c := startWeb(t)

	disclosure := func(n int, p map[string]any) {
		t.Helper()
		if p["kind"] != "confirm" || p["title"] != "I have read this and accept the risk. Continue?" || noteTitle(p) != "Read this first" ||
			p["affirmative"] != "Agree" || p["negative"] != "Cancel" {
			t.Errorf("第 %d 題要是英文的揭露:%v", n, p)
		}
	}
	ev := c.runInteractive(map[string]any{"args": []string{"auth", "login", "apple"}}, func(n int, _ string, p map[string]any) *promptReply {
		disclosure(n, p)
		return reply(false)
	})
	if ex := evExit(t, ev); ex["code"] != float64(1) || !strings.Contains(ex["message"].(string), "cancelled (disclosure not accepted)") {
		t.Fatalf("不同意:%v", ex)
	}

	wantLabels := map[string]string{"dev": "developer token (value of the authorization header)", "user": "user token (value of the media-user-token header)"}
	ev = c.runInteractive(map[string]any{"args": []string{"auth", "login", "apple"}}, func(n int, _ string, p map[string]any) *promptReply {
		switch n {
		case 1:
			disclosure(n, p)
			return reply(true)
		case 2:
			if p["title"] != "Paste the tokens" || noteTitle(p) != "Copy the tokens from the web player" || !reflect.DeepEqual(fieldLabels(p), wantLabels) {
				t.Errorf("表單:%v", p)
			}
			return reply(map[string]any{"dev": dev, "user": " "})
		case 3:
			if p["error"] != "user token: can't be empty" {
				t.Errorf("空 user token:%v", p["error"])
			}
			return reply(map[string]any{"dev": expired, "user": "MUT1"})
		case 4:
			if e, _ := p["error"].(string); !strings.HasPrefix(e, "developer token: ") || hasCJK(e) {
				t.Errorf("過期 dev token:%q", e)
			}
			return reply(map[string]any{"dev": dev, "user": ""})
		}
		t.Errorf("多出來的第 %d 題:%v", n, p)
		return nil
	})
	if ex := evExit(t, ev); ex["code"] != float64(0) {
		t.Fatalf("同意 → 表單 → 落地:%v", ex)
	}
}

func TestWebAppleOnlyDevQuestionEnglish(t *testing.T) {
	setupAppleTokens(t)
	webEnglish(t)
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"auth", "login", "apple"}}, func(n int, _ string, p map[string]any) *promptReply {
		if n == 1 {
			return reply(true)
		}
		if p["title"] != "The keychain already has a user token. Update only the developer token?" ||
			p["affirmative"] != "Only the developer token" || p["negative"] != "Paste both again" {
			t.Errorf("第 %d 題:%v", n, p)
		}
		return dismiss()
	})
	if len(prompts(ev)) != 2 {
		t.Fatalf("揭露 → 只更新 dev?:%v", ev)
	}
}

func TestWebGoogleWizardEnglish(t *testing.T) {
	setGoogleTest(t)
	googleLoginFn = fakeGoogleLogin(t, "wiz.apps.googleusercontent.com", "", "w@x")
	webEnglish(t)
	_, c := startWeb(t)
	want := map[string]string{
		"client_id":     "Client ID (usually ends in .apps.googleusercontent.com)",
		"client_secret": "Client secret (may be left empty to try without one; acceptance check G-0 will settle whether a Desktop client needs it)",
	}
	ev := c.runInteractive(map[string]any{"args": []string{"auth", "login", "google"}}, func(n int, _ string, p map[string]any) *promptReply {
		if noteTitle(p) != "Google Drive sync: create your own OAuth client first" || !reflect.DeepEqual(fieldLabels(p), want) {
			t.Errorf("第 %d 題:%v", n, p)
		}
		if n == 1 {
			return reply(map[string]any{"client_id": " ", "client_secret": ""})
		}
		if p["error"] != "Client ID: required" {
			t.Errorf("空 client id:%v", p["error"])
		}
		return reply(map[string]any{"client_id": "wiz.apps.googleusercontent.com", "client_secret": ""})
	})
	if ex := evExit(t, ev); ex["code"] != float64(0) || len(prompts(ev)) != 2 {
		t.Fatalf("%v", ex)
	}
}

func TestWebGoogleSecretPromptEnglish(t *testing.T) {
	setGoogleTest(t)
	cfg, _ := config.Load()
	cfg.GoogleClientID = "byo.apps.googleusercontent.com"
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	googleLoginFn = fakeGoogleLogin(t, "byo.apps.googleusercontent.com", "re-pasted", "a@b")
	webEnglish(t)
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"auth", "login", "google"}}, func(n int, _ string, p map[string]any) *promptReply {
		note, _ := p["note"].(map[string]any)
		body, _ := note["body"].(string)
		if noteTitle(p) != "No secret found for this client" || !strings.HasPrefix(body, "Config still has a Google client ID (") ||
			!strings.Contains(body, "just press Enter to try without one") || hasCJK(body) {
			t.Errorf("第 %d 題:%v", n, p)
		}
		return reply(map[string]any{"client_secret": "re-pasted"})
	})
	if ex := evExit(t, ev); ex["code"] != float64(0) || len(prompts(ev)) != 1 {
		t.Fatalf("%v", ex)
	}
}

// resolve --review:第一層的標題與選項來自 reviewMenu(與終端機同一份),搜尋與候選也是同一批 key;只有候選標題是 web 自己的。
func TestWebReviewPromptEnglish(t *testing.T) {
	fs, _, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a-live", Name: "song-a (Live)", ISRC: "TW00000000ZY"})
	webEnglish(t)
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"resolve", "--review"}}, func(n int, _ string, p map[string]any) *promptReply {
		title, _ := p["title"].(string)
		switch n {
		case 1:
			opts := p["options"].([]any)
			if !strings.HasPrefix(title, "[1/2] ") || hasCJK(title) ||
				!reflect.DeepEqual(opts[len(opts)-3:], []any{"Skip (ask again next time)", "Search manually", "This platform doesn't have it (pin as unavailable)"}) {
				t.Errorf("第一層:%v", p)
			}
			return reply(optionIndex(t, p, "Search manually"))
		case 2:
			if p["kind"] != "input" || title != "Search for a track" {
				t.Errorf("搜尋字串:%v", p)
			}
			return reply(p["default"])
		case 3:
			opts := p["options"].([]any)
			if title != "Pick a track to pin (close to skip)" || len(opts) == 0 || !strings.HasPrefix(opts[0].(string), "score ") {
				t.Errorf("候選:%v", p)
			}
			return dismiss()
		}
		return dismiss()
	})
	if len(prompts(ev)) < 3 {
		t.Fatalf("三段都要問到:%v", ev)
	}
}
