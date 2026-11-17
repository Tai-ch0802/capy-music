package cli

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/drive"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/provider/spotify"
)

type handlerRoundTripper struct{ h http.Handler }

func (rt handlerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
		req.Body = http.NoBody
	}
	rec := httptest.NewRecorder()
	rt.h.ServeHTTP(rec, req)
	res := rec.Result()
	res.Request = req
	return res, nil
}

func inMemClient(h http.Handler) *http.Client {
	return &http.Client{Transport: handlerRoundTripper{h: h}}
}

// memDrive:純記憶體 http.Handler(等價 drivetest 路由與 q 語法),不開 TCP socket。
type memDrive struct {
	mu    sync.Mutex
	files []*memDriveFile
	seq   int
	now   time.Time
}

type memDriveFile struct {
	id, name string
	version  int64
	modified time.Time
	props    map[string]string
	content  []byte
}

var (
	memReTrashed = regexp.MustCompile(`^trashed = false`)
	memReName    = regexp.MustCompile(`^name = '((?:[^'\\]|\\.)*)'`)
	memReProp    = regexp.MustCompile(`^appProperties has \{ key='((?:[^'\\]|\\.)*)' and value='((?:[^'\\]|\\.)*)' \}`)
)

func newMemDrive() *memDrive {
	return &memDrive{now: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)}
}

