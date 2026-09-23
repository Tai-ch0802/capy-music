package i18n

// 守門測試(計畫 §2.5):漏翻、key 打錯、佔位符對不上、程式碼裡又冒出沒搬的中文,都在 CI 擋下。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode"

	"golang.org/x/text/feature/plural"
)

const importPath = "github.com/Tai-ch0802/capy-music/internal/i18n"

var placeholderRE = regexp.MustCompile(`\{([a-z_][a-z0-9_]*)\}`)

// placeholders:訊息用到的佔位符名稱;複數訊息一定要帶 count(用來選類別,不一定每個類別都印它)。
func placeholders(m message) map[string]bool {
	out := map[string]bool{}
	texts := []string{m.text}
	for _, s := range m.plural {
		texts = append(texts, s)
	}
	for _, s := range texts {
		for _, sm := range placeholderRE.FindAllStringSubmatch(s, -1) {
			out[sm[1]] = true
		}
	}
	if m.plural != nil {
		out["count"] = true
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := slices.Collect(maps.Keys(m))
	sort.Strings(out)
	return out
}

func TestLocaleFilesAreCanonicalTags(t *testing.T) {
	files, err := fs.Glob(localeFS, "locales/*.json")
	if err != nil || len(files) < 2 {
		t.Fatalf("至少要有 en 與 zh-TW:%v %v", files, err)
	}
	for _, f := range files {
		if code := strings.TrimSuffix(path.Base(f), ".json"); locales[code] == nil {
			t.Errorf("%s:檔名要是正規的 BCP 47 寫法(例如 zh-TW、ja),Normalize / config set 才找得到", f)
		}
	}
	if locales[Source] == nil || locales["zh-TW"] == nil {
		t.Fatal("en(來源)與 zh-TW 兩個目錄都要在")
	}
}

// TestCatalogsMatchSource:每個語系的 key、佔位符、單複數形狀都跟 en.json 一樣;複數類別符合該語言的 CLDR 規則。
func TestCatalogsMatchSource(t *testing.T) {
	src := locales[Source]
	for _, code := range Supported() {
		l := locales[code]
		for _, key := range sortedKeys(src.msgs) {
			sm := src.msgs[key]
			m, ok := l.msgs[key]
			switch {
			case !ok:
				t.Errorf("%s.json 缺 %q(不確定怎麼翻就先抄英文)", code, key)
			case m.text == "" && m.plural == nil:
				t.Errorf("%s.json 的 %q 是空字串:不確定怎麼翻就先抄英文(空譯文會讓訊息消失,有些檢查靠它非空,例如 Apple 的不可寫原因)", code, key)
			case (m.plural == nil) != (sm.plural == nil):
				t.Errorf("%s.json 的 %q:en 是%s訊息,這裡要一樣", code, key, map[bool]string{true: "一般", false: "複數"}[sm.plural == nil])
			case !maps.Equal(placeholders(m), placeholders(sm)):
				t.Errorf("%s.json 的 %q 佔位符 %v,en 是 %v", code, key, sortedKeys(placeholders(m)), sortedKeys(placeholders(sm)))
			}
		}
		for _, key := range sortedKeys(l.msgs) {
			if _, ok := src.msgs[key]; !ok {
				t.Errorf("%s.json 有 en.json 沒有的 %q(新 key 先加進 en.json)", code, key)
			}
		}
		need := map[plural.Form]bool{} // 整數 count 會用到的類別(程式只傳整數)
		for n := range 200 {
			need[plural.Cardinal.MatchPlural(l.tag, n, 0, 0, 0, 0)] = true
		}
		for _, key := range sortedKeys(l.msgs) {
			m := l.msgs[key]
			if m.plural == nil {
				continue
			}
			for name, f := range forms {
				_, has := m.plural[f]
				switch {
				case need[f] && !has:
					t.Errorf("%s.json 的 %q 缺複數類別 %q", code, key, name)
				case has && !need[f] && f != plural.Other:
					t.Errorf("%s.json 的 %q 有 %s 用不到的複數類別 %q", code, key, code, name)
				}
			}
		}
	}
}

// ── 掃程式碼 ──

type callSite struct {
	pos     string
	key     string // "" = 第一個參數不是字串字面
	names   []string
	bad     string // 參數形狀不對的原因
	dynamic bool   // 網頁的 t(key, params):第二個參數是變數而不是物件字面,佔位符到執行時才知道,不查
}

type goScan struct {
	calls []callSite
	cjk   map[string]int // repo 相對路徑 → 含中日韓字元的字串字面個數
}

var scanOnce = sync.OnceValues(scanRepo)

// scanRepo 掃整個 repo 的非測試 Go 檔:i18n.T / i18n.Errorf 的呼叫點,以及含中日韓字元的字串字面(註解不算)。
func scanRepo() (goScan, error) {
	root, err := filepath.Abs("../..")
	if err != nil {
		return goScan{}, err
	}
	s := goScan{cjk: map[string]int{}}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		rel0, _ := filepath.Rel(root, p)
		if ext := filepath.Ext(p); strings.HasPrefix(filepath.ToSlash(rel0), "internal/cli/webui/") && (ext == ".js" || ext == ".html" || ext == ".css") {
			b, err := os.ReadFile(p) // 網頁前端:註解以外的中日韓字元一行算一個(計畫 §2.5;T3 搬進目錄)
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(rel0)
			src := stripComments(string(b), ext)
			for _, line := range strings.Split(src, "\n") {
				if hasCJK(line) {
					s.cjk[rel]++
				}
			}
			// 呼叫點:JS 的 t()、HTML 的 data-i18n*。i18n.js 自己不算(同 Go 的 i18n 套件:t 在裡面拿變數當 key)。
			if ext != ".css" && rel != "internal/cli/webui/js/i18n.js" {
				s.calls = append(s.calls, scanWeb(rel, src, ext)...)
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		local := "" // 這個檔替 i18n 取的名字;i18n 自己的檔不算呼叫點(T 在裡面拿變數當 key)
		for _, im := range f.Imports {
			if v, _ := strconv.Unquote(im.Path.Value); v == importPath {
				local = "i18n"
				if im.Name != nil {
					local = im.Name.Name
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if x.Kind == token.STRING || x.Kind == token.CHAR {
					v, err := strconv.Unquote(x.Value)
					if err != nil {
						v = x.Value
					}
					if hasCJK(v) {
						s.cjk[rel]++
					}
				}
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok || local == "" {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != local || (sel.Sel.Name != "T" && sel.Sel.Name != "Errorf") {
					return true
				}
				c := callSite{pos: rel + ":" + strconv.Itoa(fset.Position(x.Pos()).Line)}
				if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					c.key, _ = strconv.Unquote(lit.Value)
				}
				rest := x.Args[1:]
				switch {
				case x.Ellipsis != token.NoPos:
					c.bad = "不可用 args... 展開:佔位符名稱要寫在呼叫點"
				case len(rest)%2 != 0:
					c.bad = "佔位符要成對(名稱, 值)"
				}
				for i := 0; i+1 < len(rest) && c.bad == ""; i += 2 {
					lit, ok := rest[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						c.bad = "佔位符名稱要是字串字面"
						break
					}
					name, _ := strconv.Unquote(lit.Value)
					c.names = append(c.names, name)
				}
				s.calls = append(s.calls, c)
			}
			return true
		})
		return nil
	})
	return s, err
}

// stripComments:拿掉 JS / CSS 的 // 與 /* */、HTML 的 <!-- -->;字串('…' "…" `…`)裡的不算註解。
// 區塊註解裡的換行留著:呼叫點的行號才對得上原檔。
// ponytail: 不分辨 JS 的除號與正規表示式字面;正規表示式裡有引號的極少數情況會讓字串狀態錯位,T3 真遇到再換 tokenizer。
func stripComments(src, ext string) string {
	var b strings.Builder
	newlines := func(s string) { b.WriteString(strings.Repeat("\n", strings.Count(s, "\n"))) }
	if ext == ".html" {
		for {
			i := strings.Index(src, "<!--")
			if i < 0 {
				b.WriteString(src)
				return b.String()
			}
			b.WriteString(src[:i])
			j := strings.Index(src[i:], "-->")
			if j < 0 {
				return b.String()
			}
			newlines(src[i : i+j])
			src = src[i+j+3:]
		}
	}
	var quote byte
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				b.WriteByte(src[i])
			} else if c == quote || (c == '\n' && quote != '`') { // '…' 與 "…" 不能跨行:正規表示式裡的引號只會錯位到行尾
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
			b.WriteByte(c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/' && ext == ".js":
			for i < len(src) && src[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				return b.String()
			}
			newlines(src[i : i+2+j])
			i += j + 3
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

var (
	webCallRE = regexp.MustCompile(`(?:^|[^\w$.])t\(`) // t( 前面不是識別字元或 .:x.t(、split(、at( 都不算
	webAttrRE = regexp.MustCompile(`\sdata-i18n(?:-aria-label|-placeholder|-title)?="([^"]*)"`)
	webNameRE = regexp.MustCompile(`^(?:([A-Za-z_$][\w$]*)|'([^']*)'|"([^"]*)")\s*(:|$)`)
)

// scanWeb:網頁前端的呼叫點(src 已拿掉註解)。JS 是 t('key') / t("key", { a, b: v }):第二個參數是物件字面就取它的屬性名稱
// 當佔位符;沒有第二個參數 = 沒給佔位符(同 Go 的 T("key"));是別的運算式(變數)就不查佔位符。
// HTML 是 data-i18n / data-i18n-aria-label / data-i18n-placeholder / data-i18n-title:靜態文字,不能帶佔位符。
func scanWeb(rel, src, ext string) []callSite {
	var out []callSite
	pos := func(i int) string { return rel + ":" + strconv.Itoa(1+strings.Count(src[:i], "\n")) }
	if ext == ".html" {
		for _, m := range webAttrRE.FindAllStringSubmatchIndex(src, -1) {
			out = append(out, callSite{pos: pos(m[0]), key: src[m[2]:m[3]]})
		}
		return out
	}
	const space = " \t\r\n"
	for _, m := range webCallRE.FindAllStringIndex(src, -1) {
		c := callSite{pos: pos(m[1])}
		rest := strings.TrimLeft(src[m[1]:], space)
		if rest != "" && (rest[0] == '\'' || rest[0] == '"') {
			if end := strings.IndexByte(rest[1:], rest[0]); end >= 0 {
				c.key, rest = rest[1:1+end], strings.TrimLeft(rest[2+end:], space)
			}
		}
		if c.key != "" && strings.HasPrefix(rest, ",") {
			switch rest = strings.TrimLeft(rest[1:], space); {
			case strings.HasPrefix(rest, "{"):
				c.names, c.bad = objectNames(rest)
			case !strings.HasPrefix(rest, ")"): // t('k',) 的結尾逗號不算第二個參數
				c.dynamic = true
			}
		}
		out = append(out, c)
	}
	return out
}

// objectNames:s 開頭的物件字面 {…} 的屬性名稱(簡寫 {a} 與 a: v 都算)。只在最外層的逗號切開:值裡的字串、括號、
// 巢狀物件不影響。
// ponytail: 樣板字串 `…${…}…` 的 ${} 裡再套引號會讓引號狀態錯位;真遇到再換 tokenizer。
func objectNames(s string) (names []string, bad string) {
	var parts []string
	depth, quote, start := 0, byte(0), 1
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '{' || c == '(' || c == '[':
			depth++
		case c == ',' && depth == 1:
			parts, start = append(parts, s[start:i]), i+1
		case c == '}' || c == ')' || c == ']':
			if depth--; depth > 0 {
				continue
			}
			for _, p := range append(parts, s[start:i]) {
				m := webNameRE.FindStringSubmatch(strings.TrimSpace(p))
				switch {
				case strings.TrimSpace(p) == "": // 結尾逗號
				case m == nil:
					return nil, "佔位符名稱要寫在呼叫點(不可用 ...展開或 [算出來的名稱])"
				default:
					names = append(names, m[1]+m[2]+m[3])
				}
			}
			return names, ""
		}
	}
	return nil, "物件字面沒有結尾"
}

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) ||
			(r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) { // 、。「」 與全形標點也算:它們一樣要進目錄
			return true
		}
	}
	return false
}

