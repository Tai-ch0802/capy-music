// Command spotify-probe 是 P0-4 的量測工具:
// 量 Spotify Development Mode 的實際 rate limit(spec §9 P0-4)。
//
// ponytail: throwaway probe —— 量完把數字記進 docs/ARCHITECTURE.md 即棄,
// 不隨 release 分發,無單元測試(互動式;CI 以 vet/build 保證可編譯)。
//
// 用法:
//
//	SPOTIFY_CLIENT_ID=xxx go run ./cmd/spotify-probe [-interval 100ms] [-n 200]
//
// 前置:Spotify dashboard 已註冊 redirect URI http://127.0.0.1:8888/callback
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/browser"
)

func main() {
	interval := flag.Duration("interval", 100*time.Millisecond, "請求間隔")
	n := flag.Int("n", 200, "最大請求數")
	flag.Parse()
	if err := run(*interval, *n); err != nil {
		fmt.Fprintln(os.Stderr, "錯誤:", err)
		os.Exit(1)
	}
}

func run(interval time.Duration, n int) error {
	clientID := os.Getenv("SPOTIFY_CLIENT_ID")
	if clientID == "" {
		return fmt.Errorf("需要 SPOTIFY_CLIENT_ID(你自己 app 的 Client ID)")
	}
	state, err := auth.NewState()
	if err != nil {
		return err
	}
	lb, err := auth.NewLoopback(auth.DefaultSpotifyPort, state)
	if err != nil {
		return err
	}
	defer lb.Close()
	if lb.Port() != auth.DefaultSpotifyPort {
		return fmt.Errorf("port 8888 被佔用(dashboard 註冊的是固定 8888),請先釋放")
	}

	conf := &oauth2.Config{
		ClientID:    clientID,
		RedirectURL: lb.BaseURL() + "/callback",
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.spotify.com/authorize",
			TokenURL: "https://accounts.spotify.com/api/token",
		},
	}
	verifier := oauth2.GenerateVerifier()
	lb.Start()
	authURL := conf.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	fmt.Println("開啟瀏覽器授權中…若沒有自動開啟,手動前往:")
	fmt.Println(" ", authURL)
	if err := browser.Open(authURL); err != nil {
		fmt.Fprintln(os.Stderr, "無法自動開瀏覽器:", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	vals, err := lb.Wait(ctx)
	if err != nil {
		return fmt.Errorf("等待授權 callback:%w", err)
	}
	if e := vals.Get("error"); e != "" {
		return fmt.Errorf("授權被拒:%s", e)
	}
	tok, err := conf.Exchange(ctx, vals.Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		return fmt.Errorf("token 交換:%w", err)
	}
	client := conf.Client(context.Background(), tok)

	fmt.Printf("開始探測:每 %v 一發,最多 %d 發(GET /v1/search, limit=10)\n", interval, n)
	okCount := 0
	start := time.Now()
	for i := 1; i <= n; i++ {
		resp, err := client.Get("https://api.spotify.com/v1/search?type=track&limit=10&q=mayday")
		if err != nil {
			return err
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			elapsed := time.Since(start)
			fmt.Printf("\n第 %d 發收到 429。成功 %d 發 / %.1fs(≈ %.1f req/s)\n",
				i, okCount, elapsed.Seconds(), float64(okCount)/elapsed.Seconds())
			fmt.Printf("Retry-After:%s 秒\n", resp.Header.Get("Retry-After"))
			fmt.Println("→ 把數字記到 docs/ARCHITECTURE.md §9 P0-4(決定同步併發度與退避策略)")
			return nil
		case http.StatusOK:
			okCount++
			fmt.Print(".")
		default:
			fmt.Printf("\n第 %d 發非預期狀態 %d\n", i, resp.StatusCode)
		}
		time.Sleep(interval)
	}
	fmt.Printf("\n%d 發全過(每 %v 一發)未觸發 429 —— 用更短 -interval 再跑一輪\n", n, interval)
	return nil
}
