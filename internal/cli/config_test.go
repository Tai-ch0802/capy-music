package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/config"
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
	if out, _ := runCLI(t, "config", "list"); out != "default_provider\tapple\nlocal_root\t\nspotify_client_id\tcid\napple_storefront\ttw\n" {
		t.Fatalf("list 非 TTY 應列出四個非機密欄位 key\\tvalue,得到 %q", out)
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
