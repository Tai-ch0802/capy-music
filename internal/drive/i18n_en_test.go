package drive_test

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

func withLanguage(t *testing.T, lang string) {
	t.Helper()
	prev := i18n.Current()
	if !i18n.Set(lang) {
		t.Fatalf("不支援 %s", lang)
	}
	t.Cleanup(func() { i18n.Set(prev) })
}

// 英文模式:capy 自己的措辭是英文,Google 回的 message 原樣接在後面;哨兵照舊 errors.Is 得到。
func TestDriveMessagesEnglish(t *testing.T) {
	withLanguage(t, "en")
	c, srv := newClient(t)
	var stderr bytes.Buffer
	orig := drive.Stderr
	drive.Stderr = &stderr
	t.Cleanup(func() { drive.Stderr = orig })

	props := map[string]string{"kind": "manifest"}
	mustCreate(t, c, "manifest.json", props, "old")
	srv.Advance(time.Hour)
	newer := mustCreate(t, c, "manifest.json", props, "new")
	if _, err := c.Find(ctx, "manifest.json", props); err != nil {
		t.Fatal(err)
	}
	if want := "Warning: Drive appdata has 2 copies of manifest.json; using the newest (" + newer.ModifiedTime.Format(time.RFC3339) + ")\n"; stderr.String() != want {
		t.Errorf("got %q\nwant %q", stderr.String(), want)
	}

	if _, err := c.Update(ctx, newer.ID, nil, nil); err == nil || err.Error() != "drive.Update needs content: empty content would wipe the file on Drive" {
		t.Errorf("empty update: %v", err)
	}

	srv.Fail(1, http.StatusForbidden, "storageQuotaExceeded", "")
	if _, err := c.List(ctx, ""); !errors.Is(err, drive.ErrStorageQuota) || !strings.HasSuffix(err.Error(), "try again: The user's Drive storage quota has been exceeded.") {
		t.Errorf("quota: %v", err)
	}
	srv.Fail(1, http.StatusForbidden, "insufficientPermissions", "")
	_, err := c.List(ctx, "")
	if !errors.Is(err, provider.ErrAuthExpired) || !strings.HasPrefix(err.Error(), "authorization expired: the Drive authorization is missing the drive.appdata scope (") ||
		!strings.HasSuffix(err.Error(), `); when you log in again, tick "See, create, and delete its own configuration data in your Google Drive"`) {
		t.Errorf("scope: %v", err)
	}
	srv.Fail(1, http.StatusUnauthorized, "authError", "")
	_, err = c.List(ctx, "")
	var ae *drive.APIError
	if !errors.Is(err, provider.ErrAuthExpired) || errors.As(err, &ae) || !strings.HasPrefix(err.Error(), "authorization expired: drive API 401 authError") {
		t.Errorf("401 要是 ErrAuthExpired 加上原文,*APIError 不進錯誤鏈:%v", err)
	}
}
