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

// 訊號一定要讓程式結束(使用者回報後查到的:互動式介面大約六次有一次收到 SIGTERM 不結束,之後只剩 SIGKILL)。
// 成因是一場賽跑(見 newProgram),所以跑很多輪:沒修之前幾乎每一輪都卡死,修好之後沒有 handler 可以卡、每一輪都是
// 毫秒級結束。ctx 跟 Execute 一樣是被訊號取消的(結束碼那一半在 root_unix_test.go)。兩個訊號都要測:bubbletea 那個阻塞送出有兩個分支
// (SIGINT → InterruptMsg、其餘 → QuitMsg),而 pty 實跑裡 SIGINT 那一支其實更容易中(review #74)。
func TestProgramAlwaysExitsOnSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			for i := 0; i < 40; i++ {
				if !exitsOn(t, sig) {
					t.Fatalf("第 %d 輪:收到 %v 三秒還沒結束——bubbletea 的 signal handler 又跟 ctx 搶同一個訊號了(見 newProgram)", i, sig)
				}
			}
		})
	}
}

// exitsOn:開一個程式、對自己送 sig,回報它有沒有在三秒內結束。
func exitsOn(t *testing.T, sig syscall.Signal) bool {
	t.Helper()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// 每一條離開的路都要 stop:NotifyContext 是行程層級的 signal.Notify,掛著不放的話整個測試 binary 之後收到
	// SIGTERM / SIGINT 都不會結束(go test 逾時、CI 收屍只剩 SIGKILL)——正是這個 PR 在修的症狀。
	defer stop()
	m := startedModel{started: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := newProgram(ctx, m, io.Discard, tea.WithInput(nil)).Run()
		done <- err
	}()
	select {
	case <-m.started:
	case <-time.After(5 * time.Second):
		t.Fatal("程式沒有跑起來")
	}
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		return true
	case <-time.After(3 * time.Second):
		return false
	}
}
