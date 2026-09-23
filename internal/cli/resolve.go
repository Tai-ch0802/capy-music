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
	"github.com/Tai-ch0802/capy-music/internal/i18n"
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
	return i18n.T("resolve.err.review_needs_tty", "count", e.N)
}

var resolveHeader = []string{"ACTION", "CID", "PROVIDER", "PROVIDER_ID", "CONFIDENCE", "SOURCE", "TITLE", "ARTISTS", "REASON", "REASON_CODE"}

// resolveItem 是佇列的一列:map(待自動寫入)/ review(要人裁決)/ conflict(來源 (c):conflicts 非空且 mapping 未 pinned)。
type resolveItem struct {
	action  string
	cid     string
	prov    string
	track   canon.Track
	cand    *provider.Track // map:要寫的候選;review:最佳候選(可能 nil)
	score   int
	source  string
	reason  string        // 給人看,跟著語系
	code    string        // REASON_CODE 欄:機器可讀,永不翻譯
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
	return []string{it.action, it.cid, it.prov, pid, conf, src, it.track.Title, strings.Join(it.track.Artists, ", "), it.reason, it.code}
}

func describe(t provider.Track) string {
	return i18n.T("resolve.track", "title", t.Title, "artists", strings.Join(t.Artists, ", "), "duration", mmss(t.DurationMS))
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

// findCandidate:Layer 1(alias set 逐個 ISRC 反查)→ Layer 2(模糊搜尋)。回傳候選(nil = 找不到)、分數、來源與打了幾次 API。
func findCandidate(ctx context.Context, c *layerClients, tr canon.Track) (cand *provider.Track, score int, source string, calls int, err error) {
	if c.lookup != nil {
		var cands []provider.Track
		for _, isrc := range tr.ISRC {
			found, err := c.lookup.LookupISRC(ctx, isrc)
			if errors.Is(err, provider.ErrBadISRC) { // 契約:不合格不打 API,所以不計
				continue
			}
			calls++
			if err != nil {
				return nil, 0, "", calls, err
			}
			cands = append(cands, found...)
		}
		if pick, ok := resolve.PickISRC(tr, cands); ok {
			return &pick, isrcConfidence, canon.SourceISRC, calls, nil
		}
	}
	if c.searcher != nil {
		if q := resolve.FuzzyQuery(tr); q != "" {
			found, err := c.searcher.Search(ctx, provider.Query{Text: q, Limit: fuzzyLimit})
			calls++
			if err != nil {
				return nil, 0, "", calls, err
			}
			if ranked := resolve.RankFuzzy(tr, found); len(ranked) > 0 {
				return &ranked[0].Track, ranked[0].Score, canon.SourceFuzzy, calls, nil
			}
		}
	}
	return nil, 0, "", calls, nil
}

// planResolve:對 targets 裡缺 mapping 的 (cid, provider) 跑 Layer 1 → Layer 2,產出佇列;不寫任何東西、不合併。
// 所有權(決策 21):候選 (provider, id) 或其 ISRC 已屬另一個 cid、或這一輪已配給別的 cid → review,自動絕不合併。
// 失敗不是全有全無:provider 級(建不了 client、授權過期——Apple token 本來就會定期失效)只跳過該 provider、stderr 說一次,
// 別的 provider 照解;單次查詢失敗(429 退避後仍失敗、5xx)只讓那筆列成 review、reason 放錯誤,前面幾百次呼叫不白打。
func planResolve(ctx context.Context, s *canonState, targets []*canon.Playlist, only string, stderr io.Writer) ([]resolveItem, error) {
	pls := make([]canon.Playlist, 0, len(targets))
	for _, pl := range targets {
		pls = append(pls, *pl)
	}
	id := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged)
	clients := map[string]*layerClients{}
	claimed := map[string]string{} // provider\x00id → 這一輪配給的 cid
	calls := 0
	failed := map[string]bool{} // provider 級失敗:這輪跳過它
	var items []resolveItem
	needs := resolve.Needs(pls, s.tracks.Tracks)
	if only != "" { // 先篩出這一輪真的要查的,進度的分母才是真的
		needs = slices.DeleteFunc(needs, func(n resolve.Need) bool { return n.Provider != only })
	}
	done, total := 0, len(needs) // 進度只數真的去查的(決策 47):provider 級失敗之後跳過的那些不算做了
	for i, need := range needs {
		if failed[need.Provider] {
			continue
		}
		done++
		reportProgress("match", done, total) // 最久的一段:每一首都是一到數次 API 呼叫
		tr := s.tracks.Tracks[need.CID]
		it := resolveItem{action: "review", cid: need.CID, prov: need.Provider, track: tr}
		c, err := clientsFor(ctx, clients, need.Provider)
		if err == nil {
			var n int
			it.cand, it.score, it.source, n, err = findCandidate(ctx, c, tr)
			calls += n
		}
		switch {
		case err == nil:
		case c == nil || errors.Is(err, provider.ErrAuthExpired):
			failed[need.Provider] = true
			fmt.Fprintln(stderr, i18n.T("resolve.provider_skipped", "provider", need.Provider, "err", friendlyErr(need.Provider, err).Error()))
			// 這個 provider 從這一首起都不會查了:從分母扣掉(含沒查成的這一首)。不扣的話進度條會從這一刻
			// 空轉衝到底,看起來像「很快就比對完了」,其實一首都沒查(review #69)。
			done--
			for _, rest := range needs[i:] {
				if rest.Provider == need.Provider {
					total--
				}
			}
			reportProgress("match", done, total)
			continue
		default:
			it.reason, it.code = i18n.T("resolve.reason.lookup_failed", "err", friendlyErr(need.Provider, err).Error()), "lookup_failed"
			items = append(items, it)
			continue
		}
		switch {
		case it.cand == nil:
			it.reason, it.code = i18n.T("resolve.reason.no_candidate"), "no_candidate"
		default:
			key := need.Provider + "\x00" + it.cand.ProviderID
			switch owner, prev := ownedBy(s, id, need.CID, need.Provider, *it.cand), claimed[key]; {
			case owner != "":
				it.reason, it.code = i18n.T("resolve.reason.candidate_taken", "candidate", describe(*it.cand), "cid", owner), "candidate_taken"
			case prev != "":
				it.reason, it.code = i18n.T("resolve.reason.candidate_assigned", "candidate", describe(*it.cand), "cid", prev), "candidate_assigned"
			case it.score < autoThreshold:
				it.reason, it.code = i18n.T("resolve.reason.low_score", "score", it.score, "threshold", autoThreshold, "candidate", describe(*it.cand)), "low_score"
			default:
				it.action = "map"
				if it.source == canon.SourceISRC {
					it.reason, it.code = i18n.T("resolve.reason.isrc"), "isrc"
				} else {
					it.reason, it.code = i18n.T("resolve.reason.fuzzy", "score", it.score, "candidate", describe(*it.cand)), "fuzzy"
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
						others = append(others, i18n.T("resolve.conflict_id", "id", cf.ProviderID, "title", cf.Title, "duration", mmss(cf.DurationMS)))
					}
				}
				if len(others) == 0 {
					continue
				}
				items = append(items, resolveItem{action: "conflict", cid: item.CID, prov: prov, track: tr, current: m,
					reason: i18n.T("resolve.reason.isrc_conflict", "ids", strings.Join(others, i18n.T("sep.list"))), code: "isrc_conflict"})
			}
		}
	}
	if calls > apiCallHint {
		fmt.Fprintln(stderr, i18n.T("resolve.api_call_hint", "count", calls, "limit", apiCallHint))
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
	title, opts := reviewMenu(it, pos, total)
	kind := "skip"
	if err := newForm(huh.NewGroup(huh.NewSelect[string]().Title(title).Options(opts...).Value(&kind))).Run(); err != nil {
		return reviewDecision{}, err // Esc / Ctrl-C 原樣往上:RunE 把 huh.ErrUserAborted 當「取消 = 整輪不寫入」exit 2
	}
	d := reviewDecision{kind: kind}
	switch kind {
	case "accept":
		d.cand = it.cand
	case "manual":
		q := resolve.FuzzyQuery(it.track)
		if err := newForm(huh.NewGroup(huh.NewInput().Title(i18n.T("resolve.review.search_query")).Value(&q))).Run(); err != nil {
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
			picks[i] = huh.NewOption(i18n.T("resolve.review.pick_item", "score", resolve.ScoreFuzzy(it.track, t), "candidate", describe(t)), i)
		}
		idx := 0
		if err := newForm(huh.NewGroup(huh.NewSelect[int]().Title(i18n.T("resolve.review.pick_title")).Options(picks...).Value(&idx))).Run(); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				return reviewDecision{kind: "skip"}, nil
			}
			return reviewDecision{}, err
		}
		d.cand = &found[idx]
	}
	return d, nil
}

