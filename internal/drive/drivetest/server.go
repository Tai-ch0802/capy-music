// Package drivetest 是記憶體版假 Drive(files.list / multipart create、update / get alt=media / delete),
// 給 drive 與 cli 的測試共用。只實作 capy 會用到的 q 語法與參數,其餘一律 400,讓 client 一改就炸。
package drivetest

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type file struct {
	id, name string
	version  int64
	modified time.Time
	props    map[string]string
	content  []byte
}

type failure struct {
	status     int
	reason     string
	retryAfter string
}

// Server 是一個 httptest.Server 加記憶體檔案表。PageSize 非 0 時強制分頁(忽略請求的 pageSize)。
type Server struct {
	*httptest.Server
	mu        sync.Mutex
	files     []*file
	seq       int
	now       time.Time
	fails     []failure
	PageSize  int
	ListCalls int
}

func New(t testing.TB) *Server {
	s := &Server{now: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

// Advance 撥快假時鐘(modifiedTime 由它決定)。
func (s *Server) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = s.now.Add(d)
}

// Fail 讓接下來 n 個請求回 status 與 Google 格式的錯誤 body;retryAfter 非空時加 Retry-After 標頭。
func (s *Server) Fail(n, status int, reason, retryAfter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for range n {
		s.fails = append(s.fails, failure{status, reason, retryAfter})
	}
}

// Len 回目前檔案數。
func (s *Server) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.files)
}

// messages:仿 Google 真實錯誤訊息;accessNotConfigured 的訊息含啟用連結,client 要原樣轉出去。
var messages = map[string]string{
	"accessNotConfigured":  "Google Drive API has not been used in project 123 before or it is disabled. Enable it by visiting https://console.developers.google.com/apis/api/drive.googleapis.com/overview?project=123 then retry.",
	"storageQuotaExceeded": "The user's Drive storage quota has been exceeded.",
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.fails) > 0 {
		f := s.fails[0]
		s.fails = s.fails[1:]
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		msg, ok := messages[f.reason]
		if !ok {
			msg = "injected " + f.reason
		}
		writeErr(w, f.status, f.reason, msg)
		return
	}
	p := r.URL.Path
	id, isFile := strings.CutPrefix(p, "/drive/v3/files/")
	uid, isUpload := strings.CutPrefix(p, "/upload/drive/v3/files/")
	switch {
	case r.Method == http.MethodGet && p == "/drive/v3/files":
		s.list(w, r)
	case r.Method == http.MethodPost && p == "/upload/drive/v3/files":
		s.create(w, r)
	case r.Method == http.MethodPatch && isUpload:
		s.update(w, r, uid)
	case r.Method == http.MethodGet && isFile:
		s.download(w, r, id)
	case r.Method == http.MethodDelete && isFile:
		s.del(w, id)
	default:
		writeErr(w, http.StatusNotFound, "notFound", "fake drive: no route "+r.Method+" "+p)
	}
}

