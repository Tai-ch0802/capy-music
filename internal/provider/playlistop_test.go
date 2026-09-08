package provider

import (
	"slices"
	"strings"
	"testing"
)

// 位置語意:依序套用、每個位置指的是前面 ops 套完後的狀態;move 的 Pos 指拿出來之後的序列。
func TestApplyPlaylistOpsSequentialPositions(t *testing.T) {
	cur := []string{"a", "b", "c"}
	for _, tc := range []struct {
		name string
		ops  []PlaylistOp
		want []string
		nm   string
		err  string
	}{
		{"空 ops = 原樣", nil, []string{"a", "b", "c"}, "", ""},
		{"add 頭尾", []PlaylistOp{{Kind: OpAdd, ProviderID: "x", Pos: 0}, {Kind: OpAdd, ProviderID: "y", Pos: 4}}, []string{"x", "a", "b", "c", "y"}, "", ""},
		{"remove 之後的位置是縮短後的", []PlaylistOp{{Kind: OpRemove, Pos: 0, ProviderID: "a"}, {Kind: OpRemove, Pos: 1}}, []string{"b"}, "", ""},
		{"move 到尾端", []PlaylistOp{{Kind: OpMove, From: 0, Pos: 2}}, []string{"b", "c", "a"}, "", ""},
		{"move 到頭", []PlaylistOp{{Kind: OpMove, From: 2, Pos: 0}}, []string{"c", "a", "b"}, "", ""},
		{"rename 不動 items", []PlaylistOp{{Kind: OpRename, Name: "通勤 2026"}}, []string{"a", "b", "c"}, "通勤 2026", ""},
		{"清空", []PlaylistOp{{Kind: OpRemove, Pos: 2}, {Kind: OpRemove, Pos: 1}, {Kind: OpRemove, Pos: 0}}, []string{}, "", ""},
		{"add 越界", []PlaylistOp{{Kind: OpAdd, ProviderID: "x", Pos: 4}}, nil, "", "add 位置 4 越界"},
		{"remove 越界", []PlaylistOp{{Kind: OpRemove, Pos: 3}}, nil, "", "remove 位置 3 越界"},
		{"remove 核對不符", []PlaylistOp{{Kind: OpRemove, Pos: 0, ProviderID: "b"}}, nil, "", "是 a 不是 b"},
		{"move 越界", []PlaylistOp{{Kind: OpMove, From: 0, Pos: 3}}, nil, "", "move 0 → 3 越界"},
		{"未知 Kind", []PlaylistOp{{Kind: "swap"}}, nil, "", `未知的 Kind "swap"`},
	} {
		got, nm, err := ApplyPlaylistOps(cur, tc.ops)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("%s:要錯 %q,得 %v", tc.name, tc.err, err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tc.want) || nm != tc.nm {
			t.Fatalf("%s:得 %v %q %v", tc.name, got, nm, err)
		}
	}
	if !slices.Equal(cur, []string{"a", "b", "c"}) {
		t.Fatal("不能改動傳入的 current")
	}
	if got, _, _ := ApplyPlaylistOps(nil, nil); got == nil {
		t.Fatal("nil current 也回非 nil 的空序列(PUT 才會送 [] 不是 null)")
	}
}
