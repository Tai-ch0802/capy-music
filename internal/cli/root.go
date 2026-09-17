package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// version 由 release 流程以 ldflags 注入;開發環境為 dev。
var version = "dev"

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "capy",
		Short:         "跨平台音樂 CLI:搜尋、播放遙控、播放清單同步",
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
				return errors.New("--port 只能配 --web 使用")
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
	cmd.Flags().Bool("web", false, "在瀏覽器操作:只綁 127.0.0.1,啟動時印一次性網址")
	cmd.Flags().Int("port", 0, "--web 的 port(預設 0 = 動態;不可用 8888)")
	cmd.AddCommand(newDebugCmd())
	cmd.AddCommand(newAuthCmd())
	cmd.AddCommand(newSearchCmd())
	cmd.AddCommand(newPlayCmd())
	cmd.AddCommand(newPlCmd())
	cmd.AddCommand(newMigrateCmd())
	cmd.AddCommand(
		simpleCtl("pause", "暫停播放", "⏸ 已暫停", func(ctx context.Context, pc provider.PlaybackController) error { return pc.Pause(ctx) }),
		simpleCtl("next", "下一首", "⏭ 下一首", func(ctx context.Context, pc provider.PlaybackController) error { return pc.Next(ctx) }),
		simpleCtl("prev", "上一首", "⏮ 上一首", func(ctx context.Context, pc provider.PlaybackController) error { return pc.Prev(ctx) }),
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
// (spec 硬約束:可腳本化/可被 cron 終止是核心價值)。
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return newRootCmd().ExecuteContext(ctx)
}
