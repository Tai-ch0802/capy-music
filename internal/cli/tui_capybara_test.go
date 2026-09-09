package cli

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// 每一幀都要是同寬同高的矩形,不然橫幅會隨著眨眼／嚼草抖動。加字元前先跑這條。
func TestCapybaraFramesAreRectangular(t *testing.T) {
	first := capybaraFrame(0)
	want := ansi.StringWidth(first[0])
	if want == 0 {
		t.Fatal("第一行是空的")
	}
	for n := 0; n < capyBlinkEvery*capyChewEvery*2; n++ {
		f := capybaraFrame(n)
		if len(f) != len(first) {
			t.Fatalf("第 %d 幀 %d 行,第 0 幀 %d 行", n, len(f), len(first))
		}
		for i, line := range f {
			if w := ansi.StringWidth(line); w != want {
				t.Errorf("第 %d 幀第 %d 行寬度 %d,要 %d:%q", n, i, w, want, line)
			}
		}
	}
	if capybaraWidth() != want {
		t.Errorf("capybaraWidth() = %d,要 %d", capybaraWidth(), want)
	}
}

// 眨眼與嚼草各自照週期走,而且身體(眼睛與嘴以外的行)每一幀都一樣。
func TestCapybaraAnimates(t *testing.T) {
	const eyeRow, mouthRow = 3, 6
	base := capybaraFrame(1) // 幀 1:張眼、草伸長
	if !strings.Contains(base[eyeRow], "●") {
		t.Fatalf("幀 1 應該張著眼:%q", base[eyeRow])
	}
	if !strings.Contains(capybaraFrame(capyBlinkEvery)[eyeRow], "▬") {
		t.Error("第 capyBlinkEvery 幀應該眨眼")
	}
	if capybaraFrame(capyChewEvery)[mouthRow] == base[mouthRow] {
		t.Error("嚼草的那一幀嘴／草應該不一樣")
	}
	// 身體不動:除了眼睛列與嘴列,其他行在任何一幀都相同
	for n := 0; n < capyBlinkEvery*2; n++ {
		f := capybaraFrame(n)
		for i := range f {
			if i == eyeRow || i == mouthRow {
				continue
			}
			if f[i] != base[i] {
				t.Fatalf("第 %d 幀第 %d 行不該變:%q vs %q", n, i, f[i], base[i])
			}
		}
	}
}