// reviewMenu:reviewPrompt 第一層的標題與選項(純函式:huh 表單要 TTY,測試從這裡看文字)。
func reviewMenu(it resolveItem, pos, total int) (string, []huh.Option[string]) {
	title := i18n.T("resolve.review.title", "pos", pos, "total", total, "title", it.track.Title, "artists", strings.Join(it.track.Artists, ", "),
		"duration", mmss(it.track.DurationMS), "provider", it.prov, "reason", it.reason)
	var opts []huh.Option[string]
	if it.cand != nil {
		opts = append(opts, huh.NewOption(i18n.T("resolve.review.opt.accept", "score", it.score, "candidate", describe(*it.cand)), "accept"))
	}
	if it.action == "conflict" {
		opts = append(opts, huh.NewOption(i18n.T("resolve.review.opt.keep", "id", it.current.ID), "keep"))
	}
	opts = append(opts, huh.NewOption(i18n.T("resolve.review.opt.skip"), "skip"), huh.NewOption(i18n.T("resolve.review.opt.manual"), "manual"),
		huh.NewOption(i18n.T("resolve.review.opt.none"), "none"))
	return title, opts
}

// reviewIsTTY:--review 的 TTY 閘;測試替換點(測試的 stdout 是 buffer)。
var reviewIsTTY = func(cmd *cobra.Command) bool { return bothTTY(cmd) } // 委派、不複製函式值:覆寫 bothTTY 一處七個閘全對齊(review #57)

