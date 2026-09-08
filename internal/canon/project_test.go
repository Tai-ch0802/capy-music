package canon_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func liveItems(ids ...string) []canon.LiveItem {
	out := make([]canon.LiveItem, len(ids))
	for i, id := range ids {
		out[i] = canon.LiveItem{CID: cidOf(id), ProviderID: id}
	}
	return out
}

// idOf:cid → 種子 id(mapping 用);nomap 裡的沒有 mapping。
func mapper(nomap ...string) func(cid string) (string, bool) {
	return func(cid string) (string, bool) {
		id := strings.ToLower(strings.TrimPrefix(cid, "i:TW"))
		id = strings.TrimLeft(id, "0")
		if slices.Contains(nomap, id) {
			return "", false
		}
		return id, true
	}
}

func applyPlan(t *testing.T, live []string, ops []provider.PlaylistOp) ([]string, string) {
	t.Helper()
	got, name, err := provider.ApplyPlaylistOps(live, ops)
	if err != nil {
		t.Fatalf("ops 套不上去:%v\n%+v", err, ops)
	}
	return got, name
}

func kinds(ops []provider.PlaylistOp) string {
	var out []string
	for _, op := range ops {
		out = append(out, op.Kind)
	}
	return strings.Join(out, " ")
}

func TestPushPlanTable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		live      []string
		want      []string
		nomap     []string
		liveName  string
		wantName  string
		expect    []string // 套完 ops 後的平台序列
		expectOps string   // Kind 序列(空 = 無)
		skipped   int
		rename    string
	}{
		{name: "一樣 = 無事", live: []string{"a", "b", "c"}, want: []string{"a", "b", "c"}, expect: []string{"a", "b", "c"}},
		{name: "只重排", live: []string{"a", "b", "c"}, want: []string{"c", "a", "b"}, expect: []string{"c", "a", "b"}, expectOps: "move"},
		{name: "移除", live: []string{"a", "b", "c"}, want: []string{"a", "c"}, expect: []string{"a", "c"}, expectOps: "remove"},
		{name: "新增在中間", live: []string{"a", "b"}, want: []string{"a", "x", "b"}, expect: []string{"a", "x", "b"}, expectOps: "add"},
		{name: "混合", live: []string{"a", "b", "c"}, want: []string{"b", "a", "d"}, expect: []string{"b", "a", "d"}, expectOps: "remove move add"},
		{name: "C 有、平台沒有、推不出去 = skip", live: []string{"a"}, want: []string{"a", "y"}, nomap: []string{"y"}, expect: []string{"a"}, skipped: 1},
		{name: "釘成不可得但曲目在平台上 = 配對、不刪", live: []string{"a", "b"}, want: []string{"a", "b"}, nomap: []string{"b"}, expect: []string{"a", "b"}},
		{name: "重複曲目照算(move 不求最少)", live: []string{"a", "a", "b"}, want: []string{"a", "b", "a"}, expect: []string{"a", "b", "a"}, expectOps: "move move"},
		{name: "清空", live: []string{"a", "b"}, want: nil, expect: []string{}, expectOps: "remove remove"},
		{name: "從空清單開始", live: nil, want: []string{"a", "b"}, expect: []string{"a", "b"}, expectOps: "add add"},
		{name: "改名在最後", live: []string{"a"}, want: []string{"a"}, liveName: "舊", wantName: "新", expect: []string{"a"}, expectOps: "rename", rename: "新"},
		{name: "wantName 空 = 不改名", live: []string{"a"}, want: []string{"a"}, liveName: "舊", wantName: "", expect: []string{"a"}},
	} {
		pl, _ := world(t, tc.want...)
		ops, skipped := canon.PushPlan(liveItems(tc.live...), pl.Items, tc.liveName, tc.wantName, mapper(tc.nomap...))
		got, name := applyPlan(t, tc.live, ops)
		if !slices.Equal(got, tc.expect) || kinds(ops) != tc.expectOps || len(skipped) != tc.skipped || name != tc.rename {
			t.Fatalf("%s:得 %v ops=%q skipped=%d name=%q\n%+v", tc.name, got, kinds(ops), len(skipped), name, ops)
		}
	}
}

