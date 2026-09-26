//go:build darwin

package apple

import (
	"context"
	"errors"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 英文模式(T2d):player_darwin.go 的錯誤。AppleScript 原始碼不翻,只翻給使用者看的訊息。
func TestEnglishPlayerErrors(t *testing.T) {
	withEnglish(t)
	ctx := context.Background()

	orig := runOSA
	t.Cleanup(func() { runOSA = orig })
	runOSA = func(string, ...string) (string, error) { return "", errors.New("exit status 1") }
	if _, err := (&Provider{}).State(ctx); err == nil || err.Error() != "osascript failed (Music.app not installed, or automation not allowed?): exit status 1" {
		t.Errorf("osascript 失敗:%v", err)
	}
	runOSA = func(string, ...string) (string, error) { return "playing\tx", nil }
	if _, err := (&Provider{}).State(ctx); err == nil || err.Error() != `unexpected osascript output: "playing\tx"` {
		t.Errorf("格式非預期:%v", err)
	}
	runOSA = func(string, ...string) (string, error) { return "playing\ta\tb\tc\t1,5\t0", nil }
	if _, err := (&Provider{}).State(ctx); err == nil || err.Error() != `can't parse the numeric fields in osascript output: "playing\ta\tb\tc\t1,5\t0"` {
		t.Errorf("數值欄位:%v", err)
	}
	runOSA = func(string, ...string) (string, error) { return "not running", nil }
	if _, err := (&Provider{}).State(ctx); !errors.Is(err, provider.ErrPlayerNotRunning) || err.Error() != "Music.app is not running (capy play starts it): the player is not running" {
		t.Errorf("未執行:%v", err)
	}

	scripts := stubOSA(t, "")
	err := (&Provider{}).Play(ctx, provider.PlayRequest{PlaylistID: "p.1"})
	if !errors.Is(err, provider.ErrNotSupported) || err.Error() != "capy can't start an Apple Music playlist yet — list its tracks with capy pl show, then use play --id: this platform doesn't support this operation" {
		t.Errorf("清單播放:%v", err)
	}
	if err := (&Provider{}).Play(ctx, provider.PlayRequest{TrackIDs: []string{"s1"}}); err == nil || err.Error() != "apple provider has no client" {
		t.Errorf("沒有 client:%v", err)
	}
	if len(*scripts) != 0 {
		t.Errorf("出錯的路徑不得執行 AppleScript:%v", *scripts)
	}
}
