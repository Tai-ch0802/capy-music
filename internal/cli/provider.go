package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
		return fmt.Errorf("授權已過期或被拒(%v)— 重新執行 capy auth login %s", err, providerID)
	case errors.Is(err, provider.ErrNoActiveDevice):
		return fmt.Errorf("沒有作用中的播放裝置 — 開一個播放器,或用 capy devices --provider %s 查看後以 --device 指定", providerID)
	case errors.Is(err, provider.ErrNotFound):
		return err // 訊息已可行動(如「清單為空或不存在」),不需再包一層
	default:
		return err
	}
}

// spotifyAPIBase:測試用種子;正式為空(用預設 base)。刻意不吃環境變數:oauth2.Transport 不檢查
// host 也不檢查 scheme,把 base 指到哪都照樣附上 Bearer,而 Spotify 的 base 沒有 Apple C-0 那種
// 「正確位址還沒定案」的理由需要逃生口(doctor_test 對 auth.SpotifyEndpoint 也是用套件變數種)。
var spotifyAPIBase string

// appleAPIBaseSeed:測試用種子(httptest 是 http://,不經過下面的 https 檢查);正式為空。
var appleAPIBaseSeed string

// appleAPIBase:Apple API base 的覆寫,是驗收步驟 C-0 的逃生口(正確的 host 與端點形狀還沒用真 token
// 驗過,所以不做白名單——那會把 C-0 本身擋掉)。但一律要求 https:這條路送出的標頭裡有 developer token
// 與 Media-User-Token(長期有效的帳號憑證),明文連線等於外洩。
func appleAPIBase() (string, error) {
	if appleAPIBaseSeed != "" {
		return appleAPIBaseSeed, nil
	}
	raw := os.Getenv("CAPY_APPLE_API_BASE")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("CAPY_APPLE_API_BASE 不是合法的 URL(%v)— 請改成 https://<主機>/v1 的形式", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("CAPY_APPLE_API_BASE 必須是 https(目前 %q)— developer token 與 Media-User-Token 會放在標頭送出,明文連線等於把它們交出去;請改成 https://<主機>/v1", raw)
	}
	return raw, nil
}

// newAppleProvider:keychain 讀 dev token(缺/過期 → 提示 login)→ MUT(缺 → 提示 login)→
// storefront(缺 → 提示 login)。
func newAppleProvider(ctx context.Context) (*appleprov.Provider, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	dev, exp, err := apple.DeveloperToken(time.Now())
	switch {
	case errors.Is(err, secret.ErrNotFound):
		return nil, errors.New("尚未登入 Apple Music — 先執行 capy auth login apple")
	case errors.Is(err, apple.ErrDevTokenExpired):
		return nil, fmt.Errorf("Apple developer token 已於 %s 過期(Apple 定期輪替)— 重新執行 capy auth login apple", exp.Format(time.RFC3339))
	case err != nil:
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
	base, err := appleAPIBase()
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	return appleprov.New(hc, base, dev, mut, cfg.AppleStorefront), nil
}
