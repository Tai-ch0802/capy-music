package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

const keyDefaultProvider = "default_provider"

// ponytail: 目前只有一個 key,用 switch;第三個 key 出現再改成表。
func configGet(c *config.Config, key string) (string, error) {
	switch key {
	case keyDefaultProvider:
		return c.DefaultProvider, nil
	}
	return "", fmt.Errorf("未知的設定 %q(可用:%s)", key, keyDefaultProvider)
}

func configSet(c *config.Config, key, val string) error {
	switch key {
	case keyDefaultProvider:
		if val != "spotify" && val != "apple" {
			return fmt.Errorf("%s 只能是 spotify 或 apple,得到 %q", key, val)
		}
		c.DefaultProvider = val
		return nil
	}
	return fmt.Errorf("未知的設定 %q(可用:%s)", key, keyDefaultProvider)
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "讀寫非機密設定(config.json;憑證一律在 keychain)"}
	cmd.AddCommand(
		&cobra.Command{
			Use: "get <key>", Short: "印出設定值(未設印空行)", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := config.Load()
				if err != nil {
					return err
				}
				v, err := configGet(c, args[0])
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), v)
				return nil
			},
		},
		&cobra.Command{
			Use: "set <key> <value>", Short: "寫入設定值", Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := config.Load()
				if err != nil {
					return err
				}
				if err := configSet(c, args[0], args[1]); err != nil {
					return err
				}
				if err := config.Save(c); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s = %s\n", args[0], args[1])
				return nil
			},
		},
		&cobra.Command{
			Use: "list", Short: "列出所有設定(非 TTY 為 key\\tvalue)", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := config.Load()
				if err != nil {
					return err
				}
				tty := stdoutIsTTY(cmd)
				v := c.DefaultProvider
				if tty && v == "" {
					v = "(未設,預設 spotify)"
				}
				ui.Table(cmd.OutOrStdout(), tty, []string{"設定", "值"}, [][]string{{keyDefaultProvider, v}})
				return nil
			},
		},
	)
	return cmd
}

// defaultProviderHint:登入成功後,尚未設預設平台就提示一行(不自動設)。
func defaultProviderHint(cmd *cobra.Command, id string) {
	if c, err := config.Load(); err == nil && c.DefaultProvider == "" {
		fmt.Fprintf(cmd.OutOrStdout(), "提示:`capy config set default_provider %s` 可把 %s 設為預設平台(之後不必帶 --provider)\n", id, id)
	}
}
