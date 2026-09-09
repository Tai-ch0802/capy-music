package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// 斜線選單。輸入行以 / 開頭時就是開著的:/ 後面打的字即時過濾,↑↓ 選,⏎ 把命令**帶進輸入行**
// 而不是直接執行(還要補參數),Esc 清掉輸入就收起來。
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
