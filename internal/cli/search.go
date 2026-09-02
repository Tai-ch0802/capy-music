package cli

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/provider/spotify"
	"github.com/Tai-ch0802/capy-music/internal/secret"
	"github.com/Tai-ch0802/capy-music/internal/ui"
	"golang.org/x/oauth2"
)

// newSpotifyProvider 是所有讀 API 命令的入口;測試以假 API 替換。
var newSpotifyProvider = func(ctx context.Context) (*spotify.Provider, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.SpotifyClientID == "" {
		return nil, errors.New("尚未設定 Spotify — 先執行 capy auth login spotify")
	}
	ts, err := auth.SpotifyTokenSource(ctx, cfg.SpotifyClientID)
	if errors.Is(err, secret.ErrNotFound) {
		return nil, errors.New("尚未登入 Spotify — 先執行 capy auth login spotify")
	}
	if err != nil {
		return nil, err
	}
	return spotify.New(oauth2.NewClient(ctx, ts), ""), nil
}

// friendlyErr 把語意化錯誤轉成可行動訊息(spec R-5:錯誤訊息直接指出下一步)。
func friendlyErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, provider.ErrAuthExpired):
		return errors.New("授權已過期 — 重新執行 capy auth login spotify")
	case errors.Is(err, provider.ErrNoActiveDevice):
		return errors.New("沒有作用中的 Spotify 裝置 — 開一個 Spotify 播放器,或用 capy devices 查看後以 --device 指定")
	default:
		return err
	}
}

func stdoutIsTTY(cmd *cobra.Command) bool {
	if f, ok := cmd.OutOrStdout().(*os.File); ok {
		return ui.IsTTY(f)
	}
	return false // 測試 buffer / pipe 一律純文字
}

func trackRows(tracks []provider.Track, tty bool) [][]string {
	rows := make([][]string, len(tracks))
	for i, tr := range tracks {
		dur := strconv.Itoa(tr.DurationMS)
		if tty {
			dur = ui.FormatDuration(tr.DurationMS)
		}
		rows[i] = []string{tr.ProviderID, tr.Title, strings.Join(tr.Artists, ", "), tr.Album, dur}
	}
	return rows
}

func newSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search <query...>",
		Short: "搜尋曲目",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			limit, _ := cmd.Flags().GetInt("limit")
			p, err := newSpotifyProvider(cmd.Context())
			if err != nil {
				return err
			}
			tracks, err := p.Search(cmd.Context(), provider.Query{Text: strings.Join(args, " "), Limit: limit})
			if err != nil {
				return friendlyErr(err)
			}
			tty := stdoutIsTTY(cmd)
			// TSV 欄序(文件化):id, title, artists, album, duration_ms
			ui.Table(cmd.OutOrStdout(), tty, []string{"ID", "曲名", "藝人", "專輯", "時長"}, trackRows(tracks, tty))
			return nil
		},
	}
	cmd.Flags().Int("limit", 10, "結果數(單次 API 上限 10,超過自動分頁)")
	return cmd
}
