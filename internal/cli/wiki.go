package cli

// 歌曲 wiki(決策 59;計畫 docs/superpowers/plans/2026-09-28-song-wiki.md)。這個檔是 wiki setup:接使用者自己的
// OpenAI 相容端點(base URL / model / 母語進 config.json,API key 與自訂標頭進 keychain)。capy wiki 本身在 wiki_run.go。

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/ai"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/secret"
)

// wikiProbeTimeout:GET /models 與 1-token chat 各自的上限。測試替換點。
var wikiProbeTimeout = 15 * time.Second

// wikiSetupInput:第一段表單的值。APIKey / HeadersText 留空 = 沿用 keychain 現值(HadKey / HadHeaders 讓表單把這件事說出來);
// 要清掉只能走旗標(--api-key "" / --header "")。
type wikiSetupInput struct {
	BaseURL     string
	APIKey      string
	HeadersText string
	Language    string
	HadKey      bool
	HadHeaders  bool
}

// wikiSetupForm:第一段精靈(huh)。接縫:web 換成提示橋(web_prompt.go 的 webWikiSetupForm),CLAUDE.md 硬約束。
var wikiSetupForm = runWikiSetupForm

// 三個驗證抽成具名函式:huh 的 Validate 與 web 提示橋共用,訊息一字不差。
func validateAIBaseURL(s string) error {
	_, err := ai.NormalizeBaseURL(s)
	return err
}

func validateAIHeaders(s string) error { return ai.ValidateHeadersText(s) }

func validateNativeLanguage(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil // 空 = 跟介面語系
	}
	_, _, err := ai.LanguageLabel(s)
	return err
}

func wikiGuide() string { return i18n.T("wiki.setup.guide") }

func wikiKeyLabel(had bool) string {
	if had {
		return i18n.T("wiki.setup.api_key_keep")
	}
	return i18n.T("wiki.setup.api_key")
}

func wikiHeadersLabel(had bool) string {
	if had {
		return i18n.T("wiki.setup.headers_keep")
	}
	return i18n.T("wiki.setup.headers")
}

func runWikiSetupForm(in wikiSetupInput) (wikiSetupInput, error) {
	out := in
	form := newForm(huh.NewGroup(
		huh.NewNote().Title(i18n.T("wiki.setup.title")).Description(wikiGuide()),
		huh.NewInput().Title(i18n.T("wiki.setup.base_url")).Placeholder("https://api.openai.com/v1").Value(&out.BaseURL).Validate(validateAIBaseURL),
		huh.NewInput().Title(wikiKeyLabel(in.HadKey)).EchoMode(huh.EchoModePassword).Value(&out.APIKey),
		// 純技術字面,刻意不走語系目錄(同 arc-like):標頭名加 xxx,沒有可翻的字。
		huh.NewText().Title(wikiHeadersLabel(in.HadHeaders)).Placeholder("CF-Access-Client-Id: xxx\nCF-Access-Client-Secret: xxx").Lines(3).Value(&out.HeadersText).Validate(validateAIHeaders),
		huh.NewInput().Title(i18n.T("wiki.setup.language")).Placeholder("ja").Value(&out.Language).Validate(validateNativeLanguage),
	))
	if err := form.Run(); err != nil {
		return in, err
	}
	return out, nil
}

func newWikiCmd() *cobra.Command {
	// 位置參數 = 歌名(Q82;cobra 先認子命令,所以 capy wiki setup 照舊是 setup,歌名剛好叫 setup 的用 --title)。
	cmd := &cobra.Command{Use: "wiki [title...]", Short: i18n.T("cmd.wiki.short"), Long: i18n.T("cmd.wiki.long"), Args: cobra.ArbitraryArgs, RunE: runWiki}
	cmd.Flags().String("title", "", i18n.T("cmd.wiki.flag.title"))
	cmd.Flags().String("artist", "", i18n.T("cmd.wiki.flag.artist"))
	cmd.Flags().Bool("refresh", false, i18n.T("cmd.wiki.flag.refresh"))
	cmd.Flags().Bool("json", false, i18n.T("cmd.wiki.flag.json"))
	providerFlag(cmd)
	cmd.AddCommand(newWikiSetupCmd())
	return cmd
}

func newWikiSetupCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "setup", Short: i18n.T("cmd.wiki.setup.short"), Args: cobra.NoArgs, RunE: runWikiSetup}
	cmd.Flags().String("base-url", "", i18n.T("cmd.wiki.setup.flag.base_url"))
	cmd.Flags().String("model", "", i18n.T("cmd.wiki.setup.flag.model"))
	cmd.Flags().String("api-key", "", i18n.T("cmd.wiki.setup.flag.api_key"))
	cmd.Flags().StringArray("header", nil, i18n.T("cmd.wiki.setup.flag.header")) // 刻意沒有短旗標:web 的 deny 只認 --header
	cmd.Flags().String("native-language", "", i18n.T("cmd.wiki.setup.flag.native_language"))
	return cmd
}

// runWikiSetup:第一段(端點 / 金鑰 / 標頭 / 母語)→ **先存** → 探測 GET /models → 第二段選 model → 存。
// 探測失敗時第一段的設定留著(使用者修好金鑰再跑一次就好),model 不動。
// 有任何旗標、或沒有終端機,第一段就不開精靈(旗標沒給的欄位沿用現值);model 的挑選只看有沒有終端機。
func runWikiSetup(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return i18n.Errorf("config.err.broken", "err", err)
	}
	curKey, curHeaders, err := ai.LoadSecrets()
	if err != nil {
		return i18n.Errorf("wiki.setup.err.keychain", "err", err)
	}
	in := wikiSetupInput{BaseURL: cfg.AIBaseURL, Language: cfg.NativeLanguage, HadKey: curKey != "", HadHeaders: curHeaders != ""}
	fl := cmd.Flags()
	flagged := fl.Changed("base-url") || fl.Changed("model") || fl.Changed("api-key") || fl.Changed("header") || fl.Changed("native-language")
	clearKey, clearHeaders := false, false
	if flagged || !stdinIsTTY() {
		if !fl.Changed("base-url") && cfg.AIBaseURL == "" {
			return i18n.Errorf("wiki.setup.err.need_flags")
		}
		if fl.Changed("base-url") {
			in.BaseURL, _ = fl.GetString("base-url")
		}
		if fl.Changed("api-key") { // --api-key "" = 清掉(精靈裡留空是沿用,分不出「清掉」)
			in.APIKey, _ = fl.GetString("api-key")
			clearKey = strings.TrimSpace(in.APIKey) == ""
		}
		if fl.Changed("header") {
			hs, _ := fl.GetStringArray("header")
			in.HeadersText = strings.Join(hs, "\n")
			clearHeaders = strings.TrimSpace(in.HeadersText) == ""
		}
		if fl.Changed("native-language") {
			in.Language, _ = fl.GetString("native-language")
		}
	} else if in, err = wikiSetupForm(in); err != nil {
		return err
	}
	base, err := ai.NormalizeBaseURL(in.BaseURL)
	if err != nil {
		return err
	}
	if err := ai.ValidateHeadersText(in.HeadersText); err != nil {
		return err
	}
	tag := ""
	if strings.TrimSpace(in.Language) != "" {
		if _, tag, err = ai.LanguageLabel(in.Language); err != nil {
			return err
		}
	}
	// 這一輪之後 keychain 裡會是哪一份標頭:新給的、清掉、或沿用。沿用的那份也要在存任何東西之前過得了解析——
	// 壞掉的舊值不能等到 ai_base_url 存好了才報錯(要修就給 --header 換掉它)。
	headersText := curHeaders
	switch {
	case clearHeaders:
		headersText = ""
	case strings.TrimSpace(in.HeadersText) != "":
		headersText = in.HeadersText
	}
	hdrs, err := ai.ParseHeaders(headersText)
	if err != nil {
		return err
	}
	cfg.AIBaseURL, cfg.NativeLanguage = base, tag
	if err := config.Save(cfg); err != nil {
		return i18n.Errorf("wiki.setup.err.save_config", "err", err)
	}
	stderr := cmd.ErrOrStderr()
	if ai.InsecureRemote(base) {
		fmt.Fprintln(stderr, i18n.T("config.warn.http", "url", base))
	}
	key := curKey
	switch {
	case clearKey:
		key = ""
		if err := secret.Delete(ai.KeyAPIKey); err != nil && !errors.Is(err, secret.ErrNotFound) {
			return i18n.Errorf("wiki.setup.err.save_secret", "err", err)
		}
	case strings.TrimSpace(in.APIKey) != "":
		key = strings.TrimSpace(in.APIKey)
		if err := secret.Set(ai.KeyAPIKey, key); err != nil {
			return i18n.Errorf("wiki.setup.err.save_secret", "err", err)
		}
	}
	switch {
	case clearHeaders:
		if err := secret.Delete(ai.KeyHeaders); err != nil && !errors.Is(err, secret.ErrNotFound) {
			return i18n.Errorf("wiki.setup.err.save_secret", "err", err)
		}
	case headersText != curHeaders:
		if err := secret.Set(ai.KeyHeaders, headersText); err != nil {
			return i18n.Errorf("wiki.setup.err.save_secret", "err", err)
		}
	}
	c := ai.Config{BaseURL: base, APIKey: key, Headers: hdrs}

	fmt.Fprintln(stderr, i18n.T("wiki.setup.probing", "url", base))
	modelFlag, _ := fl.GetString("model")
	model, err := wikiPickModel(cmd.Context(), c, strings.TrimSpace(modelFlag), stdinIsTTY())
	if err != nil {
		return err
	}
	cfg.AIModel = model
	if err := config.Save(cfg); err != nil {
		return i18n.Errorf("wiki.setup.err.save_config", "err", err)
	}
	keyState := i18n.T("wiki.setup.key.none")
	if key != "" {
		keyState = i18n.T("wiki.setup.key.saved")
	}
	hdrState := i18n.T("wiki.setup.headers.none")
	if n := len(hdrs); n > 0 {
		hdrState = i18n.T("wiki.setup.headers.count", "count", n)
	}
	lang := tag
	if lang == "" {
		ui, _ := configLanguage()
		lang = i18n.T("wiki.setup.language_default", "language", ui)
	}
	fmt.Fprintln(cmd.OutOrStdout(), i18n.T("wiki.setup.done", "url", base, "model", model, "language", lang, "key", keyState, "headers", hdrState))
	return nil
}

