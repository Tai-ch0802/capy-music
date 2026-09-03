//go:build darwin

package apple

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func stubOSA(t *testing.T, out string) *[]string {
	t.Helper()
	var scripts []string
	orig := runOSA
	runOSA = func(script string) (string, error) { scripts = append(scripts, script); return out, nil }
	t.Cleanup(func() { runOSA = orig })
	return &scripts
}

func TestStateParsesPlaying(t *testing.T) {
	stubOSA(t, "playing\t派對動物\t五月天\t自傳\t227500\t61200")
	p := &Provider{}
	st, err := p.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Playing || st.Track == nil || st.Track.Title != "派對動物" || st.Track.Artists[0] != "五月天" ||
		st.Track.DurationMS != 227500 || st.ProgressMS != 61200 || st.Device.Name != "Music.app" {
		t.Errorf("state = %+v track=%+v", st, st.Track)
	}
}

func TestStateStoppedIsNil(t *testing.T) {
	stubOSA(t, "stopped")
	st, err := (&Provider{}).State(context.Background())
	if err != nil || st != nil {
		t.Fatalf("stopped 應回 (nil, nil):(%+v, %v)", st, err)
	}
}

// TestStateRejectsUnparsableNumbers:locale 用 "," 當小數點(如 de/fr macOS)時,數值欄位
// 必須回錯,不能被 strconv 靜默吞成 0(review finding 1)。
func TestStateRejectsUnparsableNumbers(t *testing.T) {
	stubOSA(t, "playing\t派對動物\t五月天\t自傳\t227,5\t61200")
	if _, err := (&Provider{}).State(context.Background()); err == nil {
		t.Fatal("數值欄位無法解析時應回錯,不應靜默為 0")
	}
}

func TestPauseNextPrevScripts(t *testing.T) {
	scripts := stubOSA(t, "")
	p := &Provider{}
	_ = p.Pause(context.Background())
	_ = p.Next(context.Background())
	_ = p.Prev(context.Background())
	want := []string{"pause", "next track", "previous track"}
	for i, w := range want {
		if !strings.Contains((*scripts)[i], `tell application "Music" to `+w) {
			t.Errorf("script[%d] = %q, want contains %q", i, (*scripts)[i], w)
		}
	}
}

func TestPlayResume(t *testing.T) {
	scripts := stubOSA(t, "")
	if err := (&Provider{}).Play(context.Background(), provider.PlayRequest{}); err != nil {
		t.Fatal(err)
	}
	if len(*scripts) != 1 || !strings.Contains((*scripts)[0], `tell application "Music" to play`) {
		t.Errorf("resume script = %v", *scripts)
	}
}

func TestPlayTrackUsesSongURLMechanismA(t *testing.T) {
	t.Setenv("CAPY_APPLE_PLAY_MECHANISM", "")
	scripts := stubOSA(t, "")
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[%s]}`, songJSONFx("s1"))
	})
	p := &Provider{c: c, storefront: "tw"}
	if err := p.Play(context.Background(), provider.PlayRequest{TrackIDs: []string{"s1"}}); err != nil {
		t.Fatal(err)
	}
	if len(*scripts) != 1 || !strings.Contains((*scripts)[0], `open location "music://music.apple.com/tw/album/x/1?i=s1"`) {
		t.Errorf("機制 A 應用 open location + music:// 網址:%v", *scripts)
	}
}

func TestPlayTrackMechanismB(t *testing.T) {
	t.Setenv("CAPY_APPLE_PLAY_MECHANISM", "open")
	stubOSA(t, "")
	var opened string
	orig := runOpen
	runOpen = func(u string) error { opened = u; return nil }
	t.Cleanup(func() { runOpen = orig })
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[%s]}`, songJSONFx("s1"))
	})
	p := &Provider{c: c, storefront: "tw"}
	if err := p.Play(context.Background(), provider.PlayRequest{TrackIDs: []string{"s1"}}); err != nil {
		t.Fatal(err)
	}
	if opened != "music://music.apple.com/tw/album/x/1?i=s1" {
		t.Errorf("機制 B 應 open music:// 網址:%q", opened)
	}
}

// TestPlayTrackQuotesURLSafely:機制 A 用 %q 組 AppleScript 字串字面值——%q 的
// \" 跳脫與 AppleScript 字串字面值相容,是防注入的關鍵(review finding 2)。
// URL 含雙引號時,送進 runOSA 的 script 必須是跳脫過的字面值,不能讓引號提前結束字串。
func TestPlayTrackQuotesURLSafely(t *testing.T) {
	t.Setenv("CAPY_APPLE_PLAY_MECHANISM", "")
	scripts := stubOSA(t, "")
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"s1","attributes":{"name":"x","artistName":"x","albumName":"x",
"durationInMillis":1,"url":"https://music.apple.com/tw/x?i=1&q=a\"b"}}]}`)
	})
	p := &Provider{c: c, storefront: "tw"}
	if err := p.Play(context.Background(), provider.PlayRequest{TrackIDs: []string{"s1"}}); err != nil {
		t.Fatal(err)
	}
	want := `open location "music://music.apple.com/tw/x?i=1&q=a\"b"`
	if len(*scripts) != 1 || !strings.Contains((*scripts)[0], want) {
		t.Errorf("script = %v, want contains %q", *scripts, want)
	}
}
