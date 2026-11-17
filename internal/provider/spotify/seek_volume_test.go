package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// Seek / SetVolume 都是 PUT + query 參數(沒有 body);位置與音量分別叫 position_ms 與 volume_percent。
func TestSeekAndVolumeRequestShape(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	if err := c.Seek(context.Background(), 83000); err != nil {
		t.Fatal(err)
	}
	if err := c.SetVolume(context.Background(), 40); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"PUT /me/player/seek?position_ms=83000",
		"PUT /me/player/volume?volume_percent=40",
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("請求形狀:\n%v\n要\n%v", got, want)
	}
}

// 手機與部分喇叭回 403 VOLUME_CONTROL_DISALLOW:訊息要講「這個裝置不給調」,不能落到授權那一族。
func TestVolumeControlDisallowedMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"status":403,"reason":"VOLUME_CONTROL_DISALLOW","message":"Player command failed"}}`))
	}))
	defer srv.Close()
	err := NewClient(srv.Client(), srv.URL).SetVolume(context.Background(), 40)
	if !errors.Is(err, provider.ErrVolumeNotAllowed) { // 斷言 sentinel,不是中文訊息:訊息可以改寫、可以翻譯
		t.Fatalf("403 VOLUME_CONTROL_DISALLOW 要是 ErrVolumeNotAllowed:%v", err)
	}
	if errors.Is(err, provider.ErrAuthExpired) {
		t.Fatal("不能被當成授權過期")
	}
	if !strings.Contains(err.Error(), "403") { // 原始的 apiError 留在鏈上,debug 看得到 status
		t.Errorf("原始錯誤要留在鏈上:%v", err)
	}
	// 別的 403 不冒充成音量問題
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"status":403,"reason":"PREMIUM_REQUIRED","message":"x"}}`))
	}))
	defer srv2.Close()
	if err := NewClient(srv2.Client(), srv2.URL).SetVolume(context.Background(), 40); err == nil || errors.Is(err, provider.ErrVolumeNotAllowed) {
		t.Fatalf("其他 403 不該套音量的說法:%v", err)
	}
}

// 免費帳號(計畫 2026-09-30 Q4):每個 player 端點的 403 PREMIUM_REQUIRED 都映射成 ErrPremiumRequired,原始錯誤留在鏈上;
// 不是授權過期(重新登入沒用),也不是音量問題。沒有 reason 的 403 照舊原樣往上。
func TestPremiumRequiredOnEveryPlayerCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"status":403,"reason":"PREMIUM_REQUIRED","message":"Player command failed: Premium required"}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL)
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"play":     func() error { return c.Play(ctx, []string{"spotify:track:4uLU6hMCjMI75M1A2tKUQC"}, "") },
		"context":  func() error { return c.PlayContext(ctx, "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M", "") },
		"pause":    func() error { return c.Pause(ctx) },
		"next":     func() error { return c.Next(ctx) },
		"prev":     func() error { return c.Prev(ctx) },
		"seek":     func() error { return c.Seek(ctx, 1000) },
		"volume":   func() error { return c.SetVolume(ctx, 40) },
		"transfer": func() error { return c.Transfer(ctx, "d1", true) },
		"shuffle":  func() error { return c.SetShuffle(ctx, true) },
		"repeat":   func() error { return c.SetRepeat(ctx, provider.RepeatOff) },
	} {
		err := call()
		var ae *apiError
		if !errors.Is(err, provider.ErrPremiumRequired) || errors.Is(err, provider.ErrAuthExpired) || errors.Is(err, provider.ErrVolumeNotAllowed) || !errors.As(err, &ae) {
			t.Errorf("%s:403 PREMIUM_REQUIRED 要是 ErrPremiumRequired、原始錯誤留在鏈上:%v", name, err)
		}
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"status":403,"message":"x"}}`))
	}))
	defer plain.Close()
	if err := NewClient(plain.Client(), plain.URL).Pause(ctx); err == nil || errors.Is(err, provider.ErrPremiumRequired) {
		t.Fatalf("沒有 reason 的 403 不該冒充成 Premium 問題:%v", err)
	}
}

// seek 也吃 NO_ACTIVE_DEVICE 的映射(mapPlayerErr 的既有分支不能因為加了 403 而漏掉)。
func TestSeekNoActiveDevice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"status":404,"reason":"NO_ACTIVE_DEVICE","message":"x"}}`))
	}))
	defer srv.Close()
	if err := NewClient(srv.Client(), srv.URL).Seek(context.Background(), 1000); !errors.Is(err, provider.ErrNoActiveDevice) {
		t.Fatalf("404 NO_ACTIVE_DEVICE 要映射:%v", err)
	}
}

func TestTransferShuffleRepeatRequestShape(t *testing.T) {
	var got []string
	var transferBody map[string]any
	hc := memSpotifyClient(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if r.URL.Path == "/me/player" {
			_ = json.NewDecoder(r.Body).Decode(&transferBody)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	p := New(hc, "https://spotify.test")
	ctx := context.Background()
	if err := p.Transfer(ctx, "dev-123", true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetShuffle(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetShuffle(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := p.SetRepeat(ctx, provider.RepeatContext); err != nil {
		t.Fatal(err)
	}
	if err := p.SetRepeat(ctx, provider.RepeatTrack); err != nil {
		t.Fatal(err)
	}
	if err := p.SetRepeat(ctx, provider.RepeatOff); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"PUT /me/player?",
		"PUT /me/player/shuffle?state=true",
		"PUT /me/player/shuffle?state=false",
		"PUT /me/player/repeat?state=context",
		"PUT /me/player/repeat?state=track",
		"PUT /me/player/repeat?state=off",
	}
	if len(got) != len(want) {
		t.Fatalf("請求數 %d != %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	ids, _ := transferBody["device_ids"].([]any)
	if len(ids) != 1 || ids[0] != "dev-123" || transferBody["play"] != true {
		t.Errorf("Transfer body 不符預期:%+v", transferBody)
	}
	if err := p.Transfer(ctx, "", false); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("空 deviceID 應回 ErrNotFound:%v", err)
	}
	if err := p.SetRepeat(ctx, provider.RepeatMode("bad")); !errors.Is(err, provider.ErrNotSupported) {
		t.Errorf("無效 RepeatMode 應回 ErrNotSupported:%v", err)
	}
}

func TestRestrictionViolatedMapsToRestrictedNotAuthExpired(t *testing.T) {
	hc := memSpotifyClient(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"status":403,"reason":"RESTRICTION_VIOLATED","message":"Player command failed: Restriction violated"}}`))
	})
	err := New(hc, "https://spotify.test").SetRepeat(context.Background(), provider.RepeatTrack)
	var ae *apiError
	if !errors.Is(err, provider.ErrRestricted) || errors.Is(err, provider.ErrAuthExpired) || errors.Is(err, provider.ErrPremiumRequired) || !errors.As(err, &ae) {
		t.Fatalf("403 RESTRICTION_VIOLATED 應映射為 ErrRestricted(非授權過期)並保留 *apiError:%v", err)
	}
}
