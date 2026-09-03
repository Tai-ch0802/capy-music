//go:build darwin

package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// checkOSA:驗 osascript 能對 Music.app 下指令。注意:這會啟動 Music.app,
// 首次會跳「允許 capy 控制 Music」的自動化權限對話框。
func init() {
	checkOSA = func(ctx context.Context) (string, error) {
		out, err := exec.CommandContext(ctx, "osascript", "-e", `tell application "Music" to return "ok"`).Output()
		if err != nil || strings.TrimSpace(string(out)) != "ok" {
			return "", fmt.Errorf("osascript 無法控制 Music.app(未安裝或未授權自動化,到 系統設定 → 隱私權 → 自動化 允許終端機控制 Music):%v", err)
		}
		return "Music.app 可控制", nil
	}
}
