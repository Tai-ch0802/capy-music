package canon_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// twoCids:「同一錄音、兩個 cid」的世界。x 由 spotify 觀測(id z、ISRC …Z)、y 由 apple 觀測(id i.b、ISRC …B),清單 C 各放一個 item。
// 字典序 B < Z,所以合併後勝者是 y、敗者是 x——base 裡記的 spotify 那邊的 cid 正是敗者,合併後的刪除必須靠墓碑重導才刪得掉。
func twoCids(t *testing.T) (tracks *canon.Tracks, pl *canon.Playlist, x, y string) {
	t.Helper()
	pin(t)
	tracks = canon.NewTracks()
	tx := canon.NewTrack(prov, ptrack("z"))
	ty := canon.NewTrack("apple", provider.Track{ProviderID: "i.b", ISRC: "TW000000000B", Title: "song-z", Artists: []string{"artist"}, DurationMS: 200000})
	tracks.Tracks[tx.CID], tracks.Tracks[ty.CID] = tx, ty
	pl = canon.NewPlaylist("通勤")
	for _, cid := range []string{tx.CID, ty.CID} {
		if _, err := pl.Append(cid); err != nil {
			t.Fatal(err)
		}
	}
	return tracks, pl, tx.CID, ty.CID
}

func TestIdentityResolveOrder(t *testing.T) {
	tracks, _, x, y := twoCids(t)
	tr := tracks.Tracks[x]
	tr.Mappings["apple"] = canon.Mapping{ID: "i.pinned", Confidence: 100, Pinned: true, Source: canon.SourceReview}
	tr.Mappings["tidal"] = canon.Mapping{ID: "", Confidence: 100, Pinned: true, Source: canon.SourceReview} // 釘成「不可得」:空 id 不是索引鍵
	tracks.Tracks[x] = tr
	tracks.Merged["i:TW000000000O"] = x // 墓碑:公式算出 …O 的觀測要追到 x
	id := canon.NewIdentity(tracks.Tracks, tracks.Merged)
	for _, c := range []struct{ prov, pid, isrc, want string }{
		{"apple", "i.pinned", "TW000000000B", x},        // (1) mapping 先於 ISRC:即使 ISRC 屬於 y 的 alias set
		{"apple", "i.b", "", y},                         // (1) 沒 ISRC 也靠 mapping
		{prov, "zzz", "TW000000000Z", x},                // (2) alias set
		{prov, "zzz", "tw-00000000-0z", x},              // (2) 正規化後命中
		{prov, "zzz", "TW000000000Q", "i:TW000000000Q"}, // (3) 公式
		{prov, "zzz", "", "p:spotify:zzz"},              // (3) 公式,無 ISRC
		{"tidal", "", "", "p:tidal:"},                   // 釘成不可得的空 id 不會把「空 id」的觀測吸到 x
		{prov, "zzz", "TW000000000O", x},                // (4) 公式結果是墓碑 → 追到勝者
	} {
		if got := id.Resolve(c.prov, c.pid, c.isrc); got != c.want {
			t.Fatalf("Resolve(%s, %q, %q) = %s,要 %s", c.prov, c.pid, c.isrc, got, c.want)
		}
	}
	if len(id.Warnings()) != 0 {
		t.Fatalf("乾淨的 tracks 不該有警告:%v", id.Warnings())
	}
}

// 多重歸屬理論上不發生;真發生就取字典序最小並警告,而且不隨 map 走訪順序抖動。
func TestIdentityMultiHitTakesSmallestCidAndWarns(t *testing.T) {
	pin(t)
	tracks := map[string]canon.Track{}
	for _, cid := range []string{"i:B", "i:A", "i:C"} {
		tracks[cid] = canon.Track{CID: cid, ISRC: []string{"TW000000000D"}, Mappings: map[string]canon.Mapping{prov: {ID: "dup", Confidence: 100, Source: canon.SourceObserved}}}
	}
	first := canon.NewIdentity(tracks, nil)
	if first.Resolve(prov, "dup", "") != "i:A" || first.Resolve(prov, "other", "TW000000000D") != "i:A" {
		t.Fatalf("多命中取字典序最小:%s / %s", first.Resolve(prov, "dup", ""), first.Resolve(prov, "other", "TW000000000D"))
	}
	if w := first.Warnings(); len(w) != 4 || !strings.Contains(w[0], "spotify:dup 同時屬於 i:A 與 i:B,採用 i:A") {
		t.Fatalf("每個多命中一則警告(兩個鍵 × 兩個多出來的 cid):%v", w)
	}
	for n := 0; n < 20; n++ {
		again := canon.NewIdentity(tracks, nil)
		if !slices.Equal(again.Warnings(), first.Warnings()) {
			t.Fatalf("兩次建索引要同解:%v vs %v", again.Warnings(), first.Warnings())
		}
	}
}

