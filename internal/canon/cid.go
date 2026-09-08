package canon

import (
	"strings"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// NormalizeISRC:大寫、去連字號與空白;非 12 碼視為缺失(回空字串)。定義在 provider(反查也要用同一個),這裡只是門面。
func NormalizeISRC(s string) string { return provider.NormalizeISRC(s) }

// CID 是決定性 ID:有 ISRC → i:<正規化 ISRC>,否則 p:<prov>:<trackID>(prov 是平台名,trackID 是該平台的曲目 id)。
// 它是 Drive 檔案裡的鍵,只能由觀測到的那首歌決定,不能隨「當時還看到什麼」而變——所以沒有
// 「同 ISRC 但 metadata 不符就退回 p:」的防呆,衝突另記在 Track.Conflicts(spec §6.2)。
func CID(prov, trackID, isrc string) string {
	if n := NormalizeISRC(isrc); n != "" {
		return "i:" + n
	}
	return "p:" + prov + ":" + trackID
}

// NewTrack 由平台 prov 的觀測建 canonical track(t.ProviderID 是該平台的曲目 id)。
func NewTrack(prov string, t provider.Track) Track {
	tr := Track{
		CID:        CID(prov, t.ProviderID, t.ISRC),
		Title:      t.Title,
		Artists:    append([]string{}, t.Artists...),
		Album:      t.Album,
		DurationMS: t.DurationMS,
		Mappings:   map[string]Mapping{prov: observedMapping(t.ProviderID)},
	}
	if n := NormalizeISRC(t.ISRC); n != "" {
		tr.ISRC = []string{n}
	}
	return tr
}

// Observe 併入同一 cid 的另一次觀測:該 provider 尚無 mapping 就加(已有就保留第一個,mapping 不可抖動,
// T7 的 remove 只算有 mapping 的 cid);metadata 不符(時長差 >3s 或標題不同)記一筆 Conflict,同一筆不重複,
// 每輪 pull 都會再觀測一次,不冪等的話 tracks.json 會一直長。cid 永不改。回傳是否新增了衝突。
// ponytail: 標題比對只做 trim + 不分大小寫,P4 的 norm()(feat. / Remastered / 全半形)到時替換這裡。
func observedMapping(id string) Mapping {
	return Mapping{ID: id, Confidence: 100, Source: SourceObserved, UpdatedAt: Now().Unix()}
}

// Observe:平台 prov 在清單裡看到 t 對到這個 cid。mapping 依決策 20 的優先序 pinned > observed > isrc / fuzzy:
// 沒有 → 加 observed;observed → 不動(不抖動);isrc / fuzzy → 被真實觀測覆寫(同 id 也升成 observed / 100);
// pinned → 永不動,觀測到不同 id(含釘成「不可得」卻看到了)只記進 conflicts 交給 review。
// 回 (changed, conflict):changed = mapping 動了;conflict = 新記了一筆 metadata / 釘選矛盾(重複觀測不重複記)。
func (tr *Track) Observe(prov string, t provider.Track) (changed, conflict bool) {
	if tr.Mappings == nil {
		tr.Mappings = map[string]Mapping{}
	}
	if m, ok := tr.Mappings[prov]; !ok {
		tr.Mappings[prov] = observedMapping(t.ProviderID)
		changed = true
	} else if m.Pinned {
		if m.ID != t.ProviderID {
			return false, tr.addConflict(prov, t)
		}
	} else if m.Source != SourceObserved || m.ID == "" { // 非 pinned 的空 id 是壞資料(手改 / 舊檔寫了 ""),不是「不可得」——那是 pinned 的語意;看到就修回來
		tr.Mappings[prov] = observedMapping(t.ProviderID)
		changed = true
	}
	d := tr.DurationMS - t.DurationMS
	if d < 0 {
		d = -d
	}
	if d <= 3000 && strings.EqualFold(strings.TrimSpace(tr.Title), strings.TrimSpace(t.Title)) {
		return changed, false
	}
	return changed, tr.addConflict(prov, t)
}

// addConflict 記一筆衝突事實(去重;每輪 pull 都會再看到同一筆)。
func (tr *Track) addConflict(prov string, t provider.Track) bool {
	c := Conflict{Provider: prov, ProviderID: t.ProviderID, Title: t.Title, DurationMS: t.DurationMS}
	for _, e := range tr.Conflicts {
		if e == c {
			return false
		}
	}
	tr.Conflicts = append(tr.Conflicts, c)
	return true
}
