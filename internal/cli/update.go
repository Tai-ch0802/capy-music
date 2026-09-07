package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	updateRepo = "Tai-ch0802/capy-music"
	updatePkg  = "github.com/Tai-ch0802/capy-music/cmd/capy"
)

// 測試替換點。
var (
	updateGitHubAPI  = "https://api.github.com"
	updateExecutable = os.Executable
	updateGoInstall  = goInstall
)

// ponytail: 正式版的更新路徑(GitHub Releases 下載 + 校驗)等 T10 有 release 再做;現在只有 --dev。
func newUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "更新 capy 自己(目前只有 --dev:main 最新節點)",
		Long: `更新 capy 自己。

--dev 會問 GitHub 取 main 分支最新 commit,用 go install 從原始碼重建,再覆蓋目前執行中的這顆 binary
(需要 Go toolchain;第一次會下載相依,約 20 秒)。正式版尚未發行,沒有 --dev 目前只會告訴你這件事。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dev, _ := cmd.Flags().GetBool("dev"); !dev {
				return errors.New("正式版尚未發行(release 流程是 P3 T10);目前請用 capy update --dev 更新到 main 最新節點")
			}
			return runDevUpdate(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().Bool("dev", false, "更新到 main 分支最新節點(從原始碼建置,需要 Go toolchain)")
	return cmd
}

type mainHead struct {
	SHA  string
	Date time.Time
}

// devVersion:與手動安裝相同的版本戳記格式 YYYY.MM.DD-<sha7>(日期取 commit 的 UTC 日期,各機器一致)。
func (h mainHead) devVersion() string { return h.Date.UTC().Format("2006.01.02") + "-" + h.SHA[:7] }

// resolveMainHead 問 GitHub API(未認證,每小時 60 次)main 的最新 commit。不走 go list -m @main:
// 那要經 module proxy,分支名的解析會被快取;直接問 GitHub 永遠是最新。
func resolveMainHead(ctx context.Context) (mainHead, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateGitHubAPI+"/repos/"+updateRepo+"/commits/main", nil)
	if err != nil {
		return mainHead{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return mainHead{}, fmt.Errorf("問 GitHub 取 main 最新 commit:%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return mainHead{}, errors.New("GitHub API 額度用完(未認證每小時 60 次),稍後再試")
	}
	if resp.StatusCode != http.StatusOK {
		return mainHead{}, fmt.Errorf("GitHub API 回 %d", resp.StatusCode)
	}
	var body struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || len(body.SHA) < 7 {
		return mainHead{}, fmt.Errorf("GitHub API 回應解析失敗:%v", err)
	}
	return mainHead{SHA: body.SHA, Date: body.Commit.Committer.Date}, nil
}

func runDevUpdate(ctx context.Context, out, stderr io.Writer) error {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return errors.New("找不到 go:--dev 是從原始碼建置,需要 Go toolchain(https://go.dev/dl/)")
	}
	exe, err := updateExecutable()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	head, err := resolveMainHead(ctx)
	if err != nil {
		return err
	}
	ver := head.devVersion()
	if i := strings.LastIndex(version, "-"); i >= 0 && strings.HasPrefix(head.SHA, version[i+1:]) {
		fmt.Fprintf(out, "已是最新:%s(main %s)\n", version, head.SHA[:7])
		return nil
	}
	fmt.Fprintf(out, "main 最新 %s(%s),目前 %s;建置中(第一次會下載相依,約 20 秒)…\n", head.SHA[:7], head.Date.Local().Format("2006-01-02 15:04"), version)
	// 暫存目錄開在 binary 旁邊:同一個檔案系統,最後的 rename 才是原子的。
	tmp, err := os.MkdirTemp(filepath.Dir(exe), ".capy-update-")
	if err != nil {
		return fmt.Errorf("無法在 %s 建暫存目錄(要有寫入權限才能覆蓋自己):%w", filepath.Dir(exe), err)
	}
	defer os.RemoveAll(tmp)
	if err := updateGoInstall(ctx, goBin, head.SHA, ver, tmp, stderr); err != nil {
		return fmt.Errorf("go install 失敗:%w", err)
	}
	built := filepath.Join(tmp, "capy")
	if runtime.GOOS == "windows" {
		built += ".exe"
	}
	if err := replaceExecutable(exe, built); err != nil {
		return err
	}
	fmt.Fprintf(out, "已更新 %s → %s\n", exe, ver)
	return nil
}

// goInstall 以 go install <pkg>@<sha> 建進 gobin;版本戳記與手動安裝同一套 ldflags。
// cwd 設在 gobin:go install pkg@version 本就不看當前 go.mod,但別讓壞掉的 go.work 之類攪局。
func goInstall(ctx context.Context, goBin, sha, ver, gobin string, stderr io.Writer) error {
	c := exec.CommandContext(ctx, goBin, "install", "-trimpath",
		"-ldflags", "-s -w -X github.com/Tai-ch0802/capy-music/internal/cli.version="+ver, updatePkg+"@"+sha)
	c.Dir = gobin
	c.Env = append(os.Environ(), "GOBIN="+gobin)
	c.Stdout, c.Stderr = stderr, stderr
	return c.Run()
}

// replaceExecutable 用 src 換掉 dst。先把舊檔挪成 .old 再把新檔 rename 進來:Unix 本來就能直接覆蓋執行中的檔,
// 但 Windows 不能覆寫執行中的 exe、只能改名,兩邊走同一條路。失敗就把舊檔放回去;.old 在 Windows 上
// 執行中刪不掉,留給下次更新開頭清。
func replaceExecutable(dst, src string) error {
	old := dst + ".old"
	_ = os.Remove(old)
	if err := os.Rename(dst, old); err != nil {
		return fmt.Errorf("移開舊 binary:%w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Rename(old, dst)
		return fmt.Errorf("放入新 binary:%w", err)
	}
	_ = os.Remove(old)
	return nil
}
