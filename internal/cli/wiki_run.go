package cli

// capy wiki(決策 59,T2):對正在播的歌(或指定的一首)用使用者自己的 AI 端點產生母語的介紹,逐行串流到 stdout,
// 結尾 MV 的 YouTube 搜尋連結、來源與免責一行;結果進 state.db 的 wiki_cache(純快取、不過期,--refresh 重跑)。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tai-ch0802/capy-music/internal/ai"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/provider"
	"github.com/Tai-ch0802/capy-music/internal/store"
	"github.com/Tai-ch0802/capy-music/internal/ui"
)

// wikiChatTimeout:一次 chat/completions 的總上限(reasoning model 第一個 token 可能等幾十秒);Ctrl-C / web 的中止隨時砍。
// 測試替換點。
var wikiChatTimeout = 3 * time.Minute

// wikiClock:快取記的時間。測試替換點。
var wikiClock = time.Now

const (
	// wikiStateTimeout:抓正在播的歌時每一家 State 的上限(同 TUI 的 tuiStateTimeout;帶 WithoutWait,不睡在 429 裡)。
	wikiStateTimeout = 10 * time.Second
	// wikiCacheBusy:快取讀寫等別的 capy 放鎖的上限——純快取,等不到就當沒有 / 寫不進去只在 stderr 說一聲。
	wikiCacheBusy = 200 * time.Millisecond
)

// wikiJSON:--json 的形狀(給腳本;欄位只增不改、列舉值不翻譯)。
type wikiJSON struct {
	Title            string   `json:"title"`
	Artists          []string `json:"artists"`
	Album            string   `json:"album,omitempty"`
	ReleaseDate      string   `json:"release_date,omitempty"`
	Genres           []string `json:"genres,omitempty"`
	Language         string   `json:"language"`
	Model            string   `json:"model"`
	Cached           bool     `json:"cached"`
	FetchedAt        string   `json:"fetched_at"`
	Body             string   `json:"body"`
	YouTubeSearchURL string   `json:"youtube_search_url"`
}

