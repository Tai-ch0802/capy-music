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
	pos   string
	key   string // "" = 第一個參數不是字串字面
	names []string
	bad   string // 參數形狀不對的原因
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
			for _, line := range strings.Split(stripComments(string(b), ext), "\n") {
				if hasCJK(line) {
					s.cjk[filepath.ToSlash(rel0)]++
				}
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
// ponytail: 不分辨 JS 的除號與正規表示式字面;正規表示式裡有引號的極少數情況會讓字串狀態錯位,T3 真遇到再換 tokenizer。
func stripComments(src, ext string) string {
	var b strings.Builder
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
			i += j + 3
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
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
	used := map[string]bool{}
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
	} {
		if got := stripComments(tc.src, tc.ext); got != tc.want {
			t.Errorf("%s %q:得 %q,要 %q", tc.ext, tc.src, got, tc.want)
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