// 性質:任意 live / want(含重複、含推不出去的),套完 ops = want 去掉 skipped;remove 由後往前、add 遞增(位置才不互相影響)。
func TestPushPlanProperty(t *testing.T) {
	letters := []string{"a", "b", "c", "d", "e", "f"}
	seed := rand.Uint64()
	r := rand.New(rand.NewPCG(seed, 7))
	pick := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = letters[r.IntN(len(letters))]
		}
		return out
	}
	for i := 0; i < 1500; i++ {
		live, want := pick(r.IntN(7)), pick(r.IntN(7))
		var nomap []string
		if r.IntN(3) == 0 {
			nomap = []string{letters[r.IntN(len(letters))]}
		}
		_ = canon.Reordered(toCIDs(live), toCIDs(want)) // 「LCS > 共同元素」的 panic 不可能:任意序列對都不會觸發
		pl, _ := world(t, want...)
		ops, skipped := canon.PushPlan(liveItems(live...), pl.Items, "n", "n", mapper(nomap...))
		// 期望:結果是 want 的子序列(順序不變),有 mapping 的 id 全部都在;沒 mapping 的 id 只能靠配對留下,
		// 留幾份 = min(live 出現, want 出現)(哪幾份由 LCS 決定,不釘)。
		got, _ := applyPlan(t, live, ops)
		lc, wc, gc := map[string]int{}, map[string]int{}, map[string]int{}
		for _, id := range live {
			lc[id]++
		}
		for _, id := range want {
			wc[id]++
		}
		for _, id := range got {
			gc[id]++
		}
		nskip := 0
		for id, w := range wc {
			keep := w
			if slices.Contains(nomap, id) {
				keep = min(lc[id], w)
				nskip += w - keep
			}
			if gc[id] != keep {
				t.Fatalf("seed %d #%d live=%v want=%v nomap=%v:%s 要 %d 份,得 %v\n%+v", seed, i, live, want, nomap, id, keep, got, ops)
			}
		}
		if !isSubsequence(got, want) || len(skipped) != nskip {
			t.Fatalf("seed %d #%d live=%v want=%v nomap=%v:得 %v(要是 want 的子序列)skipped=%d 要 %d\n%+v", seed, i, live, want, nomap, got, len(skipped), nskip, ops)
		}
		// 再算一次是空的(冪等)
		if again, _ := canon.PushPlan(liveItems(got...), pl.Items, "n", "n", mapper(nomap...)); len(again) != 0 {
			t.Fatalf("seed %d #%d 套完再算要無事:%+v", seed, i, again)
		}
	}
}

func TestReordered(t *testing.T) {
	c := func(ids ...string) []string {
		out := make([]string, len(ids))
		for i, id := range ids {
			out[i] = cidOf(id)
		}
		return out
	}
	for _, tc := range []struct {
		name       string
		base, live []string
		want       bool
	}{
		{"一樣", []string{"a", "b", "c"}, []string{"a", "b", "c"}, false},
		{"只新增", []string{"a", "b", "c"}, []string{"a", "b", "c", "d"}, false},
		{"只移除", []string{"a", "b", "c"}, []string{"a", "c"}, false},
		{"新增在中間", []string{"a", "c"}, []string{"a", "b", "c"}, false},
		{"重排", []string{"a", "b", "c"}, []string{"b", "a", "c"}, true},
		{"重排 + 新增", []string{"a", "b", "c"}, []string{"c", "d", "a", "b"}, true},
		{"重複曲目換位", []string{"a", "a", "b"}, []string{"a", "b", "a"}, true},
		{"重複曲目少一份不算重排", []string{"a", "a", "b"}, []string{"a", "b"}, false},
		{"刪前面那份還是移後面那份分不出來 = 不算重排", []string{"a", "b", "a"}, []string{"b", "a"}, false},
		{"空", nil, []string{"a"}, false},
	} {
		if got := canon.Reordered(c(tc.base...), c(tc.live...)); got != tc.want {
			t.Fatalf("%s:得 %v", tc.name, got)
		}
	}
	_ = fmt.Sprint
}

func isSubsequence(sub, of []string) bool {
	j := 0
	for _, x := range of {
		if j < len(sub) && sub[j] == x {
			j++
		}
	}
	return j == len(sub)
}
