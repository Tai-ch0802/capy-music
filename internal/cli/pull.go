package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
	"github.com/Tai-ch0802/capy-music/internal/ui"
	"github.com/Tai-ch0802/capy-music/internal/ulid"
)

// P3 T8:pl link / unlink / pull(spec §6.1、§6.5、§6.6;計畫 T8)。三個命令共用 withCanonical 的骨架:
// 鎖 → FETCH → 閘 → 改狀態 → COMMIT(Drive 先、SQLite 後),寫入以位元組差異判定。

// ---- exit code(對外契約,README 有表;main.go 只呼叫 ExitCode)----

// PendingError:有待套用的變更但這次沒套用(--dry-run、非 TTY 沒 --yes、TTY 取消)→ exit 2。變更集已印在 stdout。
type PendingError struct{ N int }

func (e *PendingError) Error() string {
	return fmt.Sprintf("%d 筆變更待套用:加 --yes 套用,或在終端機執行以確認", e.N)
}

// BlockedError:安全閥擋下(Drive 不完整、刪除超過閾值)→ exit 3,零寫入。
type BlockedError struct{ Msg string }

func (e *BlockedError) Error() string { return e.Msg }

// ExitCode 把 Execute 的錯誤對到 exit code 與要印到 stderr 的訊息:0 成功(含無變更、已套用)、1 錯誤、
// 2 需要人介入(歧義、待套用變更)、3 安全閥擋下。「這次動了幾筆」由 stdout 的 TSV 行數判斷,不佔 exit code。
func ExitCode(err error) (int, string) {
	var amb *AmbiguousError
	var pend *PendingError
	var rev *ReviewNeedsTTYError
	var blk *BlockedError
	switch {
	case err == nil:
		return 0, ""
	case errors.As(err, &amb), errors.As(err, &pend), errors.As(err, &rev):
		return 2, err.Error()
	case errors.As(err, &blk):
		return 3, err.Error()
	}
	return 1, "Error: " + err.Error()
}

// ---- 共用骨架 ----

const pullBusy = 5 * time.Second // 等別的 capy 放 db 鎖的上限;canonical 路徑可以比補全長

// canonState 是 FETCH 下來的整個 Drive appdata,fn 直接改它;COMMIT 只上傳位元組有變的檔。
type canonState struct {
	deviceID  string
	manifest  *canon.Manifest
	tracks    *canon.Tracks
	playlists map[string]*canon.Playlist    // pid → 清單
	devices   map[string]*canon.DeviceState // device_id → 裝置檔(含自己)
	fetched   map[string]fetchedFile        // 檔名 → Drive 上那份與位元組;沒有 = COMMIT 要 Create
	stderr    io.Writer
}

type fetchedFile struct {
	file drive.File
	body []byte
}

func (s *canonState) mine() *canon.DeviceState {
	if d, ok := s.devices[s.deviceID]; ok {
		return d
	}
	d := canon.NewDeviceState(s.deviceID)
	s.devices[s.deviceID] = d
	return d
}

