package canon_test

import (
	"bytes"
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

func snap(name string, ids ...string) *canon.Snapshot { return &canon.Snapshot{Name: name, Items: ids} }

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
	again := mustDerive(t, canon.DeriveInput{Provider: in.Provider, Playlist: res.Playlist, Tracks: merged(in.Tracks, res.Tracks), Base: &res.Snapshot, Live: in.Live})
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
	if len(res.Tracks) != 3 || res.Tracks[cidOf("a")].Mappings[prov] != "a" || res.VisibleCount != 0 {
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
	if len(res.Changes) != 0 || len(res.Tracks) != 0 || tracks[cidOf("x")].Mappings[prov] != "x" {
		t.Fatalf("同 ISRC 換版本應零變更、mapping 不動:%s %+v", actions(res.Changes), res.Tracks)
	}
	if !bytes.Equal(encode(t, &res.Playlist), encode(t, &pl)) {
		t.Fatal("清單逐位元不變")
	}
	// 下一輪 base 是 [y]:反查得到同一個 cid,仍然零變更
	converges(t, in, res)
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
	if got := res.Tracks[cidOf("a")]; got.Mappings[prov] != "a" || got.Mappings["apple"] != "i.a" {
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
