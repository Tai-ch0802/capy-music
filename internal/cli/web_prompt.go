package cli

// 提示橋(P7 決策 40 第 3–4 點):huh 表單不動,web 模式把每個 package var 接縫換成「送 prompt 事件到瀏覽器、等 POST 回答」,
// 每個接縫的取消回傳值照抄原本(errCancelled / huh.ErrUserAborted / (false, nil)),命令本體一個位元組不改。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/resolve"
)

// webPromptTimeout:job 級——逾時先砍整個 job 再回該接縫的取消值,讓 reviewLoop 的下一筆立刻回 ErrUserAborted →
// 整輪不寫入、pull.lock 立刻放。只套在會握 pull.lock 的路徑;auth login * 不設(handleRun)。測試替換點。
var webPromptTimeout = 5 * time.Minute

// webAuthPromptTimeout:auth login * 的提示上限。這條路不取任何鎖,使用者要開 DevTools 抄兩個 token,
// 所以放寬到 30 分鐘;但不是 0——沒有上限等於讓被放生的分頁永久占住序列槽(review #60)。測試替換點。
var webAuthPromptTimeout = 30 * time.Minute

type webPrompt struct {
	Kind        string     `json:"kind"` // confirm | select | input | form
	Title       string     `json:"title"`
	Note        *webNote   `json:"note,omitempty"`
	Options     []string   `json:"options,omitempty"`
	Fields      []webField `json:"fields,omitempty"`
	Affirmative string     `json:"affirmative,omitempty"`
	Negative    string     `json:"negative,omitempty"`
	Default     any        `json:"default,omitempty"`
	Error       string     `json:"error,omitempty"` // 上一次答案沒過接縫層驗證,同 kind、新 id 重問時帶著
}

type webNote struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type webField struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Secret bool   `json:"secret,omitempty"` // 值只在 answer body 裡經 loopback 進行程,不進事件、不記 log、不回顯
	// 驗證失敗重問時把上一輪的值帶回去,使用者只要改錯的那一欄(終端機的 huh 本來就是這樣)。
	// secret 欄不能帶值(決策 40–41),只帶 Filled:前端顯示「已填,留空 = 沿用上次」,伺服器端沿用。
	Value  string `json:"value,omitempty"`
	Filled bool   `json:"filled,omitempty"`
}

// refill:重問前把上一輪的答案帶回欄位——非 secret 欄回填值,secret 欄只標「已填」。
func refill(fields []webField, last map[string]string) {
	for i := range fields {
		f := &fields[i]
		if f.Secret {
			f.Value, f.Filled = "", last[f.Name] != ""
			continue
		}
		f.Value = last[f.Name]
	}
}

// keepSecrets:secret 欄留空 = 沿用上一輪的值(值從沒離開過行程)。
func keepSecrets(fields []webField, v, last map[string]string) {
	for _, f := range fields {
		if f.Secret && strings.TrimSpace(v[f.Name]) == "" && last[f.Name] != "" {
			v[f.Name] = last[f.Name]
		}
	}
}

type webPromptEvent struct {
	Type string `json:"type"`
	ID   int    `json:"id"`
	webPrompt
}

// webAnswer:瀏覽器 → 伺服器;Cancel = Esc / 關掉。
type webAnswer struct {
	ID     int             `json:"id"`
	Cancel bool            `json:"cancel"`
	Value  json.RawMessage `json:"value"`
}

func (a webAnswer) boolValue() bool     { var b bool; _ = json.Unmarshal(a.Value, &b); return b }
func (a webAnswer) intValue() int       { var i int; _ = json.Unmarshal(a.Value, &i); return i }
func (a webAnswer) stringValue() string { var s string; _ = json.Unmarshal(a.Value, &s); return s }
func (a webAnswer) formValue() map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal(a.Value, &m)
	return m
}

type pendingPrompt struct {
	id int
	p  webPrompt
}

