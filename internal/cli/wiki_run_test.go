package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/provider"
)

// wikiConfigured:setWikiTest 之上,config 已接好 f 這個端點(model m1);時鐘固定。
func wikiConfigured(t *testing.T, f *fakeAI) {
	t.Helper()
	c, _ := config.Load()
	c.AIBaseURL, c.AIModel = f.base(), "m1"
	if err := config.Save(c); err != nil {
		t.Fatal(err)
	}
	origClock, origTimeout := wikiClock, wikiChatTimeout
	wikiClock = func() time.Time { return time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { wikiClock, wikiChatTimeout = origClock, origTimeout })
}

// wikiPlayers:spotify 有曲目(st),其他家一律建不起來(沒登入)。
func wikiPlayers(t *testing.T, st *provider.PlaybackState, stErr error) {
	t.Helper()
	orig := newProvider
	newProvider = func(_ context.Context, id string) (provider.Provider, error) {
		if id == "spotify" {
			return &watchFake{playFake: playFake{fakeProvider: fakeProvider{caps: provider.CapPlaybackControl}}, st: st, err: stErr}, nil
		}
		return nil, errors.New(id + ": not logged in")
	}
	t.Cleanup(func() { newProvider = orig })
}

func (f *fakeAI) chats() int {
	n := 0
	for _, p := range f.paths() {
		if p == "/v1/chat/completions" {
			n++
		}
	}
	return n
}

// userMessage:送出的 chat 請求裡 user 那一段的原文(JSON 已解開,換行是真的換行)。
func userMessage(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("請求不是 JSON:%v\n%s", err, body)
	}
	for _, m := range req.Messages {
		if m.Role == "user" {
			return m.Content
		}
	}
	t.Fatalf("沒有 user 段:%s", body)
	return ""
}

func runCLICtx(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return buf.String(), err
}

func TestWikiNowPlayingStreamsThenCaches(t *testing.T) {
	setWikiTest(t)
	f := newFakeAI(t)
	f.chatText = "## 基本資料\n1997 年發行\n\n## 故事\n- 一句"
	wikiConfigured(t, f)
	st := playingState()
	st.Playing = false // 暫停中也算「你在聽的這首」
	st.Track.ReleaseDate, st.Track.Genres = "1997-08-25", []string{"Mandopop"}
	wikiPlayers(t, st, nil)

	out, err := runCLI(t, "wiki")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := "正在詢問 m1…\n" +
		"## 基本資料\n1997 年發行\n\n## 故事\n- 一句\n" +
		"\n" +
		"MV:https://www.youtube.com/results?search_query=%E6%B4%BE%E5%B0%8D%E5%8B%95%E7%89%A9+%E4%BA%94%E6%9C%88%E5%A4%A9+MV\n" +
		"由 m1 於 2026-09-29 寫成\n" +
		"以上由 AI 產生,可能有錯;歌詞請以播放器或官方為準。\n"
	if out != want {
		t.Fatalf("第一次(串流):\n%q\n要\n%q", out, want)
	}
	if f.chats() != 1 {
		t.Fatalf("要打一次端點:%v", f.paths())
	}
	body := f.bodies[len(f.bodies)-1]
	for _, s := range []string{`title: 派對動物`, `artist: 五月天`, `album: 自傳`, `release date: 1997-08-25`, `genre: Mandopop`, `"stream":true`} {
		if !strings.Contains(body, s) {
			t.Errorf("送出的請求要有 %q:\n%s", s, body)
		}
	}
	if strings.Contains(body, `"max_tokens"`) || strings.Contains(body, `"max_completion_tokens"`) {
		t.Errorf("正式的 wiki 不帶 token 上限(reasoning model 會無聲截斷):%s", body)
	}
	if !strings.Contains(body, "Chinese (Taiwan) (zh-TW)") {
		t.Errorf("沒設母語 = 介面語系(測試二進位是 zh-TW):%s", body)
	}
	// 第二次:零請求、標來自快取、內容一樣。
	out2, err := runCLI(t, "wiki")
	if err != nil || f.chats() != 1 {
		t.Fatalf("第二次要用快取、不打端點:%v %v\n%s", err, f.paths(), out2)
	}
	if !strings.HasPrefix(out2, "(來自這台電腦的快取,2026-09-29;--refresh 會再問端點一次)\n## 基本資料\n") || !strings.HasSuffix(out2, want[len("正在詢問 m1…\n"):]) {
		t.Fatalf("快取命中的輸出:\n%q", out2)
	}
	// --refresh:再問一次並覆寫。
	f.chatText = "## 新的\n改寫"
	out3, err := runCLI(t, "wiki", "--refresh")
	if err != nil || f.chats() != 2 || !strings.Contains(out3, "## 新的\n改寫\n") {
		t.Fatalf("--refresh:%v %v\n%s", err, f.paths(), out3)
	}
	if out4, _ := runCLI(t, "wiki"); !strings.Contains(out4, "## 新的") || f.chats() != 2 {
		t.Fatalf("覆寫後的快取:\n%s", out4)
	}
	// 換母語 = 另一份 key。
	if _, err := runCLI(t, "config", "set", "native_language", "ja"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "wiki"); err != nil || f.chats() != 3 || !strings.Contains(f.bodies[len(f.bodies)-1], "Japanese (ja)") {
		t.Fatalf("換母語要重問、prompt 帶新語言:%v %v", err, f.paths())
	}
}

