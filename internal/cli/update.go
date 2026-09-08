package cli

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	updateRepo = "Tai-ch0802/capy-music"
	updatePkg  = "github.com/Tai-ch0802/capy-music/cmd/capy"
	// updateChecksums 與 updateAssetName 是和 .goreleaser.yaml 的契約:
	// checksum.name_template 與 archives[0].name_template 改了,這裡要跟著改(有測試對照 yaml 原文)。
	updateChecksums = "checksums.txt"
	// updateMaxDownload:release 檔案的大小上限(binary 約 20 MB;擋住壞掉或被換過的巨檔把磁碟塞滿)。
	updateMaxDownload = 256 << 20
)

// 測試替換點。
var (
	updateGitHubAPI  = "https://api.github.com"
	updateExecutable = os.Executable
	updateGoInstall  = goInstall
	updateVerify     = verifyBinary
)

var errGitHubNotFound = errors.New("GitHub 回 404")

// devStampRe:capy update --dev / 手動安裝的版本戳記 YYYY.MM.DD-<sha7>;正式版是 tag 去掉 v 的 semver。
var devStampRe = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}-[0-9a-f]{7}$`)

func newUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "更新 capy 自己(正式版;--dev 則是 main 最新節點)",
		Long: `更新 capy 自己。

不帶參數:問 GitHub Releases 最新正式版,下載這個平台的檔案、用 checksums.txt 做 SHA-256 校驗、
跑一次新 binary 的 --version 確認沒壞,才覆蓋目前執行中的這顆。校驗只保證下載完整(release 沒有簽章)。
從 dev 版(go install 或 --dev 建的)執行會換成正式版。

