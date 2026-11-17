package cli

// 直達端點(P7 決策 42 + Stage 2 主力播放器):ISRC 查詢、播放面板、直達播放控制(/api/player、/api/player/devices)、
// 定時器(/api/timer)、唯讀曲庫摘要(/api/library)與離線快取歌曲介紹(/api/wiki/cached)。
// 它們不經 cobra、不進 runMu、不呼叫 defaultProvider()、不呼叫 i18n.Set——
// 會碰的接縫只有 BackoffStderr / LockStderr 兩個 stderr 全域(已序列化)與 package var newProvider(不換)。
// 共享點另有 SQLite,唯讀開、零副作用。

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/ai"
	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/resolve"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

// webNowWait:handler 最多等這麼久就回上一輪的結果;真正的 State 用伺服器 ctx 在背景跑完
// (token 換發不能被 handler 的逾時腰斬——Spotify 的 refresh token 會輪替,腰斬等於燒掉它)。測試替換點。
var webNowWait = 2 * time.Second

const (
	// webISRCTimeout:每個 provider 各自一份,從 r.Context() 派生——關分頁就全部收工。
	webISRCTimeout = 30 * time.Second
	// webCanonBusy:唯讀開 SQLite 的 busy timeout;讀不到就回 canonical: null,不等寫入者。
	webCanonBusy = 200 * time.Millisecond
	// webNowStoreBusy:/api/now 離線查詢本機 state.db 的短逾時,絕不卡住輪詢。
	webNowStoreBusy = 50 * time.Millisecond
	// webPlayerTimeout:/api/player 與 /api/player/devices 對 PlaybackController 的單次操作逾時。
	webPlayerTimeout = 3 * time.Second
	// webMaxPlayQueue:歌單依序/隨機連播單次最多送出的曲目數上限。
	webMaxPlayQueue = 100
	// webPlayerBodyMax:直達 JSON POST 端點的請求大小上限。
	webPlayerBodyMax = 64 << 10
	webRecentLimit   = 20
	webJournalLimit  = 100
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// ── GET /api/isrc/{isrc} ──

// isrcProviderIDs:/api/isrc 不帶 ?provider 時一次問的平台。YouTube Music 沒有 ISRC(決策 60,provider 不宣告
// CapISRCLookup),問了那一格永遠是「不支援」的紅字卡,所以預設不問;明確 ?provider=youtube 仍照常回那個錯。
// ponytail: 靜態手抄本,TestWebMoveWizardCapabilitiesAndHeadersMatchGo 釘它等於宣告 CapISRCLookup 的平台;
// 能不登入就查到平台能力時再改成過濾。
var isrcProviderIDs = []string{"spotify", "apple", "local"}

type isrcResponse struct {
	ISRC            string               `json:"isrc"`
	Parts           *provider.ISRCParts  `json:"parts"` // null = 拆不出四段(平台照樣查,頁面把那一區留白)
	Providers       map[string]*isrcProv `json:"providers"`
	Canonical       *isrcCanonical       `json:"canonical"`
	CanonicalSource string               `json:"canonical_source,omitempty"`
	CanonicalError  string               `json:"canonical_error,omitempty"`
}

type isrcProv struct {
	Tracks []webTrack `json:"tracks"`
	Error  string     `json:"error,omitempty"` // 單一平台失敗不讓整個回應失敗
}

// webTrack:provider.Track 的網頁形狀(Raw 不外流)。
type webTrack struct {
	ID          string   `json:"id"`
	ISRC        string   `json:"isrc,omitempty"`
	Title       string   `json:"title"`
	Artists     []string `json:"artists"`
	Album       string   `json:"album,omitempty"`
	DurationMS  int      `json:"duration_ms"`
	Explicit    bool     `json:"explicit,omitempty"`
	URL         string   `json:"url,omitempty"`
	ArtworkURL  string   `json:"artwork_url,omitempty"`
	PreviewURL  string   `json:"preview_url,omitempty"`
	ReleaseDate string   `json:"release_date,omitempty"`
	Genres      []string `json:"genres,omitempty"`
}

func toWebTrack(t provider.Track) webTrack {
	return webTrack{
		ID: t.ProviderID, ISRC: t.ISRC, Title: t.Title, Artists: nonNilStrings(t.Artists), Album: t.Album,
		DurationMS: t.DurationMS, Explicit: t.Explicit, URL: t.URL, ArtworkURL: t.ArtworkURL,
		PreviewURL: t.PreviewURL, ReleaseDate: t.ReleaseDate, Genres: t.Genres,
	}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

type isrcCanonical struct {
	CID        string                   `json:"cid"`
	Title      string                   `json:"title"`
	Artists    []string                 `json:"artists"`
	Album      string                   `json:"album,omitempty"`
	DurationMS int                      `json:"duration_ms"`
	ISRC       []string                 `json:"isrc"` // alias set
	Mappings   map[string]canon.Mapping `json:"mappings"`
	Conflicts  []canon.Conflict         `json:"conflicts,omitempty"`
	Playlists  []isrcPlaylistRef        `json:"playlists"`
}

type isrcPlaylistRef struct {
	PID   string            `json:"pid"`
	Name  string            `json:"name"`
	Pos   int               `json:"pos"`
	Links map[string]string `json:"links"`
}

func (s *webServer) handleISRC(w http.ResponseWriter, r *http.Request) {
	isrc := provider.NormalizeISRC(r.PathValue("isrc"))
	if isrc == "" {
		httpErr(w, http.StatusBadRequest, provider.ErrBadISRC.Error())
		return
	}
	resp := &isrcResponse{ISRC: isrc, Providers: map[string]*isrcProv{}}
	// 拆不出四段不是錯(isrcPartsRe 比 NormalizeISRC 嚴):平台查得到就照樣顯示,只有四段那一區留白。
	if p, ok := provider.ParseISRC(isrc); ok {
		resp.Parts = &p
	}

	ids := isrcProviderIDs
	if q := r.URL.Query().Get("provider"); q != "" && q != "all" {
		if !isProviderID(q) {
			httpErr(w, http.StatusBadRequest, i18n.T("web.err.unknown_provider", "id", q))
			return
		}
		ids = []string{q}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), webISRCTimeout)
			defer cancel()
			out := &isrcProv{Tracks: []webTrack{}}
			if tracks, err := lookupISRC(ctx, id, isrc); err != nil {
				out.Error = friendlyErr(id, err).Error() // Apple token 失效 → 指向 auth login,不是「查無此曲」
			} else {
				for _, t := range tracks {
					out.Tracks = append(out.Tracks, toWebTrack(t))
				}
			}
			mu.Lock()
			resp.Providers[id] = out
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	s.fillCanonical(resp, isrc)
	writeJSON(w, resp)
}

func lookupISRC(ctx context.Context, id, isrc string) ([]provider.Track, error) {
	p, err := newProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	l, err := asISRCLookup(p)
	if err != nil {
		return nil, err
	}
	return l.LookupISRC(ctx, isrc)
}

// fillCanonical:本機鏡像,零副作用——唯讀開、讀不到就 canonical: null 帶原因,絕不自癒、絕不建檔。
func (s *webServer) fillCanonical(resp *isrcResponse, isrc string) {
	st, err := store.OpenReadOnly(webCanonBusy)
	if err != nil {
		// 還沒有 state.db 是第一次 pl pull 之前的正常狀態,不是錯誤:讓頁面走「本機還沒有這首的紀錄」那句 muted 指路,
		// 不要畫成紅字(review #61)。真正的錯誤(壞檔、schema 不符、busy)才帶 canonical_error。
		if !errors.Is(err, store.ErrNoDB) {
			resp.CanonicalError = err.Error()
		}
		return
	}
	defer st.Close()
	c, err := st.Dump()
	if err != nil {
		resp.CanonicalError = err.Error()
		return
	}
	resp.CanonicalSource = "local-cache"
	idx := canon.NewIdentity(c.Tracks.Tracks, c.Tracks.Merged)
	// prov / providerID 刻意傳空:只走 byISRC 那一層。miss 會落 §6.2 的 i:<ISRC> 公式(一定回一個 cid),
	// 所以必須再確認 Tracks[cid] 真的存在,才算「本機有這首」。
	cid := idx.Resolve("", "", isrc)
	tr, ok := c.Tracks.Tracks[cid]
	if !ok {
		return
	}
	out := &isrcCanonical{
		CID: cid, Title: tr.Title, Artists: nonNilStrings(tr.Artists), Album: tr.Album, DurationMS: tr.DurationMS,
		ISRC: nonNilStrings(tr.ISRC), Mappings: tr.Mappings, Conflicts: tr.Conflicts, Playlists: []isrcPlaylistRef{},
	}
	if out.Mappings == nil {
		out.Mappings = map[string]canon.Mapping{}
	}
	for _, pl := range c.Playlists {
		for i, it := range pl.Items {
			if idx.Redirect(it.CID) != cid { // item 的 cid 先沿墓碑追:合併過的曲目也要找得到
				continue
			}
			links := pl.Links
			if links == nil {
				links = map[string]string{}
			}
			out.Playlists = append(out.Playlists, isrcPlaylistRef{PID: pl.PID, Name: pl.Name, Pos: i, Links: links})
			break
		}
	}
	sort.Slice(out.Playlists, func(i, j int) bool { return out.Playlists[i].PID < out.Playlists[j].PID })
	resp.Canonical = out
}

// ── GET /api/now ──

// 播放面板的核心(跟隨規則、每家的快取與有效期、安定期、世代號)在 now_tracker.go,TUI 也用同一套(決策 51、Q64)。
// 這裡只剩 HTTP 的部分:單飛、handler 逾時回上一輪的結果、JSON 形狀。

type nowResponse struct {
	Provider    string          `json:"provider"`
	Playing     bool            `json:"playing"`
	Track       *webTrack       `json:"track"`
	PositionMS  int             `json:"position_ms"`
	Device      *nowDevice      `json:"device"`
	Shuffle     bool            `json:"shuffle,omitempty"`
	Repeat      string          `json:"repeat,omitempty"`
	InPlaylists []string        `json:"in_playlists,omitempty"`
	WikiCached  *cachedWikiView `json:"wiki_cached,omitempty"`
	Timer       *sleepSnapshot  `json:"timer,omitempty"`
	Error       string          `json:"error,omitempty"` // 播放器沒開 / 未登入 / 不支援都是 200 帶 error
	Stale       bool            `json:"stale"`
	StaleMS     int64           `json:"stale_ms,omitempty"`
}

type nowDevice struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	VolumePct   int    `json:"volume_pct"`
	VolumeKnown bool   `json:"volume_known"`
}

