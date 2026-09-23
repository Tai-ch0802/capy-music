package provider

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Tai-ch0802/capy-music/internal/i18n"
)

func withLanguage(t *testing.T, lang string) {
	t.Helper()
	prev := i18n.Current()
	if !i18n.Set(lang) {
		t.Fatalf("不支援 %s", lang)
	}
	t.Cleanup(func() { i18n.Set(prev) })
}

// 退避提示是一行(web 的活動列只顯示 stderr 最後一行);兩個語系都要以 "rate limited" 開頭(既有測試與使用者 grep 都認它)。
func TestBackoffMessagesEnglish(t *testing.T) {
	withLanguage(t, "en")
	stubWait(t)
	buf := &bytes.Buffer{}
	orig := BackoffStderr
	BackoffStderr = buf
	t.Cleanup(func() { BackoffStderr = orig })

	if err := Backoff(context.Background(), resp429("7"), 0); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "rate limited, retrying in 7s…\n" {
		t.Errorf("waiting = %q", got)
	}
	var rl *RateLimitError
	if err := Backoff(context.Background(), resp429("90"), 0); !errors.As(err, &rl) || err.Error() != "rate limited: the server asked to wait 90s, which is over the 60s limit; try again later" {
		t.Errorf("too long = %v", err)
	}
	if err := Backoff(context.Background(), resp429("1"), MaxRetries); err == nil || err.Error() != "rate limited, retry limit reached" {
		t.Errorf("gave up = %v", err)
	}
}

// zh-TW 的原文一個位元組不變(拆成語系目錄前是 Sprintf 直接拼的)。
func TestBackoffMessagesZhTW(t *testing.T) {
	withLanguage(t, "zh-TW")
	stubWait(t)
	buf := &bytes.Buffer{}
	orig := BackoffStderr
	BackoffStderr = buf
	t.Cleanup(func() { BackoffStderr = orig })

	if err := Backoff(context.Background(), resp429("7"), 0); err != nil || buf.String() != "rate limited,等待 7s 後重試…\n" {
		t.Errorf("%v %q", err, buf.String())
	}
	if err := Backoff(context.Background(), resp429("90"), 0); err == nil || err.Error() != "rate limited,伺服器要求等待 90 秒(超過上限 60 秒),請稍後再試" {
		t.Errorf("%v", err)
	}
}

func TestApplyPlaylistOpsErrorsEnglish(t *testing.T) {
	withLanguage(t, "en")
	for _, c := range []struct {
		ops  []PlaylistOp
		want string
	}{
		{[]PlaylistOp{{Kind: OpAdd, ProviderID: "x", Pos: 4}}, "op 0: add position 4 is out of range (length 2)"},
		{[]PlaylistOp{{Kind: OpRemove, Pos: 2}}, "op 0: remove position 2 is out of range (length 2)"},
		{[]PlaylistOp{{Kind: OpRemove, Pos: 0, ProviderID: "b"}}, "op 0: remove position 0 holds a, not b"},
		{[]PlaylistOp{{Kind: OpRename, Name: "n"}, {Kind: OpMove, From: 0, Pos: 3}}, "op 1: move 0 → 3 is out of range (length 2)"},
		{[]PlaylistOp{{Kind: OpRename}}, "op 0: rename has no Name"},
		{[]PlaylistOp{{Kind: "swap"}}, `op 0: unknown Kind "swap"`},
	} {
		if _, _, err := ApplyPlaylistOps([]string{"a", "b"}, c.ops); err == nil || err.Error() != c.want {
			t.Errorf("%+v:got %v, want %q", c.ops, err, c.want)
		}
	}
}

// 半截寫入:英文依已寫首數選單複數;改名與否是兩句完整的句子。zh-TW 的組法與拆開前一樣。
func TestPartialWriteErrorMessages(t *testing.T) {
	boom := errors.New("boom")
	withLanguage(t, "en")
	for _, c := range []struct {
		e    PartialWriteError
		want string
	}{
		{PartialWriteError{PlaylistID: "p1", Written: 100, Want: 251, Err: boom}, "playlist p1 was only partly written: the platform now has just the first 100 tracks (of 251); run push again to add the rest: boom"},
		{PartialWriteError{PlaylistID: "p1", Written: 1, Want: 3, Renamed: true, Err: boom}, "playlist p1 was only partly written: the platform now has just the first track (of 3); run push again to add the rest; the new name was already applied: boom"},
	} {
		if got := c.e.Error(); got != c.want {
			t.Errorf("got %q\nwant %q", got, c.want)
		}
	}
	e := &PartialWriteError{PlaylistID: "p1", Written: 100, Want: 251, Renamed: true, Err: boom}
	if !errors.Is(e, boom) {
		t.Error("Unwrap 要留著")
	}
	i18n.Set("zh-TW")
	if got := e.Error(); got != "清單 p1 寫到一半失敗:平台現在只有前 100 首(目標 251 首),重跑 push 補回其餘;名字已先改好:boom" {
		t.Errorf("zh-TW = %q", got)
	}
	e.Renamed = false
	if got := e.Error(); got != "清單 p1 寫到一半失敗:平台現在只有前 100 首(目標 251 首),重跑 push 補回其餘:boom" {
		t.Errorf("zh-TW = %q", got)
	}
}
