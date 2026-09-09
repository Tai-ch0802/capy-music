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
			// 旗標互斥(--id/--pick、--id/--type 由 cobra 擋);其餘會靜默吃掉輸入的組合在這裡明講,不猜。
			isRef := len(args) == 1 && (spotifyTrackURIRe.MatchString(args[0]) || spotifyBase62IDRe.MatchString(args[0]))
			switch {
			case id != "" && len(args) > 0:
				return fmt.Errorf("--id 與搜尋詞擇一")
			case typ != "" && (len(args) == 0 || isRef):
				return errors.New("--type 只用在搜尋詞上(無參數是恢復播放;URI / track ID 不需要類型)")
			case id != "":
				req.TrackIDs = []string{id}
				label = id
			case pick:
				if !interactive {
					return errors.New("--pick 需要終端機;非互動環境請直接給搜尋詞並用 --type 或前綴指定類型")
				}
				q := strings.TrimSpace(strings.Join(args, " ")) // --pick <query>:query 是快取候選的前置過濾
				cands := markUnplayablePlaylists(p, cacheCandidates(cache.Load(), p.ID(), q))
				if len(cands) == 0 && q != "" {
					return fmt.Errorf("本機快取裡沒有符合 %q 的清單或最近項目;不帶搜尋詞的 --pick 會列出全部", q)
				}
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
					for _, c := range cands { // TSV 候選到 stdout;exit 2 與原因(stderr)由 main 決定
						fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", c.Type, c.ID, tsvCell(c.Label), tsvCell(c.Detail+c.Note))
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
	cmd.MarkFlagsMutuallyExclusive("id", "pick")
	cmd.MarkFlagsMutuallyExclusive("id", "type")
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
		if len(ids) > 1 && !p.Caps().Has(provider.CapPlayQueue) { // 不能排佇列的平台只播第一首,標籤別說謊
			return provider.PlayRequest{TrackIDs: ids}, fmt.Sprintf("%s:%s(%s 一次只播一首,佇列待 P4)", c.Label, tracks[0].Title, p.DisplayName()), nil
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
			cands[i].Note = "(" + p.DisplayName() + " 暫不支援清單播放,用 capy pl show)" // Note 不進快取
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

// ctlWithArg:吃一個參數的播放遙控(seek / vol)。骨架同 simpleCtl,但參數先解析再取 provider——
// 打錯格式不該先去建 client、更不該先要求有憑證。
func ctlWithArg(use, short string, parse func(string) (int, error), do func(context.Context, provider.PlaybackController, int) error, done func(int) string) *cobra.Command {
	cmd := &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := parse(args[0])
			if err != nil {
				return err
			}
			p, err := getProvider(cmd)
			if err != nil {
				return err
			}
			pc, err := asPlayback(p)
			if err != nil {
				return err
			}
			if err := do(cmd.Context(), pc, v); err != nil {
				return friendlyErr(p.ID(), err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), done(v))
			return nil
		},
	}
	providerFlag(cmd)
	return cmd
}

// parseSeekPos:純秒數、mm:ss 或 h:mm:ss → 毫秒。最後兩段要 0-59(「1:75」比較可能是打錯而不是
// 想要 2:15);最前面那段沒有上界,65:30 是合法的 65 分 30 秒。h:mm:ss 是為了 podcast 與 live 全場——
// 只收 mm:ss 的話那些內容只能自己換算成秒。
func parseSeekPos(s string) (int, error) {
	bad := fmt.Errorf("位置要寫成 [h:]mm:ss 或純秒數(例:1:23、1:05:30 或 83),不是 %q", s)
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) > 3 {
		return 0, bad
	}
	total := 0
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, bad
		}
		if i > 0 && n > 59 { // 第一段是最大的單位,不設上界;後面的是分與秒
			return 0, bad
		}
		total = total*60 + n
	}
	return total * 1000, nil
}

// parseVolPct:0-100 的整數。夾範圍不做:使用者打 150 是想調到 150,靜靜改成 100 會讓人以為壞了。
func parseVolPct(s string) (int, error) {
	pct, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || pct < 0 || pct > 100 {
		return 0, fmt.Errorf("音量要是 0-100 的整數,不是 %q", s)
	}
	return pct, nil
}

func newSeekCmd() *cobra.Command {
	return ctlWithArg("seek <[h:]mm:ss|秒>", "跳到曲目內的指定位置", parseSeekPos,
		func(ctx context.Context, pc provider.PlaybackController, ms int) error { return pc.Seek(ctx, ms) },
		func(ms int) string { return "⏩ 已跳到 " + ui.FormatDuration(ms) })
}

func newVolCmd() *cobra.Command {
	return ctlWithArg("vol <0-100>", "設定播放音量", parseVolPct,
		func(ctx context.Context, pc provider.PlaybackController, pct int) error {
			return pc.SetVolume(ctx, pct)
		},
		func(pct int) string { return fmt.Sprintf("🔊 音量 %d", pct) })
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
			w := cmd.OutOrStdout()
			if errors.Is(err, provider.ErrPlayerNotRunning) { // 狀態查詢:播放器沒開是一種狀態,不是錯
				fmt.Fprintln(w, err.Error())
				return nil
			}
			if err != nil {
				return friendlyErr(p.ID(), err)
			}
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
	cmd.Flags().Bool("watch", false, "持續顯示(bubbletea 畫面;space 播放/暫停、n/p 上下首、q/esc 離開)")
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
