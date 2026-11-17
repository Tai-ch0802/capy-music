package ui

import (
	"image/color"
	"slices"
	"strings"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// Theme:互動式介面(capy 無參數)的顏色。**這個 struct 就是換色的接縫**——未來要開放使用者切換,
// 是在這裡多幾個具名 Theme 加一個 config key,不是把顏色散到各個 View 裡。表格與各命令的純文字輸出
// 刻意不上色(非 TTY 一律純文字是硬約束,上色只會多一種要剝的東西)。
type Theme struct {
	Accent color.Color // 主色:水豚、進度條、聚焦的輸入框
	Text   color.Color // 主要文字
	Muted  color.Color // 次要文字:提示列、單位、時間、錯誤
}

// GeekGreen:冷冽的 geek 綠(預設主題)。刻意不用螢光綠(#00FF00 那種在深色終端機上會糊成一片、看久了刺眼),
// 取偏青、飽和度收斂的中綠當主色,配冷灰的文字階層。
var GeekGreen = Theme{
	Accent: lipgloss.Color("#3FB27F"),
	Text:   lipgloss.Color("#C9D1D9"),
	Muted:  lipgloss.Color("#6E7681"),
}

// YuzuOnsen:柚子溫泉——暖金柑橘主色配溫潤米白與暖岩灰。
var YuzuOnsen = Theme{
	Accent: lipgloss.Color("#E5A93C"),
	Text:   lipgloss.Color("#EDE6D6"),
	Muted:  lipgloss.Color("#8C8275"),
}

// VinylAmber:黑膠琥珀——真空管暖銅橘主色配羊皮紙暖白與胡桃灰。
var VinylAmber = Theme{
	Accent: lipgloss.Color("#D97742"),
	Text:   lipgloss.Color("#E6DBD0"),
	Muted:  lipgloss.Color("#8A7968"),
}

// NightSakura:夜櫻——暮色櫻粉主色配冷月白與藍灰次要字。
var NightSakura = Theme{
	Accent: lipgloss.Color("#D48FB0"),
	Text:   lipgloss.Color("#D8DEE9"),
	Muted:  lipgloss.Color("#7B88A1"),
}

// DaylightInk:白晝墨綠——專為淺色背景終端機設計的高對比深墨綠配深炭字與中灰。
var DaylightInk = Theme{
	Accent: lipgloss.Color("#1A7F5A"),
	Text:   lipgloss.Color("#1F2328"),
	Muted:  lipgloss.Color("#57606A"),
}

const DefaultThemeName = "geek-green"

var themeOrder = []string{
	"geek-green",
	"yuzu-onsen",
	"vinyl-amber",
	"night-sakura",
	"daylight-ink",
}

var themesByName = map[string]Theme{
	"geek-green":   GeekGreen,
	"yuzu-onsen":   YuzuOnsen,
	"vinyl-amber":  VinylAmber,
	"night-sakura": NightSakura,
	"daylight-ink": DaylightInk,
}

// DefaultTheme:目前作用中的主題(預設 GeekGreen,可由 ApplyTheme 切換)。
var DefaultTheme = GeekGreen

// ThemeNames 回傳所有內建主題名稱(固定順序、回傳複本以防外部改動)。
func ThemeNames() []string {
	return slices.Clone(themeOrder)
}

// LookupTheme 依名稱(忽略前後空白與大小寫)查找內建主題。
func LookupTheme(name string) (Theme, bool) {
	t, ok := themesByName[strings.ToLower(strings.TrimSpace(name))]
	return t, ok
}

// ApplyTheme 將 DefaultTheme 切換為指定內建主題;名稱未知時不改動並回 false。
func ApplyTheme(name string) bool {
	t, ok := LookupTheme(name)
	if !ok {
		return false
	}
	DefaultTheme = t
	return true
}

func (t Theme) accentStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(t.Accent) }
func (t Theme) mutedStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(t.Muted) }
func (t Theme) textStyle() lipgloss.Style   { return lipgloss.NewStyle().Foreground(t.Text) }

