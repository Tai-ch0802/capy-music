package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

func newDebugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "debug",
		Short:  "開發驗證用指令(介面不保證穩定)",
		Hidden: true,
	}
	cmd.AddCommand(newDebugAppleTokenCmd())
	return cmd
}

// newDebugAppleTokenCmd:印出 keychain 裡已登入的 Apple token(dev 或 --user),
// 給 scripts/p0 用。登入本身走 capy auth login apple——這裡不簽發、不擷取。
func newDebugAppleTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apple-token",
		Short: "印出 keychain 裡的 Apple developer / user token(scripts/p0 用)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			asUser, _ := cmd.Flags().GetBool("user")
			if asUser {
				tok, err := secret.Get(apple.KeyMusicUserToken)
				if err != nil {
					return errors.New("keychain 沒有 user token — 執行 capy auth login apple")
				}
				fmt.Fprintln(cmd.OutOrStdout(), tok)
				return nil
			}
			dev, _, err := apple.DeveloperToken(time.Now())
			if err != nil {
				return fmt.Errorf("keychain 沒有可用的 developer token — 執行 capy auth login apple:%w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), dev)
			return nil
		},
	}
	cmd.Flags().Bool("user", false, "印 user token(media-user-token)而非 developer token")
	return cmd
}
