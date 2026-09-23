package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/secret"
	"github.com/Tai-ch0802/capy-music/internal/ulid"
)

// 測試替換點。
var (
	googleLoginFn      func(context.Context, auth.GoogleClient, func(string) error) (*oauth2.Token, string, error) = auth.LoginGoogle
	googleWizard                                                                                                   = runGoogleClientWizard
	googleSecretPrompt                                                                                             = runGoogleSecretPrompt
)

// googleClientSource:這次登入的 client 從哪來(auth status 也要能講)。
type googleClientSource string

const (
	googleFromFlags   googleClientSource = "flag/env"
	googleFromWizard  googleClientSource = "wizard" // 印出時翻(googleSourceLabel);其餘三個是機器字,原樣印
	googleFromConfig  googleClientSource = "config"
	googleFromBuiltin googleClientSource = "builtin"
)

// resolveGoogleClient:flag / 環境變數 → config(+ keychain 的 secret)→ 內建。都沒有回空 ID,由呼叫端決定精靈或報錯。
func resolveGoogleClient(cmd *cobra.Command, cfg *config.Config) (auth.GoogleClient, googleClientSource, error) {
	id, _ := cmd.Flags().GetString("client-id")
	sec, _ := cmd.Flags().GetString("client-secret")
	if id == "" {
		id = os.Getenv("CAPY_GOOGLE_CLIENT_ID")
	}
	if sec == "" {
		sec = os.Getenv("CAPY_GOOGLE_CLIENT_SECRET")
	}
	if id != "" {
		return auth.GoogleClient{ID: strings.TrimSpace(id), Secret: strings.TrimSpace(sec)}, googleFromFlags, nil
	}
	if sec != "" { // 只給 secret:配 config 裡的 client id;沒有就報錯,不能靜默丟掉使用者給的東西
		if cfg.GoogleClientID == "" {
			return auth.GoogleClient{}, "", i18n.Errorf("google.err.secret_without_id")
		}
		return auth.GoogleClient{ID: cfg.GoogleClientID, Secret: strings.TrimSpace(sec)}, googleFromFlags, nil
	}
	return googleClientFromConfig(cfg)
}

// googleGuide:BYO 精靈的說明原文;huh 表單、web 提示橋與非 TTY 的錯誤共用。用的時候才翻(語系在建樹前才設)。
func googleGuide() string { return i18n.T("google.guide") }

func runGoogleClientWizard() (id, sec string, err error) {
	form := newForm(huh.NewGroup(
		huh.NewNote().Title(i18n.T("google.wizard.title")).Description(googleGuide()),
		huh.NewInput().Title(i18n.T("google.wizard.client_id")).Value(&id).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return i18n.Errorf("google.wizard.required")
			}
			return nil
		}),
		huh.NewInput().Title(i18n.T("google.wizard.client_secret")).EchoMode(huh.EchoModePassword).Value(&sec),
	))
	if err := form.Run(); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(id), strings.TrimSpace(sec), nil
}

// runGoogleSecretPrompt:config 有 client id、但 secret 不在 keychain 也沒從 flag/env 來(logout 會刪掉 secret)
// 的互動路徑——只問 secret,不重跑整個精靈;Enter 留空 = 試試看不帶 secret(Q1 未定)。
func runGoogleSecretPrompt(clientID string) (string, error) {
	var sec string
	form := newForm(huh.NewGroup(
		huh.NewNote().Title(i18n.T("google.secret_prompt.title")).Description(i18n.T("google.secret_prompt.body", "client_id", maskGoogleClientID(clientID))),
		huh.NewInput().Title("Client secret").EchoMode(huh.EchoModePassword).Value(&sec),
	))
	if err := form.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(sec), nil
}

func googleLogin(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	client, source, err := resolveGoogleClient(cmd, cfg)
	if err != nil {
		return err
	}
	if client.ID == "" {
		if !stdinIsTTY() {
			return i18n.Errorf("google.err.no_client", "guide", googleGuide())
		}
		id, sec, err := googleWizard()
		if err != nil {
			return err
		}
		client, source = auth.GoogleClient{ID: id, Secret: sec}, googleFromWizard
	}
	if source == googleFromConfig && client.Secret == "" && stdinIsTTY() { // logout 之後:互動使用者要有路重貼 secret
		sec, err := googleSecretPrompt(client.ID)
		if err != nil {
			return err
		}
		client.Secret = sec
	}
	fmt.Fprintln(cmd.ErrOrStderr(), i18n.T("google.login.waiting"))
	ctx, cancel := context.WithTimeout(cmd.Context(), 180*time.Second)
	defer cancel()
	_, email, err := googleLoginFn(ctx, client, openBrowser)
	if err != nil {
		return err
	}
	// 授權成功後才落地 client 與帳號資訊(失敗不留半殘 config)。BYO 的 secret 只進 keychain;內建的不落地。
	// 這三個寫入沒有交易性:token 已在 keychain,之後任何一步失敗都要講明「授權其實已成功」,否則使用者會以為要重登。
	if !client.Builtin {
		cfg.GoogleClientID = client.ID
		if client.Secret != "" {
			if err := secret.Set(auth.KeyGoogleClientSecret, client.Secret); err != nil {
				return i18n.Errorf("google.err.save_secret", "err", err)
			}
		}
	}
	cfg.GoogleEmail = email
	if cfg.DeviceID == "" { // 在這裡出生:第一次要跟 Drive 說話的時候
		cfg.DeviceID = ulid.New()
	}
	if err := config.Save(cfg); err != nil {
		return i18n.Errorf("google.err.save_config", "err", err)
	}
	who := email
	if who == "" {
		who = i18n.T("google.login.no_email")
	}
	fmt.Fprintln(cmd.OutOrStdout(), i18n.T("google.login.done", "who", who, "source", googleSourceLabel(source)))
	return nil
}

func googleSourceLabel(s googleClientSource) string {
	if s == googleFromWizard {
		return i18n.T("google.source.wizard")
	}
	return string(s)
}

// maskGoogleClientID:顯示 .apps.googleusercontent.com 前的頭 6 碼。
func maskGoogleClientID(id string) string {
	base := strings.TrimSuffix(id, ".apps.googleusercontent.com")
	if len(base) <= 6 {
		return i18n.T("google.client_id.set")
	}
	return i18n.T("google.client_id.set_prefix", "prefix", base[:6])
}
