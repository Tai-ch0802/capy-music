package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// P5 T4:pl push(spec §6.5.2、附錄 C 決策 28):canonical → 平台。形狀與 exit code 鏡像 pl pull;對齊鍵是 cid(canon.PushPlan);
// 兩個前提(本裝置 pull 過、平台沒有未 pull 的變更)擋住「把平台刪光」與「蓋掉使用者剛在平台改的」。

// platforms:每個 provider 只建一次 client、只列一次清單;pull 與 push 共用。
type platforms struct {
	ctx     context.Context
	provs   map[string]provider.Provider
	readers map[string]provider.PlaylistReader
	listed  map[string]map[string]provider.PlaylistRef
}

func newPlatforms(ctx context.Context) *platforms {
	return &platforms{ctx: ctx, provs: map[string]provider.Provider{}, readers: map[string]provider.PlaylistReader{}, listed: map[string]map[string]provider.PlaylistRef{}}
}

func (c *platforms) provider(prov string) (provider.Provider, error) {
	if p, ok := c.provs[prov]; ok {
		return p, nil
	}
	p, err := newProvider(c.ctx, prov)
	if err != nil {
		return nil, err
	}
	c.provs[prov] = p
	return p, nil
}

// reader:PlaylistReader 與 ListPlaylists 的結果(id → ref);gone 的判準就是「不在這張表裡」。
func (c *platforms) reader(prov string) (provider.PlaylistReader, map[string]provider.PlaylistRef, error) {
	if r, ok := c.readers[prov]; ok {
		return r, c.listed[prov], nil
	}
	p, err := c.provider(prov)
	if err != nil {
		return nil, nil, err
	}
	r, err := asPlaylistReader(p)
	if err != nil {
		return nil, nil, err
	}
	refs, err := r.ListPlaylists(c.ctx)
	if err != nil {
		return nil, nil, friendlyErr(prov, err)
	}
	m := map[string]provider.PlaylistRef{}
	for _, ref := range refs {
		m[ref.ID] = ref
	}
	c.readers[prov], c.listed[prov] = r, m
	return r, m, nil
}

// mergedBase:所有裝置檔的 base 合併(LWW)。
func mergedBase(s *canonState) map[string]map[string]canon.Base {
	var devs []canon.DeviceState
	for _, id := range slices.Sorted(maps.Keys(s.devices)) {
		devs = append(devs, *s.devices[id])
	}
	return canon.MergeBase(devs)
}

// absorb:把 OBSERVE / DERIVE 產出的 track 寫回 s.tracks;新出現的 ISRC 衝突印警告。
func (s *canonState) absorb(updated map[string]canon.Track) {
	for _, cid := range slices.Sorted(maps.Keys(updated)) {
		tr := updated[cid]
		if old, ok := s.tracks.Tracks[cid]; ok && len(tr.Conflicts) > len(old.Conflicts) {
			c := tr.Conflicts[len(tr.Conflicts)-1]
			fmt.Fprintln(s.stderr, i18n.T("push.warn.isrc_conflict", "cid", cid, "platform", c.Provider, "title", c.Title, "id", c.ProviderID))
		}
		s.tracks.Tracks[cid] = tr
	}
}

// observeLive:push 的 OBSERVE——L 的 cid 與快照,觀測寫回 tracks(與 pull 的 DERIVE 同一條 canon.Observe)。
func observeLive(s *canonState, prov string, live canon.Observed) (lcid []string, snap canon.Snapshot) {
	id := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged)
	lcid, updated := canon.Observe(id, prov, s.tracks.Tracks, live.Tracks)
	s.absorb(updated)
	return lcid, canon.Snapshot{ID: live.ID, Name: live.Name, Items: idsOf(live.Tracks), CIDs: lcid}
}

func idsOf(tracks []provider.Track) []string {
	ids := make([]string, len(tracks))
	for i, t := range tracks {
		ids[i] = t.ProviderID
	}
	return ids
}

// liveUnchanged:前提二——比清單 id、名稱、provider id 序列,**不比 cid**:釘選 / 合併墓碑會讓同一個平台 id 的觀測 cid 變,
// 那不是平台變更(push 列 skip → resolve pin → 再 push 是正常流程,比 cid 會每次都被擋)。
func liveUnchanged(base, now canon.Snapshot) bool {
	return base.ID == now.ID && base.Name == now.Name && slices.Equal(base.Items, now.Items)
}

