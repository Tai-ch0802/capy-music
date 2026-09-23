package cli

import (
	"context"
	"encoding/json"
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
	"github.com/Tai-ch0802/capy-music/internal/i18n"
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
	cmd := &cobra.Command{Use: "auth", Short: i18n.T("cmd.auth.short")}
	cmd.AddCommand(newAuthLoginCmd(), newAuthStatusCmd(), newAuthLogoutCmd())
	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login <spotify|apple|google>",
		Short: i18n.T("cmd.auth.login.short"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "apple":
				if err := appleLogin(cmd); err != nil {
					return err
				}
				defaultProviderHint(cmd, "apple")
				return nil
			case "google":
				return googleLogin(cmd) // 不是音樂 provider,不提示 default_provider
			case "spotify":
				// 走下方既有流程。
			case "local":
				return i18n.Errorf("auth.err.local_no_credentials")
			default:
				return i18n.Errorf("auth.err.unsupported_provider")
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
					return i18n.Errorf("auth.spotify.err.non_tty_client_id")
				}
				cid, err = runClientIDWizard()
				if err != nil {
					return err
				}
			}
			cid = strings.TrimSpace(cid)
			if err := validateSpotifyClientID(cid); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), i18n.T("auth.spotify.waiting"))
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
			fmt.Fprintln(cmd.OutOrStdout(), i18n.T("auth.spotify.done"))
			defaultProviderHint(cmd, "spotify")
			return nil
		},
	}
	cmd.Flags().String("client-id", "", i18n.T("cmd.auth.login.flag.client_id"))
	cmd.Flags().String("client-secret", "", i18n.T("cmd.auth.login.flag.client_secret"))
	cmd.Flags().String("developer-token", "", i18n.T("cmd.auth.login.flag.developer_token"))
	cmd.Flags().String("user-token", "", i18n.T("cmd.auth.login.flag.user_token"))
	cmd.Flags().Bool("i-understand", false, i18n.T("cmd.auth.login.flag.i_understand"))
	cmd.Flags().Bool("auto", false, "")
	_ = cmd.Flags().MarkHidden("auto") // 未文件化、opt-in、開發者自負(CLAUDE.md 鐵則的唯一例外,見 auto_darwin.go)
	return cmd
}

// appleDisclosure / appleGuide:用到時才翻(命令樹可能在語系切換後重建;web 的提示橋也共用)。
// 揭露每個語系都要完整(CLAUDE.md 鐵則),i18n_en_auth_test.go 逐語系釘住。
func appleDisclosure() string { return i18n.T("auth.apple.disclosure") }