// find:pid 直接命中,否則名稱不分大小寫精確比對——唯一即用,同名多個要求改用 pid,沒有回 nil。
func (s *canonState) find(arg string) (*canon.Playlist, error) {
	if pl, ok := s.playlists[arg]; ok {
		return pl, nil
	}
	var hits []*canon.Playlist
	for _, pid := range slices.Sorted(maps.Keys(s.playlists)) {
		if strings.EqualFold(s.playlists[pid].Name, arg) {
			hits = append(hits, s.playlists[pid])
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return nil, nil
	}
	pids := make([]string, len(hits))
	for i, h := range hits {
		pids[i] = h.PID
	}
	return nil, fmt.Errorf("有 %d 個同名的 canonical 清單,請改用 pid:%s", len(hits), strings.Join(pids, "、"))
}

// errSkipCommit:fn 用它表示「成功,但這次不要 COMMIT」(--dry-run 永不寫入,連 bootstrap 檔都不建)。
var errSkipCommit = errors.New("skip commit")

// withCanonical:鎖 → FETCH → 閘(Drive 不完整 / schema 太新 → 零寫入)→ fn 改狀態 → COMMIT。fn 回錯就什麼都不寫。
// pull.lock 包住整段,兩個 capy 同時 pull 不會互相蓋掉對方剛上傳的檔;token 鎖在它裡面,順序固定不會死鎖。
func withCanonical(ctx context.Context, stderr io.Writer, fn func(*canonState) error) error {
	unlock, err := auth.LockFile(ctx, "pull.lock", "對方正在同步播放清單")
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.DeviceID == "" { // device_id 在 auth login google 出生
		return errNotLoggedInGoogle
	}
	dc, err := newDriveClient(ctx)
	if err != nil {
		return err
	}
	st, err := store.Open(pullBusy)
	if err != nil {
		return err
	}
	defer st.Close()
	s, err := fetchCanonical(ctx, dc, st, cfg.DeviceID, stderr)
	if err != nil {
		return err
	}
	if err := fn(s); errors.Is(err, errSkipCommit) {
		return nil
	} else if err != nil {
		return err
	}
	return commitCanonical(ctx, dc, st, s)
}

// fetchCanonical:一次 files.list 列全部、同名取最新(drive.Newest),下載 manifest / tracks / pl__* / dev__*。
// 閘:「manifest 宣告、或本機 db 記得,但 Drive 取不到」的檔 > 0 → BlockedError(exit 3),不會被讀成「使用者刪光了」;
// Drive 全空且本機也空才是 bootstrap。任何檔 schema 太新 → 一般錯誤(exit 1),同樣零寫入。
func fetchCanonical(ctx context.Context, dc *drive.Client, st *store.Store, deviceID string, stderr io.Writer) (*canonState, error) {
	files, err := dc.List(ctx, "")
	if err != nil {
		return nil, friendlyErr("google", err)
	}
	byName := map[string][]drive.File{}
	for _, f := range files {
		byName[f.Name] = append(byName[f.Name], f)
	}
	s := &canonState{deviceID: deviceID, playlists: map[string]*canon.Playlist{}, devices: map[string]*canon.DeviceState{}, fetched: map[string]fetchedFile{}, stderr: stderr}
	get := func(name string) ([]byte, bool, error) {
		fs := byName[name]
		if len(fs) == 0 {
			return nil, false, nil
		}
		f := drive.Newest(fs)
		if len(fs) > 1 {
			fmt.Fprintf(stderr, "警告:Drive appdata 有 %d 份 %s,採用最新的一份\n", len(fs), name)
		}
		b, err := dc.Download(ctx, f.ID)
		if err != nil {
			return nil, false, fmt.Errorf("下載 %s:%w", name, friendlyErr("google", err))
		}
		s.fetched[name] = fetchedFile{f, b}
		return b, true, nil
	}
	decodeErr := func(name string, err error) error { return fmt.Errorf("讀取 Drive 的 %s:%w", name, err) }
	s.manifest = canon.NewManifest()
	if b, ok, err := get("manifest.json"); err != nil {
		return nil, err
	} else if ok {
		if s.manifest, err = canon.Decode[canon.Manifest](b); err != nil {
			return nil, decodeErr("manifest.json", err)
		}
	}
	s.tracks = canon.NewTracks()
	if b, ok, err := get("tracks.json"); err != nil {
		return nil, err
	} else if ok {
		if s.tracks, err = canon.Decode[canon.Tracks](b); err != nil {
			return nil, decodeErr("tracks.json", err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		switch {
		case strings.HasPrefix(name, "pl__") && strings.HasSuffix(name, ".json"):
			b, _, err := get(name)
			if err != nil {
				return nil, err
			}
			pl, err := canon.Decode[canon.Playlist](b)
			if err != nil {
				return nil, decodeErr(name, err)
			}
			s.playlists[pl.PID] = pl
		case strings.HasPrefix(name, "dev__") && strings.HasSuffix(name, ".json"):
			b, _, err := get(name)
			if err != nil {
				return nil, err
			}
			d, err := canon.Decode[canon.DeviceState](b)
			if err != nil {
				return nil, decodeErr(name, err)
			}
			s.devices[d.DeviceID] = d
		}
	}
	// 合併墓碑的自癒(決策 21):COMMIT 途中斷掉會留下「tracks.json 已合併、清單還指著敗者 cid」,這裡把 item 改指勝者;
	// 位元組變了,COMMIT 自然重傳(無變更的 pull 也會走 COMMIT)。多重歸屬的警告也在這裡印一次。
	id := canon.NewIdentity(s.tracks.Tracks, s.tracks.Merged)
	for _, w := range id.Warnings() {
		fmt.Fprintln(stderr, "警告:tracks.json "+w)
	}
	healed := 0
	var healedNames []string // 自癒會動到所有受影響的清單(不只這次的目標),名字要列出來
	for _, pid := range slices.Sorted(maps.Keys(s.playlists)) {
		if n := id.RedirectItems(s.playlists[pid]); n > 0 {
			healed += n
			healedNames = append(healedNames, s.playlists[pid].Name)
		}
	}
	if healed > 0 { // 這裡還不知道會不會寫入(--dry-run / 閾值 / 取消都不會),只講事實、不承諾這次上傳
		fmt.Fprintf(stderr, "修復 %d 筆清單項目的合併殘留(%s;上次寫入中斷:tracks.json 已合併、清單還指著舊 cid),下次寫入時一併上傳\n", healed, strings.Join(healedNames, "、"))
	}
	// 閘的兩個證人:manifest 宣告的 pid、本機 db 記得的 pid(db 壞或空就沒有第二個證人,不算錯)。
	known := slices.Clone(s.manifest.Playlists)
	if local, err := st.Dump(); err == nil {
		for _, pl := range local.Playlists {
			known = append(known, pl.PID)
		}
	}
	slices.Sort(known)
	known = slices.Compact(known)
	var missing []string
	for _, pid := range known {
		if _, ok := s.playlists[pid]; !ok {
			missing = append(missing, canon.PlaylistFile(pid).Name)
		}
	}
	if len(known) > 0 {
		for _, name := range []string{"manifest.json", "tracks.json"} {
			if _, ok := s.fetched[name]; !ok {
				missing = append(missing, name)
			}
		}
	}
	if len(missing) > 0 {
		return nil, &BlockedError{Msg: fmt.Sprintf("Drive appdata 不完整,取不到:%s。這不會被當成「使用者刪光了」,零寫入;--yes / --force 都不放行。"+
			"若 Drive 真的被清空或登錯 Google 帳號(目前 %s),出口是 capy drive init --from-local(只補 Drive 缺的檔;本機 state.db 是唯一剩下的一份,別刪)", strings.Join(missing, "、"), googleAccount())}
	}
	return s, nil
}

func googleAccount() string {
	if cfg, err := config.Load(); err == nil && cfg.GoogleEmail != "" {
		return cfg.GoogleEmail
	}
	return "未知帳號"
}

// commitCanonical:Drive 先(tracks → 清單 → 自己的 dev 檔 → manifest 最後,它宣告的檔要先存在)、SQLite 後。
// 只上傳位元組有變的檔;manifest 的裝置註冊只在「新裝置」或「本來就要寫東西」時 touch,否則 last_seen 每次都動
// 等於每次都上傳。上傳失敗就不寫 cache,下次 pull 重算;cache 寫失敗只警告(Drive 才是 source of truth)。
func commitCanonical(ctx context.Context, dc *drive.Client, st *store.Store, s *canonState) error {
	type upload struct {
		ref  canon.FileRef
		body []byte
	}
	var ups []upload
	stage := func(ref canon.FileRef, v any) error {
		b, err := canon.Encode(v)
		if err != nil {
			return err
		}
		if f, ok := s.fetched[ref.Name]; ok && bytes.Equal(f.body, b) {
			return nil
		}
		ups = append(ups, upload{ref, b})
		return nil
	}
	if err := stage(canon.TracksFile(), s.tracks); err != nil {
		return err
	}
	pids := slices.Sorted(maps.Keys(s.playlists))
	for _, pid := range pids {
		if err := stage(canon.PlaylistFile(pid), s.playlists[pid]); err != nil {
			return err
		}
	}
	if err := stage(canon.DeviceFile(s.deviceID), s.mine()); err != nil {
		return err
	}
	for _, pid := range pids {
		s.manifest.AddPlaylist(pid)
	}
	registered := slices.ContainsFunc(s.manifest.Devices, func(d canon.Device) bool { return d.ID == s.deviceID })
	mb, err := canon.Encode(s.manifest)
	if err != nil {
		return err
	}
	if f, ok := s.fetched["manifest.json"]; !registered || !ok || !bytes.Equal(f.body, mb) || len(ups) > 0 {
		s.manifest.Touch(s.deviceID, hostname())
		if err := stage(canon.ManifestFile(), s.manifest); err != nil {
			return err
		}
	}
	for _, u := range ups {
		var err error
		if f, ok := s.fetched[u.ref.Name]; ok {
			_, err = dc.Update(ctx, f.file.ID, u.ref.Props, u.body)
		} else {
			_, err = dc.Create(ctx, u.ref.Name, u.ref.Props, u.body)
		}
		if err != nil {
			return fmt.Errorf("上傳 %s 失敗(本機快取未動,下次 pull 重算):%w", u.ref.Name, friendlyErr("google", err))
		}
	}
	c := store.Canonical{Manifest: s.manifest, Tracks: s.tracks}
	for _, pid := range pids {
		c.Playlists = append(c.Playlists, *s.playlists[pid])
	}
	for _, id := range slices.Sorted(maps.Keys(s.devices)) {
		c.Devices = append(c.Devices, *s.devices[id])
	}
	if err := st.Hydrate(c); err != nil {
		fmt.Fprintf(s.stderr, "警告:Drive 已更新,但本機快取寫入失敗(下次 pull 會重建):%v\n", err)
	}
	return nil
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

// splitProviderRef:<provider>:<清單 ID 或名稱>。
func splitProviderRef(arg string) (prov, ref string, err error) {
	prov, ref, ok := strings.Cut(arg, ":")
	if !ok || !isProviderID(prov) || ref == "" {
		return "", "", fmt.Errorf("格式是 <provider>:<清單 ID 或名稱>,provider 為 %s:%q", strings.Join(providerIDs, "|"), arg)
	}
	return prov, ref, nil
}

// ---- pl link / unlink ----

func newPlLinkCmd() *cobra.Command {
	return &cobra.Command{
		Use: "link <name|pid> <provider>:<playlist ID|名稱>", Short: "把 canonical 清單連結到平台清單(不存在就建立;只認明確 link)", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			prov, ref, err := splitProviderRef(args[1])
			if err != nil {
				return err
			}
			p, err := newProvider(ctx, prov)
			if err != nil {
				return err
			}
			r, err := asPlaylistReader(p)
			if err != nil {
				return err
			}
			id, err := resolvePlaylistID(ctx, r, prov, ref)
			if err != nil {
				return err
			}
			// 「存在」的定義要跟 pull 的 gone 判準一致(在 ListPlaylists 裡):像 base62 的 ID 會被 resolvePlaylistID 直接放行,
			// 別人的公開清單讀得到卻不在自己的列表裡,連了第一次 pull 就會被當成已刪除而自動 unlink。
			refs, err := r.ListPlaylists(ctx)
			if err != nil {
				return friendlyErr(prov, err)
			}
			if !slices.ContainsFunc(refs, func(x provider.PlaylistRef) bool { return x.ID == id }) {
				return fmt.Errorf("%s:%s 不在你的清單列表裡(capy pl list 看得到的才算):pull 會把不在列表裡的清單視為已刪除並自動取消連結,所以不可連結", prov, id)
			}
			// 讀不到的清單(Spotify 編輯清單、他人清單,spec §1.1)不可連結:pull 永遠會跳過它,連了只是騙自己。
			// 404 放行:Apple 的 library 端點對空清單回 404,「新建空清單 → link → 再放歌」是正常起手式。
			if _, err := r.GetPlaylistItems(ctx, id); errors.Is(err, provider.ErrRestricted) {
				return fmt.Errorf("%s 清單 %s 讀不到內容(開發模式 app 拿不到 Spotify 官方 / 他人的清單),不可連結", prov, id)
			} else if err != nil && !errors.Is(err, provider.ErrNotFound) {
				return friendlyErr(prov, err)
			}
			return withCanonical(ctx, cmd.ErrOrStderr(), func(s *canonState) error {
				pl, err := s.find(args[0])
				if err != nil {
					return err
				}
				for _, pid := range slices.Sorted(maps.Keys(s.playlists)) {
					if other := s.playlists[pid]; other.Links[prov] == id && (pl == nil || other.PID != pl.PID) {
						return fmt.Errorf("%s:%s 已連結到 canonical 清單 %s(%s),一個平台清單只能連一個", prov, id, other.Name, other.PID)
					}
				}
				if pl == nil {
					if _, err := ulid.Time(args[0]); err == nil && strings.ToUpper(args[0]) == args[0] { // 合法 ULID 卻不存在:別把它當名字建清單
						return fmt.Errorf("找不到 pid %s 的 canonical 清單", args[0])
					}
					pl = canon.NewPlaylist(args[0])
					s.playlists[pl.PID] = pl
					fmt.Fprintf(cmd.ErrOrStderr(), "建立 canonical 清單 %s(%s)\n", pl.Name, pl.PID)
				}
				if cur, ok := pl.Links[prov]; ok && cur != id {
					return fmt.Errorf("%s(%s)已連結 %s:%s,先 capy pl unlink %s %s", pl.Name, pl.PID, prov, cur, pl.Name, prov)
				}
				if pl.Links[prov] != id {
					pl.Links[prov] = id
					pl.UpdatedAt = canon.Now().Unix()
				}
				fmt.Fprintf(cmd.OutOrStdout(), "已連結 %s(%s)↔ %s:%s;接著 capy pl pull %s\n", pl.Name, pl.PID, prov, id, pl.Name)
				return nil
			})
		},
	}
}

func newPlUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use: "unlink <name|pid> <provider>", Short: "取消 canonical 清單與平台清單的連結(canonical 內容不動)", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isProviderID(args[1]) {
				return fmt.Errorf("provider 為 %s:%q", strings.Join(providerIDs, "|"), args[1])
			}
			return withCanonical(cmd.Context(), cmd.ErrOrStderr(), func(s *canonState) error {
				pl, err := s.find(args[0])
				if err != nil {
					return err
				}
				if pl == nil {
					return fmt.Errorf("找不到 canonical 清單 %q", args[0])
				}
				id, ok := pl.Links[args[1]]
				if !ok {
					return fmt.Errorf("%s(%s)沒有連結 %s", pl.Name, pl.PID, args[1])
				}
				delete(pl.Links, args[1])
				pl.UpdatedAt = canon.Now().Unix()
				fmt.Fprintf(cmd.OutOrStdout(), "已取消連結 %s(%s)↔ %s:%s\n", pl.Name, pl.PID, args[1], id)
				return nil
			})
		},
	}
}