// toNowResponse:追蹤器的結果轉成 /api/now 的 JSON。錯誤經 friendlyErr(Apple token 失效 → 指向 auth login)。
// Playing 跟 Track 分開看:Spotify 播 podcast 或廣告時 item 是 null,那也是「正在播」。
func toNowResponse(r nowResult) *nowResponse {
	resp := &nowResponse{Provider: r.provider}
	if r.err != nil {
		resp.Error = friendlyErr(r.provider, r.err).Error()
	}
	if st := r.st; st != nil {
		resp.Playing = st.Playing
		resp.Shuffle = st.Shuffle
		resp.Repeat = string(st.Repeat)
		resp.Device = &nowDevice{
			ID: st.Device.ID, Name: st.Device.Name, Type: st.Device.Type,
			VolumePct: st.Device.VolumePct, VolumeKnown: st.Device.VolumeKnown,
		}
		if st.Track != nil {
			t := toWebTrack(*st.Track)
			resp.Track, resp.PositionMS = &t, st.ProgressMS
		}
	}
	return resp
}

func (s *webServer) handleNow(w http.ResponseWriter, r *http.Request) {
	pin := r.URL.Query().Get("provider") // 有明指(?provider= 或 --web --provider)就釘住;否則跟隨正在播的平台
	if pin == "" {
		pin = s.provFlag
	}
	if pin != "" && !isProviderID(pin) {
		httpErr(w, http.StatusBadRequest, i18n.T("web.err.unknown_provider", "id", pin))
		return
	}
	// 單飛:上一輪還在等 token 鎖或平台回應時,立刻回上一輪的結果,不排隊、不堆 goroutine。
	if !s.pollMu.TryLock() {
		writeJSON(w, s.staleNow(pin))
		return
	}
	done := make(chan *nowResponse, 1)
	go func() {
		var (
			resp  *nowResponse
			res   nowResult
			ok    bool
			entry *store.PlayJournalEntry
		)
		func() {
			defer s.pollMu.Unlock()
			gen := s.nowGen.Load()
			res = s.pollRound(pin)
			resp = toNowResponse(res)
			ok, entry = s.commitNow(res, pin == "", gen)
			if !ok { // 問的期間 dropNow 過(登出、換帳號):舊帳號的結果不記、也不送
				resp = s.staleNow(pin)
			}
		}()
		if entry != nil {
			s.recordPlayJournal(*entry)
		}
		if ok {
			if res.err == nil && s.timer != nil {
				s.timer.Observe(res.provider, res.st)
			}
			s.enrichNow(resp)
		}
		done <- resp
	}()
	select {
	case resp := <-done:
		writeJSON(w, resp)
	case <-time.After(webNowWait):
		// 背景那個 goroutine 繼續跑完(伺服器 ctx,不被腰斬),寫回快照後才放 pollMu。
		writeJSON(w, s.staleNow(pin))
	case <-r.Context().Done():
	}
}

