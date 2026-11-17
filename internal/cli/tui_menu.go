package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/store"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// 斜線選單。輸入行以 / 開頭時就是開著的:/ 後面打的字即時過濾,↑↓ 選,Tab / ⏎ 把命令**帶進輸入行**
// 而不是直接執行(還要補參數),Esc 清掉輸入就收起來。/ 後面已經是完整命令(可帶參數)時選單收起、
// ⏎ 直接執行:/ 只是開選單的記號,執行時拿掉。
//
// 第二階段參數補全:當輸入 /play 、/pl show 、/pl pull 、/pl push 、/pl sync 、/pl add 後接空白時,
// 讀取本機快取(cache.Load 與 state.db 正本清單)即時過濾候選,含空白名稱按 Tab / ⏎ 自動包雙引號。
//
// 選單向上長、壓在捲動區前面 —— 不插進歷程:歷程是永久的,選單是暫時的,混在一起就分不出
// 「我看過什麼」與「我正在選什麼」。
//
// 沒有畫外框:┌─┐│└┘ 全是 East Asian Ambiguous,在 CJK 終端機是兩欄,一條滿版的框線會翻倍
// 成兩行(和分隔線同一個坑,見 tui_width.go)。選中那列用 "> " 標示,與輸入行的提示符一致。

const (
	tuiMenuRows      = 8 // 最多顯示幾列;超過就捲
	tuiMenuStoreBusy = 100 * time.Millisecond
)

type tuiCmdItem struct {
	path     string
	short    string
	complete string // Tab / ⏎ 寫回輸入行的完整字串;空 = path + " "
}

func (it tuiCmdItem) completion() string {
	if it.complete != "" {
		return it.complete
	}
	return it.path + " "
}

// tuiCommands:從 cobra 的命令樹長出來,並附上 TUI 內建的 sleep 命令。
func tuiCommands() []tuiCmdItem {
	out := tuiCommandsOf(newRootCmd())
	out = append(out, tuiCmdItem{path: "sleep", short: i18n.T("tui.cmd.sleep.short")})
	return out
}

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
				out = append(out, tuiCmdItem{path: path, short: sub.Short})
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
// 是的話選單收起、⏎ 直接執行。
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

// tuiQuoteArg:參數含空白或雙引號時包上雙引號(與 splitArgs 的解析規則對齊)。
func tuiQuoteArg(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t") {
		return `"` + strings.ReplaceAll(s, `"`, "") + `"`
	}
	return s
}

// parseMenuArgStage:辨識支援第二階段參數補全的 6 個命令前綴(後面必須已經打了至少一個空白)。
func parseMenuArgStage(q string) (cmdPrefix, argRaw string, ok bool) {
	s := strings.TrimLeft(q, " \t")
	if strings.HasPrefix(s, "play") && len(s) > 4 && (s[4] == ' ' || s[4] == '\t') {
		return "play", strings.TrimLeft(s[4:], " \t"), true
	}
	if strings.HasPrefix(s, "pl") && len(s) > 2 && (s[2] == ' ' || s[2] == '\t') {
		rest := strings.TrimLeft(s[2:], " \t")
		for _, sub := range []string{"show", "pull", "push", "sync", "add"} {
			if strings.HasPrefix(rest, sub) && len(rest) > len(sub) && (rest[len(sub)] == ' ' || rest[len(sub)] == '\t') {
				return "pl " + sub, strings.TrimLeft(rest[len(sub):], " \t"), true
			}
		}
	}
	return "", "", false
}

// tuiStoreAllowed:測試中若未設定獨立的 CAPY_CONFIG_DIR(或指向真實家目錄),一律不碰本機設定目錄下的 state.db / config.json。
func tuiStoreAllowed() bool {
	dir, err := config.Dir()
	if err != nil || dir == "" {
		return false
	}
	if testing.Testing() {
		if os.Getenv("CAPY_CONFIG_DIR") == "" {
			return false
		}
		if real, err := os.UserConfigDir(); err == nil && real != "" {
			realCapy := filepath.Clean(filepath.Join(real, "capy-music"))
			cleanDir := filepath.Clean(dir)
			if cleanDir == realCapy || strings.HasPrefix(cleanDir, realCapy+string(filepath.Separator)) {
				return false
			}
		}
	}
	return true
}

