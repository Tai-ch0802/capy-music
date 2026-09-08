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
			fmt.Fprintf(s.stderr, "警告:ISRC 衝突 %s:%s 的 %s(%s)與既有 metadata 不符,已記進 tracks.json 的 conflicts(spec §6.2)\n", cid, c.Provider, c.Title, c.ProviderID)
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
// strict:明說 --provider 卻寫不了(Apple)是錯(push);sync 不是——「sync Apple」在 T6 前就是只 pull,stderr 說明後照常。
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
					return nil, nil, nil, nil, fmt.Errorf("%s 的 %s:%s 屬於裝置 %s,不是這台的本機清單(要在這台接手:capy pl link %s %s:<檔名>)", pl.Name, prov, link, deviceName(s, link), pl.Name, prov)
				}
				if strict { // 單獨 push 沒人說過;sync 的 pull 半邊已經說過一次,不重印
					fmt.Fprintf(stderr, "跳過 %s 的 %s:%s 屬於裝置 %s(要在這台接手:capy pl link %s %s:<檔名>)\n", pl.Name, prov, link, deviceName(s, link), pl.Name, prov)
				}
				continue
			}
			w, err := asPlaylistWriter(p)
			if err != nil {
				if strict && only == prov { // 明說要推這個平台才算錯;--all / 沒指定時只跳過(Apple 在 T6 前寫不了)
					return nil, nil, nil, nil, err
				}
				fmt.Fprintf(stderr, "跳過 %s 的 %s:%v\n", pl.Name, prov, err)
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
				fmt.Fprintf(stderr, "跳過 %s 的 %s 的 push 半邊:%s\n", pl.Name, prov, msg)
			}
			ref, ok := refs[link]
			if !ok {
				refuse(fmt.Sprintf("%s 端找不到清單 %s(%s),先 capy pl pull %s(會取消連結)", prov, link, pl.Name, pl.Name))
				continue
			}
			b, ok := merged[pl.PID][prov]
			if !ok || b.Snapshot.ID != link { // 前提一
				refuse(fmt.Sprintf("%s 的 %s 還沒 pull 過(沒有 base),先 capy pl pull %s", pl.Name, prov, pl.Name))
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
					fmt.Fprintf(stderr, "跳過 %s 的 %s:%s(開發模式 app 讀不到 Spotify 官方 / 他人的清單,也寫不了)\n", pl.Name, prov, link)
					continue
				case errors.Is(err, provider.ErrNotFound):
					tracks = nil
				case err != nil:
					return nil, nil, nil, nil, fmt.Errorf("讀取 %s 的 %s:%s:%w", pl.Name, prov, link, friendlyErr(prov, err))
				}
				live = canon.Observed{ID: link, Name: ref.Name, Tracks: tracks}
			}
			tracks := live.Tracks
			lcid, snap := observeLive(s, prov, live)
			if !liveUnchanged(b.Snapshot, snap) { // 前提二
				refuse(fmt.Sprintf("%s 在 %s 有未 pull 的變更,先 capy pl pull %s(或 capy pl sync)", pl.Name, prov, pl.Name))
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
				refuse(fmt.Sprintf("%s 在 %s 有 %d 首 local file(%s),整批取代會把它們弄丟,這個清單暫不支援 push", pl.Name, prov, len(local), strings.Join(local, "、")))
				continue
			}
			if removalBlocked(plan.removes, len(tracks)) {
				blocked = append(blocked, fmt.Sprintf("%s 在 %s 要移除 %d 首(平台 %d 首),超過閾值", pl.Name, prov, plan.removes, len(tracks)))
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
	row := func(action string, pos int, cid, id, reason string) {
		t := s.tracks.Tracks[cid]
		rows = append(rows, []string{action, plan.prov, plan.pl.Name, strconv.Itoa(pos), cid, id, t.Title, strings.Join(t.Artists, ", "), reason})
	}
	for _, op := range plan.ops {
		switch op.Kind {
		case provider.OpRemove:
			cid := work[op.Pos]
			work, ids = slices.Delete(work, op.Pos, op.Pos+1), slices.Delete(ids, op.Pos, op.Pos+1)
			row("remove", op.Pos, cid, op.ProviderID, "canonical 已移除")
		case provider.OpMove:
			cid, id := work[op.From], ids[op.From]
			work = slices.Insert(slices.Delete(work, op.From, op.From+1), op.Pos, cid)
			ids = slices.Insert(slices.Delete(ids, op.From, op.From+1), op.Pos, id)
			row("move", op.Pos, cid, id, "canonical 換序")
		case provider.OpAdd:
			cid := byID[op.ProviderID]
			work, ids = slices.Insert(work, op.Pos, cid), slices.Insert(ids, op.Pos, op.ProviderID)
			row("add", op.Pos, cid, op.ProviderID, "推到平台")
		case provider.OpRename:
			rows = append(rows, []string{"rename", plan.prov, plan.pl.Name, "", "", "", op.Name, "", "canonical 改名:" + plan.liveName + " → " + op.Name})
		}
	}
	for _, sk := range skipped {
		t := s.tracks.Tracks[sk.CID]
		reason, id := sk.Reason, ""
		if m := t.Mappings[plan.prov]; m.ID != "" { // 有 mapping 但推不出去:resolve 修不了,提示也不會算它
			reason, id = "有 mapping 但推不出去(local file / library-only / 檔不在這台),只能在平台手動加", m.ID // ponytail: Pushable 只回 bool,理由三選一由使用者看平台判斷;第三個平台時讓 Pushable 回原因(P6 §2 A10)
		}
		rows = append(rows, []string{"skip", plan.prov, plan.pl.Name, "", sk.CID, id, t.Title, strings.Join(t.Artists, ", "), reason})
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
	for _, op := range skipped { // ponytail: 平台不支援的 op 先印 stderr;Apple append-only(T6)時再決定 manual 列怎麼進表
		if op.Kind == provider.OpRename { // 平台沒改名就不能把 base 記成新名字(A13 的第二道防線:planPush 已不排 rename 給不支援的平台)
			renamed = false
		}
		fmt.Fprintf(stderr, "手動:%s 的 %s 不支援 %s(位置 %d),請在平台上自己做\n", p.pl.Name, p.prov, op.Kind, op.Pos)
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
			fmt.Fprintf(stderr, "警告:%s 在 %s 套用後的內容與預期不同(平台拒收或改了順序),下次 push 會再對一次\n", p.pl.Name, p.prov)
		}
	case written < 0:
		fmt.Fprintf(stderr, "警告:重讀 %s 的 %s 失敗(%v);平台沒動,base 不變\n", p.pl.Name, p.prov, friendlyErr(p.prov, rerr))
		return n, touched, false, werr
	default:
		fmt.Fprintf(stderr, "警告:重讀 %s 的 %s 失敗(%v);base 先記成已寫入的 %d 首,下次 pull 會校正\n", p.pl.Name, p.prov, friendlyErr(p.prov, rerr), written)
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
			why := "於確認期間變了"
			if err != nil {
				why = "套用前重讀失敗:" + err.Error()
			}
			fmt.Fprintf(stderr, "%s 在 %s %s,這份不寫\n", p.pl.Name, p.prov, why)
			stale = append(stale, p.pl.Name+" 在 "+p.prov)
		case err != nil:
			fmt.Fprintf(stderr, "寫入 %s 的 %s 失敗:%v\n", p.pl.Name, p.prov, err)
			if deferred == nil {
				deferred = err
			}
		}
	}
	if deferred == nil && len(stale) > 0 {
		deferred = &BlockedError{Msg: strings.Join(stale, "、") + " 在確認期間有變動,那幾份零寫入;先 capy pl pull 再 push"}
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
		driveMsg = "Drive 上的檔在這次執行期間變了(" + ge.Files + ")"
	default:
		driveMsg = "Drive 沒寫成:" + err.Error()
	}
	switch {
	case driveMsg != "" && deferred != nil:
		return fmt.Errorf("%v;而且 %s——base 沒前進:先 capy pl pull --dry-run 看清楚(平台上少的那截會被列成移除),%s", deferred, driveMsg, nextAfterHalf)
	case driveMsg != "":
		return fmt.Errorf("平台已寫入 %d 筆,但 %s(base 沒前進):%s", applied, driveMsg, next)
	}
	return deferred
}

