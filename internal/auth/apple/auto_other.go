//go:build !darwin

package apple

// AutoWebTokens:隱藏 --auto 的機制是 AppleScript 驅動 Safari / Chrome,macOS 專用。
func AutoWebTokens() (WebTokens, error) { return WebTokens{}, ErrAutoUnsupported }
