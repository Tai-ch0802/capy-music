package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Tai-ch0802/capy-music/internal/cache"
	"github.com/Tai-ch0802/capy-music/internal/canon"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

type deckFakeController struct {
	watchFake
	lastPlay  provider.PlayRequest
	devices   []provider.Device
	transfers []string
	shuffles  []bool
	repeats   []provider.RepeatMode
}

func (f *deckFakeController) Play(_ context.Context, r provider.PlayRequest) error {
	f.lastPlay = r
	f.calls = append(f.calls, "play")
	return nil
}

func (f *deckFakeController) Devices(context.Context) ([]provider.Device, error) {
	return f.devices, nil
}

func (f *deckFakeController) Transfer(_ context.Context, deviceID string, play bool) error {
	mode := "pause"
	if play {
		mode = "play"
	}
	f.transfers = append(f.transfers, deviceID+":"+mode)
	return nil
}

func (f *deckFakeController) SetShuffle(_ context.Context, on bool) error {
	f.shuffles = append(f.shuffles, on)
	return nil
}

func (f *deckFakeController) SetRepeat(_ context.Context, mode provider.RepeatMode) error {
	f.repeats = append(f.repeats, mode)
	return nil
}

func seedTestCanonicalDB(t *testing.T) {
	t.Helper()
	s, err := store.Open(tuiMenuStoreBusy)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	tracks := canon.NewTracks()
	tracks.Tracks["i:USRC10000001"] = canon.Track{
		CID: "i:USRC10000001", Title: "First Song", Artists: []string{"Artist A"}, DurationMS: 180000,
		Mappings: map[string]canon.Mapping{"spotify": {ID: "sp_1", Confidence: 100, Source: canon.SourceISRC}},
	}
	tracks.Tracks["i:USRC10000002"] = canon.Track{
		CID: "i:USRC10000002", Title: "Pinned Unavailable", Artists: []string{"Artist B"}, DurationMS: 200000,
		Mappings: map[string]canon.Mapping{"spotify": {ID: "", Confidence: 0, Pinned: true, Source: canon.SourceReview}},
	}
	tracks.Tracks["i:USRC10000003"] = canon.Track{
		CID: "i:USRC10000003", Title: "Third Song", Artists: []string{"Artist C"}, DurationMS: 210000,
		Mappings: map[string]canon.Mapping{"spotify": {ID: "sp_3", Confidence: 100, Source: canon.SourceISRC}},
	}
	tracks.Tracks["i:USRC10000004"] = canon.Track{
		CID: "i:USRC10000004", Title: "Merged Winner", Artists: []string{"Artist D"}, DurationMS: 195000,
		Mappings: map[string]canon.Mapping{"spotify": {ID: "sp_4", Confidence: 100, Source: canon.SourceReview}},
	}
	tracks.Merged["p:spotify:old4"] = "i:USRC10000004"

	// 刻意使用非字典序的 CID 排列(sp_3 -> 無對應跳過 -> sp_1 -> 墓碑轉向 sp_4),驗證決策 38 原序不重排。
	plFocus := canon.Playlist{
		SchemaVersion: canon.SchemaVersion,
		PID:           "01FOCUSPID0000000000000001",
		Name:          "Late Night Focus",
		Links:         map[string]string{"spotify": "sp_pl_focus"},
		Items: []canon.Item{
			{IID: "01A", CID: "i:USRC10000003", Rank: "a0"},
			{IID: "01B", CID: "i:USRC10000002", Rank: "a1"},
			{IID: "01C", CID: "i:USRC10000001", Rank: "a2"},
			{IID: "01D", CID: "p:spotify:old4", Rank: "a3"},
		},
	}
	plAlpha := canon.Playlist{
		SchemaVersion: canon.SchemaVersion,
		PID:           "01ALPHAPID0000000000000002",
		Name:          "Alpha Acoustic",
		Links:         map[string]string{},
		Items:         []canon.Item{},
	}

	manifest := canon.NewManifest()
	manifest.AddPlaylist(plFocus.PID)
	manifest.AddPlaylist(plAlpha.PID)

	if err := s.Hydrate(store.Canonical{
		Manifest:  manifest,
		Tracks:    tracks,
		Playlists: []canon.Playlist{plFocus, plAlpha},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTUIQuickDeckPlaylistsPreservesCanonOrderAndHeight(t *testing.T) {
	fc := &deckFakeController{watchFake: watchFake{st: playingState()}}
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "spotify", "", testTracker(t, "spotify", fc), watchPollSpotify)
	m.st = fc.st
	recordPrintln(t)

	seedTestCanonicalDB(t)
	cc := cache.Load()
	cc.SetPlaylists("spotify", []cache.Playlist{{ID: "pl_chill", Name: "Chill Vibes", Total: 12}})
	if err := cc.Save(); err != nil {
		t.Fatal(err)
	}

	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: tuiAliveMinHeight}, true)
	if !m.alive() || len(viewLines(m)) != capyBlockRows+4 {
		t.Fatalf("前置條件:水豚常駐 15 行:%d", len(viewLines(m)))
	}

	// 按 o 打開 Quick Deck:借用常駐水豚的 11 行區塊,總高度維持 15 行不變。
	m = step(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"}, false)
	if !m.drawerOpen() || len(viewLines(m)) != capyBlockRows+4 || strings.Contains(m.View().Content, capyFeet) {
		t.Fatalf("抽屜開啟時借用水豚區塊且高度固定 15 行:open=%v lines=%d", m.drawerOpen(), len(viewLines(m)))
	}

	// 打字過濾定位到 Late Night Focus 並按 Enter 點播。
	m = typeKeys(t, m, "Late Night")
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, true)
	if m.drawerOpen() || !strings.Contains(m.View().Content, capyFeet) || len(viewLines(m)) != capyBlockRows+4 {
		t.Fatalf("點播後抽屜收起、水豚歸位且高度仍為 15 行:open=%v lines=%d", m.drawerOpen(), len(viewLines(m)))
	}
	wantIDs := []string{"sp_3", "sp_1", "sp_4"}
	if !slices.Equal(fc.lastPlay.TrackIDs, wantIDs) {
		t.Fatalf("正本點播必須嚴格依 playlist_items 原序送出對應 ID(含合併轉向、略過無對應):got %v, want %v", fc.lastPlay.TrackIDs, wantIDs)
	}

	// 驗證本機正本順序絲毫未動(決策 38)。
	snap, ok := tuiLoadCanonSnapshot()
	if !ok {
		t.Fatal("應讀得到正本快照")
	}
	for _, pl := range snap.Playlists {
		if pl.Name == "Late Night Focus" {
			gotCIDs := []string{pl.Items[0].CID, pl.Items[1].CID, pl.Items[2].CID, pl.Items[3].CID}
			wantCIDs := []string{"i:USRC10000003", "i:USRC10000002", "i:USRC10000001", "p:spotify:old4"}
			if !slices.Equal(gotCIDs, wantCIDs) {
				t.Fatalf("正本順序絕不可改變:got %v, want %v", gotCIDs, wantCIDs)
			}
		}
	}

	// 再按 o 過濾平台快取清單 Chill Vibes 並點播。
	m = step(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"}, false)
	m = typeKeys(t, m, "Chill")
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, true)
	if fc.lastPlay.PlaylistID != "pl_chill" {
		t.Fatalf("平台清單應以 PlaylistID 播放:got %+v", fc.lastPlay)
	}
}

