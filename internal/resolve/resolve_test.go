package resolve

import (
	"math"
	"slices"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func TestNorm(t *testing.T) {
	for in, want := range map[string]string{
		"派對動物":                              "派對動物",
		"Ｐａｒｔｙ　Ａｎｉｍａｌ!":                     "party animal",
		"Song (feat. Ed Sheeran)":           "song",
		"Song [Remastered 2011]":            "song",
		"Song - Live":                       "song",
		"Song - Remastered 2011":            "song",
		"Song - Live at Wembley - 2011":     "song",
		"Song - Part 2":                     "song part 2", // 不是標籤字起頭的後綴留著
		"Song (Part 2)":                     "song part 2",
		"Song (Live) [Remix]":               "song",
		"Don't Stop Me Now!":                "don t stop me now",
		"  Rock-n-Roll   Star ":             "rock n roll star",
		"(Live":                             "live", // 沒閉合:照留
		"":                                  "",
		"派對動物 (Live)":                       "派對動物",
		"Song (Version 2) - Radio Edit":     "song",
		"Something About Us (Original Mix)": "something about us",
		"派對動物(Live)完整版":                     "派對動物 完整版", // 剝掉群組也要留分隔:CJK 不斷詞
		"派對動物 (Live) 完整版":                   "派對動物 完整版",
		"派對動物 (Recorded Live at Wembley)":   "派對動物", // 標籤字不在第一個也剝
		"Song (Piano Version)":              "song",
		"Song (Part 2 Live)":                "song", // 混合寫法整組吃掉(capSet 會把它壓到 84)
		"Song - Recorded Live 2011":         "song",
	} {
		if got := Norm(in); got != want {
			t.Errorf("Norm(%q) = %q,要 %q", in, got, want)
		}
	}
}

func TestJaroWinkler(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want float64
	}{
		{"", "", 1}, {"", "a", 0}, {"abc", "abc", 1}, {"MARTHA", "MARHTA", 0.961}, {"DWAYNE", "DUANE", 0.840}, {"abc", "xyz", 0},
		{"派對動物", "派對動物", 1}, {"派對動物", "派對動物 live", 0.889}, {"a", "b", 0}, {"abcd", "abdc", 0.933},
	} {
		got := JaroWinkler(c.a, c.b)
		if math.Abs(got-c.want) > 1e-3 || math.Abs(JaroWinkler(c.b, c.a)-got) > 1e-12 || got < 0 || got > 1 {
			t.Errorf("JW(%q, %q) = %.4f,要 %.3f(且對稱、在 [0,1])", c.a, c.b, got, c.want)
		}
	}
}

func target(title string, dur int, artists ...string) canon.Track {
	return canon.Track{CID: "i:X", Title: title, Artists: artists, Album: "自傳", DurationMS: dur}
}

func cand(id, title string, dur int, artists ...string) provider.Track {
	return provider.Track{ProviderID: id, Title: title, Artists: artists, DurationMS: dur}
}