func (d *memDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := r.URL.Path
	id, isFile := strings.CutPrefix(p, "/drive/v3/files/")
	uid, isUpload := strings.CutPrefix(p, "/upload/drive/v3/files/")
	switch {
	case r.Method == http.MethodGet && p == "/drive/v3/files":
		match, err := memParseQuery(r.URL.Query().Get("q"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var out []map[string]any
		for _, f := range d.files {
			if match(f) {
				out = append(out, f.toJSON())
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"files": out})
	case r.Method == http.MethodPost && p == "/upload/drive/v3/files":
		meta, content, err := memParseUpload(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name, _ := meta["name"].(string)
		d.seq++
		f := &memDriveFile{
			id:       fmt.Sprintf("id%03d", d.seq),
			name:     name,
			version:  1,
			modified: d.now,
			props:    memPropsOf(meta),
			content:  content,
		}
		d.files = append(d.files, f)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.toJSON())
	case r.Method == http.MethodPatch && isUpload:
		var f *memDriveFile
		for _, cand := range d.files {
			if cand.id == uid {
				f = cand
				break
			}
		}
		if f == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		meta, content, err := memParseUpload(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for k, v := range memPropsOf(meta) {
			if f.props == nil {
				f.props = map[string]string{}
			}
			f.props[k] = v
		}
		f.content = content
		f.version++
		f.modified = d.now
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.toJSON())
	case r.Method == http.MethodGet && isFile:
		for _, f := range d.files {
			if f.id == id {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(f.content)
				return
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	case r.Method == http.MethodDelete && isFile:
		i := slices.IndexFunc(d.files, func(f *memDriveFile) bool { return f.id == id })
		if i < 0 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		d.files = slices.Delete(d.files, i, i+1)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected route "+r.Method+" "+p, http.StatusNotFound)
	}
}

func (f *memDriveFile) toJSON() map[string]any {
	sum := md5.Sum(f.content)
	return map[string]any{
		"id":            f.id,
		"name":          f.name,
		"version":       strconv.FormatInt(f.version, 10),
		"md5Checksum":   hex.EncodeToString(sum[:]),
		"modifiedTime":  f.modified,
		"appProperties": f.props,
	}
}

func memParseUpload(r *http.Request) (map[string]any, []byte, error) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/related" {
		return nil, nil, fmt.Errorf("want multipart/related, got %q", r.Header.Get("Content-Type"))
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	p1, err := mr.NextPart()
	if err != nil {
		return nil, nil, err
	}
	var meta map[string]any
	if err := json.NewDecoder(p1).Decode(&meta); err != nil {
		return nil, nil, err
	}
	p2, err := mr.NextPart()
	if err != nil {
		return nil, nil, err
	}
	content, err := io.ReadAll(p2)
	return meta, content, err
}

func memPropsOf(meta map[string]any) map[string]string {
	raw, _ := meta["appProperties"].(map[string]any)
	if raw == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range raw {
		out[k], _ = v.(string)
	}
	return out
}

func memParseQuery(q string) (func(*memDriveFile) bool, error) {
	unesc := strings.NewReplacer(`\'`, `'`, `\\`, `\`)
	var preds []func(*memDriveFile) bool
	for rest := q; rest != ""; {
		if m := memReTrashed.FindString(rest); m != "" {
			rest = rest[len(m):]
		} else if m := memReName.FindStringSubmatch(rest); m != nil {
			name := unesc.Replace(m[1])
			preds = append(preds, func(f *memDriveFile) bool { return f.name == name })
			rest = rest[len(m[0]):]
		} else if m := memReProp.FindStringSubmatch(rest); m != nil {
			k, v := unesc.Replace(m[1]), unesc.Replace(m[2])
			preds = append(preds, func(f *memDriveFile) bool { return f.props[k] == v })
			rest = rest[len(m[0]):]
		} else {
			return nil, fmt.Errorf("unsupported q: %q", rest)
		}
		if rest == "" {
			break
		}
		var ok bool
		if rest, ok = strings.CutPrefix(rest, " and "); !ok || rest == "" {
			return nil, fmt.Errorf("bad q: %q", q)
		}
	}
	return func(f *memDriveFile) bool {
		for _, p := range preds {
			if !p(f) {
				return false
			}
		}
		return true
	}, nil
}

func plAddWorld(t *testing.T) (*fakeSpotify, *drive.Client) {
	t.Helper()
	setCLITestConfig(t)
	keyring.MockInit()
	if err := config.Save(&config.Config{DeviceID: "01TESTDEVICE00000000000000", GoogleEmail: "tai@example.com"}); err != nil {
		t.Fatal(err)
	}
	md := newMemDrive()
	dc := drive.New(inMemClient(md), "http://drive.test")
	origDrive := newDriveClient
	newDriveClient = func(context.Context) (*drive.Client, error) { return dc, nil }
	t.Cleanup(func() { newDriveClient = origDrive })

	fs := newFakeSpotify()
	spClient := inMemClient(fs.handler(t))
	origProv := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id == "spotify" {
			return spotify.New(spClient, "http://spotify.test"), nil
		}
		return nil, fmt.Errorf("provider %s not logged in", id)
	}
	t.Cleanup(func() { newProvider = origProv })
	return fs, dc
}

func plAddTwoPlatforms(t *testing.T) (fs1, fs2 *fakeSpotify, dc *drive.Client) {
	t.Helper()
	fs1, dc = plAddWorld(t)
	fs2 = newFakeSpotify()
	apClient := inMemClient(fs2.handler(t))
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		if id == "apple" {
			return spotify.New(apClient, "http://apple.test"), nil
		}
		return orig(ctx, id)
	}
	t.Cleanup(func() { newProvider = orig })
	return fs1, fs2, dc
}

// plAddMockProvider:供 --now 與 Apple catalog 補齊測試的輕量 provider 替身。
type plAddMockProvider struct {
	id      string
	display string
	caps    provider.Capability
	state   *provider.PlaybackState
	stErr   error
	search  func(context.Context, provider.Query) ([]provider.Track, error)
}

func (m *plAddMockProvider) ID() string                   { return m.id }
func (m *plAddMockProvider) DisplayName() string          { return m.display }
func (m *plAddMockProvider) Caps() provider.Capability    { return m.caps }
func (m *plAddMockProvider) Health(context.Context) error { return nil }

func (m *plAddMockProvider) State(context.Context) (*provider.PlaybackState, error) {
	if m.stErr != nil {
		return nil, m.stErr
	}
	return m.state, nil
}

var (
	_ provider.PlaybackController = (*plAddMockProvider)(nil)
	_ provider.Searcher           = (*plAddMockProvider)(nil)
)

func (m *plAddMockProvider) Play(context.Context, provider.PlayRequest) error { return nil }
func (m *plAddMockProvider) Pause(context.Context) error                      { return nil }
func (m *plAddMockProvider) Next(context.Context) error                       { return nil }
func (m *plAddMockProvider) Prev(context.Context) error                       { return nil }
func (m *plAddMockProvider) Seek(context.Context, int) error                  { return nil }
func (m *plAddMockProvider) SetVolume(context.Context, int) error             { return nil }
func (m *plAddMockProvider) Devices(context.Context) ([]provider.Device, error) {
	return nil, nil
}

func (m *plAddMockProvider) Search(ctx context.Context, q provider.Query) ([]provider.Track, error) {
	if m.search == nil {
		return nil, nil
	}
	return m.search(ctx, q)
}

// TestPlAddTrackByIDAppendsStrictlyAtTailAndHydratesDB:
// 決策 38 鐵則——既有 [z, a] 順序與 rank 一字不動,新曲目嚴格加在尾端(pos=2);
// 預設不碰平台;確認閘與 --dry-run 守 exit 2;COMMIT 後 state.db 與從 Drive 重建逐位元等價。
func TestPlAddTrackByIDAppendsStrictlyAtTailAndHydratesDB(t *testing.T) {
	fs, dc := plAddWorld(t)
	fs.set("p1", "通勤", "z", "a")
	fs.addCatalog(fakeCatalogTrack{ID: "m", Name: "song-m", Artist: "artist", ISRC: fakeISRC("m"), Dur: 210000})
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")

	beforePl := drivePlaylistNamed(t, dc, "通勤")
	beforeFiles := driveFiles(t, dc)

	wantRow := "add\tadd\tspotify\t通勤\t2\t" + fakeCID("m") + "\tm\tsong-m\tartist\t加進正本尾端\tadded_to_master\n"
	out, _, err := runPull(t, "pl", "add", "通勤", "spotify:m", "--dry-run")
	if exitOf(t, err) != 2 || out != wantRow {
		t.Fatalf("--dry-run 應回 exit 2 並印完整 TSV 列:err=%v\nout=%q\nwant=%q", err, out, wantRow)
	}
	if cols := strings.Split(strings.TrimSuffix(out, "\n"), "\t"); len(cols) != len(syncHeader) {
		t.Fatalf("TSV 欄數 %d ≠ syncHeader %d", len(cols), len(syncHeader))
	}
	if _, _, err := runPull(t, "pl", "add", "通勤", "spotify:m"); exitOf(t, err) != 2 {
		t.Fatalf("非 TTY 無 --yes 應回 exit 2:%v", err)
	}
	if !sameFiles(beforeFiles, driveFiles(t, dc)) || len(fs.written()) != 0 {
		t.Fatal("dry-run / 未確認不得寫 Drive 或平台")
	}

	// TTY 下 confirmWrite 取消 → exit 2、零寫入;同意(且帶 spotify:track:<id> URI)→ 寫入正本尾端。
	origBoth, origConfirm := bothTTY, confirmWrite
	t.Cleanup(func() { bothTTY, confirmWrite = origBoth, origConfirm })
	bothTTY = func(*cobra.Command) bool { return true }
	askedKey, askedPrompt := "", ""
	confirmWrite = func(key, prompt string) (bool, error) {
		askedKey, askedPrompt = key, prompt
		return false, nil
	}
	if _, _, err := runPull(t, "pl", "add", "通勤", "spotify:m"); exitOf(t, err) != 2 || askedKey != "pl.add.confirm" || !strings.Contains(askedPrompt, "song-m") {
		t.Fatalf("TTY 拒絕應回 exit 2 且 key=pl.add.confirm:err=%v key=%q prompt=%q", err, askedKey, askedPrompt)
	}
	if !sameFiles(beforeFiles, driveFiles(t, dc)) {
		t.Fatal("拒絕確認不得寫 Drive")
	}

	confirmWrite = func(string, string) (bool, error) { return true, nil }
	out, errs := mustPull(t, "pl", "add", "通勤", "spotify:track:m")
	if out != wantRow || !strings.Contains(errs, "已把「song-m」加進正本 通勤(") {
		t.Fatalf("寫入輸出不符:\nout=%q\nerrs=%q", out, errs)
	}

	afterPl := drivePlaylistNamed(t, dc, "通勤")
	if !slices.Equal(cidsOf(afterPl), []string{fakeCID("z"), fakeCID("a"), fakeCID("m")}) {
		t.Fatalf("必須嚴格加在尾端且不重排:%v", cidsOf(afterPl))
	}
	if afterPl.Items[0] != beforePl.Items[0] || afterPl.Items[1] != beforePl.Items[1] {
		t.Fatalf("既有曲目的 iid/rank/added_at 不得變動:before=%+v after=%+v", beforePl.Items[:2], afterPl.Items[:2])
	}
	if !slices.Equal(fs.tracksOf("p1"), []string{"z", "a"}) || len(fs.written()) != 0 {
		t.Fatalf("預設(無 --push)不得碰平台:%v writes=%v", fs.tracksOf("p1"), fs.written())
	}

	// state.db 重建等價:Hydrate 後的 db 與刪掉重拉(且後續 pull 零變更)必須逐位元一致。
	filesAfter, liveDump := driveFiles(t, dc), dumpBytes(t)
	deleteDB(t)
	if pullOut, _ := mustPull(t, "pl", "pull", "通勤", "--yes"); pullOut != "" {
		t.Fatalf("重建後 pull 應零變更:%q", pullOut)
	}
	if !sameFiles(filesAfter, driveFiles(t, dc)) || !bytes.Equal(liveDump, dumpBytes(t)) {
		t.Fatal("pl add 後 state.db 與從 Drive 重建不一致")
	}
}

// TestPlAddDuplicateDetectionAndAllowDup:
// 同 cid(含同 ID 或跨版本同 ISRC)預設印提示、exit 0、零寫入;--allow-dup 明確放行時加在尾端。
func TestPlAddDuplicateDetectionAndAllowDup(t *testing.T) {
	fs, dc := plAddWorld(t)
	fs.set("p1", "通勤", "a", "b")
	fs.addCatalog(fakeCatalogTrack{ID: "a", Name: "song-a", Artist: "artist", ISRC: fakeISRC("a")})
	fs.addCatalog(fakeCatalogTrack{ID: "a-remaster", Name: "song-a", Artist: "artist", ISRC: fakeISRC("a")})
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")
	before := driveFiles(t, dc)

	out, errs := mustPull(t, "pl", "add", "通勤", "spotify:a", "--yes")
	if out != "" || !strings.Contains(errs, "已在正本 通勤(") || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("同 ID 重複應 exit 0 零寫入:out=%q errs=%q", out, errs)
	}
	out, errs = mustPull(t, "pl", "add", "通勤", "spotify:a-remaster", "--yes")
	if out != "" || !strings.Contains(errs, "已在正本 通勤(") || !sameFiles(before, driveFiles(t, dc)) {
		t.Fatalf("同 ISRC 重複應 exit 0 零寫入:out=%q errs=%q", out, errs)
	}

	out, _ = mustPull(t, "pl", "add", "通勤", "spotify:a", "--allow-dup", "--yes")
	if !strings.Contains(out, "\t2\t"+fakeCID("a")+"\t") {
		t.Fatalf("--allow-dup 應加在 pos 2:%q", out)
	}
	if got := cidsOf(drivePlaylistNamed(t, dc, "通勤")); !slices.Equal(got, []string{fakeCID("a"), fakeCID("b"), fakeCID("a")}) {
		t.Fatalf("--allow-dup 後正本順序錯誤:%v", got)
	}
}

// TestPlAddNowFollowsPlayingProviderAndAppleCatalogEnrichment:
// --now 優先跟隨 Playing 平台、--provider 可釘死目標;macOS Apple Music 無 ProviderID 時透過 SearchSongs(歌名+歌手+時長 ±2s)補齊 catalog ID 與 ISRC;
// 無 Apple web token 時回清楚的 auth login apple 指引。
func TestPlAddNowFollowsPlayingProviderAndAppleCatalogEnrichment(t *testing.T) {
	fs, dc := plAddWorld(t)
	fs.set("p1", "通勤", "a")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")

	spPaused := &plAddMockProvider{
		id: "spotify", display: "Spotify",
		caps: provider.CapPlaybackControl,
		state: &provider.PlaybackState{
			Playing: false,
			Track:   &provider.Track{ProviderID: "sp-paused", Title: "Paused Song", Artists: []string{"Band"}, DurationMS: 180000, ISRC: "USSP00000001"},
		},
	}
	var gotQuery string
	apPlaying := &plAddMockProvider{
		id: "apple", display: "Apple Music",
		caps: provider.CapPlaybackControl | provider.CapSearch,
		state: &provider.PlaybackState{
			Playing: true,
			// macOS Music.app State():無 catalog ProviderID、無 ISRC。
			Track: &provider.Track{Title: "夜に駆ける", Artists: []string{"YOASOBI"}, Album: "THE BOOK", DurationMS: 261000},
		},
		search: func(_ context.Context, q provider.Query) ([]provider.Track, error) {
			gotQuery = q.Text
			return []provider.Track{
				// 時長差 > 2s → 淘汰
				{ProviderID: "wrong-dur", Title: "夜に駆ける", Artists: []string{"YOASOBI"}, Album: "THE BOOK", DurationMS: 270000, ISRC: "JPXX00000099"},
				// 時長在 ±2s 內但專輯不同 → 排在同專輯之後
				{ProviderID: "live-ver", Title: "夜に駆ける", Artists: []string{"YOASOBI"}, Album: "LIVE", DurationMS: 261200, ISRC: "JPXX00000002"},
				// 歌名、歌手、時長 ±2s 且同專輯 → 命中!
				{ProviderID: "1546069641", Title: "夜に駆ける", Artists: []string{"YOASOBI"}, Album: "THE BOOK", DurationMS: 261800, ISRC: "JPXX01900001"},
			}, nil
		},
	}

	origProv := newProvider
	newProvider = func(_ context.Context, id string) (provider.Provider, error) {
		switch id {
		case "spotify":
			return spPaused, nil
		case "apple":
			return apPlaying, nil
		default:
			return nil, errors.New("not configured")
		}
	}
	t.Cleanup(func() { newProvider = origProv })

	// 1. 未釘 --provider:default_provider=spotify 暫停中,apple 播放中 → 自動選 apple 並補齊 catalog ID + ISRC。
	out, _ := mustPull(t, "pl", "add", "通勤", "--now", "--yes")
	if gotQuery != "夜に駆ける YOASOBI" {
		t.Fatalf("SearchSongs 查詢字串不符:%q", gotQuery)
	}
	if !strings.Contains(out, "add\tadd\tapple\t通勤\t1\ti:JPXX01900001\t1546069641\t夜に駆ける\tYOASOBI\t") {
		t.Fatalf("應補齊 catalog ID 1546069641 與 ISRC JPXX01900001:\n%s", out)
	}

	// 2. --provider spotify 明確釘住暫停中的 spotify。
	out, _ = mustPull(t, "pl", "add", "通勤", "--now", "--provider", "spotify", "--yes")
	if !strings.Contains(out, "add\tadd\tspotify\t通勤\t2\ti:USSP00000001\tsp-paused\tPaused Song\t") {
		t.Fatalf("--provider spotify 應釘住 spotify:\n%s", out)
	}

	// 3. Apple 無 CapSearch 且未登入 Apple web token → 回明確引導 capy auth login apple,零寫入。
	beforeFiles := driveFiles(t, dc)
	apNoToken := &plAddMockProvider{
		id: "apple", display: "Apple Music",
		caps:  provider.CapPlaybackControl, // 無 CapSearch → 觸發 newAppleProvider 讀 keychain
		state: apPlaying.state,
	}
	newProvider = func(_ context.Context, id string) (provider.Provider, error) {
		if id == "apple" {
			return apNoToken, nil
		}
		return nil, errors.New("not configured")
	}
	_, _, err := runPull(t, "pl", "add", "通勤", "--now", "--provider", "apple", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "capy auth login apple") || !sameFiles(beforeFiles, driveFiles(t, dc)) {
		t.Fatalf("缺 Apple web token 應引導 auth login apple 且零寫入:%v", err)
	}

	// 4. Apple 搜尋無匹配(本機自匯入音檔)→ 回 apple_no_catalog_match 錯誤且零寫入。
	apNoMatch := &plAddMockProvider{
		id: "apple", display: "Apple Music",
		caps:   provider.CapPlaybackControl | provider.CapSearch,
		state:  apPlaying.state,
		search: func(context.Context, provider.Query) ([]provider.Track, error) { return nil, nil },
	}
	newProvider = func(_ context.Context, id string) (provider.Provider, error) {
		if id == "apple" {
			return apNoMatch, nil
		}
		return nil, errors.New("not configured")
	}
	_, _, err = runPull(t, "pl", "add", "通勤", "--now", "--yes")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "夜に駆ける") || !sameFiles(beforeFiles, driveFiles(t, dc)) {
		t.Fatalf("無 catalog 對應應回清楚錯誤且零寫入:%v", err)
	}
}

// TestPlAddPushTwoHalfModeAndSkipsReadOnlyOrUnaligned:
// --push 比照 pl dedup 雙半邊:先 pull 對齊已連結平台 → 正本尾端 Append → strict=false 推播可寫且有 base 的平台;
// 無 base 或唯讀平台只印 stderr 跳過 push 半邊,絕不阻斷正本 COMMIT(#9 2A)。
func TestPlAddPushTwoHalfModeAndSkipsReadOnlyOrUnaligned(t *testing.T) {
	fs1, fs2, dc := plAddTwoPlatforms(t)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		tr := fakeCatalogTrack{ID: id, Name: "song-" + id, Artist: "artist", ISRC: fakeISRC(id)}
		fs1.addCatalog(tr)
		fs2.addCatalog(tr)
	}
	// 先在另一張輔助清單觀測 c, d, e 在兩家平台的 ID(使 tracks.json 已有雙邊 mapping)。
	fs1.set("seed", "曲庫種子", "c", "d", "e")
	fs2.set("seed-ap", "曲庫種子", "c", "d", "e")
	mustPull(t, "pl", "link", "曲庫種子", "spotify:seed")
	mustPull(t, "pl", "link", "曲庫種子", "apple:seed-ap")
	mustPull(t, "pl", "sync", "曲庫種子", "--yes")

	fs1.set("p1", "通勤", "a", "b")
	fs2.set("q1", "通勤", "a", "b")
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "link", "通勤", "apple:q1")
	mustPull(t, "pl", "sync", "通勤", "--yes")

	// 模擬手機在 Spotify 先加了一首 c:pl add --push 先 pull 吸收 c,再把 d 加在尾端,最後把 c 推給 apple、把 d 推給兩家。
	fs1.set("p1", "通勤", "a", "b", "c")
	out, errs := mustPull(t, "pl", "add", "通勤", "spotify:d", "--push", "--yes")
	if !strings.Contains(out, "pull\tadd\tspotify\t通勤\t2\t"+fakeCID("c")) || !strings.Contains(out, "add\tadd\tspotify\t通勤\t3\t"+fakeCID("d")) {
		t.Fatalf("--push 應同時列出 pull 與 add:\n%s", out)
	}
	if !strings.Contains(errs, "已把「song-d」加進正本 通勤(") {
		t.Fatalf("stderr 應回報加入完成:%s", errs)
	}
	wantIDs := []string{"a", "b", "c", "d"}
	if !slices.Equal(fs1.tracksOf("p1"), wantIDs) || !slices.Equal(fs2.tracksOf("q1"), wantIDs) {
		t.Fatalf("兩家平台都應收斂為 [a b c d]:sp=%v ap=%v", fs1.tracksOf("p1"), fs2.tracksOf("q1"))
	}
	if out, errs := mustPull(t, "pl", "sync", "通勤", "--yes"); strings.TrimSpace(out) != "" || !strings.Contains(errs, "無變更") {
		t.Fatalf("--push 後再 sync 應零變更:out=%q errs=%q", out, errs)
	}

	// 將 apple 換成唯讀替身:strict=false 應在 stderr 提示跳過 apple push,但 spotify 與 Drive 正本照常寫入!
	orig := newProvider
	newProvider = func(ctx context.Context, id string) (provider.Provider, error) {
		p, err := orig(ctx, id)
		if id == "apple" && err == nil {
			return readOnlyProvider{p, p.(provider.PlaylistReader)}, nil
		}
		return p, err
	}
	t.Cleanup(func() { newProvider = orig })

	out, errs = mustPull(t, "pl", "add", "通勤", "spotify:e", "--push", "--yes")
	if !slices.Equal(dirActions(out), []string{"add add spotify", "push add spotify"}) {
		t.Fatalf("唯讀平台應跳過 push 半邊、不阻斷可寫平台與正本:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(errs), "apple") || !slices.Equal(fs1.tracksOf("p1"), []string{"a", "b", "c", "d", "e"}) {
		t.Fatalf("stderr 應說明跳過 apple 且 spotify 已更新:errs=%q sp=%v", errs, fs1.tracksOf("p1"))
	}
	if got := cidsOf(drivePlaylistNamed(t, dc, "通勤")); len(got) != 5 || got[4] != fakeCID("e") {
		t.Fatalf("正本應照常 COMMIT 第 5 首:%v", got)
	}
}

// TestPlAddInteractivePickerAndWebSeams:
// 非 TTY 缺清單名必報錯;TTY 下缺清單名走 pickCanonicalPlaylist(預設委派 pickOne);
// installWebSeams 必須替換並還原 pickCanonicalPlaylist 接縫。
func TestPlAddInteractivePickerAndWebSeams(t *testing.T) {
	fs, dc := plAddWorld(t)
	fs.set("p1", "通勤", "a")
	fs.set("p2", "睡前", "b")
	fs.addCatalog(fakeCatalogTrack{ID: "m", Name: "song-m", Artist: "artist", ISRC: fakeISRC("m")})
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "link", "睡前", "spotify:p2")
	mustPull(t, "pl", "pull", "--all", "--yes")

	if _, _, err := runPull(t, "pl", "add", "spotify:m", "--yes"); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "缺正本清單名稱或 pid") {
		t.Fatalf("非 TTY 省清單名應回 need_playlist:%v", err)
	}
	if _, _, err := runPull(t, "pl", "add", "--now", "--yes"); exitOf(t, err) != 1 || !strings.Contains(err.Error(), "缺正本清單名稱或 pid") {
		t.Fatalf("非 TTY --now 省清單名應回 need_playlist:%v", err)
	}

	origInter, origBoth, origPick := isInteractive, bothTTY, pickOne
	t.Cleanup(func() { isInteractive, bothTTY, pickOne = origInter, origBoth, origPick })
	isInteractive = func(*cobra.Command) bool { return true }
	bothTTY = func(*cobra.Command) bool { return true }

	var gotLabels []string
	pickOne = func(_ string, labels []string) (int, error) {
		gotLabels = labels
		for i, l := range labels {
			if strings.HasPrefix(l, "睡前") {
				return i, nil
			}
		}
		return 0, nil
	}
	mustPull(t, "pl", "add", "spotify:m", "--yes")
	if len(gotLabels) != 2 {
		t.Fatalf("挑選器應列出 2 個正本清單:%v", gotLabels)
	}
	if got := cidsOf(drivePlaylistNamed(t, dc, "睡前")); !slices.Equal(got, []string{fakeCID("b"), fakeCID("m")}) {
		t.Fatalf("挑選「睡前」後應寫入睡前尾端:%v", got)
	}

	// 驗 installWebSeams 替換與還原 pickCanonicalPlaylist 指標。
	beforePtr := reflect.ValueOf(pickCanonicalPlaylist).Pointer()
	srv, err := newWebServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restore := installWebSeams(srv)
	duringPtr := reflect.ValueOf(pickCanonicalPlaylist).Pointer()
	restore()
	afterPtr := reflect.ValueOf(pickCanonicalPlaylist).Pointer()
	if duringPtr == beforePtr {
		t.Fatal("installWebSeams 必須替換 pickCanonicalPlaylist")
	}
	if afterPtr != beforePtr {
		t.Fatal("restore() 必須還原 pickCanonicalPlaylist")
	}
}

// TestPlAddEnglishI18n:英文語系下 --help、TSV 理由、TTY 確認詞、完成訊息與錯誤訊息皆零 CJK。
func TestPlAddEnglishI18n(t *testing.T) {
	fs, _ := plAddWorld(t)
	withLanguage(t, "en")
	fs.set("p1", "Commute", "a")
	fs.addCatalog(fakeCatalogTrack{ID: "m", Name: "Song M", Artist: "Artist", ISRC: fakeISRC("m")})
	mustPull(t, "pl", "link", "Commute", "spotify:p1")
	mustPull(t, "pl", "pull", "Commute", "--yes")

	helpOut, err := runCLI(t, "pl", "add", "--help")
	if err != nil || hasCJK(helpOut) || !strings.Contains(helpOut, "Appends a single track") {
		t.Fatalf("英文 --help 不應含 CJK:err=%v\n%s", err, helpOut)
	}

	origBoth, origConfirm := bothTTY, confirmWrite
	t.Cleanup(func() { bothTTY, confirmWrite = origBoth, origConfirm })
	bothTTY = func(*cobra.Command) bool { return true }
	var prompt string
	confirmWrite = func(_, p string) (bool, error) { prompt = p; return true, nil }

	out, errs := mustPull(t, "pl", "add", "Commute", "spotify:m")
	if hasCJK(out) || hasCJK(errs) || hasCJK(prompt) || !strings.Contains(out, "added to the master copy\tadded_to_master") {
		t.Fatalf("英文執行輸出不應含 CJK:\nout=%q\nerrs=%q\nprompt=%q", out, errs, prompt)
	}
	_, dupErrs := mustPull(t, "pl", "add", "Commute", "spotify:m", "--yes")
	if hasCJK(dupErrs) || !strings.Contains(dupErrs, "already in") {
		t.Fatalf("英文重複提示不應含 CJK:%q", dupErrs)
	}
	if _, _, err := runPull(t, "pl", "add", "spotify:m", "--now"); err == nil || hasCJK(err.Error()) {
		t.Fatalf("英文錯誤訊息不應含 CJK:%v", err)
	}
}

// TestPlAddVersionGuardBlocksLostUpdate:FETCH 與 COMMIT 之間若別台裝置改了 Drive 檔案,guardVersions 必擋下(exit 1、零覆蓋)。
func TestPlAddVersionGuardBlocksLostUpdate(t *testing.T) {
	fs, dc := plAddWorld(t)
	fs.set("p1", "通勤", "a")
	fs.addCatalog(fakeCatalogTrack{ID: "m", Name: "song-m", Artist: "artist", ISRC: fakeISRC("m")})
	mustPull(t, "pl", "link", "通勤", "spotify:p1")
	mustPull(t, "pl", "pull", "通勤", "--yes")

	origBoth, origConfirm := bothTTY, confirmWrite
	t.Cleanup(func() { bothTTY, confirmWrite = origBoth, origConfirm })
	bothTTY = func(*cobra.Command) bool { return true }

	var otherDeviceFiles map[string][]byte
	confirmWrite = func(string, string) (bool, error) {
		pl := drivePlaylistNamed(t, dc, "通勤")
		pl.Links["apple"] = "ap-other"
		putPlaylist(t, dc, pl)
		otherDeviceFiles = driveFiles(t, dc)
		return true, nil
	}
	_, _, err := runPull(t, "pl", "add", "通勤", "spotify:m")
	if exitOf(t, err) != 1 || !strings.Contains(err.Error(), "被別台裝置改過") || !sameFiles(otherDeviceFiles, driveFiles(t, dc)) {
		t.Fatalf("版本守衛應擋下並保留對方寫入:err=%v", err)
	}
}
