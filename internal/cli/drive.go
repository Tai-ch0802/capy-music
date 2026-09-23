package cli

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/secret"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

var errNotLoggedInGoogle = i18n.Errorf("drive.err.not_logged_in")

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
			return auth.GoogleClient{}, "", i18n.Errorf("drive.err.read_client_secret", "err", err)
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
		Short: "List the files in Google Drive appdata (P3 acceptance)",
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
			return ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), []string{"ID", "NAME", "KIND", "PID", "DEVICE", "VER", "MODIFIED"}, rows)
		},
	}
	cmd.Flags().String("q", "", "the q parameter of files.list (e.g. appProperties has { key='kind' and value='pl' })")
	return cmd
}
