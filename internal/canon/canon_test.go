package canon_test

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// pin 把時鐘與 ULID 釘死(每呼叫一次重新從頭開始),每次 Now() 前進一秒。
func pin(t *testing.T) {
	t.Helper()
	origNow, origULID := canon.Now, canon.NewULID
	clock, seq := time.Unix(1_756_600_000, 0), 0
	canon.Now = func() time.Time { clock = clock.Add(time.Second); return clock }
	canon.NewULID = func() string { seq++; return fmt.Sprintf("01TESTULID%016d", seq) }
	t.Cleanup(func() { canon.Now, canon.NewULID = origNow, origULID })
}

func TestNormalizeISRC(t *testing.T) {
	for in, want := range map[string]string{
		"TW-A47-24-00123": "TWA472400123", "twa472400123": "TWA472400123", " TWA472400123 ": "TWA472400123",
		"TWA47240012": "", "TWA4724001234": "", "": "", "TW-A47-24-0012!": "",
	} {
		if got := canon.NormalizeISRC(in); got != want {
			t.Errorf("NormalizeISRC(%q) = %q,要 %q", in, got, want)
		}
	}
}

func TestCIDDeterministicAcrossDevices(t *testing.T) {
	x := provider.Track{ProviderID: "6rq", ISRC: "tw-a47-24-00123", Title: "派對動物", Artists: []string{"五月天"}, DurationMS: 227000}
	live := provider.Track{ProviderID: "i.abc", ISRC: "TWA472400123", Title: "派對動物 (Live)", DurationMS: 252000}

	a := canon.NewTrack("spotify", x) // 裝置 A 只看到一首
	b := canon.NewTrack("spotify", x) // 裝置 B 還看到衝突的一首
	if added := b.Observe("apple", live); !added {
		t.Fatal("時長差 25s 應記衝突")
	}
	if a.CID != "i:TWA472400123" || b.CID != a.CID {
		t.Fatalf("兩台裝置的 cid 必須相同且不因衝突退回 p::%s / %s", a.CID, b.CID)
	}
	if len(b.Conflicts) != 1 || b.Conflicts[0].ProviderID != "i.abc" || b.Mappings["apple"] != "i.abc" {
		t.Fatalf("衝突要記事實、mapping 照加:%+v", b)
	}
	if b.Observe("apple", live) || len(b.Conflicts) != 1 {
		t.Fatal("同一筆衝突再觀測一次不可重複記錄(每輪 pull 都會再看到)")
	}
	if b.Observe("spotify", provider.Track{ProviderID: "album-ver", ISRC: "TWA472400123", Title: " 派對動物", DurationMS: 229000}) {
		t.Fatal("同 provider 另一個版本、metadata 相符不算衝突")
	}
	if b.Mappings["spotify"] != "6rq" || len(b.Conflicts) != 1 {
		t.Fatalf("mapping 要留第一個,不可抖動:%+v", b.Mappings)
	}
	if !reflect.DeepEqual(a.ISRC, []string{"TWA472400123"}) || a.Artists == nil {
		t.Fatalf("ISRC 正規化進 alias set、artists 不可為 nil:%+v", a)
	}

	up := canon.NewTrack("apple", provider.Track{ProviderID: "i.lib1", Title: "上傳曲"})
	if up.CID != "p:apple:i.lib1" || up.ISRC != nil || up.Mappings["apple"] != "i.lib1" {
		t.Fatalf("沒有 ISRC 要保留成 provider-only:%+v", up)
	}
	if canon.CID("spotify", "x", "TWA47240012") != "p:spotify:x" {
		t.Fatal("非 12 碼 ISRC 視為缺失")
	}
}

