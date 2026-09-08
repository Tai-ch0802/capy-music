// Package resolve 是 P4 resolver 的純函式核心(spec §5.1–5.2,附錄 C 決策 23):標題正規化、Jaro-Winkler、fuzzy 評分與排序、
// ISRC 候選消歧、「哪些 (清單, provider) 缺 mapping」。不碰 IO——誰去打 API、誰寫 mapping 是 capy resolve(T4)的事。
package resolve

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// capTags:任一只出現在標題的一邊就把分數壓到 84(強制進 review,決策 23)。
// stripTags:Norm 會剝掉的括號群組 / ` - ` 後綴的起頭字,涵蓋 capTags——所以 cap 一定要在剝掉之前(fold 之後)判斷,
// 不然 "(Live)" 先被 Norm 吃掉,cap 永遠不會觸發。
var (
	capTags   = []string{"live", "remix", "acoustic", "cover", "demo", "instrumental", "karaoke"}
	stripTags = append([]string{"feat", "ft", "featuring", "with", "remaster", "remastered", "version", "ver", "edit", "mix", "mono", "stereo",
		"radio", "single", "deluxe", "bonus", "explicit", "clean", "extended", "original", "from", "prod", "official"}, capTags...)
)

// Norm:小寫 → 全形 ASCII 轉半形 → 剝掉以標籤字起頭的 (…) / […] 群組與 ` - …` 後綴 → 標點換空白 → 空白壓縮。CJK 是字母,留著、不斷詞。
// ponytail: 不做 NFKC(不引 x/text)、不斷中文詞、artist alias 表延後(決策 23)。
func Norm(s string) string { return strings.Join(tokens(stripSuffixes(widen(s))), " ") }

// widen:小寫、全形 ASCII(U+FF01–FF5E)與全形空白(U+3000)轉半形;標點留著給 stripSuffixes 認括號與 ` - `。
func widen(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == 0x3000:
			return ' '
		case r >= 0xFF01 && r <= 0xFF5E:
			return r - 0xFEE0
		}
		return r
	}, strings.ToLower(s))
}

// tokens:字母 / 數字以外全當分隔(標點換空白,"don't" → "don t";JW 對這種差異不敏感,兩邊同規則就好)。
func tokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func isTag(tok string, tags []string) bool { return slices.Contains(tags, tok) }

// stripSuffixes:(…) / […] 群組第一個 token 是標籤字就整組刪,否則只留內容(括號本身之後會被 tokens 當分隔);
// ` - ` 後面第一個 token 是標籤字就從那裡截斷(從左邊第一個命中的截,後面的一起掉)。
func stripSuffixes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		open := s[i]
		if open != '(' && open != '[' {
			b.WriteByte(open)
			i++
			continue
		}
		closeCh := byte(')')
		if open == '[' {
			closeCh = ']'
		}
		end := strings.IndexByte(s[i+1:], closeCh)
		if end < 0 {
			b.WriteString(s[i:])
			break
		}
		inner := s[i+1 : i+1+end]
		if t := tokens(inner); len(t) == 0 || !isTag(t[0], stripTags) {
			b.WriteByte(' ')
			b.WriteString(inner)
			b.WriteByte(' ')
		}
		i += end + 2
	}
	out := b.String()
	for from := 0; ; {
		k := strings.Index(out[from:], " - ")
		if k < 0 {
			break
		}
		k += from
		if t := tokens(out[k+3:]); len(t) > 0 && isTag(t[0], stripTags) {
			out = out[:k]
			break
		}
		from = k + 3
	}
	return out
}

// capSet:標題裡出現的 capTags(在 fold 後、剝標籤前的整個標題上找,含括號內)。
func capSet(title string) map[string]bool {
	out := map[string]bool{}
	for _, t := range tokens(widen(title)) {
		if isTag(t, capTags) {
			out[t] = true
		}
	}
	return out
}

