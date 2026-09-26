//go:build darwin

package apple

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const playbackSupported = true

// 測試替換點。runOSA 的 args 經 argv 傳進腳本的 `on run argv`,前面一定加 "--":值(歌名、歌手)不進腳本原始碼、
// 不必跳脫;沒有 "--" 的話,開頭是 "-" 的值會被 osascript 當成旗標,"-e" 甚至會被當成更多原始碼(2026-09-27 實測)。
var (
	runOSA = func(script string, args ...string) (string, error) {
		out, err := exec.Command("osascript", append([]string{"-e", script, "--"}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	runOpen = func(u string) error { return exec.Command("open", u).Run() }
)

const musicDevice = "music.app"

// StubOSAForTest 把 runOSA 與 runOpen 一起換成記錄器(僅供測試;darwin 專用)。每個 osascript 回空字串;
// runOpen 記成 "open <網址>" 放進同一個切片。兩個一定一起換:Play 找不到資料庫裡的那首就會 open music://,
// 只換 runOSA 的話,測試會在開發機與 CI 上真的打開 Music.app。
// 跨套件測試鉤子不能放 _test.go(cli 套件的測試需要呼叫它,但 _test.go 的匯出只在同套件內可見),
// 故放在一般檔案裡,以 *ForTest 命名清楚標示用途(同 P1 SetTestDir 討論)。
func StubOSAForTest(t interface{ Cleanup(func()) }) *[]string {
	var calls []string
	origOSA, origOpen := runOSA, runOpen
	runOSA = func(script string, _ ...string) (string, error) { calls = append(calls, script); return "", nil }
	runOpen = func(u string) error { calls = append(calls, "open "+u); return nil }
	t.Cleanup(func() { runOSA, runOpen = origOSA, origOpen })
	return &calls
}

func (p *Provider) Devices(context.Context) ([]provider.Device, error) {
	return []provider.Device{{ID: musicDevice, Name: "Music.app", Type: "Computer", Active: true}}, nil
}

// stateScript:一行 tab 分隔輸出;stopped 時只回 "stopped"。
// 時長/進度在 AppleScript 端先算成整數毫秒(而非 "as text" 後在 Go 端乘 1000)——
// 避開 macOS 非 en-US locale(如 de/fr)把小數點印成 "," 導致 Go 端解析失敗的問題
// (review finding 1;"," 不是合法 Go float,且原本錯誤被 "_" 吞掉,靜默變 0)。
// 先用 `application "Music" is running` 判斷(不進 tell 區塊就不會把沒開的 Music.app 啟動起來;
// now --watch 每 2 秒輪詢一次,若每次都啟動 app 會很糟)。
const stateScript = `if not (application "Music" is running) then return "not running"
tell application "Music"
	if player state is stopped then return "stopped"
	set t to current track
	return (player state as text) & tab & (name of t) & tab & (artist of t) & tab & (album of t) & tab & (((duration of t) * 1000) as integer) & tab & (((player position) * 1000) as integer)
end tell`

func (p *Provider) State(context.Context) (*provider.PlaybackState, error) {
	out, err := runOSA(stateScript)
	if err != nil {
		return nil, i18n.Errorf("apple.player.err.osascript_failed", "err", err)
	}
	if out == "not running" {
		return nil, ErrNotRunning
	}
	if out == "stopped" || out == "" {
		return nil, nil
	}
	f := strings.Split(out, "\t")
	if len(f) < 6 {
		return nil, i18n.Errorf("apple.player.err.unexpected_output", "output", strconv.Quote(out))
	}
	dur, err1 := strconv.Atoi(f[4])
	pos, err2 := strconv.Atoi(f[5])
	if err1 != nil || err2 != nil {
		return nil, i18n.Errorf("apple.player.err.bad_number", "output", strconv.Quote(out))
	}
	tr := provider.Track{Title: f[1], Artists: []string{f[2]}, Album: f[3], DurationMS: dur}
	return &provider.PlaybackState{
		Playing:    f[0] == "playing",
		Track:      &tr,
		ProgressMS: pos,
		Device:     provider.Device{ID: musicDevice, Name: "Music.app", Type: "Computer", Active: true},
	}, nil
}

// Play:空 = resume;帶 TrackIDs 的只播第一首(Music.app 不能排佇列,沒有 CapPlayQueue)。
// 決策 52:AppleScript 的 play 只收資料庫裡的曲目,純 AppleScript 播不了目錄歌曲,深層連結也都不會播(計畫 2026-09-24 §2.2)。
// 所以先在資料庫裡找唯一一首對得上的來播,並確認真的開始播;找不到、有好幾份、或沒確認到,就用單曲網址在 Music.app 打開
// 並標出那一首(這一步不需要自動化權限),回 *provider.OpenedError——CLI 照實說沒播、不印 ▶、exit 0。
// 單曲網址是自己拼的:取代 P2「一律取 attributes.url」,因為單曲網址 2026-09-24 驗過會標亮那一首,專輯網址不會。
func (p *Provider) Play(ctx context.Context, req provider.PlayRequest) error {
	if req.PlaylistID != "" { // R4:不自創 music:// 清單 URL
		return i18n.Errorf("apple.player.err.playlist_unsupported", "err", provider.ErrNotSupported)
	}
	if len(req.TrackIDs) == 0 {
		_, err := runOSA(`tell application "Music" to play`)
		return err
	}
	if p.c == nil {
		return i18n.Errorf("apple.player.err.no_client")
	}
	id := req.TrackIDs[0]
	tr, err := p.GetTrack(ctx, id) // 同時確認 id 存在;token 過期照實回錯,不拿打開頁面蓋過去
	if err != nil {
		return err
	}
	// osascript 的錯(沒有自動化權限 -1743、Music.app 剛啟動、資料庫那份已下架)一律退回打開頁面,不當失敗;錯誤訊息是在地化的,不解析。
	// 但每一步之前先看 ctx:終端機的 Ctrl-C 會連 osascript 一起殺掉,那不是「沒找到」——使用者喊停之後不再開始播放、不再打開 Music.app。
	// web 的「中止」不會殺掉 osascript(runOSA 不吃 ctx):查資料庫那一步照樣跑完,但下一步之前的檢查一樣擋得住;確認迴圈裡的 play
	// 在腳本第一行就送出了,殺掉也收不回,所以這段不吃取消(同 web_run.go 的 webExitReason)。
	if pid := libraryMatch(tr); pid != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		if out, err := runOSA(playLibraryScript, pid); err == nil && out == "pid" {
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runOpen("music://music.apple.com/" + url.PathEscape(p.storefront) + "/song/" + url.PathEscape(id)); err != nil {
		return err
	}
	return &provider.OpenedError{Label: songLabel(tr)}
}

// songLabel:「歌名 — 歌手」,給 play --id 的那句話用(CLI 手上只有 id)。
func songLabel(tr provider.Track) string {
	if tr.Title == "" || len(tr.Artists) == 0 {
		return tr.Title
	}
	return tr.Title + " — " + tr.Artists[0]
}

// libraryMatchScript:在資料庫裡找歌名與歌手都相同的曲目,每首一行「persistent ID、時長(毫秒)、專輯是否相同(0/1)」。
// whose 只放文字條件:這時 Music 比對會忽略大小寫、變音符號、NFC/NFD、全半形與 NBSP;加上非文字條件(media kind)會切成逐碼位
// 比對,NFD 的歌名就對不到了,所以 media kind 放到迴圈裡看(2026-09-27 在 8318 首的資料庫上實測)。時長在 AppleScript 端先轉成
// 整數毫秒,理由同 stateScript(非 en-US locale 的小數點)。專輯只回 0/1:不讓任何自由文字欄位(可能含 tab、換行)進到要解析的輸出。
// 腳本裡不寫註解:Go raw string 裡的 AppleScript 註解會被 i18n 的中文字面守門測試當成字串。
const libraryMatchScript = `on run argv
	set {wantName, wantArtist, wantAlbum} to argv
	tell application "Music"
		set ts to (every track of library playlist 1 whose name is wantName and artist is wantArtist)
		set out to {}
		repeat with t in ts
			set d to duration of t
			if d is not missing value and media kind of t is song then
				ignoring diacriticals
					set sameAlbum to (album of t is wantAlbum)
				end ignoring
				set end of out to (persistent ID of t) & tab & (((d * 1000) as integer) as text) & tab & (sameAlbum as integer)
			end if
		end repeat
	end tell
	set AppleScript's text item delimiters to linefeed
	return out as text
end run`

// playLibraryScript:播資料庫裡的那一首,並確認真的開始播了(播放中、而且目前曲目就是它);最多等 5 秒(串流要緩衝)。
// 2026-09-27 真機:訂閱的雲端曲目(shared track)約 0.8 秒就回 pid。不用歌名補判:同名的另一首還在播時會誤報 ▶。
// 變數別叫 st:那是保留的序數字尾,osacompile 會報 -2741。
const playLibraryScript = `on run argv
	set wantPID to item 1 of argv
	tell application "Music"
		play (first track of library playlist 1 whose persistent ID is wantPID)
		repeat 25 times
			if player state is playing then
				if persistent ID of current track is wantPID then return "pid"
			end if
			delay 0.2
		end repeat
		return "unconfirmed"
	end tell
end run`

// libraryMatch:資料庫裡對得上的唯一一首的 persistent ID,對不到就回 ""(計畫 §2.2 的兩步篩選)。
// 先看時長(與 catalog 差 2 秒以內),剩一首就是它——專輯名可能不同(catalog《Overexposed》、資料庫《Overexposed (Deluxe Version)》);
// 剩好幾首再看專輯,剛好一首才算。其他(沒有、同名同專輯同長度的好幾份)一律回 "",交給打開頁面。
// 沒有歌手或時長就不找:空歌手會對上所有沒有歌手的曲目,沒有時長就沒有東西可以比。
func libraryMatch(tr provider.Track) string {
	if tr.Title == "" || len(tr.Artists) == 0 || tr.Artists[0] == "" || tr.DurationMS <= 0 {
		return ""
	}
	out, err := runOSA(libraryMatchScript, tr.Title, tr.Artists[0], tr.Album)
	if err != nil || out == "" {
		return ""
	}
	var near, sameAlbum []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		ms, err := strconv.Atoi(f[1])
		if err != nil || ms < tr.DurationMS-2000 || ms > tr.DurationMS+2000 {
			continue
		}
		near = append(near, f[0])
		if f[2] == "1" {
			sameAlbum = append(sameAlbum, f[0])
		}
	}
	switch {
	case len(near) == 1:
		return near[0]
	case len(sameAlbum) == 1:
		return sameAlbum[0]
	}
	return ""
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

// Seek / SetVolume:Music.app 的 player position 單位是秒、sound volume 是 0-100。
// 位置取整到秒(mm:ss 本來就是秒精度),順便避開小數點在非 en-US locale 的格式疑慮(見 stateScript 的註解)。
func (p *Provider) Seek(_ context.Context, posMS int) error {
	_, err := runOSA(fmt.Sprintf(`tell application "Music" to set player position to %d`, (posMS+500)/1000))
	return err
}

func (p *Provider) SetVolume(_ context.Context, pct int) error {
	_, err := runOSA(fmt.Sprintf(`tell application "Music" to set sound volume to %d`, pct))
	return err
}
