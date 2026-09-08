package canon_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const prov = "spotify"

// ptrack:provider 觀測。id 同時當 ISRC 的種子(TW + 10 碼),讓每個 id 有自己的 cid。
func ptrack(id string) provider.Track {
	return provider.Track{ProviderID: id, ISRC: "TW" + strings.Repeat("0", 10-len(id)) + strings.ToUpper(id), Title: "song-" + id, Artists: []string{"artist"}, DurationMS: 200000}
}

func cidOf(id string) string { return canon.CID(prov, id, ptrack(id).ISRC) }

// world 建一個已經有 tracks 與清單 C 的世界:ids 依序 Append(rank V, k, s, …)。
func world(t *testing.T, ids ...string) (canon.Playlist, map[string]canon.Track) {
	t.Helper()
	pin(t)
	tracks := map[string]canon.Track{}
	pl := canon.NewPlaylist("通勤")
	for _, id := range ids {
		tr := canon.NewTrack(prov, ptrack(id))
		tracks[tr.CID] = tr
		if _, err := pl.Append(tr.CID); err != nil {
			t.Fatal(err)
		}
	}
	return *pl, tracks
}

func live(name string, ids ...string) *canon.Observed {
	o := &canon.Observed{Name: name}
	for _, id := range ids {
		o.Tracks = append(o.Tracks, ptrack(id))
	}
	return o
}

// snap:base 快照,cid 依 ptrack 的 ISRC 算(與 Derive 觀測時算出的相同)。
func snap(name string, ids ...string) *canon.Snapshot {
	s := &canon.Snapshot{Name: name, Items: ids}
	for _, id := range ids {
		s.CIDs = append(s.CIDs, cidOf(id))
	}
	return s
}

func actions(cs []canon.Change) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Action+":"+c.CID)
	}
	return strings.Join(out, " ")
}

func cids(pl canon.Playlist) []string {
	var out []string
	for _, it := range pl.Items {
		out = append(out, it.CID)
	}
	return out
}

func mustDerive(t *testing.T, in canon.DeriveInput) canon.DeriveResult {
	t.Helper()
	res, err := canon.Derive(in)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := canon.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// merged:把 res.Tracks 併回 tracks(T8 會做的事),給收斂測試用。
func merged(tracks, touched map[string]canon.Track) map[string]canon.Track {
	out := map[string]canon.Track{}
	for k, v := range tracks {
		out[k] = v
	}
	for k, v := range touched {
		out[k] = v
	}
	return out
}

// converges:用結果再跑一次,必須零變更且清單逐位元相同(T8 兩輪 e2e 依賴這條)。
func converges(t *testing.T, in canon.DeriveInput, res canon.DeriveResult) {
	t.Helper()
	again := mustDerive(t, canon.DeriveInput{Provider: in.Provider, Playlist: res.Playlist, Tracks: merged(in.Tracks, res.Tracks), Base: &res.Snapshot, Live: in.Live, Merged: in.Merged})
	if len(again.Changes) != 0 || !bytes.Equal(encode(t, &again.Playlist), encode(t, &res.Playlist)) {
		t.Fatalf("第二輪應零變更且逐位元相同:%s\n%s\n%s", actions(again.Changes), encode(t, &res.Playlist), encode(t, &again.Playlist))
	}
}

func TestDeriveFirstPullEmptyC(t *testing.T) {
	pl, tracks := world(t) // 空清單、空 tracks
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Live: live("通勤", "a", "b", "c")}
	res := mustDerive(t, in)
	if actions(res.Changes) != "add:"+cidOf("a")+" add:"+cidOf("b")+" add:"+cidOf("c") {
		t.Fatalf("首次 pull 全部 add:%s", actions(res.Changes))
	}
	if ranks := []string{res.Playlist.Items[0].Rank, res.Playlist.Items[1].Rank, res.Playlist.Items[2].Rank}; strings.Join(ranks, ",") != strings.Join(canon.Ranks(3), ",") {
		t.Fatalf("C 全空時 rank 應用 Ranks 均分:%v", ranks)
	}
	if len(res.Tracks) != 3 || res.Tracks[cidOf("a")].Mappings[prov].ID != "a" || res.VisibleCount != 0 {
		t.Fatalf("新曲要建 track 含 mapping;變更前可見數 0:%+v %d", res.Tracks, res.VisibleCount)
	}
	if strings.Join(res.Snapshot.Items, ",") != "a,b,c" || res.Snapshot.Name != "通勤" {
		t.Fatalf("新 base = L 原文:%+v", res.Snapshot)
	}
	if res.Playlist.Items[0].IID == "" || res.Playlist.Items[0].AddedAt == 0 || res.Playlist.UpdatedAt <= pl.UpdatedAt {
		t.Fatalf("新 item 要有 iid / added_at,清單 updated_at 前進:%+v", res.Playlist)
	}
	converges(t, in, res)
}

