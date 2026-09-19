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
	const eyeRow, mouthRow = 3, 5
	base := capybaraFrame(1) // 幀 1:張眼、草伸長
	if !strings.Contains(base[eyeRow], "o") {
		t.Fatalf("幀 1 應該張著眼:%q", base[eyeRow])
	}
	if strings.Contains(capybaraFrame(capyBlinkEvery)[eyeRow], "o") {
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

// 水豚要畫成水豚(2026-09-20 重畫):舊版是正面圓臉、頭頂兩耳、橢圓鼻配兩個鼻孔——那是豬。水豚好認的是側面:
// 一隻眼睛、又小又高、緊貼著長在頭後上方的小耳朵;鼻孔在口鼻的最前上角;口鼻前端是鈍的;草從嘴邊(最前面)伸出去;
// 屁股那一側沒有尾巴。哪天有人把牠「修」回正面臉,這裡要紅。
func TestCapybaraIsASideProfile(t *testing.T) {
	still := capybaraStill()
	all := strings.Join(still, "\n")
	if strings.Count(all, "o") != 1 {
		t.Fatalf("側面只有一隻眼睛:\n%s", all)
	}
	var earRow, eyeRow int
	for i, l := range still {
		if strings.Contains(l, "( )") {
			earRow = i
		}
		if strings.Contains(l, "o") {
			eyeRow = i
		}
	}
	ear, eye := strings.Index(still[earRow], "( )"), strings.Index(still[eyeRow], "o")
	nostril, muzzle := strings.LastIndex(still[eyeRow], "."), strings.LastIndex(still[eyeRow], "|")
	if !(earRow < eyeRow && ear < eye && eye < nostril && nostril < muzzle) {
		t.Errorf("由後往前要是:耳朵(在眼睛的後上方)→ 眼睛 → 鼻孔 → 鈍的口鼻前端:ear=%d,%d eye=%d,%d nostril=%d muzzle=%d\n%s", earRow, ear, eyeRow, eye, nostril, muzzle, all)
	}
	if eyeRow != earRow+1 {
		t.Errorf("眼睛要在頭的最上緣(耳朵的下一行),不是臉的正中央:耳朵第 %d 行、眼睛第 %d 行", earRow, eyeRow)
	}
	mouth := still[eyeRow+2]
	if !strings.HasSuffix(strings.TrimRight(mouth, " "), "~") || strings.Index(mouth, "~") < muzzle {
		t.Errorf("草要從口鼻的最前面伸出去:%q", mouth)
	}
	for _, l := range still { // 沒有尾巴:屁股那一側(左邊)只有身體的輪廓
		if strings.ContainsAny(strings.TrimLeft(l, " ")[:1], "~=<") {
			t.Errorf("水豚沒有尾巴:%q", l)
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