// JaroWinkler 以 rune 計算,標準定義:配對窗 = max(len)/2 − 1、轉置數 = 錯位配對數 / 2、共同前綴最多 4、p = 0.1。
// 兩邊都空 = 1、一邊空 = 0;對稱;範圍 [0, 1]。
func JaroWinkler(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 && len(rb) == 0 {
		return 1
	}
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	window := max(len(ra), len(rb))/2 - 1
	if window < 0 {
		window = 0
	}
	ma, mb := make([]bool, len(ra)), make([]bool, len(rb))
	m := 0
	for i := range ra {
		for j := max(0, i-window); j <= min(len(rb)-1, i+window); j++ {
			if !mb[j] && ra[i] == rb[j] {
				ma[i], mb[j] = true, true
				m++
				break
			}
		}
	}
	if m == 0 {
		return 0
	}
	t, k := 0, 0
	for i := range ra {
		if !ma[i] {
			continue
		}
		for !mb[k] {
			k++
		}
		if ra[i] != rb[k] {
			t++
		}
		k++
	}
	fm := float64(m)
	jaro := (fm/float64(len(ra)) + fm/float64(len(rb)) + (fm-float64(t)/2)/fm) / 3
	l := 0
	for l < 4 && l < len(ra) && l < len(rb) && ra[l] == rb[l] {
		l++
	}
	return jaro + float64(l)*0.1*(1-jaro)
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

func primary(artists []string) string {
	if len(artists) == 0 {
		return ""
	}
	return artists[0]
}

// ScoreFuzzy(決策 23 Layer 2):60·JW(title) + 25·JW(primary artist) + 15·時長項(≤3 s 滿分、≥30 s 零、線性);
// capTags 任一只在一邊、或 |Δ時長| > 3 s → 上限 84(時長是標題沒帶關鍵字時唯一能分辨 extended mix / radio edit 的訊號,
// 不能被 title + artist 的 85 分蓋過去);floor 取整(84.9 進 review)。
// ponytail: 時長不明(0,上傳曲)的一邊 |Δ| 幾乎必 ≥ 30 s → 時長項 0 且被壓到 84,永遠不會自動寫入,只會進 review——刻意保守。
// 主要藝人任一邊缺 → 藝人項 0(兩邊都缺不算相同)。
func ScoreFuzzy(target canon.Track, c provider.Track) int {
	s := 60*JaroWinkler(Norm(target.Title), Norm(c.Title)) + 25*artistTerm(target.Artists, c.Artists) + 15*durationTerm(target.DurationMS, c.DurationMS)
	if !maps.Equal(capSet(target.Title), capSet(c.Title)) || absDiff(target.DurationMS, c.DurationMS) > 3000 {
		s = math.Min(s, 84)
	}
	return int(math.Floor(s + 1e-9)) // 1e-9:浮點誤差不該把剛好 84 / 100 變成 83 / 99
}

func artistTerm(a, b []string) float64 {
	pa, pb := Norm(primary(a)), Norm(primary(b))
	if pa == "" || pb == "" {
		return 0
	}
	return JaroWinkler(pa, pb)
}

func durationTerm(a, b int) float64 {
	d := absDiff(a, b)
	switch {
	case d <= 3000:
		return 1
	case d >= 30000:
		return 0
	}
	return float64(30000-d) / 27000
}

// Scored 是排好序的 fuzzy 候選。
type Scored struct {
	Track provider.Track
	Score int
}

// RankFuzzy:候選依 ScoreFuzzy 評分,<60 不列;排序 分數高 > |Δ時長| 小 > provider id 字典序小——同分(專輯版 vs 合輯重發)時
// 兩台裝置也要選到同一個(決策 21 / 22)。
func RankFuzzy(target canon.Track, cands []provider.Track) []Scored {
	var out []Scored
	for _, c := range cands {
		if s := ScoreFuzzy(target, c); s >= 60 {
			out = append(out, Scored{c, s})
		}
	}
	slices.SortStableFunc(out, func(x, y Scored) int {
		if c := cmp.Compare(y.Score, x.Score); c != 0 {
			return c
		}
		if c := cmp.Compare(absDiff(x.Track.DurationMS, target.DurationMS), absDiff(y.Track.DurationMS, target.DurationMS)); c != 0 {
			return c
		}
		return strings.Compare(x.Track.ProviderID, y.Track.ProviderID)
	})
	return out
}

// FuzzyQuery 是 Layer 2 的搜尋字串:norm(title) + " " + norm(primary artist)。與評分用同一個 Norm,不會各走各的。
func FuzzyQuery(t canon.Track) string {
	return strings.TrimSpace(Norm(t.Title) + " " + Norm(primary(t.Artists)))
}

// PickISRC(決策 23 Layer 1):候選只留正規化 ISRC 在 target alias set 裡的;多筆消歧 專輯名(Norm)相同 > |Δ時長| 最小 > provider id 字典序最小。
func PickISRC(target canon.Track, cands []provider.Track) (provider.Track, bool) {
	alias := map[string]bool{}
	for _, i := range target.ISRC {
		if n := provider.NormalizeISRC(i); n != "" {
			alias[n] = true
		}
	}
	var hits []provider.Track
	for _, c := range cands {
		if alias[provider.NormalizeISRC(c.ISRC)] {
			hits = append(hits, c)
		}
	}
	if len(hits) == 0 {
		return provider.Track{}, false
	}
	album := Norm(target.Album)
	slices.SortStableFunc(hits, func(a, b provider.Track) int {
		if x, y := Norm(a.Album) == album, Norm(b.Album) == album; x != y {
			if x {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(absDiff(a.DurationMS, target.DurationMS), absDiff(b.DurationMS, target.DurationMS)); c != 0 {
			return c
		}
		return strings.Compare(a.ProviderID, b.ProviderID)
	})
	return hits[0], true
}

// Need:某個 cid 在某個 provider 缺 mapping,以及哪些清單(pid,排序)要它。
type Need struct {
	CID       string
	Provider  string
	Playlists []string
}

// needsMapping:沒有 mapping、或非 pinned 的空 id(壞資料,PR #26)= 缺;pinned 不論 id 都算有(空 id = 使用者裁定該平台沒有)。
func needsMapping(m canon.Mapping, ok bool) bool { return !ok || (m.ID == "" && !m.Pinned) }

// Needs:每個有 link 的 (清單, provider),items 裡缺該 provider mapping 的 cid;懸空 item(tracks 沒有的 cid)跳過;
// 跨清單去重;依 (provider, cid) 排序——決定性,cron 下每次一樣。
func Needs(playlists []canon.Playlist, tracks map[string]canon.Track) []Need {
	byKey := map[[2]string]*Need{}
	for _, pl := range playlists {
		for _, prov := range slices.Sorted(maps.Keys(pl.Links)) {
			for _, it := range pl.Items {
				tr, ok := tracks[it.CID]
				if !ok {
					continue
				}
				if m, has := tr.Mappings[prov]; !needsMapping(m, has) {
					continue
				}
				k := [2]string{prov, it.CID}
				n := byKey[k]
				if n == nil {
					n = &Need{CID: it.CID, Provider: prov}
					byKey[k] = n
				}
				if !slices.Contains(n.Playlists, pl.PID) {
					n.Playlists = append(n.Playlists, pl.PID)
				}
			}
		}
	}
	out := make([]Need, 0, len(byKey))
	for _, n := range byKey {
		slices.Sort(n.Playlists)
		out = append(out, *n)
	}
	slices.SortFunc(out, func(a, b Need) int {
		if c := strings.Compare(a.Provider, b.Provider); c != 0 {
			return c
		}
		return strings.Compare(a.CID, b.CID)
	})
	return out
}
