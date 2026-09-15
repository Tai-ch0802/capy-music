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
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// capy migrate(2026-09-15,計畫 docs/superpowers/plans/2026-09-15-order-dedup-migrate.md §3、附錄 C 決策 39):把 A 平台的一個清單搬
// (複製)到 B 平台,使用者不必自己串 link / pull / resolve / push。一個 withCanonical 裡依序:讀 A → 決定正本 C 與 B(既有的 B 先 pull 進 C)
// → 把 A 裡 C 還沒有的依 A 的順序接在尾端(同平台 id / 同 ISRC 的略過,A 自己的重複也只留一份)→ resolve 到 B(ISRC 反查 → 模糊比對;
// TTY 可當場逐筆裁決)→ 一張表、一次確認 → 需要時才在 B 建清單 → push。
// 順序:B(或 C)原本的順序是前綴、A 的曲目依 A 的順序接在後面——明確建,不靠 DERIVE(沒有 base 的 bootstrap 會採平台順序)。
// A 不連結 canonical(使用者定案):一次性複製,之後 pl sync 不會因 bootstrap 把 B 重排成 A 的順序;要持續同步走 README 的 link + sync。
// 永遠不刪 A;對 B 只做 add——B 有待同步的移除 / 換序 / 改名時擋下(exit 3),先 pl sync。

// migrateIsTTY:確認與當場裁決的 TTY 閘;測試替換點(同 reviewIsTTY 慣例)。
var migrateIsTTY = bothTTY

// migrateEnd:一端的平台清單。id 空 = 目標要新建(名字跟來源)。
type migrateEnd struct{ prov, id, name string }

func (e migrateEnd) String() string { return e.prov + ":" + e.id }

func newMigrateCmd() *cobra.Command {
	var from, to string
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "migrate [來源清單 ID 或名稱] --from <平台> --to <平台>[:<既有清單 ID 或名稱>]",
		Short: "把一個平台的清單搬到另一個平台(新建或加進既有清單);順序不動、不刪來源、只新增",
		Long: `把 --from 平台的清單複製到 --to 平台,不用自己串 pl link / pull / resolve / push。--to 只給平台 = 在那裡建一個跟來源同名的私人清單
(Spotify 才能建);--to <平台>:<清單> = 加進既有清單。不帶參數且在終端機裡會逐段挑選(來源平台 → 清單 → 目標平台 → 既有清單或建新的);
非 TTY 要給清單與 --from / --to。

順序:目標原本的順序是前綴,來源的曲目依來源的順序接在後面;來源裡目標已經有的(同平台 id 或同 ISRC)略過,來源自己的重複也只留一份。
永遠不動來源;對目標只做新增——目標有還沒同步的移除 / 換序 / 改名時以 exit 3 擋下,先 capy pl sync。
每一首先用 ISRC 反查、再模糊比對(≥85 自動);沒對到的這次不推,表裡會說,終端機裡可以當場逐筆裁決。
一張表(非 TTY 是 TSV:dir action provider playlist pos cid provider_id title artists reason;dir ∈ pull / migrate / push)、一次確認;
--dry-run 只列(有東西時 exit 2、不建清單);非 TTY 沒 --yes 也是 exit 2。確認之後才在目標平台建清單。
完成後只有目標連著 capy 的正本(來源不連結,一次性複製);要持續同步,結尾會給 pl link + pl sync 的命令。
目標不能是 Apple(目前只讀);local 只能加進既有檔(--to local:<檔名>)。`,
		Args: argsOrPicker(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runMigrate(cmd, args, from, to, dryRun, yes) },
	}
	cmd.Flags().StringVar(&from, "from", "", "來源平台("+strings.Join(providerIDs, "|")+");終端機裡不給會挑選")
	cmd.Flags().StringVar(&to, "to", "", "目標平台,或 <平台>:<既有清單 ID 或名稱>;只給平台 = 建一個跟來源同名的新清單(Spotify);終端機裡不給會挑選")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只列出要搬什麼,不建清單、不碰平台也不碰 Drive(有東西時 exit 2)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳過確認(cron / 管線用);不當場裁決沒對到的曲目")
	return cmd
}

