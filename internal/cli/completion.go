package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

// shell 補全(決策 14)。鐵則:只讀本機快取(state.db,等鎖上限 200 ms、逾時視同空)、絕不打網路、絕不碰 keychain——
// TAB 每按一次就跑一次,而建構 provider 會取檔案鎖、讀 keychain、打網路;這裡絕不呼叫 newProvider。
// 候選要先跑過 capy pl list / play 才會有。

func completeFromCache(cmd *cobra.Command, toComplete string, playlists, recent bool) ([]string, cobra.ShellCompDirective) {
	pid, _ := cmd.Flags().GetString(flagProvider)
	c := cache.Load()
	var out []string
	seen := map[string]bool{}
	add := func(name, desc string) {
		name, desc = tsvCell(name), tsvCell(desc) // cobra 的補全協定是「一行一個候選、\t 後接說明」,名稱裡的 tab/換行會弄壞它
		if name == "" || seen[name] || !strings.HasPrefix(strings.ToLower(name), strings.ToLower(toComplete)) {
			return
		}
		seen[name] = true
		out = append(out, name+"\t"+desc)
	}
	if playlists {
		for _, p := range c.Playlists[pid] {
			add(p.Name, i18n.T("completion.playlist"))
		}
	}
	if recent {
		for _, r := range c.Recent {
			if r.Provider == pid {
				add(r.Label, recentDesc(r.Type))
			}
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// recentDesc:最近紀錄候選的說明。刻意不借 play_resolve.go 的 typeNames(挑選器的標籤):整句各自翻,英文才通順。
func recentDesc(typ string) string {
	switch typ {
	case cache.TypePlaylist:
		return i18n.T("completion.recent.playlist")
	case cache.TypeArtist:
		return i18n.T("completion.recent.artist")
	case cache.TypeTrack:
		return i18n.T("completion.recent.track")
	case cache.TypeQuery:
		return i18n.T("completion.recent.query")
	}
	return i18n.T("completion.recent.other")
}

func playCompletion(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeFromCache(cmd, toComplete, true, true)
}

func plShowCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeFromCache(cmd, toComplete, true, false)
}
