package cli

// nowTracker:「現在在播什麼」的共用核心——web 的播放面板(/api/now)與 TUI 的狀態列都用它(決策 51;Q64)。
// 跟隨規則(只有「正在播」能把顯示拉走)、每家一份 controller 與結果快取、有效期(Spotify 的配額靠它省)、
// 播放命令後的安定期、dropNow 的世代號都在這裡;HTTP 的單飛、逾時與 JSON 形狀留在 web_api.go,按鍵與畫面留在 tui.go。

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 有效期(決策 51;計畫 docs/superpowers/plans/2026-09-24-web-player-follow-apple-play.md §1.2)。畫面照樣每 2–2.5 秒問一輪——
// 那一段只打本機;真的打 Spotify Web API 的次數由這幾個值決定(dev mode 的配額以開發者帳號計,跟 pl sync、cron 共用)。
const (
	webNowFailTTL = time.Minute      // 建不起來(沒登入)、token 失效:一分鐘後再試;沒登入 Apple 的人不會每 2.5 秒讀 keychain
	webNowIdleTTL = 15 * time.Second // Spotify 閒置或暫停(Q65):手機上開始播,最慢 15 秒出現;5xx、網路斷一下也是 15 秒後再問
	// webNowUnsupportedTTL:平台不支援播放(local、Windows 上的 apple)——這個行程裡不會變,dropNow(換帳號、改設定)時才重來。
	webNowUnsupportedTTL = 24 * time.Hour
	webNowPlayTTL        = 10 * time.Second // Spotify 正在播:手機上暫停或換歌,最慢 10 秒出現;這首結束時提早重問
	webNowSettle         = 3 * time.Second  // 播放命令後的安定期:Spotify 的播放器寫入是最終一致,剛按完可能讀到舊狀態
)

// webNowClock:有效期、安定期、stale_ms 都看它。測試替換點。
var webNowClock = time.Now

// nowResult:一家的播放狀態。st 為 nil = 沒有播放內容(或出錯);err 是原始錯誤(TUI 要分辨 Music.app 沒開、限流,
// web 在邊界才轉成給人看的字);retryAt:限流冷卻中,冷卻結束的時間(TUI 說「幾點再試」);fresh:這一次真的問了平台,
// 不是快取(TUI 只拿真的失敗算連續失敗,快取裡的同一則錯誤不重複算)。
type nowResult struct {
	provider string
	st       *provider.PlaybackState
	err      error
	retryAt  time.Time
	fresh    bool
}

func (r nowResult) playing() bool { return r.st != nil && r.st.Playing }

type nowSnapshot struct {
	res nowResult
	at  time.Time
}

// nowEntry:某個 provider 最近一次真的問到的結果;until 之前用它回答(正在播的進度依經過時間往前推)。
// cooldown:這是限流(429 / QUOTA_EXCEEDED)的冷卻——安定期、expireExcept、dropNow 都不縮短它,冷卻期內重問就違反 Retry-After。
// unsupported:平台不支援播放——安定期與 expireExcept 不動它(重問也不會變),dropNow 才清。
type nowEntry struct {
	res         nowResult
	at, until   time.Time
	cooldown    bool
	unsupported bool
}

// nowBuildErr:建不起來(沒登入、平台不支援播放)——跟 State 回錯分開,有效期不同(見 nowTTL)。
type nowBuildErr struct{ error }

func (e nowBuildErr) Unwrap() error { return e.error }

// nowTracker:ctx 是長駐的那一個(web 的伺服器 ctx、TUI 的程式 ctx)——token 換發(TokenSource 等 <key>.token.lock
// 用的是建構時的 ctx)不能被一次輪詢的逾時砍掉。now 是每個 provider 的 PlaybackController(建一次就快取),nowCache 是每家
// 最近一次真的問到的結果與有效期,兩者都由 nowMu 守。lastNow 是上一輪顯示的那份,shown 是上一輪顯示的平台(跟隨規則的 base;
// 只有換帳號、換預設平台時 dropNow 才清)。nowGen 由 dropNow 遞增,在飛的那一輪拿舊世代的結果就不寫回快取與快照;
// settleUntil 是播放命令後的安定期,settleSeq 每次 settleNow 加一(在飛的那一輪若跨過一個播放命令,它讀到的不快取)。
// pollMu:pollRound / consult 同時只能有一個在跑——呼叫端拿著它問一輪並 setNow(web 用 TryLock 單飛,
// TUI 用 Lock:控制鍵起的那一輪等舊鏈在飛的那一輪做完,才看得到它剛寫的快取,不會對同一家重打一次)。
type nowTracker struct {
	ctx         context.Context
	timeout     time.Duration // 每次 State 的上限;0 = 不設(web:handler 自己有 webNowWait,卡住的那一輪由 staleNow 承擔)
	pollMu      sync.Mutex
	nowMu       sync.Mutex
	now         map[string]provider.PlaybackController
	nowCache    map[string]nowEntry
	lastNow     atomic.Pointer[nowSnapshot]
	shown       atomic.Pointer[string]
	nowGen      atomic.Uint64
	settleUntil atomic.Pointer[time.Time]
	settleSeq   atomic.Uint64
}

