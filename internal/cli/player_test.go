package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/provider"
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

// TestPlayByIDFlagSkipsSearch:--id 是給非 spotify base62 格式 ID 用的逃生口(例如 Apple 的
// 數字字串 catalog id),但在 spotify 上也該一樣跳過搜尋直接播放。
func TestPlayByIDFlagSkipsSearch(t *testing.T) {
	var playedURIs []string
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			t.Error("--id 不應觸發 search")
		case "/me/player/play":
			var body struct {
				URIs []string `json:"uris"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			playedURIs = body.URIs
			w.WriteHeader(http.StatusNoContent)
		}
	})
	out, err := runCLI(t, "play", "--id", "customid123")
	if err != nil {
		t.Fatal(err)
	}
	if len(playedURIs) != 1 || playedURIs[0] != "spotify:track:customid123" {
		t.Errorf("播放 URI:%v", playedURIs)
	}
	if !strings.Contains(out, "▶ customid123") {
		t.Errorf("輸出應以 id 當 label:%q", out)
	}
}

func TestPlayIDAndQueryConflict(t *testing.T) {
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("--id 與搜尋詞擇一不應呼叫 API:%s", r.URL.Path)
	})
	_, err := runCLI(t, "play", "--id", "abc", "query")
	if err == nil || !strings.Contains(err.Error(), "--id 與搜尋詞擇一") {
		t.Fatalf("同時給 --id 與搜尋詞應報錯:%v", err)
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

// 免費帳號(計畫 2026-09-30 Q4):播放命令被拒時說人話——是 Spotify 的規定、請改用 Spotify app,不叫人重新登入。
func TestPauseFreeAccountSaysPremium(t *testing.T) {
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"status":403,"reason":"PREMIUM_REQUIRED","message":"Player command failed: Premium required"}}`))
	})
	_, err := runCLI(t, "pause")
	if err == nil || !strings.Contains(err.Error(), "Spotify 只讓 Premium 帳號從其他 app 遙控播放") || !strings.Contains(err.Error(), "Spotify app") || strings.Contains(err.Error(), "auth login") {
		t.Fatalf("免費帳號要說是 Premium 的限制、指到 Spotify app:%v", err)
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

type ctlModeFake struct {
	*playFake
	devices      []provider.Device
	transferred  []string
	transferPlay []bool
	st           *provider.PlaybackState
	shuffles     []bool
	repeats      []provider.RepeatMode
}

func (f *ctlModeFake) Devices(context.Context) ([]provider.Device, error) { return f.devices, nil }
func (f *ctlModeFake) State(context.Context) (*provider.PlaybackState, error) {
	return f.st, nil
}
func (f *ctlModeFake) Transfer(_ context.Context, id string, play bool) error {
	f.transferred = append(f.transferred, id)
	f.transferPlay = append(f.transferPlay, play)
	return nil
}
func (f *ctlModeFake) SetShuffle(_ context.Context, on bool) error {
	f.shuffles = append(f.shuffles, on)
	if f.st != nil {
		f.st.Shuffle = on
	}
	return nil
}
func (f *ctlModeFake) SetRepeat(_ context.Context, mode provider.RepeatMode) error {
	f.repeats = append(f.repeats, mode)
	if f.st != nil {
		f.st.Repeat = mode
	}
	return nil
}

func TestDevicesSwitchAndPickAndSeamRestore(t *testing.T) {
	base := newPlayFake(t)
	f := &ctlModeFake{
		playFake: base,
		devices: []provider.Device{
			{ID: "dev-phone-1", Name: "My iPhone", Type: "Smartphone", Active: true, VolumePct: 45, VolumeKnown: true},
			{ID: "dev-spk-2", Name: "Living Room Speaker", Type: "Speaker", Active: false, VolumePct: 70, VolumeKnown: true},
		},
	}
	swapProviderWith(t, f)

	// 1. 無參數維持既有 5 欄 TSV 契約 100% 不變
	out, err := runCLI(t, "devices")
	if err != nil || out != "My iPhone\tSmartphone\tactive\t45\tdev-phone-1\nLiving Room Speaker\tSpeaker\t-\t70\tdev-spk-2\n" {
		t.Fatalf("無參數 devices 5 欄 TSV 契約不可變:err=%v out=%q", err, out)
	}

	// 2. --switch 不分大小寫比對名稱、或精確比對 ID,呼叫 Transfer(ctx, id, true)
	out, err = runCLI(t, "devices", "--switch", "living room speaker")
	if err != nil || !slices.Equal(f.transferred, []string{"dev-spk-2"}) || !slices.Equal(f.transferPlay, []bool{true}) || !strings.Contains(out, "Living Room Speaker") {
		t.Fatalf("--switch 名稱比對:err=%v transferred=%v play=%v out=%q", err, f.transferred, f.transferPlay, out)
	}
	f.transferred, f.transferPlay = nil, nil
	if _, err := runCLI(t, "devices", "--switch", "dev-phone-1"); err != nil || !slices.Equal(f.transferred, []string{"dev-phone-1"}) {
		t.Fatalf("--switch 精確 ID 比對:err=%v transferred=%v", err, f.transferred)
	}
	if _, err := runCLI(t, "devices", "--switch", "Kitchen"); err == nil || !strings.Contains(err.Error(), `"Kitchen"`) {
		t.Fatalf("--switch 找不到裝置應列出可用裝置:%v", err)
	}

	// 3. 平台不支援 DeviceTransferrer 時回傳清楚 i18n 錯誤(含 ErrNotSupported)
	swapProviderWith(t, base) // playFake 未實作 DeviceTransferrer
	if _, err := runCLI(t, "devices", "--switch", "My iPhone"); err == nil || !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("不支援 DeviceTransferrer 應回傳包裝 ErrNotSupported 的錯誤:%v", err)
	}
	swapProviderWith(t, f)

	// 4. --pick 非 TTY 擋下;TTY 下走 pickDevice 接縫
	if _, err := runCLI(t, "devices", "--pick"); err == nil || !strings.Contains(err.Error(), "--pick") {
		t.Fatalf("非 TTY --pick 應擋下:%v", err)
	}
	origTTY := isInteractive
	isInteractive = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { isInteractive = origTTY })
	origPickDev := pickDevice
	var pickedTitle string
	pickDevice = func(ds []provider.Device, title string) (provider.Device, error) {
		pickedTitle = title
		return ds[1], nil
	}
	t.Cleanup(func() { pickDevice = origPickDev })
	f.transferred = nil
	out, err = runCLI(t, "devices", "--pick")
	if err != nil || pickedTitle == "" || !slices.Equal(f.transferred, []string{"dev-spk-2"}) || !strings.Contains(out, "Living Room Speaker") {
		t.Fatalf("TTY --pick 應走 pickDevice 接縫並切換:err=%v title=%q transferred=%v out=%q", err, pickedTitle, f.transferred, out)
	}
	pickDevice = origPickDev

	// 5. installWebSeams 必須替換並完整還原 pickDevice 接縫
	beforePtr := reflect.ValueOf(pickDevice).Pointer()
	ws, err := newWebServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restore := installWebSeams(ws)
	duringPtr := reflect.ValueOf(pickDevice).Pointer()
	restore()
	afterPtr := reflect.ValueOf(pickDevice).Pointer()
	if duringPtr == beforePtr || afterPtr != beforePtr {
		t.Fatalf("installWebSeams 必須替換並還原 pickDevice:before=%x during=%x after=%x", beforePtr, duringPtr, afterPtr)
	}
}

