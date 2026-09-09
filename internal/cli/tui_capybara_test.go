package cli

import (
	"strings"
	"testing"
)

// 橫幅只能用 ASCII。方框繪製字元與 ● ≋ 那類符號是 East Asian Ambiguous:在 CJK 終端機寬度是 2,
// 矩形會垮、眨眼那行會左右抖,而 ansi.StringWidth 一律當 1 所以量不到(PR #41 review 實測)。
// 純 ASCII 的話位元組數就是顯示寬度,不管終端機怎麼設定都成立。
func TestCapybaraFramesAreASCIIRectangles(t *testing.T) {
	first := capybaraFrame(0)
	want := len(first[0])
	if want == 0 {
		t.Fatal("第一行是空的")
	}
	for n := 0; n < capyBlinkEvery*capyChewEvery*2; n++ {
		f := capybaraFrame(n)
		if len(f) != len(first) {
			t.Fatalf("第 %d 幀 %d 行,第 0 幀 %d 行", n, len(f), len(first))
		}
		for i, line := range f {
			for _, r := range line {
				if r > 127 {
					t.Fatalf("第 %d 幀第 %d 行有非 ASCII 字元 %q:%q", n, i, r, line)
				}
			}
			if len(line) != want {
				t.Errorf("第 %d 幀第 %d 行長度 %d,要 %d:%q", n, i, len(line), want, line)
			}
		}
	}
	if capybaraWidth() != want {
		t.Errorf("capybaraWidth() = %d,要 %d", capybaraWidth(), want)
	}
	for _, r := range capyOneLine { // 窄螢幕的替代品同理
		if r > 127 {
			t.Errorf("capyOneLine 有非 ASCII 字元 %q", r)
		}
	}
}

// 眨眼與嚼草各自照週期走,而且身體(眼睛與嘴以外的行)每一幀都一樣。
func TestCapybaraAnimates(t *testing.T) {
	const eyeRow, mouthRow = 3, 6
	base := capybaraFrame(1) // 幀 1:張眼、草伸長
	if !strings.Contains(base[eyeRow], "O") {
		t.Fatalf("幀 1 應該張著眼:%q", base[eyeRow])
	}
	if strings.Contains(capybaraFrame(capyBlinkEvery)[eyeRow], "O") {
		t.Error("第 capyBlinkEvery 幀應該眨眼")
	}
	if capybaraFrame(capyChewEvery)[mouthRow] == base[mouthRow] {
		t.Error("嚼草的那一幀嘴／草應該不一樣")
	}
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

func TestCapyTagline(t *testing.T) {
	if got := capyTagline("spotify"); got != "capy · spotify" {
		t.Errorf("有 provider:%q", got)
	}
	if got := capyTagline(""); got != "capy" { // 沒登入時不要印孤零零的間隔點
		t.Errorf("沒有 provider 時只印 capy,得到 %q", got)
	}
}
