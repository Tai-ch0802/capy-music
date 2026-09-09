package cli

import "strings"

// 水豚橫幅。設計約束(不是裝飾,是為了畫面不抖):
//   - 每一幀的行數、每一行的顯示寬度都一樣;身體固定,只有眼睛列與嘴／草列會變。
//   - 只用等寬字型下寬度為 1 的字元(方框繪製 + ● ▬ ▄ ≋),Windows Terminal 的預設字型都有。
//     TestCapybaraFramesAreRectangular 會逐格量,加字前先跑它。
//
// 動作:平視前方(兩眼同高、瞳孔置中 = 看著使用者),嘴角橫叼一根牧草,一邊嚼一邊偶爾眨眼。
const (
	capyBlinkEvery = 12 // 幀:約 4 秒眨一次
	capyChewEvery  = 3  // 幀:約 1 秒嚼一口
)

var (
	capyHead = []string{
		"    ╭──╮            ╭──╮      ",
		"  ╭─╯  ╰────────────╯  ╰─╮    ",
		"  │                      │    ",
	}
	capyEyesOpen  = "  │    ●            ●    │    "
	capyEyesShut  = "  │    ▬            ▬    │    "
	capyMidriff   = "  │                      │    "
	capyNose      = "  │         ▄▄▄▄         │    "
	capyMouthOut  = "  │        ╰───≋≋≋≋≋≋≋≋≋≋≋≋   " // 草從嘴角穿出臉緣
	capyMouthChew = "  │        ╰▄──≋≋≋≋≋≋≋≋≋≋≋    " // 嚼一口:嘴動了,草短一截
	capyChin      = "  ╰──────────────────────╯    "
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
	return append(out, eyes, capyMidriff, capyNose, mouth, capyChin)
}

// capybaraWidth:橫幅要的欄數(所有幀等寬,取第一幀量即可)。
func capybaraWidth() int {
	w := 0
	for _, line := range capybaraFrame(0) {
		w = max(w, len([]rune(line)))
	}
	return w
}

// capyOneLine:終端機窄到放不下橫幅時的替代品(同一隻水豚,壓成一行)。
const capyOneLine = "(●  ●)≋ capy"

func capyTagline(prov string) string {
	return strings.TrimSpace("capy · " + prov)
}