// staleNow:上一輪的結果加 stale 標記;正在播的進度照樣往前推(不然一次 TryLock 失敗,進度條就倒退)。
// 釘住時認平台:快照是另一家的就不端出來(config set default_provider 之後、重建那一次特別容易走到)——寧可回空的 stale,
// 也不要顯示不是這個平台在放的歌。還沒有任何一輪就回空的 stale(頁面顯示「讀取中」而不是凍住)。
func (s *webServer) staleNow(pin string) *nowResponse {
	snap := s.lastNow.Load()
	if snap == nil || (pin != "" && snap.res.provider != pin) {
		id := pin
		if id == "" {
			id = loadDefaultProvider()
			if p := s.shown.Load(); p != nil {
				id = *p
			}
		}
		resp := &nowResponse{Provider: id, Stale: true}
		s.attachTimer(resp)
		return resp
	}
	age := webNowClock().Sub(snap.at)
	resp := toNowResponse(advance(snap.res, age))
	resp.Stale = true
	resp.StaleMS = age.Milliseconds()
	s.enrichNow(resp)
	return resp
}

func (s *webServer) attachTimer(resp *nowResponse) {
	if s.timer == nil || resp == nil {
		return
	}
	if snap := s.timer.Snapshot(); snap.Mode != SleepOff {
		resp.Timer = &snap
	}
}

