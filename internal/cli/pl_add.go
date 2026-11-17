package cli

// capy pl add:把單首曲目(<provider>:<id> 或 --now)收進 Google Drive 正本清單尾端(決策 38)。
// 預設只寫 Drive 正本並 Hydrate state.db;--push 比照 pl dedup 雙半邊模式(先 pull 對齊 → 尾端 Append → strict=false push)。

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/resolve"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// pickCanonicalPlaylist:TTY 下未給清單名時開正本清單挑選器,回選中的 pid。
// 包成 package-level var 並註冊進 installWebPromptSeams(CLAUDE.md 與 seams_test.go 硬約束)。
var pickCanonicalPlaylist = runPickCanonicalPlaylist

func sortedCanonicalPlaylists(s *canonState) ([]*canon.Playlist, []string, error) {
	if len(s.playlists) == 0 {
		return nil, nil, i18n.Errorf("pl.add.err.no_playlists")
	}
	pls := make([]*canon.Playlist, 0, len(s.playlists))
	for _, pid := range slices.Sorted(maps.Keys(s.playlists)) {
		pls = append(pls, s.playlists[pid])
	}
	slices.SortStableFunc(pls, func(a, b *canon.Playlist) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.PID, b.PID)
	})
	labels := make([]string, len(pls))
	for i, pl := range pls {
		if len(pl.Links) > 0 {
			labels[i] = pl.Name + " — " + linkSummary(pl)
		} else {
			labels[i] = pl.Name
		}
	}
	return pls, labels, nil
}

func runPickCanonicalPlaylist(s *canonState, title string) (string, error) {
	pls, labels, err := sortedCanonicalPlaylists(s)
	if err != nil {
		return "", err
	}
	idx, err := pickOne(title, labels)
	if err != nil {
		return "", err
	}
	return pls[idx].PID, nil
}

func newPlAddCmd() *cobra.Command {
	var nowFlag, pushFlag, allowDup, dryRun, yes bool
	var prov string
	cmd := &cobra.Command{
		Use:   "add " + i18n.T("cmd.pl.add.args"),
		Short: i18n.T("cmd.pl.add.short"),
		Long:  i18n.T("cmd.pl.add.long"),
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlAdd(cmd, args, prov, nowFlag, pushFlag, allowDup, dryRun, yes)
		},
	}
	cmd.Flags().BoolVar(&nowFlag, "now", false, i18n.T("cmd.pl.add.flag.now"))
	cmd.Flags().StringVar(&prov, "provider", "", i18n.T("cmd.pl.add.flag.provider"))
	cmd.Flags().BoolVar(&pushFlag, "push", false, i18n.T("cmd.pl.add.flag.push"))
	cmd.Flags().BoolVar(&allowDup, "allow-dup", false, i18n.T("cmd.pl.add.flag.allow_dup"))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("cmd.pl.add.flag.dry_run"))
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, i18n.T("cmd.pl.add.flag.yes"))
	return cmd
}

func parsePlAddArgs(cmd *cobra.Command, args []string, nowFlag bool) (playlistArg, trackProv, trackID string, err error) {
	if len(args) > 2 {
		return "", "", "", i18n.Errorf("pl.add.err.too_many_args")
	}
	interactive := isInteractive(cmd)
	if nowFlag {
		switch len(args) {
		case 2:
			return "", "", "", i18n.Errorf("pl.add.err.now_with_track")
		case 1:
			if p, ref, ok := strings.Cut(args[0], ":"); ok && isProviderID(p) && ref != "" {
				return "", "", "", i18n.Errorf("pl.add.err.now_with_track")
			}
			return args[0], "", "", nil
		default:
			if !interactive {
				return "", "", "", i18n.Errorf("pl.add.err.need_playlist")
			}
			return "", "", "", nil
		}
	}
	switch len(args) {
	case 0:
		return "", "", "", i18n.Errorf("pl.add.err.need_track")
	case 1:
		if p, _, ok := strings.Cut(args[0], ":"); ok && isProviderID(p) {
			prov, ref, err := splitPlAddTrackRef(args[0])
			if err != nil {
				return "", "", "", err
			}
			if !interactive {
				return "", "", "", i18n.Errorf("pl.add.err.need_playlist")
			}
			return "", prov, ref, nil
		}
		return "", "", "", i18n.Errorf("pl.add.err.need_track")
	default:
		prov, ref, err := splitPlAddTrackRef(args[1])
		if err != nil {
			return "", "", "", err
		}
		return args[0], prov, ref, nil
	}
}

