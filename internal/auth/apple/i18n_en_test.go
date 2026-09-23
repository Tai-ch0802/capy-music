package apple

import (
	"encoding/base64"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

func setEnglish(t *testing.T) {
	t.Helper()
	orig := i18n.Current()
	if !i18n.Set("en") {
		t.Fatal("en 不在支援清單")
	}
	t.Cleanup(func() { i18n.Set(orig) })
}

func TestEnglishJWTErrors(t *testing.T) {
	setEnglish(t)
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for _, c := range []struct{ tok, want string }{
		{"abc", "not a JWT: expected three dot-separated parts, starting with eyJ"},
		{"eyJ.!!!.sig", "the JWT payload isn't base64url: illegal base64 data at input byte 0"},
		{"eyJ." + b64("nope") + ".sig", "the JWT payload isn't JSON: invalid character 'o' in literal null (expecting 'u')"},
		{"eyJ." + b64(`{"a":1}`) + ".sig", "the JWT has no exp"},
	} {
		if _, err := JWTExp(c.tok); err == nil || err.Error() != c.want {
			t.Errorf("JWTExp(%q) = %v, want %q", c.tok, err, c.want)
		}
	}
}
