package cli

// 共用唯讀 Wiki 快取查詢器(決策 59):供 TUI 與 webServer 在本機離線查詢已快取的歌曲介紹,
// 絕不發送任何網路或 AI 端點請求、不碰 keychain、不建立或修改 state.db。

import (
	"strings"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/ai"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

const wikiLookupBusy = 100 * time.Millisecond

// cachedWikiView:命中本機 wiki_cache 時回傳的唯讀視圖;結尾免責聲明不可關閉(CLAUDE.md 硬約束)。
type cachedWikiView struct {
	Title      string    `json:"title"`
	Artists    string    `json:"artists,omitempty"`
	Language   string    `json:"language"`
	Body       string    `json:"body"`
	Model      string    `json:"model"`
	CreatedAt  time.Time `json:"created_at"`
	MVURL      string    `json:"mv_url"`
	Disclaimer string    `json:"disclaimer"`
}

// wikiEffectiveLang:計算歌曲 wiki 使用的正規化語言標籤(優先 native_language,否則退回介面 language 或預設語系)。
func wikiEffectiveLang(cfg config.Config) string {
	langCode := strings.TrimSpace(cfg.NativeLanguage)
	if langCode == "" {
		if l, ok := i18n.Normalize(cfg.Language); ok {
			langCode = l
		} else {
			langCode = i18n.Default()
		}
	}
	_, tag, err := ai.LanguageLabel(langCode)
	if err != nil {
		return ""
	}
	return tag
}

// lookupCachedWiki:純本機離線查詢 state.db 的 wiki_cache;未建庫、忙碌、壞設定或未命中一律回 (nil, false)。
func lookupCachedWiki(cfg config.Config, song ai.Song) (*cachedWikiView, bool) {
	if strings.TrimSpace(song.Title) == "" {
		return nil, false
	}
	tag := wikiEffectiveLang(cfg)
	if tag == "" {
		return nil, false
	}
	s, err := store.OpenReadOnly(wikiLookupBusy)
	if err != nil {
		return nil, false
	}
	defer s.Close()
	entry, ok, err := s.CachedWiki(ai.WikiCacheKey(tag, song))
	if err != nil || !ok {
		return nil, false
	}
	return &cachedWikiView{
		Title:      entry.Title,
		Artists:    entry.Artists,
		Language:   entry.Language,
		Body:       entry.Body,
		Model:      entry.Model,
		CreatedAt:  entry.FetchedAt,
		MVURL:      wikiYouTubeURL(song),
		Disclaimer: i18n.T("wiki.disclaimer"),
	}, true
}
