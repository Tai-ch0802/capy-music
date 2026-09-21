package cli

import (
	"context"
	"io"

	tea "charm.land/bubbletea/v2"
)

// newProgram:帶著命令的 ctx 開 bubbletea 程式的唯一入口(互動式介面、now --watch)。
//
// 訊號只准有一個主人。Execute(executeSignalled)已經把 SIGINT / SIGTERM 變成 ctx 取消,而 bubbletea 預設
// 還會自己再聽一次同樣的訊號——兩邊同時收到同一個 SIGTERM 就是一場賽跑:
//
//   - bubbletea 的 handler 先到:送 QuitMsg、事件迴圈收下、正常離開;
//   - ctx 取消先到:事件迴圈看到 ctx.Done 直接結束,不再收訊息;handler 這時才做 `p.msgs <- QuitMsg{}`——
//     那是一個沒有 select 的阻塞送出,再也沒有人收。接著 Run 收尾的 shutdown 等所有 handler 結束,而那個 handler
//     永遠不會結束:**死結**。行程不結束,而且因為訊號已經被 Notify 接走,之後再送 SIGTERM 也沒用,只剩 SIGKILL。
//
// 實測大約六次一次(pty 實跑 + SIGQUIT 的 goroutine 堆疊:主 goroutine 停在 channelHandlers.shutdown,
// handleSignals 停在 tea.go 的 chan send)。v2.0.2 到 v2.0.9 那一段都一樣,升版解決不了。
// 所以關掉 bubbletea 自己的 handler:訊號一律走 ctx。行為不變——賽跑的兩種結果本來就都以 ErrProgramKilled 收場
// (Run 結束時看的是 ctx 有沒有被取消),呼叫端也早就把它當成「使用者要離開」。
//
// 不帶 ctx 的程式(ui.Pager、huh 表單)沒有這個問題:只有 bubbletea 一個主人,事件迴圈一直在收。
func newProgram(ctx context.Context, m tea.Model, out io.Writer, opts ...tea.ProgramOption) *tea.Program {
	return tea.NewProgram(m, append([]tea.ProgramOption{tea.WithContext(ctx), tea.WithOutput(out), tea.WithoutSignalHandler()}, opts...)...)
}
