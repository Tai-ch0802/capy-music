package apple

import "errors"

// WebTokens:從已登入的網頁播放器分頁讀到的兩個 token(隱藏 --auto 專用)。
type WebTokens struct{ Developer, User string }

var ErrAutoUnsupported = errors.New("--auto 目前只支援 macOS(Safari / Google Chrome)")
