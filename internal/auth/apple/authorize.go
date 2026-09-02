package apple

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/Tai-ch0802/capy-music/internal/auth"
)

//go:embed web/authorize.html
var authorizePage string

var authorizeTmpl = template.Must(template.New("authorize").Parse(authorizePage))

// AuthStderr:AuthorizeMUT 印手動授權 URL / 開瀏覽器失敗訊息的目的地。測試替換點。
var AuthStderr io.Writer = os.Stderr

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
		_ = http.NewResponseController(w).Flush() // Deliver 會解鎖 Wait→Close;先把回應送出去
		lb.Deliver(url.Values{"music_user_token": {r.PostForm.Get("music_user_token")}})
	})

	lb.Start()
	pageURL := lb.BaseURL() + "/apple/authorize"
	fmt.Fprintf(AuthStderr, "若瀏覽器未自動開啟,請手動前往:\n  %s\n", pageURL)
	if err := openBrowser(pageURL); err != nil {
		fmt.Fprintf(AuthStderr, "無法自動開瀏覽器:%v(可手動開啟上方網址)\n", err)
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
