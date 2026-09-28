package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

// httpClient:不設 Timeout(chat 要串流,reasoning model 第一個 token 可能等幾十秒);期限由呼叫端的 ctx 給。測試替換點。
var httpClient = &http.Client{}

// HTTPError:非 2xx。Body 是截過、遮過金鑰的節錄(有些端點 401 會把 key 回顯在 body 裡,這句會印到終端機與網頁)。
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

var keyShaped = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`)

// IncompleteError:回答沒寫完——串流沒收到 [DONE] 就斷了(proxy / Zero Trust 斷線、端點中途掛掉),或端點自己的
// token 上限到了(finish_reason == "length")。已經印出來的正文照留,但呼叫端不能把半截當完整的存起來(#114 review)。
type IncompleteError struct{ Reason string } // eof | length

func (e *IncompleteError) Error() string { return i18n.T("ai.err.incomplete", "reason", e.Reason) }

func scrub(body []byte) string {
	s := strings.TrimSpace(string(body))
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return keyShaped.ReplaceAllString(s, "sk-***")
}

// newRequest:URL = BaseURL + path;JSON body;Bearer(有金鑰才帶);使用者的標頭**最後**套(Set:自己寫的 Authorization 蓋掉 Bearer、不重複)。
func (c Config) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for k, vs := range c.Headers {
		if len(vs) > 0 {
			req.Header.Set(k, vs[len(vs)-1])
		}
	}
	return req, nil
}

func (c Config) do(req *http.Request) (*http.Response, error) {
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, &HTTPError{Status: resp.StatusCode, Body: scrub(b)}
	}
	return resp, nil
}

// Models:GET /models 的 id 清單(照端點給的順序)。404 / 405 回 ErrNoModelList(有些相容端點沒實作這條)。
func (c Config) Models(ctx context.Context) ([]string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && (he.Status == http.StatusNotFound || he.Status == http.StatusMethodNotAllowed) {
			return nil, ErrNoModelList
		}
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, i18n.Errorf("ai.err.bad_json", "err", err)
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// ChatRequest:一次 chat/completions。MaxTokens 0 = 不帶。
type ChatRequest struct {
	System    string
	User      string
	MaxTokens int
}

// Ping:1-token 的 chat,任何 2xx 都算通(reasoning model 對 1 token 可能回空字串)。/models 不可用時 setup 用它驗 model。
func (c Config) Ping(ctx context.Context) error {
	_, err := c.chat(ctx, ChatRequest{User: "ping", MaxTokens: 1}, false, nil, true)
	return err
}

// Chat:串流版。delta 在這裡拼,湊到 \n 才回呼 onLine 一次(不含換行;結束時把沒有換行的尾巴也回呼)——
// 終端機要看到整行才知道它是不是標題,web 那邊本來就只渲染完整行。回整段文字。
// 端點回的不是 text/event-stream(有些 proxy 無視 stream)就整批解析。400 而且訊息提到 max_completion_tokens 就換參數重試一次
// (400 在任何 delta 之前到,重試安全)。回空字串是錯(ai.err.empty)。
func (c Config) Chat(ctx context.Context, req ChatRequest, onLine func(string)) (string, error) {
	return c.chat(ctx, req, true, onLine, false)
}

// ChatOnce:不串流,等整份回來(--json 用)。
func (c Config) ChatOnce(ctx context.Context, req ChatRequest) (string, error) {
	return c.chat(ctx, req, false, nil, false)
}

// chat:probe(1-token 探測)時空字串與「上限到了」都不算錯——那次本來就只要一個 token。
// 不完整的回答(*IncompleteError)連同已收到的文字一起回:呼叫端印過的照留、不進快取。
func (c Config) chat(ctx context.Context, req ChatRequest, stream bool, onLine func(string), probe bool) (string, error) {
	text, err := c.chatWith(ctx, req, stream, onLine, "max_tokens")
	var he *HTTPError
	if errors.As(err, &he) && he.Status == http.StatusBadRequest && strings.Contains(he.Body, "max_completion_tokens") {
		text, err = c.chatWith(ctx, req, stream, onLine, "max_completion_tokens")
	}
	var inc *IncompleteError
	if errors.As(err, &inc) {
		if probe {
			return text, nil
		}
		return text, err
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" && !probe {
		return "", i18n.Errorf("ai.err.empty")
	}
	return text, nil
}

func (c Config) chatWith(ctx context.Context, req ChatRequest, stream bool, onLine func(string), tokenParam string) (string, error) {
	msgs := []map[string]string{}
	if req.System != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": req.System})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": req.User})
	body := map[string]any{"model": c.Model, "messages": msgs}
	if req.MaxTokens > 0 {
		body[tokenParam] = req.MaxTokens
	}
	if stream {
		body["stream"] = true
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	hr, err := c.newRequest(ctx, http.MethodPost, "/chat/completions", b)
	if err != nil {
		return "", err
	}
	resp, err := c.do(hr)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	lines := &lineBuffer{onLine: onLine}
	var finish string
	if stream && ct == "text/event-stream" {
		done, reason, err := readSSE(resp.Body, lines)
		if err != nil {
			return "", err
		}
		lines.flush()
		if !done { // 沒收到 [DONE] 就 EOF:連線在中途斷了,手上這一截不是整份
			return lines.all.String(), &IncompleteError{Reason: "eof"}
		}
		finish = reason
	} else {
		var out completion
		if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
			return "", i18n.Errorf("ai.err.bad_json", "err", err)
		}
		if out.Error != nil {
			return "", i18n.Errorf("ai.err.stream_error", "message", out.Error.Message)
		}
		if len(out.Choices) > 0 {
			lines.write(out.Choices[0].Message.Content)
			finish = out.Choices[0].FinishReason
		}
		lines.flush()
	}
	if finish == "length" { // 端點(或它前面的 proxy)自己的 token 上限到了:寫到一半被砍
		return lines.all.String(), &IncompleteError{Reason: "length"}
	}
	return lines.all.String(), nil
}

// completion:chat/completions 的回應(整批與串流的幀共用形狀;沒用到的欄位不解)。
type completion struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"` // "length" = 端點的 token 上限到了,回答不完整
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// readSSE:每個 data: 一幀;[DONE] 結束(done = true);幀裡帶 error 就報錯(LiteLLM / OpenRouter 會在 HTTP 200 的串流裡
// 塞錯誤幀,不能默默回一截半的文字)。解不開的幀跳過(keep-alive 註解、別家的擴充)。沒看到 [DONE] 就 EOF 的話 done = false,
// 呼叫端當成不完整。reason 是最後一幀帶的 finish_reason("length" = 上限到了)。
func readSSE(r io.Reader, lines *lineBuffer) (done bool, reason string, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(strings.TrimRight(sc.Text(), "\r"), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			return true, reason, nil
		}
		var fr completion
		if err := json.Unmarshal([]byte(data), &fr); err != nil {
			continue
		}
		if fr.Error != nil {
			return false, reason, i18n.Errorf("ai.err.stream_error", "message", fr.Error.Message)
		}
		if len(fr.Choices) > 0 {
			lines.write(fr.Choices[0].Delta.Content)
			if fr.Choices[0].FinishReason != "" {
				reason = fr.Choices[0].FinishReason
			}
		}
	}
	return false, reason, sc.Err()
}

// lineBuffer:delta → 完整的行。onLine 拿到的行不含換行。
type lineBuffer struct {
	onLine func(string)
	all    strings.Builder
	cur    strings.Builder
}

func (l *lineBuffer) write(s string) {
	l.all.WriteString(s)
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			l.cur.WriteString(s)
			return
		}
		l.cur.WriteString(s[:i])
		l.emit()
		s = s[i+1:]
	}
}

func (l *lineBuffer) emit() {
	line := strings.TrimRight(l.cur.String(), "\r")
	l.cur.Reset()
	if l.onLine != nil {
		l.onLine(line)
	}
}

func (l *lineBuffer) flush() {
	if l.cur.Len() > 0 {
		l.emit()
	}
}
