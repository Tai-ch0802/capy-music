package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// recordProviderID:把 newProvider 換成只記錄被要求的 id 的假 provider。
func recordProviderID(t *testing.T) *string {
	t.Helper()
	var got string
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		got = id
		return fakeProvider{}, nil
	}
	t.Cleanup(func() { newProvider = orig })
	return &got
}

func TestDefaultProviderFromConfig(t *testing.T) {
	setCLITestConfig(t)
	got := recordProviderID(t)

	_, _ = runCLI(t, "search", "x") // 未設 → spotify
	if *got != "spotify" {
		t.Fatalf("未設 default_provider 應走 spotify,得到 %q", *got)
	}

	if out, err := runCLI(t, "config", "set", "default_provider", "apple"); err != nil || !strings.Contains(out, "default_provider = apple") {
		t.Fatalf("config set 失敗:%v %q", err, out)
	}
	_, _ = runCLI(t, "search", "x") // 設 apple、不帶 flag → apple
	if *got != "apple" {
		t.Fatalf("default_provider=apple 時不帶 flag 應走 apple,得到 %q", *got)
	}
	_, _ = runCLI(t, "search", "x", "--provider", "spotify") // flag 永遠優先
	if *got != "spotify" {
		t.Fatalf("--provider 應覆蓋 config,得到 %q", *got)
	}
}

func TestConfigSetRejectsUnknownKeyAndValue(t *testing.T) {
	setCLITestConfig(t)
	_, err := runCLI(t, "config", "set", "default_provider", "google")
	if err == nil || !strings.Contains(err.Error(), "spotify") || !strings.Contains(err.Error(), "apple") {
		t.Fatalf("非法值應被拒且列出合法值:%v", err)
	}
	_, err = runCLI(t, "config", "set", "theme", "dark")
	if err == nil || !strings.Contains(err.Error(), "default_provider") {
		t.Fatalf("未知 key 應被拒且列出可用 key:%v", err)
	}
	if c, _ := config.Load(); c.DefaultProvider != "" {
		t.Fatal("被拒的 set 不得落地")
	}
}

func TestConfigGetAndListPlainText(t *testing.T) {
	setCLITestConfig(t)
	out, err := runCLI(t, "config", "get", "default_provider")
	if err != nil || out != "\n" {
		t.Fatalf("未設時 get 應印空行,得到 %v %q", err, out)
	}
	_, _ = runCLI(t, "config", "set", "default_provider", "apple")
	if out, _ := runCLI(t, "config", "get", "default_provider"); out != "apple\n" {
		t.Fatalf("get 非 TTY 應印裸值,得到 %q", out)
	}
	_ = config.Save(&config.Config{DefaultProvider: "apple", SpotifyClientID: "cid", AppleStorefront: "tw"})
	if out, _ := runCLI(t, "config", "list"); out != "default_provider\tapple\nlocal_root\t\nspotify_client_id\tcid\napple_storefront\ttw\nlanguage\t\n" {
		t.Fatalf("list 非 TTY 應列出五個非機密欄位 key\\tvalue(language 加在最後,既有的行位置不動),得到 %q", out)
	}
	if out, _ := runCLI(t, "config", "get", "spotify_client_id"); out != "cid\n" {
		t.Fatalf("get 應支援三個欄位,得到 %q", out)
	}
	if _, err := runCLI(t, "config", "set", "spotify_client_id", "x"); err == nil || !strings.Contains(err.Error(), "auth login spotify") {
		t.Fatalf("set spotify_client_id 應指向 auth login:%v", err)
	}
	if _, err := runCLI(t, "config", "get", "nope"); err == nil {
		t.Fatal("未知 key 的 get 應報錯")
	}
}

func TestBrokenConfigDoesNotBreakHelp(t *testing.T) {
	setCLITestConfig(t)
	dir, _ := config.Dir()
	_ = os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "search", "--help")
	if err != nil || !strings.Contains(out, "--provider") {
		t.Fatalf("config 壞掉時 --help 仍須正常:%v %q", err, out)
	}
	if !strings.Contains(out, `default "spotify"`) {
		t.Fatalf("config 讀不到時 flag 預設應退回 spotify:%q", out)
	}
	_, err = runCLI(t, "config", "set", "default_provider", "apple")
	if err == nil || !strings.Contains(err.Error(), "刪掉該檔") {
		t.Fatalf("config 壞掉時 set 的錯誤要告訴人怎麼救:%v", err)
	}
}

func TestInvalidDefaultProviderInFileFallsBackToSpotify(t *testing.T) {
	setCLITestConfig(t)
	_ = config.Save(&config.Config{DefaultProvider: "tidal"}) // 手改檔案 / 別版 binary 寫進來的值
	resetDefaultProvider()
	got := recordProviderID(t)
	out, err := runCLI(t, "search", "--help")
	if err != nil || !strings.Contains(out, `default "spotify"`) || strings.Contains(out, "tidal") {
		t.Fatalf("非法 default_provider 不得成為 flag 預設值:%v %q", err, out)
	}
	_, _ = runCLI(t, "search", "x")
	if *got != "spotify" {
		t.Fatalf("非法值應退回 spotify,得到 %q", *got)
	}
}

func TestDefaultProviderReadOncePerProcess(t *testing.T) {
	setCLITestConfig(t)
	_ = config.Save(&config.Config{DefaultProvider: "apple"})
	resetDefaultProvider()
	if defaultProvider() != "apple" {
		t.Fatal("應讀到 apple")
	}
	_ = config.Save(&config.Config{DefaultProvider: "spotify"}) // 檔案改了但沒重置 → 仍是快取值
	if defaultProvider() != "apple" {
		t.Fatal("同一 process 應只讀一次 config(未重置前維持快取值)")
	}
	resetDefaultProvider()
	if defaultProvider() != "spotify" {
		t.Fatal("重置後應重讀")
	}
}