func TestRedirectFollowsChainAndSurvivesCycle(t *testing.T) {
	id := canon.NewIdentity(nil, map[string]string{"a": "b", "b": "c", "x": "y", "y": "x", "s": "s"})
	if id.Redirect("a") != "c" || id.Redirect("b") != "c" || id.Redirect("c") != "c" || id.Redirect("q") != "q" || id.Redirect("s") != "s" {
		t.Fatalf("沿鏈追到底、不在表裡就原樣:%s %s %s %s", id.Redirect("a"), id.Redirect("b"), id.Redirect("c"), id.Redirect("q"))
	}
	if got := id.Redirect("x"); got != "x" && got != "y" {
		t.Fatalf("迴圈不卡死:%s", got)
	}
	if canon.NewIdentity(nil, nil).Redirect("a") != "a" {
		t.Fatal("沒有墓碑時原樣")
	}
}

func TestMergeRules(t *testing.T) {
	tracks, pl, x, y := twoCids(t)
	other := canon.NewPlaylist("另一份")
	for _, cid := range []string{y, x} {
		if _, err := other.Append(cid); err != nil {
			t.Fatal(err)
		}
	}
	tx, ty := tracks.Tracks[x], tracks.Tracks[y]
	ty.Mappings[prov] = canon.Mapping{ID: "z2", Confidence: 100, Source: canon.SourceObserved, UpdatedAt: 5}              // 同 provider 不同 id:敗者的 z 進 conflicts
	ty.Mappings["tidal"] = canon.Mapping{ID: "", Confidence: 100, Pinned: true, Source: canon.SourceReview, UpdatedAt: 6} // 勝者釘成不可得、敗者有 id → 進 conflicts、釘選不動
	tx.Mappings["tidal"] = canon.Mapping{ID: "t9", Confidence: 70, Source: canon.SourceFuzzy, UpdatedAt: 7}
	tx.Mappings["youtube"] = canon.Mapping{ID: "yt", Confidence: 100, Source: canon.SourceObserved, UpdatedAt: 8}         // 勝者沒有 → 搬過去
	tx.Conflicts = []canon.Conflict{{Provider: "apple", ProviderID: "i.dup", Title: "song-z (Live)", DurationMS: 250000}} // 敗者的 conflicts 聯集
	tracks.Tracks[x], tracks.Tracks[y] = tx, ty
	tracks.Merged["i:TW000000000W"] = x // 既有鏈 W → x:合併後要壓平成 W → y
	before := pl.UpdatedAt
	fixClock(t, 1_756_700_000)
	s, err := canon.Merge(tracks, map[string]*canon.Playlist{pl.PID: pl, other.PID: other}, x, y)
	if err != nil || s != y {
		t.Fatalf("勝者字典序較小(%s):%s %v", y, s, err)
	}
	if _, ok := tracks.Tracks[x]; ok {
		t.Fatal("敗者要從 tracks 移除")
	}
	got := tracks.Tracks[y]
	if !slices.Equal(got.ISRC, []string{"TW000000000B", "TW000000000Z"}) {
		t.Fatalf("alias set 聯集且排序:%v", got.ISRC)
	}
	if got.Mappings[prov].ID != "z2" || got.Mappings["apple"].ID != "i.b" || got.Mappings["youtube"].ID != "yt" || !got.Mappings["tidal"].Pinned || got.Mappings["tidal"].ID != "" {
		t.Fatalf("mappings:勝者的留著、敗者獨有的搬過來、釘選不動:%+v", got.Mappings)
	}
	want := []canon.Conflict{
		{Provider: prov, ProviderID: "z", Title: "song-z", DurationMS: 200000},
		{Provider: "tidal", ProviderID: "t9", Title: "song-z", DurationMS: 200000},
		{Provider: "apple", ProviderID: "i.dup", Title: "song-z (Live)", DurationMS: 250000},
	}
	if !slices.Equal(got.Conflicts, want) {
		t.Fatalf("同 provider 不同 id 的敗者 id 進 conflicts、敗者原有 conflicts 聯集:%+v", got.Conflicts)
	}
	if tracks.Merged[x] != y || tracks.Merged["i:TW000000000W"] != y || len(tracks.Merged) != 2 {
		t.Fatalf("墓碑 + 壓平既有鏈:%v", tracks.Merged)
	}
	if !slices.Equal(cids(*pl), []string{y, y}) || !slices.Equal(cids(*other), []string{y, y}) {
		t.Fatalf("所有清單的敗者 item 改指勝者:%v %v", cids(*pl), cids(*other))
	}
	if pl.UpdatedAt <= before || pl.UpdatedAt != 1_756_700_000 {
		t.Fatalf("改到的清單 updated_at 前進:%d", pl.UpdatedAt)
	}
	if _, err := canon.Merge(tracks, nil, x, y); err == nil || !strings.Contains(err.Error(), "同一個") {
		t.Fatalf("拿舊 cid 再合併同一對:先追墓碑、發現已是同一個 → 錯誤:%v", err)
	}
	if _, err := canon.Merge(tracks, nil, y, "i:NOPE"); err == nil {
		t.Fatal("未知 cid 要錯")
	}
}

