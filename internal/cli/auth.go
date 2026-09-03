package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/Tai-ch0802/capy-music/internal/browser"
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
	openBrowser                                                                           = browser.Open
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "帳號授權"}
	cmd.AddCommand(newAuthLoginCmd(), newAuthStatusCmd(), newAuthLogoutCmd())
	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login <spotify|apple>",
		Short: "登入平台(Spotify:自己的 app + PKCE;Apple:自抓 web token)",
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
	cmd.Flags().String("developer-token", "", "(apple)developer token;等同 CAPY_APPLE_DEVELOPER_TOKEN。argv 可被 ps 看到,建議用環境變數")
	cmd.Flags().String("user-token", "", "(apple)media-user-token;等同 CAPY_APPLE_USER_TOKEN")
	cmd.Flags().Bool("i-understand", false, "(apple)以 flag/環境變數提供 token 時,表示已閱讀「非 Apple 官方支援」聲明")
	cmd.Flags().Bool("auto", false, "")
	_ = cmd.Flags().MarkHidden("auto") // 未文件化、opt-in、開發者自負(CLAUDE.md 鐵則的唯一例外,見 auto_darwin.go)
	return cmd
}

const appleDisclosure = `⚠️ 非 Apple 官方支援。你要貼上的兩個 token 屬於 Apple 網頁播放器(music.apple.com):
  · Apple 可能隨時更換或撤銷 —— 屆時重新執行 capy auth login apple 即可
  · 以第三方工具存取 Apple Music 的服務條款風險由你自行承擔
  · capy 只指導你複製,不會讀取你的瀏覽器資料`

const appleGuide = `從 Apple 網頁播放器複製 token(約 1 分鐘):
  1. 用瀏覽器開 https://music.apple.com 並登入
  2. 開 DevTools(F12 / ⌥⌘I)→ Network 分頁,篩選 "amp-api"
  3. 隨便點一首歌或播放清單,點任一 amp-api 請求 → Request Headers
  4. 複製 authorization 的值(整串;含不含 "Bearer " 都可)→ developer token
  5. 複製 media-user-token 的值 → user token`

// appleLogin:三條入口(flag/env、TTY 精靈[Task 3]、隱藏 --auto[Task 4])全收口到 applePersist。
// 揭露不可跳過:flag/env 路徑要 --i-understand(拒絕訊息本身就帶聲明);精靈路徑是第一頁的 Confirm。
func appleLogin(cmd *cobra.Command) error {
	dev, _ := cmd.Flags().GetString("developer-token")
	if dev == "" {
		dev = os.Getenv("CAPY_APPLE_DEVELOPER_TOKEN")
	}
	user, _ := cmd.Flags().GetString("user-token")
	if user == "" {
		user = os.Getenv("CAPY_APPLE_USER_TOKEN")
	}
	if dev == "" && user != "" {
		return errors.New("給了 user token(--user-token / CAPY_APPLE_USER_TOKEN)但沒有 developer token — 兩個都給,或都不給(走精靈 / --auto)")
	}
	if auto, _ := cmd.Flags().GetBool("auto"); auto && dev == "" { // 明確提供任一 token 就不走 --auto;user-only 已在上面報錯,故這裡 dev == "" 等同「兩者都沒給」
		// 唯一例外(CLAUDE.md):隱藏、opt-in、開發者自負。揭露照樣不可跳過。
		fmt.Fprintln(cmd.ErrOrStderr(), "--auto:將以 AppleScript 讀取已登入分頁的 MusicKit token(隱藏功能,開發者自負)")
		if stdinIsTTY() {
			if err := confirmAppleDisclosure(); err != nil {
				return err
			}
		} else {
			if ok, _ := cmd.Flags().GetBool("i-understand"); !ok {
				return errors.New(appleDisclosure + "\n\n--auto 在非互動環境需加 --i-understand")
			}
			fmt.Fprintln(cmd.ErrOrStderr(), appleDisclosure) // 非 TTY 沒有 Confirm 頁,揭露要另外印出來(每條路徑都出現;CLAUDE.md 鐵則)
		}
		wt, err := appleAutoTokens()
		if err == nil {
			return applePersist(cmd.Context(), cmd.OutOrStdout(), wt.Developer, wt.User)
		}
		if !stdinIsTTY() {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "自動擷取失敗,改用手動貼上:%v\n", err)
		_, gerr := secret.Get(apple.KeyMusicUserToken)
		dev, user, err = runAppleWizardInputs(gerr == nil)
		if err != nil {
			return err
		}
		return applePersist(cmd.Context(), cmd.OutOrStdout(), dev, user)
	}
	if dev == "" {
		if !stdinIsTTY() {
			return errors.New("非互動環境請設 CAPY_APPLE_DEVELOPER_TOKEN(首次登入另需 CAPY_APPLE_USER_TOKEN)並加 --i-understand。\n" + appleGuide)
		}
		if err := confirmAppleDisclosure(); err != nil {
			return err
		}
		_, err := secret.Get(apple.KeyMusicUserToken)
		dev, user, err = runAppleWizardInputs(err == nil)
		if err != nil {
			return err
		}
		return applePersist(cmd.Context(), cmd.OutOrStdout(), dev, user)
	}
	if ok, _ := cmd.Flags().GetBool("i-understand"); !ok {
		return errors.New(appleDisclosure + "\n\n以 flag / 環境變數提供 token 時,請加 --i-understand 表示已閱讀並同意上述聲明")
	}
	fmt.Fprintln(cmd.ErrOrStderr(), appleDisclosure) // 揭露在指令內、每條路徑都出現(CLAUDE.md 鐵則);印到 stderr,不動 stdout 契約
	return applePersist(cmd.Context(), cmd.OutOrStdout(), dev, user)
}