type pushPlan struct {
	pl       *canon.Playlist
	prov     string
	link     string
	reader   provider.PlaylistReader
	writer   provider.PlaylistWriter
	liveName string
	base     canon.Snapshot
	current  []string // L 的 provider id(ApplyOps 的 current;套用前再讀一次比對)
	want     []string // ops 套完後的 provider id 序列
	wantCIDs []string // 與 want 對齊的 cid(重讀 L′ 失敗時 base 的 cid 來源)
	wantName string   // 有 rename 才非空
	ops      []provider.PlaylistOp
	removes  int
}

// planPush:對每個目標 (清單, provider) 做 OBSERVE、兩個前提、PushPlan、閾值,產出計畫與 TSV 列。
// refused 是 --force 也不放行的(前提、local file、清單消失);blocked 是刪除閾值(--force 越過)。
// lives 非 nil 時該 (清單, provider) 的 L 直接用它(pl sync 的 push 半邊重用 pull 半邊剛讀的 L,決策 31)。
// strict:明說 --provider 卻寫不了的平台是錯(push);sync 不是——「sync 一個寫不了的平台」就是只 pull,stderr 說明後照常。
// 同理 refused(前提、local file、清單消失)在 strict 時擋整輪(exit 3),不 strict 時只跳過那一格的 push 半邊(PR #36 review:
// cron 的 sync --all 不能被一個含 local file 的清單永久綁死;pull 半邊照常落地)。
func planPush(ctx context.Context, s *canonState, targets []*canon.Playlist, only string, stderr io.Writer, pf *platforms, lives map[liveKey]*canon.Observed, strict bool) (plans []*pushPlan, rows [][]string, blocked, refused []string, err error) {
	merged := mergedBase(s)
	for _, pl := range targets {
		for _, prov := range slices.Sorted(maps.Keys(pl.Links)) {
			if only != "" && prov != only {
				continue
			}
			p, err := pf.provider(prov)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			link := pl.Links[prov]
			if foreignLink(p, link) { // 決策 33:別台裝置的本機清單只跳過——不算 refused;明說要推這個平台才算錯(同下面寫入端的規矩)
				if strict && only == prov {
					return nil, nil, nil, nil, i18n.Errorf("push.err.foreign_link", "playlist", pl.Name, "platform", prov, "link", link, "device", deviceName(s, link))
				}
				if strict { // 單獨 push 沒人說過;sync 的 pull 半邊已經說過一次,不重印
					fmt.Fprintln(stderr, i18n.T("push.skip.foreign_link", "playlist", pl.Name, "platform", prov, "link", link, "device", deviceName(s, link)))
				}
				continue
			}
			w, err := asPlaylistWriter(p)
			if err != nil {
				if strict && only == prov { // 明說要推這個平台才算錯;--all / 沒指定時只跳過(三個平台現在都能寫,這條留給沒有寫入能力的 provider)
					return nil, nil, nil, nil, err
				}
				fmt.Fprintln(stderr, i18n.T("push.skip.not_writable", "playlist", pl.Name, "platform", prov, "err", err))
				continue
			}
			r, refs, err := pf.reader(prov)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			refuse := func(msg string) {
				if strict {
					refused = append(refused, msg)
					return
				}
				fmt.Fprintln(stderr, i18n.T("push.skip.push_half", "playlist", pl.Name, "platform", prov, "reason", msg))
			}
			ref, ok := refs[link]
			if !ok {
				refuse(i18n.T("push.refuse.playlist_missing", "platform", prov, "link", link, "playlist", pl.Name))
				continue
			}
			if ref.Unwritable != "" { // 光看列表就知道寫不了(Apple 精選、協作清單):plan 階段列 refused——sync 只跳過那一格,cron 不會每輪 exit 1(PR #81 review)
				refuse(i18n.T("push.refuse.unwritable", "playlist", pl.Name, "platform", prov, "link", link, "reason", ref.Unwritable))
				continue
			}
			b, ok := merged[pl.PID][prov]
			if !ok || b.Snapshot.ID != link { // 前提一
				refuse(i18n.T("push.refuse.no_base", "playlist", pl.Name, "platform", prov))
				continue
			}
			reused, seen := lives[liveKey{pl.PID, prov}]
			if seen && reused == nil { // pull 半邊已跳過(restricted):不重讀、不重印
				continue
			}
			var live canon.Observed
			if seen {
				live = *reused
			} else {
				tracks, err := r.GetPlaylistItems(ctx, link)
				switch {
				case errors.Is(err, provider.ErrRestricted):
					fmt.Fprintln(stderr, i18n.T("push.skip.restricted", "playlist", pl.Name, "platform", prov, "link", link))
					continue
				case errors.Is(err, provider.ErrNotFound):
					tracks = nil
				case err != nil:
					return nil, nil, nil, nil, i18n.Errorf("push.err.read", "playlist", pl.Name, "platform", prov, "link", link, "err", friendlyErr(prov, err))
				}
				live = canon.Observed{ID: link, Name: ref.Name, Tracks: tracks}
			}
			tracks := live.Tracks
			lcid, snap := observeLive(s, prov, live)
			if !liveUnchanged(b.Snapshot, snap) { // 前提二
				refuse(i18n.T("push.refuse.unpulled", "playlist", pl.Name, "platform", prov))
				continue
			}
			items := make([]canon.LiveItem, len(tracks))
			var local []string
			for i, t := range tracks {
				items[i] = canon.LiveItem{CID: lcid[i], ProviderID: t.ProviderID}
				if t.Unpushable {
					local = append(local, t.Title)
				}
			}
			mappingID := func(cid string) (string, bool) { // 有 mapping 但推不出去(別的清單觀測進來的 local file)= skip,不是整批被拒
				m := s.tracks.Tracks[cid].Mappings[prov]
				return m.ID, m.ID != "" && w.Pushable(m.ID)
			}
			wantName := pl.Name
			if !p.Caps().Has(provider.CapPlaylistRename) { // 規則 4:rename 只在 provider 支援時排;不然 base 會記成新名字而平台還是舊的,下一輪規則 7 把 C 改回去(計畫 §2 A13)
				wantName = ""
			}
			ops, skipped := canon.PushPlan(items, pl.Items, live.Name, wantName, mappingID)
			plan := &pushPlan{pl: pl, prov: prov, link: link, reader: r, writer: w, liveName: live.Name, base: b.Snapshot, current: snap.Items, ops: ops}
			itemsChange := false
			for _, op := range ops {
				if op.Kind == provider.OpRemove {
					plan.removes++
				}
				if op.Kind != provider.OpRename {
					itemsChange = true
				}
			}
			if itemsChange && len(local) > 0 { // 計畫 Q22:整批取代加不回 local file,最小操作做好前拒絕;只改名不算
				refuse(i18n.T("push.refuse.local_files", "playlist", pl.Name, "platform", prov, "count", len(local), "titles", strings.Join(local, i18n.T("sep.list"))))
				continue
			}
			if removalBlocked(plan.removes, len(tracks)) {
				blocked = append(blocked, i18n.T("push.blocked.threshold", "playlist", pl.Name, "platform", prov, "count", plan.removes, "total", len(tracks)))
			}
			if plan.want, plan.wantName, err = provider.ApplyPlaylistOps(plan.current, ops); err != nil {
				return nil, nil, nil, nil, err
			}
			var prows [][]string
			prows, plan.wantCIDs = pushRows(s, plan, lcid, skipped)
			rows = append(rows, prows...)
			if len(ops) > 0 {
				plans = append(plans, plan)
			}
		}
	}
	return plans, rows, blocked, refused, nil
}

