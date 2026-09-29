package youtube

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"

	ytauth "github.com/Tai-ch0802/capy-music/internal/auth/youtube"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// TestLiveWriteRoundTrip:真帳號、真 API 跑一遍寫端(CAPY_YOUTUBE_LIVE_HEADERS=<貼上的 headers 檔> 才跑;CI 沒有它就跳過)。
// 在一份拋棄式清單上:CreatePlaylist → 純 append 三首 → 反序(整批取代)→ 改名 → 讀回,結尾由測試自己送 playlist/delete 清場
// (provider 絕不送它)。不碰 Drive、不動既有清單。這是唯一讓「Go 的寫入程式碼」而不只是 curl 探測碰到真平台的地方(PR #118 review)。
func TestLiveWriteRoundTrip(t *testing.T) {
	path := os.Getenv("CAPY_YOUTUBE_LIVE_HEADERS")
	if path == "" {
		t.Skip("set CAPY_YOUTUBE_LIVE_HEADERS=<headers file> to run against the real account")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ytauth.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	hc := &http.Client{Timeout: 60 * time.Second}
	acc, err := NewClient(hc, "", h, "zh-TW").AccountInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := New(hc, "", h, "zh-TW", acc.ChannelID)
	t.Logf("account %s (%s) channel %s", acc.Name, acc.Handle, acc.ChannelID)

	hits, err := p.Search(ctx, provider.Query{Text: "五月天 派對動物", Limit: 3})
	if err != nil || len(hits) < 3 {
		t.Fatalf("search:%v %d", err, len(hits))
	}
	ids := []string{hits[0].ProviderID, hits[1].ProviderID, hits[2].ProviderID}

	ref, err := p.CreatePlaylist(ctx, fmt.Sprintf("capy-probe-live-%d", time.Now().Unix()))
	if err != nil {
		t.Fatal(err)
	}
	plid, _ := p.own(ref.ID)
	t.Cleanup(func() { // 清場走測試自己的 raw 呼叫,provider 沒有這個方法(決策 60)
		_, derr := p.c.post(context.Background(), "playlist/delete", map[string]any{"playlistId": plid}, "")
		t.Logf("playlist/delete %s: %v", plid, derr)
	})
	t.Logf("created %s", ref.ID)

	// 純 append 三首(一個請求)。
	var ops []provider.PlaylistOp
	for i, id := range ids {
		ops = append(ops, provider.PlaylistOp{Kind: provider.OpAdd, Pos: i, ProviderID: id})
	}
	if _, err := p.ApplyOps(ctx, ref.ID, nil, ops); err != nil {
		t.Fatal(err)
	}
	got := readIDs(t, ctx, p, ref.ID)
	if !slices.Equal(got, ids) {
		t.Fatalf("append:%v want %v", got, ids)
	}
	// 反序 + 改名:整批取代一個請求(REMOVE 三列 + ADD 三首)。
	rev := []string{ids[2], ids[1], ids[0]}
	ops = []provider.PlaylistOp{{Kind: provider.OpRename, Name: ref.Name + "-renamed"}, {Kind: provider.OpMove, From: 2, Pos: 0}, {Kind: provider.OpMove, From: 2, Pos: 1}}
	if _, err := p.ApplyOps(ctx, ref.ID, ids, ops); err != nil {
		t.Fatal(err)
	}
	// 讀回可能落後幾秒(探測看過剛寫完的空頁):最多等 30 s,每次都記下來,才分得出「傳播延遲」與「請求送錯」。
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := readIDs(t, ctx, p, ref.ID)
		t.Logf("%s read back: %v", time.Now().Format("15:04:05.000"), got)
		if slices.Equal(got, rev) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replace:%v want %v", got, rev)
		}
		time.Sleep(3 * time.Second)
	}
	rows, editable, err := p.playlistRows(ctx, ref.ID)
	if err != nil || !editable || len(rows) != 3 {
		t.Fatalf("re-read:%v editable=%v rows=%d", err, editable, len(rows))
	}
	// 讀過之後平台變了(這裡用錯的 current 模擬)→ 零寫入。
	if _, err := p.ApplyOps(ctx, ref.ID, []string{ids[0]}, []provider.PlaylistOp{{Kind: provider.OpRemove, Pos: 0}}); err == nil {
		t.Fatal("stale current must refuse")
	}
	if got := readIDs(t, ctx, p, ref.ID); !slices.Equal(got, rev) {
		t.Fatalf("after refused write:%v", got)
	}
	t.Logf("live write round trip ok: %v", rev)
}

func readIDs(t *testing.T, ctx context.Context, p *Provider, id string) []string {
	t.Helper()
	tracks, err := p.GetPlaylistItems(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(tracks))
	for i, tr := range tracks {
		out[i] = tr.ProviderID
	}
	return out
}
