package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/store"
)

// withSnapshots:假 Spotify 的清單列表附 snapshot_id(決策 57 的快取才會用上)。
func withSnapshots(fs *fakeSpotify) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.snapshots = true
}

// TestPullSkipsUnchangedPlaylist:【決策 57】清單版本(snapshot_id)沒換就用本機快取,不重讀每一頁——結果跟真的讀一模一樣;
// 版本一換就真的讀。
func TestPullSkipsUnchangedPlaylist(t *testing.T) {
	fs, _, _ := pullWorld(t)
	withSnapshots(fs)
	fs.set("p1", "通勤", "a", "b", "c")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	before := fs.reads()
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); out != "" || fs.reads() != before {
		t.Fatalf("沒變的清單不重讀、零變更:讀了 %d 次 %q", fs.reads()-before, out)
	}
	fs.set("p1", "通勤", "a", "b", "c", "d") // 版本換了
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); !strings.HasPrefix(out, "add\tspotify\t通勤\t3\t"+fakeCID("d")) || fs.reads() != before+1 {
		t.Fatalf("版本一換就真的讀:讀了 %d 次 %q", fs.reads()-before, out)
	}
	fs.set("p1", "通勤", "b", "a", "c", "d") // 只換序:曲目數一樣,版本換了——光比數量看不出來
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); !strings.HasPrefix(out, "move\tspotify\t通勤") || fs.reads() != before+2 {
		t.Fatalf("曲目數一樣、版本換了也要真的讀:讀了 %d 次 %q", fs.reads()-before, out)
	}
}

// TestPullRefetchesWhenTotalDiffers:版本一樣、曲目數卻跟列表的 Total 不同(不該發生,但發生了就別信快取)→ 真的讀。
func TestPullRefetchesWhenTotalDiffers(t *testing.T) {
	fs, _, _ := pullWorld(t)
	withSnapshots(fs)
	fs.mu.Lock()
	fs.snapFixed = map[string]string{"p1": "same"}
	fs.mu.Unlock()
	fs.set("p1", "通勤", "a", "b", "c")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	fs.set("p1", "通勤", "a", "b", "c", "d")
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); !strings.HasPrefix(out, "add\tspotify\t通勤\t3\t"+fakeCID("d")) {
		t.Fatalf("曲目數對不上就別信快取:%q", out)
	}
}

// TestPullRefetchesAfterTTL:快取最多用一週(Developer Terms IV.3.2 只准暫時快取 metadata)。
func TestPullRefetchesAfterTTL(t *testing.T) {
	fs, _, _ := pullWorld(t)
	withSnapshots(fs)
	clock := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	orig := itemCacheNow
	itemCacheNow = func() time.Time { return clock }
	t.Cleanup(func() { itemCacheNow = orig })
	fs.set("p1", "通勤", "a", "b")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	before := fs.reads()
	clock = clock.Add(itemCacheTTL - time.Minute)
	mustPull(t, "pl", "pull", "通勤", "--yes")
	if fs.reads() != before {
		t.Fatalf("一週內用快取:讀了 %d 次", fs.reads()-before)
	}
	withStore(t, func(st *store.Store) { // 解除連結的清單留下的列:一列過期、一列還新
		for id, at := range map[string]time.Time{"gone": clock.Add(-itemCacheTTL), "kept": clock} {
			if err := st.SavePlaylistItems("spotify", id, store.PlaylistItems{Version: "v", Tracks: []byte("[]"), FetchedAt: at}, time.Time{}); err != nil {
				t.Fatal(err)
			}
		}
	})
	clock = clock.Add(2 * time.Minute)
	mustPull(t, "pl", "pull", "通勤", "--yes")
	if fs.reads() != before+1 {
		t.Fatalf("過了一週要真的讀:讀了 %d 次", fs.reads()-before)
	}
	withStore(t, func(st *store.Store) { // 真的讀了就會存,存的時候過期的列一起刪(不無限期留著 Spotify 的 metadata)
		for id, want := range map[string]bool{"gone": false, "kept": true, "p1": true} {
			if _, ok, err := st.CachedPlaylistItems("spotify", id); err != nil || ok != want {
				t.Errorf("%s:留著=%v,要 %v(%v)", id, ok, want, err)
			}
		}
	})
}

// withStore:開本機 db 做 f、關掉(開著的話下一個命令會等鎖)。
func withStore(t *testing.T, f func(*store.Store)) {
	t.Helper()
	st, err := store.Open(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f(st)
}

// TestPullRefetchesAfterDBDeleted:快取在本機 db,刪掉只會多讀一次,結果一樣。
func TestPullRefetchesAfterDBDeleted(t *testing.T) {
	fs, _, _ := pullWorld(t)
	withSnapshots(fs)
	fs.set("p1", "通勤", "a", "b")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	deleteDB(t)
	before := fs.reads()
	if out, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); out != "" || fs.reads() != before+1 {
		t.Fatalf("刪 db 後真的讀一次、零變更:讀了 %d 次 %q", fs.reads()-before, out)
	}
}

