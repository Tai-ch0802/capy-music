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

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// capy migrate(2026-09-15,計畫 docs/superpowers/plans/2026-09-15-order-dedup-migrate.md §3、附錄 C 決策 39):把 A 平台的一個清單搬
// (複製)到 B 平台,使用者不必自己串 link / pull / resolve / push。一個 withCanonical 裡依序:讀 A → 決定正本 C 與 B(既有的 B 先 pull 進 C)
// → 把 A 裡 C 還沒有的依 A 的順序接在尾端(同平台 id / 同 ISRC 的略過,A 自己的重複也只留一份)→ resolve 到 B(ISRC 反查 → 模糊比對;
// TTY 可當場逐筆裁決)→ 一張表、一次確認 → 需要時才在 B 建清單 → push。
// 順序:B(或 C)原本的順序是前綴、A 的曲目依 A 的順序接在後面——明確建,不靠 DERIVE(沒有 base 的 bootstrap 會採平台順序)。
// A 不連結 canonical(使用者定案):一次性複製,之後 pl sync 不會因 bootstrap 把 B 重排成 A 的順序;要持續同步走 README 的 link + sync。
// 例外:沿用的正本本來就連著 A(手動流程做到一半)——那 A 那半也 pull 進 C、以正本為準,不尾端追加(review #55:不然 C 與 A 順序分岔,
// 之後的 sync 會重排 A);結尾也照 links 講「來源連著」而不是「一次性複製」。
// 永遠不刪 A;對 B 只做 add——B 有待同步的移除 / 換序 / 改名時擋下(exit 3),先 pl sync。

// migrateIsTTY:確認與當場裁決的 TTY 閘;測試替換點(同 reviewIsTTY 慣例)。
var migrateIsTTY = func(cmd *cobra.Command) bool { return bothTTY(cmd) } // 委派、不複製函式值:覆寫 bothTTY 一處七個閘全對齊(review #57)

// migrateEnd:一端的平台清單。id 空 = 目標要新建(名字跟來源)。
type migrateEnd struct{ prov, id, name string }

func (e migrateEnd) String() string { return e.prov + ":" + e.id }

func newMigrateCmd() *cobra.Command {
	var from, to string
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "migrate " + i18n.T("cmd.migrate.use"),
		Short: i18n.T("cmd.migrate.short"),
		Long:  i18n.T("cmd.migrate.long"),
		Args:  argsOrPicker(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return runMigrate(cmd, args, from, to, dryRun, yes) },
	}
	cmd.Flags().StringVar(&from, "from", "", i18n.T("cmd.migrate.flag.from", "ids", strings.Join(providerIDs, "|")))
	cmd.Flags().StringVar(&to, "to", "", i18n.T("cmd.migrate.flag.to"))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("cmd.migrate.flag.dry_run"))
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, i18n.T("cmd.migrate.flag.yes"))
	return cmd
}