// validateAnswer:伺服器驗型別與範圍(不合 400,pending 不動,前端重送)。
func validateAnswer(p webPrompt, a webAnswer) error {
	if a.Cancel {
		return nil
	}
	switch p.Kind {
	case "confirm":
		var b bool
		if json.Unmarshal(a.Value, &b) != nil {
			return errors.New("confirm 的答案要是 true / false")
		}
	case "select":
		var i int
		if json.Unmarshal(a.Value, &i) != nil || i < 0 || i >= len(p.Options) {
			return fmt.Errorf("select 的答案要是 0–%d 的整數", len(p.Options)-1)
		}
	case "input":
		var s string
		if json.Unmarshal(a.Value, &s) != nil {
			return errors.New("input 的答案要是字串")
		}
	case "form":
		var m map[string]string
		if json.Unmarshal(a.Value, &m) != nil {
			return errors.New("form 的答案要是 {欄位: 字串}")
		}
		for _, f := range p.Fields {
			if _, ok := m[f.Name]; !ok {
				return fmt.Errorf("form 缺欄位 %s", f.Name)
			}
		}
	default:
		return errors.New("未知的提示種類")
	}
	return nil
}

// ask:送 prompt 事件、等答案。(answer, nil) = 有答案(含 Cancel);err = ctx 取消 / 逾時 / 串流已關 / 沒有 job。
// 各接縫把這些對回原本 huh 版的取消值。
func (s *webServer) ask(p webPrompt) (webAnswer, error) {
	j := s.current()
	if j == nil { // 接縫只在 runMu 內的 ExecuteContext 期間執行;沒有 job 是程式錯,回錯而不是掛住
		return webAnswer{}, errors.New("web 提示只能在命令執行中出現(沒有目前 job)")
	}
	return j.ask(p)
}

func (j *webJob) ask(p webPrompt) (webAnswer, error) {
	j.mu.Lock()
	j.promptSeq++
	id := j.promptSeq
	j.pending = &pendingPrompt{id: id, p: p}
	select { // 防禦:上一題不可能留答案(handleAnswer 只在 pending 相符時送),但 cap 1 的 channel 要乾淨
	case <-j.answers:
	default:
	}
	j.mu.Unlock()
	if err := j.sse.event(webPromptEvent{Type: "prompt", ID: id, webPrompt: p}); err != nil {
		j.clearPending(id)
		return webAnswer{}, err
	}
	var timeout <-chan time.Time
	if j.promptTimeout > 0 {
		t := time.NewTimer(j.promptTimeout)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case a := <-j.answers:
		reason := "answered"
		if a.Cancel {
			reason = "dismissed"
		}
		j.closed(id, reason)
		return a, nil
	case <-j.ctx.Done():
		j.clearPending(id)
		j.closed(id, "cancelled")
		return webAnswer{}, context.Cause(j.ctx)
	case <-timeout:
		j.clearPending(id)
		j.cancel(errPromptTimeout) // 砍整個 job:握著 pull.lock 的路徑不能等到天荒地老
		j.closed(id, "timeout")
		return webAnswer{}, errPromptTimeout
	}
}

func (j *webJob) clearPending(id int) {
	j.mu.Lock()
	if j.pending != nil && j.pending.id == id {
		j.pending = nil
	}
	j.mu.Unlock()
}

func (j *webJob) closed(id int, reason string) {
	_ = j.sse.event(map[string]any{"type": "prompt_closed", "id": id, "reason": reason})
}

