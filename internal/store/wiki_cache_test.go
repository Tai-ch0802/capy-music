package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWikiCacheRoundTripAndForgetLeavesIt(t *testing.T) {
	s, err := OpenAt(filepath.Join(t.TempDir(), "state.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok, err := s.CachedWiki("v1|ja|x|y"); ok || err != nil {
		t.Fatalf("空的:%v %v", ok, err)
	}
	at := time.Unix(1_700_000_000, 0)
	e := WikiEntry{Title: "x", Artists: "y", Language: "ja", Model: "m", Body: "## A\nbody", FetchedAt: at}
	if err := s.SaveWiki("v1|ja|x|y", e); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.CachedWiki("v1|ja|x|y")
	if err != nil || !ok || got != e {
		t.Fatalf("round trip:%+v %v %v", got, ok, err)
	}
	e.Body, e.FetchedAt = "## B", at.Add(time.Hour)
	if err := s.SaveWiki("v1|ja|x|y", e); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.CachedWiki("v1|ja|x|y"); got.Body != "## B" || !got.FetchedAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("要覆寫:%+v", got)
	}
	// 登出某個平台清的是那個平台的快取;wiki 的回答不屬於任何平台,留著。
	if err := s.ForgetProvider("spotify"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.CachedWiki("v1|ja|x|y"); !ok {
		t.Fatal("ForgetProvider 不該動 wiki_cache")
	}
}
