//go:build darwin

package apple

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// TestOsaErr:exec.Command(...).Output() 失敗時只回傳 exit code,真正原因(如 TCC 拒絕自動化的
// -1743)在 stderr——osaErr 把 stderr 抽出來當 error 訊息,讓 AutoWebTokens 的彙總失敗訊息分辨得出
// 「未授權自動化」與「JavaScript from Apple Events 未開」(review item 2)。
// runOSA 是套件變數,stub 版測試(stubOSA)繞過真正的 exec.Command,蓋不到這段——故獨立測 osaErr。
func TestOsaErr(t *testing.T) {
	t.Run("ExitError 帶 stderr 時抽出 stderr", func(t *testing.T) {
		// ProcessState 刻意留 nil:osaErr 只讀 Stderr 欄位,不呼叫 ee.Error()(那個才需要 ProcessState)。
		ee := &exec.ExitError{Stderr: []byte("execution error: Not authorized to send Apple events to Safari. (-1743)\n")}
		got := osaErr(ee)
		want := "execution error: Not authorized to send Apple events to Safari. (-1743)"
		if got == nil || got.Error() != want {
			t.Errorf("osaErr = %v, want %q", got, want)
		}
	})
	t.Run("nil error 原樣回傳", func(t *testing.T) {
		if got := osaErr(nil); got != nil {
			t.Errorf("osaErr(nil) = %v, want nil", got)
		}
	})
	t.Run("非 ExitError 原樣回傳", func(t *testing.T) {
		orig := errors.New("boom")
		if got := osaErr(orig); got != orig {
			t.Errorf("osaErr = %v, want %v(原樣回傳)", got, orig)
		}
	})
	t.Run("ExitError 但 Stderr 為空原樣回傳", func(t *testing.T) {
		ee := &exec.ExitError{}
		got := osaErr(ee)
		if got != error(ee) {
			t.Errorf("osaErr = %v, want %v(原樣回傳,沒有 stderr 可抽)", got, ee)
		}
	})
}

type osaResult struct {
	out string
	err error
}

// stubOSA:依 browserScripts 的固定順序(Safari 在前、Chrome 在後)依序回傳結果;
// 回傳呼叫次數指標,供「成功後不該再試下一個瀏覽器」之類的斷言。
func stubOSA(t *testing.T, results ...osaResult) *int {
	t.Helper()
	orig := runOSA
	calls := 0
	runOSA = func(string) (string, error) {
		if calls >= len(results) {
			t.Fatalf("runOSA 呼叫次數超出預期(第 %d 次)", calls+1)
		}
		r := results[calls]
		calls++
		return r.out, r.err
	}
	t.Cleanup(func() { runOSA = orig })
	return &calls
}

// (a) Safari 回有效 JSON → 成功且不再試 Chrome。
func TestAutoWebTokensSafariSuccess(t *testing.T) {
	calls := stubOSA(t, osaResult{out: `{"d":"DEV1","u":"MUT1"}`})
	wt, err := AutoWebTokens()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if wt.Developer != "DEV1" || wt.User != "MUT1" {
		t.Errorf("wt = %+v", wt)
	}
	if *calls != 1 {
		t.Errorf("runOSA 呼叫次數 = %d, want 1(成功不該再試 Chrome)", *calls)
	}
}

// (b) Safari 回 error、Chrome 回 JSON → 成功且 error 訊息未出現(err 為 nil)。
func TestAutoWebTokensFallsBackToChromeOnSafariError(t *testing.T) {
	stubOSA(t,
		osaResult{err: errors.New("Safari 得到一個 AppleEvent 逾時(-1712)")},
		osaResult{out: `{"d":"DEV2","u":"MUT2"}`},
	)
	wt, err := AutoWebTokens()
	if err != nil {
		t.Fatalf("err = %v,want nil(Chrome 應成功接手)", err)
	}
	if wt.Developer != "DEV2" || wt.User != "MUT2" {
		t.Errorf("wt = %+v", wt)
	}
}

// (c) 兩個瀏覽器都回空字串(沒開或沒有 music.apple.com 分頁)→ error 說明前提。
func TestAutoWebTokensBothEmptyErrors(t *testing.T) {
	stubOSA(t, osaResult{}, osaResult{})
	wt, err := AutoWebTokens()
	if err == nil {
		t.Fatal("預期 error")
	}
	if !strings.Contains(err.Error(), "Apple 事件") || !strings.Contains(err.Error(), "music.apple.com") {
		t.Errorf("err = %v,應提及 Apple 事件與 music.apple.com", err)
	}
	if wt != (WebTokens{}) {
		t.Errorf("失敗不該回傳半殘資料:%+v", wt)
	}
}

// (d) JSON 缺 u(兩個瀏覽器都缺)→ error。
func TestAutoWebTokensMissingUserTokenErrors(t *testing.T) {
	stubOSA(t,
		osaResult{out: `{"d":"DEV3"}`},
		osaResult{out: `{"d":"DEV4"}`},
	)
	_, err := AutoWebTokens()
	if err == nil {
		t.Fatal("預期 error(頁面沒有回傳兩個 token)")
	}
}
