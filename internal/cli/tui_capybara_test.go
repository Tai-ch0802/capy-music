package cli

import (
	"fmt"
	"os"
	"regexp"
	"slices"
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
	for n := 0; n < len(capyStory)+3; n++ { // 劇本的每一幀,加上演完之後停住的那幾幀
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

// 開場的動畫要真的看得到(2026-09-21;使用者要眨眼、轉耳朵、把牧草吃短)。舊版用取餘數排動作,而開場只有 6 幀:
// 第 0 幀閉著眼開場、之後再也不眨,草只短一次,耳朵不動——等於沒有動畫。所以這裡只看開場**實際會播**的那幾幀,
// 而且只用 capybaraFrame / capybaraStill / tuiIntro / tuiFrameInterval 來量:換回舊的實作,這個測試要紅。
func TestCapybaraIntroShowsBlinkEarAndEating(t *testing.T) {
	frames := int(tuiIntro / tuiFrameInterval)
	still := capybaraStill()
	ink := func(f []string) int { return len(strings.ReplaceAll(strings.Join(f, ""), " ", "")) } // 眨眼與轉耳朵不改筆畫數,只有草會
	var opened, blinked, eared bool
	bites, run, prev := 0, 0, ink(capybaraFrame(0))
	for n := 0; n < frames; n++ {
		f := capybaraFrame(n)
		if strings.Contains(strings.Join(f, ""), "o") {
			opened = true
		} else if opened {
			blinked = true // 先睜著、後來閉上:看得到的那種眨眼(閉著眼開場不算)
		}
		if f[0] != still[0] || f[1] != still[1] {
			eared = true
		}
		switch now := ink(f); {
		case now < prev:
			run++
			bites = max(bites, run)
			prev = now
		case now > prev:
			run, prev = 0, now
		}
		for _, i := range []int{3, 6, 7, 8} { // 鼻孔那一行、下巴、肚子、腳:身體不動,不然整隻會抖
			if f[i] != still[i] {
				t.Fatalf("第 %d 幀第 %d 行不該變:%q vs %q", n, i, f[i], still[i])
			}
		}
	}
	if !blinked {
		t.Error("開場要看得到眨眼:先睜著、再閉一下")
	}
	if !eared {
		t.Error("開場要看得到轉耳朵")
	}
	if bites < 3 {
		t.Errorf("開場要看得到牧草越吃越短:至少連續短三次,只短了 %d 次", bites)
	}
	for n := frames - 1; n < frames+3; n++ { // 最後一幀就是定格幀,多跳幾幀也停在那裡:定格的那一下不可以跳
		if got := strings.Join(capybaraFrame(n), "\n"); got != strings.Join(still, "\n") {
			t.Errorf("第 %d 幀要跟定格幀一樣(叼著新的一根草、睜著眼、耳朵回正):\n%s", n, got)
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
	// 口鼻的輪廓用「嘴裡沒有草」的姿勢量:草在口鼻的外面,會蓋掉右緣。
	bare := capybaraLines(capyPose{})
	right := func(l string) int { return len(strings.TrimRight(l, " ")) }
	var head []int // 從頭頂線往下、直到下巴往後收之前:每一行的右緣
	for _, l := range bare[earRow:] {
		if len(head) > 0 && right(l) < head[0] {
			break
		}
		head = append(head, right(l))
	}
	front := slices.Max(head)
	nostril := strings.LastIndex(still[eyeRow+1], ".")
	if !(earRow < eyeRow && ear < eye && eye < nostril && nostril < front-1) {
		t.Errorf("由後往前要是:耳朵(在眼睛的後上方)→ 眼睛 → 鼻孔 → 口鼻前端:ear=%d,%d eye=%d,%d nostril=%d front=%d\n%s", earRow, ear, eyeRow, eye, nostril, front, all)
	}
	// 口鼻是圓的(2026-09-21;使用者:「鼻子有點太方形了,應該要比照屁股的地方稍微圓滑一點」):上角與下角都往後收,
	// 而且跟屁股一樣是一道凸的弧(右緣的變化量只減不增)。整排 | 疊成的方臉,第一個條件就會紅。
	if len(head) < 3 || head[0] >= front || head[len(head)-1] >= front {
		t.Errorf("口鼻的上角與下角都要往後收,不可以是方的:右緣 %v", head)
	}
	for i := 2; i < len(head); i++ {
		if head[i]-head[i-1] > head[i-1]-head[i-2] {
			t.Errorf("口鼻要是一道凸的弧:右緣 %v 在第 %d 個凹進去了", head, i)
			break
		}
	}
	if eyeRow != earRow+1 {
		t.Errorf("眼睛要在頭的最上緣(耳朵的下一行),不是臉的正中央:耳朵第 %d 行、眼睛第 %d 行", earRow, eyeRow)
	}
	// 頭頂線是平的、跟背連成一條(沒有脖子):耳朵那一行就是從屁股到口鼻的整條上緣,只准有線與耳朵;
	// 它上面只准有耳朵的頂。第一版背在第 0 列、頭頂在第 2 列,中間用 `--. 掉下來——那個凹口讀起來就是脖子(review #71)。
	if top := strings.TrimSpace(still[earRow]); !regexp.MustCompile(`^_+\( \)_+$`).MatchString(top) {
		t.Errorf("上緣要是一條平線,線上只有耳朵:%q", top)
	}
	for _, l := range still[:earRow] {
		if strings.TrimSpace(l) != "_" || strings.Index(l, "_") != ear+1 {
			t.Errorf("頭頂線上面只准有耳朵的頂(在耳朵的正上方):%q", l)
		}
	}
	// 牧草不是一條直線(使用者:「太直了,看不出來是牧草」):它從口鼻的外面開始、跨兩行往上彎、頂端有穗。
	var straw []int
	for i := range still {
		if still[i] != bare[i] {
			straw = append(straw, i)
			if strings.TrimRight(still[i][:right(bare[i])], " ") != strings.TrimRight(bare[i], " ") {
				t.Errorf("草要在口鼻的外面,不可以畫進臉裡:%q", still[i])
			}
		}
	}
	if len(straw) != 2 || straw[1] != straw[0]+1 || !strings.HasSuffix(strings.TrimRight(still[straw[0]], " "), `"`) ||
		right(still[straw[0]]) <= right(still[straw[1]]) {
		t.Errorf("草要跨相鄰的兩行往前上方彎、頂端是穗:第 %v 行\n%s", straw, all)
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
	var story []string
	for n, p := range capyStory { // 劇本的每一幀的每一行,JS 那段都要有
		for _, l := range capybaraFrame(n) {
			if !strings.Contains(guide, strconv.Quote(l)) {
				t.Errorf("指南的 JS 幀少了這一行(或跟終端機不一樣):%q", l)
			}
		}
		story = append(story, fmt.Sprintf("[%d, %d, %d]", b2i(p.ear), b2i(p.shut), p.straw))
	}
	if want := "const STORY = [" + strings.Join(story, ", ") + "];"; !strings.Contains(guide, want) {
		t.Errorf("指南的劇本要跟終端機同一份([轉耳朵, 閉眼, 草的長度]):\n%s", want)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestCapyTagline(t *testing.T) {
	if got := capyTagline("spotify"); got != "capy · spotify" {
		t.Errorf("有 provider:%q", got)
	}
	if got := capyTagline(""); got != "capy" { // 沒登入時不要印孤零零的間隔點
		t.Errorf("沒有 provider 時只印 capy,得到 %q", got)
	}
}
