package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/store"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// P3 T9:兩個逃生口(spec §6.6、附錄 A)。
// - export:只讀本機 state.db,不碰 Drive、網路、keychain;輸出「Drive 檔的合併形式」——鍵是檔名、值是該檔內容,
//   與 Drive 上逐位元同形,不發明第三種 JSON 形狀(將來的 import 就是它的反向)。
// - drive init --from-local:Drive 空 / 部分遺失時唯一允許寫入的命令。只建 Drive 缺的檔、不覆寫既有檔、不動本機 cache;
//   別台裝置的 dev__ 檔不代為上傳(每台裝置只寫自己的檔,spec §6.3)。

// localFiles:本機 state.db 的 Dump 編成 Drive 檔(檔名 → 位元組,與 COMMIT 上傳的完全相同);沒有 db 回空 map。
// 走唯讀開法:逃生口不得有副作用——store.Open 對壞檔會直接刪、對版本不符會改名,而 Drive 空掉 + 本機 db 有點壞
// 正是最需要逃生口的組合,不能讓 export 把僅剩的一份毀掉;全新機器上也不該順手建出空 db。
func localFiles() (map[string][]byte, error) {
	st, err := store.OpenReadOnly(pullBusy)
	if errors.Is(err, store.ErrNoDB) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, i18n.Errorf("escape.err.open_local", "err", err, "hint", retainedHint())
	}
	defer st.Close()
	c, err := st.Dump()
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	if len(c.Playlists) == 0 && len(c.Devices) == 0 && len(c.Tracks.Tracks) == 0 && len(c.Manifest.Devices) == 0 {
		return out, nil
	}
	add := func(ref canon.FileRef, v any) error {
		b, err := canon.Encode(v)
		if err != nil {
			return err
		}
		out[ref.Name] = b
		return nil
	}
	if err := add(canon.ManifestFile(), c.Manifest); err != nil {
		return nil, err
	}
	if err := add(canon.TracksFile(), c.Tracks); err != nil {
		return nil, err
	}
	for i := range c.Playlists {
		if err := add(canon.PlaylistFile(c.Playlists[i].PID), &c.Playlists[i]); err != nil {
			return nil, err
		}
	}
	for i := range c.Devices {
		if err := add(canon.DeviceFile(c.Devices[i].DeviceID), &c.Devices[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// fileRefOf:由檔名還原 appProperties(Create 要帶),不是我們的檔名回 false。
func fileRefOf(name string) (canon.FileRef, bool) {
	switch {
	case name == canon.ManifestFile().Name:
		return canon.ManifestFile(), true
	case name == canon.TracksFile().Name:
		return canon.TracksFile(), true
	case strings.HasPrefix(name, "pl__") && strings.HasSuffix(name, ".json"):
		return canon.PlaylistFile(strings.TrimSuffix(strings.TrimPrefix(name, "pl__"), ".json")), true
	case strings.HasPrefix(name, "dev__") && strings.HasSuffix(name, ".json"):
		return canon.DeviceFile(strings.TrimSuffix(strings.TrimPrefix(name, "dev__"), ".json")), true
	}
	return canon.FileRef{}, false
}

var errNothingLocal = i18n.Errorf("escape.err.nothing_local")

// retainedHint:找得到升版時保留的舊版快取就講出來——三個命令互相指路、資料卻躺在旁邊沒人提,是最糟的體驗。
func retainedHint() string {
	p, err := store.Path()
	if err != nil {
		return ""
	}
	kept, _ := filepath.Glob(p + ".v*")
	if len(kept) == 0 {
		return ""
	}
	return i18n.T("escape.hint.retained", "paths", strings.Join(kept, i18n.T("sep.list")))
}

func newExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export",
		Short: i18n.T("cmd.export.short"),
		Long:  i18n.T("cmd.export.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			files, err := localFiles()
			if err != nil {
				return err
			}
			if len(files) == 0 {
				return i18n.Errorf("escape.err.nothing_to_export", "err", errNothingLocal, "hint", retainedHint())
			}
			raw := map[string]json.RawMessage{}
			for name, b := range files {
				raw[name] = bytes.TrimSpace(b)
			}
			b, err := json.MarshalIndent(raw, "", "  ") // map 鍵排序,輸出決定性
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(b))
			return nil
		},
	}
}

func newDriveCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "drive", Short: i18n.T("cmd.drive.short")}
	cmd.AddCommand(newDriveInitCmd())
	return cmd
}

