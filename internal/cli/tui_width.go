package cli

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// 底部區與推進捲動區的每一行都要夾寬度,理由有兩層:
//
//  1. 寫滿最後一欄時某些終端機會多換一行,底部區就會多佔一行、上緣被頂掉(重繪面積是這次重做的主因)。
//  2. bubbletea 的 insertAbove 用 ansi.StringWidth 算「要捲幾行」;推進去的行如果真的換了行而它沒算到,
//     捲動量會少一行,舊畫面留在上面。
//
// 而 ansi.StringWidth 量不準:› · ▶ ⚠ … ← → ± 這些 East Asian Ambiguous 字元在 CJK 語系的終端機
// 是兩欄,它一律回 1(水豚那次就是這樣一路綠燈到使用者眼前,PR #41 review)。所以自己數,而且往寬的猜。

// tuiWidth:保守的顯示寬度 —— 非 ASCII 一律算兩欄。CJK 本來就是 2,ambiguous 在 CJK 終端機也是 2;
// 猜寬只會少填一欄,猜窄會換行。
func tuiWidth(s string) int {
	n := 0
	for _, r := range ansi.Strip(s) {
		switch {
		case r < 32 || r == 127: // 控制字元不佔欄。bubbles 的 textinput 會用 NUL 當寬字元的續格
		case r < 128:
			n++
		default:
			n += 2
		}
	}
	return n
}

// tuiClip:把純文字夾在 w 欄內(tuiWidth 的算法),截掉的話補 ".."(… 自己就是 ambiguous 字元)。
// 只吃沒有樣式的字串:樣式要在夾完之後才套,不然會切進 ANSI 跳脫序列中間。
func tuiClip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if tuiWidth(s) <= w {
		return s
	}
	if w <= 2 {
		return strings.Repeat(".", w)
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		rw := 1
		if r >= 128 {
			rw = 2
		}
		if n+rw > w-2 {
			break
		}
		b.WriteRune(r)
		n += rw
	}
	return b.String() + ".."
}

// tuiSeg:一行裡的一段。style 為 nil 就不上色。
type tuiSeg struct {
	text  string
	style func(string) string
}

// tuiJoin:把幾段接成總寬不超過 w 的一行。哪一段超出就在那裡截斷,後面的段丟掉。
// 「丟掉」要看有沒有截斷,不能只看 used >= w:tuiClip 遇到寬字元常常停在 w-1,
// 下一段就會拿到 1 格預算再吐一顆點出來,變成三個點。
func tuiJoin(w int, segs ...tuiSeg) string {
	var b strings.Builder
	used := 0
	for _, sg := range segs {
		rem := w - used
		if rem <= 0 {
			break
		}
		t := tuiClip(sg.text, rem)
		if t != "" {
			used += tuiWidth(t)
			if sg.style != nil {
				t = sg.style(t)
			}
			b.WriteString(t)
		}
		if tuiWidth(sg.text) > rem { // 這一段放不完,後面的段沒有意義
			break
		}
	}
	return b.String()
}