func (s *Server) find(id string) *file {
	for _, f := range s.files {
		if f.id == id {
			return f
		}
	}
	return nil
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	s.ListCalls++
	q := r.URL.Query()
	if q.Get("spaces") != "appDataFolder" {
		writeErr(w, http.StatusBadRequest, "invalid", "fake drive: list 需要 spaces=appDataFolder")
		return
	}
	if !strings.Contains(q.Get("fields"), "files(") {
		writeErr(w, http.StatusBadRequest, "invalid", "fake drive: list 沒帶 fields=(真 Drive 會只回四個欄位)")
		return
	}
	if !strings.HasPrefix(q.Get("q"), "trashed = false") {
		writeErr(w, http.StatusBadRequest, "invalid", "fake drive: list 沒帶 trashed = false(v3 預設會回垃圾桶裡的檔)")
		return
	}
	match, err := parseQuery(q.Get("q"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	var all []*file
	for _, f := range s.files {
		if match(f) {
			all = append(all, f)
		}
	}
	size := s.PageSize
	if size == 0 {
		if size, _ = strconv.Atoi(q.Get("pageSize")); size <= 0 {
			size = 100
		}
	}
	start, _ := strconv.Atoi(q.Get("pageToken"))
	start = min(start, len(all))
	end := min(start+size, len(all))
	out := map[string]any{"files": toJSON(all[start:end])}
	if end < len(all) {
		out["nextPageToken"] = strconv.Itoa(end)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	meta, content, err := parseUpload(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	name, _ := meta["name"].(string)
	parents, _ := meta["parents"].([]any)
	if name == "" || !slices.Contains(parents, any("appDataFolder")) {
		writeErr(w, http.StatusBadRequest, "invalid", fmt.Sprintf("fake drive: create 需要 name 與 parents=[appDataFolder],得 %v", meta))
		return
	}
	s.seq++
	f := &file{id: fmt.Sprintf("id%03d", s.seq), name: name, version: 1, modified: s.now, props: propsOf(meta), content: content}
	s.files = append(s.files, f)
	writeJSON(w, http.StatusOK, toJSON([]*file{f})[0])
}

func (s *Server) update(w http.ResponseWriter, r *http.Request, id string) {
	f := s.find(id)
	if f == nil {
		writeErr(w, http.StatusNotFound, "notFound", "File not found: "+id)
		return
	}
	meta, content, err := parseUpload(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	for k, v := range propsOf(meta) { // Drive 的 appProperties 是 merge
		if f.props == nil {
			f.props = map[string]string{}
		}
		f.props[k] = v
	}
	f.content = content
	f.version++
	f.modified = s.now
	writeJSON(w, http.StatusOK, toJSON([]*file{f})[0])
}

func (s *Server) download(w http.ResponseWriter, r *http.Request, id string) {
	if r.URL.Query().Get("alt") != "media" {
		writeErr(w, http.StatusBadRequest, "invalid", "fake drive: 只支援 alt=media")
		return
	}
	f := s.find(id)
	if f == nil {
		writeErr(w, http.StatusNotFound, "notFound", "File not found: "+id)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(f.content)
}

func (s *Server) del(w http.ResponseWriter, id string) {
	i := slices.IndexFunc(s.files, func(f *file) bool { return f.id == id })
	if i < 0 {
		writeErr(w, http.StatusNotFound, "notFound", "File not found: "+id)
		return
	}
	s.files = slices.Delete(s.files, i, i+1)
	w.WriteHeader(http.StatusNoContent)
}

// parseUpload 解 uploadType=multipart:multipart/related,第一段 metadata JSON、第二段內容。
func parseUpload(r *http.Request) (map[string]any, []byte, error) {
	q := r.URL.Query()
	if q.Get("uploadType") != "multipart" || !strings.Contains(q.Get("fields"), "version") {
		return nil, nil, fmt.Errorf("fake drive: upload 需要 uploadType=multipart 與含 version 的 fields=,得 %q", r.URL.RawQuery)
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/related" {
		return nil, nil, fmt.Errorf("fake drive: 要 multipart/related,得 %q", r.Header.Get("Content-Type"))
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	p1, err := mr.NextPart()
	if err != nil {
		return nil, nil, fmt.Errorf("fake drive: 缺 metadata 段:%w", err)
	}
	if ct := p1.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return nil, nil, fmt.Errorf("fake drive: metadata 段要 application/json,得 %q", ct)
	}
	var meta map[string]any
	if err := json.NewDecoder(p1).Decode(&meta); err != nil {
		return nil, nil, fmt.Errorf("fake drive: metadata 不是 JSON:%w", err)
	}
	p2, err := mr.NextPart()
	if err != nil {
		return nil, nil, fmt.Errorf("fake drive: 缺內容段:%w", err)
	}
	content, err := io.ReadAll(p2)
	if err != nil {
		return nil, nil, err
	}
	return meta, content, nil
}

func propsOf(meta map[string]any) map[string]string {
	raw, _ := meta["appProperties"].(map[string]any)
	if raw == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range raw {
		out[k], _ = v.(string)
	}
	return out
}

var (
	reTrashed = regexp.MustCompile(`^trashed = false`)
	reName    = regexp.MustCompile(`^name = '((?:[^'\\]|\\.)*)'`)
	reProp    = regexp.MustCompile(`^appProperties has \{ key='((?:[^'\\]|\\.)*)' and value='((?:[^'\\]|\\.)*)' \}`)
)

func unescape(s string) string { return strings.NewReplacer(`\'`, `'`, `\\`, `\`).Replace(s) }

// parseQuery 只認 trashed = false、name = '…' 與 appProperties has { key='…' and value='…' },用 and 串;其他語法回錯。
func parseQuery(q string) (func(*file) bool, error) {
	var preds []func(*file) bool
	for rest := q; rest != ""; {
		if m := reTrashed.FindString(rest); m != "" { // 假 Drive 沒有垃圾桶,子句只需能解析
			rest = rest[len(m):]
		} else if m := reName.FindStringSubmatch(rest); m != nil {
			name := unescape(m[1])
			preds = append(preds, func(f *file) bool { return f.name == name })
			rest = rest[len(m[0]):]
		} else if m := reProp.FindStringSubmatch(rest); m != nil {
			k, v := unescape(m[1]), unescape(m[2])
			preds = append(preds, func(f *file) bool { return f.props[k] == v })
			rest = rest[len(m[0]):]
		} else {
			return nil, fmt.Errorf("fake drive: 不支援的 q 語法:%q", rest)
		}
		if rest == "" {
			break
		}
		var ok bool
		if rest, ok = strings.CutPrefix(rest, " and "); !ok || rest == "" {
			return nil, fmt.Errorf("fake drive: q 子句之間要 ' and ':%q", q)
		}
	}
	return func(f *file) bool {
		for _, p := range preds {
			if !p(f) {
				return false
			}
		}
		return true
	}, nil
}

type fileJSON struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Version       string            `json:"version"` // 真 Drive 是字串(int64 format)
	MD5           string            `json:"md5Checksum"`
	ModifiedTime  time.Time         `json:"modifiedTime"`
	AppProperties map[string]string `json:"appProperties,omitempty"`
}

func toJSON(fs []*file) []fileJSON {
	out := make([]fileJSON, 0, len(fs))
	for _, f := range fs {
		sum := md5.Sum(f.content)
		out = append(out, fileJSON{f.id, f.name, strconv.FormatInt(f.version, 10), hex.EncodeToString(sum[:]), f.modified, f.props})
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 仿 Google 的錯誤 body:reason 在 error.errors[0].reason。
func writeErr(w http.ResponseWriter, status int, reason, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": status, "message": msg,
		"errors": []map[string]string{{"domain": "usageLimits", "reason": reason, "message": msg}},
	}})
}