func newDriveInitCmd() *cobra.Command {
	var fromLocal, dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "init --from-local",
		Short: i18n.T("cmd.drive.init.short"),
		Long:  i18n.T("cmd.drive.init.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !fromLocal {
				return i18n.Errorf("escape.err.from_local_only")
			}
			ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
			unlock, err := auth.LockFile(ctx, "pull.lock", i18n.T("escape.lock_notice"))
			if err != nil {
				return err
			}
			defer unlock()
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.DeviceID == "" {
				return errNotLoggedInGoogle
			}
			dc, err := newDriveClient(ctx)
			if err != nil {
				return err
			}
			local, err := localFiles()
			if err != nil {
				return err
			}
			if len(local) == 0 {
				return i18n.Errorf("escape.err.nothing_to_restore", "err", errNothingLocal, "hint", retainedHint())
			}
			present, err := dc.List(ctx, "")
			if err != nil {
				return friendlyErr("google", err)
			}
			have := map[string]bool{}
			for _, f := range present {
				have[f.Name] = true
			}
			mine := canon.DeviceFile(cfg.DeviceID).Name
			var rows [][]string
			var skipped []string
			for _, name := range slices.Sorted(maps.Keys(local)) {
				if have[name] {
					continue
				}
				if strings.HasPrefix(name, "dev__") && name != mine {
					skipped = append(skipped, name)
					continue
				}
				rows = append(rows, []string{"create", name})
			}
			// manifest 最後,理由同 commitCanonical:它宣告的檔要先存在,上傳中斷才不會自己製造「Drive 不完整」;列出順序 = 上傳順序。
			slices.SortStableFunc(rows, func(a, b []string) int {
				am, bm := a[1] == canon.ManifestFile().Name, b[1] == canon.ManifestFile().Name
				if am == bm {
					return 0
				}
				if am {
					return 1
				}
				return -1
			})
			if len(skipped) > 0 {
				fmt.Fprintln(stderr, i18n.T("escape.drive_init.skipped_devices", "count", len(skipped), "files", strings.Join(skipped, i18n.T("sep.list"))))
			}
			if lost, err := inspectDrive(ctx, dc, present, have, local); err != nil {
				return err
			} else if len(lost) > 0 {
				return i18n.Errorf("escape.err.lost_playlists", "count", len(lost), "files", strings.Join(lost, i18n.T("sep.list")))
			}
			if len(rows) == 0 {
				msg := i18n.T("escape.drive_init.nothing_missing")
				if len(skipped) > 0 {
					msg = i18n.T("escape.drive_init.nothing_missing_skipped", "count", len(skipped))
				}
				fmt.Fprintln(stderr, msg)
				return nil
			}
			if err := ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), []string{"ACTION", "FILE"}, rows, tableOpts(yes)...); err != nil {
				return err
			}
			if dryRun {
				return &PendingError{N: len(rows)}
			}
			if !yes {
				if !bothTTY(cmd) {
					return &PendingError{N: len(rows)}
				}
				ok, err := confirmWrite(i18n.T("escape.drive_init.confirm", "count", len(rows), "account", googleAccount()))
				if err != nil {
					return err
				}
				if !ok {
					return &PendingError{N: len(rows)}
				}
			}
			for _, r := range rows {
				ref, ok := fileRefOf(r[1])
				if !ok {
					return i18n.Errorf("escape.err.unknown_file", "name", r[1])
				}
				if _, err := dc.Create(ctx, ref.Name, ref.Props, local[ref.Name]); err != nil {
					return i18n.Errorf("escape.err.upload", "name", ref.Name, "err", friendlyErr("google", err))
				}
			}
			fmt.Fprintln(stderr, i18n.T("escape.drive_init.done", "count", len(rows), "account", googleAccount())) // --yes 也要看得到目標帳號:登錯帳號是這個閘存在的理由
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromLocal, "from-local", false, i18n.T("cmd.drive.init.flag.from_local"))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("cmd.drive.init.flag.dry_run"))
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, i18n.T("cmd.drive.init.flag.yes"))
	return cmd
}

// inspectDrive:Drive 上還在的每個我們的檔(同名取最新)都下載一次檢查 schema——任何檔比這個 capy 新就回錯、零寫入
// (spec §6.6 的硬規則;不然 manifest 不見時會把 v1 的 manifest 建到新版檔旁邊)。順便回 manifest 宣告但 Drive 與本機都沒有的清單檔。
func inspectDrive(ctx context.Context, dc *drive.Client, present []drive.File, have map[string]bool, local map[string][]byte) (lost []string, err error) {
	byName := map[string][]drive.File{}
	for _, f := range present {
		byName[f.Name] = append(byName[f.Name], f)
	}
	var manifest *canon.Manifest
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		if _, ours := fileRefOf(name); !ours {
			continue
		}
		f := drive.Newest(byName[name])
		b, err := dc.Download(ctx, f.ID)
		if err != nil {
			return nil, i18n.Errorf("escape.err.download", "name", name, "err", friendlyErr("google", err))
		}
		if err := canon.CheckSchema(b); err != nil {
			return nil, i18n.Errorf("escape.err.drive_file", "name", name, "err", err)
		}
		if name == canon.ManifestFile().Name {
			if manifest, err = canon.Decode[canon.Manifest](b); err != nil {
				return nil, i18n.Errorf("escape.err.read_manifest", "err", err)
			}
		}
	}
	if manifest == nil {
		return nil, nil
	}
	for _, pid := range manifest.Playlists {
		name := canon.PlaylistFile(pid).Name
		if _, ok := local[name]; !ok && !have[name] {
			lost = append(lost, name)
		}
	}
	return lost, nil
}
