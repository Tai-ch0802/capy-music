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

// StubOSAForTest 把 runOSA 換成記錄器(僅供測試;darwin 專用)。
// 跨套件測試鉤子不能放 _test.go(cli 套件的測試需要呼叫它,但 _test.go 的匯出只在同套件內可見),
// 故放在一般檔案裡,以 *ForTest 命名清楚標示用途(同 P1 SetTestDir 討論;若未來認為應避免,
// 替代方案是 CAPY_OSA_SCRIPT_LOG=<path> 環境變數把 script 寫檔)。
func StubOSAForTest(t interface{ Cleanup(func()) }) *[]string {
	var scripts []string
	orig := runOSA
	runOSA = func(script string) (string, error) { scripts = append(scripts, script); return "", nil }
	t.Cleanup(func() { runOSA = orig })
	return &scripts
}

func (p *Provider) Devices(context.Context) ([]provider.Device, error) {
	return []provider.Device{{ID: musicDevice, Name: "Music.app", Type: "Computer", Active: true}}, nil
}

// stateScript:一行 tab 分隔輸出;stopped 時只回 "stopped"。
// 時長/進度在 AppleScript 端先算成整數毫秒(而非 "as text" 後在 Go 端乘 1000)——
// 避開 macOS 非 en-US locale(如 de/fr)把小數點印成 "," 導致 Go 端解析失敗的問題
// (review finding 1;"," 不是合法 Go float,且原本錯誤被 "_" 吞掉,靜默變 0)。
const stateScript = `tell application "Music"
	if player state is stopped then return "stopped"
	set t to current track
	return (player state as text) & tab & (name of t) & tab & (artist of t) & tab & (album of t) & tab & (((duration of t) * 1000) as integer) & tab & (((player position) * 1000) as integer)
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
	dur, err1 := strconv.Atoi(f[4])
	pos, err2 := strconv.Atoi(f[5])
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("osascript 數值欄位無法解析:%q", out)
	}
	tr := provider.Track{Title: f[1], Artists: []string{f[2]}, Album: f[3], DurationMS: dur}
	return &provider.PlaybackState{
		Playing:    f[0] == "playing",
		Track:      &tr,
		ProgressMS: pos,
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
	// %q 的 \" 與 \\ 跳脫與 AppleScript 字串字面值一致;改成 "%s" 會開啟 AppleScript 注入(reviewer 實測)。
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
