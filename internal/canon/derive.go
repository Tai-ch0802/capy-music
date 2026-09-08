package canon

import (
	"fmt"
	"maps"
	"slices"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// DERIVE(spec §6.5.1):純函式,不碰 IO,不改動傳入的 Playlist / Tracks。

// Observed 是平台清單的現況:名稱與依平台順序的曲目(含 ISRC)。
type Observed struct {
	Name   string
	Tracks []provider.Track
}

type DeriveInput struct {
	Provider string
	Playlist Playlist         // C
	Tracks   map[string]Track // tracks.json 的 cid → track(只讀)
	Base     *Snapshot        // 本裝置上次觀測;nil = 沒有(首次 pull)
	Live     *Observed        // 平台現況;nil = 清單消失
}

// Change 是變更集的一列;欄位對齊 T8 的 TSV:action pos cid provider_id title artists reason。
type Change struct {
	Action     string // add / remove / move / rename / unlink
	Pos        int    // add、move:在 L 的位置;remove:原本在 C 的位置(rank 序)
	From       int    // move:原本在 C 的位置
	IID        string
	CID        string
	ProviderID string
	Title      string
	Artists    []string
	Reason     string
}

type DeriveResult struct {
	Playlist     Playlist         // 套用後的 C;沒有變更時與輸入逐位元相同
	Tracks       map[string]Track // 新建或加了 mapping / 衝突的 track
	Snapshot     Snapshot         // 新的 base = L 的 provider id 原文(Gone 時為零值)
	Changes      []Change         // 順序:rename、remove(依 C 位置)、add(依 L 位置)、move(依 L 位置)
	Gone         bool
	VisibleCount int // 變更前 cid 有該 provider mapping 的 item 數(Q3 閾值分母)
}

func Derive(in DeriveInput) (DeriveResult, error) {
	prov := in.Provider
	c := in.Playlist
	c.Items = slices.Clone(in.Playlist.Items)
	c.Links = maps.Clone(in.Playlist.Links)
	c.normalize()
	res := DeriveResult{Playlist: c, Tracks: map[string]Track{}}
	for _, it := range c.Items {
		if tr, ok := in.Tracks[it.CID]; ok && tr.Mappings[prov] != "" {
			res.VisibleCount++
		}
	}
	if in.Live == nil {
		res.Gone = true
		res.Changes = []Change{{Action: "unlink", Reason: "平台端清單不存在(Q6:自動取消連結)"}}
		return res, nil
	}
	L := in.Live.Tracks
	now := Now().Unix()

	// 規則 1、3:L 的每首算 cid 並觀測寫回 tracks。
	lcid := make([]string, len(L))
	lookup := func(cid string) (Track, bool) {
		if t, ok := res.Tracks[cid]; ok {
			return t, true
		}
		t, ok := in.Tracks[cid]
		return t, ok
	}
	for i, t := range L {
		cid := CID(prov, t.ProviderID, t.ISRC)
		lcid[i] = cid
		tr, ok := lookup(cid)
		if !ok {
			res.Tracks[cid] = NewTrack(prov, t)
			continue
		}
		cp := cloneTrack(tr)
		hadMap := cp.Mappings[prov] != ""
		if conflict := cp.Observe(prov, t); conflict || !hadMap {
			res.Tracks[cid] = cp
		}
	}

	// 規則 2:同 cid 第 n 次出現互相配對。
	cOcc := map[string][]int{} // cid → C 的 item 索引(rank 序)
	for i, it := range c.Items {
		cOcc[it.CID] = append(cOcc[it.CID], i)
	}
	lOcc := map[string][]int{} // cid → L 的位置
	for pos, cid := range lcid {
		lOcc[cid] = append(lOcc[cid], pos)
	}
	pairedItem := make([]int, len(c.Items)) // item → L 位置,-1 = 未配對
	pairedL := make([]int, len(L))          // L 位置 → item,-1 = 未配對
	for i := range pairedItem {
		pairedItem[i] = -1
	}
	for i := range pairedL {
		pairedL[i] = -1
	}
	for cid, lp := range lOcc {
		co := cOcc[cid]
		for i := 0; i < min(len(co), len(lp)); i++ {
			pairedL[lp[i]], pairedItem[co[i]] = co[i], lp[i]
		}
	}

	// 規則 5:只在 base 存在時移除;base 的 provider id 經 mapping 反查成 cid。
	removed := map[int]bool{}
	if in.Base != nil {
		rev := map[string]string{} // provider id → cid
		for cid, tr := range in.Tracks {
			if id := tr.Mappings[prov]; id != "" {
				rev[id] = cid
			}
		}
		bCnt := map[string]int{}
		for _, id := range in.Base.Items {
			if cid, ok := rev[id]; ok {
				bCnt[cid]++
			}
		}
		for cid, b := range bCnt {
			extra := b - len(lOcc[cid])
			co := cOcc[cid]
			unpaired := co[min(len(co), len(lOcc[cid])):]
			for i := len(unpaired) - 1; i >= 0 && extra > 0; i-- {
				removed[unpaired[i]] = true
				extra--
			}
		}
	}

	// 規則 6:配對的 item 依 L 順序;LCS 找最少搬動。
	var current, target []int
	for i := range c.Items {
		if pairedItem[i] >= 0 {
			current = append(current, i)
		}
	}
	for pos := range L {
		if pairedL[pos] >= 0 {
			target = append(target, pairedL[pos])
		}
	}
	kept := lcsKeep(current, target)
	moved := map[int]bool{}
	for _, i := range current {
		if !kept[i] {
			moved[i] = true
		}
	}

	// 最終整體順序:留在原位的 item 依 rank,搬動與新增的插在它在 L 的前一個元素之後。
	type ref struct{ item, lpos int } // item = -1 表示新增(lpos 是 L 位置)
	var final []ref
	for i := range c.Items {
		if !removed[i] && !moved[i] {
			final = append(final, ref{i, -1})
		}
	}
	fixed := func(r ref) bool { return r.item >= 0 && !moved[r.item] }
	prev := -1
	for pos := range L {
		if item := pairedL[pos]; item >= 0 && !moved[item] {
			prev = slices.IndexFunc(final, func(r ref) bool { return r.item == item })
			continue
		}
		prev++
		final = slices.Insert(final, prev, ref{pairedL[pos], pos})
	}

	// 規則 4、6:rank。C 全空用 Ranks 均分;其餘 RankBetween 於最終順序的鄰居之間(下一個鄰居取最近的未搬動 item)。
	items := make([]Item, 0, len(final))
	var even []string
	if len(c.Items) == 0 {
		even = Ranks(len(final))
	}
	prevRank := ""
	for k, r := range final {
		var it Item
		switch {
		case fixed(r):
			it = c.Items[r.item]
		default:
			var rank string
			if even != nil {
				rank = even[k]
			} else {
				next := ""
				for j := k + 1; j < len(final); j++ {
					if fixed(final[j]) {
						next = c.Items[final[j].item].Rank
						break
					}
				}
				var err error
				if rank, err = RankBetween(prevRank, next); err != nil {
					return res, fmt.Errorf("清單 %s 的 rank 資料髒了,不自動重排:%w", c.PID, err)
				}
			}
			if r.item >= 0 {
				it = c.Items[r.item]
				it.Rank = rank
			} else {
				it = Item{IID: NewULID(), CID: lcid[r.lpos], Rank: rank, AddedAt: now}
			}
		}
		items = append(items, it)
		prevRank = it.Rank
	}

	// 變更集(順序:rename、remove、add、move)。
	var changes []Change
	if in.Base != nil && in.Live.Name != in.Base.Name && c.Name != in.Live.Name { // 規則 7
		changes = append(changes, Change{Action: "rename", Title: in.Live.Name, Reason: "平台改名:" + c.Name + " → " + in.Live.Name})
		c.Name = in.Live.Name
	}
	title := func(cid string) (string, []string) {
		if t, ok := lookup(cid); ok {
			return t.Title, t.Artists
		}
		return "", nil
	}
	for i, it := range c.Items {
		if removed[i] {
			t, a := title(it.CID)
			pid := ""
			if tr, ok := lookup(it.CID); ok {
				pid = tr.Mappings[prov]
			}
			changes = append(changes, Change{Action: "remove", Pos: i, IID: it.IID, CID: it.CID, ProviderID: pid, Title: t, Artists: a, Reason: "平台已移除"})
		}
	}
	for pos, t := range L {
		if pairedL[pos] < 0 {
			changes = append(changes, Change{Action: "add", Pos: pos, CID: lcid[pos], ProviderID: t.ProviderID, Title: t.Title, Artists: t.Artists, Reason: "平台新增"})
		}
	}
	for pos, t := range L {
		if item := pairedL[pos]; item >= 0 && moved[item] {
			it := c.Items[item]
			changes = append(changes, Change{Action: "move", Pos: pos, From: item, IID: it.IID, CID: it.CID, ProviderID: t.ProviderID, Title: t.Title, Artists: t.Artists, Reason: "平台換序"})
		}
	}
	if len(changes) > 0 { // 規則 10:無變更時逐位元不變
		c.Items = items
		c.UpdatedAt = now
	}
	res.Playlist = c
	res.Changes = changes
	ids := make([]string, len(L))
	for i, t := range L {
		ids[i] = t.ProviderID
	}
	res.Snapshot = Snapshot{Name: in.Live.Name, Items: ids}
	return res, nil
}

// lcsKeep 回 current 與 target 的最長共同子序列成員(不用搬的 item);多解時保留 current 中較前者。
// ponytail: O(n·m) 的 DP,清單幾百首沒問題;上萬首再換 patience diff。
func lcsKeep(current, target []int) map[int]bool {
	n, m := len(current), len(target)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if current[i-1] == target[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max(dp[i-1][j], dp[i][j-1])
			}
		}
	}
	keep := map[int]bool{}
	for i, j := n, m; i > 0 && j > 0; {
		switch {
		case current[i-1] == target[j-1]:
			keep[current[i-1]] = true
			i--
			j--
		case dp[i-1][j] >= dp[i][j-1]: // 平手時先捨棄 current 較後的元素
			i--
		default:
			j--
		}
	}
	return keep
}

func cloneTrack(t Track) Track {
	t.ISRC = slices.Clone(t.ISRC)
	t.Artists = slices.Clone(t.Artists)
	t.Mappings = maps.Clone(t.Mappings)
	t.Conflicts = slices.Clone(t.Conflicts)
	return t
}
