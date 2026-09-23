//go:build darwin

package cli

import (
	"context"
	"os/exec"
	"strings"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

// checkOSA:驗 osascript 能對 Music.app 下指令。注意:這會啟動 Music.app,
// 首次會跳「允許 capy 控制 Music」的自動化權限對話框。
func init() {
	checkOSA = func(ctx context.Context) (string, error) {
		out, err := exec.CommandContext(ctx, "osascript", "-e", `tell application "Music" to return "ok"`).Output()
		if err != nil || strings.TrimSpace(string(out)) != "ok" {
			return "", i18n.Errorf("doctor.osa.err", "err", err)
		}
		return i18n.T("doctor.osa.ok"), nil
	}
}
