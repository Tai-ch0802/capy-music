package site

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "用 docs/guide.html 與 docs/guide.en.html 重新產生 site/public/guide.html、en/guide.html 與 guide.css")

// guideSite:一份指南掛上網站時各自不同的部分。中文在 /guide、英文在 /en/guide(Q53);兩份共用 /guide.css,
// 所以兩個 <style> 區塊(連同行內 margin-top 用到的值)必須一模一樣,TestGuideOnSiteIsCurrent 會比。
type guideSite struct {
	src, out, desc, canonical, home, navLabel, hreflang string
}

var guides = []guideSite{
	{"guide.html", "guide.html", "capy 的完整使用說明:安裝、連接帳號、互動式介面、網頁介面、命令參考,以及 Spotify、Apple Music、本機曲庫的能力差異。",
		"https://capy.taislife.work/guide", "/", "網站", "zh-Hant"},
	{"guide.en.html", "en/guide.html", "The complete capy user guide: installing, connecting your accounts, interactive mode, the web UI, the command reference, and what Spotify, Apple Music and the Local library can each do.",
		"https://capy.taislife.work/en/guide", "/en/", "Site", "en"},
}

// guideForSite:把 repo 裡那份單檔的使用指南(docs/guide.html)變成能掛在品牌網站上的樣子。
// 網站的 CSP 是 default-src 'none'; style-src 'self'——不准 inline style、不准任何 script(隱私權政策 §8 與每一頁的頁尾
// 都這樣承諾)。所以:<style> 搬成 /guide.css;<script>(只是讓開頭的水豚動起來)整段拿掉,第一幀本來就寫在 HTML 裡;
// 再補上回首頁的連結、favicon、canonical 與兩種語言的 alternate。docs/guide.html 本身不動:它仍然是離線打得開的單一檔案,Artifact 也用它。
// 英文版 docs/guide.en.html 走同一條轉換,只換 guideSite 裡的那幾樣。
func guideForSite(src string, g guideSite) (html, css string, err error) {
	style := regexp.MustCompile(`(?s)<style>\n?(.*?)</style>\n?`)
	// 只認得「剛好一個 <style> 區塊」:兩個的話第二塊的 CSS 會掉、head 那組標籤反而被插兩次(review #78)。
	if n := strings.Count(src, "<style>"); n != 1 {
		return "", "", fmt.Errorf("docs/%s 要剛好一個 <style> 區塊,現在有 %d 個:在 guideForSite 補上對應的處理", g.src, n)
	}
	css = style.FindStringSubmatch(src)[1] + `
/* 以下是掛上網站時加的(site/guide_test.go):回首頁的連結、行內 margin-top 換成的 class */
.site-back { padding: 1.25rem 0 0; font-family: var(--mono); font-size: var(--step--1); }
.site-back a { color: var(--muted); text-decoration: none; }
.site-back a:hover { color: var(--accent); }
`
	// 跟網站其他雙語頁一樣宣告兩種語言的網址;x-default 跟其他頁一樣指中文版(guides[0])。
	alt := ""
	for _, o := range guides {
		alt += `<link rel="alternate" hreflang="` + o.hreflang + `" href="` + o.canonical + `">` + "\n"
	}
	alt += `<link rel="alternate" hreflang="x-default" href="` + guides[0].canonical + `">` + "\n"
	html = style.ReplaceAllLiteralString(src, `<meta name="description" content="`+g.desc+`">
<link rel="canonical" href="`+g.canonical+`">
`+alt+`<link rel="icon" href="/favicon.svg" type="image/svg+xml">
<link rel="stylesheet" href="/guide.css">
`)
	html = regexp.MustCompile(`(?s)<script>.*?</script>\n?`).ReplaceAllString(html, "")
	// style="margin-top:1.5rem" 這種行內樣式也會被 CSP 擋掉:換成 class(mt-15 = 1.5rem),規則從**實際產生的 class** 收集,
	// 不寫死有哪幾個值——不然指南哪天用了 2rem,class 產生了、規則沒產生,線上就安靜地少一段間距(review #78)。
	rules := map[string]string{}
	mt := func(rem string) string {
		cls := "mt-" + strings.ReplaceAll(rem, ".", "")
		rules[cls] = rem
		return cls
	}
	withClass := regexp.MustCompile(`class="([^"]*)" style="margin-top:([\d.]+)rem"`)
	html = withClass.ReplaceAllStringFunc(html, func(m string) string {
		g := withClass.FindStringSubmatch(m)
		return `class="` + g[1] + " " + mt(g[2]) + `"`
	})
	bare := regexp.MustCompile(` style="margin-top:([\d.]+)rem"`)
	html = bare.ReplaceAllStringFunc(html, func(m string) string {
		return ` class="` + mt(bare.FindStringSubmatch(m)[1]) + `"`
	})
	for _, cls := range slices.Sorted(maps.Keys(rules)) {
		css += "." + cls + " { margin-top: " + rules[cls] + "rem; }\n"
	}
	html = strings.Replace(html, `<div class="wrap">`+"\n", `<div class="wrap">`+"\n"+`  <nav class="site-back" aria-label="`+g.navLabel+`"><a href="`+g.home+`">← capy.taislife.work</a></nav>`+"\n", 1)

	// 不認得的輸入要紅,不可以安靜地產生錯的東西:網站的 CSP 會把下面每一種都擋掉或弄壞,而且不會有任何人發現。
	for what, re := range map[string]string{
		"<style> 區塊": `<style`,
		"style= 屬性(只認得 margin-top:<數字>rem)": `style="`,
		"script":               `<script`,
		"行內事件處理器(onclick= 之類)": `(?i)<[^>]+\son[a-z]+\s*=`,
		"同一個標籤兩個 class=(style 寫在 class 前面)": `<[^>]*\bclass="[^"]*"[^>]*\bclass=`,
	} {
		if m := regexp.MustCompile(re).FindString(html); m != "" {
			return "", "", fmt.Errorf("轉換之後還有%s:%q——網站的 CSP 會擋掉它。在 guideForSite 補上對應的轉換", what, m)
		}
	}
	if !strings.Contains(html, `href="/guide.css"`) || !strings.Contains(html, `class="site-back"`) {
		return "", "", fmt.Errorf("轉換沒有做完:要連到 /guide.css、要有回首頁的連結(docs/%s 的 <div class=\"wrap\"> 還在嗎?)", g.src)
	}
	return html, css, nil
}

