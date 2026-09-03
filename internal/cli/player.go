package cli

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

var (
	spotifyTrackURIRe = regexp.MustCompile(`^spotify:track:([0-9A-Za-z]{22})$`)
	// spotifyBase62IDRe:22 碼 base62,track ID 與 playlist ID 共用此格式。
	spotifyBase62IDRe = regexp.MustCompile(`^[0-9A-Za-z]{22}$`)
)

// resolveDeviceID:名稱不分大小寫精確比對,取第一個相符(重名取先;ponytail: 夠用)。
func resolveDeviceID(ctx context.Context, pc provider.PlaybackController, providerID, name string) (string, error) {
	ds, err := pc.Devices(ctx)
	if err != nil {
		return "", friendlyErr(providerID, err)
	}
	if len(ds) == 0 {
		return "", friendlyErr(providerID, provider.ErrNoActiveDevice)
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
			p, err := getProvider(cmd)
			if err != nil {
				return err
			}
			pc, err := asPlayback(p)
			if err != nil {
				return err
			}
			id, _ := cmd.Flags().GetString("id")
			req := provider.PlayRequest{}
			var label string
			switch {
			case id != "" && len(args) > 0:
				return fmt.Errorf("--id 與搜尋詞擇一")
			case id != "":
				req.TrackIDs = []string{id}
				label = id
			case len(args) == 0: // resume
				label = "恢復播放"
			case len(args) == 1 && spotifyTrackURIRe.MatchString(args[0]):
				req.TrackIDs = []string{spotifyTrackURIRe.FindStringSubmatch(args[0])[1]}
				label = args[0]
			case len(args) == 1 && spotifyBase62IDRe.MatchString(args[0]):
				req.TrackIDs = []string{args[0]}
				label = args[0]
			case len(args) == 1 && strings.HasPrefix(args[0], "spotify:"):
				return fmt.Errorf("目前只支援 track URI/ID,不支援 %s — 用 capy pl show 取出曲目 ID 再播放", args[0])
			default:
				s, err := asSearcher(p)
				if err != nil {
					return err
				}
				q := strings.Join(args, " ")
				tracks, err := s.Search(ctx, provider.Query{Text: q, Limit: 1})
				if err != nil {
					return friendlyErr(p.ID(), err)
				}
				if len(tracks) == 0 {
					return fmt.Errorf("找不到:%s", q)
				}
				req.TrackIDs = []string{tracks[0].ProviderID}
				label = fmt.Sprintf("%s — %s", tracks[0].Title, strings.Join(tracks[0].Artists, ", "))
			}
			if name, _ := cmd.Flags().GetString("device"); name != "" {
				if req.DeviceID, err = resolveDeviceID(ctx, pc, p.ID(), name); err != nil {
					return err
				}
			}
			if err := pc.Play(ctx, req); err != nil {
				return friendlyErr(p.ID(), err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "▶", ui.Bold(stdoutIsTTY(cmd), label))
			return nil
		},
	}
	cmd.Flags().String("device", "", "目標裝置名稱(見 capy devices)")
	cmd.Flags().String("id", "", "直接以 provider 的 track ID 播放(跳過搜尋)")
	providerFlag(cmd)
	return cmd
}

// simpleCtl:pause/next/prev 共用形狀。
func simpleCtl(use, short, done string, call func(ctx context.Context, pc provider.PlaybackController) error) *cobra.Command {
	cmd := &cobra.Command{
		Use: use, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := getProvider(cmd)
			if err != nil {
				return err
			}
			pc, err := asPlayback(p)
			if err != nil {
				return err
			}
			if err := call(cmd.Context(), pc); err != nil {
				return friendlyErr(p.ID(), err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), done)
			return nil
		},
	}
	providerFlag(cmd)
	return cmd
}

func newNowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "now", Short: "目前播放狀態", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := getProvider(cmd)
			if err != nil {
				return err
			}
			pc, err := asPlayback(p)
			if err != nil {
				return err
			}
			st, err := pc.State(cmd.Context())
			if err != nil {
				return friendlyErr(p.ID(), err)
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
	providerFlag(cmd)
	return cmd
}

func newDevicesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "devices", Short: "列出 Spotify Connect 裝置", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := getProvider(cmd)
			if err != nil {
				return err
			}
			pc, err := asPlayback(p)
			if err != nil {
				return err
			}
			ds, err := pc.Devices(cmd.Context())
			if err != nil {
				return friendlyErr(p.ID(), err)
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
	providerFlag(cmd)
	return cmd
}
