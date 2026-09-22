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
var errRestrictedPlaylist = errors.New("無法讀取這個清單的內容 — 可能是追蹤的他人清單(Spotify 2026-02 起只提供 metadata,spec §1.1),也可能是授權不足;先跑 capy doctor 確認授權")

var dedupReportHeader = []string{"POS", "ID", "TITLE", "ARTISTS", "REASON"}

func newPlDedupCmd() *cobra.Command {
	var dryRun, yes, force bool
	var prov string
	cmd := &cobra.Command{
		Use:   "dedup [name|pid | <provider>:<清單 ID 或名稱>]",
		Short: "去掉清單裡重複的曲目(同平台 id 或同 ISRC;保留第一份,其餘順序不動)",
		Long: `重複 = 同平台 id、或同 ISRC(單曲版 / 專輯版算同一首)。保留第一次出現的那份、拿掉後面的,剩下的相對順序一個都不動。

<provider>:<清單 ID 或名稱>:直接讀平台清單、只報告(非 TTY 是無標題 TSV:pos id title artists reason;pos 從 0 起);不碰 Drive、
不需要連結;有沒有重複 exit code 都是 0。要由 capy 移除,把清單連到正本再用下面那種寫法。--yes / --force / --dry-run / --provider 配這種寫法是錯誤。

canonical 清單(name|pid;不帶參數且在終端機裡會開挑選器):pl sync 的一輪中間多一步——先 pull(平台現況吸進正本)、
正本去重、再 push 把多出來的份從可寫的平台拿掉。一張表(非 TTY 是 TSV:dir action provider playlist pos cid provider_id title artists reason,
dir ∈ pull / dedup / push;dedup 列的 pos 是正本裡的位置)、一次確認;exit code 同 pl sync(0 無變更或已套用、1 錯誤、2 待套用、3 安全閥)。
正本與這次檢查的平台都沒有重複時零寫入(pull 半邊看到的其他變更留給 pl sync;--provider 沒選到或讀不到的平台這次沒檢查,stderr 會說)。
刪除閾值去重與 push 各算(>10 首,或 >30% 且 >3 首),--force 越過;寫不了的平台還留著的份會列在 stderr,請手動刪,下一次 pull 不會把它們加回正本。`,
		Args: argsOrPicker(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if prov != "" && !isProviderID(prov) {
				return fmt.Errorf("provider 為 %s:%q", strings.Join(providerIDs, "|"), prov)
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
						return fmt.Errorf("%s:%s 只報告、不改平台(--yes / --force / --dry-run / --provider 在這裡沒有意義);要由 capy 移除:capy pl link <名稱> %s:%s,再 capy pl dedup <名稱>", p, ref, p, ref)
					}
					return dedupReport(cmd, p, ref)
				}
			}
			var deferred error
			applied, touched, pulled, deduped := 0, false, 0, 0
			err := withCanonical(ctx, stderr, func(s *canonState) error {
				targets, err := pullTargets(s, args, false, prov, "去重")
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
					blocked = append(blocked, fmt.Sprintf("%s 要去除 %d 份重複(共 %d 首),超過閾值", pl.Name, len(dedupRows), total))
				}
				plans, pushRows, pblocked, refused, err := planPush(ctx, s, targets, prov, stderr, pf, lives, false)
				if err != nil {
					return err
				}
				manual, leftover, unchecked := platformDuplicates(s, pl, lives, plans)
				if len(unchecked) > 0 { // 沒看過的平台不能算進「沒有重複」的結論(PR #54 review):只讀平台那份的 stderr 是使用者唯一會知道的管道
					fmt.Fprintf(stderr, "%s 這次沒檢查(--provider 沒選到、或讀不到),它上面有沒有重複不在這次的結論裡\n", strings.Join(unchecked, "、"))
				}
				if len(dedupRows) == 0 && !leftover {
					msg := "沒有重複"
					if len(unchecked) > 0 {
						msg = "正本與這次檢查的平台沒有重複"
					}
					if len(pullRows) > 0 {
						msg += fmt.Sprintf("(pull 半邊看到 %d 筆平台變更,留給 capy pl sync)", len(pullRows))
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
					return &BlockedError{Msg: strings.Join(refused, ";")}
				}
				if blocked = append(blocked, pblocked...); len(blocked) > 0 && !force {
					return &BlockedError{Msg: strings.Join(blocked, ";") + "。加 --force 越過(先用 --dry-run 看清楚要刪什麼)"}
				}
				resolveHint(s, targets, prov, stderr)
				ops := 0
				for _, p := range plans {
					ops += len(p.ops)
				}
				n := len(pullRows) + len(dedupRows) + ops
				if n == 0 { // 正本早已去重、多的份只剩在寫不了的平台上(上面 manual 已列)
					fmt.Fprintln(stderr, "無變更")
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
					prompt := fmt.Sprintf("套用以上 %d 筆變更(去除 %d 份重複;pull %d 筆到 Drive、push %d 筆到平台)?", n, len(dedupRows), len(pullRows), ops)
					if force {
						prompt = "--force:刪除會傳播到這個清單連結的每一個平台。" + prompt
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
					fmt.Fprintf(stderr, "已推送 %d 筆變更\n", applied)
				}
				pulled, deduped = len(pullRows), len(dedupRows)
				return nil
			})
			switch {
			case err != nil:
			case deduped > 0 && pulled > 0:
				fmt.Fprintf(stderr, "已去除 %d 份重複(另套用 %d 筆 pull 變更)\n", deduped, pulled)
			case deduped > 0:
				fmt.Fprintf(stderr, "已去除 %d 份重複\n", deduped)
			case pulled > 0: // 正本沒有要去除的份、多的份只剩在寫不了的平台上,但 pull 半邊有東西落地
				fmt.Fprintf(stderr, "已套用 %d 筆 pull 變更(正本沒有要去除的份)\n", pulled)
			}
			return finishPush(err, applied, touched, deferred, "重跑 capy pl dedup 或 capy pl sync(pull 半邊會把平台上已拿掉的那份當平台變更吸收,不會重複)", "再重跑 capy pl dedup")
		},
	}
	cmd.Flags().StringVar(&prov, "provider", "", "只走這個 provider 的 pull / push 半邊(預設:清單連結的全部 provider);正本的去重不分平台")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只列出變更,不碰平台也不碰 Drive(有變更時 exit 2)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳過確認(cron / 管線用)")
	cmd.Flags().BoolVar(&force, "force", false, "越過刪除閾值(去重與 push 各算);去掉的份會在同一個指令裡推到清單連結的其他平台,先 --dry-run")
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
		label = fmt.Sprintf("%s:%s(%s)", prov, ref, id)
	}
	if len(dups) == 0 {
		fmt.Fprintf(stderr, "%s 沒有重複\n", label)
		return nil
	}
	rows := make([][]string, len(dups))
	for i, d := range dups {
		t := tracks[d.Pos]
		reason := fmt.Sprintf("重複:與 pos %d 同 id", d.Keep)
		if tracks[d.Keep].ProviderID != t.ProviderID {
			reason = fmt.Sprintf("重複:與 pos %d 同 ISRC %s", d.Keep, strings.TrimPrefix(keys[d.Pos], "i:"))
		}
		rows[i] = []string{strconv.Itoa(d.Pos), t.ProviderID, t.Title, strings.Join(t.Artists, ", "), reason}
	}
	if err := ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), dedupReportHeader, rows); err != nil {
		return err
	}
	if _, err := asPlaylistWriter(p); err != nil {
		fmt.Fprintf(stderr, "%d 份重複;%s 目前只讀,請照上表在 app 裡手動刪除(pos 從 0 起)\n", len(dups), prov)
	} else {
		fmt.Fprintf(stderr, "%d 份重複;要由 capy 移除:capy pl link <名稱> %s:%s,再 capy pl dedup <名稱>\n", len(dups), prov, id)
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
		rows = append(rows, []string{"remove", "", pl.Name, strconv.Itoa(d.Pos), it.CID, "", t.Title, strings.Join(t.Artists, ", "), fmt.Sprintf("重複:與 pos %d 同一首,保留前面那份", d.Keep)})
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
		manual = append(manual, fmt.Sprintf("%s:%s 還有 %d 份重複(pos %s),這個平台目前寫不了或這次推不了,請在 app 裡手動刪除;正本不會再把它們加回來", prov, live.ID, len(dups), strings.Join(poss, "、")))
	}
	return manual, leftover, unchecked
}
