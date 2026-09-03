package apple

import (
	"encoding/base64"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// fakeJWT:只有 exp 的假 JWT——本專案不驗簽,夠用。
func fakeJWT(t *testing.T, payload string) string {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"ES256","kid":"WebPlayKid"}`)) + "." + enc([]byte(payload)) + ".c2ln"
}

func TestJWTExp(t *testing.T) {
	exp := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)
	got, err := JWTExp(fakeJWT(t, `{"iss":"AMPWebPlay","exp":`+strconv.FormatInt(exp.Unix(), 10)+`}`))
	if err != nil || !got.Equal(exp) {
		t.Fatalf("exp 應解析:(%v, %v)", got, err)
	}
	if _, err := JWTExp(fakeJWT(t, `{"exp":1.7e9}`)); err != nil { // 浮點 exp 也收
		t.Errorf("float exp:%v", err)
	}
	for name, tok := range map[string]string{
		"沒有 exp":         fakeJWT(t, `{"iss":"x"}`),
		"兩段":             "eyJ.eyJ",
		"payload 非 JSON": "eyJ." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".sig",
		"空字串":            "",
	} {
		if _, err := JWTExp(tok); err == nil {
			t.Errorf("%s 應回錯", name)
		}
	}
}

func TestNormalizeDevToken(t *testing.T) {
	for in, want := range map[string]string{
		"  Bearer eyJabc \n": "eyJabc",
		"bearer eyJabc":      "eyJabc",
		"eyJabc":             "eyJabc",
		"Bearer":             "Bearer", // 不是前綴+token 的形狀,原樣留給 JWTExp 去擋
	} {
		if got := NormalizeDevToken(in); got != want {
			t.Errorf("%q → %q,want %q", in, got, want)
		}
	}
}

func TestDeveloperTokenRoundtripAndExpiry(t *testing.T) {
	keyring.MockInit()
	_ = secret.Delete(KeyDeveloperToken)
	now := time.Now()
	if _, _, err := DeveloperToken(now); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("空 keychain 應回 ErrNotFound:%v", err)
	}
	if err := SaveDeveloperToken("eyJtok", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	tok, exp, err := DeveloperToken(now)
	if err != nil || tok != "eyJtok" || !exp.Equal(now.Add(time.Hour).Truncate(time.Second)) {
		t.Fatalf("有效 token:(%q, %v, %v)", tok, exp, err)
	}
	tok, exp, err = DeveloperToken(now.Add(2 * time.Hour))
	if !errors.Is(err, ErrDevTokenExpired) || tok != "" || exp.IsZero() {
		t.Fatalf("過期應回 ErrDevTokenExpired 並附 exp:(%q, %v, %v)", tok, exp, err)
	}
	// 壞掉的紀錄視同不存在(不要讓一筆壞 JSON 永遠卡住使用者)。
	_ = secret.Set(KeyDeveloperToken, "not json")
	if _, _, err := DeveloperToken(now); !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("壞 JSON 應視同不存在:%v", err)
	}
}
