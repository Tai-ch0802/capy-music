//go:build darwin

package apple

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strings"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

// runOSA:測試替換點(與 provider/apple 的 StubOSAForTest 各自獨立,不跨包戳)。
var runOSA = func(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).Output()
	return strings.TrimSpace(string(out)), osaErr(err)
}

// osaErr:exec.Command(...).Output() 只把 exit code 包進 error,真正原因(如 TCC 拒絕自動化的
// -1743)在 *exec.ExitError.Stderr——抽出來取代原本的「exit status 1」,讓彙總失敗訊息分辨得出
// 是 TCC 未授權還是「JavaScript from Apple Events」沒開。
func osaErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return errors.New(strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}

const musicKitJS = `JSON.stringify({d:MusicKit.getInstance().developerToken,u:MusicKit.getInstance().musicUserToken})`

// browserScripts:AppleScript 用 `application "X" is running` 先判斷,避免 `tell` 把沒開的瀏覽器啟動起來。
var browserScripts = []struct{ name, script string }{
	{"Safari", `if application "Safari" is running then
	tell application "Safari"
		repeat with w in windows
			repeat with t in tabs of w
				if URL of t starts with "https://music.apple.com" then
					return do JavaScript "` + musicKitJS + `" in t
				end if
			end repeat
		end repeat
	end tell
end if
return ""`},
	{"Google Chrome", `if application "Google Chrome" is running then
	tell application "Google Chrome"
		repeat with w in windows
			repeat with t in tabs of w
				if URL of t starts with "https://music.apple.com" then
					return execute t javascript "` + musicKitJS + `"
				end if
			end repeat
		end repeat
	end tell
end if
return ""`},
}

// AutoWebTokens:依序試 Safari、Chrome;第一個成功的贏。全部失敗 → 一個 error 說明每個瀏覽器的原因與怎麼開啟權限。
func AutoWebTokens() (WebTokens, error) {
	var reasons []string
	for _, b := range browserScripts {
		out, err := runOSA(b.script)
		if err != nil {
			reasons = append(reasons, i18n.T("apple.auto.reason", "browser", b.name, "reason", err))
			continue
		}
		if out == "" {
			reasons = append(reasons, i18n.T("apple.auto.reason", "browser", b.name, "reason", i18n.T("apple.auto.no_tab")))
			continue
		}
		var v struct{ D, U string }
		if json.Unmarshal([]byte(out), &v) != nil || v.D == "" || v.U == "" {
			reasons = append(reasons, i18n.T("apple.auto.reason", "browser", b.name, "reason", i18n.T("apple.auto.no_tokens")))
			continue
		}
		return WebTokens{Developer: v.D, User: v.U}, nil
	}
	return WebTokens{}, i18n.Errorf("apple.err.auto_failed", "reasons", strings.Join(reasons, i18n.T("sep.clause")))
}
