package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
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
			fmt.Fprintln(w, i18n.T("doctor.line.fail", "name", c.name, "err", err)) // 整行一則:英文冒號後要空格
			continue
		}
		fmt.Fprintln(w, i18n.T("doctor.line.ok", "name", c.name, "detail", detail))
	}
	return failed
}

func checkConfig(ctx context.Context) (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", i18n.Errorf("doctor.config.err.read", "err", err)
	}
	if cfg.SpotifyClientID == "" {
		return "", i18n.Errorf("doctor.config.err.no_client_id")
	}
	if !clientIDRe.MatchString(cfg.SpotifyClientID) {
		return "", i18n.Errorf("doctor.config.err.bad_client_id", "id", strconv.Quote(cfg.SpotifyClientID))
	}
	return i18n.T("doctor.config.ok"), nil
}

func checkKeychain(ctx context.Context) (string, error) {
	const probe = "doctor.probe"
	if err := secret.Set(probe, "ok"); err != nil {
		return "", i18n.Errorf("doctor.keychain.err.write", "err", err)
	}
	v, err := secret.Get(probe)
	_ = secret.Delete(probe)
	if err != nil {
		return "", i18n.Errorf("doctor.keychain.err.read", "err", err)
	}
	if v != "ok" {
		return "", i18n.Errorf("doctor.keychain.err.mismatch", "value", strconv.Quote(v))
	}
	return i18n.T("doctor.keychain.ok"), nil
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
		return "", i18n.Errorf("doctor.port.err.listen", "err", err)
	}
	defer lb.Close()
	if lb.Port() != auth.DefaultSpotifyPort {
		return "", i18n.Errorf("doctor.port.err.in_use", "hint", portHint(runtime.GOOS))
	}
	return i18n.T("doctor.port.ok"), nil
}

// checkRefreshToken:「沒有」與「讀不到 / 內容毀損」是不同的病、不同的下一步,不能都印成「沒有」。
func checkRefreshToken(ctx context.Context) (string, error) {
	switch err := auth.SpotifyStored(); {
	case err == nil:
		return i18n.T("doctor.in_keychain"), nil
	case errors.Is(err, secret.ErrNotFound):
		return "", i18n.Errorf("doctor.refresh_token.err.missing")
	default:
		return "", i18n.Errorf("doctor.refresh_token.err.read", "err", err)
	}
}

func checkTokenRefresh(ctx context.Context) (string, error) {
	cfg, err := config.Load()
	if err != nil || cfg.SpotifyClientID == "" {
		return "", i18n.Errorf("doctor.token_refresh.err.needs_config")
	}
	ts, err := auth.SpotifyTokenSource(ctx, cfg.SpotifyClientID)
	switch {
	case err == nil:
	case errors.Is(err, secret.ErrNotFound):
		return "", i18n.Errorf("doctor.token_refresh.err.needs_refresh_token")
	default:
		// JSON 毀損、keychain 讀不到、等鎖被中斷、遷移失敗都會走到這。一律印「需要先通過 refresh token
		// 檢查」的話,會緊接在上一項的 ✅ 後面自相矛盾,而且真正的原因被吞掉。
		return "", i18n.Errorf("doctor.token_refresh.err.token_source", "err", err)
	}
	// 這一項就是要明確驗「refresh token 還活著」,所以強制換發;結果會寫回 keychain,
	// 後面的 ⑥ Spotify API 直接用這顆新 token,一次 doctor 只輪替一次(issue #3)。
	if _, err := ts.Refresh(); err != nil {
		return "", i18n.Errorf("doctor.token_refresh.err.refresh", "err", err)
	}
	return i18n.T("doctor.token_refresh.ok"), nil
}

func checkAPI(ctx context.Context) (string, error) {
	p, err := newProvider(ctx, "spotify")
	if err != nil {
		return "", err
	}
	if err := p.Health(ctx); err != nil {
		return "", i18n.Errorf("doctor.api.err.failed", "err", friendlyErr("spotify", err))
	}
	return i18n.T("doctor.api.ok.spotify"), nil
}

