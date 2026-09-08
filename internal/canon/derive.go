package canon

import (
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// DERIVE(spec §6.5.1):純函式,不碰 IO,不改動傳入的 Playlist / Tracks。

// Observed 是平台清單的現況:平台清單 id、名稱與依平台順序的曲目(含 ISRC)。
type Observed struct {
	ID     string
	Name   string
	Tracks []provider.Track
}

type DeriveInput struct {
	Provider string
	Playlist Playlist          // C;items 的 cid 必須已經過墓碑重導(fetchCanonical 負責),Derive 只重導 base 與 L——C 裡殘留的敗者 cid 會配不到、又多報一筆 add
	Tracks   map[string]Track  // tracks.json 的 cid → track(只讀)
	Base     *Snapshot         // 本裝置上次觀測;nil = 沒有(首次 pull)
	Live     *Observed         // 平台現況;nil = 清單消失
	Merged   map[string]string // tracks.json 的合併墓碑(敗者 cid → 勝者);nil = 沒有
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
	Snapshot     Snapshot         // 新的 base = L 的 provider id 原文與觀測當時的 cid(Gone 時為零值)
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
		if tr, ok := in.Tracks[it.CID]; ok && tr.Mappings[prov].ID != "" {
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

	// 規則 1、3:OBSERVE。
	id := NewIdentity(in.Tracks, in.Merged)
	lcid, updated := Observe(id, prov, in.Tracks, L)
	res.Tracks = updated
	lookup := func(cid string) (Track, bool) {
		if t, ok := res.Tracks[cid]; ok {
			return t, true
		}
		t, ok := in.Tracks[cid]
		return t, ok
	}

	// 規則 2、6:配對由 LCS 決定。先以 cid 序列(C 依 rank、L 依位置,重複照算)找最長共同子序列,對上的留在原位;
	// L 裡沒對上的出現依 L 順序拿同 cid 剩下的 C item(rank 序最前者)配對並搬動;沒有剩下的才是新增。
	// 盲配「第 n 次出現」會在重複曲目換序時這輪搬這份、下輪搬那份,永遠多報一筆 move。
	cOcc := map[string][]int{} // cid → C 的 item 索引(rank 序)
	for i, it := range c.Items {
		cOcc[it.CID] = append(cOcc[it.CID], i)
	}
	pairedItem, pairedL := lcsPairs(len(c.Items), lcid, cOcc) // item → L 位置 / L 位置 → item,-1 = 未配對
	moved := map[int]bool{}
	cursor := map[string]int{} // cid → cOcc 裡下一個還沒配對的候選
	for pos, cid := range lcid {
		if pairedL[pos] >= 0 {
			continue
		}
		co, k := cOcc[cid], cursor[cid]
		for k < len(co) && pairedItem[co[k]] >= 0 {
			k++
		}
		cursor[cid] = k
		if k < len(co) {
			pairedL[pos], pairedItem[co[k]] = co[k], pos
			moved[co[k]] = true
		}
	}
	lCnt := map[string]int{}
	for _, cid := range lcid {
		lCnt[cid]++
	}

	// 規則 4′、5、6′(P5 決策 27):base 存在時三方語意——計數用 base 快照裡觀測當時的 cid,只沿合併墓碑改寫(決策 19:
	// 不用 mapping 反查——重釘到別的 id 的 cid 反查會走丟,那首就永遠移不掉)。
	removed := map[int]bool{}
	ignoredL := map[int]bool{} // 規則 4′:L 裡配不到、又沒超出 base 計數的出現 = 平台還沒跟上 C 的移除,不新增、留給 push
	if in.Base != nil {
		baseCids := make([]string, len(in.Base.CIDs))
		bCnt := map[string]int{}
		for i, cid := range in.Base.CIDs {
			baseCids[i] = id.Redirect(cid)
			bCnt[baseCids[i]]++
		}
		// 規則 6′:平台沒重排(base 與 L 的共同元素順序一致)→ 配對上的 item 保留 C 的 rank、不報 move;
		// 不然 Spotify 的重排會被 Apple 的下一次 pull 翻回舊順序再 push 回 Spotify。兩邊都重排 → 後 pull 者勝。
		if !Reordered(baseCids, lcid) {
			clear(moved)
		}
		// 規則 4′:某 cid 在 L 出現 l 次、base b 次,l > b 才新增、至多 l − b 個,取 L 順序最後的那幾個(平台新增通常在尾端)
		unpaired := map[string][]int{}
		for pos, cid := range lcid {
			if pairedL[pos] < 0 {
				unpaired[cid] = append(unpaired[cid], pos)
			}
		}
		for cid, poss := range unpaired {
			allowed := max(0, lCnt[cid]-bCnt[cid])
			for _, pos := range poss[:max(0, len(poss)-allowed)] {
				ignoredL[pos] = true
			}
		}
		for cid, b := range bCnt {
			extra := b - lCnt[cid]
			co := cOcc[cid]
			for i := len(co) - 1; i >= 0 && extra > 0; i-- {
				if pairedItem[co[i]] < 0 {
					removed[co[i]] = true
					extra--
				}
			}
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
	lastPaired := -1     // 規則 4′:L 尾端的新增(後面沒有配對上的元素)一律接在 C 的尾端——平台沒重排而 C 順序不同時(6′),
	for pos := range L { // 使用者在平台尾端加的歌才會在 C 也在尾端,而不是插在「L 前一個元素」在 C 的位置之後(那可能是 C 的開頭)
		if pairedL[pos] >= 0 {
			lastPaired = pos
		}
	}
	prev := -1
	for pos := range L {
		if item := pairedL[pos]; item >= 0 && !moved[item] {
			prev = slices.IndexFunc(final, func(r ref) bool { return r.item == item })
			continue
		}
		if ignoredL[pos] {
			continue
		}
		if pos > lastPaired {
			prev = len(final)
		} else {
			prev++
		}
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
				pid = tr.Mappings[prov].ID
			}
			changes = append(changes, Change{Action: "remove", Pos: i, IID: it.IID, CID: it.CID, ProviderID: pid, Title: t, Artists: a, Reason: "平台已移除"})
		}
	}
	for pos, t := range L {
		if pairedL[pos] < 0 && !ignoredL[pos] {
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
	res.Snapshot = Snapshot{ID: in.Live.ID, Name: in.Live.Name, Items: ids, CIDs: lcid}
	return res, nil
}

// Observe(spec §6.5.1 規則 1、3):L 的每首用身分函式(決策 19:mapping → ISRC alias → 公式 → 墓碑)算 cid,
// 並把觀測寫回 track(新建、補 mapping、記衝突)。純函式:不改 tracks,回傳要寫回的那些。pull 的 DERIVE 與 push 的 L / L′ 共用(P5 T4)。
func Observe(id *Identity, prov string, tracks map[string]Track, L []provider.Track) (lcid []string, updated map[string]Track) {
	lcid = make([]string, len(L))
	updated = map[string]Track{}
	for i, t := range L {
		cid := id.Resolve(prov, t.ProviderID, t.ISRC)
		lcid[i] = cid
		tr, ok := updated[cid]
		if !ok {
			tr, ok = tracks[cid]
		}
		if !ok {
			nt := NewTrack(prov, t)
			nt.CID = cid // Resolve 可能經墓碑指到 tracks 裡已經沒有的勝者(手改過的檔):map key 與 cid 欄位必須一致,不然經 db 來回位元組會變
			updated[cid] = nt
			continue
		}
		cp := cloneTrack(tr)
		if changed, conflict := cp.Observe(prov, t); changed || conflict { // 決策 20:isrc / fuzzy 被觀測覆寫也要寫回
			updated[cid] = cp
		}
	}
	return lcid, updated
}

// lcsPairs 找 C(rank 序,cOcc 是 cid → item 索引)與 L(lcid)的最長共同 cid 子序列,回傳兩邊的配對索引(-1 = 沒對上)。
// Hunt–Szymanski:對 L 的每個位置列出 C 裡同 cid 的索引(遞減,免得同一個 L 位置對到兩份),對索引求最長嚴格遞增子序列
// (patience:tails 存每個長度的最小索引,lower_bound 取代)。時間 O((n + r)·log n)、記憶體 O(n + r),r = 同 cid 的配對點數
// (沒有重複曲目時 r ≤ len(L));O(n·m) 的 DP 在萬首清單(Spotify 單一清單上限)要配置 855 MB。多解時偏好 C 較前的 item。
// ponytail: r = Σ(C 裡的份數 × L 裡的次數),整份清單同一首上千份才會爆,不處理。
func lcsPairs(n int, lcid []string, cOcc map[string][]int) (pairedItem, pairedL []int) {
	pairedItem, pairedL = make([]int, n), make([]int, len(lcid))
	for i := range pairedItem {
		pairedItem[i] = -1
	}
	for i := range pairedL {
		pairedL[i] = -1
	}
	type node struct{ item, pos, prev int }
	var nodes []node
	var tails []int // tails[k] = 長度 k+1 的鏈尾(nodes 索引),其 item 最小
	for pos, cid := range lcid {
		co := cOcc[cid]
		for k := len(co) - 1; k >= 0; k-- {
			i := co[k]
			at := sort.Search(len(tails), func(t int) bool { return nodes[tails[t]].item >= i })
			prev := -1
			if at > 0 {
				prev = tails[at-1]
			}
			nodes = append(nodes, node{i, pos, prev})
			if at == len(tails) {
				tails = append(tails, len(nodes)-1)
			} else {
				tails[at] = len(nodes) - 1
			}
		}
	}
	if len(tails) == 0 {
		return pairedItem, pairedL
	}
	for x := tails[len(tails)-1]; x >= 0; x = nodes[x].prev {
		pairedItem[nodes[x].item], pairedL[nodes[x].pos] = nodes[x].pos, nodes[x].item
	}
	return pairedItem, pairedL
}

func cloneTrack(t Track) Track {
	t.ISRC = slices.Clone(t.ISRC)
	t.Artists = slices.Clone(t.Artists)
	t.Mappings = maps.Clone(t.Mappings)
	t.Conflicts = slices.Clone(t.Conflicts)
	return t
}
