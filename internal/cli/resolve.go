package cli

// capy resolve(spec §5.3,附錄 C 決策 22–24):把 canonical 曲目對應到各平台的 id。
// Layer 1 ISRC 反查 → Layer 2 模糊比對(internal/resolve 是純函式核心);≥85 自動寫入走 pl pull 同一套 withCanonical,
// 其餘列成 review 佇列(佇列有東西仍 exit 0);--review 在 TTY 逐筆裁決;pin 子命令給腳本用。

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
	"github.com/Tai-ch0802/capy-music/internal/resolve"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

const (
	autoThreshold  = 85 // ≥85 自動寫入(決策 22)
	isrcConfidence = 95 // Layer 1 命中的信心度(決策 23)
	fuzzyLimit     = 10 // Layer 2 搜尋取前 10 筆
)

// apiCallHint:單次 resolve 的 API 呼叫超過這個數就在 stderr 提醒——決策 24 把 negative cache 延後,觸發條件要有人看得到。測試替換點。
var apiCallHint = 200

// ReviewNeedsTTYError:--review 在非 TTY 下——佇列已印成 TSV,exit 2(明確要求人工裁決卻沒有終端機)。
type ReviewNeedsTTYError struct{ N int }

func (e *ReviewNeedsTTYError) Error() string {
	return fmt.Sprintf("%d 筆待人工裁決:--review 需要終端機(佇列已印出;腳本用 capy resolve pin <cid> <provider>:<id|none>)", e.N)
}

var resolveHeader = []string{"ACTION", "CID", "PROVIDER", "PROVIDER_ID", "CONFIDENCE", "SOURCE", "TITLE", "ARTISTS", "REASON"}

// resolveItem 是佇列的一列:map(待自動寫入)/ review(要人裁決)/ conflict(來源 (c):conflicts 非空且 mapping 未 pinned)。
type resolveItem struct {
	action  string
	cid     string
	prov    string
	track   canon.Track
	cand    *provider.Track // map:要寫的候選;review:最佳候選(可能 nil)
	score   int
	source  string
	reason  string
	current canon.Mapping // conflict:現有 mapping
}

func (it resolveItem) row() []string {
	pid, conf, src := "", "", ""
	switch {
	case it.action == "conflict":
		pid, conf, src = it.current.ID, strconv.Itoa(it.current.Confidence), it.current.Source
	case it.cand != nil:
		pid, conf, src = it.cand.ProviderID, strconv.Itoa(it.score), it.source
	}
	return []string{it.action, it.cid, it.prov, pid, conf, src, it.track.Title, strings.Join(it.track.Artists, ", "), it.reason}
}

func describe(t provider.Track) string {
	return fmt.Sprintf("%s — %s(%s)", t.Title, strings.Join(t.Artists, ", "), mmss(t.DurationMS))
}