func newPlPushCmd() *cobra.Command {
	var all, dryRun, yes, force bool
	var prov string
	cmd := &cobra.Command{
		Use:   "push [name|pid]",
		Short: "canonical → 平台:列出變更、確認後才寫(spec §6.5.2)",
		Long: `canonical → 平台(spec §6.5.2)。鏡像 pl pull:變更集先印出(非 TTY 是無標題 TSV,欄位同 pull;action 多了 skip = C 有、平台沒有、
又沒這個平台的 mapping,先 capy resolve),確認後才寫平台;寫完重讀平台現況記成 base,再寫 Drive(canonical 內容不變)。
exit code:0 無變更或已套用、1 錯誤(含平台寫到一半:訊息會說已寫幾首,重跑 push 補回)、2 待套用、3 安全閥。
兩個前提(--force 也不放行):本裝置 pull 過這個平台清單(不然會把平台刪光);平台沒有未 pull 的變更(不然會蓋掉你剛在平台改的)——
先 capy pl pull 或 capy pl sync。含 local file 的 Spotify 清單暫不支援 push。刪除閾值同 pull(分母是平台曲數),--force 越過且只能配單一清單。
「動了幾筆」看 stdout 行數時要扣掉 skip 列。`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return errors.New("指定一個清單(名稱或 pid),或用 --all 推全部已連結的清單")
			}
			if prov != "" && !isProviderID(prov) {
				return fmt.Errorf("provider 為 %s:%q", strings.Join(providerIDs, "|"), prov)
			}
			if force && all {
				return errors.New("--force 只能配單一清單(capy pl push <name> --force),不能配 --all")
			}
			ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
			var deferred error // ApplyOps 失敗 / 確認期間平台變了:base 要落地(COMMIT 要走),所以 fn 回 nil、這裡收尾再回錯
			applied, touched := 0, false
			err := withCanonical(ctx, stderr, func(s *canonState) error {
				targets, err := pullTargets(s, args, all, prov)
				if err != nil {
					return err
				}
				plans, rows, blocked, refused, err := planPush(ctx, s, targets, prov, stderr, newPlatforms(ctx), nil, true)
				if err != nil {
					return err
				}
				if len(rows) > 0 {
					ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), pullHeader, rows)
				}
				if len(refused) > 0 {
					return &BlockedError{Msg: strings.Join(refused, ";")}
				}
				if len(blocked) > 0 && !force {
					return &BlockedError{Msg: strings.Join(blocked, ";") + "。加 --force 越過(先用 --dry-run 看清楚要刪什麼)"}
				}
				resolveHint(s, targets, prov, stderr)
				n := 0
				for _, p := range plans {
					n += len(p.ops)
				}
				if n == 0 {
					fmt.Fprintln(stderr, "無變更")
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
					ok, err := confirmWrite(fmt.Sprintf("推送以上 %d 筆變更到平台?", n))
					if err != nil {
						return err
					}
					if !ok {
						return &PendingError{N: n}
					}
				}
				applied, touched, deferred = applyPlans(ctx, s, plans, stderr)
				if applied > 0 {
					fmt.Fprintf(stderr, "已推送 %d 筆變更\n", applied)
				}
				return nil
			})
			return finishPush(err, applied, touched, deferred, "先 capy pl pull 再 capy pl push", "再 pull、再 push")
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "推全部已連結的清單")
	cmd.Flags().StringVar(&prov, "provider", "", "只推這個 provider 的連結(預設:清單連結的全部 provider)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只列出變更,不碰平台也不碰 Drive(有變更時 exit 2)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳過確認(cron / 管線用);不放行兩個前提")
	cmd.Flags().BoolVar(&force, "force", false, "越過刪除閾值(>10 首,或 >30% 且 >3 首);只能配單一清單、不能配 --all;不放行兩個前提")
	return cmd
}
