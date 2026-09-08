// Package local:本機曲庫 provider(P6,spec §1.4、附錄 C 決策 33–36)。清單 = local_root 這一層的 *.m3u8 / *.m3u,曲庫 = library.json;
// 不讀音訊 tag、不加相依。它的存在是為了驗證 SPI 沒有偷渡網路平台的假設——每一條「local 不適用」都記在計畫 §2。
//
// 綁裝置(決策 33):playlist id = <device_id>/<檔名>,別台裝置的 id 是 Foreign,CLI 只跳過。track id 不帶 device——
// 只是正規化相對路徑,cid `p:local:<路徑>` 才撐得過重灌 / 接管(device_id 變了,沒 ISRC 的曲目不會整批分裂;PR #37 review)。
// id 是正規化相對路徑(決策 34):forward slash、去 ./、NFC;改名 / 搬家 = 下一次 pull 的 remove + add。
package local

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/text/unicode/norm"

	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// LibrarySchemaVersion:library.json 的 schema_version;比這新就拒讀(同 Drive 檔的規矩)。
const LibrarySchemaVersion = 1

// Library:<root>/library.json。鍵是相對於 root 的正規化路徑;由使用者(或未來的 capy local scan)維護。
type Library struct {
	SchemaVersion int                     `json:"schema_version"`
	Tracks        map[string]LibraryTrack `json:"tracks"`
}

type LibraryTrack struct {
	Title      string   `json:"title"`
	Artists    []string `json:"artists"`
	Album      string   `json:"album,omitempty"`
	DurationMS int      `json:"duration_ms,omitempty"`
	ISRC       string   `json:"isrc,omitempty"`
}

// Provider:root 目錄 + 本裝置 id。曲庫每個 process 讀一次(惰性);清單檔每次讀(它們是別的工具會改的東西)。
type Provider struct {
	root, deviceID string
	once           sync.Once
	lib            *Library
	libErr         error
}

var (
	_ provider.Provider       = (*Provider)(nil)
	_ provider.Searcher       = (*Provider)(nil)
	_ provider.PlaylistReader = (*Provider)(nil)
	_ provider.ISRCLookup     = (*Provider)(nil)
	_ provider.TrackGetter    = (*Provider)(nil)
	_ provider.DeviceScoped   = (*Provider)(nil)
	_ provider.PlaylistWriter = (*Provider)(nil)
)

func New(root, deviceID string) *Provider {
	return &Provider{root: filepath.Clean(root), deviceID: deviceID}
}

func (p *Provider) ID() string          { return "local" }
func (p *Provider) DisplayName() string { return "本機曲庫" }
func (p *Provider) Root() string        { return p.root }

func (p *Provider) Caps() provider.Capability {
	// 不宣告播放(Q28)、不宣告 Create;不宣告 Rename——local 的 id 就是檔名,改名 = id 變 = 下一次 pull 當 gone(計畫 §2 A12)
	return provider.CapSearch | provider.CapISRCExpose | provider.CapISRCLookup | provider.CapPlaylistRead | provider.CapDeviceBound |
		provider.CapPlaylistAppend | provider.CapPlaylistRemove | provider.CapPlaylistReorder
}

