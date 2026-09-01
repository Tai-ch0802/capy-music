package cli

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestP8(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "AuthKey_TESTKID.p8")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(p, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDebugAppleToken(t *testing.T) {
	p8 := writeTestP8(t)
	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"debug", "apple-token", "--p8", p8, "--team", "TEAM1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	tok := strings.TrimSpace(buf.String())
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("輸出應為三段式 JWT,得到 %q", tok)
	}
	hdrJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var hdr struct{ Kid string }
	if err := json.Unmarshal(hdrJSON, &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr.Kid != "TESTKID" {
		t.Errorf("kid 應從檔名推得 TESTKID,得到 %q", hdr.Kid)
	}
}

func TestDebugAppleTokenMissingP8(t *testing.T) {
	t.Setenv("CAPY_APPLE_P8_PATH", "")
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"debug", "apple-token", "--team", "T"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("缺 .p8 應回錯誤")
	}
}
