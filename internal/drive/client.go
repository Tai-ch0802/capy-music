// Package drive 是 Google Drive appDataFolder 的最小客戶端:list / find / create / update / download / delete。
// 手寫 net/http 而非 google.golang.org/api/drive/v3——量過(2026-09-07,v0.297.0):binary 9.7 MB → 17.3 MB、
// go mod graph 192 → 469 行,換五個端點不划算。假 Drive 在 drivetest/。
//
// 沒有樂觀鎖:v3 沒有 etag、files.update 沒有 precondition,version 只能事後偵測,別設計成依賴 CAS。
package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

const (
	DefaultOrigin = "https://www.googleapis.com"
	// fileFields:files.* 預設只回 id/name/mimeType/kind,version 與 modifiedTime 要明講。
	fileFields = "id,name,version,md5Checksum,modifiedTime,appProperties"
	pageSize   = "1000" // 上限;capy 的檔案數 = 2 + 清單數 + 裝置數,通常一頁
)

var (
	// ErrStorageQuota:403 storageQuotaExceeded。呼叫端可讀、可 dry-run,不可上傳。
	ErrStorageQuota = errors.New("Google Drive 空間已滿,無法上傳(仍可讀取與 dry-run);清出空間後再試")
	// ErrAPINotEnabled:403 accessNotConfigured;包起來的 Google 訊息含啟用連結。
	ErrAPINotEnabled = errors.New("Google Drive API 尚未在你的 Google Cloud 專案啟用")

	// Stderr:警告輸出(stderr 不污染 TSV)。測試替換點。
	Stderr io.Writer = os.Stderr

	// retryReasons:403 裡值得退避重試的 reason;dailyLimitExceeded 等一天內不會好,直接回錯。
	retryReasons = map[string]bool{"userRateLimitExceeded": true, "rateLimitExceeded": true}
)

// APIError:Drive 回的 >=400 且沒有專門映射的錯誤。
type APIError struct {
	Status  int
	Reason  string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("drive API %d %s %s", e.Status, e.Reason, e.Message)
}

// File 是 Drive 檔案 metadata 的子集。Version 在 JSON 是字串(int64 format),沒有 ,string 第一個真回應就解不開。
type File struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Version       int64             `json:"version,string"`
	MD5           string            `json:"md5Checksum"`
	ModifiedTime  time.Time         `json:"modifiedTime"`
	AppProperties map[string]string `json:"appProperties"`
}

// Client 綁一個 origin;hc 應帶 oauth2 transport。
type Client struct {
	hc         *http.Client
	base       string // {origin}/drive/v3
	uploadBase string // {origin}/upload/drive/v3——multipart 上傳的 base path 與其他端點不同
}

// New:origin 正式用 DefaultOrigin,測試指向 drivetest.Server.URL。
func New(hc *http.Client, origin string) *Client {
	return &Client{hc: hc, base: origin + "/drive/v3", uploadBase: origin + "/upload/drive/v3"}
}

// Query 組 files.list 的 q:name(空字串略過)與每個 appProperties 相等,全部 AND。
func Query(name string, props map[string]string) string {
	var terms []string
	if name != "" {
		terms = append(terms, "name = '"+escape(name)+"'")
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		terms = append(terms, fmt.Sprintf("appProperties has { key='%s' and value='%s' }", escape(k), escape(props[k])))
	}
	return strings.Join(terms, " and ")
}

func escape(s string) string { return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) }

// List 列出 appDataFolder 內符合 q 的檔案(q 空 = 全部),自動走完 nextPageToken。
// 一律加 trashed = false:2026-09-07 真帳號實測,appdata 檔可以被 PATCH 成 trashed=true,而 v3 的
// files.list 預設會把垃圾桶裡的檔一起回(連 appProperties 過濾也照回)。capy 自己從不 trash,但這是一行保險。
func (c *Client) List(ctx context.Context, q string) ([]File, error) {
	var out []File
	for token := ""; ; {
		v := url.Values{"spaces": {"appDataFolder"}, "fields": {"nextPageToken,files(" + fileFields + ")"}, "pageSize": {pageSize}}
		v.Set("q", "trashed = false")
		if q != "" {
			v.Set("q", "trashed = false and "+q)
		}
		if token != "" {
			v.Set("pageToken", token)
		}
		var page struct {
			NextPageToken string `json:"nextPageToken"`
			Files         []File `json:"files"`
		}
		if err := c.doJSON(ctx, http.MethodGet, c.base+"/files?"+v.Encode(), "", nil, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Files...)
		if token = page.NextPageToken; token == "" {
			return out, nil
		}
	}
}

