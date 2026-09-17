package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

type promptReply struct {
	cancel bool
	value  any
}

func reply(v any) *promptReply { return &promptReply{value: v} }
func dismiss() *promptReply    { return &promptReply{cancel: true} }

// runInteractive:邊讀 SSE 邊回答提示。answer 收到第 n 題(從 1 起)、job id、prompt 事件;回 nil = 不回答(留給逾時 / 斷線)。
func (c *webClient) runInteractive(body any, answer func(n int, job string, p map[string]any) *promptReply) []map[string]any {
	c.t.Helper()
	resp := c.req(context.Background(), http.MethodPost, "/api/run", body, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("status %d", resp.StatusCode)
	}
	var out []map[string]any
	job, n := "", 0
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			c.t.Fatal(err)
		}
		out = append(out, ev)
		switch ev["type"] {
		case "start":
			job = ev["job"].(string)
		case "prompt":
			n++
			if r := answer(n, job, ev); r != nil {
				c.answer(job, int(ev["id"].(float64)), r)
			}
		}
	}
	return out
}

func (c *webClient) answer(job string, id int, r *promptReply) {
	c.t.Helper()
	if st := c.status(http.MethodPost, "/api/jobs/"+job+"/answer", map[string]any{"id": id, "cancel": r.cancel, "value": r.value}, nil); st != http.StatusNoContent {
		c.t.Fatalf("回答提示 %d:%d", id, st)
	}
}

func prompts(events []map[string]any) []map[string]any {
	var out []map[string]any
	for _, e := range events {
		if e["type"] == "prompt" {
			out = append(out, e)
		}
	}
	return out
}

func optionIndex(t *testing.T, p map[string]any, substr string) int {
	t.Helper()
	for i, o := range p["options"].([]any) {
		if strings.Contains(o.(string), substr) {
			return i
		}
	}
	t.Fatalf("選項裡沒有 %q:%v", substr, p["options"])
	return -1
}

func fieldNames(p map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, f := range p["fields"].([]any) {
		m := f.(map[string]any)
		out[m["name"].(string)] = m["secret"] == true
	}
	return out
}

// ── 確認閘(pl pull)──

// TestWebPlPullConfirmBridgeApplies:【fails-before-fix】bothTTY 還是 false 時直接 exit 2、沒有 prompt 事件。
func TestWebPlPullConfirmBridgeApplies(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1", "t2")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	before := driveFiles(t, dc)
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"pl", "pull", "通勤"}}, func(n int, _ string, p map[string]any) *promptReply {
		if p["kind"] != "confirm" || !strings.Contains(p["title"].(string), "套用以上 2 筆") || p["affirmative"] != "套用" || p["negative"] != "取消" {
			t.Errorf("第 %d 題:%v", n, p)
		}
		return reply(true)
	})
	if len(prompts(ev)) != 1 || evFirst(ev, "table") == nil {
		t.Fatalf("先送變更集表格、再問一次:%v", ev)
	}
	if ex := evExit(t, ev); ex["code"] != float64(0) || sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("確認 → 套用、Drive 要有變更:%v", ex)
	}
	if pc := evFirst(ev, "prompt_closed"); pc == nil || pc["reason"] != "answered" {
		t.Errorf("prompt_closed answered:%v", pc)
	}
}

func TestWebConfirmWriteNoIsPendingExit2ZeroWrite(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	before := driveFiles(t, dc)
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"pl", "pull", "通勤"}}, func(int, string, map[string]any) *promptReply { return reply(false) })
	if ex := evExit(t, ev); ex["code"] != float64(2) || !strings.Contains(ex["message"].(string), "待套用") || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("按「取消」= (false, nil) → PendingError exit 2、零寫入:%v", ex)
	}
}

func TestWebConfirmDismissIsUserAbortedExit1(t *testing.T) {
	fs, dc, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	before := driveFiles(t, dc)
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"pl", "pull", "通勤"}}, func(int, string, map[string]any) *promptReply { return dismiss() })
	if ex := evExit(t, ev); ex["code"] != float64(1) || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("關掉 = huh.ErrUserAborted 原樣往上 → exit 1、零寫入:%v", ex)
	}
	if pc := evFirst(ev, "prompt_closed"); pc == nil || pc["reason"] != "dismissed" {
		t.Errorf("prompt_closed dismissed:%v", pc)
	}
}

