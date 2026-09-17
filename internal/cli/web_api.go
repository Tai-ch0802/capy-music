package cli

// 兩個直達端點(P7 決策 42):ISRC 查詢與播放面板。它們不經 cobra、不進 runMu、不呼叫 defaultProvider()——
// 會碰的接縫只有 BackoffStderr / LockStderr 兩個 stderr 全域(已序列化)與 package var newProvider(不換)。
// 共享點另有 SQLite,唯讀開、零副作用。

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

// webNowWait:handler 最多等這麼久就回上一次的快照;真正的 State 用伺服器 ctx 在背景跑完
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
			httpErr(w, http.StatusBadRequest, "未知的 provider "+q)
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
		resp.CanonicalError = err.Error()
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

// nowProvider:--provider 有明指就用它(同 runTUI),否則每次讀 config 的 default_provider
// (不呼叫 defaultProvider():那是 sync.OnceValue,長駐行程會凍在啟動當下的值)。
func (s *webServer) nowProvider(q string) string {
	if q != "" {
		return q
	}
	if s.provFlag != "" {
		return s.provFlag
	}
	return loadDefaultProvider()
}

func (s *webServer) handleNow(w http.ResponseWriter, r *http.Request) {
	id := s.nowProvider(r.URL.Query().Get("provider"))
	if !isProviderID(id) {
		httpErr(w, http.StatusBadRequest, "未知的 provider "+id)
		return
	}
	// 單飛:上一次 poll 還在等 token 鎖或平台回應時,立刻回上次快照,不排隊、不堆 goroutine。
	if !s.pollMu.TryLock() {
		writeJSON(w, s.staleNow(id))
		return
	}
	done := make(chan *nowResponse, 1)
	go func() {
		defer s.pollMu.Unlock()
		resp := s.pollNow(id)
		s.setNow(resp)
		done <- resp
	}()
	select {
	case resp := <-done:
		writeJSON(w, resp)
	case <-time.After(webNowWait):
		// 背景那個 goroutine 繼續跑完(伺服器 ctx,不被腰斬),寫回快照後才放 pollMu。
		writeJSON(w, s.staleNow(id))
	case <-r.Context().Done():
	}
}

// pollNow:用伺服器 ctx——token 換發(TokenSource 等 <key>.token.lock 用的是建構時的 ctx、s.mu 橫跨等鎖)
// 不能被 handler 的逾時砍掉。面板卡住的代價由 staleNow 承擔,不是讓 refresh 半途而廢。
func (s *webServer) pollNow(id string) *nowResponse {
	resp := &nowResponse{Provider: id}
	pc, err := s.playback(id)
	if err != nil {
		resp.Error = friendlyErr(id, err).Error()
		return resp
	}
	st, err := pc.State(s.ctx)
	if err != nil {
		s.dropNow() // 壞掉的 controller 不留著:下次重建(換過帳號 / 換過裝置)
		resp.Error = friendlyErr(id, err).Error()
		return resp
	}
	if st == nil || st.Track == nil {
		return resp
	}
	t := toWebTrack(*st.Track)
	resp.Playing, resp.Track, resp.PositionMS = st.Playing, &t, st.ProgressMS
	resp.Device = &nowDevice{Name: st.Device.Name, Type: st.Device.Type,
		VolumePct: st.Device.VolumePct, VolumeKnown: st.Device.VolumeKnown}
	return resp
}

// playback:每個 provider 的 PlaybackController 建一次就快取(同 runTUI 的先例),用伺服器 ctx。
func (s *webServer) playback(id string) (provider.PlaybackController, error) {
	s.nowMu.Lock()
	defer s.nowMu.Unlock()
	if pc, ok := s.now[id]; ok {
		return pc, nil
	}
	p, err := newProvider(s.ctx, id)
	if err != nil {
		return nil, err
	}
	pc, err := asPlayback(p)
	if err != nil {
		return nil, err
	}
	if s.now == nil {
		s.now = map[string]provider.PlaybackController{}
	}
	s.now[id] = pc
	return pc, nil
}

// dropNow:作廢快取的 controller。時機:auth 相關命令或 config set 結束後(帳號 / 預設平台換了)、State 回錯。
func (s *webServer) dropNow() {
	s.nowMu.Lock()
	s.now = nil
	s.nowMu.Unlock()
}

func (s *webServer) setNow(resp *nowResponse) {
	s.lastNow.Store(&nowSnapshot{resp: resp, at: time.Now()})
}

// staleNow:上一次的快照加 stale 標記;還沒有任何快照就回一個空的 stale 回應(頁面顯示「讀取中」而不是凍住)。
func (s *webServer) staleNow(id string) *nowResponse {
	snap := s.lastNow.Load()
	if snap == nil || snap.resp == nil {
		return &nowResponse{Provider: id, Stale: true}
	}
	cp := *snap.resp
	cp.Stale = true
	cp.StaleMS = time.Since(snap.at).Milliseconds()
	return &cp
}

// webNowInvalidatedBy:這個命令跑完要不要作廢快取的 controller(帳號或設定變了)。
func webNowInvalidatedBy(path string) bool {
	return strings.HasPrefix(path, "capy auth") || path == "capy config set"
}