--dev:問 GitHub 取 main 分支最新 commit,用 go install 從原始碼重建,再覆蓋目前執行中的這顆 binary
(需要 Go toolchain;第一次會下載相依,約 20 秒)。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dev, _ := cmd.Flags().GetBool("dev"); dev {
				return runDevUpdate(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			return runReleaseUpdate(cmd.Context(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().Bool("dev", false, "更新到 main 分支最新節點(從原始碼建置,需要 Go toolchain)")
	return cmd
}

// updateAssetName:GoReleaser 產的檔名 capy_<版本>_<os>_<arch>.tar.gz(Windows 是 .zip);版本不含 v。
func updateAssetName(ver string) string {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return "capy_" + ver + "_" + runtime.GOOS + "_" + runtime.GOARCH + ext
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "capy.exe"
	}
	return "capy"
}

// githubJSON 打 GitHub REST API(未認證,每小時 60 次)並解析 JSON;404 回 errGitHubNotFound。
func githubJSON(ctx context.Context, apiPath string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateGitHubAPI+apiPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("問 GitHub:%w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return errors.New("GitHub API 額度用完(未認證每小時 60 次),稍後再試")
	case http.StatusNotFound:
		return errGitHubNotFound
	default:
		return fmt.Errorf("GitHub API 回 %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("GitHub API 回應解析失敗:%w", err)
	}
	return nil
}

type ghRelease struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// runReleaseUpdate:GitHub Releases 最新版 → 下載本平台檔 + checksums.txt → SHA-256 → 解出 capy →
// 跑 --version 自檢 → 才換掉自己。任何一步失敗,原 binary 原封不動。
func runReleaseUpdate(ctx context.Context, out io.Writer) error {
	exe, err := updateExecutable()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	var rel ghRelease
	if err := githubJSON(ctx, "/repos/"+updateRepo+"/releases/latest", &rel); err != nil {
		if errors.Is(err, errGitHubNotFound) {
			return errors.New("尚無正式版 release(維護者還沒發第一版);目前可用 capy update --dev 更新到 main 最新節點")
		}
		return err
	}
	latest := strings.TrimPrefix(rel.Tag, "v")
	// ponytail: 只比「相同與否」不比大小;維護者撤掉一版時 latest 會倒退,那正是要跟的。
	if version == latest {
		fmt.Fprintf(out, "已是最新:v%s\n", latest)
		return nil
	}
	name := updateAssetName(latest)
	urls := map[string]string{}
	for _, a := range rel.Assets {
		urls[a.Name] = a.URL
	}
	if urls[name] == "" || urls[updateChecksums] == "" {
		return fmt.Errorf("release v%s 缺 %s(這個平台 %s/%s 的檔)或 %s;看看 https://github.com/%s/releases", latest, name, runtime.GOOS, runtime.GOARCH, updateChecksums, updateRepo)
	}
	from := version
	if version == "dev" || devStampRe.MatchString(version) {
		from = "dev 版 " + version + ",換到正式版"
	}
	fmt.Fprintf(out, "最新正式版 v%s(目前 %s);下載 %s…\n", latest, from, name)
	// 暫存目錄開在 binary 旁邊:同一個檔案系統,最後的 rename 才是原子的。
	tmp, err := os.MkdirTemp(filepath.Dir(exe), ".capy-update-")
	if err != nil {
		return fmt.Errorf("無法在 %s 建暫存目錄(要有寫入權限才能覆蓋自己):%w", filepath.Dir(exe), err)
	}
	defer os.RemoveAll(tmp)
	sums, err := fetchBytes(ctx, urls[updateChecksums], 1<<20)
	if err != nil {
		return fmt.Errorf("下載 %s:%w", updateChecksums, err)
	}
	want := checksumFor(sums, name)
	if want == "" {
		return fmt.Errorf("%s 沒有 %s 這一行,不敢裝", updateChecksums, name)
	}
	archive := filepath.Join(tmp, name)
	if err := downloadVerified(ctx, urls[name], archive, want); err != nil {
		return fmt.Errorf("下載 %s:%w", name, err)
	}
	built, err := extractCapy(archive, tmp, binaryName())
	if err != nil {
		return fmt.Errorf("解開 %s:%w", name, err)
	}
	if err := updateVerify(built, latest); err != nil {
		return fmt.Errorf("新 binary 自檢失敗(舊的沒動):%w", err)
	}
	if err := replaceExecutable(exe, built); err != nil {
		return err
	}
	fmt.Fprintf(out, "已更新 %s → v%s\n", exe, latest)
	return nil
}

func fetchBytes(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("超過 %d bytes", limit)
	}
	return b, nil
}

// checksumFor:GoReleaser 的 checksums.txt 每行「<sha256 hex>  <檔名>」;找不到回空字串。
func checksumFor(sums []byte, name string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name && len(f[0]) == sha256.Size*2 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// downloadVerified 邊下載邊算 SHA-256,不符就刪檔回錯(絕不留下半個可疑檔給下一步)。
func downloadVerified(ctx context.Context, url, dst, wantHex string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, updateMaxDownload))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantHex {
		_ = os.Remove(dst)
		return fmt.Errorf("SHA-256 校驗失敗(下載不完整,或 release 檔被換過):%s ≠ %s 裡的 %s", got[:12], updateChecksums, wantHex[:12])
	}
	return nil
}

// extractCapy 只從 archive(.zip 或 .tar.gz)拿出叫 name 的那個檔寫到 dir,其餘(README 等)不理。
func extractCapy(archive, dir, name string) (string, error) {
	dst := filepath.Join(dir, name)
	write := func(r io.Reader) error {
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, io.LimitReader(r, updateMaxDownload))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return "", err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if path.Base(f.Name) != name || f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			err = write(rc)
			rc.Close()
			return dst, err
		}
		return "", fmt.Errorf("壓縮檔裡沒有 %s", name)
	}
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("壓縮檔裡沒有 %s", name)
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag == tar.TypeReg && path.Base(hdr.Name) == name {
			return dst, write(tr)
		}
	}
}

// verifyBinary 跑一次新 binary 的 --version:抓到下錯架構、下載被截斷這類「檔在但不能跑」的狀況,
// 免得把能用的舊 binary 換成一顆廢的。
func verifyBinary(bin, want string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return fmt.Errorf("執行 %s --version:%w", bin, err)
	}
	if !strings.Contains(string(out), want) {
		return fmt.Errorf("%s --version 回 %q,不含預期的 %s", bin, strings.TrimSpace(string(out)), want)
	}
	return nil
}

type mainHead struct {
	SHA  string
	Date time.Time
}

// devVersion:與手動安裝相同的版本戳記格式 YYYY.MM.DD-<sha7>(日期取 commit 的 UTC 日期,各機器一致)。
func (h mainHead) devVersion() string { return h.Date.UTC().Format("2006.01.02") + "-" + h.SHA[:7] }

// resolveMainHead 問 GitHub API main 的最新 commit。不走 go list -m @main:
// 那要經 module proxy,分支名的解析會被快取;直接問 GitHub 永遠是最新。
func resolveMainHead(ctx context.Context) (mainHead, error) {
	var body struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := githubJSON(ctx, "/repos/"+updateRepo+"/commits/main", &body); err != nil {
		return mainHead{}, err
	}
	if len(body.SHA) < 7 {
		return mainHead{}, errors.New("GitHub API 回應解析失敗:沒有 sha")
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
	if err := replaceExecutable(exe, filepath.Join(tmp, binaryName())); err != nil {
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
