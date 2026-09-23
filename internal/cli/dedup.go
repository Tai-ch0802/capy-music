package cli

import (
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
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// pl dedup(2026-09-15):去掉清單裡重複出現的曲目。重複 = capy 認定為同一首:同平台 id,或同 ISRC(cid 的定義——
// 單曲版 / 專輯版同一個 ISRC 也算)。保留第一次出現的那份、其餘拿掉,剩下的相對順序與 rank 一個都不動:清單順序是
// 使用者的記憶(CLAUDE.md 硬約束),去重永遠不是重排。
// 兩條路徑:
//   - <provider>:<清單>:直接讀平台清單、只報告。不碰 Drive、不需要連結;要由 capy 移除,連到正本再走下一種寫法。
//   - canonical 清單(name|pid):pl sync 的一輪中間多一步——pull 半邊先把平台現況吸進 C(push 的兩個前提靠它)→ C 去重
//     → push 半邊把多出來的份從可寫的平台拿掉。一張表(dir 多一種 dedup)、一次確認、閾值(去重與 push 各算)、
//     exit code 同 pl sync。C 沒有重複、平台也沒有 → 「沒有重複」、零寫入,不會退化成一次普通的 sync。
//     C 早已去重、寫不了的平台還留著多的份時,DERIVE 規則 4′ 不會把它加回來;那幾份列在 stderr 請使用者手動刪。
// 同 ISRC 不同 id 時 C 記的是第一份(mapping 是第一次觀測到的 id),平台上留下哪個 id 由 push 的 LCS 配對決定——
// 相鄰兩份時留後面那個 id(lcsPairs 對同一個 C item 取 L 順序最後的候選)。曲目一樣、順序一樣,只差 id;測試釘住這個行為。

// errRestrictedPlaylist:pl show / pl dedup 讀到 ErrRestricted 時的說法。
var errRestrictedPlaylist = i18n.Errorf("dedup.err.restricted_playlist")

var dedupReportHeader = []string{"POS", "ID", "TITLE", "ARTISTS", "REASON", "REASON_CODE"}

func newPlDedupCmd() *cobra.Command {
	var dryRun, yes, force bool
	var prov string
	cmd := &cobra.Command{
		Use:   "dedup " + i18n.T("cmd.pl.dedup.args"), // 第一個字是命令名,不翻
		Short: i18n.T("cmd.pl.dedup.short"),
		Long:  i18n.T("cmd.pl.dedup.long"),
		Args:  argsOrPicker(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if prov != "" && !isProviderID(prov) {
				return i18n.Errorf("pl.err.bad_provider", "ids", strings.Join(providerIDs, "|"), "value", strconv.Quote(prov))
			}
			ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
			if len(args) == 1 {
				if p, _, ok := strings.Cut(args[0], ":"); ok && isProviderID(p) { // 平台清單:只報告
					p, ref, err := splitProviderRef(args[0])
					if err != nil {
						return err
					}
					// 帶著寫入形狀的 flag 卻只報告、exit 0,會讓人以為刪了;--dry-run 反過來會讓人以為沒有重複——canonical 路徑有重複是 exit 2,
					// 這裡永遠是 0,同一個 flag 兩種 exit code 不能並存(PR #54 review)
					if yes || force || dryRun || prov != "" {
						return i18n.Errorf("dedup.err.report_only", "platform", p, "ref", ref)
					}
					return dedupReport(cmd, p, ref)
				}
			}
			var deferred error
			applied, touched, pulled, deduped := 0, false, 0, 0
			err := withCanonical(ctx, stderr, func(s *canonState) error {
				targets, err := pullTargets(s, args, false, prov, i18n.T("dedup.pick_title"))
				if err != nil {
					return err
				}
				pl := targets[0]
				pf := newPlatforms(ctx)
				pullRows, blocked, lives, err := observeAndDerive(ctx, s, targets, prov, stderr, pf)
				if err != nil {
					return err
				}
				total := len(pl.Items)
				dedupRows := dedupCanonical(s, pl)
				if removalBlocked(len(dedupRows), total) {
					blocked = append(blocked, i18n.T("dedup.blocked.threshold", "playlist", pl.Name, "count", len(dedupRows), "total", total))
				}
				plans, pushRows, pblocked, refused, err := planPush(ctx, s, targets, prov, stderr, pf, lives, false)
				if err != nil {
					return err
				}
				manual, leftover, unchecked := platformDuplicates(s, pl, lives, plans)
				if len(unchecked) > 0 { // 沒看過的平台不能算進「沒有重複」的結論(PR #54 review):只讀平台那份的 stderr 是使用者唯一會知道的管道
					fmt.Fprintln(stderr, i18n.T("dedup.unchecked", "platforms", strings.Join(unchecked, i18n.T("sep.list")), "count", len(unchecked)))
				}
				if len(dedupRows) == 0 && !leftover {
					msg := i18n.T("dedup.none")
					switch {
					case len(unchecked) > 0 && len(pullRows) > 0:
						msg = i18n.T("dedup.none_checked.pull_pending", "count", len(pullRows))
					case len(unchecked) > 0:
						msg = i18n.T("dedup.none_checked")
					case len(pullRows) > 0:
						msg = i18n.T("dedup.none.pull_pending", "count", len(pullRows))
					}
					fmt.Fprintln(stderr, msg)
					return errSkipCommit // dedup 沒東西去重就一個位元組都不寫
				}
				var rows [][]string
				for _, r := range pullRows {
					rows = append(rows, append([]string{"pull"}, r...))
				}
				for _, r := range dedupRows {
					rows = append(rows, append([]string{"dedup"}, r...))
				}
				for _, r := range pushRows {
					rows = append(rows, append([]string{"push"}, r...))
				}
				if len(rows) > 0 {
					if err := ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), syncHeader, rows, tableOpts(yes)...); err != nil {
						return err
					}
				}
				for _, m := range manual {
					fmt.Fprintln(stderr, m)
				}
				if len(refused) > 0 { // planPush 不 strict 時不會回 refused;同 sync,留著免得哪天改回 strict 而靜靜寫入
					return &BlockedError{Msg: strings.Join(refused, i18n.T("sep.clause"))}
				}
				if blocked = append(blocked, pblocked...); len(blocked) > 0 && !force {
					return &BlockedError{Msg: i18n.T("changeset.blocked", "reasons", strings.Join(blocked, i18n.T("sep.clause")))}
				}
				resolveHint(s, targets, prov, stderr)
				ops := 0
				for _, p := range plans {
					ops += len(p.ops)
				}
				n := len(pullRows) + len(dedupRows) + ops
				if n == 0 { // 正本早已去重、多的份只剩在寫不了的平台上(上面 manual 已列)
					fmt.Fprintln(stderr, i18n.T("dedup.no_changes"))
					if dryRun {
						return errSkipCommit
					}
					return nil
				}
				if dryRun {
					return &PendingError{N: n}
				}
				if !yes {
					if !bothTTY(cmd) {
						return &PendingError{N: n}
					}
					prompt := i18n.T("dedup.confirm", "count", n, "dups", len(dedupRows), "pulls", len(pullRows), "pushes", ops)
					if force {
						prompt = i18n.T("dedup.confirm_force", "count", n, "dups", len(dedupRows), "pulls", len(pullRows), "pushes", ops)
					}
					ok, err := confirmWrite(prompt)
					if err != nil {
						return err
					}
					if !ok {
						return &PendingError{N: n}
					}
				}
				applied, touched, deferred = applyPlans(ctx, s, plans, stderr)
				if applied > 0 { // 平台寫入是既成事實;正本那半(去重 + pull)是 Drive 的事,COMMIT 成功後才講
					fmt.Fprintln(stderr, i18n.T("dedup.pushed", "count", applied))
				}
				pulled, deduped = len(pullRows), len(dedupRows)
				return nil
			})
			switch {
			case err != nil:
			case deduped > 0 && pulled > 0:
				fmt.Fprintln(stderr, i18n.T("dedup.removed_and_pulled", "count", deduped, "pulls", i18n.T("dedup.pull_changes", "count", pulled)))
			case deduped > 0:
				fmt.Fprintln(stderr, i18n.T("dedup.removed", "count", deduped))
			case pulled > 0: // 正本沒有要去除的份、多的份只剩在寫不了的平台上,但 pull 半邊有東西落地
				fmt.Fprintln(stderr, i18n.T("dedup.pulled_only", "count", pulled))
			}
			return finishPush(err, applied, touched, deferred, i18n.T("dedup.next"), i18n.T("dedup.next_after_half"))
		},
	}
	cmd.Flags().StringVar(&prov, "provider", "", i18n.T("cmd.pl.dedup.flag.provider"))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("cmd.pl.dedup.flag.dry_run"))
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, i18n.T("cmd.pl.dedup.flag.yes"))
	cmd.Flags().BoolVar(&force, "force", false, i18n.T("cmd.pl.dedup.flag.force"))
	return cmd
}

