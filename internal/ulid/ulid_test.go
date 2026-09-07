package ulid

import (
	"strings"
	"testing"
	"time"
)

func TestNewShapeAndRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 7, 12, 0, 0, 123_000_000, time.UTC)
	Now = func() time.Time { return at }
	t.Cleanup(func() { Now = time.Now })
	a, b := New(), New()
	for _, s := range []string{a, b} {
		if len(s) != 26 || strings.Trim(s, alphabet) != "" {
			t.Fatalf("ULID 應為 26 個 Crockford base32 字元:%q", s)
		}
	}
	if a == b {
		t.Fatal("同毫秒兩個 ULID 隨機段應不同")
	}
	if a[:10] != b[:10] {
		t.Fatalf("同毫秒時戳段(前 10 字)應相同:%s vs %s", a, b)
	}
	got, err := Time(a)
	if err != nil || !got.Equal(at.Truncate(time.Millisecond)) {
		t.Fatalf("Time 應解回毫秒時戳:%v %v", got, err)
	}
}

func TestOrderFollowsTime(t *testing.T) {
	at := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	Now = func() time.Time { return at }
	t.Cleanup(func() { Now = time.Now })
	first := New()
	Now = func() time.Time { return at.Add(time.Second) }
	second := New()
	if !(first < second) {
		t.Fatalf("字典序應跟時間走:%s !< %s", first, second)
	}
}

func TestTimeRejectsBadInput(t *testing.T) {
	for _, s := range []string{"", "short", strings.Repeat("U", 26), strings.Repeat("Z", 26)} { // U 不在字母表;全 Z 超出 128 bit
		if _, err := Time(s); err == nil {
			t.Errorf("%q 應被拒", s)
		}
	}
}