func scan(t *testing.T) goScan {
	t.Helper()
	s, err := scanOnce()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestCodeKeysExistInCatalog:每個呼叫點的 key 是字串字面、在 en.json 裡,給的佔位符跟目錄一字不差。
func TestCodeKeysExistInCatalog(t *testing.T) {
	src := locales[Source]
	for _, c := range scan(t).calls {
		m, ok := src.msgs[c.key]
		switch {
		case c.key == "":
			t.Errorf("%s:key 要寫成字串字面(不可在執行時拼出來),守門測試才查得到", c.pos)
		case !ok:
			t.Errorf("%s:en.json 沒有 %q", c.pos, c.key)
		case c.bad != "":
			t.Errorf("%s:%s", c.pos, c.bad)
		case c.dynamic:
		default:
			got := map[string]bool{}
			for _, n := range c.names {
				got[n] = true
			}
			if want := placeholders(m); !maps.Equal(got, want) {
				t.Errorf("%s:%q 給了 %v,目錄要的是 %v", c.pos, c.key, sortedKeys(got), sortedKeys(want))
			}
		}
	}
}

func TestNoUnusedKeys(t *testing.T) {
	used := map[string]bool{"lang.name": true} // LocaleName 拿語系代碼查它(不經 T),給網頁的語言選單
	for _, c := range scan(t).calls {
		used[c.key] = true
	}
	for _, key := range sortedKeys(locales[Source].msgs) {
		if !used[key] {
			t.Errorf("en.json 的 %q 沒有任何程式碼在用:刪掉它(所有語系一起刪)", key)
		}
	}
}

// TestNoCJKOutsideCatalog:使用者看得到的字只准在語系目錄裡。遷移期間 cjkAllowlist 列出還沒搬的檔,只准縮小。
func TestNoCJKOutsideCatalog(t *testing.T) {
	cjk := scan(t).cjk
	allowed := map[string]bool{}
	for _, f := range cjkAllowlist {
		allowed[f] = true
		if cjk[f] == 0 {
			t.Errorf("%s 已經沒有中日韓字串了:從 cjkAllowlist 拿掉它(白名單只准縮小)", f)
		}
	}
	for _, f := range sortedKeys(cjk) {
		if !allowed[f] {
			t.Errorf("%s 有 %d 個含中日韓字元的字串字面:搬進 internal/i18n/locales(en 與 zh-TW 同時補)", f, cjk[f])
		}
	}
}

func TestCJKAllowlistOnlyShrinks(t *testing.T) {
	switch n := len(cjkAllowlist); {
	case n > cjkAllowlistLen:
		t.Fatalf("白名單有 %d 個檔,上限 %d:白名單只准縮小,新的字串直接搬進語系目錄", n, cjkAllowlistLen)
	case n < cjkAllowlistLen:
		t.Fatalf("白名單剩 %d 個檔:把 cjkAllowlistLen 一起調成 %d,釘住新的上限", n, n)
	}
}

// TestDefaultIsEnglishOnceMigrated:白名單清空 = 字串搬完,預設語系就該是英文(決策 50)。
func TestDefaultIsEnglishOnceMigrated(t *testing.T) {
	if locales[productionDefault] == nil { // 測試二進位的 Default() 不經這個常數:打錯只會在發出去的 binary 的 init 才炸
		t.Fatalf("productionDefault %q 沒有對應的目錄", productionDefault)
	}
	if len(cjkAllowlist) == 0 && productionDefault != Source {
		t.Fatalf("CJK 白名單已清空,productionDefault 要改成 %q", Source)
	}
}

func TestStripComments(t *testing.T) {
	for _, tc := range []struct{ src, ext, want string }{
		{"a // 註解\nb", ".js", "a \nb"},
		{"x /* 註解 */ y", ".js", "x  y"},
		{"u = 'http://中'; // 註解", ".js", "u = 'http://中'; \n"},
		{"s = \"不是 /* 註解 */\"", ".js", "s = \"不是 /* 註解 */\""},
		{"<p>字</p><!-- 註解 --><b>", ".html", "<p>字</p><b>"},
		{"a { content: '字'; } /* 註解 */", ".css", "a { content: '字'; } "},
		{"url(http://x) // 不是註解", ".css", "url(http://x) // 不是註解"},
		{"r = /\"/g;\n// 註解\nx", ".js", "r = /\"/g;\n\nx"}, // 正規表示式裡的引號:錯位到行尾為止,下一行的註解照樣剝掉
		{"a /* 一\n二 */ b", ".js", "a \n b"},                // 區塊註解的換行留著:行號對得上原檔
		{"<p>\n<!-- 一\n二 -->\n</p>", ".html", "<p>\n\n\n</p>"},
	} {
		if got := stripComments(tc.src, tc.ext); got != tc.want {
			t.Errorf("%s %q:得 %q,要 %q", tc.ext, tc.src, got, tc.want)
		}
	}
}

func TestScanWeb(t *testing.T) {
	for _, tc := range []struct {
		src, ext string
		want     []callSite // pos 只比行號部分
	}{
		{"t('webui.a')", ".js", []callSite{{pos: "1", key: "webui.a"}}},
		{`x = t("webui.a", { count, name: n })`, ".js", []callSite{{pos: "1", key: "webui.a", names: []string{"count", "name"}}}},
		{"x\ny(t(\n  'webui.a',\n  { 'q': 1, n: f(a, b), s: xs.join(', '), o: { z: 1 }, },\n))", ".js", // 值裡的逗號、括號、巢狀物件不切
			[]callSite{{pos: "2", key: "webui.a", names: []string{"q", "n", "s", "o"}}}},
		{"t('webui.a', params)", ".js", []callSite{{pos: "1", key: "webui.a", dynamic: true}}},
		{"t('webui.a',)", ".js", []callSite{{pos: "1", key: "webui.a"}}},
		{"t(key)", ".js", []callSite{{pos: "1"}}},                                           // key 不是字面:TestCodeKeysExistInCatalog 會報
		{"t(`webui.a`)", ".js", []callSite{{pos: "1"}}},                                     // 樣板字串也不算字面
		{"t('webui.a', { ...p })", ".js", []callSite{{pos: "1", key: "webui.a", bad: "x"}}}, // 展開:佔位符看不出來
		{"t('webui.a', { [k]: 1 })", ".js", []callSite{{pos: "1", key: "webui.a", bad: "x"}}},
		{"x.t('a'); s.split('a'); a.at(1); rotate(1); set('a'); $t('a'); _t('a')", ".js", nil}, // 不是 t( 的呼叫
		{`<span data-i18n="webui.a"></span><input data-i18n-placeholder="webui.b">` + "\n" + `<b data-i18n-aria-label="webui.c" data-i18n-title="webui.d">`, ".html",
			[]callSite{{pos: "1", key: "webui.a"}, {pos: "1", key: "webui.b"}, {pos: "2", key: "webui.c"}, {pos: "2", key: "webui.d"}}},
		{`<p data-x="webui.a">`, ".html", nil},
	} {
		got := scanWeb("f", tc.src, tc.ext)
		for i := range got {
			got[i].pos = strings.TrimPrefix(got[i].pos, "f:")
			if got[i].bad != "" {
				got[i].bad = "x"
			}
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %q:\n得 %+v\n要 %+v", tc.ext, tc.src, got, tc.want)
		}
	}
}

func TestScanSeesThisRepo(t *testing.T) { // 掃描本身壞掉(路徑錯、什麼都沒掃到)時,上面幾個測試會假性通過
	s := scan(t)
	if len(s.calls) < 30 {
		t.Fatalf("只掃到 %d 個 i18n 呼叫點,掃描路徑可能不對", len(s.calls))
	}
	if _, err := os.Stat("../../go.mod"); err != nil {
		t.Fatal(err)
	}
}
