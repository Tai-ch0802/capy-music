package canon

import (
	"fmt"
	"maps"
	"slices"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 身分函式與合併(spec §5.1,決策 19、21)。

// Identity 是 tracks.json 的反查索引:(provider, id) → cid、正規化 ISRC → cid、合併墓碑鏈。NewIdentity 建一次、整輪重複用;只讀。
type Identity struct {
	byMapping map[string]string // provider + "\x00" + id → cid
	byISRC    map[string]string // 正規化 ISRC → cid
	merged    map[string]string // 敗者 cid → 勝者 cid(Tracks.Merged)
	warnings  []string
}

// NewIdentity 由 tracks(cid → track)與 merged(墓碑)建索引。同一個 (provider, id) 或 ISRC 落在多個 cid 理論上不發生
// (合併只由人決定、alias set 只在人工操作成長;pinned 對到別人的 id 那種矛盾 T2a 記在 conflicts、不進 mappings),
// 真的發生就取字典序最小的 cid 並記警告——兩次建出的結果相同,不隨 map 走訪順序抖動。
func NewIdentity(tracks map[string]Track, merged map[string]string) *Identity {
	id := &Identity{byMapping: map[string]string{}, byISRC: map[string]string{}, merged: merged}
	claim := func(index map[string]string, key, cid, what string) {
		if prev, ok := index[key]; ok {
			id.warnings = append(id.warnings, fmt.Sprintf("%s 同時屬於 %s 與 %s,採用 %s", what, prev, cid, prev))
			return
		}
		index[key] = cid
	}
	for _, cid := range slices.Sorted(maps.Keys(tracks)) { // 字典序走訪:先到先得 = 取最小
		tr := tracks[cid]
		for _, prov := range slices.Sorted(maps.Keys(tr.Mappings)) {
			if m := tr.Mappings[prov]; m.ID != "" { // 釘成「不可得」的空 id 不是索引鍵
				claim(id.byMapping, mappingKey(prov, m.ID), cid, prov+":"+m.ID)
			}
		}
		for _, isrc := range tr.ISRC {
			if n := NormalizeISRC(isrc); n != "" {
				claim(id.byISRC, n, cid, "ISRC "+n)
			}
		}
	}
	return id
}

func mappingKey(prov, id string) string { return prov + "\x00" + id }

// Warnings 是建索引時發現的多重歸屬(pull 印到 stderr)。
func (id *Identity) Warnings() []string { return id.warnings }

// Resolve 是決策 19 的身分函式:平台 prov 觀測到 (providerID, isrc) 屬於哪個 cid。
// (1) 既有 mapping (prov, id) → cid;(2) 正規化 ISRC 在某個 alias set → cid;(3) §6.2 公式;(4) 結果是墓碑就沿鏈追到勝者。
func (id *Identity) Resolve(prov, providerID, isrc string) string {
	cid, ok := id.byMapping[mappingKey(prov, providerID)]
	if !ok {
		if n := NormalizeISRC(isrc); n != "" {
			cid, ok = id.byISRC[n]
		}
	}
	if !ok {
		cid = CID(prov, providerID, isrc)
	}
	return id.Redirect(cid)
}

// Redirect 只沿墓碑追到勝者。base 快照與清單 item 的 cid 用這個、不用 Resolve:重釘到別的 id 的 cid 反查會走丟(決策 19)。
// 鏈在寫入時已壓平,這裡仍逐步追、步數以墓碑數為限:手改過的檔弄出迴圈也不會讓 pull 卡死。
func (id *Identity) Redirect(cid string) string {
	for n := 0; n < len(id.merged); n++ {
		next, ok := id.merged[cid]
		if !ok || next == cid {
			break
		}
		cid = next
	}
	return cid
}

// RedirectItems 把清單裡指向墓碑的 item cid 改成勝者(COMMIT 途中斷掉、tracks.json 已寫但清單沒寫到時的自癒)。
// 回傳改了幾筆;有改就是清單真的變了,updated_at 前進。
func (id *Identity) RedirectItems(p *Playlist) int {
	n := 0
	for i, it := range p.Items {
		if to := id.Redirect(it.CID); to != it.CID {
			p.Items[i].CID = to
			n++
		}
	}
	if n > 0 {
		p.UpdatedAt = Now().Unix()
	}
	return n
}

// Merge 把 a、b 併成一個 cid(決策 21;只由人決定,自動寫入永不呼叫)。勝者是字典序較小的 cid;敗者的 alias set、mappings、
// conflicts 併入勝者——同 provider 的 mapping 一律取優先序高的那個(pinned > observed > 其他,同級勝者留):同 id 就只留一個,
// 不同 id 時輸的那邊的 id 進 conflicts[](交給 review;敗者是 pinned 而勝者只是觀測 → 敗者的釘選留下、勝者的 id 進 conflicts);
// 所有清單裡敗者的 item 改指勝者(清單 updated_at 前進);敗者從 tracks 移除、留墓碑 merged[敗者] = 勝者,既有指向敗者的墓碑一併壓平。
// 傳入的 cid 先沿墓碑追:review 拿到的可能是已合併過的舊 cid。cid 除此之外永不改寫,p: 也不會因為拿到 ISRC 而換成 i:。
func Merge(tr *Tracks, playlists map[string]*Playlist, a, b string) (survivor string, err error) {
	id := NewIdentity(nil, tr.Merged)
	a, b = id.Redirect(a), id.Redirect(b)
	if a == b {
		return "", fmt.Errorf("%s 已經是同一個 cid", a)
	}
	for _, cid := range []string{a, b} {
		if _, ok := tr.Tracks[cid]; !ok {
			return "", fmt.Errorf("tracks 沒有 %s", cid)
		}
	}
	survivor, loser := min(a, b), max(a, b)
	s, l := cloneTrack(tr.Tracks[survivor]), tr.Tracks[loser]
	if s.Mappings == nil { // Decode / NewTrack / Dump 都給非 nil,手組的 Track 不一定
		s.Mappings = map[string]Mapping{}
	}
	for _, isrc := range l.ISRC {
		if !slices.Contains(s.ISRC, isrc) {
			s.ISRC = append(s.ISRC, isrc)
		}
	}
	slices.Sort(s.ISRC)
	for _, prov := range slices.Sorted(maps.Keys(l.Mappings)) {
		lm, sm := l.Mappings[prov], s.Mappings[prov]
		switch _, has := s.Mappings[prov]; {
		case !has:
			s.Mappings[prov] = lm
		case sm.ID == lm.ID:
			if mappingRank(lm) > mappingRank(sm) {
				s.Mappings[prov] = lm
			}
		default: // 不同 id:優先序高的留下(同級勝者留),輸的那邊的 id 進 conflicts——人的釘選不會被一次自動觀測蓋掉(決策 20)
			if mappingRank(lm) > mappingRank(sm) {
				s.Mappings[prov] = lm
				if sm.ID != "" {
					s.addConflict(prov, provider.Track{ProviderID: sm.ID, Title: s.Title, DurationMS: s.DurationMS})
				}
			} else if lm.ID != "" { // 敗者釘成「不可得」而輸給勝者的 pinned id:空 id 沒東西可記
				s.addConflict(prov, provider.Track{ProviderID: lm.ID, Title: l.Title, DurationMS: l.DurationMS})
			}
		}
	}
	for _, c := range l.Conflicts {
		s.addConflict(c.Provider, provider.Track{ProviderID: c.ProviderID, Title: c.Title, DurationMS: c.DurationMS})
	}
	tr.Tracks[survivor] = s
	delete(tr.Tracks, loser)
	if tr.Merged == nil {
		tr.Merged = map[string]string{}
	}
	for k, v := range tr.Merged {
		if v == loser {
			tr.Merged[k] = survivor
		}
	}
	tr.Merged[loser] = survivor
	for _, p := range playlists {
		n := 0
		for i, it := range p.Items {
			if it.CID == loser {
				p.Items[i].CID = survivor
				n++
			}
		}
		if n > 0 {
			p.UpdatedAt = Now().Unix()
		}
	}
	return survivor, nil
}

// mappingRank:決策 20 的優先序 pinned > observed > isrc / fuzzy / review。
func mappingRank(m Mapping) int {
	switch {
	case m.Pinned:
		return 2
	case m.Source == SourceObserved:
		return 1
	}
	return 0
}