// 同 id 取優先序高的那個(pinned > observed > 其他);敗者是已合併過的舊 cid 也先追墓碑。
func TestMergeSameIDKeepsHigherPrecedenceAndFollowsTombstones(t *testing.T) {
	tracks, _, x, y := twoCids(t)
	tz := canon.NewTrack(prov, ptrack("q")) // 第三個 cid,spotify id 與 x 相同但 pinned
	tz.Mappings[prov] = canon.Mapping{ID: "z", Confidence: 100, Pinned: true, Source: canon.SourceReview, UpdatedAt: 9}
	tracks.Tracks[tz.CID] = tz
	if _, err := canon.Merge(tracks, nil, x, y); err != nil {
		t.Fatal(err)
	}
	s, err := canon.Merge(tracks, nil, x, tz.CID) // x 已是墓碑 → 追到 y 再合併
	if err != nil || s != y {
		t.Fatalf("追墓碑後合併:%s %v", s, err)
	}
	if m := tracks.Tracks[y].Mappings[prov]; !m.Pinned || m.ID != "z" || len(tracks.Tracks[y].Conflicts) != 0 {
		t.Fatalf("同 id 取 pinned、不記衝突:%+v %+v", m, tracks.Tracks[y].Conflicts)
	}
	if tracks.Merged[x] != y || tracks.Merged[tz.CID] != y {
		t.Fatalf("兩個敗者都指向 y:%v", tracks.Merged)
	}
}

func TestRedirectItemsHealsPartialCommit(t *testing.T) {
	tracks, pl, x, y := twoCids(t)
	if _, err := canon.Merge(tracks, nil, x, y); err != nil { // 清單沒跟著改 = COMMIT 傳完 tracks.json 就斷了
		t.Fatal(err)
	}
	fixClock(t, 1_756_700_200)
	id := canon.NewIdentity(tracks.Tracks, tracks.Merged)
	if n := id.RedirectItems(pl); n != 1 || !slices.Equal(cids(*pl), []string{y, y}) || pl.UpdatedAt != 1_756_700_200 {
		t.Fatalf("修 1 筆、updated_at 前進:%d %v %d", n, cids(*pl), pl.UpdatedAt)
	}
	fixClock(t, 1_756_700_300)
	if n := id.RedirectItems(pl); n != 0 || pl.UpdatedAt != 1_756_700_200 {
		t.Fatalf("冪等、沒改就不動 updated_at:%d %d", n, pl.UpdatedAt)
	}
}

func TestTracksMergedEncodesAndDecodesBitEqual(t *testing.T) {
	tracks, _, x, y := twoCids(t)
	if _, err := canon.Merge(tracks, nil, x, y); err != nil {
		t.Fatal(err)
	}
	b := encode(t, tracks)
	if !strings.Contains(string(b), `"merged":{"`+x+`":"`+y+`"}`) {
		t.Fatalf("tracks.json 的 merged 表:%s", b)
	}
	d, err := canon.Decode[canon.Tracks](b)
	if err != nil || !bytes.Equal(encode(t, d), b) {
		t.Fatalf("Encode(Decode(b)) 逐位元相等:%v\n%s\n%s", err, b, encode(t, d))
	}
	if e := string(encode(t, canon.NewTracks())); !strings.Contains(e, `"merged":{}`) {
		t.Fatalf("空的也要有 merged 欄(位元組決定性):%s", e)
	}
	d2, err := canon.Decode[canon.Tracks]([]byte(`{"schema_version":2,"tracks":{}}`))
	if err != nil || d2.Merged == nil {
		t.Fatalf("沒有 merged 欄的舊檔解進來要是空 map:%v %v", err, d2)
	}
}