// Health:root 是可讀目錄、library.json(若存在)讀得懂。沒有 library.json 不算錯(曲庫空,清單仍讀得到)。
func (p *Provider) Health(ctx context.Context) error {
	st, err := os.Stat(p.root)
	if err != nil {
		return fmt.Errorf("local_root %s:%w", p.root, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("local_root %s 不是目錄", p.root)
	}
	if _, err := os.ReadDir(p.root); err != nil {
		return fmt.Errorf("local_root %s:%w", p.root, err)
	}
	_, err = p.library()
	return err
}

// Foreign:id 不是本裝置的(決策 33)。空 id 也算 foreign(不會誤把壞資料當本機的)。
func (p *Provider) Foreign(id string) bool { return !strings.HasPrefix(id, p.deviceID+"/") }

// idOf / relOf 只用在 playlist id;track id 就是路徑本身。
func (p *Provider) idOf(rel string) string { return p.deviceID + "/" + rel }

// relOf:本機 playlist id → 檔名;foreign 回 ErrNotFound(呼叫端本來就該先問 Foreign)。
func (p *Provider) relOf(id string) (string, error) {
	if p.Foreign(id) {
		return "", fmt.Errorf("%s 屬於別台裝置:%w", id, provider.ErrNotFound)
	}
	return strings.TrimPrefix(id, p.deviceID+"/"), nil
}

// NormalizePath:決策 34——Clean(去 ./ 與多餘的 /)、NFC;`\` 只在 Windows 翻成 `/`(那裡它是分隔符;在 macOS 它是合法的檔名字元,
// 翻了會把使用者真實存在的 `mix\pop.m3u8` 拆成假目錄——PR #37 review)。M3U 內容裡的 `\` 由 readM3U 另外翻。相對路徑一律不帶開頭的 /。
func NormalizePath(s string) string {
	s = norm.NFC.String(strings.TrimSpace(s))
	if runtime.GOOS == "windows" {
		s = strings.ReplaceAll(s, "\\", "/")
	}
	if s == "" {
		return ""
	}
	return path.Clean(s)
}

// isAbsAny:forward-slash 化之後的路徑是不是絕對路徑——path.IsAbs 只認開頭的 /,認不得 Windows 匯出的 M3U 常見的 C:/…;
// //server/share 開頭是 / 所以也算。絕對路徑原樣當 id、不接到目錄後面、也不去 root 底下找(在 macOS 上 `C:` 是合法的目錄名)。
func isAbsAny(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && p[2] == '/' && (('a' <= p[0] && p[0] <= 'z') || ('A' <= p[0] && p[0] <= 'Z'))
}

// underRoot:相對路徑而且沒跳出 root(可以去 root 底下 stat)。
func underRoot(rel string) bool {
	return rel != "" && rel != "." && rel != ".." && !isAbsAny(rel) && !strings.HasPrefix(rel, "../")
}

// fsPath:正規化的相對路徑 → 檔案系統上的實際路徑。直接找得到就用;找不到就掃同目錄找「正規化後同名」的項目——
// APFS 對 NFC / NFD 不分所以 macOS 永遠直接命中,NTFS / ext4 分,而 M3U 裡的路徑與目錄裡的名字可能是不同形式(決策 34)。
// 都找不到就回直接路徑,讓後面的 open / stat 用它自己的錯誤說話。
func (p *Provider) fsPath(rel string) string {
	full := filepath.Join(p.root, filepath.FromSlash(rel))
	if _, err := os.Lstat(full); err == nil {
		return full
	}
	dir, base := filepath.Split(full)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return full
	}
	want := norm.NFC.String(base)
	for _, e := range ents {
		if norm.NFC.String(e.Name()) == want {
			return filepath.Join(dir, e.Name())
		}
	}
	return full
}

func (p *Provider) library() (*Library, error) {
	p.once.Do(func() {
		p.lib, p.libErr = LoadLibrary(filepath.Join(p.root, "library.json"))
	})
	return p.lib, p.libErr
}

// LoadLibrary:讀 library.json;不存在 = 空曲庫;schema 太新或 JSON 壞 → 錯誤(訊息指向檔案)。鍵一律正規化。
func LoadLibrary(file string) (*Library, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return &Library{SchemaVersion: LibrarySchemaVersion, Tracks: map[string]LibraryTrack{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("讀取 %s:%w", file, err)
	}
	var lib Library
	if err := json.Unmarshal(b, &lib); err != nil {
		return nil, fmt.Errorf("%s 不是合法的 JSON:%w", file, err)
	}
	if lib.SchemaVersion > LibrarySchemaVersion {
		return nil, fmt.Errorf("%s 的 schema_version %d 比這版 capy 認得的 %d 新,請更新 capy", file, lib.SchemaVersion, LibrarySchemaVersion)
	}
	out := &Library{SchemaVersion: LibrarySchemaVersion, Tracks: make(map[string]LibraryTrack, len(lib.Tracks))}
	seen := map[string]string{} // 正規化後的鍵 → 原鍵:撞到就報錯(map 迭代順序隨機,不能讓「哪筆勝出」每次不同)
	for _, k := range sortedKeys(lib.Tracks) {
		n := NormalizePath(k)
		if prev, dup := seen[n]; dup {
			return nil, fmt.Errorf("%s:%q 與 %q 正規化後是同一個路徑 %q,留一個", file, prev, k, n)
		}
		seen[n] = k
		out.Tracks[n] = lib.Tracks[k]
	}
	return out, nil
}

// ListPlaylists:root 這一層的 *.m3u8 / *.m3u(不遞迴,Q25),依檔名排序;Name = 去副檔名的檔名,Total = 曲目行數。
func (p *Provider) ListPlaylists(ctx context.Context) ([]provider.PlaylistRef, error) {
	ents, err := os.ReadDir(p.root)
	if err != nil {
		return nil, fmt.Errorf("local_root %s:%w", p.root, err)
	}
	var refs []provider.PlaylistRef
	for _, e := range ents {
		if e.IsDir() || !isPlaylistFile(e.Name()) {
			continue
		}
		name := NormalizePath(e.Name())
		items, _, err := p.readM3U(name)
		total := len(items)
		if err != nil { // 一個讀不了的檔不拖垮整個 local(cron 的 sync --all、不相干清單的 link);Total -1 = 未知(pl list 印 -),真的用到它時 GetPlaylistItems 會說真正的錯
			total = -1
		}
		refs = append(refs, provider.PlaylistRef{ID: p.idOf(name), Name: strings.TrimSuffix(name, path.Ext(name)), Total: total})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs, nil
}

func isPlaylistFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".m3u8", ".m3u":
		return true
	}
	return false
}

