package cli

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestUpdateInEnglish:capy update 的輸出與錯誤在英文模式下是英文(T2a update 區)。
func TestUpdateInEnglish(t *testing.T) {
	setCLITestConfig(t)
	withLanguage(t, "en")

	if out, err := runCLI(t, "update", "--help"); err != nil || hasCJK(out) || !strings.Contains(out, "Update capy itself.") || !strings.Contains(out, "Update to the latest commit on main") {
		t.Errorf("update --help:%v %q", err, out)
	}

	t.Run("release already latest", func(t *testing.T) {
		stubRelease(t, http.StatusOK, "v1.2.3", releaseAssets(t, "1.2.3"))
		stubExecutable(t)
		stubVersion(t, "1.2.3")
		stubVerify(t, nil)
		if out, err := runCLI(t, "update"); err != nil || out != "Already up to date: v1.2.3\n" {
			t.Fatalf("%v %q", err, out)
		}
	})

	t.Run("release from dev build", func(t *testing.T) {
		stubRelease(t, http.StatusOK, "v1.2.3", releaseAssets(t, "1.2.3"))
		stubExecutable(t)
		stubVersion(t, "dev")
		stubVerify(t, nil)
		out, err := runCLI(t, "update")
		want := "Latest release is v1.2.3 (you're on a dev build, dev; switching to the release); downloading " + updateAssetName("1.2.3") + "…\nUpdated "
		if err != nil || hasCJK(out) || !strings.HasPrefix(out, want) || !strings.HasSuffix(out, " → v1.2.3\n") {
			t.Fatalf("%v %q", err, out)
		}
	})

	t.Run("release errors", func(t *testing.T) {
		name := updateAssetName("1.2.3")
		assets := releaseAssets(t, "1.2.3")
		assets[updateChecksums] = []byte(strings.Repeat("0", 64) + "  other.tar.gz\n")
		stubRelease(t, http.StatusOK, "v1.2.3", assets)
		stubExecutable(t)
		stubVersion(t, "1.0.0")
		stubVerify(t, nil)
		if _, err := runCLI(t, "update"); err == nil || err.Error() != "checksums.txt has no line for "+name+"; refusing to install" {
			t.Errorf("missing checksum line:%v", err)
		}

		stubRelease(t, http.StatusOK, "v1.2.3", releaseAssets(t, "1.2.3"))
		boom := errors.New("exec format error")
		stubVerify(t, boom)
		_, err := runCLI(t, "update")
		if err == nil || err.Error() != "the new binary failed its self-check (the old one is untouched): exec format error" || !errors.Is(err, boom) {
			t.Errorf("self-check:%v", err)
		}

		stubRelease(t, http.StatusNotFound, "", nil)
		if _, err := runCLI(t, "update"); err == nil || err.Error() != "there is no release yet (the maintainer hasn't published one); for now, capy update --dev updates to the latest commit on main" {
			t.Errorf("no release:%v", err)
		}
	})

	t.Run("dev", func(t *testing.T) {
		stubGitHub(t, http.StatusOK, headJSON)
		stubInstall(t)
		stubExecutable(t)
		stubVersion(t, "2026.09.07-0123456")
		if out, err := runCLI(t, "update", "--dev"); err != nil || out != "Already up to date: 2026.09.07-0123456 (main 0123456)\n" {
			t.Fatalf("already latest:%v %q", err, out)
		}
		stubVersion(t, "2026.09.04-77b72b4")
		out, err := runCLI(t, "update", "--dev")
		if err != nil || hasCJK(out) || !strings.HasPrefix(out, "Latest on main is 0123456 (") || !strings.HasSuffix(out, " → 2026.09.07-0123456\n") {
			t.Fatalf("rebuild:%v %q", err, out)
		}

		stubGitHub(t, http.StatusForbidden, `{"message":"API rate limit exceeded"}`)
		if _, err := runCLI(t, "update", "--dev"); err == nil || err.Error() != "GitHub API rate limit reached (60 requests an hour without authentication); try again later" {
			t.Errorf("rate limit:%v", err)
		}

		t.Setenv("PATH", t.TempDir())
		if _, err := runCLI(t, "update", "--dev"); err == nil || err.Error() != "go not found: --dev builds from source and needs the Go toolchain (https://go.dev/dl/)" {
			t.Errorf("no go:%v", err)
		}
	})
}
