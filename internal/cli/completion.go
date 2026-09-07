package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/cache"
)

// shell 補全(決策 14)。鐵則:只讀 cache.json——TAB 每按一次就跑一次,而建構 provider 會取檔案鎖、
// 讀 keychain、打網路;這裡絕不呼叫 newProvider。候選要先跑過 capy pl list / play 才會有。

func completeFromCache(cmd *cobra.Command, toComplete string, playlists, recent bool) ([]string, cobra.ShellCompDirective) {
	pid, _ := cmd.Flags().GetString(flagProvider)
	c := cache.Load()
	var out []string
	seen := map[string]bool{}
	add := func(name, desc string) {
		if name == "" || seen[name] || !strings.HasPrefix(strings.ToLower(name), strings.ToLower(toComplete)) {
			return
		}
		seen[name] = true
		out = append(out, name+"\t"+desc)
	}
	if playlists {
		for _, p := range c.Playlists[pid] {
			add(p.Name, "播放清單")
		}
	}
	if recent {
		for _, r := range c.Recent {
			if r.Provider == pid {
				add(r.Label, "最近:"+typeNames[r.Type])
			}
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
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