func splitPlAddTrackRef(arg string) (prov, id string, err error) {
	prov, id, err = splitProviderRef(arg)
	if err != nil {
		return "", "", err
	}
	// 相容完整 Spotify track URI(spotify:track:<id>)與一般 <provider>:<id>。
	if prov == "spotify" && strings.HasPrefix(id, "track:") {
		if stripped := strings.TrimPrefix(id, "track:"); stripped != "" {
			id = stripped
		}
	}
	return prov, id, nil
}

func runPlAdd(cmd *cobra.Command, args []string, prov string, nowFlag, pushFlag, allowDup, dryRun, yes bool) error {
	if prov != "" && !isProviderID(prov) {
		return i18n.Errorf("pl.err.bad_provider", "ids", strings.Join(providerIDs, "|"), "value", strconv.Quote(prov))
	}
	playlistArg, trackProv, trackID, err := parsePlAddArgs(cmd, args, nowFlag)
	if err != nil {
		return err
	}

	ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
	var track provider.Track
	if nowFlag {
		trackProv, track, err = resolvePlAddNow(ctx, prov)
	} else {
		track, err = resolvePlAddTrackByID(ctx, trackProv, trackID)
	}
	if err != nil {
		return err
	}

	var deferred error
	applied, touched := 0, false
	added := false
	addedTitle, addedName, addedPID, addedPos := "", "", "", 0

	err = withCanonical(ctx, stderr, func(s *canonState) error {
		targetArg := playlistArg
		if targetArg == "" {
			var err error
			if targetArg, err = pickCanonicalPlaylist(s, i18n.T("pl.add.pick_title")); err != nil {
				return err
			}
		}
		pl, err := s.find(targetArg)
		if err != nil {
			return err
		}
		if pl == nil {
			return i18n.Errorf("pl.add.err.not_found", "arg", strconv.Quote(targetArg))
		}
		addedName = pl.Name

		syncProv := ""
		if !nowFlag && prov != "" {
			syncProv = prov
		}
		targets := []*canon.Playlist{pl}
		var (
			pf       *platforms
			pullRows [][]string
			blocked  []string
			lives    map[liveKey]*canon.Observed
		)
		if pushFlag && len(pl.Links) > 0 {
			pf = newPlatforms(ctx)
			pullRows, blocked, lives, err = observeAndDerive(ctx, s, targets, syncProv, stderr, pf)
			if err != nil {
				return err
			}
		}

		id := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged)
		cids, updated := canon.Observe(id, trackProv, s.tracks.Tracks, []provider.Track{track})
		cid := cids[0]
		s.absorb(updated)
		tr := s.tracks.Tracks[cid]

		if !allowDup && slices.ContainsFunc(pl.Items, func(it canon.Item) bool { return id.Redirect(it.CID) == cid }) {
			fmt.Fprintln(stderr, i18n.T("pl.add.already_present", "title", tr.Title, "name", pl.Name, "pid", pl.PID))
			return errSkipCommit
		}

		pos := len(pl.Items)
		if _, err := pl.Append(cid); err != nil {
			return err
		}
		addRow := []string{"add", trackProv, pl.Name, strconv.Itoa(pos), cid, track.ProviderID, tr.Title, strings.Join(tr.Artists, ", "), i18n.T("pl.add.reason.added"), "added_to_master"}

		if !pushFlag {
			rows := [][]string{append([]string{"add"}, addRow...)}
			if err := ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), syncHeader, rows, tableOpts(yes)...); err != nil {
				return err
			}
			if dryRun {
				return &PendingError{N: 1}
			}
			if !yes {
				if !bothTTY(cmd) {
					return &PendingError{N: 1}
				}
				ok, err := confirmWrite("pl.add.confirm", i18n.T("pl.add.confirm", "title", tr.Title, "name", pl.Name))
				if err != nil {
					return err
				}
				if !ok {
					return &PendingError{N: 1}
				}
			}
			added = true
			addedTitle, addedPID, addedPos = tr.Title, pl.PID, pos
			return nil
		}

		var (
			plans    []*pushPlan
			pushRows [][]string
			pblocked []string
			refused  []string
		)
		if len(pl.Links) > 0 {
			plans, pushRows, pblocked, refused, err = planPush(ctx, s, targets, syncProv, stderr, pf, lives, false)
			if err != nil {
				return err
			}
		}
		var rows [][]string
		for _, r := range pullRows {
			rows = append(rows, append([]string{"pull"}, r...))
		}
		rows = append(rows, append([]string{"add"}, addRow...))
		for _, r := range pushRows {
			rows = append(rows, append([]string{"push"}, r...))
		}
		if err := ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), syncHeader, rows, tableOpts(yes)...); err != nil {
			return err
		}
		if len(refused) > 0 {
			return &BlockedError{Msg: strings.Join(refused, i18n.T("sep.clause"))}
		}
		if blocked = append(blocked, pblocked...); len(blocked) > 0 {
			return &BlockedError{Msg: i18n.T("changeset.blocked", "reasons", strings.Join(blocked, i18n.T("sep.clause")))}
		}
		if len(pl.Links) > 0 {
			resolveHint(s, targets, syncProv, stderr)
		}
		ops := 0
		for _, p := range plans {
			ops += len(p.ops)
		}
		n := len(pullRows) + 1 + ops
		if dryRun {
			return &PendingError{N: n}
		}
		if !yes {
			if !bothTTY(cmd) {
				return &PendingError{N: n}
			}
			ok, err := confirmWrite("pl.add.confirm_push", i18n.T("pl.add.confirm_push", "count", n, "title", tr.Title, "name", pl.Name, "pulls", len(pullRows), "pushes", ops))
			if err != nil {
				return err
			}
			if !ok {
				return &PendingError{N: n}
			}
		}
		if len(plans) > 0 {
			applied, touched, deferred = applyPlans(ctx, s, plans, stderr)
			if applied > 0 {
				fmt.Fprintln(stderr, i18n.T("pl.add.pushed", "count", applied))
			}
		}
		added = true
		addedTitle, addedPID, addedPos = tr.Title, pl.PID, pos
		return nil
	})
	if err == nil && added {
		fmt.Fprintln(stderr, i18n.T("pl.add.done", "title", addedTitle, "name", addedName, "pid", addedPID, "pos", addedPos))
	}
	if pushFlag {
		nameArg := addedName
		if nameArg == "" {
			nameArg = playlistArg
		}
		qName := strconv.Quote(nameArg)
		return finishPush(err, applied, touched, deferred, i18n.T("pl.add.next", "name", qName), i18n.T("pl.add.next_after_half", "name", qName))
	}
	return err
}