// 決策 23 的規格表:分數只決定「自動(≥85)vs review」,照規格驗、不照實作反推。
func TestScoreFuzzyTable(t *testing.T) {
	for name, c := range map[string]struct {
		t    canon.Track
		c    provider.Track
		want int
	}{
		"完全相同":                           {target("派對動物", 249957, "五月天"), cand("1", "派對動物", 249957, "五月天"), 100},
		"feat 後綴不影響":                     {target("派對動物", 249957, "五月天"), cand("1", "派對動物 (feat. 蕭敬騰)", 249000, "五月天"), 100},
		"全形半形":                           {target("Party Animal", 249957, "Mayday"), cand("1", "Ｐａｒｔｙ　Ａｎｉｍａｌ", 249957, "Ｍａｙｄａｙ"), 100},
		"Live 只在一邊 → 上限 84":              {target("派對動物", 249957, "五月天"), cand("1", "派對動物 (Live)", 249957, "五月天"), 84},
		"Recorded Live 也是 84":            {target("派對動物", 249957, "五月天"), cand("1", "派對動物 (Recorded Live at Wembley)", 249957, "五月天"), 84},
		"Live 在兩邊 → 不壓":                  {target("派對動物 - Live", 249957, "五月天"), cand("1", "派對動物 (Live)", 249957, "五月天"), 100},
		"remix 只在一邊(不是括號)":               {target("派對動物", 249957, "五月天"), cand("1", "派對動物 Club Remix", 249957, "五月天"), 84},
		"時長差 3 s 滿分":                     {target("派對動物", 249957, "五月天"), cand("1", "派對動物", 252957, "五月天"), 100},
		"時長差 10 s → 上限 84":               {target("派對動物", 249957, "五月天"), cand("1", "派對動物", 259957, "五月天"), 84},
		"時長差 30 s → 60+25 也只有 84":        {target("派對動物", 249957, "五月天"), cand("1", "派對動物", 279957, "五月天"), 84},
		"時長不明(0)永不自動":                    {target("上傳曲", 0, "藝人"), cand("1", "上傳曲", 200000, "藝人"), 84},
		"藝人一邊缺 → 藝人項 0":                  {target("派對動物", 249957), cand("1", "派對動物", 249957, "五月天"), 75},
		"藝人兩邊缺也是 0":                      {target("派對動物", 249957), cand("1", "派對動物", 249957), 75},
		"floor(abcdefg/abcdefh = 96.57)": {target("abcdefg", 249957, "五月天"), cand("1", "abcdefh", 249957, "五月天"), 96},
		"完全不同 <60":                       {target("派對動物", 249957, "五月天"), cand("1", "Bohemian Rhapsody", 354000, "Queen"), 0},
		"標題相同藝人不同":                       {target("派對動物", 249957, "五月天"), cand("1", "派對動物", 249957, "Queen"), 75},
	} {
		if got := ScoreFuzzy(c.t, c.c); got != c.want {
			t.Errorf("%s:%d,要 %d", name, got, c.want)
		}
	}
}

func TestRankFuzzyFiltersAndBreaksTies(t *testing.T) {
	tg := target("派對動物", 249957, "五月天")
	cands := []provider.Track{
		cand("z", "派對動物", 249957, "五月天"),                 // 100,同分 → id 字典序
		cand("b", "派對動物", 250957, "五月天"),                 // 100,|Δ| 1 s
		cand("a", "派對動物", 249957, "五月天"),                 // 100,|Δ| 0,id 最小
		cand("live", "派對動物 (Live)", 249957, "五月天"),       // 84
		cand("no", "Bohemian Rhapsody", 354000, "Queen"), // <60 不列
	}
	got := RankFuzzy(tg, cands)
	var ids []string
	for _, s := range got {
		ids = append(ids, s.Track.ProviderID)
	}
	if !slices.Equal(ids, []string{"a", "z", "b", "live"}) || got[0].Score != 100 || got[3].Score != 84 {
		t.Fatalf("分數高 > |Δ時長| 小 > id 小,<60 不列:%v %+v", ids, got)
	}
	if q := FuzzyQuery(tg); q != "派對動物 五月天" {
		t.Fatalf("FuzzyQuery:%q", q)
	}
	if q := FuzzyQuery(target("Song (Live)", 1)); q != "song" {
		t.Fatalf("沒有藝人就只有標題:%q", q)
	}
}

