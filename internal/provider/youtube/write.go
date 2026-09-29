package youtube

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// 寫端(決策 60,T2;計畫 §3.3、§4 補測):
//   - 建清單走 playlist/create(PRIVATE),再輪詢列表到出現(真帳號 3 s;Apple 同一套退避與逾時契約)。
//   - 所有會讓整輪放棄的檢查(可編輯、重讀對齊)都在第一個寫入之前,而且是**同一次**重讀:不然 rename 已落地、items 才拒絕。
//   - **整輪就是一個 edit_playlist 請求**:ACTION_SET_PLAYLIST_NAME(有改名時)+ 純尾端 append(want 的前綴 = current)的每首 ACTION_ADD_VIDEO
//     (帶 DEDUPE_OPTION_SKIP,決策 38 的重複要留得住),或其他形狀的 ACTION_REMOVE_VIDEO 每一列(靠重讀拿到的 setVideoId,同一首兩列各自不同)
//     + ACTION_ADD_VIDEO 照 want 順序 = Spotify PUT / Apple PUT 同款的整批取代;加入日期會重設。
//     2026-09-29 真帳號:478 / 640 / 2000 / 5000 首(5000 = YouTube 的清單上限,10000 個 action、143 s)都是一個請求 STATUS_SUCCEEDED、順序正確;
//     夾一個壞 id 是 HTTP 400 且清單與名稱都原封不動(rename 在同一個請求裡也一起被拒,PR #118 review 第 1 點)→ **原子**:不分批、
//     沒有 PartialWriteError、也沒有「名字改了曲目沒寫」的中間狀態;失敗回普通 error,push.go 當「平台沒動」就是事實。
//   - 只寫自己建的清單(決策 60):可編輯 header 子樹裡的擁有者頻道 id 要等於自己的(別人建、自己是協作者的清單 header 也可能是可編輯的,
//     PR #118 review 第 2 點);plan 階段的 PlaylistRef.Unwritable 是第一道,這裡是第二道。
//   - 灰掉(下架)的列 ADD 得回去(補測),留在 current / want 裡沒問題。
//   - 絕不送 playlist/delete。

var (
	_ provider.PlaylistWriter  = (*Provider)(nil)
	_ provider.PlaylistCreator = (*Provider)(nil)
)

// createPollDelays:建清單後輪詢列表的間隔——退避 1 → 2 → 4 → 8 → 15 s(共 30 s;真帳號量到 3 s)。測試替換點。
var createPollDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second}

// Pushable:videoId 都推得動(下架的列也 ADD 得回去);只有空 id 不行。
func (p *Provider) Pushable(id string) bool { return id != "" }

// CreatePlaylist:建一個私人空清單,輪詢到列表出現才回傳(pull 的 gone 判準看列表);逾時回錯並帶 id——不能默默回成功,
// 不然 migrate 接著 observe 會把它當 gone。
func (p *Provider) CreatePlaylist(ctx context.Context, name string) (provider.PlaylistRef, error) {
	root, err := p.c.post(ctx, "playlist/create", map[string]any{"title": name, "privacyStatus": "PRIVATE"}, "")
	if err != nil {
		return provider.PlaylistRef{}, err
	}
	plid := root.get("playlistId").str()
	if plid == "" {
		return provider.PlaylistRef{}, i18n.Errorf("youtube.client.err.create_no_id")
	}
	ref := provider.PlaylistRef{ID: p.idOf(plid), Name: name, Total: 0}
	var total time.Duration
	for _, d := range createPollDelays {
		total += d
	}
	for attempt := 0; ; attempt++ {
		refs, err := p.ListPlaylists(ctx)
		if err != nil {
			return ref, i18n.Errorf("youtube.client.err.create_list_failed", "name", name, "id", ref.ID, "err", err)
		}
		if slices.ContainsFunc(refs, func(r provider.PlaylistRef) bool { return r.ID == ref.ID }) {
			return ref, nil
		}
		if attempt >= len(createPollDelays) {
			return ref, i18n.Errorf("youtube.client.err.create_not_listed", "name", name, "id", ref.ID, "wait", total)
		}
		if attempt == 0 { // 安靜等待要有一句話;走 BackoffStderr 接縫,web 模式也看得到
			fmt.Fprintln(provider.BackoffStderr, i18n.T("youtube.client.create.waiting", "id", ref.ID))
		}
		if err := provider.Wait(ctx, createPollDelays[attempt]); err != nil {
			return ref, i18n.Errorf("youtube.client.err.create_interrupted", "name", name, "id", ref.ID, "err", err)
		}
	}
}

