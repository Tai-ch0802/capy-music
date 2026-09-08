package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	write(t, root, norm.NFD.String("café")+".m3u8", norm.NFD.String("café")+".mp3\n") // 目錄裡是 NFD(e + 結合重音),id 要是 NFC;裡面的路徑也是 NFD
	write(t, root, norm.NFD.String("café")+".mp3", "")
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
	wantIDs := []string{dev + "/café.m3u8", dev + "/empty.m3u", dev + "/通勤.m3u8"}
	if !slices.Equal(ids, wantIDs) || !slices.Equal(names, []string{"café", "empty", "通勤"}) {
		t.Fatalf("清單 id / 名稱(NFC、不遞迴、只認 m3u):%v %v", ids, names)
	}
	if refs[2].Total != 4 || refs[1].Total != 0 || refs[0].Total != 1 {
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
	want := []string{ // track id 不帶 device 前綴(只有 playlist id 帶):cid 才撐得過重灌 / 接管
		"a.mp3|Song A|TWA123400001|Artist One|200000",
		"b.flac|Song B||Artist Two,Feat|180000",
		"x.mp3|Extra Song|||123500",                  // 曲庫沒有:EXTINF 當標題與時長,仍是一首
		"sub/c.m4a|Song C|TWA123400002|Artist One|0", // M3U 裡的 sub\c.m4a:\ 當 Windows 分隔符翻
	}
	if !slices.Equal(got, want) {
		t.Fatalf("曲目:\n%s\n要\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if items, err := p.GetPlaylistItems(ctx, dev+"/empty.m3u"); err != nil || len(items) != 0 {
		t.Fatalf("空清單:%v %v", items, err)
	}
	// NFC 的 id 開 NFD 的檔(NTFS 分 NFC / NFD,APFS 不分):清單本身與裡面的路徑都要找得到
	if items, err := p.GetPlaylistItems(ctx, dev+"/café.m3u8"); err != nil || len(items) != 1 || items[0].ProviderID != "café.mp3" {
		t.Fatalf("NFD 檔名的清單以 NFC id 讀:%+v %v", items, err)
	}
	if tr, err := p.GetTrack(ctx, dev+"/café.mp3"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("track id 沒有 device 前綴,帶了就是找不到:%+v %v", tr, err)
	}
	if tr, err := p.GetTrack(ctx, "café.mp3"); err != nil || tr.Title != "café" {
		t.Fatalf("NFD 檔案以 NFC id 找:%+v %v", tr, err)
	}
	if norm.NFC.String(norm.NFD.String("café")) != "café" || norm.NFD.String("café") == "café" {
		t.Fatal("夾具要真的有分解形(咖啡沒有,café 有)")
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
	if !p.Caps().Has(provider.CapDeviceBound) || p.Caps().Has(provider.CapPlaybackControl) || p.Caps().Has(provider.CapPlaylistCreate) {
		t.Fatalf("能力:%b", p.Caps())
	}
}

func TestSearchISRCAndGetTrack(t *testing.T) {
	p, _ := world(t)
	ctx := context.Background()
	got, err := p.Search(ctx, provider.Query{Text: "artist one", Limit: 10})
	if err != nil || len(got) != 2 || got[0].ProviderID != "a.mp3" || got[1].ProviderID != "sub/c.m4a" {
		t.Fatalf("搜尋(每個 token 都要在標題 + 藝人 + 專輯裡):%+v %v", got, err)
	}
	if got, _ := p.Search(ctx, provider.Query{Text: "song ALPHA", Limit: 1}); len(got) != 1 || got[0].Title != "Song A" {
		t.Fatalf("不分大小寫、Limit:%+v", got)
	}
	if got, _ := p.Search(ctx, provider.Query{Text: "   "}); got != nil {
		t.Fatalf("空查詢回空:%+v", got)
	}
	if got, err := p.LookupISRC(ctx, "twa-1234-00002"); err != nil || len(got) != 1 || got[0].ProviderID != "sub/c.m4a" {
		t.Fatalf("ISRC 反查(正規化):%+v %v", got, err)
	}
	if _, err := p.LookupISRC(ctx, "bad"); !errors.Is(err, provider.ErrBadISRC) {
		t.Fatalf("壞 ISRC:%v", err)
	}
	if tr, err := p.GetTrack(ctx, "./b.flac"); err != nil || tr.Title != "Song B" || tr.ProviderID != "b.flac" { // 使用者打的也正規化
		t.Fatalf("GetTrack 曲庫:%+v %v", tr, err)
	}
	if tr, err := p.GetTrack(ctx, "notes.txt"); err != nil || tr.Title != "notes" { // 曲庫沒有但檔案在:檔名當標題
		t.Fatalf("GetTrack 檔案在:%+v %v", tr, err)
	}
	for _, id := range []string{"nope.mp3", "", ".", "..", "../x.mp3", "/etc/passwd"} {
		if _, err := p.GetTrack(ctx, id); !errors.Is(err, provider.ErrNotFound) {
			t.Fatalf("%q:都沒有 / 跳出 root 要 ErrNotFound:%v", id, err)
		}
	}
}

// Windows 匯出的 M3U 帶 C:\ 絕對路徑:不是相對路徑(不接目錄、不去 root 底下找——macOS 上 `C:` 是合法目錄名),但仍是清單裡的一首。
func TestWindowsAbsolutePathIsNotRelative(t *testing.T) {
	p, root := world(t)
	ctx := context.Background()
	write(t, root, "abs.m3u8", "C:\\music\\z.mp3\n\\\\server\\share\\y.mp3\n/abs/x.mp3\n")
	items, err := p.GetPlaylistItems(ctx, dev+"/abs.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ProviderID)
	}
	if !slices.Equal(ids, []string{"C:/music/z.mp3", "/server/share/y.mp3", "/abs/x.mp3"}) { // UNC 的 // 被 Clean 縮成 /:仍是絕對路徑,只是 id 字面
		t.Fatalf("絕對路徑原樣當 id:%v", ids)
	}
	if runtime.GOOS == "windows" {
		return // 建不出名為 C: 的目錄
	}
	write(t, root, "C:/music/z.mp3", "")
	if _, err := p.GetTrack(ctx, "C:/music/z.mp3"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("C:/ 是絕對路徑,不能被當成 root 底下的 C:/music:%v", err)
	}
}

// macOS 上 `\` 是合法檔名字元:目錄裡的 mix\pop.m3u8 不能被拆成 mix/pop;一個讀不了的清單檔只讓它自己 Total -1,別的清單照常。
func TestBackslashNameAndUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("Windows 沒有反斜線檔名;root 讀得了 0000 的檔")
	}
	p, root := world(t)
	ctx := context.Background()
	write(t, root, "mix\\pop.m3u8", "a.mp3\n")
	write(t, root, "locked.m3u8", "a.mp3\n")
	if err := os.Chmod(filepath.Join(root, "locked.m3u8"), 0); err != nil {
		t.Fatal(err)
	}
	refs, err := p.ListPlaylists(ctx)
	if err != nil {
		t.Fatalf("一個讀不了的檔不能拖垮全部:%v", err)
	}
	byID := map[string]int{}
	for _, r := range refs {
		byID[r.ID] = r.Total
	}
	if byID[dev+"/mix\\pop.m3u8"] != 1 || byID[dev+"/locked.m3u8"] != -1 || byID[dev+"/通勤.m3u8"] != 4 {
		t.Fatalf("反斜線檔名讀得到、讀不了的 Total -1、其他照常:%v", byID)
	}
	if items, err := p.GetPlaylistItems(ctx, dev+"/mix\\pop.m3u8"); err != nil || len(items) != 1 {
		t.Fatalf("反斜線檔名:%v %v", items, err)
	}
	if _, err := p.GetPlaylistItems(ctx, dev+"/locked.m3u8"); err == nil || errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("讀不了的檔用到時要說真正的錯(不是不存在):%v", err)
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
	root4 := t.TempDir() // 鍵正規化後撞在一起:報錯指出兩個鍵,不讓 map 順序決定誰勝出
	write(t, root4, "library.json", `{"schema_version":1,"tracks":{"a.mp3":{"title":"x"},"./a.mp3":{"title":"y"}}}`)
	if err := New(root4, dev).Health(context.Background()); err == nil || !strings.Contains(err.Error(), `"./a.mp3"`) || !strings.Contains(err.Error(), `"a.mp3"`) {
		t.Fatalf("撞鍵要報錯並指出兩個鍵:%v", err)
	}
	table := map[string]string{"./a.mp3": "a.mp3", " x/./y//z.mp3 ": "x/y/z.mp3", norm.NFD.String("café.mp3"): "café.mp3", "": ""}
	if runtime.GOOS == "windows" {
		table["sub\\c.m4a"] = "sub/c.m4a" // Windows 的 \ 是分隔符
	} else {
		table["mix\\pop.m3u8"] = "mix\\pop.m3u8" // macOS 的 \ 是檔名的一部分
	}
	for in, want := range table {
		if got := NormalizePath(in); got != want {
			t.Fatalf("NormalizePath(%q) = %q,要 %q", in, got, want)
		}
	}
}

