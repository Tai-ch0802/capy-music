// Package i18n 是使用者看得到的字串的語系目錄(附錄 C 決策 50;計畫 docs/superpowers/plans/2026-09-23-i18n.md)。
//
// 葉子套件:只依賴標準函式庫與 x/text,不讀 config——語系由 cli 層在建命令樹之前 Set,
// 所以 auth、provider、canon 這些低層套件也能用它而不繞成循環依賴。
// 每個語系是 locales/ 裡的一個 <BCP 47>.json;新增語系 = 新增一個檔(步驟見 README.md)。
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
)

// Source 是來源語系:其他語系缺的 key 退回它,新語系從它複製。
const Source = "en"

// productionDefault:config 沒設 language 時的語系。搬字串期間維持 zh-TW,main 上的 dev binary 才不會半中半英;
// 清空 CJK 白名單的那個 PR 改成 "en"(TestDefaultIsEnglishOnceMigrated 會逼這件事)。
const productionDefault = "zh-TW"

//go:embed locales/*.json
var localeFS embed.FS

type message struct {
	text   string                 // 一般訊息
	plural map[plural.Form]string // 複數訊息(依 {count} 選 CLDR 類別);nil = 一般訊息
}

type locale struct {
	tag  language.Tag
	msgs map[string]message
}

var forms = map[string]plural.Form{
	"other": plural.Other, "zero": plural.Zero, "one": plural.One,
	"two": plural.Two, "few": plural.Few, "many": plural.Many,
}

var (
	locales = load()
	cur     atomic.Pointer[locale]
)

func init() { cur.Store(locales[Default()]) }

// load 在 init 讀嵌入的目錄;格式錯就 panic(嵌入的檔,任何一個測試都會先炸,發不出去)。
func load() map[string]*locale {
	files, err := fs.Glob(localeFS, "locales/*.json")
	if err != nil {
		panic(err)
	}
	out := map[string]*locale{}
	for _, f := range files {
		b, err := localeFS.ReadFile(f)
		if err != nil {
			panic(err)
		}
		l, err := parseLocale(strings.TrimSuffix(path.Base(f), ".json"), b)
		if err != nil {
			panic(fmt.Sprintf("i18n: %s: %v", f, err))
		}
		out[l.tag.String()] = l
	}
	return out
}

// parseLocale:值是字串(一般訊息)或「CLDR 類別 → 字串」的物件(複數訊息)。
func parseLocale(code string, b []byte) (*locale, error) {
	tag, err := language.Parse(code)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	l := &locale{tag: tag, msgs: make(map[string]message, len(raw))}
	for k, v := range raw {
		var m message
		if json.Unmarshal(v, &m.text) != nil {
			var ps map[string]string
			if err := json.Unmarshal(v, &ps); err != nil {
				return nil, fmt.Errorf("%q must be a string or an object of plural forms: %w", k, err)
			}
			m.plural = map[plural.Form]string{}
			for cat, s := range ps {
				form, ok := forms[cat]
				if !ok {
					return nil, fmt.Errorf("%q: %q is not a CLDR plural category", k, cat)
				}
				m.plural[form] = s
			}
		}
		l.msgs[k] = m
	}
	return l, nil
}

// Default:config 沒設 language 時用的語系。測試二進位裡固定 zh-TW:既有測試斷言的中文就是 zh-TW 目錄的內容,
// 一處就讓每個套件的中文斷言照舊有效;要測英文的測試自己 Set("en") 並在 Cleanup 還原。
func Default() string {
	if testing.Testing() {
		return "zh-TW"
	}
	return productionDefault
}

// Supported 回傳支援的語系代碼(排序過)。
func Supported() []string {
	out := make([]string, 0, len(locales))
	for code := range locales {
		out = append(out, code)
	}
	slices.Sort(out)
	return out
}

// Normalize 把使用者打的代碼換成支援清單裡的寫法:大小寫與底線寬鬆(zh_tw → zh-TW);不支援回 false。
// 刻意不用 language.Matcher:它會把 zh-CN 配到 zh-TW,使用者以為設成功了。
func Normalize(s string) (string, bool) {
	tag, err := language.Parse(s)
	if err != nil {
		return "", false
	}
	_, ok := locales[tag.String()]
	return tag.String(), ok
}