// ---- pl pull ----

// 寫入 Drive 前的確認提示,pl pull 與 drive init 共用;測試替換點(同 confirmAppleDisclosure 慣例),
// 只在 stdin 與 stdout 都是 TTY 時才會被呼叫。
var confirmWrite = func(prompt string) (bool, error) {
	ok := false
	err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(prompt).Affirmative("套用").Negative("取消").Value(&ok),
	)).Run()
	return ok, err
}

// bothTTY:stdout 與 stdin 都是終端機才能問;echo | capy … 這種 stdin 是管線的不問,直接當待套用。
func bothTTY(cmd *cobra.Command) bool { return stdoutIsTTY(cmd) && ui.IsTTY(os.Stdin) }

// removalBlocked:刪除閾值(Q3 採 B,2026-09-08;附錄 C 決策 18):單一 (清單, provider) 要移除 >10 首,
// 或 >30% 且 >3 首。分母是「該 provider 可見的曲數」(DeriveResult.VisibleCount),不是 canonical 總曲數。
func removalBlocked(removes, visible int) bool {
	return removes > 10 || (removes > 3 && removes*10 > visible*3)
}

var pullHeader = []string{"ACTION", "PROVIDER", "PLAYLIST", "POS", "CID", "PROVIDER_ID", "TITLE", "ARTISTS", "REASON"}

