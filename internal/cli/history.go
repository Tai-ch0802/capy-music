package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

func newHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "history", Short: i18n.T("cmd.history.short")}
	cmd.AddCommand(&cobra.Command{
		Use: "clear", Short: i18n.T("cmd.history.clear.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := cache.Load()
			n := len(c.Recent)
			c.ClearRecent()
			if err := c.Save(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), i18n.T("history.cleared", "count", n))
			return nil
		},
	})
	return cmd
}
