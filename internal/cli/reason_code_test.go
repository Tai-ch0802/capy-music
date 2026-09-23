package cli

// REASON_CODE(計畫 §2.4 第 3 點、Q52):REASON 給人看、跟著語系;REASON_CODE 是給腳本與網頁的穩定代碼,加在最後一欄、永不翻譯。

import (
	"net/http"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/provider/spotify"
)

func TestReasonCodeIsLastColumn(t *testing.T) {
	for name, h := range map[string][]string{"pull": pullHeader, "sync": syncHeader, "resolve": resolveHeader, "dedup report": dedupReportHeader} {
		if n := len(h); n < 2 || h[n-1] != "REASON_CODE" || h[n-2] != "REASON" {
			t.Errorf("%s 的表頭要以 REASON、REASON_CODE 結尾(既有欄位不動):%v", name, h)
		}
	}
}

// pushRows 的每一種列:ops 與 skipped 由真的 canon.PushPlan 算(skip 的 no_mapping 來自 canon.Skip.Code)。
func TestPushRowsReasonCodes(t *testing.T) {
	s := &canonState{tracks: canon.NewTracks()}
	for cid, id := range map[string]string{"c1": "a", "c2": "b", "c3": "c", "c4": "", "c5": "spotify:local:x:y:z:200"} {
		tr := canon.Track{CID: cid, Title: "song-" + cid, Mappings: map[string]canon.Mapping{}}
		if id != "" {
			tr.Mappings["spotify"] = canon.Mapping{ID: id}
		}
		s.tracks.Tracks[cid] = tr
	}
	pl := &canon.Playlist{Name: "new"}
	for _, cid := range []string{"c2", "c1", "c3", "c4", "c5"} { // c2 / c1 換序、c3 新增、c4 沒 mapping、c5 有 mapping 推不出去
		pl.Items = append(pl.Items, canon.Item{IID: "i" + cid, CID: cid})
	}
	live := []canon.LiveItem{{CID: "c1", ProviderID: "a"}, {CID: "c2", ProviderID: "b"}, {CID: "cx", ProviderID: "x"}} // cx 要移除
	w := spotify.New(http.DefaultClient, "http://127.0.0.1:1")
	ops, skipped := canon.PushPlan(live, pl.Items, "old", pl.Name, func(cid string) (string, bool) {
		m := s.tracks.Tracks[cid].Mappings["spotify"]
		return m.ID, m.ID != "" && w.Pushable(m.ID)
	})
	plan := &pushPlan{pl: pl, prov: "spotify", liveName: "old", current: []string{"a", "b", "x"}, ops: ops}
	rows, _ := pushRows(s, plan, []string{"c1", "c2", "cx"}, skipped)
	want := map[string]string{"remove cx": "removed_in_master", "add c3": "push", "rename ": "renamed_in_master", "skip c4": "no_mapping", "skip c5": "unpushable"}
	got := map[string]string{}
	for _, r := range rows {
		if len(r) != len(pullHeader) {
			t.Fatalf("push 列的欄數要跟 pullHeader 一樣:%q", r)
		}
		if r[0] == "move" {
			if r[len(r)-1] != "moved_in_master" {
				t.Errorf("move 列:%q", r)
			}
			got["move"] = "ok"
			continue
		}
		got[r[0]+" "+r[4]] = r[len(r)-1]
	}
	if got["move"] != "ok" {
		t.Errorf("要有一列 move:%q", rows)
	}
	for k, code := range want {
		if got[k] != code {
			t.Errorf("%s 的 REASON_CODE = %q,want %q(全部:%q)", k, got[k], code, rows)
		}
	}
}

// migrateReason 的三種去向;網頁的搬家精靈靠 "push" 認「這一首搬得過去」(move.js 的 tally)。
func TestMigrateReasonCodes(t *testing.T) {
	var w provider.PlaylistWriter = spotify.New(http.DefaultClient, "http://127.0.0.1:1")
	s := &canonState{tracks: canon.NewTracks()}
	s.tracks.Tracks["ok"] = canon.Track{Mappings: map[string]canon.Mapping{"spotify": {ID: "a", Source: canon.SourceISRC, Confidence: 95}}}
	s.tracks.Tracks["local"] = canon.Track{Mappings: map[string]canon.Mapping{"spotify": {ID: "spotify:local:x:y:z:200"}}}
	s.tracks.Tracks["none"] = canon.Track{Mappings: map[string]canon.Mapping{}}
	for cid, want := range map[string]struct {
		code string
		ok   bool
	}{"ok": {"push", true}, "local": {"unpushable", false}, "none": {"no_mapping", false}} {
		if _, _, code, ok := migrateReason(s, w, "spotify", cid); code != want.code || ok != want.ok {
			t.Errorf("%s:code %q ok %v,want %q %v", cid, code, ok, want.code, want.ok)
		}
	}
}
