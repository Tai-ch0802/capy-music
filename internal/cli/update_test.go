package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fakeSHA = "0123456789abcdef0123456789abcdef01234567"

// stubGitHub 假 GitHub API:回 main 的 sha 與 commit 時間;status 非 200 時只回狀態碼。
func stubGitHub(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/Tai-ch0802/capy-music/commits/main" {
			t.Errorf("問錯端點:%s", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	orig := updateGitHubAPI
	updateGitHubAPI = srv.URL
	t.Cleanup(func() { updateGitHubAPI = orig })
}

const headJSON = `{"sha":"` + fakeSHA + `","commit":{"committer":{"date":"2026-09-07T06:55:35Z"}}}`

// stubInstall 把 go install 換成「在 gobin 寫一個內容為 built:<ver> 的檔」,並記錄收到的 sha / ver。
func stubInstall(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	orig := updateGoInstall
	updateGoInstall = func(_ context.Context, _, sha, ver, gobin string, _ io.Writer) error {
		calls = append(calls, sha+" "+ver)
		name := "capy"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		return os.WriteFile(filepath.Join(gobin, name), []byte("built:"+ver), 0o755)
	}
	t.Cleanup(func() { updateGoInstall = orig })
	return &calls
}

func stubExecutable(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "capy")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := updateExecutable
	updateExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { updateExecutable = orig })
	return exe
}

func stubVersion(t *testing.T, v string) {
	t.Helper()
	orig := version
	version = v
	t.Cleanup(func() { version = orig })
}

func TestUpdateWithoutDevExplains(t *testing.T) {
	_, err := runCLI(t, "update")
	if err == nil || !strings.Contains(err.Error(), "--dev") {
		t.Fatalf("沒有正式版時應指向 --dev:%v", err)
	}
}

func TestUpdateDevReplacesExecutable(t *testing.T) {
	stubGitHub(t, http.StatusOK, headJSON)
	calls := stubInstall(t)
	exe := stubExecutable(t)
	stubVersion(t, "2026.09.04-77b72b4")
	out, err := runCLI(t, "update", "--dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0] != fakeSHA+" 2026.09.07-0123456" {
		t.Fatalf("go install 應收到完整 sha 與 YYYY.MM.DD-sha7 版本戳記:%v", *calls)
	}
	if b, _ := os.ReadFile(exe); string(b) != "built:2026.09.07-0123456" {
		t.Fatalf("binary 應被換成新建的:%q", b)
	}
	if !strings.Contains(out, "已更新") || !strings.Contains(out, "2026.09.07-0123456") {
		t.Fatalf("輸出:%s", out)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".capy-update-*")); len(left) != 0 {
		t.Fatalf("暫存目錄應清掉:%v", left)
	}
	if _, err := os.Stat(exe + ".old"); runtime.GOOS != "windows" && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(".old 應被刪掉")
	}
}

func TestUpdateDevAlreadyLatest(t *testing.T) {
	stubGitHub(t, http.StatusOK, headJSON)
	calls := stubInstall(t)
	exe := stubExecutable(t)
	stubVersion(t, "2026.09.07-0123456")
	out, err := runCLI(t, "update", "--dev")
	if err != nil || !strings.Contains(out, "已是最新") || len(*calls) != 0 {
		t.Fatalf("版本尾巴等於 main sha 前綴時不該重建:%q %v %v", out, err, *calls)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("不該動到 binary")
	}
}

func TestUpdateDevRateLimited(t *testing.T) {
	stubGitHub(t, http.StatusForbidden, `{"message":"API rate limit exceeded"}`)
	stubInstall(t)
	stubExecutable(t)
	stubVersion(t, "dev")
	if _, err := runCLI(t, "update", "--dev"); err == nil || !strings.Contains(err.Error(), "額度") {
		t.Fatalf("403 應說明額度:%v", err)
	}
}

func TestUpdateDevNeedsGo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	stubGitHub(t, http.StatusOK, headJSON)
	calls := stubInstall(t)
	stubVersion(t, "dev")
	if _, err := runCLI(t, "update", "--dev"); err == nil || !strings.Contains(err.Error(), "go.dev/dl") || len(*calls) != 0 {
		t.Fatalf("沒有 go 應指向下載頁、不呼叫 install:%v", err)
	}
}

func TestReplaceExecutableRollsBackWhenSourceMissing(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "capy")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst+".old", []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(dst, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("來源不存在應失敗")
	}
	if b, _ := os.ReadFile(dst); string(b) != "old" {
		t.Fatalf("失敗時舊 binary 要放回原位:%q", b)
	}
}
