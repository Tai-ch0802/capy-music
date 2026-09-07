package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/huh/v2"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// candidate:play 的一個可播放目標。Type 用 cache 的常數(track / artist / playlist)。
type candidate struct {
	Type   string
	ID     string
	Label  string // 曲名 / 藝人名 / 清單名
	Detail string // 曲目:藝人 · 專輯;藝人:熱門歌曲;清單:N 首
	Note   string // 只給人看的附註(例如「暫不支援清單播放」);不進快取
	Total  int
}

// AmbiguousError:多個候選且無法互動(非 TTY)。候選已印在 stdout,main 以 exit 2 結束、不再印訊息。
type AmbiguousError struct{ Candidates []candidate }

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("有 %d 個候選;用 --type track|artist|playlist 或前綴 artist: / pl: / track: 指定,或在終端機執行以開啟挑選器", len(e.Candidates))
}

var playPrefixes = map[string]string{"artist:": cache.TypeArtist, "pl:": cache.TypePlaylist, "playlist:": cache.TypePlaylist, "track:": cache.TypeTrack}

// parsePlayQuery:前綴(artist: / pl: / track:)是 --type 的簡寫;兩者同時給且不同 → 錯。
func parsePlayQuery(args []string, typ string) (string, string, error) {
	q := strings.TrimSpace(strings.Join(args, " "))
	for pre, t := range playPrefixes {
		if strings.HasPrefix(strings.ToLower(q), pre) {
			if typ != "" && typ != t {
				return "", "", fmt.Errorf("前綴 %s 與 --type %s 衝突", pre, typ)
			}
			return strings.TrimSpace(q[len(pre):]), t, nil
		}
	}
	switch typ {
	case "", cache.TypeTrack, cache.TypeArtist, cache.TypePlaylist:
		return q, typ, nil
	}
	return "", "", fmt.Errorf("--type 只能是 track、artist、playlist,得到 %q", typ)
}

// playSources:解析用的資料來源,讓規則可以離線測。artists 為 nil = provider 不支援藝人搜尋。
type playSources struct {
	playlists func() []cache.Playlist
	artists   func(ctx context.Context, q string, limit int) ([]provider.Artist, error)
	tracks    func(ctx context.Context, q string, limit int) ([]provider.Track, error)
}

const resolveLimit = 5

func plCandidate(p cache.Playlist) candidate {
	return candidate{Type: cache.TypePlaylist, ID: p.ID, Label: p.Name, Detail: fmt.Sprintf("%d 首", p.Total), Total: p.Total}
}
func artistCandidate(a provider.Artist) candidate {
	return candidate{Type: cache.TypeArtist, ID: a.ProviderID, Label: a.Name, Detail: "熱門歌曲"}
}
func trackCandidate(t provider.Track) candidate {
	return candidate{Type: cache.TypeTrack, ID: t.ProviderID, Label: t.Title, Detail: strings.Join(t.Artists, ", ") + " · " + t.Album}
}

func eq(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }

// uniqueExact:標籤與 q 完全相符(trim、不分大小寫)的候選恰一個才算命中。
func uniqueExact(cs []candidate, q string) *candidate {
	var hit *candidate
	for i := range cs {
		if eq(cs[i].Label, q) {
			if hit != nil {
				return nil
			}
			hit = &cs[i]
		}
	}
	return hit
}

// relevant:候選的可搜尋文字含 query 的任一 token(小寫、trim)。平台的搜尋是模糊比對,「派對動物」會回一串
// 無關藝人;不過濾的話規則 (d) 永遠不成立、挑選器塞滿雜訊。任一 token 命中而不是整串,才吃得下多字查詢。
func relevant(text, q string) bool {
	text = strings.ToLower(text)
	for _, tok := range strings.Fields(strings.ToLower(q)) {
		if strings.Contains(text, tok) {
			return true
		}
	}
	return false
}

