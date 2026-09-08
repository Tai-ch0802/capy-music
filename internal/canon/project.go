package canon

// pl push 的純函式核心(spec §6.5.2、附錄 C 決策 28;P5 T3):把 canonical 清單 C 投影到某個 provider,算出要對平台清單 L 做的 ops。
// 對齊鍵是 cid(同 §6.5.1 規則 1),配對上的 item 不動——所以「pinned 成不可得、但曲目此刻就在平台上」與 Apple 的
// library-only 曲目都會配對、不會被當成 remove;純 skip 只剩「C 有、平台沒有、又推不出去」。
// ops 的位置語意由 provider.ApplyPlaylistOps 唯一定義(依序套用),這裡的性質測試也拿它驗「套完 = want」。

import (
	"slices"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// LiveItem:平台清單 L 的一首,cid 由 OBSERVE 依身分規則算好。推不出去的曲目(Spotify local file、Apple library-only)
// 在 L 裡就配對、不動;要不要因為它們拒絕整個清單的 push 是呼叫端的事(計畫 Q22),這裡不需要知道。
type LiveItem struct {
	CID        string
	ProviderID string
}

// Skip:C 的 item 在平台上沒有、又推不出去(沒有 mapping、pinned 不可得、Apple 只有 library id)。
type Skip struct {
	IID, CID string
	Reason   string
}

// PushPlan:diff(L 的 cid 序列, C 的 cid 序列)→ ops。mappingID 給 cid 在這個 provider 的**可推** id——沒有 mapping、釘成不可得、
// 或 id 推不出去(Spotify local file 的 mapping 存的是 `spotify:local:…` uri、Apple 的 library-only id)都必須回 false,
// 不然那首會變成 add 而讓 provider 整批拒收;這裡不認得各平台的 id 形狀,判斷交給呼叫端(provider.PlaylistWriter.Pushable)。
// 回傳的 ops 依序套在 L 的 provider id 序列上等於「C 的順序、去掉 skipped、id 取 mapping(配對上的沿用 L 原文)」;
// wantName ≠ liveName 時多一個 rename。remove 依 L 位置由後往前、move 用最簡單的逐位選擇、add 依目標位置遞增。
// ponytail: move 不求最少(每個錯位的 item 一次 move),Spotify 反正整批取代;真要最少 move 再用 LCS 的補集。
func PushPlan(live []LiveItem, want []Item, liveName, wantName string, mappingID func(cid string) (string, bool)) (ops []provider.PlaylistOp, skipped []Skip) {
	lcid := make([]string, len(live))
	for i, it := range live {
		lcid[i] = it.CID
	}
	cOcc := map[string][]int{}
	for i, it := range want {
		cOcc[it.CID] = append(cOcc[it.CID], i)
	}
	pairedW, pairedL := lcsPairs(len(want), lcid, cOcc)
	// LCS 沒對上的:依 L 順序拿同 cid 剩下的 want item 配對(同 §6.5.1 規則 2),沒有剩下的才是新增 / 移除
	cursor := map[string]int{}
	for pos, cid := range lcid {
		if pairedL[pos] >= 0 {
			continue
		}
		co, k := cOcc[cid], cursor[cid]
		for k < len(co) && pairedW[co[k]] >= 0 {
			k++
		}
		cursor[cid] = k
		if k < len(co) {
			pairedL[pos], pairedW[co[k]] = co[k], pos
		}
	}

	// 目標序列:want 的順序;配對上的沿用 L 的 provider id 原文,沒配對的要 mapping,沒有就 skip
	type slot struct {
		id  string
		src int // 配對到的 L 位置;-1 = 新增
	}
	var target []slot
	for i, it := range want {
		if pos := pairedW[i]; pos >= 0 {
			target = append(target, slot{live[pos].ProviderID, pos})
			continue
		}
		id, ok := mappingID(it.CID)
		if !ok {
			skipped = append(skipped, Skip{IID: it.IID, CID: it.CID, Reason: "沒有這個平台的 mapping(或釘成不可得):capy resolve"})
			continue
		}
		target = append(target, slot{id, -1})
	}

	// 1. remove:L 裡沒配對的,由後往前(位置才不會互相影響)
	cur := make([]int, 0, len(live)) // 工作序列:存 L 位置(-1 之後不會出現,新增在最後才插)
	for pos := range live {
		if pairedL[pos] >= 0 {
			cur = append(cur, pos)
		}
	}
	for pos := len(live) - 1; pos >= 0; pos-- {
		if pairedL[pos] < 0 {
			ops = append(ops, provider.PlaylistOp{Kind: provider.OpRemove, Pos: pos, ProviderID: live[pos].ProviderID})
		}
	}
	// 2. move:配對上的 item 排成目標順序——逐位選擇,cur[t] 不是目標就把目標從後面搬過來
	var order []int // 目標裡配對上的 L 位置,依 want 順序
	for _, s := range target {
		if s.src >= 0 {
			order = append(order, s.src)
		}
	}
	for t, wantPos := range order {
		if cur[t] == wantPos {
			continue
		}
		from := slices.Index(cur[t:], wantPos) + t
		ops = append(ops, provider.PlaylistOp{Kind: provider.OpMove, From: from, Pos: t})
		cur = slices.Insert(slices.Delete(cur, from, from+1), t, wantPos)
	}
	// 3. add:新增依目標位置遞增插入(前面的位置都已是最終狀態)
	for i, s := range target {
		if s.src < 0 {
			ops = append(ops, provider.PlaylistOp{Kind: provider.OpAdd, Pos: i, ProviderID: s.id})
		}
	}
	if wantName != "" && wantName != liveName {
		ops = append(ops, provider.PlaylistOp{Kind: provider.OpRename, Name: wantName})
	}
	return ops, skipped
}

// Reordered:規則 6′ 的判斷——base 的 cid 序列拿掉 L 沒有的、L 的拿掉 base 沒有的,兩者相同就是平台沒重排。
// 重複曲目以出現序配對:LCS 長度 = 共同元素數(Σ min(b, l))即沒重排。所以「重複曲目少一份」看起來像沒重排
// (base [a, b, a] → L [b, a] 是刪了前面那份還是移了後面那份,分不出來;決策 26 已接受的那種平手)。
func Reordered(base, live []string) bool {
	bCnt, lCnt := map[string]int{}, map[string]int{}
	for _, c := range base {
		bCnt[c]++
	}
	for _, c := range live {
		lCnt[c]++
	}
	common := 0
	for c, b := range bCnt {
		common += min(b, lCnt[c])
	}
	bOcc := map[string][]int{}
	for i, c := range base {
		bOcc[c] = append(bOcc[c], i)
	}
	_, pairedL := lcsPairs(len(base), live, bOcc)
	n := 0
	for _, p := range pairedL {
		if p >= 0 {
			n++
		}
	}
	// n > common 依構造不可能(每個 cid 的配對數 ≤ min(b, l),加總 ≤ Σ min);真的發生也只當「沒重排」(保留 C 順序),
	// 這是 pull 的主路徑,不 panic。性質測試對任意序列對都呼叫過。
	return n < common
}
