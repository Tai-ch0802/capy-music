package provider

import "testing"

func TestCapabilityHas(t *testing.T) {
	caps := CapSearch | CapPlaylistRead | CapPlaybackControl
	if !caps.Has(CapSearch) || !caps.Has(CapPlaylistRead|CapPlaybackControl) {
		t.Error("Has 應支援單一與組合查詢")
	}
	if caps.Has(CapPlaylistRemove) {
		t.Error("未含的能力不應回 true")
	}
	if caps.Has(CapSearch | CapLibraryWrite) {
		t.Error("組合查詢缺一即 false")
	}
}