func TestTUIQuickDeckRecentAndDeviceTransfer(t *testing.T) {
	fc := &deckFakeController{
		watchFake: watchFake{st: playingState()},
		devices: []provider.Device{
			{ID: "dev_mac", Name: "MacBook Pro", Type: "Computer", Active: true, VolumePct: 50, VolumeKnown: true},
			{ID: "dev_spk", Name: "Studio Monitor", Type: "Speaker", Active: false, VolumePct: 70, VolumeKnown: true},
		},
	}
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "spotify", "", testTracker(t, "spotify", fc), watchPollSpotify)
	m.st = fc.st
	m.frozen = true
	recordPrintln(t)

	cc := cache.Load()
	cc.AddRecent(cache.Recent{Provider: "spotify", Type: cache.TypeTrack, ID: "sp_recent_1", Label: "Sunny Day", Detail: "Jay"})
	if err := cc.Save(); err != nil {
		t.Fatal(err)
	}

	// o 開啟 → Tab 切到 [2:Recent] → Enter 續播最近曲目。
	m = step(t, m, tea.KeyPressMsg{Code: 'o', Text: "o"}, false)
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyTab}, false)
	if m.drawer.tab != tuiDeckTabRecent {
		t.Fatalf("Tab 應切到 Recent 分頁:got %d", m.drawer.tab)
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, true)
	if !slices.Equal(fc.lastPlay.TrackIDs, []string{"sp_recent_1"}) {
		t.Fatalf("Recent 曲目點播應送出 sp_recent_1:got %+v", fc.lastPlay)
	}

	// d 直達 [3:Devices] 分頁 → 載入裝置 → 選第二台裝置 → Enter 切換輸出裝置。
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = next.(tuiModel)
	if m.drawer.mode != tuiDrawerDeck || m.drawer.tab != tuiDeckTabDevices || cmd == nil {
		t.Fatalf("d 應直達 Devices 分頁並發出非同步查詢:mode=%v tab=%d cmd=%v", m.drawer.mode, m.drawer.tab, cmd)
	}
	m = step(t, m, cmd(), false)
	if len(m.drawer.items) != 2 || !strings.Contains(ansi.Strip(strings.Join(m.drawerView(99), "\n")), "* MacBook Pro") {
		t.Fatalf("目前作用中裝置應標示 *:%v", m.drawer.items)
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyDown}, false)
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, true)
	if !slices.Equal(fc.transfers, []string{"dev_spk:play"}) {
		t.Fatalf("Enter 應呼叫 DeviceTransferrer.Transfer:got %v", fc.transfers)
	}
	if m.st.Device.Name != "Studio Monitor" {
		t.Errorf("切換裝置應提供樂觀預覽:got %q", m.st.Device.Name)
	}
}

