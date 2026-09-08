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
		return nil, fmt.Errorf("%w(逃生口只讀不改:這個檔原封不動)%s", err, retainedHint())
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

var errNothingLocal = errors.New("本機沒有任何 canonical 資料(沒有 state.db 或它是空的)")

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
	return fmt.Sprintf(";另外找到升版時保留的舊版快取 %s——要從它復原,請用當時版本的 capy binary 跑 capy export", strings.Join(kept, "、"))
}

func newExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export",
		Short: "把本機 canonical 資料輸出到 stdout(Drive 檔的合併形式;逃生口,不依賴 Drive)",
		Long: `只讀本機 state.db,不碰 Drive、網路、keychain。輸出一份 JSON:鍵是 Drive 上的檔名(manifest.json、tracks.json、
pl__<pid>.json、dev__<device_id>.json),值就是該檔的內容,與 Drive 上逐位元同形。TTY 與非 TTY 同一份輸出。
本機沒有資料時 exit 1、stdout 不印東西(capy export > backup.json 不會靜默寫出空殼)。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			files, err := localFiles()
			if err != nil {
				return err
			}
			if len(files) == 0 {
				return fmt.Errorf("%w:沒有東西可以匯出;先 capy pl pull%s", errNothingLocal, retainedHint())
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
	cmd := &cobra.Command{Use: "drive", Short: "Google Drive appdata 的維護命令"}
	cmd.AddCommand(newDriveInitCmd())
	return cmd
}

func newDriveInitCmd() *cobra.Command {
	var fromLocal, dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "init --from-local",
		Short: "Drive appdata 空或部分遺失時,用本機 state.db 補回缺的檔(那個狀態下唯一允許寫入的命令)",
		Long: `pl pull 以 exit 3 擋下「Drive 不完整」之後的出口。只建 Drive 缺的檔、不覆寫既有檔(Drive 上還在的以 Drive 為準)、
不動本機 cache;別台裝置的 dev__ 檔不代為上傳(每台裝置只寫自己的檔,那台下次 pull 會當作沒有 base、只加不刪)。
先列出要建的檔(非 TTY 是無標題 TSV:action file),確認後才上傳;exit 0 完成或沒缺、1 錯誤、2 待套用(--dry-run、非 TTY 沒 --yes、取消)。
確認訊息會帶目前登入的 Google 帳號:登錯帳號時這個命令會把整個曲庫傳到別人的 appdata。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !fromLocal {
				return errors.New("目前只有 --from-local(從本機 state.db 補回 Drive 缺的檔)")
			}
			ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
			unlock, err := auth.LockFile(ctx, "pull.lock", "對方正在同步播放清單")
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
				return fmt.Errorf("%w:沒有東西可以補回 Drive%s", errNothingLocal, retainedHint())
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
				fmt.Fprintf(stderr, "不代為上傳別台裝置的檔(每台裝置只寫自己的,spec §6.3):%s;那台下次 pull 會當作沒有 base,只加不刪\n", strings.Join(skipped, "、"))
			}
			if lost, err := inspectDrive(ctx, dc, present, have, local); err != nil {
				return err
			} else if len(lost) > 0 {
				return fmt.Errorf("Drive 的 manifest 宣告了 %s,但 Drive 與本機都沒有這些檔:補不回,pl pull 會繼續 exit 3。那些清單已經遺失(兩邊都沒有);"+
					"唯一的出路是到 Google 帳號設定「管理應用程式 → 刪除隱藏的應用程式資料」清空 appdata,再跑一次 capy drive init --from-local(manifest 會從本機重建、不含它們)", strings.Join(lost, "、"))
			}
			if len(rows) == 0 {
				msg := "Drive 已有本機記得的全部檔案,不需要補"
				if len(skipped) > 0 {
					msg += fmt.Sprintf(";另有 %d 個別台裝置的檔按設計不代傳", len(skipped))
				}
				fmt.Fprintln(stderr, msg)
				return nil
			}
			ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), []string{"ACTION", "FILE"}, rows)
			if dryRun {
				return &PendingError{N: len(rows)}
			}
			if !yes {
				if !bothTTY(cmd) {
					return &PendingError{N: len(rows)}
				}
				ok, err := confirmWrite(fmt.Sprintf("把以上 %d 個檔上傳到 %s 的 Drive appdata?(登錯帳號會把曲庫傳到別人的空間)", len(rows), googleAccount()))
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
					return fmt.Errorf("不認得的檔名 %s,不上傳", r[1])
				}
				if _, err := dc.Create(ctx, ref.Name, ref.Props, local[ref.Name]); err != nil {
					return fmt.Errorf("上傳 %s 失敗(已建的檔留著,重跑會接著補):%w", ref.Name, friendlyErr("google", err))
				}
			}
			fmt.Fprintf(stderr, "已補回 %d 個檔到 %s 的 Drive appdata;接著 capy pl pull --all\n", len(rows), googleAccount()) // --yes 也要看得到目標帳號:登錯帳號是這個閘存在的理由
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromLocal, "from-local", false, "從本機 state.db 補回 Drive 缺的檔(目前唯一的模式)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只列出要建的檔,不上傳(有東西要建時 exit 2)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳過確認")
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
			return nil, fmt.Errorf("下載 %s:%w", name, friendlyErr("google", err))
		}
		if err := canon.CheckSchema(b); err != nil {
			return nil, fmt.Errorf("Drive 上的 %s:%w", name, err)
		}
		if name == canon.ManifestFile().Name {
			if manifest, err = canon.Decode[canon.Manifest](b); err != nil {
				return nil, fmt.Errorf("讀取 Drive 的 manifest.json:%w", err)
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
