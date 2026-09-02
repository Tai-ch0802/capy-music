package cli

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func plFixtureHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/playlists":
			w.Write([]byte(`{"items":[
{"id":"p1","name":"通勤","owner":{"display_name":"tai"},"items":{"total":2}},
{"id":"p2","name":"健身","owner":{"display_name":"tai"},"items":{"total":5}},
{"id":"p3","name":"健身","owner":{"display_name":"別人"},"items":{"total":1}}],"total":3}`))
		case "/playlists/p1/items":
			fmt.Fprintf(w, `{"items":[{"item":%s}],"total":1}`, fmt.Sprintf(cliTrackFx, "t1"))
		case "/playlists/pfollowed0123456789abc/items":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"status":403,"message":"Forbidden"}}`))
		default:
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
	}
}

func TestPlListTSV(t *testing.T) {
	swapProvider(t, plFixtureHandler(t))
	out, err := runCLI(t, "pl", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "p1\t通勤\t2\ttai\n") {
		t.Errorf("pl list TSV:%q", out)
	}
}

func TestPlShowByName(t *testing.T) {
	swapProvider(t, plFixtureHandler(t))
	out, err := runCLI(t, "pl", "show", "通勤")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "派對動物") {
		t.Errorf("pl show:%q", out)
	}
}

func TestPlShowAmbiguousName(t *testing.T) {
	swapProvider(t, plFixtureHandler(t))
	if _, err := runCLI(t, "pl", "show", "健身"); err == nil || !strings.Contains(err.Error(), "ID") {
		t.Fatalf("同名清單應要求用 ID:%v", err)
	}
}

func TestPlShowNotFound(t *testing.T) {
	swapProvider(t, plFixtureHandler(t))
	if _, err := runCLI(t, "pl", "show", "不存在的"); err == nil || !strings.Contains(err.Error(), "capy pl list") {
		t.Fatalf("找不到應提示 pl list:%v", err)
	}
}

func TestPlShowFollowedPlaylistRestricted(t *testing.T) {
	swapProvider(t, plFixtureHandler(t))
	// 22 碼引數 → 當 playlist ID 直接查
	_, err := runCLI(t, "pl", "show", "pfollowed0123456789abc")
	if err == nil || !strings.Contains(err.Error(), "2026-02") {
		t.Fatalf("追蹤他人清單應給政策說明:%v", err)
	}
}