// ApplyOps:provider.PlaylistWriter(見檔頭)。
func (p *Provider) ApplyOps(ctx context.Context, id string, current []string, ops []provider.PlaylistOp) ([]provider.PlaylistOp, error) {
	want, name, err := provider.ApplyPlaylistOps(current, ops)
	if err != nil {
		return nil, err
	}
	itemsChanged := !slices.Equal(want, current)
	if !itemsChanged && name == "" {
		return nil, nil
	}
	if itemsChanged {
		for i, v := range want {
			if v == "" {
				return nil, i18n.Errorf("youtube.err.empty_track_id", "pos", i+1)
			}
		}
	}
	plid, err := p.own(id)
	if err != nil {
		return nil, err
	}
	// 一次重讀:可編輯 + 擁有者 + 對齊,都在唯一的寫入之前(這次重讀就是 §6.5.2 規則 6 的併發比對)。
	rows, meta, err := p.playlistRows(ctx, id)
	if err != nil {
		return nil, err
	}
	if !meta.editable { // 第二道防線:plan 階段的 PlaylistRef.Unwritable 已擋過一次(別人的、自動清單)
		return nil, i18n.Errorf("youtube.err.not_editable", "id", plid)
	}
	if !slices.Contains(meta.owners, p.channelID) { // 可編輯但不是自己建的(協作者):不寫
		return nil, i18n.Errorf("youtube.err.not_owner", "id", plid)
	}
	live := make([]string, len(rows))
	for i, r := range rows {
		live[i] = r.videoID
	}
	if !slices.Equal(live, current) {
		return nil, i18n.Errorf("youtube.err.changed_since_read", "id", plid)
	}
	n := len(current)
	appendOnly := itemsChanged && len(want) > n && slices.Equal(want[:n], current)
	if itemsChanged && !appendOnly {
		for i, r := range rows {
			if r.setVideoID == "" { // 沒有列 id 就移不掉那一列:零寫入,不硬做
				return nil, i18n.Errorf("youtube.err.no_set_video_id", "id", plid, "pos", i+1)
			}
		}
	}
	var actions []map[string]any
	if name != "" {
		actions = append(actions, map[string]any{"action": "ACTION_SET_PLAYLIST_NAME", "playlistName": name})
	}
	switch {
	case !itemsChanged:
	case appendOnly:
		actions = append(actions, addActions(want[n:])...)
	default:
		for _, r := range rows {
			actions = append(actions, map[string]any{"action": "ACTION_REMOVE_VIDEO", "setVideoId": r.setVideoID, "removedVideoId": r.videoID})
		}
		actions = append(actions, addActions(want)...)
	}
	if err := p.c.editPlaylist(ctx, plid, actions); err != nil {
		return nil, i18n.Errorf("youtube.err.write_failed", "id", plid, "err", err)
	}
	return nil, nil
}

func addActions(ids []string) []map[string]any {
	out := make([]map[string]any, len(ids))
	for i, v := range ids {
		out[i] = map[string]any{"action": "ACTION_ADD_VIDEO", "addedVideoId": v, "dedupeOption": "DEDUPE_OPTION_SKIP"}
	}
	return out
}
