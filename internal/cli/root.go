package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// version 由 release 流程以 ldflags 注入;開發環境為 dev。
var version = "dev"

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "capy",
		Short:        "跨平台音樂 CLI:搜尋、播放遙控、播放清單同步",
		Version:      version,
		SilenceUsage: true,
	}
	cmd.AddCommand(newDebugCmd())
	cmd.AddCommand(newAuthCmd())
	cmd.AddCommand(newSearchCmd())
	cmd.AddCommand(newPlayCmd())
	cmd.AddCommand(newPlCmd())
	cmd.AddCommand(
		simpleCtl("pause", "暫停播放", "⏸ 已暫停", func(ctx context.Context, p *spotifyProviderT) error { return p.Pause(ctx) }),
		simpleCtl("next", "下一首", "⏭ 下一首", func(ctx context.Context, p *spotifyProviderT) error { return p.Next(ctx) }),
		simpleCtl("prev", "上一首", "⏮ 上一首", func(ctx context.Context, p *spotifyProviderT) error { return p.Prev(ctx) }),
	)
	cmd.AddCommand(newNowCmd(), newDevicesCmd())
	cmd.AddCommand(newDoctorCmd())
	return cmd
}

// Execute 是 CLI 進入點。SIGINT/SIGTERM 取消 ctx,讓 429 退避等待中的請求可被中斷
// (spec 硬約束:可腳本化/可被 cron 終止是核心價值)。
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return newRootCmd().ExecuteContext(ctx)
}