func runMigrate(cmd *cobra.Command, args []string, from, to string, dryRun, yes bool) error {
	ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
	interactive := isInteractive(cmd)
	if !interactive && (from == "" || to == "") {
		return i18n.Errorf("migrate.err.need_from_to")
	}
	// 來源
	var err error
	if from == "" {
		if from, err = pickProvider(i18n.T("migrate.pick.from")); err != nil {
			return err
		}
	}
	if !isProviderID(from) {
		return i18n.Errorf("migrate.err.bad_from", "ids", strings.Join(providerIDs, "|"), "value", strconv.Quote(from))
	}
	pA, err := newProvider(ctx, from)
	if err != nil {
		return err
	}
	rA, err := asPlaylistReader(pA)
	if err != nil {
		return err
	}
	refsA, err := rA.ListPlaylists(ctx)
	if err != nil {
		return friendlyErr(from, err)
	}
	src := migrateEnd{prov: from}
	if len(args) == 1 {
		if src.id, err = resolvePlaylistID(ctx, rA, from, args[0]); err != nil {
			return err
		}
		src.name = args[0]
	} else if src.id, err = pickPlatformPlaylist(from, refsA, ""); err != nil {
		return err
	}
	for _, x := range refsA {
		if x.ID == src.id {
			src.name = x.Name
		}
	}
	// 目標
	dst, toRef := migrateEnd{}, ""
	switch {
	case to == "":
		if dst.prov, err = pickProvider(i18n.T("migrate.pick.to")); err != nil {
			return err
		}
	case strings.Contains(to, ":"):
		if dst.prov, toRef, err = splitProviderRef(to); err != nil {
			return err
		}
	case isProviderID(to):
		dst.prov = to
	default:
		return i18n.Errorf("migrate.err.bad_to", "ids", strings.Join(providerIDs, "|"), "value", strconv.Quote(to))
	}
	pB, err := newProvider(ctx, dst.prov)
	if err != nil {
		return err
	}
	wB, err := asPlaylistWriter(pB)
	if err != nil {
		return i18n.Errorf("migrate.err.target_read_only", "platform", dst.prov, "err", err)
	}
	rB, err := asPlaylistReader(pB)
	if err != nil {
		return err
	}
	refsB, err := rB.ListPlaylists(ctx)
	if err != nil {
		return friendlyErr(dst.prov, err)
	}
	creator, cerr := asPlaylistCreator(pB)
	switch {
	case toRef != "":
		if dst.id, err = resolvePlaylistID(ctx, rB, dst.prov, toRef); err != nil {
			return err
		}
		if !slices.ContainsFunc(refsB, func(x provider.PlaylistRef) bool { return x.ID == dst.id }) { // 同 pl link:不在自己列表裡的 pull 會當 gone
			return i18n.Errorf("migrate.err.target_not_listed", "platform", dst.prov, "id", dst.id)
		}
	case to == "": // 挑選器:既有的清單,或建一個新的
		newLabel := ""
		if cerr == nil {
			newLabel = i18n.T("migrate.pick.create_new", "platform", dst.prov)
		}
		if dst.id, err = pickPlatformPlaylist(dst.prov, refsB, newLabel); err != nil {
			return err
		}
	}
	if dst.id != "" { // --to 與挑選器兩條路都要過:讀不到的清單 push 前提一會擋,但它指路的 pl pull 一樣讀不到,使用者會卡住(review #55)
		if ok, err := readable(ctx, rB, dst.id); err != nil {
			return friendlyErr(dst.prov, err)
		} else if !ok {
			return i18n.Errorf("migrate.err.target_unreadable", "platform", dst.prov, "id", dst.id)
		}
	}
	if dst.id == "" {
		if cerr != nil {
			return i18n.Errorf("migrate.err.cannot_create", "platform", dst.prov, "err", cerr.Error())
		}
		dup, err := sameNamePlaylists(ctx, rB, refsB, src.name)
		if err != nil {
			return friendlyErr(dst.prov, err)
		}
		if len(dup) > 0 {
			return i18n.Errorf("migrate.err.same_name_exists", "platform", dst.prov, "name", src.name, "ids", strings.Join(dup, i18n.T("sep.list")), "id", dup[0])
		}
		dst.name = src.name
	} else {
		for _, x := range refsB {
			if x.ID == dst.id {
				dst.name = x.Name
			}
		}
	}
	// 來源的曲目:直接讀,不連結、不記 base
	reportProgress("read", 0, 0)
	tracksA, err := rA.GetPlaylistItems(ctx, src.id)
	switch {
	case errors.Is(err, provider.ErrRestricted):
		return errRestrictedPlaylist
	case errors.Is(err, provider.ErrNotFound):
		tracksA = nil
	case err != nil:
		return friendlyErr(from, err)
	}
	if len(tracksA) == 0 {
		fmt.Fprintln(stderr, i18n.T("migrate.source_empty", "src", src))
		return nil
	}
	var deferred error
	applied, touched := 0, false
	created, plName, summary := "", "", ""
	err = withCanonical(ctx, stderr, func(s *canonState) error {
		pl, err := migrateCanonical(s, src, dst, stderr)
		if err != nil {
			return err
		}
		plName = pl.Name
		targets := []*canon.Playlist{pl}
		pf := newPlatforms(ctx)
		var pullRows [][]string
		lives := map[liveKey]*canon.Observed{}
		observe := func(prov string, pf *platforms) error {
			rows, blocked, lv, err := observeAndDerive(ctx, s, targets, prov, stderr, pf)
			if err != nil {
				return err
			}
			if len(blocked) > 0 {
				return &BlockedError{Msg: i18n.T("migrate.err.blocked", "blocked", strings.Join(blocked, i18n.T("sep.clause")), "name", pl.Name)}
			}
			pullRows = append(pullRows, rows...)
			maps.Copy(lives, lv)
			return nil
		}
		// 正本已連著來源(README 手動流程做到一半、或本來就在同步):來源那半也 pull 進 C,新歌落在它在來源的真實位置——
		// 尾端追加會讓正本與來源的順序分岔,結尾建議的 pl sync 就會把使用者的來源清單重排(review #55,踩到決策 38)
		if pl.Links[src.prov] != "" {
			if err := observe(src.prov, pf); err != nil {
				return err
			}
		}
		if dst.id != "" { // 既有的 B 先吸進 C:C 之後就是 B 的原樣(前綴),push 的兩個前提也靠這一步
			if err := observe(dst.prov, pf); err != nil {
				return err
			}
			if pl.Links[dst.prov] != dst.id {
				return i18n.Errorf("migrate.err.target_gone", "platform", dst.prov, "id", dst.id)
			}
		}
		type appended struct {
			pos   int
			cid   string
			track provider.Track
		}
		var added []appended
		// 正本就是來源的正本(pull 剛做過):以正本為準,不再尾端追加——來源裡 C 刻意移除的那幾首(規則 4′ 留給 push)不能被加回來
		follow := pl.Links[src.prov] == src.id
		if !follow { // A 的曲目:C 還沒有的依 A 的順序接在尾端;同一首(同平台 id 或同 ISRC)只留一份
			id := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged)
			lcid, updated := canon.Observe(id, src.prov, s.tracks.Tracks, tracksA)
			s.absorb(updated)
			have := map[string]bool{}
			for _, it := range pl.Items {
				have[id.Redirect(it.CID)] = true
			}
			for i, cid := range lcid {
				if have[cid] {
					continue
				}
				have[cid] = true
				if _, err := pl.Append(cid); err != nil {
					return err
				}
				added = append(added, appended{len(pl.Items) - 1, cid, tracksA[i]})
			}
		}
		existing := len(pl.Items) - len(added) // 正本既有的份數:新建的目標要把它們一起推過去(沿用了連著來源的正本時就是全部);既有的目標它們已在上面
		if len(added) == 0 && dst.id != "" {   // 沒東西可接就一個位元組都不寫:pull 半邊看到的變更留給 sync(同 pl dedup 的規矩),不退化成一次 sync
			msg := i18n.T("migrate.all_present", "src", src, "count", len(tracksA), "name", pl.Name, "pid", pl.PID)
			if len(pullRows) > 0 {
				msg += i18n.T("migrate.all_present.pull_pending", "count", len(pullRows))
			}
			fmt.Fprintln(stderr, msg)
			return errSkipCommit
		}
		// resolve 到 B。新建的 B 還沒有 id,而 resolve.Needs 只看有 link 的 (清單, provider):給副本一個佔位 link,不碰 pl 本身
		probe := *pl
		probe.Links = maps.Clone(pl.Links)
		if probe.Links[dst.prov] == "" {
			probe.Links[dst.prov] = "(new)"
		}
		items, err := planResolve(ctx, s, []*canon.Playlist{&probe}, dst.prov, stderr)
		if err != nil {
			return err
		}
		mapped := applyMappings(s, items)
		queued := 0
		for _, it := range items {
			if it.action != "map" {
				queued++
			}
		}
		if mapped > 0 || queued > 0 {
			fmt.Fprintln(stderr, i18n.T("migrate.resolve_summary", "mapped", mapped, "platform", dst.prov, "queued", queued))
		}
		if queued > 0 && !yes && !dryRun && migrateIsTTY(cmd) {
			ok, err := confirmWrite("migrate.confirm.review", i18n.T("migrate.confirm.review", "count", queued, "platform", dst.prov, "name_arg", strconv.Quote(pl.Name)))
			if err != nil {
				return err
			}
			if ok {
				n, err := reviewLoop(ctx, s, items, false, stderr)
				if errors.Is(err, huh.ErrUserAborted) { // 取消 = 整輪不寫入(同 resolve --review):清單也還沒建
					fmt.Fprintln(stderr, i18n.T("migrate.review.cancelled"))
					return &PendingError{N: len(added) + len(pullRows)}
				}
				if err != nil {
					return err
				}
				fmt.Fprintln(stderr, i18n.T("migrate.reviewed", "count", n))
			}
		}
		// 表:pull(既有 B 的變更)、migrate(接在尾端的來源曲目)、push(既有 B 才算得出來;新建的要建了才有 id)
		var rows [][]string
		for _, r := range pullRows {
			rows = append(rows, append([]string{"pull"}, r...))
		}
		unmapped := 0
		if dst.id == "" { // 新建的目標:正本既有的也會推過去——同樣進表(建了才算得出真的 push 列,先用 mapping 算等價的)、同樣算沒對應的;
			pos := 0 //   不然對不到的默默不推、表是空的、結尾不說(review #55)
			for _, it := range pl.Items[:existing] {
				id, reason, code, ok := migrateReason(s, wB, dst.prov, it.CID)
				action, at := "add", strconv.Itoa(pos)
				if ok {
					pos++
				} else {
					action, at, unmapped = "skip", "", unmapped+1
				}
				t := s.tracks.Tracks[it.CID]
				rows = append(rows, []string{"push", action, dst.prov, pl.Name, at, it.CID, id, t.Title, strings.Join(t.Artists, ", "), reason, code})
			}
		}
		for _, a := range added {
			_, reason, code, ok := migrateReason(s, wB, dst.prov, a.cid)
			if !ok {
				unmapped++
			}
			rows = append(rows, []string{"migrate", "add", src.prov, pl.Name, strconv.Itoa(a.pos), a.cid, a.track.ProviderID, a.track.Title, strings.Join(a.track.Artists, ", "), reason, code})
		}
		var plans []*pushPlan
		if dst.id != "" {
			var pushRows [][]string
			if plans, pushRows, err = migratePlanPush(ctx, s, targets, dst.prov, stderr, pf, lives); err != nil {
				return err
			}
			for _, r := range pushRows {
				rows = append(rows, append([]string{"push"}, r...))
			}
		}
		if len(rows) > 0 {
			if err := ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), syncHeader, rows, tableOpts(yes)...); err != nil {
				return err
			}
		}
		n := len(pullRows) + len(added)
		if dst.id == "" {
			n += existing
		}
		if dryRun {
			return &PendingError{N: n}
		}
		if !yes {
			if !migrateIsTTY(cmd) {
				return &PendingError{N: n}
			}
			var key, prompt string
			switch {
			case dst.id != "":
				key, prompt = "migrate.confirm.add", i18n.T("migrate.confirm.add", "src", src, "count", len(added), "dst", dst)
			case follow:
				key, prompt = "migrate.confirm.create_follow", i18n.T("migrate.confirm.create_follow", "platform", dst.prov, "name", pl.Name, "src", src, "count", len(pl.Items))
			case existing > 0:
				key, prompt = "migrate.confirm.create_with_existing", i18n.T("migrate.confirm.create_with_existing", "platform", dst.prov, "name", pl.Name, "src", src, "count", len(added), "existing", existing)
			default:
				key, prompt = "migrate.confirm.create", i18n.T("migrate.confirm.create", "platform", dst.prov, "name", pl.Name, "src", src, "count", len(added))
			}
			if unmapped > 0 { // 附加的半句不換 key:這一則提示是哪一種,看前面那句
				prompt += i18n.T("migrate.confirm.unmapped", "count", unmapped, "platform", dst.prov)
			}
			ok, err := confirmWrite(key, prompt)
			if err != nil {
				return err
			}
			if !ok {
				return &PendingError{N: n}
			}
		}
		if dst.id == "" { // 確認之後才建:取消、--dry-run、擋下都不會在平台留下沒人連的空清單
			made, err := creator.CreatePlaylist(ctx, pl.Name)
			if err != nil {
				return friendlyErr(dst.prov, err)
			}
			dst.id, dst.name, created = made.ID, made.Name, made.ID
			pl.Links[dst.prov] = made.ID
			pl.UpdatedAt = canon.Now().Unix()
			fmt.Fprintln(stderr, i18n.T("migrate.created", "platform", dst.prov, "name", made.Name, "id", made.ID))
			// 重新 list 才看得到剛建的清單;讀到空清單、記下 base(push 前提一);沒有 base 又是空的 L,DERIVE 不會動 C
			pf2 := newPlatforms(ctx)
			if _, _, lives, err = observeAndDerive(ctx, s, targets, dst.prov, stderr, pf2); err != nil {
				return err
			}
			if pl.Links[dst.prov] != made.ID { // 剛建的清單不在第二次 list 裡(真帳號還沒驗過的路徑):被當 gone 取消連結;收尾會帶 pl link 接回的命令
				return i18n.Errorf("migrate.err.created_not_listed", "platform", dst.prov, "id", made.ID)
			}
			if plans, _, err = migratePlanPush(ctx, s, targets, dst.prov, stderr, pf2, lives); err != nil {
				return err
			}
		}
		reportProgress("write", 0, 0)
		applied, touched, deferred = applyPlans(ctx, s, plans, stderr)
		if applied > 0 {
			fmt.Fprintln(stderr, i18n.T("migrate.pushed", "count", applied))
		}
		nameArg := strconv.Quote(pl.Name)
		switch {
		case follow && len(pullRows) > 0:
			summary = i18n.T("migrate.summary.follow_pulled", "name", pl.Name, "pid", pl.PID, "src", src, "pulled", len(pullRows), "count", applied, "dst", dst) + "\n"
		case follow:
			summary = i18n.T("migrate.summary.follow", "name", pl.Name, "pid", pl.PID, "src", src, "count", applied, "dst", dst) + "\n"
		default:
			summary = i18n.T("migrate.summary.appended", "src", src, "count", len(added), "name", pl.Name, "pid", pl.PID, "total", len(pl.Items), "pushed", applied, "dst", dst) + "\n"
		}
		if unmapped > 0 {
			summary += i18n.T("migrate.summary.unmapped", "count", unmapped, "platform", dst.prov, "name_arg", nameArg) + "\n"
		}
		if link := pl.Links[src.prov]; link != "" { // 兩句都要看 links 講:來源連著時「一次性複製」是假話、再叫人 pl link 是多餘的(review #55)
			summary += i18n.T("migrate.summary.source_linked", "platform", src.prov, "id", link, "name_arg", nameArg) + "\n"
		} else {
			summary += i18n.T("migrate.summary.source_unlinked", "platform", src.prov, "name_arg", nameArg, "id", src.id) + "\n"
		}
		return nil
	})
	if err == nil && deferred == nil { // push 失敗(deferred)時 COMMIT 照走、但結尾要以它收場(同 push / sync 經 finishPush);成功才講成功
		fmt.Fprint(stderr, summary)
	}
	next := i18n.T("migrate.next.rerun", "name_arg", strconv.Quote(src.name), "from", src.prov, "platform", dst.prov, "id", dst.id)
	after := i18n.T("migrate.next.rerun_after")
	if created != "" { // 清單建好了、連結卻沒寫進 Drive:重跑會撞同名,要指到已建好的那個
		next = i18n.T("migrate.next.relink", "platform", dst.prov, "created_name", dst.name, "id", created, "name_arg", strconv.Quote(plName))
		after = i18n.T("migrate.next.relink_after", "name_arg", strconv.Quote(plName), "platform", dst.prov, "id", created)
		if err != nil && !touched {
			err = fmt.Errorf("%w%s%s", err, i18n.T("sep.clause"), next)
		}
	}
	return finishPush(err, applied, touched, deferred, next, after)
}

