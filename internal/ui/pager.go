package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// 表格比終端機寬時的檢視窗格(alt screen):整張表以自然寬度排、每列一行,←/→(h/l)橫向、↑/↓(j/k)上下、
// PgUp/PgDn(b/f)、u/d 半頁、g/G 到頭尾,q / Esc 離開。Ctrl-C 是**中止**不是離開:命令跟著結束,
// 後面的確認提示不會再問(pl pull 看到要刪 8 首按 Ctrl-C,不該再被問「套用?」)。表頭固定在最上面、
// 跟著橫向捲;底部一行說目前看到的列與欄——文字的捲軸,不畫圖形的(方框字元在 CJK 終端機寬度不對)。
// 看完由 Table 把換行版印進捲動區當紀錄:窗格關了就沒了,紀錄要留在可回捲、可複製的地方
// (計畫 2026-09-15-table-full.md §2)。
//
// 子命令拿到的是真的 TTY(互動式介面用 tea.ExecProcess 重新執行 capy),所以介面裡也開得起來,
// 不需要 pty + VT 模擬器那套。視窗矮到 3 行以下時表頭會被擠掉:resize 途中的瞬間而已,不另外處理。

const pagerStep = 8 // ←/→ 一次捲幾欄

type pagerModel struct {
	header  string // 表頭,已排成一行(自然寬)
	body    viewport.Model
	width   int
	longest int // 最寬的一行(含表頭),狀態列用
}

func newPager(header string, rows []string) pagerModel {
	longest := ansi.StringWidth(header)
	for _, r := range rows {
		longest = max(longest, ansi.StringWidth(r))
	}
	// viewport 的橫向上限只看它自己的內容:表頭比最長的列寬時(欄名比值長、列尾的補白又被修掉),
	// 表頭最右邊幾欄會捲不到。把每列補到一樣寬,上限才涵蓋表頭。
	padded := make([]string, len(rows))
	for i, r := range rows {
		padded[i] = r + strings.Repeat(" ", longest-ansi.StringWidth(r))
	}
	vp := viewport.New()
	vp.SetHorizontalStep(pagerStep)
	vp.SetContentLines(padded)
	return pagerModel{header: header, body: vp, longest: longest}
}

func (m pagerModel) Init() tea.Cmd { return nil }

func (m pagerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.body.SetWidth(msg.Width)
		m.body.SetHeight(max(1, msg.Height-2)) // 表頭一行、狀態一行
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc":
			return m, tea.Quit
		case "ctrl+c": // raw mode 下 ctrl+c 是按鍵不是 SIGINT:自己攔、還原成中斷(Run 回 ErrInterrupted),不是「看完了」
			return m, tea.Interrupt
		case "g", "home":
			m.body.GotoTop()
			return m, nil
		case "G", "end":
			m.body.GotoBottom()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.body, cmd = m.body.Update(msg)
	return m, cmd
}

func (m pagerModel) View() tea.View {
	x := m.body.XOffset()
	rows := m.body.TotalLineCount()
	top, bottom := 0, 0
	if rows > 0 {
		top = m.body.YOffset() + 1
		bottom = min(rows, m.body.YOffset()+m.body.Height())
	}
	status := fmt.Sprintf("第 %d–%d 列 / %d · 欄 %d–%d / %d · ←→ 橫向 ↑↓ 上下 g/G 頭尾 q 離開",
		top, bottom, rows, x+1, min(m.longest, x+m.width), m.longest)
	v := tea.NewView(boldStyle.Render(ansi.Cut(m.header, x, x+m.width)) + "\n" +
		m.body.View() + "\n" +
		DefaultTheme.Mutedly(ansi.Truncate(status, m.width, "")))
	v.AltScreen = true
	return v
}

// Pager:開檢視窗格看一張比終端機寬的表;header 與 rows 都已排成自然寬度的行(每列一行),畫在 w
// (跟表同一個地方,不是固定 stdout)。使用者按 Ctrl-C 回 tea.ErrInterrupted。測試替換點。
// 沒帶 ctx:Table 沒有 ctx 可傳;外部 SIGINT 由 bubbletea 自己的 signal handler 轉成中斷,同一條路。
var Pager = func(w io.Writer, header string, rows []string) error {
	_, err := tea.NewProgram(newPager(header, rows), tea.WithOutput(w)).Run()
	return err
}

// StdinIsTTY:窗格要吃鍵盤,stdout 是 TTY 還不夠(cron 把 stdout 接到終端機、stdin 是管線那種不能開)。
// 測試替換點。
var StdinIsTTY = func() bool { return IsTTY(os.Stdin) }

// PagerEnabled:CAPY_PAGER=never 一律不開窗格 —— script / expect 包起來跑的、CI 給了 pty 的、tmux send-keys
// 的,都是「有 TTY 但沒有人」;可腳本化是核心價值,TTY 下也要有逃生口。測試替換點。
var PagerEnabled = func() bool { return os.Getenv("CAPY_PAGER") != "never" }
