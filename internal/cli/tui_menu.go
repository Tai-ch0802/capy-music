package cli

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// 斜線選單。輸入行以 / 開頭時就是開著的:/ 後面打的字即時過濾,↑↓ 選,Tab / ⏎ 把命令**帶進輸入行**
// 而不是直接執行(還要補參數),Esc 清掉輸入就收起來。/ 後面已經是完整命令(可帶參數)時選單收起、
// ⏎ 直接執行:/ 只是開選單的記號,執行時拿掉。
//
// 選單向上長、壓在捲動區前面 —— 不插進歷程:歷程是永久的,選單是暫時的,混在一起就分不出
// 「我看過什麼」與「我正在選什麼」。
//
// 沒有畫外框:┌─┐│└┘ 全是 East Asian Ambiguous,在 CJK 終端機是兩欄,一條滿版的框線會翻倍
// 成兩行(和分隔線同一個坑,見 tui_width.go)。選中那列用 "> " 標示,與輸入行的提示符一致。

const tuiMenuRows = 8 // 最多顯示幾列;超過就捲

type tuiCmdItem struct{ path, short string }

// tuiCommands:從 cobra 的命令樹長出來,不另外維護一份清單 —— 手寫的那份一定會跟實作走鏢。
func tuiCommands() []tuiCmdItem { return tuiCommandsOf(newRootCmd()) }

// tuiCommandsOf:判準是 Runnable(),不是「葉節點」—— resolve 自己吃參數能跑,底下卻掛了
// resolve pin,用葉節點當判準會把它整個丟掉。隱藏的與 help / completion 不收:後兩個是 cobra
// 在 Execute 時才掛上去的,newRootCmd() 這棵新樹上還沒有,但它們是 cobra 自己加的東西、不是
// capy 的命令,擋在這裡才不會哪天默默冒出來(測試直接餵一棵掛好的樹驗這件事)。
func tuiCommandsOf(root *cobra.Command) []tuiCmdItem {
	var out []tuiCmdItem
	var walk func(c *cobra.Command, prefix string)
	walk = func(c *cobra.Command, prefix string) {
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			path := strings.TrimSpace(prefix + " " + sub.Name())
			if sub.Runnable() {
				out = append(out, tuiCmdItem{path, sub.Short})
			}
			walk(sub, path)
		}
	}
	walk(root, "")
	return out
}

// tuiMenuQuery:輸入行以 / 開頭時,回 / 後面那段(即過濾字)與 true。
func tuiMenuQuery(input string) (string, bool) {
	if !strings.HasPrefix(input, "/") {
		return "", false
	}
	return strings.TrimPrefix(input, "/"), true
}

// tuiIsCommand:q(/ 後面那段)已經是一個完整命令了嗎 —— 剛好等於某個命令路徑,或命令路徑後面接著參數。
// 是的話選單收起、⏎ 直接執行。原本整行都當過濾字:打了 /pl show 冬日暖調 比不到任何命令,⏎ 沒東西可帶
// 就什麼都不做,使用者得回到行首把 / 刪掉才送得出去。用字詞比對,幾個空白分隔都算;大小寫要一樣,cobra 也是。
// 命令自己可執行、底下又掛著子命令時(resolve / resolve pin),/resolve p 算「還在打 resolve pin」而不是
// 「resolve 加參數 p」,選單留著讓人補齊;/resolve pin 剛好是命令、/resolve x 沒有命令接得下去,才算完整。
// 剛好等於命令的一律算完整(/resolve 也是,⏎ 就跑):「打完整命令就執行」是使用者定的規則,
// 想看 resolve 底下有什麼,/res 或 /resolve p 都看得到(PR #49 review 的取捨)。
func tuiIsCommand(all []tuiCmdItem, q string) bool {
	words := strings.Fields(q)
	if len(words) == 0 {
		return false
	}
	var exact, withArgs, extends bool
	for _, it := range all {
		p := strings.Fields(it.path)
		switch {
		case slices.Equal(words, p):
			exact = true
		case len(words) > len(p) && slices.Equal(words[:len(p)], p):
			withArgs = true
		case len(p) >= len(words) && slices.Equal(p[:len(words)-1], words[:len(words)-1]) &&
			strings.HasPrefix(p[len(words)-1], words[len(words)-1]):
			extends = true // 還有更長的命令接得下去(最後一個字打到一半也算:resolve p → resolve pin)
		}
	}
	return exact || (withArgs && !extends)
}

// tuiMenuFilter:命令路徑含有查詢字(不分大小寫)就留下。查詢字裡的空白照樣比對,
// 所以打 "pl s" 會同時留下 pl show / pl sync。
func tuiMenuFilter(all []tuiCmdItem, q string) []tuiCmdItem {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return all
	}
	var out []tuiCmdItem
	for _, it := range all {
		if strings.Contains(strings.ToLower(it.path), q) {
			out = append(out, it)
		}
	}
	return out
}

// tuiMenuWindow:selected 落在哪一段可視範圍。清單比 tuiMenuRows 長時跟著選取捲動。
func tuiMenuWindow(n, sel int) (lo, hi int) {
	if n <= tuiMenuRows {
		return 0, n
	}
	lo = max(0, min(sel-tuiMenuRows/2, n-tuiMenuRows))
	return lo, lo + tuiMenuRows
}

// tuiMenuView:選單的每一列(由上而下),已經夾好寬度。空清單回一行「沒有符合的命令」。
func tuiMenuView(items []tuiCmdItem, sel, w int, t ui.Theme) []string {
	if len(items) == 0 {
		return []string{tuiJoin(w, tuiSeg{"  沒有符合的命令", t.Mutedly})}
	}
	pathW := 0
	for _, it := range items {
		pathW = max(pathW, tuiWidth(it.path))
	}
	lo, hi := tuiMenuWindow(len(items), sel)
	out := make([]string, 0, hi-lo)
	for i := lo; i < hi; i++ {
		mark, name := "  ", t.Mutedly
		if i == sel {
			mark, name = "> ", t.Strong
		}
		pad := strings.Repeat(" ", pathW-tuiWidth(items[i].path)+2)
		out = append(out, tuiJoin(w,
			tuiSeg{mark, t.Accented},
			tuiSeg{items[i].path, name},
			tuiSeg{pad + items[i].short, t.Mutedly},
		))
	}
	return out
}
