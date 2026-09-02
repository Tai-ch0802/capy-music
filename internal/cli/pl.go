package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

func newPlCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pl", Short: "播放清單"}
	cmd.AddCommand(newPlListCmd(), newPlShowCmd())
	return cmd
}

func newPlListCmd() *cobra.Command {
	return &cobra.Command{
		Use: "list", Short: "列出我的播放清單", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := newSpotifyProvider(cmd.Context())
			if err != nil {
				return err
			}
			refs, err := p.ListPlaylists(cmd.Context())
			if err != nil {
				return friendlyErr(err)
			}
			rows := make([][]string, len(refs))
			for i, r := range refs {
				rows[i] = []string{r.ID, r.Name, strconv.Itoa(r.Total), r.Owner}
			}
			ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), []string{"ID", "名稱", "曲數", "擁有者"}, rows)
			return nil
		},
	}
}

// resolvePlaylistID:名稱(不分大小寫)精確比對 → 唯一即用;
// 0 筆且引數像 22 碼 base62 → 當 ID;其餘給可行動錯誤。
func resolvePlaylistID(ctx context.Context, p interface {
	ListPlaylists(context.Context) ([]provider.PlaylistRef, error)
}, arg string) (string, error) {
	refs, err := p.ListPlaylists(ctx)
	if err != nil {
		return "", friendlyErr(err)
	}
	var hits []provider.PlaylistRef
	for _, r := range refs {
		if strings.EqualFold(r.Name, arg) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0].ID, nil
	case 0:
		if spotifyTrackIDRe.MatchString(arg) { // 22 碼 base62,playlist ID 同格式
			return arg, nil
		}
		return "", fmt.Errorf("找不到名為 %q 的清單 — 用 capy pl list 查看", arg)
	default:
		ids := make([]string, len(hits))
		for i, h := range hits {
			ids[i] = fmt.Sprintf("%s(%s)", h.ID, h.Owner)
		}
		return "", fmt.Errorf("有 %d 個同名清單,請改用 ID:%s", len(hits), strings.Join(ids, "、"))
	}
}

func newPlShowCmd() *cobra.Command {
	return &cobra.Command{
		Use: "show <name|playlist ID>", Short: "顯示清單內容", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := newSpotifyProvider(ctx)
			if err != nil {
				return err
			}
			id, err := resolvePlaylistID(ctx, p, args[0])
			if err != nil {
				return err
			}
			tracks, err := p.GetPlaylistItems(ctx, id)
			if errors.Is(err, provider.ErrRestricted) {
				return fmt.Errorf("這是追蹤的他人清單 — Spotify 2026-02 起只提供 metadata、不提供內容(spec §1.1),無法顯示")
			}
			if err != nil {
				return friendlyErr(err)
			}
			tty := stdoutIsTTY(cmd)
			ui.Table(cmd.OutOrStdout(), tty, []string{"ID", "曲名", "藝人", "專輯", "時長"}, trackRows(tracks, tty))
			return nil
		},
	}
}