// 網站上的指南必須就是 docs/guide.html(與英文版 docs/guide.en.html)的轉換結果:改了指南忘了重新產生,這裡會紅,
// 訊息告訴你跑哪個命令。(repo 接了 Cloudflare Workers Builds,合併後要去看線上有沒有更新。)
func TestGuideOnSiteIsCurrent(t *testing.T) {
	want := map[string]string{}
	for _, g := range guides {
		src, err := os.ReadFile(filepath.Join("..", "docs", g.src))
		if err != nil {
			t.Fatal(err)
		}
		html, css, err := guideForSite(strings.ReplaceAll(string(src), "\r\n", "\n"), g)
		if err != nil {
			t.Fatal(err)
		}
		// 兩份共用 /guide.css:比的是產生出來的 CSS,所以只有一份用到的 margin-top 值也會在這裡紅,不會讓另一頁安靜地少一段間距。
		// 兩頁都要宣告兩種語言的網址(寫死網址當尺):擋的是 guides 表打錯網址、產生器漏插;產生結果跟 site/public 的比對在下面。
		for _, link := range []string{
			`<link rel="alternate" hreflang="zh-Hant" href="https://capy.taislife.work/guide">`,
			`<link rel="alternate" hreflang="en" href="https://capy.taislife.work/en/guide">`,
			`<link rel="alternate" hreflang="x-default" href="https://capy.taislife.work/guide">`,
		} {
			if !strings.Contains(html, link) {
				t.Errorf("site/public/%s 少了 %s:跟網站其他雙語頁一樣要宣告另一種語言的網址", g.out, link)
			}
		}
		if prev, ok := want["guide.css"]; ok && css != prev {
			t.Fatalf("docs/%s 的 <style> 區塊(或行內 margin-top 用到的值)跟 docs/guide.html 不一樣:兩份指南共用 /guide.css,兩邊要改成一模一樣", g.src)
		}
		want["guide.css"], want[g.out] = css, html
	}
	for name, want := range want {
		path := filepath.Join("public", filepath.FromSlash(name))
		if *update {
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, _ := os.ReadFile(path)
		if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
			t.Errorf("site/public/%s 不是 docs/ 裡指南目前的轉換結果。重新產生:go test ./site/ -run TestGuideOnSiteIsCurrent -update", name)
		}
	}
}