func TestDecodeSchemaVersion(t *testing.T) {
	if _, err := canon.Decode[canon.Manifest]([]byte(`{"schema_version":2,"devices":[]}`)); !errors.Is(err, canon.ErrSchemaTooNew) {
		t.Fatalf("高於支援版本應拒絕:%v", err)
	}
	if _, err := canon.Decode[canon.Manifest]([]byte(`{"devices":[]}`)); err == nil {
		t.Fatal("缺 schema_version 應報錯")
	}
	m, err := canon.Decode[canon.Manifest]([]byte(`{"schema_version":1,"devices":[{"id":"d","name":"n","last_seen":1}],"future_field":{"x":1}}`))
	if err != nil || len(m.Devices) != 1 {
		t.Fatalf("未知欄位要忽略:%v %+v", err, m)
	}
}

func TestMergeBaseLWWWithTiebreak(t *testing.T) {
	snap := func(name string) canon.Snapshot { return canon.Snapshot{Name: name, Items: []string{"t1"}} }
	a := canon.DeviceState{DeviceID: "A", Base: map[string]map[string]canon.Base{
		"p1": {"spotify": {Snapshot: snap("a-old"), ObservedAt: 100}},
		"p2": {"spotify": {Snapshot: snap("a-tie"), ObservedAt: 300}},
		"p3": {"spotify": {Snapshot: snap("a-only"), ObservedAt: 50}},
	}}
	b := canon.DeviceState{DeviceID: "B", Base: map[string]map[string]canon.Base{
		"p1": {"spotify": {Snapshot: snap("b-new"), ObservedAt: 200}},
		"p2": {"spotify": {Snapshot: snap("b-tie"), ObservedAt: 300}},
	}}
	got := canon.MergeBase([]canon.DeviceState{a, b})
	if got["p1"]["spotify"].Snapshot.Name != "b-new" {
		t.Fatalf("observed_at 大者勝:%+v", got["p1"])
	}
	if got["p2"]["spotify"].Snapshot.Name != "b-tie" {
		t.Fatalf("同秒取 device_id 較大者:%+v", got["p2"])
	}
	if got["p3"]["spotify"].Snapshot.Name != "a-only" {
		t.Fatalf("只有一台有的要原樣通過:%+v", got["p3"])
	}
	if !reflect.DeepEqual(got, canon.MergeBase([]canon.DeviceState{b, a})) {
		t.Fatal("合併結果不可隨輸入順序改變")
	}
}

