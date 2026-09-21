package cli

// 水豚橫幅。設計約束(不是裝飾,是為了畫面不抖):
//   - **只用 ASCII**。方框繪製字元(U+2500 區塊)與 ● ▬ ≋ 全都是 East Asian Ambiguous:
//     在 CJK 語系的終端機(iTerm2 的 ambiguous-width = double、Windows Terminal 配 CJK 字型、
//     tmux -u 加中文 locale)寬度變成 2,矩形會垮成鋸齒,眨眼那一行還會左右抖。
//     而 ansi.StringWidth 一律當 1,量不出這件事——所以測試改成強制純 ASCII(PR #41 review)。
//   - 每一幀的行數與長度都一樣;身體固定,只有耳朵、眼睛、草那幾列會變。
//
// 長相(2026-09-20 重畫;使用者:「水豚的形象和你認知的相差甚遠,先研究水豚是怎樣的動物」):
// 舊版是正面的圓臉、頭頂兩隻耳朵、橢圓鼻配兩個鼻孔——那是豬。水豚最好認的是**側面**,照這幾點畫:
//   - 頭像一塊圓角的磚:頭頂線是平的、跟背連成一條(沒有脖子),口鼻前端是鈍的(鈍,但角是圓的);
//   - 眼睛、耳朵、鼻孔都長在頭的最上緣(泡在水裡只露這三樣):眼睛小、位置很後面,緊貼著小圓耳;
//     鼻孔在口鼻的最前上角;
//   - 身體是桶狀,屁股渾圓、沒有尾巴;腿短。
//
// 第一版(PR #71 的第一個 commit)背在第 0 列、頭頂在第 2 列,中間掉兩列下來、耳朵卡在凹口裡——讀起來是
// 「有脖子的動物頂著一顆圈」,跟上面第一條自己打架(review #71)。現在從屁股到口鼻是同一條線,耳朵是線上唯一的凸起。
//
// 卡通版的慣例是同一個側面、麵包一樣的身體、一臉無所謂。嘴邊那根草留著:它是嚼草動畫的主角。
//
// 動作(2026-09-21;使用者:「比照 web 版本,也加上眨眼、轉耳朵還有吃牧草(牧草越來越短)」):
// 動畫只活在開場——定格之後水豚是印進捲動區的文字,不會再動(PR #45:留在 View 裡就是「水豚頭重複三次」)。
// 舊版用取餘數排動作:眨眼每 12 幀、嚼草每 3 幀,而開場只有 6 幀——第 0 幀閉著眼開場、之後再也不眨,草只短一次,
// 等於沒有動畫。現在開場照一份**劇本**演一遍:轉耳朵 → 眨眼 → 一口一口把草吃短 → 叼起新的一根。
// 最後一幀就是定格幀,所以定格不會跳一下;開場的長度由劇本的長度決定(tui.go 的 tuiIntro)。
type capyPose struct {
	ear   bool // 耳朵往後撥一下
	shut  bool // 閉眼
	straw int  // 草還剩幾段:0(吃完了)到 capyStrawFull
}

const capyStrawFull = 4

var capyStory = [...]capyPose{
	{straw: capyStrawFull},
	{straw: capyStrawFull, ear: true},
	{straw: capyStrawFull},
	{straw: capyStrawFull, shut: true},
	{straw: 3},
	{straw: 3},
	{straw: 2},
	{straw: 2, ear: true},
	{straw: 1},
	{straw: 1, shut: true},
	{straw: 0},
	{straw: capyStrawFull}, // 叼起新的一根:跟定格幀一模一樣
}

