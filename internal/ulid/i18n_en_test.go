package ulid

import (
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

func TestEnglishErrors(t *testing.T) {
	prev := i18n.Current()
	i18n.Set("en")
	t.Cleanup(func() { i18n.Set(prev) })
	for in, want := range map[string]string{
		"short":                      "ulid: must be 26 characters long",
		"0000000000000000000000000U": "ulid: contains a character outside Crockford base32",
		"80000000000000000000000000": "ulid: exceeds 128 bits",
	} {
		if _, err := Time(in); err == nil || err.Error() != want {
			t.Errorf("Time(%q) = %v,要 %q", in, err, want)
		}
	}
}