// dedupReport:<provider>:<清單> 的報告路徑。鍵是 canon.CID 的公式(同 id 或同 ISRC = 同一首),不碰 Drive。
func dedupReport(cmd *cobra.Command, prov, ref string) error {
	ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
	p, err := newProvider(ctx, prov)
	if err != nil {
		return err
	}
	r, err := asPlaylistReader(p)
	if err != nil {
		return err
	}
	id, err := resolvePlaylistID(ctx, r, prov, ref)
	if err != nil {
		return err
	}
	tracks, err := r.GetPlaylistItems(ctx, id)
	switch {
	case errors.Is(err, provider.ErrRestricted):
		return errRestrictedPlaylist
	case errors.Is(err, provider.ErrNotFound):
		tracks = nil // 有列出卻 404 = 空清單(Apple 的 library 端點就這樣回)
	case err != nil:
		return friendlyErr(prov, err)
	}
	keys := make([]string, len(tracks))
	for i, t := range tracks {
		keys[i] = canon.CID(prov, t.ProviderID, t.ISRC)
	}
	dups := canon.Duplicates(keys)
	label := prov + ":" + id
	if ref != id { // 使用者打的是名稱:兩個都印,對得起來(PR #54 review)
		label = i18n.T("dedup.report.label", "platform", prov, "ref", ref, "id", id)
	}
	if len(dups) == 0 {
		fmt.Fprintln(stderr, i18n.T("dedup.report.none", "playlist", label))
		return nil
	}
	rows := make([][]string, len(dups))
	for i, d := range dups {
		t := tracks[d.Pos]
		reason, code := i18n.T("dedup.reason.dup_id", "pos", d.Keep), "dup_id"
		if tracks[d.Keep].ProviderID != t.ProviderID {
			reason, code = i18n.T("dedup.reason.dup_isrc", "pos", d.Keep, "isrc", strings.TrimPrefix(keys[d.Pos], "i:")), "dup_isrc"
		}
		rows[i] = []string{strconv.Itoa(d.Pos), t.ProviderID, t.Title, strings.Join(t.Artists, ", "), reason, code}
	}
	if err := ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), dedupReportHeader, rows); err != nil {
		return err
	}
	if _, err := asPlaylistWriter(p); err != nil {
		fmt.Fprintln(stderr, i18n.T("dedup.report.read_only", "count", len(dups), "platform", prov))
	} else {
		fmt.Fprintln(stderr, i18n.T("dedup.report.link_hint", "count", len(dups), "platform", prov, "id", id))
	}
	return nil
}

