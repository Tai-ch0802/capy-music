// Package youtube:YouTube Music 憑證的解析與 keychain 存取(spec §4.6、決策 60)。
// 憑證是使用者自己從 music.youtube.com 的 DevTools 複製的請求標頭(非官方;跟 Apple 的 web token 同一類 BYO):
// 這裡只解析、驗證、儲存,絕不自動擷取(CLAUDE.md 鐵則)。跟 Google Drive 的登入(auth.KeyGoogleToken)完全分開,
// 不讀、不寫、不推導它——YouTube Music 帳號可以是另一個 Google 帳號。
package youtube

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// KeyHeaders:keychain 鍵。值是 Headers 的 JSON;cookie 只留白名單(2026-09-29 真帳號探測:__Secure-3PSID + __Secure-3PAPISID +
// __Secure-3PSIDTS 三個就能登入、322 bytes;整段 2.2 KB 會撞 Windows Credential Manager 的 2560 與 macOS 約 3 KB 的上限)。
const KeyHeaders = "youtube.headers"

// Origin:SAPISIDHASH 綁的 origin,也是每個請求的 Origin / X-Origin。
const Origin = "https://music.youtube.com"

// Headers:從貼上的標頭裡留下來的三樣。AuthUser 是瀏覽器同時登入多個 Google 帳號時的索引(抄錯 = 變成別人);
// PageID 是品牌帳號(選填)。
type Headers struct {
	Cookie   string `json:"cookie"`
	AuthUser string `json:"authuser"`
	PageID   string `json:"pageid,omitempty"`
}

// cookie3P / cookie1P:留下來的三個 cookie。3P 是探測驗過的最小集合;1P 是同一組的另一個家族(探測也能單獨登入),
// 只在 3P 不齊時才改留 1P——揭露、政策、CLAUDE.md 都寫「只留三個 session cookie」,程式要跟字面一致(PR #117 review 第 1 點)。
var (
	cookie3P = []string{"__Secure-3PSID", "__Secure-3PAPISID", "__Secure-3PSIDTS"}
	cookie1P = []string{"__Secure-1PSID", "__Secure-1PAPISID", "__Secure-1PSIDTS"}
)

var (
	ErrNoCookie   = i18n.Errorf("youtube.err.no_cookie")
	ErrNoSAPISID  = i18n.Errorf("youtube.err.no_sapisid")
	ErrNoSession  = i18n.Errorf("youtube.err.no_session")
	curlHeaderRe  = regexp.MustCompile(`^\s*-H\s+\$?['"](.*?)['"]\s*\\?\s*$`) // $'…' 是 bash 版值裡有 ' 時 Chrome 用的 ANSI-C 引號
	curlCookieRe  = regexp.MustCompile(`^\s*(?:-b|--cookie)\s+\$?['"](.*?)['"]\s*\\?\s*$`)
	plainHeaderRe = regexp.MustCompile(`^\s*([A-Za-z0-9-]+):\s*(.*?)\s*$`)
)