func mmss(ms int) string {
	s := ms / 1000
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// layerClients:每個 provider 只建一次;沒有 ISRC 反查 / 搜尋 / 單曲能力的層直接跳過。
type layerClients struct {
	lookup   provider.ISRCLookup
	searcher provider.Searcher
	getter   provider.TrackGetter
}

func clientsFor(ctx context.Context, cache map[string]*layerClients, prov string) (*layerClients, error) {
	if c, ok := cache[prov]; ok {
		return c, nil
	}
	p, err := newProvider(ctx, prov)
	if err != nil {
		return nil, err
	}
	c := &layerClients{}
	if l, err := asISRCLookup(p); err == nil {
		c.lookup = l
	}
	if s, err := asSearcher(p); err == nil {
		c.searcher = s
	}
	if g, ok := p.(provider.TrackGetter); ok {
		c.getter = g
	}
	cache[prov] = c
	return c, nil
}

// ownedBy:候選 (prov, id) 或它的 ISRC 依身分規則屬於哪個既有的 cid;是 cid 自己或不屬於任何人就回空字串。
func ownedBy(s *canonState, id *canon.Identity, cid, prov string, t provider.Track) string {
	owner := id.Resolve(prov, t.ProviderID, t.ISRC)
	if owner == cid {
		return ""
	}
	if _, exists := s.tracks.Tracks[owner]; !exists {
		return ""
	}
	return owner
}

// planResolve:對 targets 裡缺 mapping 的 (cid, provider) 跑 Layer 1 → Layer 2,產出佇列;不寫任何東西、不合併。
// 所有權(決策 21):候選 (provider, id) 或其 ISRC 已屬另一個 cid、或這一輪已配給別的 cid → review,自動絕不合併。
func planResolve(ctx context.Context, s *canonState, targets []*canon.Playlist, only string, stderr io.Writer) ([]resolveItem, error) {
	pls := make([]canon.Playlist, 0, len(targets))
	for _, pl := range targets {
		pls = append(pls, *pl)
	}
	id := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged)
	clients := map[string]*layerClients{}
	claimed := map[string]string{} // provider\x00id → 這一輪配給的 cid
	calls := 0
	var items []resolveItem
	for _, need := range resolve.Needs(pls, s.tracks.Tracks) {
		if only != "" && need.Provider != only {
			continue
		}
		tr := s.tracks.Tracks[need.CID]
		c, err := clientsFor(ctx, clients, need.Provider)
		if err != nil {
			return nil, err
		}
		it := resolveItem{action: "review", cid: need.CID, prov: need.Provider, track: tr}
		if c.lookup != nil {
			var cands []provider.Track
			for _, isrc := range tr.ISRC {
				found, err := c.lookup.LookupISRC(ctx, isrc)
				if errors.Is(err, provider.ErrBadISRC) { // 契約:不合格不打 API,所以不計
					continue
				}
				calls++
				if err != nil {
					return nil, friendlyErr(need.Provider, err)
				}
				cands = append(cands, found...)
			}
			if pick, ok := resolve.PickISRC(tr, cands); ok {
				it.cand, it.score, it.source = &pick, isrcConfidence, canon.SourceISRC
			}
		}
		if it.cand == nil && c.searcher != nil {
			if q := resolve.FuzzyQuery(tr); q != "" {
				found, err := c.searcher.Search(ctx, provider.Query{Text: q, Limit: fuzzyLimit})
				calls++
				if err != nil {
					return nil, friendlyErr(need.Provider, err)
				}
				if ranked := resolve.RankFuzzy(tr, found); len(ranked) > 0 {
					it.cand, it.score, it.source = &ranked[0].Track, ranked[0].Score, canon.SourceFuzzy
				}
			}
		}
		switch {
		case it.cand == nil:
			it.reason = "找不到候選"
		default:
			key := need.Provider + "\x00" + it.cand.ProviderID
			switch owner, prev := ownedBy(s, id, need.CID, need.Provider, *it.cand), claimed[key]; {
			case owner != "":
				it.reason = fmt.Sprintf("候選 %s 已屬 cid %s(合併只由人決定:capy resolve pin)", describe(*it.cand), owner)
			case prev != "":
				it.reason = fmt.Sprintf("候選 %s 這一輪已配給 cid %s", describe(*it.cand), prev)
			case it.score < autoThreshold:
				it.reason = fmt.Sprintf("%d 分 < %d:候選 %s", it.score, autoThreshold, describe(*it.cand))
			default:
				it.action = "map"
				if it.source == canon.SourceISRC {
					it.reason = "ISRC 反查"
				} else {
					it.reason = fmt.Sprintf("fuzzy %d 分:%s", it.score, describe(*it.cand))
				}
				claimed[key] = need.CID
			}
		}
		items = append(items, it)
	}
	// 來源 (c):conflicts 非空且該 provider 的 mapping 不是 pinned。pinned 的不浮現(Q14:釘選 = 人已裁決;要改用 resolve pin)。
	seen := map[string]bool{}
	for _, pl := range targets {
		for _, prov := range slices.Sorted(maps.Keys(pl.Links)) {
			if only != "" && prov != only || pl.Links[prov] == "" { // 空字串 = 沒連結,同 resolve.Needs
				continue
			}
			for _, item := range pl.Items {
				k := prov + "\x00" + item.CID
				if seen[k] {
					continue
				}
				seen[k] = true
				tr, ok := s.tracks.Tracks[item.CID]
				if !ok {
					continue
				}
				m, has := tr.Mappings[prov]
				if !has || m.Pinned {
					continue
				}
				var others []string
				for _, cf := range tr.Conflicts {
					if cf.Provider == prov {
						others = append(others, fmt.Sprintf("%s(%s,%s)", cf.ProviderID, cf.Title, mmss(cf.DurationMS)))
					}
				}
				if len(others) == 0 {
					continue
				}
				items = append(items, resolveItem{action: "conflict", cid: item.CID, prov: prov, track: tr, current: m,
					reason: "同 ISRC 觀測到不同 id:" + strings.Join(others, "、") + ";--review 的 keep 釘住現有 mapping"})
			}
		}
	}
	if calls > apiCallHint {
		fmt.Fprintf(stderr, "這次 resolve 打了 %d 次 API(超過 %d):未解開的 cid 每次都會重查;若已放進 cron 請回報,該加決策 24 延後的 negative cache 了\n", calls, apiCallHint)
	}
	return items, nil
}