func newPlPullCmd() *cobra.Command {
	var all, dryRun, yes, force bool
	var prov string
	cmd := &cobra.Command{
		Use:   "pull [name|pid]",
		Short: "平台 → canonical → Drive:列出變更、確認後才寫(spec §6.1)",
		Long: `平台 → canonical → Drive(spec §6.1、§6.5)。變更集先印出(非 TTY 是無標題 TSV:action provider playlist pos cid provider_id title artists reason),
確認後才寫入;寫入順序 Drive 先、SQLite 後。exit code:0 無變更或已套用、1 錯誤、2 有待套用變更(--dry-run、非 TTY 沒 --yes、取消)、
3 安全閥擋下(Drive 不完整;刪除 >10 首或 >30% 且 >3 首)。--yes 跳過確認、--force 才越過刪除閾值,兩者都不放行「Drive 不完整」。`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return errors.New("指定一個清單(名稱或 pid),或用 --all 拉全部已連結的清單")
			}
			if prov != "" && !isProviderID(prov) {
				return fmt.Errorf("provider 為 %s:%q", strings.Join(providerIDs, "|"), prov)
			}
			if force && all { // 安全閥一次只解除一個清單:整輪放行會連「使用者自己都還不知道被清空」的清單一起放掉
				return errors.New("--force 只能配單一清單(capy pl pull <name> --force),不能配 --all")
			}
			ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
			return withCanonical(ctx, stderr, func(s *canonState) error {
				targets, err := pullTargets(s, args, all, prov)
				if err != nil {
					return err
				}
				rows, blocked, err := observeAndDerive(ctx, s, targets, prov, stderr)
				if err != nil {
					return err
				}
				if len(rows) > 0 { // 零列時 TTY 也不印空表頭
					ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), pullHeader, rows)
				}
				if len(blocked) > 0 && !force { // 安全閥先於提示:被擋下的 derive 結果不落地,拿它算「尚未對應」會對不上
					return &BlockedError{Msg: strings.Join(blocked, ";") + "。加 --force 越過(先用 --dry-run 看清楚要刪什麼)"}
				}
				resolveHint(s, targets, prov, stderr) // pull 不做 resolve,只提示(決策 22);--provider 那輪只提示它
				if len(rows) == 0 {
					fmt.Fprintln(stderr, "無變更")
					if dryRun {
						return errSkipCommit
					}
					return nil // base / 裝置註冊仍可能要寫,COMMIT 以位元組差異決定
				}
				if dryRun {
					return &PendingError{N: len(rows)}
				}
				if !yes {
					if !bothTTY(cmd) {
						return &PendingError{N: len(rows)}
					}
					ok, err := confirmWrite(fmt.Sprintf("套用以上 %d 筆變更到 Drive?", len(rows)))
					if err != nil {
						return err
					}
					if !ok {
						return &PendingError{N: len(rows)}
					}
				}
				fmt.Fprintf(stderr, "已套用 %d 筆變更\n", len(rows))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "拉全部已連結的清單")
	cmd.Flags().StringVar(&prov, "provider", "", "只拉這個 provider 的連結(預設:清單連結的全部 provider)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只列出變更,不寫入(有變更時 exit 2)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳過確認(cron / 管線用);不放行 Drive 不完整")
	cmd.Flags().BoolVar(&force, "force", false, "越過刪除閾值(>10 首,或 >30% 且 >3 首);只能配單一清單、不能配 --all;不放行 Drive 不完整")
	return cmd
}