// ── 挑選器 ──

func TestWebPickOneCancelIsErrCancelledExit1(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"pl", "show"}}, func(n int, _ string, p map[string]any) *promptReply {
		if p["kind"] != "select" || len(p["options"].([]any)) == 0 {
			t.Errorf("pl show 無參數在 web 開挑選器:%v", p)
		}
		return dismiss()
	})
	if ex := evExit(t, ev); ex["code"] != float64(1) || !strings.Contains(ex["message"].(string), "已取消") {
		t.Fatalf("取消挑選器 = errCancelled exit 1:%v", ex)
	}
}

func TestWebPlLinkPickChainThenNewName(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"pl", "link"}}, linkChain(t, "上班"))
	if ex := evExit(t, ev); ex["code"] != float64(0) || !strings.Contains(evText(ev, "stdout"), "已連結 上班") || len(prompts(ev)) < 3 {
		t.Fatalf("挑選鏈(平台 → 平台清單 → 建立新的 → 名字):%v %q", ex, evText(ev, "stdout"))
	}
}

// linkChain:pl link 無參數的挑選鏈,用偏好挑而不綁段數:平台選 spotify、平台清單選「通勤」、canonical 選「建立」、名字打 name。
func linkChain(t *testing.T, name string) func(int, string, map[string]any) *promptReply {
	return func(n int, _ string, p map[string]any) *promptReply {
		if p["kind"] == "input" {
			return reply(name)
		}
		if p["kind"] != "select" {
			t.Errorf("第 %d 題:%v", n, p)
			return dismiss()
		}
		for _, want := range []string{"通勤", "建立", "spotify"} {
			for i, o := range p["options"].([]any) {
				if strings.Contains(o.(string), want) {
					return reply(i)
				}
			}
		}
		t.Errorf("第 %d 題沒有可挑的:%v", n, p["options"])
		return dismiss()
	}
}

func TestWebPromptNewNameEmptyIsCancelled(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"pl", "link"}}, linkChain(t, "   "))
	if ex := evExit(t, ev); ex["code"] != float64(1) || !strings.Contains(ex["message"].(string), "已取消") {
		t.Fatalf("名字 TrimSpace 後空 = errCancelled:%v", ex)
	}
}

func TestWebPlayAmbiguousOpensPickerViaPickOne(t *testing.T) {
	f := newPlayFake(t)
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"play", "派對"}}, func(n int, _ string, p map[string]any) *promptReply {
		if p["kind"] != "select" || len(p["options"].([]any)) != 3 {
			t.Errorf("歧義 → 三個候選:%v", p)
		}
		return reply(2)
	})
	if ex := evExit(t, ev); ex["code"] != float64(0) || len(f.played) != 1 || f.played[0].TrackIDs[0] != "t2" {
		t.Fatalf("選第三個就播它:%v played=%+v", ex, f.played)
	}
}

// ── 逾時、id 簿記、斷線 ──

// TestWebPromptTimeoutCancelsWholeJob:【fails-before-fix】逾時砍整個 job:resolve --review 兩筆(a、b 都找不到候選)
// 第一題不回答 → 不問第二題、整輪不寫入、pull.lock 立刻可取。
func TestWebPromptTimeoutCancelsWholeJob(t *testing.T) {
	_, dc, _ := resolveWorld(t)
	before := driveFiles(t, dc)
	orig := webPromptTimeout
	webPromptTimeout = 50 * time.Millisecond
	t.Cleanup(func() { webPromptTimeout = orig })
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"resolve", "--review"}}, func(int, string, map[string]any) *promptReply { return nil })
	if n := len(prompts(ev)); n != 1 {
		t.Errorf("逾時後不問第二筆:問了 %d 題", n)
	}
	if ex := evExit(t, ev); ex["reason"] != "timeout" || ex["code"] == float64(0) {
		t.Errorf("exit 帶 reason timeout、非 0:%v", ex)
	}
	if pc := evFirst(ev, "prompt_closed"); pc == nil || pc["reason"] != "timeout" {
		t.Errorf("prompt_closed timeout:%v", pc)
	}
	if !sameFiles(before, driveFiles(t, dc)) {
		t.Error("整輪不寫入")
	}
	lctx, lcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer lcancel()
	unlock, err := auth.LockFile(lctx, "pull.lock", "測試")
	if err != nil {
		t.Fatalf("逾時後 pull.lock 要立刻放掉:%v", err)
	}
	unlock()
}

