package i18n

import (
	"errors"
	"io/fs"
	"testing"
)

// fakeLocales:換成測試自己的目錄(真目錄受守門測試約束,放不進只給測試用的 key),結束還原。
func fakeLocales(t *testing.T, files map[string]string, current string) {
	t.Helper()
	prevL, prevC := locales, cur.Load()
	t.Cleanup(func() { locales = prevL; cur.Store(prevC) })
	locales = map[string]*locale{}
	for code, js := range files {
		l, err := parseLocale(code, []byte(js))
		if err != nil {
			t.Fatal(err)
		}
		locales[code] = l
	}
	cur.Store(locales[current])
}

func TestDefaultInTestBinaryIsZhTW(t *testing.T) {
	if Default() != "zh-TW" || Current() != "zh-TW" {
		t.Fatalf("測試二進位的預設語系要是 zh-TW(既有中文斷言靠它):Default %s Current %s", Default(), Current())
	}
}

func TestNormalizeAndSet(t *testing.T) {
	t.Cleanup(func() { Set(Default()) })
	for in, want := range map[string]string{"zh_tw": "zh-TW", "ZH-tw": "zh-TW", "EN": "en", "en": "en"} {
		if got, ok := Normalize(in); !ok || got != want {
			t.Errorf("Normalize(%q) = %q %v,要 %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "xx", "zh-CN", "zh-Hant-TW", "en-US", "klingon"} {
		if _, ok := Normalize(in); ok {
			t.Errorf("Normalize(%q) 不該成功:不支援的語系不能被配到相近的語系", in)
		}
	}
	Set("en")
	if Set("zh-CN") || Current() != "en" {
		t.Fatalf("不支援的語系 Set 要回 false 且語系不變:%s", Current())
	}
	if !Set("zh_tw") || Current() != "zh-TW" {
		t.Fatalf("Set 也要寬鬆接受:%s", Current())
	}
}

func TestTPlaceholdersPluralAndFallback(t *testing.T) {
	fakeLocales(t, map[string]string{
		"en": `{"a": "A {x} {y}", "pct": "100% {x}", "only.en": "E", "p": {"one": "{count} track", "other": "{count} tracks"},
			"ponly": {"one": "{count} item", "other": "{count} items"}}`,
		"zh-TW": `{"a": "甲 {y} {x}", "pct": "百分之百 {x}", "p": {"other": "{count} 首"}}`,
	}, "zh-TW")
	check := func(lang string, got func() string, want string) {
		t.Helper()
		Set(lang)
		if g := got(); g != want {
			t.Errorf("[%s] 得 %q,要 %q", lang, g, want)
		}
	}
	check("zh-TW", func() string { return T("a", "x", 1, "y", "b") }, "甲 b 1") // 語序由譯文決定
	check("zh-TW", func() string { return T("only.en") }, "E")                 // 缺 key 退回英文
	check("zh-TW", func() string { return T("nope") }, "nope")                 // 英文也缺就回 key
	check("zh-TW", func() string { return T("p", "count", 1) }, "1 首")
	check("zh-TW", func() string { return T("ponly", "count", 1) }, "1 item") // 退回英文時用英文的複數規則
	check("en", func() string { return T("p", "count", 1) }, "1 track")
	check("en", func() string { return T("p", "count", 0) }, "0 tracks")
	check("en", func() string { return T("p", "count", 21) }, "21 tracks")
	check("en", func() string { return T("p", "count", int64(1)) }, "1 track")
	check("en", func() string { return T("p", "count", -1) }, "-1 track")
	check("en", func() string { return T("p", "count", uint(1)) }, "1 track") // 任何整數型別都認
	check("en", func() string { return T("p", "count", int32(1)) }, "1 track")
	type tracks int
	check("en", func() string { return T("p", "count", tracks(1)) }, "1 track")
	check("en", func() string { return T("a", "x", "{y}", "y", "z") }, "A {y} z") // 值裡的 {y} 不再被替換
	check("en", func() string { return T("pct", "x", "%d") }, "100% %d")          // 不經 fmt
}

func TestErrorfIsLazyAndWraps(t *testing.T) {
	fakeLocales(t, map[string]string{
		"en":    `{"e": "oops {n}", "w": "wrapped: {err}"}`,
		"zh-TW": `{"e": "糟糕 {n}", "w": "包起來:{err}"}`,
	}, "en")
	sentinel := Errorf("e", "n", 1) // 像 package 層級的錯誤值:語系設定之前就建好
	Set("zh-TW")
	if sentinel.Error() != "糟糕 1" {
		t.Fatalf("Error() 要在印出時才翻:%q", sentinel.Error())
	}
	if errors.Is(Errorf("e", "n", 1), sentinel) {
		t.Fatal("兩個 Errorf 不是同一個錯誤值:errors.Is 比的是指標")
	}
	w := Errorf("w", "err", sentinel)
	if !errors.Is(w, sentinel) || w.Error() != "包起來:糟糕 1" {
		t.Fatalf("args 裡的 error 要被包起來:%v %q", errors.Is(w, sentinel), w.Error())
	}
	var pe *fs.PathError
	if !errors.As(Errorf("w", "err", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist}), &pe) || pe.Path != "x" {
		t.Fatal("errors.As 也要走得通")
	}
}

func TestParseLocaleRejectsBadPluralCategory(t *testing.T) {
	if _, err := parseLocale("en", []byte(`{"p": {"single": "x", "other": "y"}}`)); err == nil {
		t.Fatal("不是 CLDR 類別的複數鍵要被拒")
	}
	if _, err := parseLocale("en", []byte(`{"p": 3}`)); err == nil {
		t.Fatal("值不是字串也不是物件要被拒")
	}
}
