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
	stubOSA(t, "playing\t派對動物\t五月天\t自傳\t227.5\t61.2")
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
