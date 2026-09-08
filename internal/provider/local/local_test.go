package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const dev = "01TESTDEVICE00000000000000"

func write(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// world:曲庫三首(a 有 ISRC、b 沒有、c 有);清單 通勤 = [a, b, 曲庫沒有的 x(帶 EXTINF), 子目錄的 c];空清單;NFD 檔名的清單。
func world(t *testing.T) (*Provider, string) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "library.json", `{"schema_version":1,"tracks":{
	  "./a.mp3":{"title":"Song A","artists":["Artist One"],"album":"Alpha","duration_ms":200000,"isrc":"tw-a12-34-00001"},
	  "b.flac":{"title":"Song B","artists":["Artist Two","Feat"],"duration_ms":180000},
	  "sub/c.m4a":{"title":"Song C","artists":["Artist One"],"isrc":"TWA123400002"}}}`)
	write(t, root, "通勤.m3u8", "#EXTM3U\r\n#EXTINF:200,Song A\r\na.mp3\r\n\r\n# comment\r\nb.flac\r\n#EXTINF:123.5,Extra Song\r\nx.mp3\r\nsub\\c.m4a\r\n")
	write(t, root, "empty.m3u", "#EXTM3U\n")
	write(t, root, norm.NFD.String("咖啡")+".m3u8", "a.mp3\n")
	write(t, root, "notes.txt", "not a playlist")
	write(t, root, "sub/nested.m3u8", "c.m4a\n") // 不遞迴(Q25)
	return New(root, dev), root
}

func TestListAndItems(t *testing.T) {
	p, _ := world(t)
	ctx := context.Background()
	if err := p.Health(ctx); err != nil {
		t.Fatal(err)
	}
	refs, err := p.ListPlaylists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids, names []string
	for _, r := range refs {
		ids, names = append(ids, r.ID), append(names, r.Name)
	}
	wantIDs := []string{dev + "/empty.m3u", dev + "/咖啡.m3u8", dev + "/通勤.m3u8"}
	if !slices.Equal(ids, wantIDs) || !slices.Equal(names, []string{"empty", "咖啡", "通勤"}) {
		t.Fatalf("清單 id / 名稱(NFC、不遞迴、只認 m3u):%v %v", ids, names)
	}
	if refs[2].Total != 4 || refs[0].Total != 0 {
		t.Fatalf("Total 是曲目行數:%+v", refs)
	}
	tracks, err := p.GetPlaylistItems(ctx, dev+"/通勤.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tr := range tracks {
		got = append(got, tr.ProviderID+"|"+tr.Title+"|"+tr.ISRC+"|"+strings.Join(tr.Artists, ",")+"|"+strconv.Itoa(tr.DurationMS))
	}
	want := []string{
		dev + "/a.mp3|Song A|TWA123400001|Artist One|200000",
		dev + "/b.flac|Song B||Artist Two,Feat|180000",
		dev + "/x.mp3|Extra Song|||123500", // 曲庫沒有:EXTINF 當標題與時長,仍是一首
		dev + "/sub/c.m4a|Song C|TWA123400002|Artist One|0",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("曲目:\n%s\n要\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if items, err := p.GetPlaylistItems(ctx, dev+"/empty.m3u"); err != nil || len(items) != 0 {
		t.Fatalf("空清單:%v %v", items, err)
	}
	if _, err := p.GetPlaylistItems(ctx, dev+"/nope.m3u8"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("不存在的清單要 ErrNotFound:%v", err)
	}
}

func TestForeignAndDevicePrefix(t *testing.T) {
	p, _ := world(t)
	ctx := context.Background()
	other := "01OTHERDEVICE0000000000000/通勤.m3u8"
	if !p.Foreign(other) || p.Foreign(dev+"/通勤.m3u8") || !p.Foreign("") || !p.Foreign("通勤.m3u8") {
		t.Fatal("Foreign 只認本裝置前綴")
	}
	if _, err := p.GetPlaylistItems(ctx, other); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("foreign id 讀 items 要 ErrNotFound:%v", err)
	}
	if !p.Caps().Has(provider.CapDeviceBound) || p.Caps().Has(provider.CapPlaylistAppend) || p.Caps().Has(provider.CapPlaybackControl) {
		t.Fatalf("能力:%b", p.Caps())
	}
	if _, ok := any(p).(provider.PlaylistWriter); ok {
		t.Fatal("T1 還沒有寫入端")
	}
}

