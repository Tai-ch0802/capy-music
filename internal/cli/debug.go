package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/secret"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

func newDebugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "debug",
		Short:  "開發驗證用指令(介面不保證穩定)",
		Hidden: true,
	}
	cmd.AddCommand(newDebugAppleTokenCmd())
	cmd.AddCommand(newDebugDriveLsCmd())
	cmd.AddCommand(newDebugGoogleClientCmd())
	cmd.AddCommand(newDebugLookupISRCCmd())
	return cmd
}

// newDebugLookupISRCCmd:以 ISRC 反查曲目——P4 resolver 的 Layer 1 走的就是這條路,真帳號驗收 R-1 用。
func newDebugLookupISRCCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lookup-isrc <isrc>",
		Short: "以 ISRC 反查曲目(P4 resolver Layer 1 驗收用)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := getProvider(cmd)
			if err != nil {
				return err
			}
			l, err := asISRCLookup(p)
			if err != nil {
				return err
			}
			tracks, err := l.LookupISRC(cmd.Context(), args[0])
			if err != nil {
				return friendlyErr(p.ID(), err)
			}
			if len(tracks) == 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s 沒有曲目符合 ISRC %s\n", p.DisplayName(), args[0])
				return nil
			}
			tty := stdoutIsTTY(cmd)
			rows := trackRows(tracks, tty)
			for i := range rows {
				rows[i] = append(rows[i], tracks[i].ISRC)
			}
			ui.Table(cmd.OutOrStdout(), tty, []string{"ID", "TITLE", "ARTISTS", "ALBUM", "DURATION", "ISRC"}, rows)
			return nil
		},
	}
	providerFlag(cmd)
	return cmd
}

// newDebugGoogleClientCmd:印出這顆 binary 內建的 Google client ID——CI 用它斷言 release 的 -ldflags 注入
// 真的到得了程式(決策 9;go tool link -X 對不存在的符號是靜默 no-op,沒人驗就會靜靜出貨)。
// 只印 ID、永遠不印 secret:ID 本來就會出現在授權網址裡,secret 不會。--secret-state 只回 set / unset。
func newDebugGoogleClientCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "google-client",
		Short: "印出內建的 Google client ID(release 注入檢查用;不印 secret)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if auth.BuiltinGoogleClientID == "" {
				return errors.New("這顆 binary 沒有內建 Google client(go install / 自己 build 的都沒有):auth login google 會走 BYO 精靈")
			}
			if state, _ := cmd.Flags().GetBool("secret-state"); state {
				if auth.BuiltinGoogleClientSecret == "" {
					fmt.Fprintln(cmd.OutOrStdout(), "unset")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "set")
				}
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), auth.BuiltinGoogleClientID)
			return nil
		},
	}
	cmd.Flags().Bool("secret-state", false, "改印內建 secret 有沒有注入(set / unset),不印 secret 本身")
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