func TestDeriveFirstPullOntoNonEmptyCNeverRemoves(t *testing.T) {
	pl, tracks := world(t, "a", "b", "d") // d 在 C、不在平台;沒有 base → 不可移除
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Live: live("別的名字", "b", "a", "c")}
	res := mustDerive(t, in)
	if actions(res.Changes) != "add:"+cidOf("c")+" move:"+cidOf("b") {
		t.Fatalf("配對 a、b;新增 c;b 搬到前面;d 留著、不改名:%s", actions(res.Changes))
	}
	if got := strings.Join(cids(res.Playlist), " "); got != strings.Join([]string{cidOf("b"), cidOf("a"), cidOf("c"), cidOf("d")}, " ") && got != strings.Join([]string{cidOf("b"), cidOf("a"), cidOf("d"), cidOf("c")}, " ") {
		t.Fatalf("順序:%s", got)
	}
	if res.Playlist.Name != "通勤" || res.VisibleCount != 3 {
		t.Fatalf("首次 pull 不改名;可見數 3:%s %d", res.Playlist.Name, res.VisibleCount)
	}
	converges(t, in, res)
}

func TestDeriveDuplicateRemovesLastOccurrence(t *testing.T) {
	pl, tracks := world(t, "a", "a", "b")
	first := pl.Items[0].IID
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "a", "b"), Live: live("通勤", "a", "b")}
	res := mustDerive(t, in)
	if actions(res.Changes) != "remove:"+cidOf("a") || res.Changes[0].Pos != 1 {
		t.Fatalf("重複曲少一份 → 移除 rank 較後的那份:%+v", res.Changes)
	}
	if len(res.Playlist.Items) != 2 || res.Playlist.Items[0].IID != first {
		t.Fatalf("留下的是第一份(iid 不變):%+v", res.Playlist.Items)
	}
	converges(t, in, res)
}

// C 有三份、base 只看過兩份(第三份是別的 provider 加的)、平台刪到剩一份 → 只移除 base 記的差額,且由後往前。
// PR #19 review 的最小反例:C 有兩份 b、L 只有一份且換了序。盲配「第 n 次出現」會這輪搬這份、下輪搬那份,第二輪多一筆 move。
func TestDeriveDuplicateReorderConverges(t *testing.T) {
	pl, tracks := world(t, "b", "b", "c", "e")
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "b", "c", "e"), Live: live("通勤", "c", "e", "b")}
	res := mustDerive(t, in)
	if actions(res.Changes) != "move:"+cidOf("b") {
		t.Fatalf("恰一個 move:%s", actions(res.Changes))
	}
	if got := cids(res.Playlist); strings.Join(got, " ") != strings.Join([]string{cidOf("b"), cidOf("c"), cidOf("e"), cidOf("b")}, " ") {
		t.Fatalf("落單的那份留在原位、被配對的那份搬到最後:%v", got)
	}
	converges(t, in, res)
}

// 同一個 provider id 先後落到兩個 cid(先沒 ISRC → p:spotify:x,後來補上 → i:…):結果必須是輸入的函數。
// base 快照記 cid 之後這裡沒有任何 map 反查,所以是決定性的一次性 remove + add,下一輪收斂。
// 決策 19 / 21:同一個 provider id 先無 ISRC(p: cid)、之後帶 ISRC 出現——身分函式先看 mapping,還是同一個 p: cid,零變更;
// cid 除合併外永不改寫(T2b 之前這裡會 remove p: 再 add i:,同一首歌兩筆)。
func TestDeriveProviderIDKeepsCidWhenISRCAppearsLater(t *testing.T) {
	pin(t)
	bare := ptrack("x")
	bare.ISRC = ""
	first := mustDerive(t, canon.DeriveInput{Provider: prov, Playlist: *canon.NewPlaylist("通勤"), Tracks: map[string]canon.Track{}, Live: &canon.Observed{Name: "通勤", Tracks: []provider.Track{bare}}})
	if actions(first.Changes) != "add:p:spotify:x" {
		t.Fatalf("沒 ISRC 先落到 provider 型 cid:%s", actions(first.Changes))
	}
	in := canon.DeriveInput{Provider: prov, Playlist: first.Playlist, Tracks: first.Tracks, Base: &first.Snapshot, Live: live("通勤", "x")}
	res := mustDerive(t, in)
	if len(res.Changes) != 0 || len(res.Tracks) != 0 || !slices.Equal(res.Snapshot.CIDs, []string{"p:spotify:x"}) {
		t.Fatalf("p: 維持 p:,零變更、tracks 不動:%s %v %v", actions(res.Changes), res.Tracks, res.Snapshot.CIDs)
	}
	converges(t, in, res)
}