// 隨機合併(含拿舊 cid 合併)後的不變量:每個 ISRC / (provider, id) 只屬一個 cid、墓碑壓平且指向活著的 cid、清單沒有懸空 item、
// Identity 兩次同解;而且用合併前的 base 跑 DERIVE:平台照舊零變更、平台刪一首恰好一筆 remove、第二輪收斂。
func TestRandomMergesKeepInvariantsAndConverge(t *testing.T) {
	rng := rand.New(rand.NewPCG(2026, 10))
	ids := []string{"a", "b", "c", "d", "e", "f"}
	for n := 0; n < 300; n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			pin(t)
			tracks := canon.NewTracks()
			pl := canon.NewPlaylist("通勤")
			for _, id := range ids {
				tr := canon.NewTrack(prov, ptrack(id))
				tracks.Tracks[tr.CID] = tr
				if _, err := pl.Append(tr.CID); err != nil {
					t.Fatal(err)
				}
			}
			base := snap("通勤", ids...) // 合併前觀測的 cid
			for k := rng.IntN(5); k > 0; k-- {
				a, b := cidOf(ids[rng.IntN(len(ids))]), cidOf(ids[rng.IntN(len(ids))])
				if _, err := canon.Merge(tracks, map[string]*canon.Playlist{pl.PID: pl}, a, b); err != nil && !strings.Contains(err.Error(), "同一個") {
					t.Fatal(err)
				}
			}
			id := canon.NewIdentity(tracks.Tracks, tracks.Merged)
			if len(id.Warnings()) != 0 {
				t.Fatalf("每個 ISRC / (provider, id) 只屬一個 cid:%v", id.Warnings())
			}
			for loser, s := range tracks.Merged {
				if _, chained := tracks.Merged[s]; chained {
					t.Fatalf("鏈沒壓平:%s → %s → …", loser, s)
				}
				if _, alive := tracks.Tracks[s]; !alive {
					t.Fatalf("墓碑指向不存在的 %s", s)
				}
				if _, still := tracks.Tracks[loser]; still {
					t.Fatalf("敗者 %s 還在 tracks", loser)
				}
			}
			for _, it := range pl.Items {
				if _, ok := tracks.Tracks[it.CID]; !ok {
					t.Fatalf("清單 item 懸空:%s", it.CID)
				}
			}
			again := canon.NewIdentity(tracks.Tracks, tracks.Merged)
			for _, x := range ids {
				got := id.Resolve(prov, x, ptrack(x).ISRC)
				if _, ok := tracks.Tracks[got]; !ok || got != again.Resolve(prov, x, ptrack(x).ISRC) {
					t.Fatalf("Resolve 要落在活著的 cid 且兩次同解:%s", got)
				}
			}
			in := canon.DeriveInput{Provider: prov, Playlist: *pl, Tracks: tracks.Tracks, Merged: tracks.Merged, Base: base, Live: live("通勤", ids...)}
			if res := mustDerive(t, in); len(res.Changes) != 0 {
				t.Fatalf("合併後平台照舊 → 零變更:%s", actions(res.Changes))
			}
			drop := rng.IntN(len(ids))
			in.Live = live("通勤", slices.Delete(slices.Clone(ids), drop, drop+1)...)
			res := mustDerive(t, in)
			if actions(res.Changes) != "remove:"+id.Resolve(prov, ids[drop], ptrack(ids[drop]).ISRC) {
				t.Fatalf("平台刪一首 → 恰好一筆 remove:%s", actions(res.Changes))
			}
			converges(t, in, res)
		})
	}
}

func TestMergeToleratesNilMappings(t *testing.T) {
	pin(t)
	tracks := canon.NewTracks()
	tracks.Tracks["i:A"] = canon.Track{CID: "i:A", Title: "t"} // 手組的 Track:Mappings 是 nil
	tracks.Tracks["i:B"] = canon.NewTrack(prov, ptrack("b"))
	if s, err := canon.Merge(tracks, nil, "i:B", "i:A"); err != nil || s != "i:A" || tracks.Tracks["i:A"].Mappings[prov].ID != "b" {
		t.Fatalf("勝者 mappings 為 nil 也要能搬:%s %v %+v", s, err, tracks.Tracks["i:A"].Mappings)
	}
}
