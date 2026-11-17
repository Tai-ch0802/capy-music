package cli

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

const (
	playCanonBusy      = 200 * time.Millisecond
	maxPlayCanonTracks = 100
)

var (
	spotifyTrackURIRe = regexp.MustCompile(`^spotify:track:([0-9A-Za-z]{22})$`)
	// spotifyBase62IDRe:22 碼 base62,track ID 與 playlist ID 共用此格式。
	spotifyBase62IDRe = regexp.MustCompile(`^[0-9A-Za-z]{22}$`)

	// playRandIntn:Fisher-Yates 洗牌隨機源(測試替換點)。
	playRandIntn = rand.IntN

	// pickDevice:TTY 下開播放裝置挑選器,回選中的裝置。
	// 包成 package-level var 並註冊進 installWebPromptSeams(CLAUDE.md 與 seams_test.go 硬約束)。
	pickDevice = runPickDevice
)

func shuffleStrings(xs []string) {
	for i := len(xs) - 1; i > 0; i-- {
		j := playRandIntn(i + 1)
		xs[i], xs[j] = xs[j], xs[i]
	}
}

func matchDevice(ds []provider.Device, query string) (provider.Device, bool) {
	trimmed := strings.TrimSpace(query)
	for _, d := range ds {
		if d.ID != "" && (d.ID == query || d.ID == trimmed) {
			return d, true
		}
	}
	for _, d := range ds {
		if strings.EqualFold(d.Name, query) || strings.EqualFold(strings.TrimSpace(d.Name), trimmed) {
			return d, true
		}
	}
	return provider.Device{}, false
}

func devicePickerLabels(ds []provider.Device) []string {
	labels := make([]string, len(ds))
	for i, d := range ds {
		label := i18n.T("player.device", "name", d.Name, "type", d.Type)
		if d.VolumeKnown {
			label += i18n.T("devices.pick.volume", "pct", d.VolumePct)
		}
		if d.Active {
			label += i18n.T("devices.pick.active")
		}
		labels[i] = label
	}
	return labels
}

func runPickDevice(ds []provider.Device, title string) (provider.Device, error) {
	idx, err := pickOne(title, devicePickerLabels(ds))
	if err != nil {
		return provider.Device{}, err
	}
	return ds[idx], nil
}

// resolveDeviceID:精確比對 ID 或不分大小寫比對名稱,取第一個相符(重名取先;ponytail: 夠用)。
func resolveDeviceID(ctx context.Context, pc provider.PlaybackController, providerID, name string) (string, error) {
	ds, err := pc.Devices(ctx)
	if err != nil {
		return "", friendlyErr(providerID, err)
	}
	if len(ds) == 0 {
		return "", friendlyErr(providerID, provider.ErrNoActiveDevice)
	}
	if d, ok := matchDevice(ds, name); ok {
		return d.ID, nil
	}
	names := make([]string, len(ds))
	for i, d := range ds {
		names[i] = d.Name
	}
	return "", i18n.Errorf("play.err.device_not_found", "name", strconv.Quote(name), "devices", strings.Join(names, i18n.T("sep.list")))
}

func newPlayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "play [query...|artist:<name>|pl:<name>|canon:<name|pid>|track:<name>|spotify:track:URI|track ID]",
		Short: i18n.T("cmd.play.short"),
		Long:  i18n.T("cmd.play.long"),
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
			shuffle, _ := cmd.Flags().GetBool("shuffle")
			interactive := isInteractive(cmd)
			req := provider.PlayRequest{}
			var label string
			var chosen *candidate
			// 旗標互斥(--id/--pick、--id/--type 由 cobra 擋);其餘會靜默吃掉輸入的組合在這裡明講,不猜。
			isRef := len(args) == 1 && (spotifyTrackURIRe.MatchString(args[0]) || spotifyBase62IDRe.MatchString(args[0]))
			switch {
			case id != "" && len(args) > 0:
				return i18n.Errorf("play.err.id_with_query")
			case typ != "" && (len(args) == 0 || isRef):
				return i18n.Errorf("play.err.type_without_query")
			case id != "":
				req.TrackIDs = []string{id}
				label = id
			case pick:
				if !interactive {
					return i18n.Errorf("play.err.pick_needs_tty")
				}
				q := strings.TrimSpace(strings.Join(args, " ")) // --pick <query>:query 是快取候選的前置過濾
				cc := cache.Load()
				canons := loadCanonPlaylists(p)
				// 冷快取友善優化:清單快取與正本皆空且 provider 支援讀取清單時,自動抓一次清單寫入快取再開挑選器。
				if len(cc.Playlists[p.ID()]) == 0 && len(canons) == 0 {
					if r, rerr := asPlaylistReader(p); rerr == nil {
						refs, lerr := r.ListPlaylists(ctx)
						if lerr != nil && len(cc.Recent) == 0 {
							return friendlyErr(p.ID(), lerr)
						}
						if lerr == nil && len(refs) > 0 {
							pls := make([]cache.Playlist, len(refs))
							for i, ref := range refs {
								pls[i] = cache.Playlist{ID: ref.ID, Name: ref.Name, Total: ref.Total}
							}
							cc.SetPlaylists(p.ID(), pls)
							_ = cc.Save()
						}
					}
				}
				cands := markUnplayablePlaylists(p, cacheCandidatesWithCanons(cc, canons, p.Caps().Has(provider.CapPlayPlaylist), p.ID(), q))
				if len(cands) == 0 && q != "" {
					return i18n.Errorf("play.err.pick_no_match", "query", strconv.Quote(q))
				}
				if len(cands) == 0 {
					return i18n.Errorf("play.err.pick_cache_empty")
				}
				if chosen, err = runPlayPicker(cands); err != nil {
					return err
				}
			case len(args) == 0: // resume(R1:維持既有語意)
				label = i18n.T("play.label.resume")
			case len(args) == 1 && spotifyTrackURIRe.MatchString(args[0]):
				req.TrackIDs = []string{spotifyTrackURIRe.FindStringSubmatch(args[0])[1]}
				label = args[0]
			case len(args) == 1 && spotifyBase62IDRe.MatchString(args[0]):
				req.TrackIDs = []string{args[0]}
				label = args[0]
			case len(args) == 1 && strings.HasPrefix(args[0], "spotify:"):
				return i18n.Errorf("play.err.unsupported_uri", "ref", args[0])
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
					return i18n.Errorf("play.err.not_found", "query", q)
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
				if req, label, err = playRequestForOpts(ctx, p, *chosen, shuffle); err != nil {
					return friendlyErr(p.ID(), err)
				}
			}
			if name, _ := cmd.Flags().GetString("device"); name != "" {
				if req.DeviceID, err = resolveDeviceID(ctx, pc, p.ID(), name); err != nil {
					return err
				}
			}
			playErr := pc.Play(ctx, req)
			opened := errors.Is(playErr, provider.ErrOpenedNotPlaying) // 只在播放器裡打開、沒開始播(Apple 目錄歌曲,決策 52)
			if playErr != nil && !opened {
				return friendlyErr(p.ID(), playErr)
			}
			if !opened && shuffle && (chosen == nil || chosen.Type == cache.TypePlaylist) && len(req.TrackIDs) == 0 {
				if mc, ok := p.(provider.PlaybackModeController); ok {
					if err := mc.SetShuffle(ctx, true); err != nil {
						return friendlyErr(p.ID(), err)
					}
				}
			}
			if chosen != nil {
				rememberRecent(p.ID(), *chosen)
			}
			if opened { // 照實說、不印 ▶、exit 0:web 搜尋頁看「不以 ▶ 開頭」把這句顯示出來(search.js)
				// 平台查到的「歌名 — 歌手」優先:--id 時 label 只是 id;artist: 的 label 是「藝人:歌名(只取第一首…)」整句,
				// 包進這句話會括號套括號,而且沒開始播,「只取第一首」在這裡也沒意義(#96 review)。
				var oe *provider.OpenedError
				if errors.As(playErr, &oe) && oe.Label != "" {
					label = oe.Label
				}
				fmt.Fprintln(cmd.OutOrStdout(), i18n.T("play.opened_not_playing", "label", label))
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "▶", ui.Bold(stdoutIsTTY(cmd), label))
			return nil
		},
	}
	cmd.Flags().String("device", "", i18n.T("cmd.play.flag.device"))
	cmd.Flags().String("id", "", i18n.T("cmd.play.flag.id"))
	cmd.Flags().String("type", "", i18n.T("cmd.play.flag.type"))
	cmd.Flags().Bool("pick", false, i18n.T("cmd.play.flag.pick"))
	cmd.Flags().Bool("shuffle", false, i18n.T("play.flag.shuffle"))
	providerFlag(cmd)
	cmd.MarkFlagsMutuallyExclusive("id", "pick")
	cmd.MarkFlagsMutuallyExclusive("id", "type")
	cmd.ValidArgsFunction = playCompletion
	_ = cmd.RegisterFlagCompletionFunc("type", cobra.FixedCompletions([]string{"track", "artist", "playlist", "canon"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

// isInteractive:挑選器要同時能畫(stdout)與能讀鍵盤(stdin)。測試替換點。
var isInteractive = func(cmd *cobra.Command) bool { return stdoutIsTTY(cmd) && stdinIsTTY() }

var tsvCellEscaper = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")

func tsvCell(s string) string { return tsvCellEscaper.Replace(s) }

// loadCanonPlaylists:唯讀開啟 state.db(200ms),讀取本機 Canonical 正本清單候選;無 db 或失敗靜默回空(零寫入副作用)。
func loadCanonPlaylists(p provider.Provider) []canonPlaylist {
	st, err := store.OpenReadOnly(playCanonBusy)
	if err != nil {
		return nil
	}
	defer st.Close()
	c, err := st.Dump()
	if err != nil || len(c.Playlists) == 0 {
		return nil
	}
	id := canon.NewIdentity(c.Tracks.Tracks, c.Tracks.Merged)
	out := make([]canonPlaylist, 0, len(c.Playlists))
	for _, pl := range c.Playlists {
		mapped := 0
		for _, it := range pl.Items {
			cid := id.Redirect(it.CID)
			if tr, ok := c.Tracks.Tracks[cid]; ok {
				if m, has := tr.Mappings[p.ID()]; has && m.ID != "" {
					mapped++
				}
			}
		}
		linkID := pl.Links[p.ID()]
		if linkID != "" && foreignLink(p, linkID) {
			linkID = ""
		}
		out = append(out, canonPlaylist{
			PID:    pl.PID,
			Name:   pl.Name,
			Total:  len(pl.Items),
			Mapped: mapped,
			LinkID: linkID,
		})
	}
	return out
}

// playSourcesFor:把 provider 的能力與本機快取(平台清單 + Canonical 正本清單)接成 resolvePlay 的資料來源。
func playSourcesFor(p provider.Provider) playSources {
	src := playSources{
		playlists:       func() []cache.Playlist { return cache.Load().Playlists[p.ID()] },
		canons:          func() []canonPlaylist { return loadCanonPlaylists(p) },
		canPlayPlaylist: p.Caps().Has(provider.CapPlayPlaylist),
	}
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

// playRequestFor:候選 → PlayRequest 與顯示標籤(預設不 shuffle)。
func playRequestFor(ctx context.Context, p provider.Provider, c candidate) (provider.PlayRequest, string, error) {
	return playRequestForOpts(ctx, p, c, false)
}

func playRequestForOpts(ctx context.Context, p provider.Provider, c candidate, shuffle bool) (provider.PlayRequest, string, error) {
	switch c.Type {
	case cache.TypePlaylist:
		return provider.PlayRequest{PlaylistID: c.ID}, i18n.T("play.label.playlist", "name", c.Label, "detail", c.Detail), nil
	case typeCanon:
		return playRequestForCanon(p, c.ID, shuffle)
	case cache.TypeArtist:
		a, ok := p.(provider.ArtistSearcher)
		if !ok || !p.Caps().Has(provider.CapArtistSearch) {
			return provider.PlayRequest{}, "", notSupported(p, i18n.T("platform.cap.artist_top_tracks"))
		}
		tracks, err := a.ArtistTopTracks(ctx, provider.Artist{ProviderID: c.ID, Name: c.Label})
		if errors.Is(err, provider.ErrOnlyNamesakes) { // 照實說找不到、為什麼,並建議直接搜歌名(不改播同名的別人)
			return provider.PlayRequest{}, "", i18n.Errorf("play.err.no_top_tracks_namesakes", "artist", c.Label, "platform", p.DisplayName())
		}
		if err != nil {
			return provider.PlayRequest{}, "", err
		}
		if len(tracks) == 0 {
			return provider.PlayRequest{}, "", i18n.Errorf("play.err.no_top_tracks", "artist", c.Label, "platform", p.DisplayName())
		}
		ids := make([]string, len(tracks))
		for i, t := range tracks {
			ids[i] = t.ProviderID
		}
		if shuffle && len(ids) > 1 {
			shuffleStrings(ids)
		}
		if len(ids) > 1 && !p.Caps().Has(provider.CapPlayQueue) { // 不能排佇列的平台只播第一首,標籤別說謊
			firstTitle := tracks[0].Title
			if shuffle {
				for _, t := range tracks {
					if t.ProviderID == ids[0] {
						firstTitle = t.Title
						break
					}
				}
			}
			return provider.PlayRequest{TrackIDs: ids}, i18n.T("play.label.artist_first_only", "artist", c.Label, "title", firstTitle, "platform", p.DisplayName()), nil
		}
		return provider.PlayRequest{TrackIDs: ids}, i18n.T("play.label.artist_top", "artist", c.Label, "count", len(ids)), nil
	default:
		return provider.PlayRequest{TrackIDs: []string{c.ID}}, c.Label + " — " + strings.SplitN(c.Detail, " · ", 2)[0], nil
	}
}

// playRequestForCanon:正本清單直接連播與非破壞性隨機(決策 38)。
//  1. 未帶 --shuffle、已 link 當前平台且平台具 CapPlayPlaylist → 走 PlayRequest{PlaylistID: linkID}。
//  2. 否則依 playlist_items 原序(rank, iid)取出當前平台可播的 ProviderID 切片;若帶 --shuffle 僅在記憶體做 Fisher-Yates 洗牌,
//     state.db 與 Drive 正本 0 寫入;超過 100 首乾淨截斷前 100 首。
func playRequestForCanon(p provider.Provider, pid string, shuffle bool) (provider.PlayRequest, string, error) {
	st, err := store.OpenReadOnly(playCanonBusy)
	if err != nil {
		return provider.PlayRequest{}, "", i18n.Errorf("play.err.canon_not_found", "id", pid)
	}
	defer st.Close()
	c, err := st.Dump()
	if err != nil {
		return provider.PlayRequest{}, "", err
	}
	var pl *canon.Playlist
	for i := range c.Playlists {
		if c.Playlists[i].PID == pid {
			pl = &c.Playlists[i]
			break
		}
	}
	if pl == nil {
		return provider.PlayRequest{}, "", i18n.Errorf("play.err.canon_not_found", "id", pid)
	}
	linkID := pl.Links[p.ID()]
	if linkID != "" && foreignLink(p, linkID) {
		linkID = ""
	}
	if !shuffle && linkID != "" && p.Caps().Has(provider.CapPlayPlaylist) {
		detail := i18n.T("play.candidate.tracks", "count", len(pl.Items))
		return provider.PlayRequest{PlaylistID: linkID}, i18n.T("play.label.canon", "name", pl.Name, "detail", detail), nil
	}
	if len(pl.Items) == 0 {
		return provider.PlayRequest{}, "", i18n.Errorf("play.err.canon_empty", "name", pl.Name)
	}
	id := canon.NewIdentity(c.Tracks.Tracks, c.Tracks.Merged)
	ids := make([]string, 0, len(pl.Items))
	for _, it := range pl.Items {
		cid := id.Redirect(it.CID)
		if tr, ok := c.Tracks.Tracks[cid]; ok {
			if m, has := tr.Mappings[p.ID()]; has && m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
	}
	if len(ids) == 0 {
		return provider.PlayRequest{}, "", i18n.Errorf("play.err.canon_no_mappings", "name", pl.Name, "platform", p.ID())
	}
	if shuffle {
		shuffleStrings(ids)
	}
	if len(ids) > maxPlayCanonTracks {
		ids = ids[:maxPlayCanonTracks]
	}
	if len(ids) > 1 && !p.Caps().Has(provider.CapPlayQueue) {
		return provider.PlayRequest{TrackIDs: ids}, i18n.T("play.label.canon_first_only", "name", pl.Name, "platform", p.DisplayName()), nil
	}
	if shuffle {
		return provider.PlayRequest{TrackIDs: ids}, i18n.T("play.label.canon_shuffled", "name", pl.Name, "count", len(ids)), nil
	}
	detail := i18n.T("play.candidate.tracks", "count", len(ids))
	return provider.PlayRequest{TrackIDs: ids}, i18n.T("play.label.canon", "name", pl.Name, "detail", detail), nil
}

// markUnplayablePlaylists:provider 沒有 CapPlayPlaylist(Apple,R4)時,挑選器與 TSV 裡的清單候選標明不可播;
// 正本清單若尚未有任何對應到該平台的曲目(且無可播 link)也一併標明,讓人在選之前就知道。
func markUnplayablePlaylists(p provider.Provider, cands []candidate) []candidate {
	canPl := p.Caps().Has(provider.CapPlayPlaylist)
	for i := range cands {
		switch cands[i].Type {
		case cache.TypePlaylist:
			if !canPl {
				cands[i].Note = i18n.T("play.note.playlist_unplayable", "platform", p.DisplayName()) // Note 不進快取
			}
		case typeCanon:
			if cands[i].Total > 0 && cands[i].Mapped == 0 && (cands[i].LinkID == "" || !canPl) {
				cands[i].Note = i18n.T("play.note.canon_unmapped", "platform", p.ID())
			}
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
	bad := i18n.Errorf("player.err.bad_seek_pos", "value", strconv.Quote(s))
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
		return 0, i18n.Errorf("player.err.bad_volume", "value", strconv.Quote(s))
	}
	return pct, nil
}

func newSeekCmd() *cobra.Command {
	return ctlWithArg(i18n.T("cmd.seek.use"), i18n.T("cmd.seek.short"), parseSeekPos,
		func(ctx context.Context, pc provider.PlaybackController, ms int) error { return pc.Seek(ctx, ms) },
		func(ms int) string { return i18n.T("cmd.seek.done", "pos", ui.FormatDuration(ms)) })
}

func newVolCmd() *cobra.Command {
	return ctlWithArg("vol <0-100>", i18n.T("cmd.vol.short"), parseVolPct,
		func(ctx context.Context, pc provider.PlaybackController, pct int) error {
			return pc.SetVolume(ctx, pct)
		},
		func(pct int) string { return i18n.T("cmd.vol.done", "pct", pct) })
}

func parseShuffleArg(args []string) (mode string, err error) {
	if len(args) == 0 {
		return "toggle", nil
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "on":
		return "on", nil
	case "off":
		return "off", nil
	case "toggle":
		return "toggle", nil
	default:
		return "", i18n.Errorf("play.err.bad_shuffle", "value", strconv.Quote(args[0]))
	}
}

func newShuffleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "shuffle [on|off|toggle]",
		Short:             i18n.T("play.shuffle.short"),
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: cobra.FixedCompletions([]string{"on", "off", "toggle"}, cobra.ShellCompDirectiveNoFileComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode, err := parseShuffleArg(args)
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
			mc, ok := p.(provider.PlaybackModeController)
			if !ok {
				return i18n.Errorf("play.err.mode_not_supported", "platform", p.DisplayName(), "err", provider.ErrNotSupported)
			}
			ctx := cmd.Context()
			var target bool
			switch mode {
			case "on":
				target = true
			case "off":
				target = false
			case "toggle":
				st, err := pc.State(ctx)
				if err != nil {
					return friendlyErr(p.ID(), err)
				}
				if st == nil {
					return friendlyErr(p.ID(), provider.ErrNoActiveDevice)
				}
				target = !st.Shuffle
			}
			if err := mc.SetShuffle(ctx, target); err != nil {
				return friendlyErr(p.ID(), err)
			}
			if target {
				fmt.Fprintln(cmd.OutOrStdout(), i18n.T("play.shuffle.on"))
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), i18n.T("play.shuffle.off"))
			}
			return nil
		},
	}
	providerFlag(cmd)
	return cmd
}

func parseRepeatArg(args []string) (mode provider.RepeatMode, cycle bool, err error) {
	if len(args) == 0 {
		return "", true, nil
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "off":
		return provider.RepeatOff, false, nil
	case "context", "all":
		return provider.RepeatContext, false, nil
	case "track", "one":
		return provider.RepeatTrack, false, nil
	case "cycle":
		return "", true, nil
	default:
		return "", false, i18n.Errorf("play.err.bad_repeat", "value", strconv.Quote(args[0]))
	}
}

func nextRepeatMode(cur provider.RepeatMode) provider.RepeatMode {
	switch cur {
	case provider.RepeatContext:
		return provider.RepeatTrack
	case provider.RepeatTrack:
		return provider.RepeatOff
	default:
		return provider.RepeatContext
	}
}

func newRepeatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "repeat [off|context|track|cycle]",
		Short:             i18n.T("play.repeat.short"),
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: cobra.FixedCompletions([]string{"off", "context", "track", "cycle"}, cobra.ShellCompDirectiveNoFileComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, cycle, err := parseRepeatArg(args)
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
			mc, ok := p.(provider.PlaybackModeController)
			if !ok {
				return i18n.Errorf("play.err.mode_not_supported", "platform", p.DisplayName(), "err", provider.ErrNotSupported)
			}
			ctx := cmd.Context()
			if cycle {
				st, err := pc.State(ctx)
				if err != nil {
					return friendlyErr(p.ID(), err)
				}
				if st == nil {
					return friendlyErr(p.ID(), provider.ErrNoActiveDevice)
				}
				target = nextRepeatMode(st.Repeat)
			}
			if err := mc.SetRepeat(ctx, target); err != nil {
				return friendlyErr(p.ID(), err)
			}
			switch target {
			case provider.RepeatContext:
				fmt.Fprintln(cmd.OutOrStdout(), i18n.T("play.repeat.context"))
			case provider.RepeatTrack:
				fmt.Fprintln(cmd.OutOrStdout(), i18n.T("play.repeat.track"))
			default:
				fmt.Fprintln(cmd.OutOrStdout(), i18n.T("play.repeat.off"))
			}
			return nil
		},
	}
	providerFlag(cmd)
	return cmd
}

func newNowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "now", Short: i18n.T("cmd.now.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			watch, _ := cmd.Flags().GetBool("watch")
			if watch && !isInteractive(cmd) {
				return i18n.Errorf("player.err.watch_needs_tty")
			}
			// --watch 沒用 --provider 釘住:跟著正在播的平台走(決策 58),平台由 tracker 自己建——預設平台沒登入但別家可以,
			// 照樣開得起來。釘住的照舊先建好再開畫面(平台名打錯、沒登入、不支援播放,一開始就失敗)。
			// 看 Changed 不看值:--provider 的預設值就是 default_provider,值永遠有。
			if watch && !cmd.Flags().Changed(flagProvider) {
				return runWatch(cmd, "", nil)
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
				return runWatch(cmd, p.ID(), pc)
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
				fmt.Fprintln(w, i18n.T("player.nothing_playing"))
				return nil
			}
			tty := stdoutIsTTY(cmd)
			mark := "⏸"
			if st.Playing {
				mark = "▶"
			}
			fmt.Fprintf(w, "%s %s — %s\n", mark, ui.Bold(tty, st.Track.Title), strings.Join(st.Track.Artists, ", "))
			fmt.Fprintf(w, "  %s / %s · %s\n",
				ui.FormatDuration(st.ProgressMS), ui.FormatDuration(st.Track.DurationMS),
				i18n.T("player.device", "name", st.Device.Name, "type", st.Device.Type))
			return nil
		},
	}
	cmd.Flags().Bool("watch", false, i18n.T("cmd.now.flag.watch"))
	providerFlag(cmd)
	return cmd
}

