package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

// TestReplaceExecutableKeepsOldMode:umask 077 下解出來的新檔是 0700,換上去要沿用舊 binary 的 0755。
func TestReplaceExecutableKeepsOldMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 沒有 Unix 權限位元")
	}
	dir := t.TempDir()
	dst, src := filepath.Join(dir, "capy"), filepath.Join(dir, "new")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(dst, src); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o755 {
		t.Fatalf("新 binary 要沿用舊檔權限 0755,得到 %o", fi.Mode().Perm())
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

// ---- 正式版路徑(GitHub Releases) ----

// fakeArchive 做一個 GoReleaser 形狀的壓縮檔:README.md + capy(或 capy.exe),內容 release:<ver>;
// zipped 為 true 做 .zip、否則 .tar.gz(Windows 的 release 是 zip,其餘 tar.gz)。
func fakeArchive(t *testing.T, ver, name string, zipped bool) []byte {
	t.Helper()
	entries := []struct {
		n string
		b []byte
	}{{"README.md", []byte("readme")}, {name, []byte("release:" + ver)}}
	var buf bytes.Buffer
	if zipped {
		zw := zip.NewWriter(&buf)
		for _, e := range entries {
			w, err := zw.Create(e.n)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(e.b)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.n, Mode: 0o755, Size: int64(len(e.b)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(e.b)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// stubRelease 假 GitHub:releases/latest 回 tag 與 assets(每個 asset 的下載網址指回這台假伺服器);
// status 非 200 時只回狀態碼。回傳下載計數(assets 被抓幾次)。
func stubRelease(t *testing.T, status int, tag string, assets map[string][]byte) *int {
	t.Helper()
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name, ok := strings.CutPrefix(r.URL.Path, "/dl/"); ok {
			b, ok := assets[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			downloads++
			_, _ = w.Write(b)
			return
		}
		if r.URL.Path != "/repos/Tai-ch0802/capy-music/releases/latest" {
			t.Errorf("問錯端點:%s", r.URL.Path)
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			return
		}
		rel := ghRelease{Tag: tag}
		for name := range assets {
			rel.Assets = append(rel.Assets, struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			}{name, "http://" + r.Host + "/dl/" + name})
		}
		_ = json.NewEncoder(w).Encode(rel)
	}))
	t.Cleanup(srv.Close)
	orig := updateGitHubAPI
	updateGitHubAPI = srv.URL
	t.Cleanup(func() { updateGitHubAPI = orig })
	return &downloads
}

// releaseAssets:本平台的壓縮檔 + 對得上的 checksums.txt(另附一個別平台的檔,證明會挑對)。
func releaseAssets(t *testing.T, ver string) map[string][]byte {
	t.Helper()
	name := updateAssetName(ver)
	archive := fakeArchive(t, ver, binaryName(), strings.HasSuffix(name, ".zip"))
	other := "capy_" + ver + "_plan9_mips.tar.gz"
	sums := sha256Hex(archive) + "  " + name + "\n" + sha256Hex([]byte("x")) + "  " + other + "\n"
	return map[string][]byte{name: archive, other: []byte("x"), updateChecksums: []byte(sums)}
}

func stubVerify(t *testing.T, fail error) *[]string {
	t.Helper()
	var calls []string
	orig := updateVerify
	updateVerify = func(bin, want string) error {
		b, _ := os.ReadFile(bin)
		calls = append(calls, want+" "+string(b))
		return fail
	}
	t.Cleanup(func() { updateVerify = orig })
	return &calls
}

func TestUpdateReleaseReplacesExecutable(t *testing.T) {
	downloads := stubRelease(t, http.StatusOK, "v1.2.3", releaseAssets(t, "1.2.3"))
	exe := stubExecutable(t)
	stubVersion(t, "1.0.0")
	verified := stubVerify(t, nil)
	out, err := runCLI(t, "update")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "release:1.2.3" {
		t.Fatalf("binary 應被換成 release 裡的 capy:%q", b)
	}
	if *downloads != 2 {
		t.Fatalf("只該下載 checksums.txt 與本平台那一個檔:%d", *downloads)
	}
	if len(*verified) != 1 || (*verified)[0] != "1.2.3 release:1.2.3" {
		t.Fatalf("換掉之前要對解出來的 binary 跑自檢:%v", *verified)
	}
	if !strings.Contains(out, "已更新") || !strings.Contains(out, "v1.2.3") || strings.Contains(out, "dev 版") {
		t.Fatalf("輸出:%s", out)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".capy-update-*")); len(left) != 0 {
		t.Fatalf("暫存目錄應清掉:%v", left)
	}
	if _, err := os.Stat(exe + ".old"); runtime.GOOS != "windows" && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(".old 應被刪掉")
	}
}