// dedupCanonical:把 C 裡第二次以後出現的同一首(cid 經墓碑重導後相同)拿掉,回變更集的列(欄位同 pullHeader,provider 空、
// pos 是 C 的位置)。其餘 item 與 rank 一個都不動——Items 在這裡已是 rank 序(Decode / Derive 都會 normalize)。
func dedupCanonical(s *canonState, pl *canon.Playlist) (rows [][]string) {
	id := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged)
	keys := make([]string, len(pl.Items))
	for i, it := range pl.Items {
		keys[i] = id.Redirect(it.CID)
	}
	dups := canon.Duplicates(keys)
	if len(dups) == 0 {
		return nil
	}
	drop := map[int]bool{}
	for _, d := range dups {
		it := pl.Items[d.Pos]
		t := s.tracks.Tracks[keys[d.Pos]]
		rows = append(rows, []string{"remove", "", pl.Name, strconv.Itoa(d.Pos), it.CID, "", t.Title, strings.Join(t.Artists, ", "), i18n.T("dedup.reason.duplicate", "pos", d.Keep), "duplicate"})
		drop[d.Pos] = true
	}
	kept := make([]canon.Item, 0, len(pl.Items)-len(dups))
	for i, it := range pl.Items {
		if !drop[i] {
			kept = append(kept, it)
		}
	}
	pl.Items = kept
	pl.UpdatedAt = canon.Now().Unix()
	return rows
}

// platformDuplicates:pull 半邊讀到的每個平台清單 L 裡還有幾份重複。有 push 計畫的平台會由 push 拿掉(C 已去重,LCS 配不到的那份就是 remove);
// 沒有計畫的(寫不了、這次推不了)只能請使用者手動刪——那幾份列成 stderr 的句子。leftover = 任一個 L 有重複(不管拿不拿得掉)。
// unchecked = 連著、但這次沒看過的平台(--provider 沒選到、foreign、restricted):「沒看過」不是「沒重複」,呼叫端不能把它算進結論(PR #54 review)。
func platformDuplicates(s *canonState, pl *canon.Playlist, lives map[liveKey]*canon.Observed, plans []*pushPlan) (manual []string, leftover bool, unchecked []string) {
	id := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged)
	for _, prov := range slices.Sorted(maps.Keys(pl.Links)) {
		live, seen := lives[liveKey{pl.PID, prov}]
		if !seen || live == nil {
			unchecked = append(unchecked, prov)
			continue
		}
		lcid, _ := canon.Observe(id, prov, s.tracks.Tracks, live.Tracks)
		dups := canon.Duplicates(lcid)
		if len(dups) == 0 {
			continue
		}
		leftover = true
		if slices.ContainsFunc(plans, func(p *pushPlan) bool { return p.pl.PID == pl.PID && p.prov == prov }) {
			continue
		}
		poss := make([]string, len(dups))
		for i, d := range dups {
			poss[i] = strconv.Itoa(d.Pos)
		}
		manual = append(manual, i18n.T("dedup.manual_leftover", "platform", prov, "id", live.ID, "count", len(dups), "positions", strings.Join(poss, i18n.T("sep.list"))))
	}
	return manual, leftover, unchecked
}