// pinMapping:把 (prov, m) 釘給 cid。cid 先沿墓碑追——同一輪 --review 的前一筆可能已經把它合併掉(accept 對到已屬另一 cid 的候選),
// 直接寫會把敗者當幽靈 track 復活;勝者不在 tracks 就回錯。回傳實際釘上的 cid。
func pinMapping(s *canonState, cid, prov string, m canon.Mapping) (string, error) {
	cid = canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged).Redirect(cid)
	tr, ok := s.tracks.Tracks[cid]
	if !ok {
		return "", i18n.Errorf("resolve.err.no_cid", "cid", cid)
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
		return "", false, i18n.Errorf("resolve.err.no_cid", "cid", cid)
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
		return i18n.T("resolve.decision.skipped"), false, nil
	case "none":
		if _, err := pinMapping(s, it.cid, it.prov, canon.Mapping{ID: "", Confidence: 100, Pinned: true, Source: canon.SourceReview, UpdatedAt: now}); err != nil {
			return "", false, err
		}
		return i18n.T("resolve.decision.pinned_none"), true, nil
	case "keep": // 釘住「現在」在那個 cid 上的 mapping,不是佇列建好時的那個:前一筆合併可能已經換掉它
		cid := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged).Redirect(it.cid)
		cur, has := s.tracks.Tracks[cid].Mappings[it.prov]
		if !has {
			return i18n.T("resolve.decision.skipped_no_mapping"), false, nil
		}
		if _, err := pinMapping(s, cid, it.prov, canon.Mapping{ID: cur.ID, Confidence: 100, Pinned: true, Source: canon.SourceReview, UpdatedAt: now}); err != nil {
			return "", false, err
		}
		return i18n.T("resolve.decision.kept", "id", cur.ID), true, nil
	case "accept", "manual":
		if d.cand == nil {
			return "", false, i18n.Errorf("resolve.err.no_candidate_to_accept")
		}
		cid, merged, err := pinTrack(s, it.cid, it.prov, *d.cand, confirmMerge)
		if err != nil {
			return "", false, err
		}
		if merged {
			return i18n.T("resolve.decision.pinned_merged", "id", d.cand.ProviderID, "cid", cid), true, nil
		}
		return i18n.T("resolve.decision.pinned", "id", d.cand.ProviderID), true, nil
	}
	return "", false, i18n.Errorf("resolve.err.unknown_decision", "kind", strconv.Quote(d.kind))
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
				err = i18n.Errorf("resolve.err.search_unsupported", "provider", it.prov)
			}
			var found []provider.Track
			if err == nil {
				found, err = c.searcher.Search(ctx, provider.Query{Text: q, Limit: fuzzyLimit})
			}
			if err != nil { // 一次 429 / 未登入不該讓整輪重來(前面的裁決全丟):當搜不到,這筆下次再處理
				fmt.Fprintln(stderr, i18n.T("resolve.review.search_failed", "err", friendlyErr(it.prov, err).Error()))
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
			ok, err := confirmWrite("resolve.review.confirm_merge", i18n.T("resolve.review.confirm_merge", "provider", it.prov, "other", other, "cid", it.cid))
			if errors.Is(err, huh.ErrUserAborted) { // 確認畫面 Esc / Ctrl-C = 不同意合併:這筆略過,不中斷整輪
				return false, nil
			}
			return ok, err
		}
		msg, decided, err := applyDecision(s, it, d, confirm)
		var pend *PendingError
		if errors.As(err, &pend) {
			msg, err = i18n.T("resolve.decision.skipped_not_merged"), nil
		}
		if err != nil {
			return n, err
		}
		if decided {
			n++
		}
		fmt.Fprintln(stderr, i18n.T("resolve.review.progress", "pos", i+1, "total", len(queue), "title", it.track.Title, "result", msg))
	}
	return n, nil
}

