package canon_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func fixClock(t *testing.T, unix int64) {
	t.Helper()
	orig := canon.Now
	canon.Now = func() time.Time { return time.Unix(unix, 0) }
	t.Cleanup(func() { canon.Now = orig })
}

// TestMappingLegacyStringDecodesAndReencodesAsObject:schema 1 的字串 mapping 解得進來(observed / 100 / 不 pinned /
// updated_at 0),Encode 一律寫物件並把 schema_version 蓋成目前值——否則 v1 檔重編出去還寫 1,舊 binary 拿到的是
// JSON 型別錯誤而不是 ErrSchemaTooNew(R-5)。
func TestMappingLegacyStringDecodesAndReencodesAsObject(t *testing.T) {
	legacy := `{"schema_version":1,"tracks":{"i:X":{"cid":"i:X","title":"t","artists":["a"],"duration_ms":1,"mappings":{"spotify":"abc"}}}}` + "\n"
	tr, err := canon.Decode[canon.Tracks]([]byte(legacy))
	if err != nil {
		t.Fatal(err)
	}
	want := canon.Mapping{ID: "abc", Confidence: 100, Source: canon.SourceObserved}
	if got := tr.Tracks["i:X"].Mappings["spotify"]; got != want {
		t.Fatalf("字串舊形要視為 observed / 100:%+v", got)
	}
	b, err := canon.Encode(tr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), `{"schema_version":2,`) || !strings.Contains(string(b), `"mappings":{"spotify":{"id":"abc","confidence":100,"pinned":false,"source":"observed","updated_at":0}}`) {
		t.Fatalf("寫出要是物件形、schema_version 蓋成 2:%s", b)
	}
	if _, err := canon.Decode[canon.Tracks]([]byte(`{"schema_version":2,"tracks":{"i:X":{"cid":"i:X","artists":[],"mappings":{"spotify":12}}}}`)); err == nil {
		t.Fatal("既不是字串也不是物件要回錯")
	}
}

// TestNormalizeStampsSchemaVersion:四種檔從 v1 解進來、Encode 出去都是目前版本。
func TestNormalizeStampsSchemaVersion(t *testing.T) {
	for name, f := range map[string]func() ([]byte, error){
		"manifest": func() ([]byte, error) {
			v, err := canon.Decode[canon.Manifest]([]byte(`{"schema_version":1,"devices":[],"playlists":[]}`))
			if err != nil {
				return nil, err
			}
			return canon.Encode(v)
		},
		"playlist": func() ([]byte, error) {
			v, err := canon.Decode[canon.Playlist]([]byte(`{"schema_version":1,"pid":"p","name":"n","updated_at":1,"items":[],"links":{}}`))
			if err != nil {
				return nil, err
			}
			return canon.Encode(v)
		},
		"device": func() ([]byte, error) {
			v, err := canon.Decode[canon.DeviceState]([]byte(`{"schema_version":1,"device_id":"d","base":{}}`))
			if err != nil {
				return nil, err
			}
			return canon.Encode(v)
		},
	} {
		b, err := f()
		if err != nil || !strings.HasPrefix(string(b), `{"schema_version":2,`) {
			t.Fatalf("%s:%v %s", name, err, b)
		}
	}
}