func TestLoginHintsDefaultProviderOnlyWhenUnset(t *testing.T) {
	setCLITestConfig(t)
	const cid = "0123456789abcdef0123456789abcdef"
	orig := spotifyLogin
	spotifyLogin = fakeLoginOK(t, cid)
	t.Cleanup(func() { spotifyLogin = orig })
	_ = config.Save(&config.Config{SpotifyClientID: cid})

	out, err := runCLI(t, "auth", "login", "spotify")
	if err != nil || !strings.Contains(out, "config set default_provider spotify") {
		t.Fatalf("未設預設平台時登入成功應提示:%v %q", err, out)
	}
	_ = config.Save(&config.Config{SpotifyClientID: cid, DefaultProvider: "apple"})
	out, _ = runCLI(t, "auth", "login", "spotify")
	if strings.Contains(out, "config set default_provider") {
		t.Fatalf("已設預設平台就不該再提示:%q", out)
	}
}

// withLanguage:換語系並在測試結束還原(i18n 是 process 全域;這個 repo 沒有 t.Parallel)。
func withLanguage(t *testing.T, lang string) {
	t.Helper()
	prev := i18n.Current()
	if !i18n.Set(lang) {
		t.Fatalf("不支援 %s", lang)
	}
	t.Cleanup(func() { i18n.Set(prev) })
}

// hasCJK:英文模式的輸出不該有中日韓字元(含 、。 與全形標點)。
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			return true
		}
	}
	return false
}

func TestConfigSetLanguage(t *testing.T) {
	setCLITestConfig(t)
	withLanguage(t, i18n.Current()) // 只為了還原:config set language 會當場換語系
	if out, err := runCLI(t, "config", "set", "language", "EN"); err != nil || out != "language = en\n" || i18n.Current() != "en" {
		t.Fatalf("大小寫寬鬆、印存下去的值、當場換語系:%v %q %s", err, out, i18n.Current())
	}
	if out, err := runCLI(t, "config", "set", "language", "zh_tw"); err != nil || out != "language = zh-TW\n" || i18n.Current() != "zh-TW" {
		t.Fatalf("底線也認:%v %q %s", err, out, i18n.Current())
	}
	if out, _ := runCLI(t, "config", "get", "language"); out != "zh-TW\n" {
		t.Fatalf("存正規化後的代碼:%q", out)
	}
	_, err := runCLI(t, "config", "set", "language", "zh-CN") // 不能被配到相近的 zh-TW
	if err == nil || !strings.Contains(err.Error(), "en 或 zh-TW") || !strings.Contains(err.Error(), `"zh-CN"`) {
		t.Fatalf("不支援的語系要被拒並列出可用的:%v", err)
	}
	if c, _ := config.Load(); c.Language != "zh-TW" || i18n.Current() != "zh-TW" {
		t.Fatalf("被拒的 set 不得落地、也不換語系:%q %s", c.Language, i18n.Current())
	}
}

// TestApplyLanguage:Execute 與網頁 job 在建命令樹之前走這裡。runCLI 不經 Execute,所以直接測。
func TestApplyLanguage(t *testing.T) {
	setCLITestConfig(t)
	withLanguage(t, i18n.Current())
	dir, _ := config.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, file, want string
		warn             bool
	}{
		{"沒有 config.json", "", i18n.Default(), false},
		{"en", `{"language":"en"}`, "en", false},
		{"手改成底線", `{"language":"zh_tw"}`, "zh-TW", false},
		{"不認得的值", `{"language":"klingon"}`, i18n.Default(), true},
		{"config 壞掉:由讀它的命令報,這裡不吵", `{not json`, i18n.Default(), false},
	} {
		_ = os.Remove(filepath.Join(dir, "config.json"))
		if c.file != "" {
			if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(c.file), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		i18n.Set("en") // 起點刻意跟預期不同:證明是 applyLanguage 設的
		if c.want == "en" {
			i18n.Set("zh-TW")
		}
		warn := applyLanguage()
		if i18n.Current() != c.want || (warn != "") != c.warn || strings.Contains(warn, "\n") {
			t.Errorf("%s:語系 %s(要 %s)、提示 %q", c.name, i18n.Current(), c.want, warn)
		}
		if c.warn && (!strings.Contains(warn, `"klingon"`) || !strings.Contains(warn, "capy config set language")) {
			t.Errorf("%s:提示要講是哪個值、怎麼改:%q", c.name, warn)
		}
	}
}

// TestConfigInEnglish:搬進目錄的字在英文模式下整段是英文(T2 每搬一區就在這類測試加一組命令)。
func TestConfigInEnglish(t *testing.T) {
	setCLITestConfig(t)
	withLanguage(t, "en")
	_, err := runCLI(t, "config", "set", "theme", "dark")
	if err == nil || err.Error() != `unknown setting "theme" (settable: default_provider, local_root, language)` {
		t.Fatalf("英文的錯誤訊息:%v", err)
	}
	_, err = runCLI(t, "config", "set", "language", "fr")
	if err == nil || err.Error() != `language must be en or zh-TW, got "fr"` {
		t.Fatalf("英文的錯誤訊息:%v", err)
	}
	for _, args := range [][]string{{"config", "--help"}, {"config", "set", "--help"}, {"config", "list"}} {
		if out, err := runCLI(t, args...); err != nil || hasCJK(out) {
			t.Errorf("capy %s 在英文模式下不該有中文:%v %q", strings.Join(args, " "), err, out)
		}
	}
}