// applyMappings 寫入 map 列(自動寫入不動 alias set,決策 19)。
func applyMappings(s *canonState, items []resolveItem) int {
	now := canon.Now().Unix()
	n := 0
	for _, it := range items {
		if it.action != "map" {
			continue
		}
		tr := s.tracks.Tracks[it.cid]
		tr.Mappings[it.prov] = canon.Mapping{ID: it.cand.ProviderID, Confidence: it.score, Source: it.source, UpdatedAt: now}
		s.tracks.Tracks[it.cid] = tr
		n++
	}
	return n
}

// reviewDecision:--review 對一筆的裁決。accept / manual 帶候選;none 釘成不可得;keep 釘住現有 mapping;skip 不動。
type reviewDecision struct {
	kind string
	cand *provider.Track
}

// reviewPrompt:TTY 逐筆裁決的 huh 層;search 給 manual 用。測試替換點(同 confirmWrite 慣例),寫入邏輯全在 applyDecision。
var reviewPrompt = func(it resolveItem, pos, total int, search func(string) ([]provider.Track, error)) (reviewDecision, error) {
	title := fmt.Sprintf("[%d/%d] %s — %s(%s)\n%s:%s", pos, total, it.track.Title, strings.Join(it.track.Artists, ", "), mmss(it.track.DurationMS), it.prov, it.reason)
	var opts []huh.Option[string]
	if it.cand != nil {
		opts = append(opts, huh.NewOption(fmt.Sprintf("接受 %d 分候選:%s", it.score, describe(*it.cand)), "accept"))
	}
	if it.action == "conflict" {
		opts = append(opts, huh.NewOption("釘住現有 mapping "+it.current.ID+"(人確認過,之後不再問)", "keep"))
	}
	opts = append(opts, huh.NewOption("略過(下次再問)", "skip"), huh.NewOption("手動搜尋", "manual"), huh.NewOption("這個平台沒有這首(釘成不可得)", "none"))
	kind := "skip"
	if err := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title(title).Options(opts...).Value(&kind))).Run(); err != nil {
		return reviewDecision{}, err // Ctrl-C 原樣往上:RunE 把 huh.ErrUserAborted 當「取消 = 整輪不寫入」exit 2
	}
	d := reviewDecision{kind: kind}
	switch kind {
	case "accept":
		d.cand = it.cand
	case "manual":
		q := resolve.FuzzyQuery(it.track)
		if err := huh.NewForm(huh.NewGroup(huh.NewInput().Title("搜尋字串").Value(&q))).Run(); err != nil {
			return reviewDecision{}, err
		}
		found, err := search(q)
		if err != nil {
			return reviewDecision{}, err
		}
		if len(found) == 0 {
			return reviewDecision{kind: "skip"}, nil
		}
		picks := make([]huh.Option[int], len(found))
		for i, t := range found {
			picks[i] = huh.NewOption(fmt.Sprintf("%d 分  %s", resolve.ScoreFuzzy(it.track, t), describe(t)), i)
		}
		idx := 0
		if err := huh.NewForm(huh.NewGroup(huh.NewSelect[int]().Title("選一首釘上(Ctrl-C 略過)").Options(picks...).Value(&idx))).Run(); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				return reviewDecision{kind: "skip"}, nil
			}
			return reviewDecision{}, err
		}
		d.cand = &found[idx]
	}
	return d, nil
}