// 決策 19:合併後緊接的平台刪除要在第一次 pull 就移除——base 的 cid 只經墓碑重導、不查 mapping、不落公式。三種情境。
func TestDeriveAfterMergePlatformDeleteRemovesOnFirstPull(t *testing.T) {
	t.Run("一般", func(t *testing.T) {
		tracks, pl, x, y := twoCids(t) // C = [x, y];spotify 清單只有 z(x);base [z] → [x],x 是敗者
		base := snap("通勤", "z")
		s, err := canon.Merge(tracks, map[string]*canon.Playlist{pl.PID: pl}, x, y)
		if err != nil {
			t.Fatal(err)
		}
		in := canon.DeriveInput{Provider: prov, Playlist: *pl, Tracks: tracks.Tracks, Merged: tracks.Merged, Base: base, Live: live("通勤")}
		res := mustDerive(t, in)
		if actions(res.Changes) != "remove:"+s || len(res.Playlist.Items) != 1 {
			t.Fatalf("base 的敗者 cid 經墓碑重導才對得上清單裡的勝者;C 有兩個勝者 item、base 只有一個 → 只移除一個:%s %v", actions(res.Changes), cids(res.Playlist))
		}
		converges(t, in, res)
	})
	t.Run("敗者 id 只在 conflicts", func(t *testing.T) {
		tracks, pl, x, y := twoCids(t)
		ty := tracks.Tracks[y]
		ty.Mappings[prov] = canon.Mapping{ID: "z2", Confidence: 100, Source: canon.SourceObserved, UpdatedAt: 5}
		tracks.Tracks[y] = ty
		base := &canon.Snapshot{Name: "通勤", Items: []string{"z", "z2"}, CIDs: []string{x, y}} // 平台兩個 id 都在
		s, err := canon.Merge(tracks, map[string]*canon.Playlist{pl.PID: pl}, x, y)           // 敗者的 z 進 conflicts、mapping 只剩 z2
		if err != nil {
			t.Fatal(err)
		}
		z2 := provider.Track{ProviderID: "z2", ISRC: "TW000000000B", Title: "song-z", Artists: []string{"artist"}, DurationMS: 200000}
		in := canon.DeriveInput{Provider: prov, Playlist: *pl, Tracks: tracks.Tracks, Merged: tracks.Merged, Base: base, Live: &canon.Observed{Name: "通勤", Tracks: []provider.Track{ptrack("z"), z2}}}
		if res := mustDerive(t, in); len(res.Changes) != 0 {
			t.Fatalf("清單裡敗者與勝者各一個 item、平台照舊 → 兩個都對到勝者、零變更:%s", actions(res.Changes))
		}
		in.Live = &canon.Observed{Name: "通勤", Tracks: []provider.Track{z2}} // 平台刪掉 z(它的 id 只剩 conflicts 記得)
		res := mustDerive(t, in)
		if actions(res.Changes) != "remove:"+s || len(res.Playlist.Items) != 1 {
			t.Fatalf("恰好一筆 remove:%s %v", actions(res.Changes), cids(res.Playlist))
		}
		converges(t, in, res)
	})
	t.Run("cid 釘到別的 id、清單裡是另一個 id", func(t *testing.T) {
		pl, tracks := world(t, "a")
		x := cidOf("a")
		tr := tracks[x]
		tr.Mappings[prov] = canon.Mapping{ID: "P", Confidence: 100, Pinned: true, Source: canon.SourceReview}
		tracks[x] = tr
		base := snap("通勤", "a")
		in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: base, Live: live("通勤", "a")}
		if res := mustDerive(t, in); len(res.Changes) != 0 { // mapping 沒中(釘的是 P)、alias set 命中 → 還是 x
			t.Fatalf("平台仍有 a → 零變更:%s", actions(res.Changes))
		}
		in.Live = live("通勤")
		res := mustDerive(t, in)
		if actions(res.Changes) != "remove:"+x { // base 的 x 不查 mapping(P 查不到)、不落公式,保留原 cid 才對得上
			t.Fatalf("平台刪掉 a → remove:%s", actions(res.Changes))
		}
		converges(t, in, res)
	})
}

