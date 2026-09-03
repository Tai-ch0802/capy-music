package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/config"
	appleprov "github.com/Tai-ch0802/capy-music/internal/provider/apple"
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
		Use:   "login <spotify|apple>",
		Short: "登入平台(BYO Client ID + PKCE)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "apple":
				return appleLogin(cmd)
			case "spotify":
				// 走下方既有流程。
			default:
				return fmt.Errorf("目前支援 spotify、apple(google 於 P3)")
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
				return fmt.Errorf("Client ID 應為 32 位小寫十六進位字串(從 dashboard 複製)")
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "在瀏覽器完成 Spotify 授權…(180s 內)")
			ctx, cancel := context.WithTimeout(cmd.Context(), 180*time.Second)
			defer cancel()
			if _, err := spotifyLogin(ctx, cid, openBrowser); err != nil {
				return err
			}
			// 授權成功後才落地 client ID——失敗不留半殘的 config(review 便條)。
			cfg.SpotifyClientID = cid
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "✅ Spotify 授權完成(refresh token 已入 keychain)")
			return nil
		},
	}
	cmd.Flags().String("client-id", "", "你的 Spotify app Client ID(略過精靈)")
	return cmd
}

// appleLogin:developer token(來源鏈)→ preflight → MUT(MusicKit 橋接,180s)→ storefront → 存 keychain/config。
// 成功後才 Save config——失敗不留半殘狀態(P1 review 教訓)。
func appleLogin(cmd *cobra.Command) error {
	_ = secret.Delete(apple.KeyDeveloperToken) // login = 重來整條鏈;容忍 ErrNotFound,壞快取不該一直被信任
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	config.EnsureInstallID(cfg) // 成功後才 Save
	dev, src, err := apple.DeveloperToken(cmd.Context(), devTokenOptsFromEnv(cfg))
	if err != nil {
		return err
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	if err := appleprov.NewClient(hc, appleAPIBase(), dev, "").Preflight(cmd.Context()); err != nil {
		return fmt.Errorf("developer token 被 Apple 拒絕 — BYO 請檢查 .p8/Key ID/Team ID,Worker 請檢查 secrets:%w", err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "developer token 來源:%s;在瀏覽器完成 Apple Music 授權…(180s 內)\n", src)
	ctx, cancel := context.WithTimeout(cmd.Context(), 180*time.Second)
	defer cancel()
	mut, err := appleAuthorize(ctx, dev, openBrowser)
	if err != nil {
		return err
	}
	sf, err := appleprov.NewClient(hc, appleAPIBase(), dev, mut).Storefront(ctx)
	if err != nil {
		return friendlyErr("apple", err)
	}
	if err := secret.Set(apple.KeyMusicUserToken, mut); err != nil {
		return fmt.Errorf("寫入 keychain 失敗:%w", err)
	}
	cfg.AppleStorefront = sf
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "✅ Apple Music 授權完成(storefront %s;user token 已入 keychain)\n", sf)
	return nil
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
					return errors.New("Client ID 應為 32 位小寫十六進位字串(從 dashboard 複製)")
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
			fmt.Fprintln(w, "apple:")
			if raw, err := secret.Get(apple.KeyDeveloperToken); err == nil {
				var c struct{ Exp int64 }
				_ = json.Unmarshal([]byte(raw), &c)
				fmt.Fprintf(w, "  developer token: 快取有效至 %s\n", time.Unix(c.Exp, 0).Format(time.RFC3339))
			} else {
				fmt.Fprintln(w, "  developer token: 無快取(下次使用時取得)")
			}
			if _, err := secret.Get(apple.KeyMusicUserToken); err == nil {
				fmt.Fprintln(w, "  user token: 存在")
			} else {
				fmt.Fprintln(w, "  user token: 不存在(執行 capy auth login apple)")
			}
			if cfg.AppleStorefront != "" {
				fmt.Fprintf(w, "  storefront: %s\n", cfg.AppleStorefront)
			} else {
				fmt.Fprintln(w, "  storefront: 未設定")
			}
			return nil
		},
	}
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout <spotify|apple>",
		Short: "登出平台(刪除 keychain 憑證;client_id 保留在 config)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "spotify":
				if err := secret.Delete(auth.KeySpotifyRefreshToken); err != nil && !errors.Is(err, secret.ErrNotFound) {
					return err
				}
			case "apple":
				if err := secret.Delete(apple.KeyMusicUserToken); err != nil && !errors.Is(err, secret.ErrNotFound) {
					return err
				}
				if err := secret.Delete(apple.KeyDeveloperToken); err != nil && !errors.Is(err, secret.ErrNotFound) {
					return err
				}
			default:
				return fmt.Errorf("目前支援 spotify、apple")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "已登出 %s\n", args[0])
			return nil
		},
	}
}
