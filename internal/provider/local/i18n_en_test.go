package local

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func withLanguage(t *testing.T, lang string) {
	t.Helper()
	prev := i18n.Current()
	if !i18n.Set(lang) {
		t.Fatalf("不支援 %s", lang)
	}
	t.Cleanup(func() { i18n.Set(prev) })
}

// 英文模式:路徑原樣、句子是英文;包起來的哨兵(ErrNotFound)errors.Is 照舊認得。
func TestLocalErrorsEnglish(t *testing.T) {
	withLanguage(t, "en")
	ctx := context.Background()
	p, root := world(t)
	if got := p.DisplayName(); got != "Local library" {
		t.Errorf("DisplayName = %q", got)
	}

	badRoot := t.TempDir() // p 的曲庫是惰性讀的:壞掉的 library.json 放別的目錄,不拖累下面的斷言
	lib := filepath.Join(badRoot, "library.json")
	for _, c := range []struct{ body, want string }{
		{`{"schema_version":2,"tracks":{}}`, lib + " has schema_version 2, newer than the 1 this version of capy understands; update capy"},
		{`{"schema_version":1,"tracks":{"a.mp3":{"title":"x"},"./a.mp3":{"title":"y"}}}`, lib + `: "./a.mp3" and "a.mp3" normalize to the same path "a.mp3"; keep only one`},
	} {
		write(t, badRoot, "library.json", c.body)
		if _, err := LoadLibrary(lib); err == nil || err.Error() != c.want {
			t.Errorf("got %v\nwant %q", err, c.want)
		}
	}

	notDir := filepath.Join(root, "notes.txt")
	if err := New(notDir, dev).Health(ctx); err == nil || err.Error() != "local_root "+notDir+" is not a directory" {
		t.Errorf("not a dir: %v", err)
	}

	for _, c := range []struct {
		err  error
		want string
	}{
		{errOf(p.GetTrack(ctx, "nope.mp3")), "nope.mp3 is in neither library.json nor local_root: not found"},
		{errOf(p.GetPlaylistItems(ctx, dev+"/nope.m3u8")), "playlist nope.m3u8 doesn't exist: not found"},
		{errOf(p.GetPlaylistItems(ctx, "01OTHERDEVICE0000000000000/x.m3u8")), "01OTHERDEVICE0000000000000/x.m3u8 belongs to another device: not found"},
		{errOf(p.ApplyOps(ctx, dev+"/library.json", nil, []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "a.mp3"}})), "playlist library.json is outside local_root or isn't a playlist file: not found"},
	} {
		if c.err == nil || c.err.Error() != c.want || !errors.Is(c.err, provider.ErrNotFound) {
			t.Errorf("got %v\nwant %q (and ErrNotFound)", c.err, c.want)
		}
	}
	add := []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "x\ny.mp3"}}
	if _, err := p.ApplyOps(ctx, dev+"/empty.m3u", nil, add); err == nil || err.Error() != `"x\ny.mp3" contains a line break, which M3U can't escape; nothing was written` {
		t.Errorf("newline: %v", err)
	}
}

func errOf[T any](_ T, err error) error { return err }