// 隨機性質測試(PR #19 review 給的骨架):隨機 (C, base, L),universe 6 首、長度 0–6、允許重複,base 四分之一機率沒有;
// 檢查 rank 嚴格遞增、決定性(變更集、cid 序列、rank)、第二輪零變更且逐位元相同。固定種子,壞掉時子測試名稱就是案例。
func TestDeriveRandomProperties(t *testing.T) {
	pin(t)
	rng := rand.New(rand.NewPCG(2026, 9))
	universe := []string{"a", "b", "c", "d", "e", "f"}
	pick := func() []string {
		ids := make([]string, rng.IntN(7))
		for i := range ids {
			ids[i] = universe[rng.IntN(len(universe))]
		}
		return ids
	}
	ranks := func(pl canon.Playlist) []string {
		var out []string
		for _, it := range pl.Items {
			out = append(out, it.Rank)
		}
		return out
	}
	for n := 0; n < 3000; n++ {
		cIDs, bIDs, lIDs := pick(), pick(), pick()
		hasBase := rng.IntN(4) != 0
		t.Run(fmt.Sprintf("%d C=%v base=%v/%t L=%v", n, cIDs, bIDs, hasBase, lIDs), func(t *testing.T) {
			tracks := map[string]canon.Track{}
			pl := canon.NewPlaylist("通勤")
			for _, id := range cIDs {
				tr := canon.NewTrack(prov, ptrack(id))
				tracks[tr.CID] = tr
				if _, err := pl.Append(tr.CID); err != nil {
					t.Fatal(err)
				}
			}
			var base *canon.Snapshot
			if hasBase {
				base = snap("通勤", bIDs...)
			}
			in := canon.DeriveInput{Provider: prov, Playlist: *pl, Tracks: tracks, Base: base, Live: live("通勤", lIDs...)}
			res := mustDerive(t, in)
			for i := 1; i < len(res.Playlist.Items); i++ {
				if res.Playlist.Items[i-1].Rank >= res.Playlist.Items[i].Rank {
					t.Fatalf("rank 沒有嚴格遞增:%v", ranks(res.Playlist))
				}
			}
			again := mustDerive(t, in)
			if actions(again.Changes) != actions(res.Changes) || !slices.Equal(cids(again.Playlist), cids(res.Playlist)) || !slices.Equal(ranks(again.Playlist), ranks(res.Playlist)) {
				t.Fatalf("同一輸入兩次結果不同:%s vs %s", actions(res.Changes), actions(again.Changes))
			}
			converges(t, in, res)
		})
	}
}

// 萬首清單(Spotify 單一清單上限)只把頭尾對調:O(n·m) 的 DP 要配置 855 MB,LIS 版本應在幾 MB 內,且只報兩個 move。
func TestDeriveTenThousandItems(t *testing.T) {
	pin(t)
	ids := make([]string, 10000)
	for i := range ids {
		ids[i] = fmt.Sprintf("t%04d", i)
	}
	first := mustDerive(t, canon.DeriveInput{Provider: prov, Playlist: *canon.NewPlaylist("通勤"), Tracks: map[string]canon.Track{}, Live: live("通勤", ids...)})
	swapped := slices.Clone(ids)
	swapped[0], swapped[len(swapped)-1] = swapped[len(swapped)-1], swapped[0]
	in := canon.DeriveInput{Provider: prov, Playlist: first.Playlist, Tracks: first.Tracks, Base: &first.Snapshot, Live: live("通勤", swapped...)}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	res := mustDerive(t, in)
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 64<<20 {
		t.Fatalf("Derive 配置了 %d MB", got>>20)
	}
	if len(res.Changes) != 2 || res.Changes[0].Action != "move" || res.Changes[1].Action != "move" {
		t.Fatalf("頭尾對調 = 兩個 move:%d 筆", len(res.Changes))
	}
	converges(t, in, res)
}

