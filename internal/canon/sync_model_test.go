package canon_test

// P5 決策 27 的兩條反例(固定測試)與多平台 / 多裝置的 pull-push 模型測試。
// oracle 是意圖不是收斂:預期的最終狀態 = 初始狀態依序套上每個平台操作(一輪只動一個平台,所以操作不衝突);
// 「三方相等」本身不夠——重排被抵銷的那條軌跡三方也會相等(都錯成舊順序),復活也一樣。

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 規則 4′:使用者在 Spotify 刪了 b、Spotify pull 已從 C 移除;Apple 的 L 還有 b、base 也有 b → 不加回來、零變更。
func TestDeriveRule4PrimeDoesNotResurrectDeletion(t *testing.T) {
	pl, tracks := world(t, "a", "c")
	res := mustDerive(t, canon.DeriveInput{Provider: "apple", Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b", "c"), Live: live("通勤", "a", "b", "c")})
	if len(res.Changes) != 0 || !slices.Equal(cids(res.Playlist), cids(pl)) {
		t.Fatalf("Apple 還沒跟上 C 的移除,不是新增:%s %v", actions(res.Changes), cids(res.Playlist))
	}
	// 平台真的新增(超出 base 計數)才是新增:d 是新的、b 不是
	res = mustDerive(t, canon.DeriveInput{Provider: "apple", Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b", "c"), Live: live("通勤", "a", "b", "c", "d")})
	if actions(res.Changes) != "add:"+cidOf("d") || !slices.Equal(cids(res.Playlist), []string{cidOf("a"), cidOf("c"), cidOf("d")}) {
		t.Fatalf("只有 d:%s %v", actions(res.Changes), cids(res.Playlist))
	}
	// 重複計數:base 兩份、L 三份、C 一份 → 只加一份(取 L 順序最後的)
	pl1, tracks1 := world(t, "x")
	res = mustDerive(t, canon.DeriveInput{Provider: "apple", Playlist: pl1, Tracks: tracks1, Base: snap("通勤", "x", "x"), Live: live("通勤", "x", "x", "x")})
	if actions(res.Changes) != "add:"+cidOf("x") || len(res.Playlist.Items) != 2 {
		t.Fatalf("l − b = 1:%s %d", actions(res.Changes), len(res.Playlist.Items))
	}
	// 取 L 順序最後的那幾個:x 在 base 1 份、L 2 份、C 0 份 → 加 1 份,是 L 尾端那份(位置 2),不是開頭那份
	pl2, tracks2 := world(t, "a")
	res = mustDerive(t, canon.DeriveInput{Provider: "apple", Playlist: pl2, Tracks: tracks2, Base: snap("通勤", "x", "a"), Live: live("通勤", "x", "a", "x")})
	if len(res.Changes) != 1 || res.Changes[0].Pos != 2 || !slices.Equal(cids(res.Playlist), []string{cidOf("a"), cidOf("x")}) {
		t.Fatalf("取最後的那份:%+v %v", res.Changes, cids(res.Playlist))
	}
	// 沒有 base = bootstrap,全部新增(不變)
	res = mustDerive(t, canon.DeriveInput{Provider: "apple", Playlist: pl, Tracks: tracks, Live: live("通勤", "a", "b", "c")})
	if actions(res.Changes) != "add:"+cidOf("b") {
		t.Fatalf("bootstrap:%s", actions(res.Changes))
	}
}

// 規則 6′:Spotify 重排後 C = [c, a, b];Apple 的 L = base = [a, b, c](沒動)→ C 的順序不動、不報 move。
func TestDeriveRule6PrimeKeepsCOrderWhenPlatformDidNotReorder(t *testing.T) {
	pl, tracks := world(t, "c", "a", "b")
	res := mustDerive(t, canon.DeriveInput{Provider: "apple", Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b", "c"), Live: live("通勤", "a", "b", "c")})
	if len(res.Changes) != 0 || !slices.Equal(cids(res.Playlist), cids(pl)) {
		t.Fatalf("平台沒重排就不動 C:%s %v", actions(res.Changes), cids(res.Playlist))
	}
	// 平台真的重排 → 採 L 的順序(後 pull 者勝)
	res = mustDerive(t, canon.DeriveInput{Provider: "apple", Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b", "c"), Live: live("通勤", "b", "a", "c")})
	if !slices.Equal(cids(res.Playlist), []string{cidOf("b"), cidOf("a"), cidOf("c")}) || len(res.Changes) == 0 {
		t.Fatalf("平台重排 → L 順序:%s %v", actions(res.Changes), cids(res.Playlist))
	}
	// 沒重排但有新增:中間的新增插在它在 L 的前一個元素所配對的 C 位置之後,其餘順序不動
	res = mustDerive(t, canon.DeriveInput{Provider: "apple", Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b", "c"), Live: live("通勤", "a", "d", "b", "c")})
	if actions(res.Changes) != "add:"+cidOf("d") || !slices.Equal(cids(res.Playlist), []string{cidOf("c"), cidOf("a"), cidOf("d"), cidOf("b")}) {
		t.Fatalf("沒重排 + 新增:%s %v", actions(res.Changes), cids(res.Playlist))
	}
	// L 尾端的新增接在 C 的尾端(不是插在 c 之後 = C 的第 2 位);開頭的新增在 C 的開頭
	res = mustDerive(t, canon.DeriveInput{Provider: "apple", Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b", "c"), Live: live("通勤", "a", "b", "c", "d", "e")})
	if !slices.Equal(cids(res.Playlist), []string{cidOf("c"), cidOf("a"), cidOf("b"), cidOf("d"), cidOf("e")}) {
		t.Fatalf("尾端新增接 C 尾端:%v", cids(res.Playlist))
	}
	res = mustDerive(t, canon.DeriveInput{Provider: "apple", Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b", "c"), Live: live("通勤", "d", "a", "b", "c")})
	if !slices.Equal(cids(res.Playlist), []string{cidOf("d"), cidOf("c"), cidOf("a"), cidOf("b")}) {
		t.Fatalf("開頭新增在 C 開頭:%v", cids(res.Playlist))
	}
	// 沒有 base → 維持原行為(採 L 順序)
	res = mustDerive(t, canon.DeriveInput{Provider: "apple", Playlist: pl, Tracks: tracks, Live: live("通勤", "a", "b", "c")})
	if !slices.Equal(cids(res.Playlist), []string{cidOf("a"), cidOf("b"), cidOf("c")}) {
		t.Fatalf("無 base 採 L 順序:%v", cids(res.Playlist))
	}
}

// ---- 模型:兩個平台、一個 canonical、per-platform base;pull 用 Derive、push 用 PushPlan + ApplyPlaylistOps ----

type platform struct {
	name string
	ids  []string
}

type model struct {
	t      *testing.T
	pl     canon.Playlist
	tracks map[string]canon.Track
	base   map[string]*canon.Snapshot
	plats  map[string]*platform
}

func newModel(t *testing.T, initial ...string) *model {
	pl := canon.NewPlaylist("通勤")
	m := &model{t: t, pl: *pl, tracks: map[string]canon.Track{}, base: map[string]*canon.Snapshot{}, plats: map[string]*platform{}}
	for _, p := range []string{"spotify", "apple"} {
		m.plats[p] = &platform{name: p, ids: slices.Clone(initial)}
	}
	return m
}

func (m *model) pull(p string) []canon.Change {
	pf := m.plats[p]
	res := mustDerive(m.t, canon.DeriveInput{Provider: p, Playlist: m.pl, Tracks: m.tracks, Base: m.base[p], Live: live("通勤", pf.ids...)})
	m.pl = res.Playlist
	for cid, tr := range res.Tracks {
		m.tracks[cid] = tr
	}
	s := res.Snapshot
	m.base[p] = &s
	return res.Changes
}

// resolve:把缺 mapping 的補上(id 在兩個平台相同,resolver 一定對得到)。
func (m *model) resolve(p string) {
	for _, it := range m.pl.Items {
		tr := m.tracks[it.CID]
		if tr.Mappings[p].ID == "" {
			if tr.Mappings == nil {
				tr.Mappings = map[string]canon.Mapping{}
			}
			tr.Mappings[p] = canon.Mapping{ID: idOfCID(it.CID), Confidence: 95, Source: canon.SourceISRC}
			m.tracks[it.CID] = tr
		}
	}
}

func idOfCID(cid string) string {
	for _, id := range alphabet {
		if cidOf(id) == cid {
			return id
		}
	}
	panic("未知 cid " + cid)
}

var alphabet = []string{"a", "b", "c", "d", "e", "f", "g", "h"}

func (m *model) push(p string) []provider.PlaylistOp {
	pf := m.plats[p]
	if b := m.base[p]; b == nil || !slices.Equal(b.Items, pf.ids) {
		m.t.Fatalf("push %s 的前提:base 要等於平台現況(先 pull)", p)
	}
	m.resolve(p)
	ops, skipped := canon.PushPlan(liveItems(pf.ids...), m.pl.Items, "通勤", "通勤", func(cid string) (string, bool) {
		id := m.tracks[cid].Mappings[p].ID
		return id, id != ""
	})
	if len(skipped) != 0 {
		m.t.Fatalf("模型裡 resolve 過了,不該有 skip:%+v", skipped)
	}
	got, _, err := provider.ApplyPlaylistOps(pf.ids, ops)
	if err != nil {
		m.t.Fatalf("push %s:%v", p, err)
	}
	pf.ids = got
	// VERIFY:重讀 L′、base := L′(規則 7);Observe 補 mapping 走 pull 同一條路——這裡直接記快照
	m.base[p] = snap("通勤", got...)
	return ops
}

// sync:先 pull 兩邊再 push 兩邊;順序由呼叫端給——pull 的順序決定 6′ 有沒有真的被執行(先 pull 沒動的那邊時 6′ 是空操作),
// 所以模型測試每輪隨機、固定測試兩種順序都跑。
func (m *model) sync(order ...string) (changes int) {
	if len(order) == 0 {
		order = []string{"apple", "spotify"}
	}
	for _, p := range order {
		changes += len(m.pull(p))
	}
	for _, p := range order {
		changes += len(m.push(p))
	}
	return changes
}

func shuffled(r *rand.Rand) []string {
	if r.IntN(2) == 0 {
		return []string{"spotify", "apple"}
	}
	return []string{"apple", "spotify"}
}

func (m *model) cids() []string { return cids(m.pl) }

func toCIDs(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = cidOf(id)
	}
	return out
}

// 一輪只動一個平台:add(新 id 或重複)、remove、move,同步套到 expected(意圖)。
func mutate(r *rand.Rand, ids []string, expected *[]string) []string {
	ids = slices.Clone(ids)
	switch k := r.IntN(3); {
	case k == 0 || len(ids) == 0: // add
		id := alphabet[r.IntN(len(alphabet))]
		pos := r.IntN(len(ids) + 1)
		ids = slices.Insert(ids, pos, id)
		*expected = slices.Insert(*expected, pos, id)
	case k == 1: // remove
		pos := r.IntN(len(ids))
		ids = slices.Delete(ids, pos, pos+1)
		*expected = slices.Delete(*expected, pos, pos+1)
	default: // move
		from, to := r.IntN(len(ids)), r.IntN(len(ids))
		id := ids[from]
		ids = slices.Insert(slices.Delete(ids, from, from+1), to, id)
		*expected = slices.Insert(slices.Delete(*expected, from, from+1), to, id)
	}
	return ids
}

// 模型測試:每輪隨機挑一個平台做 1–3 個操作,sync 一輪後 C 與兩個平台都等於意圖;再 sync 一輪零變更。
func TestSyncModelMatchesIntent(t *testing.T) {
	seed := rand.Uint64()
	r := rand.New(rand.NewPCG(seed, 11))
	for round := 0; round < 200; round++ {
		m := newModel(t, "a", "b", "c")
		expected := []string{"a", "b", "c"}
		if m.sync(); !slices.Equal(m.cids(), toCIDs(expected)) { // bootstrap
			t.Fatalf("seed %d bootstrap:%v", seed, m.cids())
		}
		for step := 0; step < 6; step++ {
			p := []string{"spotify", "apple"}[r.IntN(2)]
			for n := 1 + r.IntN(3); n > 0; n-- {
				m.plats[p].ids = mutate(r, m.plats[p].ids, &expected)
			}
			m.sync(shuffled(r)...)
			for _, q := range []string{"spotify", "apple"} {
				if !slices.Equal(m.plats[q].ids, expected) {
					t.Fatalf("seed %d round %d step %d(動 %s):%s = %v,意圖 %v,C = %v", seed, round, step, p, q, m.plats[q].ids, expected, m.cids())
				}
			}
			if !slices.Equal(m.cids(), toCIDs(expected)) {
				t.Fatalf("seed %d round %d step %d:C = %v,意圖 %v", seed, round, step, m.cids(), expected)
			}
			if n := m.sync(shuffled(r)...); n != 0 {
				t.Fatalf("seed %d round %d step %d:第二輪 sync 要零變更,得 %d", seed, round, step, n)
			}
		}
	}
}

// 決策 27 的兩條軌跡走完整 sync:Spotify 刪一首 / 重排一次 / 尾端加一首 → Apple 拿到、Spotify 不被改回。
// 兩種 pull 順序都跑:先 pull Spotify 再 pull Apple 才是 6′ 真的在擋的那條(Apple 的 L = base 沒動,C 已經是新順序)。
func TestSyncModelDeleteAndReorderPropagate(t *testing.T) {
	for _, order := range [][]string{{"spotify", "apple"}, {"apple", "spotify"}} {
		m := newModel(t, "a", "b", "c")
		m.sync(order...)
		m.plats["spotify"].ids = []string{"a", "c"} // 刪 b
		m.sync(order...)
		if !slices.Equal(m.plats["apple"].ids, []string{"a", "c"}) || !slices.Equal(m.plats["spotify"].ids, []string{"a", "c"}) || !slices.Equal(m.cids(), toCIDs([]string{"a", "c"})) {
			t.Fatalf("%v 刪除要傳到 Apple、不復活:%v %v %v", order, m.plats["spotify"].ids, m.plats["apple"].ids, m.cids())
		}
		m.plats["spotify"].ids = []string{"c", "a"} // 重排
		m.sync(order...)
		if !slices.Equal(m.plats["apple"].ids, []string{"c", "a"}) || !slices.Equal(m.plats["spotify"].ids, []string{"c", "a"}) {
			t.Fatalf("%v 重排要傳到 Apple、Spotify 不被改回:%v %v", order, m.plats["spotify"].ids, m.plats["apple"].ids)
		}
		m.plats["spotify"].ids = []string{"c", "a", "d"} // 尾端加 d:Apple 也要在尾端
		m.sync(order...)
		if !slices.Equal(m.plats["apple"].ids, []string{"c", "a", "d"}) || !slices.Equal(m.cids(), toCIDs([]string{"c", "a", "d"})) {
			t.Fatalf("%v 尾端新增:%v %v", order, m.plats["apple"].ids, m.cids())
		}
		// 同一輪兩邊都動:Spotify 重排、Apple 尾端加 e——先 pull Spotify 時 C 已是新順序,Apple 的 e 要接尾端(4′ 的尾端規則),不能插在中間
		m.plats["spotify"].ids = []string{"d", "c", "a"}
		m.plats["apple"].ids = []string{"c", "a", "d", "e"}
		m.sync(order...)
		if !slices.Equal(m.plats["apple"].ids, []string{"d", "c", "a", "e"}) || !slices.Equal(m.plats["spotify"].ids, []string{"d", "c", "a", "e"}) {
			t.Fatalf("%v 重排 + 另一邊尾端新增:%v %v %v", order, m.plats["spotify"].ids, m.plats["apple"].ids, m.cids())
		}
		if n := m.sync(order...); n != 0 {
			t.Fatalf("%v 再 sync 零變更,得 %d", order, n)
		}
	}
	_ = fmt.Sprint
}
