package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

const keyDefaultProvider = "default_provider"

// configKeys:config.json 的非機密欄位(機密只在 keychain)。settableKeys 之外的不可用 config set 改:
// spotify_client_id 換了 token 也要重換(走 auth login spotify),apple_storefront 由 auth login apple 從帳號取得。
// language 加在最後:config list 的非 TTY 輸出是一行一個 key,既有的行位置不動。
const (
	keyLocalRoot = "local_root" // P6:本機曲庫目錄;可用 config set 改
	keyLanguage  = "language"   // 決策 50:介面語系;可用 config set 改,網頁的語言選單改的也是它
)

var (
	configKeys   = []string{keyDefaultProvider, keyLocalRoot, "spotify_client_id", "apple_storefront", keyLanguage}
	settableKeys = []string{keyDefaultProvider, keyLocalRoot, keyLanguage}
)

func configGet(c *config.Config, key string) (string, error) {
	switch key {
	case keyDefaultProvider:
		return c.DefaultProvider, nil
	case "spotify_client_id":
		return c.SpotifyClientID, nil
	case "apple_storefront":
		return c.AppleStorefront, nil
	case keyLocalRoot:
		return c.LocalRoot, nil
	case keyLanguage:
		return c.Language, nil
	}
	return "", i18n.Errorf("config.err.unknown_key", "key", strconv.Quote(key), "keys", strings.Join(configKeys, i18n.T("sep.list")))
}

func configSet(c *config.Config, key, val string) error {
	switch key {
	case keyDefaultProvider:
		if !isProviderID(val) {
			return i18n.Errorf("config.err.bad_value", "key", key, "valid", strings.Join(providerIDs, i18n.T("sep.or")), "value", strconv.Quote(val))
		}
		c.DefaultProvider = val
		return nil
	case keyLanguage: // 大小寫與底線寬鬆(zh_tw),存正規化後的代碼
		lang, ok := i18n.Normalize(val)
		if !ok {
			return i18n.Errorf("config.err.bad_value", "key", key, "valid", strings.Join(i18n.Supported(), i18n.T("sep.or")), "value", strconv.Quote(val))
		}
		c.Language = lang
		return nil
	case "spotify_client_id":
		return i18n.Errorf("config.err.spotify_client_id")
	case "apple_storefront":
		return i18n.Errorf("config.err.apple_storefront")
	case keyLocalRoot: // 存絕對路徑;要是存在的目錄(手打錯路徑時當場知道,不是等到 pl list 才錯)
		abs, err := filepath.Abs(val)
		if err != nil {
			return err
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return i18n.Errorf("config.err.not_dir", "key", key, "path", abs)
		}
		c.LocalRoot = abs
		return nil
	}
	return i18n.Errorf("config.err.unknown_settable", "key", strconv.Quote(key), "keys", strings.Join(settableKeys, i18n.T("sep.list")))
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: i18n.T("cmd.config.short")}
	cmd.AddCommand(
		&cobra.Command{
			Use: "get <key>", Short: i18n.T("cmd.config.get.short"), Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := config.Load()
				if err != nil {
					return err
				}
				v, err := configGet(c, args[0])
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), v)
				return nil
			},
		},
		&cobra.Command{
			Use: "set <key> <value>", Short: i18n.T("cmd.config.set.short"), Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := config.Load()
				if err != nil {
					return i18n.Errorf("config.err.broken", "err", err)
				}
				if err := configSet(c, args[0], args[1]); err != nil {
					return err
				}
				if err := config.Save(c); err != nil {
					return err
				}
				resetDefaultProvider() // 同一個 process 內(測試、之後的 REPL)立刻生效
				if args[0] == keyLanguage {
					i18n.Set(c.Language) // 同上;網頁的語言選單跑的就是這個命令
				}
				v, _ := configGet(c, args[0]) // 印存下去的值:language 正規化過、local_root 是絕對路徑
				fmt.Fprintf(cmd.OutOrStdout(), "%s = %s\n", args[0], v)
				return nil
			},
		},
		&cobra.Command{
			Use: "list", Short: i18n.T("cmd.config.list.short"), Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := config.Load()
				if err != nil {
					return err
				}
				tty := stdoutIsTTY(cmd)
				rows := make([][]string, 0, len(configKeys))
				for _, k := range configKeys {
					v, _ := configGet(c, k)
					if tty {
						switch _, langOK := i18n.Normalize(v); {
						case k == keyDefaultProvider && v == "":
							v = i18n.T("config.list.unset", "default", "spotify")
						case k == keyDefaultProvider && !isProviderID(v):
							v += i18n.T("config.list.invalid", "default", "spotify", "valid", strings.Join(providerIDs, "|"))
						case k == keyLanguage && v == "":
							v = i18n.T("config.list.unset", "default", i18n.Default())
						case k == keyLanguage && !langOK:
							v += i18n.T("config.list.invalid", "default", i18n.Default(), "valid", strings.Join(i18n.Supported(), "|"))
						}
					}
					rows = append(rows, []string{k, v})
				}
				return ui.Table(cmd.OutOrStdout(), tty, []string{"KEY", "VALUE"}, rows)
			},
		},
	)
	return cmd
}

// configLanguage:config 的 language 正規化後的代碼。沒設、config 讀不到、值不認得都是 i18n.Default();
// 不認得時 bad 是那個值。config 壞掉不在這裡報:真正讀 config 的命令會報,--help 照樣能用。
func configLanguage() (lang, bad string) {
	c, err := config.Load()
	if err != nil || c.Language == "" {
		return i18n.Default(), ""
	}
	if l, ok := i18n.Normalize(c.Language); ok {
		return l, ""
	}
	return i18n.Default(), c.Language
}

// applyLanguage:建命令樹之前設語系(決策 50)——cobra 的 Short 與 flag 說明是建樹時寫進去的。
// Execute 與網頁每個 job 各呼叫一次;值不認得時回一句提示給呼叫端印到 stderr。
func applyLanguage() (warn string) {
	lang, bad := configLanguage()
	i18n.Set(lang)
	if bad == "" {
		return ""
	}
	return i18n.T("config.language.unknown", "value", strconv.Quote(bad), "default", lang, "valid", strings.Join(i18n.Supported(), i18n.T("sep.list")))
}

// defaultProviderHint:登入成功後,尚未設預設平台就提示一行(不自動設)。
func defaultProviderHint(cmd *cobra.Command, id string) {
	if c, err := config.Load(); err == nil && c.DefaultProvider == "" {
		fmt.Fprintln(cmd.OutOrStdout(), i18n.T("config.hint.default_provider", "id", id))
	}
}