// 測試替換點(精靈本體需要 TTY;單元測試只測分流,同 Spotify runClientIDWizard 慣例)。
var (
	confirmAppleDisclosure = appleConfirmDisclosure
	runAppleWizardInputs   = appleWizardInputs
	appleAutoTokens        = apple.AutoWebTokens
)

// appleConfirmDisclosure:揭露頁,Confirm 預設「取消」;不同意即 error。CLAUDE.md:不可跳過。
func appleConfirmDisclosure() error {
	agree := false
	if err := huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("使用前請先閱讀").Description(appleDisclosure),
		huh.NewConfirm().Title("我已閱讀,同意自負風險,繼續?").Affirmative("同意").Negative("取消").Value(&agree),
	)).Run(); err != nil {
		return err
	}
	if !agree {
		return errors.New("已取消(未同意聲明)")
	}
	return nil
}

// appleWizardInputs:已有 user token 時先問「只更新 developer token?」(預設是——Apple 輪替時的常態,R-6 唯一緩解)
// → 指引 → 貼 token。回傳 user 空字串 = 只更新 developer token。
func appleWizardInputs(hasUser bool) (dev, user string, err error) {
	onlyDev := hasUser
	if hasUser {
		if err := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title("keychain 已有 user token。只更新 developer token?").
				Affirmative("只更新 developer token").Negative("兩個都重新貼").Value(&onlyDev),
		)).Run(); err != nil {
			return "", "", err
		}
	}
	fields := []huh.Field{
		huh.NewNote().Title("從網頁播放器複製 token").Description(appleGuide),
		huh.NewInput().Title("developer token(authorization 標頭的值)").Value(&dev).Validate(func(s string) error {
			exp, err := apple.JWTExp(apple.NormalizeDevToken(s))
			if err != nil {
				return err
			}
			if !exp.After(time.Now()) {
				return fmt.Errorf("已於 %s 過期,請重新複製", exp.Format(time.RFC3339))
			}
			return nil
		}),
	}
	if !onlyDev {
		fields = append(fields, huh.NewInput().Title("user token(media-user-token 標頭的值)").
			EchoMode(huh.EchoModePassword).Value(&user).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("不可為空")
			}
			return nil
		}))
	}
	if err := huh.NewForm(huh.NewGroup(fields...)).Run(); err != nil {
		return "", "", err
	}
	return dev, user, nil
}

