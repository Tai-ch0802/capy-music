package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/cache"
)

func newHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "history", Short: "最近搜尋/播放紀錄(本機快取,供補全與挑選器)"}
	cmd.AddCommand(&cobra.Command{
		Use: "clear", Short: "清空最近紀錄(播放清單快取保留)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := cache.Load()
			n := len(c.Recent)
			c.ClearRecent()
			if err := c.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "已清空最近紀錄(%d 筆)\n", n)
			return nil
		},
	})
	return cmd
}
