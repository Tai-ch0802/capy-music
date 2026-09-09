package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Theme:互動式介面(capy 無參數)的顏色。**這個 struct 就是換色的接縫**——未來要開放使用者切換,
// 是在這裡多幾個具名 Theme 加一個 config key,不是把顏色散到各個 View 裡。表格與各命令的純文字輸出
// 刻意不上色(非 TTY 一律純文字是硬約束,上色只會多一種要剝的東西)。
type Theme struct {
	Accent    color.Color // 主色:水豚、進度條、聚焦的輸入框
	AccentDim color.Color // 主色的暗調:進度條的未播部分、次要邊框
	Text      color.Color // 主要文字
	Muted     color.Color // 次要文字:提示列、單位、時間
	Border    color.Color // 分隔線
}

// GeekGreen:冷冽的 geek 綠。刻意不用螢光綠(#00FF00 那種在深色終端機上會糊成一片、看久了刺眼),
// 取偏青、飽和度收斂的中綠當主色,配冷灰的文字階層。
var GeekGreen = Theme{
	Accent:    lipgloss.Color("#3FB27F"),
	AccentDim: lipgloss.Color("#2A6E52"),
	Text:      lipgloss.Color("#C9D1D9"),
	Muted:     lipgloss.Color("#6E7681"),
	Border:    lipgloss.Color("#30363D"),
}

// DefaultTheme:目前唯一的主題。
var DefaultTheme = GeekGreen

func (t Theme) accentStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(t.Accent) }
func (t Theme) mutedStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(t.Muted) }
func (t Theme) textStyle() lipgloss.Style   { return lipgloss.NewStyle().Foreground(t.Text) }

// 這三個是 View 唯一該用的上色入口(TTY 才會進到這裡:互動式介面本來就只在 TTY 跑)。
func (t Theme) Accented(s string) string { return t.accentStyle().Render(s) }
func (t Theme) Mutedly(s string) string  { return t.mutedStyle().Render(s) }
func (t Theme) Plain(s string) string    { return t.textStyle().Render(s) }
func (t Theme) Strong(s string) string   { return t.textStyle().Bold(true).Render(s) }