func TestWebAnswerWrongIDIs409AndLateAnswerIsDropped(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"pl", "pull", "通勤"}}, func(_ int, job string, p map[string]any) *promptReply {
		id := int(p["id"].(float64))
		body := func(i int, v any) map[string]any { return map[string]any{"id": i, "cancel": false, "value": v} }
		if st := c.status(http.MethodPost, "/api/jobs/"+job+"/answer", body(id+7, false), nil); st != http.StatusConflict {
			t.Errorf("id 不符 → 409,得到 %d", st)
		}
		if st := c.status(http.MethodPost, "/api/jobs/"+job+"/answer", body(id, "不是 bool"), nil); st != http.StatusBadRequest {
			t.Errorf("型別不合 → 400、pending 不動,得到 %d", st)
		}
		if st := c.status(http.MethodPost, "/api/jobs/nope/answer", body(id, false), nil); st != http.StatusNotFound {
			t.Errorf("job 不對 → 404,得到 %d", st)
		}
		if st := c.status(http.MethodPost, "/api/jobs/"+job+"/answer", body(id, false), nil); st != http.StatusNoContent {
			t.Errorf("對的答案 → 204,得到 %d", st)
		}
		if st := c.status(http.MethodPost, "/api/jobs/"+job+"/answer", body(id, true), nil); st != http.StatusConflict && st != http.StatusNotFound {
			t.Errorf("重複 POST 進不了下一題 → 409(job 已結束則 404),得到 %d", st)
		}
		return nil
	})
	if ex := evExit(t, ev); ex["code"] != float64(2) {
		t.Fatalf("第一個對的答案(false)生效 → exit 2:%v", ex)
	}
}