func TestTUICollectDrawerPinsLastAndShiftFInstantCollects(t *testing.T) {
	fc := &deckFakeController{watchFake: watchFake{st: playingState()}}
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "spotify", "", testTracker(t, "spotify", fc), watchPollSpotify)
	m.st = fc.st
	m.frozen = true
	recordPrintln(t)
	ran := recordExec(t)
	seedTestCanonicalDB(t)

	// 第一次按 f:預設按名稱排序(Alpha Acoustic 在前,Late Night Focus 在後)。
	m = step(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"}, false)
	if len(m.drawer.items) != 2 || m.drawer.items[0].label != "Alpha Acoustic" {
		t.Fatalf("初始正本清單排序不符:%+v", m.drawer.items)
	}
	// 選第二個 Late Night Focus 按 Enter。
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyDown}, false)
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, true)
	wantCmd := []string{"/bin/capy", "pl", "add", "Late Night Focus", "--now", "--yes", "--provider", "spotify"}
	if len(*ran) != 1 || !slices.Equal((*ran)[0], wantCmd) {
		t.Fatalf("f → Enter 應執行 pl add --now --yes:got %v, want %v", *ran, wantCmd)
	}
	m = step(t, m, tuiExecMsg{args: wantCmd[1:]}, false)

	// 第二次按 f:上次收藏的 Late Night Focus 置頂且標記 [last]。
	m = step(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"}, false)
	if len(m.drawer.items) != 2 || m.drawer.items[0].label != "Late Night Focus" || !strings.Contains(m.drawer.items[0].detail, "[last]") {
		t.Fatalf("上次收藏目標應置頂並標記 [last]:%+v", m.drawer.items)
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, false)

	// 按 F(Shift+F):不開抽屜,一鍵收進上次收藏的 Late Night Focus。
	m = step(t, m, tea.KeyPressMsg{Code: 'F', Text: "F"}, true)
	if m.drawerOpen() || len(*ran) != 2 || !slices.Equal((*ran)[1], wantCmd) {
		t.Fatalf("F 應直接收進上次目標清單:open=%v ran=%v", m.drawerOpen(), *ran)
	}
}

func TestTUIThemeDrawerLivePreviewPersistAndEscRestore(t *testing.T) {
	t.Cleanup(func() { ui.ApplyTheme(ui.DefaultThemeName) })
	ui.ApplyTheme(ui.DefaultThemeName)

	fc := &deckFakeController{watchFake: watchFake{st: playingState()}}
	m := newTUIModel(context.Background(), ui.DefaultTheme, "/bin/capy", "spotify", "", testTracker(t, "spotify", fc), watchPollSpotify)
	m.st = fc.st
	m.frozen = true
	recordPrintln(t)

	// 按 C 開啟 5 款主題即時預覽抽屜。
	m = step(t, m, tea.KeyPressMsg{Code: 'C', Text: "C"}, false)
	if m.drawer.mode != tuiDrawerTheme || len(m.drawer.items) != 5 {
		t.Fatalf("C 應列出全部 5 款主題:%+v", m.drawer.items)
	}
	// ↓ 移到 yuzu-onsen:水豚與畫面主題立即變色預覽。
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyDown}, false)
	if m.theme != ui.YuzuOnsen || ui.DefaultTheme != ui.YuzuOnsen {
		t.Fatalf("上下移動應即時套用 YuzuOnsen 預覽:m.theme=%+v default=%+v", m.theme, ui.DefaultTheme)
	}
	// Esc 取消:還原為原本的 GeekGreen,且不寫入 config.json。
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, false)
	if m.drawerOpen() || m.theme != ui.GeekGreen || ui.DefaultTheme != ui.GeekGreen {
		t.Fatalf("Esc 應還原原主題:open=%v theme=%+v", m.drawerOpen(), m.theme)
	}
	if cfg, err := config.Load(); err != nil || cfg.Theme != "" {
		t.Fatalf("Esc 不該寫入 config.json:cfg=%+v err=%v", cfg, err)
	}

	// 再按 C,移到最後一款 daylight-ink 並按 Enter 儲存。
	m = step(t, m, tea.KeyPressMsg{Code: 'C', Text: "C"}, false)
	for range 4 {
		m = step(t, m, tea.KeyPressMsg{Code: tea.KeyDown}, false)
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}, false)
	if m.drawerOpen() || m.theme != ui.DaylightInk || ui.DefaultTheme != ui.DaylightInk || m.themeName != "daylight-ink" {
		t.Fatalf("Enter 應套用 daylight-ink:themeName=%q", m.themeName)
	}
	cfg, err := config.Load()
	if err != nil || cfg.Theme != "daylight-ink" {
		t.Fatalf("Enter 應持久化至 config.Config.Theme:cfg=%+v err=%v", cfg, err)
	}
}
