package cli

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/provider/spotify"
	"github.com/Tai-ch0802/capy-music/internal/secret"
	"github.com/Tai-ch0802/capy-music/internal/ui"
	"golang.org/x/oauth2"
)

// newSpotifyProvider 是 spotify provider 的建構入口,由 newProvider 依 id 呼叫。
func newSpotifyProvider(ctx context.Context) (*spotify.Provider, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.SpotifyClientID == "" {
		return nil, i18n.Errorf("platform.err.spotify_not_configured")
	}
	ts, err := auth.SpotifyTokenSource(ctx, cfg.SpotifyClientID)
	if errors.Is(err, secret.ErrNotFound) {
		return nil, i18n.Errorf("platform.err.spotify_not_logged_in")
	}
	if err != nil {
		return nil, err
	}
	hc := oauth2.NewClient(ctx, ts)
	hc.Timeout = 30 * time.Second
	return spotify.New(hc, spotifyAPIBase), nil
}

func stdoutIsTTY(cmd *cobra.Command) bool {
	if f, ok := cmd.OutOrStdout().(*os.File); ok {
		return ui.IsTTY(f)
	}
	return false // 測試 buffer / pipe 一律純文字
}

// trackHeader:search 與 pl show 的表頭。欄名是機器欄位,不跟語系(決策 50;網頁照欄名找欄位)。
var trackHeader = []string{"ID", "TITLE", "ARTISTS", "ALBUM", "DURATION"}

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
		Short: i18n.T("cmd.search.short"),
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			limit, _ := cmd.Flags().GetInt("limit")
			p, err := getProvider(cmd)
			if err != nil {
				return err
			}
			s, err := asSearcher(p)
			if err != nil {
				return err
			}
			tracks, err := s.Search(cmd.Context(), provider.Query{Text: strings.Join(args, " "), Limit: limit})
			if err != nil {
				return friendlyErr(p.ID(), err)
			}
			q := strings.Join(args, " ")
			cc := cache.Load() // 補全用的最近搜尋(只是快取,寫失敗靜默)
			cc.AddRecent(cache.Recent{Provider: p.ID(), Type: cache.TypeQuery, ID: q, Label: q})
			_ = cc.Save()
			tty := stdoutIsTTY(cmd)
			// TSV 欄序(文件化):id, title, artists, album, duration_ms
			return ui.Table(cmd.OutOrStdout(), tty, trackHeader, trackRows(tracks, tty))
		},
	}
	cmd.Flags().Int("limit", 10, i18n.T("cmd.search.flag.limit"))
	providerFlag(cmd)
	return cmd
}