// handleAnswer:POST /api/jobs/{job}/answer。id 不符或沒有 pending → 409;型別不合 → 400(pending 不動)。
func (s *webServer) handleAnswer(w http.ResponseWriter, r *http.Request) {
	var a webAnswer
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(body).Decode(&a); err != nil {
		httpErr(w, http.StatusBadRequest, "JSON 壞掉:"+err.Error())
		return
	}
	_, _ = io.Copy(io.Discard, body)
	j := s.current()
	if j == nil || j.id != r.PathValue("job") {
		httpErr(w, http.StatusNotFound, "沒有這個 job(已結束?)")
		return
	}
	j.mu.Lock()
	p := j.pending
	if p == nil || p.id != a.ID {
		j.mu.Unlock()
		httpErr(w, http.StatusConflict, "沒有等待中的提示,或 id 不符(遲到的答案不收)")
		return
	}
	if err := validateAnswer(p.p, a); err != nil {
		j.mu.Unlock()
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	j.pending = nil
	select { // 在鎖內入列:「pending 相符」與「答案入列」是同一個原子動作,ask 的 drain 才真的只會看到自己那一題
	case j.answers <- a:
	default:
	}
	j.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// ── 接縫的橋接版 ──

// installWebPromptSeams:互動閘翻成 true(argsOrPicker / needTarget 放行 0 參數、play 歧義開挑選器、三家精靈走替身、
// 七個確認閘會問),每個 huh 接縫換成提示橋。回還原函式。
func installWebPromptSeams(s *webServer) (restore func()) {
	origInteractive, origStdin, origBoth := isInteractive, stdinIsTTY, bothTTY
	origConfirm, origPick, origName, origReview := confirmWrite, pickOne, promptNewName, reviewPrompt
	origCID, origDisclosure, origApple := runClientIDWizard, confirmAppleDisclosure, runAppleWizardInputs
	origGoogle, origGoogleSecret, origOpen := googleWizard, googleSecretPrompt, openBrowser
	isInteractive = func(*cobra.Command) bool { return true }
	stdinIsTTY = func() bool { return true }
	bothTTY = func(*cobra.Command) bool { return true } // reviewIsTTY / migrateIsTTY 委派到這裡(T1)
	confirmWrite = s.webConfirmWrite
	pickOne = s.webPickOne
	promptNewName = s.webPromptNewName
	reviewPrompt = s.webReviewPrompt
	runClientIDWizard = s.webClientIDWizard
	confirmAppleDisclosure = s.webAppleDisclosure
	runAppleWizardInputs = s.webAppleWizardInputs
	googleWizard = s.webGoogleWizard
	googleSecretPrompt = s.webGoogleSecretPrompt
	openBrowser = s.webOpenBrowser
	return func() {
		isInteractive, stdinIsTTY, bothTTY = origInteractive, origStdin, origBoth
		confirmWrite, pickOne, promptNewName, reviewPrompt = origConfirm, origPick, origName, origReview
		runClientIDWizard, confirmAppleDisclosure, runAppleWizardInputs = origCID, origDisclosure, origApple
		googleWizard, googleSecretPrompt, openBrowser = origGoogle, origGoogleSecret, origOpen
	}
}

// webConfirmWrite:按「取消」回 (false, nil)(pull.go → PendingError exit 2,同終端機選取消);
// 關掉 / 逾時 / ctx 取消回 (false, huh.ErrUserAborted)(pull.go 原樣 return → exit 1;reviewLoop 當「不同意合併」)。
func (s *webServer) webConfirmWrite(prompt string) (bool, error) {
	a, err := s.ask(webPrompt{Kind: "confirm", Title: prompt, Affirmative: "套用", Negative: "取消", Default: false})
	if err != nil || a.Cancel {
		return false, huh.ErrUserAborted
	}
	return a.boolValue(), nil
}

func (s *webServer) webPickOne(title string, labels []string) (int, error) {
	a, err := s.ask(webPrompt{Kind: "select", Title: title, Options: labels})
	if err != nil || a.Cancel {
		return 0, errCancelled
	}
	return a.intValue(), nil
}

func (s *webServer) webPromptNewName(title string) (string, error) {
	a, err := s.ask(webPrompt{Kind: "input", Title: title})
	if err != nil || a.Cancel {
		return "", errCancelled
	}
	name := strings.TrimSpace(a.stringValue())
	if name == "" {
		return "", errCancelled
	}
	return name, nil
}

// webReviewPrompt:三段式鏡像 resolve.go 的 reviewPrompt:第一層取消 = 整輪不寫入(ErrUserAborted);manual 的搜尋字串
// 取消也是;第三層(候選)取消 = 這筆略過。寫入邏輯全在 applyDecision,兩版共用。
func (s *webServer) webReviewPrompt(it resolveItem, pos, total int, search func(string) ([]provider.Track, error)) (reviewDecision, error) {
	title := fmt.Sprintf("[%d/%d] %s — %s(%s)\n%s:%s", pos, total, it.track.Title, strings.Join(it.track.Artists, ", "), mmss(it.track.DurationMS), it.prov, it.reason)
	var labels, kinds []string
	if it.cand != nil {
		labels, kinds = append(labels, fmt.Sprintf("接受 %d 分候選:%s", it.score, describe(*it.cand))), append(kinds, "accept")
	}
	if it.action == "conflict" {
		labels, kinds = append(labels, "釘住現有 mapping "+it.current.ID+"(人確認過,之後不再問)"), append(kinds, "keep")
	}
	labels = append(labels, "略過(下次再問)", "手動搜尋", "這個平台沒有這首(釘成不可得)")
	kinds = append(kinds, "skip", "manual", "none")
	a, err := s.ask(webPrompt{Kind: "select", Title: title, Options: labels})
	if err != nil || a.Cancel {
		return reviewDecision{}, huh.ErrUserAborted
	}
	d := reviewDecision{kind: kinds[a.intValue()]}
	switch d.kind {
	case "accept":
		d.cand = it.cand
	case "manual":
		a, err := s.ask(webPrompt{Kind: "input", Title: "搜尋字串", Default: resolve.FuzzyQuery(it.track)})
		if err != nil || a.Cancel {
			return reviewDecision{}, huh.ErrUserAborted
		}
		found, err := search(a.stringValue())
		if err != nil {
			return reviewDecision{}, err
		}
		if len(found) == 0 {
			return reviewDecision{kind: "skip"}, nil
		}
		picks := make([]string, len(found))
		for i, t := range found {
			picks[i] = fmt.Sprintf("%d 分  %s", resolve.ScoreFuzzy(it.track, t), describe(t))
		}
		a, err = s.ask(webPrompt{Kind: "select", Title: "選一首釘上(關掉 = 略過)", Options: picks})
		if err != nil {
			return reviewDecision{}, huh.ErrUserAborted
		}
		if a.Cancel {
			return reviewDecision{kind: "skip"}, nil
		}
		d.cand = &found[a.intValue()]
	}
	return d, nil
}

func (s *webServer) webClientIDWizard() (string, error) {
	p := webPrompt{Kind: "form", Title: "Client ID", Note: &webNote{Title: spotifyAppTitle, Body: spotifyAppSteps},
		Fields: []webField{{Name: "client_id", Label: "Client ID"}}}
	last := map[string]string{}
	for {
		refill(p.Fields, last)
		a, err := s.ask(p)
		if err != nil || a.Cancel {
			return "", huh.ErrUserAborted
		}
		last = a.formValue()
		cid := strings.TrimSpace(last["client_id"])
		if err := validateSpotifyClientID(cid); err != nil {
			p.Error = err.Error()
			continue
		}
		return cid, nil
	}
}

// webAppleDisclosure:揭露不可跳過,由伺服器判定——答案不是 true 就是「已取消(未同意聲明)」,前端 checkbox 不被信任。
func (s *webServer) webAppleDisclosure() error {
	a, err := s.ask(webPrompt{Kind: "confirm", Title: "我已閱讀,同意自負風險,繼續?", Note: &webNote{Title: "使用前請先閱讀", Body: appleDisclosure},
		Affirmative: "同意", Negative: "取消", Default: false})
	if err != nil || a.Cancel {
		return huh.ErrUserAborted
	}
	if !a.boolValue() {
		return errors.New("已取消(未同意聲明)")
	}
	return nil
}

func (s *webServer) webAppleWizardInputs(hasUser bool) (dev, user string, err error) {
	onlyDev := hasUser
	if hasUser {
		a, err := s.ask(webPrompt{Kind: "confirm", Title: "keychain 已有 user token。只更新 developer token?",
			Affirmative: "只更新 developer token", Negative: "兩個都重新貼", Default: true})
		if err != nil || a.Cancel {
			return "", "", huh.ErrUserAborted
		}
		onlyDev = a.boolValue()
	}
	// dev 欄刻意不設 Secret,與 huh 版一致(那邊也只有 user token 是 EchoModePassword):它是一長串 JWT,
	// 貼錯要看得出來;`autocomplete=off` 已設。它一樣不得出現在任何事件裡,由測試釘住。
	fields := []webField{{Name: "dev", Label: "developer token(authorization 標頭的值)"}}
	if !onlyDev {
		fields = append(fields, webField{Name: "user", Label: "user token(media-user-token 標頭的值)", Secret: true})
	}
	p := webPrompt{Kind: "form", Title: "貼上 token", Note: &webNote{Title: "從網頁播放器複製 token", Body: appleGuide}, Fields: fields}
	last := map[string]string{}
	for {
		refill(p.Fields, last)
		a, err := s.ask(p)
		if err != nil || a.Cancel {
			return "", "", huh.ErrUserAborted
		}
		v := a.formValue()
		keepSecrets(p.Fields, v, last) // user token 留空 = 沿用上次:dev token 過期重問時不必回 DevTools 重抄
		last = v
		if err := validateAppleDevToken(v["dev"]); err != nil {
			p.Error = "developer token:" + err.Error()
			continue
		}
		if !onlyDev && strings.TrimSpace(v["user"]) == "" {
			p.Error = "user token:不可為空"
			continue
		}
		return v["dev"], v["user"], nil
	}
}

func (s *webServer) webGoogleWizard() (id, sec string, err error) {
	p := webPrompt{Kind: "form", Title: "Google OAuth client", Note: &webNote{Title: "Google Drive 同步:先建自己的 OAuth client", Body: googleGuide},
		Fields: []webField{
			{Name: "client_id", Label: "Client ID(結尾通常是 .apps.googleusercontent.com)"},
			{Name: "client_secret", Label: "Client secret(可留空試試看;G-0 驗收會確定 Desktop client 要不要)", Secret: true},
		}}
	last := map[string]string{}
	for {
		refill(p.Fields, last)
		a, err := s.ask(p)
		if err != nil || a.Cancel {
			return "", "", huh.ErrUserAborted
		}
		v := a.formValue()
		keepSecrets(p.Fields, v, last)
		last = v
		if strings.TrimSpace(v["client_id"]) == "" {
			p.Error = "Client ID:必填"
			continue
		}
		return strings.TrimSpace(v["client_id"]), strings.TrimSpace(v["client_secret"]), nil
	}
}

func (s *webServer) webGoogleSecretPrompt(clientID string) (string, error) {
	a, err := s.ask(webPrompt{Kind: "form", Title: "Client secret",
		Note:   &webNote{Title: "找不到這個 client 的 secret", Body: "client id " + maskGoogleClientID(clientID) + " 還在 config,但 secret 不在 keychain(capy auth logout google 會刪掉它)。\n貼上 secret;留空則試試看不帶 secret(Desktop client 是否必須帶 secret 由 G-0 驗收決定)。"},
		Fields: []webField{{Name: "client_secret", Label: "Client secret", Secret: true}}})
	if err != nil || a.Cancel {
		return "", huh.ErrUserAborted
	}
	return strings.TrimSpace(a.formValue()["client_secret"]), nil
}

// webOpenBrowser:不 exec open(伺服器那台機器不一定是使用者面前這台,而且會開兩次);送 open_url 事件,前端給可點連結。
// 授權 URL 也會經 LoginStderr 落進 job 的 stderr 事件,連結沒渲染也看得到。
func (s *webServer) webOpenBrowser(url string) error {
	j := s.current()
	if j == nil {
		return errors.New("沒有目前 job")
	}
	return j.sse.event(map[string]any{"type": "open_url", "url": url})
}