// wikiPickModel:探測 GET /models 再決定 model。
//   - 列得出來:旗標給的要在清單裡(清單是空的就照收);沒給就開挑選器(≤ MaxModelsForPicker,最後一項「手動輸入」),
//     太多就手打;沒有終端機 → 錯誤,清單印在錯誤裡給腳本挑。
//   - 404 / 405(端點沒實作):旗標或手打 model,再用 1-token chat 驗一次。
//   - 其他錯(401 / 403 / 網路):照實回,第一段的設定留著。
func wikiPickModel(ctx context.Context, c ai.Config, model string, canPick bool) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, wikiProbeTimeout)
	defer cancel()
	models, err := c.Models(pctx)
	switch {
	case err == nil:
		list := strings.Join(models, i18n.T("sep.list"))
		switch {
		case model != "":
			if len(models) > 0 && !slices.Contains(models, model) {
				return "", i18n.Errorf("wiki.setup.err.model_not_listed", "model", model, "models", list)
			}
			return model, nil
		case !canPick:
			return "", i18n.Errorf("wiki.setup.err.need_model", "models", list)
		case len(models) > 0 && len(models) <= ai.MaxModelsForPicker:
			labels := append(slices.Clone(models), i18n.T("wiki.setup.manual_model"))
			idx, err := pickOne(i18n.T("wiki.setup.pick_model"), labels)
			if err != nil {
				return "", err
			}
			if idx < len(models) {
				return models[idx], nil
			}
			return promptNewName(i18n.T("wiki.setup.type_model"))
		default:
			return promptNewName(i18n.T("wiki.setup.type_model_many", "count", len(models)))
		}
	case errors.Is(err, ai.ErrNoModelList):
		if model == "" {
			if !canPick {
				return "", i18n.Errorf("wiki.setup.err.need_model_no_list")
			}
			if model, err = promptNewName(i18n.T("wiki.setup.type_model_no_list")); err != nil {
				return "", err
			}
		}
		c.Model = model
		vctx, vcancel := context.WithTimeout(ctx, wikiProbeTimeout)
		defer vcancel()
		if err := c.Ping(vctx); err != nil {
			return "", i18n.Errorf("wiki.setup.err.ping", "model", model, "err", err)
		}
		return model, nil
	default:
		return "", i18n.Errorf("wiki.setup.err.probe", "err", err)
	}
}
