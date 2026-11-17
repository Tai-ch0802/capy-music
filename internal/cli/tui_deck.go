package cli

import (
	"context"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// 11 行借位抽屜(Quick Deck / 收進正本歌單 / 主題即時預覽):
//   - 常駐水豚(alive)時借用 capyBlockRows(11 行)固定空間,畫面總高度維持 15 行一列不變,零閃爍、零殘留;
//   - 定格後(!alive)比照斜線選單向上展開並由 menuHigh 守住高度,直到下次推進捲動區才縮回 4 行。
//   - 外觀嚴守純 ASCII (< 128):只用 > 、* 、[ ]、- 排版,絕不使用方框字元或 East Asian Ambiguous 符號。

type tuiDrawerMode int

const (
	tuiDrawerNone tuiDrawerMode = iota
	tuiDrawerDeck
	tuiDrawerCollect
	tuiDrawerTheme
)

const (
	tuiDeckTabPlaylists = 0
	tuiDeckTabRecent    = 1
	tuiDeckTabDevices   = 2

	tuiDeckMaxRows = 8 // 1 列標題 + 1 列過濾 + 最多 8 列項目 = 10 列 <= capyBlockRows(11)
)

type tuiDeckItemKind int

const (
	tuiDeckItemCanonPlaylist tuiDeckItemKind = iota
	tuiDeckItemPlatformPlaylist
	tuiDeckItemRecent
	tuiDeckItemDevice
	tuiDeckItemCollectTarget
	tuiDeckItemTheme
)

type tuiDeckItem struct {
	kind     tuiDeckItemKind
	label    string
	detail   string
	active   bool
	prov     string
	id       string
	recType  string
	canonPL  canon.Playlist
	canonTrk *canon.Tracks
	collect  string // pl add 使用的目標名稱或 pid
	themeID  string
}

type tuiDevicesMsg struct {
	deckGen int
	prov    string
	devices []provider.Device
	err     error
}

type tuiDrawerState struct {
	mode       tuiDrawerMode
	tab        int // Quick Deck 上次停的分頁(0..2)
	sel        int
	filter     string
	items      []tuiDeckItem
	devLoading bool
	devErr     string
	devGen     int
	themeOrig  ui.Theme
}

func (m tuiModel) drawerOpen() bool {
	return m.drawer.mode != tuiDrawerNone
}

func (m tuiModel) playTargetProvider() string {
	if m.provFlag != "" {
		return m.provFlag
	}
	if m.provID != "" && m.pcErr == nil {
		return m.provID
	}
	return loadDefaultProvider()
}

func (m tuiModel) openQuickDeck(tab int) (tuiModel, tea.Cmd) {
	m.typing, m.input = false, blurred(m.input)
	m.input.SetValue("")
	m.drawer.mode = tuiDrawerDeck
	m.drawer.tab = tab
	m.drawer.sel = 0
	m.drawer.filter = ""
	return m.reloadDeckTab()
}

func (m tuiModel) reloadDeckTab() (tuiModel, tea.Cmd) {
	m.drawer.sel = 0
	m.drawer.devLoading = false
	m.drawer.devErr = ""
	switch m.drawer.tab {
	case tuiDeckTabPlaylists:
		m.drawer.items = m.buildDeckPlaylists()
		return m, nil
	case tuiDeckTabRecent:
		m.drawer.items = m.buildDeckRecent()
		return m, nil
	case tuiDeckTabDevices:
		m.drawer.items = nil
		m.drawer.devLoading = true
		m.drawer.devGen++
		return m, m.loadDeckDevicesCmd(m.drawer.devGen)
	}
	return m, nil
}

func (m tuiModel) buildDeckPlaylists() []tuiDeckItem {
	var out []tuiDeckItem
	targetProv := m.playTargetProvider()
	if snap, ok := tuiLoadCanonSnapshot(); ok {
		for _, pl := range snap.Playlists {
			det := i18n.T("tui.deck.tracks", "count", len(pl.Items))
			if len(pl.Links) > 0 {
				var provs []string
				for p, id := range pl.Links {
					if id != "" {
						provs = append(provs, p)
					}
				}
				slices.Sort(provs)
				if len(provs) > 0 {
					det += " - " + strings.Join(provs, ", ")
				}
			}
			out = append(out, tuiDeckItem{
				kind:     tuiDeckItemCanonPlaylist,
				label:    "[canon] " + pl.Name,
				detail:   det,
				prov:     targetProv,
				id:       pl.PID,
				canonPL:  pl,
				canonTrk: snap.Tracks,
			})
		}
	}
	if tuiStoreAllowed() {
		cc := cache.Load()
		var provs []string
		if m.provFlag != "" {
			provs = []string{m.provFlag}
		} else {
			provs = append(provs, targetProv)
			for _, id := range providerIDs {
				if id != targetProv {
					provs = append(provs, id)
				}
			}
		}
		for _, p := range provs {
			for _, pl := range cc.Playlists[p] {
				det := ""
				if pl.Total >= 0 {
					det = i18n.T("tui.deck.tracks", "count", pl.Total)
				}
				out = append(out, tuiDeckItem{
					kind:   tuiDeckItemPlatformPlaylist,
					label:  "[" + p + "] " + pl.Name,
					detail: det,
					prov:   p,
					id:     pl.ID,
				})
			}
		}
	}
	return out
}

func (m tuiModel) buildDeckRecent() []tuiDeckItem {
	if !tuiStoreAllowed() {
		return nil
	}
	cc := cache.Load()
	var out []tuiDeckItem
	for _, r := range cc.Recent {
		if r.Type == cache.TypeQuery {
			continue
		}
		if m.provFlag != "" && r.Provider != m.provFlag {
			continue
		}
		out = append(out, tuiDeckItem{
			kind:    tuiDeckItemRecent,
			label:   "[" + r.Provider + "] " + r.Label,
			detail:  r.Detail,
			prov:    r.Provider,
			id:      r.ID,
			recType: r.Type,
		})
	}
	return out
}

func (m tuiModel) loadDeckDevicesCmd(deckGen int) tea.Cmd {
	prov := m.playTargetProvider()
	trk := m.trk
	ctx := m.ctx
	wait := tuiRoundWait
	return func() tea.Msg {
		if trk == nil || (trk.ctx != nil && trk.ctx.Err() != nil) {
			return tuiDevicesMsg{deckGen: deckGen, prov: prov}
		}
		pc, err := trk.playback(prov)
		if err != nil {
			return tuiDevicesMsg{deckGen: deckGen, prov: prov, err: err}
		}
		dctx, cancel := context.WithTimeout(provider.WithoutWait(ctx), wait)
		defer cancel()
		ds, err := pc.Devices(dctx)
		return tuiDevicesMsg{deckGen: deckGen, prov: prov, devices: ds, err: err}
	}
}

func (m tuiModel) applyDevicesMsg(msg tuiDevicesMsg) (tuiModel, tea.Cmd) {
	if m.drawer.mode != tuiDrawerDeck || m.drawer.tab != tuiDeckTabDevices || msg.deckGen != m.drawer.devGen {
		return m, nil
	}
	m.drawer.devLoading = false
	if msg.err != nil {
		m.drawer.devErr = friendlyErr(msg.prov, msg.err).Error()
		m.drawer.items = nil
		return m, nil
	}
	m.drawer.devErr = ""
	items := make([]tuiDeckItem, len(msg.devices))
	activeIdx := 0
	for i, d := range msg.devices {
		det := d.Type
		if d.VolumeKnown || d.VolumePct > 0 {
			vol := i18n.T("tui.status.volume", "pct", d.VolumePct)
			if det != "" {
				det += " - " + vol
			} else {
				det = vol
			}
		}
		items[i] = tuiDeckItem{
			kind:   tuiDeckItemDevice,
			label:  d.Name,
			detail: det,
			active: d.Active,
			prov:   msg.prov,
			id:     d.ID,
		}
		if d.Active {
			activeIdx = i
		}
	}
	m.drawer.items = items
	m.drawer.sel = activeIdx
	m.menuHigh = max(m.menuHigh, m.overlayRows())
	return m, nil
}

func (m tuiModel) openCollectDrawer() (tuiModel, tea.Cmd) {
	m.typing, m.input = false, blurred(m.input)
	m.input.SetValue("")
	m.drawer.mode = tuiDrawerCollect
	m.drawer.sel = 0
	m.drawer.filter = ""
	m.drawer.devLoading = false
	m.drawer.devErr = ""

	var items []tuiDeckItem
	if snap, ok := tuiLoadCanonSnapshot(); ok && len(snap.Playlists) > 0 {
		nameCount := map[string]int{}
		for _, pl := range snap.Playlists {
			nameCount[strings.ToLower(pl.Name)]++
		}
		var pinned *tuiDeckItem
		var rest []tuiDeckItem
		for _, pl := range snap.Playlists {
			arg := pl.Name
			if nameCount[strings.ToLower(pl.Name)] > 1 {
				arg = pl.PID
			}
			det := i18n.T("tui.deck.tracks", "count", len(pl.Items))
			it := tuiDeckItem{
				kind:    tuiDeckItemCollectTarget,
				label:   pl.Name,
				detail:  det,
				id:      pl.PID,
				collect: arg,
			}
			if m.lastCollectPID != "" && pl.PID == m.lastCollectPID {
				it.active = true
				it.detail = "[last] " + det
				cp := it
				pinned = &cp
			} else {
				rest = append(rest, it)
			}
		}
		if pinned != nil {
			items = append([]tuiDeckItem{*pinned}, rest...)
		} else {
			items = rest
		}
	}
	m.drawer.items = items
	return m, nil
}

func (m tuiModel) collectToLast() (tuiModel, tea.Cmd) {
	if m.lastCollectArg == "" {
		return m.openCollectDrawer()
	}
	return m.execCollectTarget(m.lastCollectPID, m.lastCollectArg)
}

func (m tuiModel) execCollectTarget(pid, targetArg string) (tuiModel, tea.Cmd) {
	m.drawer.mode = tuiDrawerNone
	if m.exe == "" {
		m.menuHigh = 0
		return m, m.println(tuiSeg{"✗ " + i18n.T("tui.err.no_executable"), m.theme.Mutedly})
	}
	m.lastCollectPID = pid
	m.lastCollectArg = targetArg
	args := []string{"pl", "add", targetArg, "--now", "--yes"}
	raw := "pl add " + tuiQuoteArg(targetArg) + " --now --yes"
	return m.startSubcommand(raw, args)
}

func tuiThemeDesc(name string) string {
	switch name {
	case "yuzu-onsen":
		return i18n.T("tui.theme.yuzu_onsen")
	case "vinyl-amber":
		return i18n.T("tui.theme.vinyl_amber")
	case "night-sakura":
		return i18n.T("tui.theme.night_sakura")
	case "daylight-ink":
		return i18n.T("tui.theme.daylight_ink")
	default:
		return i18n.T("tui.theme.geek_green")
	}
}

func (m tuiModel) openThemeDrawer() (tuiModel, tea.Cmd) {
	m.typing, m.input = false, blurred(m.input)
	m.input.SetValue("")
	m.drawer.mode = tuiDrawerTheme
	m.drawer.filter = ""
	m.drawer.devLoading = false
	m.drawer.devErr = ""
	m.drawer.themeOrig = m.theme

	names := ui.ThemeNames()
	items := make([]tuiDeckItem, len(names))
	sel := 0
	curName := m.themeName
	if curName == "" {
		curName = ui.DefaultThemeName
	}
	for i, name := range names {
		isCur := strings.EqualFold(name, curName)
		if isCur {
			sel = i
		}
		items[i] = tuiDeckItem{
			kind:    tuiDeckItemTheme,
			label:   name,
			detail:  tuiThemeDesc(name),
			active:  isCur,
			themeID: name,
		}
	}
	m.drawer.items = items
	m.drawer.sel = sel
	return m, nil
}

func (m tuiModel) filteredDrawerItems() []tuiDeckItem {
	q := strings.ToLower(strings.TrimSpace(m.drawer.filter))
	if q == "" || m.drawer.mode == tuiDrawerTheme {
		return m.drawer.items
	}
	var out []tuiDeckItem
	for _, it := range m.drawer.items {
		if strings.Contains(strings.ToLower(it.label+" "+it.detail), q) {
			out = append(out, it)
		}
	}
	return out
}

func (m tuiModel) onDrawerKey(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	key := msg.String()
	switch key {
	case "ctrl+c":
		if m.drawer.mode == tuiDrawerTheme {
			ui.DefaultTheme = m.drawer.themeOrig
			m.setTheme(m.drawer.themeOrig)
		}
		m.ctrlC = true
		return m, tea.Quit
	case "esc":
		if m.drawer.mode != tuiDrawerTheme && m.drawer.filter != "" {
			m.drawer.filter = ""
			m.drawer.sel = 0
			return m, nil
		}
		if m.drawer.mode == tuiDrawerTheme {
			ui.DefaultTheme = m.drawer.themeOrig
			m.setTheme(m.drawer.themeOrig)
		}
		m.drawer.mode = tuiDrawerNone
		return m, nil
	case "tab", "shift+tab":
		if m.drawer.mode == tuiDrawerDeck {
			if key == "shift+tab" {
				m.drawer.tab = (m.drawer.tab + 2) % 3
			} else {
				m.drawer.tab = (m.drawer.tab + 1) % 3
			}
			m.drawer.filter = ""
			return m.reloadDeckTab()
		}
		return m, nil
	case "ctrl+r":
		if m.drawer.mode == tuiDrawerDeck {
			return m.reloadDeckTab()
		}
		return m, nil
	case "up", "down":
		items := m.filteredDrawerItems()
		if len(items) == 0 {
			return m, nil
		}
		d := -1
		if key == "down" {
			d = 1
		}
		m.drawer.sel = min(len(items)-1, max(0, m.drawer.sel+d))
		if m.drawer.mode == tuiDrawerTheme {
			chosen := items[m.drawer.sel].themeID
			if ui.ApplyTheme(chosen) {
				m.setTheme(ui.DefaultTheme)
			}
		}
		return m, nil
	case "backspace":
		if m.drawer.mode != tuiDrawerTheme && m.drawer.filter != "" {
			runes := []rune(m.drawer.filter)
			m.drawer.filter = string(runes[:len(runes)-1])
			items := m.filteredDrawerItems()
			m.drawer.sel = min(m.drawer.sel, max(0, len(items)-1))
		}
		return m, nil
	case "enter":
		items := m.filteredDrawerItems()
		if len(items) == 0 {
			return m, nil
		}
		chosen := items[min(m.drawer.sel, len(items)-1)]
		return m.executeDeckItem(chosen)
	}

	if m.drawer.mode != tuiDrawerTheme && msg.Text != "" {
		var clean strings.Builder
		for _, r := range msg.Text {
			if r >= 32 && r != 127 {
				clean.WriteRune(r)
			}
		}
		if clean.Len() > 0 {
			m.drawer.filter += clean.String()
			items := m.filteredDrawerItems()
			m.drawer.sel = min(m.drawer.sel, max(0, len(items)-1))
		}
	}
	return m, nil
}

func (m tuiModel) executeDeckItem(it tuiDeckItem) (tuiModel, tea.Cmd) {
	switch it.kind {
	case tuiDeckItemCanonPlaylist:
		m.drawer.mode = tuiDrawerNone
		prov := it.prov
		var ids []string
		if it.canonTrk != nil {
			idx := canon.NewIdentity(it.canonTrk.Tracks, it.canonTrk.Merged)
			// 嚴格依 playlist_items 原序取對應曲目 ID(決策 38:絕不排序或重排正本)。
			for _, item := range it.canonPL.Items {
				cid := idx.Redirect(item.CID)
				if tr, ok := it.canonTrk.Tracks[cid]; ok {
					if mp, ok := tr.Mappings[prov]; ok && mp.ID != "" {
						ids = append(ids, mp.ID)
					}
				}
			}
		}
		if len(ids) > 0 {
			m = m.newChain()
			return m, m.controlOn(prov, func(pc provider.PlaybackController, ctx context.Context) error {
				return tuiIgnoreOpenedErr(pc.Play(ctx, provider.PlayRequest{TrackIDs: ids}))
			}, false)
		}
		if linkID := it.canonPL.Links[prov]; linkID != "" && prov == "spotify" {
			m = m.newChain()
			return m, m.controlOn(prov, func(pc provider.PlaybackController, ctx context.Context) error {
				return tuiIgnoreOpenedErr(pc.Play(ctx, provider.PlayRequest{PlaylistID: linkID}))
			}, false)
		}
		m.menuHigh = 0
		return m, m.println(tuiSeg{"! " + i18n.T("tui.deck.no_mapped_tracks", "platform", prov, "name", it.canonPL.Name), m.theme.Mutedly})

	case tuiDeckItemPlatformPlaylist:
		m.drawer.mode = tuiDrawerNone
		if tuiStoreAllowed() {
			rememberRecent(it.prov, candidate{Type: cache.TypePlaylist, ID: it.id, Label: strings.TrimPrefix(it.label, "["+it.prov+"] "), Detail: it.detail})
		}
		m = m.newChain()
		return m, m.controlOn(it.prov, func(pc provider.PlaybackController, ctx context.Context) error {
			return tuiIgnoreOpenedErr(pc.Play(ctx, provider.PlayRequest{PlaylistID: it.id}))
		}, false)

	case tuiDeckItemRecent:
		m.drawer.mode = tuiDrawerNone
		switch it.recType {
		case cache.TypePlaylist:
			m = m.newChain()
			return m, m.controlOn(it.prov, func(pc provider.PlaybackController, ctx context.Context) error {
				return tuiIgnoreOpenedErr(pc.Play(ctx, provider.PlayRequest{PlaylistID: it.id}))
			}, false)
		case cache.TypeArtist:
			name := strings.TrimPrefix(it.label, "["+it.prov+"] ")
			m = m.newChain()
			return m, m.controlOn(it.prov, func(pc provider.PlaybackController, ctx context.Context) error {
				p, ok := pc.(provider.Provider)
				if !ok {
					var err error
					p, err = newProvider(ctx, it.prov)
					if err != nil {
						return err
					}
				}
				req, _, err := playRequestFor(ctx, p, candidate{Type: cache.TypeArtist, ID: it.id, Label: name})
				if err != nil {
					return err
				}
				return tuiIgnoreOpenedErr(pc.Play(ctx, req))
			}, false)
		default:
			m = m.newChain()
			return m, m.controlOn(it.prov, func(pc provider.PlaybackController, ctx context.Context) error {
				return tuiIgnoreOpenedErr(pc.Play(ctx, provider.PlayRequest{TrackIDs: []string{it.id}}))
			}, false)
		}

	case tuiDeckItemDevice:
		m.drawer.mode = tuiDrawerNone
		m = m.newChain()
		if m.st != nil {
			m.saveOptimistic()
			cp := clonePlaybackState(m.st)
			cp.Device.ID = it.id
			cp.Device.Name = it.label
			cp.Playing = true
			m.st = cp
		}
		return m, m.controlOn(it.prov, func(pc provider.PlaybackController, ctx context.Context) error {
			dt, ok := pc.(provider.DeviceTransferrer)
			if !ok {
				return provider.ErrNotSupported
			}
			return dt.Transfer(ctx, it.id, true)
		}, false)

	case tuiDeckItemCollectTarget:
		return m.execCollectTarget(it.id, it.collect)

	case tuiDeckItemTheme:
		m.drawer.mode = tuiDrawerNone
		if ui.ApplyTheme(it.themeID) {
			m.setTheme(ui.DefaultTheme)
			m.themeName = it.themeID
		}
		if tuiStoreAllowed() {
			if cfg, err := config.Load(); err == nil {
				cfg.Theme = it.themeID
				if err := config.Save(cfg); err != nil {
					pr := m.printErr(err)
					return m, pr
				}
			}
		}
		m.menuHigh = 0
		return m, m.println(tuiSeg{"* " + i18n.T("tui.theme.saved", "theme", it.themeID), m.theme.Mutedly})
	}
	m.drawer.mode = tuiDrawerNone
	return m, nil
}

func (m tuiModel) drawerView(w int) []string {
	t := m.theme
	var lines []string

	switch m.drawer.mode {
	case tuiDrawerDeck:
		tabs := []string{
			i18n.T("tui.deck.tab.playlists"),
			i18n.T("tui.deck.tab.recent"),
			i18n.T("tui.deck.tab.devices"),
		}
		var segs []tuiSeg
		segs = append(segs, tuiSeg{"  ", nil})
		for i, name := range tabs {
			if i > 0 {
				segs = append(segs, tuiSeg{"  ", nil})
			}
			if i == m.drawer.tab {
				segs = append(segs, tuiSeg{"[" + name + "]", t.Strong})
			} else {
				segs = append(segs, tuiSeg{" " + name + " ", t.Mutedly})
			}
		}
		lines = append(lines, tuiJoin(w, segs...))

	case tuiDrawerCollect:
		lines = append(lines, tuiJoin(w, tuiSeg{"  " + i18n.T("tui.deck.collect.title"), t.Strong}))

	case tuiDrawerTheme:
		lines = append(lines, tuiJoin(w, tuiSeg{"  " + i18n.T("tui.deck.theme.title"), t.Strong}))
	}

	if m.drawer.mode != tuiDrawerTheme {
		if m.drawer.filter != "" {
			lines = append(lines, tuiJoin(w, tuiSeg{"  " + i18n.T("tui.deck.filter", "query", m.drawer.filter), t.Accented}))
		} else {
			lines = append(lines, tuiJoin(w, tuiSeg{"  " + i18n.T("tui.deck.filter_empty"), t.Mutedly}))
		}
	}

	if m.drawer.mode == tuiDrawerDeck && m.drawer.tab == tuiDeckTabDevices {
		if m.drawer.devLoading {
			lines = append(lines, tuiJoin(w, tuiSeg{"  " + i18n.T("tui.deck.devices.loading"), t.Mutedly}))
			return lines
		}
		if m.drawer.devErr != "" {
			lines = append(lines, tuiJoin(w, tuiSeg{"  ! " + m.drawer.devErr, t.Mutedly}))
			return lines
		}
	}

	items := m.filteredDrawerItems()
	if len(items) == 0 {
		empty := i18n.T("tui.deck.no_match")
		if m.drawer.filter == "" {
			switch {
			case m.drawer.mode == tuiDrawerCollect:
				empty = i18n.T("tui.deck.collect.empty")
			case m.drawer.mode == tuiDrawerDeck && m.drawer.tab == tuiDeckTabPlaylists:
				empty = i18n.T("tui.deck.playlists.empty")
			case m.drawer.mode == tuiDrawerDeck && m.drawer.tab == tuiDeckTabRecent:
				empty = i18n.T("tui.deck.recent.empty")
			case m.drawer.mode == tuiDrawerDeck && m.drawer.tab == tuiDeckTabDevices:
				empty = i18n.T("tui.deck.devices.empty")
			}
		}
		lines = append(lines, tuiJoin(w, tuiSeg{"  " + empty, t.Mutedly}))
		return lines
	}

	sel := min(max(0, m.drawer.sel), len(items)-1)
	lo, hi := tuiDeckWindow(len(items), sel)
	labelW := 0
	for i := lo; i < hi; i++ {
		labelW = max(labelW, tuiWidth(items[i].label))
	}
	labelW = min(labelW, max(12, w/2))

	for i := lo; i < hi; i++ {
		it := items[i]
		cursor := "  "
		labelStyle := t.Mutedly
		if i == sel {
			cursor = "> "
			labelStyle = t.Strong
		}
		activeMark := "  "
		if it.active {
			activeMark = "* "
		}
		clippedLabel := tuiClip(it.label, labelW)
		var segs []tuiSeg
		segs = append(segs,
			tuiSeg{cursor, t.Accented},
			tuiSeg{activeMark, t.Accented},
			tuiSeg{clippedLabel, labelStyle},
		)
		if it.detail != "" {
			pad := strings.Repeat(" ", max(1, labelW-tuiWidth(clippedLabel)+2))
			segs = append(segs, tuiSeg{pad + it.detail, t.Mutedly})
		}
		lines = append(lines, tuiJoin(w, segs...))
	}
	return lines
}

func tuiDeckWindow(n, sel int) (lo, hi int) {
	if n <= tuiDeckMaxRows {
		return 0, n
	}
	lo = max(0, min(sel-tuiDeckMaxRows/2, n-tuiDeckMaxRows))
	return lo, lo + tuiDeckMaxRows
}