// reviewIsTTY:--review 的 TTY 閘;測試替換點(測試的 stdout 是 buffer)。
var reviewIsTTY = bothTTY

// pinMapping:把 (prov, m) 釘給 cid。cid 先沿墓碑追——同一輪 --review 的前一筆可能已經把它合併掉(accept 對到已屬另一 cid 的候選),
// 直接寫會把敗者當幽靈 track 復活;勝者不在 tracks 就回錯。回傳實際釘上的 cid。
func pinMapping(s *canonState, cid, prov string, m canon.Mapping) (string, error) {
	cid = canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged).Redirect(cid)
	tr, ok := s.tracks.Tracks[cid]
	if !ok {
		return "", fmt.Errorf("tracks 沒有 cid %s", cid)
	}
	tr.Mappings[prov] = m
	s.tracks.Tracks[cid] = tr
	return cid, nil
}

// pinTrack:把 (prov, t) 釘給 cid(pinned / 100 / review),t 的 ISRC 進 alias set——alias 只在人工操作時成長(決策 19)。
// t 的 id 或 ISRC 已屬另一個 cid = 兩個 cid 是同一錄音:經 confirmMerge 同意後依決策 21 合併,釘在勝者上;不同意回 PendingError。
// 回傳最後釘上的 cid(合併後可能不是傳入的那個)與是否合併。
func pinTrack(s *canonState, cid, prov string, t provider.Track, confirmMerge func(other string) (bool, error)) (string, bool, error) {
	id := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged)
	cid = id.Redirect(cid)
	if _, ok := s.tracks.Tracks[cid]; !ok {
		return "", false, fmt.Errorf("tracks 沒有 cid %s", cid)
	}
	merged := false
	if other := ownedBy(s, id, cid, prov, t); other != "" {
		ok, err := confirmMerge(other)
		if err != nil {
			return "", false, err
		}
		if !ok {
			return "", false, &PendingError{N: 1}
		}
		survivor, err := canon.Merge(s.tracks, s.playlists, cid, other)
		if err != nil {
			return "", false, err
		}
		cid, merged = survivor, true
	}
	tr := s.tracks.Tracks[cid]
	tr.Mappings[prov] = canon.Mapping{ID: t.ProviderID, Confidence: 100, Pinned: true, Source: canon.SourceReview, UpdatedAt: canon.Now().Unix()}
	if n := canon.NormalizeISRC(t.ISRC); n != "" && !slices.Contains(tr.ISRC, n) {
		tr.ISRC = append(tr.ISRC, n)
		slices.Sort(tr.ISRC)
	}
	s.tracks.Tracks[cid] = tr
	return cid, merged, nil
}