// enrichNow:純本機離線補齊 in_playlists、Apple catalog 單曲外連、wiki_cached 與定時器快照。
// 嚴守 #9 1D 與 CLAUDE.md:絕不在背景輪詢打 SearchSongs 或任何網路/AI 請求,只做本機唯讀查詢。
func (s *webServer) enrichNow(resp *nowResponse) {
	if resp == nil {
		return
	}
	s.attachTimer(resp)
	if resp.Track == nil {
		return
	}
	cfg, cfgErr := config.Load()
	if cfgErr != nil || cfg == nil {
		cfg = &config.Config{}
	}
	enrichNowFromCanon(resp, cfg)
	if wv, ok := lookupCachedWikiWithLang(*cfg, ai.Song{
		Title:       resp.Track.Title,
		Artists:     resp.Track.Artists,
		Album:       resp.Track.Album,
		ReleaseDate: resp.Track.ReleaseDate,
		Genres:      resp.Track.Genres,
	}); ok {
		resp.WikiCached = wv
	}
}

func lookupCachedWikiWithLang(cfg config.Config, song ai.Song) (*cachedWikiView, bool) {
	wv, ok := lookupCachedWiki(cfg, song)
	if !ok || wv == nil {
		return nil, false
	}
	lang, _ := configLanguage()
	if msgs := i18n.Messages(lang, func(k string) bool { return k == "wiki.disclaimer" }); msgs != nil {
		if d, ok := msgs["wiki.disclaimer"].(string); ok && d != "" {
			wv.Disclaimer = d
		}
	}
	return wv, true
}

