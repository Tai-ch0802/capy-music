// Package canon 是 Drive 上 source of truth 的資料模型(spec §6.2 / §6.3):manifest、tracks、每個 playlist、
// 每台裝置自己的 base。只有型別、編解碼、cid、base 合併與 rank;不碰 Drive、不碰 SQLite、不做 DERIVE(T6 / T7)。
//
// 時間一律 unix 秒(spec 範例的單位)。同秒的兩次觀測靠 device_id 決勝,見 MergeBase。
package canon

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/ulid"
)

// SchemaVersion:每個檔案頂層都有;讀時忽略未知欄位,高於這個值就拒絕(Decode)。
// 2(2026-09-08,P4 T2a,決策 20):mappings 從字串改物件;所有檔種共用同一個常數,所以一起跳版。Encode 一律蓋成目前值
// (Decode 保留檔案原值,不蓋的話 v1 檔重編出去還是寫 1,舊 binary 會拿到 JSON 型別錯誤而不是 ErrSchemaTooNew)。
const SchemaVersion = 2

// 測試替換點:observed_at / updated_at / added_at / iid 全由這兩個衍生,沒有替換點的話逐位元相等的測試不可能穩定。
var (
	Now     = time.Now
	NewULID = ulid.New
)

var ErrSchemaTooNew = errors.New("Drive 上的檔案 schema 比這個 capy 新,請先 capy update")

// Manifest:manifest.json。playlists 是 Drive 上應該存在的 pl__<pid>.json(2026-09-08 T8 加):pull 的閘用
// 「manifest 宣告、但 Drive 取不到」偵測部分遺失(spec §6.3)。last_compaction 隨 op log 佈局待 P5,先不放。
type Manifest struct {
	SchemaVersion int      `json:"schema_version"`
	Devices       []Device `json:"devices"`
	Playlists     []string `json:"playlists"` // pid,排序去重
}

type Device struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	LastSeen int64  `json:"last_seen"`
}

func NewManifest() *Manifest {
	return &Manifest{SchemaVersion: SchemaVersion, Devices: []Device{}, Playlists: []string{}}
}

// AddPlaylist 宣告 pid 的檔存在;回傳是否為新宣告。P3 沒有刪清單的命令,所以只加不減。
func (m *Manifest) AddPlaylist(pid string) bool {
	defer m.normalize()
	if slices.Contains(m.Playlists, pid) {
		return false
	}
	m.Playlists = append(m.Playlists, pid)
	return true
}

// Touch 註冊或更新裝置(last_seen = Now);回傳是否為新裝置。裝置只能由使用者用 device forget 移除,這裡不刪。
func (m *Manifest) Touch(id, name string) bool {
	defer m.normalize()
	now := Now().Unix()
	for i := range m.Devices {
		if m.Devices[i].ID == id {
			m.Devices[i].Name, m.Devices[i].LastSeen = name, now
			return false
		}
	}
	m.Devices = append(m.Devices, Device{ID: id, Name: name, LastSeen: now})
	return true
}

// normalize:devices 依 ID 排序、playlists 排序去重——manifest 是共用檔,兩台裝置註冊順序不同不能得到不同位元組。
func (m *Manifest) normalize() {
	m.SchemaVersion = SchemaVersion
	if m.Devices == nil {
		m.Devices = []Device{}
	}
	slices.SortFunc(m.Devices, func(a, b Device) int { return strings.Compare(a.ID, b.ID) })
	if m.Playlists == nil {
		m.Playlists = []string{}
	}
	slices.Sort(m.Playlists)
	m.Playlists = slices.Compact(m.Playlists)
}

// Tracks:tracks.json,cid → 曲目。
type Tracks struct {
	SchemaVersion int              `json:"schema_version"`
	Tracks        map[string]Track `json:"tracks"`
}

func NewTracks() *Tracks { return &Tracks{SchemaVersion: SchemaVersion, Tracks: map[string]Track{}} }

