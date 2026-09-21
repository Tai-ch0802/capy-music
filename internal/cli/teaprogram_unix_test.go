//go:build !windows

package cli

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// started:Init 的命令跑起來的時候,bubbletea 的 handler(訊號、resize、命令)都已經掛好了。
type startedModel struct{ started chan struct{} }

func (m startedModel) Init() tea.Cmd {
	return func() tea.Msg { close(m.started); return nil }
}
func (m startedModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m startedModel) View() tea.View                      { return tea.NewView("") }

// SIGTERM 一定要讓程式結束(使用者回報後查到的:互動式介面大約六次有一次收到 SIGTERM 不結束,之後只剩 SIGKILL)。
// 成因是一場賽跑(見 newProgram),所以跑很多輪:沒修之前每一輪大約有一成多的機率卡死,四十輪幾乎必中;
// 修好之後沒有 handler 可以卡,每一輪都是毫秒級結束。ctx 的接法跟 Execute 一模一樣。
func TestProgramAlwaysExitsOnSIGTERM(t *testing.T) {
	for i := 0; i < 40; i++ {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		m := startedModel{started: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			_, err := newProgram(ctx, m, io.Discard, tea.WithInput(nil)).Run()
			done <- err
		}()
		select {
		case <-m.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("第 %d 輪:程式沒有跑起來", i)
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			stop()
			t.Fatalf("第 %d 輪:收到 SIGTERM 三秒還沒結束——bubbletea 的 signal handler 又跟 ctx 搶同一個訊號了(見 newProgram)", i)
		}
		stop()
	}
}