// m3uItem:清單裡的一行(相對於 root 的正規化路徑)與 #EXTINF 給的備援標題。
type m3uItem struct {
	rel   string
	title string
	dur   int
}

// readM3U:非 # 開頭的每一行是一個路徑(相對於 M3U 檔所在目錄,再正規化成相對於 root;`\` 一律當 Windows 匯出的分隔符翻成 `/`);
// #EXTINF:秒,標題 只當備援。絕對路徑(含 C:/…)或跳出 root 的路徑原樣保留(不會在曲庫裡,推不出去,但它仍是清單裡的一首)。
func (p *Provider) readM3U(name string) (items []m3uItem, lines []string, err error) {
	f, err := os.Open(p.fsPath(name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("清單 %s 不存在:%w", name, provider.ErrNotFound)
		}
		return nil, nil, fmt.Errorf("讀取清單 %s:%w", name, err)
	}
	defer f.Close()
	dir := path.Dir(name)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var pending m3uItem
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		lines = append(lines, line)
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case strings.HasPrefix(t, "#EXTINF:"):
			if dur, title, ok := strings.Cut(strings.TrimPrefix(t, "#EXTINF:"), ","); ok {
				pending.title = strings.TrimSpace(title)
				if d, err := strconv.ParseFloat(strings.TrimSpace(dur), 64); err == nil && d > 0 {
					pending.dur = int(d * 1000)
				}
			}
		case strings.HasPrefix(t, "#"):
		default:
			rel := NormalizePath(strings.ReplaceAll(t, "\\", "/"))
			if !isAbsAny(rel) && dir != "." {
				rel = path.Clean(dir + "/" + rel)
			}
			items = append(items, m3uItem{rel: rel, title: pending.title, dur: pending.dur})
			pending = m3uItem{}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("讀取清單 %s:%w", name, err)
	}
	return items, lines, nil
}

// GetPlaylistItems:M3U 的每一行一首;metadata 來自曲庫,曲庫沒有的用 #EXTINF 或檔名當標題(它仍是一首:A5)。
func (p *Provider) GetPlaylistItems(ctx context.Context, id string) ([]provider.Track, error) {
	name, err := p.relOf(id)
	if err != nil {
		return nil, err
	}
	items, _, err := p.readM3U(name)
	if err != nil {
		return nil, err
	}
	lib, err := p.library()
	if err != nil {
		return nil, err
	}
	out := make([]provider.Track, len(items))
	for i, it := range items {
		out[i] = p.track(lib, it.rel, it.title, it.dur)
	}
	return out, nil
}

func (p *Provider) track(lib *Library, rel, fallbackTitle string, fallbackDur int) provider.Track {
	tr := provider.Track{ProviderID: rel}
	if t, ok := lib.Tracks[rel]; ok {
		tr.Title, tr.Artists, tr.Album, tr.DurationMS, tr.ISRC = t.Title, slices.Clone(t.Artists), t.Album, t.DurationMS, provider.NormalizeISRC(t.ISRC)
		return tr
	}
	tr.Title, tr.DurationMS = fallbackTitle, fallbackDur
	if tr.Title == "" {
		tr.Title = strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	}
	return tr
}

