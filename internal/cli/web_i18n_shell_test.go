package cli

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestWebShellTextsHaveEnglish:殼層(index.html、app.js、console.js、player.js)用到的每個 key,英文目錄都送得到、
// 不是空的、沒有中日韓字元——英文模式的頁面不會冒出中文(行為面見 webui_console.mjs 的情境 13)。
func TestWebShellTextsHaveEnglish(t *testing.T) {
	en := webI18n("en")["messages"].(map[string]any)
	call := regexp.MustCompile(`(?:^|[^\w$.])t\(\s*['"]([^'"]+)['"]`)
	attr := regexp.MustCompile(`\sdata-i18n(?:-aria-label|-placeholder|-title)?="([^"]+)"`)
	n := 0
	for _, name := range []string{"index.html", "js/app.js", "js/console.js", "js/player.js"} {
		b, err := webUI.ReadFile("webui/" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, re := range []*regexp.Regexp{call, attr} {
			for _, m := range re.FindAllSubmatch(b, -1) {
				n++
				key := string(m[1])
				var vals []string
				switch v := en[key].(type) {
				case string:
					vals = []string{v}
				case map[string]string:
					for _, s := range v {
						vals = append(vals, s)
					}
				default:
					t.Errorf("%s 用了 %q,英文目錄沒送它", name, key)
				}
				for _, v := range vals {
					if v == "" || hasCJK(v) {
						t.Errorf("%s 的 %q 英文是 %q:不可以是空的、不可以有中日韓字元", name, key, v)
					}
					if strings.Contains(v, "Keychain") { // 英文目錄其他地方都寫小寫的 keychain(指作業系統的鑰匙圈,不是產品名)
						t.Errorf("%s 的 %q 英文是 %q:keychain 小寫,跟其他地方一樣", name, key, v)
					}
				}
			}
		}
	}
	if n < 80 { // 殼層有 90 個上下的呼叫點:掃不到東西時別假性通過
		t.Fatalf("只掃到 %d 個呼叫點", n)
	}
}

// TestWebBadTokenBodyIsTheNotice:token 不對時 /api/i18n 也是 401、目錄讀不到,app.js 的提示直接印 401 回應的 error。
// 所以那個欄位就是頁面上的字:zh-TW 要跟搬進目錄之前的那句一字不差。
func TestWebBadTokenBodyIsTheNotice(t *testing.T) {
	_, c := startWeb(t)
	resp := c.req(context.Background(), http.MethodGet, "/api/commands", nil, map[string]string{"X-Capy-Token": "nope"})
	if got := errBody(t, resp); resp.StatusCode != http.StatusUnauthorized || got != "token 不對或已失效:回到啟動 capy --web 時印的網址" {
		t.Errorf("401:%d %q", resp.StatusCode, got)
	}
}