var (
	// 耳朵:[平常, 往後撥]。每組兩行:耳朵的頂、屁股到口鼻那條平線(沒有脖子;耳朵是線上唯一的凸起)。
	capyEar = [2][2]string{
		{
			"                         _                ",
			"        ________________( )_____          ",
		},
		{
			"                        _                 ",
			"        ________________\\ \\_____          ",
		},
	}
	// 口鼻是圓的(2026-09-21;使用者:「鼻子有點太方形了,應該要比照屁股的地方稍微圓滑一點」):
	// 上角、斜線、最前緣的 |、下角——屁股那一側的鏡像,只是收得比較緊。
	capyEyesOpen = "     .-'                    o   `-.       " // o 眼睛:頭頂線的下一行、緊貼著耳朵
	capyEyesShut = "     .-'                    -   `-.       "
	capyNose     = "   .'                           .  \\      " // . 鼻孔:口鼻的前上角
	// 牧草:[還剩幾段][臉頰那一行, 嘴那一行]。一根直線看不出是草(使用者:「太直了」),所以它從嘴角出來、
	// 往上彎、頂端有穗;吃的時候是被往嘴裡拉,穗一路靠近,不是從尖端消失。
	capyStraw = [capyStrawFull + 1][2]string{
		{
			"  /                                 |     ",
			" |                            ____.'      ",
		},
		{
			"  /                                 |     ",
			" |                            ____.'-\"    ",
		},
		{
			"  /                                 | ,\"  ",
			" |                            ____.'-'    ",
		},
		{
			"  /                                 |  ,\" ",
			" |                            ____.'--'   ",
		},
		{
			"  /                                 |  ,-\"",
			" |                            ____.'--'   ",
		},
	}
	capyBelly = []string{
		"  \\                       __.-'           ", // 下巴往後收到胸口:頭比身體淺
		"   `.|  |`----------'|  |'                ",
	}
	capyFeet = "     |__|            |__|                 " // 每一幀都一樣的一行(測試拿它認「橫幅有沒有畫出來」)
)

// 常駐的水豚(終端機夠大時;見 tui.go 的 alive)演完劇本之後的日子:大部分時間站著不動,偶爾眨眼、撥耳朵,
// 隔一陣子吃一根草。節奏跟網頁那隻一樣(眨眼 4 秒、耳朵 5.5 秒,兩個週期錯開);每 250 毫秒都在動的水豚很吵。
const (
	capyBlinkEvery = 16 // 幀:4 秒眨一次
	capyEarEvery   = 22 // 幀:5.5 秒往後撥兩下
	capyEatEvery   = 96 // 幀:24 秒吃一根草
	capyBlockRows  = 11 // 常駐那一塊的高度:水豚 9 行 + 空行 + 招牌
)

var capyEating = [...]int{3, 3, 2, 2, 1, 1, 0, 0} // 吃一根草:每口停兩幀,吃完空著嘴一下,下一幀叼起新的

func capyIdle(n int) capyPose {
	p := capyPose{straw: capyStrawFull}
	p.shut = n%capyBlinkEvery == capyBlinkEvery-1
	p.ear = n%capyEarEvery == 9 || n%capyEarEvery == 11
	if k := n%capyEatEvery - (capyEatEvery - len(capyEating)); k >= 0 {
		p.straw = capyEating[k]
	}
	return p
}

// capybaraFrame:第 n 幀(n 單調遞增,由 tick 推)。先照劇本演一遍,演完接 capyIdle。
// 不常駐的時候幀的 ticker 與定格的計時器是兩個各走各的 timer,可能多跳一兩幀——capyIdle 的頭幾幀就是定格幀,不會跳。
func capybaraFrame(n int) []string {
	if n = max(n, 0); n < len(capyStory) {
		return capybaraLines(capyStory[n])
	}
	return capybaraLines(capyIdle(n - len(capyStory)))
}

// capybaraStill:開場動畫結束後定格、印進捲動區的那一幀:睜著眼、叼著整根草。
func capybaraStill() []string { return capybaraLines(capyStory[len(capyStory)-1]) }

func capybaraLines(p capyPose) []string {
	ear, eyes := capyEar[0], capyEyesOpen
	if p.ear {
		ear = capyEar[1]
	}
	if p.shut {
		eyes = capyEyesShut
	}
	straw := capyStraw[p.straw]
	out := []string{ear[0], ear[1], eyes, capyNose, straw[0], straw[1]}
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
const capyOneLine = "(_____o.)--\" capy" // 屁股、背、眼睛、鼻孔、圓的口鼻、帶穗的草

// capyTagline:招牌。還沒登入時 provider 是空的,不要印出孤零零的間隔點。
func capyTagline(prov string) string {
	if prov == "" {
		return "capy"
	}
	return "capy · " + prov
}