// 這三個是 View 唯一該用的上色入口(TTY 才會進到這裡:互動式介面本來就只在 TTY 跑)。
// 欄位只留真的有人用的——宣告了卻沒接的欄位等於一個沒兌現的承諾,下一個人不會知道該用它(PR #41 review)。
func (t Theme) Accented(s string) string { return t.accentStyle().Render(s) }
func (t Theme) Mutedly(s string) string  { return t.mutedStyle().Render(s) }
func (t Theme) Strong(s string) string   { return t.textStyle().Bold(true).Render(s) }

// HuhStyles:huh 表單(挑選器、確認提示、輸入精靈)也套 DefaultTheme。huh 預設的 Charm 主題把標題染成靛藍
// (#7571F9,背景偵測不到時還會退成更深的 #5A56E0),深色終端機上很難讀 —— resolve --review 那兩行說明
// 就是這樣被使用者回報的。標題與說明都用 Text、選中的用 Accent、提示用 Muted,跟互動式介面同一套。
// 不看 isDark:capy 其餘介面本來就只有一套深色配色(theme.go 開頭),這裡一樣。huh.ThemeFunc(HuhStyles) 就是主題。
func HuhStyles(bool) *huh.Styles {
	t := DefaultTheme
	s := huh.ThemeBase(true)
	text := lipgloss.NewStyle().Foreground(t.Text)
	accent := lipgloss.NewStyle().Foreground(t.Accent)
	red := lipgloss.Color("#ED567A") // 驗證錯誤:主題裡沒有第四個顏色,錯誤就該跳出來

	s.Focused.Base = s.Focused.Base.BorderForeground(t.Muted)
	s.Focused.Card = s.Focused.Base
	s.Focused.Title = text.Bold(true)
	s.Focused.NoteTitle = text.Bold(true).MarginBottom(1)
	s.Focused.Description = text
	s.Focused.Directory = accent
	s.Focused.ErrorIndicator = s.Focused.ErrorIndicator.Foreground(red)
	s.Focused.ErrorMessage = s.Focused.ErrorMessage.Foreground(red)
	s.Focused.SelectSelector = s.Focused.SelectSelector.Foreground(t.Accent)
	s.Focused.NextIndicator = s.Focused.NextIndicator.Foreground(t.Accent)
	s.Focused.PrevIndicator = s.Focused.PrevIndicator.Foreground(t.Accent)
	s.Focused.MultiSelectSelector = s.Focused.MultiSelectSelector.Foreground(t.Accent)
	s.Focused.Option = text
	s.Focused.UnselectedOption = text
	s.Focused.SelectedOption = accent
	s.Focused.SelectedPrefix = accent.SetString("✓ ")
	s.Focused.UnselectedPrefix = lipgloss.NewStyle().Foreground(t.Muted).SetString("• ")
	btnFg := lipgloss.Color("#0D1117")
	blurFg := t.Text
	if t == DaylightInk {
		btnFg = lipgloss.Color("#F6F8FA")
		blurFg = lipgloss.Color("#F6F8FA")
	}
	s.Focused.FocusedButton = s.Focused.FocusedButton.Foreground(btnFg).Background(t.Accent)
	s.Focused.Next = s.Focused.FocusedButton
	s.Focused.BlurredButton = s.Focused.BlurredButton.Foreground(blurFg).Background(t.Muted)
	s.Focused.TextInput.Cursor = s.Focused.TextInput.Cursor.Foreground(t.Accent)
	s.Focused.TextInput.Prompt = s.Focused.TextInput.Prompt.Foreground(t.Accent)
	s.Focused.TextInput.Placeholder = s.Focused.TextInput.Placeholder.Foreground(t.Muted)
	s.Focused.TextInput.Text = text
	s.Help.ShortKey = text
	s.Help.ShortDesc = lipgloss.NewStyle().Foreground(t.Muted)
	s.Help.ShortSeparator = s.Help.ShortDesc
	s.Help.FullKey, s.Help.FullDesc, s.Help.FullSeparator = s.Help.ShortKey, s.Help.ShortDesc, s.Help.ShortSeparator

	s.Blurred = s.Focused
	s.Blurred.Base = s.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	s.Blurred.Card = s.Blurred.Base
	s.Blurred.NextIndicator = lipgloss.NewStyle()
	s.Blurred.PrevIndicator = lipgloss.NewStyle()
	s.Group.Title = s.Focused.Title
	s.Group.Description = s.Focused.Description
	return s
}