func TestShuffleAndRepeatCommands(t *testing.T) {
	base := newPlayFake(t)
	f := &ctlModeFake{
		playFake: base,
		st:       &provider.PlaybackState{Playing: true, Shuffle: false, Repeat: provider.RepeatOff},
	}
	swapProviderWith(t, f)

	// 非法參數在建構 provider 前就被擋下
	origNew := newProvider
	newProvider = func(context.Context, string) (provider.Provider, error) {
		t.Fatal("非法參數不應呼叫 getProvider")
		return nil, nil
	}
	if _, err := runCLI(t, "shuffle", "maybe"); err == nil {
		t.Fatal("shuffle 非法參數應報錯")
	}
	if _, err := runCLI(t, "repeat", "forever"); err == nil {
		t.Fatal("repeat 非法參數應報錯")
	}
	newProvider = origNew

	// shuffle: on / off / toggle(預設無參數也是 toggle)
	if out, err := runCLI(t, "shuffle", "on"); err != nil || !slices.Equal(f.shuffles, []bool{true}) || !strings.Contains(out, "開") {
		t.Fatalf("shuffle on:err=%v shuffles=%v out=%q", err, f.shuffles, out)
	}
	f.shuffles = nil
	if out, err := runCLI(t, "shuffle"); err != nil || !slices.Equal(f.shuffles, []bool{false}) || !strings.Contains(out, "關") {
		t.Fatalf("shuffle 無參數應 toggle 為 off:err=%v shuffles=%v out=%q", err, f.shuffles, out)
	}

	// repeat: cycle(off → context → track → off)與明確指定
	f.repeats = nil
	for _, want := range []provider.RepeatMode{provider.RepeatContext, provider.RepeatTrack, provider.RepeatOff} {
		if _, err := runCLI(t, "repeat"); err != nil {
			t.Fatalf("repeat cycle:%v", err)
		}
		if f.repeats[len(f.repeats)-1] != want {
			t.Fatalf("repeat cycle 順序錯誤:got %v, want %s", f.repeats, want)
		}
	}
	f.repeats = nil
	if out, err := runCLI(t, "repeat", "track"); err != nil || !slices.Equal(f.repeats, []provider.RepeatMode{provider.RepeatTrack}) || !strings.Contains(out, "單曲") {
		t.Fatalf("repeat track:err=%v repeats=%v out=%q", err, f.repeats, out)
	}

	// 不支援 PlaybackModeController 的平台
	swapProviderWith(t, base)
	if _, err := runCLI(t, "shuffle", "on"); err == nil || !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("不支援 PlaybackModeController 應回 ErrNotSupported:%v", err)
	}
	swapProviderWith(t, f)

	// 英文語系零 CJK 驗證
	withLanguage(t, "en")
	for _, args := range [][]string{
		{"devices", "--help"},
		{"shuffle", "--help"},
		{"repeat", "--help"},
		{"shuffle", "on"},
		{"repeat", "context"},
	} {
		out, err := runCLI(t, args...)
		if err != nil || hasCJK(out) {
			t.Errorf("%v 英文模式不該有 CJK:err=%v out=%q", args, err, out)
		}
	}
}
