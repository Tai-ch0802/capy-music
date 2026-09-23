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
	interval := flag.Duration("interval", 100*time.Millisecond, "interval between requests")
	n := flag.Int("n", 200, "maximum number of requests")
	flag.Parse()
	if err := run(*interval, *n); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(interval time.Duration, n int) error {
	clientID := os.Getenv("SPOTIFY_CLIENT_ID")
	if clientID == "" {
		return fmt.Errorf("SPOTIFY_CLIENT_ID is required (the Client ID of your own app)")
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
		return fmt.Errorf("port 8888 is in use (the dashboard registers the fixed port 8888); free it first")
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
	fmt.Println("Opening the browser to authorize… if it does not open, go to:")
	fmt.Println(" ", authURL)
	if err := browser.Open(authURL); err != nil {
		fmt.Fprintln(os.Stderr, "could not open the browser:", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	vals, err := lb.Wait(ctx)
	if err != nil {
		return fmt.Errorf("waiting for the authorization callback: %w", err)
	}
	if e := vals.Get("error"); e != "" {
		return fmt.Errorf("authorization denied: %s", e)
	}
	tok, err := conf.Exchange(ctx, vals.Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}
	client := conf.Client(context.Background(), tok)

	fmt.Printf("Probing: one request every %v, at most %d (GET /v1/search, limit=10)\n", interval, n)
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
			fmt.Printf("\nRequest %d got 429. %d succeeded in %.1fs (≈ %.1f req/s)\n",
				i, okCount, elapsed.Seconds(), float64(okCount)/elapsed.Seconds())
			fmt.Printf("Retry-After: %s s\n", resp.Header.Get("Retry-After"))
			fmt.Println("→ record the numbers in docs/ARCHITECTURE.md §9 P0-4 (they decide sync concurrency and backoff)")
			return nil
		case http.StatusOK:
			okCount++
			fmt.Print(".")
		default:
			fmt.Printf("\nRequest %d: unexpected status %d\n", i, resp.StatusCode)
		}
		time.Sleep(interval)
	}
	fmt.Printf("\nAll %d requests passed (one every %v) without a 429 — run again with a shorter -interval\n", n, interval)
	return nil
}