func TestDeriveRemoveCountFollowsBaseNotC(t *testing.T) {
	pl, tracks := world(t, "a", "a", "a")
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "a"), Live: live("通勤", "a")}
	res := mustDerive(t, in)
	if actions(res.Changes) != "remove:"+cidOf("a") || res.Changes[0].Pos != 2 {
		t.Fatalf("base 2 − L 1 = 只移除一份,且是 rank 最後那份:%+v", res.Changes)
	}
	if len(res.Playlist.Items) != 2 || res.Playlist.Items[0].IID != pl.Items[0].IID || res.Playlist.Items[1].IID != pl.Items[1].IID {
		t.Fatalf("留下前兩份:%+v", res.Playlist.Items)
	}
	converges(t, in, res)
}

func TestDeriveOrderOnlyMovesMinimum(t *testing.T) {
	pl, tracks := world(t, "a", "b", "c", "d")
	before := map[string]string{}
	for _, it := range pl.Items {
		before[it.CID] = it.Rank
	}
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b", "c", "d"), Live: live("通勤", "a", "c", "d", "b")}
	res := mustDerive(t, in)
	if actions(res.Changes) != "move:"+cidOf("b") || res.Changes[0].From != 1 || res.Changes[0].Pos != 3 {
		t.Fatalf("只換順序 → 恰一個 move(b):%+v", res.Changes)
	}
	if strings.Join(cids(res.Playlist), " ") != strings.Join([]string{cidOf("a"), cidOf("c"), cidOf("d"), cidOf("b")}, " ") {
		t.Fatalf("順序:%v", cids(res.Playlist))
	}
	for _, it := range res.Playlist.Items {
		if it.CID != cidOf("b") && it.Rank != before[it.CID] {
			t.Fatalf("沒搬的 item rank 不可動:%s %s → %s", it.CID, before[it.CID], it.Rank)
		}
	}
	if b := res.Playlist.Items[3]; b.CID != cidOf("b") || b.Rank <= before[cidOf("d")] {
		t.Fatalf("b 的新 rank 要在 d 之後:%+v", b)
	}
	converges(t, in, res)
}

func TestDeriveLCSTieKeepsEarlier(t *testing.T) {
	pl, tracks := world(t, "a", "b")
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b"), Live: live("通勤", "b", "a")}
	res := mustDerive(t, in)
	if actions(res.Changes) != "move:"+cidOf("b") {
		t.Fatalf("多解時保留 C 較前者(a)、搬 b:%s", actions(res.Changes))
	}
	converges(t, in, res)
}

func TestDerivePlatformDeletedOne(t *testing.T) {
	pl, tracks := world(t, "a", "b", "c")
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b", "c"), Live: live("通勤", "a", "c")}
	res := mustDerive(t, in)
	if actions(res.Changes) != "remove:"+cidOf("b") || res.Changes[0].Title != "song-b" || res.Changes[0].ProviderID != "b" {
		t.Fatalf("平台刪一首:%+v", res.Changes)
	}
	if res.Playlist.Items[0].Rank != pl.Items[0].Rank || res.Playlist.Items[1].Rank != pl.Items[2].Rank {
		t.Fatal("其餘 rank 不動")
	}
	converges(t, in, res)
}

func TestDerivePlaylistGone(t *testing.T) {
	pl, tracks := world(t, "a")
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "a"), Live: nil}
	res := mustDerive(t, in)
	if !res.Gone || actions(res.Changes) != "unlink:" || !bytes.Equal(encode(t, &res.Playlist), encode(t, &pl)) {
		t.Fatalf("清單消失:gone、一筆 unlink、清單不動:%+v", res)
	}
}

func TestDeriveUnmappedCidUntouched(t *testing.T) {
	pl, tracks := world(t, "a")
	apple := canon.NewTrack("apple", provider.Track{ProviderID: "i.x", Title: "上傳曲"}) // 只有 apple,沒 spotify mapping
	tracks[apple.CID] = apple
	if _, err := pl.Append(apple.CID); err != nil {
		t.Fatal(err)
	}
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "a"), Live: live("通勤", "a")}
	res := mustDerive(t, in)
	if len(res.Changes) != 0 || res.VisibleCount != 1 || len(res.Playlist.Items) != 2 {
		t.Fatalf("沒 mapping 的 item 不參與、不計入可見數:%s %d", actions(res.Changes), res.VisibleCount)
	}
	if !bytes.Equal(encode(t, &res.Playlist), encode(t, &pl)) {
		t.Fatal("無變更時清單逐位元不變")
	}
}