func TestRankBetween(t *testing.T) {
	for _, c := range []struct{ a, b, want string }{{"", "", "V"}, {"a0", "a1", "a0V"}, {"V", "", "k"}, {"", "V", "F"}, {"z", "", "zV"}} {
		if got, err := canon.RankBetween(c.a, c.b); err != nil || got != c.want {
			t.Errorf("RankBetween(%q,%q) = %q %v,要 %q", c.a, c.b, got, err, c.want)
		}
	}
	for _, c := range [][2]string{{"b", "a"}, {"a", "a"}, {"a", "a0"}, {"a", "a00"}, {"", "0"}, {"a-b", "a-c"}, {"a", "b-"}, {"é", ""}} {
		if _, err := canon.RankBetween(c[0], c[1]); err == nil {
			t.Errorf("RankBetween(%q,%q) 應報錯(壞資料要看得見,不能 panic 或回出界的值)", c[0], c[1])
		}
	}
	if got, err := canon.RankBetween("a0", ""); err != nil || got <= "a0" {
		t.Errorf("尾端 0 是合法的 spec 鍵,之後仍要能插:%q %v", got, err)
	}
	// 性質:隨機在鄰居之間插入 500 次,序列永遠嚴格遞增、沒有 rank 以 '0' 結尾
	rng := rand.New(rand.NewSource(1))
	seq := []string{}
	for range 500 {
		i := rng.Intn(len(seq) + 1)
		a, b := "", ""
		if i > 0 {
			a = seq[i-1]
		}
		if i < len(seq) {
			b = seq[i]
		}
		r, err := canon.RankBetween(a, b)
		if err != nil {
			t.Fatal(err)
		}
		seq = append(seq[:i], append([]string{r}, seq[i:]...)...)
	}
	checkRanks(t, seq)
	for _, n := range []int{1, 5, 61, 62, 200, 4000} {
		rs := canon.Ranks(n)
		if len(rs) != n {
			t.Fatalf("Ranks(%d) 回 %d 個", n, len(rs))
		}
		checkRanks(t, rs)
		if n > 1 {
			if _, err := canon.RankBetween(rs[0], rs[1]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if rs := canon.Ranks(1); rs[0] != "V" {
		t.Fatalf("單一 rank 應與 RankBetween(\"\",\"\") 一致:%q", rs[0])
	}
}

func checkRanks(t *testing.T, seq []string) {
	t.Helper()
	if !sort.StringsAreSorted(seq) {
		t.Fatal("rank 序列應已排序")
	}
	for i, r := range seq {
		if strings.HasSuffix(r, "0") || r == "" {
			t.Fatalf("rank 不可為空或以 0 結尾:%q", r)
		}
		if i > 0 && seq[i-1] >= r {
			t.Fatalf("rank 要嚴格遞增:%q >= %q", seq[i-1], r)
		}
	}
}

// build 用釘死的時鐘與 ULID 建一整套 Drive 檔,回 (manifest, tracks, playlist, device) 的位元組。
func build(t *testing.T) [][]byte {
	t.Helper()
	pin(t)
	m := canon.NewManifest()
	if !m.Touch("dev1", "mac") || m.Touch("dev1", "mac-renamed") {
		t.Fatal("Touch:第一次新裝置、第二次更新")
	}
	tr := canon.NewTracks()
	x := canon.NewTrack("spotify", provider.Track{ProviderID: "6rq", ISRC: "TWA472400123", Title: "派對動物", Artists: []string{"五月天"}, Album: "自傳", DurationMS: 227000})
	x.Observe("apple", provider.Track{ProviderID: "i.abc", ISRC: "TWA472400123", Title: "派對動物 (Live)", DurationMS: 252000})
	tr.Tracks[x.CID] = x
	up := canon.NewTrack("apple", provider.Track{ProviderID: "i.lib1", Title: "上傳曲"})
	tr.Tracks[up.CID] = up
	pl := canon.NewPlaylist("通勤")
	pl.Description = "上班聽"
	pl.Links["spotify"] = "37i9"
	for _, cid := range []string{x.CID, up.CID, x.CID} { // 同曲第二次出現合法(決策 13)
		if _, err := pl.Append(cid); err != nil {
			t.Fatal(err)
		}
	}
	dev := canon.NewDeviceState("dev1")
	dev.SetBase(pl.PID, "spotify", canon.Snapshot{Name: "通勤", Items: []string{"6rq", "other", "6rq"}, CIDs: []string{x.CID, "p:spotify:other", x.CID}})
	dev.SetBase(pl.PID, "apple", canon.Snapshot{Name: "通勤"})
	var out [][]byte
	for _, v := range []any{m, tr, pl, dev} {
		b, err := canon.Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func TestRoundTripBitEqual(t *testing.T) {
	first := build(t)
	again := build(t)
	for i := range first {
		if !bytes.Equal(first[i], again[i]) {
			t.Fatalf("同樣的建構要得到同樣的位元組(第 %d 個檔):\n%s\n%s", i, first[i], again[i])
		}
	}
	decodeEncode := []func([]byte) ([]byte, error){
		func(b []byte) ([]byte, error) { v, err := canon.Decode[canon.Manifest](b); return enc(v, err) },
		func(b []byte) ([]byte, error) { v, err := canon.Decode[canon.Tracks](b); return enc(v, err) },
		func(b []byte) ([]byte, error) { v, err := canon.Decode[canon.Playlist](b); return enc(v, err) },
		func(b []byte) ([]byte, error) { v, err := canon.Decode[canon.DeviceState](b); return enc(v, err) },
	}
	for i, f := range decodeEncode {
		got, err := f(first[i])
		if err != nil || !bytes.Equal(got, first[i]) {
			t.Fatalf("Encode(Decode(b)) 要逐位元相等(第 %d 個檔):%v\n%s\n%s", i, err, first[i], got)
		}
	}
	m, tr, pl, dev := string(first[0]), string(first[1]), string(first[2]), string(first[3])
	for _, want := range []string{`{"schema_version":1,"devices":[{"id":"dev1","name":"mac-renamed","last_seen":1756600002}]}`} {
		if !strings.Contains(m, want) {
			t.Fatalf("manifest 形狀:%s", m)
		}
	}
	if !strings.Contains(tr, `"i:TWA472400123":{"cid":"i:TWA472400123","isrc":["TWA472400123"],"title":"派對動物","artists":["五月天"],"album":"自傳","duration_ms":227000,"mappings":{"apple":"i.abc","spotify":"6rq"},"conflicts":[{"provider":"apple","provider_id":"i.abc","title":"派對動物 (Live)","duration_ms":252000}]}`) ||
		!strings.Contains(tr, `"p:apple:i.lib1":{"cid":"p:apple:i.lib1","title":"上傳曲","artists":[],"duration_ms":0,"mappings":{"apple":"i.lib1"}}`) {
		t.Fatalf("tracks 形狀(spec §6.2):%s", tr)
	}
	if !strings.HasPrefix(pl, `{"schema_version":1,"pid":"01TESTULID0000000000000001","name":"通勤","description":"上班聽","updated_at":`) ||
		!strings.Contains(pl, `"items":[{"iid":"01TESTULID0000000000000002","cid":"i:TWA472400123","rank":"V","added_at":`) ||
		!strings.Contains(pl, `"rank":"k"`) || !strings.Contains(pl, `"rank":"s"`) || !strings.HasSuffix(pl, `"links":{"spotify":"37i9"}}`+"\n") {
		t.Fatalf("playlist 形狀:%s", pl)
	}
	if !strings.Contains(dev, `"base":{"01TESTULID0000000000000001":{"apple":{"snapshot":{"name":"通勤","items":[],"cids":[]},"observed_at":`) ||
		!strings.Contains(dev, `"spotify":{"snapshot":{"name":"通勤","items":["6rq","other","6rq"],"cids":["i:TWA472400123","p:spotify:other","i:TWA472400123"]},"observed_at":`) {
		t.Fatalf("device 形狀:%s", dev)
	}
	b, _ := canon.Encode(canon.NewPlaylist("空"))
	if !strings.Contains(string(b), `"items":[]`) || !strings.Contains(string(b), `"links":{}`) {
		t.Fatalf("空清單要寫 items: [] 與 links: {} 而非省略/null:%s", b)
	}
	empty, err := canon.Decode[canon.Playlist](b)
	if err != nil || empty.Links == nil || empty.Items == nil {
		t.Fatalf("空清單解回來 Links / Items 不可為 nil(T7 會直接賦值):%v %+v", err, empty)
	}
	empty.Links["spotify"] = "x" // 不可 panic
}

func enc(v any, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return canon.Encode(v)
}

func TestDecodeFillsMissingContainers(t *testing.T) {
	tr, err := canon.Decode[canon.Tracks]([]byte(`{"schema_version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	tr.Tracks["c"] = canon.Track{} // 不可 panic
	dev, err := canon.Decode[canon.DeviceState]([]byte(`{"schema_version":1,"device_id":"d","base":null}`))
	if err != nil {
		t.Fatal(err)
	}
	dev.SetBase("p", "spotify", canon.Snapshot{})
	pl, err := canon.Decode[canon.Playlist]([]byte(`{"schema_version":1,"pid":"p","name":"n","updated_at":1}`))
	if err != nil || pl.Items == nil {
		t.Fatalf("缺 items 要補成空 slice:%v %+v", err, pl)
	}
	pl.Links["spotify"] = "x"
	m, err := canon.Decode[canon.Manifest]([]byte(`{"schema_version":1}`))
	if err != nil || m.Devices == nil || !m.Touch("d", "n") {
		t.Fatalf("缺 devices 要補成空 slice:%v %+v", err, m)
	}
	var zero canon.DeviceState
	zero.SetBase("p", "apple", canon.Snapshot{}) // 零值也不可 panic
	if b, _ := canon.Encode(&canon.Playlist{SchemaVersion: 1}); !strings.Contains(string(b), `"items":[]`) || !strings.Contains(string(b), `"links":{}`) {
		t.Fatalf("Encode 也要補齊容器:%s", b)
	}
}

func TestManifestDeviceOrderIsDeterministic(t *testing.T) {
	orig := canon.Now
	canon.Now = func() time.Time { return time.Unix(1_756_600_000, 0) }
	t.Cleanup(func() { canon.Now = orig })
	m1, m2 := canon.NewManifest(), canon.NewManifest()
	m1.Touch("A", "a")
	m1.Touch("B", "b")
	m2.Touch("B", "b")
	m2.Touch("A", "a")
	b1, _ := canon.Encode(m1)
	b2, _ := canon.Encode(m2)
	if !bytes.Equal(b1, b2) {
		t.Fatalf("manifest 是共用檔,註冊順序不同不能得到不同位元組:\n%s%s", b1, b2)
	}
	if !strings.Contains(string(b1), `"devices":[{"id":"A"`) {
		t.Fatalf("devices 應依 ID 排序:%s", b1)
	}
}

func TestMergeBaseDoesNotAliasInput(t *testing.T) {
	d := canon.DeviceState{DeviceID: "A", Base: map[string]map[string]canon.Base{"p1": {"spotify": {Snapshot: canon.Snapshot{Items: []string{"t1"}, CIDs: []string{"c1"}}, ObservedAt: 1}}}}
	merged := canon.MergeBase([]canon.DeviceState{d})
	merged["p1"]["spotify"].Snapshot.Items[0] = "MUTATED"
	merged["p1"]["spotify"].Snapshot.CIDs[0] = "MUTATED"
	if s := d.Base["p1"]["spotify"].Snapshot; s.Items[0] != "t1" || s.CIDs[0] != "c1" {
		t.Fatal("合併結果不可與輸入共用底層陣列")
	}
}

func TestPlaylistItemsSortedByRankOnEncode(t *testing.T) {
	pl := &canon.Playlist{SchemaVersion: 1, PID: "p", Items: []canon.Item{{IID: "i2", CID: "c", Rank: "k"}, {IID: "i1", CID: "c", Rank: "V"}, {IID: "i0", CID: "c", Rank: "k"}}}
	b, err := canon.Encode(pl)
	if err != nil {
		t.Fatal(err)
	}
	if i, j, k := strings.Index(string(b), `"iid":"i1"`), strings.Index(string(b), `"iid":"i0"`), strings.Index(string(b), `"iid":"i2"`); !(i < j && j < k) {
		t.Fatalf("items 應依 (rank, iid) 排序:%s", b)
	}
	tr := canon.NewTracks()
	tr.Tracks["p:x:1"] = canon.Track{CID: "p:x:1", Mappings: map[string]string{"x": "1"}}
	if b, _ := canon.Encode(tr); !strings.Contains(string(b), `"artists":[]`) {
		t.Fatalf("artists nil 要寫成 []:%s", b)
	}
}

func TestLayout(t *testing.T) {
	if f := canon.PlaylistFile("01X"); f.Name != "pl__01X.json" || f.Props["kind"] != canon.KindPlaylist || f.Props["pid"] != "01X" {
		t.Fatalf("%+v", f)
	}
	if f := canon.DeviceFile("D1"); f.Name != "dev__D1.json" || f.Props["kind"] != canon.KindDevice || f.Props["device_id"] != "D1" {
		t.Fatalf("%+v", f)
	}
	if canon.ManifestFile().Name != "manifest.json" || canon.TracksFile().Props["kind"] != canon.KindTracks {
		t.Fatal("manifest / tracks 檔名")
	}
}