// pollRound:一輪。釘住就只問那一家。否則照跟隨規則(決策 51):只有「正在播」能把顯示拉走。
// base(上一輪顯示的;還沒有就用 config 的 default_provider,每次重讀——不用 defaultProvider(),它是 OnceValue)在播就顯示它,
// 別家一律不問(照這條規則它們改變不了結果,Spotify 一次都不打);base 沒在播才依序問其他平台(預設先,再照 providerIDs),
// 第一個在播的就切過去;都沒在播就留在 base,不管它是暫停、閒置還是出錯。暫停中的歌不能搶顯示:Spotify 暫停幾分鐘會從
// 200 變 204,這時 Music.app 掛著昨天暫停的那首就會把顯示搶回去——使用者 2026-09-24 回報的就是這個。
// 例外(決策 54):base 根本建不起來(沒登入、這台電腦不支援播放)時,改留在第一個建得起來的平台——不然預設平台沒登入的人,
// 另一家明明登入了、只是暫停,整個播放列(TUI 的按鍵)也只能說「沒有播放遙控」。都建不起來才照實說 base 的原因。
func (t *nowTracker) pollRound(pin string) nowResult {
	if pin != "" {
		return t.consult(pin)
	}
	def := loadDefaultProvider()
	base := def
	if p := t.shown.Load(); p != nil {
		base = *p
	}
	res := t.consult(base)
	if res.playing() {
		return res
	}
	if snap := t.lastNow.Load(); snap != nil && snap.res.provider == base && snap.res.playing() {
		t.expireExcept(base) // base 剛停:免費的觸發點(Apple 每輪都問),馬上看別家有沒有接著播,不等快取過期
	}
	var usable *nowResult // 第一個建得起來的:base 沒登入或不支援播放時,顯示它而不是一句「沒有播放遙控」(決策 54)
	for i, id := range append([]string{def}, providerIDs...) {
		if id == base || (i > 0 && id == def) {
			continue
		}
		r := t.consult(id)
		if r.playing() {
			return r
		}
		if usable == nil && !isBuildErr(r.err) {
			usable = &r
		}
	}
	if usable != nil && isBuildErr(res.err) {
		return *usable
	}
	return res
}

func isBuildErr(err error) bool {
	var be nowBuildErr
	return errors.As(err, &be)
}

// consult:問一家。快取沒過期就用快取;否則真的問,並依結果定有效期。只在 pollMu 裡被呼叫,
// 所以讀快取與寫快取之間只需要防 dropNow——用世代號。
func (t *nowTracker) consult(id string) nowResult {
	now := webNowClock()
	t.nowMu.Lock()
	e, ok := t.nowCache[id]
	t.nowMu.Unlock()
	// 安定期不必在這裡另外判斷:settleNow 已經讓命令之前讀到的快取過期(限流的冷卻與「不支援播放」除外),
	// 安定期內讀到的成功結果也不留(下面的 until = at)。留下來的只剩該守的:冷卻、不支援,以及安定期內讀到的錯誤。
	if ok && now.Before(e.until) {
		return advance(e.res, now.Sub(e.at))
	}
	gen, sg := t.nowGen.Load(), t.settleSeq.Load()
	st, err := t.pollNow(id)
	at := webNowClock()
	until := at.Add(nowTTL(id, st, err))
	// 安定期內讀到的、或問的期間有播放命令跑完的,都可能還是命令之前的狀態:不留,下一輪就重問。
	if err == nil && (t.settling(at) || t.settleSeq.Load() != sg) {
		until = at
	}
	var rl *provider.RateLimitError
	cooldown := errors.As(err, &rl)
	res := nowResult{provider: id, st: st, err: err, fresh: true}
	if cooldown {
		res.retryAt = until
	}
	t.nowMu.Lock()
	if t.nowGen.Load() == gen { // 問的期間 dropNow 過(登出、換帳號):舊世代的結果不寫回
		if t.nowCache == nil {
			t.nowCache = map[string]nowEntry{}
		}
		cached := res
		cached.fresh = false // 之後從快取端出來的就不是真的問的了
		t.nowCache[id] = nowEntry{res: cached, at: at, until: until, cooldown: cooldown, unsupported: errors.Is(err, provider.ErrNotSupported)}
	}
	t.nowMu.Unlock()
	return res
}