func TestDeriveCrossProviderItemNotRemoved(t *testing.T) {
	pl, tracks := world(t, "a", "k") // k 有 spotify mapping(經 ISRC 解析),但從沒在 spotify 的 base 出現
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "a"), Live: live("通勤", "a")}
	res := mustDerive(t, in)
	if len(res.Changes) != 0 || len(res.Playlist.Items) != 2 {
		t.Fatalf("不在 base 的曲目不可被讀成「平台刪了它」:%s", actions(res.Changes))
	}
}

func TestDeriveVersionSwapIsNoChange(t *testing.T) {
	pl, tracks := world(t, "x")
	y := ptrack("x")
	y.ProviderID = "y" // 專輯版:同 ISRC、不同 provider id
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "x"), Live: &canon.Observed{Name: "通勤", Tracks: []provider.Track{y}}}
	res := mustDerive(t, in)
	if len(res.Changes) != 0 || len(res.Tracks) != 0 || tracks[cidOf("x")].Mappings[prov].ID != "x" {
		t.Fatalf("同 ISRC 換版本應零變更、mapping 不動:%s %+v", actions(res.Changes), res.Tracks)
	}
	if !bytes.Equal(encode(t, &res.Playlist), encode(t, &pl)) {
		t.Fatal("清單逐位元不變")
	}
	// 下一輪 base 是 [y]:反查得到同一個 cid,仍然零變更
	converges(t, in, res)
}

// 平台把 x 重新連結成 y(同 ISRC)→ 零變更;之後使用者刪掉 → mapping 還是 x、base 的 id 是 y,靠快照裡的 cid 才刪得掉。
func TestDeriveRelinkThenDeleteStillRemoves(t *testing.T) {
	pl, tracks := world(t, "x")
	y := ptrack("x")
	y.ProviderID = "y"
	swap := mustDerive(t, canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "x"), Live: &canon.Observed{Name: "通勤", Tracks: []provider.Track{y}}})
	if len(swap.Changes) != 0 || swap.Snapshot.Items[0] != "y" || swap.Snapshot.CIDs[0] != cidOf("x") {
		t.Fatalf("換版本零變更,快照同時記 id 與當時的 cid:%s %+v", actions(swap.Changes), swap.Snapshot)
	}
	del := mustDerive(t, canon.DeriveInput{Provider: prov, Playlist: swap.Playlist, Tracks: merged(tracks, swap.Tracks), Base: &swap.Snapshot, Live: live("通勤")})
	if actions(del.Changes) != "remove:"+cidOf("x") || len(del.Playlist.Items) != 0 {
		t.Fatalf("重新連結後的刪除要傳播:%s", actions(del.Changes))
	}
}

func TestDeriveExistingCidGainsMapping(t *testing.T) {
	pl, tracks := world(t)
	fromApple := canon.NewTrack("apple", provider.Track{ProviderID: "i.a", ISRC: ptrack("a").ISRC, Title: "song-a", DurationMS: 200000})
	tracks[fromApple.CID] = fromApple
	if _, err := pl.Append(fromApple.CID); err != nil {
		t.Fatal(err)
	}
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Live: live("通勤", "a")}
	res := mustDerive(t, in)
	if len(res.Changes) != 0 || len(res.Playlist.Items) != 1 {
		t.Fatalf("別的 provider 建的 cid 第一次被觀測要配對,不是再加一份:%s", actions(res.Changes))
	}
	if got := res.Tracks[cidOf("a")]; got.Mappings[prov].ID != "a" || got.Mappings["apple"].ID != "i.a" {
		t.Fatalf("要加上這個 provider 的 mapping:%+v", got)
	}
	converges(t, in, res)
}

func TestDeriveRenameOnlyWithBase(t *testing.T) {
	pl, tracks := world(t, "a")
	res := mustDerive(t, canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("舊名", "a"), Live: live("新名", "a")})
	if actions(res.Changes) != "rename:" || res.Playlist.Name != "新名" || res.Changes[0].Title != "新名" {
		t.Fatalf("base 存在且平台改名 → rename:%+v", res.Changes)
	}
	res = mustDerive(t, canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Live: live("新名", "a")})
	if len(res.Changes) != 0 || res.Playlist.Name != "通勤" {
		t.Fatalf("首次 pull 不改名:%+v", res.Changes)
	}
}