func TestWikiPinnedProviderAndNothingPlaying(t *testing.T) {
	setWikiTest(t)
	f := newFakeAI(t)
	wikiConfigured(t, f)
	wikiPlayers(t, nil, nil) // spotify 沒有播放內容
	_, err := runCLI(t, "wiki")
	if err == nil || !strings.Contains(err.Error(), "--artist") || f.chats() != 0 {
		t.Fatalf("哪裡都沒在播:要指路 --title、不打端點:%v", err)
	}
	_, err = runCLI(t, "wiki", "--provider", "spotify")
	if err == nil || !strings.Contains(err.Error(), "spotify") || !strings.Contains(err.Error(), "--artist") {
		t.Fatalf("釘住的平台沒在播:%v", err)
	}
	_, err = runCLI(t, "wiki", "--provider", "apple")
	if err == nil || !strings.Contains(err.Error(), "apple") {
		t.Fatalf("釘住建不起來的平台:錯誤照實回:%v", err)
	}
	wikiPlayers(t, nil, provider.ErrPlayerNotRunning)
	if _, err := runCLI(t, "wiki", "--provider", "spotify"); err == nil {
		t.Fatal("釘住時 State 的錯要回")
	}
	if _, err := runCLI(t, "wiki"); err == nil || !strings.Contains(err.Error(), "--artist") {
		t.Fatalf("沒釘住時 State 的錯只是跳過:%v", err)
	}
}