func appleGuide() string { return i18n.T("auth.apple.guide") }

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
		return i18n.Errorf("auth.apple.err.user_without_dev")
	}
	if auto, _ := cmd.Flags().GetBool("auto"); auto && dev == "" { // 明確提供任一 token 就不走 --auto;user-only 已在上面報錯,故這裡 dev == "" 等同「兩者都沒給」
		// 唯一例外(CLAUDE.md):隱藏、opt-in、開發者自負。揭露照樣不可跳過。
		fmt.Fprintln(cmd.ErrOrStderr(), i18n.T("auth.apple.auto_notice"))
		if stdinIsTTY() {
			if err := confirmAppleDisclosure(); err != nil {
				return err
			}
		} else {
			if ok, _ := cmd.Flags().GetBool("i-understand"); !ok {
				return i18n.Errorf("auth.apple.err.auto_needs_i_understand", "disclosure", appleDisclosure())
			}
			fmt.Fprintln(cmd.ErrOrStderr(), appleDisclosure()) // 非 TTY 沒有 Confirm 頁,揭露要另外印出來(每條路徑都出現;CLAUDE.md 鐵則)
		}
		wt, err := appleAutoTokens()
		if err == nil {
			return applePersist(cmd.Context(), cmd.OutOrStdout(), wt.Developer, wt.User)
		}
		if !stdinIsTTY() {
			return err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), i18n.T("auth.apple.auto_failed", "err", err))
		_, gerr := secret.Get(apple.KeyMusicUserToken)
		dev, user, err = runAppleWizardInputs(gerr == nil)
		if err != nil {
			return err
		}
		return applePersist(cmd.Context(), cmd.OutOrStdout(), dev, user)
	}
	if dev == "" {
		if !stdinIsTTY() {
			return i18n.Errorf("auth.apple.err.non_tty_needs_env", "guide", appleGuide())
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
		return i18n.Errorf("auth.apple.err.flags_need_i_understand", "disclosure", appleDisclosure())
	}
	fmt.Fprintln(cmd.ErrOrStderr(), appleDisclosure()) // 揭露在指令內、每條路徑都出現(CLAUDE.md 鐵則);印到 stderr,不動 stdout 契約
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
	if err := newForm(huh.NewGroup(
		huh.NewNote().Title(i18n.T("auth.apple.confirm.title")).Description(appleDisclosure()),
		huh.NewConfirm().Title(i18n.T("auth.apple.confirm.question")).
			Affirmative(i18n.T("auth.apple.confirm.agree")).Negative(i18n.T("auth.apple.confirm.cancel")).Value(&agree),
	)).Run(); err != nil {
		return err
	}
	if !agree {
		return i18n.Errorf("auth.apple.err.declined")
	}
	return nil
}

// appleWizardInputs:已有 user token 時先問「只更新 developer token?」(預設是——Apple 輪替時的常態,R-6 唯一緩解)
// → 指引 → 貼 token。回傳 user 空字串 = 只更新 developer token。
func appleWizardInputs(hasUser bool) (dev, user string, err error) {
	onlyDev := hasUser
	if hasUser {
		if err := newForm(huh.NewGroup(
			huh.NewConfirm().Title(i18n.T("auth.apple.wizard.only_dev_question")).
				Affirmative(i18n.T("auth.apple.wizard.only_dev")).Negative(i18n.T("auth.apple.wizard.both")).Value(&onlyDev),
		)).Run(); err != nil {
			return "", "", err
		}
	}
	fields := []huh.Field{
		huh.NewNote().Title(i18n.T("auth.apple.wizard.guide_title")).Description(appleGuide()),
		huh.NewInput().Title(i18n.T("auth.apple.wizard.dev_label")).Value(&dev).Validate(validateAppleDevToken),
	}
	if !onlyDev {
		fields = append(fields, huh.NewInput().Title(i18n.T("auth.apple.wizard.user_label")).
			EchoMode(huh.EchoModePassword).Value(&user).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return i18n.Errorf("auth.apple.err.empty")
			}
			return nil
		}))
	}
	if err := newForm(huh.NewGroup(fields...)).Run(); err != nil {
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
		return i18n.Errorf("auth.apple.err.dev_token_malformed", "err", err)
	}
	if !exp.After(time.Now()) {
		return i18n.Errorf("auth.apple.err.dev_token_expired", "time", exp.Format(time.RFC3339))
	}
	user = strings.TrimSpace(user)
	keepUser := user == ""
	if keepUser {
		user, err = secret.Get(apple.KeyMusicUserToken)
		if errors.Is(err, secret.ErrNotFound) {
			return i18n.Errorf("auth.apple.err.no_stored_user_token")
		}
		if err != nil {
			return err
		}
	}
	cfg, err := config.Load() // 先讀 config:壞掉的 config.json 不該等 keychain 寫完才發現
	if err != nil {
		return err
	}
	base, err := appleAPIBase()
	if err != nil {
		return err
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	verified, err := appleprov.NewClient(hc, base, dev, "").Preflight(ctx)
	if err != nil {
		return i18n.Errorf("auth.apple.err.dev_token_rejected", "err", err)
	}
	sf, err := appleprov.NewClient(hc, base, dev, user).Storefront(ctx)
	if err != nil {
		if !verified {
			err = i18n.Errorf("auth.apple.err.api_base_hint", "err", err)
		}
		if keepUser {
			return i18n.Errorf("auth.apple.err.stored_user_token_dead", "err", err)
		}
		return i18n.Errorf("auth.apple.err.user_token_rejected", "err", err)
	}
	if err := apple.SaveDeveloperToken(dev, exp); err != nil {
		return err
	}
	if !keepUser {
		if err := secret.Set(apple.KeyMusicUserToken, user); err != nil {
			return i18n.Errorf("auth.err.keychain_write", "err", err)
		}
	}
	cfg.AppleStorefront = sf
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintln(w, i18n.T("auth.apple.done", "storefront", sf, "expiry", exp.Format("2006-01-02")))
	return nil
}

