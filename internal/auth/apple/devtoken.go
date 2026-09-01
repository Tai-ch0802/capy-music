// Package apple 實作 Apple Music 的認證(spec §4.3)。
// devtoken.go 是 BYO .p8 的本地簽發路徑 —— CLAUDE.md 硬約束,與 Worker 路徑同等地位。
package apple

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTTL:簽短期 token(12h)而非上限 6 個月,外洩損害可控(spec §4.3 設計決策)。
const DefaultTTL = 12 * time.Hour

// LoadP8 載入 MusicKit 的 PKCS#8 ECDSA P-256 私鑰。
func LoadP8(path string) (*ecdsa.PrivateKey, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("%s 不是 PEM 格式", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("不是 ECDSA 私鑰")
	}
	if ec.Curve != elliptic.P256() {
		return nil, fmt.Errorf("MusicKit 金鑰必須是 P-256,得到 %s", ec.Curve.Params().Name)
	}
	return ec, nil
}

// KIDFromPath 從 Apple 慣例檔名 AuthKey_<KID>.p8 取 Key ID;不符回空字串。
func KIDFromPath(path string) string {
	base := filepath.Base(path)
	name, ok := strings.CutPrefix(base, "AuthKey_")
	if !ok {
		return ""
	}
	kid, ok := strings.CutSuffix(name, ".p8")
	if !ok || kid == "" {
		return ""
	}
	return kid
}

// SignDeveloperToken 簽 ES256 developer token(header {alg,kid} / payload {iss,iat,exp})。
// stdlib 即可,不需 JWT 函式庫;JOSE 簽章為 r‖s 各 32 bytes 定長。
func SignDeveloperToken(key *ecdsa.PrivateKey, kid, teamID string, now time.Time, ttl time.Duration) (string, error) {
	b64 := base64.RawURLEncoding.EncodeToString
	header := fmt.Sprintf(`{"alg":"ES256","kid":%q}`, kid)
	payload := fmt.Sprintf(`{"iss":%q,"iat":%d,"exp":%d}`, teamID, now.Unix(), now.Add(ttl).Unix())
	signingInput := b64([]byte(header)) + "." + b64([]byte(payload))

	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + b64(sig), nil
}
