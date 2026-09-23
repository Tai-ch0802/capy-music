// Package browser 在預設瀏覽器開啟 URL。Linux 非目標(CLAUDE.md)。
package browser

import (
	"os/exec"
	"runtime"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

// command 拆成純函式方便測試。
func command(goos, url string) (name string, args []string, err error) {
	switch goos {
	case "darwin":
		return "open", []string{url}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}, nil
	default:
		return "", nil, i18n.Errorf("browser.err.unsupported_os", "os", goos)
	}
}

func Open(url string) error {
	name, args, err := command(runtime.GOOS, url)
	if err != nil {
		return err
	}
	return exec.Command(name, args...).Start()
}
