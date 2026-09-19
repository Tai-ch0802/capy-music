package cli

import (
	"os"
	"regexp"
	"strconv"
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
	const eyeRow, mouthRow = 2, 5
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
	// 頭頂線是平的、跟背連成一條(沒有脖子):耳朵那一行就是從屁股到口鼻的整條上緣,只准有線、耳朵、口鼻的圓角;
	// 它上面只准有耳朵的頂。第一版背在第 0 列、頭頂在第 2 列,中間用 `--. 掉下來——那個凹口讀起來就是脖子(review #71)。
	if top := strings.TrimSpace(still[earRow]); !regexp.MustCompile(`^_+\( \)_+\.$`).MatchString(top) {
		t.Errorf("上緣要是一條平線,線上只有耳朵,最前面收一個圓角:%q", top)
	}
	for _, l := range still[:earRow] {
		if strings.TrimSpace(l) != "_" || strings.Index(l, "_") != ear+1 {
			t.Errorf("頭頂線上面只准有耳朵的頂(在耳朵的正上方):%q", l)
		}
	}
	var mouth string
	for _, l := range still {
		if strings.Contains(l, "~") {
			mouth = l
		}
	}
	if !strings.HasSuffix(strings.TrimRight(mouth, " "), "~") || strings.Index(mouth, "~") <= muzzle {
		t.Errorf("草要從口鼻的外面才開始、伸到最前面:%q", mouth)
	}
	// 沒有尾巴:屁股是一道凸的弧,所以左緣的欄位由上到下先往左、再往右,而且變化量只增不減。
	// 屁股上多出任何一筆(不管用哪個字元畫),那一行的左緣就會突然凸出去,差分就不單調了。
	var left []int
	for _, l := range still[earRow:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		left = append(left, len(l)-len(strings.TrimLeft(l, " ")))
	}
	for i := 2; i < len(left); i++ {
		if left[i]-left[i-1] < left[i-1]-left[i-2] {
			t.Errorf("水豚沒有尾巴:屁股那一側要是一道凸的弧,左緣欄位 %v 在第 %d 個凸出去了\n%s", left, i, all)
			break
		}
	}
}

// 使用指南是水豚的第三份拷貝(開頭的 <pre>、TUI 示意、會動的那段 JS),沒有共用執行期,只能逐行比(review #71:
// tui_capybara.go 與 console.js 由 TestWebCapybaraMatchesTUI 釘住,指南那份以前只靠人眼)。
func TestGuideCapybaraMatchesTUI(t *testing.T) {
	b, err := os.ReadFile("../../docs/guide.html")
	if err != nil {
		t.Fatal(err)
	}
	guide := strings.ReplaceAll(string(b), "\r\n", "\n")
	var still []string
	for _, l := range capybaraStill() {
		still = append(still, strings.TrimRight(l, " "))
	}
	if n := strings.Count(guide, strings.Join(still, "\n")); n != 2 {
		t.Errorf("指南裡靜態的水豚要有兩隻(開頭與 TUI 示意)、都跟終端機的定格幀一樣,找到 %d 隻", n)
	}
	for _, n := range []int{0, 1, capyChewEvery} { // 閉眼、睜眼、嚼一口:JS 那段的每一行
		for _, l := range capybaraFrame(n) {
			if !strings.Contains(guide, strconv.Quote(l)) {
				t.Errorf("指南的 JS 幀少了這一行(或跟終端機不一樣):%q", l)
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