func resolvePlAddTrackByID(ctx context.Context, trackProv, trackID string) (provider.Track, error) {
	p, err := newProvider(ctx, trackProv)
	if err != nil {
		return provider.Track{}, err
	}
	g, ok := p.(provider.TrackGetter)
	if !ok {
		return provider.Track{}, i18n.Errorf("pl.add.err.no_track_getter", "provider", trackProv)
	}
	tr, err := g.GetTrack(ctx, trackID)
	if err != nil {
		return provider.Track{}, i18n.Errorf("pl.add.err.track_lookup", "provider", trackProv, "id", trackID, "err", friendlyErr(trackProv, err))
	}
	if tr.ProviderID == "" {
		tr.ProviderID = trackID
	}
	return tr, nil
}

// resolvePlAddNow:查詢目前播放中的曲目。
// 有 --provider 釘該家;否則優先找正在 Playing 的平台,都沒在播再退回 default_provider(或其餘有暫停曲目的平台)。
func resolvePlAddNow(ctx context.Context, pin string) (string, provider.Track, error) {
	if pin != "" {
		p, err := newProvider(ctx, pin)
		if err != nil {
			if pin == "apple" {
				return "", provider.Track{}, i18n.Errorf("pl.add.err.apple_token_needed", "err", err)
			}
			return "", provider.Track{}, err
		}
		pc, err := asPlayback(p)
		if err != nil {
			return "", provider.Track{}, err
		}
		st, err := wikiState(ctx, pc)
		if err != nil {
			return "", provider.Track{}, friendlyErr(pin, err)
		}
		if st == nil || st.Track == nil {
			return "", provider.Track{}, i18n.Errorf("pl.add.err.nothing_playing_on", "provider", pin)
		}
		tr := *st.Track
		if pin == "apple" && tr.ProviderID == "" {
			tr, err = enrichAppleNowTrack(ctx, p, tr)
			if err != nil {
				return "", provider.Track{}, err
			}
		}
		return pin, tr, nil
	}

	def := loadDefaultProvider()
	var (
		fallbackID    string
		fallbackProv  provider.Provider
		fallbackTrack *provider.Track
		appleBuildErr error
	)
	seen := map[string]bool{}
	for _, id := range append([]string{def}, providerIDs...) {
		if seen[id] {
			continue
		}
		seen[id] = true
		p, err := newProvider(ctx, id)
		if err != nil {
			if id == "apple" {
				appleBuildErr = err
			}
			continue
		}
		pc, err := asPlayback(p)
		if err != nil {
			continue
		}
		st, err := wikiState(ctx, pc)
		if err != nil || st == nil || st.Track == nil {
			continue
		}
		if st.Playing {
			tr := *st.Track
			if id == "apple" && tr.ProviderID == "" {
				tr, err = enrichAppleNowTrack(ctx, p, tr)
				if err != nil {
					return "", provider.Track{}, err
				}
			}
			return id, tr, nil
		}
		if fallbackTrack == nil {
			cp := *st.Track
			fallbackID, fallbackProv, fallbackTrack = id, p, &cp
		}
	}
	if fallbackTrack != nil {
		tr := *fallbackTrack
		if fallbackID == "apple" && tr.ProviderID == "" {
			var err error
			tr, err = enrichAppleNowTrack(ctx, fallbackProv, tr)
			if err != nil {
				return "", provider.Track{}, err
			}
		}
		return fallbackID, tr, nil
	}
	if appleBuildErr != nil && def == "apple" {
		return "", provider.Track{}, i18n.Errorf("pl.add.err.apple_token_needed", "err", appleBuildErr)
	}
	return "", provider.Track{}, i18n.Errorf("pl.add.err.nothing_playing")
}

