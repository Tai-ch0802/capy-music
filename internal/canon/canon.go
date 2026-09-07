// Package canon 是 Drive 上 source of truth 的資料模型(spec §6.2 / §6.3):manifest、tracks、每個 playlist、
// 每台裝置自己的 base。只有型別、編解碼、cid、base 合併與 rank;不碰 Drive、不碰 SQLite、不做 DERIVE(T6 / T7)。
//
// 時間一律 unix 秒(spec 範例的單位)。同秒的兩次觀測靠 device_id 決勝,見 MergeBase。
package canon

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/ulid"
)

// SchemaVersion:每個檔案頂層都有;讀時忽略未知欄位,高於這個值就拒絕(Decode)。
const SchemaVersion = 1

// 測試替換點:observed_at / updated_at / added_at / iid 全由這兩個衍生,沒有替換點的話逐位元相等的測試不可能穩定。
var (
	Now     = time.Now
	NewULID = ulid.New
)

var ErrSchemaTooNew = errors.New("Drive 上的檔案 schema 比這個 capy 新,請先 capy update")

// Manifest:manifest.json。last_compaction 隨 op log 佈局待 P5(spec §6.3),先不放。
type Manifest struct {
	SchemaVersion int      `json:"schema_version"`
	Devices       []Device `json:"devices"`
}

type Device struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	LastSeen int64  `json:"last_seen"`
}

func NewManifest() *Manifest { return &Manifest{SchemaVersion: SchemaVersion, Devices: []Device{}} }

// Touch 註冊或更新裝置(last_seen = Now);回傳是否為新裝置。裝置只能由使用者用 device forget 移除,這裡不刪。
func (m *Manifest) Touch(id, name string) bool {
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

// Tracks:tracks.json,cid → 曲目。
type Tracks struct {
	SchemaVersion int              `json:"schema_version"`
	Tracks        map[string]Track `json:"tracks"`
}

func NewTracks() *Tracks { return &Tracks{SchemaVersion: SchemaVersion, Tracks: map[string]Track{}} }

// Track:canonical 曲目(spec §6.2)。Mappings 是 provider → provider_id,P3 不帶 confidence / pinned。
type Track struct {
	CID        string            `json:"cid"`
	ISRC       []string          `json:"isrc,omitempty"`
	Title      string            `json:"title"`
	Artists    []string          `json:"artists"`
	Album      string            `json:"album,omitempty"`
	DurationMS int               `json:"duration_ms"`
	Mappings   map[string]string `json:"mappings"`
	Conflicts  []Conflict        `json:"conflicts,omitempty"`
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
	Links         map[string]string `json:"links,omitempty"`
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

// Snapshot 是平台清單的觀測原文:名稱與依平台順序排列的 provider 曲目 id。存 provider id 不存 cid——
// mapping 會變,觀測不該跟著變。
type Snapshot struct {
	Name  string   `json:"name"`
	Items []string `json:"items"`
}

func NewDeviceState(deviceID string) *DeviceState {
	return &DeviceState{SchemaVersion: SchemaVersion, DeviceID: deviceID, Base: map[string]map[string]Base{}}
}

// SetBase 記下本裝置對 (pid, provider) 的最新觀測。
func (d *DeviceState) SetBase(pid, provider string, s Snapshot) {
	if d.Base[pid] == nil {
		d.Base[pid] = map[string]Base{}
	}
	if s.Items == nil {
		s.Items = []string{}
	}
	d.Base[pid][provider] = Base{Snapshot: s, ObservedAt: Now().Unix()}
}

// Encode:緊湊 JSON 加換行。encoding/json 對 map 鍵排序、struct 欄位序固定,所以同一狀態永遠同一串位元組。
func Encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Decode 先看 schema_version:缺 → 錯;高於支援 → ErrSchemaTooNew(拒寫的閘就在這裡:讀不進來就寫不出去);
// 未知欄位一律忽略(新版 capy 加的欄位,舊版不能因此壞掉)。
func Decode[T any](b []byte) (*T, error) {
	var head struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return nil, err
	}
	if head.SchemaVersion == 0 {
		return nil, errors.New("檔案缺 schema_version")
	}
	if head.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("%w(檔案 %d,支援 %d)", ErrSchemaTooNew, head.SchemaVersion, SchemaVersion)
	}
	v := new(T)
	if err := json.Unmarshal(b, v); err != nil {
		return nil, err
	}
	return v, nil
}