func TestApplyOpsAndPushable(t *testing.T) {
	p, root := world(t)
	ctx := context.Background()
	id := dev + "/通勤.m3u8"
	if !p.Pushable("a.mp3") || !p.Pushable("notes.txt") || p.Pushable("nope.mp3") || p.Pushable("sub") || p.Pushable("a\n.mp3") || p.Pushable("") {
		t.Fatal("Pushable:曲庫有或檔案在(目錄不算、含換行不算);track id 不帶 device,沒有 Foreign 判斷")
	}
	if !p.Caps().Has(provider.CapPlaylistAppend|provider.CapPlaylistRemove|provider.CapPlaylistReorder) || p.Caps().Has(provider.CapPlaylistRename) {
		t.Fatalf("寫入能力(沒有 Rename):%b", p.Caps())
	}
	current := []string{"a.mp3", "b.flac", "x.mp3", "sub/c.m4a"}
	ops := []provider.PlaylistOp{
		{Kind: provider.OpRemove, Pos: 2, ProviderID: "x.mp3"},
		{Kind: provider.OpMove, From: 2, Pos: 0},
		{Kind: provider.OpAdd, Pos: 1, ProviderID: "notes.txt"},
		{Kind: provider.OpRename, Name: "新名"},
	}
	skipped, err := p.ApplyOps(ctx, id, current, ops)
	if err != nil || len(skipped) != 1 || skipped[0].Kind != provider.OpRename {
		t.Fatalf("rename 回 skipped、其餘做:%v %+v", err, skipped)
	}
	got, err := os.ReadFile(filepath.Join(root, "通勤.m3u8"))
	if err != nil || string(got) != "#EXTM3U\nsub/c.m4a\nnotes.txt\na.mp3\nb.flac\n" {
		t.Fatalf("整檔重寫(EXTINF 與註解丟掉):%q %v", got, err)
	}
	if ents, _ := os.ReadDir(root); slices.ContainsFunc(ents, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), ".capy-") }) {
		t.Fatal("不留暫存檔")
	}
	items, _ := p.GetPlaylistItems(ctx, id)
	if len(items) != 4 || items[0].ProviderID != "sub/c.m4a" || items[1].Title != "notes" {
		t.Fatalf("重讀:%+v", items)
	}
	// 只有 rename:不動檔案
	before, _ := os.ReadFile(filepath.Join(root, "通勤.m3u8"))
	if sk, err := p.ApplyOps(ctx, id, idsOf(items), []provider.PlaylistOp{{Kind: provider.OpRename, Name: "x"}}); err != nil || len(sk) != 1 {
		t.Fatalf("只有 rename:%v %+v", err, sk)
	}
	if after, _ := os.ReadFile(filepath.Join(root, "通勤.m3u8")); string(after) != string(before) {
		t.Fatal("只有 rename 不寫檔")
	}
	// op 位置越界 → 錯且不寫(路徑越界在 TestApplyOpsGuardsAndFidelity);foreign 清單 / 不存在的清單
	if _, err := p.ApplyOps(ctx, id, idsOf(items), []provider.PlaylistOp{{Kind: provider.OpRemove, Pos: 99}}); err == nil {
		t.Fatal("越界要錯")
	}
	if after, _ := os.ReadFile(filepath.Join(root, "通勤.m3u8")); string(after) != string(before) {
		t.Fatal("錯誤路徑零寫入")
	}
	if _, err := p.ApplyOps(ctx, "01OTHERDEVICE0000000000000/通勤.m3u8", nil, []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "a.mp3"}}); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("foreign 清單:%v", err)
	}
	if _, err := p.ApplyOps(ctx, dev+"/nope.m3u8", nil, []provider.PlaylistOp{{Kind: provider.OpAdd, ProviderID: "a.mp3"}}); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("不存在的清單:%v", err)
	}
}

