package cli

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

func newPlCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pl", Short: i18n.T("cmd.pl.short")}
	cmd.AddCommand(newPlListCmd(), newPlShowCmd(), newPlLinkCmd(), newPlUnlinkCmd(), newPlPullCmd(), newPlPushCmd(), newPlSyncCmd(), newPlDedupCmd())
	return cmd
}

func newPlListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "list", Short: i18n.T("cmd.pl.list.short"), Args: cobra.NoArgs,
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
				total := strconv.Itoa(ref.Total)
				if ref.Total < 0 { // Apple library playlist 物件不含曲數(client.go)
					total = "-"
				}
				rows[i] = []string{ref.ID, ref.Name, total, ref.Owner}
			}
			if err := ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), []string{"ID", "NAME", "TRACKS", "OWNER"}, rows); err != nil {
				return err
			}
			cc := cache.Load() // 補全與 play 的清單來源(只是快取,寫失敗靜默)
			pls := make([]cache.Playlist, len(refs))
			for i, ref := range refs {
				pls[i] = cache.Playlist{ID: ref.ID, Name: ref.Name, Total: ref.Total}
			}
			cc.SetPlaylists(p.ID(), pls)
			_ = cc.Save()
			return nil
		},
	}
	providerFlag(cmd)
	return cmd
}

// resolvePlaylistID:引數像 22 碼 base62 → 直接當 ID,省一次 list 呼叫;
// 否則先比其他 provider 自己的 ID 格式(Apple 是 p.xxx,不進 base62 快速路徑);
// 再不中才按名稱(不分大小寫)精確比對 → 唯一即用,其餘給可行動錯誤。
func resolvePlaylistID(ctx context.Context, pr provider.PlaylistReader, providerID, arg string) (string, error) {
	if spotifyBase62IDRe.MatchString(arg) {
		return arg, nil
	}
	refs, err := pr.ListPlaylists(ctx)
	if err != nil {
		return "", friendlyErr(providerID, err)
	}
	for _, r := range refs { // provider 自己的 ID 格式(Apple 是 p.xxx)不進 base62 快速路徑,先比 ID;
		if r.ID == arg || strings.HasSuffix(r.ID, "/"+arg) { // local 的 id 是 <device_id>/<檔名>,使用者只打檔名(決策 33)
			return r.ID, nil
		}
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
		return "", i18n.Errorf("pl.err.not_found", "name", strconv.Quote(arg))
	default:
		ids := make([]string, len(hits))
		for i, h := range hits {
			ids[i] = i18n.T("pl.same_name_item", "id", h.ID, "owner", h.Owner)
		}
		return "", i18n.Errorf("pl.err.same_name", "count", len(hits), "ids", strings.Join(ids, i18n.T("sep.list")))
	}
}

func newPlShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "show [name|playlist ID]", Short: i18n.T("cmd.pl.show.short"), Args: argsOrPicker(1),
		ValidArgsFunction: plShowCompletion,
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
			var id string
			if len(args) == 1 {
				if id, err = resolvePlaylistID(ctx, r, p.ID(), args[0]); err != nil {
					return err
				}
			} else { // 標題點名平台:沒給 --provider 時列的是 default_provider 的清單
				refs, err := r.ListPlaylists(ctx)
				if err != nil {
					return friendlyErr(p.ID(), err)
				}
				if id, err = pickPlatformPlaylist(p.ID(), refs, ""); err != nil {
					return err
				}
			}
			tracks, err := r.GetPlaylistItems(ctx, id)
			if errors.Is(err, provider.ErrRestricted) {
				return errRestrictedPlaylist
			}
			if err != nil {
				return friendlyErr(p.ID(), err)
			}
			tty := stdoutIsTTY(cmd)
			return ui.Table(cmd.OutOrStdout(), tty, trackHeader, trackRows(tracks, tty))
		},
	}
	providerFlag(cmd)
	return cmd
}