// Parse 解析使用者貼上的整段標頭。兩種格式都收:DevTools「Request Headers」的純文字(一行一個 name: value)與
// 「Copy as cURL」(-H 'name: value' \ 與 -b '<cookie>';Windows 的 cmd 變體用 ^" 與 ^ 也認得)。只取 cookie / x-goog-authuser / x-goog-pageid,其他一律丟掉;
// cookie 只留白名單。沒有 cookie、cookie 裡沒有 SAPISID、或沒有 session cookie 各回不同錯誤(貼錯地方的人才知道下一步)。
func Parse(raw string) (Headers, error) {
	var h Headers
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		// Windows Chrome 的「Copy as cURL (cmd)」:引號是 ^",換行接續是 ^;先還原成 bash 的樣子再比對。
		line = strings.ReplaceAll(line, `^"`, `"`)
		line = strings.TrimSuffix(strings.TrimRight(line, " \t"), "^")
		if m := curlCookieRe.FindStringSubmatch(line); m != nil {
			h.Cookie = unquoteANSIC(m[1])
			continue
		}
		if m := curlHeaderRe.FindStringSubmatch(line); m != nil {
			line = unquoteANSIC(m[1])
		}
		m := plainHeaderRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch strings.ToLower(m[1]) {
		case "cookie":
			h.Cookie = m[2]
		case "x-goog-authuser":
			h.AuthUser = m[2]
		case "x-goog-pageid":
			h.PageID = m[2]
		}
	}
	if strings.TrimSpace(h.Cookie) == "" {
		return Headers{}, ErrNoCookie
	}
	if sapisid(h.Cookie) == "" { // 先在整段上驗,錯誤才分得出「沒登入」與「缺 session」
		return Headers{}, ErrNoSAPISID
	}
	if !hasCookie(h.Cookie, "__Secure-3PSID") && !hasCookie(h.Cookie, "__Secure-1PSID") {
		return Headers{}, ErrNoSession
	}
	h.Cookie = trimCookie(h.Cookie)
	if h.AuthUser == "" {
		h.AuthUser = "0"
	}
	if _, err := strconv.Atoi(h.AuthUser); err != nil {
		return Headers{}, i18n.Errorf("youtube.err.bad_authuser", "value", strconv.Quote(h.AuthUser))
	}
	return h, nil
}

// unquoteANSIC:$'…' 裡的 \' 還原成 '(其他跳脫 cookie 值裡不會出現)。
func unquoteANSIC(s string) string { return strings.ReplaceAll(s, `\'`, `'`) }

// trimCookie:只留三個 cookie、照原本的順序,重組成 "a=1; b=2"。3P 的 SID 與 APISID 都在就留 3P 那三個,否則留 1P 那三個。
func trimCookie(cookie string) string {
	allow := cookie3P
	if !hasCookie(cookie, "__Secure-3PSID") || !hasCookie(cookie, "__Secure-3PAPISID") {
		allow = cookie1P
	}
	var kept []string
	for _, pair := range strings.Split(cookie, ";") {
		pair = strings.TrimSpace(pair)
		name, _, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		for _, a := range allow {
			if name == a {
				kept = append(kept, pair)
				break
			}
		}
	}
	return strings.Join(kept, "; ")
}

func hasCookie(cookie, name string) bool { return cookieValue(cookie, name) != "" }

func cookieValue(cookie, name string) string {
	for _, pair := range strings.Split(cookie, ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(pair), "=")
		if k == name {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// sapisid:算 SAPISIDHASH 用的值。3P 優先(探測驗過),沒有就 1P。
func sapisid(cookie string) string {
	if v := cookieValue(cookie, "__Secure-3PAPISID"); v != "" {
		return v
	}
	return cookieValue(cookie, "__Secure-1PAPISID")
}

// Authorization:網頁播放器每個請求都帶的 SAPISIDHASH:sha1("<unix ts> <SAPISID> <origin>"),前面接時間戳。
// 每次請求現算(它綁時間);貼上的 authorization 標頭不用留。
func (h Headers) Authorization(now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	sum := sha1.Sum([]byte(ts + " " + sapisid(h.Cookie) + " " + Origin))
	return "SAPISIDHASH " + ts + "_" + hex.EncodeToString(sum[:])
}

// Save / Load / Delete:keychain。Load 沒有時原樣透傳 secret.ErrNotFound;記錄壞掉(別的版本寫的)也當沒有——
// 重新登入會覆寫。
func Save(h Headers) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if err := secret.Set(KeyHeaders, string(b)); err != nil {
		return i18n.Errorf("auth.err.keychain_write", "err", err)
	}
	return nil
}

func Load() (Headers, error) {
	raw, err := secret.Get(KeyHeaders)
	if err != nil {
		return Headers{}, err
	}
	var h Headers
	if json.Unmarshal([]byte(raw), &h) != nil || h.Cookie == "" {
		return Headers{}, secret.ErrNotFound
	}
	return h, nil
}

func Delete() error { return secret.Delete(KeyHeaders) }