// Search:曲庫內比對——查詢的每個 token(不分大小寫)都要出現在 標題 + 藝人 + 專輯 裡。沒有網路、沒有 quota(A6)。
// ponytail: 沒用 resolve.Norm(provider 不能依賴 resolve);全半形 / feat. 之類的正規化等真的比不到再說。
func (p *Provider) Search(ctx context.Context, q provider.Query) ([]provider.Track, error) {
	lib, err := p.library()
	if err != nil {
		return nil, err
	}
	toks := strings.Fields(strings.ToLower(norm.NFC.String(q.Text)))
	if len(toks) == 0 {
		return nil, nil
	}
	var out []provider.Track
	for _, rel := range sortedKeys(lib.Tracks) {
		t := lib.Tracks[rel]
		hay := strings.ToLower(norm.NFC.String(t.Title + " " + strings.Join(t.Artists, " ") + " " + t.Album))
		ok := true
		for _, tok := range toks {
			if !strings.Contains(hay, tok) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, p.track(lib, rel, "", 0))
			if q.Limit > 0 && len(out) >= q.Limit {
				break
			}
		}
	}
	return out, nil
}

// LookupISRC:曲庫裡 isrc 相同的全部(可能多筆,消歧交給 resolver)。
func (p *Provider) LookupISRC(ctx context.Context, isrc string) ([]provider.Track, error) {
	n := provider.NormalizeISRC(isrc)
	if n == "" {
		return nil, provider.ErrBadISRC
	}
	lib, err := p.library()
	if err != nil {
		return nil, err
	}
	var out []provider.Track
	for _, rel := range sortedKeys(lib.Tracks) {
		if provider.NormalizeISRC(lib.Tracks[rel].ISRC) == n {
			out = append(out, p.track(lib, rel, "", 0))
		}
	}
	return out, nil
}

// GetTrack:id = 路徑(使用者打的也先正規化);曲庫有就回曲庫的;曲庫沒有但檔案在 root 底下就回檔名當標題的;都沒有 → ErrNotFound。
func (p *Provider) GetTrack(ctx context.Context, id string) (provider.Track, error) {
	rel := NormalizePath(id)
	lib, err := p.library()
	if err != nil {
		return provider.Track{}, err
	}
	if _, ok := lib.Tracks[rel]; ok {
		return p.track(lib, rel, "", 0), nil
	}
	if underRoot(rel) {
		if _, err := os.Stat(p.fsPath(rel)); err == nil {
			return p.track(lib, rel, "", 0), nil
		}
	}
	return provider.Track{}, fmt.Errorf("曲庫與 local_root 都沒有 %s:%w", rel, provider.ErrNotFound)
}

// Pushable:本機的 id、而且曲庫有或檔案在 root 底下(同 GetTrack 的認定);foreign 一律 false(決策 36)。
func (p *Provider) Pushable(id string) bool {
	_, err := p.GetTrack(context.Background(), id)
	return err == nil
}

// ApplyOps(決策 36):用 ApplyPlaylistOps 算目標序列,整檔重寫(#EXTM3U + 每首一行相對路徑;**別的工具寫的 #EXTINF 與註解會被丟掉**),
// 寫到同目錄的暫存檔再 os.Rename 原子取代。rename 回在 skipped(id 就是檔名,改名會讓 id 變,SPI 沒有「新 id」可回——A12),
// 呼叫端列成 manual。一次寫入,不會有 PartialWriteError。
func (p *Provider) ApplyOps(ctx context.Context, id string, current []string, ops []provider.PlaylistOp) (skipped []provider.PlaylistOp, err error) {
	name, err := p.relOf(id)
	if err != nil {
		return nil, err
	}
	var doable []provider.PlaylistOp
	for _, op := range ops {
		if op.Kind == provider.OpRename {
			skipped = append(skipped, op)
			continue
		}
		doable = append(doable, op)
	}
	want, _, err := provider.ApplyPlaylistOps(current, doable)
	if err != nil {
		return nil, err
	}
	if len(doable) == 0 {
		return skipped, nil
	}
	target := p.fsPath(name) // NTFS 分 NFC / NFD:寫回目錄裡實際的那個檔
	if _, err := os.Stat(target); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("清單 %s 不存在:%w", name, provider.ErrNotFound)
		}
		return nil, err
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, tid := range want { // track id 就是路徑(不帶 device);推不出去的(檔不在這台)Pushable 已經擋在 add 之前,配對上的原樣寫回
		b.WriteString(tid)
		b.WriteString("\n")
	}
	tmp, err := os.CreateTemp(p.root, ".capy-*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name()) // rename 成功後這行是 no-op;失敗時不留暫存檔
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return nil, fmt.Errorf("寫入清單 %s:%w", name, err)
	}
	return skipped, nil
}

func sortedKeys(m map[string]LibraryTrack) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
