package spotify

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const trackJSONRichFixture = `{"id":"r1","name":"派對動物","uri":"spotify:track:r1","duration_ms":227000,"explicit":false,"popularity":73,
"preview_url":"https://p.scdn.co/mp3-preview/abc","external_urls":{"spotify":"https://open.spotify.com/track/r1"},
"album":{"name":"自傳","release_date":"2016-07-21","images":[{"url":"https://i.scdn.co/image/640","height":640,"width":640},{"url":"https://i.scdn.co/image/300","height":300,"width":300}]},
"artists":[{"name":"五月天"}],"external_ids":{"isrc":"TWA472400123"}}`

// TestSpotifyTrackJSONDecodesRichFields:P7 決策 42 六個豐富欄位從同一份 track 物件解出(零額外呼叫);
// 封面取 images[0](Spotify 依大到小排);舊 fixture(沒這些欄位)解出零值、其餘欄位不變。
func TestSpotifyTrackJSONDecodesRichFields(t *testing.T) {
	var tj trackJSON
	if err := json.Unmarshal([]byte(trackJSONRichFixture), &tj); err != nil {
		t.Fatal(err)
	}
	want := provider.Track{ProviderID: "r1", ISRC: "TWA472400123", Title: "派對動物", Artists: []string{"五月天"}, Album: "自傳", DurationMS: 227000,
		URL: "https://open.spotify.com/track/r1", ArtworkURL: "https://i.scdn.co/image/640", PreviewURL: "https://p.scdn.co/mp3-preview/abc",
		ReleaseDate: "2016-07-21", Popularity: 73}
	if got := tj.toTrack(); !reflect.DeepEqual(got, want) {
		t.Errorf("toTrack:\n got %+v\nwant %+v", got, want)
	}

	var old trackJSON
	if err := json.Unmarshal([]byte(trackFx("o1")), &old); err != nil {
		t.Fatal(err)
	}
	got := old.toTrack()
	if got.URL != "" || got.ArtworkURL != "" || got.PreviewURL != "" || got.ReleaseDate != "" || got.Popularity != 0 || got.Genres != nil {
		t.Errorf("舊 fixture 的豐富欄位要是零值:%+v", got)
	}
	if got.ProviderID != "o1" || got.Title != "派對動物" || got.ISRC != "TWA472400123" || got.DurationMS != 227000 {
		t.Errorf("既有欄位不變:%+v", got)
	}
}
