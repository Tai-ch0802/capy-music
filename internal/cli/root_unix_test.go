//go:build !windows

package cli

import (
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/spf13/cobra"
)

// 被訊號結束的行程不能回 0:`capy now --watch; echo $?` 要分得出「被砍」跟「做完」(cron / 腳本靠它)。
// 真的對自己送訊號、走 Execute 同一條路。在 RunE 裡面送:那時 signal.Notify 一定已經掛好了。
// 兩種收尾都要測:互動式介面 / now --watch / --web 把 ctx 取消當「使用者要離開」回 nil;其餘命令回包著 context.Canceled 的錯。
func TestExecuteSignalExitCode(t *testing.T) {
	for _, tc := range []struct {
		sig  syscall.Signal
		want int
	}{{syscall.SIGINT, 130}, {syscall.SIGTERM, 143}} {
		for _, wrapCtxErr := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/回ctx錯=%v", tc.sig, wrapCtxErr), func(t *testing.T) {
				cmd := &cobra.Command{Use: "capy", SilenceUsage: true, SilenceErrors: true, RunE: func(cmd *cobra.Command, _ []string) error {
					if err := syscall.Kill(os.Getpid(), tc.sig); err != nil {
						return err
					}
					<-cmd.Context().Done()
					if wrapCtxErr {
						return fmt.Errorf("讀取失敗:%w", cmd.Context().Err())
					}
					return nil
				}}
				cmd.SetArgs(nil)
				code, msg := ExitCode(executeSignalled(cmd))
				if code != tc.want {
					t.Errorf("結束碼 %d(要 %d)", code, tc.want)
				}
				if wantMsg := map[bool]string{false: "", true: "Error: 讀取失敗:context canceled"}[wrapCtxErr]; msg != wantMsg {
					t.Errorf("stderr 的位元組不變,只改結束碼:%q(要 %q)", msg, wantMsg)
				}
			})
		}
	}
}

// 沒收到訊號的命令一個位元組都不變。
func TestExecuteWithoutSignalUnchanged(t *testing.T) {
	cmd := &cobra.Command{Use: "capy", RunE: func(*cobra.Command, []string) error { return nil }}
	cmd.SetArgs(nil)
	if code, msg := ExitCode(executeSignalled(cmd)); code != 0 || msg != "" {
		t.Errorf("(%d, %q)", code, msg)
	}
}
