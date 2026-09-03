package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// doctor:R-5 的一等公民 — 每個失敗都要指出下一步(spec §8.5.7)。
type check struct {
	name string
	fn   func(ctx context.Context) (detail string, err error)
}

// checkOSA 的實作由平台決定(darwin_*.go / other_*.go)。
var checkOSA func(ctx context.Context) (string, error)

func runChecks(ctx context.Context, w io.Writer, checks []check) (failed int) {
	for _, c := range checks {
		detail, err := c.fn(ctx)
		if err != nil {
			failed++
			fmt.Fprintf(w, "❌ %s:%v\n", c.name, err)
			continue
		}
		fmt.Fprintf(w, "✅ %s:%s\n", c.name, detail)
	}
	return failed
}

func checkConfig(ctx context.Context) (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("讀取失敗:%w", err)
	}
	if cfg.SpotifyClientID == "" {
		return "", errors.New("未設定 Spotify Client ID — 執行 capy auth login spotify")
	}
	if !clientIDRe.MatchString(cfg.SpotifyClientID) {
		return "", fmt.Errorf("Client ID 格式錯誤(應為 32 位十六進位):%q", cfg.SpotifyClientID)
	}
	return "client ID 格式正確", nil
}

func checkKeychain(ctx context.Context) (string, error) {
	const probe = "doctor.probe"
	if err := secret.Set(probe, "ok"); err != nil {
		return "", fmt.Errorf("寫入失敗(keychain 不可用?):%w", err)
	}
	v, err := secret.Get(probe)
	_ = secret.Delete(probe)
	if err != nil {
		return "", fmt.Errorf("讀回失敗:%w", err)
	}
	if v != "ok" {
		return "", fmt.Errorf("讀回值不符(得到 %q)— keychain 可能被其他程式覆寫", v)
	}
	return "讀寫正常", nil
}

// portHint:查誰佔用 8888 的指令,windows 與其他平台(macOS/Linux)不同。
func portHint(goos string) string {
	if goos == "windows" {
		return "netstat -ano | findstr :8888"
	}
	return "lsof -i :8888"
}

func checkPort8888(ctx context.Context) (string, error) {
	lb, err := auth.NewLoopback(auth.DefaultSpotifyPort, "probe")
	if err != nil {
		return "", fmt.Errorf("無法建立 listener:%w", err)
	}
	defer lb.Close()
	if lb.Port() != auth.DefaultSpotifyPort {
		return "", fmt.Errorf("8888 被其他程序佔用 — auth login 會失敗,請先釋放(%s)", portHint(runtime.GOOS))
	}
	return "8888 可用", nil
}

func checkRefreshToken(ctx context.Context) (string, error) {
	if _, err := secret.Get(auth.KeySpotifyRefreshToken); err != nil {
		return "", errors.New("keychain 沒有 refresh token — 執行 capy auth login spotify")
	}
	return "keychain 存在", nil
}

func checkTokenRefresh(ctx context.Context) (string, error) {
	cfg, err := config.Load()
	if err != nil || cfg.SpotifyClientID == "" {
		return "", errors.New("需要先通過設定檔檢查")
	}
	ts, err := auth.SpotifyTokenSource(ctx, cfg.SpotifyClientID)
	if err != nil {
		return "", errors.New("需要先通過 refresh token 檢查")
	}
	if _, err := ts.Token(); err != nil {
		return "", fmt.Errorf("換發失敗(token 可能已被撤銷,重跑 capy auth login spotify):%w", err)
	}
	return "access token 換發成功(輪替已依硬約束覆寫)", nil
}

func checkAPI(ctx context.Context) (string, error) {
	p, err := newProvider(ctx, "spotify")
	if err != nil {
		return "", err
	}
	if err := p.Health(ctx); err != nil {
		return "", fmt.Errorf("API 呼叫失敗:%w", friendlyErr("spotify", err))
	}
	return "API 可達、授權有效", nil
}

func checkAppleDevToken(ctx context.Context) (string, error) {
	_, exp, err := apple.DeveloperToken(time.Now())
	switch {
	case err == nil:
		return fmt.Sprintf("有效至 %s", exp.Format(time.RFC3339)), nil
	case errors.Is(err, apple.ErrDevTokenExpired):
		return "", fmt.Errorf("已於 %s 過期 — 重新執行 capy auth login apple", exp.Format(time.RFC3339))
	default:
		return "", errors.New("keychain 沒有 developer token — 執行 capy auth login apple")
	}
}

func checkAppleUserToken(ctx context.Context) (string, error) {
	if _, err := secret.Get(apple.KeyMusicUserToken); err != nil {
		return "", errors.New("keychain 沒有 Music User Token — 執行 capy auth login apple")
	}
	return "keychain 存在", nil
}

func checkAppleStorefront(ctx context.Context) (string, error) {
	cfg, err := config.Load()
	if err != nil || cfg.AppleStorefront == "" {
		return "", errors.New("未設定 — 重新執行 capy auth login apple")
	}
	return cfg.AppleStorefront, nil
}

func checkAppleAPI(ctx context.Context) (string, error) {
	p, err := newProvider(ctx, "apple")
	if err != nil {
		return "", err
	}
	if err := p.Health(ctx); err != nil {
		return "", fmt.Errorf("API 呼叫失敗:%w", friendlyErr("apple", err))
	}
	return "API 可達、token 有效", nil
}

func appleChecks() []check {
	cs := []check{
		{"Apple developer token", checkAppleDevToken},
		{"Apple user token", checkAppleUserToken},
		{"Apple storefront", checkAppleStorefront},
		{"Apple Music API", checkAppleAPI},
	}
	if runtime.GOOS == "darwin" {
		cs = append(cs, check{"osascript / Music.app", checkOSA})
	}
	return cs
}

func spotifyChecks() []check {
	return []check{
		{"設定檔", checkConfig},
		{"Keychain", checkKeychain},
		{"Port 8888", checkPort8888},
		{"Refresh token", checkRefreshToken},
		{"Token 換發", checkTokenRefresh},
		{"Spotify API", checkAPI},
	}
}

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "doctor", Short: "診斷設定與連線(BYO 問題一站排除)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			provider, _ := cmd.Flags().GetString(flagProvider)
			var checks []check
			switch provider {
			case "spotify":
				checks = spotifyChecks()
			case "apple":
				checks = appleChecks()
			default:
				return fmt.Errorf("未知的 provider %q(可用:spotify、apple)", provider)
			}
			if failed := runChecks(cmd.Context(), cmd.OutOrStdout(), checks); failed > 0 {
				return fmt.Errorf("%d 項檢查未通過", failed)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "全部通過 🎉")
			return nil
		},
	}
	providerFlag(cmd)
	return cmd
}