// enrichAppleNowTrack:macOS Apple Music 的 State() 僅有歌名/歌手/專輯/時長、無 catalog ProviderID;
// 透過已登入的 Apple catalog client 執行 Search(SearchSongs),比對歌名、歌手與時長 ±2s 補齊 catalog ProviderID 與 ISRC。
func enrichAppleNowTrack(ctx context.Context, p provider.Provider, want provider.Track) (provider.Track, error) {
	searcher, err := asSearcher(p)
	if err != nil {
		ap, aerr := newAppleProvider(ctx)
		if aerr != nil {
			return provider.Track{}, i18n.Errorf("pl.add.err.apple_token_needed", "err", aerr)
		}
		searcher = ap
	}
	artist := ""
	if len(want.Artists) > 0 {
		artist = strings.TrimSpace(want.Artists[0])
	}
	title := strings.TrimSpace(want.Title)
	if title == "" || artist == "" || want.DurationMS <= 0 {
		return provider.Track{}, i18n.Errorf("pl.add.err.apple_no_catalog_match", "title", want.Title, "artist", artist)
	}
	q := strings.TrimSpace(title + " " + artist)
	cands, err := searcher.Search(ctx, provider.Query{Text: q, Limit: 25})
	if err != nil {
		if errors.Is(err, provider.ErrAuthExpired) {
			return provider.Track{}, i18n.Errorf("pl.add.err.apple_token_needed", "err", friendlyErr("apple", err))
		}
		return provider.Track{}, friendlyErr("apple", err)
	}
	matched, ok := matchAppleCatalogTrack(want, cands)
	if !ok {
		return provider.Track{}, i18n.Errorf("pl.add.err.apple_no_catalog_match", "title", want.Title, "artist", artist)
	}
	return matched, nil
}

func matchAppleCatalogTrack(want provider.Track, cands []provider.Track) (provider.Track, bool) {
	if len(want.Artists) == 0 {
		return provider.Track{}, false
	}
	rawTitle := strings.TrimSpace(want.Title)
	rawArtist := strings.TrimSpace(want.Artists[0])
	normTitle := resolve.Norm(rawTitle)
	normArtist := resolve.Norm(rawArtist)
	normAlbum := resolve.Norm(want.Album)
	if (normTitle == "" && rawTitle == "") || (normArtist == "" && rawArtist == "") || want.DurationMS <= 0 {
		return provider.Track{}, false
	}

	var hits []provider.Track
	for _, c := range cands {
		if c.ProviderID == "" || c.DurationMS <= 0 || absPlAddDiff(c.DurationMS, want.DurationMS) > 2000 {
			continue
		}
		titleOK := (normTitle != "" && resolve.Norm(c.Title) == normTitle) || strings.EqualFold(strings.TrimSpace(c.Title), rawTitle)
		if !titleOK {
			continue
		}
		artistOK := false
		for _, ca := range c.Artists {
			if (normArtist != "" && resolve.Norm(ca) == normArtist) || strings.EqualFold(strings.TrimSpace(ca), rawArtist) {
				artistOK = true
				break
			}
		}
		if !artistOK {
			continue
		}
		hits = append(hits, c)
	}
	if len(hits) == 0 {
		return provider.Track{}, false
	}
	slices.SortStableFunc(hits, func(a, b provider.Track) int {
		if normAlbum != "" {
			sa, sb := resolve.Norm(a.Album) == normAlbum, resolve.Norm(b.Album) == normAlbum
			if sa != sb {
				if sa {
					return -1
				}
				return 1
			}
		}
		if c := cmp.Compare(absPlAddDiff(a.DurationMS, want.DurationMS), absPlAddDiff(b.DurationMS, want.DurationMS)); c != 0 {
			return c
		}
		return strings.Compare(a.ProviderID, b.ProviderID)
	})
	return hits[0], true
}

func absPlAddDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
