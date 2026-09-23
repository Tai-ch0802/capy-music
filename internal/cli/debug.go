package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
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
		Short:  "Developer verification commands (interface not guaranteed stable)",
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
		Short: "Look up tracks by ISRC (P4 resolver Layer 1 acceptance)",
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
				fmt.Fprintf(cmd.ErrOrStderr(), "%s has no track matching ISRC %s\n", p.DisplayName(), args[0])
				return nil
			}
			tty := stdoutIsTTY(cmd)
			rows := make([][]string, len(tracks))
			for i, tr := range tracks { // 自己組 row:不靠 trackRows「不過濾、不重排」的隱性前提把 ISRC 接到別首歌上
				dur := strconv.Itoa(tr.DurationMS)
				if tty {
					dur = ui.FormatDuration(tr.DurationMS)
				}
				rows[i] = []string{tr.ProviderID, tr.Title, strings.Join(tr.Artists, ", "), tr.Album, dur, tr.ISRC}
			}
			return ui.Table(cmd.OutOrStdout(), tty, []string{"ID", "TITLE", "ARTISTS", "ALBUM", "DURATION", "ISRC"}, rows)
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
		Short: "Print the built-in Google client ID (release injection check; never prints the secret)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if auth.BuiltinGoogleClientID == "" {
				return errors.New("this binary has no built-in Google client (go install and local builds never do): auth login google will use the BYO wizard")
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
	cmd.Flags().Bool("secret-state", false, "print whether the built-in secret was injected (set / unset) instead; never the secret itself")
	return cmd
}

// newDebugAppleTokenCmd:印出 keychain 裡已登入的 Apple token(dev 或 --user),
// 給 scripts/p0 用。登入本身走 capy auth login apple——這裡不簽發、不擷取。
func newDebugAppleTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apple-token",
		Short: "Print the Apple developer / user token from the keychain (for scripts/p0)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			asUser, _ := cmd.Flags().GetBool("user")
			if asUser {
				tok, err := secret.Get(apple.KeyMusicUserToken)
				if err != nil {
					return errors.New("no user token in the keychain — run capy auth login apple")
				}
				fmt.Fprintln(cmd.OutOrStdout(), tok)
				return nil
			}
			dev, _, err := apple.DeveloperToken(time.Now())
			if err != nil {
				return fmt.Errorf("no usable developer token in the keychain — run capy auth login apple: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), dev)
			return nil
		},
	}
	cmd.Flags().Bool("user", false, "print the user token (media-user-token) instead of the developer token")
	return cmd
}