// PR #38 review:ApplyOps 是唯一會覆寫使用者磁碟檔案的路徑——路徑越界 / 非清單檔零寫入;保留權限;symlink 寫穿;
// 寫回用磁碟上的拼法(NFD 就 NFD);# 開頭補 ./ 讓下一輪不被讀成註解;含換行拒寫。
func TestApplyOpsGuardsAndFidelity(t *testing.T) {
	p, root := world(t)
	ctx := context.Background()
	add := func(id string) []provider.PlaylistOp {
		return []provider.PlaylistOp{{Kind: provider.OpAdd, Pos: 0, ProviderID: id}}
	}
	// 路徑越界:root 之外的檔一個位元組都不能動;library.json 不是清單檔
	outside := filepath.Join(filepath.Dir(root), "victim-"+filepath.Base(root)+".txt")
	if err := os.WriteFile(outside, []byte("important"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)
	for _, id := range []string{dev + "/../" + filepath.Base(outside), dev + "/library.json", dev + "/sub/../../x.m3u8"} {
		if _, err := p.ApplyOps(ctx, id, nil, add("a.mp3")); !errors.Is(err, provider.ErrNotFound) {
			t.Fatalf("%s:要 ErrNotFound 零寫入:%v", id, err)
		}
	}
	if got, _ := os.ReadFile(outside); string(got) != "important" {
		t.Fatalf("root 外的檔被覆寫了:%q", got)
	}
	if err := p.Health(ctx); err != nil {
		t.Fatalf("library.json 不能被寫成 M3U:%v", err)
	}
	// 寫回磁碟上的拼法:café.mp3 在磁碟上是 NFD,id 是 NFC,寫回去要是 NFD;# 開頭補 ./
	write(t, root, "#b.mp3", "")
	if _, err := p.ApplyOps(ctx, dev+"/café.m3u8", []string{"café.mp3"}, []provider.PlaylistOp{{Kind: provider.OpAdd, Pos: 1, ProviderID: "#b.mp3"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, norm.NFD.String("café")+".m3u8"))
	if string(got) != "#EXTM3U\n"+norm.NFD.String("café")+".mp3\n./#b.mp3\n" {
		t.Fatalf("寫回的拼法要跟磁碟一樣、# 開頭補 ./:%q", got)
	}
	items, err := p.GetPlaylistItems(ctx, dev+"/café.m3u8")
	if err != nil || len(items) != 2 || items[0].ProviderID != "café.mp3" || items[1].ProviderID != "#b.mp3" {
		t.Fatalf("round-trip:%+v %v", items, err)
	}
	// 含換行的檔名:整批不寫
	before, _ := os.ReadFile(filepath.Join(root, "通勤.m3u8"))
	if _, err := p.ApplyOps(ctx, dev+"/通勤.m3u8", []string{"a.mp3"}, add("x\ny.mp3")); err == nil || !strings.Contains(err.Error(), "換行") {
		t.Fatalf("含換行要拒寫:%v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(root, "通勤.m3u8")); string(after) != string(before) {
		t.Fatal("拒寫時零寫入")
	}
	if runtime.GOOS == "windows" {
		return // 權限位元與 symlink 在 Windows 上另一回事
	}
	// 權限保留:0644 push 之後還是 0644(CreateTemp 是 0600)
	if err := os.Chmod(filepath.Join(root, "通勤.m3u8"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ApplyOps(ctx, dev+"/通勤.m3u8", []string{"a.mp3"}, add("b.flac")); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(root, "通勤.m3u8")); st.Mode().Perm() != 0o644 {
		t.Fatalf("權限要保留:%v", st.Mode())
	}
	// symlink:寫它指到的檔,symlink 本身還在
	real := filepath.Join(t.TempDir(), "real.m3u8")
	if err := os.WriteFile(real, []byte("a.mp3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "linked.m3u8")); err != nil {
		t.Skip(err)
	}
	if _, err := p.ApplyOps(ctx, dev+"/linked.m3u8", []string{"a.mp3"}, add("b.flac")); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(filepath.Join(root, "linked.m3u8")); st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink 不能被換成一般檔")
	}
	if got, _ := os.ReadFile(real); string(got) != "#EXTM3U\nb.flac\na.mp3\n" {
		t.Fatalf("要寫穿到指到的檔:%q", got)
	}
}

func idsOf(ts []provider.Track) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ProviderID
	}
	return out
}