func enrichNowFromCanon(resp *nowResponse, cfg *config.Config) {
	st, err := store.OpenReadOnly(webNowStoreBusy)
	if err != nil {
		return
	}
	defer st.Close()
	c, err := st.Dump()
	if err != nil {
		return
	}
	idx := canon.NewIdentity(c.Tracks.Tracks, c.Tracks.Merged)
	var cid string
	if resp.Track.ID != "" || resp.Track.ISRC != "" {
		if cand := idx.Resolve(resp.Provider, resp.Track.ID, resp.Track.ISRC); cand != "" {
			if _, ok := c.Tracks.Tracks[cand]; ok {
				cid = cand
			}
		}
		if cid == "" && resp.Track.ISRC != "" {
			if cand := idx.Resolve("", "", resp.Track.ISRC); cand != "" {
				if _, ok := c.Tracks.Tracks[cand]; ok {
					cid = cand
				}
			}
		}
	}
	if cid == "" {
		cid = matchOfflineCanonTrack(resp.Track, c.Tracks.Tracks)
	}
	if cid == "" {
		return
	}
	tr, ok := c.Tracks.Tracks[cid]
	if !ok {
		return
	}
	var pids []string
	for _, pl := range c.Playlists {
		for _, it := range pl.Items {
			if idx.Redirect(it.CID) == cid {
				pids = append(pids, pl.PID)
				break
			}
		}
	}
	sort.Strings(pids)
	resp.InPlaylists = pids

	if resp.Provider == "apple" && resp.Track.URL == "" {
		if m, ok := tr.Mappings["apple"]; ok && isAllDigits(m.ID) {
			sf := strings.TrimSpace(cfg.AppleStorefront)
			if sf != "" {
				resp.Track.URL = "https://music.apple.com/" + url.PathEscape(sf) + "/song/" + url.PathEscape(m.ID)
			} else {
				resp.Track.URL = "https://music.apple.com/song/" + url.PathEscape(m.ID)
			}
		}
	}
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func matchOfflineCanonTrack(want *webTrack, tracks map[string]canon.Track) string {
	if want == nil || len(want.Artists) == 0 || want.DurationMS <= 0 {
		return ""
	}
	rawTitle := strings.TrimSpace(want.Title)
	rawArtist := strings.TrimSpace(want.Artists[0])
	if rawTitle == "" || rawArtist == "" {
		return ""
	}
	normTitle := resolve.Norm(rawTitle)
	normArtist := resolve.Norm(rawArtist)
	normAlbum := resolve.Norm(want.Album)
	type cand struct {
		cid      string
		albumHit bool
		durDiff  int
	}
	var hits []cand
	for cid, tr := range tracks {
		if tr.DurationMS <= 0 || absPlAddDiff(tr.DurationMS, want.DurationMS) > 2000 {
			continue
		}
		titleOK := (normTitle != "" && resolve.Norm(tr.Title) == normTitle) || strings.EqualFold(strings.TrimSpace(tr.Title), rawTitle)
		if !titleOK {
			continue
		}
		artistOK := false
		for _, a := range tr.Artists {
			if (normArtist != "" && resolve.Norm(a) == normArtist) || strings.EqualFold(strings.TrimSpace(a), rawArtist) {
				artistOK = true
				break
			}
		}
		if !artistOK {
			continue
		}
		hits = append(hits, cand{
			cid:      cid,
			albumHit: normAlbum != "" && resolve.Norm(tr.Album) == normAlbum,
			durDiff:  absPlAddDiff(tr.DurationMS, want.DurationMS),
		})
	}
	if len(hits) == 0 {
		return ""
	}
	slices.SortStableFunc(hits, func(a, b cand) int {
		if a.albumHit != b.albumHit {
			if a.albumHit {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(a.durDiff, b.durDiff); c != 0 {
			return c
		}
		return strings.Compare(a.cid, b.cid)
	})
	return hits[0].cid
}

// ── POST /api/player & GET /api/player/devices ──

type playerActionReq struct {
	Action     string   `json:"action"`
	Provider   string   `json:"provider"`
	ID         string   `json:"id"`
	TrackIDs   []string `json:"track_ids"`
	PlaylistID string   `json:"playlist_id"`
	Shuffle    bool     `json:"shuffle"`
	On         *bool    `json:"on"`
	PositionMS *int     `json:"position_ms"`
	VolumePct  *int     `json:"volume_pct"`
	Repeat     string   `json:"repeat"`
	DeviceID   string   `json:"device_id"`
	Play       *bool    `json:"play"`
}

type webDevice struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Active      bool   `json:"active"`
	VolumePct   int    `json:"volume_pct"`
	VolumeKnown bool   `json:"volume_known"`
}

func (s *webServer) resolveActiveProvider(explicit string) (string, bool) {
	if explicit != "" {
		if !isProviderID(explicit) {
			return "", false
		}
		return explicit, true
	}
	if s.provFlag != "" {
		return s.provFlag, true
	}
	if p := s.shown.Load(); p != nil && *p != "" {
		return *p, true
	}
	return loadDefaultProvider(), true
}

func decodeJSONBody(r *http.Request, dst any) error {
	return json.NewDecoder(io.LimitReader(r.Body, webPlayerBodyMax)).Decode(dst)
}

func (s *webServer) evictIfAuthExpired(prov string, err error) {
	if errors.Is(err, provider.ErrAuthExpired) {
		s.nowMu.Lock()
		delete(s.now, prov)
		s.nowMu.Unlock()
	}
}

func (s *webServer) handlePlayer(w http.ResponseWriter, r *http.Request) {
	var req playerActionReq
	if err := decodeJSONBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, i18n.T("webui.api.err.bad_json", "err", err.Error()))
		return
	}
	prov, ok := s.resolveActiveProvider(strings.TrimSpace(req.Provider))
	if !ok {
		httpErr(w, http.StatusBadRequest, i18n.T("web.err.unknown_provider", "id", req.Provider))
		return
	}
	pc, err := s.playback(prov)
	if err != nil {
		httpErr(w, http.StatusUnprocessableEntity, friendlyErr(prov, err).Error())
		return
	}
	ctx, cancel := context.WithTimeout(provider.WithoutWait(r.Context()), webPlayerTimeout)
	defer cancel()

	action := strings.TrimSpace(req.Action)
	var (
		opErr  error
		queued int
		pr     provider.PlayRequest
	)
	switch action {
	case "play":
		targets := 0
		if strings.TrimSpace(req.ID) != "" {
			targets++
		}
		if len(req.TrackIDs) > 0 {
			targets++
		}
		if strings.TrimSpace(req.PlaylistID) != "" {
			targets++
		}
		if targets > 1 {
			httpErr(w, http.StatusBadRequest, i18n.T("webui.api.err.play_conflict"))
			return
		}
		pr.DeviceID = strings.TrimSpace(req.DeviceID)
		switch {
		case strings.TrimSpace(req.PlaylistID) != "":
			if p, ok := pc.(provider.Provider); ok && !p.Caps().Has(provider.CapPlayPlaylist) {
				httpErr(w, http.StatusUnprocessableEntity, friendlyErr(prov, provider.ErrNotSupported).Error())
				return
			}
			pr.PlaylistID = strings.TrimSpace(req.PlaylistID)
		case len(req.TrackIDs) > 0:
			ids := make([]string, 0, len(req.TrackIDs))
			for _, id := range req.TrackIDs {
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					ids = append(ids, trimmed)
				}
			}
			if len(ids) == 0 {
				httpErr(w, http.StatusBadRequest, i18n.T("webui.api.err.empty_tracks"))
				return
			}
			if req.Shuffle {
				rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
			}
			if len(ids) > webMaxPlayQueue {
				ids = ids[:webMaxPlayQueue]
			}
			pr.TrackIDs = ids
			queued = len(ids)
		case strings.TrimSpace(req.ID) != "":
			pr.TrackIDs = []string{strings.TrimSpace(req.ID)}
			queued = 1
		}
		opErr = pc.Play(ctx, pr)
		if errors.Is(opErr, provider.ErrOpenedNotPlaying) {
			label := strings.TrimSpace(req.ID)
			if label == "" && len(pr.TrackIDs) > 0 {
				label = pr.TrackIDs[0]
			}
			var oe *provider.OpenedError
			if errors.As(opErr, &oe) && oe.Label != "" {
				label = oe.Label
			}
			if req.Provider != "" && s.provFlag == "" {
				p := prov
				s.shown.Store(&p)
			}
			s.settleNow()
			writeJSON(w, map[string]any{
				"ok":                 true,
				"provider":           prov,
				"opened_not_playing": true,
				"message":            i18n.T("play.opened_not_playing", "label", label),
			})
			return
		}

	case "pause":
		opErr = pc.Pause(ctx)

	case "next":
		opErr = pc.Next(ctx)

	case "prev":
		opErr = pc.Prev(ctx)

	case "seek":
		if req.PositionMS == nil {
			httpErr(w, http.StatusBadRequest, i18n.T("webui.api.err.bad_seek"))
			return
		}
		opErr = pc.Seek(ctx, max(0, *req.PositionMS))

	case "volume":
		if req.VolumePct == nil {
			httpErr(w, http.StatusBadRequest, i18n.T("webui.api.err.bad_volume"))
			return
		}
		opErr = pc.SetVolume(ctx, min(100, max(0, *req.VolumePct)))

	case "shuffle":
		mc, ok := pc.(provider.PlaybackModeController)
		if !ok {
			httpErr(w, http.StatusUnprocessableEntity, friendlyErr(prov, provider.ErrNotSupported).Error())
			return
		}
		on := req.Shuffle
		if req.On != nil {
			on = *req.On
		}
		opErr = mc.SetShuffle(ctx, on)

	case "repeat":
		mode := provider.RepeatMode(strings.TrimSpace(req.Repeat))
		if mode != provider.RepeatOff && mode != provider.RepeatContext && mode != provider.RepeatTrack {
			httpErr(w, http.StatusBadRequest, i18n.T("webui.api.err.bad_repeat"))
			return
		}
		mc, ok := pc.(provider.PlaybackModeController)
		if !ok {
			httpErr(w, http.StatusUnprocessableEntity, friendlyErr(prov, provider.ErrNotSupported).Error())
			return
		}
		opErr = mc.SetRepeat(ctx, mode)

	case "transfer":
		devID := strings.TrimSpace(req.DeviceID)
		if devID == "" {
			httpErr(w, http.StatusBadRequest, i18n.T("webui.api.err.missing_device"))
			return
		}
		dt, ok := pc.(provider.DeviceTransferrer)
		if !ok {
			httpErr(w, http.StatusUnprocessableEntity, friendlyErr(prov, provider.ErrNotSupported).Error())
			return
		}
		play := false
		if req.Play != nil {
			play = *req.Play
		} else if snap := s.lastNow.Load(); snap != nil && snap.res.provider == prov && snap.res.playing() {
			play = true
		}
		opErr = dt.Transfer(ctx, devID, play)

	default:
		httpErr(w, http.StatusBadRequest, i18n.T("webui.api.err.bad_action", "action", req.Action))
		return
	}

	if opErr != nil {
		s.evictIfAuthExpired(prov, opErr)
		httpErr(w, http.StatusUnprocessableEntity, friendlyErr(prov, opErr).Error())
		return
	}
	if req.Provider != "" && s.provFlag == "" && action == "play" {
		p := prov
		s.shown.Store(&p)
	}
	s.settleNow()
	out := map[string]any{"ok": true, "provider": prov}
	if queued > 0 {
		out["queued"] = queued
	}
	writeJSON(w, out)
}

