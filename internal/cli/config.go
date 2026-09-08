package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

const keyDefaultProvider = "default_provider"

// configKeys:config.json 的非機密欄位(機密只在 keychain)。只有 default_provider 可用 config set 改:
// spotify_client_id 換了 token 也要重換(走 auth login spotify),apple_storefront 由 auth login apple 從帳號取得。
const keyLocalRoot = "local_root" // P6:本機曲庫目錄;可用 config set 改

var configKeys = []string{keyDefaultProvider, keyLocalRoot, "spotify_client_id", "apple_storefront"}

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
	}
	return "", fmt.Errorf("未知的設定 %q(可用:%s)", key, strings.Join(configKeys, "、"))
}

func configSet(c *config.Config, key, val string) error {
	switch key {
	case keyDefaultProvider:
		if !isProviderID(val) {
			return fmt.Errorf("%s 只能是 %s,得到 %q", key, strings.Join(providerIDs, " 或 "), val)
		}
		c.DefaultProvider = val
		return nil
	case "spotify_client_id":
		return errors.New("spotify_client_id 請用 capy auth login spotify 重設(client ID 換了,token 也要重新授權)")
	case "apple_storefront":
		return errors.New("apple_storefront 由 capy auth login apple 從你的帳號取得,不手動設")
	case keyLocalRoot: // 存絕對路徑;要是存在的目錄(手打錯路徑時當場知道,不是等到 pl list 才錯)
		abs, err := filepath.Abs(val)
		if err != nil {
			return err
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return fmt.Errorf("%s 不是存在的目錄:%s", key, abs)
		}
		c.LocalRoot = abs
		return nil
	}
	return fmt.Errorf("未知的設定 %q(可設定:%s、%s)", key, keyDefaultProvider, keyLocalRoot)
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "讀寫非機密設定(config.json;憑證一律在 keychain)"}
	cmd.AddCommand(
		&cobra.Command{
			Use: "get <key>", Short: "印出設定值(未設印空行)", Args: cobra.ExactArgs(1),
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
			Use: "set <key> <value>", Short: "寫入設定值(可設:default_provider、local_root)", Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := config.Load()
				if err != nil {
					return fmt.Errorf("%w\n設定檔壞了無法就地修;若確定要放棄裡面的設定,刪掉該檔再重跑 config set 與 auth login", err)
				}
				if err := configSet(c, args[0], args[1]); err != nil {
					return err
				}
				if err := config.Save(c); err != nil {
					return err
				}
				resetDefaultProvider() // 同一個 process 內(測試、之後的 REPL)立刻生效
				fmt.Fprintf(cmd.OutOrStdout(), "%s = %s\n", args[0], args[1])
				return nil
			},
		},
		&cobra.Command{
			Use: "list", Short: "列出所有設定(非 TTY 為 key\tvalue)", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := config.Load()
				if err != nil {
					return err
				}
				tty := stdoutIsTTY(cmd)
				rows := make([][]string, 0, len(configKeys))
				for _, k := range configKeys {
					v, _ := configGet(c, k)
					if tty && k == keyDefaultProvider {
						switch {
						case v == "":
							v = "(未設,預設 spotify)"
						case !isProviderID(v):
							v += "(非法值,已忽略、視同 spotify;可用 " + strings.Join(providerIDs, "|") + ")"
						}
					}
					rows = append(rows, []string{k, v})
				}
				ui.Table(cmd.OutOrStdout(), tty, []string{"設定", "值"}, rows)
				return nil
			},
		},
	)
	return cmd
}

// defaultProviderHint:登入成功後,尚未設預設平台就提示一行(不自動設)。
func defaultProviderHint(cmd *cobra.Command, id string) {
	if c, err := config.Load(); err == nil && c.DefaultProvider == "" {
		fmt.Fprintf(cmd.OutOrStdout(), "提示:`capy config set default_provider %s` 可把 %s 設為預設平台(之後不必帶 --provider)\n", id, id)
	}
}
