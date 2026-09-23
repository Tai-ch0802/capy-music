package cli

// 網頁的多語系(i18n T3 基礎建設):/api/i18n、提示事件的 key、取消時不送訊息。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

type webI18nResp struct {
	Lang      string
	Supported []map[string]string
	Messages  map[string]any
}

// TestWebI18nEndpoint:第三個直達端點——同樣要 token、過 Host / Origin 守門;只送 webui.* 與 webSharedKeys;
// 語系跟著 config(每次重讀、不 i18n.Set);複數訊息以「類別 → 字串」的物件送到。
func TestWebI18nEndpoint(t *testing.T) {
	setCLITestConfig(t)
	withLanguage(t, i18n.Current())
	orig := webSharedKeys
	t.Cleanup(func() { webSharedKeys = orig })                      // 先註冊:在伺服器關掉之後才還原
	webSharedKeys = append(slices.Clone(orig), "changeset.pending") // 一個複數 key,看它怎麼送到
	s, c := startWeb(t)

	_, port, _ := strings.Cut(s.hostport, ":")
	for _, tc := range []struct {
		hdr  map[string]string
		want int
	}{
		{map[string]string{"X-Capy-Token": ""}, http.StatusUnauthorized},
		{map[string]string{"X-Capy-Token": "wrong"}, http.StatusUnauthorized},
		{map[string]string{"Host": "localhost:" + port}, http.StatusMisdirectedRequest},
		{map[string]string{"Origin": "http://evil"}, http.StatusForbidden},
		{map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
	} {
		if st := c.status(http.MethodGet, "/api/i18n", nil, tc.hdr); st != tc.want {
			t.Errorf("/api/i18n %v → %d,要 %d", tc.hdr, st, tc.want)
		}
	}

	get := func() webI18nResp {
		t.Helper()
		resp := c.req(context.Background(), http.MethodGet, "/api/i18n", nil, nil)
		defer resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("/api/i18n:%d %v", resp.StatusCode, resp.Header)
		}
		var d webI18nResp
		if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	webui := i18n.Messages("en", func(k string) bool { return strings.HasPrefix(k, "webui.") })

	d := get()
	if d.Lang != "zh-TW" || len(d.Supported) != 2 || d.Supported[0]["code"] != "en" || d.Supported[0]["name"] != "English" ||
		d.Supported[1]["code"] != "zh-TW" || d.Supported[1]["name"] != "繁體中文" {
		t.Fatalf("沒設 language:測試二進位的預設 zh-TW;supported 是代碼 + 語系自己的名稱:%s %v", d.Lang, d.Supported)
	}
	for k := range d.Messages {
		if !strings.HasPrefix(k, "webui.") && !slices.Contains(webSharedKeys, k) {
			t.Errorf("/api/i18n 送了不該送的 %q:只送 webui.* 與 webSharedKeys", k)
		}
	}
	for k := range webui {
		if _, ok := d.Messages[k]; !ok {
			t.Errorf("/api/i18n 少了 %q", k)
		}
	}
	if d.Messages["webui.lang.label"] != "語言" || d.Messages["web.err.bad_token"] != i18n.T("web.err.bad_token") {
		t.Errorf("zh-TW 的值:%v %v", d.Messages["webui.lang.label"], d.Messages["web.err.bad_token"])
	}
	if p, ok := d.Messages["changeset.pending"].(map[string]any); !ok || len(p) != 1 || p["other"] == nil {
		t.Errorf("複數訊息要以物件送到(zh-TW 只有 other):%#v", d.Messages["changeset.pending"])
	}

	if err := config.Save(&config.Config{Language: "en"}); err != nil { // 終端機(或語言選單的 config set)換了語系
		t.Fatal(err)
	}
	d = get()
	if d.Lang != "en" || d.Messages["webui.lang.label"] != "Language" {
		t.Errorf("config 換成 en,下一次就是英文:%s %v", d.Lang, d.Messages["webui.lang.label"])
	}
	if p, ok := d.Messages["changeset.pending"].(map[string]any); !ok || p["one"] == nil || p["other"] == nil {
		t.Errorf("英文的複數訊息有 one 與 other:%#v", d.Messages["changeset.pending"])
	}
	if i18n.Current() != "zh-TW" {
		t.Errorf("直達端點不可以 i18n.Set(它在 runMu 外):目前 %s", i18n.Current())
	}
}

// TestWebI18nServesEveryKeyThePageUses:頁面 t('…') 與 data-i18n* 用到的每個 key,/api/i18n 都要送——不然頁面上只會出現 key 本身。
// (key 在不在 en.json、佔位符對不對,由 internal/i18n 的守門測試查;這裡查的是「有沒有送到頁面」。)
func TestWebI18nServesEveryKeyThePageUses(t *testing.T) {
	msgs := webI18n("zh-TW")["messages"].(map[string]any)
	call := regexp.MustCompile(`(?:^|[^\w$.])t\(\s*['"]([^'"]+)['"]`)
	attr := regexp.MustCompile(`\sdata-i18n(?:-aria-label|-placeholder|-title)?="([^"]+)"`)
	n := 0
	err := walkEmbedded(t, "webui", func(name string, b []byte) {
		if strings.HasSuffix(name, "/i18n.js") { // t() 本身住在這裡,註解裡的是範例
			return
		}
		for _, re := range []*regexp.Regexp{call, attr} {
			for _, m := range re.FindAllSubmatch(b, -1) {
				n++
				if _, ok := msgs[string(m[1])]; !ok {
					t.Errorf("%s 用了 %q,但 /api/i18n 不送它:key 放在 webui.* 底下,或加進 web.go 的 webSharedKeys", name, m[1])
				}
			}
		}
	})
	if err != nil || n < 2 { // 至少 lang.js 的 t() 與 index.html 的 data-i18n-aria-label
		t.Fatalf("掃描沒掃到東西:%v %d", err, n)
	}
}

// TestWebCancelledExitHasNoMessage:【fails-before-fix】中止的 job,exit 事件的 message 是空的(每個語系都是):
// 頁面看 reason = cancelled 就知道,不必比對跟著語系變的「已取消 / cancelled / context canceled」。
func TestWebCancelledExitHasNoMessage(t *testing.T) {
	for _, lang := range []string{"zh-TW", "en"} {
		t.Run(lang, func(t *testing.T) {
			fs, _, _ := pullWorld(t)
			if lang == "en" {
				webEnglish(t)
			}
			entered, release := blockingHook(t, fs)
			defer release()
			s, c := startWeb(t)
			done := make(chan []map[string]any, 1)
			go func() {
				_, ev, _ := c.run(map[string]any{"args": []string{"search", "x"}})
				done <- ev
			}()
			waitFor(t, "job 進 hook", entered)
			if st := c.status(http.MethodPost, "/api/jobs/"+s.current().id+"/cancel", nil, nil); st != http.StatusNoContent {
				t.Fatalf("cancel → 204,得到 %d", st)
			}
			select {
			case ev := <-done:
				if ex := evExit(t, ev); ex["reason"] != "cancelled" || ex["code"] != float64(1) || ex["message"] != "" {
					t.Errorf("中止:exit 1、reason cancelled、message 空:%v", ex)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("取消後 job 沒結束")
			}
		})
	}
}

// TestWebMigrateReviewPromptCarriesKey:搬家精靈認「現在逐筆裁決?」那一則,靠提示事件的 key(confirmWrite 帶的
// i18n key),不靠跟著語系變的標題;最後的確認是另一個 key。伺服器與頁面兩頭在這裡接起來:migrate 真的送出的第一個 key
// 就是 move.js 拿來比 ev.key 的那個字面,任何一邊改了另一邊沒改都會紅。
func TestWebMigrateReviewPromptCarriesKey(t *testing.T) {
	b, err := webUI.ReadFile("webui/js/pages/move.js")
	if err != nil {
		t.Fatal(err)
	}
	pageKeys := regexp.MustCompile(`ev\.key === '([^']+)'`).FindAllStringSubmatch(string(b), -1)
	if len(pageKeys) != 1 {
		t.Fatalf("move.js 要剛好一處拿 ev.key 比字面(逐筆裁決那一則):%v", pageKeys)
	}
	fs1, fs2, _, _ := twoPlatforms(t)
	catalogISRC(fs1, "a")
	fs2.set("q1", "road trip", "a", "n") // n 在 spotify 上沒有:要逐筆裁決
	_, c := startWeb(t)
	var keys []any
	ev := c.runInteractive(map[string]any{"args": []string{"migrate", "q1", "--from", "apple", "--to", "spotify"}}, func(_ int, _ string, p map[string]any) *promptReply {
		keys = append(keys, p["key"])
		return reply(false)
	})
	if len(keys) != 2 || keys[0] != pageKeys[0][1] || keys[1] != "migrate.confirm.create" {
		t.Errorf("第一則是逐筆裁決(move.js 認的 %q)、第二則是最後確認,各帶自己的 key:%v", pageKeys[0][1], keys)
	}
	if ex := evExit(t, ev); ex["code"] != float64(2) {
		t.Errorf("兩則都按取消 = exit 2:%v", ex)
	}
}

// TestWebStopDuringPromptExitHasNoMessage:【fails-before-fix】提示開著時按「中止」:挑選器回 errCancelled、確認閘回
// huh.ErrUserAborted——兩個都只是在重複「已取消」(後者還是 huh 的英文 user aborted,每個語系都一樣),exit 的 message
// 要是空的;不然 exit 行是「· exit 1 · 已取消 · 已取消」/「· exit 1 · 已取消 · user aborted」。
// 關掉提示(reason done)照舊帶著訊息:TestWebPickOneCancelIsErrCancelledExit1。
func TestWebStopDuringPromptExitHasNoMessage(t *testing.T) {
	for _, lang := range []string{"zh-TW", "en"} {
		for _, args := range [][]string{{"pl", "show"}, {"pl", "pull", "通勤"}} {
			t.Run(lang+"/"+strings.Join(args, " "), func(t *testing.T) {
				fs, _, _ := pullWorld(t)
				fs.set("p1", "通勤", "t1")
				mustPull(t, "pl", "link", "通勤", "spotify:p1")
				if lang == "en" {
					webEnglish(t)
				}
				_, c := startWeb(t)
				asked := 0
				ev := c.runInteractive(map[string]any{"args": args}, func(_ int, job string, p map[string]any) *promptReply {
					asked++
					if want := map[string]string{"show": "select", "pull": "confirm"}[args[1]]; p["kind"] != want {
						t.Errorf("%v 的提示要是 %s:%v", args, want, p)
					}
					if st := c.status(http.MethodPost, "/api/jobs/"+job+"/cancel", nil, nil); st != http.StatusNoContent {
						t.Errorf("cancel → 204,得到 %d", st)
					}
					return nil // 不回答:提示由中止收掉
				})
				if ex := evExit(t, ev); asked != 1 || ex["reason"] != "cancelled" || ex["code"] != float64(1) || ex["message"] != "" {
					t.Errorf("提示開著時中止:問一次、exit 1、reason cancelled、message 空:%d %v", asked, ex)
				}
			})
		}
	}
}

// TestWebStopKeepsARealErrorMessage:中止時剛好撞上的真錯誤不是取消本身,訊息照送(這裡是不吃取消的 Pause 回來時帶著錯)。
func TestWebStopKeepsARealErrorMessage(t *testing.T) {
	f := &pauseIgnoresCtx{nowFake: newNowFake(), entered: make(chan struct{}), release: make(chan struct{}), err: errors.New("Music.app 沒有回應")}
	s, c := startWeb(t)
	swapProviderWith(t, f)
	done := make(chan []map[string]any, 1)
	go func() {
		_, ev, _ := c.run(map[string]any{"args": []string{"pause"}})
		done <- ev
	}()
	waitFor(t, "pause 進 provider", f.entered)
	if st := c.status(http.MethodPost, "/api/jobs/"+s.current().id+"/cancel", nil, nil); st != http.StatusNoContent {
		t.Fatalf("cancel → 204,得到 %d", st)
	}
	close(f.release)
	select {
	case ev := <-done:
		if ex := evExit(t, ev); ex["code"] != float64(1) || ex["reason"] != "cancelled" || !strings.Contains(ex["message"].(string), "Music.app 沒有回應") {
			t.Errorf("中止時撞上的真錯誤要照送:%v", ex)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job 沒結束")
	}
}

// TestWebStopDuringReviewKeepsPending:逐筆裁決被中止 → reviewLoop 回 ErrUserAborted → migrate 回 PendingError(exit 2)。
// 那是「N 筆沒套用」的實情(終端機按 Esc 也是它),不是取消本身:訊息照送。
func TestWebStopDuringReviewKeepsPending(t *testing.T) {
	fs1, fs2, _, _ := twoPlatforms(t)
	catalogISRC(fs1, "a")
	fs2.set("q1", "road trip", "a", "n") // n 在 spotify 上沒有:要逐筆裁決
	_, c := startWeb(t)
	ev := c.runInteractive(map[string]any{"args": []string{"migrate", "q1", "--from", "apple", "--to", "spotify"}}, func(_ int, job string, p map[string]any) *promptReply {
		if p["kind"] == "confirm" {
			return reply(true) // 現在逐筆裁決
		}
		if st := c.status(http.MethodPost, "/api/jobs/"+job+"/cancel", nil, nil); st != http.StatusNoContent {
			t.Errorf("cancel → 204,得到 %d", st)
		}
		return nil
	})
	if ex := evExit(t, ev); ex["code"] != float64(2) || ex["reason"] != "cancelled" || !strings.Contains(ex["message"].(string), "待套用") {
		t.Errorf("逐筆裁決被中止:exit 2、reason cancelled、訊息照送待套用的筆數:%v", ex)
	}
}

// TestWebI18nNotBlockedByRunningJob:/api/i18n 是直達端點、不進 runMu——序列槽被握著時照樣回(同 TestWebNowNotBlockedByRunningJob)。
func TestWebI18nNotBlockedByRunningJob(t *testing.T) {
	setCLITestConfig(t)
	s, c := startWeb(t)
	s.runMu.Lock() // = 有一個 job 正在跑
	defer s.runMu.Unlock()

	done := make(chan int, 1)
	go func() { done <- c.status(http.MethodGet, "/api/i18n", nil, nil) }()
	select {
	case st := <-done:
		if st != http.StatusOK {
			t.Errorf("/api/i18n → %d,要 200", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("/api/i18n 被序列槽擋住了")
	}
	if code, _, _ := c.run(map[string]any{"args": []string{"--help"}}); code != http.StatusConflict {
		t.Errorf("序列槽握著時 /api/run 要 409,得到 %d", code)
	}
}
