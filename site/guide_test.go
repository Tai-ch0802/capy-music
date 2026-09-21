package site

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "用 docs/guide.html 重新產生 site/public/guide.html 與 guide.css")

// guideForSite:把 repo 裡那份單檔的使用指南(docs/guide.html)變成能掛在品牌網站上的樣子。
// 網站的 CSP 是 default-src 'none'; style-src 'self'——不准 inline style、不准任何 script(隱私權政策 §8 與每一頁的頁尾
// 都這樣承諾)。所以:<style> 搬成 /guide.css;<script>(只是讓開頭的水豚動起來)整段拿掉,第一幀本來就寫在 HTML 裡;
// 再補上回首頁的連結、favicon 與 canonical。docs/guide.html 本身不動:它仍然是離線打得開的單一檔案,Artifact 也用它。
func guideForSite(src string) (html, css string) {
	style := regexp.MustCompile(`(?s)<style>\n?(.*?)</style>\n?`)
	css = style.FindStringSubmatch(src)[1] + `
/* 以下是掛上網站時加的(site/guide_test.go):回首頁的連結 */
.site-back { padding: 1.25rem 0 0; font-family: var(--mono); font-size: var(--step--1); }
.site-back a { color: var(--muted); text-decoration: none; }
.site-back a:hover { color: var(--accent); }
`
	html = style.ReplaceAllString(src, `<meta name="description" content="capy 的完整使用說明:安裝、連接帳號、互動式介面、網頁介面、命令參考,以及 Spotify、Apple Music、本機曲庫的能力差異。">
<link rel="canonical" href="https://capy.taislife.work/guide">
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
<link rel="stylesheet" href="/guide.css">
`)
	html = regexp.MustCompile(`(?s)<script>.*?</script>\n?`).ReplaceAllString(html, "")
	// style="margin-top:1.5rem" 這種行內樣式也會被 CSP 擋掉:換成 class(mt-15 = 1.5rem、mt-125 = 1.25rem),規則補在 guide.css 尾端。
	mt := func(rem string) string { return "mt-" + strings.ReplaceAll(rem, ".", "") }
	html = regexp.MustCompile(`class="([^"]*)" style="margin-top:([\d.]+)rem"`).ReplaceAllStringFunc(html, func(m string) string {
		g := regexp.MustCompile(`class="([^"]*)" style="margin-top:([\d.]+)rem"`).FindStringSubmatch(m)
		return `class="` + g[1] + " " + mt(g[2]) + `"`
	})
	html = regexp.MustCompile(` style="margin-top:([\d.]+)rem"`).ReplaceAllStringFunc(html, func(m string) string {
		return ` class="` + mt(regexp.MustCompile(`[\d.]+`).FindString(m)) + `"`
	})
	for _, rem := range []string{"1.25", "1.5"} {
		if strings.Contains(html, mt(rem)) {
			css += "." + mt(rem) + " { margin-top: " + rem + "rem; }\n"
		}
	}
	html = strings.Replace(html, `<div class="wrap">`+"\n", `<div class="wrap">`+"\n"+`  <nav class="site-back" aria-label="網站"><a href="/">← capy.taislife.work</a></nav>`+"\n", 1)
	return html, css
}

// 網站上的指南必須就是 docs/guide.html 的轉換結果:改了指南忘了重新產生,這裡會紅,訊息告訴你跑哪個命令。
// (之後還要重新部署:cd site && wrangler deploy。)
func TestGuideOnSiteIsCurrent(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "docs", "guide.html"))
	if err != nil {
		t.Fatal(err)
	}
	html, css := guideForSite(strings.ReplaceAll(string(src), "\r\n", "\n"))
	if strings.Contains(html, "<script") || strings.Contains(html, "<style") || strings.Contains(html, `style="`) || !strings.Contains(html, `href="/guide.css"`) || !strings.Contains(html, `class="site-back"`) {
		t.Fatal("轉換沒有做完:網站上的指南不可以有 <style>、style= 屬性或任何 script(CSP 會擋掉),而且要連到 /guide.css、有回首頁的連結。指南裡出現新的行內樣式的話,在 guideForSite 裡補上對應的轉換")
	}
	for name, want := range map[string]string{"guide.html": html, "guide.css": css} {
		path := filepath.Join("public", name)
		if *update {
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, _ := os.ReadFile(path)
		if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
			t.Errorf("site/public/%s 不是 docs/guide.html 目前的轉換結果。重新產生:go test ./site/ -run TestGuideOnSiteIsCurrent -update(然後重新部署網站)", name)
		}
	}
}