// pushRows:ops 是 provider id 的語意,列給人看要有 cid / 標題——沿著 ops 依序模擬(remove 取 L 位置、move 追工作序列、
// add 由 mapping 反查),順便得到與 want 對齊的 cid。skip 列在 ops 之後。
func pushRows(s *canonState, plan *pushPlan, lcid []string, skipped []canon.Skip) (rows [][]string, cids []string) {
	byID := map[string]string{} // 這個平台的 id → cid(add 的 provider id 一定來自 mapping)
	for _, it := range plan.pl.Items {
		if m := s.tracks.Tracks[it.CID].Mappings[plan.prov]; m.ID != "" {
			byID[m.ID] = it.CID
		}
	}
	work, ids := slices.Clone(lcid), slices.Clone(plan.current)
	row := func(action string, pos int, cid, id, reason, code string) {
		t := s.tracks.Tracks[cid]
		rows = append(rows, []string{action, plan.prov, plan.pl.Name, strconv.Itoa(pos), cid, id, t.Title, strings.Join(t.Artists, ", "), reason, code})
	}
	for _, op := range plan.ops {
		switch op.Kind {
		case provider.OpRemove:
			cid := work[op.Pos]
			work, ids = slices.Delete(work, op.Pos, op.Pos+1), slices.Delete(ids, op.Pos, op.Pos+1)
			row("remove", op.Pos, cid, op.ProviderID, i18n.T("push.reason.removed"), "removed_in_master")
		case provider.OpMove:
			cid, id := work[op.From], ids[op.From]
			work = slices.Insert(slices.Delete(work, op.From, op.From+1), op.Pos, cid)
			ids = slices.Insert(slices.Delete(ids, op.From, op.From+1), op.Pos, id)
			row("move", op.Pos, cid, id, i18n.T("push.reason.moved"), "moved_in_master")
		case provider.OpAdd:
			cid := byID[op.ProviderID]
			work, ids = slices.Insert(work, op.Pos, cid), slices.Insert(ids, op.Pos, op.ProviderID)
			row("add", op.Pos, cid, op.ProviderID, i18n.T("push.reason.push"), "push")
		case provider.OpRename:
			rows = append(rows, []string{"rename", plan.prov, plan.pl.Name, "", "", "", op.Name, "", i18n.T("push.reason.renamed", "from", plan.liveName, "to", op.Name), "renamed_in_master"})
		}
	}
	for _, sk := range skipped {
		t := s.tracks.Tracks[sk.CID]
		reason, code, id := sk.Reason, sk.Code, ""
		if m := t.Mappings[plan.prov]; m.ID != "" { // 有 mapping 但推不出去:resolve 修不了,提示也不會算它
			reason, code, id = i18n.T("push.reason.unpushable"), "unpushable", m.ID // ponytail: Pushable 只回 bool,理由三選一由使用者看平台判斷;第三個平台時讓 Pushable 回原因(P6 §2 A10)
		}
		rows = append(rows, []string{"skip", plan.prov, plan.pl.Name, "", sk.CID, id, t.Title, strings.Join(t.Artists, ", "), reason, code})
	}
	return rows, work
}