// TestWebDisconnectWhileWaitingOnPromptCancelsJob:關分頁時 job 正卡在 prompt(不是 hook)→ r.Context 取消 → 接縫回取消值 →
// 序列槽與 pull.lock 都放掉。
func TestWebDisconnectWhileWaitingOnPromptCancelsJob(t *testing.T) {
	fs, _, _ := pullWorld(t)
	fs.set("p1", "通勤", "t1")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	_, c := startWeb(t)
	reqCtx, disconnect := context.WithCancel(context.Background())
	resp := c.req(reqCtx, http.MethodPost, "/api/run", map[string]any{"args": []string{"pl", "pull", "通勤"}}, nil)
	sc := bufio.NewScanner(resp.Body)
	sawPrompt := false
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"type":"prompt"`) {
			sawPrompt = true
			break
		}
	}
	if !sawPrompt {
		t.Fatal("要卡在 prompt")
	}
	disconnect()
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if code, _, _ := c.run(map[string]any{"args": []string{"--help"}}); code == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("關分頁後卡在 prompt 的 job 沒被取消")
		}
		time.Sleep(50 * time.Millisecond)
	}
	lctx, lcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer lcancel()
	unlock, err := auth.LockFile(lctx, "pull.lock", "測試")
	if err != nil {
		t.Fatalf("pull.lock 要放掉:%v", err)
	}
	unlock()
}

func TestWebPromptOutsideJobErrorsInsteadOfHanging(t *testing.T) {
	s, _ := startWeb(t)
	done := make(chan error, 1)
	go func() { _, err := s.ask(webPrompt{Kind: "confirm", Title: "x"}); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("沒有 job 要回錯")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("沒有 job 時 ask 掛住了")
	}
}

// ── 三家精靈 ──

// TestWebSpotifyClientIDWizardValidatesAndReprompts:【fails-before-fix】runClientIDWizard 還是普通函式時走真 huh →
// 「bubbletea: error opening TTY」exit 1(或抓走伺服器的終端機)。
func TestWebSpotifyClientIDWizardValidatesAndReprompts(t *testing.T) {
	setCLITestConfig(t)
	const good = "0123456789abcdef0123456789abcdef"
	orig := spotifyLogin
	spotifyLogin = fakeLoginOK(t, good)
	t.Cleanup(func() { spotifyLogin = orig })
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"auth", "login", "spotify"}}, func(n int, _ string, p map[string]any) *promptReply {
		note, _ := p["note"].(map[string]any)
		if p["kind"] != "form" || note == nil || !strings.Contains(note["body"].(string), "developer.spotify.com") || !reflect.DeepEqual(fieldNames(p), map[string]bool{"client_id": false}) {
			t.Errorf("第 %d 題要是帶建 app 步驟的表單:%v", n, p)
		}
		if n == 1 {
			return reply(map[string]any{"client_id": "bad"})
		}
		if e, _ := p["error"].(string); !strings.Contains(e, "32 位") {
			t.Errorf("重問要帶驗證錯誤:%v", p["error"])
		}
		return reply(map[string]any{"client_id": " " + good + " "})
	})
	if ex := evExit(t, ev); ex["code"] != float64(0) || len(prompts(ev)) != 2 {
		t.Fatalf("壞 id 重問一次、好 id 放行:%v %d", ex, len(prompts(ev)))
	}
	if cfg, _ := config.Load(); cfg.SpotifyClientID != good {
		t.Errorf("client id 落地(TrimSpace):%q", cfg.SpotifyClientID)
	}
	if evFirst(ev, "open_url") != nil {
		t.Errorf("fakeLoginOK 不開瀏覽器,不該有 open_url:%v", ev)
	}
}

// TestWebAppleWizardDisclosureCannotBeSkipped:第一題就是揭露(note.body 是 appleDisclosure 原文、預設「取消」),
// 由伺服器判定:false → 「已取消(未同意聲明)」exit 1、零寫入;true 才到表單(user 欄 secret);dev 過期重問。
// 整條事件流不含 user token 值(TestWebApplePasswordPromptNeverEchoed 的斷言併在這裡)。
func TestWebAppleWizardDisclosureCannotBeSkipped(t *testing.T) {
	clearAppleTokens(t)
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "")
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")
	dev := fakeJWT(t, time.Now().Add(24*time.Hour))
	expired := fakeJWT(t, time.Now().Add(-time.Hour))
	appleServer(t, dev, "MUT1")
	_, c := startWeb(t)

	ev := c.runInteractive(map[string]any{"args": []string{"auth", "login", "apple"}}, func(n int, _ string, p map[string]any) *promptReply {
		note, _ := p["note"].(map[string]any)
		if n != 1 || p["kind"] != "confirm" || note == nil || note["body"] != appleDisclosure || p["default"] != false || p["negative"] != "取消" {
			t.Errorf("第 %d 題要是揭露、預設取消:%v", n, p)
		}
		return reply(false)
	})
	if ex := evExit(t, ev); ex["code"] != float64(1) || !strings.Contains(ex["message"].(string), "未同意聲明") || len(prompts(ev)) != 1 {
		t.Fatalf("不同意 = 已取消(未同意聲明):%v", ex)
	}
	assertAppleNotPersisted(t)

	ev = c.runInteractive(map[string]any{"args": []string{"auth", "login", "apple"}}, func(n int, _ string, p map[string]any) *promptReply {
		switch n {
		case 1:
			return reply(true)
		case 2:
			if p["kind"] != "form" || !reflect.DeepEqual(fieldNames(p), map[string]bool{"dev": false, "user": true}) {
				t.Errorf("表單:dev 明碼、user 密碼欄:%v", p)
			}
			return reply(map[string]any{"dev": expired, "user": "MUT1"})
		case 3:
			if e, _ := p["error"].(string); !strings.Contains(e, "過期") {
				t.Errorf("過期 dev 重問要帶錯誤:%v", p["error"])
			}
			// 重問要把上一輪的值帶回來:非 secret 欄回填值、secret 欄只標 filled(值絕不回到頁面)。
			var devF, userF map[string]any
			for _, f := range p["fields"].([]any) {
				m := f.(map[string]any)
				if m["name"] == "dev" {
					devF = m
				} else {
					userF = m
				}
			}
			if devF["value"] != expired {
				t.Errorf("dev 欄要帶回上一輪的值,得到 %v", devF["value"])
			}
			if userF["filled"] != true || userF["value"] != nil {
				t.Errorf("user 欄只帶 filled、不帶值:%v", userF)
			}
			// user 留空 = 沿用上一輪的 MUT1:dev token 過期重問時不必回 DevTools 重抄沒問題的那一個。
			return reply(map[string]any{"dev": dev, "user": ""})
		}
		t.Errorf("多出來的第 %d 題", n)
		return nil
	})
	if ex := evExit(t, ev); ex["code"] != float64(0) || !strings.Contains(evText(ev, "stdout"), "登入完成") {
		t.Fatalf("同意 → 表單 → 落地:%v %q", ex, evText(ev, "stdout"))
	}
	if user, err := secret.Get(apple.KeyMusicUserToken); err != nil || user != "MUT1" {
		t.Errorf("user token 進 keychain:%q %v", user, err)
	}
	all, _ := json.Marshal(ev)
	if strings.Contains(string(all), "MUT1") {
		t.Error("secret 欄的值不得出現在任何事件裡")
	}
}

// TestWebAuthLoginHasLongerPromptTimeout:auth login * 用的是 webAuthPromptTimeout 而不是一般的 5 分鐘
// (Apple 使用者要開 DevTools 抄兩個 token),但**不是沒有上限**——runMu 壓在整個 handleRun 上,
// 被放生的分頁若永遠不逾時就會永久占住單一序列槽(review #60)。
func TestWebAuthLoginHasLongerPromptTimeout(t *testing.T) {
	if webAuthPromptTimeout <= webPromptTimeout || webAuthPromptTimeout <= 0 {
		t.Fatalf("auth 的上限要比一般的寬、且必須有限:%v vs %v", webAuthPromptTimeout, webPromptTimeout)
	}
	clearAppleTokens(t)
	t.Setenv("CAPY_APPLE_DEVELOPER_TOKEN", "")
	t.Setenv("CAPY_APPLE_USER_TOKEN", "")
	origWait, origAuth := webPromptTimeout, webAuthPromptTimeout
	webPromptTimeout, webAuthPromptTimeout = 50*time.Millisecond, 5*time.Second
	t.Cleanup(func() { webPromptTimeout, webAuthPromptTimeout = origWait, origAuth })
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"auth", "login", "apple"}}, func(n int, _ string, _ map[string]any) *promptReply {
		time.Sleep(300 * time.Millisecond) // 遠超過一般的 50 ms,仍要收
		return reply(false)
	})
	if ex := evExit(t, ev); ex["reason"] != "done" || !strings.Contains(ex["message"].(string), "未同意聲明") {
		t.Fatalf("auth login 用的是寬鬆的上限:%v", ex)
	}

	// 但上限是存在的:放生不回答,到點就砍掉 job、序列槽拿得回來。
	webAuthPromptTimeout = 80 * time.Millisecond
	ev = c.runInteractive(map[string]any{"args": []string{"auth", "login", "apple"}}, func(int, string, map[string]any) *promptReply { return nil })
	if ex := evExit(t, ev); ex["reason"] != "timeout" {
		t.Fatalf("放生的 auth login 也要逾時(否則序列槽永遠拿不回來):%v", ex)
	}
	if code, _, _ := c.run(map[string]any{"args": []string{"--help"}}); code != 200 {
		t.Errorf("逾時後序列槽要放掉,得到 %d", code)
	}
}

func TestWebGoogleWizardSecretField(t *testing.T) {
	setGoogleTest(t)
	googleLoginFn = fakeGoogleLogin(t, "wiz.apps.googleusercontent.com", "s3cret", "w@x")
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"auth", "login", "google"}}, func(n int, _ string, p map[string]any) *promptReply {
		if p["kind"] != "form" || !reflect.DeepEqual(fieldNames(p), map[string]bool{"client_id": false, "client_secret": true}) {
			t.Errorf("Google 精靈:client_id 明碼、client_secret 密碼欄:%v", p)
		}
		if n == 1 {
			return reply(map[string]any{"client_id": " ", "client_secret": ""})
		}
		if e, _ := p["error"].(string); !strings.Contains(e, "必填") {
			t.Errorf("空 client id 重問:%v", p["error"])
		}
		return reply(map[string]any{"client_id": "wiz.apps.googleusercontent.com", "client_secret": "s3cret"})
	})
	if ex := evExit(t, ev); ex["code"] != float64(0) || len(prompts(ev)) != 2 {
		t.Fatalf("%v", ex)
	}
	if cfg, _ := config.Load(); cfg.GoogleClientID != "wiz.apps.googleusercontent.com" {
		t.Errorf("client id 落地:%+v", cfg)
	}
	if s, _ := secret.Get(auth.KeyGoogleClientSecret); s != "s3cret" {
		t.Errorf("secret 進 keychain:%q", s)
	}
	if all, _ := json.Marshal(ev); strings.Contains(string(all), "s3cret") {
		t.Error("secret 不得出現在事件裡")
	}
}

func TestWebGoogleSecretPromptAfterLogout(t *testing.T) {
	setGoogleTest(t)
	cfg, _ := config.Load()
	cfg.GoogleClientID = "byo.apps.googleusercontent.com"
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	googleLoginFn = fakeGoogleLogin(t, "byo.apps.googleusercontent.com", "re-pasted", "a@b")
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"auth", "login", "google"}}, func(n int, _ string, p map[string]any) *promptReply {
		if p["kind"] != "form" || !reflect.DeepEqual(fieldNames(p), map[string]bool{"client_secret": true}) {
			t.Errorf("logout 後只問 secret:%v", p)
		}
		return reply(map[string]any{"client_secret": "re-pasted"})
	})
	if ex := evExit(t, ev); ex["code"] != float64(0) || len(prompts(ev)) != 1 {
		t.Fatalf("%v", ex)
	}
	if s, _ := secret.Get(auth.KeyGoogleClientSecret); s != "re-pasted" {
		t.Errorf("新 secret 進 keychain:%q", s)
	}
}

func TestWebAppleAutoAlwaysDenied(t *testing.T) {
	_, c := startWeb(t)
	for _, args := range [][]string{{"auth", "login", "apple", "--auto"}, {"auth", "login", "apple", "--auto=true"}, {"--provider", "apple", "auth", "login", "apple", "--auto"}} {
		if code, _, msg := c.run(map[string]any{"args": args}); code != http.StatusForbidden || !strings.Contains(msg, "--auto") {
			t.Errorf("%v → %d %q", args, code, msg)
		}
	}
}

// ── resolve --review 三段式 ──

func TestWebReviewPromptEscIsExit2NoWrite(t *testing.T) {
	_, dc, _ := resolveWorld(t)
	before := driveFiles(t, dc)
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"resolve", "--review"}}, func(n int, _ string, p map[string]any) *promptReply {
		if p["kind"] != "select" {
			t.Errorf("第一層是 select:%v", p)
		}
		optionIndex(t, p, "略過(下次再問)")
		optionIndex(t, p, "手動搜尋")
		optionIndex(t, p, "釘成不可得")
		return dismiss()
	})
	if ex := evExit(t, ev); ex["code"] != float64(2) || len(prompts(ev)) != 1 || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("第一層關掉 = ErrUserAborted = 整輪不寫入 exit 2:%v %d", ex, len(prompts(ev)))
	}
}

func TestWebReviewManualThirdLevelCancelIsSkip(t *testing.T) {
	fs, dc, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a-live", Name: "song-a (Live)", ISRC: "TW00000000ZY"})
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"resolve", "--review"}}, func(n int, _ string, p map[string]any) *promptReply {
		switch n {
		case 1: // a:手動搜尋
			return reply(optionIndex(t, p, "手動搜尋"))
		case 2: // 搜尋字串預填 FuzzyQuery
			if p["kind"] != "input" || p["default"] == nil || !strings.Contains(p["default"].(string), "song") {
				t.Errorf("搜尋字串要預填:%v", p)
			}
			return reply(p["default"])
		case 3: // 候選:關掉 = 這筆略過
			if p["kind"] != "select" || len(p["options"].([]any)) == 0 {
				t.Errorf("候選清單:%v", p)
			}
			return dismiss()
		case 4: // b:釘成不可得
			return reply(optionIndex(t, p, "釘成不可得"))
		}
		t.Errorf("多出來的第 %d 題:%v", n, p)
		return nil
	})
	if ex := evExit(t, ev); ex["code"] != float64(0) || len(prompts(ev)) != 4 {
		t.Fatalf("第三層取消只略過那一筆、整輪照常:%v %d", ex, len(prompts(ev)))
	}
	tr := driveTracks(t, dc)
	if _, ok := tr.Tracks[fakeCID("a")].Mappings["apple"]; ok {
		t.Error("a 略過:不寫 mapping")
	}
	if m := tr.Tracks[fakeCID("b")].Mappings["apple"]; !m.Pinned || m.ID != "" {
		t.Errorf("b 釘成不可得:%+v", m)
	}
}

func TestWebReviewMergeConfirmDismissSkipsItem(t *testing.T) {
	fs, dc, _ := resolveWorld(t)
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("a")})
	mustPull(t, "resolve", "--yes")                                                  // a → ap-a
	fs.addCatalog(fakeCatalogTrack{ID: "ap-a", Name: "song-a", ISRC: fakeISRC("b")}) // b 的候選也是 ap-a(已屬 a)→ 合併要確認
	before := driveFiles(t, dc)
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"resolve", "--review"}}, func(n int, _ string, p map[string]any) *promptReply {
		switch n {
		case 1:
			return reply(optionIndex(t, p, "接受"))
		case 2:
			if p["kind"] != "confirm" {
				t.Errorf("合併確認是 confirm:%v", p)
			}
			return dismiss()
		}
		t.Errorf("多出來的第 %d 題:%v", n, p)
		return nil
	})
	if ex := evExit(t, ev); ex["code"] != float64(0) || !strings.Contains(evText(ev, "stderr"), "略過(未合併)") || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("合併確認關掉 = 不同意合併、這筆略過、零寫入:%v %q", ex, evText(ev, "stderr"))
	}
}

// ── 還原 ──

func TestWebSeamsRestore(t *testing.T) {
	setCLITestConfig(t)
	snapshot := func() []uintptr {
		fns := []any{isInteractive, stdinIsTTY, bothTTY, confirmWrite, pickOne, promptNewName, reviewPrompt, runClientIDWizard,
			confirmAppleDisclosure, runAppleWizardInputs, googleWizard, googleSecretPrompt, openBrowser, runTUI, runWatch}
		out := make([]uintptr, len(fns))
		for i, f := range fns {
			out[i] = reflect.ValueOf(f).Pointer()
		}
		return out
	}
	before := snapshot()
	s, err := newWebServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restore := installWebSeams(s)
	during := snapshot()
	restore()
	after := snapshot()
	for i := range before {
		if during[i] == before[i] {
			t.Errorf("第 %d 個接縫沒被換掉", i)
		}
		if after[i] != before[i] {
			t.Errorf("第 %d 個接縫沒還原", i)
		}
	}
	if reflect.ValueOf(io.Writer(auth.LockStderr)).Pointer() == reflect.ValueOf(io.Writer(&webLockStderr{})).Pointer() {
		t.Error("LockStderr 要還原")
	}
}