// expireExcept:讓 keep 以外的快取立刻過期——包括出錯的(一次 502、沒登入),使用者按了播放或 base 剛停,就值得再問一次。
// 限流的冷卻不動(冷卻期內重問就違反 Retry-After),「不支援播放」也不動(重問也不會變)。
func (t *nowTracker) expireExcept(keep string) {
	t.nowMu.Lock()
	defer t.nowMu.Unlock()
	for id, e := range t.nowCache {
		if id != keep && !e.cooldown && !e.unsupported {
			e.until = time.Time{}
			t.nowCache[id] = e
		}
	}
}

// advance:正在播的結果,進度依經過時間往前推(不超過曲長);其他原樣。
func advance(r nowResult, age time.Duration) nowResult {
	st := r.st
	if st == nil || !st.Playing || st.Track == nil || age <= 0 {
		return r
	}
	cp := *st
	cp.ProgressMS += int(age / time.Millisecond)
	if d := st.Track.DurationMS; d > 0 && cp.ProgressMS > d {
		cp.ProgressMS = d
	}
	r.st = &cp
	return r
}

// nowTTL:這份結果可以用多久。Apple 的 State 是本機 osascript、不花配額,每輪都問——包括回錯的時候(Music.app 沒開、
// 放的是沒有時長的串流):下一輪就看得到變化。建不起來的(沒登入)一分鐘才重試:那要讀 keychain。其他平台的每一問都是一次 Web API 呼叫:
// token 失效一分鐘一次(重建要換發 token);5xx、網路斷一下跟閒置一樣 15 秒——不讓一次 502 把面板卡住一分鐘。
func nowTTL(id string, st *provider.PlaybackState, err error) time.Duration {
	var rl *provider.RateLimitError
	var be nowBuildErr
	switch {
	case errors.As(err, &rl):
		return max(webNowFailTTL, time.Duration(rl.Seconds)*time.Second) // 照 Retry-After:冷卻期內不重試
	case errors.As(err, &be) && errors.Is(err, provider.ErrNotSupported):
		return webNowUnsupportedTTL
	case errors.As(err, &be):
		return webNowFailTTL
	case id == "apple": // Apple 的 State 錯只會來自 osascript(Music.app 沒開、沒有時長的串流),不會是 ErrAuthExpired
		return 0
	case errors.Is(err, provider.ErrAuthExpired):
		return webNowFailTTL
	case err != nil:
		return webNowIdleTTL
	case st == nil || !st.Playing:
		return webNowIdleTTL
	case st.Track == nil || st.Track.DurationMS <= 0:
		return webNowPlayTTL
	}
	left := time.Duration(st.Track.DurationMS-st.ProgressMS)*time.Millisecond + time.Second // 這首結束後約 1 秒就換歌了
	return max(time.Second, min(webNowPlayTTL, left))
}

// pollNow:真的問一家。用長駐的 ctx——token 換發不能被一次輪詢的逾時砍掉。WithoutWait:429 不睡(會凍住整條顯示),
// 冷卻交給 nowTTL。只有 ErrAuthExpired 丟掉那一家的 controller(token 被撤、換過帳號);Music.app 沒開、限流、平台暫時出錯
// 都只是狀態,不重建——以前任何錯都全域 dropNow,Music.app 關著時每 2.5 秒就重建一次 Apple provider(每次 2–3 次 keychain)。
func (t *nowTracker) pollNow(id string) (*provider.PlaybackState, error) {
	pc, err := t.playback(id)
	if err != nil {
		return nil, nowBuildErr{err}
	}
	ctx := provider.WithoutWait(t.ctx)
	if t.timeout > 0 { // TUI:osascript 或 HTTP 卡住時這一輪要回錯(fails → 停擺、按 r 重試),不是讓狀態列永遠停在上一首
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.timeout)
		defer cancel()
	}
	st, err := pc.State(ctx)
	if err != nil {
		if errors.Is(err, provider.ErrAuthExpired) {
			t.nowMu.Lock()
			delete(t.now, id)
			t.nowMu.Unlock()
		}
		return nil, err
	}
	return st, nil
}