func (s *webServer) handlePlayerDevices(w http.ResponseWriter, r *http.Request) {
	prov, ok := s.resolveActiveProvider(strings.TrimSpace(r.URL.Query().Get("provider")))
	if !ok {
		httpErr(w, http.StatusBadRequest, i18n.T("web.err.unknown_provider", "id", r.URL.Query().Get("provider")))
		return
	}
	pc, err := s.playback(prov)
	if err != nil {
		writeJSON(w, map[string]any{
			"provider":           prov,
			"supported_transfer": false,
			"devices":            []webDevice{},
			"error":              friendlyErr(prov, err).Error(),
		})
		return
	}
	_, canTransfer := pc.(provider.DeviceTransferrer)
	ctx, cancel := context.WithTimeout(provider.WithoutWait(r.Context()), webPlayerTimeout)
	defer cancel()
	ds, err := pc.Devices(ctx)
	if err != nil {
		s.evictIfAuthExpired(prov, err)
		writeJSON(w, map[string]any{
			"provider":           prov,
			"supported_transfer": canTransfer,
			"devices":            []webDevice{},
			"error":              friendlyErr(prov, err).Error(),
		})
		return
	}
	out := make([]webDevice, 0, len(ds))
	for _, d := range ds {
		out = append(out, webDevice{
			ID: d.ID, Name: d.Name, Type: d.Type, Active: d.Active,
			VolumePct: d.VolumePct, VolumeKnown: d.VolumeKnown,
		})
	}
	writeJSON(w, map[string]any{
		"provider":           prov,
		"supported_transfer": canTransfer,
		"devices":            out,
	})
}

