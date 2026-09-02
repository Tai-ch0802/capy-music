package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth/apple"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	appleprov "github.com/Tai-ch0802/capy-music/internal/provider/apple"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

const flagProvider = "provider"

// newProvider 依 id 建構 provider;是所有讀 API 命令的共用入口,測試以假 API 替換。
// 不做 registry:兩個 provider 用 switch 就夠(P6 接 local provider 時再評估)。
var newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
	switch id {
	case "spotify":
		return newSpotifyProvider(ctx)
	case "apple":
		return newAppleProvider(ctx)
	default:
		return nil, fmt.Errorf("未知的 provider %q(可用:spotify、apple)", id)
	}
}

func providerFlag(cmd *cobra.Command) {
	cmd.Flags().String(flagProvider, "spotify", "平台(spotify|apple)")
}

func getProvider(cmd *cobra.Command) (provider.Provider, error) {
	id, _ := cmd.Flags().GetString(flagProvider)
	return newProvider(cmd.Context(), id)
}

func notSupported(p provider.Provider, what string) error {
	return fmt.Errorf("%s 不支援%s:%w", p.DisplayName(), what, provider.ErrNotSupported)
}

// as*:型別斷言 + Caps() 雙重檢查(Caps 可依平台變動,例如 Apple 播放只在 macOS)。
func asSearcher(p provider.Provider) (provider.Searcher, error) {
	s, ok := p.(provider.Searcher)
	if !ok || !p.Caps().Has(provider.CapSearch) {
		return nil, notSupported(p, "搜尋")
	}
	return s, nil
}

func asPlayback(p provider.Provider) (provider.PlaybackController, error) {
	c, ok := p.(provider.PlaybackController)
	if !ok || !p.Caps().Has(provider.CapPlaybackControl) {
		return nil, notSupported(p, "播放遙控(Apple Music 的播放只在 macOS 可用;其他平台請用 --provider spotify)")
	}
	return c, nil
}

func asPlaylistReader(p provider.Provider) (provider.PlaylistReader, error) {
	r, ok := p.(provider.PlaylistReader)
	if !ok || !p.Caps().Has(provider.CapPlaylistRead) {
		return nil, notSupported(p, "讀取播放清單")
	}
	return r, nil
}

// friendlyErr 把語意化錯誤轉成可行動訊息(spec R-5),指向對應 provider 的下一步。
func friendlyErr(providerID string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, provider.ErrAuthExpired):
		return fmt.Errorf("授權已過期 — 重新執行 capy auth login %s", providerID)
	case errors.Is(err, provider.ErrNoActiveDevice):
		return fmt.Errorf("沒有作用中的播放裝置 — 開一個播放器,或用 capy devices --provider %s 查看後以 --device 指定", providerID)
	default:
		return err
	}
}

// 測試替換點。
var appleAuthorize = apple.AuthorizeMUT

// appleAPIBase:測試以 CAPY_APPLE_API_BASE 指向 httptest;正式為空(用預設)。
func appleAPIBase() string { return os.Getenv("CAPY_APPLE_API_BASE") }

func devTokenOptsFromEnv(cfg *config.Config) apple.DevTokenOptions {
	return apple.DevTokenOptions{
		P8Path:    os.Getenv("CAPY_APPLE_P8_PATH"),
		KID:       os.Getenv("CAPY_APPLE_KID"),
		TeamID:    os.Getenv("CAPY_APPLE_TEAM_ID"),
		Endpoint:  cfg.AppleTokenEndpoint,
		InstallID: cfg.InstallID,
	}
}

// newAppleProvider:config → dev token(來源鏈)→ MUT(缺 → 提示 login)→ storefront(缺 → 提示 login)。
func newAppleProvider(ctx context.Context) (*appleprov.Provider, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if config.EnsureInstallID(cfg) {
		if err := config.Save(cfg); err != nil {
			return nil, err
		}
	}
	dev, _, err := apple.DeveloperToken(ctx, devTokenOptsFromEnv(cfg))
	if err != nil {
		return nil, err
	}
	mut, err := secret.Get(apple.KeyMusicUserToken)
	if errors.Is(err, secret.ErrNotFound) {
		return nil, errors.New("尚未登入 Apple Music — 先執行 capy auth login apple")
	}
	if err != nil {
		return nil, err
	}
	if cfg.AppleStorefront == "" {
		return nil, errors.New("缺 Apple storefront — 重新執行 capy auth login apple")
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	return appleprov.New(hc, appleAPIBase(), dev, mut, cfg.AppleStorefront), nil
}
