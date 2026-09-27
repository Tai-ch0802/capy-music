package cli

// 兩個直達端點(P7 決策 42):ISRC 查詢與播放面板。它們不經 cobra、不進 runMu、不呼叫 defaultProvider()——
// 會碰的接縫只有 BackoffStderr / LockStderr 兩個 stderr 全域(已序列化)與 package var newProvider(不換)。
// 共享點另有 SQLite,唯讀開、零副作用。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
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

// 播放面板的核心(跟隨規則、每家的快取與有效期、安定期、世代號)在 now_tracker.go,TUI 也用同一套(決策 51、Q64)。
// 這裡只剩 HTTP 的部分:單飛、handler 逾時回上一輪的結果、JSON 形狀。

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

// toNowResponse:追蹤器的結果轉成 /api/now 的 JSON。錯誤經 friendlyErr(Apple token 失效 → 指向 auth login)。
// Playing 跟 Track 分開看:Spotify 播 podcast 或廣告時 item 是 null,那也是「正在播」。
func toNowResponse(r nowResult) *nowResponse {
	resp := &nowResponse{Provider: r.provider}
	if r.err != nil {
		resp.Error = friendlyErr(r.provider, r.err).Error()
	}
	if st := r.st; st != nil {
		resp.Playing = st.Playing
		resp.Device = &nowDevice{Name: st.Device.Name, Type: st.Device.Type,
			VolumePct: st.Device.VolumePct, VolumeKnown: st.Device.VolumeKnown}
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
		defer s.pollMu.Unlock()
		gen := s.nowGen.Load()
		res := s.pollRound(pin)
		resp := toNowResponse(res)
		if !s.setNow(res, pin == "", gen) { // 問的期間 dropNow 過(登出、換帳號):舊帳號的結果不記、也不送
			resp = s.staleNow(pin)
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
		return &nowResponse{Provider: id, Stale: true}
	}
	age := webNowClock().Sub(snap.at)
	resp := toNowResponse(advance(snap.res, age))
	resp.Stale = true
	resp.StaleMS = age.Milliseconds()
	return resp
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
	case "capy play", "capy pause", "capy next", "capy prev", "capy seek", "capy vol":
		return true
	}
	return false
}
