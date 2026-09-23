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
	"syscall"
	"time"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/auth"
	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
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
	return i18n.T("changeset.pending", "count", e.N) // 網頁 console.js 看 exit 2 的訊息裡有沒有 --yes:每個語系都要留著它
}

// BlockedError:安全閥擋下(Drive 不完整、刪除超過閾值)→ exit 3,零寫入。
type BlockedError struct{ Msg string }

func (e *BlockedError) Error() string { return e.Msg }

// ExitCode 把 Execute 的錯誤對到 exit code 與要印到 stderr 的訊息:0 成功(含無變更、已套用)、1 錯誤、
// 2 需要人介入(歧義、待套用變更)、3 安全閥擋下、130 / 143 被 SIGINT / SIGTERM 結束(見 SignalError)。
// 「這次動了幾筆」由 stdout 的 TSV 行數判斷,不佔 exit code。
func ExitCode(err error) (int, string) {
	var amb *AmbiguousError
	var pend *PendingError
	var rev *ReviewNeedsTTYError
	var blk *BlockedError
	var sig *SignalError
	switch {
	case errors.As(err, &sig): // 只改結束碼:stderr 印的跟沒有訊號時一模一樣(回 nil 的不印)
		_, msg := ExitCode(sig.Err)
		if s, ok := sig.Sig.(syscall.Signal); ok { // 128+n 就是規則(SIGINT 130、SIGTERM 143),executeSignalled 多掛一個訊號也不用回來改
			return 128 + int(s), msg
		}
		return 130, msg // 到不了:signal.Notify 送來的在兩個平台上都是 syscall.Signal
	case err == nil:
		return 0, ""
	case errors.As(err, &amb), errors.As(err, &pend), errors.As(err, &rev):
		return 2, err.Error()
	case errors.As(err, &blk):
		return 3, err.Error()
	case errors.Is(err, ui.ErrInterrupted): // 檢視窗格 / 互動式介面 / now --watch 裡按了 Ctrl-C:同 SIGINT 的 130,不印東西
		return 130, ""
	}
	return 1, "Error: " + err.Error()
}

// tableOpts:--yes 的命令不開檢視窗格 —— README 說 --yes「只跳過確認」,它從頭到尾不該碰鍵盤,
// 而且變更集是握著 pull.lock 印的,窗格開多久鎖就握多久(PR #52 review)。
func tableOpts(yes bool) []ui.TableOption {
	if yes {
		return []ui.TableOption{ui.NoPager}
	}
	return nil
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
	file  drive.File
	body  []byte
	peers []idVer // FETCH 時同名檔全部的 (ID, Version),排序後比——同名多份時別台裝置改到「另一份」也要擋
}

type idVer struct {
	ID      string
	Version int64
}

func idVers(fs []drive.File) []idVer {
	out := make([]idVer, len(fs))
	for i, f := range fs {
		out[i] = idVer{f.ID, f.Version}
	}
	slices.SortFunc(out, func(a, b idVer) int { return strings.Compare(a.ID, b.ID) })
	return out
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
	return nil, i18n.Errorf("pull.err.same_name", "count", len(hits), "pids", strings.Join(pids, i18n.T("sep.list")))
}

// errSkipCommit:fn 用它表示「成功,但這次不要 COMMIT」(--dry-run 永不寫入,連 bootstrap 檔都不建)。
var errSkipCommit = errors.New("skip commit")

// withCanonical:鎖 → FETCH → 閘(Drive 不完整 / schema 太新 → 零寫入)→ fn 改狀態 → COMMIT。fn 回錯就什麼都不寫。
// pull.lock 包住整段,兩個 capy 同時 pull 不會互相蓋掉對方剛上傳的檔;token 鎖在它裡面,順序固定不會死鎖。
func withCanonical(ctx context.Context, stderr io.Writer, fn func(*canonState) error) error {
	unlock, err := auth.LockFile(ctx, "pull.lock", i18n.T("escape.lock_notice"))
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
			fmt.Fprintln(stderr, i18n.T("pull.warn.duplicate_files", "count", len(fs), "name", name))
		}
		b, err := dc.Download(ctx, f.ID)
		if err != nil {
			return nil, false, i18n.Errorf("escape.err.download", "name", name, "err", friendlyErr("google", err))
		}
		s.fetched[name] = fetchedFile{f, b, idVers(fs)}
		return b, true, nil
	}
	decodeErr := func(name string, err error) error {
		return i18n.Errorf("pull.err.read_drive_file", "name", name, "err", err)
	}
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
		fmt.Fprintln(stderr, i18n.T("pull.warn.tracks", "warning", w))
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
		fmt.Fprintln(stderr, i18n.T("pull.healed", "count", healed, "playlists", strings.Join(healedNames, i18n.T("sep.list"))))
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
		return nil, &BlockedError{Msg: i18n.T("pull.blocked.drive_incomplete", "files", strings.Join(missing, i18n.T("sep.list")), "account", googleAccount())}
	}
	return s, nil
}

