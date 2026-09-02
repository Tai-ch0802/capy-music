package cli

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/provider/spotify"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// spotifyProviderT 固定型別別名,讓 simpleCtl 簽名可讀。
type spotifyProviderT = spotify.Provider

var (
	spotifyTrackURIRe = regexp.MustCompile(`^spotify:track:([0-9A-Za-z]{22})$`)
	spotifyTrackIDRe  = regexp.MustCompile(`^[0-9A-Za-z]{22}$`)
)

// resolveDeviceID:名稱不分大小寫精確比對,取第一個相符(重名取先;ponytail: 夠用)。
func resolveDeviceID(ctx context.Context, p interface {
	Devices(context.Context) ([]provider.Device, error)
}, name string) (string, error) {
	ds, err := p.Devices(ctx)
	if err != nil {
		return "", friendlyErr(err)
	}
	names := make([]string, len(ds))
	for i, d := range ds {
		names[i] = d.Name
		if strings.EqualFold(d.Name, name) {
			return d.ID, nil
		}
	}
	return "", fmt.Errorf("找不到裝置 %q;可用裝置:%s", name, strings.Join(names, "、"))
}

func newPlayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "play [query...|spotify:track:URI|track ID]",
		Short: "播放(無參數 = 恢復播放)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := newSpotifyProvider(ctx)
			if err != nil {
				return err
			}
			req := provider.PlayRequest{}
			var label string
			switch {
			case len(args) == 0: // resume
				label = "恢復播放"
			case len(args) == 1 && spotifyTrackURIRe.MatchString(args[0]):
				req.TrackIDs = []string{spotifyTrackURIRe.FindStringSubmatch(args[0])[1]}
				label = args[0]
			case len(args) == 1 && spotifyTrackIDRe.MatchString(args[0]):
				req.TrackIDs = []string{args[0]}
				label = args[0]
			default:
				q := strings.Join(args, " ")
				tracks, err := p.Search(ctx, provider.Query{Text: q, Limit: 1})
				if err != nil {
					return friendlyErr(err)
				}
				if len(tracks) == 0 {
					return fmt.Errorf("找不到:%s", q)
				}
				req.TrackIDs = []string{tracks[0].ProviderID}
				label = fmt.Sprintf("%s — %s", tracks[0].Title, strings.Join(tracks[0].Artists, ", "))
			}
			if name, _ := cmd.Flags().GetString("device"); name != "" {
				if req.DeviceID, err = resolveDeviceID(ctx, p, name); err != nil {
					return err
				}
			}
			if err := p.Play(ctx, req); err != nil {
				return friendlyErr(err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "▶ ", ui.Bold(stdoutIsTTY(cmd), label))
			return nil
		},
	}
	cmd.Flags().String("device", "", "目標裝置名稱(見 capy devices)")
	return cmd
}

// simpleCtl:pause/next/prev 共用形狀。
func simpleCtl(use, short, done string, call func(ctx context.Context, p *spotifyProviderT) error) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newSpotifyProvider(cmd.Context())
			if err != nil {
				return err
			}
			if err := call(cmd.Context(), p); err != nil {
				return friendlyErr(err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), done)
			return nil
		},
	}
}

func newNowCmd() *cobra.Command {
	return &cobra.Command{
		Use: "now", Short: "目前播放狀態", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newSpotifyProvider(cmd.Context())
			if err != nil {
				return err
			}
			st, err := p.State(cmd.Context())
			if err != nil {
				return friendlyErr(err)
			}
			w := cmd.OutOrStdout()
			if st == nil || st.Track == nil {
				fmt.Fprintln(w, "目前沒有播放內容")
				return nil
			}
			tty := stdoutIsTTY(cmd)
			mark := "⏸"
			if st.Playing {
				mark = "▶"
			}
			fmt.Fprintf(w, "%s %s — %s\n", mark, ui.Bold(tty, st.Track.Title), strings.Join(st.Track.Artists, ", "))
			fmt.Fprintf(w, "  %s / %s · %s(%s)\n",
				ui.FormatDuration(st.ProgressMS), ui.FormatDuration(st.Track.DurationMS),
				st.Device.Name, st.Device.Type)
			return nil
		},
	}
}

func newDevicesCmd() *cobra.Command {
	return &cobra.Command{
		Use: "devices", Short: "列出 Spotify Connect 裝置", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newSpotifyProvider(cmd.Context())
			if err != nil {
				return err
			}
			ds, err := p.Devices(cmd.Context())
			if err != nil {
				return friendlyErr(err)
			}
			rows := make([][]string, len(ds))
			for i, d := range ds {
				active := "-"
				if d.Active {
					active = "active"
				}
				rows[i] = []string{d.Name, d.Type, active, strconv.Itoa(d.VolumePct), d.ID}
			}
			ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), []string{"名稱", "類型", "狀態", "音量", "ID"}, rows)
			return nil
		},
	}
}