func TestUpdateReleaseFromDevBuildSaysSo(t *testing.T) {
	for _, v := range []string{"dev", "2026.09.08-9698382"} {
		stubRelease(t, http.StatusOK, "v1.2.3", releaseAssets(t, "1.2.3"))
		exe := stubExecutable(t)
		stubVersion(t, v)
		stubVerify(t, nil)
		out, err := runCLI(t, "update")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "dev 版 "+v+",換到正式版") {
			t.Fatalf("%s:要講明是從 dev 版換到正式版:%s", v, out)
		}
		if b, _ := os.ReadFile(exe); string(b) != "release:1.2.3" {
			t.Fatalf("%s:binary 應被換掉:%q", v, b)
		}
	}
}

func TestUpdateReleaseAlreadyLatest(t *testing.T) {
	downloads := stubRelease(t, http.StatusOK, "v1.2.3", releaseAssets(t, "1.2.3"))
	exe := stubExecutable(t)
	stubVersion(t, "1.2.3")
	verified := stubVerify(t, nil)
	out, err := runCLI(t, "update")
	if err != nil || !strings.Contains(out, "已是最新:v1.2.3") {
		t.Fatalf("版本與 tag(去掉 v)相同時不該動:%q %v", out, err)
	}
	if *downloads != 0 || len(*verified) != 0 {
		t.Fatalf("已是最新不該下載或自檢:%d %v", *downloads, *verified)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("不該動到 binary")
	}
}

func TestUpdateReleaseChecksumMismatchLeavesBinary(t *testing.T) {
	assets := releaseAssets(t, "1.2.3")
	name := updateAssetName("1.2.3")
	assets[updateChecksums] = []byte(strings.Repeat("0", 64) + "  " + name + "\n")
	stubRelease(t, http.StatusOK, "v1.2.3", assets)
	exe := stubExecutable(t)
	stubVersion(t, "1.0.0")
	verified := stubVerify(t, nil)
	_, err := runCLI(t, "update")
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("校驗不符要明講:%v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" || len(*verified) != 0 {
		t.Fatalf("校驗失敗不能碰 binary、不該跑到自檢:%q %v", b, *verified)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".capy-update-*")); len(left) != 0 {
		t.Fatalf("失敗也要清暫存目錄:%v", left)
	}
}

func TestUpdateReleaseMissingChecksumLine(t *testing.T) {
	name := updateAssetName("1.2.3")
	for label, sums := range map[string]string{
		"缺這個檔那行":   sha256Hex([]byte("x")) + "  capy_1.2.3_plan9_mips.tar.gz\n",
		"hash 被截斷": "deadbeef  " + name + "\n", // 不是 64 字元就不算數(壞掉或被動過的 checksums.txt 不能讓程式 panic)
	} {
		assets := releaseAssets(t, "1.2.3")
		assets[updateChecksums] = []byte(sums)
		downloads := stubRelease(t, http.StatusOK, "v1.2.3", assets)
		exe := stubExecutable(t)
		stubVersion(t, "1.0.0")
		stubVerify(t, nil)
		if _, err := runCLI(t, "update"); err == nil || !strings.Contains(err.Error(), "沒有 "+name+" 這一行") {
			t.Fatalf("%s:要拒絕:%v", label, err)
		}
		if b, _ := os.ReadFile(exe); string(b) != "old" || *downloads != 1 {
			t.Fatalf("%s:不該碰 binary、不該再下載壓縮檔:%q %d", label, b, *downloads)
		}
	}
}