// TestObservePrecedence:決策 20 的優先序 pinned > observed > isrc / fuzzy,以及 updated_at 只在真的改變時才動。
func TestObservePrecedence(t *testing.T) {
	fixClock(t, 1000)
	base := provider.Track{ProviderID: "a1", ISRC: "TWA472400123", Title: "派對動物", DurationMS: 227000}
	tr := canon.NewTrack("spotify", base)
	if m := tr.Mappings["spotify"]; m != (canon.Mapping{ID: "a1", Confidence: 100, Source: canon.SourceObserved, UpdatedAt: 1000}) {
		t.Fatalf("NewTrack 的 mapping:%+v", m)
	}

	// observed 不動:同 provider 另一個版本(同 ISRC)不覆寫、不改 updated_at、不算 changed。
	fixClock(t, 2000)
	if changed, conflict := tr.Observe("spotify", provider.Track{ProviderID: "a2", ISRC: "TWA472400123", Title: "派對動物", DurationMS: 228000}); changed || conflict {
		t.Fatalf("observed 不可抖動:%v %v", changed, conflict)
	}
	if m := tr.Mappings["spotify"]; m.ID != "a1" || m.UpdatedAt != 1000 {
		t.Fatalf("observed 被動到了:%+v", m)
	}

	// fuzzy 被真實觀測覆寫(不同 id):changed、updated_at 換成現在。
	tr.Mappings["apple"] = canon.Mapping{ID: "f1", Confidence: 80, Source: canon.SourceFuzzy, UpdatedAt: 5}
	if changed, _ := tr.Observe("apple", provider.Track{ProviderID: "o1", ISRC: "TWA472400123", Title: "派對動物", DurationMS: 227000}); !changed {
		t.Fatal("fuzzy 要被觀測覆寫")
	}
	if m := tr.Mappings["apple"]; m != (canon.Mapping{ID: "o1", Confidence: 100, Source: canon.SourceObserved, UpdatedAt: 2000}) {
		t.Fatalf("覆寫後:%+v", m)
	}
	// isrc 同 id 也升級成 observed / 100(這是一次真的改變,updated_at 動一次),之後再觀測不再動。
	tr.Mappings["tidal"] = canon.Mapping{ID: "t1", Confidence: 95, Source: canon.SourceISRC, UpdatedAt: 5}
	if changed, _ := tr.Observe("tidal", provider.Track{ProviderID: "t1", ISRC: "TWA472400123", Title: "派對動物", DurationMS: 227000}); !changed {
		t.Fatal("isrc 同 id 也要升級成 observed")
	}
	fixClock(t, 3000)
	if changed, _ := tr.Observe("tidal", provider.Track{ProviderID: "t1", ISRC: "TWA472400123", Title: "派對動物", DurationMS: 227000}); changed || tr.Mappings["tidal"].UpdatedAt != 2000 {
		t.Fatalf("沒有實質變化不可再動:%+v", tr.Mappings["tidal"])
	}

	// pinned 永不動:觀測到不同 id 只記衝突;同一筆再觀測不重複記;釘成「不可得」卻看到了同樣記衝突。
	tr.Mappings["youtube"] = canon.Mapping{ID: "p1", Confidence: 100, Pinned: true, Source: canon.SourceReview, UpdatedAt: 7}
	seen := provider.Track{ProviderID: "y9", ISRC: "TWA472400123", Title: "派對動物", DurationMS: 227000}
	if changed, conflict := tr.Observe("youtube", seen); changed || !conflict {
		t.Fatalf("pinned 對到不同 id:mapping 不動、記衝突:%v %v", changed, conflict)
	}
	if changed, conflict := tr.Observe("youtube", seen); changed || conflict {
		t.Fatal("同一筆矛盾不重複記")
	}
	if m := tr.Mappings["youtube"]; m.ID != "p1" || !m.Pinned || m.UpdatedAt != 7 {
		t.Fatalf("pinned 被動到了:%+v", m)
	}
	tr.Mappings["deezer"] = canon.Mapping{ID: "", Confidence: 100, Pinned: true, Source: canon.SourceReview, UpdatedAt: 7}
	if changed, conflict := tr.Observe("deezer", provider.Track{ProviderID: "d1", Title: "派對動物", DurationMS: 227000}); changed || !conflict || tr.Mappings["deezer"].ID != "" {
		t.Fatalf("釘成不可得卻看到了:釘選不動、記衝突:%v %v %+v", changed, conflict, tr.Mappings["deezer"])
	}
	if n := len(tr.Conflicts); n != 2 || tr.Conflicts[0].ProviderID != "y9" || tr.Conflicts[1].ProviderID != "d1" {
		t.Fatalf("衝突紀錄:%+v", tr.Conflicts)
	}
	// pinned 且 id 相同:照常走 metadata 檢查(時長差 30 s 記衝突)。
	if _, conflict := tr.Observe("youtube", provider.Track{ProviderID: "p1", Title: "派對動物 (Live)", DurationMS: 257000}); !conflict {
		t.Fatal("pinned 同 id 但 metadata 不符仍記衝突")
	}
}

// TestMappingSameIgnoresUpdatedAt:等價比較不看 updated_at。
func TestMappingSameIgnoresUpdatedAt(t *testing.T) {
	a := canon.Mapping{ID: "x", Confidence: 95, Source: canon.SourceISRC, UpdatedAt: 1}
	b := a
	b.UpdatedAt = 2
	if !a.Same(b) {
		t.Fatal("只差 updated_at 應視為相同")
	}
	b.Pinned = true
	if a.Same(b) {
		t.Fatal("pinned 不同就不同")
	}
}

// Decode 本身就經 normalize():解出來的 SchemaVersion 永遠是目前值(檔案原本的版本號要在 Decode 前用 CheckSchema 看)。
func TestDecodeStampsSchemaVersion(t *testing.T) {
	m, err := canon.Decode[canon.Manifest]([]byte(`{"schema_version":1,"devices":[],"playlists":[]}`))
	if err != nil || m.SchemaVersion != canon.SchemaVersion {
		t.Fatalf("Decode 後 SchemaVersion 應為目前值 %d:%d %v", canon.SchemaVersion, m.SchemaVersion, err)
	}
}
