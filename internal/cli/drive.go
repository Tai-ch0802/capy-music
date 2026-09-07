package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/secret"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

var errNotLoggedInGoogle = errors.New("尚未登入 Google — 先執行 capy auth login google")

// newDriveClient:測試替換點(對照 provider.go 的 newProvider),e2e 用 drivetest 假 Drive 接進來。
var newDriveClient = func(ctx context.Context) (*drive.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	gc, src, err := googleClientFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	if src == "" {
		return nil, errNotLoggedInGoogle
	}
	ts, err := auth.GoogleTokenSource(ctx, gc)
	if errors.Is(err, secret.ErrNotFound) {
		return nil, errNotLoggedInGoogle
	}
	if err != nil {
		return nil, err
	}
	hc := oauth2.NewClient(ctx, ts)
	hc.Timeout = 30 * time.Second
	return drive.New(hc, drive.DefaultOrigin), nil
}

// googleClientFromConfig:不看 flag/env 的 client 來源(config + keychain secret → builtin);來源為空表示沒有。
func googleClientFromConfig(cfg *config.Config) (auth.GoogleClient, googleClientSource, error) {
	if cfg.GoogleClientID != "" {
		sec, err := secret.Get(auth.KeyGoogleClientSecret)
		if err != nil && !errors.Is(err, secret.ErrNotFound) {
			return auth.GoogleClient{}, "", fmt.Errorf("讀取 keychain 的 google.client_secret:%w", err)
		}
		return auth.GoogleClient{ID: cfg.GoogleClientID, Secret: sec}, googleFromConfig, nil
	}
	if auth.BuiltinGoogleClientID != "" {
		return auth.GoogleClient{ID: auth.BuiltinGoogleClientID, Secret: auth.BuiltinGoogleClientSecret, Builtin: true}, googleFromBuiltin, nil
	}
	return auth.GoogleClient{}, "", nil
}

// newDebugDriveLsCmd:列出 appdata 裡的檔案,P3 驗收(G-3 / G-4 / G-5)用;非 TTY 印 TSV。
func newDebugDriveLsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "drive-ls",
		Short: "列出 Google Drive appdata 的檔案(P3 驗收用)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, _ := cmd.Flags().GetString("q")
			c, err := newDriveClient(cmd.Context())
			if err != nil {
				return err
			}
			fs, err := c.List(cmd.Context(), q)
			if err != nil {
				return friendlyErr("google", err)
			}
			rows := make([][]string, 0, len(fs))
			for _, f := range fs {
				rows = append(rows, []string{f.ID, f.Name, f.AppProperties["kind"], f.AppProperties["pid"], f.AppProperties["device_id"],
					strconv.FormatInt(f.Version, 10), f.ModifiedTime.Local().Format("2006-01-02 15:04:05")})
			}
			ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), []string{"ID", "NAME", "KIND", "PID", "DEVICE", "VER", "MODIFIED"}, rows)
			return nil
		},
	}
	cmd.Flags().String("q", "", "files.list 的 q(例:appProperties has { key='kind' and value='pl' })")
	return cmd
}