// apply:確認之後再讀一次 L 比對 current(平台端沒有 CAS,這是縮小窗口的做法;spec §6.5.2 規則 6)→ ApplyOps → 重讀 L′ → base := L′。
// stale = 確認期間平台變了(或重讀失敗),這份零寫入;其餘 err 是 ApplyOps 的錯(可能寫了一半)。n 是套了幾個 op;
// touched = 平台有被改到(全部成功或半截),COMMIT 失敗時的說法靠它,不靠 n(半截時 n 是 0)。
func (p *pushPlan) apply(ctx context.Context, s *canonState, stderr io.Writer) (n int, touched, stale bool, err error) {
	now, err := p.reader.GetPlaylistItems(ctx, p.link)
	if errors.Is(err, provider.ErrNotFound) {
		now, err = nil, nil
	}
	if err != nil {
		return 0, false, true, friendlyErr(p.prov, err)
	}
	if !slices.Equal(idsOf(now), p.current) {
		return 0, false, true, nil
	}
	skipped, werr := p.writer.ApplyOps(ctx, p.link, p.current, p.ops)
	written, renamed := len(p.want), p.wantName != ""
	var pw *provider.PartialWriteError
	switch {
	case werr == nil:
		n, touched = len(p.ops)-len(skipped), true
	case errors.As(werr, &pw):
		written, renamed, touched = pw.Written, pw.Renamed, true
	default:
		written, renamed = -1, false // 第一個請求就失敗,平台沒動
		werr = friendlyErr(p.prov, werr)
	}
	for _, op := range skipped { // ponytail: 平台不支援的 op 先印 stderr;Spotify / Apple / local 全部 Kind 都支援,第一個真的會 skip 的平台出現時再決定 manual 列怎麼進表
		if op.Kind == provider.OpRename { // 平台沒改名就不能把 base 記成新名字(A13 的第二道防線:planPush 已不排 rename 給不支援的平台)
			renamed = false
		}
		fmt.Fprintln(stderr, i18n.T("push.manual_op", "playlist", p.pl.Name, "platform", p.prov, "kind", op.Kind, "pos", op.Pos))
	}
	// 規則 7:成功或失敗都重讀 L′、base := L′。重讀也失敗時 base 記成「我們相信平台現在的樣子」(want 的前 written 首):
	// 不然斷網時 PUT 已落地、base 還停在 L,下一次 pull 會把自己的半截寫入讀成使用者刪了歌。
	name := p.liveName
	if renamed {
		name = p.wantName
	}
	var snap canon.Snapshot
	after, rerr := p.reader.GetPlaylistItems(ctx, p.link)
	if errors.Is(rerr, provider.ErrNotFound) {
		after, rerr = nil, nil
	}
	switch {
	case rerr == nil:
		_, snap = observeLive(s, p.prov, canon.Observed{ID: p.link, Name: name, Tracks: after})
		if werr == nil && !slices.Equal(snap.Items, p.want) {
			fmt.Fprintln(stderr, i18n.T("push.warn.unexpected_result", "playlist", p.pl.Name, "platform", p.prov))
		}
	case written < 0:
		fmt.Fprintln(stderr, i18n.T("push.warn.reread_failed_untouched", "playlist", p.pl.Name, "platform", p.prov, "err", friendlyErr(p.prov, rerr)))
		return n, touched, false, werr
	default:
		fmt.Fprintln(stderr, i18n.T("push.warn.reread_failed_written", "playlist", p.pl.Name, "platform", p.prov, "err", friendlyErr(p.prov, rerr), "count", written))
		snap = canon.Snapshot{ID: p.link, Name: name, Items: slices.Clone(p.want[:written]), CIDs: slices.Clone(p.wantCIDs[:written])}
	}
	if !snapshotEqual(p.base, snap) {
		s.mine().SetBase(p.pl.PID, p.prov, snap)
	}
	return n, touched, false, werr
}

