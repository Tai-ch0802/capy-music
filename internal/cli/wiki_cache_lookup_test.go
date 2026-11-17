package cli

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tai-ch0802/capy-music/internal/ai"
	"github.com/Tai-ch0802/capy-music/internal/config"
	"github.com/Tai-ch0802/capy-music/internal/i18n"
	"github.com/Tai-ch0802/capy-music/internal/store"
)

func TestLookupCachedWikiNoDBStaysClean(t *testing.T) {
	setCLITestConfig(t)
	song := ai.Song{Title: "Yellow", Artists: []string{"Coldplay"}}
	if got, ok := lookupCachedWiki(config.Config{Language: "en"}, song); ok || got != nil {
		t.Fatalf("未建 state.db 時應回 (nil, false):%+v %v", got, ok)
	}
	p, err := store.Path()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenReadOnly 不得建立空的 state.db:%v", err)
	}
}

func TestLookupCachedWikiHitMissAndOfflineGuarantee(t *testing.T) {
	setCLITestConfig(t)
	song := ai.Song{Title: "派對動物", Artists: []string{"五月天"}}
	fetched := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)

	st, err := store.Open(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	jaKey := ai.WikiCacheKey("ja", song)
	if err := st.SaveWiki(jaKey, store.WikiEntry{
		Title:     song.Title,
		Artists:   "五月天",
		Language:  "ja",
		Model:     "gpt-test",
		Body:      "## 基本情報\nテスト本文",
		FetchedAt: fetched,
	}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	zhKey := ai.WikiCacheKey("zh-TW", song)
	if err := st.SaveWiki(zhKey, store.WikiEntry{
		Title:     song.Title,
		Artists:   "五月天",
		Language:  "zh-TW",
		Model:     "local-llama",
		Body:      "## 基本資料\n離線快取正文",
		FetchedAt: fetched,
	}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// 即使 AIBaseURL 指到不可連位址、且完全沒設 keychain secret,純本機查詢依然命中且零網路請求。
	cfgJA := config.Config{AIBaseURL: "http://127.0.0.1:1", NativeLanguage: "JA", Language: "en"}
	got, ok := lookupCachedWiki(cfgJA, song)
	if !ok || got == nil {
		t.Fatal("應命中 ja 快取")
	}
	if got.Body != "## 基本情報\nテスト本文" || got.Model != "gpt-test" || !got.CreatedAt.Equal(fetched) {
		t.Errorf("快取內容不符:%+v", got)
	}
	if wantMV := wikiYouTubeURL(song); got.MVURL != wantMV || !strings.HasPrefix(got.MVURL, "https://www.youtube.com/results?search_query=") {
		t.Errorf("MVURL = %q, want %q", got.MVURL, wantMV)
	}
	if wantDisc := i18n.T("wiki.disclaimer"); got.Disclaimer == "" || got.Disclaimer != wantDisc {
		t.Errorf("Disclaimer 不可為空且必須對齊 wiki.disclaimer:%q vs %q", got.Disclaimer, wantDisc)
	}

	// NativeLanguage 未設時退回 Language(含大小寫/底線正規化 zh_tw → zh-TW)。
	cfgFallback := config.Config{Language: "zh_tw"}
	gotZH, ok := lookupCachedWiki(cfgFallback, song)
	if !ok || gotZH == nil || gotZH.Model != "local-llama" || gotZH.Body != "## 基本資料\n離線快取正文" {
		t.Fatalf("NativeLanguage 為空時應退回介面語系 zh-TW:%+v %v", gotZH, ok)
	}

	// 其他語言、其他歌曲、空歌名或壞語系代碼皆安全回 (nil, false)。
	for name, tc := range map[string]struct {
		cfg  config.Config
		song ai.Song
	}{
		"unmatched lang": {config.Config{NativeLanguage: "fr"}, song},
		"unmatched song": {cfgJA, ai.Song{Title: "倔強", Artists: []string{"五月天"}}},
		"empty title":    {cfgJA, ai.Song{Title: "   "}},
		"bad native tag": {config.Config{NativeLanguage: "not-a-bcp47-tag-!!!"}, song},
	} {
		if v, hit := lookupCachedWiki(tc.cfg, tc.song); hit || v != nil {
			t.Errorf("%s:預期未命中,得到 %+v %v", name, v, hit)
		}
	}
}
