package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

// Load 在 applyLanguage 設語系之前就跑:壞掉的 config.json 的錯要在印出時才翻,
// 所以 zh-TW 時建的錯、換成 en 之後印出來是英文。
func TestParseErrorIsTranslatedWhenPrinted(t *testing.T) {
	prev := i18n.Current()
	i18n.Set("zh-TW")
	t.Cleanup(func() { i18n.Set(prev) })
	p := filepath.Join(setTestDir(t), "capy-music", "config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	broken := []byte("{not json")
	if err := os.WriteFile(p, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	cause := json.Unmarshal(broken, &Config{})
	if err == nil || err.Error() != "解析 "+p+":"+cause.Error() {
		t.Fatalf("zh-TW:%v", err)
	}
	i18n.Set("en")
	if err.Error() != "parsing "+p+": "+cause.Error() {
		t.Errorf("en:%v", err)
	}
	var se *json.SyntaxError
	if !errors.As(err, &se) {
		t.Errorf("要包著 json 的錯:%v", err)
	}
}