// applyPlans:逐清單 apply;回傳套了幾個 op、平台有沒有被改到、要在 COMMIT 之後才回的錯(ApplyOps 失敗 exit 1 蓋過「確認期間變了」exit 3)。
func applyPlans(ctx context.Context, s *canonState, plans []*pushPlan, stderr io.Writer) (applied int, touched bool, deferred error) {
	var stale []string
	for _, p := range plans {
		k, hit, isStale, err := p.apply(ctx, s, stderr)
		applied, touched = applied+k, touched || hit
		switch {
		case isStale:
			if err != nil {
				fmt.Fprintln(stderr, i18n.T("push.stale.reread_failed", "playlist", p.pl.Name, "platform", p.prov, "err", err))
			} else {
				fmt.Fprintln(stderr, i18n.T("push.stale.changed", "playlist", p.pl.Name, "platform", p.prov))
			}
			stale = append(stale, i18n.T("push.stale.item", "playlist", p.pl.Name, "platform", p.prov))
		case err != nil:
			fmt.Fprintln(stderr, i18n.T("push.write_failed", "playlist", p.pl.Name, "platform", p.prov, "err", err))
			if deferred == nil {
				deferred = err
			}
		}
	}
	if deferred == nil && len(stale) > 0 {
		deferred = &BlockedError{Msg: i18n.T("push.err.stale", "count", len(stale), "items", strings.Join(stale, i18n.T("sep.list")))}
	}
	return applied, touched, deferred
}

