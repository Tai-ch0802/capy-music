package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/cache"
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
		Use:   "play [query...|artist:<name>|pl:<name>|track:<name>|spotify:track:URI|track ID]",
		Short: "播放:曲目、藝人熱門歌曲、我的播放清單(無參數 = 恢復播放;歧義時在終端機開挑選器)",
		Long: `統一搜尋曲目、藝人與本機快取的播放清單(先跑過 capy pl list 才有清單)。
終端機下唯一明確命中直接播,否則開挑選器;非 TTY 一律確定性:歧義回 exit 2 並印 TSV 候選(type, id, label, detail),
用 --type 或前綴(artist: / pl: / track:)指定類型即可在腳本裡穩定使用。`,
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
			typ, _ := cmd.Flags().GetString("type")
			pick, _ := cmd.Flags().GetBool("pick")
			interactive := isInteractive(cmd)
			req := provider.PlayRequest{}
			var label string
			var chosen *candidate
			switch {
			case id != "" && len(args) > 0:
				return fmt.Errorf("--id 與搜尋詞擇一")
			case id != "":
				req.TrackIDs = []string{id}
				label = id
			case pick:
				if !interactive {
					return errors.New("--pick 需要終端機;非互動環境請直接給搜尋詞並用 --type 或前綴指定類型")
				}
				cands := markUnplayablePlaylists(p, cacheCandidates(cache.Load(), p.ID()))
				if len(cands) == 0 {
					return errors.New("本機快取是空的:先跑 capy pl list,或先用 capy play <query> 播過幾次")
				}
				if chosen, err = runPlayPicker(cands); err != nil {
					return err
				}
			case len(args) == 0: // resume(R1:維持既有語意)
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
				q, t, err := parsePlayQuery(args, typ)
				if err != nil {
					return err
				}
				hit, cands, err := resolvePlay(ctx, playSourcesFor(p), q, t)
				if err != nil {
					return friendlyErr(p.ID(), err)
				}
				cands = markUnplayablePlaylists(p, cands)
				switch {
				case hit != nil:
					chosen = hit
				case len(cands) == 0:
					return fmt.Errorf("找不到:%s", q)
				case interactive:
					if chosen, err = runPlayPicker(cands); err != nil {
						return err
					}
				default:
					for _, c := range cands { // TSV 候選到 stdout;exit 2 由 main 決定
						fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", c.Type, c.ID, tsvCell(c.Label), tsvCell(c.Detail))
					}
					return &AmbiguousError{Candidates: cands}
				}
			}
			if chosen != nil {
				if req, label, err = playRequestFor(ctx, p, *chosen); err != nil {
					return friendlyErr(p.ID(), err)
				}
			}
			if name, _ := cmd.Flags().GetString("device"); name != "" {
				if req.DeviceID, err = resolveDeviceID(ctx, pc, p.ID(), name); err != nil {
					return err
				}
			}
			if err := pc.Play(ctx, req); err != nil {
				return friendlyErr(p.ID(), err)
			}
			if chosen != nil {
				rememberRecent(p.ID(), *chosen)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "▶", ui.Bold(stdoutIsTTY(cmd), label))
			return nil
		},
	}
	cmd.Flags().String("device", "", "目標裝置名稱(見 capy devices)")
	cmd.Flags().String("id", "", "直接以 provider 的 track ID 播放(跳過搜尋)")
	cmd.Flags().String("type", "", "只搜這一類:track|artist|playlist(前綴 artist: / pl: / track: 同義)")
	cmd.Flags().Bool("pick", false, "直接開挑選器(本機快取的清單與最近項目;需要終端機)")
	providerFlag(cmd)
	cmd.ValidArgsFunction = playCompletion
	_ = cmd.RegisterFlagCompletionFunc("type", cobra.FixedCompletions([]string{"track", "artist", "playlist"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

// isInteractive:挑選器要同時能畫(stdout)與能讀鍵盤(stdin)。測試替換點。
var isInteractive = func(cmd *cobra.Command) bool { return stdoutIsTTY(cmd) && stdinIsTTY() }

var tsvCellEscaper = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")

func tsvCell(s string) string { return tsvCellEscaper.Replace(s) }

// playSourcesFor:把 provider 的能力接成 resolvePlay 的資料來源;快取清單永遠可用(不打網路)。
func playSourcesFor(p provider.Provider) playSources {
	src := playSources{playlists: func() []cache.Playlist { return cache.Load().Playlists[p.ID()] }}
	if s, ok := p.(provider.Searcher); ok && p.Caps().Has(provider.CapSearch) {
		src.tracks = func(ctx context.Context, q string, limit int) ([]provider.Track, error) {
			return s.Search(ctx, provider.Query{Text: q, Limit: limit})
		}
	}
	if a, ok := p.(provider.ArtistSearcher); ok && p.Caps().Has(provider.CapArtistSearch) {
		src.artists = func(ctx context.Context, q string, limit int) ([]provider.Artist, error) {
			return a.SearchArtists(ctx, provider.Query{Text: q, Limit: limit})
		}
	}
	return src
}

// playRequestFor:候選 → PlayRequest 與顯示標籤。藝人 = 熱門歌曲全部排進去(Apple 端只播第一首,見其 Play)。
func playRequestFor(ctx context.Context, p provider.Provider, c candidate) (provider.PlayRequest, string, error) {
	switch c.Type {
	case cache.TypePlaylist:
		return provider.PlayRequest{PlaylistID: c.ID}, fmt.Sprintf("清單:%s(%s)", c.Label, c.Detail), nil
	case cache.TypeArtist:
		a, ok := p.(provider.ArtistSearcher)
		if !ok || !p.Caps().Has(provider.CapArtistSearch) {
			return provider.PlayRequest{}, "", notSupported(p, "藝人熱門歌曲")
		}
		tracks, err := a.ArtistTopTracks(ctx, provider.Artist{ProviderID: c.ID, Name: c.Label})
		if err != nil {
			return provider.PlayRequest{}, "", err
		}
		if len(tracks) == 0 {
			return provider.PlayRequest{}, "", fmt.Errorf("%s 沒有熱門歌曲", c.Label)
		}
		ids := make([]string, len(tracks))
		for i, t := range tracks {
			ids[i] = t.ProviderID
		}
		return provider.PlayRequest{TrackIDs: ids}, fmt.Sprintf("%s 的熱門歌曲(%d 首)", c.Label, len(ids)), nil
	default:
		return provider.PlayRequest{TrackIDs: []string{c.ID}}, c.Label + " — " + strings.SplitN(c.Detail, " · ", 2)[0], nil
	}
}

// markUnplayablePlaylists:provider 沒有 CapPlayPlaylist(Apple,R4)時,挑選器與 TSV 裡的清單候選標明不可播,
// 讓人在選之前就知道,而不是選了才吃錯誤。
func markUnplayablePlaylists(p provider.Provider, cands []candidate) []candidate {
	if p.Caps().Has(provider.CapPlayPlaylist) {
		return cands
	}
	for i := range cands {
		if cands[i].Type == cache.TypePlaylist {
			cands[i].Detail += "(" + p.DisplayName() + " 暫不支援清單播放,用 capy pl show)"
		}
	}
	return cands
}

// rememberRecent:成功播放後記到快取(失敗靜默——它只是快取)。
func rememberRecent(providerID string, c candidate) {
	cc := cache.Load()
	cc.AddRecent(cache.Recent{Provider: providerID, Type: c.Type, ID: c.ID, Label: c.Label, Detail: c.Detail})
	_ = cc.Save()
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
		Use: "now", Short: "目前播放狀態(--watch 持續顯示,含進度條與鍵位控制)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			watch, _ := cmd.Flags().GetBool("watch")
			if watch && !isInteractive(cmd) {
				return errors.New("--watch 需要終端機;非互動環境請用 capy now(單次純文字)")
			}
			p, err := getProvider(cmd)
			if err != nil {
				return err
			}
			pc, err := asPlayback(p)
			if err != nil {
				return err
			}
			if watch {
				return runWatch(cmd, p, pc)
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
	cmd.Flags().Bool("watch", false, "持續顯示(bubbletea 畫面;space 播放/暫停、n/p 上下首、q 離開)")
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
