package apple

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/secret"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func writeP8(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	p := filepath.Join(t.TempDir(), "AuthKey_KID1.p8")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func fixedNow() time.Time { return time.Unix(1_800_000_000, 0) }

func jwtExp(t *testing.T, tok string) int64 {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("不是 JWT:%q", tok)
	}
	raw, err := base64RawURLDecode(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ Exp int64 }
	_ = json.Unmarshal(raw, &c)
	return c.Exp
}

// ⭐ load-bearing(BYO 路徑)
func TestDeveloperTokenBYOSignsAndCaches(t *testing.T) {
	_ = secret.Delete(KeyDeveloperToken)
	opts := DevTokenOptions{P8Path: writeP8(t), TeamID: "TEAM1", Now: fixedNow}
	tok, src, err := LegacyDeveloperToken(context.Background(), opts)
	if err != nil || src != "byo" {
		t.Fatalf("(%q, %v)", src, err)
	}
	if exp := jwtExp(t, tok); exp != fixedNow().Add(DefaultTTL).Unix() {
		t.Errorf("exp = %d", exp)
	}
	cached, err := secret.Get(KeyDeveloperToken)
	if err != nil || !strings.Contains(cached, tok) {
		t.Fatalf("應快取到 keychain:(%q, %v)", cached, err)
	}
	// 第二次:命中快取,不重簽
	tok2, src2, err := LegacyDeveloperToken(context.Background(), opts)
	if err != nil || src2 != "cache" || tok2 != tok {
		t.Fatalf("第二次應命中快取:(%q, %v)", src2, err)
	}
}

// ⭐ load-bearing(Worker 路徑)
func TestDeveloperTokenWorkerFetchesAndCaches(t *testing.T) {
	_ = secret.Delete(KeyDeveloperToken)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/apple/developer-token" {
			t.Errorf("請求形狀錯誤:%s %s", r.Method, r.URL.Path)
		}
		var body struct {
			InstallID string `json:"install_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.InstallID != "0123456789abcdef0123456789abcdef" {
			t.Errorf("install_id = %q", body.InstallID)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"token":"eyJ.worker.tok","expires_at":1800043200}`))
	}))
	defer srv.Close()
	opts := DevTokenOptions{Endpoint: srv.URL + "/v1/apple/developer-token", InstallID: "0123456789abcdef0123456789abcdef", HTTP: srv.Client(), Now: fixedNow}
	tok, src, err := LegacyDeveloperToken(context.Background(), opts)
	if err != nil || src != "worker" || tok != "eyJ.worker.tok" {
		t.Fatalf("(%q, %q, %v)", tok, src, err)
	}
	if _, src2, _ := LegacyDeveloperToken(context.Background(), opts); src2 != "cache" || calls != 1 {
		t.Fatalf("第二次應命中快取且不再打 Worker:src=%q calls=%d", src2, calls)
	}
}

func TestDeveloperTokenRefreshesWhenNearExpiry(t *testing.T) {
	// 快取剩 30 分鐘 → 視為過期,重取
	_ = secret.Set(KeyDeveloperToken, `{"token":"old","exp":`+itoa(fixedNow().Add(30*time.Minute).Unix())+`}`)
	opts := DevTokenOptions{P8Path: writeP8(t), TeamID: "T", Now: fixedNow}
	tok, src, err := LegacyDeveloperToken(context.Background(), opts)
	if err != nil || src != "byo" || tok == "old" {
		t.Fatalf("接近到期應重簽:(%q, %q, %v)", tok, src, err)
	}
}

func TestDeveloperTokenBYOPrecedesWorker(t *testing.T) {
	_ = secret.Delete(KeyDeveloperToken)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("有 .p8 時不應打 Worker")
	}))
	defer srv.Close()
	opts := DevTokenOptions{P8Path: writeP8(t), TeamID: "T", Endpoint: srv.URL, HTTP: srv.Client(), Now: fixedNow}
	if _, src, err := LegacyDeveloperToken(context.Background(), opts); err != nil || src != "byo" {
		t.Fatalf("(%q, %v)", src, err)
	}
}

func TestDeveloperTokenNoSource(t *testing.T) {
	_ = secret.Delete(KeyDeveloperToken)
	_, _, err := LegacyDeveloperToken(context.Background(), DevTokenOptions{Now: fixedNow})
	if err == nil || !strings.Contains(err.Error(), "CAPY_APPLE_P8_PATH") {
		t.Fatalf("無來源應指出 BYO 環境變數與 endpoint:%v", err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func base64RawURLDecode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