// Find 找一份符合 name + props 的檔:沒有 → nil, nil。同名多份 → 取 modifiedTime 最新並警告,不清理:
// merge 對重複檔無害,而 list-before-create 也擋不住兩台裝置同時建檔——別再加。
func (c *Client) Find(ctx context.Context, name string, props map[string]string) (*File, error) {
	fs, err := c.List(ctx, Query(name, props))
	if err != nil || len(fs) == 0 {
		return nil, err
	}
	newest := Newest(fs)
	if len(fs) > 1 {
		fmt.Fprintf(Stderr, "警告:Drive appdata 有 %d 份 %s,採用最新的一份(%s)\n", len(fs), name, newest.ModifiedTime.Format(time.RFC3339))
	}
	return &newest, nil
}

// Newest 在同名多份裡挑 modifiedTime 最新者;同一毫秒用 ID 決勝:files.list 沒有 orderBy,順序未定義,
// 沒有決定性 tiebreak 兩台裝置會各認一份、永不收斂。fs 不可為空。
func Newest(fs []File) File {
	newest := fs[0]
	for _, f := range fs[1:] {
		if f.ModifiedTime.After(newest.ModifiedTime) || (f.ModifiedTime.Equal(newest.ModifiedTime) && f.ID > newest.ID) {
			newest = f
		}
	}
	return newest
}

// Create 在 appDataFolder 建檔。內容一律 application/json——capy 放進 Drive 的全是 JSON。
func (c *Client) Create(ctx context.Context, name string, props map[string]string, content []byte) (*File, error) {
	meta := map[string]any{"name": name, "parents": []string{"appDataFolder"}, "mimeType": "application/json"}
	if props != nil {
		meta["appProperties"] = props
	}
	return c.upload(ctx, http.MethodPost, c.uploadBase+"/files", meta, content)
}

// Update 覆寫內容;props 非 nil 時併入 appProperties(Drive 語意是 merge,不是取代)。
// content 不可為空:multipart 會把 Drive 上的檔(source of truth)清成 0 byte,SQLite 那份救不回來。
// 只改 metadata 的路(PATCH {base}/files/{id},不走 upload)等 T7 真的需要再加 UpdateMeta。
func (c *Client) Update(ctx context.Context, id string, props map[string]string, content []byte) (*File, error) {
	if len(content) == 0 {
		return nil, errors.New("drive.Update 需要內容:空內容會把 Drive 上的檔清空")
	}
	meta := map[string]any{}
	if props != nil {
		meta["appProperties"] = props
	}
	return c.upload(ctx, http.MethodPatch, c.uploadBase+"/files/"+url.PathEscape(id), meta, content)
}

func (c *Client) upload(ctx context.Context, method, u string, meta map[string]any, content []byte) (*File, error) {
	body, ct, err := multipartBody(meta, content)
	if err != nil {
		return nil, err
	}
	v := url.Values{"uploadType": {"multipart"}, "fields": {fileFields}}
	var f File
	if err := c.doJSON(ctx, method, u+"?"+v.Encode(), ct, body, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Download 取檔案內容(alt=media)。
func (c *Client) Download(ctx context.Context, id string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, c.base+"/files/"+url.PathEscape(id)+"?alt=media", "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// Delete 永久刪除(files.delete 不進垃圾桶)。appdata 檔可以被 PATCH 成 trashed=true 進垃圾桶(2026-09-07 真帳號實測),
// 但 capy 不走那條:List 一律排除垃圾桶裡的檔,進了就等於消失。目前只有測試與 probe 用。
func (c *Client) Delete(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, c.base+"/files/"+url.PathEscape(id), "", nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// multipartBody:Drive 的 uploadType=multipart 是 multipart/related(不是 form-data):第一段 metadata JSON、第二段內容。
func multipartBody(meta any, content []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	p, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}})
	if err != nil {
		return nil, "", err
	}
	if err := json.NewEncoder(p).Encode(meta); err != nil {
		return nil, "", err
	}
	if p, err = w.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json"}}); err != nil {
		return nil, "", err
	}
	if _, err := p.Write(content); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), mime.FormatMediaType("multipart/related", map[string]string{"boundary": w.Boundary()}), nil
}

