package cli

// 水豚橫幅。設計約束(不是裝飾,是為了畫面不抖):
//   - **只用 ASCII**。方框繪製字元(U+2500 區塊)與 ● ▬ ≋ 全都是 East Asian Ambiguous:
//     在 CJK 語系的終端機(iTerm2 的 ambiguous-width = double、Windows Terminal 配 CJK 字型、
//     tmux -u 加中文 locale)寬度變成 2,矩形會垮成鋸齒,眨眼那一行還會左右抖。
//     而 ansi.StringWidth 一律當 1,量不出這件事——所以測試改成強制純 ASCII(PR #41 review)。
//   - 每一幀的行數與長度都一樣;身體固定,只有眼睛列與嘴／草列會變。
//
// 動作:平視前方(兩眼同高、置中 = 看著使用者),嘴角橫叼一根牧草,一邊嚼一邊偶爾眨眼。
const (
	capyBlinkEvery = 12 // 幀:約 4 秒眨一次
	capyChewEvery  = 3  // 幀:約 1 秒嚼一口
)

var (
	capyHead = []string{
		"    __            __      ",
		"   /  \\__________/  \\     ",
		"  |                  |    ",
	}
	capyEyesOpen  = "  |   O          O   |    "
	capyEyesShut  = "  |   -          -   |    "
	capyMidriff   = "  |                  |    "
	capyBrow      = "  |      ______      |    "
	capyMouthOut  = "  |     (__..__)~~~~~~~~~ " // 草從嘴角伸出臉外
	capyMouthChew = "  |     (__--__)~~~~~~    " // 嚼一口:鼻孔瞇起來,草短一截
	capyChin      = "   \\________________/     "
)

// capybaraFrame:第 n 幀(n 單調遞增,由 tick 推)。
func capybaraFrame(n int) []string {
	eyes := capyEyesOpen
	if n%capyBlinkEvery == 0 {
		eyes = capyEyesShut
	}
	mouth := capyMouthOut
	if (n/capyChewEvery)%2 == 1 {
		mouth = capyMouthChew
	}
	out := make([]string, 0, len(capyHead)+5)
	out = append(out, capyHead...)
	return append(out, eyes, capyMidriff, capyBrow, mouth, capyChin)
}

// capybaraWidth:橫幅要的欄數。全部是 ASCII,所以位元組數就是顯示寬度。
func capybaraWidth() int {
	w := 0
	for _, line := range capybaraFrame(0) {
		w = max(w, len(line))
	}
	return w
}

// capyOneLine:終端機窄到放不下橫幅時的替代品(同一隻水豚,壓成一行)。同樣只用 ASCII。
const capyOneLine = "(O  O)~~ capy"

// capyTagline:招牌。還沒登入時 provider 是空的,不要印出孤零零的間隔點。
func capyTagline(prov string) string {
	if prov == "" {
		return "capy"
	}
	return "capy · " + prov
}
