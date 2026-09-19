package cli

// 水豚橫幅。設計約束(不是裝飾,是為了畫面不抖):
//   - **只用 ASCII**。方框繪製字元(U+2500 區塊)與 ● ▬ ≋ 全都是 East Asian Ambiguous:
//     在 CJK 語系的終端機(iTerm2 的 ambiguous-width = double、Windows Terminal 配 CJK 字型、
//     tmux -u 加中文 locale)寬度變成 2,矩形會垮成鋸齒,眨眼那一行還會左右抖。
//     而 ansi.StringWidth 一律當 1,量不出這件事——所以測試改成強制純 ASCII(PR #41 review)。
//   - 每一幀的行數與長度都一樣;身體固定,只有眼睛列與嘴／草列會變。
//
// 長相(2026-09-20 重畫;使用者:「水豚的形象和你認知的相差甚遠,先研究水豚是怎樣的動物」):
// 舊版是正面的圓臉、頭頂兩隻耳朵、橢圓鼻配兩個鼻孔——那是豬。水豚最好認的是**側面**,照這幾點畫:
//   - 頭像一塊圓角的磚:頭頂線是平的、跟背連成一條(沒有脖子),口鼻前端又鈍又方;
//   - 眼睛、耳朵、鼻孔都長在頭的最上緣(泡在水裡只露這三樣):眼睛小、位置很後面,緊貼著小圓耳;
//     鼻孔在口鼻的最前上角;
//   - 身體是桶狀、背微微隆起,屁股渾圓、沒有尾巴;腿短。
//
// 卡通版的慣例是同一個側面、麵包一樣的身體、一臉無所謂。嘴邊那根草留著:它是嚼草動畫的主角。
//
// 動作:側身朝右站著,嘴角叼一根牧草,一邊嚼一邊偶爾眨眼。
const (
	capyBlinkEvery = 12 // 幀:約 4 秒眨一次
	capyChewEvery  = 3  // 幀:約 1 秒嚼一口
)

var (
	capyBack = []string{
		"        ____________                      ",
		"     .-'            `--.   _              ",
		"   .'                   `-( )------.      ", // ( ) 是耳朵:小、圓、長在頭的後上方
	}
	capyEyesOpen  = "  /                            o   .|     " // o 眼睛(小、高、貼著耳朵) . 鼻孔(最前上角) | 又鈍又方的口鼻
	capyEyesShut  = "  /                            -   .|     "
	capyCheek     = " |                                  |     "
	capyMouthOut  = " |                             _____|~~~~ " // 草從嘴角伸出去
	capyMouthChew = " |                             _____|~~   " // 嚼一口:草短一截
	capyBelly     = []string{
		"  \\                       __.-'           ", // 下巴往後收到胸口:頭比身體淺
		"   `.|  |`----------'|  |'                ",
	}
	capyFeet = "     |__|            |__|                 " // 每一幀都一樣的一行(測試拿它認「橫幅有沒有畫出來」)
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
	return capybaraLines(eyes, mouth)
}

// capybaraStill:開場動畫結束後定格、印進捲動區的那一幀:睜著眼、草伸出去。
// 不可以拿 capybaraFrame(0) 當定格 —— 第 0 幀 0%capyBlinkEvery == 0,是閉著眼的。
func capybaraStill() []string { return capybaraLines(capyEyesOpen, capyMouthOut) }

func capybaraLines(eyes, mouth string) []string {
	out := make([]string, 0, len(capyBack)+len(capyBelly)+4)
	out = append(out, capyBack...)
	out = append(out, eyes, capyCheek, mouth)
	out = append(out, capyBelly...)
	return append(out, capyFeet)
}

// capybaraWidth:橫幅要的欄數。全部是 ASCII,所以位元組數就是顯示寬度。
func capybaraWidth() int {
	w := 0
	for _, line := range capybaraStill() {
		w = max(w, len(line))
	}
	return w
}

// capyOneLine:終端機窄到放不下橫幅時的替代品(同一隻水豚,壓成一行)。同樣只用 ASCII。
const capyOneLine = "(_____o.]~~ capy" // 屁股、背、眼睛、鼻孔、又鈍又方的口鼻、草

// capyTagline:招牌。還沒登入時 provider 是空的,不要印出孤零零的間隔點。
func capyTagline(prov string) string {
	if prov == "" {
		return "capy"
	}
	return "capy · " + prov
}