func runMigrate(cmd *cobra.Command, args []string, from, to string, dryRun, yes bool) error {
	ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
	interactive := isInteractive(cmd)
	if !interactive && (from == "" || to == "") {
		return errors.New("--from 與 --to 必填(非終端機沒有挑選器):capy migrate <清單> --from apple --to spotify[:<既有清單>]")
	}
	// 來源
	var err error
	if from == "" {
		if from, err = pickProvider("從哪個平台搬?"); err != nil {
			return err
		}
	}
	if !isProviderID(from) {
		return fmt.Errorf("--from 為 %s:%q", strings.Join(providerIDs, "|"), from)
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
		if dst.prov, err = pickProvider("搬到哪個平台?"); err != nil {
			return err
		}
	case strings.Contains(to, ":"):
		if dst.prov, toRef, err = splitProviderRef(to); err != nil {
			return err
		}
	case isProviderID(to):
		dst.prov = to
	default:
		return fmt.Errorf("--to 為 <平台> 或 <平台>:<既有清單>,平台為 %s:%q", strings.Join(providerIDs, "|"), to)
	}
	pB, err := newProvider(ctx, dst.prov)
	if err != nil {
		return err
	}
	wB, err := asPlaylistWriter(pB)
	if err != nil {
		return fmt.Errorf("%s 目前只讀,不能當 migrate 的目標:%w", dst.prov, err)
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
			return fmt.Errorf("%s:%s 不在你的清單列表裡(capy pl list 看得到的才算),不能當目標", dst.prov, dst.id)
		}
		if ok, err := readable(ctx, rB, dst.id); err != nil {
			return friendlyErr(dst.prov, err)
		} else if !ok {
			return fmt.Errorf("%s 清單 %s 讀不到內容(開發模式 app 拿不到 Spotify 官方 / 他人的清單),不能當目標", dst.prov, dst.id)
		}
	case to == "": // 挑選器:既有的清單,或建一個新的
		newLabel := ""
		if cerr == nil {
			newLabel = "+ 在 " + dst.prov + " 建一個跟來源同名的新清單"
		}
		if dst.id, err = pickPlatformPlaylist(dst.prov, refsB, newLabel); err != nil {
			return err
		}
	}
	if dst.id == "" {
		if cerr != nil {
			return fmt.Errorf("%s 不能建清單,只能加進既有的:--to %s:<清單 ID 或名稱>(%v)", dst.prov, dst.prov, cerr)
		}
		dup, err := sameNamePlaylists(ctx, rB, refsB, src.name)
		if err != nil {
			return friendlyErr(dst.prov, err)
		}
		if len(dup) > 0 {
			return fmt.Errorf("%s 上已經有叫「%s」的清單(%s):要加進它就 --to %s:%s;真的要另建一個,先在 app 裡建好再用 --to %s:<ID>", dst.prov, src.name, strings.Join(dup, "、"), dst.prov, dup[0], dst.prov)
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
		fmt.Fprintf(stderr, "%s 是空的,沒有東西可搬\n", src)
		return nil
	}
	target := func() string {
		if dst.id == "" {
			return dst.prov + ":" + dst.name + "(新建)"
		}
		return dst.String()
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
		var lives map[liveKey]*canon.Observed
		if dst.id != "" { // 既有的 B 先吸進 C:C 之後就是 B 的原樣(前綴),push 的兩個前提也靠這一步
			rows, blocked, lv, err := observeAndDerive(ctx, s, targets, dst.prov, stderr, pf)
			if err != nil {
				return err
			}
			if len(blocked) > 0 {
				return &BlockedError{Msg: strings.Join(blocked, ";") + "。migrate 不越過刪除閾值:先 capy pl sync " + pl.Name + " 處理那邊的變更,再 migrate"}
			}
			if pl.Links[dst.prov] != dst.id {
				return fmt.Errorf("%s 端找不到清單 %s(已取消連結),重跑一次", dst.prov, dst.id)
			}
			pullRows, lives = rows, lv
		}
		// A 的曲目:C 還沒有的依 A 的順序接在尾端;同一首(同平台 id 或同 ISRC)只留一份
		id := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged)
		lcid, updated := canon.Observe(id, src.prov, s.tracks.Tracks, tracksA)
		s.absorb(updated)
		have := map[string]bool{}
		for _, it := range pl.Items {
			have[id.Redirect(it.CID)] = true
		}
		type appended struct {
			pos   int
			cid   string
			track provider.Track
		}
		var added []appended
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
		existing := len(pl.Items) - len(added) // 正本既有的份數:新建的目標要把它們一起推過去(沿用了連著來源的正本時就是全部);既有的目標它們已在上面
		if len(added) == 0 && dst.id != "" {   // 沒東西可接就一個位元組都不寫:pull 半邊看到的變更留給 sync(同 pl dedup 的規矩),不退化成一次 sync
			msg := fmt.Sprintf("%s 的 %d 首都已在正本 %s(%s)裡,無變更", src, len(tracksA), pl.Name, pl.PID)
			if len(pullRows) > 0 {
				msg += fmt.Sprintf("(pull 半邊看到 %d 筆平台變更,留給 capy pl sync)", len(pullRows))
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
			fmt.Fprintf(stderr, "resolve:%d 首自動對應到 %s,%d 首要人裁決\n", mapped, dst.prov, queued)
		}
		if queued > 0 && !yes && !dryRun && migrateIsTTY(cmd) {
			ok, err := confirmWrite(fmt.Sprintf("%d 首在 %s 沒有自動對應到,現在逐筆裁決?(否 = 先推有對應的,之後 capy resolve %q --provider %s --review)", queued, dst.prov, pl.Name, dst.prov))
			if err != nil {
				return err
			}
			if ok {
				n, err := reviewLoop(ctx, s, items, false, stderr)
				if errors.Is(err, huh.ErrUserAborted) { // 取消 = 整輪不寫入(同 resolve --review):清單也還沒建
					fmt.Fprintln(stderr, "已取消:這輪不寫入任何東西")
					return &PendingError{N: len(added) + len(pullRows)}
				}
				if err != nil {
					return err
				}
				fmt.Fprintf(stderr, "裁決 %d 筆\n", n)
			}
		}
		// 表:pull(既有 B 的變更)、migrate(接在尾端的來源曲目)、push(既有 B 才算得出來;新建的要建了才有 id)
		var rows [][]string
		for _, r := range pullRows {
			rows = append(rows, append([]string{"pull"}, r...))
		}
		unmapped := 0
		for _, a := range added {
			m := s.tracks.Tracks[a.cid].Mappings[dst.prov]
			var reason string
			switch {
			case m.ID != "" && wB.Pushable(m.ID):
				reason = fmt.Sprintf("推到 %s:%s(%s %d)", dst.prov, m.ID, m.Source, m.Confidence)
			case m.ID != "":
				reason, unmapped = "有 mapping 但推不出去(local file / library-only),只能在平台手動加", unmapped+1
			default:
				reason, unmapped = dst.prov+" 沒有對應,這次不推", unmapped+1
			}
			rows = append(rows, []string{"migrate", "add", src.prov, pl.Name, strconv.Itoa(a.pos), a.cid, a.track.ProviderID, a.track.Title, strings.Join(a.track.Artists, ", "), reason})
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
			prompt := fmt.Sprintf("把 %s 的 %d 首加進 %s?", src, len(added), target())
			if unmapped > 0 {
				prompt += fmt.Sprintf("(其中 %d 首在 %s 沒有對應,這次不推)", unmapped, dst.prov)
			}
			if dst.id == "" && existing > 0 {
				prompt += fmt.Sprintf("(正本既有的 %d 首會一起推到新清單)", existing)
			}
			ok, err := confirmWrite(prompt)
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
			fmt.Fprintf(stderr, "在 %s 建立清單 %s(%s)\n", dst.prov, made.Name, made.ID)
			// 重新 list 才看得到剛建的清單;讀到空清單、記下 base(push 前提一);沒有 base 又是空的 L,DERIVE 不會動 C
			pf2 := newPlatforms(ctx)
			if _, _, lives, err = observeAndDerive(ctx, s, targets, dst.prov, stderr, pf2); err != nil {
				return err
			}
			if pl.Links[dst.prov] != made.ID { // 剛建的清單不在第二次 list 裡(真帳號還沒驗過的路徑):被當 gone 取消連結;收尾會帶 pl link 接回的命令
				return fmt.Errorf("%s 端的清單列表看不到剛建的清單 %s,已取消連結", dst.prov, made.ID)
			}
			if plans, _, err = migratePlanPush(ctx, s, targets, dst.prov, stderr, pf2, lives); err != nil {
				return err
			}
		}
		applied, touched, deferred = applyPlans(ctx, s, plans, stderr)
		if applied > 0 {
			fmt.Fprintf(stderr, "已推送 %d 筆變更\n", applied)
		}
		summary = fmt.Sprintf("已把 %s 的 %d 首接進正本 %s(%s),推了 %d 首到 %s\n", src, len(added), pl.Name, pl.PID, applied, dst)
		if created != "" && existing > 0 {
			summary += fmt.Sprintf("正本既有的 %d 首一起推到新清單了\n", existing)
		}
		if unmapped > 0 {
			summary += fmt.Sprintf("%d 首在 %s 沒有對應、這次沒推:capy resolve %q --provider %s --review 裁決後,capy pl sync %q --provider %s 推過去\n", unmapped, dst.prov, pl.Name, dst.prov, pl.Name, dst.prov)
		}
		summary += fmt.Sprintf("來源沒有連結(一次性複製)。之後要跟著 %s 的變動:capy pl link %q %s:%s,再 capy pl sync %q\n", src.prov, pl.Name, src.prov, src.id, pl.Name)
		return nil
	})
	if err == nil && deferred == nil { // push 失敗(deferred)時 COMMIT 照走、但結尾要以它收場(同 push / sync 經 finishPush);成功才講成功
		fmt.Fprint(stderr, summary)
	}
	next := fmt.Sprintf("重跑 capy migrate %q --from %s --to %s:%s(已推到平台的曲目會當既有內容吸收,不會重複)", src.name, src.prov, dst.prov, dst.id)
	after := "再重跑 capy migrate"
	if created != "" { // 清單建好了、連結卻沒寫進 Drive:重跑會撞同名,要指到已建好的那個
		next = fmt.Sprintf("%s 上的清單 %s(%s)已經建好,但連結沒寫進 Drive:用 capy pl link %q %s:%s 接回來,再 capy pl sync %q --provider %s", dst.prov, dst.name, created, plName, dst.prov, created, plName, dst.prov)
		after = fmt.Sprintf("再 capy pl link %q %s:%s 接回來並 capy pl sync %q --provider %s", plName, dst.prov, created, plName, dst.prov)
		if err != nil && !touched {
			err = fmt.Errorf("%w;%s", err, next)
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
				fmt.Fprintf(stderr, "沿用 %s 連著的 canonical 清單 %s(%s)\n", dst, pl.Name, pl.PID)
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
		fmt.Fprintf(stderr, "建立 canonical 清單 %s(%s)\n", pl.Name, pl.PID)
	case pl.Links[dst.prov] != "" && dst.id == "":
		return nil, fmt.Errorf("已經有叫「%s」的 canonical 清單(%s)連著 %s:%s:要加進那個清單就 --to %s:%s", pl.Name, pl.PID, dst.prov, pl.Links[dst.prov], dst.prov, pl.Links[dst.prov])
	case pl.Links[dst.prov] != "":
		return nil, fmt.Errorf("同名的 canonical 清單 %s(%s)已連結 %s:%s,不是 %s:先 capy pl unlink %q %s", pl.Name, pl.PID, dst.prov, pl.Links[dst.prov], dst.id, pl.Name, dst.prov)
	default:
		links := "還沒連任何平台"
		if len(pl.Links) > 0 {
			links = "連著 " + linkSummary(pl)
		}
		fmt.Fprintf(stderr, "沿用既有的 canonical 清單 %s(%s;%s)\n", pl.Name, pl.PID, links)
	}
	if dst.id != "" && pl.Links[dst.prov] != dst.id {
		pl.Links[dst.prov] = dst.id
		pl.UpdatedAt = canon.Now().Unix()
		fmt.Fprintf(stderr, "已連結 %s(%s)↔ %s\n", pl.Name, pl.PID, dst)
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
		return nil, nil, &BlockedError{Msg: strings.Join(refused, ";")}
	}
	for _, p := range plans {
		for _, op := range p.ops {
			if op.Kind != provider.OpAdd {
				return nil, nil, &BlockedError{Msg: fmt.Sprintf("%s 在 %s 有還沒同步的 %s(migrate 只做新增):先 capy pl sync %q --provider %s,再 migrate", p.pl.Name, p.prov, op.Kind, p.pl.Name, p.prov)}
			}
		}
	}
	return plans, rows, nil
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