// applyDecision 把一筆裁決寫進 s(決策一律 pinned / 100 / review,之後永久沿用)。回傳給 stderr 的一句話,與這筆算不算裁決(略過不算)。
func applyDecision(s *canonState, it resolveItem, d reviewDecision, confirmMerge func(other string) (bool, error)) (string, bool, error) {
	now := canon.Now().Unix()
	switch d.kind {
	case "skip":
		return "略過", false, nil
	case "none":
		if _, err := pinMapping(s, it.cid, it.prov, canon.Mapping{ID: "", Confidence: 100, Pinned: true, Source: canon.SourceReview, UpdatedAt: now}); err != nil {
			return "", false, err
		}
		return "釘成不可得", true, nil
	case "keep": // 釘住「現在」在那個 cid 上的 mapping,不是佇列建好時的那個:前一筆合併可能已經換掉它
		cid := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged).Redirect(it.cid)
		cur, has := s.tracks.Tracks[cid].Mappings[it.prov]
		if !has {
			return "略過(合併後已沒有這個 provider 的 mapping)", false, nil
		}
		if _, err := pinMapping(s, cid, it.prov, canon.Mapping{ID: cur.ID, Confidence: 100, Pinned: true, Source: canon.SourceReview, UpdatedAt: now}); err != nil {
			return "", false, err
		}
		return "釘住現有 mapping " + cur.ID, true, nil
	case "accept", "manual":
		if d.cand == nil {
			return "", false, errors.New("沒有候選可接受")
		}
		cid, merged, err := pinTrack(s, it.cid, it.prov, *d.cand, confirmMerge)
		if err != nil {
			return "", false, err
		}
		msg := "釘選 " + d.cand.ProviderID
		if merged {
			msg += ",並與另一個 cid 合併為 " + cid
		}
		return msg, true, nil
	}
	return "", false, fmt.Errorf("未知的裁決 %q", d.kind)
}

// reviewLoop:對佇列(非 map 列)逐筆問 reviewPrompt 並套用;不同意合併的那筆當略過,不中斷整輪。回傳裁決筆數(略過不算)。
func reviewLoop(ctx context.Context, s *canonState, items []resolveItem, yes bool, stderr io.Writer) (int, error) {
	clients := map[string]*layerClients{}
	var queue []resolveItem
	for _, it := range items {
		if it.action != "map" {
			queue = append(queue, it)
		}
	}
	n := 0
	for i, it := range queue {
		search := func(q string) ([]provider.Track, error) {
			c, err := clientsFor(ctx, clients, it.prov)
			if err == nil && c.searcher == nil {
				err = fmt.Errorf("%s 不支援搜尋", it.prov)
			}
			var found []provider.Track
			if err == nil {
				found, err = c.searcher.Search(ctx, provider.Query{Text: q, Limit: fuzzyLimit})
			}
			if err != nil { // 一次 429 / 未登入不該讓整輪重來(前面的裁決全丟):當搜不到,這筆下次再處理
				fmt.Fprintf(stderr, "搜尋失敗,這筆先略過:%v\n", friendlyErr(it.prov, err))
				return nil, nil
			}
			return found, nil
		}
		d, err := reviewPrompt(it, i+1, len(queue), search)
		if err != nil {
			return n, err
		}
		confirm := func(other string) (bool, error) {
			if yes {
				return true, nil
			}
			ok, err := confirmWrite(fmt.Sprintf("%s 的這個 id 已屬 cid %s:把 %s 與它合併(勝者字典序小、清單 item 全部改指勝者)?", it.prov, other, it.cid))
			if errors.Is(err, huh.ErrUserAborted) { // 確認畫面 Ctrl-C = 不同意合併:這筆略過,不中斷整輪
				return false, nil
			}
			return ok, err
		}
		msg, decided, err := applyDecision(s, it, d, confirm)
		var pend *PendingError
		if errors.As(err, &pend) {
			msg, err = "略過(未合併)", nil
		}
		if err != nil {
			return n, err
		}
		if decided {
			n++
		}
		fmt.Fprintf(stderr, "[%d/%d] %s:%s\n", i+1, len(queue), it.track.Title, msg)
	}
	return n, nil
}

func resolveTargets(s *canonState, args []string, prov string) ([]*canon.Playlist, error) {
	if len(args) == 0 {
		return pullTargets(s, nil, true, prov)
	}
	return pullTargets(s, args, false, prov)
}

// resolveHint:pl pull 結尾提示尚未對應的曲目數(pull 不做 resolve:API 成本與關注點分離,決策 22)。
// only 非空只提示那個 provider(--provider 那輪沒碰別的平台)。
func resolveHint(s *canonState, targets []*canon.Playlist, only string, stderr io.Writer) {
	pls := make([]canon.Playlist, 0, len(targets))
	for _, pl := range targets {
		pls = append(pls, *pl)
	}
	perProv := map[string]int{}
	for _, n := range resolve.Needs(pls, s.tracks.Tracks) {
		if only == "" || n.Provider == only {
			perProv[n.Provider]++
		}
	}
	for _, p := range slices.Sorted(maps.Keys(perProv)) {
		fmt.Fprintf(stderr, "%d 首尚未對應到 %s,跑 capy resolve\n", perProv[p], p)
	}
}

