package cli

import (
	"strings"
	"testing"
)

// 英文模式(T2d apple):Apple provider 的「寫不了」理由在列清單的當下翻好,push 的拒絕訊息接它;英文不得暗示 capy 寫得了協作清單。
func TestEnglishPushRefusesCollaborativeApplePlaylist(t *testing.T) {
	withLanguage(t, "en")
	fs, _, _ := pullWorld(t)
	fs.set("p1", "Commute", "a", "b")
	amp := newFakeAmp(t)
	amp.catalog[fakeISRC("a")] = "1001"
	amp.names["1001"] = "song-a"
	amp.order = append(amp.order, "p.collab")
	amp.lists["p.collab"] = &ampList{Name: "Commute", CanEdit: true, Collab: true, Entries: []ampEntry{{ID: "a.1", Catalog: "1001"}}}
	swapAmp(t, amp)
	mustPull(t, "pl", "link", "Commute", "spotify:p1")
	mustPull(t, "pl", "link", "Commute", "apple:p.collab")
	mustPull(t, "pl", "sync", "Commute", "--yes")

	_, _, err := runPull(t, "pl", "push", "Commute", "--provider", "apple", "--yes")
	const reason = "it's a collaborative playlist: Apple returns 500 when its tracks are replaced in one batch (tested 2026-09-22), so capy doesn't write to it; make the change by hand in the Apple Music app, or copy it into a regular playlist and link that instead (untested, usually works)"
	if exitOf(t, err) != 3 || !strings.Contains(err.Error(), "can't write to Commute on apple (p.collab): "+reason) {
		t.Fatalf("協作清單要 exit 3、英文講明不寫:%v", err)
	}
	if len(amp.writes) != 0 {
		t.Fatalf("零寫入:%+v", amp.writes)
	}
}