// migrateCanonical:決定 migrate 用哪個 canonical 清單 C。
//   - B 既有且已連著某個 C → 就是它。
//   - B 既有、沒連 → 找同名(B 的名字;不然 push 會多排一個 rename 把使用者的清單改名)的 C 接管,沒有就建;連著 B 平台別的清單的擋下。
//   - B 新建 → 找同名(A 的名字)的 C:沒有就建;連著 B 平台清單的擋下(要加進那個用 --to);其餘沿用——README 手動流程做到一半的人
//     就在這裡(canonical 已連著來源平台),再建一個同名的會讓 find 永遠歧義。
func migrateCanonical(s *canonState, src, dst migrateEnd, stderr io.Writer) (*canon.Playlist, error) {
	if dst.id != "" {
		for _, pid := range slices.Sorted(maps.Keys(s.playlists)) {
			if pl := s.playlists[pid]; pl.Links[dst.prov] == dst.id {
				fmt.Fprintln(stderr, i18n.T("migrate.canon.reuse_linked", "end", dst, "name", pl.Name, "pid", pl.PID))
				return pl, nil
			}
		}
	}
	name := dst.name
	if dst.id == "" {
		name = src.name
	}
	pl, err := s.find(name)
	if err != nil {
		return nil, err
	}
	switch {
	case pl == nil:
		pl = canon.NewPlaylist(name)
		s.playlists[pl.PID] = pl
		fmt.Fprintln(stderr, i18n.T("migrate.canon.created", "name", pl.Name, "pid", pl.PID))
	case pl.Links[dst.prov] != "" && dst.id == "":
		return nil, i18n.Errorf("migrate.err.canon_linked_to_target", "name", pl.Name, "pid", pl.PID, "platform", dst.prov, "id", pl.Links[dst.prov])
	case pl.Links[dst.prov] != "":
		return nil, i18n.Errorf("migrate.err.canon_linked_elsewhere", "name", pl.Name, "pid", pl.PID, "platform", dst.prov, "linked", pl.Links[dst.prov], "id", dst.id, "name_arg", strconv.Quote(pl.Name))
	case dst.id == "" && pl.Links[src.prov] != "" && pl.Links[src.prov] != src.id: // 同名但連著來源平台另一份:沿用會把這份的歌推去那份
		return nil, i18n.Errorf("migrate.err.canon_linked_other_source", "name", pl.Name, "pid", pl.PID, "platform", src.prov, "linked", pl.Links[src.prov], "src", src, "name_arg", strconv.Quote(pl.Name))
	case len(pl.Links) == 0:
		fmt.Fprintln(stderr, i18n.T("migrate.canon.reuse_unlinked", "name", pl.Name, "pid", pl.PID))
	default:
		fmt.Fprintln(stderr, i18n.T("migrate.canon.reuse", "name", pl.Name, "pid", pl.PID, "links", linkSummary(pl)))
	}
	if dst.id != "" && pl.Links[dst.prov] != dst.id {
		pl.Links[dst.prov] = dst.id
		pl.UpdatedAt = canon.Now().Unix()
		fmt.Fprintln(stderr, i18n.T("migrate.canon.linked", "name", pl.Name, "pid", pl.PID, "end", dst))
	}
	return pl, nil
}

