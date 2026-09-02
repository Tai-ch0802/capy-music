package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestPlayByQuerySearchesThenPlays(t *testing.T) {
	var playedURIs []string
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			fmt.Fprintf(w, `{"tracks":{"items":[%s],"total":1}}`, fmt.Sprintf(cliTrackFx, "hit1"))
		case "/me/player/play":
			var body struct {
				URIs []string `json:"uris"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			playedURIs = body.URIs
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
	})
	out, err := runCLI(t, "play", "派對動物")
	if err != nil {
		t.Fatal(err)
	}
	if len(playedURIs) != 1 || playedURIs[0] != "spotify:track:hit1" {
		t.Errorf("播放 URI:%v", playedURIs)
	}
	if !strings.Contains(out, "▶ 派對動物") {
		t.Errorf("輸出應為單一空格的 ▶ 前綴且含曲名:%q", out)
	}
}

func TestPlayByURIDoesNotSearch(t *testing.T) {
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			t.Error("URI 直播不應觸發 search")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if _, err := runCLI(t, "play", "spotify:track:0123456789abcdefghijkl"); err != nil {
		t.Fatal(err)
	}
}

func TestPlayRejectsNonTrackURI(t *testing.T) {
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			t.Errorf("非 track URI 不應觸發 search")
		}
	})
	_, err := runCLI(t, "play", "spotify:album:0123456789abcdefghijkl")
	if err == nil || !strings.Contains(err.Error(), "只支援 track") {
		t.Fatalf("非 track 的 spotify: URI 應被擋下並給出替代路徑,得到 %v", err)
	}
}

func TestPlayDeviceFlagNoDevices(t *testing.T) {
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/player/devices":
			w.Write([]byte(`{"devices":[]}`))
		case "/search":
			fmt.Fprintf(w, `{"tracks":{"items":[%s],"total":1}}`, fmt.Sprintf(cliTrackFx, "x1"))
		default:
			t.Errorf("非預期路徑:%s", r.URL.Path)
		}
	})
	_, err := runCLI(t, "play", "x", "--device", "客廳喇叭")
	// friendlyErr 已 provider 泛化(T1):訊息不再寫死 "Spotify",改指向 --provider 對應值。
	if err == nil || !strings.Contains(err.Error(), "capy devices --provider spotify") {
		t.Fatalf("零裝置應給可行動訊息,得到 %v", err)
	}
}

func TestPlayDeviceFlagResolvesName(t *testing.T) {
	var gotDevice string
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/player/devices":
			w.Write([]byte(`{"devices":[{"id":"d9","name":"客廳喇叭","type":"Speaker","is_active":false,"volume_percent":30}]}`))
		case "/search":
			fmt.Fprintf(w, `{"tracks":{"items":[%s],"total":1}}`, fmt.Sprintf(cliTrackFx, "x1"))
		case "/me/player/play":
			gotDevice = r.URL.Query().Get("device_id")
			w.WriteHeader(http.StatusNoContent)
		}
	})
	if _, err := runCLI(t, "play", "x", "--device", "客廳喇叭"); err != nil {
		t.Fatal(err)
	}
	if gotDevice != "d9" {
		t.Errorf("device 名稱應解析為 id:%q", gotDevice)
	}
}

func TestPauseNoActiveDeviceFriendly(t *testing.T) {
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"status":404,"reason":"NO_ACTIVE_DEVICE","message":"x"}}`))
	})
	_, err := runCLI(t, "pause")
	if err == nil || !strings.Contains(err.Error(), "capy devices") {
		t.Fatalf("應給可行動訊息:%v", err)
	}
}

func TestNowNothingPlaying(t *testing.T) {
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	out, err := runCLI(t, "now")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "沒有播放內容") {
		t.Errorf("輸出:%q", out)
	}
}

func TestNowShowsStateWithProgress(t *testing.T) {
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"is_playing":true,"progress_ms":61000,"item":%s,
"device":{"id":"d1","name":"MacBook","type":"Computer","is_active":true,"volume_percent":80}}`,
			fmt.Sprintf(cliTrackFx, "t1"))
	})
	out, err := runCLI(t, "now")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"派對動物", "五月天", "1:01", "3:47", "MacBook"} {
		if !strings.Contains(out, want) {
			t.Errorf("now 輸出缺 %q:%q", want, out)
		}
	}
}

func TestDevicesTSV(t *testing.T) {
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"devices":[{"id":"d1","name":"手機","type":"Smartphone","is_active":true,"volume_percent":50}]}`))
	})
	out, err := runCLI(t, "devices")
	if err != nil {
		t.Fatal(err)
	}
	if out != "手機\tSmartphone\tactive\t50\td1\n" {
		t.Errorf("devices TSV:%q", out)
	}
}