func (c *Client) doJSON(ctx context.Context, method, u, contentType string, body []byte, out any) error {
	resp, err := c.do(ctx, method, u, contentType, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// do 送請求:2xx 回 resp(呼叫端 Close)。401 與 transport 層的 refresh 失敗 → provider.ErrAuthExpired
// (包住原錯誤,auth 的 explain 文字——「Publish app」那段——才傳得到 cron 的 log);429 與 403 rate limit →
// provider.Backoff 重試;403 storageQuotaExceeded / accessNotConfigured → 哨兵;其他 >=400 → *APIError。
// 403 要先讀 body 才知道 reason,所以 body 建成 []byte、每次重試重包 reader。
func (c *Client) do(ctx context.Context, method, u, contentType string, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, transportErr(err)
		}
		if resp.StatusCode < 400 {
			return resp, nil
		}
		apiErr := readAPIError(resp)
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, fmt.Errorf("%w:%v", provider.ErrAuthExpired, apiErr)
		case resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode == http.StatusForbidden && retryReasons[apiErr.Reason]):
			if err := provider.Backoff(ctx, resp, attempt); err != nil {
				var rl *provider.RateLimitError
				if errors.As(err, &rl) {
					return nil, &APIError{Status: resp.StatusCode, Reason: apiErr.Reason, Message: rl.Message}
				}
				return nil, err // ctx 取消原樣透傳
			}
			continue
		case resp.StatusCode == http.StatusForbidden && apiErr.Reason == "storageQuotaExceeded":
			return nil, fmt.Errorf("%w:%s", ErrStorageQuota, apiErr.Message)
		case resp.StatusCode == http.StatusForbidden && apiErr.Reason == "accessNotConfigured":
			return nil, fmt.Errorf("%w:%s", ErrAPINotEnabled, apiErr.Message)
		case resp.StatusCode == http.StatusForbidden && apiErr.Reason == "insufficientPermissions":
			// 登入時的 scope 檢查(auth.ErrGoogleScope)擋不到事後部分撤銷或 BYO 改 client scope:token 還在、refresh 照過,只有 Drive 回 403。
			return nil, fmt.Errorf("%w:Drive 授權缺 drive.appdata scope(%s),重新登入時記得勾「查看及管理應用程式自己的設定資料」", provider.ErrAuthExpired, apiErr.Message)
		}
		return nil, apiErr
	}
}

// transportErr:oauth2 refresh 失敗會以 *oauth2.RetrieveError 出現在 hc.Do 的錯誤鏈;經 auth 的 explain
// 歸因後鏈上只剩 ErrGoogleGrant / ErrGoogleClient(它不 %w 包原錯誤),兩種都要認,否則 explain 被丟掉。
func transportErr(err error) error {
	inner := err
	var uerr *url.Error
	if errors.As(err, &uerr) {
		inner = uerr.Err // 去掉 Get "https://…?fields=…" 那段噪音(cron log 會看);鏈上的 RetrieveError / ErrGoogleGrant 都還在
	}
	var rerr *oauth2.RetrieveError
	if errors.As(inner, &rerr) || errors.Is(inner, auth.ErrGoogleGrant) || errors.Is(inner, auth.ErrGoogleClient) {
		return fmt.Errorf("%w:%w", provider.ErrAuthExpired, inner) // 兩個 %w:哨兵與原鏈都要留
	}
	return err // 純網路錯誤保留 URL,dial 失敗時知道是打哪裡
}

// readAPIError 讀完並關閉 body。Google 的 reason 在 error.errors[0].reason。
func readAPIError(resp *http.Response) *APIError {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var eb struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &eb)
	e := &APIError{Status: resp.StatusCode, Message: eb.Error.Message}
	if len(eb.Error.Errors) > 0 {
		e.Reason = eb.Error.Errors[0].Reason
	}
	if e.Message == "" { // 非 JSON 的錯誤頁(proxy、HTML)留幾百字線索就夠,整頁灌進 cron log 會把有用的那行推走
		e.Message = truncate(strings.TrimSpace(string(raw)), 300)
	}
	return e
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
