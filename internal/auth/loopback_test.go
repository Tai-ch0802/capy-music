package auth

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewState(t *testing.T) {
	a, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 43 { // 32 bytes 的 raw base64url
		t.Errorf("state 長度 %d, want 43", len(a))
	}
	b, _ := NewState()
	if a == b {
		t.Error("兩次 state 不應相同")
	}
}

func TestCallbackDeliversQueryOnStateMatch(t *testing.T) {
	state, _ := NewState()
	lb, err := NewLoopback(0, state)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	lb.Start()

	resp, err := http.Get(lb.BaseURL() + "/callback?code=abc&state=" + state)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "授權完成") {
		t.Error("應回成功頁")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	vals, err := lb.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if vals.Get("code") != "abc" {
		t.Errorf("code = %q", vals.Get("code"))
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	state, _ := NewState()
	lb, err := NewLoopback(0, state)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	lb.Start()

	resp, err := http.Get(lb.BaseURL() + "/callback?code=abc&state=WRONG")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := lb.Wait(ctx); err == nil {
		t.Fatal("錯誤 state 不應 Deliver,Wait 應逾時")
	}
}

func TestFallbackToDynamicPortWhenPreferredBusy(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	busy := occupied.Addr().(*net.TCPAddr).Port

	lb, err := NewLoopback(busy, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	if lb.Port() == busy || lb.Port() == 0 {
		t.Fatalf("應 fallback 到其他實際 port,得到 %d", lb.Port())
	}
}