// Set 換目前語系(寬鬆接受大小寫與底線);不支援回 false、語系不變。
func Set(code string) bool {
	code, ok := Normalize(code)
	if ok {
		cur.Store(locales[code])
	}
	return ok
}

// Current 回傳目前語系代碼。
func Current() string { return cur.Load().tag.String() }

// Messages 回傳 lang 目錄裡 want 選中的 key 的原始值(網頁的 /api/i18n 用,可直接 JSON 編碼):一般訊息是字串,
// 複數訊息是「CLDR 類別名稱 → 字串」。lang 缺的 key 逐個退回英文;lang 不支援就整份英文。
// 只讀嵌入的目錄、不動目前語系:直達端點在 runMu 外呼叫它。
func Messages(lang string, want func(key string) bool) map[string]any {
	l := locales[lang]
	out := map[string]any{}
	for key, m := range locales[Source].msgs {
		if !want(key) {
			continue
		}
		if l != nil {
			if lm, ok := l.msgs[key]; ok {
				m = lm
			}
		}
		if m.plural == nil {
			out[key] = m.text
			continue
		}
		ps := make(map[string]string, len(m.plural))
		for name, f := range forms {
			if s, ok := m.plural[f]; ok {
				ps[name] = s
			}
		}
		out[key] = ps
	}
	return out
}

// LocaleName 回傳語系用自己的語言寫的名稱(目錄裡的 lang.name:English、繁體中文),給語言選單;不支援的代碼原樣回傳。
func LocaleName(code string) string {
	if l := locales[code]; l != nil && l.msgs["lang.name"].text != "" {
		return l.msgs["lang.name"].text
	}
	return code
}

// T 翻譯 key。args 是成對的「佔位符名稱, 值」:T("x.done", "count", n, "name", s) 填 {count} 與 {name}。
// 目前語系缺 key 退回英文,英文也缺就回 key 本身(守門測試會先抓到)。
// key 與佔位符名稱一律寫字串字面,守門測試才能靜態檢查;複數訊息依名為 count 的整數選類別。
func T(key string, args ...any) string {
	l := cur.Load()
	m, ok := l.msgs[key]
	if !ok {
		l = locales[Source]
		if m, ok = l.msgs[key]; !ok {
			return key
		}
	}
	s := m.text
	if m.plural != nil {
		var n int64 // 任何整數型別都認(uint、int32、自訂的 type Count int…);不是整數就當 0
		for i := 0; i+1 < len(args); i += 2 {
			if args[i] == "count" {
				switch v := reflect.ValueOf(args[i+1]); {
				case v.CanInt():
					n = v.Int() % 10_000_000 // MatchPlural 允許取模
				case v.CanUint():
					n = int64(v.Uint() % 10_000_000)
				}
			}
		}
		if n < 0 {
			n = -n
		}
		var found bool
		if s, found = m.plural[plural.Cardinal.MatchPlural(l.tag, int(n), 0, 0, 0, 0)]; !found {
			s = m.plural[plural.Other]
		}
	}
	if len(args) == 0 {
		return s
	}
	// 不經 fmt:譯文裡的 % 不會被當成格式;Replacer 一趟替換,值裡的 {x} 也不會再被替換。
	pairs := make([]string, 0, len(args))
	for i := 0; i+1 < len(args); i += 2 {
		pairs = append(pairs, "{"+fmt.Sprint(args[i])+"}", fmt.Sprint(args[i+1]))
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// msgError 在 Error() 被呼叫時才翻:package 層級的錯誤值在 init(語系設定之前)就建好,印出時才是對的語言。
type msgError struct {
	key  string
	args []any
}

func (e *msgError) Error() string { return T(e.key, e.args...) }

// Unwrap 回傳 args 裡所有的 error,errors.Is / errors.As 照舊走得通(同 fmt.Errorf 的 %w)。
func (e *msgError) Unwrap() []error {
	var errs []error
	for i := 1; i < len(e.args); i += 2 {
		if err, ok := e.args[i].(error); ok {
			errs = append(errs, err)
		}
	}
	return errs
}

// Errorf 回傳訊息為 T(key, args...) 的 error;args 裡的 error 會被包起來(errors.Is / As 看得到)。
// 取代 package 層級的 errors.New:回傳的是指標,errors.Is 比的是同一個值。
func Errorf(key string, args ...any) error { return &msgError{key: key, args: args} }
