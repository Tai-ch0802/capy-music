package browser

import (
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

func TestEnglishUnsupportedOS(t *testing.T) {
	orig := i18n.Current()
	i18n.Set("en")
	t.Cleanup(func() { i18n.Set(orig) })
	if _, _, err := command("linux", "https://x"); err == nil || err.Error() != "unsupported platform linux (only macOS and Windows are supported)" {
		t.Errorf("err = %v", err)
	}
}