// TestSyncForgetsCacheAfterPush:我們寫過的清單,下一輪一定真的讀(不拿寫入回傳的版本記快取:寫與重讀之間別人可能又改了);
// 之後沒變就又不讀了。push 在寫之前那次重讀照舊(安全網)。
func TestSyncForgetsCacheAfterPush(t *testing.T) {
	fs1, fs2, _, _ := syncWorld(t)
	withSnapshots(fs1)
	withSnapshots(fs2)
	mustPull(t, "pl", "sync", "通勤", "--yes") // 記下兩邊的版本
	r1, r2 := fs1.reads(), fs2.reads()
	mustPull(t, "pl", "sync", "通勤", "--yes")
	if fs1.reads() != r1 || fs2.reads() != r2 {
		t.Fatalf("兩邊都沒變:不讀:%d %d", fs1.reads()-r1, fs2.reads()-r2)
	}
	fs2.set("q1", "通勤", "a", "c") // Apple 刪一首 → push 到 Spotify
	out, _ := mustPull(t, "pl", "sync", "通勤", "--yes")
	if !strings.Contains(out, "push\tremove\tspotify") || fs1.reads()-r1 != 2 || fs2.reads()-r2 != 1 {
		t.Fatalf("Spotify:pull 半邊用快取、寫之前重讀 1、L′ 1;Apple 讀 1:%d %d\n%s", fs1.reads()-r1, fs2.reads()-r2, out)
	}
	r1, r2 = fs1.reads(), fs2.reads()
	mustPull(t, "pl", "sync", "通勤", "--yes")
	if fs1.reads()-r1 != 1 || fs2.reads() != r2 {
		t.Fatalf("剛寫過的 Spotify 真的讀一次,Apple 用快取:%d %d", fs1.reads()-r1, fs2.reads()-r2)
	}
	r1 = fs1.reads()
	mustPull(t, "pl", "sync", "通勤", "--yes")
	if fs1.reads() != r1 {
		t.Fatalf("之後沒變就又不讀了:%d", fs1.reads()-r1)
	}
}

// TestSyncForgetsCacheEvenIfVersionLags:寫完之後列表可能還回舊的 snapshot_id(最終一致)。快取要在寫完就丟掉——不然下一輪
// 拿寫之前的曲目當現況:同數量的重排比 Total 看不出來,會把自己的重排讀成平台排回去(規則 6′),再推一個反向的 move 到對面。
func TestSyncForgetsCacheEvenIfVersionLags(t *testing.T) {
	fs1, fs2, _, _ := syncWorld(t)
	withSnapshots(fs1)
	fs1.mu.Lock()
	fs1.snapFixed = map[string]string{"p1": "lagging"} // 寫了也不換
	fs1.mu.Unlock()
	mustPull(t, "pl", "sync", "通勤", "--yes") // 記下 Spotify 的版本與曲目
	fs2.set("q1", "通勤", "b", "a", "c")       // Apple 重排 → move 推到 Spotify
	if out, _ := mustPull(t, "pl", "sync", "通勤", "--yes"); !strings.Contains(out, "push\tmove\tspotify") {
		t.Fatalf("要把重排推到 Spotify:\n%s", out)
	}
	r1 := fs1.reads()
	if out, _ := mustPull(t, "pl", "sync", "通勤", "--yes"); out != "" || fs1.reads()-r1 != 1 {
		t.Fatalf("寫過的清單下一輪真的讀、零變更:讀了 %d 次\n%s", fs1.reads()-r1, out)
	}
	if a, s := strings.Join(fs2.tracksOf("q1"), ","), strings.Join(fs1.tracksOf("p1"), ","); a != "b,a,c" || s != "b,a,c" {
		t.Errorf("Apple 的重排不能被翻回去:Apple %s、Spotify %s", a, s)
	}
}

// TestItemCacheKeepsOnlySyncFields:快取只放同步要用的欄位(白名單),不放 Raw 與封面、試聽這些顯示用的欄位(決策 42 / 57)。
func TestItemCacheKeepsOnlySyncFields(t *testing.T) {
	fs, _, _ := pullWorld(t)
	withSnapshots(fs)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	st, err := store.Open(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	it, ok, err := st.CachedPlaylistItems("spotify", "p1")
	if err != nil || !ok {
		t.Fatalf("要有快取:%v %v", ok, err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(it.Tracks, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("%s %v", it.Tracks, err)
	}
	allowed := map[string]bool{"id": true, "isrc": true, "title": true, "artists": true, "album": true, "duration_ms": true, "explicit": true, "unpushable": true}
	for k := range rows[0] {
		if !allowed[k] {
			t.Errorf("快取不可以有白名單以外的欄位:%q(%s)", k, it.Tracks)
		}
	}
}

// TestLogoutForgetsItemCache:登出 Spotify 也清掉清單曲目的快取(Developer Policy I.1.b;決策 57 的快取也算)。
func TestLogoutForgetsItemCache(t *testing.T) {
	fs, _, _ := pullWorld(t)
	withSnapshots(fs)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	if _, err := runCLI(t, "auth", "logout", "spotify"); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, ok, err := st.CachedPlaylistItems("spotify", "p1"); err != nil || ok {
		t.Errorf("登出要清掉清單曲目的快取:%v %v", ok, err)
	}
}