// playback:每個 provider 的 PlaybackController 建一次就快取,用長駐的 ctx。TUI 的控制鍵也從這裡拿(送給畫面上那個平台)。
// double-check——鎖內只看快取,建構在鎖外。newProvider 可能要等 <key>.token.lock(沒有逾時),而 dropNow() 在 web
// handleRun 的收尾路徑上要拿同一把 nowMu:建構若在鎖內,auth 命令的 exit 事件會被面板的 poll 卡住,使用者看到的是「命令卡住」。
// 競態下重複建一個丟掉即可(newProvider 沒有副作用);建的期間 dropNow 過(登出)就這一次照用、不留。
// 建不起來(沒登入、平台不支援播放)不在這裡記:consult 把錯誤的結果快取起來,期間根本不會走到這裡。
func (t *nowTracker) playback(id string) (provider.PlaybackController, error) {
	t.nowMu.Lock()
	pc, ok := t.now[id]
	t.nowMu.Unlock()
	if ok {
		return pc, nil
	}
	gen := t.nowGen.Load()
	p, err := newProvider(t.ctx, id)
	if err != nil {
		return nil, err
	}
	built, err := asPlayback(p)
	if err != nil {
		return nil, err
	}
	t.nowMu.Lock()
	defer t.nowMu.Unlock()
	if pc, ok := t.now[id]; ok { // 別人先建好了:用它的,丟掉自己這個
		return pc, nil
	}
	if t.nowGen.Load() != gen {
		return built, nil
	}
	if t.now == nil {
		t.now = map[string]provider.PlaybackController{}
	}
	t.now[id] = built
	return built, nil
}

// dropNow:作廢快取的 controller 與結果。時機:auth login / logout 或 config set 結束後(帳號 / 預設平台換了)。
// 限流的冷卻留著(換帳號、換設定都不會讓 Spotify 的 Retry-After 歸零)。resetShown:換帳號、換預設平台時顯示回到預設平台
// 重新開始(不然登出 Spotify 之後,會一直停在「Spotify 未登入」);換語系不算,那不該讓顯示跳平台。
// 全部在 nowMu 裡做(含 lastNow):在飛的那一輪用世代號比對後才寫 lastNow,兩者之間不能有縫。
func (t *nowTracker) dropNow(resetShown bool) {
	now := webNowClock()
	t.nowMu.Lock()
	defer t.nowMu.Unlock()
	t.nowGen.Add(1)
	t.now = nil
	kept := map[string]nowEntry{}
	for id, e := range t.nowCache {
		if e.cooldown && now.Before(e.until) {
			kept[id] = e
		}
	}
	t.nowCache = kept
	t.lastNow.Store(nil) // 快照一起丟:否則 auth logout 之後,已登出帳號的那首歌還會被端出來一次
	if resetShown {
		t.shown.Store(nil)
	}
}

// setNow:每一輪都記(包括全用快取回答的那幾輪),web 的 stale_ms 才是「距離上一輪多久」——若是「距離上次真的打 Spotify 多久」,
// 碰上 15 秒的有效期,一次 TryLock 失敗就會超過 STALE_DEAD_MS,面板誤判失聯。follow:自動模式才記 shown(釘住的那一輪不算)。
// gen 是這一輪開始時的世代:問的期間 dropNow 過就不記,回 false(已登出帳號的那首歌不能回到快照裡)。
func (t *nowTracker) setNow(res nowResult, follow bool, gen uint64) bool {
	t.nowMu.Lock()
	defer t.nowMu.Unlock()
	if t.nowGen.Load() != gen {
		return false
	}
	t.lastNow.Store(&nowSnapshot{res: res, at: webNowClock()})
	if follow {
		p := res.provider
		t.shown.Store(&p)
	}
	return true
}

// settleNow:進安定期,並讓命令之前讀到的快取立刻過期——不然這幾秒剛好沒有一輪的話,安定期一過,命令之前的「在播」
// 還會被當成新鮮的端出來;畫面上的一次 502 也會在使用者按了播放之後繼續掛著。限流的冷卻與「不支援播放」不動。
func (t *nowTracker) settleNow() {
	at := webNowClock().Add(webNowSettle)
	t.settleUntil.Store(&at)
	t.settleSeq.Add(1)
	t.expireExcept("")
}

// settling:at 還在安定期內。存的是帶單調時鐘的 time.Time,牆上時鐘被往回調也不會把安定期拉長。
func (t *nowTracker) settling(at time.Time) bool {
	p := t.settleUntil.Load()
	return p != nil && at.Before(*p)
}
