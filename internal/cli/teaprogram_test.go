package cli

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// 在互動式介面 / now --watch 裡按 Ctrl-C 離開 = exit 130(同檢視窗格、同真的 SIGINT);按 q / Esc 離開 = 做完了,exit 0。
// raw mode 下 Ctrl-C 是按鍵不是訊號,Execute 的訊號那條路看不到它,所以 `capy now --watch; echo $?` 以前按 Ctrl-C 也是 0。
// 端到端:真的把位元組餵給 bubbletea 的輸入解析(0x03 = Ctrl-C),跑真的 model,走 runTUI / runWatch 共用的 runProgram。
func TestCtrlCKeyExits130(t *testing.T) {
	t.Setenv("CAPY_MOTION", "never") // 不演開場:這裡驗的是結束碼
	for _, tc := range []struct {
		name  string
		model func() tea.Model
		keys  string
		want  int
	}{
		{"now --watch:Ctrl-C", func() tea.Model {
			return newWatchModel(context.Background(), &watchFake{st: playingState()}, time.Hour)
		}, "\x03", 130},
		{"now --watch:q", func() tea.Model {
			return newWatchModel(context.Background(), &watchFake{st: playingState()}, time.Hour)
		}, "q", 0},
		{"now --watch:Esc", func() tea.Model {
			return newWatchModel(context.Background(), &watchFake{st: playingState()}, time.Hour)
		}, "\x1b", 0},
		{"互動式介面:Ctrl-C", func() tea.Model { return newTestTUI(t, &watchFake{st: playingState()}) }, "\x03", 130},
		{"互動式介面:輸入中 Ctrl-C(輸入中唯一的離開鍵)", func() tea.Model { return newTestTUI(t, &watchFake{st: playingState()}) }, "/\x03", 130},
		{"互動式介面:q", func() tea.Model { return newTestTUI(t, &watchFake{st: playingState()}) }, "q", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := runProgram(ctx, tc.model(), io.Discard, tea.WithInput(strings.NewReader(tc.keys)))
			if ctx.Err() != nil {
				t.Fatal("按鍵沒有讓程式結束")
			}
			if code, msg := ExitCode(err); code != tc.want || msg != "" {
				t.Errorf("(%d, %q),要 (%d, \"\")", code, msg, tc.want)
			}
		})
	}
}