func (t *Tracks) normalize() {
	t.SchemaVersion = SchemaVersion
	if t.Tracks == nil {
		t.Tracks = map[string]Track{}
	}
	for cid, tr := range t.Tracks {
		if tr.Artists == nil || tr.Mappings == nil { // "artists":null 與 [] 是兩串不同位元組;mappings 同理
			tr.Artists, tr.Mappings = nonNilStrings(tr.Artists), nonNilMappings(tr.Mappings)
			t.Tracks[cid] = tr
		}
	}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilMappings(m map[string]Mapping) map[string]Mapping {
	if m == nil {
		return map[string]Mapping{}
	}
	return m
}

// Track:canonical 曲目(spec §6.2)。Mappings 是 provider → Mapping(決策 20;P3 是純字串 id,Decode 相容)。
type Track struct {
	CID        string             `json:"cid"`
	ISRC       []string           `json:"isrc,omitempty"` // alias set:只在人工操作(accept / pin / 合併)時成長(決策 19);觀測與自動 mapping 不碰
	Title      string             `json:"title"`
	Artists    []string           `json:"artists"`
	Album      string             `json:"album,omitempty"`
	DurationMS int                `json:"duration_ms"`
	Mappings   map[string]Mapping `json:"mappings"`
	Conflicts  []Conflict         `json:"conflicts,omitempty"`
}

// Mapping 的來源。
const (
	SourceObserved = "observed" // pull 時在平台清單裡看到:id 確定,「是不是同一錄音」由 ISRC 決定
	SourceISRC     = "isrc"     // resolver Layer 1(ISRC 反查)
	SourceFuzzy    = "fuzzy"    // resolver Layer 2
	SourceReview   = "review"   // 人工:review accept / resolve pin / 合併
)

// Mapping:cid 在某個 provider 的對應(決策 20)。Confidence 0–100 整數(浮點會讓 Encode 的位元組不決定性);
// Pinned 只有 Source review 會是 true;Pinned 且 ID 空 = 使用者裁定「這個平台沒有這首」。
// UpdatedAt 只在 (ID, Confidence, Pinned, Source) 真的改變時更新——否則每次 pull 都會重傳整份 tracks.json。
type Mapping struct {
	ID         string `json:"id"`
	Confidence int    `json:"confidence"`
	Pinned     bool   `json:"pinned"`
	Source     string `json:"source"`
	UpdatedAt  int64  `json:"updated_at"`
}

// Same:等價比較不看 UpdatedAt(兩台裝置各自算出同一結果不該互相 LWW 覆蓋)。
func (m Mapping) Same(o Mapping) bool {
	return m.ID == o.ID && m.Confidence == o.Confidence && m.Pinned == o.Pinned && m.Source == o.Source
}

// UnmarshalJSON 相容 schema 1 的字串舊形("spotify": "id"):視為 observed / 100 / 不 pinned / updated_at 0。寫出永遠是物件。
func (m *Mapping) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var id string
		if err := json.Unmarshal(b, &id); err != nil {
			return err
		}
		*m = Mapping{ID: id, Confidence: 100, Source: SourceObserved}
		return nil
	}
	type raw Mapping // 去掉方法,免得遞迴
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	*m = Mapping(r)
	return nil
}

// Conflict:同 cid 但 metadata 不符的觀測;P3 只記錄不裁決(review queue 是 P4)。
type Conflict struct {
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
	Title      string `json:"title"`
	DurationMS int    `json:"duration_ms"`
}

// Playlist:pl__<pid>.json。Items 以 iid 為鍵、cid 只是屬性(決策 13),同一首可出現多次;順序看 Rank。
type Playlist struct {
	SchemaVersion int               `json:"schema_version"`
	PID           string            `json:"pid"`
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	UpdatedAt     int64             `json:"updated_at"`
	Items         []Item            `json:"items"`
	Links         map[string]string `json:"links"` // 不 omitempty:空清單解回來會是 nil map,第一次 Links[p] = id 就 panic
}

type Item struct {
	IID     string `json:"iid"`
	CID     string `json:"cid"`
	Rank    string `json:"rank"`
	AddedAt int64  `json:"added_at"`
}

func NewPlaylist(name string) *Playlist {
	return &Playlist{SchemaVersion: SchemaVersion, PID: NewULID(), Name: name, UpdatedAt: Now().Unix(), Items: []Item{}, Links: map[string]string{}}
}

// normalize:items 依 (rank, iid) 排序——順序就是 rank(spec §6.2),檔案與 SQLite 鏡像(store.Dump)才會逐位元一致。
func (p *Playlist) normalize() {
	p.SchemaVersion = SchemaVersion
	if p.Items == nil {
		p.Items = []Item{}
	}
	if p.Links == nil {
		p.Links = map[string]string{}
	}
	slices.SortStableFunc(p.Items, func(a, b Item) int {
		if c := strings.Compare(a.Rank, b.Rank); c != 0 {
			return c
		}
		return strings.Compare(a.IID, b.IID)
	})
}