func googleAccount() string {
	if cfg, err := config.Load(); err == nil && cfg.GoogleEmail != "" {
		return cfg.GoogleEmail
	}
	return i18n.T("pull.unknown_account")
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
	if len(ups) > 0 {
		names := make([]string, len(ups))
		for i, u := range ups {
			names[i] = u.ref.Name
		}
		if err := guardVersions(ctx, dc, s, names); err != nil {
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
			return i18n.Errorf("pull.err.upload", "name", u.ref.Name, "err", friendlyErr("google", err))
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
		fmt.Fprintln(s.stderr, i18n.T("pull.warn.cache_write", "err", err))
	}
	return nil
}

// guardVersions:共享檔版本守衛(spec §6.3,決策 29)。Drive 沒有 CAS,lost update 擋不住,但擋得住它的後果:
// 上傳前再 list 一次,FETCH 讀過的每個檔(不只這次要傳的——沒變的檔也參與了決策)同名檔的 (ID, Version) 集合都要跟 FETCH 時一樣
// (同名多份時對方改到另一份也算),要新建的檔也仍然不存在;任一不符就一個檔都不傳、本機不動、回錯叫使用者重跑
// (重跑會 FETCH 到對方的結果再算一次)。訊息列出全部不符的檔與各自的原因。殘餘窗口只剩上傳序列本身。
// guardError:版本守衛擋下(pull 時 Drive 零寫入;push 時平台可能已經寫了,訊息由 push 改口)。
type guardError struct{ Files string }

func (e *guardError) Error() string {
	return i18n.T("pull.err.drive_changed", "files", e.Files)
}

func guardVersions(ctx context.Context, dc *drive.Client, s *canonState, staged []string) error {
	files, err := dc.List(ctx, "")
	if err != nil {
		return friendlyErr("google", err)
	}
	byName := map[string][]drive.File{}
	for _, f := range files {
		byName[f.Name] = append(byName[f.Name], f)
	}
	var bad []string
	for _, name := range slices.Sorted(maps.Keys(s.fetched)) {
		f := s.fetched[name]
		now := idVers(byName[name])
		i := slices.IndexFunc(now, func(v idVer) bool { return v.ID == f.file.ID })
		switch {
		case slices.Equal(now, f.peers):
		case i < 0:
			bad = append(bad, i18n.T("pull.guard.deleted", "name", name))
		case now[i].Version != f.file.Version:
			bad = append(bad, i18n.T("pull.guard.modified", "name", name))
		default:
			bad = append(bad, i18n.T("pull.guard.duplicated", "name", name))
		}
	}
	for _, name := range staged {
		if _, ok := s.fetched[name]; !ok && len(byName[name]) > 0 { // 我們要 Create 的檔對方剛建了:再建就是第二份
			bad = append(bad, i18n.T("pull.guard.created", "name", name))
		}
	}
	if len(bad) > 0 {
		return &guardError{Files: strings.Join(bad, i18n.T("sep.list"))}
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
		return "", "", i18n.Errorf("pull.err.provider_ref", "ids", strings.Join(providerIDs, "|"), "arg", strconv.Quote(arg))
	}
	return prov, ref, nil
}

// ---- pl link / unlink ----

// readable:平台清單的內容讀不讀得到 = pl link 的連結條件。讀不到(ErrRestricted:Spotify 編輯清單、他人清單,spec §1.1)
// 的清單 pull 永遠會跳過,連了只是騙自己。404 算讀得到:Apple 的 library 端點對空清單回 404,「新建空清單 → link → 再放歌」是正常起手式。
func readable(ctx context.Context, r provider.PlaylistReader, id string) (bool, error) {
	_, err := r.GetPlaylistItems(ctx, id)
	if errors.Is(err, provider.ErrRestricted) {
		return false, nil
	}
	if err != nil && !errors.Is(err, provider.ErrNotFound) {
		return false, err
	}
	return true, nil
}

func newPlLinkCmd() *cobra.Command {
	var createFlag bool
	cmd := &cobra.Command{
		Use: i18n.T("cmd.pl.link.use"), Short: i18n.T("cmd.pl.link.short"), Args: argsOrPicker(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			create := createFlag // 挑選器第二段選「建新的空清單」也會把它打開
			prov, ref := "", ""
			var err error
			if len(args) == 2 {
				if create { // 平台清單還不存在,沒有 ID 或名稱可給
					if prov = args[1]; !isProviderID(prov) {
						return i18n.Errorf("link.err.create_platform_only", "ids", strings.Join(providerIDs, "|"), "arg", strconv.Quote(prov))
					}
				} else if prov, ref, err = splitProviderRef(args[1]); err != nil {
					return err
				}
			} else if prov, err = pickProvider(i18n.T("link.pick.platform")); err != nil { // 第一段。三個平台全列,不先探測誰有登入:
				return err // 探測要建 provider 又慢,選到沒登入的,下面的錯誤本來就會指路 auth login
			}
			p, err := newProvider(ctx, prov)
			if err != nil {
				return err
			}
			r, err := asPlaylistReader(p)
			if err != nil {
				return err
			}
			creator, cerr := asPlaylistCreator(p)
			if create && cerr != nil { // 在碰 Drive 之前擋
				return cerr
			}
			// ref == "" 是「走挑選器」的哨兵,靠 splitProviderRef 保證有給參數時 ref 一定非空
			// (ok / isProviderID / ref == "" 三條它都會回錯);放寬它的話非 TTY 會悄悄走進挑選器。
			var id string
			var refs []provider.PlaylistRef
			switch {
			case create: // 平台清單等 canonical 清單定了才建(名字跟著它);refs 只拿來擋同名,下面兩道存在性檢查對它不適用
				if refs, err = r.ListPlaylists(ctx); err != nil {
					return friendlyErr(prov, err)
				}
			case ref != "":
				if id, err = resolvePlaylistID(ctx, r, prov, ref); err != nil {
					return err
				}
				if refs, err = r.ListPlaylists(ctx); err != nil {
					return friendlyErr(prov, err)
				}
			default: // 第二段:同一份 refs 餵挑選器與下面的存在性檢查,不多打一趟
				if refs, err = r.ListPlaylists(ctx); err != nil {
					return friendlyErr(prov, err)
				}
				newLabel := ""
				if cerr == nil {
					newLabel = i18n.T("link.pick.create_new", "platform", prov)
				}
				if id, err = pickPlatformPlaylist(prov, refs, newLabel); err != nil {
					return err
				}
				create = id == ""
			}
			if !create {
				// 「存在」的定義要跟 pull 的 gone 判準一致(在 ListPlaylists 裡):像 base62 的 ID 會被 resolvePlaylistID 直接放行,
				// 別人的公開清單讀得到卻不在自己的列表裡,連了第一次 pull 就會被當成已刪除而自動 unlink。
				if !slices.ContainsFunc(refs, func(x provider.PlaylistRef) bool { return x.ID == id }) {
					return i18n.Errorf("link.err.not_listed", "platform", prov, "id", id)
				}
				if ok, err := readable(ctx, r, id); err != nil {
					return friendlyErr(prov, err)
				} else if !ok {
					return i18n.Errorf("link.err.unreadable", "platform", prov, "id", id)
				}
			}
			var done strings.Builder // 成功訊息等 COMMIT 成功才印:COMMIT 失敗時 stdout 不可還說「已連結」(PR #48 review)
			recovery := ""           // 平台清單建好之後才非空:之後出的錯只可能是 COMMIT,要告訴使用者怎麼接回去
			err = withCanonical(ctx, cmd.ErrOrStderr(), func(s *canonState) error {
				arg := ""
				if len(args) == 2 {
					arg = args[0]
				} else { // 第三段:挑 canonical 清單,或建一個新的
					var err error
					if arg, err = pickLinkTarget(s, prov, id); err != nil {
						return err
					}
				}
				pl, err := s.find(arg)
				if err != nil {
					return err
				}
				if !create { // 要建的清單還沒有 id,沒人連得到它;空 id 會撞上每個沒連這個平台的清單
					for _, pid := range slices.Sorted(maps.Keys(s.playlists)) {
						if other := s.playlists[pid]; other.Links[prov] == id && (pl == nil || other.PID != pl.PID) {
							return i18n.Errorf("link.err.taken", "platform", prov, "id", id, "name", other.Name, "pid", other.PID)
						}
					}
				}
				if pl == nil {
					if _, err := ulid.Time(arg); err == nil && strings.ToUpper(arg) == arg { // 合法 ULID 卻不存在:別把它當名字建清單
						return i18n.Errorf("link.err.no_pid", "pid", arg)
					}
					pl = canon.NewPlaylist(arg)
					s.playlists[pl.PID] = pl
					fmt.Fprintln(cmd.ErrOrStderr(), i18n.T("link.created_master", "name", pl.Name, "pid", pl.PID))
				}
				if cur, ok := pl.Links[prov]; ok && cur != id {
					if !foreignLink(p, cur) {
						return i18n.Errorf("link.err.already_linked", "name", pl.Name, "pid", pl.PID, "platform", prov, "id", cur)
					}
					// 決策 33 / Q30:撞到別台裝置的本機清單 → 接管(重灌後 device_id 變了也靠這條接回來);原裝置下一輪起變 foreign
					fmt.Fprintln(&done, i18n.T("link.taken_over", "name", pl.Name, "pid", pl.PID, "device", deviceName(s, cur), "platform", prov))
					delete(s.mine().Base[pl.PID], prov) // 舊 base 是別台的觀測,對本機的檔沒意義
				}
				if create { // 所有會擋的檢查都在這之前:擋下來時平台上不會留下沒人連的空清單。
					// 安全前提:withCanonical 不重試 fn;哪天它加了樂觀重試,這裡就會建出第二個清單。
					// 平台上已經有同名而且連得上的清單(先在 app 裡建過、或 COMMIT 失敗後重跑):連它就好(sameNamePlaylists,migrate 也用)。
					dup, err := sameNamePlaylists(ctx, r, refs, pl.Name)
					if err != nil {
						return friendlyErr(prov, err)
					}
					switch len(dup) {
					case 0:
					case 1:
						return i18n.Errorf("link.err.same_name_one", "platform", prov, "name", pl.Name, "id", dup[0], "quoted", strconv.Quote(pl.Name))
					default:
						return i18n.Errorf("link.err.same_name_many", "platform", prov, "count", len(dup), "name", pl.Name, "ids", strings.Join(dup, i18n.T("sep.list")), "quoted", strconv.Quote(pl.Name))
					}
					made, err := creator.CreatePlaylist(ctx, pl.Name) // 名字跟 canonical 一樣:push 不會再多排一個 rename
					if err != nil {
						return friendlyErr(prov, err)
					}
					id = made.ID
					fmt.Fprintln(cmd.ErrOrStderr(), i18n.T("link.created_platform", "platform", prov, "name", made.Name, "id", made.ID))
					recovery = i18n.T("link.recovery", "platform", prov, "id", made.ID, "quoted", strconv.Quote(pl.Name))
				}
				if pl.Links[prov] != id {
					pl.Links[prov] = id
					pl.UpdatedAt = canon.Now().Unix()
				}
				fmt.Fprintln(&done, i18n.T("link.done", "name", pl.Name, "pid", pl.PID, "platform", prov, "id", id))
				return nil
			})
			if err != nil {
				if recovery != "" {
					return i18n.Errorf("link.err.with_recovery", "err", err, "recovery", recovery)
				}
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), done.String())
			return nil
		},
	}
	cmd.Flags().BoolVar(&createFlag, "create", false, i18n.T("cmd.pl.link.flag.create"))
	return cmd
}

func newPlUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use: "unlink [name|pid] [provider]", Short: i18n.T("cmd.pl.unlink.short"), Args: argsOrPicker(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 2 && !isProviderID(args[1]) {
				return i18n.Errorf("pull.err.bad_provider", "ids", strings.Join(providerIDs, "|"), "arg", strconv.Quote(args[1]))
			}
			return withCanonical(cmd.Context(), cmd.ErrOrStderr(), func(s *canonState) error {
				arg, prov := "", ""
				if len(args) == 2 {
					arg, prov = args[0], args[1]
				} else {
					var err error
					if arg, err = pickLinkedPlaylist(s, "", i18n.T("unlink.pick.playlist")); err != nil {
						return err
					}
				}
				pl, err := s.find(arg)
				if err != nil {
					return err
				}
				if pl == nil {
					return i18n.Errorf("unlink.err.not_found", "arg", strconv.Quote(arg))
				}
				if prov == "" { // 第二段:只列這個清單真的連了的平台,選了不會撲空
					provs := slices.Sorted(maps.Keys(pl.Links))
					labels := make([]string, len(provs))
					for i, pv := range provs {
						labels[i] = pv + ":" + pl.Links[pv]
					}
					i, err := pickOne(i18n.T("unlink.pick.platform"), labels)
					if err != nil {
						return err
					}
					prov = provs[i]
				}
				id, ok := pl.Links[prov]
				if !ok {
					return i18n.Errorf("unlink.err.not_linked", "name", pl.Name, "pid", pl.PID, "platform", prov)
				}
				delete(pl.Links, prov)
				pl.UpdatedAt = canon.Now().Unix()
				fmt.Fprintln(cmd.OutOrStdout(), i18n.T("unlink.done", "name", pl.Name, "pid", pl.PID, "platform", prov, "id", id))
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
	err := newForm(huh.NewGroup(
		huh.NewConfirm().Title(prompt).Affirmative(i18n.T("changeset.confirm.apply")).Negative(i18n.T("changeset.confirm.cancel")).Value(&ok),
	)).Run()
	return ok, err
}

// bothTTY:stdout 與 stdin 都是終端機才能問;echo | capy … 這種 stdin 是管線的不問,直接當待套用。
// 測試 / web 替換點(P7 決策 40:web 模式整段換成 true)。reviewIsTTY(resolve.go)與 migrateIsTTY(migrate.go)
// 委派到這裡(不是複製函式值),覆寫這一處七個確認閘就全對齊。
var bothTTY = func(cmd *cobra.Command) bool { return stdoutIsTTY(cmd) && ui.IsTTY(os.Stdin) }

// removalBlocked:刪除閾值(Q3 採 B,2026-09-08;附錄 C 決策 18):單一 (清單, provider) 要移除 >10 首,
// 或 >30% 且 >3 首。分母是「該 provider 可見的曲數」(DeriveResult.VisibleCount),不是 canonical 總曲數。
func removalBlocked(removes, visible int) bool {
	return removes > 10 || (removes > 3 && removes*10 > visible*3)
}

var pullHeader = []string{"ACTION", "PROVIDER", "PLAYLIST", "POS", "CID", "PROVIDER_ID", "TITLE", "ARTISTS", "REASON", "REASON_CODE"}

func newPlPullCmd() *cobra.Command {
	var all, dryRun, yes, force bool
	var prov string
	cmd := &cobra.Command{
		Use:   "pull [name|pid]",
		Short: i18n.T("cmd.pl.pull.short"),
		Long:  i18n.T("cmd.pl.pull.long"),
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := needTarget(cmd, args, all, i18n.Errorf("pick.err.need_target.pull")); err != nil {
				return err
			}
			if prov != "" && !isProviderID(prov) {
				return i18n.Errorf("pull.err.bad_provider", "ids", strings.Join(providerIDs, "|"), "arg", strconv.Quote(prov))
			}
			if force && all { // 安全閥一次只解除一個清單:整輪放行會連「使用者自己都還不知道被清空」的清單一起放掉
				return i18n.Errorf("pull.err.force_with_all")
			}
			ctx, stderr := cmd.Context(), cmd.ErrOrStderr()
			return withCanonical(ctx, stderr, func(s *canonState) error {
				targets, err := pullTargets(s, args, all, prov, i18n.T("pull.pick.title.pull"))
				if err != nil {
					return err
				}
				rows, blocked, _, err := observeAndDerive(ctx, s, targets, prov, stderr, newPlatforms(ctx))
				if err != nil {
					return err
				}
				if len(rows) > 0 { // 零列時 TTY 也不印空表頭
					if err := ui.Table(cmd.OutOrStdout(), stdoutIsTTY(cmd), pullHeader, rows, tableOpts(yes)...); err != nil {
						return err
					}
				}
				if len(blocked) > 0 && !force { // 安全閥先於提示:被擋下的 derive 結果不落地,拿它算「尚未對應」會對不上
					return &BlockedError{Msg: i18n.T("changeset.blocked", "reasons", strings.Join(blocked, i18n.T("sep.clause")))}
				}
				resolveHint(s, targets, prov, stderr) // pull 不做 resolve,只提示(決策 22);--provider 那輪只提示它
				if len(rows) == 0 {
					fmt.Fprintln(stderr, i18n.T("changeset.no_changes"))
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
					ok, err := confirmWrite(i18n.T("pull.confirm", "count", len(rows)))
					if err != nil {
						return err
					}
					if !ok {
						return &PendingError{N: len(rows)}
					}
				}
				fmt.Fprintln(stderr, i18n.T("changeset.applied", "count", len(rows)))
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, i18n.T("cmd.pl.pull.flag.all"))
	cmd.Flags().StringVar(&prov, "provider", "", i18n.T("cmd.pl.pull.flag.provider"))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("cmd.pl.pull.flag.dry_run"))
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, i18n.T("cmd.pl.pull.flag.yes"))
	cmd.Flags().BoolVar(&force, "force", false, i18n.T("cmd.pl.pull.flag.force"))
	return cmd
}

// pullTargets:--all = 所有有連結的清單(可用 --provider 篩),否則指定的那一個;依 (name, pid) 排序,輸出才決定性。
// verb:挑選器標題 pull.pick.title 的 {verb},呼叫端翻好的原形動詞(en:pull / push / sync;zh-TW:拉 / 推 / 同步);
// len(args) == 1 或 --all 時用不到。ponytail: 英文「Pick a playlist to {verb}」拼得通;哪個語系的動詞會變形,再改成呼叫端傳整句標題。
// title 是挑選器的整句標題(每個呼叫者一則 key:英文不拼動詞)。
func pullTargets(s *canonState, args []string, all bool, prov, title string) ([]*canon.Playlist, error) {
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
		arg := ""
		if len(args) == 1 {
			arg = args[0]
		} else { // 不帶參數 + 終端機:挑選器補(呼叫端的 needTarget 已擋掉非 TTY)
			var err error
			if arg, err = pickLinkedPlaylist(s, prov, title); err != nil {
				return nil, err
			}
		}
		pl, err := s.find(arg)
		if err != nil {
			return nil, err
		}
		if pl == nil {
			return nil, i18n.Errorf("pull.err.not_found", "arg", strconv.Quote(arg), "name", arg)
		}
		if !linked(pl) {
			return nil, i18n.Errorf("pull.err.not_linked", "name", pl.Name, "pid", pl.PID)
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

// liveKey:(pid, provider)。
type liveKey struct{ pid, prov string }

// observeAndDerive:OBSERVE + DERIVE,直接改 s(fn 回錯時 withCanonical 不會 COMMIT,所以不必另外暫存)。
// gone 的訊號是「不在 ListPlaylists 的列表裡」而不是 items 回 404——Apple 的 library 端點對空清單也回 404(P2 遺留)。
// lives 是這輪讀到的每個 L(pl sync 的 push 半邊重用,不再打一次平台;決策 31);值是 nil 表示 pull 半邊已經跳過它
// (restricted),push 半邊也直接跳過、不重讀不重印。
func observeAndDerive(ctx context.Context, s *canonState, targets []*canon.Playlist, only string, stderr io.Writer, pf *platforms) (rows [][]string, blocked []string, lives map[liveKey]*canon.Observed, err error) {
	lives = map[liveKey]*canon.Observed{}
	merged := mergedBase(s)
	for _, pl := range targets {
		for _, prov := range slices.Sorted(maps.Keys(pl.Links)) {
			if only != "" && prov != only {
				continue
			}
			p, err := pf.provider(prov)
			if err != nil {
				return nil, nil, nil, err
			}
			link := pl.Links[prov]
			if foreignLink(p, link) { // 決策 33:別台裝置的本機清單——不是 gone、不動 base、不 unlink
				fmt.Fprintln(stderr, i18n.T("pull.skip.foreign", "name", pl.Name, "platform", prov, "id", link, "device", deviceName(s, link)))
				continue
			}
			r, refs, err := pf.reader(prov)
			if err != nil {
				return nil, nil, nil, err
			}
			in := canon.DeriveInput{Provider: prov, Playlist: *pl, Tracks: s.tracks.Tracks, Merged: s.tracks.Merged}
			if b, ok := merged[pl.PID][prov]; ok && b.Snapshot.ID == link { // 別的平台清單留下的 base 不算數
				in.Base = &b.Snapshot
			}
			if ref, ok := refs[link]; ok {
				tracks, err := r.GetPlaylistItems(ctx, link)
				switch {
				case errors.Is(err, provider.ErrRestricted):
					fmt.Fprintln(stderr, i18n.T("pull.skip.restricted", "name", pl.Name, "platform", prov, "id", link))
					lives[liveKey{pl.PID, prov}] = nil
					continue
				case errors.Is(err, provider.ErrNotFound):
					tracks = nil // 有列在清單列表裡卻 404 = 空清單(Apple 的 library 端點就這樣回);移除照常走 GATE 與閾值,不是 exit 1
				case err != nil:
					return nil, nil, nil, i18n.Errorf("pull.err.read_playlist", "name", pl.Name, "platform", prov, "id", link, "err", friendlyErr(prov, err))
				}
				in.Live = &canon.Observed{ID: link, Name: ref.Name, Tracks: tracks}
				lives[liveKey{pl.PID, prov}] = in.Live
			}
			res, err := canon.Derive(in)
			if err != nil {
				return nil, nil, nil, err
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
				rows = append(rows, []string{ch.Action, prov, pl.Name, pos, ch.CID, ch.ProviderID, ch.Title, strings.Join(ch.Artists, ", "), ch.Reason, ch.Code})
			}
			if removalBlocked(removes, res.VisibleCount) {
				blocked = append(blocked, i18n.T("pull.blocked.threshold", "name", pl.Name, "platform", prov, "removes", removes, "visible", res.VisibleCount))
			}
			if slices.ContainsFunc(res.Changes, func(ch canon.Change) bool { return ch.Action == "rename" }) {
				// 改名落地的這一輪說一次:push 不會把 rename 排給不支援改名的平台(規則 4、P6 §2 A13),之後也不再提(每輪都印太吵——PR #38 review)
				for _, other := range slices.Sorted(maps.Keys(pl.Links)) {
					if op, err := pf.provider(other); other != prov && err == nil && !op.Caps().Has(provider.CapPlaylistRename) {
						fmt.Fprintln(stderr, i18n.T("pull.hint.rename_unsupported", "name", pl.Name, "new_name", res.Playlist.Name, "platform", other))
					}
				}
			}
			if res.Gone {
				fmt.Fprintln(stderr, i18n.T("pull.warn.gone", "platform", prov, "id", link, "name", pl.Name))
				delete(pl.Links, prov)
				delete(s.mine().Base[pl.PID], prov) // 自己這台的舊 base 一起清(別台的碰不到,靠 Snapshot.ID 比對擋)
				pl.UpdatedAt = canon.Now().Unix()
				continue
			}
			s.absorb(res.Tracks)
			*pl = res.Playlist
			if b, ok := merged[pl.PID][prov]; !ok || !snapshotEqual(b.Snapshot, res.Snapshot) {
				s.mine().SetBase(pl.PID, prov, res.Snapshot) // 只在 base 缺或變了才寫:observed_at 每次都動,無條件寫 = 每次都上傳 dev 檔
			}
		}
	}
	return rows, blocked, lives, nil
}

// deviceName:綁裝置的 id(<device_id>/…)的擁有裝置名(manifest 的 hostname),沒有就印 id。
func deviceName(s *canonState, id string) string {
	dev, _, _ := strings.Cut(id, "/")
	for _, d := range s.manifest.Devices {
		if d.ID == dev && d.Name != "" {
			return i18n.T("pull.device_name", "name", d.Name, "id", dev)
		}
	}
	return dev
}

func snapshotEqual(a, b canon.Snapshot) bool {
	return a.ID == b.ID && a.Name == b.Name && slices.Equal(a.Items, b.Items) && slices.Equal(a.CIDs, b.CIDs)
}