// migratePlanPush:push 半邊(strict,同 pl push),再加 migrate 自己的規矩——只做新增:B 有待同步的移除 / 換序 / 改名就擋下,先 pl sync。
func migratePlanPush(ctx context.Context, s *canonState, targets []*canon.Playlist, prov string, stderr io.Writer, pf *platforms, lives map[liveKey]*canon.Observed) ([]*pushPlan, [][]string, error) {
	plans, rows, _, refused, err := planPush(ctx, s, targets, prov, stderr, pf, lives, true)
	if err != nil {
		return nil, nil, err
	}
	if len(refused) > 0 {
		return nil, nil, &BlockedError{Msg: strings.Join(refused, i18n.T("sep.clause"))}
	}
	for _, p := range plans {
		for _, op := range p.ops {
			if op.Kind != provider.OpAdd {
				return nil, nil, &BlockedError{Msg: i18n.T("migrate.err.pending_non_add", "name", p.pl.Name, "platform", p.prov, "kind", op.Kind, "name_arg", strconv.Quote(p.pl.Name))}
			}
		}
	}
	return plans, rows, nil
}

// migrateReason:一首曲目在目標平台的去向——推得出去 / 有 mapping 但推不出去 / 沒對應(後兩種算 unmapped);id 給表的 provider_id 欄,
// code 給 REASON_CODE 欄(同 pushRows 的 push / unpushable / no_mapping;網頁的搬家精靈靠 "push" 認「搬得過去」)。
func migrateReason(s *canonState, w provider.PlaylistWriter, prov, cid string) (id, reason, code string, ok bool) {
	m := s.tracks.Tracks[cid].Mappings[prov]
	switch {
	case m.ID != "" && w.Pushable(m.ID):
		return m.ID, i18n.T("migrate.reason.push", "platform", prov, "id", m.ID, "source", m.Source, "confidence", m.Confidence), "push", true
	case m.ID != "":
		return m.ID, i18n.T("migrate.reason.unpushable"), "unpushable", false
	}
	return "", i18n.T("migrate.reason.no_mapping", "platform", prov), "no_mapping", false
}

// sameNamePlaylists:平台上跟 name 同名(EqualFold,同 resolvePlaylistID)而且連得上的清單 id。pl link --create 與 migrate 建清單前都先擋——
// 再建一個同名的只會讓 <平台>:<名稱> 變歧義;讀不到的(追蹤的別人的清單)連不了,不算撞名,照常建。
func sameNamePlaylists(ctx context.Context, r provider.PlaylistReader, refs []provider.PlaylistRef, name string) ([]string, error) {
	var dup []string
	for _, x := range refs {
		if !strings.EqualFold(x.Name, name) {
			continue
		}
		ok, err := readable(ctx, r, x.ID)
		if err != nil {
			return nil, err
		}
		if ok {
			dup = append(dup, x.ID)
		}
	}
	return dup, nil
}