func TestWikiTitleArguments(t *testing.T) {
	setWikiTest(t)
	f := newFakeAI(t)
	wikiConfigured(t, f)
	orig := newProvider
	newProvider = func(context.Context, string) (provider.Provider, error) {
		t.Fatal("給了歌名就不問平台")
		return nil, nil
	}
	t.Cleanup(func() { newProvider = orig })
	out, err := runCLI(t, "wiki", "残酷な天使の", "テーゼ", "--artist", "高橋洋子")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	user := userMessage(t, f.bodies[len(f.bodies)-1])
	if !strings.Contains(user, "title: 残酷な天使の テーゼ\n") || !strings.Contains(user, "artist: 高橋洋子\n") || strings.Contains(user, "album:") {
		t.Errorf("位置參數接成歌名、--artist 是歌手、沒有的欄位不送:\n%s", user)
	}
	if !strings.Contains(out, "search_query=%E6%AE%8B%E9%85%B7%E3%81%AA%E5%A4%A9%E4%BD%BF%E3%81%AE+%E3%83%86%E3%83%BC%E3%82%BC+%E9%AB%98%E6%A9%8B%E6%B4%8B%E5%AD%90+MV") {
		t.Errorf("MV 連結是歌名 + 歌手 + MV 的搜尋:\n%s", out)
	}
	if _, err := runCLI(t, "wiki", "--title", "x"); err != nil || f.chats() != 2 {
		t.Fatalf("--title 也行:%v", err)
	}
	if _, err := runCLI(t, "wiki", "x", "--title", "y"); err == nil {
		t.Fatal("兩種都給要拒絕")
	}
	if _, err := runCLI(t, "wiki", "--artist", "y"); err == nil {
		t.Fatal("只給 --artist 要拒絕")
	}
	// wiki setup 照舊是子命令,不是歌名叫 setup(config 已有端點,所以非 TTY 的 setup 走到「請給 --model」)。
	if _, err := runCLI(t, "wiki", "setup"); err == nil || !strings.Contains(err.Error(), "--model") || f.chats() != 2 {
		t.Fatalf("wiki setup 要走 setup、不打 chat:%v", err)
	}
}

func TestWikiJSON(t *testing.T) {
	setWikiTest(t)
	f := newFakeAI(t)
	f.chatText = "## A\nb"
	wikiConfigured(t, f)
	wikiPlayers(t, playingState(), nil)
	parse := func(out string) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(out, "正在詢問 m1…\n")), &m); err != nil {
			t.Fatalf("不是 JSON:%v\n%s", err, out)
		}
		return m
	}
	out, err := runCLI(t, "wiki", "--json")
	if err != nil {
		t.Fatal(err)
	}
	m := parse(out)
	if m["title"] != "派對動物" || m["language"] != "zh-TW" || m["model"] != "m1" || m["cached"] != false || m["body"] != "## A\nb" ||
		m["fetched_at"] != "2026-09-29T10:00:00Z" || !strings.HasPrefix(m["youtube_search_url"].(string), "https://www.youtube.com/results?search_query=") {
		t.Fatalf("JSON 形狀:%v", m)
	}
	if strings.Contains(f.bodies[len(f.bodies)-1], `"stream":true`) {
		t.Fatal("--json 整批、不串流")
	}
	out, _ = runCLI(t, "wiki", "--json")
	if m := parse(out); m["cached"] != true || m["language"] != "zh-TW" || m["model"] != "m1" || f.chats() != 1 {
		t.Fatalf("第二次 cached 要是 true、不打端點、語言與 model 描述這一列自己:%v", m)
	}
}

func TestWikiTimeoutAndCancelDoNotCache(t *testing.T) {
	setWikiTest(t)
	f := newFakeAI(t)
	f.chatHang = make(chan struct{})
	wikiConfigured(t, f)
	wikiPlayers(t, playingState(), nil)
	wikiChatTimeout = 100 * time.Millisecond
	_, err := runCLI(t, "wiki")
	if err == nil || !strings.Contains(err.Error(), "0 秒") || !strings.Contains(err.Error(), "capy wiki setup") {
		t.Fatalf("逾時要說人話並指路:%v", err)
	}
	// 中止(Ctrl-C / web):context.Canceled 原樣回,結束碼 130 與「已中止」才認得。
	wikiChatTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := runCLICtx(t, ctx, "wiki"); !errors.Is(err, context.Canceled) {
		t.Fatalf("中止要原樣回 context.Canceled:%v", err)
	}
	// 兩次都沒進快取:放行之後第三次真的會問。只關閉、不清掉:被中止的那個請求的 handler 可能還在讀這個欄位(-race)。
	close(f.chatHang)
	if _, err := runCLI(t, "wiki"); err != nil || f.chats() != 3 {
		t.Fatalf("出錯的不進快取:%v %v", err, f.paths())
	}
}

