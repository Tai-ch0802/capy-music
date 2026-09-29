package youtube

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/secret"
)

const fullCookie = "VISITOR_INFO1_LIVE=abc; SID=s1; HSID=h1; SSID=ss; APISID=a1; SAPISID=sa1; __Secure-1PSID=p1; __Secure-3PSID=p3; " +
	"__Secure-1PAPISID=ap1; __Secure-3PAPISID=ap3/xyz; LOGIN_INFO=long-login-info; __Secure-1PSIDTS=ts1; __Secure-3PSIDTS=ts3; SIDCC=cc; __Secure-3PSIDCC=cc3"

func TestParsePlainRequestHeaders(t *testing.T) {
	raw := ":authority: music.youtube.com\r\nAccept: */*\r\nCookie: " + fullCookie + "\r\nX-Goog-AuthUser: 2\r\nX-Goog-PageId: 1234567890\r\nsec-ch-ua: \"x\"\r\n"
	h, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	// 只留白名單、照原順序;其他全部丟掉(整段 2 KB 會撞 keychain 上限,而且 LOGIN_INFO / SIDCC 都不需要)
	want := "__Secure-1PSID=p1; __Secure-3PSID=p3; __Secure-1PAPISID=ap1; __Secure-3PAPISID=ap3/xyz; __Secure-1PSIDTS=ts1; __Secure-3PSIDTS=ts3"
	if h.Cookie != want {
		t.Errorf("cookie 白名單:\n got %q\nwant %q", h.Cookie, want)
	}
	if h.AuthUser != "2" || h.PageID != "1234567890" {
		t.Errorf("authuser / pageid:%+v", h)
	}
}

func TestParseCopyAsCURL(t *testing.T) {
	raw := "curl --url 'https://music.youtube.com/youtubei/v1/browse?prettyPrint=false' \\\n" +
		"  -H 'accept: */*' \\\n" +
		"  -H 'authorization: SAPISIDHASH 1_abc' \\\n" +
		"  -b '" + fullCookie + "' \\\n" +
		"  -H 'x-goog-authuser: 0' \\\n" +
		"  --data-raw '{\"context\":{}}'\n"
	h, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h.Cookie, "__Secure-1PSID=p1; ") || h.AuthUser != "0" || h.PageID != "" {
		t.Errorf("cURL 格式:%+v", h)
	}
}

// Windows Chrome 的「Copy as cURL (cmd)」:^" 當引號、行尾 ^ 接續。
func TestParseCopyAsCURLCmd(t *testing.T) {
	raw := "curl ^\"https://music.youtube.com/youtubei/v1/browse?prettyPrint=false^\" ^\r\n" +
		"  -H ^\"accept: */*^\" ^\r\n" +
		"  -b ^\"" + fullCookie + "^\" ^\r\n" +
		"  -H ^\"x-goog-authuser: 1^\" ^\r\n" +
		"  --data-raw ^\"^{^}^\"\r\n"
	h, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h.Cookie, "__Secure-1PSID=p1; ") || h.AuthUser != "1" {
		t.Errorf("cmd 格式:%+v", h)
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      error
	}{
		{"沒有 cookie", "accept: */*\nx-goog-authuser: 0\n", ErrNoCookie},
		{"沒有 SAPISID(未登入的請求)", "cookie: VISITOR_INFO1_LIVE=abc; YSC=1\n", ErrNoSAPISID},
		{"沒有 session cookie", "cookie: __Secure-3PAPISID=ap3\n", ErrNoSession},
	} {
		if _, err := Parse(tc.raw); !errors.Is(err, tc.want) {
			t.Errorf("%s:got %v want %v", tc.name, err, tc.want)
		}
	}
	if _, err := Parse("cookie: " + fullCookie + "\nx-goog-authuser: abc\n"); err == nil || !strings.Contains(err.Error(), `"abc"`) {
		t.Errorf("authuser 不是數字要拒絕:%v", err)
	}
	if h, err := Parse("cookie: " + fullCookie + "\n"); err != nil || h.AuthUser != "0" {
		t.Errorf("沒有 x-goog-authuser 預設 0:%+v %v", h, err)
	}
}

// SAPISIDHASH 對固定時間戳的已知值(ytmusicapi 的演算法:sha1("<ts> <SAPISID> <origin>"))。
func TestAuthorization(t *testing.T) {
	h := Headers{Cookie: "__Secure-3PSID=p3; __Secure-3PAPISID=ap3/xyz"}
	got := h.Authorization(time.Unix(1700000000, 0))
	if got != "SAPISIDHASH 1700000000_a48362593844d4bcf9f660843438b80d46377aeb" {
		t.Errorf("got %q", got)
	}
	only1P := Headers{Cookie: "__Secure-1PSID=p1; __Secure-1PAPISID=ap1"}
	if !strings.HasPrefix(only1P.Authorization(time.Unix(1, 0)), "SAPISIDHASH 1_") {
		t.Errorf("1P 家族也要能算:%q", only1P.Authorization(time.Unix(1, 0)))
	}
}

func TestSaveLoadDelete(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	if _, err := Load(); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("空的 keychain 要回 ErrNotFound:%v", err)
	}
	h := Headers{Cookie: "__Secure-3PSID=p3; __Secure-3PAPISID=ap3; __Secure-3PSIDTS=ts", AuthUser: "1", PageID: "9"}
	if err := Save(h); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil || got != h {
		t.Fatalf("round-trip:%+v %v", got, err)
	}
	if err := secret.Set(KeyHeaders, "not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); !errors.Is(err, secret.ErrNotFound) { // 壞掉的記錄當沒有:重新登入覆寫
		t.Errorf("壞記錄:%v", err)
	}
	if err := Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("刪掉之後:%v", err)
	}
}
