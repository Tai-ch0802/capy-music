package apple

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func b64d(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSignDeveloperToken(t *testing.T) {
	key := testKey(t)
	now := time.Unix(1756600000, 0)
	tok, err := SignDeveloperToken(key, "KEYID", "TEAMID", now, DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT 應為三段,得到 %d 段", len(parts))
	}

	var hdr struct{ Alg, Kid string }
	if err := json.Unmarshal(b64d(t, parts[0]), &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr.Alg != "ES256" || hdr.Kid != "KEYID" {
		t.Errorf("header = %+v", hdr)
	}

	var claims struct {
		Iss string
		Iat int64
		Exp int64
	}
	if err := json.Unmarshal(b64d(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "TEAMID" || claims.Iat != now.Unix() || claims.Exp != now.Add(12*time.Hour).Unix() {
		t.Errorf("claims = %+v", claims)
	}

	sig := b64d(t, parts[2])
	if len(sig) != 64 {
		t.Fatalf("JOSE 簽章應為 64 bytes(r‖s),得到 %d", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, sum[:], r, s) {
		t.Fatal("簽章驗證失敗")
	}
}

func TestLoadP8Roundtrip(t *testing.T) {
	key := testKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "AuthKey_TEST123.p8")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(p, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadP8(p)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(key) {
		t.Fatal("載回的金鑰不相等")
	}
}

func TestLoadP8RejectsNonP256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "AuthKey_TEST384.p8")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(p, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadP8(p); err == nil {
		t.Fatal("P-384 金鑰應回錯誤,而非成功載入(SignDeveloperToken 的 FillBytes 會 panic)")
	}
}

func TestLoadP8NotPEM(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.p8")
	if err := os.WriteFile(p, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadP8(p); err == nil {
		t.Fatal("非 PEM 應回錯誤")
	}
}

func TestKIDFromPath(t *testing.T) {
	cases := map[string]string{
		"AuthKey_ABC123.p8":  "ABC123",
		"/x/y/AuthKey_Z9.p8": "Z9",
		"key.p8":             "",
		"AuthKey_.p8":        "",
		"AuthKey_ABC123.pem": "",
	}
	for in, want := range cases {
		if got := KIDFromPath(in); got != want {
			t.Errorf("KIDFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}