// spotifyAppTitle / spotifyAppSteps:BYO 精靈的說明原文;huh 表單與 web 提示橋共用同一份。
func spotifyAppTitle() string { return i18n.T("auth.spotify.app_title") }

func spotifyAppSteps() string { return i18n.T("auth.spotify.app_steps") }

// runClientIDWizard:BYO onboarding(spec §4.2)。測試 / web 替換點(P7 決策 40:web 改走表單提示橋)。
// charm v2 調整條款:huh v2 API 與此處有出入時,以 go doc charm.land/huh/v2 為準,偏差記入報告。
var runClientIDWizard = func() (string, error) {
	var cid string
	form := newForm(huh.NewGroup(
		huh.NewNote().
			Title(spotifyAppTitle()).
			Description(spotifyAppSteps()),
		huh.NewInput().
			Title("Client ID").
			Value(&cid).
			Validate(validateSpotifyClientID),
	))
	if err := form.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(cid), nil
}

// validateSpotifyClientID / validateAppleDevToken:huh 表單的 Validate 與 --client-id / env 路徑共用同一份
// (P7 T1 抽出:web 的表單提示橋也用它們;錯誤訊息一字不差)。
func validateSpotifyClientID(s string) error {
	if !clientIDRe.MatchString(strings.TrimSpace(s)) {
		return i18n.Errorf("auth.spotify.err.bad_client_id")
	}
	return nil
}

func validateAppleDevToken(s string) error {
	exp, err := apple.JWTExp(apple.NormalizeDevToken(s))
	if err != nil {
		return err
	}
	if !exp.After(time.Now()) {
		return i18n.Errorf("auth.apple.err.token_expired_recopy", "time", exp.Format(time.RFC3339))
	}
	return nil
}

// maskClientID:顯示頭尾各 4 碼;格式異常時不切片、直接指出下一步。
func maskClientID(id string) string {
	if !clientIDRe.MatchString(id) {
		return i18n.T("auth.status.client_id_malformed")
	}
	return i18n.T("auth.status.client_id_set", "head", id[:4], "tail", id[len(id)-4:])
}

func newAuthStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: i18n.T("cmd.auth.status.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(authStatusOf(cfg))
			}
			fmt.Fprintln(w, "spotify:")
			if cfg.SpotifyClientID != "" {
				fmt.Fprintf(w, "  client_id: %s\n", maskClientID(cfg.SpotifyClientID))
			} else {
				fmt.Fprintln(w, "  client_id: "+i18n.T("auth.status.not_set"))
			}
			if err := auth.SpotifyStored(); err == nil { // 新鍵優先,尚未升級的舊鍵也算「已登入」
				fmt.Fprintln(w, "  refresh token: "+i18n.T("auth.status.in_keychain"))
			} else {
				fmt.Fprintln(w, "  refresh token: "+i18n.T("auth.status.token_missing", "provider", "spotify"))
			}
			fmt.Fprintln(w, "google:")
			switch {
			case cfg.GoogleClientID != "":
				fmt.Fprintln(w, "  client: "+i18n.T("auth.status.google_config", "client", maskGoogleClientID(cfg.GoogleClientID)))
			case auth.BuiltinGoogleClientID != "":
				fmt.Fprintln(w, "  client: "+i18n.T("auth.status.google_builtin"))
			default:
				fmt.Fprintln(w, "  client: "+i18n.T("auth.status.google_not_set"))
			}
			switch tok, err := auth.GoogleStored(); {
			case err == nil:
				fmt.Fprintln(w, "  token: "+i18n.T("auth.status.google_token", "expiry", tok.Expiry.Local().Format(time.RFC3339)))
			case errors.Is(err, secret.ErrNotFound):
				fmt.Fprintln(w, "  token: "+i18n.T("auth.status.token_missing", "provider", "google"))
			default:
				fmt.Fprintln(w, "  token: "+i18n.T("auth.status.keychain_read_failed", "err", err))
			}
			if cfg.GoogleEmail != "" {
				fmt.Fprintf(w, "  email: %s\n", cfg.GoogleEmail)
			}
			if cfg.DeviceID != "" {
				fmt.Fprintf(w, "  device_id: %s\n", cfg.DeviceID)
			}
			fmt.Fprintln(w, "apple:")
			switch _, exp, err := apple.DeveloperToken(time.Now()); {
			case err == nil:
				fmt.Fprintln(w, "  developer token: "+i18n.T("auth.status.valid_until", "expiry", exp.Format(time.RFC3339)))
			case errors.Is(err, apple.ErrDevTokenExpired):
				fmt.Fprintln(w, "  developer token: "+i18n.T("auth.status.dev_token_expired", "expiry", exp.Format(time.RFC3339)))
			case errors.Is(err, secret.ErrNotFound):
				fmt.Fprintln(w, "  developer token: "+i18n.T("auth.status.token_missing", "provider", "apple"))
			default:
				fmt.Fprintln(w, "  developer token: "+i18n.T("auth.status.keychain_read_failed", "err", err))
			}
			switch _, err := secret.Get(apple.KeyMusicUserToken); {
			case err == nil:
				fmt.Fprintln(w, "  user token: "+i18n.T("auth.status.present"))
			case errors.Is(err, secret.ErrNotFound):
				fmt.Fprintln(w, "  user token: "+i18n.T("auth.status.token_missing", "provider", "apple"))
			default:
				fmt.Fprintln(w, "  user token: "+i18n.T("auth.status.keychain_read_failed", "err", err))
			}
			if cfg.AppleStorefront != "" {
				fmt.Fprintf(w, "  storefront: %s\n", cfg.AppleStorefront)
			} else {
				fmt.Fprintln(w, "  storefront: "+i18n.T("auth.status.not_set"))
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, i18n.T("cmd.auth.status.flag.json"))
	return cmd
}

// authStatus:auth status --json 的形狀——給腳本與網頁帳號頁的契約(README「給腳本讀的登入狀態」):欄位只增不改,
// 列舉值是固定的英文、不翻譯。只有「有沒有、何時到期、從哪來」這類事實,**絕不含任何 token / secret 的值**,
// client ID 也只給列舉(同文字版只露頭尾)。時間一律 UTC 的 RFC 3339。
type authStatus struct {
	Spotify struct {
		State    string `json:"state"`     // ok | missing | keychain_error(refresh token)
		ClientID string `json:"client_id"` // set | missing | malformed
	} `json:"spotify"`
	Google struct {
		State             string `json:"state"`                         // ok | missing | keychain_error
		Client            string `json:"client"`                        // config(自建)| builtin(release 內建)| none
		AccessTokenExpiry string `json:"access_token_expiry,omitempty"` // 存著的 access token 何時到期(會自動換發,不是登入的期限)
		Email             string `json:"email,omitempty"`
		DeviceID          string `json:"device_id,omitempty"`
	} `json:"google"`
	Apple struct {
		State                string `json:"state"`           // 兩個 token 合起來:keychain_error > expired > missing > ok
		DeveloperToken       string `json:"developer_token"` // ok | missing | expired | keychain_error
		DeveloperTokenExpiry string `json:"developer_token_expiry,omitempty"`
		UserToken            string `json:"user_token"` // ok | missing | keychain_error
		Storefront           string `json:"storefront,omitempty"`
	} `json:"apple"`
}

