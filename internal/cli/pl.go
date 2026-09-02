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
	cmd := &cobra.Command{
		Use: "list", Short: "列出我的播放清單", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := getProvider(cmd)
			if err != nil {
				return err
			}
			r, err := asPlaylistReader(p)
			if err != nil {
				return err
			}
			refs, err := r.ListPlaylists(cmd.Context())
			if err != nil {
				return friendlyErr(p.ID(), err)
			}
			rows := make([][]string, len(refs))
			for i, ref := range refs {
				rows[i] = []string{ref.ID, ref.Name, strconv.Itoa(ref.Total), ref.Owner}
			}
			ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), []string{"ID", "名稱", "曲數", "擁有者"}, rows)
			return nil
		},
	}
	providerFlag(cmd)
	return cmd
}

// resolvePlaylistID:引數像 22 碼 base62 → 直接當 ID,省一次 list 呼叫;
// 否則名稱(不分大小寫)精確比對 → 唯一即用,其餘給可行動錯誤。
func resolvePlaylistID(ctx context.Context, pr provider.PlaylistReader, providerID, arg string) (string, error) {
	if spotifyBase62IDRe.MatchString(arg) {
		return arg, nil
	}
	refs, err := pr.ListPlaylists(ctx)
	if err != nil {
		return "", friendlyErr(providerID, err)
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
	cmd := &cobra.Command{
		Use: "show <name|playlist ID>", Short: "顯示清單內容", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := getProvider(cmd)
			if err != nil {
				return err
			}
			r, err := asPlaylistReader(p)
			if err != nil {
				return err
			}
			id, err := resolvePlaylistID(ctx, r, p.ID(), args[0])
			if err != nil {
				return err
			}
			tracks, err := r.GetPlaylistItems(ctx, id)
			if errors.Is(err, provider.ErrRestricted) {
				return fmt.Errorf("無法讀取這個清單的內容 — 可能是追蹤的他人清單(Spotify 2026-02 起只提供 metadata,spec §1.1),也可能是授權不足;先跑 capy doctor 確認授權")
			}
			if err != nil {
				return friendlyErr(p.ID(), err)
			}
			tty := stdoutIsTTY(cmd)
			ui.Table(cmd.OutOrStdout(), tty, []string{"ID", "曲名", "藝人", "專輯", "時長"}, trackRows(tracks, tty))
			return nil
		},
	}
	providerFlag(cmd)
	return cmd
}