// ── POST /api/timer ──

type timerReq struct {
	Mode         string `json:"mode"`
	Minutes      int    `json:"minutes"`
	FocusMinutes int    `json:"focus_minutes"`
	BreakMinutes int    `json:"break_minutes"`
	Provider     string `json:"provider"`
}

func (s *webServer) handleTimer(w http.ResponseWriter, r *http.Request) {
	var req timerReq
	if err := decodeJSONBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, i18n.T("webui.api.err.bad_json", "err", err.Error()))
		return
	}
	prov := strings.TrimSpace(req.Provider)
	if prov != "" && !isProviderID(prov) {
		httpErr(w, http.StatusBadRequest, i18n.T("web.err.unknown_provider", "id", prov))
		return
	}
	if s.timer == nil {
		writeJSON(w, sleepSnapshot{Mode: SleepOff})
		return
	}
	switch strings.TrimSpace(req.Mode) {
	case "", "off":
		s.timer.Cancel(r.Context())
	case "end_of_track":
		s.timer.SetEndOfTrack(prov)
	case "countdown":
		if req.Minutes <= 0 || req.Minutes > 720 {
			httpErr(w, http.StatusBadRequest, i18n.T("webui.api.err.bad_timer"))
			return
		}
		s.timer.SetCountdownMinutes(prov, req.Minutes)
	case "pomodoro":
		if req.FocusMinutes < 0 || req.FocusMinutes > 240 || req.BreakMinutes < 0 || req.BreakMinutes > 120 {
			httpErr(w, http.StatusBadRequest, i18n.T("webui.api.err.bad_timer"))
			return
		}
		s.timer.SetPomodoro(prov, time.Duration(req.FocusMinutes)*time.Minute, time.Duration(req.BreakMinutes)*time.Minute)
	default:
		httpErr(w, http.StatusBadRequest, i18n.T("webui.api.err.bad_timer"))
		return
	}
	writeJSON(w, s.timer.Snapshot())
}

// ── GET /api/library ──

type libraryPlaylistSummary struct {
	PID   string            `json:"pid"`
	Name  string            `json:"name"`
	Count int               `json:"count"`
	Links map[string]string `json:"links"`
}

type webRecentItem struct {
	At       int64  `json:"at"`
	Provider string `json:"provider"`
	Type     string `json:"type"`
	ID       string `json:"id"`
	Label    string `json:"label"`
	Detail   string `json:"detail,omitempty"`
}

type webJournalItem struct {
	ID         int64    `json:"id"`
	PlayedAt   int64    `json:"played_at"`
	Provider   string   `json:"provider"`
	ProviderID string   `json:"provider_id,omitempty"`
	ISRC       string   `json:"isrc,omitempty"`
	Title      string   `json:"title"`
	Artists    []string `json:"artists"`
	Album      string   `json:"album,omitempty"`
	DurationMS int      `json:"duration_ms,omitempty"`
}

type libraryResponse struct {
	Auth        authStatus                `json:"auth"`
	HasDB       bool                      `json:"has_db"`
	HasCanon    bool                      `json:"has_canon"`
	StoreError  string                    `json:"store_error,omitempty"`
	Files       map[string]json.RawMessage `json:"files"`
	Playlists   []libraryPlaylistSummary  `json:"playlists"`
	Recent      []webRecentItem           `json:"recent"`
	PlayJournal []webJournalItem          `json:"play_journal"`
}

