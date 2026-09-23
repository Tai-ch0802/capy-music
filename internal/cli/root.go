package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// version 由 release 流程以 ldflags 注入;開發環境為 dev。
var version = "dev"

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "capy",
		Short:         i18n.T("cmd.root.short"),
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true, // main 負責印錯與 exit code(歧義 = 2,見 AmbiguousError)
		// NoArgs 是把意圖寫明:root 本身不吃參數。擋下 capy pasue 的其實是 cobra 對「有子命令的
		// 命令」的預設行為(legacyArgs),所以 TestUnknownSubcommandFails 守的是那個行為,不是這一行。
		Args: cobra.NoArgs,
		// 無參數:終端機裡開互動式介面,pipe / cron 一律印 help——「非 TTY 必須是純文字」是硬約束,
		// 而且 capy | head 這種用法不能突然變成一個吃鍵盤的程式。
		RunE: func(cmd *cobra.Command, _ []string) error {
			web, _ := cmd.Flags().GetBool("web")
			port, _ := cmd.Flags().GetInt("port")
			if cmd.Flags().Changed("port") && !web {
				return i18n.Errorf("root.err.port_without_web")
			}
			if web { // 在 !isInteractive 分流之前:非 TTY 啟動(launchd / nohup)也能開,只印網址不開瀏覽器
				return runWeb(cmd, port)
			}
			if !isInteractive(cmd) {
				return cmd.Help()
			}
			return runTUI(cmd)
		},
	}
	providerFlag(cmd)
	cmd.Flags().Bool("web", false, i18n.T("cmd.root.flag.web"))
	cmd.Flags().Int("port", 0, i18n.T("cmd.root.flag.port"))
	cmd.AddCommand(newDebugCmd())
	cmd.AddCommand(newAuthCmd())
	cmd.AddCommand(newSearchCmd())
	cmd.AddCommand(newPlayCmd())
	cmd.AddCommand(newPlCmd())
	cmd.AddCommand(newMigrateCmd())
	cmd.AddCommand(
		simpleCtl("pause", i18n.T("cmd.pause.short"), i18n.T("cmd.pause.done"), func(ctx context.Context, pc provider.PlaybackController) error { return pc.Pause(ctx) }),
		simpleCtl("next", i18n.T("cmd.next.short"), i18n.T("cmd.next.done"), func(ctx context.Context, pc provider.PlaybackController) error { return pc.Next(ctx) }),
		simpleCtl("prev", i18n.T("cmd.prev.short"), i18n.T("cmd.prev.done"), func(ctx context.Context, pc provider.PlaybackController) error { return pc.Prev(ctx) }),
	)
	cmd.AddCommand(newSeekCmd(), newVolCmd())
	cmd.AddCommand(newNowCmd(), newDevicesCmd())
	cmd.AddCommand(newDoctorCmd())
	cmd.AddCommand(newConfigCmd(), newHistoryCmd(), newUpdateCmd())
	cmd.AddCommand(newExportCmd(), newDriveCmd())
	cmd.AddCommand(newResolveCmd())
	return cmd
}

// Execute 是 CLI 進入點。SIGINT/SIGTERM 取消 ctx,讓 429 退避等待中的請求可被中斷
// (spec 硬約束:可腳本化/可被 cron 終止是核心價值)。語系在建命令樹之前決定(決策 50)。
func Execute() error {
	if warn := applyLanguage(); warn != "" {
		fmt.Fprintln(os.Stderr, warn)
	}
	return executeSignalled(newRootCmd())
}

// SignalError:行程是被 SIGINT / SIGTERM 結束的 → exit 130 / 143(shell 慣例 128+n),腳本才分得出「被砍」跟「做完」。
// Err 是命令自己回的錯(互動式介面 / now --watch / --web 把 ctx 取消當「使用者要離開」,回的是 nil),訊息照舊印。
type SignalError struct {
	Sig os.Signal
	Err error
}

func (e *SignalError) Error() string { return i18n.T("root.err.signal", "signal", e.Sig.String()) }
func (e *SignalError) Unwrap() error { return e.Err } // 錯誤鏈不斷:errors.Is(err, context.Canceled) 在 Execute 的呼叫端照樣成立

// executeSignalled:不用 signal.NotifyContext——它的 ctx.Err() 只有 context.Canceled,分不出是哪個訊號;
// 自己聽,把訊號記成 ctx 的 cause。測試替換點(Execute 讀 os.Args,測試裡跑不了)。
func executeSignalled(root *cobra.Command) error {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigc)
	// 第一個訊號之後 goroutine 就結束,但刻意不在這裡 signal.Stop:收尾期間(pl push 寫到一半、--web 的 Shutdown 最多 5 秒)
	// 第二個 Ctrl-C 只會被 runtime 接走丟掉,寧可讓寫入收完也不要半截;真的卡住還有 SIGKILL。跟以前 NotifyContext 的行為相同。
	go func() {
		select {
		case s := <-sigc:
			cancel(&SignalError{Sig: s})
		case <-ctx.Done():
		}
	}()
	return signalled(ctx, root.ExecuteContext(ctx))
}

// signalled:ctx 是被訊號取消的,而命令回 nil 或「被取消」的錯 → 換成 SignalError。其餘的錯原樣回:
// 設計出來的結束碼(2 / 3 / 窗格的 130)與「寫到一半、已寫 N 首」那種 exit 1 都比「被砍」更有話要說。
func signalled(ctx context.Context, err error) error {
	var se *SignalError
	if errors.As(context.Cause(ctx), &se) && (err == nil || errors.Is(err, context.Canceled)) {
		if code, _ := ExitCode(err); code <= 1 {
			return &SignalError{Sig: se.Sig, Err: err}
		}
	}
	return err
}