func TestSearchISRCAndGetTrack(t *testing.T) {
	p, _ := world(t)
	ctx := context.Background()
	got, err := p.Search(ctx, provider.Query{Text: "artist one", Limit: 10})
	if err != nil || len(got) != 2 || got[0].ProviderID != dev+"/a.mp3" || got[1].ProviderID != dev+"/sub/c.m4a" {
		t.Fatalf("搜尋(每個 token 都要在標題 + 藝人 + 專輯裡):%+v %v", got, err)
	}
	if got, _ := p.Search(ctx, provider.Query{Text: "song ALPHA", Limit: 1}); len(got) != 1 || got[0].Title != "Song A" {
		t.Fatalf("不分大小寫、Limit:%+v", got)
	}
	if got, _ := p.Search(ctx, provider.Query{Text: "   "}); got != nil {
		t.Fatalf("空查詢回空:%+v", got)
	}
	if got, err := p.LookupISRC(ctx, "twa-1234-00002"); err != nil || len(got) != 1 || got[0].ProviderID != dev+"/sub/c.m4a" {
		t.Fatalf("ISRC 反查(正規化):%+v %v", got, err)
	}
	if _, err := p.LookupISRC(ctx, "bad"); !errors.Is(err, provider.ErrBadISRC) {
		t.Fatalf("壞 ISRC:%v", err)
	}
	if tr, err := p.GetTrack(ctx, dev+"/b.flac"); err != nil || tr.Title != "Song B" {
		t.Fatalf("GetTrack 曲庫:%+v %v", tr, err)
	}
	if tr, err := p.GetTrack(ctx, dev+"/notes.txt"); err != nil || tr.Title != "notes" { // 曲庫沒有但檔案在:檔名當標題
		t.Fatalf("GetTrack 檔案在:%+v %v", tr, err)
	}
	if _, err := p.GetTrack(ctx, dev+"/nope.mp3"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("都沒有:%v", err)
	}
}

func TestLibraryErrorsAndNormalize(t *testing.T) {
	root := t.TempDir()
	write(t, root, "library.json", `{"schema_version":1,"tracks":`)
	if err := New(root, dev).Health(context.Background()); err == nil || !strings.Contains(err.Error(), "library.json") {
		t.Fatalf("壞 JSON 要指向檔案:%v", err)
	}
	root2 := t.TempDir()
	write(t, root2, "library.json", `{"schema_version":2,"tracks":{}}`)
	if err := New(root2, dev).Health(context.Background()); err == nil || !strings.Contains(err.Error(), "schema_version 2") {
		t.Fatalf("schema 太新要拒讀:%v", err)
	}
	if err := New(filepath.Join(root2, "nope"), dev).Health(context.Background()); err == nil {
		t.Fatal("root 不存在要錯")
	}
	root3 := t.TempDir()
	if err := New(root3, dev).Health(context.Background()); err != nil {
		t.Fatalf("沒有 library.json 不算錯:%v", err)
	}
	for in, want := range map[string]string{"./a.mp3": "a.mp3", "sub\\c.m4a": "sub/c.m4a", " x/./y//z.mp3 ": "x/y/z.mp3", norm.NFD.String("咖啡.mp3"): "咖啡.mp3", "": ""} {
		if got := NormalizePath(in); got != want {
			t.Fatalf("NormalizePath(%q) = %q,要 %q", in, got, want)
		}
	}
}