// finishPush:withCanonical 回來之後的收尾。COMMIT 失敗而平台已經被改到:版本守衛那句「零寫入」只對 Drive 成立,改口;
// 半截寫入 + Drive 沒寫成兩件事都要講——那是規則 7 要防的狀態(平台缺一截、base 又沒落地),下一次 pull 會把缺的那截列成移除(計畫 Q24)。
// next 是「接下來怎麼做」那句(push / sync 各自的說法);nextAfterHalf 是半截寫入那條「先 pl pull --dry-run 看清楚」之後接的那句。
func finishPush(err error, applied int, touched bool, deferred error, next, nextAfterHalf string) error {
	var ge *guardError
	var driveMsg string
	switch {
	case err == nil:
	case !touched:
		return err
	case errors.As(err, &ge):
		driveMsg = i18n.T("push.drive.guard", "files", ge.Files)
	default:
		driveMsg = i18n.T("push.drive.failed", "err", err.Error())
	}
	switch { // 傳 .Error() 不傳 error:原本是 %v、不包起來,exit code 照舊是 1(deferred 可能是確認期間變了的 BlockedError)
	case driveMsg != "" && deferred != nil:
		return i18n.Errorf("push.err.half_and_drive", "err", deferred.Error(), "drive", driveMsg, "next", nextAfterHalf)
	case driveMsg != "":
		return i18n.Errorf("push.err.written_but_drive", "count", applied, "drive", driveMsg, "next", next)
	}
	return deferred
}

func newPlPushCmd() *cobra.Command {
	var all, dryRun, yes, force bool
	var prov string
	cmd := &cobra.Command{
		Use:   "push [name|pid]",
		Short: i18n.T("cmd.pl.push.short"),
		Long:  i18n.T("cmd.pl.push.long"),
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := needTarget(cmd, args, all, i18n.Errorf("pick.err.need_target.push")); err != nil {
				return err
			}
			if prov != "" && !isProviderID(prov) {
				return i18n.Errorf("push.err.bad_provider", "valid", strings.Join(providerIDs, "|"), "value", strconv.Quote(prov))
			}
			if force && all {
				return i18n.Errorf("push.err.force_with_all")
			}
			ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
			var deferred error // ApplyOps 失敗 / 確認期間平台變了:base 要落地(COMMIT 要走),所以 fn 回 nil、這裡收尾再回錯
			applied, touched := 0, false
			err := withCanonical(ctx, stderr, func(s *canonState) error {
				targets, err := pullTargets(s, args, all, prov, i18n.T("pull.pick.title.push"))
				if err != nil {
					return err
				}
				plans, rows, blocked, refused, err := planPush(ctx, s, targets, prov, stderr, newPlatforms(ctx), nil, true)
				if err != nil {
					return err
				}
				if len(rows) > 0 {
					if err := ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), pullHeader, rows, tableOpts(yes)...); err != nil {
						return err
					}
				}
				if len(refused) > 0 {
					return &BlockedError{Msg: strings.Join(refused, i18n.T("sep.clause"))}
				}
				if len(blocked) > 0 && !force {
					return &BlockedError{Msg: i18n.T("changeset.blocked", "reasons", strings.Join(blocked, i18n.T("sep.clause")))}
				}
				resolveHint(s, targets, prov, stderr)
				n := 0
				for _, p := range plans {
					n += len(p.ops)
				}
				if n == 0 {
					fmt.Fprintln(stderr, i18n.T("changeset.no_changes"))
					if dryRun {
						return errSkipCommit
					}
					return nil // Observe 可能補了 tracks,COMMIT 以位元組差異決定
				}
				if dryRun {
					return &PendingError{N: n}
				}
				if !yes {
					if !bothTTY(cmd) {
						return &PendingError{N: n}
					}
					ok, err := confirmWrite("push.confirm", i18n.T("push.confirm", "count", n))
					if err != nil {
						return err
					}
					if !ok {
						return &PendingError{N: n}
					}
				}
				applied, touched, deferred = applyPlans(ctx, s, plans, stderr)
				if applied > 0 {
					fmt.Fprintln(stderr, i18n.T("push.done", "count", applied))
				}
				return nil
			})
			return finishPush(err, applied, touched, deferred, i18n.T("push.next"), i18n.T("push.next_after_half"))
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, i18n.T("cmd.pl.push.flag.all"))
	cmd.Flags().StringVar(&prov, "provider", "", i18n.T("cmd.pl.push.flag.provider"))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("cmd.pl.push.flag.dry_run"))
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, i18n.T("cmd.pl.push.flag.yes"))
	cmd.Flags().BoolVar(&force, "force", false, i18n.T("cmd.pl.push.flag.force"))
	return cmd
}
