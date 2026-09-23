//go:build darwin

package apple

import (
	"errors"
	"testing"
)

// 隱藏的 --auto(開發者自負):彙總失敗訊息的英文版。
func TestEnglishAutoWebTokensFailure(t *testing.T) {
	setEnglish(t)
	stubOSA(t, osaResult{err: errors.New("Not authorized to send Apple events to Safari. (-1743)")}, osaResult{})
	_, err := AutoWebTokens()
	want := "automatic extraction failed (Safari: Not authorized to send Apple events to Safari. (-1743); Google Chrome: not running, or no music.apple.com tab open). It needs a logged-in music.apple.com tab open, and the browser has to allow JavaScript from Apple Events (Safari: Develop menu; Chrome: View → Developer)"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
}

func TestEnglishAutoWebTokensNoTokens(t *testing.T) {
	setEnglish(t)
	stubOSA(t, osaResult{out: `{"d":"x"}`}, osaResult{out: "not json"})
	_, err := AutoWebTokens()
	want := "automatic extraction failed (Safari: the page didn't return both tokens (not logged in?); Google Chrome: the page didn't return both tokens (not logged in?)). It needs a logged-in music.apple.com tab open, and the browser has to allow JavaScript from Apple Events (Safari: Develop menu; Chrome: View → Developer)"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
}
