package browser

import "testing"

func TestCommandPerOS(t *testing.T) {
	name, args, err := command("darwin", "https://x")
	if err != nil || name != "open" || len(args) != 1 || args[0] != "https://x" {
		t.Errorf("darwin: (%q, %v, %v)", name, args, err)
	}

	name, args, err = command("windows", "https://x")
	if err != nil || name != "rundll32" || len(args) != 2 ||
		args[0] != "url.dll,FileProtocolHandler" || args[1] != "https://x" {
		t.Errorf("windows: (%q, %v, %v)", name, args, err)
	}

	if _, _, err = command("linux", "https://x"); err == nil {
		t.Error("linux 非目標,應回錯誤")
	}
}
