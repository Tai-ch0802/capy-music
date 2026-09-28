// Package ai 是歌曲 wiki(決策 59)用的 OpenAI 相容端點 client。端點是使用者自己的(BYO):capy 不預設任何 URL、
// 不架服務、不代持金鑰——base URL / model / 母語在 config.json,API key 與自訂標頭在 keychain(標頭整包當機密:
// 每行是不是憑證程式分不出來,Cloudflare Access 的 service token 就是)。
// 只實作 chat/completions(串流)與 GET /models 探測;不重試 5xx、不退避 429:端點是使用者的,錯了直說。
package ai

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// keychain 的兩把(CLAUDE.md:憑證只進 keychain;config set 碰不到它們,只有 wiki setup 會寫)。
const (
	KeyAPIKey  = "ai.api_key"
	KeyHeaders = "ai.headers" // 多行,每行 Name: value
	// MaxHeadersBytes:自訂標頭的上限。Windows Credential Manager 的 blob 上限 2560 位元組;macOS 的 go-keyring 先 base64
	// 再塞進 security -i 的一行命令(上限 4096 字),2048 原值 → 2732 字加前綴仍在 4096 內(2026-09-28 在真 keychain 實測過)。
	MaxHeadersBytes = 2048
	// MaxModelsForPicker:/models 列出這麼多以內才開挑選器(OpenRouter 類端點會回幾百個),超過退回手打。
	MaxModelsForPicker = 30
)

// SentFields:capy wiki 會送到端點的歌曲欄位,用隱私權政策(英文版)的用詞;T2 的 prompt 是唯一的送出點。
// 程式、政策(中英)、README 三處要一字對齊——site/site_test.go 釘住政策,改一處測試就紅。
var SentFields = []string{"title", "artist", "album", "release date", "genre"}

// ErrNotConfigured:還沒跑 wiki setup(沒有 base URL 或 model)。
var ErrNotConfigured = i18n.Errorf("ai.err.not_configured")

// ErrNoModelList:端點沒實作 GET /models(404 / 405)。呼叫端退回 1-token chat 驗 model。
var ErrNoModelList = errors.New("endpoint has no model list")

// Config:一次呼叫需要的全部設定。Headers 已解析、名稱已正規化,送出時最後套用(使用者自己寫的 Authorization 蓋掉 Bearer)。
type Config struct {
	BaseURL string
	Model   string
	APIKey  string
	Headers http.Header
}

// Load:config.json + keychain 組出 Config;沒有 base URL 或 model 回 ErrNotConfigured。
// keychain 讀不到(鎖住、使用者按取消)原樣回,不當成「沒設定」。
func Load() (Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return Config{}, err
	}
	if cfg.AIBaseURL == "" || cfg.AIModel == "" {
		return Config{}, ErrNotConfigured
	}
	key, text, err := LoadSecrets()
	if err != nil {
		return Config{}, err
	}
	h, err := ParseHeaders(text)
	if err != nil {
		return Config{}, err
	}
	return Config{BaseURL: cfg.AIBaseURL, Model: cfg.AIModel, APIKey: key, Headers: h}, nil
}

// LoadSecrets:keychain 裡的 API key 與自訂標頭原文;沒有就是空字串(兩者都可以沒有:本機 gateway 常常不用金鑰)。
func LoadSecrets() (apiKey, headersText string, err error) {
	if apiKey, err = getOrEmpty(KeyAPIKey); err != nil {
		return "", "", err
	}
	if headersText, err = getOrEmpty(KeyHeaders); err != nil {
		return "", "", err
	}
	return apiKey, headersText, nil
}

func getOrEmpty(key string) (string, error) {
	v, err := secret.Get(key)
	if errors.Is(err, secret.ErrNotFound) {
		return "", nil
	}
	return v, err
}

// ParseHeaders:多行 "Name: value" → http.Header。以第一個冒號切、兩邊 trim、空白行跳過;沒有冒號、名稱或值是空的、
// 含控制字元(CR / LF 之類,net/http 也會拒收)的行整包拒絕——setup 當場就講,不要等到第一次請求才炸。
// 名稱經 Header.Set 正規化(不直接寫 map:非 canonical 的名字會變成重複的 header);同名後面的蓋掉前面的。
func ParseHeaders(text string) (http.Header, error) {
	h := http.Header{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" || value == "" || hasControl(name) || hasControl(value) || strings.ContainsAny(name, " \t") {
			return nil, i18n.Errorf("ai.err.header_bad_line", "line", i+1)
		}
		h.Set(name, value)
	}
	return h, nil
}

// ValidateHeadersText:大小上限 + 解析(setup 與 config set 共用)。
func ValidateHeadersText(text string) error {
	if len(text) > MaxHeadersBytes {
		return i18n.Errorf("ai.err.headers_too_big", "max", MaxHeadersBytes, "size", len(text))
	}
	_, err := ParseHeaders(text)
	return err
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) })
}

// NormalizeBaseURL:trim、去掉尾端的 /;要是 http(s) 而且有 host。回正規化後的字串(到 /v1 為止,不含 /chat/completions)。
func NormalizeBaseURL(raw string) (string, error) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", i18n.Errorf("ai.err.bad_url", "value", raw)
	}
	return s, nil
}

// InsecureRemote:明文 http 打到非本機的 host——API key 與 Zero Trust 的 secret 會明文走網路。本機
// (localhost、127.x、::1、*.localhost)不算:區網或本機的 Ollama 是正當用法,所以呼叫端只警告、不拒絕(Q74)。
func InsecureRemote(base string) bool {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false
	}
	return true
}

// LanguageLabel:母語代碼 → 給 prompt 用的 {language}:英文名稱加代碼本身,例如 "Japanese (ja)"、"Chinese (Taiwan) (zh-TW)"。
// 2026-09-28 實測 display 對 zh-TW 只給 "Chinese (Taiwan)"(不說繁體),補齊 script / region 又會變成 "Japanese (Japanese, Japan)"
// 這種噪音;附上代碼模型就分得出繁簡,不寫 script 推斷。回的第二個值是正規化的代碼(zh_tw → zh-TW),config 存它。
// language.Parse 對「格式對但不存在」的(xx-YY)也會報錯。
func LanguageLabel(code string) (label, tag string, err error) {
	t, perr := language.Parse(strings.TrimSpace(code))
	if perr != nil || t == language.Und {
		return "", "", i18n.Errorf("ai.err.bad_language", "value", code)
	}
	return display.English.Tags().Name(t) + " (" + t.String() + ")", t.String(), nil
}