// resolvePlay 依 UX 計畫 T5 的規則回傳唯一命中,否則回傳候選(len ≥ 2 = 歧義;0 = 找不到)。
//
//	指定類型:playlist → 名稱完全相符,否則子字串命中恰一;artist / track → 平台第一筆(平台排序即決定性,
//	       腳本 `play --type track X` 才能像舊的 `play X`)。
//	未指定:a. 快取清單名完全相符(不打網路)→ 藝人與曲目各搜 5 筆並過濾相關性(藝人:名稱相關、或出現在任一
//	       回傳曲目的藝人欄——Spotify 對「五月天」回英文名 Mayday,靠曲目接得上;全部濾光就退回平台原始結果)
//	       → b. 藝人名完全相符 → c. 曲名完全相符 → d. 全部候選恰一 → 否則歧義。
func resolvePlay(ctx context.Context, src playSources, q, typ string) (*candidate, []candidate, error) {
	if q == "" {
		return nil, nil, errors.New("缺搜尋詞")
	}
	var pls []candidate
	if typ == "" || typ == cache.TypePlaylist {
		for _, p := range src.playlists() {
			if strings.Contains(strings.ToLower(p.Name), strings.ToLower(strings.TrimSpace(q))) {
				pls = append(pls, plCandidate(p))
			}
		}
	}
	if typ == cache.TypePlaylist {
		if hit := uniqueExact(pls, q); hit != nil {
			return hit, nil, nil
		}
		if len(pls) == 1 {
			return &pls[0], nil, nil
		}
		return nil, pls, nil
	}
	if hit := uniqueExact(pls, q); hit != nil { // 3a:資料全在本機,確定命中就不付兩次網路來回(斷線也能播)
		return hit, nil, nil
	}
	var rawArtists []provider.Artist
	if (typ == "" || typ == cache.TypeArtist) && src.artists != nil {
		as, err := src.artists(ctx, q, resolveLimit)
		if err != nil {
			return nil, nil, err
		}
		rawArtists = as
	}
	if typ == cache.TypeArtist {
		if len(rawArtists) == 0 {
			return nil, nil, nil
		}
		c := artistCandidate(rawArtists[0])
		return &c, nil, nil
	}
	var rawTracks []provider.Track
	if src.tracks != nil {
		limit := resolveLimit
		if typ == cache.TypeTrack {
			limit = 1
		}
		ts, err := src.tracks(ctx, q, limit)
		if err != nil {
			return nil, nil, err
		}
		rawTracks = ts
	}
	if typ == cache.TypeTrack {
		if len(rawTracks) == 0 {
			return nil, nil, nil
		}
		c := trackCandidate(rawTracks[0])
		return &c, nil, nil
	}
	artists, tracks := filterRelevant(rawArtists, rawTracks, q)
	if hit := uniqueExact(artists, q); hit != nil {
		return hit, nil, nil
	}
	if hit := uniqueExact(tracks, q); hit != nil {
		return hit, nil, nil
	}
	all := append(append(pls, artists...), tracks...)
	if len(all) == 1 {
		return &all[0], nil, nil
	}
	return nil, all, nil
}

// filterRelevant:把平台模糊搜尋的雜訊濾掉(見 relevant);全部濾光就退回原始結果,不比平台更差。
func filterRelevant(rawArtists []provider.Artist, rawTracks []provider.Track, q string) (artists, tracks []candidate) {
	inTracks := map[string]bool{}
	for _, t := range rawTracks {
		for _, n := range t.Artists {
			inTracks[strings.ToLower(n)] = true
		}
	}
	for _, a := range rawArtists {
		if relevant(a.Name, q) || inTracks[strings.ToLower(a.Name)] {
			artists = append(artists, artistCandidate(a))
		}
	}
	for _, t := range rawTracks {
		if relevant(t.Title+" "+strings.Join(t.Artists, " ")+" "+t.Album, q) {
			tracks = append(tracks, trackCandidate(t))
		}
	}
	if len(artists) == 0 && len(tracks) == 0 {
		for _, a := range rawArtists {
			artists = append(artists, artistCandidate(a))
		}
		for _, t := range rawTracks {
			tracks = append(tracks, trackCandidate(t))
		}
	}
	return artists, tracks
}

var typeNames = map[string]string{cache.TypePlaylist: "清單", cache.TypeArtist: "藝人", cache.TypeTrack: "曲目", cache.TypeQuery: "搜尋"}

func pickerLabel(c candidate) string {
	return fmt.Sprintf("[%s] %s — %s%s", typeNames[c.Type], c.Label, c.Detail, c.Note)
}

// runPlayPicker:TTY 挑選器(/ 進入過濾)。Esc 在 huh 裡是清除過濾,不綁成取消;取消用 Ctrl-C。測試替換點。
var runPlayPicker = func(cands []candidate) (*candidate, error) {
	opts := make([]huh.Option[int], len(cands))
	for i, c := range cands {
		opts[i] = huh.NewOption(pickerLabel(c), i)
	}
	idx := 0
	sel := huh.NewSelect[int]().Title("選一個播放(/ 過濾,Ctrl-C 取消)").Options(opts...).Filtering(true).Height(12).Value(&idx)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, errors.New("已取消")
		}
		return nil, err
	}
	return &cands[idx], nil
}

// cacheCandidates:--pick 用,全部來自本機快取(清單 + 最近項目),不打網路;q 非空時只留相關的。
// 同 type+id 只列一次(播過的清單同時在 Playlists 與 Recent 裡),清單那份先,資料比較新。
func cacheCandidates(c *cache.Cache, providerID, q string) []candidate {
	var out []candidate
	seen := map[string]bool{}
	add := func(cand candidate) {
		k := cand.Type + "\x00" + cand.ID
		if seen[k] || (q != "" && !relevant(cand.Label+" "+cand.Detail, q)) {
			return
		}
		seen[k] = true
		out = append(out, cand)
	}
	for _, p := range c.Playlists[providerID] {
		add(plCandidate(p))
	}
	for _, r := range c.Recent {
		if r.Provider != providerID || r.Type == cache.TypeQuery {
			continue
		}
		add(candidate{Type: r.Type, ID: r.ID, Label: r.Label, Detail: r.Detail})
	}
	return out
}
