package cli

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/resolve"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// pl link --merge(2026-10-01,計畫 docs/superpowers/plans/2026-10-01-sync-page-redesign.md §3.4、附錄 C 決策 62):把已經存在的正本
// 連到一份已經有歌的平台清單 L。一個 withCanonical、一張表、一次確認(形狀同 pl dedup 的正本路徑):
//  1. 找對應:正本的歌先跟 L 自己的曲目一對一比(mergeMatch,不打網路),比不上的再搜這個平台的目錄(planResolve);沒把握的
//     終端機先問要不要逐首決定(同 migrate)。mapping 只寫進這次的 canonState。
//  2. 接進正本:L 裡正本沒有的照 L 的順序接在正本尾端,同一首只接第一份(appendUnseen,同 migrate);正本原有的一首都不動。
//  3. 把 L 排成正本的樣子:剛讀到的 L 當暫時 base 交給 planPushWith(不然前提一、二會把它擋下),只對這一個平台。
//     這是決策 38 唯一的例外:只涵蓋這一張使用者確認過的表;之後 base = 寫完的 L,任何同步都不會因為合併而重排。
//
// 確認之前(--dry-run、取消、非 TTY 沒 --yes、超過刪除閾值)Drive 與平台都零寫入、不留連結。確認後照 pl sync 的 push 半邊:
// 先寫平台、再 COMMIT Drive(mapping、正本、Links、base 一起)。
func runLinkMerge(cmd *cobra.Command, args []string, dryRun, yes, force bool) error {
	if len(args) != 2 {
		return i18n.Errorf("link.err.merge_args")
	}
	ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
	prov, ref, err := splitProviderRef(args[1])
	if err != nil {
		return err
	}
	p, err := newProvider(ctx, prov)
	if err != nil {
		return err
	}
	r, err := asPlaylistReader(p)
	if err != nil {
		return err
	}
	if _, err := asPlaylistWriter(p); err != nil {
		return err
	}
	id, err := resolvePlaylistID(ctx, r, prov, ref)
	if err != nil {
		return err
	}
	refs, err := r.ListPlaylists(ctx)
	if err != nil {
		return friendlyErr(prov, err)
	}
	i := slices.IndexFunc(refs, func(x provider.PlaylistRef) bool { return x.ID == id })
	if i < 0 { // 同 pl link:不在自己列表裡的,pull 會當成已刪除
		return i18n.Errorf("link.err.not_listed", "platform", prov, "id", id)
	}
	live := refs[i]
	tracks, err := r.GetPlaylistItems(ctx, id)
	switch {
	case errors.Is(err, provider.ErrRestricted):
		return i18n.Errorf("link.err.unreadable", "platform", prov, "id", id)
	case errors.Is(err, provider.ErrNotFound):
		tracks = nil // 有列出卻 404 = 空清單(Apple 的 library 端點就這樣回)
	case err != nil:
		return friendlyErr(prov, err)
	}
	if live.Unwritable != "" {
		return i18n.Errorf("link.err.merge_unwritable", "platform", prov, "id", id, "reason", live.Unwritable)
	}
	var local []string
	for _, t := range tracks {
		if t.Unpushable {
			local = append(local, t.Title)
		}
	}
	if len(local) > 0 { // 排成正本的樣子要整份重寫,加不回去的曲目會被拿掉;之後的同步也永遠推不過去(push.refuse.local_files)
		return i18n.Errorf("link.err.merge_unpushable", "platform", prov, "id", id, "count", len(local), "titles", strings.Join(local, i18n.T("sep.list")))
	}

	var deferred error
	applied, touched := 0, false
	var done, pid string
	err = withCanonical(ctx, stderr, func(s *canonState) error {
		pl, err := s.find(args[0])
		if err != nil {
			return err
		}
		if pl == nil { // 只接到既有的正本:不像一般 link 順手建
			return i18n.Errorf("link.err.merge_no_master", "arg", strconv.Quote(args[0]))
		}
		pid = pl.PID
		if cur := pl.Links[prov]; cur != "" { // 不接管任何連結(別台裝置、別的帳號的也一樣)
			return i18n.Errorf("link.err.merge_linked", "name", pl.Name, "pid", pl.PID, "platform", prov, "id", cur)
		}
		for _, other := range slices.Sorted(maps.Keys(s.playlists)) {
			if o := s.playlists[other]; o.Links[prov] == id {
				return i18n.Errorf("link.err.taken", "platform", prov, "id", id, "name", o.Name, "pid", o.PID)
			}
		}
		pl.Links[prov] = id // 只在記憶體:確認之前 fn 一律回錯,不會 COMMIT
		pl.UpdatedAt = canon.Now().Unix()
		targets := []*canon.Playlist{pl}

		// 1. 找對應:先跟 L 自己的曲目比,再搜目錄(只搜 L 裡認不出來的正本歌)
		matched, taken, inLive := mergeMatch(s, pl, prov, tracks)
		applyMappings(s, matched)
		probe := *pl // L 裡已經認得的、在 L 裡找到候選卻要人裁決的,都不再搜目錄
		probe.Items = slices.DeleteFunc(slices.Clone(pl.Items), func(it canon.Item) bool {
			return inLive[it.CID] || slices.ContainsFunc(taken, func(r resolveItem) bool { return r.cid == it.CID })
		})
		items, err := planResolve(ctx, s, []*canon.Playlist{&probe}, prov, stderr)
		if err != nil {
			return err
		}
		items = append(taken, items...)
		mapped := applyMappings(s, items)
		inL := map[string]bool{}
		for _, t := range tracks {
			inL[t.ProviderID] = true
		}
		var rows [][]string
		resolveRow := func(it resolveItem) {
			rows = append(rows, []string{"resolve", "map", prov, pl.Name, "", it.cid, it.cand.ProviderID, it.track.Title, strings.Join(it.track.Artists, ", "),
				i18n.T("link.merge.reason.matched", "score", it.score), it.code})
		}
		for _, it := range matched {
			resolveRow(it)
		}
		queued := 0
		for _, it := range items {
			switch {
			case it.action != "map":
				queued++
			case inL[it.cand.ProviderID]: // 目錄搜尋對到的剛好是 L 裡的那首:一樣是「認成同一首」,列出來
				resolveRow(it)
			}
		}
		if len(matched)+mapped > 0 || queued > 0 {
			fmt.Fprintln(stderr, i18n.T("migrate.resolve_summary", "mapped", len(matched)+mapped, "platform", prov, "queued", queued))
		}
		if queued > 0 && !yes && !dryRun && bothTTY(cmd) { // 同 migrate:沒決定的當成不同的歌接進來
			ok, err := confirmWrite("migrate.confirm.review", i18n.T("migrate.confirm.review", "count", queued, "platform", prov, "name_arg", strconv.Quote(pl.Name)))
			if err != nil {
				return err
			}
			if ok {
				n, err := reviewLoop(ctx, s, items, false, stderr)
				if errors.Is(err, huh.ErrUserAborted) { // 取消 = 整輪不寫入、不連上
					fmt.Fprintln(stderr, i18n.T("migrate.review.cancelled"))
					return &PendingError{N: len(rows) + queued}
				}
				if err != nil {
					return err
				}
				fmt.Fprintln(stderr, i18n.T("migrate.reviewed", "count", n))
			}
		}

		// 2. 接進正本
		lcid, added, err := appendUnseen(s, pl, prov, tracks)
		if err != nil {
			return err
		}
		for _, a := range added {
			rows = append(rows, []string{"pull", "add", prov, pl.Name, strconv.Itoa(a.pos), a.cid, a.track.ProviderID, a.track.Title, strings.Join(a.track.Artists, ", "),
				i18n.T("canon.reason.added_on_platform"), "added_on_platform"})
		}
		first := map[string]int{}
		for i, cid := range lcid { // L 自己多出來的同一首:不接進正本;pos 是它在 L 裡的位置。正本的份數比較少時 push 半邊才會多一列 remove,拿掉哪一份由 push 的配對決定
			j, dup := first[cid]
			if !dup {
				first[cid] = i
				continue
			}
			t := tracks[i]
			reason, code := i18n.T("dedup.reason.dup_id", "pos", j), "dup_id"
			if tracks[j].ProviderID != t.ProviderID {
				isrc := canon.NormalizeISRC(t.ISRC)
				if isrc == "" { // ponytail: 不同 id、沒有 ISRC 卻是同一首 = 人工合併過的(決策 21);代碼只有兩種,說法借 cid 頂著
					isrc = cid
				}
				reason, code = i18n.T("dedup.reason.dup_isrc", "pos", j, "isrc", isrc), "dup_isrc"
			}
			rows = append(rows, []string{"pull", "skip", prov, pl.Name, strconv.Itoa(i), cid, t.ProviderID, t.Title, strings.Join(t.Artists, ", "), reason, code})
		}

		// 3. 把 L 排成正本的樣子:剛讀到的 L 當暫時 base(只在記憶體)、重用讀過的 L
		snap := canon.Snapshot{ID: id, Name: live.Name, Items: idsOf(tracks), CIDs: lcid}
		merged := mergedBase(s)
		if merged[pl.PID] == nil {
			merged[pl.PID] = map[string]canon.Base{}
		}
		merged[pl.PID][prov] = canon.Base{Snapshot: snap}
		lives := map[liveKey]*canon.Observed{{pl.PID, prov}: {ID: id, Name: live.Name, Tracks: tracks}}
		pf := newPlatforms(ctx)
		pf.provs[prov], pf.readers[prov] = p, r
		pf.listed[prov] = map[string]provider.PlaylistRef{id: live}
		plans, pushRows, blocked, refused, err := planPushWith(ctx, s, merged, targets, prov, stderr, pf, lives, true)
		if err != nil {
			return err
		}
		for _, row := range pushRows {
			rows = append(rows, append([]string{"push"}, row...))
		}

		// 4. 一張表、一次確認
		if len(rows) > 0 {
			if err := ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), syncHeader, rows, tableOpts(yes)...); err != nil {
				return err
			}
		}
		if len(refused) > 0 { // 前置檢查已經擋掉會被 refused 的情況;留著免得哪條漏了而靜靜寫入
			return &BlockedError{Msg: strings.Join(refused, i18n.T("sep.clause"))}
		}
		if len(blocked) > 0 && !force {
			return &BlockedError{Msg: i18n.T("changeset.blocked", "reasons", strings.Join(blocked, i18n.T("sep.clause")))}
		}
		resolveHint(s, targets, "", stderr)
		n := len(added) // 變更 = 認成同一首(要寫的 mapping)+ 接進正本的 + 對 L 的 op;skip 列不算
		for _, row := range rows {
			if row[0] == "resolve" {
				n++
			}
		}
		for _, pp := range plans {
			n += len(pp.ops)
		}
		if n == 0 { // 0 筆照樣要確認才連上:沒有「不問就連」的路
			fmt.Fprintln(stderr, i18n.T("changeset.no_changes"))
		}
		if dryRun {
			return &PendingError{N: n}
		}
		if !yes {
			if !bothTTY(cmd) {
				return &PendingError{N: n}
			}
			ok, err := confirmWrite("link.merge.confirm", i18n.T("link.merge.confirm", "count", n, "platform", prov, "id", id, "name", pl.Name))
			if err != nil {
				return err
			}
			if !ok {
				return &PendingError{N: n}
			}
		}
		s.mine().SetBase(pl.PID, prov, snap) // 0 筆時平台就是這個樣子;有寫入時 apply 換成寫完重讀的
		applied, touched, deferred = applyPlans(ctx, s, plans, stderr)
		if !touched && deferred != nil { // 平台一筆都沒寫(確認期間變了、第一個請求就失敗):整輪不寫、不連上
			var blk *BlockedError
			if errors.As(deferred, &blk) {
				return &BlockedError{Msg: i18n.T("link.merge.stale", "platform", prov, "id", id)}
			}
			return i18n.Errorf("link.err.merge_not_written", "err", deferred.Error(), "platform", prov)
		}
		if applied > 0 {
			fmt.Fprintln(stderr, i18n.T("push.done", "count", applied))
		}
		done = i18n.T("link.merge.done", "platform", prov, "id", id, "name", pl.Name, "pid", pl.PID, "added", len(added), "count", applied, "name_arg", strconv.Quote(pl.Name))
		return nil
	})
	switch {
	case err == nil && deferred != nil: // 平台寫到一半:連結與 base(實際狀態)已經 COMMIT,下一次 sync 接著做完
		return i18n.Errorf("link.err.merge_partial", "err", deferred.Error(), "platform", prov, "name_arg", strconv.Quote(args[0]))
	case err == nil:
		fmt.Fprintln(stderr, done)
		return nil
	case touched && deferred != nil: // 平台改了一部分、Drive 也沒寫成:連結沒留下,重跑 --merge 從那份清單現在的樣子算
		return i18n.Errorf("link.err.merge_half_and_drive", "err", deferred.Error(), "drive", driveFailure(err), "platform", prov, "pid", pid, "id", id)
	}
	next := i18n.T("link.merge.next", "pid", pid, "platform", prov, "id", id)
	return finishPush(err, applied, touched, nil, next, next)
}

