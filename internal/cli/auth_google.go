package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/config"
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
	googleFromWizard  googleClientSource = "精靈"
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
			return auth.GoogleClient{}, "", errors.New("給了 client secret 但沒有 client ID(--client-id / CAPY_GOOGLE_CLIENT_ID,或先登入過一次讓它進 config)")
		}
		return auth.GoogleClient{ID: cfg.GoogleClientID, Secret: strings.TrimSpace(sec)}, googleFromFlags, nil
	}
	if cfg.GoogleClientID != "" {
		switch s, err := secret.Get(auth.KeyGoogleClientSecret); {
		case err == nil:
			sec = s
		case !errors.Is(err, secret.ErrNotFound):
			return auth.GoogleClient{}, "", fmt.Errorf("讀取 keychain 的 google.client_secret:%w", err)
		}
		return auth.GoogleClient{ID: cfg.GoogleClientID, Secret: sec}, googleFromConfig, nil
	}
	if auth.BuiltinGoogleClientID != "" {
		return auth.GoogleClient{ID: auth.BuiltinGoogleClientID, Secret: auth.BuiltinGoogleClientSecret, Builtin: true}, googleFromBuiltin, nil
	}
	return auth.GoogleClient{}, "", nil
}

const googleGuide = `建立你自己的 Google OAuth client(免費,約 5 分鐘)——這個 binary 沒有內建 client(go install 建的都沒有):
  1. https://console.cloud.google.com → 建立專案 → API 和服務 → 啟用「Google Drive API」
  2. Google Auth platform → Branding:填 app 名稱與 support email;Audience 選 External
  3. Data Access:只加這三個 scope —— openid、userinfo.email、drive.appdata(多加 Gmail 之類會觸發資安評估)
  4. 建立 OAuth client:類型選「桌面應用程式(Desktop app)」;secret 只在建立當下顯示一次,立刻複製
  5. ⚠️ Audience 按「Publish app」切到 In production —— 停在 Testing 的話 refresh token 7 天就過期,你會莫名被登出
  6. 把 Client ID 與 Client secret 貼到下面`

func runGoogleClientWizard() (id, sec string, err error) {
	form := huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Google Drive 同步:先建自己的 OAuth client").Description(googleGuide),
		huh.NewInput().Title("Client ID(結尾通常是 .apps.googleusercontent.com)").Value(&id).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("必填")
			}
			return nil
		}),
		huh.NewInput().Title("Client secret(可留空試試看;G-0 驗收會確定 Desktop client 要不要)").EchoMode(huh.EchoModePassword).Value(&sec),
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
	form := huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("找不到這個 client 的 secret").Description("client id "+maskGoogleClientID(clientID)+" 還在 config,但 secret 不在 keychain(capy auth logout google 會刪掉它)。\n貼上 secret;直接 Enter 留空則試試看不帶 secret(Desktop client 是否必須帶 secret 由 G-0 驗收決定)。"),
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
			return errors.New("這個 binary 沒有內建 Google client(go install 建的都沒有),非互動環境請給 --client-id / --client-secret 或 CAPY_GOOGLE_CLIENT_ID / CAPY_GOOGLE_CLIENT_SECRET。\n" + googleGuide)
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
	fmt.Fprintln(cmd.ErrOrStderr(), "在瀏覽器完成 Google 授權…(180s 內;請勾選全部三個權限)")
	ctx, cancel := context.WithTimeout(cmd.Context(), 180*time.Second)
	defer cancel()
	_, email, err := googleLoginFn(ctx, client, openBrowser)
	if err != nil {
		return err
	}
	// 授權成功後才落地 client 與帳號資訊(失敗不留半殘 config)。BYO 的 secret 只進 keychain;內建的不落地。
	// 這三個寫入沒有交易性:token 已在 keychain,之後任何一步失敗都要講明「授權其實已成功」,否則使用者會以為要重登。
	const authOK = "Google 授權已成功、token 已入 keychain,但"
	if !client.Builtin {
		cfg.GoogleClientID = client.ID
		if client.Secret != "" {
			if err := secret.Set(auth.KeyGoogleClientSecret, client.Secret); err != nil {
				return fmt.Errorf("%s寫入 keychain 的 google.client_secret 失敗(下次 login 用 --client-secret 補):%w", authOK, err)
			}
		}
	}
	cfg.GoogleEmail = email
	if cfg.DeviceID == "" { // 在這裡出生:第一次要跟 Drive 說話的時候
		cfg.DeviceID = ulid.New()
	}
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("%s寫 config 失敗(client id / email / device_id 沒落地,修好後重跑 login 即可):%w", authOK, err)
	}
	who := email
	if who == "" {
		who = "(id_token 沒有 email)"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "✅ Google 授權完成:%s(client 來源:%s;token 已入 keychain)\n", who, source)
	return nil
}

// maskGoogleClientID:顯示 .apps.googleusercontent.com 前的頭 6 碼。
func maskGoogleClientID(id string) string {
	base := strings.TrimSuffix(id, ".apps.googleusercontent.com")
	if len(base) <= 6 {
		return "已設定"
	}
	return "已設定(" + base[:6] + "…)"
}
