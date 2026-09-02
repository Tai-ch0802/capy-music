package cli

import "github.com/spf13/cobra"

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
	return cmd
}

// Execute 是 CLI 進入點。
func Execute() error {
	return newRootCmd().Execute()
}