// Append 在最後一筆之後加一個 item(rank 取現有最大者之後,不假設 Items 已排序)。
func (p *Playlist) Append(cid string) (Item, error) {
	last := ""
	for _, it := range p.Items {
		if it.Rank > last {
			last = it.Rank
		}
	}
	r, err := RankBetween(last, "")
	if err != nil {
		return Item{}, err
	}
	now := Now().Unix()
	it := Item{IID: NewULID(), CID: cid, Rank: r, AddedAt: now}
	p.Items = append(p.Items, it)
	p.UpdatedAt = now
	return it, nil
}

// DeviceState:dev__<device_id>.json,每台裝置只寫自己的檔,所以永不衝突(spec §6.3)。
type DeviceState struct {
	SchemaVersion int                        `json:"schema_version"`
	DeviceID      string                     `json:"device_id"`
	Base          map[string]map[string]Base `json:"base"` // pid → provider → 上次觀測
}

// Base:DERIVE 的三方比對基準(spec §6.5 步驟 3:diff(base, 平台現況))。
type Base struct {
	Snapshot   Snapshot `json:"snapshot"`
	ObservedAt int64    `json:"observed_at"`
}

// Snapshot 是平台清單的觀測原文:名稱、依平台順序的 provider 曲目 id,以及觀測當時依 §6.2 算出的 cid(與 Items 對齊)。
// cid 由 (provider id, ISRC) 決定、不隨 mapping 變,所以它仍是「觀測」而非「解析結果」;DERIVE 的移除計數靠它——
// 平台把曲目重新連結成另一個版本(X → Y,同 ISRC)之後再刪除,mapping 還是 X,只靠 id 反查會永遠刪不掉。
// ID 是被觀測的平台清單 id(2026-09-08 T8 加):base 只對「目前連結的那個平台清單」有效,unlink 後改連別的清單,
// 舊 base 不能拿來算移除。
type Snapshot struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Items []string `json:"items"`
	CIDs  []string `json:"cids"`
}

func NewDeviceState(deviceID string) *DeviceState {
	return &DeviceState{SchemaVersion: SchemaVersion, DeviceID: deviceID, Base: map[string]map[string]Base{}}
}

func (d *DeviceState) normalize() {
	d.SchemaVersion = SchemaVersion
	if d.Base == nil {
		d.Base = map[string]map[string]Base{}
	}
}

// SetBase 記下本裝置對 (pid, provider) 的最新觀測。
func (d *DeviceState) SetBase(pid, provider string, s Snapshot) {
	d.normalize()
	if d.Base[pid] == nil {
		d.Base[pid] = map[string]Base{}
	}
	if s.Items == nil {
		s.Items = []string{}
	}
	if s.CIDs == nil {
		s.CIDs = []string{}
	}
	d.Base[pid][provider] = Base{Snapshot: s, ObservedAt: Now().Unix()}
}

// normalizer:Decode 收尾與 Encode 開頭都會呼叫——缺欄位 / null 補成空容器(舊版或手改的檔不能讓下一次賦值 panic),
// 可排序的欄位排成決定性順序。
type normalizer interface{ normalize() }

// Encode:緊湊 JSON 加換行。encoding/json 對 map 鍵排序、struct 欄位序固定,所以同一狀態永遠同一串位元組。
// 會**原地** normalize 傳進來的物件(補空容器、items 依 rank 排):要驗某個順序,得在 Encode 之前看。
func Encode(v any) ([]byte, error) {
	if n, ok := v.(normalizer); ok {
		n.normalize()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// CheckSchema 只看 schema_version(缺 → 錯;高於支援 → ErrSchemaTooNew),不解整份;給只想知道「能不能碰這個檔」的人用。
func CheckSchema(b []byte) error {
	var head struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	if head.SchemaVersion == 0 {
		return errors.New("檔案缺 schema_version")
	}
	if head.SchemaVersion > SchemaVersion {
		return fmt.Errorf("%w(檔案 %d,支援 %d)", ErrSchemaTooNew, head.SchemaVersion, SchemaVersion)
	}
	return nil
}

// Decode 先看 schema_version:缺 → 錯;高於支援 → ErrSchemaTooNew(拒寫的閘就在這裡:讀不進來就寫不出去);
// 未知欄位一律忽略(新版 capy 加的欄位,舊版不能因此壞掉)。
func Decode[T any](b []byte) (*T, error) {
	if err := CheckSchema(b); err != nil {
		return nil, err
	}
	v := new(T)
	if err := json.Unmarshal(b, v); err != nil {
		return nil, err
	}
	if n, ok := any(v).(normalizer); ok {
		n.normalize()
	}
	return v, nil
}
