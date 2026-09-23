// Package site 只有測試:品牌網站(https://capy.taislife.work)是幾頁手寫的靜態 HTML,沒有建置步驟,
// 所以「它還符合 Google OAuth 同意畫面的要求、而且說的跟程式做的一樣」只能靠這裡守。
package site

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/auth"
)

var pages = []string{"index.html", "privacy.html", "terms.html", "en/index.html", "en/privacy.html", "en/terms.html", "404.html", "en/404.html"}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("public", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Google 對首頁的要求(support.google.com/cloud/answer/13807376):說明 app 做什麼、透明地說明為什麼要使用者資料、
// 有隱私權政策的連結、不用登入就看得到。三頁要互相連得到,兩種語言都一樣。
func TestHomeExplainsTheAppAndLinksToPolicies(t *testing.T) {
	for _, tc := range []struct{ page, privacy, terms, guide string }{
		{"index.html", `href="/privacy"`, `href="/terms"`, `href="/guide"`},
		{"en/index.html", `href="/en/privacy"`, `href="/en/terms"`, `href="/en/guide"`}, // 英文首頁連英文指南(Q53)
	} {
		html := read(t, tc.page)
		for _, want := range []string{tc.privacy, tc.terms, tc.guide, `id="what"`, `id="google"`, "openid", "userinfo.email", "drive.appdata", "https://myaccount.google.com/permissions"} {
			if !strings.Contains(html, want) {
				t.Errorf("%s 少了 %s:首頁要說明 app 做什麼、為什麼要 Google 的資料,並連到隱私權政策與條款", tc.page, want)
			}
		}
	}
}

// 隱私權政策說的 scope 必須跟程式要的一模一樣(CLAUDE.md 硬約束:只准這三個)。程式多要一個而政策沒寫,
// 就是 Google API 服務使用者資料政策說的「未揭露的取用」;這個測試讓那種 PR 過不了。
func TestPrivacyPolicyDisclosesExactlyTheScopesTheCodeRequests(t *testing.T) {
	for _, page := range []string{"privacy.html", "en/privacy.html"} {
		html := read(t, page)
		for _, scope := range auth.GoogleScopes {
			if !strings.Contains(html, "<code>"+scope+"</code>") {
				t.Errorf("%s 沒有揭露程式要求的 scope %s", page, scope)
			}
		}
		if got := strings.Count(html, "https://www.googleapis.com/auth/"); got != len(auth.GoogleScopes)-1 { // openid 沒有網址形式
			t.Errorf("%s 列了 %d 個 googleapis scope,程式要的是 %d 個", page, got, len(auth.GoogleScopes)-1)
		}
		// 取用 / 使用 / 存放 / 分享 / 刪除 / 聯絡:Google 要求揭露的每一項各有一節;Limited Use 的聲明與連結要在。
		for _, id := range []string{"google-data", "use", "storage", "sharing", "retention", "contact"} {
			if !strings.Contains(html, `id="`+id+`"`) {
				t.Errorf("%s 少了 #%s 這一節", page, id)
			}
		}
		if !strings.Contains(html, "https://developers.google.com/terms/api-services-user-data-policy") {
			t.Errorf("%s 要有 Google API 服務使用者資料政策(Limited Use)的聲明", page)
		}
	}
}

// 中英文兩版的結構要一致:哪天只改了一邊、漏了一節,這裡要紅。
func TestBothLanguagesHaveTheSameSections(t *testing.T) {
	ids := func(html string) []string {
		var out []string
		for _, m := range regexp.MustCompile(`<(?:section|h2) id="([^"]+)"`).FindAllStringSubmatch(html, -1) {
			out = append(out, m[1])
		}
		return out
	}
	for _, name := range []string{"index.html", "privacy.html", "terms.html"} {
		if zh, en := ids(read(t, name)), ids(read(t, "en/"+name)); !slices.Equal(zh, en) || len(zh) == 0 {
			t.Errorf("%s 兩種語言的章節不一致:\n 中 %v\n 英 %v", name, zh, en)
		}
	}
}

// 零外部資源、沒有 script:隱私權政策寫了「不設 cookie、沒有分析工具、不載入任何第三方資源」,要名實相符。
// (連到別的網站的 <a> 不算;canonical / hreflang 指向自己的網域。)
func TestPagesLoadNothingFromThirdParties(t *testing.T) {
	// script / iframe / embed / object 一律不准;圖片與媒體只擋外部來源(之後放一張自己的截圖不該讓這裡變紅)。
	loads := regexp.MustCompile(`(?i)<(?:script|iframe|embed|object)\b|<(?:img|video|audio|source)\b[^>]*\bsrc="(?:https?:)?//|<link[^>]+rel="(?:stylesheet|icon|preload|preconnect|dns-prefetch)"[^>]+href="(?:https?:)?//|@import|url\(\s*['"]?(?:https?:)?//`)
	for _, name := range append(slices.Clone(pages), "style.css", "guide.html", "en/guide.html", "guide.css") {
		if m := loads.FindString(read(t, name)); m != "" {
			t.Errorf("%s 會載入外部資源或執行 script:%q", name, m)
		}
	}
	// 代管商(Cloudflare 的 zone 設定)會自動往 HTML 注入分析用的 script,是 CSP 把它們擋下來的(上線後實測)。
	// 政策 §8 把這件事講明了;這裡釘住兩邊:CSP 不開 script、政策有提到 CSP。
	for _, page := range []string{"privacy.html", "en/privacy.html"} {
		if !strings.Contains(read(t, page), "<code>default-src 'none'</code>") {
			t.Errorf("%s §8 要說明頁面以 CSP(default-src 'none')禁止執行或載入任何 script", page)
		}
	}
	// 每一頁的頁尾都講同一句(頁尾是複製八份的,最容易被順手改歪);它能成立靠的就是上面那條 CSP。
	for _, name := range pages {
		want := "不執行任何 script"
		if strings.HasPrefix(name, "en/") {
			want = "runs no scripts"
		}
		if !strings.Contains(read(t, name), want) {
			t.Errorf("%s 的頁尾要跟政策 §8 一致:%q", name, want)
		}
	}
	if h := read(t, "_headers"); !strings.Contains(h, "default-src 'none'") || strings.Contains(h, "script-src") {
		t.Errorf("_headers 的 CSP 要從 default-src 'none' 起跳、不開 script:\n%s", h)
	}
}

// 每一個站內連結都要有檔案接(Workers 靜態資產的 auto-trailing-slash:/privacy → privacy.html、/en/ → en/index.html)。
// 404 頁會被每一個不存在的網址拿去回應:不給 canonical / hreflang(不然所有壞連結都被正規化到同一個位址),並請搜尋引擎不要收錄。
func TestNotFoundPageIsNotCanonical(t *testing.T) {
	for _, name := range []string{"404.html", "en/404.html"} { // Workers 會往上找最近的一份:/en/ 底下打錯網址的人要拿到英文的
		html := read(t, name)
		if strings.Contains(html, `rel="canonical"`) || strings.Contains(html, "hreflang=\"x-default\"") || !strings.Contains(html, `<meta name="robots" content="noindex">`) {
			t.Errorf("%s 不可以有 canonical / x-default,而且要 noindex", name)
		}
	}
}

func TestInternalLinksResolve(t *testing.T) {
	href := regexp.MustCompile(`href="(/[^"#]*)`)
	for _, name := range append(slices.Clone(pages), "guide.html", "en/guide.html") {
		for _, m := range href.FindAllStringSubmatch(read(t, name), -1) {
			p := strings.TrimPrefix(m[1], "/")
			candidates := []string{p, p + ".html", filepath.ToSlash(filepath.Join(p, "index.html"))}
			if p == "" {
				candidates = []string{"index.html"}
			}
			if !slices.ContainsFunc(candidates, func(c string) bool {
				st, err := os.Stat(filepath.Join("public", filepath.FromSlash(c)))
				return err == nil && !st.IsDir()
			}) {
				t.Errorf("%s 的連結 %s 沒有對應的檔案", name, m[1])
			}
		}
	}
}

// 這個 Worker 只有靜態檔:沒有 main、沒有任何 binding(本專案不架會碰到使用者資料的伺服器端元件)。
func TestWranglerConfigIsAssetsOnly(t *testing.T) {
	b, err := os.ReadFile("wrangler.jsonc")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "//") {
			lines = append(lines, l)
		}
	}
	var cfg struct {
		Assets struct{ Directory string } `json:"assets"`
		Routes []struct {
			Pattern      string `json:"pattern"`
			CustomDomain bool   `json:"custom_domain"`
		} `json:"routes"`
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(strings.Join(lines, "\n")), &cfg)
	// observability(維護者 2026-09-21 加的 Workers Logs)只是紀錄的設定,不是程式也不是 binding,所以放行;
	// 但它代表 Cloudflare 帳號裡會留存取紀錄——隱私權政策 §8 有照實寫,下面一起釘。
	allowed := []string{"$schema", "name", "compatibility_date", "assets", "routes", "workers_dev", "preview_urls", "observability"}
	for k := range raw {
		if !slices.Contains(allowed, k) {
			t.Errorf("wrangler.jsonc 多了 %q:這個網站只准有靜態檔(沒有 main、沒有 binding、沒有 vars)", k)
		}
	}
	var assets map[string]json.RawMessage
	_ = json.Unmarshal(raw["assets"], &assets)
	for k := range assets { // binding / run_worker_first 只有配 main 才有意義:出現就代表有人開始往這裡放程式了
		if !slices.Contains([]string{"directory", "not_found_handling", "html_handling"}, k) {
			t.Errorf("wrangler.jsonc 的 assets 多了 %q:只准 directory / not_found_handling / html_handling", k)
		}
	}
	var obs struct {
		Enabled bool `json:"enabled"`
		Logs    struct {
			Enabled bool `json:"enabled"`
		} `json:"logs"`
	}
	_ = json.Unmarshal(raw["observability"], &obs)
	if obs.Enabled || obs.Logs.Enabled { // 真的開著才要求揭露;鍵在但關著不算
		for _, page := range []string{"privacy.html", "en/privacy.html"} {
			if html := read(t, page); !strings.Contains(html, "存取紀錄") && !strings.Contains(html, "access logs") {
				t.Errorf("wrangler.jsonc 開了 observability(會留存取紀錄),%s §8 要照實寫", page)
			}
		}
	}
	if cfg.Assets.Directory != "./public" || len(cfg.Routes) != 1 || cfg.Routes[0].Pattern != "capy.taislife.work" || !cfg.Routes[0].CustomDomain {
		t.Errorf("assets / routes 不對:%+v", cfg)
	}
}
