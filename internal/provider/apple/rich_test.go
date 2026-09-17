package apple

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const songRichFx = `{"id":"a1","type":"songs","attributes":{"name":"派對動物","artistName":"五月天","albumName":"自傳","durationInMillis":227000,
"isrc":"TWA472400123","contentRating":"explicit","url":"https://music.apple.com/tw/album/x/1?i=a1","releaseDate":"2016-07-21","genreNames":["Mandopop","Music"],
"artwork":{"width":3000,"height":3000,"url":"https://is1-ssl.mzstatic.com/image/thumb/Music/x/{w}x{h}bb.jpg"},
"previews":[{"url":"https://audio-ssl.itunes.apple.com/itunes-assets/AudioPreview/x.m4a"}]}}`

// TestAppleSongJSONDecodesRichFieldsAndArtworkSize:P7 決策 42——artwork 模板 {w}x{h} 展成 600、previews[0]、
// releaseDate、genreNames、attributes.url 進 Track.URL;Client.Song 的第二個回傳值(url)不變;舊 fixture 解出零值。
func TestAppleSongJSONDecodesRichFieldsAndArtworkSize(t *testing.T) {
	var sj songJSON
	if err := json.Unmarshal([]byte(songRichFx), &sj); err != nil {
		t.Fatal(err)
	}
	want := provider.Track{ProviderID: "a1", ISRC: "TWA472400123", Title: "派對動物", Artists: []string{"五月天"}, Album: "自傳", DurationMS: 227000, Explicit: true,
		URL: "https://music.apple.com/tw/album/x/1?i=a1", ArtworkURL: "https://is1-ssl.mzstatic.com/image/thumb/Music/x/600x600bb.jpg",
		PreviewURL: "https://audio-ssl.itunes.apple.com/itunes-assets/AudioPreview/x.m4a", ReleaseDate: "2016-07-21", Genres: []string{"Mandopop", "Music"}}
	if got := sj.toTrack(); !reflect.DeepEqual(got, want) {
		t.Errorf("toTrack:\n got %+v\nwant %+v", got, want)
	}

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"data":[%s]}`, songRichFx) })
	tr, u, err := c.Song(context.Background(), "tw", "a1")
	if err != nil || u != want.URL || tr.URL != u {
		t.Errorf("Song 的 url 回傳值要跟 Track.URL 一致:url=%q track.URL=%q err=%v", u, tr.URL, err)
	}

	var old songJSON
	if err := json.Unmarshal([]byte(songJSONFx("o1")), &old); err != nil {
		t.Fatal(err)
	}
	got := old.toTrack()
	if got.ArtworkURL != "" || got.PreviewURL != "" || got.ReleaseDate != "" || got.Genres != nil || got.Popularity != 0 {
		t.Errorf("舊 fixture 沒有 artwork / previews:豐富欄位要是零值:%+v", got)
	}
	if got.URL != "https://music.apple.com/tw/album/x/1?i=o1" || got.ProviderID != "o1" {
		t.Errorf("attributes.url 進 Track.URL、既有欄位不變:%+v", got)
	}
}