func TestDeriveNoOpIsByteIdentical(t *testing.T) {
	pl, tracks := world(t, "a", "b")
	res := mustDerive(t, canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b"), Live: live("通勤", "a", "b")})
	if len(res.Changes) != 0 || !bytes.Equal(encode(t, &res.Playlist), encode(t, &pl)) || len(res.Tracks) != 0 {
		t.Fatalf("無變更:%s\n%s\n%s", actions(res.Changes), encode(t, &pl), encode(t, &res.Playlist))
	}
}

func TestDeriveAddInsertsAfterPredecessor(t *testing.T) {
	pl, tracks := world(t, "a", "c")
	in := canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "c"), Live: live("通勤", "a", "b", "c")}
	res := mustDerive(t, in)
	if actions(res.Changes) != "add:"+cidOf("b") || res.Changes[0].Pos != 1 {
		t.Fatalf("%+v", res.Changes)
	}
	items := res.Playlist.Items
	if len(items) != 3 || items[1].CID != cidOf("b") || !(items[0].Rank < items[1].Rank && items[1].Rank < items[2].Rank) {
		t.Fatalf("b 要插在 a 與 c 之間且 rank 嚴格遞增:%+v", items)
	}
	if items[0].Rank != pl.Items[0].Rank || items[2].Rank != pl.Items[1].Rank {
		t.Fatal("a、c 的 rank 不動")
	}
	converges(t, in, res)
}

func TestDeriveDirtyRanksError(t *testing.T) {
	pl, tracks := world(t, "a", "b")
	pl.Items[1].Rank = pl.Items[0].Rank // 兩個 item 同 rank
	_, err := canon.Derive(canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b"), Live: live("通勤", "a", "c", "b")})
	if err == nil || !strings.Contains(err.Error(), "rank") {
		t.Fatalf("髒 rank 需要插入時應回錯不靜默重排:%v", err)
	}
}

func TestDeriveDoesNotMutateInput(t *testing.T) {
	pl, tracks := world(t, "a", "b")
	before := encode(t, &pl)
	beforeTr := encode(t, &canon.Tracks{SchemaVersion: 1, Tracks: tracks})
	mustDerive(t, canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Base: snap("通勤", "a", "b"), Live: live("x", "b", "c")})
	if !bytes.Equal(before, encode(t, &pl)) || !bytes.Equal(beforeTr, encode(t, &canon.Tracks{SchemaVersion: 1, Tracks: tracks})) {
		t.Fatal("Derive 不可改動傳入的 Playlist / Tracks")
	}
}

// TestDeriveObservationOverridesFuzzyMapping:決策 20——tracks 裡是 fuzzy 的 mapping,平台觀測到真實 id 要寫回
// (以前只在「沒有 mapping 或有衝突」時寫回,覆寫會靜默掉);再 derive 一次沒有任何 tracks 變更。
func TestDeriveObservationOverridesFuzzyMapping(t *testing.T) {
	pl, tracks := world(t, "a")
	tr := tracks[cidOf("a")]
	tr.Mappings[prov] = canon.Mapping{ID: "guess", Confidence: 80, Source: canon.SourceFuzzy, UpdatedAt: 5}
	tracks[cidOf("a")] = tr
	res, err := canon.Derive(canon.DeriveInput{Provider: prov, Playlist: pl, Tracks: tracks, Live: live("通勤", "a")})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := res.Tracks[cidOf("a")]
	if !ok || got.Mappings[prov].ID != "a" || got.Mappings[prov].Source != canon.SourceObserved || len(res.Changes) != 0 {
		t.Fatalf("觀測要覆寫 fuzzy mapping 並寫回 res.Tracks:%v %+v %s", ok, got.Mappings[prov], actions(res.Changes))
	}
	tracks[cidOf("a")] = got
	again, err := canon.Derive(canon.DeriveInput{Provider: prov, Playlist: res.Playlist, Tracks: tracks, Live: live("通勤", "a")})
	if err != nil || len(again.Tracks) != 0 || len(again.Changes) != 0 {
		t.Fatalf("第二輪不該再有 tracks 變更:%v %d %s", err, len(again.Tracks), actions(again.Changes))
	}
}