// handleLibrary:GET /api/library——唯讀直達端點,不進 runMu、不連網、絕不含任何 token/secret 值。
func (s *webServer) handleLibrary(w http.ResponseWriter, _ *http.Request) {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		cfg = &config.Config{}
	}
	resp := &libraryResponse{
		Auth:        authStatusOf(cfg),
		HasDB:       hasLocalDB(),
		Files:       map[string]json.RawMessage{},
		Playlists:   []libraryPlaylistSummary{},
		Recent:      []webRecentItem{},
		PlayJournal: []webJournalItem{},
	}
	st, err := store.OpenReadOnly(webCanonBusy)
	if errors.Is(err, store.ErrNoDB) {
		writeJSON(w, resp)
		return
	}
	if err != nil {
		resp.StoreError = i18n.Errorf("escape.err.open_local", "err", err, "hint", retainedHint()).Error()
		writeJSON(w, resp)
		return
	}
	defer st.Close()
	resp.HasDB = true

	if c, err := st.Dump(); err == nil {
		if len(c.Playlists) > 0 || len(c.Devices) > 0 || len(c.Tracks.Tracks) > 0 || len(c.Manifest.Devices) > 0 {
			resp.HasCanon = true
			addRaw := func(ref canon.FileRef, v any) {
				if b, encErr := canon.Encode(v); encErr == nil {
					resp.Files[ref.Name] = bytes.TrimSpace(b)
				}
			}
			addRaw(canon.ManifestFile(), c.Manifest)
			addRaw(canon.TracksFile(), c.Tracks)
			for i := range c.Playlists {
				pl := &c.Playlists[i]
				addRaw(canon.PlaylistFile(pl.PID), pl)
				links := map[string]string{}
				for k, v := range pl.Links {
					if v != "" {
						links[k] = v
					}
				}
				resp.Playlists = append(resp.Playlists, libraryPlaylistSummary{
					PID:   pl.PID,
					Name:  pl.Name,
					Count: len(pl.Items),
					Links: links,
				})
			}
			for i := range c.Devices {
				addRaw(canon.DeviceFile(c.Devices[i].DeviceID), &c.Devices[i])
			}
			slices.SortStableFunc(resp.Playlists, func(a, b libraryPlaylistSummary) int {
				if c := strings.Compare(a.Name, b.Name); c != 0 {
					return c
				}
				return strings.Compare(a.PID, b.PID)
			})
		}
	} else {
		resp.StoreError = err.Error()
	}

	if _, recent, err := st.LoadCache(); err == nil {
		if len(recent) > webRecentLimit {
			recent = recent[:webRecentLimit]
		}
		for _, r := range recent {
			resp.Recent = append(resp.Recent, webRecentItem{
				At: r.At, Provider: r.Provider, Type: r.Type,
				ID: r.ID, Label: r.Label, Detail: r.Detail,
			})
		}
	}

	if journal, err := st.ListPlayJournal(webJournalLimit); err == nil {
		for _, e := range journal {
			resp.PlayJournal = append(resp.PlayJournal, webJournalItem{
				ID:         e.ID,
				PlayedAt:   e.PlayedAt.Unix(),
				Provider:   e.Provider,
				ProviderID: e.ProviderID,
				ISRC:       e.ISRC,
				Title:      e.Title,
				Artists:    nonNilStrings(e.Artists),
				Album:      e.Album,
				DurationMS: e.DurationMS,
			})
		}
	}
	writeJSON(w, resp)
}

// ── GET /api/wiki/cached ──

func (s *webServer) handleWikiCached(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		cfg = &config.Config{}
	}
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	artistParam := strings.TrimSpace(r.URL.Query().Get("artist"))
	var song ai.Song
	if title != "" {
		song.Title = title
		if artistParam != "" {
			for _, part := range strings.Split(artistParam, ",") {
				if a := strings.TrimSpace(part); a != "" {
					song.Artists = append(song.Artists, a)
				}
			}
		}
	} else if snap := s.lastNow.Load(); snap != nil && snap.res.st != nil && snap.res.st.Track != nil {
		tr := snap.res.st.Track
		song = ai.Song{
			Title:       tr.Title,
			Artists:     tr.Artists,
			Album:       tr.Album,
			ReleaseDate: tr.ReleaseDate,
			Genres:      tr.Genres,
		}
	}
	wv, ok := lookupCachedWikiWithLang(*cfg, song)
	writeJSON(w, map[string]any{"cached": ok, "wiki": wv})
}

// webNowInvalidatedBy:這個命令跑完要不要作廢快取的 controller 與結果(帳號或設定變了)。auth status 是唯讀的——
// 首頁與帳號頁每次載入都會跑,不能每次都把 controller 丟掉重建、再多打一次平台。
func webNowInvalidatedBy(path string) bool {
	return strings.HasPrefix(path, "capy auth login") || strings.HasPrefix(path, "capy auth logout") || path == "capy config set"
}

// webNowResetsShown:換帳號或換預設平台——面板回到預設平台重新開始(dropNow 的 resetShown)。config set 只有動
// default_provider 才算:換語系(語言選單跑的也是 config set)不該讓面板跳平台。
func webNowResetsShown(path string, args []string) bool {
	return strings.HasPrefix(path, "capy auth ") || (path == "capy config set" && slices.Contains(args, "default_provider"))
}

// webNowSettledBy:這個命令會改變播放狀態——跑完後進安定期(播放列按鈕、快捷鍵、搜尋頁的 play --id、主控台都經過這裡)。
func webNowSettledBy(path string) bool {
	switch path {
	case "capy play", "capy pause", "capy next", "capy prev", "capy seek", "capy vol", "capy shuffle", "capy repeat":
		return true
	}
	return false
}

// webNowSettledByRun:除 webNowSettledBy 外,devices 帶 --switch / --pick 切換輸出裝置後也進安定期(純唯讀無旗標 devices 不進)。
func webNowSettledByRun(path string, args []string) bool {
	if webNowSettledBy(path) {
		return true
	}
	if path == "capy devices" {
		return slices.ContainsFunc(args, func(a string) bool {
			return a == "--switch" || strings.HasPrefix(a, "--switch=") || a == "--pick" || strings.HasPrefix(a, "--pick=")
		})
	}
	return false
}
