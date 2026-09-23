package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// 英文模式下 provider 層(local、spotify、drive、backoff;T2d)自己的字,經 CLI 出來的樣子。
// 只斷言那些套件產生的片段:外層的 doctor / platform 句子屬於別組。

func TestEnglishLocalLibraryThroughDoctor(t *testing.T) {
	_, _, root := localWorld(t)
	withLanguage(t, "en")
	writeFile(t, root, "library.json", `{"schema_version":2,"tracks":{}}`)
	out, _, err := runPull(t, "doctor", "--provider", "local")
	if want := ": " + filepath.Join(root, "library.json") + " has schema_version 2, newer than the 1 this version of capy understands; update capy\n"; err == nil || !strings.Contains(out, want) {
		t.Fatalf("(%v)\n%s\nwant a line ending %q", err, out, want)
	}
}

func TestEnglishLocalDisplayName(t *testing.T) {
	localWorld(t)
	withLanguage(t, "en")
	_, _, err := runPull(t, "pause", "--provider", "local")
	if err == nil || !strings.HasPrefix(err.Error(), "Local library ") {
		t.Fatalf("平台名要是英文的 Local library:%v", err)
	}
}