func TestUpdateReleaseMissingPlatformAsset(t *testing.T) {
	assets := releaseAssets(t, "1.2.3")
	delete(assets, updateAssetName("1.2.3"))
	downloads := stubRelease(t, http.StatusOK, "v1.2.3", assets)
	exe := stubExecutable(t)
	stubVersion(t, "1.0.0")
	_, err := runCLI(t, "update")
	if err == nil || !strings.Contains(err.Error(), runtime.GOOS+"/"+runtime.GOARCH) || !strings.Contains(err.Error(), "releases") {
		t.Fatalf("缺本平台的檔要點名平台並指向 releases 頁:%v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" || *downloads != 0 {
		t.Fatalf("不該下載也不該碰 binary:%q %d", b, *downloads)
	}
}

func TestUpdateReleaseVerifyFailureKeepsOld(t *testing.T) {
	stubRelease(t, http.StatusOK, "v1.2.3", releaseAssets(t, "1.2.3"))
	exe := stubExecutable(t)
	stubVersion(t, "1.0.0")
	stubVerify(t, errors.New("exec format error"))
	_, err := runCLI(t, "update")
	if err == nil || !strings.Contains(err.Error(), "自檢失敗") || !strings.Contains(err.Error(), "exec format error") {
		t.Fatalf("自檢失敗要帶原因:%v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("自檢失敗不能換掉舊 binary:%q", b)
	}
}

func TestUpdateReleaseNoneYetPointsToDev(t *testing.T) {
	downloads := stubRelease(t, http.StatusNotFound, "", nil)
	exe := stubExecutable(t)
	stubVersion(t, "dev")
	_, err := runCLI(t, "update")
	if err == nil || !strings.Contains(err.Error(), "--dev") {
		t.Fatalf("沒有 release 時應指向 --dev:%v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" || *downloads != 0 {
		t.Fatal("不該動 binary")
	}
}

// TestUpdateReleaseDownloadTimeout:伺服器收了請求不回,下載要在 Timeout 內放棄、binary 不動。
func TestUpdateReleaseDownloadTimeout(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(hang.Close)
	assets := releaseAssets(t, "1.2.3")
	stubRelease(t, http.StatusOK, "v1.2.3", assets)
	// 把 checksums.txt 的下載網址換成會卡住的那台:stubRelease 的網址是 host 決定的,這裡直接繞過去改 client 目標。
	origClient := downloadClient
	downloadClient = &http.Client{Timeout: 300 * time.Millisecond, Transport: rewriteHost(hang.URL)}
	t.Cleanup(func() { downloadClient = origClient })
	exe := stubExecutable(t)
	stubVersion(t, "1.0.0")
	stubVerify(t, nil)
	start := time.Now()
	_, err := runCLI(t, "update")
	if err == nil || !strings.Contains(err.Error(), updateChecksums) {
		t.Fatalf("卡住的下載要回錯並點名是哪個檔:%v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("下載沒有 Timeout,會無聲卡死")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("不該動到 binary")
	}
}

// rewriteHost:把所有請求導到 target(測試用,讓「下載」打到會卡住的伺服器)。
type rewriteHost string

func (h rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := url.Parse(string(h))
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestUpdateReleaseRateLimited(t *testing.T) {
	stubRelease(t, http.StatusForbidden, "", nil)
	stubExecutable(t)
	stubVersion(t, "1.0.0")
	if _, err := runCLI(t, "update"); err == nil || !strings.Contains(err.Error(), "額度") {
		t.Fatalf("403 應說明額度:%v", err)
	}
}

// TestExtractCapyBothFormats:tar.gz 與 zip 都只解出叫 name 的那個檔(README 不理),與平台無關。
func TestExtractCapyBothFormats(t *testing.T) {
	for _, tc := range []struct {
		ext    string
		zipped bool
	}{{".tar.gz", false}, {".zip", true}} {
		dir := t.TempDir()
		archive := filepath.Join(dir, "capy_1.2.3_x_y"+tc.ext)
		if err := os.WriteFile(archive, fakeArchive(t, "1.2.3", "capy.exe", tc.zipped), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := extractCapy(archive, dir, "capy.exe")
		if err != nil {
			t.Fatalf("%s:%v", tc.ext, err)
		}
		if b, _ := os.ReadFile(got); string(b) != "release:1.2.3" || filepath.Base(got) != "capy.exe" {
			t.Fatalf("%s:解出 %s = %q", tc.ext, got, b)
		}
		if _, err := os.Stat(filepath.Join(dir, "README.md")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s:不該解出 README", tc.ext)
		}
		if st, _ := os.Stat(got); runtime.GOOS != "windows" && st.Mode().Perm()&0o100 == 0 {
			t.Fatalf("%s:解出來的 binary 要可執行:%v", tc.ext, st.Mode())
		}
		if _, err := extractCapy(archive, dir, "nope"); err == nil || !strings.Contains(err.Error(), "沒有 nope") {
			t.Fatalf("%s:壓縮檔裡沒那個檔要回錯:%v", tc.ext, err)
		}
	}
}

// TestVerifyBinaryRunsVersion:真的執行 --version(用 shell script 假扮 binary;Windows 不跑 script)。
func TestVerifyBinaryRunsVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script 在 Windows 不可執行")
	}
	bin := filepath.Join(t.TempDir(), "capy")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'capy version 1.2.3'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyBinary(bin, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if err := verifyBinary(bin, "9.9.9"); err == nil || !strings.Contains(err.Error(), "1.2.3") {
		t.Fatalf("版本不符要把實際輸出帶出來:%v", err)
	}
	if err := verifyBinary(filepath.Join(t.TempDir(), "missing"), "1.2.3"); err == nil {
		t.Fatal("跑不起來要回錯")
	}
}

// TestReleaseAssetNamesMatchGoreleaserConfig:update.go 的檔名契約要與 .goreleaser.yaml 原文一致——
// 任一邊改了名,這裡先紅,不必等第一個真 release 才發現 capy update 抓不到檔。
func TestReleaseAssetNamesMatchGoreleaserConfig(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	yaml := string(b)
	for _, want := range []string{
		`name_template: "capy_{{ .Version }}_{{ .Os }}_{{ .Arch }}"`,
		`name_template: ` + updateChecksums,
		`binary: capy`,
		// 版本與內建 client 的注入路徑:錯一個字,verifyBinary 會拒絕每一個真 release、或 binary 沒有內建 client。
		`-X github.com/Tai-ch0802/capy-music/internal/cli.version={{ .Version }}`,
		`-X github.com/Tai-ch0802/capy-music/internal/auth.BuiltinGoogleClientID={{ .Env.GOOGLE_CLIENT_ID }}`,
		`-X github.com/Tai-ch0802/capy-music/internal/auth.BuiltinGoogleClientSecret={{ .Env.GOOGLE_CLIENT_SECRET }}`,
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf(".goreleaser.yaml 找不到 %q(update.go 的檔名契約靠它)", want)
		}
	}
	if got := updateAssetName("1.2.3"); !strings.HasPrefix(got, "capy_1.2.3_"+runtime.GOOS+"_"+runtime.GOARCH+".") {
		t.Errorf("updateAssetName:%s", got)
	}
}
