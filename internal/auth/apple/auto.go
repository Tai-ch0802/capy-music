package apple

import "github.com/Tai-ch0802/capy-music/internal/i18n"

// WebTokens:從已登入的網頁播放器分頁讀到的兩個 token(隱藏 --auto 專用)。
type WebTokens struct{ Developer, User string }

var ErrAutoUnsupported = i18n.Errorf("apple.err.auto_unsupported")