func TestPickISRC(t *testing.T) {
	tg := canon.Track{CID: "i:TWK231680790", ISRC: []string{"TWK231680790", "TWA472400123"}, Title: "派對動物", Album: "自傳", DurationMS: 249957}
	isrc := func(id, isrc, album string, dur int) provider.Track {
		return provider.Track{ProviderID: id, ISRC: isrc, Album: album, Title: "派對動物", DurationMS: dur}
	}
	if _, ok := PickISRC(tg, []provider.Track{isrc("x", "TW000000000X", "自傳", 249957), isrc("y", "", "自傳", 249957)}); ok {
		t.Fatal("ISRC 不同或缺的候選不算")
	}
	got, ok := PickISRC(tg, []provider.Track{
		isrc("c", "TWK231680790", "精選", 249957),    // 專輯不同
		isrc("b", "tw-a47-24-00123", "自傳", 251000), // alias set 第二個 ISRC、正規化後命中、專輯同、|Δ| 1 s
		isrc("a", "TWK231680790", "自傳", 249957),    // 專輯同、|Δ| 0 → 贏
	})
	if !ok || got.ProviderID != "a" {
		t.Fatalf("專輯同 > 時長差小 > id:%+v %t", got, ok)
	}
	got, _ = PickISRC(tg, []provider.Track{isrc("a", "TWK231680790", "精選", 249957), isrc("b", "TWK231680790", "自傳", 251957)})
	if got.ProviderID != "b" {
		t.Fatalf("專輯同先於時長差與 id:%s", got.ProviderID)
	}
	got, _ = PickISRC(tg, []provider.Track{isrc("b", "TWK231680790", "精選", 249957), isrc("a", "TWK231680790", "精選 2", 249957)})
	if got.ProviderID != "a" {
		t.Fatalf("都不是同專輯、時長同 → id 字典序:%s", got.ProviderID)
	}
	got, _ = PickISRC(tg, []provider.Track{isrc("b", "TWK231680790", "精選", 249957), isrc("a", "TWK231680790", "精選", 260000)})
	if got.ProviderID != "b" {
		t.Fatalf("時長差小先於 id:%s", got.ProviderID)
	}
}

func TestNeeds(t *testing.T) {
	m := func(id string, pinned bool) canon.Mapping {
		return canon.Mapping{ID: id, Confidence: 100, Pinned: pinned, Source: canon.SourceObserved}
	}
	tracks := map[string]canon.Track{
		"i:A": {CID: "i:A", Mappings: map[string]canon.Mapping{"spotify": m("a", false)}},                           // 缺 apple
		"i:B": {CID: "i:B", Mappings: map[string]canon.Mapping{"spotify": m("b", false), "apple": m("", true)}},     // apple 釘成不可得:不缺
		"i:C": {CID: "i:C", Mappings: map[string]canon.Mapping{"spotify": m("c", false), "apple": m("i.c", false)}}, // 齊
		"i:D": {CID: "i:D", Mappings: map[string]canon.Mapping{"spotify": m("", false), "apple": m("i.d", false)}},  // 非 pinned 空 id = 壞資料,算缺 spotify
	}
	p1 := canon.Playlist{PID: "p1", Links: map[string]string{"spotify": "s1", "apple": "a1"}, Items: []canon.Item{{CID: "i:A"}, {CID: "i:B"}, {CID: "i:C"}, {CID: "i:D"}, {CID: "i:GONE"}, {CID: "i:A"}}}
	p2 := canon.Playlist{PID: "p0", Links: map[string]string{"apple": "a2"}, Items: []canon.Item{{CID: "i:A"}, {CID: "i:D"}}} // 沒連 spotify:D 的 spotify 缺口不因它而來
	p3 := canon.Playlist{PID: "p3", Items: []canon.Item{{CID: "i:A"}}}                                                        // 沒有任何 link
	p4 := canon.Playlist{PID: "p4", Links: map[string]string{"tidal": ""}, Items: []canon.Item{{CID: "i:A"}}}                 // 空字串 link = 沒連結
	got := Needs([]canon.Playlist{p1, p2, p3, p4}, tracks)
	want := []Need{
		{CID: "i:A", Provider: "apple", Playlists: []string{"p0", "p1"}},
		{CID: "i:D", Provider: "spotify", Playlists: []string{"p1"}},
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i].CID != want[i].CID || got[i].Provider != want[i].Provider || !slices.Equal(got[i].Playlists, want[i].Playlists) {
			t.Fatalf("第 %d 筆:%+v,要 %+v", i, got[i], want[i])
		}
	}
	if len(Needs(nil, tracks)) != 0 || len(Needs([]canon.Playlist{p1}, nil)) != 0 {
		t.Fatal("沒清單 / 沒 tracks → 空")
	}
}