func checkAppleDevToken(ctx context.Context) (string, error) {
	_, exp, err := apple.DeveloperToken(time.Now())
	switch {
	case err == nil:
		return i18n.T("doctor.apple.dev_token.ok", "expiry", exp.Format(time.RFC3339)), nil
	case errors.Is(err, apple.ErrDevTokenExpired):
		return "", i18n.Errorf("doctor.apple.dev_token.err.expired", "expiry", exp.Format(time.RFC3339))
	default:
		if errors.Is(err, secret.ErrNotFound) {
			return "", i18n.Errorf("doctor.apple.dev_token.err.missing")
		}
		return "", i18n.Errorf("doctor.apple.err.keychain_read", "err", err)
	}
}

func checkAppleUserToken(ctx context.Context) (string, error) {
	_, err := secret.Get(apple.KeyMusicUserToken)
	switch {
	case err == nil:
		return i18n.T("doctor.in_keychain"), nil
	case errors.Is(err, secret.ErrNotFound):
		return "", i18n.Errorf("doctor.apple.user_token.err.missing")
	default:
		return "", i18n.Errorf("doctor.apple.err.keychain_read", "err", err)
	}
}

func checkAppleStorefront(ctx context.Context) (string, error) {
	cfg, err := config.Load()
	if err != nil || cfg.AppleStorefront == "" {
		return "", i18n.Errorf("doctor.apple.storefront.err.unset")
	}
	return cfg.AppleStorefront, nil
}

func checkAppleAPI(ctx context.Context) (string, error) {
	p, err := newProvider(ctx, "apple")
	if err != nil {
		return "", err
	}
	if err := p.Health(ctx); err != nil {
		return "", i18n.Errorf("doctor.api.err.failed", "err", friendlyErr("apple", err))
	}
	return i18n.T("doctor.api.ok.apple"), nil
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
		{i18n.T("doctor.check.config"), checkConfig},
		{"Keychain", checkKeychain},
		{"Port 8888", checkPort8888},
		{"Refresh token", checkRefreshToken},
		{i18n.T("doctor.check.token_refresh"), checkTokenRefresh},
		{"Spotify API", checkAPI},
	}
}

// localChecks(P6):沒有憑證、沒有網路——只看 local_root 與 library.json(計畫 §2 A8)。
func localChecks() []check {
	return []check{
		{i18n.T("doctor.check.local_root"), func(context.Context) (string, error) {
			cfg, err := config.Load()
			if err != nil {
				return "", err
			}
			if cfg.LocalRoot == "" {
				return "", i18n.Errorf("doctor.local_root.err.unset")
			}
			return cfg.LocalRoot, nil
		}},
		{i18n.T("doctor.check.local_library"), func(ctx context.Context) (string, error) {
			p, err := newLocalProvider()
			if err != nil {
				return "", err
			}
			if err := p.Health(ctx); err != nil {
				return "", err
			}
			r, err := asPlaylistReader(p) // doctor 是救命用的,不用會 panic 的型別斷言
			if err != nil {
				return "", err
			}
			refs, err := r.ListPlaylists(ctx)
			if err != nil {
				return "", err
			}
			return i18n.T("doctor.local_library.ok", "count", len(refs)), nil
		}},
	}
}

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "doctor", Short: i18n.T("cmd.doctor.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			provider, _ := cmd.Flags().GetString(flagProvider)
			var checks []check
			switch provider {
			case "spotify":
				checks = spotifyChecks()
			case "apple":
				checks = appleChecks()
			case "local":
				checks = localChecks()
			default:
				return i18n.Errorf("doctor.err.unknown_provider", "provider", strconv.Quote(provider), "valid", strings.Join(providerIDs, i18n.T("sep.list")))
			}
			if failed := runChecks(cmd.Context(), cmd.OutOrStdout(), checks); failed > 0 {
				return i18n.Errorf("doctor.err.failed", "count", failed)
			}
			fmt.Fprintln(cmd.OutOrStdout(), i18n.T("doctor.all_passed"))
			return nil
		},
	}
	providerFlag(cmd)
	return cmd
}
