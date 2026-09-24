package cli

// 兩個直達端點(P7 決策 42):ISRC 查詢與播放面板。它們不經 cobra、不進 runMu、不呼叫 defaultProvider()——
// 會碰的接縫只有 BackoffStderr / LockStderr 兩個 stderr 全域(已序列化)與 package var newProvider(不換)。
// 共享點另有 SQLite,唯讀開、零副作用。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
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
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// ── GET /api/isrc/{isrc} ──

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
	Popularity  int      `json:"popularity,omitempty"`
	Genres      []string `json:"genres,omitempty"`
}

func toWebTrack(t provider.Track) webTrack {
	return webTrack{
		ID: t.ProviderID, ISRC: t.ISRC, Title: t.Title, Artists: nonNilStrings(t.Artists), Album: t.Album,
		DurationMS: t.DurationMS, Explicit: t.Explicit, URL: t.URL, ArtworkURL: t.ArtworkURL,
		PreviewURL: t.PreviewURL, ReleaseDate: t.ReleaseDate, Popularity: t.Popularity, Genres: t.Genres,
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

	ids := providerIDs
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

// 有效期(決策 51;計畫 docs/superpowers/plans/2026-09-24-web-player-follow-apple-play.md §1.2)。瀏覽器照樣每 2.5 秒
// 問 /api/now——那一段只打本機;真的打 Spotify Web API 的次數由這幾個值決定(dev mode 的配額以開發者帳號計,跟 pl sync、cron 共用)。
const (
	webNowFailTTL = time.Minute      // 建不起來(沒登入)、State 回錯:一分鐘後再試;沒登入 Apple 的人不會每 2.5 秒讀 keychain
	webNowIdleTTL = 15 * time.Second // Spotify 閒置或暫停(Q65):手機上開始播,最慢 15 秒出現
	webNowPlayTTL = 10 * time.Second // Spotify 正在播:手機上暫停或換歌,最慢 10 秒出現;這首結束時提早重問
	webNowSettle  = 3 * time.Second  // 播放命令後的安定期:Spotify 的播放器寫入是最終一致,剛按完可能讀到舊狀態
)

// webNowClock:有效期、安定期、stale_ms 都看它。測試替換點。
var webNowClock = time.Now

type nowResponse struct {
	Provider   string     `json:"provider"`
	Playing    bool       `json:"playing"`
	Track      *webTrack  `json:"track"`
	PositionMS int        `json:"position_ms"`
	Device     *nowDevice `json:"device"`
	Error      string     `json:"error,omitempty"` // 播放器沒開 / 未登入 / 不支援都是 200 帶 error
	Stale      bool       `json:"stale"`
	StaleMS    int64      `json:"stale_ms,omitempty"`
}

type nowDevice struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	VolumePct   int    `json:"volume_pct"`
	VolumeKnown bool   `json:"volume_known"`
}

type nowSnapshot struct {
	resp *nowResponse
	at   time.Time
}

// nowEntry:某個 provider 最近一次真的問到的結果;until 之前用它回答(正在播的進度依經過時間往前推)。
type nowEntry struct {
	resp      *nowResponse
	at, until time.Time
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
		defer s.pollMu.Unlock()
		resp := s.pollRound(pin)
		s.setNow(resp, pin == "")
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

// pollRound:一輪。釘住就只問那一家。否則照跟隨規則(決策 51):只有「正在播」能把面板拉走。
// base(上一輪顯示的;還沒有就用 config 的 default_provider,每次重讀——不用 defaultProvider(),它是 OnceValue)在播就顯示它,
// 別家一律不問(照這條規則它們改變不了結果,Spotify 一次都不打);base 沒在播才依序問其他平台(預設先,再照 providerIDs),
// 第一個在播的就切過去;都沒在播就留在 base,不管它是暫停、閒置還是出錯。暫停中的歌不能搶面板:Spotify 暫停幾分鐘會從
// 200 變 204,這時 Music.app 掛著昨天暫停的那首就會把面板搶回去——使用者 2026-09-24 回報的就是這個。
func (s *webServer) pollRound(pin string) *nowResponse {
	if pin != "" {
		return s.consult(pin)
	}
	def := loadDefaultProvider()
	base := def
	if p := s.shown.Load(); p != nil {
		base = *p
	}
	resp := s.consult(base)
	if resp.Playing {
		return resp
	}
	if snap := s.lastNow.Load(); snap != nil && snap.resp.Provider == base && snap.resp.Playing {
		s.expireExcept(base) // base 剛停:免費的觸發點(Apple 每輪都問),馬上看別家有沒有接著播,不等快取過期
	}
	for i, id := range append([]string{def}, providerIDs...) {
		if id == base || (i > 0 && id == def) {
			continue
		}
		if r := s.consult(id); r.Playing {
			return r
		}
	}
	return resp
}

// consult:問一家。快取沒過期、也不在安定期,就用快取;否則真的問,並依結果定有效期。
// 只在 pollMu 裡被呼叫(同時只有一個),所以讀快取與寫快取之間只需要防 dropNow——用世代號。
func (s *webServer) consult(id string) *nowResponse {
	now := webNowClock()
	s.nowMu.Lock()
	e, ok := s.nowCache[id]
	s.nowMu.Unlock()
	if ok && now.UnixNano() >= s.settleUntil.Load() && now.Before(e.until) {
		return advance(e.resp, now.Sub(e.at))
	}
	gen := s.nowGen.Load()
	resp, err := s.pollNow(id)
	at := webNowClock()
	until := at.Add(nowTTL(id, resp, err))
	if at.UnixNano() < s.settleUntil.Load() {
		until = at // 安定期內讀到的可能還是命令之前的狀態:不留,安定期一過就重問
	}
	s.nowMu.Lock()
	if s.nowGen.Load() == gen { // 問的期間 dropNow 過(登出、換帳號):舊世代的結果不寫回
		if s.nowCache == nil {
			s.nowCache = map[string]nowEntry{}
		}
		s.nowCache[id] = nowEntry{resp: resp, at: at, until: until}
	}
	s.nowMu.Unlock()
	return resp
}

// expireExcept:讓 keep 以外、沒出錯的快取立刻過期。出錯的(沒登入、429 的冷卻)不動:冷卻期內重問就違反 Retry-After。
func (s *webServer) expireExcept(keep string) {
	s.nowMu.Lock()
	defer s.nowMu.Unlock()
	for id, e := range s.nowCache {
		if id != keep && e.resp.Error == "" {
			e.until = time.Time{}
			s.nowCache[id] = e
		}
	}
}

// advance:正在播的結果,進度依經過時間往前推(不超過曲長);其他原樣。
func advance(r *nowResponse, age time.Duration) *nowResponse {
	if !r.Playing || r.Track == nil || age <= 0 {
		return r
	}
	cp := *r
	cp.PositionMS += int(age / time.Millisecond)
	if d := r.Track.DurationMS; d > 0 && cp.PositionMS > d {
		cp.PositionMS = d
	}
	return &cp
}

// nowTTL:這份結果可以用多久。Apple 的 State 是本機 osascript、不花配額,每輪都問(Music.app 的變化 2.5 秒內看得到);
// 其他平台的每一問都是一次 Web API 呼叫。
func nowTTL(id string, r *nowResponse, err error) time.Duration {
	var rl *provider.RateLimitError
	switch {
	case errors.As(err, &rl):
		return max(webNowFailTTL, time.Duration(rl.Seconds)*time.Second) // 照 Retry-After:冷卻期內不重試
	case errors.Is(err, provider.ErrPlayerNotRunning):
		return 0 // 狀態不是錯:Music.app 一打開,下一輪就看得到
	case err != nil:
		return webNowFailTTL
	case id == "apple":
		return 0
	case !r.Playing:
		return webNowIdleTTL
	case r.Track == nil || r.Track.DurationMS <= 0:
		return webNowPlayTTL
	}
	left := time.Duration(r.Track.DurationMS-r.PositionMS)*time.Millisecond + time.Second // 這首結束後約 1 秒就換歌了
	return max(time.Second, min(webNowPlayTTL, left))
}

// pollNow:真的問一家。用伺服器 ctx——token 換發(TokenSource 等 <key>.token.lock 用的是建構時的 ctx、s.mu 橫跨等鎖)
// 不能被 handler 的逾時砍掉,面板卡住的代價由 staleNow 承擔。WithoutWait:429 不睡在 pollMu 裡(會凍住整條面板),
// 冷卻交給 nowTTL。只有 ErrAuthExpired 丟掉那一家的 controller(token 被撤、換過帳號);Music.app 沒開、限流、平台暫時出錯
// 都只是狀態,不重建——以前任何錯都呼叫全域 dropNow,Music.app 關著時每 2.5 秒就重建一次 Apple provider(每次 2–3 次 keychain)。
func (s *webServer) pollNow(id string) (*nowResponse, error) {
	resp := &nowResponse{Provider: id}
	pc, err := s.playback(id)
	if err != nil {
		resp.Error = friendlyErr(id, err).Error()
		return resp, err
	}
	st, err := pc.State(provider.WithoutWait(s.ctx))
	if err != nil {
		if errors.Is(err, provider.ErrAuthExpired) {
			s.nowMu.Lock()
			delete(s.now, id)
			s.nowMu.Unlock()
		}
		resp.Error = friendlyErr(id, err).Error()
		return resp, err
	}
	if st == nil {
		return resp, nil
	}
	// Playing 先設:Spotify 播 podcast 或廣告時 item 是 null(沒帶 additional_types),那也是「正在播」,跟隨規則要看得到。
	resp.Playing = st.Playing
	resp.Device = &nowDevice{Name: st.Device.Name, Type: st.Device.Type,
		VolumePct: st.Device.VolumePct, VolumeKnown: st.Device.VolumeKnown}
	if st.Track != nil {
		t := toWebTrack(*st.Track)
		resp.Track, resp.PositionMS = &t, st.ProgressMS
	}
	return resp, nil
}

// playback:每個 provider 的 PlaybackController 建一次就快取(同 runTUI 的先例),用伺服器 ctx。
// double-check——鎖內只看快取,建構在鎖外。newProvider 可能要等 <key>.token.lock(伺服器 ctx,沒有逾時),
// 而 dropNow() 在 handleRun 的收尾路徑上要拿同一把 nowMu:建構若在鎖內,auth 命令的 exit 事件會被面板的 poll 卡住,
// 使用者看到的是「命令卡住」。面板卡住是設計上接受的代價(staleNow 承擔),/api/run 的完成事件被卡住不是。
// 競態下重複建一個丟掉即可(newProvider 沒有副作用);建的期間 dropNow 過(登出)就這一次照用、不留。
// 建不起來(沒登入、平台不支援播放)不在這裡記:consult 把錯誤的結果快取 webNowFailTTL,期間根本不會走到這裡。
func (s *webServer) playback(id string) (provider.PlaybackController, error) {
	s.nowMu.Lock()
	pc, ok := s.now[id]
	s.nowMu.Unlock()
	if ok {
		return pc, nil
	}
	gen := s.nowGen.Load()
	p, err := newProvider(s.ctx, id)
	if err != nil {
		return nil, err
	}
	built, err := asPlayback(p)
	if err != nil {
		return nil, err
	}
	s.nowMu.Lock()
	defer s.nowMu.Unlock()
	if pc, ok := s.now[id]; ok { // 別人先建好了:用它的,丟掉自己這個
		return pc, nil
	}
	if s.nowGen.Load() != gen {
		return built, nil
	}
	if s.now == nil {
		s.now = map[string]provider.PlaybackController{}
	}
	s.now[id] = built
	return built, nil
}

// dropNow:作廢快取的 controller 與結果。時機:auth 相關命令或 config set 結束後(帳號 / 預設平台換了)。
// shown 不清:config set language 也會走到這裡,換語系不該讓面板從 Spotify 跳回預設平台。
func (s *webServer) dropNow() {
	s.nowMu.Lock()
	s.nowGen.Add(1)
	s.now = nil
	s.nowCache = nil
	s.nowMu.Unlock()
	s.lastNow.Store(nil) // 快照一起丟:否則 auth logout 之後,已登出帳號的那首歌還會被 staleNow 端出來一次
}

// setNow:每一輪都記(包括全用快取回答的那幾輪),stale_ms 才是「距離上一輪多久」——若是「距離上次真的打 Spotify 多久」,
// 碰上 15 秒的有效期,一次 TryLock 失敗就會超過 STALE_DEAD_MS,面板誤判失聯。follow:自動模式才記 shown(釘住的那一輪不算)。
func (s *webServer) setNow(resp *nowResponse, follow bool) {
	s.lastNow.Store(&nowSnapshot{resp: resp, at: webNowClock()})
	if follow {
		p := resp.Provider
		s.shown.Store(&p)
	}
}

// staleNow:上一輪的結果加 stale 標記;正在播的進度照樣往前推(不然一次 TryLock 失敗,進度條就倒退)。
// 釘住時認平台:快照是另一家的就不端出來(config set default_provider 之後、重建那一次特別容易走到)——寧可回空的 stale,
// 也不要顯示不是這個平台在放的歌。還沒有任何一輪就回空的 stale(頁面顯示「讀取中」而不是凍住)。
func (s *webServer) staleNow(pin string) *nowResponse {
	snap := s.lastNow.Load()
	if snap == nil || snap.resp == nil || (pin != "" && snap.resp.Provider != pin) {
		id := pin
		if id == "" {
			id = loadDefaultProvider()
			if p := s.shown.Load(); p != nil {
				id = *p
			}
		}
		return &nowResponse{Provider: id, Stale: true}
	}
	age := webNowClock().Sub(snap.at)
	cp := *advance(snap.resp, age)
	cp.Stale = true
	cp.StaleMS = age.Milliseconds()
	return &cp
}

// webNowInvalidatedBy:這個命令跑完要不要作廢快取的 controller(帳號或設定變了)。
func webNowInvalidatedBy(path string) bool {
	return strings.HasPrefix(path, "capy auth") || path == "capy config set"
}

// webNowSettledBy:這個命令會改變播放狀態——跑完後進安定期(播放列按鈕、快捷鍵、搜尋頁的 play --id、主控台都經過這裡)。
func webNowSettledBy(path string) bool {
	switch path {
	case "capy play", "capy pause", "capy next", "capy prev", "capy seek", "capy vol":
		return true
	}
	return false
}

func (s *webServer) settleNow() {
	s.settleUntil.Store(webNowClock().Add(webNowSettle).UnixNano())
}
