package canon_test

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/canon"
)

func TestDuplicatesFixedExample(t *testing.T) {
	got := canon.Duplicates([]string{"a", "b", "a", "", "", "c", "b", "a"})
	want := []canon.Dup{{Pos: 2, Keep: 0}, {Pos: 6, Keep: 1}, {Pos: 7, Keep: 0}}
	if !slices.Equal(got, want) {
		t.Fatalf("重複的位置:%v", got)
	}
	if got := canon.Duplicates(nil); len(got) != 0 {
		t.Fatalf("空序列沒有重複:%v", got)
	}
}

// 順序硬約束的性質:拿掉 Duplicates 指出的位置之後,(1) 沒有重複;(2) 剩下的是原序列的子序列(相對順序不變);
// (3) 每個鍵保留的是第一次出現的那份;(4) 空鍵一份都不動。位置遞增、Keep 一定在 Pos 前面且同鍵。
func TestDuplicatesKeepFirstAndOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(2026, 915))
	for n := 0; n < 500; n++ {
		keys := make([]string, r.IntN(40))
		for i := range keys {
			if r.IntN(6) == 0 {
				continue // 空鍵
			}
			keys[i] = string(rune('a' + r.IntN(5)))
		}
		dups := canon.Duplicates(keys)
		drop := map[int]bool{}
		for i, d := range dups {
			if d.Keep >= d.Pos || keys[d.Keep] != keys[d.Pos] || keys[d.Pos] == "" || (i > 0 && d.Pos <= dups[i-1].Pos) {
				t.Fatalf("%v:第 %d 筆 %+v 不合法", keys, i, d)
			}
			if slices.Index(keys, keys[d.Keep]) != d.Keep {
				t.Fatalf("%v:Keep %d 不是第一次出現", keys, d.Keep)
			}
			drop[d.Pos] = true
		}
		var kept []string
		seen := map[string]bool{}
		for i, k := range keys {
			if drop[i] {
				continue
			}
			kept = append(kept, k)
			if k != "" && seen[k] {
				t.Fatalf("%v:拿掉 %v 之後 %q 還是重複", keys, dups, k)
			}
			seen[k] = true
		}
		for _, k := range keys { // 每個非空鍵都還在(保留第一份),空鍵全部都在
			if !slices.Contains(kept, k) {
				t.Fatalf("%v:%q 整個不見了", keys, k)
			}
		}
		if len(kept) != len(keys)-len(dups) || !isSubsequence(kept, keys) { // isSubsequence 在 project_test.go
			t.Fatalf("%v:剩下的 %v 不是原序列的子序列", keys, kept)
		}
	}
}