// keychainState:讀 keychain 的結果 → ok / missing / keychain_error。
func keychainState(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, secret.ErrNotFound):
		return "missing"
	}
	return "keychain_error"
}

// authStatusOf:跟文字版讀同樣的來源(文字版的輸出一個字都不動,所以不共用它的程式碼)。
func authStatusOf(cfg *config.Config) authStatus {
	var st authStatus
	st.Spotify.State = keychainState(auth.SpotifyStored())
	switch {
	case cfg.SpotifyClientID == "":
		st.Spotify.ClientID = "missing"
	case clientIDRe.MatchString(cfg.SpotifyClientID):
		st.Spotify.ClientID = "set"
	default:
		st.Spotify.ClientID = "malformed"
	}

	switch {
	case cfg.GoogleClientID != "":
		st.Google.Client = "config"
	case auth.BuiltinGoogleClientID != "":
		st.Google.Client = "builtin"
	default:
		st.Google.Client = "none"
	}
	tok, err := auth.GoogleStored()
	if st.Google.State = keychainState(err); err == nil && !tok.Expiry.IsZero() {
		st.Google.AccessTokenExpiry = tok.Expiry.UTC().Format(time.RFC3339)
	}
	st.Google.Email, st.Google.DeviceID = cfg.GoogleEmail, cfg.DeviceID

	a := &st.Apple
	_, exp, err := apple.DeveloperToken(time.Now())
	if a.DeveloperToken = keychainState(err); errors.Is(err, apple.ErrDevTokenExpired) {
		a.DeveloperToken = "expired"
	}
	if a.DeveloperToken == "ok" || a.DeveloperToken == "expired" {
		a.DeveloperTokenExpiry = exp.UTC().Format(time.RFC3339)
	}
	_, err = secret.Get(apple.KeyMusicUserToken)
	a.UserToken = keychainState(err)
	switch {
	case a.DeveloperToken == "keychain_error" || a.UserToken == "keychain_error":
		a.State = "keychain_error"
	case a.DeveloperToken == "expired":
		a.State = "expired"
	case a.DeveloperToken == "ok" && a.UserToken == "ok":
		a.State = "ok"
	default:
		a.State = "missing"
	}
	a.Storefront = cfg.AppleStorefront
	return st
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout <spotify|apple|google>",
		Short: i18n.T("cmd.auth.logout.short"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "spotify":
				// 刪哪幾個鍵、要不要鎖,是 auth package 的內部知識(見 auth.LogoutSpotify)。
				if err := auth.LogoutSpotify(cmd.Context()); err != nil {
					return err
				}
			case "apple":
				if err := secret.Delete(apple.KeyMusicUserToken); err != nil && !errors.Is(err, secret.ErrNotFound) {
					return err
				}
				if err := secret.Delete(apple.KeyDeveloperToken); err != nil && !errors.Is(err, secret.ErrNotFound) {
					return err
				}
			case "google":
				if err := auth.LogoutGoogle(cmd.Context()); err != nil { // token + BYO client secret
					return err
				}
				switch cfg, err := config.Load(); { // 沒登入就不該還顯示 email
				case err != nil: // keychain 的鍵已經刪了,不讓整個命令失敗,但要講
					fmt.Fprintln(cmd.ErrOrStderr(), i18n.T("auth.logout.google_email_not_cleared", "err", err))
				case cfg.GoogleEmail != "":
					cfg.GoogleEmail = ""
					if err := config.Save(cfg); err != nil {
						return err
					}
				}
			default:
				return i18n.Errorf("auth.err.unsupported_provider")
			}
			fmt.Fprintln(cmd.OutOrStdout(), i18n.T("auth.logout.done", "provider", args[0]))
			return nil
		},
	}
}