// mergeMatch:第 1 步的前半(計畫 §3.4),不打網路。比的是「正本有、L 裡依身分規則認不出來、這個平台還沒有 mapping」的歌
// (釘成不可得的不算),對 L 裡認不出是正本哪首的曲目。ISRC 命中 95 分、其餘 RankFuzzy,≥85 才算。一對一用貪婪指派:所有
// (正本歌, L 曲目)配對依 ISRC 命中優先、分數由高到低、同分取正本裡先出現的、再同分取 L 裡先出現的,兩邊都還沒被指派才成立——
// 比輸的正本歌改試它下一個還沒被佔走的 L 曲目(#137 review),都沒有才落到目錄搜尋;不然目錄回另一個版本時,L 自己那一支會被
// 當成新歌接進來。這樣 YouTube 同一首有音訊版與 MV 兩個 id 時,認的也是 L 裡那一支。
// 一首正本歌的最佳候選(它所有 ≥85 候選裡排第一的)已經屬於別的既有曲目(例如另一份正本從這個平台觀測過它)時不自動對應:要合併
// 兩首,決策 21 只由人決定——列成一筆 review(candidate_taken)交給逐首決定,不參與指派、也不再搜目錄(不然目錄先排到另一個版本
// 就會自動對上,L 同時留兩支)。屬於別首的 L 曲目也不指派給任何正本歌。
// 回傳 map 列(依正本順序,還沒寫入)、要人裁決的 review 列,與 L 裡本來就認得的 cid。
func mergeMatch(s *canonState, pl *canon.Playlist, prov string, live []provider.Track) (matched, taken []resolveItem, inLive map[string]bool) {
	id := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged)
	master := map[string]bool{}
	for _, it := range pl.Items {
		master[id.Redirect(it.CID)] = true
	}
	inLive = map[string]bool{}
	var cands []provider.Track
	lpos := map[string]int{} // L 曲目的 provider id → 它在候選裡的順序(= L 的順序)
	for _, t := range live {
		cid := id.Resolve(prov, t.ProviderID, t.ISRC)
		inLive[cid] = true
		if _, dup := lpos[t.ProviderID]; master[cid] || dup {
			continue
		}
		lpos[t.ProviderID] = len(cands)
		cands = append(cands, t)
	}
	type pair struct {
		m, l, tier int // 正本歌的順序、L 曲目的順序;tier 1 = ISRC 命中,先於所有模糊比對
		item       resolveItem
	}
	order := func(a, b pair) int {
		return cmp.Or(cmp.Compare(b.tier, a.tier), cmp.Compare(b.item.score, a.item.score), cmp.Compare(a.m, b.m), cmp.Compare(a.l, b.l))
	}
	var pairs []pair
	var songs []string // 參與比對的正本歌(cid),依正本順序
	done := map[string]bool{}
	for _, it := range pl.Items {
		cid := id.Redirect(it.CID)
		tr, ok := s.tracks.Tracks[cid]
		if !ok || done[cid] || inLive[cid] {
			continue
		}
		done[cid] = true
		if m, has := tr.Mappings[prov]; has && (m.ID != "" || m.Pinned) {
			continue
		}
		mi := len(songs)
		songs = append(songs, cid)
		add := func(mine []pair, t provider.Track, tier, score int, source, code string) []pair {
			return append(mine, pair{mi, lpos[t.ProviderID], tier, resolveItem{action: "map", cid: cid, prov: prov, track: tr, cand: &t, score: score, source: source, code: code}})
		}
		var mine []pair
		if pick, ok := resolve.PickISRC(tr, cands); ok {
			mine = add(mine, pick, 1, isrcConfidence, canon.SourceISRC, "isrc")
		}
		for _, r := range resolve.RankFuzzy(tr, cands) {
			if r.Score >= autoThreshold {
				mine = add(mine, r.Track, 0, r.Score, canon.SourceFuzzy, "fuzzy")
			}
		}
		if len(mine) == 0 {
			continue
		}
		slices.SortStableFunc(mine, order)
		if best := mine[0].item; ownedBy(s, id, cid, prov, *best.cand) != "" {
			best.action = "review"
			best.reason, best.code = i18n.T("resolve.reason.candidate_taken", "candidate", describe(*best.cand), "cid", ownedBy(s, id, cid, prov, *best.cand)), "candidate_taken"
			taken = append(taken, best)
			continue
		}
		for _, p := range mine {
			if ownedBy(s, id, cid, prov, *p.item.cand) == "" {
				pairs = append(pairs, p)
			}
		}
	}
	slices.SortStableFunc(pairs, order)
	won := map[int]resolveItem{}
	usedL := map[int]bool{}
	for _, p := range pairs {
		if _, ok := won[p.m]; ok || usedL[p.l] {
			continue
		}
		won[p.m], usedL[p.l] = p.item, true
	}
	for mi := range songs {
		if it, ok := won[mi]; ok {
			matched = append(matched, it)
		}
	}
	return matched, taken, inLive
}
