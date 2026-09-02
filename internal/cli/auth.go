package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/secret"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

var clientIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// 測試替換點(spotifyLogin 顯式標型別,oauth2 import 因此有正當用途)。
var (
	spotifyLogin func(context.Context, string, func(string) error) (*oauth2.Token, error) = auth.LoginSpotify
	stdinIsTTY                                                                            = func() bool { return ui.IsTTY(os.Stdin) }
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "帳號授權"}
	cmd.AddCommand(newAuthLoginCmd(), newAuthStatusCmd(), newAuthLogoutCmd())
	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login <spotify>",
		Short: "登入平台(BYO Client ID + PKCE)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "spotify" {
				return fmt.Errorf("目前僅支援 spotify(apple 於 P2、google 於 P3 進場)")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cid, _ := cmd.Flags().GetString("client-id")
			if cid == "" {
				cid = cfg.SpotifyClientID
			}
			if cid == "" {
				if !stdinIsTTY() {
					return fmt.Errorf("非互動環境請用 --client-id(建 app 步驟:https://developer.spotify.com/dashboard,redirect URI 填 http://127.0.0.1:8888/callback)")
				}
				cid, err = runClientIDWizard()
				if err != nil {
					return err
				}
			}
			cid = strings.TrimSpace(cid)
			if !clientIDRe.MatchString(cid) {
				return fmt.Errorf("Client ID 應為 32 位十六進位字串,得到 %q", cid)
			}
			cfg.SpotifyClientID = cid
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "在瀏覽器完成 Spotify 授權…(180s 內)")
			ctx, cancel := context.WithTimeout(cmd.Context(), 180*time.Second)
			defer cancel()
			if _, err := spotifyLogin(ctx, cid, openBrowser); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "✅ Spotify 授權完成(refresh token 已入 keychain)")
			return nil
		},
	}
	cmd.Flags().String("client-id", "", "你的 Spotify app Client ID(略過精靈)")
	return cmd
}

// runClientIDWizard:BYO onboarding(spec §4.2)。
// charm v2 調整條款:huh v2 API 與此處有出入時,以 go doc charm.land/huh/v2 為準,偏差記入報告。
func runClientIDWizard() (string, error) {
	var cid string
	form := huh.NewForm(huh.NewGroup(
		huh.NewNote().
			Title("建立你自己的 Spotify app(免費,約 2 分鐘)").
			Description("Spotify 政策限制每個 app 只能有 5 位使用者,所以要用自己的 app:\n"+
				"1. 開 https://developer.spotify.com/dashboard\n"+
				"2. Create app,名稱隨意\n"+
				"3. Redirect URI 填入(完全照抄): http://127.0.0.1:8888/callback\n"+
				"4. 勾選 Web API → Save\n"+
				"5. 複製 Client ID 貼到下一欄"),
		huh.NewInput().
			Title("Client ID").
			Value(&cid).
			Validate(func(s string) error {
				if !clientIDRe.MatchString(strings.TrimSpace(s)) {
					return errors.New("應為 32 位十六進位字串")
				}
				return nil
			}),
	))
	if err := form.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(cid), nil
}

// maskClientID:顯示頭尾各 4 碼;格式異常時不切片、直接指出下一步。
func maskClientID(id string) string {
	if !clientIDRe.MatchString(id) {
		return "已設定但格式異常(應為 32 位十六進位)— 重跑 capy auth login spotify"
	}
	return "已設定(" + id[:4] + "…" + id[len(id)-4:] + ")"
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "顯示各平台授權狀態(離線檢查;線上驗證用 capy doctor)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintln(w, "spotify:")
			if cfg.SpotifyClientID != "" {
				fmt.Fprintf(w, "  client_id: %s\n", maskClientID(cfg.SpotifyClientID))
			} else {
				fmt.Fprintln(w, "  client_id: 未設定")
			}
			if _, err := secret.Get(auth.KeySpotifyRefreshToken); err == nil {
				fmt.Fprintln(w, "  refresh token: keychain 存在")
			} else {
				fmt.Fprintln(w, "  refresh token: 不存在(執行 capy auth login spotify)")
			}
			return nil
		},
	}
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout <spotify>",
		Short: "登出平台(刪除 keychain 憑證;client_id 保留在 config)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "spotify" {
				return fmt.Errorf("目前僅支援 spotify")
			}
			if err := secret.Delete(auth.KeySpotifyRefreshToken); err != nil && !errors.Is(err, secret.ErrNotFound) {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "已登出 spotify")
			return nil
		},
	}
}