func newDevicesCmd() *cobra.Command {
	var switchArg string
	var pick bool
	cmd := &cobra.Command{
		Use: "devices", Short: i18n.T("cmd.devices.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if pick && !isInteractive(cmd) {
				return i18n.Errorf("devices.err.pick_needs_tty")
			}
			p, err := getProvider(cmd)
			if err != nil {
				return err
			}
			pc, err := asPlayback(p)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if switchArg != "" || pick {
				dt, ok := p.(provider.DeviceTransferrer)
				if !ok {
					return i18n.Errorf("devices.err.transfer_not_supported", "platform", p.DisplayName(), "err", provider.ErrNotSupported)
				}
				ds, err := pc.Devices(ctx)
				if err != nil {
					return friendlyErr(p.ID(), err)
				}
				if len(ds) == 0 {
					return i18n.Errorf("devices.err.no_devices", "platform", p.DisplayName())
				}
				var target provider.Device
				if pick {
					target, err = pickDevice(ds, i18n.T("devices.pick.title"))
					if err != nil {
						return err
					}
				} else {
					var matched bool
					target, matched = matchDevice(ds, switchArg)
					if !matched {
						names := make([]string, len(ds))
						for i, d := range ds {
							names[i] = d.Name
						}
						return i18n.Errorf("play.err.device_not_found", "name", strconv.Quote(switchArg), "devices", strings.Join(names, i18n.T("sep.list")))
					}
				}
				if err := dt.Transfer(ctx, target.ID, true); err != nil {
					return friendlyErr(p.ID(), err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), i18n.T("devices.switched", "name", target.Name, "type", target.Type))
				return nil
			}
			ds, err := pc.Devices(ctx)
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
			return ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), []string{"NAME", "TYPE", "STATUS", "VOLUME", "ID"}, rows)
		},
	}
	cmd.Flags().StringVar(&switchArg, "switch", "", i18n.T("devices.flag.switch"))
	cmd.Flags().BoolVar(&pick, "pick", false, i18n.T("devices.flag.pick"))
	cmd.MarkFlagsMutuallyExclusive("switch", "pick")
	providerFlag(cmd)
	return cmd
}
