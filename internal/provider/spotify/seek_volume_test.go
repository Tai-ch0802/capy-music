package spotify

import (
	"context"
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
	if err == nil || !strings.Contains(err.Error(), "不允許遠端調整音量") {
		t.Fatalf("403 VOLUME_CONTROL_DISALLOW 要有自己的說法:%v", err)
	}
	if errors.Is(err, provider.ErrAuthExpired) {
		t.Fatal("不能被當成授權過期")
	}
	// 其他 403 沒有特別說法,原樣往上(不冒充成音量問題)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"status":403,"reason":"PREMIUM_REQUIRED","message":"x"}}`))
	}))
	defer srv2.Close()
	if err := NewClient(srv2.Client(), srv2.URL).SetVolume(context.Background(), 40); err == nil || strings.Contains(err.Error(), "不允許遠端調整音量") {
		t.Fatalf("其他 403 不該套音量的說法:%v", err)
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
