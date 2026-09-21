package cli

import (
	"context"
	"errors"
	"io"

	tea "charm.land/bubbletea/v2"

	"github.com/Tai-ch0802/capy-music/internal/ui"
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

// keyInterrupt:model 記得自己是不是被 Ctrl-C **按鍵**關掉的。raw mode 下 Ctrl-C 不是 SIGINT 而是一個按鍵,
// Execute 的訊號那條路看不到它。
type keyInterrupt interface{ interruptedByKey() bool }

// runProgram:newProgram + Run + 互動式介面與 now --watch 共同的收尾。
//
//   - ctx 取消 / 程式被砍不是錯誤(訊號一律走 ctx,見 newProgram):回 nil,結束碼 130 / 143 由 Execute 依訊號補。
//   - 按 q / Esc 離開 = 做完了,exit 0。
//   - 按 Ctrl-C 離開 = 中斷,回 ui.ErrInterrupted(exit 130、不印東西;同檢視窗格、同真的 SIGINT)——`capy now --watch; echo $?`
//     不該因為終端機剛好在 raw mode 就分不出「按 Ctrl-C」跟「做完」。
//
// model 那邊刻意不用 tea.Interrupt(檢視窗格的做法):那條路在 bubbletea 裡算 killed——不畫最後一幀、renderer 直接停。
// 窗格是全螢幕的沒差,這兩個是 inline renderer,離開時留在終端機上的畫面會變。所以照舊 tea.Quit(畫面一個位元組不變),
// model 記一筆,Run 回來之後才換成結束碼。
func runProgram(ctx context.Context, m tea.Model, out io.Writer, opts ...tea.ProgramOption) (tea.Model, error) {
	final, err := newProgram(ctx, m, out, opts...).Run()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, tea.ErrProgramKilled) {
		return final, err
	}
	if k, ok := final.(keyInterrupt); ok && k.interruptedByKey() {
		return final, ui.ErrInterrupted
	}
	return final, nil
}
