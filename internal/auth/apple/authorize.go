package apple

import (
	"context"
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"net/url"

	"github.com/Tai-ch0802/capy-music/internal/auth"
)

//go:embed web/authorize.html
var authorizePage string

var authorizeTmpl = template.Must(template.New("authorize").Parse(authorizePage))

// AuthorizeMUT 執行 spec §4.3(b) 的 MusicKit 橋接:
// 起 loopback(動態 port,P0-3 驗的就是這個)→ 開瀏覽器 → 使用者按按鈕
// → 頁面 POST 回 Music User Token。openBrowser 注入以便測試。
func AuthorizeMUT(ctx context.Context, devToken string, openBrowser func(string) error) (string, error) {
	state, err := auth.NewState()
	if err != nil {
		return "", err
	}
	lb, err := auth.NewLoopback(0, state)
	if err != nil {
		return "", err
	}
	defer lb.Close()

	lb.Handle("GET /apple/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = authorizeTmpl.Execute(w, map[string]string{
			"DeveloperToken": devToken,
			"State":          state,
		})
	})
	lb.Handle("POST /apple/callback", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		lb.Deliver(url.Values{"music_user_token": {r.PostForm.Get("music_user_token")}})
	})

	lb.Start()
	if err := openBrowser(lb.BaseURL() + "/apple/authorize"); err != nil {
		return "", err
	}
	vals, err := lb.Wait(ctx)
	if err != nil {
		return "", err
	}
	mut := vals.Get("music_user_token")
	if mut == "" {
		return "", errors.New("未取得 Music User Token")
	}
	return mut, nil
}
