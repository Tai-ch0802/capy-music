package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

// 英文模式:升版保留舊檔的提示、版本不符、孤兒列、路徑含 ? 都是英文;ErrSchemaMismatch 照舊被 errors.Is 看到。
func TestEnglishMessages(t *testing.T) {
	prev := i18n.Current()
	i18n.Set("en")
	t.Cleanup(func() { i18n.Set(prev) })

	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	s, err := OpenAt(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := OpenReadOnlyAt(path, time.Second); !errors.Is(err, ErrSchemaMismatch) ||
		err.Error() != "this computer's state.db has a different schema version from this capy: the file is v99, this capy supports v"+strconv.Itoa(schemaVersion)+"; to export it, use the capy binary of that version" {
		t.Errorf("版本不符:%v", err)
	}

	var notice bytes.Buffer
	Stderr = &notice
	t.Cleanup(func() { Stderr = os.Stderr })
	if s, err = OpenAt(path, time.Second); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := "This computer's cache schema changed from v99 to v" + strconv.Itoa(schemaVersion) + ": the old file was kept as " + path + ".v99, and the cache will be rebuilt from Drive on the next pull. " +
		"This capy doesn't read the old file; if pull says Drive is incomplete, recover with the previous capy binary (GitHub Releases): rename the old file back to state.db, run capy drive init --from-local, then update again. Once you're sure you don't need it, delete it yourself\n"
	if notice.String() != want {
		t.Errorf("升版提示:\n got %q\nwant %q", notice.String(), want)
	}

	if _, err := s.db.Exec("INSERT INTO isrcs (cid, isrc) VALUES ('ghost', 'X')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dump(); err == nil || err.Error() != "isrcs has cid ghost, which isn't in tracks (state.db is inconsistent; delete it to rebuild)" {
		t.Errorf("孤兒列:%v", err)
	}

	q := filepath.Join(dir, "q?mark", "state.db")
	if _, err := OpenAt(q, time.Second); err == nil || err.Error() != "the database path can't contain ? (point CAPY_CONFIG_DIR at another directory): "+q {
		t.Errorf("路徑含 ?:%v", err)
	}
}
