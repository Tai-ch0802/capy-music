package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	ytauth "github.com/Tai-ch0802/capy-music/internal/auth/youtube"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	youtubeprov "github.com/Tai-ch0802/capy-music/internal/provider/youtube"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// YouTube Music 登入(spec §4.6、決策 60):使用者自己從 music.youtube.com 的 DevTools 複製請求標頭貼上(非官方,跟 Apple 同一類 BYO);
// capy 只指導、絕不擷取。跟 Google Drive 的登入完全分開:不同的 keychain 鍵、不同的 config 欄,可以是不同的 Google 帳號。

// youtubeDisclosure / youtubeGuide:用到時才翻;揭露每個語系都要完整(i18n_en_auth_test.go 的 checkYouTubeDisclosure 逐語系釘住)。
func youtubeDisclosure() string { return i18n.T("auth.youtube.disclosure") }

func youtubeGuide() string { return i18n.T("auth.youtube.guide") }

// youtubeLogin:兩條入口(--headers-file / CAPY_YOUTUBE_HEADERS、TTY 精靈)收口到 youtubePersist。揭露不可跳過:
// 非互動路徑要 --i-understand(拒絕訊息本身就帶聲明);精靈路徑是第一頁的 Confirm。貼上後一定先讓使用者看到偵測到的帳號
// (瀏覽器同時登入多個 Google 帳號時抄錯 x-goog-authuser 就變成別人)。
func youtubeLogin(cmd *cobra.Command) error {
	raw := ""
	if path, _ := cmd.Flags().GetString("headers-file"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return i18n.Errorf("auth.youtube.err.headers_file", "path", path, "err", err)
		}
		raw = string(b)
	}
	if raw == "" {
		raw = os.Getenv("CAPY_YOUTUBE_HEADERS")
	}
	if raw == "" {
		if !stdinIsTTY() {
			return i18n.Errorf("auth.youtube.err.non_tty_needs_input", "guide", youtubeGuide())
		}
		if err := confirmYouTubeDisclosure(); err != nil {
			return err
		}
		raw, err := runYouTubeWizardInput()
		if err != nil {
			return err
		}
		return youtubePersist(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), raw, confirmYouTubeAccount)
	}
	if ok, _ := cmd.Flags().GetBool("i-understand"); !ok {
		return i18n.Errorf("auth.youtube.err.flags_need_i_understand", "disclosure", youtubeDisclosure())
	}
	fmt.Fprintln(cmd.ErrOrStderr(), youtubeDisclosure()) // 揭露在指令內、每條路徑都出現(CLAUDE.md 鐵則);印到 stderr,不動 stdout 契約
	return youtubePersist(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), raw, nil)
}

// 測試 / web 替換點(精靈本體需要 TTY)。
var (
	confirmYouTubeDisclosure = youtubeConfirmDisclosure
	runYouTubeWizardInput    = youtubeWizardInput
	confirmYouTubeAccount    = youtubeConfirmAccount
)

// youtubeConfirmDisclosure:揭露頁,Confirm 預設「取消」;不同意即 error。
func youtubeConfirmDisclosure() error {
	agree := false
	if err := newForm(huh.NewGroup(
		huh.NewNote().Title(i18n.T("auth.youtube.confirm.title")).Description(youtubeDisclosure()),
		huh.NewConfirm().Title(i18n.T("auth.youtube.confirm.question")).
			Affirmative(i18n.T("auth.youtube.confirm.agree")).Negative(i18n.T("auth.youtube.confirm.cancel")).Value(&agree),
	)).Run(); err != nil {
		return err
	}
	if !agree {
		return i18n.Errorf("auth.youtube.err.declined")
	}
	return nil
}

// youtubeWizardInput:指引 + 多行貼上(整段 Request Headers 或 Copy as cURL)。
func youtubeWizardInput() (string, error) {
	var raw string
	if err := newForm(huh.NewGroup(
		huh.NewNote().Title(i18n.T("auth.youtube.wizard.guide_title")).Description(youtubeGuide()),
		// 貼上的整段常 8 KB / 30 多行:bubbles v2 的 textarea 預設沒有字元上限(defaultCharLimit = 0),這裡不再設一個。
		huh.NewText().Title(i18n.T("auth.youtube.wizard.headers_label")).Value(&raw).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return i18n.Errorf("auth.youtube.err.empty")
			}
			return nil
		}),
	)).Run(); err != nil {
		return "", err
	}
	return raw, nil
}

// youtubeAccountLabel:「名稱(@handle)」;沒有 handle 的帳號(探測看過)只印名稱,不印空括號。
func youtubeAccountLabel(name, handle string) string {
	if handle == "" {
		return name
	}
	return i18n.T("auth.youtube.account_label", "name", name, "handle", handle)
}

// youtubeConfirmAccount:貼上後的帳號確認(預設「是」)。
func youtubeConfirmAccount(name, handle string) error {
	yes := true
	if err := newForm(huh.NewGroup(
		huh.NewConfirm().Title(i18n.T("auth.youtube.wizard.account_question", "account", youtubeAccountLabel(name, handle))).
			Affirmative(i18n.T("auth.youtube.wizard.account_yes")).Negative(i18n.T("auth.youtube.wizard.account_no")).Value(&yes),
	)).Run(); err != nil {
		return err
	}
	if !yes {
		return i18n.Errorf("auth.youtube.err.wrong_account")
	}
	return nil
}

