// Package local:本機曲庫 provider(P6,spec §1.4、附錄 C 決策 33–36)。清單 = local_root 這一層的 *.m3u8 / *.m3u,曲庫 = library.json;
// 不讀音訊 tag、不加相依。它的存在是為了驗證 SPI 沒有偷渡網路平台的假設——每一條「local 不適用」都記在計畫 §2。
//
// 綁裝置(決策 33):playlist / track id = <device_id>/<正規化相對路徑>,別台裝置的 id 是 Foreign,CLI 只跳過。
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
)

func New(root, deviceID string) *Provider {
	return &Provider{root: filepath.Clean(root), deviceID: deviceID}
}

func (p *Provider) ID() string          { return "local" }
func (p *Provider) DisplayName() string { return "本機曲庫" }
func (p *Provider) Root() string        { return p.root }

func (p *Provider) Caps() provider.Capability {
	// 寫入能力 T2 再加;不宣告播放(Q28)、不宣告 Create
	return provider.CapSearch | provider.CapISRCExpose | provider.CapISRCLookup | provider.CapPlaylistRead | provider.CapDeviceBound
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

func (p *Provider) idOf(rel string) string { return p.deviceID + "/" + rel }

// relOf:本機 id → 相對路徑;foreign 回 ErrNotFound(呼叫端本來就該先問 Foreign)。
func (p *Provider) relOf(id string) (string, error) {
	if p.Foreign(id) {
		return "", fmt.Errorf("%s 屬於別台裝置:%w", id, provider.ErrNotFound)
	}
	return strings.TrimPrefix(id, p.deviceID+"/"), nil
}

// NormalizePath:決策 34——forward slash、Clean(去 ./ 與多餘的 /)、NFC。相對路徑一律不帶開頭的 /。
func NormalizePath(s string) string {
	s = norm.NFC.String(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "\\", "/")
	if s == "" {
		return ""
	}
	return path.Clean(s)
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
	for k, t := range lib.Tracks {
		out.Tracks[NormalizePath(k)] = t
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
		if err != nil {
			return nil, err
		}
		refs = append(refs, provider.PlaylistRef{ID: p.idOf(name), Name: strings.TrimSuffix(name, path.Ext(name)), Total: len(items)})
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

// readM3U:非 # 開頭的每一行是一個路徑(相對於 M3U 檔所在目錄,再正規化成相對於 root);#EXTINF:秒,標題 只當備援。
// 絕對路徑或跳出 root 的路徑原樣保留(不會在曲庫裡,推不出去,但它仍是清單裡的一首)。
func (p *Provider) readM3U(name string) (items []m3uItem, lines []string, err error) {
	f, err := os.Open(filepath.Join(p.root, filepath.FromSlash(name)))
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
			rel := NormalizePath(t)
			if !path.IsAbs(rel) && dir != "." {
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
	tr := provider.Track{ProviderID: p.idOf(rel)}
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

// GetTrack:曲庫有就回曲庫的;曲庫沒有但檔案在 root 底下就回檔名當標題的;都沒有 → ErrNotFound。
func (p *Provider) GetTrack(ctx context.Context, id string) (provider.Track, error) {
	rel, err := p.relOf(id)
	if err != nil {
		return provider.Track{}, err
	}
	lib, err := p.library()
	if err != nil {
		return provider.Track{}, err
	}
	if _, ok := lib.Tracks[rel]; ok {
		return p.track(lib, rel, "", 0), nil
	}
	if !path.IsAbs(rel) && !strings.HasPrefix(rel, "../") {
		if _, err := os.Stat(filepath.Join(p.root, filepath.FromSlash(rel))); err == nil {
			return p.track(lib, rel, "", 0), nil
		}
	}
	return provider.Track{}, fmt.Errorf("曲庫與 local_root 都沒有 %s:%w", rel, provider.ErrNotFound)
}

func sortedKeys(m map[string]LibraryTrack) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