func TestWikiNotConfigured(t *testing.T) {
	setWikiTest(t)
	orig := newProvider
	newProvider = func(context.Context, string) (provider.Provider, error) {
		t.Fatal("沒設定就不該問平台")
		return nil, nil
	}
	t.Cleanup(func() { newProvider = orig })
	_, err := runCLI(t, "wiki")
	if err == nil || !strings.Contains(err.Error(), "capy wiki setup") {
		t.Fatalf("要指路 setup:%v", err)
	}
}

// TestWikiFollowsStatusBarPlatform:TUI 的命令列打 wiki 時跟著狀態列的平台(決策 53 的名單加 wiki);
// 給了歌名(位置參數或 --title)就不跟;--refresh / --json 照跟;自己打了 --provider 不動。
func TestWikiFollowsStatusBarPlatform(t *testing.T) {
	m := tuiModel{provID: "apple"}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"wiki"}, "wiki --provider apple"},
		{[]string{"wiki", "--refresh"}, "wiki --refresh --provider apple"},
		{[]string{"wiki", "--json"}, "wiki --json --provider apple"},
		{[]string{"wiki", "--title", "x"}, "wiki --title x"},
		{[]string{"wiki", "some", "song"}, "wiki some song"},
		{[]string{"wiki", "--provider", "spotify"}, "wiki --provider spotify"},
		{[]string{"wiki", "setup"}, "wiki setup"},
	} {
		if got := strings.Join(m.withProviderFlag(tc.args), " "); got != tc.want {
			t.Errorf("%v → %q,要 %q", tc.args, got, tc.want)
		}
	}
}

func TestTUIWikiKey(t *testing.T) {
	got := recordExec(t)
	f := &watchFake{st: playingState()}
	m := newTestTUI(t, f)
	m.provID = "spotify"
	printed := recordPrintln(t)
	m = step(t, m, tea.KeyPressMsg{Code: 'w'}, true)
	if len(*got) != 1 || strings.Join((*got)[0], " ") != "/bin/capy wiki --provider spotify" {
		t.Fatalf("w 要跑 wiki 並帶狀態列的平台:%v", *got)
	}
	if echo := joined(printed); !strings.Contains(echo, "> ") || !strings.Contains(echo, "wiki") { // 提示符與命令各自上色
		t.Errorf("要像打 /wiki 一樣回音:%q", echo)
	}
	if !strings.Contains(tuiKeymap(), "w ") {
		t.Error("鍵位表要列 w")
	}
}

func TestWikiEnglish(t *testing.T) {
	setWikiTest(t)
	withLanguage(t, "en")
	f := newFakeAI(t)
	f.chatText = "## Basics\nx"
	wikiConfigured(t, f)
	wikiPlayers(t, playingState(), nil)
	out, err := runCLI(t, "wiki")
	if err != nil {
		t.Fatal(err)
	}
	want := "Asking m1…\n## Basics\nx\n\nMV: https://www.youtube.com/results?search_query=%E6%B4%BE%E5%B0%8D%E5%8B%95%E7%89%A9+%E4%BA%94%E6%9C%88%E5%A4%A9+MV\n" +
		"Written by m1 on 2026-09-29\nGenerated by AI and may be wrong; for the lyrics, trust your player or the official release.\n"
	if out != want || hasCJK(strings.ReplaceAll(out, "%E6%B4%BE%E5%B0%8D%E5%8B%95%E7%89%A9+%E4%BA%94%E6%9C%88%E5%A4%A9", "")) {
		t.Fatalf("英文輸出:\n%q\n要\n%q", out, want)
	}
	if out, _ := runCLI(t, "wiki"); !strings.HasPrefix(out, "(from this computer's cache, 2026-09-29; --refresh asks the endpoint again)\n") {
		t.Fatalf("英文的快取行:%q", out)
	}
	if _, err := runCLI(t, "wiki", "--artist", "y"); err == nil || err.Error() != "--artist only goes with a title: capy wiki <title> --artist <artist>" {
		t.Fatalf("英文錯誤:%v", err)
	}
}