// youtubePersist:解析(只留三個 header、cookie 白名單)→ account_menu 驗證並取得帳號 → 確認(TTY / web)或印到 stderr(非互動)
// → 落地 keychain + config。驗證通過前完全不寫;keychain 與 config 兩個寫入不是 atomic,失敗就重跑一次覆寫。
func youtubePersist(ctx context.Context, out, errw io.Writer, raw string, confirm func(name, handle string) error) error {
	h, err := ytauth.Parse(raw)
	if err != nil {
		return err
	}
	cfg, err := config.Load() // 先讀 config:壞掉的 config.json 不該等 keychain 寫完才發現
	if err != nil {
		return err
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	acc, err := youtubeprov.NewClient(hc, youtubeAPIBaseSeed, h, cfg.Language).AccountInfo(ctx)
	if err != nil {
		return i18n.Errorf("auth.youtube.err.rejected", "err", err)
	}
	if confirm != nil {
		if err := confirm(acc.Name, acc.Handle); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(errw, i18n.T("auth.youtube.account_detected", "account", youtubeAccountLabel(acc.Name, acc.Handle)))
	}
	if err := ytauth.Save(h); err != nil {
		return err
	}
	cfg.YouTube = &config.YouTubeAccount{Name: acc.Name, Handle: acc.Handle, ChannelID: acc.ChannelID}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintln(out, i18n.T("auth.youtube.done", "account", youtubeAccountLabel(acc.Name, acc.Handle)))
	return nil
}

// youtubeLogout:刪 cookie、清 config 的帳號(不然 auth status 還顯示登入的是誰);快取的清單列表由呼叫端的 cache.Forget 清。
func youtubeLogout(cmd *cobra.Command) error {
	if err := ytauth.Delete(); err != nil && !errors.Is(err, secret.ErrNotFound) {
		return err
	}
	switch cfg, err := config.Load(); {
	case err != nil: // keychain 的鍵已經刪了,不讓整個命令失敗,但要講
		fmt.Fprintln(cmd.ErrOrStderr(), i18n.T("auth.logout.youtube_account_not_cleared", "err", err))
	case cfg.YouTube != nil:
		cfg.YouTube = nil
		if err := config.Save(cfg); err != nil {
			return err
		}
	}
	return nil
}

// youtubeStatusText:auth status 的 youtube: 段(文字版;JSON 版在 authStatusOf)。
func youtubeStatusText(w io.Writer, cfg *config.Config) {
	fmt.Fprintln(w, "youtube:")
	switch _, err := ytauth.Load(); {
	case err == nil:
		fmt.Fprintln(w, "  cookie: "+i18n.T("auth.status.in_keychain"))
	case errors.Is(err, secret.ErrNotFound):
		fmt.Fprintln(w, "  cookie: "+i18n.T("auth.status.token_missing", "provider", "youtube"))
	default:
		fmt.Fprintln(w, "  cookie: "+i18n.T("auth.status.keychain_read_failed", "err", err))
	}
	if cfg.YouTube != nil {
		fmt.Fprintln(w, "  account: "+youtubeAccountLabel(cfg.YouTube.Name, cfg.YouTube.Handle))
	}
}

func checkYouTubeCookie(ctx context.Context) (string, error) {
	switch _, err := ytauth.Load(); {
	case err == nil:
		return i18n.T("doctor.in_keychain"), nil
	case errors.Is(err, secret.ErrNotFound):
		return "", i18n.Errorf("doctor.youtube.cookie.err.missing")
	default:
		return "", i18n.Errorf("doctor.apple.err.keychain_read", "err", err)
	}
}

func checkYouTubeAccount(ctx context.Context) (string, error) {
	cfg, err := config.Load()
	if err != nil || cfg.YouTube == nil || cfg.YouTube.ChannelID == "" {
		return "", i18n.Errorf("doctor.youtube.account.err.unset")
	}
	return youtubeAccountLabel(cfg.YouTube.Name, cfg.YouTube.Handle), nil
}

// checkYouTubeAPI:account_menu 打得通,而且回來的帳號跟 config 記的是同一個(cookie 被換成別的帳號的、config 卻沒跟上)。
func checkYouTubeAPI(ctx context.Context) (string, error) {
	p, err := newProvider(ctx, "youtube")
	if err != nil {
		return "", err
	}
	yp, ok := p.(*youtubeprov.Provider)
	if !ok {
		return "", i18n.Errorf("doctor.api.err.failed", "err", "not a youtube provider")
	}
	acc, err := yp.AccountInfo(ctx)
	if err != nil {
		return "", i18n.Errorf("doctor.api.err.failed", "err", friendlyErr("youtube", err))
	}
	if acc.ChannelID != yp.ChannelID() {
		return "", i18n.Errorf("doctor.youtube.err.account_mismatch", "got", acc.Handle, "want", yp.ChannelID())
	}
	return i18n.T("doctor.api.ok.youtube", "handle", acc.Handle), nil
}

func youtubeChecks() []check {
	return []check{
		{"YouTube cookie", checkYouTubeCookie},
		{"YouTube account", checkYouTubeAccount},
		{"YouTube Music API", checkYouTubeAPI},
	}
}