func runWiki(cmd *cobra.Command, args []string) error {
	fl := cmd.Flags()
	c, err := ai.Load() // 沒 setup:ai.err.not_configured 指路
	if err != nil {
		return err
	}
	title, _ := fl.GetString("title")
	artist, _ := fl.GetString("artist")
	if len(args) > 0 {
		if strings.TrimSpace(title) != "" {
			return i18n.Errorf("wiki.err.title_twice")
		}
		title = strings.Join(args, " ")
	}
	var song ai.Song
	if strings.TrimSpace(title) != "" {
		song = ai.Song{Title: strings.TrimSpace(title)}
		// --artist 刻意是一個字串(不是 StringArray):使用者打 "A, B" 跟平台給的 []string{"A","B"} 在 prompt 與快取 key 裡
		// 都是 "A, B",同一首才會對到同一份快取。
		if a := strings.TrimSpace(artist); a != "" {
			song.Artists = []string{a}
		}
	} else {
		if strings.TrimSpace(artist) != "" {
			return i18n.Errorf("wiki.err.artist_without_title")
		}
		if song, err = wikiNowPlaying(cmd); err != nil {
			return err
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	langCode := cfg.NativeLanguage // 沒設 = 介面語系(config 的 language,或預設);兩個都在存的時候驗過
	if langCode == "" {
		langCode, _ = configLanguage()
	}
	label, tag, err := ai.LanguageLabel(langCode)
	if err != nil {
		return err
	}
	key := ai.WikiCacheKey(tag, song)
	asJSON, _ := fl.GetBool("json")
	refresh, _ := fl.GetBool("refresh")
	w, stderr, tty := cmd.OutOrStdout(), cmd.ErrOrStderr(), stdoutIsTTY(cmd)

	entry, cached := store.WikiEntry{}, false
	if !refresh {
		entry, cached = wikiCached(key)
	}
	if !cached {
		system, user := ai.WikiPrompt(label, song)
		fmt.Fprintln(stderr, i18n.T("wiki.progress", "model", c.Model))
		ctx, cancel := context.WithTimeout(cmd.Context(), wikiChatTimeout)
		defer cancel()
		// 不帶 max_tokens:reasoning model 把思考的 token 也算在上限裡,上限一到就無聲截斷;長度由 prompt 與這個逾時管。
		req := ai.ChatRequest{System: system, User: user}
		var body string
		printed := false
		if asJSON {
			body, err = c.ChatOnce(ctx, req)
		} else {
			body, err = c.Chat(ctx, req, func(line string) { printed = true; fmt.Fprintln(w, wikiLine(tty, line)) })
		}
		if err != nil {
			// 正文已經印了一截(逾時、串流中途的錯誤幀、斷線、Ctrl-C)也要收在免責行之後——不完整的內容最需要它
			// (CLAUDE.md:結尾的免責行不可關)。出錯的、不完整的(ai.IncompleteError)都不進快取。
			if printed {
				fmt.Fprintln(w)
				fmt.Fprintln(w, i18n.T("wiki.disclaimer"))
			}
			// 逾時說人話;Ctrl-C / web 的中止(context.Canceled)原樣回,結束碼 130 與「已中止」才認得。
			if errors.Is(err, context.DeadlineExceeded) && cmd.Context().Err() == nil {
				return i18n.Errorf("wiki.err.timeout", "seconds", int(wikiChatTimeout.Seconds()))
			}
			return err
		}
		entry = store.WikiEntry{Title: song.Title, Artists: strings.Join(song.Artists, ", "), Language: tag, Model: c.Model, Body: body, FetchedAt: wikiClock()}
		if err := wikiSave(key, entry); err != nil {
			fmt.Fprintln(stderr, i18n.T("wiki.cache_write_failed", "err", err))
		}
	}
	mv := wikiYouTubeURL(song)
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(wikiJSON{
			Title: song.Title, Artists: nonNilStrings(song.Artists), Album: song.Album, ReleaseDate: song.ReleaseDate, Genres: song.Genres,
			Language: entry.Language, Model: entry.Model, Cached: cached, FetchedAt: entry.FetchedAt.UTC().Format(time.RFC3339), Body: entry.Body, YouTubeSearchURL: mv, // 語言與 model 都描述這一列自己
		})
	}
	if cached { // 快取命中:整份走同一個逐行路徑,TTY 的標題加粗才一致。「來自快取」跟「正在詢問」一樣是框架字、走 stderr:
		// stdout 在命中與沒命中時長得一樣(capy wiki > song.md 拿到的都是正文 + 結尾三行)
		fmt.Fprintln(stderr, i18n.T("wiki.cached", "date", entry.FetchedAt.Format("2006-01-02")))
		for _, line := range strings.Split(strings.TrimRight(entry.Body, "\n"), "\n") {
			fmt.Fprintln(w, wikiLine(tty, line))
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T("wiki.mv", "url", mv))
	fmt.Fprintln(w, i18n.T("wiki.source", "model", entry.Model, "date", entry.FetchedAt.Format("2006-01-02")))
	fmt.Fprintln(w, i18n.T("wiki.disclaimer"))
	return nil
}

// wikiLine:TTY 把 "## " 開頭的行加粗(逐行串流才知道整行是什麼);其他原樣。
func wikiLine(tty bool, line string) string {
	if tty && strings.HasPrefix(line, "## ") {
		return ui.Bold(true, line)
	}
	return line
}

// wikiYouTubeURL:MV 是 YouTube 的搜尋網址(本機組字串,不叫 AI 猜連結、capy 不連 YouTube;決策 59 / Q70)。
func wikiYouTubeURL(song ai.Song) string {
	q := strings.TrimSpace(strings.Join(append([]string{song.Title}, song.Artists...), " ") + " MV")
	return "https://www.youtube.com/results?search_query=" + url.QueryEscape(q)
}

// wikiCached / wikiSave:純快取,盡力而為——db 開不了(別的 capy 正在寫、剛升版)就當沒有;寫不進去由呼叫端在 stderr 說一聲。
func wikiCached(key string) (store.WikiEntry, bool) {
	s, err := store.Open(wikiCacheBusy)
	if err != nil {
		return store.WikiEntry{}, false
	}
	defer s.Close()
	e, ok, err := s.CachedWiki(key)
	if err != nil {
		return store.WikiEntry{}, false
	}
	return e, ok
}

func wikiSave(key string, e store.WikiEntry) error {
	s, err := store.Open(wikiCacheBusy)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.SaveWiki(key, e)
}

// wikiNowPlaying:你在聽的這首——在播或暫停都算(wiki 講的是這首歌,不是播放狀態)。
// --provider 釘住就只問那一家,錯誤照實回(friendlyErr:token 失效指向 auth login);沒釘就先問預設平台、再照 providerIDs,
// 第一家有曲目的就用;建不起來(沒登入)、Music.app 沒開、限流、不支援播放都跳過,全部都沒有才說沒有在播、指路 --title(Q72)。
// 每一家 State 帶 WithoutWait 與上限(不睡在 429 裡、不讓卡住的 osascript 拖住整個命令);newProvider 等 token 鎖沒有上限(同別處)。
func wikiNowPlaying(cmd *cobra.Command) (ai.Song, error) {
	ctx := cmd.Context()
	if cmd.Flags().Changed(flagProvider) {
		p, err := getProvider(cmd)
		if err != nil {
			return ai.Song{}, err
		}
		pc, err := asPlayback(p)
		if err != nil {
			return ai.Song{}, err
		}
		st, err := wikiState(ctx, pc)
		if err != nil {
			return ai.Song{}, friendlyErr(p.ID(), err)
		}
		if st == nil || st.Track == nil {
			return ai.Song{}, i18n.Errorf("wiki.err.nothing_playing_on", "provider", p.ID())
		}
		return songOf(st.Track), nil
	}
	seen := map[string]bool{}
	for _, id := range append([]string{loadDefaultProvider()}, providerIDs...) {
		if seen[id] {
			continue
		}
		seen[id] = true
		p, err := newProvider(ctx, id)
		if err != nil {
			continue
		}
		pc, err := asPlayback(p)
		if err != nil {
			continue
		}
		if st, err := wikiState(ctx, pc); err == nil && st != nil && st.Track != nil {
			return songOf(st.Track), nil
		}
	}
	return ai.Song{}, i18n.Errorf("wiki.err.nothing_playing")
}

func wikiState(ctx context.Context, pc provider.PlaybackController) (*provider.PlaybackState, error) {
	ctx, cancel := context.WithTimeout(provider.WithoutWait(ctx), wikiStateTimeout)
	defer cancel()
	return pc.State(ctx)
}

func songOf(t *provider.Track) ai.Song {
	return ai.Song{Title: t.Title, Artists: t.Artists, Album: t.Album, ReleaseDate: t.ReleaseDate, Genres: t.Genres}
}
