// Package auth 提供三個 provider 共用的 loopback 授權基礎設施(spec §4.1)。
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

// DefaultSpotifyPort 是 Spotify dashboard 註冊的固定 redirect port(spec §4.2)。
const DefaultSpotifyPort = 8888

const successHTML = `<!doctype html><html lang="zh-Hant"><head><meta charset="utf-8">
<title>capy — 授權完成</title></head>
<body style="font-family:system-ui;text-align:center;padding-top:4rem">
<h1>✅ 授權完成</h1><p>可以關閉這個分頁,回到終端機。</p>
<script>window.close()</script></body></html>`

// NewState 產生 32 bytes CSPRNG 的 base64url state(CSRF 防護)。
func NewState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Loopback 是一次性授權回呼伺服器:綁 127.0.0.1,等一個 callback 就收工。
type Loopback struct {
	ln    net.Listener
	srv   *http.Server
	mux   *http.ServeMux
	state string
	done  chan url.Values
}

// NewLoopback 優先綁 127.0.0.1:preferredPort,被佔用時 fallback 動態 port。
// preferredPort 為 0 時直接用動態 port。
func NewLoopback(preferredPort int, state string) (*Loopback, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferredPort))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
	}
	mux := http.NewServeMux()
	l := &Loopback{
		ln:    ln,
		srv:   &http.Server{Handler: mux},
		mux:   mux,
		state: state,
		done:  make(chan url.Values, 1),
	}
	mux.HandleFunc("GET /callback", l.handleCallback)
	return l, nil
}

func (l *Loopback) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("state") != l.state {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, successHTML)
	l.Deliver(q)
}

// Handle 掛額外路由(Apple 授權頁會用)。須在 Start 前呼叫。
func (l *Loopback) Handle(pattern string, h http.HandlerFunc) {
	l.mux.HandleFunc(pattern, h)
}

// Deliver 供自訂 handler 交付結果;只有第一筆會被 Wait 收到。
func (l *Loopback) Deliver(v url.Values) {
	select {
	case l.done <- v:
	default:
	}
}

func (l *Loopback) Start() { go l.srv.Serve(l.ln) } //nolint:errcheck // Close 時必回錯,無需處理

// Wait 等待 callback 或 ctx 結束(呼叫端負責 timeout,spec 建議 180s)。
func (l *Loopback) Wait(ctx context.Context) (url.Values, error) {
	select {
	case v := <-l.done:
		return v, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *Loopback) Port() int {
	return l.ln.Addr().(*net.TCPAddr).Port
}

func (l *Loopback) BaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", l.Port())
}

// Close 關閉伺服器並釋放 listener。
// srv.Close 只關它經由 Serve 追蹤的 listener;Start 未被呼叫時必須直接關 l.ln。
func (l *Loopback) Close() error {
	err := l.srv.Close()
	if cerr := l.ln.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) && err == nil {
		err = cerr
	}
	return err
}
