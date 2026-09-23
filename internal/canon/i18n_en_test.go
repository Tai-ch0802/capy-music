package canon

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

func setLang(t *testing.T, lang string) {
	t.Helper()
	prev := i18n.Current()
	if !i18n.Set(lang) {
		t.Fatalf("不支援 %s", lang)
	}
	t.Cleanup(func() { i18n.Set(prev) })
}

// 英文模式:canon 回的錯與 pull 印的 tracks 警告都是英文;%w 換成 {err} 之後 errors.Is 照舊。
func TestEnglishMessages(t *testing.T) {
	setLang(t, "en")
	msg := func(err error) string {
		if err == nil {
			return "<nil>"
		}
		return err.Error()
	}
	tooNew := CheckSchema([]byte(`{"schema_version":99}`))
	if !errors.Is(tooNew, ErrSchemaTooNew) {
		t.Errorf("要包著 ErrSchemaTooNew:%v", tooNew)
	}
	_, notB62 := RankBetween("a!", "")
	_, noGap := RankBetween("a", "a0")
	var m Mapping
	badSource := json.Unmarshal([]byte(`{"id":"x","confidence":50,"source":"whatever","updated_at":1}`), &m)
	tr := &Tracks{Tracks: map[string]Track{"i:A": {CID: "i:A"}}, Merged: map[string]string{}}
	_, same := Merge(tr, nil, "i:A", "i:A")
	_, missing := Merge(tr, nil, "i:A", "i:Z")
	for _, c := range []struct{ got, want string }{
		{msg(tooNew), "the files on Drive use a newer schema than this capy; run capy update first (file schema 99, this capy supports 3)"},
		{msg(CheckSchema([]byte(`{}`))), "the file has no schema_version"},
		{msg(notB62), `rank "a!" contains a character that isn't base62`},
		{msg(noGap), `ranks "a" and "a" are out of order or have no room between them`},
		{msg(badSource), `unknown mapping source "whatever" (expected observed, isrc, fuzzy or review)`},
		{msg(same), "both already resolve to the same cid i:A"},
		{msg(missing), "i:Z is not in tracks"},
	} {
		if c.got != c.want {
			t.Errorf("got  %q\nwant %q", c.got, c.want)
		}
	}

	dup := Mapping{ID: "dup", Confidence: 100, Source: SourceObserved}
	id := NewIdentity(map[string]Track{
		"i:A": {CID: "i:A", Mappings: map[string]Mapping{"spotify": dup}},
		"i:B": {CID: "i:B", Mappings: map[string]Mapping{"spotify": dup}},
	}, map[string]string{"i:GONE": "i:GHOST"})
	want := []string{"spotify:dup belongs to both i:A and i:B; using i:A", "merge tombstone i:GONE → i:GHOST: the surviving cid is not in tracks"}
	if w := id.Warnings(); len(w) != 2 || w[0] != want[0] || w[1] != want[1] {
		t.Errorf("警告:%q", w)
	}
}