func resolveTargets(s *canonState, args []string, prov string) ([]*canon.Playlist, error) {
	if len(args) == 0 {
		return pullTargets(s, nil, true, prov, "") // --all:不開挑選器,標題用不到
	}
	return pullTargets(s, args, false, prov, "") // 這裡 args 一定有一個(上面擋掉 0 個),不開挑選器
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
		fmt.Fprintln(stderr, i18n.T("resolve.hint.unmapped", "count", perProv[p], "provider", p))
	}
}

func newResolveCmd() *cobra.Command {
	var prov string
	var dryRun, yes, review bool
	cmd := &cobra.Command{
		Use:   i18n.T("cmd.resolve.use"),
		Short: i18n.T("cmd.resolve.short"),
		Long:  i18n.T("cmd.resolve.long"),
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stderr, out := cmd.Context(), cmd.ErrOrStderr(), cmd.OutOrStdout()
			if prov != "" && !isProviderID(prov) {
				return i18n.Errorf("resolve.err.unknown_provider", "provider", strconv.Quote(prov), "ids", strings.Join(providerIDs, "|"))
			}
			if review && dryRun {
				return i18n.Errorf("resolve.err.review_with_dry_run")
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
					if err := ui.Table(out, stdoutIsTTY(cmd), resolveHeader, rows, tableOpts(yes)...); err != nil {
						return err
					}
				}
				if review && queued > 0 && !reviewIsTTY(cmd) {
					return &ReviewNeedsTTYError{N: queued}
				}
				if dryRun { // 永不寫入:連 FETCH 自癒的殘留也不上傳
					if pending > 0 {
						return &PendingError{N: pending}
					}
					fmt.Fprintln(stderr, i18n.T("resolve.no_auto_mappings"))
					return errSkipCommit
				}
				applied := 0
				if pending > 0 {
					if !yes {
						if !bothTTY(cmd) {
							return &PendingError{N: pending}
						}
						ok, err := confirmWrite("resolve.confirm_write", i18n.T("resolve.confirm_write", "count", pending))
						if err != nil {
							return err
						}
						if !ok {
							return &PendingError{N: pending}
						}
					}
					applied = applyMappings(s, items)
				} else {
					fmt.Fprintln(stderr, i18n.T("resolve.no_auto_mappings"))
				}
				switch {
				case review && queued > 0:
					n, err := reviewLoop(ctx, s, items, yes, stderr)
					if errors.Is(err, huh.ErrUserAborted) { // 取消 = 整輪不寫入(自動 mapping 與已做的裁決都丟),同 pull 取消:exit 2
						fmt.Fprintln(stderr, i18n.T("resolve.cancelled", "count", applied, "decisions", i18n.T("resolve.review_decisions", "count", n)))
						return &PendingError{N: pending + queued}
					}
					if err != nil {
						return err
					}
					fmt.Fprintln(stderr, i18n.T("resolve.reviewed", "count", n))
				case queued > 0:
					fmt.Fprintln(stderr, i18n.T("resolve.queued", "count", queued))
				}
				if applied > 0 { // 只在真的要 COMMIT 時才說「寫入」:review 迴圈中途出錯上面已 return,不會留下說了謊的過去式
					fmt.Fprintln(stderr, i18n.T("resolve.written", "count", applied))
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&prov, "provider", "", i18n.T("cmd.resolve.flag.provider"))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("cmd.resolve.flag.dry_run"))
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, i18n.T("cmd.resolve.flag.yes"))
	cmd.Flags().BoolVar(&review, "review", false, i18n.T("cmd.resolve.flag.review"))
	cmd.AddCommand(newResolvePinCmd())
	return cmd
}

func newResolvePinCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "pin <cid> <provider>:<id|none>",
		Short: i18n.T("cmd.resolve.pin.short"),
		Long:  i18n.T("cmd.resolve.pin.long"),
		Args:  cobra.ExactArgs(2),
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
					return i18n.Errorf("resolve.err.no_cid_pull", "cid", args[0])
				}
				if ref == "none" {
					if _, err := pinMapping(s, cid, prov, canon.Mapping{ID: "", Confidence: 100, Pinned: true, Source: canon.SourceReview, UpdatedAt: canon.Now().Unix()}); err != nil {
						return err
					}
					fmt.Fprintln(stderr, i18n.T("resolve.pin.done_none", "cid", cid, "provider", prov))
					return nil
				}
				t := provider.Track{ProviderID: ref}
				p, err := newProvider(ctx, prov)
				if err != nil {
					return err
				}
				if g, ok := p.(provider.TrackGetter); ok {
					if t, err = g.GetTrack(ctx, ref); err != nil {
						return i18n.Errorf("resolve.err.pin_unreadable", "provider", prov, "id", ref, "err", friendlyErr(prov, err))
					}
				} else {
					fmt.Fprintln(stderr, i18n.T("resolve.pin.unverified", "provider", prov))
				}
				confirm := func(other string) (bool, error) {
					fmt.Fprintln(stderr, i18n.T("resolve.pin.merge_notice", "provider", prov, "id", ref, "other", other, "cid", cid))
					if yes {
						return true, nil
					}
					if !bothTTY(cmd) {
						return false, nil
					}
					return confirmWrite("resolve.pin.confirm_merge", i18n.T("resolve.pin.confirm_merge"))
				}
				got, merged, err := pinTrack(s, cid, prov, t, confirm)
				if err != nil {
					return err
				}
				if merged {
					fmt.Fprintln(stderr, i18n.T("resolve.pin.merged", "cid", got))
				}
				fmt.Fprintln(stderr, i18n.T("resolve.pin.done", "cid", got, "provider", prov, "id", ref))
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, i18n.T("cmd.resolve.pin.flag.yes"))
	return cmd
}
