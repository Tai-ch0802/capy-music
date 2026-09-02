//go:build darwin

package apple

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const playbackSupported = true

// 測試替換點。
var (
	runOSA = func(script string) (string, error) {
		out, err := exec.Command("osascript", "-e", script).Output()
		return strings.TrimSpace(string(out)), err
	}
	runOpen = func(u string) error { return exec.Command("open", u).Run() }
)

const musicDevice = "music.app"

func (p *Provider) Devices(context.Context) ([]provider.Device, error) {
	return []provider.Device{{ID: musicDevice, Name: "Music.app", Type: "Computer", Active: true}}, nil
}

// stateScript:一行 tab 分隔輸出;stopped 時只回 "stopped"。
const stateScript = `tell application "Music"
	if player state is stopped then return "stopped"
	set t to current track
	return (player state as text) & tab & (name of t) & tab & (artist of t) & tab & (album of t) & tab & (duration of t as text) & tab & (player position as text)
end tell`

func (p *Provider) State(context.Context) (*provider.PlaybackState, error) {
	out, err := runOSA(stateScript)
	if err != nil {
		return nil, fmt.Errorf("osascript 失敗(Music.app 未安裝或未授權自動化?):%w", err)
	}
	if out == "stopped" || out == "" {
		return nil, nil
	}
	f := strings.Split(out, "\t")
	if len(f) < 6 {
		return nil, fmt.Errorf("osascript 輸出格式非預期:%q", out)
	}
	dur, _ := strconv.ParseFloat(f[4], 64)
	pos, _ := strconv.ParseFloat(f[5], 64)
	tr := provider.Track{Title: f[1], Artists: []string{f[2]}, Album: f[3], DurationMS: int(dur * 1000)}
	return &provider.PlaybackState{
		Playing:    f[0] == "playing",
		Track:      &tr,
		ProgressMS: int(pos * 1000),
		Device:     provider.Device{ID: musicDevice, Name: "Music.app", Type: "Computer", Active: true},
	}, nil
}

// Play:空 = resume;否則取 catalog 歌曲的 attributes.url(不自己拼)並以 music:// 交給 Music.app。
// 機制 A(預設)AppleScript open location;機制 B(CAPY_APPLE_PLAY_MECHANISM=open)shell open。
// 兩者何者可靠由附錄 C-4 真實驗收決定,之後再硬編。
func (p *Provider) Play(ctx context.Context, req provider.PlayRequest) error {
	if len(req.TrackIDs) == 0 {
		_, err := runOSA(`tell application "Music" to play`)
		return err
	}
	if p.c == nil {
		return errors.New("apple provider 未初始化 client")
	}
	_, songURL, err := p.c.Song(ctx, p.storefront, req.TrackIDs[0]) // ponytail: 先播第一首;佇列多首待 Enqueue(P4+)
	if err != nil {
		return err
	}
	if songURL == "" {
		return fmt.Errorf("Apple 未回傳歌曲 URL(id=%s)", req.TrackIDs[0])
	}
	u := strings.Replace(songURL, "https://", "music://", 1)
	if os.Getenv("CAPY_APPLE_PLAY_MECHANISM") == "open" {
		return runOpen(u)
	}
	_, err = runOSA(fmt.Sprintf(`tell application "Music" to open location %q`, u))
	return err
}

func (p *Provider) Pause(context.Context) error {
	_, err := runOSA(`tell application "Music" to pause`)
	return err
}

func (p *Provider) Next(context.Context) error {
	_, err := runOSA(`tell application "Music" to next track`)
	return err
}

func (p *Provider) Prev(context.Context) error {
	_, err := runOSA(`tell application "Music" to previous track`)
	return err
}