// 英文版是中文版的完整翻譯:少翻一節、一段、一步、一條命令或一個連結,這裡要紅(只數結構,不比內容)。兩份也要在開頭互相連到對方。
// 連結寫網站的絕對網址:離線打開的檔案、Artifact、網站上三個地方都點得到,轉換時不必改寫。
func TestGuideTranslationsMatch(t *testing.T) {
	doc := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "docs", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	zh, en := doc("guide.html"), doc("guide.en.html")
	for _, tag := range []string{"<section", "<h2", "<h3", "<p", "<pre", "<li>", "<tr", "<a ", "<b>", "<kbd", `class="step"`, `class="note`, "<code>capy "} {
		if a, b := strings.Count(zh, tag), strings.Count(en, tag); a != b {
			t.Errorf("%s:docs/guide.html 有 %d 個、docs/guide.en.html 有 %d 個——兩份指南的結構要一樣", tag, a, b)
		}
	}
	for name, link := range map[string]string{
		"guide.html":    `href="https://capy.taislife.work/en/guide"`,
		"guide.en.html": `href="https://capy.taislife.work/guide"`,
	} {
		if !strings.Contains(doc(name), link) {
			t.Errorf("docs/%s 要在開頭連到另一種語言的指南:%s", name, link)
		}
	}
	if !strings.Contains(en, `<html lang="en">`) {
		t.Error(`docs/guide.en.html 要是 <html lang="en">`)
	}
}

// guideForSite 不認得的寫法要回錯誤,不可以安靜地產生壞掉的頁面(review #78 實跑出來的三種,加上行內事件處理器)。
func TestGuideForSiteRejectsWhatItCannotConvert(t *testing.T) {
	page := func(body string) string {
		return "<!doctype html>\n<style>\nbody{}\n</style>\n<div class=\"wrap\">\n" + body + "\n</div>\n"
	}
	html, css, err := guideForSite(page(`<p class="card" style="margin-top:2rem">x</p>`), guides[0])
	if err != nil || !strings.Contains(html, `class="card mt-2"`) || !strings.Contains(css, ".mt-2 { margin-top: 2rem; }") {
		t.Errorf("沒看過的 margin-top 值:class 與規則要一起產生:%v\n%s", err, html)
	}
	for name, src := range map[string]string{
		"style 寫在 class 前面": page(`<p style="margin-top:1.5rem" class="card">x</p>`),
		"別種行內樣式":            page(`<p style="color:red">x</p>`),
		"行內事件處理器":           page(`<p onclick="x()">x</p>`),
		"第二個 style 區塊":      page("<style>\np{}\n</style>"),
		"沒有 style 區塊":       "<div class=\"wrap\">\n</div>",
	} {
		if _, _, err := guideForSite(src, guides[0]); err == nil {
			t.Errorf("%s:要回錯誤", name)
		}
	}
}