// pullTargets:--all = 所有有連結的清單(可用 --provider 篩),否則指定的那一個;依 (name, pid) 排序,輸出才決定性。
func pullTargets(s *canonState, args []string, all bool, prov string) ([]*canon.Playlist, error) {
	linked := func(pl *canon.Playlist) bool {
		if prov != "" {
			return pl.Links[prov] != ""
		}
		return len(pl.Links) > 0
	}
	var out []*canon.Playlist
	if all {
		for _, pid := range slices.Sorted(maps.Keys(s.playlists)) {
			if linked(s.playlists[pid]) {
				out = append(out, s.playlists[pid])
			}
		}
	} else {
		pl, err := s.find(args[0])
		if err != nil {
			return nil, err
		}
		if pl == nil {
			return nil, fmt.Errorf("找不到 canonical 清單 %q — 先 capy pl link %s <provider>:<清單>", args[0], args[0])
		}
		if !linked(pl) {
			return nil, fmt.Errorf("%s(%s)沒有連結任何平台清單 — 先 capy pl link", pl.Name, pl.PID)
		}
		out = append(out, pl)
	}
	slices.SortStableFunc(out, func(a, b *canon.Playlist) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.PID, b.PID)
	})
	return out, nil
}

// observeAndDerive:OBSERVE + DERIVE,直接改 s(fn 回錯時 withCanonical 不會 COMMIT,所以不必另外暫存)。
// gone 的訊號是「不在 ListPlaylists 的列表裡」而不是 items 回 404——Apple 的 library 端點對空清單也回 404(P2 遺留)。
func observeAndDerive(ctx context.Context, s *canonState, targets []*canon.Playlist, only string, stderr io.Writer) (rows [][]string, blocked []string, err error) {
	readers := map[string]provider.PlaylistReader{}
	listed := map[string]map[string]provider.PlaylistRef{}
	reader := func(prov string) (provider.PlaylistReader, map[string]provider.PlaylistRef, error) {
		if r, ok := readers[prov]; ok {
			return r, listed[prov], nil
		}
		p, err := newProvider(ctx, prov)
		if err != nil {
			return nil, nil, err
		}
		r, err := asPlaylistReader(p)
		if err != nil {
			return nil, nil, err
		}
		refs, err := r.ListPlaylists(ctx)
		if err != nil {
			return nil, nil, friendlyErr(prov, err)
		}
		m := map[string]provider.PlaylistRef{}
		for _, ref := range refs {
			m[ref.ID] = ref
		}
		readers[prov], listed[prov] = r, m
		return r, m, nil
	}
	var devs []canon.DeviceState
	for _, id := range slices.Sorted(maps.Keys(s.devices)) {
		devs = append(devs, *s.devices[id])
	}
	merged := canon.MergeBase(devs)
	for _, pl := range targets {
		for _, prov := range slices.Sorted(maps.Keys(pl.Links)) {
			if only != "" && prov != only {
				continue
			}
			r, refs, err := reader(prov)
			if err != nil {
				return nil, nil, err
			}
			link := pl.Links[prov]
			in := canon.DeriveInput{Provider: prov, Playlist: *pl, Tracks: s.tracks.Tracks, Merged: s.tracks.Merged}
			if b, ok := merged[pl.PID][prov]; ok && b.Snapshot.ID == link { // 別的平台清單留下的 base 不算數
				in.Base = &b.Snapshot
			}
			if ref, ok := refs[link]; ok {
				tracks, err := r.GetPlaylistItems(ctx, link)
				switch {
				case errors.Is(err, provider.ErrRestricted):
					fmt.Fprintf(stderr, "跳過 %s 的 %s:%s(開發模式 app 讀不到 Spotify 官方 / 他人的清單,不是清單消失)\n", pl.Name, prov, link)
					continue
				case errors.Is(err, provider.ErrNotFound):
					tracks = nil // 有列在清單列表裡卻 404 = 空清單(Apple 的 library 端點就這樣回);移除照常走 GATE 與閾值,不是 exit 1
				case err != nil:
					return nil, nil, fmt.Errorf("讀取 %s 的 %s:%s:%w", pl.Name, prov, link, friendlyErr(prov, err))
				}
				in.Live = &canon.Observed{ID: link, Name: ref.Name, Tracks: tracks}
			}
			res, err := canon.Derive(in)
			if err != nil {
				return nil, nil, err
			}
			removes := 0
			for _, ch := range res.Changes {
				if ch.Action == "remove" {
					removes++
				}
				pos := ""
				if ch.Action == "add" || ch.Action == "move" || ch.Action == "remove" {
					pos = strconv.Itoa(ch.Pos)
				}
				rows = append(rows, []string{ch.Action, prov, pl.Name, pos, ch.CID, ch.ProviderID, ch.Title, strings.Join(ch.Artists, ", "), ch.Reason})
			}
			if removalBlocked(removes, res.VisibleCount) {
				blocked = append(blocked, fmt.Sprintf("%s 在 %s 要移除 %d 首(可見 %d 首),超過閾值", pl.Name, prov, removes, res.VisibleCount))
			}
			if res.Gone {
				fmt.Fprintf(stderr, "警告:%s 端找不到清單 %s,%s 將取消連結(Q6);canonical 內容不動\n", prov, link, pl.Name)
				delete(pl.Links, prov)
				delete(s.mine().Base[pl.PID], prov) // 自己這台的舊 base 一起清(別台的碰不到,靠 Snapshot.ID 比對擋)
				pl.UpdatedAt = canon.Now().Unix()
				continue
			}
			for cid, tr := range res.Tracks {
				if old, ok := s.tracks.Tracks[cid]; ok && len(tr.Conflicts) > len(old.Conflicts) {
					c := tr.Conflicts[len(tr.Conflicts)-1]
					fmt.Fprintf(stderr, "警告:ISRC 衝突 %s:%s 的 %s(%s)與既有 metadata 不符,已記進 tracks.json 的 conflicts(spec §6.2)\n", cid, c.Provider, c.Title, c.ProviderID)
				}
				s.tracks.Tracks[cid] = tr
			}
			*pl = res.Playlist
			if b, ok := merged[pl.PID][prov]; !ok || !snapshotEqual(b.Snapshot, res.Snapshot) {
				s.mine().SetBase(pl.PID, prov, res.Snapshot) // 只在 base 缺或變了才寫:observed_at 每次都動,無條件寫 = 每次都上傳 dev 檔
			}
		}
	}
	return rows, blocked, nil
}

func snapshotEqual(a, b canon.Snapshot) bool {
	return a.ID == b.ID && a.Name == b.Name && slices.Equal(a.Items, b.Items) && slices.Equal(a.CIDs, b.CIDs)
}