func tuiLoadCanonSnapshot() (*store.Canonical, bool) {
	if !tuiStoreAllowed() {
		return nil, false
	}
	st, err := store.OpenReadOnly(tuiMenuStoreBusy)
	if err != nil {
		return nil, false
	}
	defer st.Close()
	c, err := st.Dump()
	if err != nil {
		return nil, false
	}
	slices.SortStableFunc(c.Playlists, func(a, b canon.Playlist) int {
		if cmp := strings.Compare(a.Name, b.Name); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.PID, b.PID)
	})
	return &c, true
}

func tuiArgCandidates(cmdPrefix, prov string) []tuiCmdItem {
	if !tuiStoreAllowed() {
		return nil
	}
	var out []tuiCmdItem
	seen := map[string]bool{}
	add := func(name, short string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, tuiCmdItem{
			path:     name,
			short:    short,
			complete: cmdPrefix + " " + tuiQuoteArg(name) + " ",
		})
	}

	switch cmdPrefix {
	case "pl pull", "pl push", "pl sync", "pl add":
		if snap, ok := tuiLoadCanonSnapshot(); ok {
			for _, pl := range snap.Playlists {
				add(pl.Name, i18n.T("tui.menu.arg.canon")+" ("+i18n.T("tui.deck.tracks", "count", len(pl.Items))+")")
			}
		}
	case "pl show":
		if snap, ok := tuiLoadCanonSnapshot(); ok {
			for _, pl := range snap.Playlists {
				add(pl.Name, i18n.T("tui.menu.arg.canon")+" ("+i18n.T("tui.deck.tracks", "count", len(pl.Items))+")")
			}
		}
		cc := cache.Load()
		for _, p := range cc.Playlists[prov] {
			desc := i18n.T("tui.menu.arg.playlist")
			if p.Total >= 0 {
				desc += " (" + i18n.T("tui.deck.tracks", "count", p.Total) + ")"
			}
			add(p.Name, desc)
		}
	case "play":
		if snap, ok := tuiLoadCanonSnapshot(); ok {
			for _, pl := range snap.Playlists {
				add(pl.Name, i18n.T("tui.menu.arg.canon")+" ("+i18n.T("tui.deck.tracks", "count", len(pl.Items))+")")
			}
		}
		cc := cache.Load()
		for _, p := range cc.Playlists[prov] {
			desc := i18n.T("tui.menu.arg.playlist")
			if p.Total >= 0 {
				desc += " (" + i18n.T("tui.deck.tracks", "count", p.Total) + ")"
			}
			add(p.Name, desc)
		}
		for _, r := range cc.Recent {
			if (prov == "" || r.Provider == prov) && r.Type != cache.TypeQuery {
				add(r.Label, i18n.T("tui.menu.arg.recent"))
			}
		}
	}
	return out
}

// tuiArgMenu:第二階段參數選單。若無匹配候選、使用者正在打旗標、或已完整輸入唯一相符參數,則收起選單讓 ⏎ 直接執行。
func tuiArgMenu(q, prov string) ([]tuiCmdItem, bool) {
	cmdPrefix, argRaw, ok := parseMenuArgStage(q)
	if !ok {
		return nil, false
	}
	trimmed := strings.TrimSpace(argRaw)
	if strings.HasPrefix(trimmed, "-") {
		return nil, false
	}
	cands := tuiArgCandidates(cmdPrefix, prov)
	if len(cands) == 0 {
		return nil, false
	}
	needle := strings.ToLower(strings.Trim(trimmed, `"`))
	var filtered []tuiCmdItem
	for _, it := range cands {
		if needle == "" || strings.Contains(strings.ToLower(it.path), needle) {
			filtered = append(filtered, it)
		}
	}
	if len(filtered) == 0 {
		return nil, false
	}
	if trimmed != "" && len(filtered) == 1 && tuiQuoteArg(filtered[0].path) == trimmed {
		return nil, false
	}
	return filtered, true
}

// tuiTabCompleteArg:不含前導 / 時(例如第一階段按 Tab 產生 "pl show " 之後),按 Tab 也能補齊第一個相符參數。
func tuiTabCompleteArg(input, prov string) (string, bool) {
	items, ok := tuiArgMenu(input, prov)
	if !ok || len(items) == 0 {
		return "", false
	}
	return items[0].completion(), true
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
		return []string{tuiJoin(w, tuiSeg{"  " + i18n.T("tui.menu.no_match"), t.Mutedly})}
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
