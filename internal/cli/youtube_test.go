package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"

	ytauth "github.com/Tai-ch0802/capy-music/internal/auth/youtube"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	youtubeprov "github.com/Tai-ch0802/capy-music/internal/provider/youtube"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// fakeYouTubeCURL:使用者從 DevTools「Copy as cURL」貼上的樣子(cookie 裡多的都會被丟掉)。
const fakeYouTubeCURL = "curl --url 'https://music.youtube.com/youtubei/v1/browse?prettyPrint=false' \\\n" +
	"  -H 'accept: */*' \\\n  -H 'authorization: SAPISIDHASH 1_x' \\\n" +
	"  -b 'VISITOR_INFO1_LIVE=v; SID=s; __Secure-3PSID=p3; __Secure-3PAPISID=ap3; __Secure-3PSIDTS=ts3; LOGIN_INFO=li; SIDCC=cc' \\\n" +
	"  -H 'x-goog-authuser: 0' \\\n  --data-raw '{}'\n"

const wantYouTubeCookie = "__Secure-3PSID=p3; __Secure-3PAPISID=ap3; __Secure-3PSIDTS=ts3"

// youtubeServer:假 InnerTube,只回 account_menu(fixture 是真回應去識別化的:帳號 Someone / @someone / UCtestchannel…);
// 其他端點 404。channel 可換(doctor 的帳號不一致);status 非 200 就整個回它。
func youtubeServer(t *testing.T, status *int32, channel *string) *int32 {
	t.Helper()
	var hits int32
	fixture, err := os.ReadFile("../provider/youtube/testdata/account_menu.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("Cookie") != wantYouTubeCookie || !strings.HasPrefix(r.Header.Get("Authorization"), "SAPISIDHASH ") {
			t.Errorf("請求標頭:%v", r.Header)
		}
		if st := atomic.LoadInt32(status); st != 0 && st != http.StatusOK {
			http.Error(w, "nope", int(st))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/account/account_menu") {
			http.NotFound(w, r)
			return
		}
		body := string(fixture)
		if ch := *channel; ch != "" {
			body = strings.ReplaceAll(body, "UCtestchannel000000000000", ch)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	orig := youtubeAPIBaseSeed
	youtubeAPIBaseSeed = srv.URL
	t.Cleanup(func() { youtubeAPIBaseSeed = orig })
	return &hits
}

func clearYouTube(t *testing.T) {
	t.Helper()
	setCLITestConfig(t)
	_ = ytauth.Delete()
}

// checkYouTubeDisclosure:揭露要講的每一件事都在(非 Google 官方、是 Google 帳號在 YouTube 的 session、拿到的人能以你的身分操作 YouTube、
// 撤銷的路、對其他 Google 服務無效是實測過的、會失效、違反 ToS 風險自負、只指導不擷取、只留三個 cookie、只寫自建清單、不刪清單)。
func checkYouTubeDisclosure(t *testing.T, lang string) {
	t.Helper()
	d := youtubeDisclosure()
	switch lang {
	case "zh-TW":
		for _, must := range []string{"非 Google 官方支援", "Google 帳號在 YouTube 的登入 session", "以你的身分操作整個 YouTube", "登出所有裝置",
			"登不進 Google 的其他服務", "2026-09-29 實測", "Drive API 回 401", "隨時讓它失效", "capy auth login youtube", "違反 YouTube 的服務條款", "風險由你自行承擔",
			"只指導你手動複製", "絕不讀取你的瀏覽器資料", "只留三個必要的 session cookie", "只寫你自己建的清單", "絕不刪除任何清單"} {
			if !strings.Contains(d, must) {
				t.Errorf("zh-TW 揭露缺 %q:%q", must, d)
			}
		}
	case "en":
		if hasCJK(d) {
			t.Errorf("英文揭露混了中文:%q", d)
		}
		for _, must := range []string{"Not officially supported by Google", "your Google account's YouTube login session", "act as you on all of YouTube",
			"sign out of all devices", "doesn't log in to other Google services", "checked on 2026-09-29", "Drive API answers 401", "Google may invalidate it at any time",
			"run capy auth login youtube again", "against YouTube's Terms of Service", "at your own risk", "only shows you how to copy it by hand",
			"never reads your browser's data", "only the three session cookies", "only writes to playlists you created", "never deletes a playlist"} {
			if !strings.Contains(d, must) {
				t.Errorf("英文揭露缺 %q:%q", must, d)
			}
		}
	default: // 新語系沒有逐項檢查就不准過(同 Apple)
		t.Fatalf("add a disclosure check for %s in checkYouTubeDisclosure (internal/cli/youtube_test.go)", lang)
	}
}

// 每條印出或拒絕的路徑,在每個嵌入的語系都帶著完整揭露;成功後 keychain 只有白名單 cookie、config 記下帳號。
func TestAuthLoginYouTubeDisclosureInEveryLanguage(t *testing.T) {
	t.Cleanup(keyring.MockInit)
	for _, lang := range i18n.Supported() {
		t.Run(lang, func(t *testing.T) {
			withLanguage(t, lang)
			checkYouTubeDisclosure(t, lang)
			var status int32
			channel := ""
			youtubeServer(t, &status, &channel)

			clearYouTube(t)
			t.Setenv("CAPY_YOUTUBE_HEADERS", fakeYouTubeCURL)
			out, err := runCLI(t, "auth", "login", "youtube", "--i-understand")
			if err != nil || !strings.Contains(out, youtubeDisclosure()) || !strings.Contains(out, "@someone") {
				t.Fatalf("env 路徑要印完整揭露與偵測到的帳號:%v %q", err, out)
			}
			h, err := ytauth.Load()
			if err != nil || h.Cookie != wantYouTubeCookie || h.AuthUser != "0" {
				t.Errorf("keychain 只留白名單 cookie:%+v %v", h, err)
			}
			cfg, _ := config.Load()
			if cfg.YouTube == nil || cfg.YouTube.Name != "Someone" || cfg.YouTube.Handle != "@someone" || cfg.YouTube.ChannelID != "UCtestchannel000000000000" {
				t.Errorf("config 要記下帳號:%+v", cfg.YouTube)
			}
			if cfg.GoogleEmail != "" { // 跟 Google Drive 的登入無關
				t.Errorf("不該碰 Google 的欄位:%+v", cfg)
			}

			// 沒有 --i-understand:拒絕訊息本身帶完整揭露。
			clearYouTube(t)
			if _, err := runCLI(t, "auth", "login", "youtube"); err == nil ||
				!strings.Contains(err.Error(), youtubeDisclosure()) || !strings.Contains(err.Error(), "--i-understand") {
				t.Fatalf("拒絕訊息要帶完整揭露:%v", err)
			}
			if _, err := ytauth.Load(); !errors.Is(err, secret.ErrNotFound) {
				t.Error("拒絕時不落地")
			}

			// 非 TTY 又沒給標頭:指路(帶指引),不落地。
			t.Setenv("CAPY_YOUTUBE_HEADERS", "")
			if _, err := runCLI(t, "auth", "login", "youtube"); err == nil || !strings.Contains(err.Error(), "CAPY_YOUTUBE_HEADERS") || !strings.Contains(err.Error(), youtubeGuide()) {
				t.Fatalf("非 TTY 沒標頭要指路:%v", err)
			}
		})
	}
}

func TestAuthLoginYouTubeHeadersFileAndRejected(t *testing.T) {
	t.Cleanup(keyring.MockInit)
	withLanguage(t, "en")
	var status int32
	channel := ""
	youtubeServer(t, &status, &channel)
	clearYouTube(t)
	path := t.TempDir() + "/headers.txt"
	if err := os.WriteFile(path, []byte(fakeYouTubeCURL), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "auth", "login", "youtube", "--headers-file", path, "--i-understand"); err != nil || !strings.Contains(out, "Logged in to YouTube Music as Someone (@someone)") {
		t.Fatalf("--headers-file:%v %q", err, out)
	}
	if _, err := runCLI(t, "auth", "login", "youtube", "--headers-file", path+".nope", "--i-understand"); err == nil || !strings.Contains(err.Error(), "can't read") {
		t.Errorf("讀不到檔案:%v", err)
	}
	// 貼錯東西:三種不同的錯,都不落地。
	clearYouTube(t)
	for raw, want := range map[string]string{"accept: */*\n": "no cookie line", "cookie: YSC=1\n": "no __Secure-3PAPISID", "cookie: __Secure-3PAPISID=x\n": "no __Secure-3PSID"} {
		t.Setenv("CAPY_YOUTUBE_HEADERS", raw)
		if _, err := runCLI(t, "auth", "login", "youtube", "--i-understand"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q → %v(要含 %q)", raw, err, want)
		}
	}
	// YouTube 拒絕(401):訊息指向登入,不落地。
	atomic.StoreInt32(&status, http.StatusUnauthorized)
	t.Setenv("CAPY_YOUTUBE_HEADERS", fakeYouTubeCURL)
	if _, err := runCLI(t, "auth", "login", "youtube", "--i-understand"); err == nil || !strings.Contains(err.Error(), "didn't accept") || !strings.Contains(err.Error(), "copy the headers again") {
		t.Errorf("401:%v", err)
	}
	if _, err := ytauth.Load(); !errors.Is(err, secret.ErrNotFound) {
		t.Error("401 不落地")
	}
}

// 精靈路徑:揭露 → 貼上 → 偵測到的帳號要人確認;說「不是」就什麼都不寫。
func TestAuthLoginYouTubeWizard(t *testing.T) {
	t.Cleanup(keyring.MockInit)
	withLanguage(t, "zh-TW")
	var status int32
	channel := ""
	youtubeServer(t, &status, &channel)
	clearYouTube(t)
	origTTY, origD, origI, origA := stdinIsTTY, confirmYouTubeDisclosure, runYouTubeWizardInput, confirmYouTubeAccount
	t.Cleanup(func() {
		stdinIsTTY, confirmYouTubeDisclosure, runYouTubeWizardInput, confirmYouTubeAccount = origTTY, origD, origI, origA
	})
	stdinIsTTY = func() bool { return true }
	disclosed := 0
	confirmYouTubeDisclosure = func() error { disclosed++; return nil }
	runYouTubeWizardInput = func() (string, error) { return fakeYouTubeCURL, nil }
	var asked []string
	confirmYouTubeAccount = func(name, handle string) error {
		asked = append(asked, name+" "+handle)
		return i18n.Errorf("auth.youtube.err.wrong_account")
	}
	if _, err := runCLI(t, "auth", "login", "youtube"); err == nil || !strings.Contains(err.Error(), "重新複製標頭") {
		t.Fatalf("說不是這個帳號要取消:%v", err)
	}
	if disclosed != 1 || len(asked) != 1 || asked[0] != "Someone @someone" {
		t.Errorf("揭露一次、確認一次:%d %v", disclosed, asked)
	}
	if _, err := ytauth.Load(); !errors.Is(err, secret.ErrNotFound) {
		t.Error("取消時不落地")
	}
	confirmYouTubeAccount = func(string, string) error { return nil }
	if out, err := runCLI(t, "auth", "login", "youtube"); err != nil || !strings.Contains(out, "已登入 YouTube Music:Someone(@someone)") {
		t.Fatalf("精靈成功:%v %q", err, out)
	}
	// 揭露不同意:一步都不走。
	confirmYouTubeDisclosure = func() error { return i18n.Errorf("auth.youtube.err.declined") }
	if _, err := runCLI(t, "auth", "login", "youtube"); err == nil || !strings.Contains(err.Error(), "未同意聲明") {
		t.Errorf("不同意揭露:%v", err)
	}
}

func TestAuthStatusLogoutDoctorYouTube(t *testing.T) {
	t.Cleanup(keyring.MockInit)
	withLanguage(t, "en")
	var status int32
	channel := ""
	youtubeServer(t, &status, &channel)
	clearYouTube(t)
	// 沒登入:status missing、doctor 三項都紅、平台建不起來。
	out, _ := runCLI(t, "auth", "status", "--json")
	var st map[string]map[string]any
	if err := json.Unmarshal([]byte(out), &st); err != nil || st["youtube"]["state"] != "missing" || st["youtube"]["account"] != nil {
		t.Fatalf("沒登入的 --json:%v %s", err, out)
	}
	if _, err := runCLI(t, "doctor", "--provider", "youtube"); err == nil || !strings.Contains(err.Error(), "3") {
		t.Errorf("沒登入的 doctor 要三項失敗:%v", err)
	}
	if _, err := newProvider(context.Background(), "youtube"); err == nil || !strings.Contains(err.Error(), "capy auth login youtube") {
		t.Errorf("沒登入建 provider:%v", err)
	}
	t.Setenv("CAPY_YOUTUBE_HEADERS", fakeYouTubeCURL)
	if _, err := runCLI(t, "auth", "login", "youtube", "--i-understand"); err != nil {
		t.Fatal(err)
	}
	out, _ = runCLI(t, "auth", "status")
	if !strings.Contains(out, "youtube:\n  cookie: in the keychain\n  account: Someone (@someone)\n") {
		t.Errorf("auth status 文字版:%q", out)
	}
	out, _ = runCLI(t, "auth", "status", "--json")
	if err := json.Unmarshal([]byte(out), &st); err != nil || st["youtube"]["state"] != "ok" || st["youtube"]["account"] != "Someone" || st["youtube"]["handle"] != "@someone" || st["youtube"]["channel_id"] != "UCtestchannel000000000000" {
		t.Errorf("--json:%v %s", err, out)
	}
	if strings.Contains(out, "p3") || strings.Contains(out, "ap3") { // cookie 的值絕不進 JSON
		t.Errorf("--json 洩漏 cookie:%s", out)
	}
	if out, err := runCLI(t, "doctor", "--provider", "youtube"); err != nil || !strings.Contains(out, "reachable as @someone") {
		t.Errorf("doctor 全過:%v %q", err, out)
	}
	p, err := newProvider(context.Background(), "youtube")
	if err != nil || p.ID() != "youtube" || p.(*youtubeprov.Provider).ChannelID() != "UCtestchannel000000000000" {
		t.Errorf("建 provider:%v", err)
	}
	// cookie 換成別的帳號的(config 沒跟上):doctor 要抓到。
	channel = "UCsomeoneelse00000000000"
	if _, err := runCLI(t, "doctor", "--provider", "youtube"); err == nil {
		t.Error("帳號不一致要紅")
	}
	channel = ""
	// 別的帳號的清單 id:只跳過的訊息要點名對方頻道與目前的帳號。
	if owner := linkOwner(p, nil, "UCother/PL1"); !strings.Contains(owner, "UCother") || !strings.Contains(owner, "@someone") {
		t.Errorf("linkOwner:%q", owner)
	}
	// 登出:cookie 與 config 的帳號都清掉。
	if out, err := runCLI(t, "auth", "logout", "youtube"); err != nil || !strings.Contains(out, "youtube") {
		t.Fatalf("logout:%v %q", err, out)
	}
	if _, err := ytauth.Load(); !errors.Is(err, secret.ErrNotFound) {
		t.Error("logout 要刪 cookie")
	}
	if cfg, _ := config.Load(); cfg.YouTube != nil {
		t.Errorf("logout 要清帳號:%+v", cfg.YouTube)
	}
}

func TestWebDeniesHeadersFile(t *testing.T) {
	withLanguage(t, "en")
	if why := webDenied([]string{"auth", "login", "youtube", "--headers-file", "/etc/passwd"}); why == "" || !strings.Contains(why, "--headers-file") {
		t.Errorf("web 要擋 --headers-file:%q", why)
	}
	if why := webDenied([]string{"auth", "login", "youtube", "--headers-file=/x"}); why == "" {
		t.Error("= 形式也要擋")
	}
}