func newResolveCmd() *cobra.Command {
	var prov string
	var dryRun, yes, review bool
	cmd := &cobra.Command{
		Use:   "resolve [<清單名稱|ID>]",
		Short: "把 canonical 曲目對應到各平台的 id(ISRC 反查 → 模糊比對;≥85 自動寫入,其餘列成 review 佇列)",
		Long: `對每個已連結的 (清單, provider),找出 items 裡缺該 provider mapping 的曲目:先以 ISRC 反查(信心 95),沒有再用
標題 + 藝人 + 時長模糊比對(0–100)。≥85 自動寫入(走 pl pull 同一套:pull.lock、Drive 不完整的閘、Drive 先 SQLite 後),
其餘印成 review 佇列——候選已屬另一個 cid 的一律進佇列,合併只由人決定。
非 TTY 輸出 TSV:action cid provider provider_id confidence source title artists reason(action ∈ map | review | conflict)。
exit code:0 無事可寫或已寫入(佇列有東西仍是 0)、1 錯誤、2 有待寫入的自動 mapping 但沒有確認(--dry-run、非 TTY 沒 --yes、取消)。
--review 在終端機逐筆裁決(接受 / 略過 / 手動搜尋 / 釘成不可得 / 釘住現有);非 TTY 只印佇列並以 exit 2 結束;
裁決完才一起寫入,中途 Ctrl-C 整輪不寫入(含自動 mapping)並以 exit 2 結束。`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stderr, out := cmd.Context(), cmd.ErrOrStderr(), cmd.OutOrStdout()
			if prov != "" && !isProviderID(prov) {
				return fmt.Errorf("不認得 provider %q(%s)", prov, strings.Join(providerIDs, "|"))
			}
			if review && dryRun {
				return errors.New("--review 不能配 --dry-run(裁決一定寫入;先看佇列用 capy resolve --dry-run)")
			}
			return withCanonical(ctx, stderr, func(s *canonState) error {
				targets, err := resolveTargets(s, args, prov)
				if err != nil {
					return err
				}
				items, err := planResolve(ctx, s, targets, prov, stderr)
				if err != nil {
					return err
				}
				var rows [][]string
				pending, queued := 0, 0
				for _, it := range items {
					rows = append(rows, it.row())
					if it.action == "map" {
						pending++
					} else {
						queued++
					}
				}
				if len(rows) > 0 {
					ui.Table(out, stdoutIsTTY(cmd), resolveHeader, rows)
				}
				if review && queued > 0 && !reviewIsTTY(cmd) {
					return &ReviewNeedsTTYError{N: queued}
				}
				if dryRun { // 永不寫入:連 FETCH 自癒的殘留也不上傳
					if pending > 0 {
						return &PendingError{N: pending}
					}
					fmt.Fprintln(stderr, "沒有可自動寫入的 mapping")
					return errSkipCommit
				}
				applied := 0
				if pending > 0 {
					if !yes {
						if !bothTTY(cmd) {
							return &PendingError{N: pending}
						}
						ok, err := confirmWrite(fmt.Sprintf("寫入以上 %d 筆 mapping 到 Drive?", pending))
						if err != nil {
							return err
						}
						if !ok {
							return &PendingError{N: pending}
						}
					}
					applied = applyMappings(s, items)
				} else {
					fmt.Fprintln(stderr, "沒有可自動寫入的 mapping")
				}
				switch {
				case review && queued > 0:
					n, err := reviewLoop(ctx, s, items, yes, stderr)
					if errors.Is(err, huh.ErrUserAborted) { // 取消 = 整輪不寫入(自動 mapping 與已做的裁決都丟),同 pull 取消:exit 2
						fmt.Fprintf(stderr, "已取消:這輪的 %d 筆自動 mapping 與 %d 筆裁決都不寫入\n", applied, n)
						return &PendingError{N: pending + queued}
					}
					if err != nil {
						return err
					}
					fmt.Fprintf(stderr, "裁決 %d 筆\n", n)
				case queued > 0:
					fmt.Fprintf(stderr, "%d 筆待人工裁決:在終端機跑 capy resolve --review,或用 capy resolve pin\n", queued)
				}
				if applied > 0 { // 只在真的要 COMMIT 時才說「寫入」:review 迴圈中途出錯上面已 return,不會留下說了謊的過去式
					fmt.Fprintf(stderr, "寫入 %d 筆 mapping\n", applied)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&prov, "provider", "", "只處理這個 provider(預設:清單連結的全部 provider)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只列出,不寫入(有可自動寫入的 mapping 時 exit 2;不能配 --review)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳過確認(cron / 管線用);--review 時也直接同意合併")
	cmd.Flags().BoolVar(&review, "review", false, "在終端機逐筆裁決 review 佇列(非 TTY:印佇列、exit 2;不能配 --dry-run)")
	cmd.AddCommand(newResolvePinCmd())
	return cmd
}

func newResolvePinCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "pin <cid> <provider>:<id|none>",
		Short: "手動釘選某個 cid 在某平台的 id(none = 這個平台沒有這首);id 已屬另一個 cid 時把兩者合併",
		Long: `釘選寫成 pinned / 信心 100 / source review,之後自動程序不再改它;帶 id 時先向平台讀那首曲目確認存在,並把它的 ISRC 加進
alias set。id(或它的 ISRC)已屬另一個 cid = 兩者是同一錄音:確認後依決策 21 合併(勝者字典序小、清單 item 全部改指勝者);
非 TTY 要 --yes,否則以 exit 2 結束、零寫入。`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
			prov, ref, err := splitProviderRef(args[1])
			if err != nil {
				return err
			}
			return withCanonical(ctx, stderr, func(s *canonState) error {
				id := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged)
				cid := id.Redirect(args[0])
				if _, ok := s.tracks.Tracks[cid]; !ok {
					return fmt.Errorf("tracks 沒有 cid %s(先 capy pl pull)", args[0])
				}
				if ref == "none" {
					if _, err := pinMapping(s, cid, prov, canon.Mapping{ID: "", Confidence: 100, Pinned: true, Source: canon.SourceReview, UpdatedAt: canon.Now().Unix()}); err != nil {
						return err
					}
					fmt.Fprintf(stderr, "已釘選:%s 在 %s 不可得\n", cid, prov)
					return nil
				}
				t := provider.Track{ProviderID: ref}
				p, err := newProvider(ctx, prov)
				if err != nil {
					return err
				}
				if g, ok := p.(provider.TrackGetter); ok {
					if t, err = g.GetTrack(ctx, ref); err != nil {
						return fmt.Errorf("%s 上讀不到曲目 %s(id 打錯?):%w", prov, ref, friendlyErr(prov, err))
					}
				} else {
					fmt.Fprintf(stderr, "%s 不支援讀取單曲:未驗證 id,alias set 不更新\n", prov)
				}
				confirm := func(other string) (bool, error) {
					fmt.Fprintf(stderr, "%s:%s 已屬 cid %s:釘選等於把 %s 與 %s 合併(勝者字典序小、清單 item 全部改指勝者)\n", prov, ref, other, cid, other)
					if yes {
						return true, nil
					}
					if !bothTTY(cmd) {
						return false, nil
					}
					return confirmWrite("合併這兩個 cid?")
				}
				got, merged, err := pinTrack(s, cid, prov, t, confirm)
				if err != nil {
					return err
				}
				if merged {
					fmt.Fprintf(stderr, "已合併,勝者 %s\n", got)
				}
				fmt.Fprintf(stderr, "已釘選:%s 在 %s = %s\n", got, prov, ref)
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "id 已屬另一個 cid 時直接合併(非 TTY 沒給會以 exit 2 結束、零寫入)")
	return cmd
}
