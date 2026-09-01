package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
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

// appleKeyFlags 收 --p8/--kid/--team,環境變數 fallback,kid 可從檔名推得。
func appleKeyFlags(cmd *cobra.Command) {
	cmd.Flags().String("p8", "", "MusicKit .p8 路徑(預設 $CAPY_APPLE_P8_PATH)")
	cmd.Flags().String("kid", "", "Key ID(預設 $CAPY_APPLE_KID 或從檔名 AuthKey_<KID>.p8 推得)")
	cmd.Flags().String("team", "", "Team ID(預設 $CAPY_APPLE_TEAM_ID)")
}

func resolveAppleKey(cmd *cobra.Command) (p8, kid, team string, err error) {
	p8, _ = cmd.Flags().GetString("p8")
	if p8 == "" {
		p8 = os.Getenv("CAPY_APPLE_P8_PATH")
	}
	if p8 == "" {
		return "", "", "", fmt.Errorf("需要 --p8 或 CAPY_APPLE_P8_PATH")
	}
	kid, _ = cmd.Flags().GetString("kid")
	if kid == "" {
		kid = os.Getenv("CAPY_APPLE_KID")
	}
	if kid == "" {
		kid = apple.KIDFromPath(p8)
	}
	if kid == "" {
		return "", "", "", fmt.Errorf("無法決定 Key ID:給 --kid,或把檔案命名為 AuthKey_<KID>.p8")
	}
	team, _ = cmd.Flags().GetString("team")
	if team == "" {
		team = os.Getenv("CAPY_APPLE_TEAM_ID")
	}
	if team == "" {
		return "", "", "", fmt.Errorf("需要 --team 或 CAPY_APPLE_TEAM_ID")
	}
	return p8, kid, team, nil
}

func signLocalDevToken(cmd *cobra.Command) (string, error) {
	p8, kid, team, err := resolveAppleKey(cmd)
	if err != nil {
		return "", err
	}
	key, err := apple.LoadP8(p8)
	if err != nil {
		return "", err
	}
	return apple.SignDeveloperToken(key, kid, team, time.Now(), apple.DefaultTTL)
}

func newDebugAppleTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apple-token",
		Short: "以本地 .p8 簽一顆 developer token 印到 stdout(P0-1/P0-2 用)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			tok, err := signLocalDevToken(cmd)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), tok)
			return nil
		},
	}
	appleKeyFlags(cmd)
	return cmd
}
