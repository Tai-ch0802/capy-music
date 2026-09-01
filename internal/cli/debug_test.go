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
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/secret"
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

func TestDebugAppleAuthStoresMUT(t *testing.T) {
	keyring.MockInit()
	p8 := writeTestP8(t)

	orig := openBrowser
	// done + t.Cleanup:AuthorizeMUT 一收到結果就 return、defer lb.Close(),可能搶在
	// 這個 goroutine 的 http.PostForm 拿到回應之前關掉伺服器,讓 t.Error 在 test 已
	// 標記完成後才被呼叫(→ panic)。t.Cleanup 在完成標記前執行,drain done 保證
	// goroutine 收工在 test 存活期間。
	openBrowser = func(pageURL string) error {
		done := make(chan struct{})
		t.Cleanup(func() { <-done })
		go func() {
			defer close(done)
			resp, err := http.Get(pageURL)
			if err != nil {
				t.Error(err)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			m := regexp.MustCompile(`STATE = "([A-Za-z0-9_-]+)"`).FindSubmatch(body)
			if m == nil {
				t.Error("頁面沒有 state")
				return
			}
			base := strings.TrimSuffix(pageURL, "/apple/authorize")
			if _, err := http.PostForm(base+"/apple/callback", url.Values{
				"state":            {string(m[1])},
				"music_user_token": {"FAKE_MUT"},
			}); err != nil {
				t.Error(err)
			}
		}()
		return nil
	}
	t.Cleanup(func() { openBrowser = orig })

	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"debug", "apple-auth", "--p8", p8, "--team", "T1", "--timeout", "3s"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "FAKE_MUT") {
		t.Errorf("stdout 應含 MUT,得到 %q", buf.String())
	}
	got, err := secret.Get("apple.music_user_token")
	if err != nil || got != "FAKE_MUT" {
		t.Errorf("keychain = (%q, %v)", got, err)
	}
}