// applePersist:三段驗證(JWT exp → preflight → storefront)全部通過才會落地——驗證期間完全不寫入,
// 視為一個整體(atomic):任一段失敗就直接回傳錯誤,keychain/config 都不會被動到。
// 驗證通過後的三個寫入(developer token → user token → config)依序執行、彼此不是 atomic:
// 若中途失敗(例如前兩個都寫成功、config.Save 才失敗),已寫入的不會回滾——重新執行
// capy auth login apple 用新值覆寫即可,不需要、也沒有實作 rollback。
// user 為空 = 只更新 developer token:用 keychain 既有 user token 跑第三段驗證(順便驗它還活著)。
func applePersist(ctx context.Context, w io.Writer, dev, user string) error {
	dev = apple.NormalizeDevToken(dev)
	exp, err := apple.JWTExp(dev)
	if err != nil {
		return fmt.Errorf("developer token 格式不對(%v)— 應複製 authorization 標頭的值", err)
	}
	if !exp.After(time.Now()) {
		return fmt.Errorf("developer token 已於 %s 過期 — 回網頁播放器重新複製", exp.Format(time.RFC3339))
	}
	user = strings.TrimSpace(user)
	keepUser := user == ""
	if keepUser {
		user, err = secret.Get(apple.KeyMusicUserToken)
		if errors.Is(err, secret.ErrNotFound) {
			return errors.New("keychain 沒有 user token — 首次登入請一併提供 media-user-token")
		}
		if err != nil {
			return err
		}
	}
	cfg, err := config.Load() // 先讀 config:壞掉的 config.json 不該等 keychain 寫完才發現
	if err != nil {
		return err
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	verified, err := appleprov.NewClient(hc, appleAPIBase(), dev, "").Preflight(ctx)
	if err != nil {
		return fmt.Errorf("developer token 被 Apple 拒絕 — 重新複製 authorization 標頭(Apple 可能已輪替):%w", err)
	}
	sf, err := appleprov.NewClient(hc, appleAPIBase(), dev, user).Storefront(ctx)
	if err != nil {
		if !verified {
			err = fmt.Errorf("%w;preflight 回 404,也可能是 API base 或端點形狀不對(CAPY_APPLE_API_BASE,見計畫附錄 A C-0)", err)
		}
		if keepUser {
			return fmt.Errorf("既有 user token 已失效 — 請一併提供新的 media-user-token:%w", err)
		}
		return fmt.Errorf("user token 被 Apple 拒絕 — 重新複製 media-user-token:%w", err)
	}
	if err := apple.SaveDeveloperToken(dev, exp); err != nil {
		return err
	}
	if !keepUser {
		if err := secret.Set(apple.KeyMusicUserToken, user); err != nil {
			return fmt.Errorf("寫入 keychain 失敗:%w", err)
		}
	}
	cfg.AppleStorefront = sf
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(w, "✅ Apple Music 登入完成(storefront %s;developer token 有效至 %s)\n", sf, exp.Format("2006-01-02"))
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
			switch _, exp, err := apple.DeveloperToken(time.Now()); {
			case err == nil:
				fmt.Fprintf(w, "  developer token: 有效至 %s\n", exp.Format(time.RFC3339))
			case errors.Is(err, apple.ErrDevTokenExpired):
				fmt.Fprintf(w, "  developer token: 已於 %s 過期(執行 capy auth login apple)\n", exp.Format(time.RFC3339))
			case errors.Is(err, secret.ErrNotFound):
				fmt.Fprintln(w, "  developer token: 不存在(執行 capy auth login apple)")
			default:
				fmt.Fprintf(w, "  developer token: 讀取 keychain 失敗:%v\n", err)
			}
			switch _, err := secret.Get(apple.KeyMusicUserToken); {
			case err == nil:
				fmt.Fprintln(w, "  user token: 存在")
			case errors.Is(err, secret.ErrNotFound):
				fmt.Fprintln(w, "  user token: 不存在(執行 capy auth login apple)")
			default:
				fmt.Fprintf(w, "  user token: 讀取 keychain 失敗:%v\n", err)
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
