package secret

import (
	"errors"
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestMain(m *testing.M) {
	keyring.MockInit() // 測試不碰真實 OS keychain
	os.Exit(m.Run())
}

func TestSetGetDelete(t *testing.T) {
	if err := Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	got, err := Get("k")
	if err != nil || got != "v" {
		t.Fatalf("Get = (%q, %v), want (v, nil)", got, err)
	}
	if err := Delete("k"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("刪除後應為 ErrNotFound,得到 %v", err)
	}
}

func TestGetMissingIsErrNotFound(t *testing.T) {
	if _, err := Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
