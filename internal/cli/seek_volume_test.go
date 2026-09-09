package cli

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func TestParseSeekPos(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		bad  bool
	}{
		{in: "1:23", want: 83000},
		{in: "0:05", want: 5000},
		{in: "1:5", want: 65000}, // 秒欄位允許一位
		{in: "10:00", want: 600000},
		{in: "83", want: 83000}, // 純秒數
		{in: "0", want: 0},
		{in: " 1:23 ", want: 83000},
		{in: "1:05:30", want: 3930000}, // h:mm:ss:podcast 與 live 全場
		{in: "0:00:07", want: 7000},
		{in: "2:00:00", want: 7200000},
		{in: "65:30", want: 3930000}, // 第一段沒有上界
		{in: "1:75", bad: true},      // 秒 > 59 比較可能是打錯
		{in: "1:75:00", bad: true},   // 分 > 59 同理
		{in: "1:2:3:4", bad: true},   // 超過三段
		{in: "-5", bad: true},
		{in: "1:-3", bad: true},
		{in: "abc", bad: true},
		{in: "", bad: true},
	} {
		got, err := parseSeekPos(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("%q 應該被拒,得到 %d", tc.in, got)
			} else if !strings.Contains(err.Error(), tc.in) && tc.in != "" {
				t.Errorf("%q 的錯誤訊息要帶原輸入:%v", tc.in, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseSeekPos(%q) = %d, %v;要 %d", tc.in, got, err, tc.want)
		}
	}
}

func TestParseVolPct(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		bad  bool
	}{
		{in: "0", want: 0},
		{in: "40", want: 40},
		{in: "100", want: 100},
		{in: " 40 ", want: 40},
		{in: "101", bad: true}, // 不夾範圍:靜靜改成 100 會讓人以為壞了
		{in: "-1", bad: true},
		{in: "40.5", bad: true},
		{in: "", bad: true},
	} {
		got, err := parseVolPct(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("%q 應該被拒,得到 %d", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseVolPct(%q) = %d, %v;要 %d", tc.in, got, err, tc.want)
		}
	}
}

func TestSeekAndVolCommands(t *testing.T) {
	var got []string
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+"?"+r.URL.RawQuery)
		w.WriteHeader(http.StatusNoContent)
	})
	out, err := runCLI(t, "seek", "1:23")
	if err != nil || !strings.Contains(out, "1:23") {
		t.Fatalf("seek:%q %v", out, err)
	}
	out, err = runCLI(t, "vol", "40")
	if err != nil || !strings.Contains(out, "40") {
		t.Fatalf("vol:%q %v", out, err)
	}
	want := []string{"/me/player/seek?position_ms=83000", "/me/player/volume?volume_percent=40"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("送出的請求:%v,要 %v", got, want)
	}
}

// 參數格式錯誤在取 provider 之前就擋下來:不該先要求有憑證、也不該打任何 API。
func TestSeekAndVolRejectBadArgBeforeAnyAPICall(t *testing.T) {
	var called bool
	swapProvider(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	if _, err := runCLI(t, "seek", "咖啡"); err == nil || !strings.Contains(err.Error(), "mm:ss") {
		t.Fatalf("seek 壞參數要指路:%v", err)
	}
	if _, err := runCLI(t, "seek", "1:05:30:99"); err == nil || !strings.Contains(err.Error(), "mm:ss") {
		t.Fatalf("四段要被拒:%v", err)
	}
	if _, err := runCLI(t, "vol", "300"); err == nil || !strings.Contains(err.Error(), "0-100") {
		t.Fatalf("vol 超範圍要指路:%v", err)
	}
	if called {
		t.Fatal("參數還沒過就打了 API")
	}
	// 連 provider 都拿不到(未登入)時,壞參數要回參數的錯,不是「未登入」——
	// 這條守的是「先解析再取 provider」的順序,上面那個 called 旗標守不住(getProvider 本來就不連網)。
	orig := newProvider
	newProvider = func(context.Context, string) (provider.Provider, error) {
		return nil, errors.New("尚未登入")
	}
	t.Cleanup(func() { newProvider = orig })
	if _, err := runCLI(t, "seek", "咖啡"); err == nil || !strings.Contains(err.Error(), "mm:ss") {
		t.Fatalf("壞參數要先於 provider 被擋下:%v", err)
	}
	if _, err := runCLI(t, "vol", "300"); err == nil || !strings.Contains(err.Error(), "0-100") {
		t.Fatalf("壞參數要先於 provider 被擋下:%v", err)
	}
	// 兩個都要參數(NoArgs 的 pause 家族不同)
	if _, err := runCLI(t, "seek"); err == nil {
		t.Fatal("seek 少參數要錯")
	}
	if _, err := runCLI(t, "vol"); err == nil {
		t.Fatal("vol 少參數要錯")
	}
}
